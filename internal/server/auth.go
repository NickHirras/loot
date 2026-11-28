package server

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"html"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/nickhirras/loot/internal/config"
)

const (
	sessionCookieName = "loot_session"

	// Brute-force guard on POST /login: 10 failed attempts per IP per 10 minutes.
	loginMaxFailures   = 10
	loginFailureWindow = 10 * time.Minute
)

type authRuntime struct {
	web         config.ResolvedAuth
	readTokens  []string
	writeTokens []string
	limiter     *loginLimiter
	passCache   *verifiedPassCache
	loadErr     error
}

func (a *authRuntime) enabled() bool {
	return a.loadErr != nil || a.web.Enabled || len(a.readTokens) > 0 || len(a.writeTokens) > 0
}

func (s *Server) buildAuthRuntime() *authRuntime {
	rt := &authRuntime{
		limiter:   newLoginLimiter(),
		passCache: newVerifiedPassCache(),
	}
	if s.Cfg.Auth.Configured() {
		web, err := s.Cfg.Auth.Resolve()
		if err != nil {
			s.Logger.Error("website auth config load failed; failing closed", "error", err)
			rt.loadErr = err
			return rt
		}
		rt.web = web
	}
	if s.Cfg.API.Configured() {
		readToks, writeToks, err := s.Cfg.API.Resolve()
		if err != nil {
			s.Logger.Error("api token load failed; failing closed", "error", err)
			rt.loadErr = err
			return rt
		}
		rt.readTokens = readToks
		rt.writeTokens = writeToks
	}
	return rt
}

// authMiddleware enforces optional website login (signed HttpOnly cookie) and
// optional API token auth (Authorization: Bearer / X-API-Key) across Loot.
//
// When neither `auth:` nor `api:` is configured, every request passes straight
// through (open by default, for localhost, Tailscale, or proxy-managed auth).
func (s *Server) authMiddleware(rt *authRuntime, next http.Handler) http.Handler {
	if !rt.enabled() {
		return next
	}
	if rt.loadErr != nil {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if isAlwaysPublicPath(r.URL.Path) {
				next.ServeHTTP(w, r)
				return
			}
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "authentication misconfigured"})
		})
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		if isAlwaysPublicPath(p) {
			next.ServeHTTP(w, r)
			return
		}

		// API and WebSocket requests never redirect to /login; they always
		// answer with JSON 401/403 so scripts, CLIs and MCP servers fail cleanly.
		if isProtectedAPIPath(p) {
			role, presentedToken, ok := rt.authenticate(r)
			if !ok {
				w.Header().Set("WWW-Authenticate", `Bearer realm="loot"`)
				writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
				return
			}
			if role == "read" && r.Method != http.MethodGet && r.Method != http.MethodHead {
				writeJSON(w, http.StatusForbidden, map[string]any{"error": "read-only token cannot modify state"})
				return
			}
			// Guard cookie-authenticated mutations against cross-origin CSRF.
			if role == "session" && !presentedToken && isMutatingMethod(r.Method) && !sameOriginRequest(r) {
				writeJSON(w, http.StatusForbidden, map[string]any{"error": "cross-origin request rejected"})
				return
			}
			next.ServeHTTP(w, r)
			return
		}

		// Browser / SPA routes are gated only when website login (`auth:`) is enabled.
		if !rt.web.Enabled {
			next.ServeHTTP(w, r)
			return
		}

		if rt.hasValidSession(r) {
			next.ServeHTTP(w, r)
			return
		}

		// Unauthenticated browser visit -> redirect to /login.
		target := "/login"
		if r.Method == http.MethodGet && p != "/" && p != "/index.html" && !strings.HasPrefix(p, "/assets/") {
			nextPath := r.URL.RequestURI()
			if isSafeNextPath(nextPath) {
				target = "/login?next=" + url.QueryEscape(nextPath)
			}
		}
		http.Redirect(w, r, target, http.StatusSeeOther)
	})
}

