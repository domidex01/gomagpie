package selector_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sync"
	"testing"

	"gomagpie/extract"
	"gomagpie/selector"
)

// --- fakes (copied per package; rule-of-three, no testutil) ---

type llmCall struct {
	purpose string
	url     string
}

type fakeExtractor struct {
	mu     sync.Mutex
	calls  []llmCall
	script map[string]map[string]any
	err    error
}

func (f *fakeExtractor) Name() string { return "fake" }

func (f *fakeExtractor) Extract(ctx context.Context, in extract.ExtractInput) (extract.ExtractResult, error) {
	if err := ctx.Err(); err != nil {
		return extract.ExtractResult{}, err
	}
	if f.err != nil {
		return extract.ExtractResult{}, f.err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	purpose := in.Purpose
	if purpose == "" {
		purpose = "extract"
	}
	f.calls = append(f.calls, llmCall{purpose: purpose, url: in.PromptExtra})
	rec := f.script[in.PromptExtra]
	if rec == nil {
		rec = f.script["default"]
	}
	raw, merr := json.Marshal(rec)
	if merr != nil {
		return extract.ExtractResult{}, merr
	}
	return extract.ExtractResult{Record: rec, Raw: raw, Provider: "fake", Model: "fake", Attempts: 1}, nil
}

func (f *fakeExtractor) count(purpose string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if c.purpose == purpose {
			n++
		}
	}
	return n
}

func (f *fakeExtractor) total() int { return f.count("synth") + f.count("extract") }

func cannedPropose(t *testing.T, canned map[string]string, calls *int) func(ctx context.Context, fields []string, trimmedHTML string) (map[string]string, error) {
	t.Helper()
	return func(ctx context.Context, fields []string, trimmedHTML string) (map[string]string, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		*calls++
		out := map[string]string{}
		for _, f := range fields {
			s, ok := canned[f]
			if !ok {
				return nil, fmt.Errorf("canned propose: no selector for field %q", f)
			}
			out[f] = s
		}
		return out, nil
	}
}

// --- helpers ---

const testSchemaYAML = `$schema: "https://json-schema.org/draft/2020-12/schema"
type: object
additionalProperties: false
required: [price, title]
properties:
  price:
    type: number
    x-gomagpie: { coerce: "eur_decimal" }
  title:
    type: string
    x-gomagpie: { trim: true }
  ean:
    type: string
`

const hintedSchemaYAML = `$schema: "https://json-schema.org/draft/2020-12/schema"
type: object
additionalProperties: false
required: [price, title]
properties:
  price:
    type: number
    x-gomagpie: { css_hint: "#price", coerce: "eur_decimal" }
  title:
    type: string
    x-gomagpie: { trim: true }
  ean:
    type: string
`

var testTruth = map[string]any{"price": 12.99, "title": "Widget", "ean": "4001234567890"}

func loadSelectorFixture(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile("../testdata/selector/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func mustParseTestSchema(t *testing.T) *extract.Schema {
	t.Helper()
	sch, err := extract.ParseSchema([]byte(testSchemaYAML))
	if err != nil {
		t.Fatal(err)
	}
	return sch
}

func threeSamples(t *testing.T, name string) []selector.SynthSample {
	t.Helper()
	html := loadSelectorFixture(t, name)
	out := make([]selector.SynthSample, 0, 3)
	for i := 0; i < 3; i++ {
		out = append(out, selector.SynthSample{URL: "http://ex.com/a", HTML: html, Truth: testTruth})
	}
	return out
}

func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = w
	fn()
	if err := w.Close(); err != nil {
		t.Errorf("close pipe: %v", err)
	}
	os.Stderr = old
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read pipe: %v", err)
	}
	return string(out)
}

// --- synthesis ---

func TestSynth_HeuristicWinsWithZeroPropose(t *testing.T) {
	sch := mustParseTestSchema(t)
	var proposeCalls int
	doc, err := selector.Synthesize(context.Background(), threeSamples(t, "product-A.html"), sch,
		cannedPropose(t, map[string]string{}, &proposeCalls), nil)
	if err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	if proposeCalls != 0 {
		t.Errorf("propose calls = %d, want 0 (heuristic covers everything)", proposeCalls)
	}
	if doc.Fields["price"].Expr != "#price" {
		t.Errorf("price selector = %q, want #price", doc.Fields["price"].Expr)
	}
	if doc.Fields["title"].Expr != "#productTitle" {
		t.Errorf("title selector = %q, want #productTitle", doc.Fields["title"].Expr)
	}
	if _, ok := doc.Fields["ean"]; !ok {
		t.Error("ean missing from doc")
	}
}

