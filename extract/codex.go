package extract

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// CodexExecAdapter extracts via the user's own Codex CLI (`codex exec`).
// Exec-only by design: gomagpie never touches subscription tokens — auth
// stays between the user and their CLI login. Requires a current CLI with
// --output-schema (native strict schema) and --json usage events.
type CodexExecAdapter struct {
	Model string
	Log   func(purpose string, usage TokenUsage)
	// LookPath locates the CLI (default exec.LookPath; tests stub via PATH).
	LookPath func(string) (string, error)
	// CodexBin overrides the binary path outright.
	CodexBin string
	schema   *Schema
}

func NewCodexExec(model string, sch *Schema, log func(string, TokenUsage)) *CodexExecAdapter {
	return &CodexExecAdapter{Model: model, Log: log, schema: sch}
}

func (c *CodexExecAdapter) Name() string { return "codex" }

func (c *CodexExecAdapter) lookPath() func(string) (string, error) {
	if c.LookPath != nil {
		return c.LookPath
	}
	return exec.LookPath
}

func (c *CodexExecAdapter) bin() (string, error) {
	if c.CodexBin != "" {
		return c.CodexBin, nil
	}
	bin, err := c.lookPath()("codex")
	if err != nil {
		return "", fmt.Errorf("extract: codex CLI not found on PATH; install it and run `codex login`: %w", err)
	}
	return bin, nil
}

// Preflight fails before any prompt bytes move: missing CLI → login hint,
// old CLI without --output-schema → upgrade hint. Callers (newExtractor)
// run this before any spend; Extract itself does not re-preflight.
func (c *CodexExecAdapter) Preflight() error {
	bin, err := c.bin()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if out, err := exec.CommandContext(ctx, bin, "--version").CombinedOutput(); err != nil {
		return fmt.Errorf("extract: codex --version failed (reinstall the CLI): %w: %s", err, truncate(out, 200))
	}
	out, err := exec.CommandContext(ctx, bin, "--help").CombinedOutput()
	if err != nil {
		return fmt.Errorf("extract: codex --help failed (reinstall the CLI): %w: %s", err, truncate(out, 200))
	}
	if !strings.Contains(string(out), "output-schema") {
		return fmt.Errorf("extract: codex CLI too old: --output-schema unsupported; upgrade codex and retry")
	}
	return nil
}

func (c *CodexExecAdapter) Extract(ctx context.Context, in ExtractInput) (ExtractResult, error) {
	if in.Schema == nil {
		in.Schema = c.schema
	}
	if in.Schema == nil {
		return ExtractResult{}, fmt.Errorf("extract: nil schema")
	}
	doc, err := schemaDoc(in.Schema.Raw)
	if err != nil {
		return ExtractResult{}, err
	}
	call := func(ctx context.Context, system, user string) (string, TokenUsage, error) {
		return c.runOnce(ctx, system, user, doc)
	}
	// The CLI enforces shape via --output-schema; gomagpie still verifies
	// through the normal validate+coerce repair loop.
	return runRepairLoop(ctx, call, c.Log, in, "codex", c.Model)
}

func (c *CodexExecAdapter) runOnce(ctx context.Context, system, user string, doc any) (string, TokenUsage, error) {
	bin, err := c.bin()
	if err != nil {
		return "", TokenUsage{}, err
	}
	dir, err := os.MkdirTemp("", "magpie-codex-")
	if err != nil {
		return "", TokenUsage{}, fmt.Errorf("extract: codex temp dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }() //nolint:errcheck // best-effort temp cleanup
	schemaRaw, err := json.Marshal(doc)
	if err != nil {
		return "", TokenUsage{}, fmt.Errorf("extract: marshal schema: %w", err)
	}
	schemaFile := filepath.Join(dir, "schema.json")
	if err := os.WriteFile(schemaFile, schemaRaw, 0o600); err != nil {
		return "", TokenUsage{}, fmt.Errorf("extract: write schema: %w", err)
	}
	outFile := filepath.Join(dir, "final.json")
	prompt := system + "\n\n" + user
	if err := os.WriteFile(filepath.Join(dir, "prompt.txt"), []byte(prompt), 0o600); err != nil {
		return "", TokenUsage{}, fmt.Errorf("extract: write prompt: %w", err)
	}
	// --ephemeral scopes the cumulative turn.completed usage to this call;
	// --ignore-user-config keeps MCP servers/tools in the user config from
	// silently dropping the strict schema (codex issue #15451).
	cmd := exec.CommandContext(ctx, bin, "exec",
		"--json", "--output-schema", schemaFile,
		"--ephemeral", "--skip-git-repo-check", "--ignore-user-config",
		"-o", outFile, "-m", c.Model, "-")
	cmd.Stdin = strings.NewReader(prompt)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return "", TokenUsage{}, fmt.Errorf("extract: codex exec: %w: %s", err, truncate(stderr.Bytes(), 500))
	}
	raw, err := os.ReadFile(outFile)
	if err != nil {
		return "", TokenUsage{}, fmt.Errorf("extract: codex: read output file: %w", err)
	}
	// Usage is accounting metadata: missing turn.completed degrades to zero
	// usage, never fails the record.
	return string(raw), parseCodexUsage(stdout.Bytes()), nil
}

// parseCodexUsage mines usage from --json event lines, taking the last
// turn.completed (cumulative per ephemeral session == this call).
func parseCodexUsage(stream []byte) TokenUsage {
	var u TokenUsage
	for _, line := range bytes.Split(stream, []byte("\n")) {
		var ev struct {
			Type  string `json:"type"`
			Usage struct {
				InputTokens  int `json:"input_tokens"`
				OutputTokens int `json:"output_tokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal(line, &ev); err != nil {
			continue
		}
		if ev.Type == "turn.completed" {
			u = TokenUsage{PromptTokens: ev.Usage.InputTokens, CompletionTokens: ev.Usage.OutputTokens}
		}
	}
	return u
}
