package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRegisterQueue(t *testing.T) {
	cases := []struct {
		name       string
		statusCode int
		body       string
		wantErr    bool
	}{
		{"ok", http.StatusOK, `{"ok":true,"queue":"alice"}`, false},
		{"conflict", http.StatusConflict, `name "alice" is reserved by a configured model`, true},
		{"bad request", http.StatusBadRequest, `register: JSON body {"name":"..."} required`, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var gotPath, gotMethod string
			var gotBody map[string]string
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotPath, gotMethod = r.URL.Path, r.Method
				_ = json.NewDecoder(r.Body).Decode(&gotBody)
				w.WriteHeader(c.statusCode)
				_, _ = w.Write([]byte(c.body))
			}))
			defer ts.Close()

			err := registerQueue(workerBase(ts.URL), "alice")
			if (err != nil) != c.wantErr {
				t.Fatalf("registerQueue() err = %v, wantErr %v", err, c.wantErr)
			}
			if gotMethod != http.MethodPost {
				t.Errorf("method = %q, want POST", gotMethod)
			}
			if gotPath != "/v1/workers/register" {
				t.Errorf("path = %q, want /v1/workers/register", gotPath)
			}
			if gotBody["name"] != "alice" {
				t.Errorf("body name = %q, want alice", gotBody["name"])
			}
		})
	}
}

func TestPollJob(t *testing.T) {
	t.Run("job returned", func(t *testing.T) {
		var gotPath string
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotPath = r.URL.Path + "?" + r.URL.RawQuery
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"job-1","messages":[{"role":"user","content":"hello"}]}`))
		}))
		defer ts.Close()

		job, ok, err := pollJob(workerBase(ts.URL), "alice", 25)
		if err != nil {
			t.Fatalf("pollJob() err = %v", err)
		}
		if !ok {
			t.Fatalf("pollJob() ok = false, want true")
		}
		if job.ID != "job-1" {
			t.Errorf("job.ID = %q, want job-1", job.ID)
		}
		if gotPath != "/v1/workers/alice/jobs?wait=25" {
			t.Errorf("path = %q, want /v1/workers/alice/jobs?wait=25", gotPath)
		}
	})

	t.Run("no content means idle", func(t *testing.T) {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		}))
		defer ts.Close()

		job, ok, err := pollJob(workerBase(ts.URL), "alice", 1)
		if err != nil {
			t.Fatalf("pollJob() err = %v, want nil", err)
		}
		if ok {
			t.Fatalf("pollJob() ok = true, want false")
		}
		if job.ID != "" {
			t.Errorf("job.ID = %q, want empty", job.ID)
		}
	})

	t.Run("server error", func(t *testing.T) {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte("boom"))
		}))
		defer ts.Close()

		_, ok, err := pollJob(workerBase(ts.URL), "alice", 1)
		if err == nil {
			t.Fatalf("pollJob() err = nil, want error")
		}
		if ok {
			t.Fatalf("pollJob() ok = true, want false")
		}
	})
}

func TestAnswerJob(t *testing.T) {
	cases := []struct {
		name       string
		statusCode int
		wantErr    bool
	}{
		{"ok", http.StatusOK, false},
		{"conflict (job expired/unknown)", http.StatusConflict, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var gotPath, gotMethod, gotContent string
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotPath, gotMethod = r.URL.Path, r.Method
				b, _ := io.ReadAll(r.Body)
				var body map[string]string
				_ = json.Unmarshal(b, &body)
				gotContent = body["content"]
				w.WriteHeader(c.statusCode)
				_, _ = w.Write([]byte(`{"ok":true}`))
			}))
			defer ts.Close()

			err := answerJob(workerBase(ts.URL), "alice", "job-1", "42")
			if (err != nil) != c.wantErr {
				t.Fatalf("answerJob() err = %v, wantErr %v", err, c.wantErr)
			}
			if gotMethod != http.MethodPost {
				t.Errorf("method = %q, want POST", gotMethod)
			}
			if gotPath != "/v1/workers/alice/jobs/job-1" {
				t.Errorf("path = %q, want /v1/workers/alice/jobs/job-1", gotPath)
			}
			if gotContent != "42" {
				t.Errorf("content = %q, want 42", gotContent)
			}
		})
	}
}

func TestWorkerBase(t *testing.T) {
	cases := map[string]string{
		"http://localhost:8080":  "http://localhost:8080",
		"http://localhost:8080/": "http://localhost:8080",
	}
	for in, want := range cases {
		if got := workerBase(in); got != want {
			t.Errorf("workerBase(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPromptFrom(t *testing.T) {
	msgs := []workerMessage{
		{Role: "system", Content: json.RawMessage(`"you are a bot"`)},
		{Role: "user", Content: json.RawMessage(`"first question"`)},
		{Role: "assistant", Content: json.RawMessage(`"first answer"`)},
		{Role: "user", Content: json.RawMessage(`"second question"`)},
	}
	if got := promptFrom(msgs); got != "second question" {
		t.Errorf("promptFrom() = %q, want %q", got, "second question")
	}
}
