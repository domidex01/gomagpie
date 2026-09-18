package cli

// --- Phase H: action/lang flags, watch command, crawl --status. ---

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"magpie/store"
)

// deadOrigin returns a URL whose port was closed after allocation — any
// dial is an instant failure, making zero-dial proofs unambiguous.
func deadOrigin(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	u := srv.URL
	srv.Close()
	return u
}

// TestScrapeActionFlags: the DSL never reaches the wire on bad input —
// every failure below is pre-I/O (the closed-port origin proves it).
func TestScrapeActionFlags(t *testing.T) {
	t.Run("bogus verb exits 2 pre-I/O", func(t *testing.T) {
		testEnv(t, "actions.db")
		resetGlobals()
		root := rootCmd()
		var err error
		captureOutput(t, func() {
			root.SetArgs([]string{"scrape", deadOrigin(t), "--render", "static", "--action", "frobnicate #x"})
			err = root.Execute()
		})
		if codeOf(err) != 2 {
			t.Fatalf("exit = %d (err=%v), want 2", codeOf(err), err)
		}
		if ce, ok := err.(*cmdError); ok && !strings.Contains(ce.msg, "frobnicate") {
			t.Errorf("error %q missing the verb", ce.msg)
		}
	})

	t.Run("wait cap via --actions file", func(t *testing.T) {
		testEnv(t, "actions2.db")
		file := filepath.Join(t.TempDir(), "actions.txt")
		if err := os.WriteFile(file, []byte("# comment\nclick #ok\n\nwait 99999\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		resetGlobals()
		root := rootCmd()
		var err error
		captureOutput(t, func() {
			root.SetArgs([]string{"scrape", deadOrigin(t), "--render", "static", "--actions", file})
			err = root.Execute()
		})
		if codeOf(err) != 2 {
			t.Fatalf("exit = %d (err=%v), want 2", codeOf(err), err)
		}
		if ce, ok := err.(*cmdError); ok && !strings.Contains(ce.msg, "30000") {
			t.Errorf("error %q missing the 30000 cap (file lines must reach the validator)", ce.msg)
		}
	})
}

// TestScrapeLangFlag — the CLI twin of the control-char gate: the payload
// must die pre-I/O (exit 2, zero dials against the closed origin).
func TestScrapeLangFlag(t *testing.T) {
	testEnv(t, "lang.db")
	resetGlobals()
	root := rootCmd()
	var err error
	captureOutput(t, func() {
		root.SetArgs([]string{"scrape", deadOrigin(t), "--render", "static", "--lang", "en\r\nX-Evil: 1"})
		err = root.Execute()
	})
	if codeOf(err) != 2 {
		t.Fatalf("exit = %d (err=%v), want 2", codeOf(err), err)
	}
}

// watchOrigin serves a quality-gate-clean page whose content flips when
// the atomic flag is set — the reprice.
func watchOrigin(t *testing.T, flip *atomic.Bool) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		price := "10"
		if flip.Load() {
			price = "20"
		}
		w.Header().Set("Content-Type", "text/html")
		page := `<html><head><title>Watch Target</title></head><body><h1>Watch Target</h1>` +
			`<p>The price is ` + price + ` dollars today.</p>` +
			`<p>` + strings.Repeat("Honest filler prose keeps the gate satisfied. ", 8) + `</p></body></html>`
		_, _ = w.Write([]byte(page)) //nolint:errcheck // httptest local
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// TestWatchCmd: flag validation, then a full --once e2e (baseline
// silence → flip → diff + webhook delivery) against a temp store.
func TestWatchCmd(t *testing.T) {
	t.Run("missing every exits 2", func(t *testing.T) {
		testEnv(t, "watch1.db")
		resetGlobals()
		root := rootCmd()
		var err error
		captureOutput(t, func() {
			root.SetArgs([]string{"watch", "https://example.com", "--once"})
			err = root.Execute()
		})
		if codeOf(err) != 2 {
			t.Fatalf("exit = %d (err=%v), want 2", codeOf(err), err)
		}
	})
	t.Run("5s below the 30s floor exits 2", func(t *testing.T) {
		testEnv(t, "watch2.db")
		resetGlobals()
		root := rootCmd()
		var err error
		captureOutput(t, func() {
			root.SetArgs([]string{"watch", "https://example.com", "--every", "5s"})
			err = root.Execute()
		})
		if codeOf(err) != 2 {
			t.Fatalf("exit = %d (err=%v), want 2", codeOf(err), err)
		}
	})
	t.Run("once e2e: silence then diff + webhook", func(t *testing.T) {
		dbPath := testEnv(t, "watch3.db")
		var flip atomic.Bool
		url := watchOrigin(t, &flip)
		sink := newCLISink(t)

		// Run 1: baseline — silence on stdout (diff-command precedent).
		resetGlobals()
		root := rootCmd()
		var out1 string
		var err error
		out1, _ = captureOutput(t, func() {
			root.SetArgs([]string{"watch", url, "--every", "30m", "--once", "--render", "static"})
			err = root.Execute()
		})
		if err != nil {
			t.Fatalf("baseline watch: %v", err)
		}
		if strings.Contains(out1, "changed=true") {
			t.Errorf("baseline stdout = %q, want silence", out1)
		}

		// Run 2 (same store): the flip — diff + webhook delivery.
		flip.Store(true)
		resetGlobals()
		root2 := rootCmd()
		var out2 string
		out2, _ = captureOutput(t, func() {
			root2.SetArgs([]string{"watch", url, "--every", "30m", "--once", "--render", "static", "--webhook", sink.srv.URL})
			err = root2.Execute()
		})
		if err != nil {
			t.Fatalf("change watch: %v", err)
		}
		if !strings.Contains(out2, "changed=true") {
			t.Errorf("change stdout = %q, want changed=true + diff", out2)
		}
		if n := sink.postCount(); n != 1 {
			t.Errorf("webhook POSTs = %d, want 1", n)
		}
		if dbPath == "" {
			t.Fatal("no db path")
		}
	})
	t.Run("help lists the flags", func(t *testing.T) {
		resetGlobals()
		root := rootCmd()
		out, _ := captureOutput(t, func() {
			root.SetArgs([]string{"watch", "--help"})
			if herr := root.Execute(); herr != nil {
				t.Errorf("help: %v", herr)
			}
		})
		for _, want := range []string{"--every", "--once", "--webhook"} {
			if !strings.Contains(out, want) {
				t.Errorf("help missing %q", want)
			}
		}
	})
}

// cliSink is the CLI-side webhook sink (watch_test's sibling lives in
// scrape's test package; per-package helpers stay at point of use).
type cliSink struct {
	srv   *httptest.Server
	mu    sync.Mutex
	posts int
}

func newCLISink(t *testing.T) *cliSink {
	t.Helper()
	s := &cliSink{}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body) //nolint:errcheck // test sink
		s.mu.Lock()
		s.posts++
		s.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *cliSink) postCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.posts
}

