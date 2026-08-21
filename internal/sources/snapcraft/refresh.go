package snapcraft

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Ubuntu One discharge refresh.
//
// An exported Ubuntu One login is two macaroons: the *root*, minted by the Snap
// Store and valid until the `--expires` date given to `snapcraft export-login`,
// and the *discharge*, minted by Ubuntu One SSO to prove who the root belongs
// to. The two have completely different lifetimes. The root lives for months;
// the discharge goes stale in a day or two, after which the store answers
//
//	401 {"error_list":[{"message":"Expired macaroon (age: 139631 seconds)",
//	                    "code":"macaroon-needs-refresh"}]}
//
// That is not "your login expired" — the root is fine. It means "ask SSO for a
// fresh discharge", which is exactly what the snapcraft CLI does silently, so
// nobody using snapcraft ever sees it. Loot does the same rather than telling
// the user to re-export every 1.6 days.
//
// The protocol, verified against canonical/craft-store
// (craft_store/ubuntu_one_store_client.py `_refresh_token`, and the
// `tokens_refresh` path in craft_store/endpoints.py):
//
//	POST https://login.ubuntu.com/api/v2/tokens/refresh
//	Content-Type: application/json
//	{"discharge_macaroon": "<serialized *unbound* discharge>"}
//
//	200 {"discharge_macaroon": "<new serialized unbound discharge>"}
//
// The reply replaces the stored discharge and is bound to the root per request
// exactly as before (craft-store's `_get_authorization_header` deserializes the
// stored discharge and calls `prepare_for_request` on every call, which is what
// bindDischarge reproduces). Nothing about the root changes, and the login file
// on disk is never rewritten: it is the user's export, and the refreshed
// discharge lives in Loot's own source state instead.

// DefaultAuthURL is Ubuntu One SSO, snapcraft's UBUNTU_ONE_SSO_URL default.
const DefaultAuthURL = "https://login.ubuntu.com"

// tokensRefreshPath is craft-store's `tokens_refresh` endpoint.
const tokensRefreshPath = "/api/v2/tokens/refresh"

// refreshable reports whether these credentials carry the Ubuntu One pair a
// refresh needs. Candid tokens, and snapcraft 7+ exports that are already a
// finished header, do not: the latter carry a discharge that was bound at
// export time and SSO will not refresh a bound discharge.
func (c credentials) refreshable() bool {
	return c.Root != "" && c.Discharge != ""
}

// withDischarge returns a copy of c carrying a different unbound discharge,
// rebound to the same root. Origin is carried over rather than recomputed: it
// names the login file these credentials came from, and a refresh does not
// change which file that is.
func (c credentials) withDischarge(discharge string) (credentials, error) {
	out, err := buildUbuntuOne(c.Root, discharge, c.Format, c.Email, c.Expires)
	if err != nil {
		return credentials{}, err
	}
	out.Origin = c.Origin
	return out, nil
}

// needsDischargeRefresh reports whether err is the store saying "this discharge
// is stale", as opposed to any other 401. The code is the reliable signal; the
// message is checked too because the store has phrased this as plain
// `Expired macaroon (age: …)` on 401s that carried no code at all.
//
// Deliberately narrow: `macaroon-authorization-required`, `invalid-credentials`
// and friends mean the credential is wrong rather than stale, and asking SSO to
// refresh those only delays the honest error.
func needsDischargeRefresh(err error) bool {
	var se *statusError
	if !errors.As(err, &se) || se.Status != http.StatusUnauthorized {
		return false
	}
	lower := strings.ToLower(se.Code + " " + se.Message)
	return strings.Contains(lower, "needs-refresh") ||
		strings.Contains(lower, "expired macaroon") ||
		strings.Contains(lower, "macaroon has expired")
}

