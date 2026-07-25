package config

import (
	"bufio"
	"os"
	"strings"
)

// LoadDotEnv reads a .env file at path and sets any KEY=VALUE pairs into the
// process environment, WITHOUT overriding variables that are already set — so
// the shell / service environment always wins over the file. It returns the
// number of variables it set. A missing file is not an error (returns 0, nil).
//
// This lets secrets (provider api keys, ROUTSI_TOKENS) live in
// ~/.config/routsi/.env instead of a shell profile. Values are never logged by
// callers — only the count is surfaced.
//
// Supported syntax (a deliberately small subset): blank lines and lines
// starting with '#' are ignored; an optional leading "export " is stripped;
// KEY=VALUE where VALUE may be wrapped in matching single or double quotes
// (quotes are removed, no escape processing); surrounding whitespace on the key
// and on an unquoted value is trimmed.
func LoadDotEnv(path string) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	defer f.Close()

	var set int
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		eq := strings.IndexByte(line, '=')
		if eq <= 0 {
			continue // no key, or no '='
		}
		key := strings.TrimSpace(line[:eq])
		if key == "" {
			continue
		}
		val := strings.TrimSpace(line[eq+1:])
		if len(val) >= 2 {
			if (val[0] == '"' && val[len(val)-1] == '"') ||
				(val[0] == '\'' && val[len(val)-1] == '\'') {
				val = val[1 : len(val)-1]
			}
		}
		if _, exists := os.LookupEnv(key); exists {
			continue // shell/service env wins — never override
		}
		if err := os.Setenv(key, val); err != nil {
			return set, err
		}
		set++
	}
	if err := sc.Err(); err != nil {
		return set, err
	}
	return set, nil
}
