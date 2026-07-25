package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestLoadEnvFilesPrecedence verifies the layering: process env wins over every
// file, then an explicit -env file wins over ./.env and ~/.config/routsi/.env.
func TestLoadEnvFilesPrecedence(t *testing.T) {
	// Isolate cwd so a stray repo ./.env can't interfere; give it its own .env.
	origWd, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(origWd) })
	cwd := t.TempDir()
	if err := os.Chdir(cwd); err != nil {
		t.Fatal(err)
	}
	writeEnv(t, filepath.Join(cwd, ".env"), "FROM_CWD=cwd\nSHARED=cwd\n")

	// Config dir (~/.config/routsi resolves via ROUTSI_CONFIG_DIR override).
	cfgDir := t.TempDir()
	t.Setenv("ROUTSI_CONFIG_DIR", cfgDir)
	writeEnv(t, filepath.Join(cfgDir, ".env"), "FROM_CFG=cfg\nSHARED=cfg\n")

	// Explicit env file (highest-priority file).
	explicit := filepath.Join(t.TempDir(), "custom.env")
	writeEnv(t, explicit, "FROM_EXPLICIT=explicit\nSHARED=explicit\nPROC=fromfile\n")

	// Process env must win even over the explicit file.
	t.Setenv("PROC", "fromproc")

	loadEnvFiles(explicit)

	want := map[string]string{
		"FROM_EXPLICIT": "explicit",
		"FROM_CWD":      "cwd",
		"FROM_CFG":      "cfg",
		"SHARED":        "explicit", // explicit file loaded first among files
		"PROC":          "fromproc", // process env beats every file
	}
	for k, w := range want {
		if got := os.Getenv(k); got != w {
			t.Errorf("%s = %q, want %q", k, got, w)
		}
	}
}

func writeEnv(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}
