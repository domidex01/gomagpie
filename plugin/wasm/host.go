// Package wasm sandboxes untrusted .wasm transforms in wazero behind a
// hand-audited 4-function host surface.
//
// ABI contract (guests are built against this, not against Go source):
//
//	host module "gomagpie" exports:
//	  gomagpie_log(level:i32, ptr:i32, len:i32)
//	  gomagpie_get_input() -> (ptr:i32, len:i32)
//	    host bump-allocates guest memory and writes the input record JSON
//	  gomagpie_set_output(ptr:i32, len:i32)
//	    host reads guest memory and stores the output
//	  gomagpie_config_get(kptr:i32, klen:i32) -> (vptr:i32, vlen:i32)
//	    whitelisted keys only; miss returns (0,0)
//	guest exports:
//	  gomagpie_api_version() -> i32 = major*10000+minor*100+patch
//	  run() -> i32 (0 = ok, else error)
//
// Strings cross as (ptr,len) u32 pairs — WithFunc takes numeric types only.
// The host Grows guest memory and bump-allocates regions for get_input /
// config_get return values; set_output / log read guest memory, so guests
// need no malloc.
package wasm

import (
	"context"
	"fmt"
	"io"
	"sync"
	"time"

	"gomagpie/core"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
)

// Defaults: 64 pages = 4 MiB addressable per instance, 10s per call.
const (
	DefaultMaxMemoryPages = 64
	DefaultTimeout        = 10 * time.Second
	// maxHostAlloc caps host-side bump allocation per call (4 MiB). It is an
	// independent floor below the runtime page limit: both are always active.
	maxHostAlloc = 4 << 20
)

// Runner compiles a guest once and instantiates it fresh per Transform
// (spec §7 isolation). Compile once (shared, safe for concurrent
// instantiation); fresh instance per call.
type Runner struct {
	compiled wazero.CompiledModule
	runtime  wazero.Runtime
	config   map[string]string
	maxPages uint32
	timeout  time.Duration
	mu       sync.Mutex // serializes per-call host-module state
	lastLogs []LogEntry
}

// LogEntry is one guest gomagpie_log call.
type LogEntry struct {
	Level   uint32
	Message string
}

// NewRunner compiles wasmBytes and enforces the version gate: the guest
// must export gomagpie_api_version ("not a gomagpie plugin" otherwise) and
// its major must match core.CoreAPIVersion.
func NewRunner(ctx context.Context, wasmBytes []byte) (*Runner, error) {
	return newRunnerWithConfig(ctx, wasmBytes, nil, DefaultMaxMemoryPages, DefaultTimeout)
}

func newRunnerWithConfig(ctx context.Context, wasmBytes []byte, config map[string]string, maxPages uint32, timeout time.Duration) (*Runner, error) {
	if maxPages == 0 {
		maxPages = DefaultMaxMemoryPages
	}
	if timeout == 0 {
		timeout = DefaultTimeout
	}
	// Both bounds active: the runtime page limit plus the bump-allocator cap
	// in alloc (an independent floor when guests grow memory themselves).
	r := wazero.NewRuntimeWithConfig(ctx, wazero.NewRuntimeConfig().WithMemoryLimitPages(maxPages))
	// WASI is link-only: instantiate with no dir preopens, empty env, and
	// stdout/stderr discarded. TinyGo/Rust guests import
	// wasi_snapshot_preview1 unconditionally, so refusing WASI would brick
	// the guest ecosystem; with zero preopened FDs the filesystem and
	// sockets stay unreachable (path_open/sock_send return nonzero errno).
	wasi_snapshot_preview1.MustInstantiate(ctx, r)
	compiled, err := r.CompileModule(ctx, wasmBytes)
	if err != nil {
		_ = r.Close(ctx) //nolint:errcheck // compile failed; teardown unactionable
		return nil, fmt.Errorf("wasm: compile: %w", err)
	}
	rn := &Runner{compiled: compiled, runtime: r, config: config, maxPages: maxPages, timeout: timeout}
	ver, err := rn.apiVersion(ctx)
	if err != nil {
		_ = r.Close(ctx) //nolint:errcheck // gate failed; teardown unactionable
		return nil, err
	}
	if ver/10000 != coreMajor() {
		_ = r.Close(ctx) //nolint:errcheck // gate failed; teardown unactionable
		return nil, fmt.Errorf("wasm: api version %d incompatible with core %s (major mismatch)", ver, core.CoreAPIVersion)
	}
	return rn, nil
}

