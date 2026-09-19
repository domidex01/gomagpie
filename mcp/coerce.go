package mcp

// Widen-first MCP coercion.
//
// go-sdk v1.8.0 validates tool arguments against the resolved InputSchema
// (applySchema) BEFORE unmarshaling into In, so UnmarshalJSON-only
// coercion never fires: a string "3" dies in validation and the custom
// unmarshaler never runs. The design is therefore schema-widening first
// (widenToolInput rewrites single types to unions admitting strings,
// touching Type only — required[] and descriptions stay byte-identical)
// and flex-unmarshal second (FlexInt/FlexBool/StringList/FlexMap coerce
// post-validation with errors naming the param).

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// FlexInt coerces a JSON number or a numeric string ("3" → 3). Floats
// never silently truncate: 3.5 errors.
type FlexInt int

// FlexBool coerces a JSON boolean or a bool string ("true" → true).
type FlexBool bool

// StringList coerces an array, a JSON-array string, or a single string
// (single trimmed value — never comma-split: URLs legally contain commas
// in query strings). "" decodes to empty.
type StringList []string

// FlexMap coerces an object or a JSON-object string.
type FlexMap map[string]any

func pname(p string) string {
	if p == "" {
		return "value"
	}
	return strconv.Quote(p)
}

func isNull(raw json.RawMessage) bool {
	s := strings.TrimSpace(string(raw))
	return s == "" || s == "null"
}

func (v *FlexInt) decode(param string, raw json.RawMessage) error {
	if isNull(raw) {
		return nil
	}
	var i int
	if err := json.Unmarshal(raw, &i); err == nil {
		*v = FlexInt(i)
		return nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return fmt.Errorf("mcp: %s: want integer, got %s", pname(param), strings.TrimSpace(string(raw)))
	}
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return fmt.Errorf("mcp: %s: %q is not an integer", pname(param), s)
	}
	*v = FlexInt(n)
	return nil
}

// UnmarshalJSON keeps direct decodes working; struct-level decoders
// (below) route through decode for param-named errors.
func (v *FlexInt) UnmarshalJSON(data []byte) error { return v.decode("", data) }

func (v *FlexBool) decode(param string, raw json.RawMessage) error {
	if isNull(raw) {
		return nil
	}
	var b bool
	if err := json.Unmarshal(raw, &b); err == nil {
		*v = FlexBool(b)
		return nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return fmt.Errorf("mcp: %s: want boolean, got %s", pname(param), strings.TrimSpace(string(raw)))
	}
	parsed, err := strconv.ParseBool(strings.TrimSpace(s))
	if err != nil {
		return fmt.Errorf("mcp: %s: %q is not a boolean", pname(param), s)
	}
	*v = FlexBool(parsed)
	return nil
}

// UnmarshalJSON keeps direct decodes working; struct-level decoders
// (below) route through decode for param-named errors.
func (v *FlexBool) UnmarshalJSON(data []byte) error { return v.decode("", data) }

func (v *StringList) decode(param string, raw json.RawMessage) error {
	if isNull(raw) {
		return nil
	}
	var l []string
	if err := json.Unmarshal(raw, &l); err == nil {
		*v = l
		return nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return fmt.Errorf("mcp: %s: want array of strings, got %s", pname(param), strings.TrimSpace(string(raw)))
	}
	s = strings.TrimSpace(s)
	if s == "" {
		*v = nil
		return nil
	}
	if strings.HasPrefix(s, "[") {
		if err := json.Unmarshal([]byte(s), &l); err != nil {
			return fmt.Errorf("mcp: %s: string is not a JSON array of strings: %v", pname(param), err)
		}
		*v = l
		return nil
	}
	*v = StringList{s}
	return nil
}

// UnmarshalJSON keeps direct decodes working; struct-level decoders
// (below) route through decode for param-named errors.
func (v *StringList) UnmarshalJSON(data []byte) error { return v.decode("", data) }

