package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadDotEnv(t *testing.T) {
	content := `# a comment
export FOO=bar
BAZ = qux
QUOTED="has spaces"
SINGLE='single quoted'
EMPTY=
PRESET=fromfile

# blank line above
NOEQ
=noKey
`
	dir := t.TempDir()
	p := filepath.Join(dir, ".env")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	// PRESET is already in the environment; the file must NOT override it.
	t.Setenv("PRESET", "fromshell")

	n, err := LoadDotEnv(p)
	if err != nil {
		t.Fatalf("LoadDotEnv: %v", err)
	}
	// FOO, BAZ, QUOTED, SINGLE, EMPTY set; PRESET skipped (already set).
	if n != 5 {
		t.Fatalf("set count = %d, want 5", n)
	}

	cases := map[string]string{
		"FOO":    "bar",
		"BAZ":    "qux",
		"QUOTED": "has spaces",
		"SINGLE": "single quoted",
		"EMPTY":  "",
		"PRESET": "fromshell", // shell wins
	}
	for k, want := range cases {
		if got := os.Getenv(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	if _, ok := os.LookupEnv("NOEQ"); ok {
		t.Errorf("NOEQ should not be set (no '=')")
	}
}

func TestLoadDotEnvMissingFile(t *testing.T) {
	n, err := LoadDotEnv(filepath.Join(t.TempDir(), "does-not-exist.env"))
	if err != nil {
		t.Fatalf("missing file should be nil error, got %v", err)
	}
	if n != 0 {
		t.Fatalf("missing file set count = %d, want 0", n)
	}
}
