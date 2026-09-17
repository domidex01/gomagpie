// Package core hosts the module registry: Caddy-style compile-time
// registration for third-party modules pulled in via `magpie build --with`.
//
// The registry is deliberately kind-agnostic: it stores ModuleInfo values
// with a Kind string, not Go interfaces. The spec §6 sketch lists seven
// stage interfaces (Fetcher, Cleaner, ...), but core already imports
// fetch+clean for the pipeline while every stage package would need to
// import core for init() self-registration — a direct import cycle.
// Resolving that "properly" (DTO types in core + adapters everywhere) is
// scaffolding for consumers that don't exist: exactly one implementation
// per kind ships. Kind-specific Go interfaces live at their single use
// site (plugin/exec.Exporter, plugin/wasm.Runner); promote one into core
// when a second consumer per kind appears (rule of three).
package core

import (
	"fmt"
	"sort"
	"strings"
	"sync"
)

// CoreAPIVersion is the module API version third-party modules register
// against. RegisterModule and the WASM version gate both compare majors
// against this value.
const CoreAPIVersion = "1.0.0"

// Module kinds.
const (
	KindFetcher   = "fetcher"
	KindCleaner   = "cleaner"
	KindExtractor = "extractor"
	KindExporter  = "exporter"
	KindNotifier  = "notifier"
	KindProxy     = "proxy"
	KindRateLimit = "ratelimit"
)

// ModuleInfo describes one compile-time module. Third-party modules call
// MustRegister from init().
type ModuleInfo struct {
	ID         string
	APIVersion string
	Kind       string
	New        func() any
}

var (
	regMu sync.Mutex
	reg   = map[string]ModuleInfo{}
)

// RegisterModule records info, rejecting empty IDs, empty/unparsable
// versions, duplicate IDs, and major-version skew loudly.
func RegisterModule(info ModuleInfo) error {
	if info.ID == "" {
		return fmt.Errorf("core: module with empty ID")
	}
	if info.APIVersion == "" {
		return fmt.Errorf("core: module %q with empty version", info.ID)
	}
	maj, _, _, err := parseSemver(info.APIVersion)
	if err != nil {
		return fmt.Errorf("core: module %q: %w", info.ID, err)
	}
	coreMaj, _, _, err := parseSemver(CoreAPIVersion)
	if err != nil {
		return fmt.Errorf("core: bad CoreAPIVersion %q: %w", CoreAPIVersion, err)
	}
	if maj != coreMaj {
		return fmt.Errorf("core: module %q version %s incompatible with core %s (major mismatch)", info.ID, info.APIVersion, CoreAPIVersion)
	}
	regMu.Lock()
	defer regMu.Unlock()
	if _, dup := reg[info.ID]; dup {
		return fmt.Errorf("core: duplicate module %q", info.ID)
	}
	reg[info.ID] = info
	return nil
}

// MustRegister panics on error: init-time misuse must crash, never silently skip.
func MustRegister(info ModuleInfo) {
	if err := RegisterModule(info); err != nil {
		panic(err)
	}
}

// Lookup returns the module registered under id.
func Lookup(id string) (ModuleInfo, bool) {
	regMu.Lock()
	defer regMu.Unlock()
	m, ok := reg[id]
	return m, ok
}

// ListModules returns registered modules sorted by ID. The slice is a fresh
// copy; mutating it does not affect the registry.
func ListModules() []ModuleInfo {
	regMu.Lock()
	defer regMu.Unlock()
	out := make([]ModuleInfo, 0, len(reg))
	for _, m := range reg {
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// parseSemver parses "major.minor.patch" with a hand-rolled SplitN — no dep
// for three integers.
func parseSemver(v string) (major, minor, patch string, err error) {
	parts := strings.SplitN(v, ".", 3)
	if len(parts) != 3 {
		return "", "", "", fmt.Errorf("unparsable semver %q (want major.minor.patch)", v)
	}
	for _, p := range parts {
		if p == "" {
			return "", "", "", fmt.Errorf("unparsable semver %q (empty component)", v)
		}
		for _, r := range p {
			if r < '0' || r > '9' {
				return "", "", "", fmt.Errorf("unparsable semver %q (non-numeric component)", v)
			}
		}
	}
	return parts[0], parts[1], parts[2], nil
}
