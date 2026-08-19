package appstore

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"os"
	"sync"
	"time"
)

// App Store Connect authenticates with a short-lived ES256 JSON Web Token
// signed by the .p8 key downloaded from Users and Access > Integrations.
// Apple rejects a token whose lifetime exceeds 20 minutes; 15 is the value
// Apple's own sample code uses and leaves room for a slow clock.
const (
	// tokenAudience is fixed by Apple for every App Store Connect API token.
	tokenAudience = "appstoreconnect-v1"
	// tokenTTL is how long a minted token claims to be valid.
	tokenTTL = 15 * time.Minute
	// tokenRefreshLeeway re-mints a token slightly before it expires, so a
	// request that is in flight when the clock ticks over does not 401.
	tokenRefreshLeeway = 60 * time.Second
)

// jwtHeader is the JOSE header Apple expects. "typ" is required.
type jwtHeader struct {
	Alg string `json:"alg"`
	Kid string `json:"kid"`
	Typ string `json:"typ"`
}

// jwtClaims is the payload Apple expects. "scope" is optional and omitted:
// scoping a token to one path buys nothing here, since Loot only ever calls
// the sales reports endpoint.
type jwtClaims struct {
	Iss string `json:"iss"`
	Iat int64  `json:"iat"`
	Exp int64  `json:"exp"`
	Aud string `json:"aud"`
}

// LoadPrivateKey reads an AuthKey_<KeyID>.p8 file from disk.
func LoadPrivateKey(path string) (*ecdsa.PrivateKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("appstore: read private key: %w", err)
	}
	key, err := ParsePrivateKey(data)
	if err != nil {
		return nil, fmt.Errorf("appstore: %s: %w", path, err)
	}
	return key, nil
}

// ParsePrivateKey decodes a PEM-armoured PKCS#8 EC P-256 private key, which is
// what App Store Connect hands out as a .p8 file. Exported so tests and future
// callers can parse a key held in memory.
func ParsePrivateKey(pemBytes []byte) (*ecdsa.PrivateKey, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, fmt.Errorf("not a PEM file (an App Store Connect key starts with -----BEGIN PRIVATE KEY-----)")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse PKCS#8 private key: %w", err)
	}
	key, ok := parsed.(*ecdsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("private key is %T, want an ECDSA key", parsed)
	}
	if key.Curve != elliptic.P256() {
		return nil, fmt.Errorf("private key uses curve %s, want P-256", key.Curve.Params().Name)
	}
	return key, nil
}

// SignJWT mints an ES256 token for keyID/issuerID valid from issuedAt for ttl.
//
// ES256 signatures in a JWT are the raw R‖S pair, each left-padded to the
// curve's byte size — *not* the ASN.1 DER encoding crypto/ecdsa returns from
// SignASN1, which Apple rejects with a 401.
func SignJWT(key *ecdsa.PrivateKey, keyID, issuerID string, issuedAt time.Time, ttl time.Duration) (string, error) {
	if key == nil {
		return "", fmt.Errorf("appstore: no private key")
	}
	header, err := json.Marshal(jwtHeader{Alg: "ES256", Kid: keyID, Typ: "JWT"})
	if err != nil {
		return "", fmt.Errorf("appstore: encode jwt header: %w", err)
	}
	claims, err := json.Marshal(jwtClaims{
		Iss: issuerID,
		Iat: issuedAt.Unix(),
		Exp: issuedAt.Add(ttl).Unix(),
		Aud: tokenAudience,
	})
	if err != nil {
		return "", fmt.Errorf("appstore: encode jwt claims: %w", err)
	}

	signingInput := b64(header) + "." + b64(claims)
	digest := sha256.Sum256([]byte(signingInput))

	r, s, err := ecdsa.Sign(rand.Reader, key, digest[:])
	if err != nil {
		return "", fmt.Errorf("appstore: sign jwt: %w", err)
	}

	size := (key.Curve.Params().BitSize + 7) / 8
	sig := make([]byte, 2*size)
	r.FillBytes(sig[:size])
	s.FillBytes(sig[size:])

	return signingInput + "." + b64(sig), nil
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// tokenCache mints tokens lazily and reuses one until it is nearly expired.
// A poll makes several requests (one per report day), and Apple rate-limits
// aggressively enough that re-signing per request is worth avoiding.
type tokenCache struct {
	keyID    string
	issuerID string
	key      *ecdsa.PrivateKey

	mu      sync.Mutex
	token   string
	expires time.Time
}

// bearer returns a token valid at now, minting a new one when needed.
func (t *tokenCache) bearer(now time.Time) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.token != "" && now.Add(tokenRefreshLeeway).Before(t.expires) {
		return t.token, nil
	}
	tok, err := SignJWT(t.key, t.keyID, t.issuerID, now, tokenTTL)
	if err != nil {
		return "", err
	}
	t.token = tok
	t.expires = now.Add(tokenTTL)
	return tok, nil
}