// TestCrawlStatus: the CLI twin of the MCP run_id poll.
func TestCrawlStatus(t *testing.T) {
	t.Run("unknown run_id exits 4 naming the id", func(t *testing.T) {
		testEnv(t, "status1.db")
		resetGlobals()
		root := rootCmd()
		var err error
		_, stderr := captureOutput(t, func() {
			root.SetArgs([]string{"crawl", "--status", "nope"})
			err = root.Execute()
		})
		if codeOf(err) != 4 {
			t.Fatalf("exit = %d (err=%v), want 4", codeOf(err), err)
		}
		if !strings.Contains(stderr, "nope") {
			t.Errorf("stderr %q missing the run id", stderr)
		}
	})

	t.Run("seeded run prints all four counts", func(t *testing.T) {
		dbPath := testEnv(t, "status2.db")
		db, err := store.Open(dbPath)
		if err != nil {
			t.Fatal(err)
		}
		runID := "status-run-1"
		if err := db.BeginRun(runID, "crawl"); err != nil {
			t.Fatal(err)
		}
		urls := make([]string, 7)
		for i := range urls {
			urls[i] = "https://seed.example/p" + string(rune('a'+i))
		}
		if _, err := db.Enqueue(runID, urls, 0); err != nil {
			t.Fatal(err)
		}
		claimed, err := db.Claim(runID, 4)
		if err != nil || len(claimed) != 4 {
			t.Fatalf("claim = %d, %v, want 4", len(claimed), err)
		}
		if err := db.MarkDone(runID, claimed[0].URLHash); err != nil {
			t.Fatal(err)
		}
		if err := db.MarkDone(runID, claimed[1].URLHash); err != nil {
			t.Fatal(err)
		}
		if err := db.MarkError(runID, claimed[2].URLHash, "boom"); err != nil {
			t.Fatal(err)
		}
		// claimed[3] stays inflight; 3 remain pending.
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}

		resetGlobals()
		root := rootCmd()
		var out string
		var xerr error
		out, _ = captureOutput(t, func() {
			root.SetArgs([]string{"crawl", "--status", runID})
			xerr = root.Execute()
		})
		if xerr != nil {
			t.Fatalf("status: %v", xerr)
		}
		for _, want := range []string{`"pending": 3`, `"inflight": 1`, `"done": 2`, `"errors": 1`, runID, "crawl"} {
			if !strings.Contains(out, want) {
				t.Errorf("stdout missing %q:\n%s", want, out)
			}
		}
	})

	t.Run("status with a URL arg is rejected", func(t *testing.T) {
		testEnv(t, "status3.db")
		resetGlobals()
		root := rootCmd()
		var err error
		captureOutput(t, func() {
			root.SetArgs([]string{"crawl", "--status", "x", "https://example.com"})
			err = root.Execute()
		})
		if codeOf(err) != 1 {
			t.Fatalf("exit = %d (err=%v), want 1 (usage error)", codeOf(err), err)
		}
	})
}
