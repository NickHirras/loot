package snapcraft

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// The discharge Ubuntu One hands back from /api/v2/tokens/refresh: a real,
// differently signed macaroon, so a header built from it is distinguishable
// from one built from the exported discharge.
var (
	freshSig          = sigOf("fresh-discharge")
	freshDischargeMac = testMacaroon("login.ubuntu.com", "discharge-id", freshSig)
)

// wantFreshHeader is the Authorization header the store must see *after* a
// refresh: the same root, and the refreshed discharge bound to it. The binding
// is recomputed here rather than taken from bindDischarge, so this asserts the
// retry really rebound rather than passing the new discharge through unbound.
func wantFreshHeader(t *testing.T) string {
	t.Helper()
	m, err := parseMacaroon(freshDischargeMac)
	if err != nil {
		t.Fatalf("parse fresh discharge: %v", err)
	}
	m.setSignature(wantBoundSignature(rootSig, freshSig))
	return fmt.Sprintf("Macaroon root=%s, discharge=%s", rootMac, m.serialize())
}

// loginOrigin is the fingerprint the test login file's credentials carry.
func loginOrigin(t *testing.T) string {
	t.Helper()
	c, err := parseLogin([]byte(legacyINI()))
	if err != nil {
		t.Fatalf("parseLogin: %v", err)
	}
	return c.Origin
}