func TestSynth_CSSHintWinsWithZeroPropose(t *testing.T) {
	sch, err := extract.ParseSchema([]byte(hintedSchemaYAML))
	if err != nil {
		t.Fatal(err)
	}
	var proposeCalls int
	doc, err := selector.Synthesize(context.Background(), threeSamples(t, "product-A.html"), sch,
		cannedPropose(t, map[string]string{}, &proposeCalls), nil)
	if err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	if proposeCalls != 0 {
		t.Errorf("propose calls = %d, want 0 (valid css_hint costs zero work)", proposeCalls)
	}
	if doc.Fields["price"].Expr != "#price" {
		t.Errorf("price selector = %q, want #price from css_hint", doc.Fields["price"].Expr)
	}
}

func TestSynth_WrongHintFallsThrough(t *testing.T) {
	sch, err := extract.ParseSchema([]byte(`$schema: "https://json-schema.org/draft/2020-12/schema"
type: object
additionalProperties: false
required: [price]
properties:
  price: {type: number, x-gomagpie: {css_hint: "#nope", coerce: "eur_decimal"}}
`))
	if err != nil {
		t.Fatal(err)
	}
	var proposeCalls int
	var doc selector.SelectorDoc
	stderr := captureStderr(t, func() {
		var err error
		doc, err = selector.Synthesize(context.Background(), threeSamples(t, "product-A.html"), sch,
			cannedPropose(t, map[string]string{}, &proposeCalls), nil)
		if err != nil {
			t.Errorf("Synthesize: %v", err)
		}
	})
	if doc.Fields["price"].Expr != "#price" {
		t.Errorf("price selector = %q, want #price via heuristic fallthrough", doc.Fields["price"].Expr)
	}
	if proposeCalls != 0 {
		t.Errorf("propose calls = %d, want 0", proposeCalls)
	}
	if !contains(stderr, "falling through") {
		t.Errorf("stderr missing fallthrough note; got %q", stderr)
	}
}

func TestSynth_ProposePathForHeuristicMiss(t *testing.T) {
	sch, err := extract.ParseSchema([]byte(`$schema: "https://json-schema.org/draft/2020-12/schema"
type: object
additionalProperties: false
required: [price]
properties:
  price: {type: number, x-gomagpie: {coerce: "eur_decimal"}}
`))
	if err != nil {
		t.Fatal(err)
	}
	// Same value, class-order difference: no single emitted candidate
	// validates on both (positional hooks disagree too).
	s1 := `<div><span class="a price">12.99</span></div>`
	s2 := `<div><span>other</span><span class="price b">12.99</span></div>`
	samples := []selector.SynthSample{
		{URL: "http://ex.com/1", HTML: s1, Truth: map[string]any{"price": 12.99}},
		{URL: "http://ex.com/2", HTML: s2, Truth: map[string]any{"price": 12.99}},
	}
	var proposeCalls int
	var gotFields []string
	propose := func(ctx context.Context, fields []string, _ string) (map[string]string, error) {
		proposeCalls++
		gotFields = fields
		return map[string]string{"price": `[class*="price"]`}, nil
	}
	doc, err := selector.Synthesize(context.Background(), samples, sch, propose, nil)
	if err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	if proposeCalls != 1 {
		t.Fatalf("propose calls = %d, want exactly 1", proposeCalls)
	}
	if len(gotFields) != 1 || gotFields[0] != "price" {
		t.Errorf("propose fields = %v, want [price]", gotFields)
	}
	if doc.Fields["price"].Expr != `[class*="price"]` {
		t.Errorf("price selector = %q, want proposed selector", doc.Fields["price"].Expr)
	}
}

func TestSynth_OnlyFilter(t *testing.T) {
	sch := mustParseTestSchema(t)
	var calls int
	doc, err := selector.Synthesize(context.Background(), threeSamples(t, "product-A.html"), sch,
		cannedPropose(t, map[string]string{}, &calls), []string{"price"})
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Fields) != 1 || doc.Fields["price"].Expr == "" {
		t.Errorf("only=[price] doc fields = %v, want exactly price", doc.Fields)
	}
}

func TestSynth_SampleEdges(t *testing.T) {
	sch := mustParseTestSchema(t)
	var calls int
	if _, err := selector.Synthesize(context.Background(), nil, sch, cannedPropose(t, map[string]string{}, &calls), nil); err == nil {
		t.Error("0 samples = nil error, want loud error")
	} else if !contains(err.Error(), "no samples") {
		t.Errorf("0-sample error = %q, want 'no samples'", err.Error())
	}
	stderr := captureStderr(t, func() {
		if _, err := selector.Synthesize(context.Background(), threeSamples(t, "product-A.html")[:1], sch, cannedPropose(t, map[string]string{}, &calls), nil); err != nil {
			t.Errorf("1 sample: %v", err)
		}
	})
	if !contains(stderr, "1 sample") {
		t.Errorf("1-sample stderr missing warn; got %q", stderr)
	}
}

