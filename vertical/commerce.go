package vertical

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/domidex01/magpie/clean"

	"github.com/PuerkitoBio/goquery"
)

func init() {
	register(Extractor{
		Info: Info{
			Name:     "shopify_product",
			Label:    "Shopify product",
			Desc:     "Product record via the store's /products/{handle}.js endpoint. Explicit-only: matches any /products/ path.",
			Patterns: []string{"https://{shop}/products/{handle}"},
		},
		Match:   matchShopify,
		Extract: extractShopify,
		OptIn:   true,
	})
	register(Extractor{
		Info: Info{
			Name:     "ecommerce_product",
			Label:    "E-commerce product",
			Desc:     "Product record from embedded JSON-LD (first Product block). Explicit-only: matches any page.",
			Patterns: []string{"https://{any-product-page}"},
		},
		Match:   matchEcommerce,
		Extract: extractEcommerce,
		OptIn:   true,
	})
}

// matchShopify is deliberately permissive (any /products/ path) — hence OptIn.
func matchShopify(u *url.URL) bool {
	if u.Scheme != "http" && u.Scheme != "https" {
		return false
	}
	return strings.Contains(u.Path, "/products/")
}

// matchEcommerce is maximally permissive — hence OptIn, never auto-fired.
func matchEcommerce(u *url.URL) bool {
	return u.Scheme == "http" || u.Scheme == "https"
}

func extractShopify(ctx context.Context, f Fetcher, u *url.URL) (map[string]any, error) {
	segs := pathSegs(u.Path)
	handle := ""
	for i, s := range segs {
		if s == "products" && i+1 < len(segs) {
			handle = strings.TrimSuffix(segs[i+1], ".js")
			break
		}
	}
	if handle == "" {
		return nil, fmt.Errorf("vertical: shopify: no product handle in %s", u.Path)
	}
	m, err := fetchJSON(ctx, f, u.Scheme+"://"+u.Host+"/products/"+handle+".js")
	if err != nil {
		return nil, err
	}
	// price arrives as integer cents → major units as float (compared with
	// 1e-9 tolerance in tests — 1999/100 is not exact in binary).
	price := num(m, "price") / 100
	var images []any
	if raw, _ := m["images"].([]any); raw != nil {
		for _, im := range raw {
			if imM, _ := im.(map[string]any); imM != nil {
				if src := str(imM, "src"); src != "" {
					images = append(images, src)
				}
			}
		}
	}
	if images == nil {
		images = []any{}
	}
	// NOTE: the .js payload carries no currency — the key is omitted, never guessed.
	return map[string]any{
		"title":  str(m, "title"),
		"vendor": str(m, "vendor"),
		"price":  price,
		"type":   str(m, "type", "product_type"),
		"images": images,
		"url":    u.Scheme + "://" + u.Host + "/products/" + handle,
	}, nil
}

func extractEcommerce(ctx context.Context, f Fetcher, u *url.URL) (map[string]any, error) {
	body, err := fetchBytes(ctx, f, u.String())
	if err != nil {
		return nil, err
	}
	sidecar := clean.HarvestSidecar(body)
	blocks := productBlocks(sidecar)
	if len(sidecar) == 0 {
		// HarvestSidecar misses non-ld JSON-LD spellings (case/whitespace
		// variants); scan the raw HTML for @type Product blocks directly.
		blocks = scanProductBlocks(body)
	}
	for _, block := range blocks {
		var m map[string]any
		if err := json.Unmarshal(block, &m); err != nil || !isProductType(m["@type"]) {
			continue
		}
		return productMap(m, u.String()), nil
	}
	return nil, fmt.Errorf("vertical: ecommerce: no product data at %s", u.String())
}

// productBlocks normalizes HarvestSidecar's array-or-single output shape
// into a plain list.
func productBlocks(sidecar []byte) []json.RawMessage {
	if len(sidecar) == 0 {
		return nil
	}
	var arr []json.RawMessage
	if err := json.Unmarshal(sidecar, &arr); err == nil {
		return arr
	}
	var single json.RawMessage
	if err := json.Unmarshal(sidecar, &single); err == nil {
		return []json.RawMessage{single}
	}
	return nil
}

// scanProductBlocks is the fallback for JSON-LD the goquery harvest missed:
// it extracts balanced-brace objects containing a Product @type marker.
func scanProductBlocks(html []byte) []json.RawMessage {
	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(html))
	if err != nil {
		return nil
	}
	var blocks []json.RawMessage
	doc.Find("script").Each(func(_ int, s *goquery.Selection) {
		t := strings.TrimSpace(s.Text())
		if t == "" || !strings.Contains(t, "Product") {
			return
		}
		for _, frag := range clean.BraceObjects(t) {
			if json.Valid([]byte(frag)) && strings.Contains(frag, "Product") {
				blocks = append(blocks, json.RawMessage(frag))
			}
		}
	})
	return blocks
}

func isProductType(t any) bool {
	switch v := t.(type) {
	case string:
		return strings.Contains(v, "Product")
	case []any:
		for _, e := range v {
			if s, _ := e.(string); strings.Contains(s, "Product") {
				return true
			}
		}
	}
	return false
}

func productMap(m map[string]any, pageURL string) map[string]any {
	brand := ""
	switch b := m["brand"].(type) {
	case string:
		brand = b
	case map[string]any:
		brand = str(b, "name")
	}
	offers := child(m, "offers")
	if offers == nil {
		if arr, _ := m["offers"].([]any); len(arr) > 0 {
			offers, _ = arr[0].(map[string]any)
		}
	}
	var price any
	var currency, availability string
	if offers != nil {
		price = offers["price"]
		currency = str(offers, "priceCurrency")
		availability = str(offers, "availability")
	}
	name := str(m, "name")
	if name == "" {
		name = str(m, "title")
	}
	url := str(m, "url")
	if url == "" {
		url = pageURL
	}
	return map[string]any{
		"name":         name,
		"brand":        brand,
		"price":        price,
		"currency":     currency,
		"availability": availability,
		"url":          url,
	}
}
