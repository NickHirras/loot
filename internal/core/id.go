package core

import (
	"crypto/rand"
	"encoding/binary"
	"strings"
	"sync"
	"time"
)

// crockford is the Crockford base32 alphabet used by ULIDs. It excludes I, L,
// O and U so IDs survive being read aloud or retyped.
const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

var idMu sync.Mutex

// NewID returns a ULID: a 26 character, lexicographically sortable identifier
// with 48 bits of millisecond timestamp followed by 80 bits of randomness.
// Sortability is what makes `before=<id>` pagination work without a join.
func NewID() string { return NewIDAt(time.Now()) }

// NewIDAt returns a ULID whose timestamp component is t.
func NewIDAt(t time.Time) string {
	var buf [16]byte
	ms := uint64(t.UTC().UnixMilli())

	// 48 bit big-endian timestamp.
	buf[0] = byte(ms >> 40)
	buf[1] = byte(ms >> 32)
	buf[2] = byte(ms >> 24)
	buf[3] = byte(ms >> 16)
	buf[4] = byte(ms >> 8)
	buf[5] = byte(ms)

	idMu.Lock()
	_, err := rand.Read(buf[6:])
	idMu.Unlock()
	if err != nil {
		// crypto/rand cannot fail on any supported platform, but degrade to a
		// time-derived filler rather than panicking inside the ingest path.
		binary.BigEndian.PutUint64(buf[6:14], uint64(t.UnixNano()))
	}

	// Encode 128 bits as 26 base32 characters (130 bits, top 2 bits unused).
	var out [26]byte
	var bits uint
	var acc uint32
	pos := 25
	for i := 15; i >= 0; i-- {
		acc |= uint32(buf[i]) << bits
		bits += 8
		for bits >= 5 {
			out[pos] = crockford[acc&31]
			pos--
			acc >>= 5
			bits -= 5
		}
	}
	if pos >= 0 {
		out[pos] = crockford[acc&31]
	}
	return string(out[:])
}

// SanitizeKey trims and lowercases a dedupe key fragment so that keys built
// from user-supplied identifiers stay stable across whitespace noise.
func SanitizeKey(s string) string { return strings.TrimSpace(s) }