// hasValidSession reports whether r carries a valid signed loot_session cookie.
func (rt *authRuntime) hasValidSession(r *http.Request) bool {
	if !rt.web.Enabled {
		return false
	}
	c, err := r.Cookie(sessionCookieName)
	if err != nil || c.Value == "" {
		return false
	}
	_, valid := verifySession(rt.web.SessionKey, c.Value, time.Now())
	return valid
}

// authenticate checks API and WebSocket credentials in strict precedence:
//  1. Explicit Bearer / X-API-Key token (if presented, never falls back)
//  2. Signed browser session cookie (`loot_session`)
//
// HTTP Basic Auth is intentionally NOT accepted here: bcrypt verification only
// runs on POST /login so stale browser Basic Auth headers from a former reverse
// proxy cannot burn CPU on every request or defeat /logout.
func (rt *authRuntime) authenticate(r *http.Request) (role string, presentedToken bool, ok bool) {
	if tok := extractAPIToken(r); tok != "" {
		if matchAnyToken(tok, rt.writeTokens) {
			return "write", true, true
		}
		if matchAnyToken(tok, rt.readTokens) {
			return "read", true, true
		}
		return "", true, false
	}

	if rt.hasValidSession(r) {
		return "session", false, true
	}
	return "", false, false
}

func (rt *authRuntime) verifyPassword(username, password string) bool {
	if !rt.web.Enabled {
		return false
	}
	normUser := strings.TrimSpace(username)
	cacheKey := rt.passCache.key(rt.web.SessionKey, normUser, password)
	if rt.passCache.Has(cacheKey) {
		return true
	}

	userOK := true
	if rt.web.Username != "" {
		u1 := sha256.Sum256([]byte(normUser))
		u2 := sha256.Sum256([]byte(rt.web.Username))
		userOK = subtle.ConstantTimeCompare(u1[:], u2[:]) == 1
	}

	passOK := false
	if rt.web.PasswordHash != "" {
		passOK = bcrypt.CompareHashAndPassword([]byte(rt.web.PasswordHash), []byte(password)) == nil
	} else if rt.web.Password != "" {
		p1 := sha256.Sum256([]byte(password))
		p2 := sha256.Sum256([]byte(rt.web.Password))
		passOK = subtle.ConstantTimeCompare(p1[:], p2[:]) == 1
	}
	if userOK && passOK {
		rt.passCache.Add(cacheKey)
		return true
	}
	return false
}

func (s *Server) handleAuthStatus(rt *authRuntime) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !rt.enabled() {
			writeJSON(w, http.StatusOK, map[string]any{
				"auth_enabled":  false,
				"authenticated": true,
				"role":          "open",
			})
			return
		}
		role, _, ok := rt.authenticate(r)
		resp := map[string]any{
			"auth_enabled":  rt.web.Enabled,
			"authenticated": ok,
		}
		if ok {
			resp["role"] = role
			if role == "session" {
				if c, err := r.Cookie(sessionCookieName); err == nil {
					if u, valid := verifySession(rt.web.SessionKey, c.Value, time.Now()); valid && u != "" {
						resp["username"] = u
					}
				}
			}
		}
		writeJSON(w, http.StatusOK, resp)
	}
}

func (s *Server) handleLoginGet(rt *authRuntime) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !rt.web.Enabled {
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}
		nextPath := sanitizeNextPath(r.URL.Query().Get("next"))
		if rt.hasValidSession(r) {
			http.Redirect(w, r, nextPath, http.StatusSeeOther)
			return
		}
		renderLoginPage(w, http.StatusOK, rt.web.Username != "", nextPath, "")
	}
}