func TestSynth_XPathHintWarns(t *testing.T) {
	sch, err := extract.ParseSchema([]byte(`$schema: "https://json-schema.org/draft/2020-12/schema"
type: object
additionalProperties: false
required: [price]
properties:
  price: {type: number, x-gomagpie: {xpath_hint: "//span[@id='price']", coerce: "eur_decimal"}}
`))
	if err != nil {
		t.Fatal(err)
	}
	var calls int
	stderr := captureStderr(t, func() {
		if _, err := selector.Synthesize(context.Background(), threeSamples(t, "product-A.html"), sch, cannedPropose(t, map[string]string{}, &calls), nil); err != nil {
			t.Errorf("Synthesize: %v", err)
		}
	})
	if !contains(stderr, "xpath_hint") {
		t.Errorf("stderr missing xpath warning; got %q", stderr)
	}
}

// --- validation ---

func TestValidate_TwoOfThreeCachesWithWarning(t *testing.T) {
	sch := mustParseTestSchema(t)
	// B renames the price node: #price agrees 2/3 → caches WITH warning.
	samples := []selector.SynthSample{
		{URL: "http://ex.com/a", HTML: loadSelectorFixture(t, "product-A.html"), Truth: testTruth},
		{URL: "http://ex.com/b", HTML: loadSelectorFixture(t, "product-B.html"), Truth: testTruth},
		{URL: "http://ex.com/c", HTML: loadSelectorFixture(t, "product-C.html"), Truth: testTruth},
	}
	var calls int
	var doc selector.SelectorDoc
	stderr := captureStderr(t, func() {
		var err error
		doc, err = selector.Synthesize(context.Background(), samples, sch, cannedPropose(t, map[string]string{}, &calls), nil)
		if err != nil {
			t.Errorf("Synthesize: %v", err)
		}
	})
	sel, ok := doc.Fields["price"]
	if !ok {
		t.Fatal("price missing from doc, want 2/3-accepted")
	}
	if sel.Expr != "#price" {
		t.Errorf("price selector = %q, want #price", sel.Expr)
	}
	if !contains(stderr, `"price"`) {
		t.Errorf("stderr missing named-field warning; got %q", stderr)
	}
	if _, ok := doc.Fields["title"]; !ok {
		t.Error("title (3/3) missing from doc")
	}
}

func TestValidate_OneOfThreeNonCacheable(t *testing.T) {
	sch, err := extract.ParseSchema([]byte(`$schema: "https://json-schema.org/draft/2020-12/schema"
type: object
additionalProperties: false
required: [price, sku]
properties:
  price: {type: number, x-gomagpie: {coerce: "eur_decimal"}}
  sku: {type: string}
`))
	if err != nil {
		t.Fatal(err)
	}
	truth := map[string]any{"price": 12.99, "sku": "W-1"}
	// sku node exists only in A.
	a := loadSelectorFixture(t, "product-A.html") + `<span id="sku">W-1</span>`
	samples := []selector.SynthSample{
		{URL: "http://ex.com/a", HTML: a, Truth: truth},
		{URL: "http://ex.com/b", HTML: loadSelectorFixture(t, "product-B.html"), Truth: truth},
		{URL: "http://ex.com/c", HTML: loadSelectorFixture(t, "product-C.html"), Truth: truth},
	}
	var calls int
	doc, err := selector.Synthesize(context.Background(), samples, sch,
		cannedPropose(t, map[string]string{"sku": "#does-not-exist"}, &calls), nil)
	if err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	if _, ok := doc.Fields["sku"]; ok {
		t.Errorf("sku cached with 1/3 agreement, want non-cacheable")
	}
	if _, ok := doc.Fields["price"]; !ok {
		t.Error("price (2/3) missing from doc")
	}
}

// --- heal ---

