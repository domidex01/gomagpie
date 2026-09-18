package selector

import (
	"bytes"
	"encoding/json"

	"magpie/extract"

	"github.com/PuerkitoBio/goquery"
)

// Applier serves steady-state pages with 0 LLM calls: one goquery parse per
// page, every field selector applied to the same document.
type Applier struct {
	doc SelectorDoc
	sch *extract.Schema
}

// NewApplier builds an Applier over a cached doc.
func NewApplier(doc SelectorDoc, sch *extract.Schema) *Applier {
	return &Applier{doc: doc, sch: sch}
}

// Apply extracts the record; returns the record plus names of null fields.
func (a *Applier) Apply(html string, sidecar json.RawMessage) (map[string]any, []string) {
	rec := map[string]any{}
	var nulls []string
	gdoc, err := goquery.NewDocumentFromReader(bytes.NewReader([]byte(html)))
	if err != nil {
		nulls = append(nulls, SchemaFields(a.sch)...)
		return rec, nulls
	}
	for _, f := range SchemaFields(a.sch) {
		sel, ok := a.doc.Fields[f]
		if !ok {
			nulls = append(nulls, f) // non-cacheable: caller runs per-page LLM
			continue
		}
		v, found := ExtractFieldValue(gdoc, sidecar, f, sel, a.sch.Hints)
		if !found || v == nil {
			nulls = append(nulls, f)
			continue
		}
		rec[f] = v
	}
	return rec, nulls
}

// Doc returns the underlying selector doc.
func (a *Applier) Doc() SelectorDoc { return a.doc }
