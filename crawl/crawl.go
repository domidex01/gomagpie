package crawl

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"gomagpie/clean"
	"gomagpie/core"
	"gomagpie/extract"
	"gomagpie/fetch"
	"gomagpie/selector"
	"gomagpie/store"
)

// ErrRobotsBlocked marks a crawl refused by robots.txt (deny or unreachable).
var ErrRobotsBlocked = errors.New("robots.txt disallows crawl")

// ErrCostCeiling marks a crawl aborted before exceeding --max-cost.
var ErrCostCeiling = errors.New("cost ceiling exceeded")

// Options configures one crawl run.
type Options struct {
	SeedURL      string
	Schema       *extract.Schema
	MaxPages     int
	MaxDepth     int
	SameHost     bool
	FetchWorkers int
	Rate         float64 // per-host rps; <=0 = 1
	Format       string  // jsonl|json|csv|sqlite
	Out          string
	RunID        string // fresh id (cmd-generated); ignored when Resume is set
	Resume       bool
	ResumeID     string // id to resume (set iff Resume)
	IgnoreRobots bool
	Provider     string
	Model        string
	MaxCost      float64
	DB           *store.DB
	// Progress is called once per done/errored page from the sink goroutine
	// (nil = off). Total is intentionally omitted: the frontier total is
	// unknowable upfront.
	Progress func(done int)
	// OnRecord receives each extracted record at the sink (nil = off).
	// The MCP crawl_site handler captures records through it.
	OnRecord func(map[string]any)
	// Extractor serves per-page LLM (cold start + non-cacheable + heal).
	Extractor extract.Extractor
	// Propose is the LLM-proposal step inside synthesis (nil = free paths only).
	Propose selector.ProposeFunc
}

// Result summarizes a finished crawl.
type Result struct {
	RunID    string
	PagesOK  int
	PagesErr int
	Records  int
}

// domainState holds per-domain cache/heal runtime. mu serializes the
// extract workers sharing a domain (doc map + healer + flush counter).
type domainState struct {
	mu          sync.Mutex
	doc         selector.SelectorDoc
	loaded      bool
	healer      *selector.Healer
	cachedPages int // pages served from cache since last null-rate flush
}

