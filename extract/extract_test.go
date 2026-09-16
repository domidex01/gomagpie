package extract_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"gomagpie/extract"
)

type fakeProvider struct {
	t      *testing.T
	mu     sync.Mutex
	script []string
	calls  int
	bodies []string
	status int
}

func newFakeProvider(t *testing.T, script ...string) (*httptest.Server, *fakeProvider) {
	t.Helper()
	fp := &fakeProvider{t: t, script: script, status: 200}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, rerr := io.ReadAll(r.Body)
		if rerr != nil {
			http.Error(w, rerr.Error(), http.StatusBadRequest)
			return
		}
		fp.mu.Lock()
		defer fp.mu.Unlock()
		fp.calls++
		fp.bodies = append(fp.bodies, string(body))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(fp.status)
		idx := min(fp.calls-1, len(fp.script)-1)
		if _, werr := io.WriteString(w, fp.script[idx]); werr != nil {
			fp.t.Errorf("write: %v", werr)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, fp
}

func (f *fakeProvider) callCount() int { f.mu.Lock(); defer f.mu.Unlock(); return f.calls }

func openAIEnvelope(raw string) string {
	b, merr := json.Marshal(raw)
	if merr != nil {
		panic(merr) // marshaling a string cannot fail
	}
	return `{"choices":[{"message":{"content":` + string(b) + `},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5}}`
}

func anthropicEnvelope(raw string) string {
	b, merr := json.Marshal(raw)
	if merr != nil {
		panic(merr) // marshaling a string cannot fail
	}
	return `{"content":[{"type":"text","text":` + string(b) + `}],"stop_reason":"end_turn","usage":{"input_tokens":10,"output_tokens":5}}`
}

func mustLoadSchema(t *testing.T, path string) *extract.Schema {
	t.Helper()
	sch, err := extract.LoadSchema(path)
	if err != nil {
		t.Fatalf("LoadSchema(%s): %v", path, err)
	}
	return sch
}

func TestRepairLoopInvalidThenValid(t *testing.T) {
	srv, fp := newFakeProvider(t,
		openAIEnvelope(`{"name":"Widget","price":"12.99"}`),
		openAIEnvelope(`{"name":"Widget","price":12.99}`),
	)
	ex := extract.NewOpenAI(srv.URL, "test-key", "gpt-4o-mini", mustLoadSchema(t, "../testdata/extract/price.yaml"))
	got, err := ex.Extract(t.Context(), extract.ExtractInput{Markdown: "# Widget\n\nPrice: 12.99"})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if fp.callCount() != 2 {
		t.Fatalf("calls = %d, want exactly 2", fp.callCount())
	}
	if !strings.Contains(fp.bodies[1], "at '/price'") {
		t.Errorf("repair prompt missing validator text; body[1] = %q", fp.bodies[1])
	}
	if got.Record["price"] != 12.99 {
		t.Errorf("price = %v, want 12.99", got.Record["price"])
	}
}

func TestRepairLoopExhausted(t *testing.T) {
	srv, fp := newFakeProvider(t,
		openAIEnvelope(`{"name":"Widget","price":"x"}`),
		openAIEnvelope(`{"name":"Widget","price":"y"}`),
		openAIEnvelope(`{"name":"Widget","price":"z"}`),
	)
	ex := extract.NewOpenAI(srv.URL, "test-key", "gpt-4o-mini", mustLoadSchema(t, "../testdata/extract/price.yaml"))
	_, err := ex.Extract(t.Context(), extract.ExtractInput{Markdown: "# Widget"})
	if err == nil {
		t.Fatal("expected error after 3 invalid attempts, got nil")
	}
	if !strings.Contains(err.Error(), "at '/price'") {
		t.Errorf("error missing validator text: %v", err)
	}
	if fp.callCount() != 3 {
		t.Errorf("calls = %d, want 3", fp.callCount())
	}
}

func TestAnthropicRepair(t *testing.T) {
	srv, fp := newFakeProvider(t,
		anthropicEnvelope(`{"name":"Widget","price":"12.99"}`),
		anthropicEnvelope(`{"name":"Widget","price":12.99}`),
	)
	ex := extract.NewAnthropic(srv.URL, "test-key", "claude-sonnet-5", mustLoadSchema(t, "../testdata/extract/price.yaml"))
	_, err := ex.Extract(t.Context(), extract.ExtractInput{Markdown: "# Widget"})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if fp.callCount() != 2 {
		t.Fatalf("calls = %d, want 2", fp.callCount())
	}
	var req map[string]any
	if err := json.Unmarshal([]byte(fp.bodies[0]), &req); err != nil {
		t.Fatalf("request not JSON: %v", err)
	}
	if _, ok := req["output_config"]; !ok {
		t.Error("anthropic request missing output_config")
	}
}

func TestTruncation(t *testing.T) {
	srv, _ := newFakeProvider(t, `{"content":[{"type":"text","text":"{}"}],"stop_reason":"max_tokens","usage":{"input_tokens":1,"output_tokens":1}}`)
	ex := extract.NewAnthropic(srv.URL, "k", "claude-sonnet-5", mustLoadSchema(t, "../testdata/extract/price.yaml"))
	_, err := ex.Extract(t.Context(), extract.ExtractInput{Markdown: "x"})
	if err == nil || !strings.Contains(err.Error(), "truncat") {
		t.Fatalf("expected truncation error, got %v", err)
	}
}

func TestSchemaErrorText(t *testing.T) {
	sch := mustLoadSchema(t, "../testdata/extract/price.yaml")
	raw, err := os.ReadFile("../testdata/extract/invalid-doc.json")
	if err != nil {
		t.Fatal(err)
	}
	err = sch.Validate(raw)
	if err == nil {
		t.Fatal("expected validation error")
	}
	if !strings.Contains(err.Error(), "at '/price'") {
		t.Errorf("error text = %q, want at '/price'", err.Error())
	}
}

func TestUnknownCoerce(t *testing.T) {
	_, err := extract.ParseSchema([]byte(`{"type":"object","properties":{"a":{"type":"string","x-gomagpie":{"coerce":"frobnicate"}}}}`))
	if err == nil || !strings.Contains(err.Error(), "frobnicate") {
		t.Fatalf("expected unknown-coerce error, got %v", err)
	}
}

func TestCoerceEURDecimal(t *testing.T) {
	cases := map[string]struct {
		in   string
		want float64
	}{
		"euro suffix + comma": {"1 234,56 €", 1234.56},
		"plain":               {"12.99", 12.99},
		"symbol only":         {"€7,5", 7.5},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := extract.Coerce("eur_decimal", c.in)
			if err != nil {
				t.Fatalf("Coerce: %v", err)
			}
			if got != c.want {
				t.Errorf("got %v, want %v", got, c.want)
			}
		})
	}
}

