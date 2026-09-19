package core_test

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/motherlodelab/magpie/clean"
	"github.com/motherlodelab/magpie/core"
	"github.com/motherlodelab/magpie/fetch"
)

func TestPipelineOrdersAndDrains(t *testing.T) {
	source := make(chan core.FetchTask, 10)
	for i := 0; i < 5; i++ {
		source <- core.FetchTask{URL: fmt.Sprintf("http://ex.com/%d", i)}
	}
	close(source)
	var mu sync.Mutex
	var got []string
	err := core.Run(context.Background(), core.PipelineConfig{},
		source,
		func(_ context.Context, task core.FetchTask) (core.FetchedPage, error) {
			return core.FetchedPage{Task: task, Resp: &fetch.FetchResponse{URL: task.URL}}, nil
		},
		func(_ context.Context, p core.FetchedPage) (core.Cleaned, error) {
			return core.Cleaned{Task: p.Task, Resp: p.Resp, Page: clean.CleanedPage{Markdown: p.Task.URL}}, nil
		},
		func(_ context.Context, c core.Cleaned) (core.PageResult, error) {
			return core.PageResult{Task: c.Task, Record: map[string]any{"u": c.Task.URL}}, nil
		},
		func(r core.PageResult) {
			mu.Lock()
			defer mu.Unlock()
			got = append(got, r.Task.URL)
		},
	)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	sort.Strings(got)
	if len(got) != 5 {
		t.Fatalf("sink got %d results, want 5", len(got))
	}
	for i := 0; i < 5; i++ {
		if want := fmt.Sprintf("http://ex.com/%d", i); got[i] != want {
			t.Errorf("got[%d] = %s, want %s", i, got[i], want)
		}
	}
}

func TestPipelineStageErrorAborts(t *testing.T) {
	source := make(chan core.FetchTask, 10)
	for i := 0; i < 5; i++ {
		source <- core.FetchTask{URL: fmt.Sprintf("http://ex.com/%d", i)}
	}
	close(source)
	fetchFn := func(_ context.Context, task core.FetchTask) (core.FetchedPage, error) {
		if task.URL == "http://ex.com/3" {
			return core.FetchedPage{}, fmt.Errorf("stage fatal")
		}
		return core.FetchedPage{Task: task}, nil
	}
	cleanFn := func(_ context.Context, p core.FetchedPage) (core.Cleaned, error) {
		return core.Cleaned{Task: p.Task}, nil
	}
	extractFn := func(_ context.Context, c core.Cleaned) (core.PageResult, error) {
		return core.PageResult{Task: c.Task}, nil
	}
	done := make(chan error, 1)
	go func() {
		done <- core.Run(context.Background(), core.PipelineConfig{FetchWorkers: 1}, source, fetchFn, cleanFn, extractFn, func(core.PageResult) {})
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Run = nil, want stage error")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run hung; failing instead of blocking CI")
	}
}

func TestPipelineCancelStopsPromptly(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	source := make(chan core.FetchTask) // never closed, never fed
	block := func(ctx context.Context, task core.FetchTask) (core.FetchedPage, error) {
		<-ctx.Done()
		return core.FetchedPage{}, ctx.Err()
	}
	done := make(chan error, 1)
	go func() {
		done <- core.Run(ctx, core.PipelineConfig{}, source, block,
			func(_ context.Context, p core.FetchedPage) (core.Cleaned, error) {
				return core.Cleaned{Task: p.Task}, nil
			},
			func(_ context.Context, c core.Cleaned) (core.PageResult, error) {
				return core.PageResult{Task: c.Task}, nil
			},
			func(core.PageResult) {})
	}()
	time.Sleep(100 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop within 2s of cancel")
	}
}

func TestPipelinePageErrorFlowsToSink(t *testing.T) {
	source := make(chan core.FetchTask, 2)
	source <- core.FetchTask{URL: "http://ex.com/ok"}
	source <- core.FetchTask{URL: "http://ex.com/bad"}
	close(source)
	var mu sync.Mutex
	errs, oks := 0, 0
	err := core.Run(context.Background(), core.PipelineConfig{FetchWorkers: 1, CleanWorkers: 1, ExtractWorkers: 1},
		source,
		func(_ context.Context, task core.FetchTask) (core.FetchedPage, error) {
			if task.URL == "http://ex.com/bad" {
				return core.FetchedPage{Task: task, Err: fmt.Errorf("fetch boom")}, nil
			}
			return core.FetchedPage{Task: task, Resp: &fetch.FetchResponse{URL: task.URL}}, nil
		},
		func(_ context.Context, p core.FetchedPage) (core.Cleaned, error) {
			return core.Cleaned{Task: p.Task, Resp: p.Resp, Err: p.Err}, nil
		},
		func(_ context.Context, c core.Cleaned) (core.PageResult, error) {
			return core.PageResult{Task: c.Task, Err: c.Err}, nil
		},
		func(r core.PageResult) {
			mu.Lock()
			defer mu.Unlock()
			if r.Err != nil {
				errs++
			} else {
				oks++
			}
		},
	)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if oks != 1 || errs != 1 {
		t.Errorf("ok=%d err=%d, want 1/1", oks, errs)
	}
}