// Run executes a crawl: seed/resume → pump + pipeline → writer → FinishRun.
func Run(ctx context.Context, opts Options) (Result, error) {
	if opts.DB == nil {
		return Result{}, fmt.Errorf("crawl: nil DB")
	}
	if opts.Schema == nil {
		return Result{}, fmt.Errorf("crawl: nil schema")
	}
	if opts.Extractor == nil {
		return Result{}, fmt.Errorf("crawl: nil extractor")
	}
	maxPages := opts.MaxPages
	if maxPages <= 0 {
		maxPages = 100
	}
	// maxDepth <= 0 = unset → default 3; explicit seed-only passes -1.
	maxDepth := opts.MaxDepth
	if maxDepth <= 0 {
		maxDepth = 3
	}
	if opts.MaxDepth < 0 {
		maxDepth = 0
	}
	format := opts.Format
	if format == "" {
		format = "jsonl"
	}
	schemaHash := selector.SchemaHash(opts.Schema)
	required := requiredFields(opts.Schema)

	db := opts.DB
	runID := opts.RunID
	if runID == "" {
		runID = newRunID()
	}
	if opts.Resume && opts.ResumeID != "" {
		runID = opts.ResumeID
	}
	filter := NewFilter()
	if !opts.Resume {
		if runID == "" {
			runID = newRunID()
		}
		if err := db.BeginRun(runID, "crawl"); err != nil {
			return Result{}, err
		}
	} else {
		if err := db.ResumeRun(runID); err != nil {
			return Result{}, err
		}
	}

	checker := NewChecker()
	limiters := NewHostLimiters(opts.Rate, 3)

	var outstanding atomic.Int64
	if opts.Resume {
		if _, err := db.ResetInflight(runID); err != nil {
			return Result{}, err
		}
		hashes, err := db.LoadHashes(runID)
		if err != nil {
			return Result{}, err
		}
		for _, h := range hashes {
			filter.Add(h)
		}
		if p, _, _, _, err := db.CrawlStats(runID); err == nil {
			outstanding.Add(int64(p))
		}
	}
	frontier := NewFrontier(db, filter, runID)

	// Seed (fresh runs only), robots-checked first.
	if !opts.Resume {
		seedHost, herr := hostOf(opts.SeedURL)
		if herr != nil {
			seedHost = opts.SeedURL
		}
		if isHTTP(opts.SeedURL) && !opts.IgnoreRobots {
			allowed, err := checker.Allowed(ctx, opts.SeedURL)
			if err != nil || !allowed {
				if ferr := db.FinishRun(runID, 0, 0, "robots_blocked"); ferr != nil {
					fmt.Fprintf(os.Stderr, "warning: finish run: %v\n", ferr)
				}
				if err != nil && errors.Is(err, ErrRobotsUnreachable) {
					return Result{}, fmt.Errorf("crawl: seed %s: %w", seedHost, ErrRobotsBlocked)
				}
				return Result{}, fmt.Errorf("crawl: seed %s: %w", seedHost, ErrRobotsBlocked)
			}
		}
		seeds := []string{opts.SeedURL}
		if isHTTP(opts.SeedURL) {
			seeds = append(seeds, checker.Sitemaps(ctx, opts.SeedURL)...)
		}
		n, err := frontier.Add(seeds, 0)
		if err != nil {
			return Result{}, err
		}
		outstanding.Add(int64(n))
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	source := make(chan core.FetchTask, 1000)
	var pumpErr atomic.Value
	go func() {
		defer close(source)
		claimed := 0
		for {
			if runCtx.Err() != nil {
				return
			}
			if claimed >= maxPages {
				return
			}
			batch := maxPages - claimed
			if batch > 32 {
				batch = 32
			}
			rows, err := db.Claim(runID, batch)
			if err != nil {
				pumpErr.Store(err)
				return
			}
			if len(rows) == 0 {
				if outstanding.Load() == 0 {
					return
				}
				time.Sleep(50 * time.Millisecond)
				continue
			}
			for _, r := range rows {
				select {
				case source <- core.FetchTask{URL: r.URL, URLHash: r.URLHash, Depth: r.Depth}:
					claimed++
				case <-runCtx.Done():
					return
				}
			}
		}
	}()

	staticFetcher, err := fetch.NewStaticFetcher()
	if err != nil {
		return Result{}, err
	}
	gate := core.NewBrowserGate(2)

	domains := map[string]*domainState{}
	var domainsMu sync.Mutex

	// checkCeiling aborts before LLM spend when running+projected > max.
	checkCeiling := func(promptText string) error {
		if opts.MaxCost <= 0 {
			return nil
		}
		running, err := db.RunCost(runID)
		if err != nil {
			return err // fail closed
		}
		proj := extract.ProjectedCost(opts.Model, promptText)
		if proj == 0 {
			if toks := extract.EstimatePromptTokens(promptText); toks > 0 {
				proj = float64(toks) / 1e6 * 2.00
			}
		}
		if running+proj > opts.MaxCost {
			return ErrCostCeiling
		}
		return nil
	}

	extractOne := func(ctx context.Context, markdown string, sidecar json.RawMessage, purpose, extra string) (extract.ExtractResult, error) {
		if err := checkCeiling(markdown + string(sidecar)); err != nil {
			return extract.ExtractResult{}, err
		}
		return opts.Extractor.Extract(ctx, extract.ExtractInput{
			Markdown: markdown, StructuredData: sidecar,
			Schema: opts.Schema, Purpose: purpose, PromptExtra: extra,
		})
	}

	fetchFn := func(ctx context.Context, task core.FetchTask) (core.FetchedPage, error) {
		if isHTTP(task.URL) && !opts.IgnoreRobots {
			allowed, err := checker.Allowed(ctx, task.URL)
			if err != nil || !allowed {
				return core.FetchedPage{Task: task, Err: fmt.Errorf("crawl: robots disallow %s", task.URL)}, nil
			}
			if d := checker.CrawlDelay(ctx, task.URL); d > 0 {
				if host, herr := hostOf(task.URL); herr == nil && host != "" {
					limiters.SetFloor(host, d)
				}
			}
		}
		if host, herr := hostOf(task.URL); herr == nil && host != "" {
			if err := limiters.Wait(ctx, host); err != nil {
				return core.FetchedPage{}, err
			}
		}
		var last *fetch.FetchResponse
		resp, err := FetchWithRetry(ctx, func() (*fetch.FetchResponse, error) {
			r, ferr := staticFetcher.Fetch(ctx, fetch.FetchRequest{URL: task.URL})
			// Reset on transport failure: qualityErr must classify the
			// terminal outcome, never a stale response from an earlier try.
			last = r
			return r, ferr
		})
		if err != nil {
			// Retry taxonomy (backoff.go) already ran: classify the outcome.
			if qerr := qualityErr(ctx, task, last); qerr != nil {
				return core.FetchedPage{Task: task, Err: qerr}, nil
			}
			return core.FetchedPage{Task: task, Err: err}, nil
		}
		if resp.StatusCode/100 != 2 {
			if qerr := qualityErr(ctx, task, resp); qerr != nil {
				return core.FetchedPage{Task: task, Err: qerr}, nil
			}
			return core.FetchedPage{Task: task, Err: classify(resp, nil)}, nil
		}
		if score, embedded := fetch.ScoreJSRequired(resp.HTML, resp.Headers); !embedded && fetch.NeedsBrowser(score) && isHTTP(task.URL) {
			if err := gate.Acquire(ctx); err != nil {
				return core.FetchedPage{}, err
			}
			bresp, berr := func() (*fetch.FetchResponse, error) {
				defer gate.Release()
				rod := fetch.NewRodFetcher()
				defer func() { _ = rod.Close() }() //nolint:errcheck // teardown unactionable
				return rod.Fetch(ctx, fetch.FetchRequest{URL: task.URL})
			}()
			if berr != nil {
				return core.FetchedPage{Task: task, Err: berr}, nil
			}
			resp = bresp
		}
		return core.FetchedPage{Task: task, Resp: resp}, nil
	}

	cleanFn := func(ctx context.Context, page core.FetchedPage) (core.Cleaned, error) {
		if page.Err != nil || page.Resp == nil {
			return core.Cleaned{Task: page.Task, Err: page.Err}, nil
		}
		html := page.Resp.HTML
		finalURL := page.Resp.FinalURL
		if finalURL == "" {
			finalURL = page.Task.URL
		}
		sidecar := clean.HarvestSidecar(html)
		// Link extraction ALWAYS (nav links live in boilerplate).
		if links, err := frontier.ExtractLinks(html, finalURL, page.Task.Depth, maxDepth, opts.SameHost); err == nil && links > 0 {
			outstanding.Add(int64(links))
		}
		// Trafilatura only when the page will need an LLM.
		// ponytail: one SQLite point read per page to decide (ceiling =
		// negligible WAL read on the single conn).
		needLLM := true
		if doc, ok, err := db.GetSelectors(domainOf(finalURL), schemaHash); err == nil && ok {
			needLLM = hasNonCacheable(doc, opts.Schema)
		}
		if !needLLM {
			return core.Cleaned{Task: page.Task, Resp: page.Resp,
				Page:    clean.CleanedPage{StructuredData: sidecar, FinalURL: finalURL},
				Sidecar: sidecar}, nil
		}
		cleaned, err := clean.Clean(ctx, clean.RawPage{HTML: html, URL: page.Task.URL, FinalURL: finalURL})
		if err != nil {
			return core.Cleaned{Task: page.Task, Err: err}, nil
		}
		return core.Cleaned{Task: page.Task, Resp: page.Resp, Page: cleaned, Sidecar: cleaned.StructuredData}, nil
	}

	extractFn := func(ctx context.Context, cl core.Cleaned) (core.PageResult, error) {
		if cl.Err != nil {
			return core.PageResult{Task: cl.Task, Err: cl.Err}, nil
		}
		finalURL := cl.Page.FinalURL
		if finalURL == "" && cl.Resp != nil {
			finalURL = cl.Resp.FinalURL
		}
		if finalURL == "" {
			finalURL = cl.Task.URL
		}
		domain := domainOf(finalURL)
		domainsMu.Lock()
		st, ok := domains[domain]
		if !ok {
			st = &domainState{healer: selector.NewHealer(50, 0.30, 3)}
			domains[domain] = st
		}
		domainsMu.Unlock()
		st.mu.Lock()
		defer st.mu.Unlock()

		html := ""
		if cl.Resp != nil {
			html = string(cl.Resp.HTML)
		}
		// Load-once per domain per run.
		if !st.loaded {
			if raw, found, err := db.GetSelectors(domain, schemaHash); err == nil && found {
				var doc selector.SelectorDoc
				if jerr := json.Unmarshal([]byte(raw), &doc); jerr == nil {
					st.doc = doc
				}
			}
			st.loaded = true
		}
		hasDoc := len(st.doc.Fields) > 0

		if hasDoc {
			// Markdown for fill/heal LLM calls (may have skipped trafilatura).
			md := cl.Page.Markdown
			if md == "" && html != "" {
				if cleaned, cerr := clean.Clean(ctx, clean.RawPage{HTML: []byte(html), URL: cl.Task.URL, FinalURL: finalURL}); cerr == nil {
					md = cleaned.Markdown
				}
			}
			applier := selector.NewApplier(st.doc, opts.Schema)
			rec, nulls := applier.Apply(html, cl.Sidecar)
			nullSet := map[string]bool{}
			for _, f := range nulls {
				nullSet[f] = true
			}
			var triggered []string
			for _, f := range nulls {
				if trig := st.healer.Observe(f, true); len(trig) > 0 {
					triggered = append(triggered, trig...)
				}
			}
			for f := range st.doc.Fields {
				if !nullSet[f] {
					if trig := st.healer.Observe(f, false); len(trig) > 0 {
						triggered = append(triggered, trig...)
					}
				}
			}
			// Fill non-cacheable fields via per-page LLM.
			if len(nulls) > 0 {
				res, err := extractOne(ctx, md, cl.Sidecar, "extract", "")
				if err != nil {
					if errors.Is(err, ErrCostCeiling) {
						return core.PageResult{}, err
					}
					// Degrade: emit cached fields only.
					return core.PageResult{Task: cl.Task, Record: rec}, nil
				}
				for _, f := range nulls {
					if v, present := res.Record[f]; present {
						rec[f] = v
					}
				}
				if validRequired(res.Record, required) {
					st.healer.Retain(selector.SynthSample{URL: finalURL, HTML: html, Sidecar: cl.Sidecar, Truth: res.Record})
				}
			}
			if len(triggered) > 0 {
				healField(ctx, db, st, opts, domain, schemaHash, finalURL, html, md, cl.Sidecar, triggered, extractOne)
			}
			st.cachedPages++
			if st.cachedPages%25 == 0 {
				flushNullRates(db, st, domain, schemaHash)
			}
			return core.PageResult{Task: cl.Task, Record: rec}, nil
		}

		// Cold domain: direct LLM (Purpose synth) + retain + synthesize at N.
		res, err := extractOne(ctx, cl.Page.Markdown, cl.Sidecar, "synth", "")
		if err != nil {
			if errors.Is(err, ErrCostCeiling) {
				return core.PageResult{}, err
			}
			return core.PageResult{Task: cl.Task, Err: err}, nil
		}
		if validRequired(res.Record, required) {
			st.healer.Retain(selector.SynthSample{URL: finalURL, HTML: html, Sidecar: cl.Sidecar, Truth: res.Record})
			if len(st.healer.Samples()) >= 3 {
				doc, serr := selector.Synthesize(ctx, st.healer.Samples(), opts.Schema, opts.Propose, nil)
				if serr == nil {
					doc.Domain = domain
					if raw, merr := json.Marshal(doc); merr == nil {
						if perr := db.PutSelectors(domain, schemaHash, string(raw), doc.SamplesUsed); perr != nil {
							fmt.Fprintf(os.Stderr, "warning: cache selectors: %v\n", perr)
						} else {
							st.doc = doc
						}
					}
				}
			}
		}
		return core.PageResult{Task: cl.Task, Record: res.Record}, nil
	}

	w, err := newWriter(opts.Out, format, opts.Schema, db, runID)
	if err != nil {
		return Result{}, err
	}
	var sinkErr error
	doneCount := 0
	sink := func(r core.PageResult) {
		if sinkErr != nil {
			return
		}
		if r.Err != nil {
			if merr := db.MarkError(runID, hashTask(r.Task), r.Err.Error()); merr != nil {
				fmt.Fprintf(os.Stderr, "warning: mark error: %v\n", merr)
			}
		} else {
			if werr := w.write(r); werr != nil {
				sinkErr = werr
				cancel()
				if merr := db.MarkError(runID, hashTask(r.Task), werr.Error()); merr != nil {
					fmt.Fprintf(os.Stderr, "warning: mark error: %v\n", merr)
				}
			} else if merr := db.MarkDone(runID, hashTask(r.Task)); merr != nil {
				fmt.Fprintf(os.Stderr, "warning: mark done: %v\n", merr)
			} else if opts.OnRecord != nil {
				opts.OnRecord(jsonRecord(r))
			}
		}
		outstanding.Add(-1)
		doneCount++
		if opts.Progress != nil {
			opts.Progress(doneCount)
		}
	}

	cfg := core.DefaultPipelineConfig()
	if opts.FetchWorkers > 0 {
		cfg.FetchWorkers = opts.FetchWorkers
	}
	runErr := core.Run(runCtx, cfg, source, fetchFn, cleanFn, extractFn, sink)

	// Final null-rate flush per domain.
	domainsMu.Lock()
	for domain, st := range domains {
		st.mu.Lock()
		if st.loaded && len(st.doc.Fields) > 0 {
			flushNullRates(db, st, domain, schemaHash)
		}
		st.mu.Unlock()
	}
	domainsMu.Unlock()

	if werr := w.close(); werr != nil && sinkErr == nil {
		sinkErr = werr
	}
	finishWarn := func(status string) {
		if ferr := db.FinishRun(runID, 0, 0, status); ferr != nil {
			fmt.Fprintf(os.Stderr, "warning: finish run: %v\n", ferr)
		}
	}
	if sinkErr != nil {
		finishWarn("error")
		return Result{RunID: runID}, sinkErr
	}
	if perr, ok := pumpErr.Load().(error); ok && perr != nil {
		finishWarn("error")
		return Result{RunID: runID}, perr
	}
	if runErr != nil && !errors.Is(runErr, context.Canceled) {
		finishWarn("error")
		return Result{RunID: runID}, runErr
	}

	_, _, done, errs, serr := db.CrawlStats(runID)
	if serr != nil {
		finishWarn("error")
		return Result{RunID: runID}, serr
	}
	status := "finished"
	if ctx.Err() != nil || (runErr != nil && errors.Is(runErr, context.Canceled)) {
		status = "interrupted"
	}
	if ferr := db.FinishRun(runID, done, errs, status); ferr != nil {
		return Result{RunID: runID}, ferr
	}
	return Result{RunID: runID, PagesOK: done, PagesErr: errs, Records: w.count()}, nil
}

// healField re-synthesizes triggered fields only (full template when ≥50%
// broken), merging into the live doc so healthy fields serve throughout.
func healField(ctx context.Context, db *store.DB, st *domainState, opts Options, domain, schemaHash, pageURL, html, md string, sidecar json.RawMessage, triggered []string, extractOne func(context.Context, string, json.RawMessage, string, string) (extract.ExtractResult, error)) {
	// Fresh ground truth on the current (new-template) page.
	if res, err := extractOne(ctx, md, sidecar, "synth", "heal sample: "+pageURL); err == nil {
		if validRequired(res.Record, requiredFields(opts.Schema)) {
			st.healer.Retain(selector.SynthSample{URL: pageURL, HTML: html, Sidecar: sidecar, Truth: res.Record})
		}
	}
	samples := st.healer.Samples()
	if len(samples) == 0 {
		return
	}
	only := triggered
	if selector.FullResynth(len(st.doc.Fields), len(triggered)) {
		only = nil // ≥50% broken → full redesign
	}
	doc, err := selector.Synthesize(ctx, samples, opts.Schema, opts.Propose, only)
	if err != nil {
		return
	}
	if only == nil {
		for f, sel := range doc.Fields {
			st.doc.Fields[f] = sel
		}
	} else {
		for _, f := range only {
			if sel, ok := doc.Fields[f]; ok {
				st.doc.Fields[f] = sel
			} else {
				delete(st.doc.Fields, f) // still failing → per-page LLM
			}
		}
	}
	if raw, err := json.Marshal(st.doc); err == nil {
		if perr := db.PutSelectors(domain, schemaHash, string(raw), len(samples)); perr != nil {
			fmt.Fprintf(os.Stderr, "warning: cache selectors: %v\n", perr)
		}
	}
}

// flushNullRates persists current null rates into fields_json.
// ponytail: a crash loses ≤25 pages of stats (ceiling = slightly stale
// trigger after --resume — self-corrects within 25 pages).
func flushNullRates(db *store.DB, st *domainState, domain, schemaHash string) {
	for f, sel := range st.doc.Fields {
		sel.NullRate = st.healer.NullRate(f)
		st.doc.Fields[f] = sel
	}
	if raw, err := json.Marshal(st.doc); err == nil {
		if perr := db.PutSelectors(domain, schemaHash, string(raw), st.doc.SamplesUsed); perr != nil {
			fmt.Fprintf(os.Stderr, "warning: cache selectors: %v\n", perr)
		}
	}
}

func hasNonCacheable(docJSON string, sch *extract.Schema) bool {
	var doc selector.SelectorDoc
	if err := json.Unmarshal([]byte(docJSON), &doc); err != nil {
		return true
	}
	for _, f := range selector.SchemaFields(sch) {
		if _, ok := doc.Fields[f]; !ok {
			return true
		}
	}
	return false
}

func requiredFields(sch *extract.Schema) []string {
	obj, ok := sch.Raw.(map[string]any)
	if !ok {
		return nil
	}
	req, ok := obj["required"].([]any)
	if !ok {
		return nil
	}
	var out []string
	for _, r := range req {
		if s, ok := r.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func validRequired(rec map[string]any, required []string) bool {
	for _, f := range required {
		if v, ok := rec[f]; !ok || v == nil {
			return false
		}
	}
	return true
}

// qualityErr routes a non-2xx response through Clean+Classify, returning a
// typed ErrQuality error for blocked pages or nil when content is fine.
// The page lands on the existing page-error path (counted, not cached).
func qualityErr(ctx context.Context, task core.FetchTask, resp *fetch.FetchResponse) error {
	if resp == nil || resp.StatusCode/100 == 2 {
		return nil
	}
	finalURL := resp.FinalURL
	if finalURL == "" {
		finalURL = task.URL
	}
	cleaned, cerr := clean.Clean(ctx, clean.RawPage{
		HTML: resp.HTML, URL: task.URL, FinalURL: finalURL, StatusCode: resp.StatusCode,
	})
	if cerr != nil || cleaned.Quality == clean.IssueNone {
		return nil
	}
	return &clean.QualityError{Issue: cleaned.Quality, URL: task.URL}
}

func domainOf(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "invalid"
	}
	if u.Host == "" {
		return "file"
	}
	return strings.ToLower(u.Host)
}

func hostOf(rawURL string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return "", err
	}
	return strings.ToLower(u.Host), nil
}

func isHTTP(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	return u.Scheme == "http" || u.Scheme == "https"
}

func hashTask(t core.FetchTask) string {
	if t.URLHash != "" {
		return t.URLHash
	}
	return t.URL
}

func newRunID() string {
	return strings.ReplaceAll(fmt.Sprintf("%d", time.Now().UnixNano()), "-", "") + "-crawl"
}
