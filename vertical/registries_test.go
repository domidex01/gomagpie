package vertical_test

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/motherlodelab/magpie/vertical"
)

func TestRegistriesMatch_Table(t *testing.T) {
	rows := []struct {
		name string
		yes  []string
		no   []string
	}{
		{"pypi",
			[]string{"https://pypi.org/project/demo-pkg/", "https://pypi.org/project/demo-pkg", "https://www.pypi.org/project/x/"},
			[]string{"https://example.com/project/x/", "https://pypi.org/", "https://npmjs.com/package/x"}},
		{"npm",
			[]string{"https://www.npmjs.com/package/demo-npm", "https://npmjs.com/package/x"},
			[]string{"https://example.com/package/x", "https://www.npmjs.com/", "https://pypi.org/project/x/"}},
		{"crates_io",
			[]string{"https://crates.io/crates/demo-crate", "https://crates.io/crates/demo-crate/"},
			[]string{"https://lib.rs/crates/demo-crate", "https://blog.example/crates/x", "https://crates.io/"}},
		{"dockerhub",
			[]string{"https://hub.docker.com/r/o/r", "https://hub.docker.com/r/o/r/"},
			[]string{"https://hub.docker.com/r/", "https://hub.docker.com/", "https://example.com/r/o/r"}},
		{"huggingface",
			[]string{"https://huggingface.co/o/m", "https://huggingface.co/datasets/o/d"},
			[]string{"https://huggingface.co/", "https://huggingface.co/o", "https://huggingface.co/datasets/o", "https://example.com/o/m"}},
	}
	for _, r := range rows {
		ex, _ := vertical.Lookup(r.name)
		for _, raw := range r.yes {
			if !ex.Match(mustURL(t, raw)) {
				t.Errorf("%s.Match(%s) = false, want true", r.name, raw)
			}
		}
		for _, raw := range r.no {
			if ex.Match(mustURL(t, raw)) {
				t.Errorf("%s.Match(%s) = true, want false", r.name, raw)
			}
		}
	}
}

func TestPypiExtract(t *testing.T) {
	fx := &fakeVerticalFetcher{bodies: map[string]fakeResp{
		"pypi.org": {body: verticalFixture(t, "pypi.json")},
	}}
	ex, _ := vertical.Lookup("pypi")
	for _, raw := range []string{
		"https://pypi.org/project/demo-pkg/",
		"https://pypi.org/project/demo-pkg", // trailing-slash vector
	} {
		got, err := ex.Extract(context.Background(), fx, mustURL(t, raw))
		if err != nil {
			t.Fatalf("Extract(%s): %v", raw, err)
		}
		want := map[string]any{
			"name": "demo-pkg", "version": "1.2.3",
			"summary": "A demo package for testing purposes here",
			"author":  "Jane Doe", "requires_python": ">=3.9",
			"url": "https://pypi.org/project/demo-pkg/",
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("Extract(%s) = %#v, want %#v", raw, got, want)
		}
	}
}

func TestNpmExtract(t *testing.T) {
	fx := &fakeVerticalFetcher{bodies: map[string]fakeResp{
		"registry.npmjs.org": {body: verticalFixture(t, "npm.json")},
	}}
	ex, _ := vertical.Lookup("npm")
	got, err := ex.Extract(context.Background(), fx, mustURL(t, "https://www.npmjs.com/package/demo-npm"))
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	want := map[string]any{
		"name": "demo-npm", "version": "2.0.1",
		"description": "Demo npm package fixture description", "latest": "2.0.1",
		"url": "https://www.npmjs.com/package/demo-npm",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %#v, want %#v", got, want)
	}
}

func TestCratesExtract(t *testing.T) {
	fx := &fakeVerticalFetcher{bodies: map[string]fakeResp{
		"crates.io": {body: verticalFixture(t, "crates.json")},
	}}
	ex, _ := vertical.Lookup("crates_io")
	got, err := ex.Extract(context.Background(), fx, mustURL(t, "https://crates.io/crates/demo-crate"))
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	want := map[string]any{
		"name": "demo-crate", "newest_version": "0.4.2",
		"description": "A demo crate fixture for testing",
		"downloads":   float64(12345), "url": "https://crates.io/crates/demo-crate",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %#v, want %#v", got, want)
	}
}

// Fixtures dockerhub.json / huggingface.json / huggingface-dataset.json:
// recorded 2026-09-19 shape fixtures mirroring the registry API v2 / HF
// API responses (not live captures).

func TestDockerHubExtract(t *testing.T) {
	fx := &fakeVerticalFetcher{bodies: map[string]fakeResp{
		"hub.docker.com/v2/repositories/demo/app": {body: verticalFixture(t, "dockerhub.json")},
	}}
	ex, _ := vertical.Lookup("dockerhub")
	got, err := ex.Extract(context.Background(), fx, mustURL(t, "https://hub.docker.com/r/demo/app"))
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if got["name"] != "demo/app" || got["star_count"] != float64(456) || got["pull_count"] != float64(7890123) {
		t.Errorf("record = %#v", got)
	}
	reqs := fx.requests()
	if len(reqs) != 1 || !strings.Contains(reqs[0], "/v2/repositories/demo/app/") {
		t.Errorf("request = %v, want the v2 repositories API URL", reqs)
	}
}

func TestHuggingFaceExtract(t *testing.T) {
	fx := &fakeVerticalFetcher{bodies: map[string]fakeResp{
		"api/models/org/demo-model":     {body: verticalFixture(t, "huggingface.json")},
		"api/datasets/org/demo-dataset": {body: verticalFixture(t, "huggingface-dataset.json")},
	}}
	ex, _ := vertical.Lookup("huggingface")

	got, err := ex.Extract(context.Background(), fx, mustURL(t, "https://huggingface.co/org/demo-model"))
	if err != nil {
		t.Fatalf("model Extract: %v", err)
	}
	if got["kind"] != "model" || got["id"] != "org/demo-model" || got["likes"] != float64(512) || got["downloads"] != float64(1234567) {
		t.Errorf("model = %#v", got)
	}
	if got["pipeline_tag"] != "text-generation" {
		t.Errorf("pipeline_tag = %v, want text-generation (models carry it)", got["pipeline_tag"])
	}

	ds, err := ex.Extract(context.Background(), fx, mustURL(t, "https://huggingface.co/datasets/org/demo-dataset"))
	if err != nil {
		t.Fatalf("dataset Extract: %v", err)
	}
	if ds["kind"] != "dataset" || ds["id"] != "org/demo-dataset" || ds["downloads"] != float64(4242) {
		t.Errorf("dataset = %#v", ds)
	}
	if _, has := ds["pipeline_tag"]; has {
		t.Errorf("dataset pipeline_tag = %v, want absent (datasets omit it)", ds["pipeline_tag"])
	}
}
