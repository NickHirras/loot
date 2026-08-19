package snapcraft

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// packetize renders one libmacaroons v1 packet.
func packetize(key string, value []byte) []byte {
	size := 4 + len(key) + 1 + len(value) + 1
	out := []byte(fmt.Sprintf("%04x", size))
	out = append(out, key...)
	out = append(out, ' ')
	out = append(out, value...)
	out = append(out, '\n')
	return out
}

// testMacaroon builds a minimal but genuinely well-formed v1 macaroon.
func testMacaroon(location, identifier string, sig []byte) string {
	var b []byte
	b = append(b, packetize("location", []byte(location))...)
	b = append(b, packetize("identifier", []byte(identifier))...)
	b = append(b, packetize("signature", sig)...)
	return base64.RawURLEncoding.EncodeToString(b)
}

func sigOf(seed string) []byte {
	h := sha256.Sum256([]byte(seed))
	return h[:]
}

// wantBoundSignature computes the expected binding independently of the code
// under test: HMAC(0…0, HMAC(0…0, root) ‖ HMAC(0…0, discharge)).
func wantBoundSignature(rootSig, dischargeSig []byte) []byte {
	key := make([]byte, 32)
	mac := func(msg []byte) []byte {
		h := hmac.New(sha256.New, key)
		h.Write(msg)
		return h.Sum(nil)
	}
	return mac(append(mac(rootSig), mac(dischargeSig)...))
}

var (
	rootSig      = sigOf("root")
	dischargeSig = sigOf("discharge")
	rootMac      = testMacaroon("api.snapcraft.io", "root-id", rootSig)
	dischargeMac = testMacaroon("login.ubuntu.com", "discharge-id", dischargeSig)
)

func TestMacaroonRoundTrip(t *testing.T) {
	m, err := parseMacaroon(rootMac)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got := len(m.packets); got != 3 {
		t.Fatalf("packets = %d, want 3", got)
	}
	if m.packets[0].key != "location" || string(m.packets[0].value) != "api.snapcraft.io" {
		t.Errorf("location packet = %q %q", m.packets[0].key, m.packets[0].value)
	}
	sig, err := m.signature()
	if err != nil {
		t.Fatalf("signature: %v", err)
	}
	if string(sig) != string(rootSig) {
		t.Errorf("signature round-trip mismatch")
	}
	if m.serialize() != rootMac {
		t.Errorf("serialize is not the identity for an unchanged macaroon")
	}
}

func TestMacaroonRejectsGarbage(t *testing.T) {
	for _, in := range []string{"", "not base64 !!!", base64.RawURLEncoding.EncodeToString([]byte("zzzz"))} {
		if _, err := parseMacaroon(in); err == nil {
			t.Errorf("parseMacaroon(%q) succeeded, want error", in)
		}
	}
}

func TestBindDischarge(t *testing.T) {
	bound, err := bindDischarge(rootMac, dischargeMac)
	if err != nil {
		t.Fatalf("bind: %v", err)
	}
	m, err := parseMacaroon(bound)
	if err != nil {
		t.Fatalf("parse bound: %v", err)
	}
	got, err := m.signature()
	if err != nil {
		t.Fatalf("signature: %v", err)
	}
	want := wantBoundSignature(rootSig, dischargeSig)
	if string(got) != string(want) {
		t.Errorf("bound signature = %x, want %x", got, want)
	}
	// Everything except the signature must survive unchanged.
	if string(m.packets[0].value) != "login.ubuntu.com" || string(m.packets[1].value) != "discharge-id" {
		t.Errorf("binding disturbed the discharge's other packets: %+v", m.packets)
	}
	if bound == dischargeMac {
		t.Errorf("bound discharge is identical to the unbound one")
	}
}

// legacyINI is the shape `snapcraft export-login <file>` writes for Ubuntu One
// credentials (snapcraft <= 6): an INI file with the root macaroon and an
// *unbound* discharge.
func legacyINI() string {
	return "[login.ubuntu.com]\n" +
		"macaroon = " + rootMac + "\n" +
		"unbound_discharge = " + dischargeMac + "\n" +
		"email = dev@example.com\n"
}

func wantHeader(t *testing.T) string {
	t.Helper()
	bound, err := bindDischarge(rootMac, dischargeMac)
	if err != nil {
		t.Fatalf("bind: %v", err)
	}
	return fmt.Sprintf("Macaroon root=%s, discharge=%s", rootMac, bound)
}

func TestParseLoginUbuntuOneINI(t *testing.T) {
	c, err := parseLogin([]byte(legacyINI()))
	if err != nil {
		t.Fatalf("parseLogin: %v", err)
	}
	if c.Header != wantHeader(t) {
		t.Errorf("header = %q\nwant %q", c.Header, wantHeader(t))
	}
	if c.Email != "dev@example.com" {
		t.Errorf("email = %q", c.Email)
	}
	if !strings.Contains(c.Format, "ubuntu-one") {
		t.Errorf("format = %q", c.Format)
	}
	// The header must carry the *bound* discharge, not the exported one.
	if strings.Contains(c.Header, "discharge="+dischargeMac) {
		t.Errorf("header carries the unbound discharge")
	}
}

