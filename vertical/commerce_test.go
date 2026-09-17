package vertical_test

import (
	"context"
	"math"
	"reflect"
	"strings"
	"testing"

	"gomagpie/vertical"
)

func TestCommerceMatch_OptInPositives(t *testing.T) {
	shop, _ := vertical.Lookup("shopify_product")
	if !shop.Match(mustURL(t, "https://shop.example/products/demo-t-shirt")) {
		t.Error("shopify_product.Match(/products/) = false, want true")
	}
	if shop.Match(mustURL(t, "https://shop.example/collections/all")) {
		t.Error("shopify_product.Match(non-product) = true, want false")
	}
	eco, _ := vertical.Lookup("ecommerce_product")
	if !eco.Match(mustURL(t, "https://blog.example/some/post")) {
		t.Error("ecommerce_product.Match(any http URL) = false, want true")
	}
	if eco.Match(mustURL(t, "file:///tmp/x.html")) {
		t.Error("ecommerce_product.Match(file URL) = true, want false")
	}
}

func TestShopifyExtract(t *testing.T) {
	fx := &fakeVerticalFetcher{bodies: map[string]fakeResp{
		".js": {body: verticalFixture(t, "shopify-product.json")},
	}}
	ex, _ := vertical.Lookup("shopify_product")
	got, err := ex.Extract(context.Background(), fx, mustURL(t, "https://shop.example/products/demo-t-shirt"))
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if _, present := got["currency"]; present {
		t.Errorf("currency key present (%v) — the .js payload has none, omission locked", got["currency"])
	}
	price, _ := got["price"].(float64)
	if math.Abs(price-19.99) > 1e-9 {
		t.Errorf("price = %v, want 19.99 (cents→units)", got["price"])
	}
	rest := map[string]any{}
	for k, v := range got {
		if k != "price" {
			rest[k] = v
		}
	}
	want := map[string]any{
		"title": "Demo T-Shirt", "vendor": "Demo Store", "type": "Apparel",
		"images": []any{"https://cdn.example/img1.jpg", "https://cdn.example/img2.jpg"},
		"url":    "https://shop.example/products/demo-t-shirt",
	}
	if !reflect.DeepEqual(rest, want) {
		t.Errorf("got %#v, want %#v", rest, want)
	}
}

func TestEcommerceExtract(t *testing.T) {
	fx := &fakeVerticalFetcher{bodies: map[string]fakeResp{
		"shop.example": {body: verticalFixture(t, "product-jsonld.html")},
	}}
	ex, _ := vertical.Lookup("ecommerce_product")
	got, err := ex.Extract(context.Background(), fx, mustURL(t, "https://shop.example/widget"))
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	want := map[string]any{
		"name": "Demo Widget", "brand": "WidgetCo", "price": float64(29.99),
		"currency": "USD", "availability": "https://schema.org/InStock",
		"url": "https://shop.example/widget",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %#v, want %#v", got, want)
	}
}

func TestEcommerceExtract_NoProduct(t *testing.T) {
	fx := &fakeVerticalFetcher{bodies: map[string]fakeResp{
		"blog.example": {body: []byte(`<html><head><title>Plain</title></head><body><p>` + `Just a plain blog post with no product markup at all, nothing to extract here.` + `</p></body></html>`)},
	}}
	ex, _ := vertical.Lookup("ecommerce_product")
	_, err := ex.Extract(context.Background(), fx, mustURL(t, "https://blog.example/post"))
	if err == nil {
		t.Fatal("no Product block: want error, never an empty map")
	}
	if got := err.Error(); !strings.Contains(got, "no product data") {
		t.Errorf("err = %q, want 'no product data'", got)
	}
}

func TestEcommerceExtract_NonstandardScriptType(t *testing.T) {
	// HarvestSidecar exact-matches type="application/ld+json"; a parameter
	// suffix blinds it, so the brace-scan fallback must still find Product.
	html := `<html><head><title>W</title><script type="application/ld+json; charset=utf-8">` +
		`{"@context": "https://schema.org", "@type": "Product", "name": "Fallback Widget", ` +
		`"brand": "FW", "offers": {"price": 9.5, "priceCurrency": "EUR", "availability": "https://schema.org/InStock"}}` +
		`</script></head><body><p>Buy it.</p></body></html>`
	fx := &fakeVerticalFetcher{bodies: map[string]fakeResp{
		"shop.example": {body: []byte(html)},
	}}
	ex, _ := vertical.Lookup("ecommerce_product")
	got, err := ex.Extract(context.Background(), fx, mustURL(t, "https://shop.example/w"))
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if got["name"] != "Fallback Widget" || got["currency"] != "EUR" {
		t.Errorf("fallback scan = %v, want Fallback Widget/EUR", got)
	}
}
