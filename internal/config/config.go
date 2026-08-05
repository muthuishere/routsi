// Package config loads and validates models.yaml. Silent misconfigs are worse
// than crashes (practitioner-signals.md) — everything is validated at startup.
package config

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
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
	TypeQueue   ModelType = "queue"   // pull-worker queue (ADR-001)
	TypeCommand ModelType = "command" // exec adapter (ADR-013)
)

// ToolMode is how an adapter handles a request's `tools` (ADR-013).
type ToolMode string

const (
	ToolsNative   ToolMode = "native"   // tools passed through; adapter returns tool_calls
	ToolsEmulated ToolMode = "emulated" // routsi renders the fenced-JSON manifest (ADR-011 Phase A)
	ToolsOff      ToolMode = "off"      // tools present => ErrToolsUnsupported => 400
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

	// exec-adapter fields (type: command, ADR-013). Command is a shell string
	// run via `sh -c`; the job goes to its stdin, the answer comes off stdout.
	// Workdir is shared with the CLI-agent types below.
	Command  string        `yaml:"command"`
	Timeout  time.Duration `yaml:"timeout"` // per-request cap (default 5m)
	ToolMode ToolMode      `yaml:"tools"`   // native (default) | emulated | off

	// CLI-agent fields, shared by devin/codex/copilot (UpstreamModel doubles
	// as the agent's --model / -m flag).
	DevinBin       string   `yaml:"bin"`             // binary override; default = type name from PATH
	Args           []string `yaml:"args"`            // extra args, prepended to the invocation's own args
	Workdir        string   `yaml:"workdir"`         // agent working directory (default: managed cache dir)
	PermissionMode string   `yaml:"permission_mode"` // devin --permission-mode
}

type Config struct {
	Listen    string            `yaml:"listen"`
	Default   string            `yaml:"default"`
	Tiers     map[string]string `yaml:"tiers"` // tier name -> model name
	StickyTTL time.Duration     `yaml:"sticky_ttl"`
	Models    []Model           `yaml:"models"`
	Auth      AuthConfig        `yaml:"auth"`
	TLS       TLSConfig         `yaml:"tls"`
	Workers   WorkersConfig     `yaml:"workers"`
	Decider   DeciderConfig     `yaml:"decider"`
	HTTP      HTTPConfig        `yaml:"http"`

	// StreamHeartbeat is how often the streaming envelope path writes an SSE
	// comment (": ping\n\n") while a buffered backend is silently working, so
	// idle-timeout intermediaries (nginx/ALB/Cloudflare) don't drop the
	// connection. Default 15s; 0 disables heartbeats entirely.
	StreamHeartbeat time.Duration `yaml:"stream_heartbeat"`
}

// WorkersConfig is the pull-worker (ADR-001) settings block. Auth is a
// reserved, empty placeholder so worker auth can be added later without a
// breaking config change. Freshness/MaxWait tune the broker (internal/queue);
// zero means "use the broker's built-in default".
type WorkersConfig struct {
	Auth      map[string]any `yaml:"auth"`      // reserved — no worker auth in v1
	Freshness time.Duration  `yaml:"freshness"` // a worker is "present" if it polled within this (default 30s)
	MaxWait   time.Duration  `yaml:"max_wait"`  // cap on how long a request blocks for a worker (default 5m)
}

// HTTPConfig tunes the outbound connection pool that forward upstreams share.
// Every field is optional: zero means "use the safe default" (Defaults()), so
// an omitted `http:` block behaves well without the operator knowing this
// block exists — but nothing here is fixed by the binary.
//
// The pool sizes matter more than they look. Go's http.DefaultTransport keeps
// only 2 idle connections per host, so past two concurrent calls to the same
// upstream every request opened and discarded a fresh TCP connection. Measured
// against a local mock at c=50/n=100k, that cost p99 605ms while p50 stayed
// ~1ms — a pure tail-latency defect, invisible in light testing. Upstreams are
// few and long-lived, so the default pools generously.
type HTTPConfig struct {
	Timeout               time.Duration `yaml:"timeout"`                 // whole-request cap (default 5m — streams are long)
	MaxIdleConns          int           `yaml:"max_idle_conns"`          // default 512
	MaxIdleConnsPerHost   int           `yaml:"max_idle_conns_per_host"` // default 256
	IdleConnTimeout       time.Duration `yaml:"idle_conn_timeout"`       // default 90s
	TLSHandshakeTimeout   time.Duration `yaml:"tls_handshake_timeout"`   // default 10s
	ExpectContinueTimeout time.Duration `yaml:"expect_continue_timeout"` // default 1s
	DisableHTTP2          bool          `yaml:"disable_http2"`           // default false (HTTP/2 attempted)
	DisableCompression    bool          `yaml:"disable_compression"`     // default false
	Retries               *int          `yaml:"retries"`                 // before-first-byte retries; default 2, 0 disables
}