// TestPollRefreshesStaleDischargeAndRetries is the production failure: the root
// macaroon is good until 2027, but ~39 hours after the export the store starts
// answering `401 Expired macaroon (age: …) (macaroon-needs-refresh)`. Loot must
// do what the snapcraft CLI does — ask Ubuntu One for a new discharge and retry
// — rather than tell the user to export again.
func TestPollRefreshesStaleDischargeAndRetries(t *testing.T) {
	store := &storeServer{
		t:           t,
		metricsBody: fixtureBody(t),
		staleAuth:   wantHeader(t),
		refreshWith: freshDischargeMac,
	}
	srv := store.start()
	src := testSource(t, srv.URL, 30)

	events, raw, err := src.Poll(context.Background(), nil)
	if err != nil {
		t.Fatalf("poll: %v", err)
	}
	if len(events) == 0 {
		t.Fatal("poll emitted nothing; the retry after the refresh did not succeed")
	}

	_, _, refreshCalls, staleHits := store.counts()
	if refreshCalls != 1 {
		t.Errorf("refresh calls = %d, want 1", refreshCalls)
	}
	// Exactly one request may be spent discovering the discharge is stale: the
	// refreshed credential is swapped into the caller's own copy, so the rest
	// of the poll must not collect 401s of its own.
	if staleHits != 1 {
		t.Errorf("requests rejected as stale = %d, want 1", staleHits)
	}

	// SSO is sent the *unbound* discharge — the one from the login file, not
	// the bound copy that goes in the Authorization header.
	if store.refreshSent != dischargeMac {
		t.Errorf("refresh sent %q, want the unbound discharge %q", store.refreshSent, dischargeMac)
	}
	if store.refreshBound {
		t.Error("refresh sent a bound discharge; SSO refreshes the unbound one")
	}

	// And the retry must carry a header bound from the NEW discharge.
	if store.lastAuth != wantFreshHeader(t) {
		t.Errorf("authorization after refresh = %q\nwant %q", store.lastAuth, wantFreshHeader(t))
	}
	if store.lastAuth == wantHeader(t) {
		t.Error("the store was still sent the stale credential after a refresh")
	}
	if strings.Contains(store.lastAuth, "discharge="+freshDischargeMac) {
		t.Error("the refreshed discharge was sent unbound")
	}

	// The refreshed discharge is persisted, so a restart does not begin with
	// another 401. The login file is the user's export and is left alone.
	var st state
	if err := json.Unmarshal(raw, &st); err != nil {
		t.Fatalf("state: %v", err)
	}
	if st.RefreshedDischarge != freshDischargeMac {
		t.Errorf("state refreshed_discharge = %q, want %q", st.RefreshedDischarge, freshDischargeMac)
	}
	if st.RefreshedFor != loginOrigin(t) {
		t.Errorf("state refreshed_for = %q, want the login fingerprint %q", st.RefreshedFor, loginOrigin(t))
	}
	onDisk, err := os.ReadFile(src.cfg.LoginPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(onDisk) != legacyINI() {
		t.Error("the login file was rewritten; it is the user's export and must not be touched")
	}
}

// TestPollReusesRefreshedDischargeFromState covers the restart: a new process
// with the same login file must start from the discharge the last one
// refreshed. SSO here refuses to refresh, so the poll can only succeed if the
// state's discharge was used from the first request.
func TestPollReusesRefreshedDischargeFromState(t *testing.T) {
	store := &storeServer{
		t:           t,
		metricsBody: fixtureBody(t),
		staleAuth:   wantHeader(t),
	}
	srv := store.start()
	src := testSource(t, srv.URL, 30)

	prev, err := json.Marshal(state{
		SnapIDs:            map[string]string{},
		Cursor:             map[string]string{},
		RefreshedDischarge: freshDischargeMac,
		RefreshedFor:       loginOrigin(t),
	})
	if err != nil {
		t.Fatal(err)
	}

	events, raw, err := src.Poll(context.Background(), prev)
	if err != nil {
		t.Fatalf("poll: %v", err)
	}
	if len(events) == 0 {
		t.Fatal("poll emitted nothing")
	}
	_, _, refreshCalls, staleHits := store.counts()
	if refreshCalls != 0 {
		t.Errorf("refresh calls = %d; the persisted discharge should have been used as-is", refreshCalls)
	}
	if staleHits != 0 {
		t.Errorf("%d requests went out with the login file's stale discharge", staleHits)
	}
	if store.lastAuth != wantFreshHeader(t) {
		t.Errorf("authorization = %q\nwant the persisted discharge bound to the root %q",
			store.lastAuth, wantFreshHeader(t))
	}

	// And it survives into the next state blob.
	var st state
	if err := json.Unmarshal(raw, &st); err != nil {
		t.Fatal(err)
	}
	if st.RefreshedDischarge != freshDischargeMac {
		t.Errorf("state dropped the refreshed discharge: %q", st.RefreshedDischarge)
	}
}

// TestRefreshFailureFallsBackToExportAdvice: when SSO itself refuses, the root
// really has expired or been revoked and there is nothing to do but export
// again — which is what the store's own 401 already says.
func TestRefreshFailureFallsBackToExportAdvice(t *testing.T) {
	store := &storeServer{
		t:           t,
		metricsBody: fixtureBody(t),
		staleAuth:   wantHeader(t),
		// refreshWith empty: SSO answers 401 invalid-credentials.
	}
	srv := store.start()
	src := testSource(t, srv.URL, 30)

	err := src.Check(context.Background())
	if err == nil {
		t.Fatal("check succeeded with an unrefreshable credential")
	}
	for _, want := range []string{"Expired macaroon", "snapcraft export-login"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
	if strings.Contains(err.Error(), "invalid-credentials") {
		t.Errorf("the SSO failure buried the store's advice: %v", err)
	}
	if _, _, refreshCalls, _ := store.counts(); refreshCalls != 1 {
		t.Errorf("refresh calls = %d, want 1", refreshCalls)
	}
}

// TestNoRefreshForOtherUnauthorized: a 401 that is not about a stale discharge
// is not something SSO can fix, and asking it only delays the honest error.
func TestNoRefreshForOtherUnauthorized(t *testing.T) {
	store := &storeServer{
		t:             t,
		metricsStatus: http.StatusUnauthorized,
		metricsBody: `{"error_list":[{"message":"Authorization required",` +
			`"code":"macaroon-authorization-required"}]}`,
		refreshWith: freshDischargeMac,
	}
	srv := store.start()
	src := testSource(t, srv.URL, 30)

	if err := src.Check(context.Background()); err == nil {
		t.Fatal("check succeeded on a 401")
	}
	if _, _, refreshCalls, _ := store.counts(); refreshCalls != 0 {
		t.Errorf("refresh calls = %d; macaroon-authorization-required is not a stale discharge", refreshCalls)
	}
}

// TestCheckRefreshesStaleDischarge: `loot check` must exercise the same path,
// so a discharge that went stale overnight reports a tick rather than sending
// the user off to re-export.
func TestCheckRefreshesStaleDischarge(t *testing.T) {
	store := &storeServer{
		t:           t,
		metricsBody: fixtureBody(t),
		staleAuth:   wantHeader(t),
		refreshWith: freshDischargeMac,
	}
	srv := store.start()
	src := testSource(t, srv.URL, 30)

	if err := src.Check(context.Background()); err != nil {
		t.Fatalf("check: %v", err)
	}
	_, _, refreshCalls, staleHits := store.counts()
	if refreshCalls != 1 {
		t.Errorf("refresh calls = %d, want 1", refreshCalls)
	}
	if staleHits != 1 {
		t.Errorf("requests rejected as stale = %d, want 1", staleHits)
	}
	if store.lastAuth != wantFreshHeader(t) {
		t.Errorf("authorization = %q, want the refreshed credential", store.lastAuth)
	}
}

// TestRefreshIsSingleFlight: Poll and Check share a Source, and `loot serve`
// can force a poll while one is running. Two callers that both meet a stale
// discharge must produce one SSO round trip, not two.
func TestRefreshIsSingleFlight(t *testing.T) {
	store := &storeServer{
		t:            t,
		metricsBody:  fixtureBody(t),
		staleAuth:    wantHeader(t),
		refreshWith:  freshDischargeMac,
		refreshDelay: 50 * time.Millisecond,
	}
	srv := store.start()
	src := testSource(t, srv.URL, 30)

	var wg sync.WaitGroup
	errs := make([]error, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _, errs[0] = src.Poll(context.Background(), nil)
	}()
	go func() {
		defer wg.Done()
		errs[1] = src.Check(context.Background())
	}()
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("caller %d: %v", i, err)
		}
	}
	if _, _, refreshCalls, _ := store.counts(); refreshCalls != 1 {
		t.Errorf("refresh calls = %d, want 1: the refresh is not single-flighted", refreshCalls)
	}
}