func (s *Server) handleLoginPost(rt *authRuntime) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		wantsJSON := strings.Contains(strings.ToLower(r.Header.Get("Content-Type")), "application/json") ||
			strings.Contains(strings.ToLower(r.Header.Get("Accept")), "application/json")

		if !rt.web.Enabled {
			if wantsJSON {
				writeJSON(w, http.StatusOK, map[string]any{"ok": true})
				return
			}
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}

		ip := clientIP(r)
		if !rt.limiter.Allow(ip, time.Now()) {
			if wantsJSON {
				writeJSON(w, http.StatusTooManyRequests, map[string]any{"error": "too many login attempts; try again later"})
				return
			}
			renderLoginPage(w, http.StatusTooManyRequests, rt.web.Username != "", "/", "Too many failed attempts. Please wait a few minutes and try again.")
			return
		}

		var username, password, nextPath string
		if strings.Contains(strings.ToLower(r.Header.Get("Content-Type")), "application/json") {
			var body struct {
				Username string `json:"username"`
				Password string `json:"password"`
				Next     string `json:"next"`
			}
			if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<14)).Decode(&body); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid request body"})
				return
			}
			username = body.Username
			password = body.Password
			nextPath = sanitizeNextPath(body.Next)
		} else {
			r.Body = http.MaxBytesReader(w, r.Body, 1<<14)
			if err := r.ParseForm(); err != nil {
				renderLoginPage(w, http.StatusBadRequest, rt.web.Username != "", "/", "Invalid form submission.")
				return
			}
			username = r.FormValue("username")
			password = r.FormValue("password")
			nextPath = sanitizeNextPath(r.FormValue("next"))
		}

		if !rt.verifyPassword(username, password) {
			rt.limiter.RecordFailure(ip, time.Now())
			if wantsJSON {
				writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "invalid username or password"})
				return
			}
			renderLoginPage(w, http.StatusUnauthorized, rt.web.Username != "", nextPath, "Invalid username or password.")
			return
		}

		rt.limiter.Reset(ip)
		userLabel := rt.web.Username
		if userLabel == "" {
			userLabel = "owner"
		}
		exp := time.Now().UTC().Add(rt.web.SessionTTL)
		token := signSession(rt.web.SessionKey, userLabel, exp)

		http.SetCookie(w, &http.Cookie{
			Name:     sessionCookieName,
			Value:    token,
			Path:     "/",
			HttpOnly: true,
			Secure:   isHTTPS(r),
			SameSite: http.SameSiteLaxMode,
			Expires:  exp,
			MaxAge:   int(rt.web.SessionTTL.Seconds()),
		})

		if wantsJSON {
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "username": userLabel})
			return
		}
		http.Redirect(w, r, nextPath, http.StatusSeeOther)
	}
}

func (s *Server) handleLogout(rt *authRuntime) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{
			Name:     sessionCookieName,
			Value:    "",
			Path:     "/",
			HttpOnly: true,
			Secure:   isHTTPS(r),
			SameSite: http.SameSiteLaxMode,
			MaxAge:   -1,
			Expires:  time.Unix(0, 0).UTC(),
		})
		if strings.Contains(strings.ToLower(r.Header.Get("Accept")), "application/json") {
			writeJSON(w, http.StatusOK, map[string]any{"ok": true})
			return
		}
		http.Redirect(w, r, "/login", http.StatusSeeOther)
	}
}

// ------------------------------------------------------------------ session

type sessionPayload struct {
	U   string `json:"u"`
	Exp int64  `json:"exp"`
}

func signSession(key []byte, username string, exp time.Time) string {
	raw, _ := json.Marshal(sessionPayload{U: username, Exp: exp.Unix()})
	enc := base64.RawURLEncoding.EncodeToString(raw)
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(enc))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return enc + "." + sig
}

func verifySession(key []byte, cookieVal string, now time.Time) (string, bool) {
	enc, sig, ok := strings.Cut(cookieVal, ".")
	if !ok || enc == "" || sig == "" {
		return "", false
	}
	gotSig, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil {
		return "", false
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(enc))
	if !hmac.Equal(gotSig, mac.Sum(nil)) {
		return "", false
	}
	raw, err := base64.RawURLEncoding.DecodeString(enc)
	if err != nil {
		return "", false
	}
	var p sessionPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return "", false
	}
	if p.Exp <= now.Unix() {
		return "", false
	}
	return p.U, true
}

