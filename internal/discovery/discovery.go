// Package discovery populates model variants dynamically, once at startup.
// Forwards ask their upstream's /models endpoint — the one real dynamic
// source. CLI agents (devin/codex/copilot) expose no list command, so they
// get a curated known-models table; explicit `variants:` in config always
// wins over both.
package discovery

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/muthuishere/routsi/internal/config"
)

// defaultKnownCLIModels is best-effort: codex/copilot/claude have no
// model-list command, and valid names vary by account/plan. Users edit
// ~/.config/routsi/known-models.json (written with these defaults
// on first run) — or override per model with `variants:`. Devin is discovered
// live via the probe; its entry here is only the probe-failure fallback.
var defaultKnownCLIModels = map[config.ModelType][]string{
	config.TypeDevin:   {"sonnet", "opus"},
	config.TypeCodex:   {"gpt-5.6-sol"},
	config.TypeCopilot: {"claude-sonnet-4.5", "auto"},
	config.TypeClaude:  {"sonnet", "opus", "haiku"},
}

// knownCLIModels merges known-models.json from the proxy config dir over the
// built-in defaults, creating the file (with defaults) when missing so users
// have something to edit.
func knownCLIModels() map[config.ModelType][]string {
	merged := map[config.ModelType][]string{}
	for k, v := range defaultKnownCLIModels {
		merged[k] = v
	}
	dir := config.ConfigDir()
	if dir == "" {
		return merged
	}
	path := filepath.Join(dir, "known-models.json")
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			if def, merr := json.MarshalIndent(defaultKnownCLIModels, "", "  "); merr == nil {
				_ = os.MkdirAll(dir, 0o755)
				_ = os.WriteFile(path, def, 0o644)
			}
		}
		return merged
	}
	var user map[config.ModelType][]string
	if err := json.Unmarshal(b, &user); err != nil {
		return merged // malformed file: defaults win, startup keeps working
	}
	for k, v := range user {
		if len(v) > 0 {
			merged[k] = v
		}
	}
	return merged
}

// MaxDiscovered caps how many upstream models one forward may add — a
// gateway upstream like OpenRouter lists hundreds.
const MaxDiscovered = 100

// Populate fills variants for every model with discover_models: true. Errors
// are per-model and non-fatal (the base entry still works); they are
// returned joined so the caller can log them.
func Populate(ctx context.Context, cfg *config.Config) error {
	var errs []string
	known := knownCLIModels()
	// Snapshot: AddVariants appends to cfg.Models while we iterate.
	bases := make([]config.Model, len(cfg.Models))
	copy(bases, cfg.Models)
	for _, m := range bases {
		if !m.DiscoverModels {
			continue
		}
		var (
			variants []string
			err      error
		)
		switch {
		case m.Type == config.TypeForward:
			variants, err = fetchUpstreamModels(ctx, &m)
		case m.Type == config.TypeDevin:
			// Devin lists its live model catalog in the unknown-model error —
			// probe once with a bogus name; fall back to the table.
			variants, err = probeDevinModels(ctx, &m)
			if err != nil {
				variants, err = known[m.Type], nil
			}
		case config.CLIAgent(m.Type):
			variants = known[m.Type]
		}
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", m.Name, err))
			continue
		}
		cfg.AddVariants(m.Name, variants)
	}
	if len(errs) > 0 {
		return fmt.Errorf("discovery: %s", strings.Join(errs, "; "))
	}
	return nil
}

var devinAvailable = regexp.MustCompile(`(?m)^Available:\s*(.+)$`)

// probeDevinModels runs `devin --model <bogus> -p` — the CLI rejects the
// unknown model instantly and prints "Available: a, b, c" with the account's
// live model list.
func probeDevinModels(ctx context.Context, m *config.Model) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	bin := m.DevinBin
	if bin == "" {
		bin = "devin"
	}
	out, _ := exec.CommandContext(ctx, bin, "--model", "routsi-probe", "-p", "probe").CombinedOutput()
	match := devinAvailable.FindSubmatch(out)
	if match == nil {
		return nil, fmt.Errorf("probe output had no Available: list")
	}
	var models []string
	for _, v := range strings.Split(string(match[1]), ",") {
		if v = strings.TrimSpace(v); v != "" {
			models = append(models, v)
		}
	}
	if len(models) == 0 {
		return nil, fmt.Errorf("probe Available: list was empty")
	}
	return models, nil
}

func fetchUpstreamModels(ctx context.Context, m *config.Model) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	url := strings.TrimRight(m.BaseURL, "/") + "/models"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if m.APIKeyEnv != "" {
		req.Header.Set("Authorization", "Bearer "+os.Getenv(m.APIKeyEnv))
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET /models: status %d", resp.StatusCode)
	}
	var body struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("GET /models: %w", err)
	}
	var ids []string
	for _, d := range body.Data {
		if d.ID == "" {
			continue
		}
		ids = append(ids, d.ID)
		if len(ids) >= MaxDiscovered {
			break
		}
	}
	return ids, nil
}
