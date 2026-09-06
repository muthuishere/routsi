package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"os/user"
	"runtime"
	"strings"
	"time"
)

const keychainService = "Claude Code-credentials"
const securityBinary = "/usr/bin/security"

var ErrAuthRequired = errors.New("claude code login required")

type OAuthCredential struct {
	AccessToken string
	ExpiresAt   time.Time
}

func (c OAuthCredential) Usable(now time.Time) bool {
	return c.AccessToken != "" && (c.ExpiresAt.IsZero() || now.Before(c.ExpiresAt))
}

type commandRunner interface {
	Output(context.Context, string, ...string) ([]byte, error)
}

type execRunner struct{}

func (execRunner) Output(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).Output()
}

type KeychainSource struct {
	runner commandRunner
	now    func() time.Time
}

func NewKeychainSource() *KeychainSource {
	return &KeychainSource{runner: execRunner{}, now: time.Now}
}

// Load asks macOS Keychain for Claude Code's existing login. The secret is
// consumed from the child process's stdout in memory and is never placed in an
// argument, environment variable, log message, or returned error.
func (s *KeychainSource) Load(ctx context.Context) (OAuthCredential, error) {
	if runtime.GOOS != "darwin" {
		return OAuthCredential{}, fmt.Errorf("%w: macOS Keychain is unavailable", ErrAuthRequired)
	}
	u, err := user.Current()
	if err != nil || u.Username == "" {
		return OAuthCredential{}, fmt.Errorf("%w: cannot determine local account", ErrAuthRequired)
	}
	raw, err := s.runner.Output(ctx, securityBinary, "find-generic-password", "-a", u.Username, "-s", keychainService, "-w")
	if err != nil {
		return OAuthCredential{}, fmt.Errorf("%w: keychain item unavailable", ErrAuthRequired)
	}
	defer wipe(raw)
	cred, err := parseCredential(raw)
	if err != nil {
		return OAuthCredential{}, fmt.Errorf("%w: unsupported keychain payload", ErrAuthRequired)
	}
	if !cred.Usable(s.now()) {
		return OAuthCredential{}, fmt.Errorf("%w: stored access token is absent or expired", ErrAuthRequired)
	}
	return cred, nil
}

// Refresh deliberately means "re-read vendor-owned storage". This spike does
// not copy Claude Code's private OAuth client contract or write rotated tokens.
func (s *KeychainSource) Refresh(ctx context.Context) (OAuthCredential, error) {
	return s.Load(ctx)
}

type credentialEnvelope struct {
	ClaudeAI OAuthJSON `json:"claudeAiOauth"`
}

type OAuthJSON struct {
	AccessToken string `json:"accessToken"`
	ExpiresAt   int64  `json:"expiresAt"`
}

func parseCredential(raw []byte) (OAuthCredential, error) {
	var env credentialEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return OAuthCredential{}, errors.New("invalid credential JSON")
	}
	if strings.TrimSpace(env.ClaudeAI.AccessToken) == "" {
		return OAuthCredential{}, errors.New("access token missing")
	}
	var expiry time.Time
	if env.ClaudeAI.ExpiresAt > 0 {
		expiry = time.UnixMilli(env.ClaudeAI.ExpiresAt)
	}
	return OAuthCredential{
		AccessToken: env.ClaudeAI.AccessToken, ExpiresAt: expiry,
	}, nil
}

func wipe(b []byte) {
	for i := range b {
		b[i] = 0
	}
}
