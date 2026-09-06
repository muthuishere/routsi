package main

import (
	"bytes"
	"errors"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

const trustedAPIHost = "server.codeium.com"
const maxCredentialBytes = 64 << 10

type credentials struct {
	apiKey secret
	server *url.URL
}

type secret struct{ value string }

type credMeta struct {
	Loaded        bool   `json:"loaded"`
	ServerScheme  string `json:"server_scheme,omitempty"`
	ServerHost    string `json:"server_host,omitempty"`
	KeyPresent    bool   `json:"key_present"`
	PermissionsOK bool   `json:"permissions_ok"`
}

func defaultCredentialsPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "share", "devin", "credentials.toml")
}

func loadCredentials(path string) (credentials, credMeta, error) {
	var out credentials
	var meta credMeta
	lst, err := os.Lstat(path)
	if err != nil {
		return out, meta, errors.New("credentials file unavailable")
	}
	if lst.Mode()&os.ModeSymlink != 0 || !lst.Mode().IsRegular() {
		return out, meta, errors.New("credentials file must be a regular non-symlink")
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return out, meta, errors.New("credentials file unreadable")
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() {
		return out, meta, errors.New("credentials file metadata unavailable")
	}
	if stat, ok := st.Sys().(*syscall.Stat_t); !ok || stat.Uid != uint32(os.Geteuid()) {
		return out, meta, errors.New("credentials file has the wrong owner")
	}
	meta.PermissionsOK = st.Mode().Perm()&0077 == 0
	if !meta.PermissionsOK {
		return out, meta, errors.New("credentials file permissions are too broad")
	}
	b, err := io.ReadAll(io.LimitReader(f, maxCredentialBytes+1))
	if err != nil {
		return out, meta, errors.New("credentials file unreadable")
	}
	defer wipeBytes(b)
	if len(b) > maxCredentialBytes {
		return out, meta, errors.New("credentials file is too large")
	}
	fields := parseFlatTOMLBytes(b)
	key := fields["windsurf_api_key"]
	serverText := fields["api_server_url"]
	if key == "" || serverText == "" {
		return out, meta, errors.New("required credential fields are absent")
	}
	u, err := url.Parse(serverText)
	if err != nil || u.Scheme != "https" || u.Hostname() != trustedAPIHost || u.Port() != "" || u.User != nil {
		return out, meta, errors.New("api_server_url is not the trusted Devin origin")
	}
	u.User, u.RawQuery, u.Fragment = nil, "", ""
	out = credentials{apiKey: secret{value: key}, server: u}
	meta.Loaded, meta.KeyPresent = true, true
	meta.ServerScheme, meta.ServerHost = u.Scheme, u.Host
	return out, meta, nil
}

func parseFlatTOMLBytes(b []byte) map[string]string {
	return parseFlatTOML(string(bytes.Clone(b)))
}

func parseFlatTOML(s string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "[") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key, value := strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
		if len(value) >= 2 && value[0] == '"' && value[len(value)-1] == '"' {
			value = strings.ReplaceAll(value[1:len(value)-1], `\"`, `"`)
		}
		out[key] = value
	}
	return out
}

func publicError(err error) string {
	if err == nil {
		return ""
	}
	s := err.Error()
	switch s {
	case "credentials file unavailable", "credentials file must be a regular non-symlink",
		"credentials file unreadable", "credentials file metadata unavailable",
		"credentials file has the wrong owner", "credentials file permissions are too broad",
		"credentials file is too large", "required credential fields are absent",
		"api_server_url is not the trusted Devin origin", "cannot construct discovery request",
		"discovery transport failed", "discovery response unreadable",
		"discovery response was too large", "discovery response had an unexpected content type",
		"discovery response was not valid protobuf":
		return s
	default:
		if strings.HasPrefix(s, "discovery returned HTTP ") {
			return s
		}
		return "internal spike error"
	}
}

func wipeBytes(b []byte) {
	for i := range b {
		b[i] = 0
	}
}
