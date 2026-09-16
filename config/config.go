package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/zalando/go-keyring"
	"gopkg.in/yaml.v3"
)

const keyringService = "gomagpie"

// Config holds resolved CLI configuration.
// Precedence: flags > env (GOMAGPIE_) > file > defaults.
type Config struct {
	ExtractProvider string  `yaml:"extract_provider" json:"extract_provider"`
	Model           string  `yaml:"model" json:"model"`
	Render          string  `yaml:"render" json:"render"`
	Format          string  `yaml:"format" json:"format"`
	Out             string  `yaml:"out" json:"out"`
	Schema          string  `yaml:"schema" json:"schema"`
	CacheDB         string  `yaml:"cache_db" json:"cache_db"`
	MaxCost         float64 `yaml:"max_cost" json:"max_cost"`
	NoCache         bool    `yaml:"no_cache" json:"no_cache"`

	// flag-provided API key (never persisted, never logged)
	APIKeyFlag string `yaml:"-" json:"-"`
	// file path actually loaded (for diagnostics)
	filePath string
}

// Defaults per provider.
func DefaultConfig() Config {
	return Config{
		ExtractProvider: "anthropic",
		Render:          "auto",
		Format:          "json",
		CacheDB:         DefaultDBPath(),
	}
}

func DefaultConfigDir() string {
	if v := os.Getenv("XDG_CONFIG_HOME"); v != "" {
		return filepath.Join(v, "gomagpie")
	}
	if h, err := os.UserHomeDir(); err == nil {
		return filepath.Join(h, ".config", "gomagpie")
	}
	return "."
}

func DefaultConfigPath() string {
	if appdata := os.Getenv("APPDATA"); appdata != "" {
		return filepath.Join(appdata, "gomagpie", "config.yaml")
	}
	return filepath.Join(DefaultConfigDir(), "config.yaml")
}

func DefaultDBPath() string {
	if v := os.Getenv("XDG_CACHE_HOME"); v != "" {
		return filepath.Join(v, "gomagpie", "cache.db")
	}
	if h, err := os.UserHomeDir(); err == nil {
		return filepath.Join(h, ".cache", "gomagpie", "cache.db")
	}
	return filepath.Join(".", "cache.db")
}

func DefaultModel(provider string) string {
	switch strings.ToLower(provider) {
	case "openai":
		return "gpt-4o-mini"
	case "ollama":
		return "llama3.1"
	default:
		return "claude-sonnet-5"
	}
}

// Load reads file (if present) then overlays GOMAGPIE_ env vars.
func Load(path string) (Config, error) {
	cfg := DefaultConfig()
	if path == "" {
		path = DefaultConfigPath()
	}
	cfg.filePath = path
	if data, err := os.ReadFile(path); err == nil {
		// Warn when the file holds a key but is group/world-readable.
		if fi, serr := os.Stat(path); serr == nil && fi.Mode().Perm()&0o077 != 0 && strings.Contains(string(data), "api_key") {
			fmt.Fprintln(os.Stderr, "warning: config file holds a key and is readable by others; chmod 0600")
		}
		var fileCfg Config
		if err := yaml.Unmarshal(data, &fileCfg); err != nil {
			return cfg, fmt.Errorf("config: parse %s: %w", path, err)
		}
		cfg.overlay(fileCfg)
	} else if !os.IsNotExist(err) {
		return cfg, fmt.Errorf("config: read %s: %w", path, err)
	}
	cfg.overlayEnv()
	if cfg.Model == "" {
		cfg.Model = DefaultModel(cfg.ExtractProvider)
	}
	return cfg, nil
}

// overlay copies non-zero file values over c.
func (c *Config) overlay(o Config) {
	if o.ExtractProvider != "" {
		c.ExtractProvider = o.ExtractProvider
	}
	if o.Model != "" {
		c.Model = o.Model
	}
	if o.Render != "" {
		c.Render = o.Render
	}
	if o.Format != "" {
		c.Format = o.Format
	}
	if o.Out != "" {
		c.Out = o.Out
	}
	if o.Schema != "" {
		c.Schema = o.Schema
	}
	if o.CacheDB != "" {
		c.CacheDB = o.CacheDB
	}
	if o.MaxCost != 0 {
		c.MaxCost = o.MaxCost
	}
	if o.NoCache {
		c.NoCache = o.NoCache
	}
}