// ------------------------------------------------------------------ helpers

func isAlwaysPublicPath(path string) bool {
	if path == "/api/healthz" || path == "/login" || path == "/logout" || path == "/api/auth/status" {
		return true
	}
	if strings.HasPrefix(path, "/hooks/") {
		return true
	}
	// Static PWA manifest, icons and offline shell stay reachable so Android
	// WebAPK updates and browser icon fetches work cleanly before login.
	switch path {
	case "/manifest.webmanifest", "/apple-touch-icon.png", "/favicon.ico",
		"/favicon.svg", "/favicon-32.png", "/mask-icon.svg",
		"/icon-192.png", "/icon-512.png", "/icon-maskable-512.png",
		"/offline.html", "/sw.js":
		return true
	}
	return false
}

func isProtectedAPIPath(path string) bool {
	if path == "/api/healthz" || path == "/api/auth/status" {
		return false
	}
	return path == "/ws" || path == "/api" || strings.HasPrefix(path, "/api/")
}

func isMutatingMethod(m string) bool {
	return m != http.MethodGet && m != http.MethodHead && m != http.MethodOptions
}

func sameOriginRequest(r *http.Request) bool {
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return false
	}
	return strings.EqualFold(u.Host, r.Host)
}

func isHTTPS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")), "https")
}

