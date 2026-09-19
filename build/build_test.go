package build

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuild_MainGolden_Zero(t *testing.T) {
	got, err := renderMain(nil)
	if err != nil {
		t.Fatalf("renderMain: %v", err)
	}
	want := "package main\n\nimport (\n\t\"github.com/domidex01/magpie/cli\"\n\t\"os\"\n)\n\nfunc main() {\n\tos.Exit(cli.Execute())\n}\n"
	if got != want {
		t.Errorf("zero---with main mismatch:\n got %q\nwant %q", got, want)
	}
}

func TestBuild_MainGolden_Two(t *testing.T) {
	got, err := renderMain([]string{"example.com/modA", "example.com/modB"})
	if err != nil {
		t.Fatalf("renderMain: %v", err)
	}
	want := "package main\n\nimport (\n\t_ \"example.com/modA\"\n\t_ \"example.com/modB\"\n\t\"github.com/domidex01/magpie/cli\"\n\t\"os\"\n)\n\nfunc main() {\n\tos.Exit(cli.Execute())\n}\n"
	if got != want {
		t.Errorf("two---with main mismatch:\n got %q\nwant %q", got, want)
	}
}

// hermeticEnv isolates builds: no network, warm module cache only, private
// build cache. Fail loud on cold cache — never skip (a skip would let the
// flagship reproducibility proof pass vacuously).
func hermeticEnv(t *testing.T) {
	t.Helper()
	t.Setenv("GOPROXY", "off")
	t.Setenv("GOFLAGS", "-mod=mod")
	t.Setenv("GOSUMDB", "off")
	t.Setenv("GOCACHE", t.TempDir())
}

func runBinHelp(t *testing.T, bin string, extraEnv ...string) {
	t.Helper()
	cmd := exec.Command(bin, "--help")
	cmd.Env = append(os.Environ(), extraEnv...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s --help: %v: %s", bin, err, out)
	}
}

func TestBuild_Hermetic(t *testing.T) {
	hermeticEnv(t)
	bin := filepath.Join(t.TempDir(), "magpie-test")
	if err := Build(context.Background(), BuildOptions{Output: bin}); err != nil {
		t.Fatalf("Build: %v", err)
	}
	runBinHelp(t, bin)
}

func TestBuild_FixtureMarker(t *testing.T) {
	hermeticEnv(t)
	dir := t.TempDir()
	bin := filepath.Join(dir, "magpie-custom")
	modDir, err := filepath.Abs(filepath.Join("..", "testdata", "buildmod"))
	if err != nil {
		t.Fatalf("abs buildmod: %v", err)
	}
	err = Build(context.Background(), BuildOptions{
		With:     []string{"buildmod@v0.0.0"},
		Output:   bin,
		Replaces: map[string]string{"buildmod": modDir},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	marker := filepath.Join(dir, "marker")
	runBinHelp(t, bin, "MAGPIE_BUILD_TEST_MARKER="+marker)
	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("marker file missing (init did not run): %v", err)
	}
	if len(strings.Split(strings.TrimSpace(string(data)), "\n")) < 1 || len(data) == 0 {
		t.Error("marker file empty, want >= 1 line")
	}
}

func TestBuild_BareWithRejected(t *testing.T) {
	hermeticEnv(t)
	bin := filepath.Join(t.TempDir(), "magpie-test")
	err := Build(context.Background(), BuildOptions{
		With:   []string{"./localmod"},
		Output: bin,
	})
	if err == nil {
		t.Fatal("Build(bare --with) = nil, want loud error")
	}
	if !strings.Contains(err.Error(), "@") {
		t.Errorf("error %q does not mention @", err)
	}
}
