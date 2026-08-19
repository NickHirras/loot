package snapcraft

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
)

// This file is just enough of libmacaroons to bind a discharge macaroon to its
// root, which is the one piece of cryptography the Snap Store's v1 API needs
// from a client.
//
// `snapcraft export-login` writes an *unbound* discharge. Sending it as-is is
// rejected: the store expects the discharge's signature to have been rebound to
// the root macaroon it is presented with, exactly as pymacaroons'
// `Macaroon.prepare_for_request` does. That operation only touches the
// signature field, so Loot deserializes the v1 packet stream, replaces one
// packet and re-serializes — no macaroon library, no caveat verification, and
// no new module dependency.
//
// Wire format (libmacaroons v1, base64url of a packet stream):
//
//	%04x<key><space><value>\n
//
// where the four hex digits are the total packet length *including* themselves.
// Keys are location, identifier, cid, vid, cl and signature; the signature
// value is 32 raw bytes rather than hex.

// macaroonHashSize is the length of a SHA-256 macaroon signature.
const macaroonHashSize = sha256.Size

// packet is one key/value record of the v1 serialization.
type packet struct {
	key   string
	value []byte
}

// macaroon is an ordered packet stream. Loot never inspects caveats, so the
// packets are kept verbatim and only the signature is ever rewritten.
type macaroon struct {
	packets []packet
}

// parseMacaroon decodes a base64 v1 macaroon. Padding and the standard/URL
// alphabets are all accepted, because different exporters have used all four
// combinations over the years.
func parseMacaroon(s string) (*macaroon, error) {
	raw, err := decodeBase64(s)
	if err != nil {
		return nil, fmt.Errorf("not base64: %w", err)
	}
	var m macaroon
	for i := 0; i < len(raw); {
		if len(raw)-i < 4 {
			return nil, errors.New("truncated packet header")
		}
		n, err := strconv.ParseUint(string(raw[i:i+4]), 16, 32)
		if err != nil {
			return nil, fmt.Errorf("bad packet length %q: %w", raw[i:i+4], err)
		}
		size := int(n)
		if size < 5 || i+size > len(raw) {
			return nil, fmt.Errorf("packet length %d overruns %d bytes", size, len(raw)-i)
		}
		body := raw[i+4 : i+size]
		sp := bytes.IndexByte(body, ' ')
		if sp < 0 {
			return nil, errors.New("packet has no key/value separator")
		}
		m.packets = append(m.packets, packet{
			key:   string(body[:sp]),
			value: bytes.TrimSuffix(body[sp+1:], []byte("\n")),
		})
		i += size
	}
	if len(m.packets) == 0 {
		return nil, errors.New("no packets")
	}
	if _, err := m.signature(); err != nil {
		return nil, err
	}
	return &m, nil
}

// signature returns the macaroon's current signature bytes.
func (m *macaroon) signature() ([]byte, error) {
	for _, p := range m.packets {
		if p.key == "signature" {
			return p.value, nil
		}
	}
	return nil, errors.New("macaroon has no signature packet")
}

// setSignature replaces the signature packet's value in place.
func (m *macaroon) setSignature(sig []byte) {
	for i := range m.packets {
		if m.packets[i].key == "signature" {
			m.packets[i].value = sig
			return
		}
	}
	m.packets = append(m.packets, packet{key: "signature", value: sig})
}

// serialize renders the packet stream back to base64url without padding, which
// is what pymacaroons emits and therefore what the store has always been sent.
func (m *macaroon) serialize() string {
	var buf bytes.Buffer
	for _, p := range m.packets {
		size := 4 + len(p.key) + 1 + len(p.value) + 1
		fmt.Fprintf(&buf, "%04x", size)
		buf.WriteString(p.key)
		buf.WriteByte(' ')
		buf.Write(p.value)
		buf.WriteByte('\n')
	}
	return base64.RawURLEncoding.EncodeToString(buf.Bytes())
}

// bindDischarge reproduces pymacaroons' prepare_for_request: the discharge's
// signature becomes HMAC(0…0, HMAC(0…0, rootSig) ‖ HMAC(0…0, dischargeSig)),
// and the rebound discharge is re-serialized. Both inputs are the base64
// strings straight out of the login file.
func bindDischarge(root, discharge string) (string, error) {
	rm, err := parseMacaroon(root)
	if err != nil {
		return "", fmt.Errorf("root macaroon: %w", err)
	}
	dm, err := parseMacaroon(discharge)
	if err != nil {
		return "", fmt.Errorf("discharge macaroon: %w", err)
	}
	rootSig, err := rm.signature()
	if err != nil {
		return "", err
	}
	dischargeSig, err := dm.signature()
	if err != nil {
		return "", err
	}
	dm.setSignature(bindSignature(rootSig, dischargeSig))
	return dm.serialize(), nil
}

// bindSignature is libmacaroons' macaroon_bind: hash2 under an all-zero key.
func bindSignature(rootSig, dischargeSig []byte) []byte {
	key := make([]byte, macaroonHashSize)
	combined := append(hmacSHA256(key, rootSig), hmacSHA256(key, dischargeSig)...)
	return hmacSHA256(key, combined)
}

func hmacSHA256(key, msg []byte) []byte {
	h := hmac.New(sha256.New, key)
	h.Write(msg)
	return h.Sum(nil)
}

// decodeBase64 accepts all four base64 flavours. Exporters have written
// url-safe unpadded (pymacaroons), url-safe padded, and standard padded.
func decodeBase64(s string) ([]byte, error) {
	encodings := []*base64.Encoding{
		base64.RawURLEncoding,
		base64.URLEncoding,
		base64.RawStdEncoding,
		base64.StdEncoding,
	}
	var lastErr error
	for _, enc := range encodings {
		if b, err := enc.DecodeString(s); err == nil {
			return b, nil
		} else {
			lastErr = err
		}
	}
	return nil, lastErr
}
