// Package attest holds the token a browser echoes to show that a change was
// asked for, and nothing else.
//
// It is a LEAF so that both halves of the control can reach it. The DECISION
// (which requests must show one) belongs to the request tier, because that is
// where the caller's credentials are read; the ROUTE that hands a browser its
// first token belongs to the account surface, because that is the caller's own
// surface. Those two live in packages that import each other's direction, so
// the value they share cannot live in either — it lives here, where both already
// look.
//
// TOKEN — base64url( ts_be64(8) || mac(16) ), where
//
//	mac = KeyedBLAKE3(key, domain \x00 uid \x00 org \x00 ts)[:16]  (luxfi/crypto).
//
// It is BOUND to the validated principal (X-User-Id + X-Org-Id) so a token minted
// for one identity cannot authorize a change as another, and it EXPIRES after TTL.
//
// THE KEY NO LONGER HAS TO BE SHARED. cloud.Intended admits a cookie-authenticated
// change on Sec-Fetch-Site, which the browser states on the request and script cannot
// set, so nothing outside this package reads a token and no two processes have to
// agree on a key. A process holding KeyEnv mints against it; one without mints
// against a random key of its own and checks only what it minted. Both are correct,
// because the token is no longer what admits the change.
package attest

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/luxfi/crypto/blake3"
)

const (
	domain   = "hanzo-console-csrf-v1"
	macLen   = 16             // 128-bit truncated BLAKE3 MAC — ample for a bound, expiring token
	TTL      = 12 * time.Hour // token lifetime; a client re-fetches on expiry/403
	skew     = 120            // seconds of future tolerance
	tokenLen = 8 + macLen     // ts || mac

	// Header is where a token rides. One name, so the client that stamps it and
	// the control that reads it cannot mean different headers.
	Header = "X-CSRF-Token"
)

// KeyEnv names the MAC key, and NOTHING REQUIRES IT ANY MORE.
//
// It was the value every process in the fleet had to hold the same copy of, so that
// a token minted at one address verified at another. cloud.Intended stopped reading
// tokens — it reads Sec-Fetch-Site, which the browser states on the request — so
// there is no cross-process agreement left to make. A process without it mints a key
// of its own and checks only its own tokens, which is now every process's situation
// and harms none of them.
//
// It stays because the mint route still answers, and a client that reads and echoes
// a token is not broken by being unchecked. Setting it does nothing; leaving it
// unset does nothing. That is the point: it was an agreement problem, and the fix
// was to stop needing agreement rather than to distribute the value more carefully.
const KeyEnv = "CONSOLE_CSRF_KEY"

func decodeKey() ([]byte, error) {
	raw := strings.TrimSpace(os.Getenv(KeyEnv))
	if raw == "" {
		return nil, fmt.Errorf("%s is unset, so this process holds a key nobody else does", KeyEnv)
	}
	if b, err := hex.DecodeString(raw); err == nil && len(b) == 32 {
		return b, nil
	}
	if b, err := base64.StdEncoding.DecodeString(raw); err == nil && len(b) == 32 {
		return b, nil
	}
	return nil, fmt.Errorf("%s is set but does not decode to 32 bytes of hex or base64", KeyEnv)
}

// Key mints and checks tokens. Two processes holding different keys accept none of
// each other's, which stopped mattering when the change stopped depending on the token.
type Key [32]byte

// The process-wide key, resolved once: a token is minted and checked by several
// registrations in one process, and a key per registration would leave a token
// minted by one unverifiable by the next.
var (
	once sync.Once
	held Key
)

// Process returns the key this process holds — the one from KeyEnv when it is set,
// else a random key it alone holds. Which of the two it is no longer changes what
// the process admits.
func Process() Key {
	once.Do(func() {
		if k, err := decodeKey(); err == nil {
			copy(held[:], k)
			return
		}
		if _, err := rand.Read(held[:]); err != nil {
			// crypto/rand failure is catastrophic; a zero key would be forgeable.
			panic("attest: cannot generate key: " + err.Error())
		}
	})
	return held
}

// mac computes the bound, truncated keyed-BLAKE3 MAC for (uid, org, ts).
func (k Key) mac(uid, org string, ts int64) []byte {
	var msg []byte
	msg = append(msg, domain...)
	msg = append(msg, 0)
	msg = append(msg, uid...)
	msg = append(msg, 0)
	msg = append(msg, org...)
	msg = append(msg, 0)
	var t [8]byte
	binary.BigEndian.PutUint64(t[:], uint64(ts))
	msg = append(msg, t[:]...)
	sum, err := blake3.KeyedHash(k[:], msg)
	if err != nil {
		// Only errors on a bad key length; Key is 32 bytes by construction.
		panic("attest: MAC: " + err.Error())
	}
	return sum[:macLen]
}

// Mint issues a token bound to (uid, org), valid for [TTL]. Returns the token and
// its lifetime in seconds.
func (k Key) Mint(uid, org string) (string, int64) {
	ts := time.Now().Unix()
	var out [tokenLen]byte
	binary.BigEndian.PutUint64(out[:8], uint64(ts))
	copy(out[8:], k.mac(uid, org, ts))
	return base64.RawURLEncoding.EncodeToString(out[:]), int64(TTL / time.Second)
}

// Valid checks a token against the CURRENT request's validated (uid, org) and its
// expiry, in constant time.
func (k Key) Valid(token, uid, org string) bool {
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(token))
	if err != nil || len(raw) != tokenLen {
		return false
	}
	ts := int64(binary.BigEndian.Uint64(raw[:8]))
	now := time.Now().Unix()
	if ts > now+skew || now-ts > int64(TTL/time.Second) {
		return false
	}
	return subtle.ConstantTimeCompare(raw[8:], k.mac(uid, org, ts)) == 1
}
