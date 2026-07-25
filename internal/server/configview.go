package server

import "github.com/muthuishere/routsi/internal/config"

// modelView is one catalog entry in the /config snapshot. Never carries a
// secret — api_key_env (a var NAME) would be safe to add later, but the
// current fields are enough for "what's what".
type modelView struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Provider string `json:"provider"`
}

// configView is the SANITIZED snapshot served at GET /config. It must never
// include a token/key value — only booleans, mode strings, and names.
type configView struct {
	Listen        string                       `json:"listen"`
	Default       string                       `json:"default"`
	Auth          bool                         `json:"auth"`    // token(s) configured, never the values
	TLS           string                       `json:"tls"`     // off|tls|mtls
	Decider       string                       `json:"decider"` // "rules" or "external: <command>"
	Tiers         map[string]string            `json:"tiers"`
	DynamicGroups map[string]map[string]string `json:"dynamic_groups"`
	Models        []modelView                  `json:"models"`
}

// configSnapshot builds the sanitized view from the already-loaded config.
// s.tokens is the resolved token set (server already has it; no env re-read),
// and only its length is exposed.
func (s *Server) configSnapshot() configView {
	tlsMode := "off"
	if s.cfg.TLS.Cert != "" {
		tlsMode = "tls"
		if s.cfg.TLS.ClientCA != "" {
			tlsMode = "mtls"
		}
	}
	decider := "rules"
	if s.cfg.Decider.Command != "" {
		decider = "external: " + s.cfg.Decider.Command
	}
	dynamicGroups := map[string]map[string]string{}
	models := make([]modelView, 0, len(s.cfg.Models))
	for _, m := range s.cfg.Models {
		models = append(models, modelView{Name: m.Name, Type: string(m.Type), Provider: m.Provider})
		if m.Type == config.TypeDynamic {
			levels := make(map[string]string, len(m.Levels))
			for lvl, member := range m.Levels {
				levels[lvl] = member
			}
			dynamicGroups[m.Name] = levels
		}
	}
	return configView{
		Listen:        s.cfg.Listen,
		Default:       s.cfg.Default,
		Auth:          len(s.tokens) > 0,
		TLS:           tlsMode,
		Decider:       decider,
		Tiers:         s.cfg.Tiers,
		DynamicGroups: dynamicGroups,
		Models:        models,
	}
}
