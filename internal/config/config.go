// Package config loads and validates models.yaml. Silent misconfigs are worse
// than crashes (practitioner-signals.md) — everything is validated at startup.
package config

import (
	"fmt"
	"os"
	"os/exec"
	"time"

	"gopkg.in/yaml.v3"
)

type ModelType string

const (
	TypeForward ModelType = "forward"
	TypeCustom  ModelType = "custom"
	TypeDevin   ModelType = "devin"
	TypeCodex   ModelType = "codex"
	TypeCopilot ModelType = "copilot"
	TypeClaude  ModelType = "claude"
	TypeDynamic ModelType = "dynamic"
)

// CLIAgent reports whether the type shells out to a local agent CLI.
func CLIAgent(t ModelType) bool {
	return t == TypeDevin || t == TypeCodex || t == TypeCopilot || t == TypeClaude
}

// ConfigDir is the proxy's home-directory config location
// (~/.config/routsi), overridable via ROUTSI_CONFIG_DIR
// for tests.
func ConfigDir() string {
	if d := os.Getenv("ROUTSI_CONFIG_DIR"); d != "" {
		return d
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return home + "/.config/routsi"
}

type Style string

const (
	StyleOpenAI    Style = "openai"
	StyleAnthropic Style = "anthropic"
	StyleGemini    Style = "gemini"
)

// Model is one routable target: an HTTP forward or a code-registered handler.
type Model struct {
	Name string    `yaml:"name"`
	Type ModelType `yaml:"type"`

	// Provider labels the model's origin in /v1/models (owned_by). Defaults
	// to the type name.
	Provider string `yaml:"provider"`

	// Variants expands this entry into one catalog model per inner model of
	// the backend (e.g. devin/opus, codex/gpt-5-codex). The base entry stays
	// and uses upstream_model (default: first variant).
	Variants []string `yaml:"variants"`

	// DiscoverModels populates variants dynamically at startup: forwards
	// fetch GET {base_url}/models; CLI agents use the built-in known-models
	// table. Explicit variants: always win.
	DiscoverModels bool `yaml:"discover_models"`

	// forward fields
	Style         Style  `yaml:"style"`
	BaseURL       string `yaml:"base_url"`
	APIKeyEnv     string `yaml:"api_key_env"`
	UpstreamModel string `yaml:"upstream_model"`

	// custom fields
	Handler string `yaml:"handler"`

	// dynamic fields: a virtual model that routes by task difficulty to its
	// members. Keys are router levels (low/medium/high), values are model
	// names from this catalog (variants and discovered names allowed —
	// membership is checked at server start, after discovery).
	Levels map[string]string `yaml:"levels"`

	// CLI-agent fields, shared by devin/codex/copilot (UpstreamModel doubles
	// as the agent's --model / -m flag).
	DevinBin       string `yaml:"bin"`             // binary override; default = type name from PATH
	Workdir        string `yaml:"workdir"`         // agent working directory (default: managed cache dir)
	PermissionMode string `yaml:"permission_mode"` // devin --permission-mode
}

type Config struct {
	Listen    string            `yaml:"listen"`
	Default   string            `yaml:"default"`
	Tiers     map[string]string `yaml:"tiers"` // tier name -> model name
	StickyTTL time.Duration     `yaml:"sticky_ttl"`
	Models    []Model           `yaml:"models"`
}

func Load(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(b)
}

func Parse(b []byte) (*Config, error) {
	cfg := &Config{Listen: ":8080", StickyTTL: 10 * time.Minute}
	if err := yaml.Unmarshal(b, cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	cfg.expandVariants()
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// AddVariants appends expanded variant entries for base after load time
// (dynamic discovery). Names that already exist in the catalog are skipped.
func (c *Config) AddVariants(base string, variants []string) {
	m := c.Model(base)
	if m == nil {
		return
	}
	if m.UpstreamModel == m.Name && len(variants) > 0 {
		m.UpstreamModel = variants[0]
	}
	for _, v := range variants {
		name := base + "/" + v
		if c.Model(name) != nil {
			continue
		}
		clone := *m
		clone.Name = name
		clone.UpstreamModel = v
		c.Models = append(c.Models, clone)
	}
}

// expandVariants turns `variants:` into concrete catalog entries named
// <base>/<variant>, each a copy of the base with upstream_model = variant.
// The base entry keeps its own upstream_model (default: first variant).
func (c *Config) expandVariants() {
	var out []Model
	for _, m := range c.Models {
		vs := m.Variants
		m.Variants = nil
		if len(vs) > 0 && m.UpstreamModel == "" {
			m.UpstreamModel = vs[0]
		}
		out = append(out, m)
		for _, v := range vs {
			clone := m
			clone.Name = m.Name + "/" + v
			clone.UpstreamModel = v
			out = append(out, clone)
		}
	}
	c.Models = out
}

func (c *Config) Model(name string) *Model {
	for i := range c.Models {
		if c.Models[i].Name == name {
			return &c.Models[i]
		}
	}
	return nil
}

func (c *Config) validate() error {
	if len(c.Models) == 0 {
		return fmt.Errorf("config: no models declared")
	}
	seen := map[string]bool{}
	for i := range c.Models {
		m := &c.Models[i]
		if m.Name == "" {
			return fmt.Errorf("config: model %d has no name", i)
		}
		if m.Name == "auto" {
			return fmt.Errorf("config: %q is reserved for the router", m.Name)
		}
		if seen[m.Name] {
			return fmt.Errorf("config: duplicate model %q", m.Name)
		}
		seen[m.Name] = true
		if m.Provider == "" {
			m.Provider = string(m.Type)
		}
		switch m.Type {
		case TypeForward:
			if m.Style == "" {
				m.Style = StyleOpenAI
			}
			switch m.Style {
			case StyleOpenAI, StyleAnthropic, StyleGemini:
			default:
				return fmt.Errorf("config: model %q: unknown style %q", m.Name, m.Style)
			}
			if m.BaseURL == "" {
				return fmt.Errorf("config: forward model %q needs base_url", m.Name)
			}
			if m.APIKeyEnv != "" {
				if os.Getenv(m.APIKeyEnv) == "" {
					return fmt.Errorf("config: model %q: env %s is not set", m.Name, m.APIKeyEnv)
				}
			}
			if m.UpstreamModel == "" {
				m.UpstreamModel = m.Name
			}
		case TypeCustom:
			if m.Handler == "" {
				return fmt.Errorf("config: custom model %q needs handler", m.Name)
			}
		case TypeDynamic:
			if len(m.Levels) == 0 {
				return fmt.Errorf("config: dynamic model %q needs levels", m.Name)
			}
			for level := range m.Levels {
				switch level {
				case "low", "medium", "high":
				default:
					return fmt.Errorf("config: dynamic model %q: unknown level %q (low|medium|high)", m.Name, level)
				}
			}
		case TypeDevin, TypeCodex, TypeCopilot, TypeClaude:
			bin := m.DevinBin
			if bin == "" {
				bin = string(m.Type)
			}
			if _, err := exec.LookPath(bin); err != nil {
				return fmt.Errorf("config: %s model %q: %w", m.Type, m.Name, err)
			}
			if m.Workdir != "" {
				if st, err := os.Stat(m.Workdir); err != nil || !st.IsDir() {
					return fmt.Errorf("config: %s model %q: workdir %q is not a directory", m.Type, m.Name, m.Workdir)
				}
			}
		default:
			return fmt.Errorf("config: model %q: unknown type %q", m.Name, m.Type)
		}
	}
	if c.Default == "" {
		c.Default = c.Models[0].Name
	}
	// Existence of Default/Tiers targets is checked at server start (they may
	// name discovered models that don't exist yet at parse time).
	// Legacy tier aliases map onto router levels.
	if len(c.Tiers) > 0 {
		norm := map[string]string{}
		for k, v := range c.Tiers {
			switch k {
			case "cheap":
				k = "low"
			case "strong":
				k = "high"
			}
			norm[k] = v
		}
		c.Tiers = norm
	}
	return nil
}
