package store_test

import (
	"path/filepath"
	"testing"

	"gomagpie/store"
)

func openTempDB(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close: %v", err)
		}
	})
	return db
}

func TestTablesExist(t *testing.T) {
	db := openTempDB(t)
	for _, tbl := range []string{"selector_cache", "crawl_state", "dedup", "run_history", "llm_calls"} {
		n, err := db.TableCount(tbl)
		if err != nil {
			t.Errorf("table %s: %v", tbl, err)
		}
		if n != 0 {
			t.Errorf("table %s fresh count = %d, want 0", tbl, n)
		}
	}
}

func TestForeignKeysOn(t *testing.T) {
	db := openTempDB(t)
	v, err := db.Pragma("foreign_keys")
	if err != nil {
		t.Fatal(err)
	}
	if v != 1 {
		t.Errorf("PRAGMA foreign_keys = %d, want 1", v)
	}
}

func TestRunRoundTrip(t *testing.T) {
	db := openTempDB(t)
	if err := db.BeginRun("r1", "scrape"); err != nil {
		t.Fatal(err)
	}
	if err := db.LogLLMCall("r1", store.LLMCall{Provider: "openai", Model: "gpt-4o-mini", PromptTokens: 10, CompletionTokens: 5, USDEstimate: 0.01, Purpose: "extract"}); err != nil {
		t.Fatal(err)
	}
	if err := db.FinishRun("r1", 1, 0, "finished"); err != nil {
		t.Fatal(err)
	}
	n, err := db.LLMCallCount("r1")
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("llm_calls = %d, want 1", n)
	}
	c, err := db.RunCost("r1")
	if err != nil {
		t.Fatal(err)
	}
	if c != 0.01 {
		t.Errorf("cost = %v, want 0.01", c)
	}
	// Idempotent re-open.
	db2, err := store.Open(db.Path())
	if err != nil {
		t.Fatalf("re-open: %v", err)
	}
	if err := db2.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}
