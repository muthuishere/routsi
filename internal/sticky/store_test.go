package sticky

import (
	"testing"
	"time"
)

func TestPinGet(t *testing.T) {
	s := New(time.Minute)
	if _, ok := s.Get("c1"); ok {
		t.Fatal("empty store returned a pin")
	}
	s.Pin("c1", "gpt-cheap")
	got, ok := s.Get("c1")
	if !ok || got != "gpt-cheap" {
		t.Fatalf("Get = %q,%v want gpt-cheap,true", got, ok)
	}
	s.Pin("c1", "gpt-strong") // re-pin (escalation)
	if got, _ := s.Get("c1"); got != "gpt-strong" {
		t.Fatalf("after re-pin Get = %q", got)
	}
}

func TestEmptyIDNeverPins(t *testing.T) {
	s := New(time.Minute)
	s.Pin("", "x")
	if _, ok := s.Get(""); ok {
		t.Fatal("empty id must not pin")
	}
}

func TestTTLExpiryAndRefresh(t *testing.T) {
	s := New(time.Minute)
	now := time.Unix(0, 0)
	s.now = func() time.Time { return now }
	s.Pin("c1", "m")

	now = now.Add(30 * time.Second)
	if _, ok := s.Get("c1"); !ok { // refreshes TTL
		t.Fatal("pin expired too early")
	}
	now = now.Add(50 * time.Second) // 50s since refresh < TTL
	if _, ok := s.Get("c1"); !ok {
		t.Fatal("refresh did not extend TTL")
	}
	now = now.Add(2 * time.Minute)
	if _, ok := s.Get("c1"); ok {
		t.Fatal("pin survived past TTL")
	}
}
