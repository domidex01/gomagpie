package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/motherlodelab/magpie/config"
)

func isolatedXDG(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("XDG_CACHE_HOME", dir)
	t.Setenv("APPDATA", "")
	return dir
}

func TestPrecedence(t *testing.T) {
	dir := isolatedXDG(t)
	cfgPath := filepath.Join(dir, "magpie", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfgPath, []byte("extract_provider: openai\nmodel: file-model\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MAGPIE_EXTRACT_PROVIDER", "ollama")
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ExtractProvider != "ollama" {
		t.Errorf("env should beat file: %q", cfg.ExtractProvider)
	}
	if cfg.Model != "file-model" {
		t.Errorf("file value lost: %q", cfg.Model)
	}
	cfg.ApplyFlags(config.Flags{Provider: "anthropic", ProviderChanged: true})
	if cfg.ExtractProvider != "anthropic" {
		t.Errorf("flag should beat env: %q", cfg.ExtractProvider)
	}
	def, err := config.Load(filepath.Join(dir, "nonexistent.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if def.ExtractProvider != "ollama" {
		t.Errorf("defaults+env: %q", def.ExtractProvider)
	}
}

func TestKeyLookupOrder(t *testing.T) {
	isolatedXDG(t)
	cfg := config.DefaultConfig()
	t.Setenv("MAGPIE_ANTHROPIC_API_KEY", "env-key")
	if k := cfg.APIKey("anthropic"); k != "env-key" {
		t.Errorf("env key: %q", k)
	}
	cfg.APIKeyFlag = "flag-key"
	if k := cfg.APIKey("anthropic"); k != "flag-key" {
		t.Errorf("flag key: %q", k)
	}
}

func TestKeyEnvDashToUnderscore(t *testing.T) {
	isolatedXDG(t)
	cfg := config.DefaultConfig()
	t.Setenv("MAGPIE_OPENCODE_GO_API_KEY", "env-key")
	if k := cfg.APIKey("opencode-go"); k != "env-key" {
		t.Errorf("APIKey(opencode-go) = %q, want dash-to-underscore env hit", k)
	}
}

func TestDefaultModels(t *testing.T) {
	for provider, want := range map[string]string{
		"anthropic":    "claude-sonnet-5",
		"openai":       "gpt-4o-mini",
		"ollama":       "llama3.1",
		"openrouter":   "openai/gpt-4o-mini",
		"codex":        "gpt-5.2",
		"opencode-go":  "glm-5.3",
		"opencode-zen": "claude-sonnet-4-6",
	} {
		if got := config.DefaultModel(provider); got != want {
			t.Errorf("DefaultModel(%q) = %q, want %q", provider, got, want)
		}
	}
}

func TestShowRedacts(t *testing.T) {
	isolatedXDG(t)
	t.Setenv("MAGPIE_ANTHROPIC_API_KEY", "super-secret")
	cfg, err := config.Load(filepath.Join(t.TempDir(), "nope.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	m := cfg.Redacted()
	if m["api_key"] != "***redacted***" {
		t.Errorf("api_key = %v", m["api_key"])
	}
	for _, v := range m {
		if s, ok := v.(string); ok && strings.Contains(s, "super-secret") {
			t.Error("raw key leaked in redacted output")
		}
	}
}

func TestServeKeys_OverlayChain(t *testing.T) {
	dir := isolatedXDG(t)
	cfgPath := filepath.Join(dir, "magpie", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o755); err != nil {
		t.Fatal(err)
	}
	// Defaults.
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ServeTransport != "stdio" {
		t.Errorf("default serve_transport = %q, want stdio", cfg.ServeTransport)
	}
	if cfg.ExporterCmd != "" {
		t.Errorf("default exporter_cmd = %q, want empty", cfg.ExporterCmd)
	}
	// File sets serve_addr.
	if err := os.WriteFile(cfgPath, []byte("serve_addr: 127.0.0.1:9999\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err = config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ServeAddr != "127.0.0.1:9999" {
		t.Errorf("file serve_addr = %q, want 127.0.0.1:9999", cfg.ServeAddr)
	}
	// Env beats file.
	t.Setenv("MAGPIE_SERVE_TRANSPORT", "http")
	t.Setenv("MAGPIE_EXPORTER_CMD", "myprog --fast")
	cfg, err = config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ServeTransport != "http" {
		t.Errorf("env serve_transport = %q, want http", cfg.ServeTransport)
	}
	if cfg.ExporterCmd != "myprog --fast" {
		t.Errorf("env exporter_cmd = %q", cfg.ExporterCmd)
	}
	// Flags beat env.
	cfg.ApplyFlags(config.Flags{
		ServeTransport: "stdio", ServeTransportChanged: true,
		ServeAddr: "127.0.0.1:1111", ServeAddrChanged: true,
		ExporterCmd: "other", ExporterCmdChanged: true,
	})
	if cfg.ServeTransport != "stdio" || cfg.ServeAddr != "127.0.0.1:1111" || cfg.ExporterCmd != "other" {
		t.Errorf("flag overlay = %+v, want stdio/127.0.0.1:1111/other", cfg)
	}
}

func TestConfigShow_HasPhase3Keys(t *testing.T) {
	isolatedXDG(t)
	cfg, err := config.Load(filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "magpie", "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	red := cfg.Redacted()
	for _, k := range []string{"serve_transport", "serve_addr", "exporter_cmd"} {
		if _, ok := red[k]; !ok {
			t.Errorf("Redacted() missing %q", k)
		}
	}
}
