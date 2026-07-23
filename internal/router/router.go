// Package router classifies a request into a difficulty level. Pre-request
// only — the decision is made on the request head, never mid-stream
// (docs/research/README.md #1). Pluggable so a learned scorer can replace the
// rules later without touching the server.
package router

import (
	"regexp"
	"strings"

	"github.com/muthuishere/routsi/internal/api"
)

// Levels, in escalation order. Dynamic groups map these to member models.
const (
	LevelLow    = "low"
	LevelMedium = "medium"
	LevelHigh   = "high"
)

// Rank orders levels for escalate-only stickiness. Unknown levels rank
// lowest.
func Rank(level string) int {
	switch level {
	case LevelHigh:
		return 2
	case LevelMedium:
		return 1
	default:
		return 0
	}
}

// Fallback returns the preferred member-lookup order for a level, so a group
// that doesn't declare every level still resolves.
func Fallback(level string) []string {
	switch level {
	case LevelHigh:
		return []string{LevelHigh, LevelMedium, LevelLow}
	case LevelMedium:
		return []string{LevelMedium, LevelLow, LevelHigh}
	default:
		return []string{LevelLow, LevelMedium, LevelHigh}
	}
}

// Router maps a request to a level.
type Router interface {
	Pick(req *api.ChatRequest) string
}

// Rules is the v1 heuristic router. RouterBench showed simple baselines match
// learned routers; this stays deliberately dumb and fast.
type Rules struct {
	// MediumChars / HighChars promote by total user+system text volume.
	MediumChars int
	HighChars   int
}

func NewRules() *Rules { return &Rules{MediumChars: 400, HighChars: 1500} }

var highHints = regexp.MustCompile(`(?i)\b(prove|derive|theorem|refactor|architect|debug|optimi[sz]e|step[ -]by[ -]step|reason|analy[sz]e|why does|explain in detail)\b`)

var mediumHints = regexp.MustCompile(`(?i)\b(explain|compare|summari[sz]e|implement|design|plan|review|translate|convert)\b`)

func (r *Rules) Pick(req *api.ChatRequest) string {
	// Signals come from the CURRENT turn only. Cumulative history length is
	// deliberately not a signal: it grows monotonically in client-managed
	// conversations, so every long conversation would classify high forever
	// (audit D1; length is a poor difficulty proxy anyway — arXiv
	// 2605.07112). A decaying per-turn aggregate is the planned upgrade.
	last := req.LastUserText()
	switch {
	case strings.Contains(last, "```"), highHints.MatchString(last), len(last) > r.HighChars:
		return LevelHigh
	case mediumHints.MatchString(last), len(last) > r.MediumChars:
		return LevelMedium
	default:
		return LevelLow
	}
}