func isSafeNextPath(p string) bool {
	return strings.HasPrefix(p, "/") && !strings.HasPrefix(p, "//") && !strings.HasPrefix(p, `/\`) && !strings.HasPrefix(p, "/login")
}

func sanitizeNextPath(p string) string {
	p = strings.TrimSpace(p)
	if isSafeNextPath(p) {
		return p
	}
	return "/"
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

// clientIP returns the caller's IP, honoring X-Forwarded-For only when the
// direct connection arrived from a loopback proxy (e.g. Caddy on 127.0.0.1).
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		if xff := strings.TrimSpace(r.Header.Get("X-Forwarded-For")); xff != "" {
			if first, _, _ := strings.Cut(xff, ","); strings.TrimSpace(first) != "" {
				return strings.TrimSpace(first)
			}
		}
	}
	return host
}

// verifiedPassCache memoizes successful bcrypt password checks using an
// HMAC-SHA256 digest of (username, password) keyed by the server's session key
// so raw passwords are never retained in memory and repeat logins do not burn
// CPU on high-cost bcrypt hashes (such as Caddy's default cost 14).
type verifiedPassCache struct {
	mu      sync.RWMutex
	entries map[[32]byte]struct{}
}

func newVerifiedPassCache() *verifiedPassCache {
	return &verifiedPassCache{entries: map[[32]byte]struct{}{}}
}

func (c *verifiedPassCache) key(secret []byte, username, password string) [32]byte {
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte(username))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write([]byte(password))
	var out [32]byte
	copy(out[:], mac.Sum(nil))
	return out
}

func (c *verifiedPassCache) Has(k [32]byte) bool {
	c.mu.RLock()
	_, ok := c.entries[k]
	c.mu.RUnlock()
	return ok
}

func (c *verifiedPassCache) Add(k [32]byte) {
	c.mu.Lock()
	if len(c.entries) >= 16 {
		clear(c.entries)
	}
	c.entries[k] = struct{}{}
	c.mu.Unlock()
}

// loginLimiter tracks recent failed login attempts per IP.
type loginLimiter struct {
	mu       sync.Mutex
	failures map[string][]time.Time
}

func newLoginLimiter() *loginLimiter {
	return &loginLimiter{failures: map[string][]time.Time{}}
}

func (l *loginLimiter) Allow(ip string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	kept := l.pruneLocked(ip, now)
	return len(kept) < loginMaxFailures
}

func (l *loginLimiter) RecordFailure(ip string, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	kept := l.pruneLocked(ip, now)
	l.failures[ip] = append(kept, now)
}

func (l *loginLimiter) Reset(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.failures, ip)
}

func (l *loginLimiter) pruneLocked(ip string, now time.Time) []time.Time {
	cutoff := now.Add(-loginFailureWindow)
	list := l.failures[ip]
	var kept []time.Time
	for _, ts := range list {
		if ts.After(cutoff) {
			kept = append(kept, ts)
		}
	}
	if len(kept) == 0 {
		delete(l.failures, ip)
	} else {
		l.failures[ip] = kept
	}
	return kept
}

// ------------------------------------------------------------------ login HTML

func renderLoginPage(w http.ResponseWriter, status int, askUsername bool, nextPath, errMsg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)

	var b strings.Builder
	b.WriteString(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<meta name="theme-color" content="#0b0d12">
<link rel="icon" type="image/svg+xml" href="/favicon.svg">
<title>Sign in · Loot</title>
<style>
  :root { color-scheme: dark; }
  * { box-sizing: border-box; }
  body {
    margin: 0;
    min-height: 100vh;
    display: grid;
    place-items: center;
    padding: 1.5rem;
    background: radial-gradient(circle at top, #171c28 0%, #0b0d12 65%);
    color: #e6e8ef;
    font: 15px/1.5 -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif;
  }
  .card {
    width: 100%;
    max-width: 360px;
    background: #121620;
    border: 1px solid #242b3d;
    border-radius: 14px;
    padding: 2rem;
    box-shadow: 0 24px 60px rgba(0, 0, 0, 0.45);
  }
  .brand {
    display: flex;
    align-items: center;
    gap: 0.55rem;
    margin-bottom: 1.5rem;
  }
  .gem { color: #ffd166; font-size: 1.35rem; }
  h1 { margin: 0; font-size: 1.25rem; font-weight: 600; letter-spacing: -0.01em; }
  .err {
    margin: 0 0 1rem;
    padding: 0.65rem 0.8rem;
    border-radius: 8px;
    background: rgba(239, 68, 68, 0.14);
    border: 1px solid rgba(239, 68, 68, 0.4);
    color: #fca5a5;
    font-size: 0.875rem;
  }
  label {
    display: block;
    font-size: 0.8rem;
    color: #9aa3b8;
    margin-bottom: 0.35rem;
  }
  input {
    width: 100%;
    padding: 0.65rem 0.8rem;
    margin-bottom: 1rem;
    border-radius: 8px;
    border: 1px solid #2b3347;
    background: #0b0d12;
    color: #e6e8ef;
    font: inherit;
    outline: none;
  }
  input:focus { border-color: #ffd166; }
  button {
    width: 100%;
    padding: 0.7rem 1rem;
    border: 0;
    border-radius: 8px;
    background: #ffd166;
    color: #141105;
    font: inherit;
    font-weight: 600;
    cursor: pointer;
  }
  button:hover { background: #ffdb85; }
</style>
</head>
<body>
  <main class="card">
    <div class="brand"><span class="gem" aria-hidden="true">◆</span><h1>Loot</h1></div>`)
	if errMsg != "" {
		b.WriteString(`<p class="err" role="alert">`)
		b.WriteString(html.EscapeString(errMsg))
		b.WriteString(`</p>`)
	}
	b.WriteString(`<form method="post" action="/login">`)
	b.WriteString(`<input type="hidden" name="next" value="`)
	b.WriteString(html.EscapeString(nextPath))
	b.WriteString(`">`)
	if askUsername {
		b.WriteString(`<label for="username">Username</label>`)
		b.WriteString(`<input id="username" name="username" type="text" autocomplete="username" required autofocus>`)
		b.WriteString(`<label for="password">Password</label>`)
		b.WriteString(`<input id="password" name="password" type="password" autocomplete="current-password" required>`)
	} else {
		b.WriteString(`<label for="password">Password</label>`)
		b.WriteString(`<input id="password" name="password" type="password" autocomplete="current-password" required autofocus>`)
	}
	b.WriteString(`<button type="submit">Sign in</button></form></main></body></html>`)
	_, _ = w.Write([]byte(b.String()))
}