func (c *Config) overlayEnv() {
	if v := os.Getenv("GOMAGPIE_EXTRACT_PROVIDER"); v != "" {
		c.ExtractProvider = v
	}
	if v := os.Getenv("GOMAGPIE_PROVIDER"); v != "" {
		c.ExtractProvider = v
	}
	if v := os.Getenv("GOMAGPIE_MODEL"); v != "" {
		c.Model = v
	}
	if v := os.Getenv("GOMAGPIE_RENDER"); v != "" {
		c.Render = v
	}
	if v := os.Getenv("GOMAGPIE_FORMAT"); v != "" {
		c.Format = v
	}
	if v := os.Getenv("GOMAGPIE_OUT"); v != "" {
		c.Out = v
	}
	if v := os.Getenv("GOMAGPIE_SCHEMA"); v != "" {
		c.Schema = v
	}
	if v := os.Getenv("GOMAGPIE_CACHE_DB"); v != "" {
		c.CacheDB = v
	}
	if v := os.Getenv("GOMAGPIE_MAX_COST"); v != "" {
		var f float64
		if _, err := fmt.Sscanf(v, "%g", &f); err != nil {
			fmt.Fprintf(os.Stderr, "warning: ignoring invalid GOMAGPIE_MAX_COST %q\n", v)
		} else {
			c.MaxCost = f
		}
	}
}

// ApplyFlags overlays explicit cobra flag values (only when changed).
func (c *Config) ApplyFlags(f Flags) {
	if f.ProviderChanged {
		c.ExtractProvider = f.Provider
	}
	if f.ModelChanged {
		c.Model = f.Model
	}
	if f.RenderChanged {
		c.Render = f.Render
	}
	if f.FormatChanged {
		c.Format = f.Format
	}
	if f.OutChanged {
		c.Out = f.Out
	}
	if f.SchemaChanged {
		c.Schema = f.Schema
	}
	if f.CacheDBChanged {
		c.CacheDB = f.CacheDB
	}
	if f.MaxCostChanged {
		c.MaxCost = f.MaxCost
	}
	if f.NoCacheChanged {
		c.NoCache = f.NoCache
	}
	if f.APIKeyChanged {
		c.APIKeyFlag = f.APIKey
	}
	if c.Model == "" {
		c.Model = DefaultModel(c.ExtractProvider)
	}
}

// Flags mirrors the CLI surface so config stays cobra-free.
type Flags struct {
	Provider        string
	Model           string
	Render          string
	Format          string
	Out             string
	Schema          string
	CacheDB         string
	MaxCost         float64
	NoCache         bool
	APIKey          string
	ProviderChanged bool
	ModelChanged    bool
	RenderChanged   bool
	FormatChanged   bool
	OutChanged      bool
	SchemaChanged   bool
	CacheDBChanged  bool
	MaxCostChanged  bool
	NoCacheChanged  bool
	APIKeyChanged   bool
}

// APIKey resolves flag > env > keyring > (no file keys; returns "" when absent).
func (c Config) APIKey(provider string) string {
	if c.APIKeyFlag != "" {
		return c.APIKeyFlag
	}
	p := strings.ToUpper(strings.ReplaceAll(provider, "-", "_"))
	for _, name := range []string{"GOMAGPIE_" + p + "_API_KEY", "GOMAGPIE_API_KEY"} {
		if v := os.Getenv(name); v != "" {
			return v
		}
	}
	if k, err := keyring.Get(keyringService, strings.ToLower(provider)); err == nil && k != "" {
		return k
	}
	return ""
}

// SetKey stores provider key in the OS keyring.
func SetKey(provider, key string) error {
	if err := keyring.Set(keyringService, strings.ToLower(provider), key); err != nil {
		return fmt.Errorf("no secret service; export GOMAGPIE_%s_API_KEY instead: %w",
			strings.ToUpper(provider), err)
	}
	return nil
}

// Redacted returns a copy safe for `config show`.
func (c Config) Redacted() map[string]any {
	key := "***redacted***"
	if c.APIKey(c.ExtractProvider) == "" {
		key = "(not set)"
	}
	return map[string]any{
		"extract_provider": c.ExtractProvider,
		"model":            c.Model,
		"render":           c.Render,
		"format":           c.Format,
		"cache_db":         c.CacheDB,
		"max_cost":         c.MaxCost,
		"api_key":          key,
		"config_file":      c.filePath,
	}
}
