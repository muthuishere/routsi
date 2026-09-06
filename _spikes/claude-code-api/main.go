package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"time"
)

func main() {
	model := flag.String("model", "claude-sonnet-5", "resolved Claude model id")
	version := flag.String("claude-version", "2.1.259", "compatibility User-Agent version")
	timeout := flag.Duration("timeout", 90*time.Second, "total canary timeout")
	flag.Parse()

	const marker = "ROUTSI_CLAUDE_CANARY_OK"
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	client := &Client{
		HTTP: &http.Client{Timeout: *timeout}, Auth: NewKeychainSource(), Version: *version,
	}
	result, err := client.Canary(ctx, MessagesRequest{
		Model: *model, MaxTokens: 32, Stream: true,
		Messages: []Message{{Role: "user", Content: "Reply with exactly " + marker + "."}},
	}, marker)
	if err != nil {
		// Errors are deliberately structural and never include headers or bodies.
		_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"ok": false, "error": err.Error()})
		os.Exit(1)
	}
	if err := json.NewEncoder(os.Stdout).Encode(result); err != nil {
		fmt.Fprintln(os.Stderr, "failed to encode sanitized result")
		os.Exit(1)
	}
	if !result.OK {
		os.Exit(2)
	}
}