func (v *FlexMap) decode(param string, raw json.RawMessage) error {
	if isNull(raw) {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err == nil {
		*v = m
		return nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return fmt.Errorf("mcp: %s: want object, got %s", pname(param), strings.TrimSpace(string(raw)))
	}
	if err := json.Unmarshal([]byte(s), &m); err != nil || m == nil {
		return fmt.Errorf("mcp: %s: string is not a JSON object: %v", pname(param), err)
	}
	*v = m
	return nil
}

// UnmarshalJSON keeps direct decodes working; struct-level decoders
// (below) route through decode for param-named errors.
func (v *FlexMap) UnmarshalJSON(data []byte) error { return v.decode("", data) }

// decodeFlexField decodes one raw field into a flex value with
// param-named errors; absent/null leaves the zero value (unset).
func decodeFlexField(param string, raw json.RawMessage, v any) error {
	if len(raw) == 0 || isNull(raw) {
		return nil
	}
	switch t := v.(type) {
	case *FlexInt:
		return t.decode(param, raw)
	case *FlexBool:
		return t.decode(param, raw)
	case **FlexBool:
		var b FlexBool
		if err := b.decode(param, raw); err != nil {
			return err
		}
		*t = &b
		return nil
	case *StringList:
		return t.decode(param, raw)
	case *FlexMap:
		return t.decode(param, raw)
	default:
		return fmt.Errorf("mcp: %s: unsupported flex target %T", pname(param), v)
	}
}

// widenToolInput rewrites an inferred input schema so stringy clients
// survive validation: single scalar/array/object types become unions with
// "string" (pointer-inferred unions like ["null","boolean"] gain "string").
// required[] and descriptions are never touched.
func widenToolInput(t *sdk.Tool) {
	sch, ok := t.InputSchema.(*jsonschema.Schema)
	if !ok || sch == nil {
		return
	}
	for _, prop := range sch.Properties {
		if prop == nil {
			continue
		}
		widenable := func(typ string) bool {
			switch typ {
			case "integer", "boolean", "array", "object":
				return true
			}
			return false
		}
		if widenable(prop.Type) {
			prop.Types = []string{prop.Type, "string"}
			prop.Type = ""
			continue
		}
		needs := false
		has := false
		for _, typ := range prop.Types {
			if widenable(typ) {
				needs = true
			}
			if typ == "string" {
				has = true
			}
		}
		if needs && !has {
			prop.Types = append(prop.Types, "string")
		}
	}
}

// widenedTool infers the input schema for In (keeping every jsonschema:
// description exactly as AddTool would), widens it for stringy clients,
// and returns the tool ready to register.
func widenedTool[In any](name, desc string) *sdk.Tool {
	sch, err := jsonschema.ForType(reflect.TypeFor[In](), &jsonschema.ForOptions{})
	if err != nil {
		panic(fmt.Sprintf("mcp: tool %q schema: %v", name, err))
	}
	t := &sdk.Tool{Name: name, Description: desc, InputSchema: sch}
	widenToolInput(t)
	return t
}

// Struct-level decoders: every flex-bearing In routes its flex fields
// through decodeFlexField so coercion failures name the param. Plain
// fields decode normally; absent/null flex fields stay zero (unset).

// UnmarshalJSON decodes ScrapeIn with param-named flex errors.
func (in *ScrapeIn) UnmarshalJSON(data []byte) error {
	var sh struct {
		URL             string          `json:"url"`
		Schema          json.RawMessage `json:"schema"`
		Render          string          `json:"render"`
		UseCache        json.RawMessage `json:"use_cache"`
		PageFormat      string          `json:"page_format"`
		Include         json.RawMessage `json:"include"`
		Exclude         json.RawMessage `json:"exclude"`
		OnlyMainContent json.RawMessage `json:"only_main_content"`
		Profile         string          `json:"profile"`
		Cookies         string          `json:"cookies"`
		Actions         json.RawMessage `json:"actions"`
		Lang            string          `json:"lang"`
		CaptureXHR      json.RawMessage `json:"capture_xhr"`
		CDP             string          `json:"cdp_url"`
	}
	if err := json.Unmarshal(data, &sh); err != nil {
		return err
	}
	in.URL, in.Render, in.PageFormat, in.Profile, in.Cookies, in.Lang, in.CDP =
		sh.URL, sh.Render, sh.PageFormat, sh.Profile, sh.Cookies, sh.Lang, sh.CDP
	if err := decodeFlexField("schema", sh.Schema, &in.Schema); err != nil {
		return err
	}
	if err := decodeFlexField("use_cache", sh.UseCache, &in.UseCache); err != nil {
		return err
	}
	if err := decodeFlexField("include", sh.Include, &in.Include); err != nil {
		return err
	}
	if err := decodeFlexField("exclude", sh.Exclude, &in.Exclude); err != nil {
		return err
	}
	if err := decodeFlexField("actions", sh.Actions, &in.Actions); err != nil {
		return err
	}
	if err := decodeFlexField("capture_xhr", sh.CaptureXHR, &in.CaptureXHR); err != nil {
		return err
	}
	return decodeFlexField("only_main_content", sh.OnlyMainContent, &in.OnlyMainContent)
}

// UnmarshalJSON decodes CrawlIn with param-named flex errors.
func (in *CrawlIn) UnmarshalJSON(data []byte) error {
	var sh struct {
		URL          string          `json:"url"`
		MaxPages     json.RawMessage `json:"max_pages"`
		MaxDepth     json.RawMessage `json:"max_depth"`
		SameHost     json.RawMessage `json:"same_host"`
		Schema       json.RawMessage `json:"schema"`
		RunID        string          `json:"run_id"`
		NoSitemap    json.RawMessage `json:"no_sitemap"`
		Subdomains   json.RawMessage `json:"allow_subdomains"`
		SitemapOnly  json.RawMessage `json:"sitemap_only"`
		AutoThrottle json.RawMessage `json:"auto_throttle"`
	}
	if err := json.Unmarshal(data, &sh); err != nil {
		return err
	}
	in.URL, in.RunID = sh.URL, sh.RunID
	if err := decodeFlexField("max_pages", sh.MaxPages, &in.MaxPages); err != nil {
		return err
	}
	if err := decodeFlexField("max_depth", sh.MaxDepth, &in.MaxDepth); err != nil {
		return err
	}
	if err := decodeFlexField("same_host", sh.SameHost, &in.SameHost); err != nil {
		return err
	}
	if err := decodeFlexField("schema", sh.Schema, &in.Schema); err != nil {
		return err
	}
	if err := decodeFlexField("no_sitemap", sh.NoSitemap, &in.NoSitemap); err != nil {
		return err
	}
	if err := decodeFlexField("allow_subdomains", sh.Subdomains, &in.AllowSubdomains); err != nil {
		return err
	}
	if err := decodeFlexField("sitemap_only", sh.SitemapOnly, &in.SitemapOnly); err != nil {
		return err
	}
	return decodeFlexField("auto_throttle", sh.AutoThrottle, &in.AutoThrottle)
}

// UnmarshalJSON decodes ExtractIn with param-named flex errors.
func (in *ExtractIn) UnmarshalJSON(data []byte) error {
	var sh struct {
		Content     string          `json:"content"`
		ContentType string          `json:"content_type"`
		Schema      json.RawMessage `json:"schema"`
		Prompt      string          `json:"prompt"`
	}
	if err := json.Unmarshal(data, &sh); err != nil {
		return err
	}
	in.Content, in.ContentType, in.Prompt = sh.Content, sh.ContentType, sh.Prompt
	return decodeFlexField("schema", sh.Schema, &in.Schema)
}

// UnmarshalJSON decodes BatchIn with param-named flex errors.
func (in *BatchIn) UnmarshalJSON(data []byte) error {
	var sh struct {
		URLs            json.RawMessage `json:"urls"`
		Concurrency     json.RawMessage `json:"concurrency"`
		Render          string          `json:"render"`
		Profile         string          `json:"profile"`
		Cookies         string          `json:"cookies"`
		Include         json.RawMessage `json:"include"`
		Exclude         json.RawMessage `json:"exclude"`
		OnlyMainContent json.RawMessage `json:"only_main_content"`
	}
	if err := json.Unmarshal(data, &sh); err != nil {
		return err
	}
	in.Render, in.Profile, in.Cookies = sh.Render, sh.Profile, sh.Cookies
	if err := decodeFlexField("urls", sh.URLs, &in.URLs); err != nil {
		return err
	}
	if err := decodeFlexField("concurrency", sh.Concurrency, &in.Concurrency); err != nil {
		return err
	}
	if err := decodeFlexField("include", sh.Include, &in.Include); err != nil {
		return err
	}
	if err := decodeFlexField("exclude", sh.Exclude, &in.Exclude); err != nil {
		return err
	}
	return decodeFlexField("only_main_content", sh.OnlyMainContent, &in.OnlyMainContent)
}

// UnmarshalJSON decodes SummarizeIn with param-named flex errors.
func (in *SummarizeIn) UnmarshalJSON(data []byte) error {
	var sh struct {
		URL          string          `json:"url"`
		MaxSentences json.RawMessage `json:"max_sentences"`
		Provider     string          `json:"provider"`
		Model        string          `json:"model"`
	}
	if err := json.Unmarshal(data, &sh); err != nil {
		return err
	}
	in.URL, in.Provider, in.Model = sh.URL, sh.Provider, sh.Model
	return decodeFlexField("max_sentences", sh.MaxSentences, &in.MaxSentences)
}
