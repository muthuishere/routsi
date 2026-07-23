package backend

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/muthuishere/routsi/internal/api"
	"github.com/muthuishere/routsi/internal/config"
)

// Forward relays a request to an OpenAI-style upstream as raw bytes: rewrite
// `model` and auth, send, stream the response back untouched. Retries happen
// only before the first upstream byte reaches the client — once tokens flow
// we are committed (docs/research/README.md #1, production consensus).
type Forward struct {
	Model  *config.Model
	Client *http.Client
	// Retries on connect error / 429 / 5xx before first byte.
	Retries int
}

func NewForward(m *config.Model) *Forward {
	return &Forward{Model: m, Client: &http.Client{Timeout: 5 * time.Minute}, Retries: 2}
}

// rewriteBody swaps the model field and drops proxy-only fields, preserving
// every other field byte-for-byte semantically (unknown params pass through).
func (f *Forward) rewriteBody(req *api.ChatRequest) ([]byte, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(req.Raw, &m); err != nil {
		return nil, fmt.Errorf("rewrite body: %w", err)
	}
	name, _ := json.Marshal(f.Model.UpstreamModel)
	m["model"] = name
	delete(m, "conversation_id")
	return json.Marshal(m)
}

// Relay proxies the request and writes the upstream response (streaming or
// not) directly to w. Returns the upstream status for logging.
func (f *Forward) Relay(w http.ResponseWriter, r *http.Request, req *api.ChatRequest) (int, error) {
	body, err := f.rewriteBody(req)
	if err != nil {
		return 0, err
	}
	url := strings.TrimRight(f.Model.BaseURL, "/") + "/chat/completions"

	var resp *http.Response
	for attempt := 0; ; attempt++ {
		up, err := http.NewRequestWithContext(r.Context(), http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			return 0, err
		}
		up.Header.Set("Content-Type", "application/json")
		if f.Model.APIKeyEnv != "" {
			up.Header.Set("Authorization", "Bearer "+os.Getenv(f.Model.APIKeyEnv))
		}
		resp, err = f.Client.Do(up)
		retryable := err != nil || resp.StatusCode == 429 || resp.StatusCode >= 500
		if !retryable || attempt >= f.Retries {
			if err != nil {
				return 0, fmt.Errorf("upstream %s: %w", f.Model.Name, err)
			}
			break
		}
		if resp != nil {
			resp.Body.Close()
		}
		time.Sleep(time.Duration(1<<attempt) * 200 * time.Millisecond)
	}
	defer resp.Body.Close()

	if ct := resp.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	w.WriteHeader(resp.StatusCode)
	flusher, _ := w.(http.Flusher)
	buf := make([]byte, 32*1024)
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return resp.StatusCode, nil // client went away
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		if err == io.EOF {
			return resp.StatusCode, nil
		}
		if err != nil {
			return resp.StatusCode, err
		}
	}
}
