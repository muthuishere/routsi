package router

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/muthuishere/routsi/internal/api"
)

func req(userText string) *api.ChatRequest {
	c, _ := json.Marshal(userText)
	return &api.ChatRequest{Messages: []api.Message{{Role: "user", Content: c}}}
}

func TestRules(t *testing.T) {
	r := NewRules()
	cases := []struct {
		name string
		req  *api.ChatRequest
		want string
	}{
		{"short chit-chat", req("hi, what's the capital of France?"), LevelLow},
		{"code fence", req("fix this:\n```go\npanic(1)\n```"), LevelHigh},
		{"reasoning keyword", req("prove that sqrt(2) is irrational"), LevelHigh},
		{"debug keyword", req("help me debug a race condition"), LevelHigh},
		{"long prompt", req(strings.Repeat("context ", 300)), LevelHigh},
		{"medium keyword", req("explain closures in javascript"), LevelMedium},
		{"medium by volume", req(strings.Repeat("data ", 100)), LevelMedium},
		{"empty", &api.ChatRequest{}, LevelLow},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := r.Pick(c.req); got != c.want {
				t.Fatalf("Pick() = %q, want %q", got, c.want)
			}
		})
	}
}

func TestRankAndFallback(t *testing.T) {
	if !(Rank(LevelHigh) > Rank(LevelMedium) && Rank(LevelMedium) > Rank(LevelLow)) {
		t.Fatal("rank order broken")
	}
	if got := Fallback(LevelMedium); got[0] != LevelMedium || got[1] != LevelLow {
		t.Fatalf("medium fallback = %v", got)
	}
}
