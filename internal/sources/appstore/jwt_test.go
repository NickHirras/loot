package appstore_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/nickhirras/loot/internal/sources/appstore"
)

// newKey returns a fresh P-256 key, the curve App Store Connect issues.
func newKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	return key
}

// writeKeyFile writes key as a PKCS#8 PEM, the shape of an AuthKey_*.p8.
func writeKeyFile(t *testing.T, dir string, key *ecdsa.PrivateKey) string {
	t.Helper()
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	path := dir + "/AuthKey_TEST12345.p8"
	if err := writeFile(t, path, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})); err != nil {
		t.Fatalf("write key: %v", err)
	}
	return path
}

func TestSignJWTClaimsAndSignature(t *testing.T) {
	key := newKey(t)
	issuedAt := time.Date(2026, 8, 18, 9, 0, 0, 0, time.UTC)

	token, err := appstore.SignJWT(key, "2X9R4HXF34", "11111111-2222-3333-4444-555555555555", issuedAt, 15*time.Minute)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("token has %d parts, want 3", len(parts))
	}

	var header struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
		Typ string `json:"typ"`
	}
	decodeSegment(t, parts[0], &header)
	if header.Alg != "ES256" || header.Typ != "JWT" || header.Kid != "2X9R4HXF34" {
		t.Errorf("header = %+v", header)
	}

	var claims struct {
		Iss string `json:"iss"`
		Iat int64  `json:"iat"`
		Exp int64  `json:"exp"`
		Aud string `json:"aud"`
	}
	decodeSegment(t, parts[1], &claims)
	if claims.Iss != "11111111-2222-3333-4444-555555555555" {
		t.Errorf("iss = %q", claims.Iss)
	}
	if claims.Aud != "appstoreconnect-v1" {
		t.Errorf("aud = %q, want appstoreconnect-v1", claims.Aud)
	}
	if claims.Iat != issuedAt.Unix() {
		t.Errorf("iat = %d, want %d", claims.Iat, issuedAt.Unix())
	}
	if got := claims.Exp - claims.Iat; got != int64(15*time.Minute/time.Second) {
		t.Errorf("token lives %ds, want 900 (Apple rejects anything over 20 minutes)", got)
	}

	// The signature must be the raw R‖S pair, 64 bytes for P-256 — not DER.
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatalf("decode signature: %v", err)
	}
	if len(sig) != 64 {
		t.Fatalf("signature is %d bytes, want 64 (raw R||S, not ASN.1 DER)", len(sig))
	}

	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	r := new(big.Int).SetBytes(sig[:32])
	s := new(big.Int).SetBytes(sig[32:])
	if !ecdsa.Verify(&key.PublicKey, digest[:], r, s) {
		t.Fatal("signature does not verify against the signing key")
	}
}

func TestParsePrivateKey(t *testing.T) {
	key := newKey(t)
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	armoured := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})

	parsed, err := appstore.ParsePrivateKey(armoured)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !parsed.Equal(key) {
		t.Fatal("parsed key differs from the original")
	}

	if _, err := appstore.ParsePrivateKey([]byte("not a key")); err == nil {
		t.Error("expected an error for a file that is not PEM")
	}
}

func TestLoadPrivateKeyMissingFile(t *testing.T) {
	if _, err := appstore.LoadPrivateKey(t.TempDir() + "/nope.p8"); err == nil {
		t.Fatal("expected an error for a missing key file")
	}
}

func decodeSegment(t *testing.T, segment string, into any) {
	t.Helper()
	raw, err := base64.RawURLEncoding.DecodeString(segment)
	if err != nil {
		t.Fatalf("decode segment: %v", err)
	}
	if err := json.Unmarshal(raw, into); err != nil {
		t.Fatalf("decode json: %v", err)
	}
}