// Defaults returns h with every unset field filled in. Explicit zeros that are
// meaningful (retries: 0) are preserved via the pointer field.
func (h HTTPConfig) Defaults() HTTPConfig {
	if h.Timeout <= 0 {
		h.Timeout = 5 * time.Minute
	}
	if h.MaxIdleConns <= 0 {
		h.MaxIdleConns = 512
	}
	if h.MaxIdleConnsPerHost <= 0 {
		h.MaxIdleConnsPerHost = 256
	}
	if h.IdleConnTimeout <= 0 {
		h.IdleConnTimeout = 90 * time.Second
	}
	if h.TLSHandshakeTimeout <= 0 {
		h.TLSHandshakeTimeout = 10 * time.Second
	}
	if h.ExpectContinueTimeout <= 0 {
		h.ExpectContinueTimeout = time.Second
	}
	if h.Retries == nil {
		n := 2
		h.Retries = &n
	}
	return h
}

// RetryCount is the resolved before-first-byte retry count.
func (h HTTPConfig) RetryCount() int {
	if h.Retries == nil {
		return 2
	}
	if *h.Retries < 0 {
		return 0
	}
	return *h.Retries
}

// DeciderConfig points `auto` routing at an external, user-writable decider
// process instead of the built-in Rules scorer (docs/adr/007). Empty Command
// means "no decider" — behavior is unchanged from the built-in Rules.
type DeciderConfig struct {
	Command string        `yaml:"command"` // shell command, e.g. "node decider.js"
	Timeout time.Duration `yaml:"timeout"` // default 3s
	Cwd     string        `yaml:"cwd"`     // default: process cwd
}

// AuthConfig gates the API with bearer tokens. Prefer tokens_env (values in the
// environment) over inline tokens (values in a committed file).
type AuthConfig struct {
	TokensEnv string   `yaml:"tokens_env"` // env var holding space/comma-separated tokens
	Tokens    []string `yaml:"tokens"`     // inline (discouraged — keeps secrets out of git)
}

// TLSConfig enables HTTPS, and mTLS when ClientCA is set (client certs are then
// required and verified against that CA).
type TLSConfig struct {
	Cert     string `yaml:"cert"`
	Key      string `yaml:"key"`
	ClientCA string `yaml:"client_ca"`
}

// AuthTokens resolves the effective token set (env + inline). Empty => auth off.
func (a AuthConfig) AuthTokens() []string {
	var out []string
	if a.TokensEnv != "" {
		for _, t := range strings.FieldsFunc(os.Getenv(a.TokensEnv), func(r rune) bool {
			return r == ',' || r == ' ' || r == '\n' || r == '\t'
		}) {
			out = append(out, t)
		}
	}
	out = append(out, a.Tokens...)
	return out
}

func Load(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(b)
}

func Parse(b []byte) (*Config, error) {
	cfg := &Config{Listen: ":8080", StickyTTL: 10 * time.Minute, StreamHeartbeat: 15 * time.Second}
	if err := yaml.Unmarshal(b, cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	cfg.expandVariants()
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	if cfg.Decider.Command != "" && cfg.Decider.Timeout == 0 {
		cfg.Decider.Timeout = 3 * time.Second
	}
	cfg.HTTP = cfg.HTTP.Defaults()
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
		case TypeQueue:
			// A config-declared queue only reserves a name; a worker supplies
			// the answers at runtime. Nothing else to validate.
		case TypeCommand:
			if m.Command == "" {
				return fmt.Errorf("config: command model %q needs command", m.Name)
			}
			switch m.ToolMode {
			case "":
				m.ToolMode = ToolsNative
			case ToolsNative, ToolsEmulated, ToolsOff:
			default:
				return fmt.Errorf("config: command model %q: unknown tools %q (native|emulated|off)", m.Name, m.ToolMode)
			}
			if m.Timeout <= 0 {
				m.Timeout = 5 * time.Minute
			}
			if m.Workdir != "" {
				if st, err := os.Stat(m.Workdir); err != nil || !st.IsDir() {
					return fmt.Errorf("config: command model %q: workdir %q is not a directory", m.Name, m.Workdir)
				}
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