// TestRefreshedDischargeDroppedWhenLoginChanges: the state blob outlives the
// login file. A discharge refreshed for a previous export must not be sent
// after the user exports a new one.
func TestRefreshedDischargeDroppedWhenLoginChanges(t *testing.T) {
	store := &storeServer{t: t, metricsBody: fixtureBody(t)}
	srv := store.start()
	src := testSource(t, srv.URL, 30)

	// A discharge remembered for some other login file.
	src.seedDischarge(freshDischargeMac, "0000000000000000")

	auth, err := src.auth()
	if err != nil {
		t.Fatalf("auth: %v", err)
	}
	if auth.Header != wantHeader(t) {
		t.Errorf("auth used a discharge refreshed for a different login: %q", auth.Header)
	}
	if got, _ := src.dischargeSnapshot(); got != "" {
		t.Errorf("the mismatched discharge was kept: %q", got)
	}

	// A matching fingerprint, on the other hand, is honoured.
	src.seedDischarge(freshDischargeMac, loginOrigin(t))
	auth, err = src.auth()
	if err != nil {
		t.Fatalf("auth: %v", err)
	}
	if auth.Header != wantFreshHeader(t) {
		t.Errorf("auth = %q, want the refreshed credential %q", auth.Header, wantFreshHeader(t))
	}
}