// refreshCredentials swaps a freshly minted discharge into auth.
//
// The mutex is held across the SSO call so that concurrent callers make one
// request rather than three: the scheduler drives Poll one cycle at a time, but
// `loot serve` can force a poll while another is running and Check shares the
// same Source, so the guard is not theoretical. A caller that arrives after
// someone else has already refreshed reuses that result instead of asking
// again.
func (s *Source) refreshCredentials(ctx context.Context, auth credentials) (credentials, error) {
	if !auth.refreshable() {
		return credentials{}, errors.New("snapcraft: these credentials carry no unbound discharge to refresh")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.discharge != "" && s.discharge != auth.Discharge {
		return auth.withDischarge(s.discharge)
	}
	fresh, err := s.fetchDischarge(ctx, auth.Discharge)
	if err != nil {
		return credentials{}, err
	}
	out, err := auth.withDischarge(fresh)
	if err != nil {
		return credentials{}, fmt.Errorf("snapcraft: refreshed discharge is unusable: %w", err)
	}
	s.discharge = fresh
	s.dischargeFor = auth.Origin
	return out, nil
}

// fetchDischarge performs the SSO round trip. Callers hold s.mu.
func (s *Source) fetchDischarge(ctx context.Context, unbound string) (string, error) {
	body, err := json.Marshal(struct {
		DischargeMacaroon string `json:"discharge_macaroon"`
	}{DischargeMacaroon: unbound})
	if err != nil {
		return "", fmt.Errorf("snapcraft: encode refresh request: %w", err)
	}
	url := s.authURL() + tokensRefreshPath
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("snapcraft: build refresh request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", userAgent)

	resp, err := s.client().Do(req)
	if err != nil {
		return "", fmt.Errorf("snapcraft: refresh discharge: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", fmt.Errorf("snapcraft: read refresh response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		// A 401 here is the real thing the old advice was written for: the
		// root macaroon has expired or been revoked, so SSO will not discharge
		// it again. The caller falls back to the store's own error, which
		// already says to export a new login.
		return "", fmt.Errorf("snapcraft: refresh discharge: %s: %d %s",
			url, resp.StatusCode, ssoErrorMessage(raw))
	}

	var out struct {
		DischargeMacaroon string `json:"discharge_macaroon"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("snapcraft: decode refresh response: %w", err)
	}
	if out.DischargeMacaroon == "" {
		return "", errors.New("snapcraft: refresh response carried no discharge_macaroon")
	}
	if _, err := parseMacaroon(out.DischargeMacaroon); err != nil {
		return "", fmt.Errorf("snapcraft: refreshed discharge is not a macaroon: %w", err)
	}
	return out.DischargeMacaroon, nil
}

// ssoErrorMessage pulls something readable out of an SSO error body, which is
// `{"code": …, "message": …}` when it is JSON at all.
func ssoErrorMessage(raw []byte) string {
	var payload struct {
		Message string `json:"message"`
		Code    string `json:"code"`
		Error   string `json:"error"`
	}
	if err := json.Unmarshal(raw, &payload); err == nil {
		switch {
		case payload.Message != "" && payload.Code != "":
			return fmt.Sprintf("%s (%s)", payload.Message, payload.Code)
		case payload.Message != "":
			return payload.Message
		case payload.Code != "":
			return payload.Code
		case payload.Error != "":
			return payload.Error
		}
	}
	msg := strings.TrimSpace(string(raw))
	if len(msg) > 200 {
		msg = msg[:200] + "…"
	}
	return msg
}

// fingerprint identifies the login file a refreshed discharge belongs to.
//
// Without it, a user who exports a new login would keep being authenticated
// with the discharge Loot refreshed for the *previous* export — the state blob
// outlives the file. The fingerprint is stored beside the refreshed discharge
// and compared against the file on every load; a mismatch throws the cached
// refresh away. It is a hash, not the credential, so it is safe in the state
// blob.
func fingerprint(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		fmt.Fprintf(h, "%d:%s", len(p), p)
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}