func TestHeal_BrokenFieldOnly(t *testing.T) {
	sch := mustParseTestSchema(t)
	pageA := loadSelectorFixture(t, "product-A.html")
	pageB := loadSelectorFixture(t, "product-B.html")

	fx := &fakeExtractor{script: map[string]map[string]any{
		"default": testTruth,
	}}
	// Cold start: retain 3 validated A-samples, synthesize, cache.
	h := selector.NewHealer(50, 0.30, 3)
	for i := 0; i < 3; i++ {
		res, err := fx.Extract(context.Background(), extract.ExtractInput{
			Markdown: "Widget 12.99", Schema: sch, Purpose: "synth", PromptExtra: "default",
		})
		if err != nil {
			t.Fatal(err)
		}
		h.Retain(selector.SynthSample{URL: "http://ex.com/a", HTML: pageA, Truth: res.Record})
	}
	var proposeCalls int
	doc, err := selector.Synthesize(context.Background(), h.Samples(), sch,
		cannedPropose(t, map[string]string{"price": "#price"}, &proposeCalls), nil)
	if err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	applier := selector.NewApplier(doc, sch)

	// Steady state on A: 0 LLM calls.
	before := fx.total()
	if _, nulls := applier.Apply(pageA, nil); len(nulls) != 0 {
		t.Fatalf("Apply(A) nulls = %v, want none", nulls)
	}
	if got := fx.total() - before; got != 0 {
		t.Fatalf("Apply(A) made %d LLM calls, want 0", got)
	}

	// Swap to B: price nulls, title/ean keep serving. Feed 20 B-pages past
	// the 0.30 trigger (min evidence = window/5 = 10).
	var healed []string
	for i := 0; i < 20; i++ {
		_, nulls := applier.Apply(pageB, nil)
		for _, f := range nulls {
			if trig := h.Observe(f, true); len(trig) > 0 {
				healed = append(healed, trig...)
			}
		}
		for _, f := range []string{"title", "ean"} {
			h.Observe(f, false)
		}
	}
	if len(healed) == 0 {
		t.Fatal("heal never triggered after 20 broken-price pages")
	}
	if healed[0] != "price" {
		t.Fatalf("first healed field = %q, want price-only (title/ean uninterrupted)", healed[0])
	}
	// No synth-purpose LLM before the crossing: all calls so far are the 3 cold-start synth calls.
	if got := fx.count("synth"); got != 3 {
		t.Errorf("synth calls = %d, want exactly 3 (none before threshold crossing)", got)
	}
}

func TestHealer_ObserveBoundary(t *testing.T) {
	h := selector.NewHealer(50, 0.30, 3)
	for i := 0; i < 35; i++ {
		if trig := h.Observe("price", false); len(trig) != 0 {
			t.Fatalf("trigger on ok page %d", i)
		}
	}
	for i := 0; i < 15; i++ {
		if trig := h.Observe("price", true); len(trig) != 0 {
			t.Fatalf("trigger at 15/50 (exactly 0.30), want none")
		}
	}
	if trig := h.Observe("price", true); len(trig) != 1 || trig[0] != "price" {
		t.Fatalf("16th null in window did not trigger: %v", trig)
	}
}

func TestHeal_FullResynthRule(t *testing.T) {
	if selector.FullResynth(3, 1) {
		t.Error("FullResynth(3,1) = true, want false (price-only heal)")
	}
	if !selector.FullResynth(3, 2) {
		t.Error("FullResynth(3,2) = false, want true (≥50% broken)")
	}
	if !selector.FullResynth(2, 1) {
		t.Error("FullResynth(2,1) = false, want true")
	}
	if selector.FullResynth(4, 1) {
		t.Error("FullResynth(4,1) = true, want false")
	}
}

func TestHeal_JsonldBypass(t *testing.T) {
	sch, err := extract.ParseSchema([]byte(`$schema: "https://json-schema.org/draft/2020-12/schema"
type: object
additionalProperties: false
required: [price, ean]
properties:
  price: {type: number, x-gomagpie: {coerce: "eur_decimal"}}
  ean: {type: string, x-gomagpie: {jsonld_path: "$.gtin13"}}
`))
	if err != nil {
		t.Fatal(err)
	}
	samples := threeSamples(t, "product-A.html")
	for i := range samples {
		samples[i].Sidecar = json.RawMessage(`{"gtin13":"4001234567890"}`)
		samples[i].Truth = map[string]any{"price": 12.99, "ean": "4001234567890"}
	}
	var calls int
	doc, err := selector.Synthesize(context.Background(), samples, sch, cannedPropose(t, map[string]string{}, &calls), nil)
	if err != nil {
		t.Fatal(err)
	}
	if doc.Fields["ean"].Type != "jsonld" {
		t.Errorf("ean type = %q, want jsonld (no selector needed)", doc.Fields["ean"].Type)
	}
	applier := selector.NewApplier(doc, sch)
	rec, nulls := applier.Apply(loadSelectorFixture(t, "product-A.html"), json.RawMessage(`{"gtin13":"4001234567890"}`))
	if len(nulls) != 0 {
		t.Errorf("Apply nulls = %v, want none", nulls)
	}
	if rec["ean"] != "4001234567890" {
		t.Errorf("ean = %v, want sidecar value", rec["ean"])
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
