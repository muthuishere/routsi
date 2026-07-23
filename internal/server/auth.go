package server

import (
	"crypto/subtle"
	"net/http"
	"strings"
)

// authorized reports whether the request carries a valid bearer token. When no
// tokens are configured, auth is off and everything is allowed. Token is read
// from the Authorization header ("Bearer x") or a ?token= query param so a
// browser can open the dashboard with /?token=... .
func (s *Server) authorized(r *http.Request) bool {
	if len(s.tokens) == 0 {
		return true
	}
	tok := ""
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		tok = strings.TrimSpace(h[len("Bearer "):])
	} else if q := r.URL.Query().Get("token"); q != "" {
		tok = q
	}
	if tok == "" {
		return false
	}
	// Constant-time compare against each configured token.
	ok := false
	for _, want := range s.tokens {
		if subtle.ConstantTimeCompare([]byte(tok), []byte(want)) == 1 {
			ok = true
		}
	}
	return ok
}

// guard wraps h so it 401s unless the request is authorized. /health is never
// guarded (liveness probes); the dashboard shell (/) is open but its data
// endpoints (/stats,/metrics) and the API (/v1/*) are guarded.
func (s *Server) guard(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.authorized(r) {
			w.Header().Set("WWW-Authenticate", `Bearer realm="routsi"`)
			httpError(w, http.StatusUnauthorized, "missing or invalid API token")
			return
		}
		h(w, r)
	}
}
