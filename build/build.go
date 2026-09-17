// Package build compiles custom static magpie binaries with extra
// compile-time modules: `magpie build --with module@version --output file`.
package build

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/template"
)

// BuildOptions configures one custom build.
type BuildOptions struct {
	With   []string // module@version entries (each must contain "@")
	Output string   // required output binary path
	// Replaces adds extra module→path replacements (tests use it to point
	// at fixture modules without network; no CLI surface).
	Replaces map[string]string
}

// renderMain returns the generated main.go for with-imports. Golden-tested.
func renderMain(with []string) (string, error) {
	// text/template drops the os import when unused — keep it simple and
	// explicit instead: the import is always needed (os.Exit below).
	const src = `package main

import (
{{range .}}	_ "{{.}}"
{{end}}	"gomagpie/cli"
	"os"
)

func main() {
	os.Exit(cli.Execute())
}
`
	t := template.Must(template.New("main").Parse(src))
	var sb strings.Builder
	if err := t.Execute(&sb, with); err != nil {
		return "", err
	}
	return sb.String(), nil
}

// Build codegens a temp module (replace gomagpie => repo root, blank
// --with imports, cli.Execute()) and runs go build there.
func Build(ctx context.Context, o BuildOptions) error {
	if o.Output == "" {
		return fmt.Errorf("build: --output is required")
	}
	for _, w := range o.With {
		if !strings.Contains(w, "@") {
			return fmt.Errorf("build: --with %q must be module@version (bare paths silently resolve to latest)", w)
		}
	}
	root, err := gomagpieRoot(ctx)
	if err != nil {
		return err
	}
	dir, err := os.MkdirTemp("", "magpie-build-*")
	if err != nil {
		return fmt.Errorf("build: temp dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }() //nolint:errcheck // temp cleanup; failure unactionable

	run := func(name string, args ...string) error {
		cmd := exec.CommandContext(ctx, name, args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("build: %s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
		}
		return nil
	}

	if err := run("go", "mod", "init", "magpie-custom"); err != nil {
		return err
	}
	if err := run("go", "mod", "edit", "-require=gomagpie@v0.0.0", "-replace=gomagpie="+root); err != nil {
		return err
	}
	for mod, path := range o.Replaces {
		if err := run("go", "mod", "edit", "-replace="+mod+"="+path); err != nil {
			return err
		}
	}
	// Share the full transitive closure so hermetic (GOPROXY=off) builds can
	// resolve hashes from the warm module cache alone.
	if sum, err := os.ReadFile(filepath.Join(root, "go.sum")); err == nil {
		_ = os.WriteFile(filepath.Join(dir, "go.sum"), sum, 0o600) //nolint:errcheck // build fails loudly below without it
	}
	withMods := make([]string, 0, len(o.With))
	for _, w := range o.With {
		withMods = append(withMods, strings.SplitN(w, "@", 2)[0])
	}
	// main.go must exist BEFORE go get: with only a go.mod present, go get
	// records the module as an indirect requirement and the final go build
	// then fails with "updates to go.mod needed".
	src, err := renderMain(withMods)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(src), 0o600); err != nil {
		return fmt.Errorf("build: write main.go: %w", err)
	}
	for _, w := range o.With {
		// NOTE: the only network path in the default suite's neighborhood —
		// remote --with is manual smoke only, never a Go test.
		if err := run("go", "get", w); err != nil {
			return err
		}
	}
	if len(o.With) > 0 {
		// go get records new modules as // indirect regardless of main.go's
		// imports; go build refuses that state ("updates to go.mod needed").
		// tidy reclassifies them to direct and makes the build deterministic.
		if err := run("go", "mod", "tidy"); err != nil {
			return err
		}
	}
	absOut, err := filepath.Abs(o.Output)
	if err != nil {
		return fmt.Errorf("build: output path: %w", err)
	}
	if err := run("go", "build", "-o", absOut, "."); err != nil {
		return err
	}
	return nil
}

// gomagpieRoot resolves the gomagpie source dir for the replace directive:
// `go list -m` on the main module first, GOMAGPIE_REPO_ROOT override otherwise.
func gomagpieRoot(ctx context.Context) (string, error) {
	if v := os.Getenv("GOMAGPIE_REPO_ROOT"); v != "" {
		return v, nil
	}
	cmd := exec.CommandContext(ctx, "go", "list", "-m", "-f", "{{.Dir}}", "gomagpie")
	if out, err := cmd.Output(); err == nil {
		if dir := strings.TrimSpace(string(out)); dir != "" {
			return dir, nil
		}
	}
	return "", fmt.Errorf("build: cannot locate gomagpie source (set GOMAGPIE_REPO_ROOT)")
}