// TestNeedsDischargeRefresh pins the trigger. Both shapes the store has been
// seen to use must fire, and nothing else may.
func TestNeedsDischargeRefresh(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "production 401",
			err: newStatusError(http.StatusUnauthorized, []byte(
				`{"error_list":[{"message":"Expired macaroon (age: 139631 seconds)","code":"macaroon-needs-refresh"}]}`)),
			want: true,
		},
		{
			name: "message only, no code",
			err:  newStatusError(http.StatusUnauthorized, []byte(`{"error_message":"Expired macaroon (age: 200000 seconds)"}`)),
			want: true,
		},
		{
			name: "authorization required",
			err: newStatusError(http.StatusUnauthorized, []byte(
				`{"error_list":[{"message":"Authorization required","code":"macaroon-authorization-required"}]}`)),
			want: false,
		},
		{
			name: "invalid credentials",
			err: newStatusError(http.StatusUnauthorized, []byte(
				`{"error_list":[{"message":"Invalid credentials","code":"invalid-credentials"}]}`)),
			want: false,
		},
		{
			name: "403 that mentions refresh",
			err: newStatusError(http.StatusForbidden, []byte(
				`{"error_list":[{"message":"macaroon-needs-refresh","code":"macaroon-permission-required"}]}`)),
			want: false,
		},
		{name: "not a status error", err: errors.New("dial tcp: connection refused"), want: false},
		{name: "nil", err: nil, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := needsDischargeRefresh(tc.err); got != tc.want {
				t.Errorf("needsDischargeRefresh(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// TestRefreshableOnlyForUbuntuOnePairs: a candid token has no discharge, and a
// snapcraft 7+ export that is already a finished header carries one that was
// bound at export time — SSO will not refresh either, so neither may be sent.
func TestRefreshableOnlyForUbuntuOnePairs(t *testing.T) {
	u1, err := parseLogin([]byte(legacyINI()))
	if err != nil {
		t.Fatal(err)
	}
	if !u1.refreshable() {
		t.Error("an ubuntu-one export is not refreshable")
	}

	header, err := parseLogin([]byte("Macaroon root=" + rootMac + ", discharge=" + dischargeMac))
	if err != nil {
		t.Fatal(err)
	}
	if header.refreshable() {
		t.Error("a pre-bound exported header was reported as refreshable")
	}

	candid, err := parseLogin([]byte(strings.Repeat("A", 120)))
	if err != nil {
		t.Fatal(err)
	}
	if candid.refreshable() {
		t.Error("a candid token was reported as refreshable")
	}
}

// TestWithDischargeRebinds: swapping a discharge must rebind it to the same
// root and keep the credential's provenance, including which login file it
// came from.
func TestWithDischargeRebinds(t *testing.T) {
	c, err := parseLogin([]byte(legacyINI()))
	if err != nil {
		t.Fatal(err)
	}
	got, err := c.withDischarge(freshDischargeMac)
	if err != nil {
		t.Fatalf("withDischarge: %v", err)
	}
	if got.Header != wantFreshHeader(t) {
		t.Errorf("header = %q\nwant %q", got.Header, wantFreshHeader(t))
	}
	if got.Discharge != freshDischargeMac || got.Root != rootMac {
		t.Errorf("credential pair = %q / %q", got.Root, got.Discharge)
	}
	if got.Origin != c.Origin {
		t.Errorf("origin changed on refresh: %q -> %q; it names the login file, "+
			"which a refresh does not change", c.Origin, got.Origin)
	}
	if got.Email != c.Email || got.Format != c.Format {
		t.Errorf("refresh lost provenance: %+v", got)
	}
}

// TestRefreshRejectsNonsense: SSO answering with something that is not a
// macaroon must not be swapped in — a bad discharge would fail every request
// until a restart.
func TestRefreshRejectsNonsense(t *testing.T) {
	for _, body := range []string{`{}`, `{"discharge_macaroon":"@@@ not base64"}`, `not json`} {
		store := &storeServer{t: t, metricsBody: fixtureBody(t), staleAuth: wantHeader(t)}
		srv := store.start()
		src := testSource(t, srv.URL, 30)
		// A stand-in Ubuntu One that answers 200 with a malformed body.
		src.AuthURL = badSSO(t, body)

		if err := src.Check(context.Background()); err == nil {
			t.Errorf("body %q: check succeeded", body)
		}
		if got, _ := src.dischargeSnapshot(); got != "" {
			t.Errorf("body %q: a bad discharge was adopted: %q", body, got)
		}
	}
}

// badSSO is a stand-in Ubuntu One that answers 200 with the given body.
func badSSO(t *testing.T, body string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}