func coreMajor() uint32 {
	var maj uint32
	_, _ = fmt.Sscanf(core.CoreAPIVersion, "%d.", &maj) //nolint:errcheck // CoreAPIVersion is a const; unparsable means gate rejects loudly below
	return maj
}

// Close releases the runtime and compiled module.
func (r *Runner) Close(ctx context.Context) error {
	return r.runtime.Close(ctx) //nolint:wrapcheck // teardown passthrough
}

// callState holds one Transform's host-side state. The host module is
// instantiated per call with closures over it, so concurrent Transforms
// never share memory views (serialized by mu at instantiate time).
type callState struct {
	input  []byte
	output []byte
	hasOut bool
	logs   []LogEntry
	config map[string]string
	alloc  uint32 // bump-allocated bytes this call (independent floor)
	next   uint32 // next free guest address for host writes
}

// Transform runs the guest's run() against input and returns what the guest
// stored via gomagpie_set_output. run returning 0 without set_output is a
// guest bug and errors loudly.
func (r *Runner) Transform(ctx context.Context, input []byte) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	st := &callState{input: input, config: r.config}
	host, err := r.instantiateHost(ctx, st)
	if err != nil {
		return nil, err
	}
	defer func() { _ = host.Close(ctx) }() //nolint:errcheck // per-call teardown; failure unactionable

	mod, err := r.runtime.InstantiateModule(ctx, r.compiled, wazero.NewModuleConfig().
		WithName("").
		WithStdin(emptyReader{}).
		WithStdout(io.Discard).
		WithStderr(io.Discard))
	if err != nil {
		return nil, fmt.Errorf("wasm: instantiate: %w", err)
	}
	defer func() { _ = mod.Close(ctx) }() //nolint:errcheck // per-call teardown

	// ponytail: watchdog-Close CPU bound — a pathological guest may survive
	// until the runtime closes with it (ceiling); upgrade to a per-call
	// runtime with shared CompilationCache if ever observed.
	callCtx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()
	go func() {
		<-callCtx.Done()
		_ = mod.Close(context.WithoutCancel(callCtx)) //nolint:errcheck // watchdog; error unactionable
	}()

	runFn := mod.ExportedFunction("run")
	if runFn == nil {
		return nil, fmt.Errorf("wasm: not a gomagpie plugin (missing run export)")
	}
	results, err := runFn.Call(callCtx)
	if err != nil {
		return nil, fmt.Errorf("wasm: run: %w", err)
	}
	code := uint32(0)
	if len(results) > 0 {
		code = uint32(results[0])
	}
	if code != 0 {
		return nil, fmt.Errorf("wasm: run returned %d", code)
	}
	if !st.hasOut {
		return nil, fmt.Errorf("wasm: run returned 0 without set_output")
	}
	r.lastLogs = append([]LogEntry(nil), st.logs...)
	return st.output, nil
}

// LastLogs returns the guest log lines captured by the most recent
// Transform (test observability; production discards them).
func (r *Runner) LastLogs() []LogEntry {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]LogEntry(nil), r.lastLogs...)
}

