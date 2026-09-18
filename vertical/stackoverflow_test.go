package vertical_test

import (
	"strings"
	"testing"

	"magpie/vertical"
)

// Fixture stackoverflow.json / stackoverflow-answers.json: recorded
// 2026-09-19 shape fixtures mirroring api.stackexchange.com/2.3
// responses with filter=withbody (not live captures).

func TestStackOverflowMatch_Table(t *testing.T) {
	ex, _ := vertical.Lookup("stackoverflow")
	yes := []string{
		"https://stackoverflow.com/questions/123456",
		"https://stackoverflow.com/questions/123456/how-do-i-parse-json",
		"https://www.stackoverflow.com/questions/9/x/",
	}
	no := []string{
		"https://stackoverflow.com/questions", // bare tag index
		"https://stackoverflow.com/users/1/me",
		"https://example.com/questions/123",
	}
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

func TestStackOverflowExtract(t *testing.T) {
	fx := &fakeVerticalFetcher{bodies: map[string]fakeResp{
		"api.stackexchange.com/2.3/questions/123456?": {body: verticalFixture(t, "stackoverflow.json")},
		"api.stackexchange.com/2.3/questions/123456/": {body: verticalFixture(t, "stackoverflow-answers.json")},
	}}
	ex, _ := vertical.Lookup("stackoverflow")
	got, err := ex.Extract(t.Context(), fx, mustURL(t, "https://stackoverflow.com/questions/123456/how-do-i-parse-json"))
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if got["kind"] != "question" || got["title"] != "How do I parse JSON in Go?" || got["score"] != float64(42) {
		t.Errorf("question fields = %#v", got)
	}
	if got["owner"] != "soasker" {
		t.Errorf("owner = %v, want soasker", got["owner"])
	}
	tags, _ := got["tags"].([]any)
	if len(tags) != 2 || tags[0] != "go" {
		t.Errorf("tags = %#v, want [go json]", tags)
	}
	answers, _ := got["answers"].([]any)
	if len(answers) != 2 {
		t.Fatalf("answers = %#v, want 2", answers)
	}
	a0, _ := answers[0].(map[string]any)
	if a0["score"] != float64(75) || a0["is_accepted"] != true {
		t.Errorf("answers[0] = %#v, want accepted score 75", a0)
	}
	a1, _ := answers[1].(map[string]any)
	if a1["is_accepted"] != false {
		t.Errorf("answers[1] = %#v, want not accepted", a1)
	}
	// Request order: question first, then answers (the fake's order log
	// is the oracle — counters-over-error-text, Phase D lesson).
	reqs := fx.requests()
	if len(reqs) != 2 || !strings.Contains(reqs[0], "/questions/123456?") || !strings.Contains(reqs[1], "/answers") {
		t.Errorf("requests = %v, want question then answers", reqs)
	}
}

func TestStackOverflow_Non2xxErrors(t *testing.T) {
	fx := &fakeVerticalFetcher{bodies: map[string]fakeResp{
		"api.stackexchange.com": {status: 503, body: []byte("throttled")},
	}}
	ex, _ := vertical.Lookup("stackoverflow")
	_, err := ex.Extract(t.Context(), fx, mustURL(t, "https://stackoverflow.com/questions/1/x"))
	if err == nil || !strings.Contains(err.Error(), "503") {
		t.Errorf("err = %v, want HTTP 503", err)
	}
}
