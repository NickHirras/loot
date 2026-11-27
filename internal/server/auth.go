package server

import (
	"crypto/subtle"
	"net/http"
	"strings"
)

// apiAuthMiddleware protects /api/* (except /api/healthz) and /ws with optional
// bearer or X-API-Key tokens configured under `api:`.
//
// When no tokens are configured, every request passes straight through — the
// same open-by-default behaviour Loot has always had for localhost and
// reverse-proxy deployments.
//
// When at least one token is configured:
//   - /api/healthz stays unauthenticated for container healthchecks.
//   - /hooks/* keeps its own per-source webhook authentication.
//   - Static dashboard files (/, /assets/*, …) stay untouched.
//   - /api/* and /ws accept `Authorization: Bearer <token>` or `X-API-Key: <token>`.
//   - Read tokens allow GET and HEAD (including GET /ws); other methods receive 403.
//   - Write tokens allow both read and write requests.
func (s *Server) apiAuthMiddleware(next http.Handler) http.Handler {
	if !s.Cfg.API.Configured() {
		return next
	}
	readTokens, writeTokens, err := s.Cfg.API.Resolve()
	if err != nil {
		// Fail closed if a configured token file cannot be read rather than
		// silently exposing the API.
		s.Logger.Error("api token load failed; failing closed", "error", err)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if isProtectedAPIPath(r.URL.Path) {
				writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "api authentication misconfigured"})
				return
			}
			next.ServeHTTP(w, r)
		})
	}
	if len(readTokens) == 0 && len(writeTokens) == 0 {
		return next
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isProtectedAPIPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}

		token := extractAPIToken(r)
		if token == "" {
			w.Header().Set("WWW-Authenticate", `Bearer realm="loot"`)
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
			return
		}

		if matchAnyToken(token, writeTokens) {
			next.ServeHTTP(w, r)
			return
		}
		if matchAnyToken(token, readTokens) {
			if r.Method == http.MethodGet || r.Method == http.MethodHead {
				next.ServeHTTP(w, r)
				return
			}
			writeJSON(w, http.StatusForbidden, map[string]any{"error": "read-only token cannot modify state"})
			return
		}

		w.Header().Set("WWW-Authenticate", `Bearer realm="loot"`)
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
	})
}

func isProtectedAPIPath(path string) bool {
	if path == "/api/healthz" {
		return false
	}
	return path == "/ws" || path == "/api" || strings.HasPrefix(path, "/api/")
}

// extractAPIToken reads a bearer token from Authorization or a raw key from
// X-API-Key. Accepting X-API-Key alongside Bearer lets Loot tokens coexist with
// an authenticating reverse proxy that uses Authorization: Basic.
func extractAPIToken(r *http.Request) string {
	if auth := strings.TrimSpace(r.Header.Get("Authorization")); auth != "" {
		fields := strings.SplitN(auth, " ", 2)
		if len(fields) == 2 && strings.EqualFold(fields[0], "Bearer") {
			if tok := strings.TrimSpace(fields[1]); tok != "" {
				return tok
			}
		}
	}
	if key := strings.TrimSpace(r.Header.Get("X-API-Key")); key != "" {
		return key
	}
	return ""
}

func matchAnyToken(presented string, allowed []string) bool {
	matched := 0
	pBytes := []byte(presented)
	for _, candidate := range allowed {
		cBytes := []byte(candidate)
		if len(pBytes) == len(cBytes) && subtle.ConstantTimeCompare(pBytes, cBytes) == 1 {
			matched = 1
		}
	}
	return matched == 1
}
