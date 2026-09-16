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

func TestSelectorRoundTrip(t *testing.T) {
	db := openTempDB(t)
	doc := `{"fields":{"price":{"type":"css","expr":"#price"}}}`
	if err := db.PutSelectors("ex.com", "h1", doc, 3); err != nil {
		t.Fatal(err)
	}
	got, ok, err := db.GetSelectors("ex.com", "h1")
	if err != nil || !ok {
		t.Fatalf("Get = %q,%v,%v", got, ok, err)
	}
	if got != doc {
		t.Errorf("Get = %q, want %q", got, doc)
	}
	// Update path.
	if err := db.PutSelectors("ex.com", "h1", doc, 5); err != nil {
		t.Fatal(err)
	}
	// Scoped delete.
	if n, err := db.DeleteSelectors("ex.com", "h1"); err != nil || n != 1 {
		t.Fatalf("Delete = %d,%v want 1", n, err)
	}
	if _, ok, gerr := db.GetSelectors("ex.com", "h1"); gerr != nil || ok {
		t.Errorf("Get after delete = %v,%v, want miss", ok, gerr)
	}
	// Domain-wide clear leaves other domains alone.
	if err := db.PutSelectors("ex.com", "h1", doc, 3); err != nil {
		t.Fatal(err)
	}
	if err := db.PutSelectors("ex.com", "h2", doc, 3); err != nil {
		t.Fatal(err)
	}
	if err := db.PutSelectors("other.com", "h1", doc, 3); err != nil {
		t.Fatal(err)
	}
	if n, err := db.DeleteSelectors("ex.com", ""); err != nil || n != 2 {
		t.Fatalf("Delete domain = %d,%v want 2", n, err)
	}
	if _, ok, gerr := db.GetSelectors("other.com", "h1"); gerr != nil || !ok {
		t.Errorf("other domain Get = %v,%v, want untouched hit", ok, gerr)
	}
}

func TestFrontierVector(t *testing.T) {
	db := openTempDB(t)
	urls := []string{"http://ex.com/1", "http://ex.com/2", "http://ex.com/3", "http://ex.com/4", "http://ex.com/5"}
	if n, err := db.Enqueue("r1", urls, 0); err != nil || n != 5 {
		t.Fatalf("Enqueue = %d,%v want 5", n, err)
	}
	if n, err := db.Enqueue("r1", urls, 0); err != nil || n != 0 {
		t.Fatalf("re-Enqueue = %d,%v want 0", n, err)
	}
	claimed, err := db.Claim("r1", 2)
	if err != nil || len(claimed) != 2 {
		t.Fatalf("Claim = %d,%v want 2", len(claimed), err)
	}
	for _, c := range claimed {
		if c.URL == "" || c.URLHash == "" {
			t.Errorf("claimed row missing fields: %+v", c)
		}
	}
	p, i, d, e, err := db.CrawlStats("r1")
	if err != nil || p != 3 || i != 2 || d != 0 || e != 0 {
		t.Fatalf("stats = %d/%d/%d/%d,%v want 3/2/0/0", p, i, d, e, err)
	}
	if n, err := db.ResetInflight("r1"); err != nil || n != 2 {
		t.Fatalf("ResetInflight = %d,%v want 2", n, err)
	}
	if p, i, d, e, serr := db.CrawlStats("r1"); serr != nil || p != 5 || i != 0 || d != 0 || e != 0 {
		t.Fatalf("stats after reset = %d/%d/%d/%d,%v want 5/0/0/0", p, i, d, e, serr)
	}
	claimed, err = db.Claim("r1", 2)
	if err != nil || len(claimed) != 2 {
		t.Fatalf("Claim2 = %d,%v", len(claimed), err)
	}
	if err := db.MarkDone("r1", claimed[0].URLHash); err != nil {
		t.Fatal(err)
	}
	if err := db.MarkError("r1", claimed[1].URLHash, "boom"); err != nil {
		t.Fatal(err)
	}
	if p, i, d, e, serr := db.CrawlStats("r1"); serr != nil || p != 3 || i != 0 || d != 1 || e != 1 {
		t.Fatalf("stats final = %d/%d/%d/%d,%v want 3/0/1/1", p, i, d, e, serr)
	}
}

func TestResumeRun(t *testing.T) {
	db := openTempDB(t)
	if err := db.ResumeRun("nope"); err == nil {
		t.Fatal("ResumeRun unknown id = nil, want error")
	}
	if err := db.BeginRun("r1", "crawl"); err != nil {
		t.Fatal(err)
	}
	if err := db.FinishRun("r1", 1, 0, "finished"); err != nil {
		t.Fatal(err)
	}
	if err := db.ResumeRun("r1"); err != nil {
		t.Fatalf("ResumeRun finished = %v", err)
	}
}

func TestSeenTwice(t *testing.T) {
	db := openTempDB(t)
	seen, err := db.Seen("r1", "abc")
	if err != nil || seen {
		t.Fatalf("Seen1 = %v,%v want false", seen, err)
	}
	seen, err = db.Seen("r1", "abc")
	if err != nil || !seen {
		t.Fatalf("Seen2 = %v,%v want true", seen, err)
	}
	hashes, err := db.LoadHashes("r1")
	if err != nil || len(hashes) != 1 || hashes[0] != "abc" {
		t.Fatalf("LoadHashes = %v,%v", hashes, err)
	}
}

func TestReopenPersists(t *testing.T) {
	db := openTempDB(t)
	path := db.Path()
	if err := db.PutSelectors("ex.com", "h1", `{"a":1}`, 3); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Enqueue("r1", []string{"http://ex.com/1"}, 0); err != nil {
		t.Fatal(err)
	}
	db2, err := store.Open(path)
	if err != nil {
		t.Fatalf("re-open: %v", err)
	}
	defer func() {
		if cerr := db2.Close(); cerr != nil {
			t.Errorf("close: %v", cerr)
		}
	}()
	if _, ok, gerr := db2.GetSelectors("ex.com", "h1"); gerr != nil || !ok {
		t.Errorf("selectors lost across reopen: %v", gerr)
	}
	if p, _, _, _, serr := db2.CrawlStats("r1"); serr != nil || p != 1 {
		t.Errorf("pending after reopen = %d,%v want 1", p, serr)
	}
}
