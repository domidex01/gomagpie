package wasm

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// --- minimal wasm assembler: no guest toolchain ---

func uleb(v uint32) []byte {
	var out []byte
	for {
		b := byte(v & 0x7f)
		v >>= 7
		if v == 0 {
			return append(out, b)
		}
		out = append(out, b|0x80)
	}
}

func sleb(v int64) []byte {
	var out []byte
	for {
		b := byte(v & 0x7f)
		v >>= 7
		sign := b&0x40 != 0
		if (v == 0 && !sign) || (v == -1 && sign) {
			return append(out, b)
		}
		out = append(out, b|0x80)
	}
}

func vec(parts ...[]byte) []byte {
	out := uleb(uint32(len(parts)))
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

func bstr(s string) []byte {
	b := []byte(s)
	return append(uleb(uint32(len(b))), b...)
}

func sect(id byte, body []byte) []byte {
	out := []byte{id}
	return append(append(out, uleb(uint32(len(body)))...), body...)
}

const (
	i32 = 0x7f
	i64 = 0x7e
)

func functype(params, results []byte) []byte {
	out := []byte{0x60, byte(len(params))}
	out = append(out, params...)
	out = append(out, byte(len(results)))
	return append(out, results...)
}

type imp struct {
	mod, name string
	kind      byte
	sig       uint32 // func type idx (kind 0)
}

type exp struct {
	name string
	kind byte
	idx  uint32
}

type seg struct {
	off  uint32
	data []byte
}

// encodeModule assembles magic+version+type/import/function/memory/export/code/data.
func encodeModule(types [][]byte, imps []imp, ftypes []uint32, bodies [][]byte, exps []exp, memMin uint32, segs []seg) []byte {
	out := []byte{0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00}
	tparts := append([][]byte{}, types...)
	out = append(out, sect(1, vec(tparts...))...)
	var iparts [][]byte
	for _, im := range imps {
		e := append(bstr(im.mod), bstr(im.name)...)
		e = append(e, im.kind)
		e = append(e, uleb(im.sig)...)
		iparts = append(iparts, e)
	}
	out = append(out, sect(2, vec(iparts...))...)
	var fparts [][]byte
	for _, ft := range ftypes {
		fparts = append(fparts, uleb(ft))
	}
	out = append(out, sect(3, vec(fparts...))...)
	// one memory, min pages
	out = append(out, sect(5, vec(append([]byte{0x00}, uleb(memMin)...)))...)
	var eparts [][]byte
	for _, e := range exps {
		eparts = append(eparts, append(append(bstr(e.name), e.kind), uleb(e.idx)...))
	}
	out = append(out, sect(7, vec(eparts...))...)
	var cparts [][]byte
	for _, b := range bodies {
		cparts = append(cparts, append(uleb(uint32(len(b))), b...))
	}
	out = append(out, sect(10, vec(cparts...))...)
	if len(segs) > 0 {
		var dparts [][]byte
		for _, s := range segs {
			expr := append([]byte{0x41}, sleb(int64(s.off))...)
			expr = append(expr, 0x0b)
			dparts = append(dparts, append(append([]byte{0x00}, expr...), append(uleb(uint32(len(s.data))), s.data...)...))
		}
		out = append(out, sect(11, vec(dparts...))...)
	}
	return out
}

// instr helpers
func i32c(n int32) []byte  { return append([]byte{0x41}, sleb(int64(n))...) }
func i64c(n int64) []byte  { return append([]byte{0x42}, sleb(n)...) }
func call(i uint32) []byte { return append([]byte{0x10}, uleb(i)...) }
func i32load(off uint32) []byte {
	return append(append([]byte{0x28}, uleb(2)...), uleb(off)...)
}

var (
	drop = []byte{0x1a}
	end  = []byte{0x0b}
	none = []byte{0x00} // empty locals vec
)

func body(locals []byte, instrs ...[]byte) []byte {
	out := append([]byte{}, locals...)
	for _, in := range instrs {
		out = append(out, in...)
	}
	return append(out, end...)
}

// host import types (shared by every fixture)
var (
	tVoidI32  = functype(nil, []byte{i32})                   // version/run
	tLog      = functype([]byte{i32, i32, i32}, nil)         // log
	tGetInput = functype(nil, []byte{i32, i32})              // get_input
	tSetOut   = functype([]byte{i32, i32}, nil)              // set_output
	tCfgGet   = functype([]byte{i32, i32}, []byte{i32, i32}) // config_get
	tEnvSizes = functype([]byte{i32, i32}, []byte{i32})      // environ_sizes_get
	tSockSend = functype([]byte{i32, i32, i32, i32, i32}, []byte{i32})
	tPathOpen = functype([]byte{i32, i32, i32, i32, i32, i64, i64, i32, i32}, []byte{i32})
)

// happyGuest echoes its input via get_input/set_output and logs once.
// ver tunes magpie_api_version (10000 = core 1.0.0).
func happyGuest(ver int32) []byte {
	types := [][]byte{tVoidI32, tLog, tGetInput, tSetOut}
	imps := []imp{
		{"magpie", "magpie_log", 0, 1},
		{"magpie", "magpie_get_input", 0, 2},
		{"magpie", "magpie_set_output", 0, 3},
	}
	run := body(none,
		i32c(1), i32c(0x2000), i32c(5), call(0), // log(1, "hello")
		call(1), call(2), // set_output(get_input())
		i32c(0),
	)
	verf := body(none, i32c(ver))
	return encodeModule(types, imps, []uint32{0, 0}, [][]byte{run, verf},
		[]exp{{"magpie_api_version", 0, 4}, {"run", 0, 3}},
		1, []seg{{0x2000, []byte("hello")}})
}

func newTestRunner(t *testing.T, wasmBytes []byte) *Runner {
	t.Helper()
	r, err := NewRunner(context.Background(), wasmBytes)
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	t.Cleanup(func() { _ = r.Close(context.Background()) }) //nolint:errcheck // test teardown; failure unactionable
	return r
}

func TestWASM_Happy(t *testing.T) {
	r := newTestRunner(t, happyGuest(10000))
	in := []byte(`{"title":"Widget"}`)
	out, err := r.Transform(context.Background(), in)
	if err != nil {
		t.Fatalf("Transform: %v", err)
	}
	if string(out) != string(in) {
		t.Errorf("output = %q, want input echo %q", out, in)
	}
	logs := r.LastLogs()
	if len(logs) != 1 || logs[0].Level != 1 || logs[0].Message != "hello" {
		t.Errorf("logs = %+v, want [{1 hello}]", logs)
	}
}

func TestWASM_VersionGate(t *testing.T) {
	_, err := NewRunner(context.Background(), happyGuest(2*10000))
	if err == nil {
		t.Fatal("NewRunner(major 2) = nil, want version reject")
	}
	if !strings.Contains(err.Error(), "version") {
		t.Errorf("error %q does not mention version", err)
	}
}

func TestWASM_NotAPlugin(t *testing.T) {
	types := [][]byte{tVoidI32, tSetOut}
	imps := []imp{{"magpie", "magpie_set_output", 0, 1}}
	run := body(none, i32c(0x1000), i32c(1), call(0), i32c(0))
	mod := encodeModule(types, imps, []uint32{0}, [][]byte{run},
		[]exp{{"run", 0, 1}}, 1, []seg{{0x1000, []byte("x")}})
	_, err := NewRunner(context.Background(), mod)
	if err == nil {
		t.Fatal("NewRunner(no version export) = nil, want reject")
	}
	if !strings.Contains(err.Error(), "not a magpie plugin") {
		t.Errorf("error %q does not say not a magpie plugin", err)
	}
}

func TestWASM_SockDeny(t *testing.T) {
	types := [][]byte{tVoidI32, tLog, tSetOut, tSockSend}
	imps := []imp{
		{"magpie", "magpie_log", 0, 1},
		{"magpie", "magpie_set_output", 0, 2},
		{"wasi_snapshot_preview1", "sock_send", 0, 3},
	}
	run := body(none,
		i32c(0x1000), i32c(1), call(1), // set_output("x") so run/0 succeeds
		i32c(99), i32c(0), i32c(0), i32c(0), i32c(0x3000), call(2), // sock_send(99,...) -> errno
		i32c(0), i32c(0), call(0), // log(errno, "", "")
		i32c(0),
	)
	ver := body(none, i32c(10000))
	mod := encodeModule(types, imps, []uint32{0, 0}, [][]byte{run, ver},
		[]exp{{"magpie_api_version", 0, 4}, {"run", 0, 3}},
		1, []seg{{0x1000, []byte("x")}})
	r := newTestRunner(t, mod)
	if _, err := r.Transform(context.Background(), []byte("{}")); err != nil {
		t.Fatalf("Transform: %v", err)
	}
	var errno uint32
	found := false
	for _, l := range r.LastLogs() {
		if l.Message == "" {
			errno, found = l.Level, true
		}
	}
	if !found {
		t.Fatalf("no errno log captured: %+v", r.LastLogs())
	}
	if errno == 0 {
		t.Error("sock_send(99) returned errno 0, want nonzero (no socket FDs preopened)")
	}
}

func TestWASM_EnvDeny(t *testing.T) {
	types := [][]byte{tVoidI32, tLog, tSetOut, tEnvSizes}
	imps := []imp{
		{"magpie", "magpie_log", 0, 1},
		{"magpie", "magpie_set_output", 0, 2},
		{"wasi_snapshot_preview1", "environ_sizes_get", 0, 3},
	}
	run := body(none,
		i32c(0x1000), i32c(1), call(1),
		i32c(0x3000), i32c(0x3004), call(2), drop, // environ_sizes_get
		i32c(0x3000), i32load(0), i32c(0), i32c(0), call(0), // log(count)
		i32c(0x3004), i32load(0), i32c(0), i32c(0), call(0), // log(size)
		i32c(0),
	)
	ver := body(none, i32c(10000))
	mod := encodeModule(types, imps, []uint32{0, 0}, [][]byte{run, ver},
		[]exp{{"magpie_api_version", 0, 4}, {"run", 0, 3}},
		1, []seg{{0x1000, []byte("x")}})
	r := newTestRunner(t, mod)
	if _, err := r.Transform(context.Background(), []byte("{}")); err != nil {
		t.Fatalf("Transform: %v", err)
	}
	var levels []uint32
	for _, l := range r.LastLogs() {
		if l.Message == "" {
			levels = append(levels, l.Level)
		}
	}
	if len(levels) != 2 || levels[0] != 0 || levels[1] != 0 {
		t.Errorf("environ (count,size) = %v, want [0 0]", levels)
	}
}

func TestWASM_FSDeny(t *testing.T) {
	dir := t.TempDir()
	victim := filepath.Join(dir, "victim.txt")
	pathBytes := []byte(victim)
	types := [][]byte{tVoidI32, tLog, tSetOut, tPathOpen}
	imps := []imp{
		{"magpie", "magpie_log", 0, 1},
		{"magpie", "magpie_set_output", 0, 2},
		{"wasi_snapshot_preview1", "path_open", 0, 3},
	}
	run := body(none,
		i32c(0x2000), i32c(1), call(1),
		i32c(3), i32c(0), i32c(0x1000), i32c(int32(len(pathBytes))),
		i32c(0), i64c(0), i64c(0), i32c(0), i32c(0x3000), call(2), // path_open -> errno
		i32c(0), i32c(0), call(0), // log(errno)
		i32c(0),
	)
	ver := body(none, i32c(10000))
	mod := encodeModule(types, imps, []uint32{0, 0}, [][]byte{run, ver},
		[]exp{{"magpie_api_version", 0, 4}, {"run", 0, 3}},
		1, []seg{{0x1000, pathBytes}, {0x2000, []byte("x")}})
	r := newTestRunner(t, mod)
	if _, err := r.Transform(context.Background(), []byte("{}")); err != nil {
		t.Fatalf("Transform: %v", err)
	}
	var errno uint32
	found := false
	for _, l := range r.LastLogs() {
		if l.Message == "" {
			errno, found = l.Level, true
		}
	}
	if !found {
		t.Fatalf("no errno log captured: %+v", r.LastLogs())
	}
	if errno == 0 {
		t.Error("path_open returned errno 0, want nonzero (zero preopens)")
	}
	if _, err := os.Stat(victim); !os.IsNotExist(err) {
		t.Errorf("victim file exists or stat odd: %v (guest must not create files)", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read temp dir: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("temp dir has %d stray files, want 0", len(entries))
	}
}

func TestWASM_CanceledCtx(t *testing.T) {
	r := newTestRunner(t, happyGuest(10000))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan error, 1)
	go func() { _, err := r.Transform(ctx, []byte("{}")); done <- err }()
	select {
	case err := <-done:
		if err == nil {
			t.Error("Transform(canceled ctx) = nil, want error")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Transform(canceled ctx) hung")
	}
}

func TestWASM_ConfigGet(t *testing.T) {
	types := [][]byte{tVoidI32, tCfgGet, tSetOut}
	imps := []imp{
		{"magpie", "magpie_config_get", 0, 1},
		{"magpie", "magpie_set_output", 0, 2},
	}
	run := body(none,
		i32c(0x1000), i32c(3), call(0), // config_get("key") -> (ptr,len)
		call(1), // set_output(ptr,len)
		i32c(0),
	)
	ver := body(none, i32c(10000))
	mod := encodeModule(types, imps, []uint32{0, 0}, [][]byte{run, ver},
		[]exp{{"magpie_api_version", 0, 3}, {"run", 0, 2}},
		1, []seg{{0x1000, []byte("key")}})
	r, err := newRunnerWithConfig(context.Background(), mod, map[string]string{"key": "val"}, 0, 0)
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	t.Cleanup(func() { _ = r.Close(context.Background()) }) //nolint:errcheck // test teardown; failure unactionable
	out, err := r.Transform(context.Background(), []byte("{}"))
	if err != nil {
		t.Fatalf("Transform: %v", err)
	}
	if string(out) != "val" {
		t.Errorf("output = %q, want val", out)
	}
}
