package store

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

const ddl = `
PRAGMA journal_mode=WAL;
PRAGMA foreign_keys=ON;

CREATE TABLE IF NOT EXISTS selector_cache (
    schema_hash    TEXT NOT NULL,
    domain         TEXT NOT NULL,
    fields_json    TEXT NOT NULL,
    samples_used   INTEGER NOT NULL,
    engine_version INTEGER NOT NULL DEFAULT 1,
    synthesized_at TEXT NOT NULL,
    updated_at     TEXT NOT NULL,
    PRIMARY KEY (domain, schema_hash)
);

CREATE TABLE IF NOT EXISTS crawl_state (
    run_id        TEXT NOT NULL,
    url           TEXT NOT NULL,
    url_hash      TEXT NOT NULL,
    status        TEXT NOT NULL CHECK(status IN ('pending','inflight','done','error')),
    depth         INTEGER NOT NULL DEFAULT 0,
    error_msg     TEXT,
    discovered_at TEXT NOT NULL,
    updated_at    TEXT NOT NULL,
    PRIMARY KEY (run_id, url_hash)
);
CREATE INDEX IF NOT EXISTS idx_crawl_status ON crawl_state(run_id, status);

CREATE TABLE IF NOT EXISTS dedup (
    run_id     TEXT NOT NULL,
    url_hash   TEXT NOT NULL,
    first_seen TEXT NOT NULL,
    PRIMARY KEY (run_id, url_hash)
);

CREATE TABLE IF NOT EXISTS run_history (
    run_id            TEXT PRIMARY KEY,
    command           TEXT NOT NULL,
    started_at        TEXT NOT NULL,
    finished_at       TEXT,
    pages_ok          INTEGER NOT NULL DEFAULT 0,
    pages_err         INTEGER NOT NULL DEFAULT 0,
    prompt_tokens     INTEGER NOT NULL DEFAULT 0,
    completion_tokens INTEGER NOT NULL DEFAULT 0,
    usd_estimate      REAL NOT NULL DEFAULT 0,
    status            TEXT NOT NULL DEFAULT 'running'
);

CREATE TABLE IF NOT EXISTS llm_calls (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    run_id            TEXT NOT NULL,
    provider          TEXT NOT NULL,
    model             TEXT NOT NULL,
    prompt_tokens     INTEGER NOT NULL,
    completion_tokens INTEGER NOT NULL,
    usd_estimate      REAL NOT NULL,
    purpose           TEXT NOT NULL,
    ts                TEXT NOT NULL,
    FOREIGN KEY(run_id) REFERENCES run_history(run_id)
);
`

// DB is a single-writer SQLite handle.
type DB struct {
	db   *sql.DB
	path string
}

// Open creates parent dirs, opens with WAL/foreign_keys pragmas in the DSN,
// and applies the full DDL idempotently.
func Open(path string) (*DB, error) {
	if path == "" {
		return nil, fmt.Errorf("store: empty db path")
	}
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("store: mkdir %s: %w", dir, err)
		}
	}
	dsn := "file:" + path + "?_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("store: open %s: %w", path, err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(ddl); err != nil {
		if cerr := db.Close(); cerr != nil {
			return nil, fmt.Errorf("store: migrate: %v (also close: %v)", err, cerr)
		}
		return nil, fmt.Errorf("store: migrate: %w", err)
	}
	return &DB{db: db, path: path}, nil
}

func (d *DB) Close() error { return d.db.Close() }

// Path returns the backing file path.
func (d *DB) Path() string { return d.path }

// BeginRun inserts a running run_history row.
func (d *DB) BeginRun(runID, command string) error {
	_, err := d.db.Exec(`INSERT INTO run_history(run_id, command, started_at, status) VALUES(?,?,?, 'running')`,
		runID, command, time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return fmt.Errorf("store: begin run: %w", err)
	}
	return nil
}

// FinishRun marks a run finished with page counters.
func (d *DB) FinishRun(runID string, pagesOK, pagesErr int, status string) error {
	_, err := d.db.Exec(`UPDATE run_history SET finished_at=?, pages_ok=?, pages_err=?, status=? WHERE run_id=?`,
		time.Now().UTC().Format(time.RFC3339), pagesOK, pagesErr, status, runID)
	if err != nil {
		return fmt.Errorf("store: finish run: %w", err)
	}
	return nil
}

// LLMCall records one provider call and rolls tokens/cost into run_history.
type LLMCall struct {
	Provider         string
	Model            string
	PromptTokens     int
	CompletionTokens int
	USDEstimate      float64
	Purpose          string // synth|extract|repair
}

// LogLLMCall inserts into llm_calls and accumulates run_history totals.
func (d *DB) LogLLMCall(runID string, c LLMCall) error {
	ts := time.Now().UTC().Format(time.RFC3339)
	if _, err := d.db.Exec(`INSERT INTO llm_calls(run_id, provider, model, prompt_tokens, completion_tokens, usd_estimate, purpose, ts) VALUES(?,?,?,?,?,?,?,?)`,
		runID, c.Provider, c.Model, c.PromptTokens, c.CompletionTokens, c.USDEstimate, c.Purpose, ts); err != nil {
		return fmt.Errorf("store: log llm call: %w", err)
	}
	_, err := d.db.Exec(`UPDATE run_history SET prompt_tokens=prompt_tokens+?, completion_tokens=completion_tokens+?, usd_estimate=usd_estimate+? WHERE run_id=?`,
		c.PromptTokens, c.CompletionTokens, c.USDEstimate, runID)
	if err != nil {
		return fmt.Errorf("store: roll up run: %w", err)
	}
	return nil
}

// RunCost returns the accumulated usd_estimate for a run.
func (d *DB) RunCost(runID string) (float64, error) {
	var v float64
	err := d.db.QueryRow(`SELECT usd_estimate FROM run_history WHERE run_id=?`, runID).Scan(&v)
	if err != nil {
		return 0, fmt.Errorf("store: run cost: %w", err)
	}
	return v, nil
}

// LLMCallCount counts llm_calls rows (optionally filtered by run).
func (d *DB) LLMCallCount(runID string) (int, error) {
	var n int
	var err error
	if runID == "" {
		err = d.db.QueryRow(`SELECT COUNT(*) FROM llm_calls`).Scan(&n)
	} else {
		err = d.db.QueryRow(`SELECT COUNT(*) FROM llm_calls WHERE run_id=?`, runID).Scan(&n)
	}
	if err != nil {
		return 0, fmt.Errorf("store: count llm calls: %w", err)
	}
	return n, nil
}

// TableCount counts rows in a table (test helper).
func (d *DB) TableCount(table string) (int, error) {
	var n int
	if err := d.db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// Pragma reads an integer PRAGMA (test helper).
func (d *DB) Pragma(name string) (int, error) {
	var v int
	if err := d.db.QueryRow(`PRAGMA ` + name).Scan(&v); err != nil {
		return 0, err
	}
	return v, nil
}