func TestCoerceVectors(t *testing.T) {
	for _, tc := range []struct {
		kind string
		in   string
		want any
	}{{"int", "42", 42}, {"trim", "  x  ", "x"}, {"bool", "ja", true}} {
		v, err := extract.Coerce(tc.kind, tc.in)
		if err != nil {
			t.Fatalf("Coerce(%s): %v", tc.kind, err)
		}
		if v != tc.want {
			t.Errorf("Coerce(%s) = %v, want %v", tc.kind, v, tc.want)
		}
	}
	if v, err := extract.Coerce("iso_date", "12.03.2024"); err != nil || v != "2024-03-12" {
		t.Errorf("iso_date: %v %v", v, err)
	}
	if v, err := extract.ApplyRegex(`[0-9]+[.,][0-9]{2}`, "price 12,99 EUR"); err != nil || v != "12,99" {
		t.Errorf("regex: %v %v", v, err)
	}
}

func TestJSONLDWalk(t *testing.T) {
	side := json.RawMessage(`{"offers":{"price":"49.99"}}`)
	v, ok := extract.JSONLDWalk(side, "$.offers.price")
	if !ok || v != "49.99" {
		t.Errorf("walk: %v %v", v, ok)
	}
}

func TestCostTable(t *testing.T) {
	if c := extract.EstimateCost("gpt-4o-mini", 1_000_000, 1_000_000); c != 0.75 {
		t.Errorf("cost = %v, want 0.75", c)
	}
	if c := extract.EstimateCost("no-such-model", 1000, 1000); c != 0 {
		t.Errorf("unknown model cost = %v, want 0", c)
	}
	if c := extract.ProjectedCost("gpt-4o-mini", strings.Repeat("x", 4000)); c <= 0 {
		t.Errorf("projected = %v, want > 0", c)
	}
}
