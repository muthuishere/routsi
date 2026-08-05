// Package adapters ships the default adapter templates (ADR-013) inside the
// binary and lays them down in the user's config dir so they are editable
// files, not vendored routsi code.
//
// The templates are *defaults*, not behaviour: routsi never runs one unless a
// models.yaml entry names it. Users edit them freely, and an edited file is
// never overwritten — same contract as known-models.json.
package adapters

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/muthuishere/routsi/internal/config"
)

//go:embed all:templates
var templateFS embed.FS

// Dir is where adapter templates live: <config dir>/adapters. Empty when the
// config dir cannot be resolved (no home directory).
func Dir() string {
	base := config.ConfigDir()
	if base == "" {
		return ""
	}
	return filepath.Join(base, "adapters")
}

// Ensure writes any missing template into Dir() and returns the files it
// created. Existing files are left alone — a user's edits always win, so this
// is safe to call on every startup.
func Ensure() ([]string, error) {
	dir := Dir()
	if dir == "" {
		return nil, nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("adapters: create %s: %w", dir, err)
	}

	var written []string
	err := fs.WalkDir(templateFS, "templates", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, relErr := filepath.Rel("templates", p)
		if relErr != nil {
			return relErr
		}
		dest := filepath.Join(dir, rel)
		if _, statErr := os.Stat(dest); statErr == nil {
			return nil // present already: never clobber a user's edits
		}
		body, readErr := templateFS.ReadFile(p)
		if readErr != nil {
			return readErr
		}
		// Executable: an adapter is often run directly during development.
		if writeErr := os.WriteFile(dest, body, 0o755); writeErr != nil {
			return fmt.Errorf("adapters: write %s: %w", dest, writeErr)
		}
		written = append(written, dest)
		return nil
	})
	return written, err
}

// Reset rewrites every template from the binary, overwriting local edits. Only
// for an explicit `routsi install --adapters --force`.
func Reset() ([]string, error) {
	dir := Dir()
	if dir == "" {
		return nil, nil
	}
	entries, err := fs.ReadDir(templateFS, "templates")
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		_ = os.Remove(filepath.Join(dir, e.Name()))
	}
	return Ensure()
}

// Names lists the template file names shipped in this binary.
func Names() []string {
	entries, err := fs.ReadDir(templateFS, "templates")
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() {
			out = append(out, e.Name())
		}
	}
	return out
}
