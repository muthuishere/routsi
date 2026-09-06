package main

import (
	"strings"
	"testing"
	"time"
)

func TestParseCredential(t *testing.T) {
	raw := []byte(`{"claudeAiOauth":{"accessToken":"fake-access","refreshToken":"fake-refresh","expiresAt":4102444800000,"scopes":["scope:a"]}}`)
	cred, err := parseCredential(raw)
	if err != nil {
		t.Fatal(err)
	}
	if cred.AccessToken != "fake-access" {
		t.Fatal("credential fields were not parsed")
	}
	if !cred.Usable(time.Unix(0, 0)) {
		t.Fatal("expected credential to be usable")
	}
}

func TestParseCredentialRejectsMissingTokenWithoutLeakingPayload(t *testing.T) {
	secret := "should-never-appear"
	_, err := parseCredential([]byte(`{"claudeAiOauth":{"refreshToken":"` + secret + `"}}`))
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatal("secret leaked")
	}
}

func TestWipe(t *testing.T) {
	b := []byte("fake-secret")
	wipe(b)
	for _, v := range b {
		if v != 0 {
			t.Fatal("buffer was not wiped")
		}
	}
}
