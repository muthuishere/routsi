package router

import (
	"testing"
	"time"
)

func TestExternal(t *testing.T) {
	rules := NewRules()
	r := req("hi, what's the capital of France?") // Rules would say low

	cases := []struct {
		name    string
		command string
		timeout time.Duration
		want    string // expected level; "" means "whatever Rules picks for r"
	}{
		{
			name:    "decider returns high",
			command: `echo '{"level":"high"}'`,
			want:    LevelHigh,
		},
		{
			name:    "malformed json falls back",
			command: `echo 'not json'`,
		},
		{
			name:    "empty output falls back",
			command: `true`,
		},
		{
			name:    "unknown level falls back",
			command: `echo '{"level":"extreme"}'`,
		},
		{
			name:    "non-zero exit falls back",
			command: `exit 1`,
		},
		{
			name:    "timeout falls back",
			command: `sleep 5`,
			timeout: 100 * time.Millisecond,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ext := NewExternal(c.command, c.timeout, "", nil, rules)
			got := ext.Pick(r)
			want := c.want
			if want == "" {
				want = rules.Pick(r)
			}
			if got != want {
				t.Fatalf("Pick() = %q, want %q", got, want)
			}
		})
	}
}

func TestExternal_NoCommandIsRulesSanity(t *testing.T) {
	// Sanity: when decider.command is unset, callers use NewRules() directly
	// (see cmd/routsi/main.go) — External is never constructed in that path.
	// This just documents the equivalence for a case someone might still route
	// through External by accident with an empty command (a no-op shell run
	// that produces no JSON, so it must fall back).
	rules := NewRules()
	ext := NewExternal("true", 0, "", nil, rules)
	r := req("hi, what's the capital of France?")
	if got, want := ext.Pick(r), rules.Pick(r); got != want {
		t.Fatalf("Pick() = %q, want %q", got, want)
	}
}
