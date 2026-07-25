package main

import "testing"

func TestResolveListen(t *testing.T) {
	cases := []struct {
		name         string
		listen, port string
		want         string
	}{
		{"no flags", "", "", ""},
		{"listen wins outright", ":9090", "9000", ":9090"},
		{"listen with host", "0.0.0.0:9090", "", "0.0.0.0:9090"},
		{"digits-only port becomes :port", "", "9000", ":9000"},
		{"port with host passes through", "", "0.0.0.0:9000", "0.0.0.0:9000"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := resolveListen(c.listen, c.port); got != c.want {
				t.Fatalf("resolveListen(%q, %q) = %q, want %q", c.listen, c.port, got, c.want)
			}
		})
	}
}

func TestIsDigits(t *testing.T) {
	cases := map[string]bool{
		"9000":         true,
		"":             false,
		"0.0.0.0:9000": false,
		":9000":        false,
		"90a0":         false,
	}
	for in, want := range cases {
		if got := isDigits(in); got != want {
			t.Fatalf("isDigits(%q) = %v, want %v", in, got, want)
		}
	}
}
