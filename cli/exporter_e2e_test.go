package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestHelperExporterProcess(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}
	out := os.Getenv("MAGPIE_TEST_OUT")
	if os.Getenv("MAGPIE_TEST_FAIL") == "1" {
		fmt.Fprintln(os.Stderr, "boom: downstream exploded")
		os.Exit(3)
	}
	data, _ := io.ReadAll(os.Stdin)    //nolint:errcheck // test helper child; short read unactionable
	_ = os.WriteFile(out, data, 0o600) //nolint:errcheck // test helper child; write failure surfaces in parent assertions
	os.Exit(0)
}

func helperExporterCmd(t *testing.T, out string, fail bool) string {
	t.Helper()
	t.Setenv("MAGPIE_TEST_OUT", out)
	t.Setenv("GO_WANT_HELPER_PROCESS", "1")
	if fail {
		t.Setenv("MAGPIE_TEST_FAIL", "1")
	} else {
		t.Setenv("MAGPIE_TEST_FAIL", "")
	}
	// No quoting (strings.Fields contract): the test binary path is space-free here.
	return os.Args[0] + " -test.run=TestHelperExporterProcess"
}

func decodeLines(t *testing.T, path string) []map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var r map[string]any
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			t.Fatalf("decode line: %v", err)
		}
		out = append(out, r)
	}
	return out
}

func TestCrawl_ExporterCmdTee(t *testing.T) {
	testEnv(t, "cache.db")
	fakeLLM(t, `{"name":"Widget","price":12.99}`)
	seed := writeFileSite(t, 2)
	out := filepath.Join(t.TempDir(), "r.jsonl")
	expFile := filepath.Join(t.TempDir(), "export.jsonl")
	if err := runCrawl(t.Context(), seed, crawlCLIOptions{
		Schema: priceSchema(t), Format: "jsonl", Out: out,
		MaxPages: 10, MaxDepth: 3, Concurrency: 4, SameHost: true, Rate: 1000,
		Provider: "openai", Model: "gpt-4o-mini",
		ExporterCmd: helperExporterCmd(t, expFile, false),
	}); err != nil {
		t.Fatalf("crawl: %v", err)
	}
	// Same encoder shape, two sinks: decode both line-by-line and compare
	// records (raw byte-compare would couple the test to map iteration
	// order across two independent json.Encoders).
	got, want := decodeLines(t, expFile), decodeLines(t, out)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("export records != .jsonl records:\n got %v\nwant %v", got, want)
	}
	if len(got) != 3 {
		t.Errorf("records = %d, want 3 (index + 2 pages)", len(got))
	}
}

func TestCrawl_ExporterCmdFailure(t *testing.T) {
	testEnv(t, "cache.db")
	fakeLLM(t, `{"name":"Widget","price":12.99}`)
	seed := writeFileSite(t, 2)
	out := filepath.Join(t.TempDir(), "r.jsonl")
	expFile := filepath.Join(t.TempDir(), "export.jsonl")
	var runErr error
	_, stderr := captureOutput(t, func() {
		runErr = runCrawl(t.Context(), seed, crawlCLIOptions{
			Schema: priceSchema(t), Format: "jsonl", Out: out,
			MaxPages: 10, MaxDepth: 3, Concurrency: 4, SameHost: true, Rate: 1000,
			Provider: "openai", Model: "gpt-4o-mini",
			ExporterCmd: helperExporterCmd(t, expFile, true),
		})
	})
	if codeOf(runErr) != 4 {
		t.Errorf("exit = %d, want 4 (exporter failure path)", codeOf(runErr))
	}
	if !strings.Contains(stderr, "WARNING") {
		t.Errorf("stderr %q has no WARNING", stderr)
	}
	if !strings.Contains(stderr, filepath.Base(os.Args[0])) {
		t.Errorf("stderr %q does not name the exporter program", stderr)
	}
	// Primary sink unaffected: .jsonl still complete.
	if got := decodeLines(t, out); len(got) != 3 {
		t.Errorf(".jsonl records = %d, want 3 (tee never blocks primary)", len(got))
	}
}
