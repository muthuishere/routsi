package backend

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/muthuishere/routsi/internal/api"
	"github.com/muthuishere/routsi/internal/config"
)

// fakeDevin writes a shell script that mimics the devin CLI: `-p` answers and
// bumps a session in list.json; `list --format json` prints it; `-r` answers
// with a resumed marker. Every call is appended to calls.log.
func fakeDevin(t *testing.T) (bin, dir string) {
	t.Helper()
	dir = t.TempDir()
	list, _ := json.Marshal([]map[string]any{{"id": "sess-abc", "last_activity_at": 100}})
	if err := os.WriteFile(filepath.Join(dir, "list.json"), list, 0o644); err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
dir="$(dirname "$0")"
echo "$@" >> "$dir/calls.log"
if [ "$1" = "list" ]; then cat "$dir/list.json"; exit 0; fi
resumed=""
prompt=""
while [ $# -gt 0 ]; do
  case "$1" in
    -r) resumed="$2"; shift 2 ;;
    -p) prompt="$2"; shift 2 ;;
    *) shift ;;
  esac
done
if [ -n "$resumed" ]; then echo "resumed($resumed): $prompt"; else echo "fresh: $prompt"; fi
`
	bin = filepath.Join(dir, "devin")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, dir
}

func devinReq(convID, text string) *api.ChatRequest {
	c, _ := json.Marshal(text)
	return &api.ChatRequest{
		ConversationID: convID,
		Messages:       []api.Message{{Role: "user", Content: c}},
	}
}

func TestDevinCreatesThenResumesSession(t *testing.T) {
	bin, dir := fakeDevin(t)
	d := NewDevin(&config.Model{Name: "devin", Type: config.TypeDevin, DevinBin: bin, Workdir: dir})

	// Turn 1: fresh run, session captured from list.
	out, err := d.Complete(context.Background(), devinReq("conv-1", "hello devin"))
	if err != nil {
		t.Fatalf("turn1: %v", err)
	}
	if out != "fresh: hello devin" {
		t.Fatalf("turn1 out = %q", out)
	}

	// Turn 2 same conversation: resumes sess-abc.
	out, err = d.Complete(context.Background(), devinReq("conv-1", "and again"))
	if err != nil {
		t.Fatalf("turn2: %v", err)
	}
	if out != "resumed(sess-abc): and again" {
		t.Fatalf("turn2 out = %q, want resume of sess-abc", out)
	}

	// A different conversation starts fresh.
	out, err = d.Complete(context.Background(), devinReq("conv-2", "other"))
	if err != nil {
		t.Fatalf("conv2: %v", err)
	}
	if out != "fresh: other" {
		t.Fatalf("conv2 out = %q", out)
	}
}

func TestDevinRendersHistoryOnNewSession(t *testing.T) {
	bin, dir := fakeDevin(t)
	d := NewDevin(&config.Model{Name: "devin", Type: config.TypeDevin, DevinBin: bin, Workdir: dir})

	u1, _ := json.Marshal("first question")
	a1, _ := json.Marshal("first answer")
	u2, _ := json.Marshal("follow-up")
	req := &api.ChatRequest{
		ConversationID: "conv-hist",
		Messages: []api.Message{
			{Role: "user", Content: u1},
			{Role: "assistant", Content: a1},
			{Role: "user", Content: u2},
		},
	}
	out, err := d.Complete(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Prior conversation:", "first question", "first answer", "follow-up"} {
		if !strings.Contains(out, want) {
			t.Fatalf("transcript prompt missing %q in %q", want, out)
		}
	}
}

func TestDevinModelAndPermissionFlags(t *testing.T) {
	bin, dir := fakeDevin(t)
	d := NewDevin(&config.Model{
		Name: "devin", Type: config.TypeDevin, DevinBin: bin, Workdir: dir,
		UpstreamModel: "opus", PermissionMode: "auto",
	})
	if _, err := d.Complete(context.Background(), devinReq("", "hi")); err != nil {
		t.Fatal(err)
	}
	calls, _ := os.ReadFile(filepath.Join(dir, "calls.log"))
	if !strings.Contains(string(calls), "--model opus") || !strings.Contains(string(calls), "--permission-mode auto") {
		t.Fatalf("flags not passed: %s", calls)
	}
	// Empty conversation id must not create a session mapping (no list call).
	if strings.Contains(string(calls), "list") {
		t.Fatalf("session lookup ran for empty conversation id: %s", calls)
	}
}

func TestDevinExtraArgsPrepended(t *testing.T) {
	bin, dir := fakeDevin(t)
	d := NewDevin(&config.Model{
		Name: "devin", Type: config.TypeDevin, DevinBin: bin, Workdir: dir,
		Args: []string{"--org", "acme"}, UpstreamModel: "opus",
	})
	if _, err := d.Complete(context.Background(), devinReq("", "hi")); err != nil {
		t.Fatal(err)
	}
	calls, _ := os.ReadFile(filepath.Join(dir, "calls.log"))
	got := string(calls)
	if !strings.Contains(got, "--org acme") {
		t.Fatalf("configured args missing: %s", got)
	}
	if strings.Index(got, "--org acme") > strings.Index(got, "--model opus") {
		t.Fatalf("configured args must be prepended before the built-in flags: %s", got)
	}
}

func TestDevinDefaultsToManagedWorkdir(t *testing.T) {
	d := NewDevin(&config.Model{Name: "devin-x", Type: config.TypeDevin})
	if d.workdir == "" {
		t.Fatal("no managed workdir assigned")
	}
	if st, err := os.Stat(d.workdir); err != nil || !st.IsDir() {
		t.Fatalf("managed workdir %q not created: %v", d.workdir, err)
	}
	if !strings.Contains(d.workdir, "routsi") {
		t.Fatalf("workdir %q should live under the proxy cache", d.workdir)
	}
}

func TestDevinIgnoresFingerprintConversationIDs(t *testing.T) {
	bin, dir := fakeDevin(t)
	d := NewDevin(&config.Model{Name: "devin", Type: config.TypeDevin, DevinBin: bin, Workdir: dir})

	// Two "users" sharing a fingerprint id must never share a session.
	out, err := d.Complete(context.Background(), devinReq("fp-abcd1234", "hello"))
	if err != nil {
		t.Fatal(err)
	}
	if out != "fresh: hello" {
		t.Fatalf("turn1 = %q", out)
	}
	out, err = d.Complete(context.Background(), devinReq("fp-abcd1234", "hello"))
	if err != nil {
		t.Fatal(err)
	}
	if out != "fresh: hello" {
		t.Fatalf("fingerprint id resumed a session: %q", out)
	}
	if len(d.sessions) != 0 {
		t.Fatalf("fingerprint id created session mapping: %v", d.sessions)
	}
}
