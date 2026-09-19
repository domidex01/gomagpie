package exec_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	pluginExec "github.com/motherlodelab/magpie/plugin/exec"
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

func helperExporter(t *testing.T, out string, fail bool) []string {
	t.Helper()
	t.Setenv("MAGPIE_TEST_OUT", out)
	if fail {
		t.Setenv("MAGPIE_TEST_FAIL", "1")
	} else {
		t.Setenv("MAGPIE_TEST_FAIL", "")
	}
	// The child needs GO_WANT_HELPER_PROCESS=1 in its env (inherited).
	t.Setenv("GO_WANT_HELPER_PROCESS", "1")
	return []string{os.Args[0], "-test.run=TestHelperExporterProcess"}
}

func tempPath(t *testing.T, name string) string {
	t.Helper()
	return filepath.Join(t.TempDir(), name)
}

func feed(records []map[string]any) <-chan map[string]any {
	ch := make(chan map[string]any, len(records))
	for _, r := range records {
		ch <- r
	}
	close(ch)
	return ch
}

func decodeJSONL(t *testing.T, path string) []map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
		var r map[string]any
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			t.Fatalf("decode line %q: %v", line, err)
		}
		out = append(out, r)
	}
	return out
}

func TestExport_JSONLRoundTrip(t *testing.T) {
	out := tempPath(t, "export.jsonl")
	records := []map[string]any{
		{"url": "https://example.com/a", "title": "Widget"},
		{"url": "https://example.com/b", "title": "Ünïcodé ✓", "nested": map[string]any{"x": 1.5}},
		{"url": "https://example.com/c", "n": float64(3)},
	}
	exp := &pluginExec.Exporter{Cmd: helperExporter(t, out, false)}
	if err := exp.Export(context.Background(), feed(records)); err != nil {
		t.Fatalf("Export: %v", err)
	}
	if got := decodeJSONL(t, out); !reflect.DeepEqual(got, records) {
		t.Errorf("round-trip mismatch:\n got %v\nwant %v", got, records)
	}
}

func TestExport_FailureAttribution(t *testing.T) {
	out := tempPath(t, "export.jsonl")
	exp := &pluginExec.Exporter{Cmd: helperExporter(t, out, true)}
	err := exp.Export(context.Background(), feed([]map[string]any{{"a": "b"}}))
	if err == nil {
		t.Fatal("Export(failing child) = nil, want loud error")
	}
	if !strings.Contains(err.Error(), "boom: downstream exploded") {
		t.Errorf("error %q does not quote stderr", err)
	}
	if !strings.Contains(err.Error(), os.Args[0]) {
		t.Errorf("error %q does not name the program", err)
	}
}

func TestExport_SpawnFailure(t *testing.T) {
	exp := &pluginExec.Exporter{Cmd: []string{"magpie-definitely-missing-binary-xyz"}}
	err := exp.Export(context.Background(), feed([]map[string]any{{"a": "b"}}))
	if err == nil {
		t.Fatal("Export(missing binary) = nil, want error at Start")
	}
	if !strings.Contains(err.Error(), "magpie-definitely-missing-binary-xyz") {
		t.Errorf("error %q does not name the binary", err)
	}
}

func TestExport_CanceledCtx(t *testing.T) {
	out := tempPath(t, "export.jsonl")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	exp := &pluginExec.Exporter{Cmd: helperExporter(t, out, false)}
	if err := exp.Export(ctx, feed([]map[string]any{{"a": "b"}})); err == nil {
		t.Error("Export(canceled ctx) = nil, want error")
	}
}
