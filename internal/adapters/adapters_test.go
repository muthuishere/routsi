package adapters

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Ensure must create missing templates and then never touch them again — a
// user's edits are the point of shipping them as files (ADR-013).
func TestEnsureBootstrapsThenPreservesEdits(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("ROUTSI_CONFIG_DIR", dir)

	wrote, err := Ensure()
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if len(wrote) != len(Names()) {
		t.Fatalf("wrote %d files, want %d (%v)", len(wrote), len(Names()), Names())
	}

	target := filepath.Join(Dir(), "cli.js")
	edited := "// USER EDIT\n"
	if err := os.WriteFile(target, []byte(edited), 0o755); err != nil {
		t.Fatalf("edit: %v", err)
	}

	again, err := Ensure()
	if err != nil {
		t.Fatalf("second Ensure: %v", err)
	}
	if len(again) != 0 {
		t.Errorf("second Ensure wrote %v, want nothing", again)
	}
	got, _ := os.ReadFile(target)
	if string(got) != edited {
		t.Error("Ensure clobbered a user-edited adapter")
	}
}

func TestResetRestoresShippedTemplates(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("ROUTSI_CONFIG_DIR", dir)
	if _, err := Ensure(); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	target := filepath.Join(Dir(), "cli.js")
	if err := os.WriteFile(target, []byte("// broken\n"), 0o755); err != nil {
		t.Fatalf("edit: %v", err)
	}
	if _, err := Reset(); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	got, _ := os.ReadFile(target)
	if !strings.Contains(string(got), "ADAPTER_CLI") {
		t.Error("Reset did not restore the shipped cli.js")
	}
}

func TestDirUnderConfigDir(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("ROUTSI_CONFIG_DIR", dir)
	if want := filepath.Join(dir, "adapters"); Dir() != want {
		t.Errorf("Dir() = %q, want %q", Dir(), want)
	}
}
