package integrations

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"

	luxlog "github.com/luxfi/log"
)

// stateKeyEnv is the operator-injected HMAC key (base64, >=32 bytes) that signs
// the OAuth state. Injected from KMS via KMSSecret. Absent → a random per-boot key
// (see resolveStateKey): the flow still works within one process lifetime, and the
// 10-minute state window makes a restart mid-flow a fail-safe retry, not a breach.
const stateKeyEnv = "CLOUD_INTEGRATIONS_STATE_KEY"

// minStateKeyLen is the floor for the signing key. HMAC-SHA256's block is 64
// bytes; 32 bytes of key is the standard 256-bit security floor and rejects a
// short/typo'd operator secret rather than signing weakly.
const minStateKeyLen = 32

// maxStateLen bounds the state token verify will even look at. A legitimate token
// (base64url payload + '.' + base64url of a 32-byte MAC) is ~160 bytes; 4 KiB is
// far above any real value and caps the base64 decode work a forged callback can
// force. The router/fasthttp already bound the request URI; this is the local
// belt-and-suspenders at the crypto boundary.
const maxStateLen = 4096

// stateTTL bounds how long a signed state is valid. A var (not const) so a test
// can force expiry deterministically; production never mutates it.
var stateTTL = 10 * time.Minute

// errBadState is the single opaque failure the handler maps to a labeled console
// redirect — every distinct verify failure (bad MAC, malformed, expired, wrong
// provider, bad org) returns it so the callback leaks no oracle to an attacker
// probing states.
var errBadState = errors.New("integrations: invalid state")

// statePayload is the CSRF + org-binding carried across the OAuth round trip. It
// is signed (HMAC-SHA256), NOT encrypted — org/provider are not secret; the MAC
// is what makes them unforgeable, and Nonce (single-use, in the store) is what
// makes a valid token non-replayable.
type statePayload struct {
	Org      string `json:"org"`
	Provider string `json:"provider"`
	Nonce    string `json:"nonce"`
	Exp      int64  `json:"exp"` // unix seconds
}

// resolveStateKey decodes the operator key or, absent/invalid, generates a random
// 32-byte key at boot and WARNs. It never returns nil — signing always has a key.
func resolveStateKey(b64 string, log luxlog.Logger) []byte {
	b64 = strings.TrimSpace(b64)
	if b64 != "" {
		if key, err := base64.StdEncoding.DecodeString(b64); err == nil && len(key) >= minStateKeyLen {
			return key
		}
		if log != nil {
			log.Warn("CLOUD_INTEGRATIONS_STATE_KEY is not valid base64 of >=32 bytes; using a random per-boot key")
		}
	}
	key := make([]byte, minStateKeyLen)
	if _, err := rand.Read(key); err != nil {
		// crypto/rand failure is catastrophic and vanishingly rare; a zero key is
		// never acceptable, so panic rather than sign with predictable bytes.
		panic("integrations: crypto/rand failed generating state key: " + err.Error())
	}
	if log != nil {
		log.Warn("CLOUD_INTEGRATIONS_STATE_KEY not set; using a random per-boot state key (set it from KMS for multi-replica / restart-stable flows)")
	}
	return key
}

// sign builds the state token: base64url(payload) + "." + base64url(HMAC(payload)).
// The MAC covers the base64url payload STRING exactly as it appears on the wire,
// so verify recomputes over the same bytes without a decode ambiguity.
func (s *svc) sign(org, provider, nonce string) (string, error) {
	raw, err := json.Marshal(statePayload{
		Org: org, Provider: provider, Nonce: nonce,
		Exp: time.Now().Add(stateTTL).Unix(),
	})
	if err != nil {
		return "", err
	}
	payloadB64 := base64.URLEncoding.EncodeToString(raw)
	return payloadB64 + "." + base64.URLEncoding.EncodeToString(s.mac(payloadB64)), nil
}

// verify authenticates a state token for the route's provider and returns the
// bound payload. Order: constant-time MAC check FIRST (so a tampered payload is
// rejected before it is ever parsed), then decode, then exp/provider/org checks.
// Every failure returns errBadState (no distinguishing oracle).
func (s *svc) verify(token, provider string) (statePayload, error) {
	// Bound the token before any work: a well-formed state is ~160 bytes, so a
	// multi-KB token is hostile. Rejecting first stops an attacker forcing a large
	// base64 allocation (the MAC-half decode) on every forged callback.
	if len(token) > maxStateLen {
		return statePayload{}, errBadState
	}
	dot := strings.IndexByte(token, '.')
	if dot <= 0 || dot == len(token)-1 {
		return statePayload{}, errBadState
	}
	payloadB64, macB64 := token[:dot], token[dot+1:]
	gotMAC, err := base64.URLEncoding.DecodeString(macB64)
	if err != nil {
		return statePayload{}, errBadState
	}
	if !hmac.Equal(gotMAC, s.mac(payloadB64)) {
		return statePayload{}, errBadState
	}
	raw, err := base64.URLEncoding.DecodeString(payloadB64)
	if err != nil {
		return statePayload{}, errBadState
	}
	var p statePayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return statePayload{}, errBadState
	}
	if p.Provider != provider {
		return statePayload{}, errBadState
	}
	if time.Now().Unix() > p.Exp {
		return statePayload{}, errBadState
	}
	if !validOrg(p.Org) || strings.TrimSpace(p.Nonce) == "" {
		return statePayload{}, errBadState
	}
	return p, nil
}

// mac computes HMAC-SHA256 over the payload string with the signing key.
func (s *svc) mac(payloadB64 string) []byte {
	h := hmac.New(sha256.New, s.stateKey)
	h.Write([]byte(payloadB64))
	return h.Sum(nil)
}
