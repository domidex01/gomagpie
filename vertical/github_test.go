package vertical_test

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"magpie/vertical"
)

func TestGithubMatch_Table(t *testing.T) {
	yes := []string{
		"https://github.com/o/r",
		"https://github.com/o/r/issues/7",
		"https://github.com/o/r/pull/9",
		"https://github.com/o/r/releases",
		"https://github.com/o/r/releases/tag/v1.0",
		"https://www.github.com/o/r",
	}
	no := []string{
		"https://github.com/o",
		"https://github.com/",
		"https://gist.github.com/o/abc",
		"https://raw.githubusercontent.com/o/r/main/f",
		"https://example.com/o/r",
	}
	ex, _ := vertical.Lookup("github_repo")
	for _, raw := range yes {
		if !ex.Match(mustURL(t, raw)) {
			t.Errorf("Match(%s) = false, want true", raw)
		}
	}
	for _, raw := range no {
		if ex.Match(mustURL(t, raw)) {
			t.Errorf("Match(%s) = true, want false", raw)
		}
	}
}

func TestGithubExtract_Repo(t *testing.T) {
	fx := &fakeVerticalFetcher{bodies: map[string]fakeResp{
		"api.github.com": {body: verticalFixture(t, "github-repo.json")},
	}}
	ex, _ := vertical.Lookup("github_repo")
	got, err := ex.Extract(context.Background(), fx, mustURL(t, "https://github.com/o/r"))
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	want := map[string]any{
		"kind": "repo", "full_name": "o/r", "description": "demo",
		"stars": float64(42), "forks": float64(7), "language": "Go",
		"license": "mit", "open_issues": float64(3), "url": "https://github.com/o/r",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %#v, want %#v", got, want)
	}
	if reqs := fx.requests(); len(reqs) != 1 || !strings.Contains(reqs[0], "api.github.com/repos/o/r") {
		t.Errorf("requests = %v, want [api.github.com/repos/o/r]", reqs)
	}
}

func TestGithubExtract_Issue(t *testing.T) {
	fx := &fakeVerticalFetcher{bodies: map[string]fakeResp{
		"api.github.com": {body: verticalFixture(t, "github-issue.json")},
	}}
	ex, _ := vertical.Lookup("github_repo")
	got, err := ex.Extract(context.Background(), fx, mustURL(t, "https://github.com/o/r/issues/7"))
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	want := map[string]any{
		"kind": "issue", "title": "Crash on empty input", "author": "octocat",
		"state": "open", "comments": float64(5),
		"body": "Steps to reproduce this crash on empty input are straightforward and documented here.",
		"url":  "https://github.com/o/r/issues/7",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %#v, want %#v", got, want)
	}
	if reqs := fx.requests(); len(reqs) != 1 || !strings.Contains(reqs[0], "api.github.com/repos/o/r/issues/7") {
		t.Errorf("requests = %v, want [api.github.com/repos/o/r/issues/7]", reqs)
	}
}

func TestGithubExtract_PR(t *testing.T) {
	fx := &fakeVerticalFetcher{bodies: map[string]fakeResp{
		"api.github.com": {body: verticalFixture(t, "github-issue.json")},
	}}
	ex, _ := vertical.Lookup("github_repo")
	got, err := ex.Extract(context.Background(), fx, mustURL(t, "https://github.com/o/r/pull/9"))
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if got["kind"] != "pr" {
		t.Errorf("kind = %v, want pr", got["kind"])
	}
	if reqs := fx.requests(); len(reqs) != 1 || !strings.Contains(reqs[0], "/pulls/9") {
		t.Errorf("requests = %v, want [/pulls/9]", reqs)
	}
}

func TestGithubExtract_404(t *testing.T) {
	fx := &fakeVerticalFetcher{bodies: map[string]fakeResp{
		"api.github.com": {status: 404, body: []byte("not found")},
	}}
	ex, _ := vertical.Lookup("github_repo")
	if _, err := ex.Extract(context.Background(), fx, mustURL(t, "https://github.com/o/r")); err == nil {
		t.Error("404: want hard error")
	}
}