func (r *Runner) apiVersion(ctx context.Context) (uint32, error) {
	st := &callState{}
	host, err := r.instantiateHost(ctx, st)
	if err != nil {
		return 0, err
	}
	defer func() { _ = host.Close(ctx) }() //nolint:errcheck // gate teardown
	mod, err := r.runtime.InstantiateModule(ctx, r.compiled, wazero.NewModuleConfig().
		WithStdin(emptyReader{}).
		WithStdout(io.Discard).
		WithStderr(io.Discard))
	if err != nil {
		// Missing host/WASI imports surface here as instantiation failure.
		return 0, fmt.Errorf("wasm: not a gomagpie plugin: %w", err)
	}
	defer func() { _ = mod.Close(ctx) }() //nolint:errcheck // gate teardown
	fn := mod.ExportedFunction("gomagpie_api_version")
	if fn == nil {
		return 0, fmt.Errorf("wasm: not a gomagpie plugin (missing gomagpie_api_version export)")
	}
	results, err := fn.Call(ctx)
	if err != nil {
		return 0, fmt.Errorf("wasm: api version: %w", err)
	}
	if len(results) == 0 {
		return 0, fmt.Errorf("wasm: not a gomagpie plugin (api version returned nothing)")
	}
	return uint32(results[0]), nil
}

func (r *Runner) instantiateHost(ctx context.Context, st *callState) (api.Module, error) {
	b := r.runtime.NewHostModuleBuilder("gomagpie")
	b.NewFunctionBuilder().
		WithFunc(func(_ context.Context, mod api.Module, level, ptr, length uint32) {
			msg := readGuest(mod, ptr, length)
			st.logs = append(st.logs, LogEntry{Level: level, Message: string(msg)})
		}).
		Export("gomagpie_log")
	b.NewFunctionBuilder().
		WithFunc(func(_ context.Context, mod api.Module) (uint32, uint32) {
			return writeGuest(mod, st, st.input)
		}).
		Export("gomagpie_get_input")
	b.NewFunctionBuilder().
		WithFunc(func(_ context.Context, mod api.Module, ptr, length uint32) {
			buf := readGuest(mod, ptr, length)
			out := append([]byte(nil), buf...)
			st.output, st.hasOut = out, true
		}).
		Export("gomagpie_set_output")
	b.NewFunctionBuilder().
		WithFunc(func(_ context.Context, mod api.Module, kptr, klen uint32) (uint32, uint32) {
			val, ok := st.config[string(readGuest(mod, kptr, klen))]
			if !ok {
				return 0, 0
			}
			return writeGuest(mod, st, []byte(val))
		}).
		Export("gomagpie_config_get")
	return b.Instantiate(ctx)
}

// readGuest copies [ptr,ptr+len) out of guest memory (nil on OOB — a
// hostile ptr must never panic the host).
func readGuest(mod api.Module, ptr, length uint32) []byte {
	if length == 0 {
		return []byte{}
	}
	buf, ok := mod.Memory().Read(ptr, length)
	if !ok {
		return nil
	}
	return append([]byte(nil), buf...)
}

// writeGuest bump-allocates len(data) bytes in guest memory and copies data
// in, returning (ptr,len). Allocation grows guest memory on demand and is
// capped by maxHostAlloc independently of the runtime page limit.
func writeGuest(mod api.Module, st *callState, data []byte) (uint32, uint32) {
	if len(data) == 0 {
		return 0, 0
	}
	if st.alloc+uint32(len(data)) > maxHostAlloc {
		return 0, 0
	}
	mem := mod.Memory()
	size := mem.Size()
	if st.next == 0 {
		st.next = size // first host write starts past current memory
	}
	end := st.next + uint32(len(data))
	for end > mem.Size() {
		if _, ok := mem.Grow(1); !ok {
			return 0, 0
		}
	}
	if !mem.Write(st.next, data) {
		return 0, 0
	}
	addr := st.next
	st.next = end
	st.alloc += uint32(len(data))
	return addr, uint32(len(data))
}

type emptyReader struct{}

func (emptyReader) Read([]byte) (int, error) { return 0, io.EOF }
