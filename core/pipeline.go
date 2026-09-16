package core

import (
	"context"
	"encoding/json"
	"runtime"

	"gomagpie/clean"
	"gomagpie/fetch"

	"golang.org/x/sync/errgroup"
)

// FetchTask is one URL claimed from the frontier.
type FetchTask struct {
	URL     string
	URLHash string
	Depth   int
}

// FetchedPage is a fetch-stage outcome; Err is per-page (flows to sink),
// never stage-fatal.
type FetchedPage struct {
	Task FetchTask
	Resp *fetch.FetchResponse
	Err  error
}

// Cleaned is a clean-stage outcome.
type Cleaned struct {
	Task    FetchTask
	Resp    *fetch.FetchResponse
	Page    clean.CleanedPage
	Sidecar json.RawMessage
	Err     error
}

// PageResult is one finished page for the writer.
type PageResult struct {
	Task   FetchTask
	Record map[string]any
	Links  []string
	Err    error
}

// PipelineConfig bounds per-stage concurrency.
type PipelineConfig struct {
	FetchWorkers   int
	CleanWorkers   int
	ExtractWorkers int
}

// DefaultPipelineConfig: fetch 8 / clean GOMAXPROCS / extract 4 (spec §5.2).
func DefaultPipelineConfig() PipelineConfig {
	return PipelineConfig{FetchWorkers: 8, CleanWorkers: runtime.GOMAXPROCS(0), ExtractWorkers: 4}
}

func (c PipelineConfig) sanitized() PipelineConfig {
	d := DefaultPipelineConfig()
	if c.FetchWorkers <= 0 {
		c.FetchWorkers = d.FetchWorkers
	}
	if c.CleanWorkers <= 0 {
		c.CleanWorkers = d.CleanWorkers
	}
	if c.ExtractWorkers <= 0 {
		c.ExtractWorkers = d.ExtractWorkers
	}
	return c
}

// BrowserGate caps concurrent browser fetches (memory-heavy). Created by the
// caller and captured in fetchFn; core never touches rod.
type BrowserGate struct {
	sem chan struct{}
}

// NewBrowserGate builds a gate with n slots.
func NewBrowserGate(n int) *BrowserGate {
	if n <= 0 {
		n = 2
	}
	return &BrowserGate{sem: make(chan struct{}, n)}
}

// Acquire takes a slot or returns ctx.Err().
func (b *BrowserGate) Acquire(ctx context.Context) error {
	select {
	case b.sem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Release frees a slot.
func (b *BrowserGate) Release() {
	select {
	case <-b.sem:
	default:
	}
}

// Run wires source→fetch→clean→extract→sink over bounded channels
// (1000/100/100). Each stage closes its output when its workers finish, so
// closing source drains the whole pipeline in order. A non-nil error return
// from any stage fn is stage-fatal: the errgroup cancels siblings and Run
// returns the first error. Per-page failures belong in the structs' Err.
func Run(
	ctx context.Context,
	cfg PipelineConfig,
	source <-chan FetchTask,
	fetchFn func(context.Context, FetchTask) (FetchedPage, error),
	cleanFn func(context.Context, FetchedPage) (Cleaned, error),
	extractFn func(context.Context, Cleaned) (PageResult, error),
	sink func(PageResult),
) error {
	cfg = cfg.sanitized()
	g, gctx := errgroup.WithContext(ctx)

	fetchOut := make(chan FetchedPage, 1000)
	cleanOut := make(chan Cleaned, 100)
	extractOut := make(chan PageResult, 100)

	g.Go(func() error {
		defer close(fetchOut)
		wg, wctx := errgroup.WithContext(gctx)
		wg.SetLimit(cfg.FetchWorkers)
	loop:
		for {
			select {
			case <-wctx.Done():
				break loop
			case task, ok := <-source:
				if !ok {
					break loop
				}
				wg.Go(func() error {
					res, err := fetchFn(wctx, task)
					if err != nil {
						return err
					}
					select {
					case fetchOut <- res:
						return nil
					case <-wctx.Done():
						return wctx.Err()
					}
				})
			}
		}
		return wg.Wait()
	})

	g.Go(func() error {
		defer close(cleanOut)
		wg, wctx := errgroup.WithContext(gctx)
		wg.SetLimit(cfg.CleanWorkers)
	loop:
		for {
			select {
			case <-wctx.Done():
				break loop
			case page, ok := <-fetchOut:
				if !ok {
					break loop
				}
				wg.Go(func() error {
					res, err := cleanFn(wctx, page)
					if err != nil {
						return err
					}
					select {
					case cleanOut <- res:
						return nil
					case <-wctx.Done():
						return wctx.Err()
					}
				})
			}
		}
		return wg.Wait()
	})

	g.Go(func() error {
		defer close(extractOut)
		wg, wctx := errgroup.WithContext(gctx)
		wg.SetLimit(cfg.ExtractWorkers)
	loop:
		for {
			select {
			case <-wctx.Done():
				break loop
			case cl, ok := <-cleanOut:
				if !ok {
					break loop
				}
				wg.Go(func() error {
					res, err := extractFn(wctx, cl)
					if err != nil {
						return err
					}
					select {
					case extractOut <- res:
						return nil
					case <-wctx.Done():
						return wctx.Err()
					}
				})
			}
		}
		return wg.Wait()
	})

	g.Go(func() error {
		for r := range extractOut {
			sink(r)
		}
		return nil
	})

	return g.Wait() //nolint:wrapcheck // errgroup error already contextual
}