func TestParseLoginJSONExport(t *testing.T) {
	body := fmt.Sprintf(`{"macaroon": %q, "unbound_discharge": %q, "email": "dev@example.com"}`,
		rootMac, dischargeMac)
	c, err := parseLogin([]byte(body))
	if err != nil {
		t.Fatalf("parseLogin: %v", err)
	}
	if c.Header != wantHeader(t) {
		t.Errorf("header = %q", c.Header)
	}
}

func TestParseLoginSnapcraft7Base64Header(t *testing.T) {
	// snapcraft 7+ (craft-store) exports the already-bound credential as the
	// finished header value, and every published recipe base64-wraps it.
	header := "Macaroon root=" + rootMac + ", discharge=" + dischargeMac
	wrapped := base64.StdEncoding.EncodeToString([]byte(header))

	c, err := parseLogin([]byte(wrapped))
	if err != nil {
		t.Fatalf("parseLogin: %v", err)
	}
	if c.Header != header {
		t.Errorf("header = %q, want %q", c.Header, header)
	}
	if !strings.Contains(c.Format, "base64") {
		t.Errorf("format = %q, want it to mention base64", c.Format)
	}

	// The same thing unwrapped must work too.
	c2, err := parseLogin([]byte(header + "\n"))
	if err != nil {
		t.Fatalf("parseLogin unwrapped: %v", err)
	}
	if c2.Header != header {
		t.Errorf("unwrapped header = %q", c2.Header)
	}
}

func TestParseLoginBase64WrappedINI(t *testing.T) {
	wrapped := base64.StdEncoding.EncodeToString([]byte(legacyINI()))
	c, err := parseLogin([]byte("# exported by snapcraft\n" + wrapped + "\n"))
	if err != nil {
		t.Fatalf("parseLogin: %v", err)
	}
	if c.Header != wantHeader(t) {
		t.Errorf("header = %q", c.Header)
	}
}

func TestParseLoginCandidToken(t *testing.T) {
	token := strings.Repeat("A", 120)
	c, err := parseLogin([]byte(token + "\n"))
	if err != nil {
		t.Fatalf("parseLogin: %v", err)
	}
	if c.Header != "Macaroon "+token {
		t.Errorf("header = %q", c.Header)
	}
	if !strings.Contains(c.Format, "candid") {
		t.Errorf("format = %q", c.Format)
	}
}

func TestParseLoginErrors(t *testing.T) {
	cases := map[string]string{
		"empty":            "",
		"junk":             "hello there",
		"ini without keys": "[login.ubuntu.com]\nemail = dev@example.com\n",
		"broken macaroon":  "[login.ubuntu.com]\nmacaroon = @@@\nunbound_discharge = @@@\n",
	}
	for name, in := range cases {
		if _, err := parseLogin([]byte(in)); err == nil {
			t.Errorf("%s: parseLogin succeeded, want error", name)
		}
	}
}

func TestLoadLoginMissingFile(t *testing.T) {
	_, err := loadLogin(filepath.Join(t.TempDir(), "nope"))
	if err == nil {
		t.Fatal("want error")
	}
	if !strings.Contains(err.Error(), "export-login") {
		t.Errorf("error does not say how to fix it: %v", err)
	}
}

func TestLoadLoginReadsFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "snapcraft-login")
	if err := os.WriteFile(path, []byte(legacyINI()), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := loadLogin(path)
	if err != nil {
		t.Fatalf("loadLogin: %v", err)
	}
	if c.Header != wantHeader(t) {
		t.Errorf("header = %q", c.Header)
	}
}

// Seen in the wild with snapcraft 9.0.1: `export-login` writes base64 of
// {"t":"u1-macaroon","v":{"r":<root>,"d":<unbound discharge>}}. Before this
// was recognised the file fell through to the "bare candid token" branch and
// the store answered 401 macaroon-authorization-required.
func TestParseLoginSnapcraft9TypedExport(t *testing.T) {
	body := fmt.Sprintf(`{"t":"u1-macaroon","v":{"r":%q,"d":%q}}`, rootMac, dischargeMac)
	wrapped := base64.StdEncoding.EncodeToString([]byte(body))
	c, err := parseLogin([]byte(wrapped))
	if err != nil {
		t.Fatalf("parseLogin: %v", err)
	}
	if c.Header != wantHeader(t) {
		t.Errorf("header = %q", c.Header)
	}
	if !strings.Contains(c.Format, "u1-macaroon") {
		t.Errorf("format = %q", c.Format)
	}

	// And the candid flavour of the same envelope.
	candid := base64.StdEncoding.EncodeToString([]byte(`{"t":"macaroon","v":"MDAxY2xvY2F0aW9u"}`))
	c, err = parseLogin([]byte(candid))
	if err != nil {
		t.Fatalf("candid parseLogin: %v", err)
	}
	if c.Header != "Macaroon MDAxY2xvY2F0aW9u" {
		t.Errorf("candid header = %q", c.Header)
	}
}
