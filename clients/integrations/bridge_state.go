package integrations

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"strconv"
	"strings"
	"sync"
	"time"
)

// bridge_state.go holds the PLATFORM-AGNOSTIC signed-state primitive the non-Slack
// account-link flows share: a signed, TTL'd, single-use "subject state". It is
// provider-blind — Discord's (guild,user), Teams' (tenant,user) and Telegram's
// (chat,user) link all compose their subject and sign it here. Orthogonal to the
// OAuth-connect state in state.go (statePayload{org,provider,nonce}); both HMAC with
// the SAME s.State.stateKey — one signing key, distinct named subjects.
//
// The signing/verifying algebra is the generalized twin of slack_verify.go's
// signSlackSubject/verifySlackSubject. The shared low-level primitives both compose —
// the constant-time hmacB64URL, the single-use seenSet, abs64 — live ONCE HERE
// (provider-agnostic) and are reused by the Slack primitives too (no redeclaration).

// linkStateTTLSec is the account-link state lifetime — the browser legs must
// complete within it. It is ALSO the single-use seen-set TTL (bridgeSeen), so a
// signed link state redeems exactly once within its validity.
const linkStateTTLSec = 60 * 10 // 10 minutes

// signSubject binds an opaque subject into a signed, TTL'd, single-use-noncable
// state:
//
//	base64url("<subject>.<exp>.<nonce>") + "." + base64url(hmac)
//
// The subject MUST NOT contain '.' (the field separator). Returns an error only if
// the CSPRNG fails — a predictable nonce would let a redeemed state replay once its
// seen-set entry expires. `now`=0 → time.Now.
func signSubject(key []byte, subject string, now int64) (string, error) {
	if now == 0 {
		now = time.Now().Unix()
	}
	exp := now + linkStateTTLSec
	nonce, err := genToken() // 128-bit CSPRNG hex (integrations.genToken)
	if err != nil {
		return "", err
	}
	payload := base64.RawURLEncoding.EncodeToString(
		[]byte(subject + "." + strconv.FormatInt(exp, 10) + "." + nonce))
	return payload + "." + hmacB64URL(key, payload), nil
}

// verifySubject verifies a signed subject state and returns the bound subject +
// single-use nonce. The constant-time MAC is checked BEFORE the payload is parsed
// (a tampered payload never reaches the split/parse). ok=false on any MAC / format
// / expiry failure. `now`=0 → time.Now.
func verifySubject(key []byte, state string, now int64) (subject, nonce string, ok bool) {
	dot := strings.LastIndexByte(state, '.')
	if dot <= 0 {
		return "", "", false
	}
	payload := state[:dot]
	mac := state[dot+1:]
	if !hmac.Equal([]byte(mac), []byte(hmacB64URL(key, payload))) {
		return "", "", false
	}
	decoded, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return "", "", false
	}
	parts := strings.SplitN(string(decoded), ".", 3)
	if len(parts) != 3 || parts[0] == "" || parts[2] == "" {
		return "", "", false
	}
	exp, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return "", "", false
	}
	if now == 0 {
		now = time.Now().Unix()
	}
	if now > exp {
		return "", "", false
	}
	return parts[0], parts[2], true
}

// ── in-process single-use seen-set (link-state nonces) ──────────────────────

// seenSet is an age-based single-use / seen-set with an atomic test-and-set. It
// backs every platform's per-user link single-use guarantee (bridgeSeen for the
// generalized adapters, slackUsedStates for Slack): a signed link state is redeemed
// exactly once within its TTL.
//
// SCOPE: per-PROCESS. In a multi-replica deployment the single-use guarantee here
// is DEFENSE-IN-DEPTH: the PRIMARY single-use guarantee is the platform's own
// server-side single-use OAuth `code` (a second exchange of the same code fails at
// the platform) AND hanzo.id's single-use OIDC `code`, plus the state's HMAC +
// browser-bound cookie + short TTL. Eviction is strictly age-based (a within-TTL
// entry is NEVER evicted), which is what forbids an evict-then-replay attack; memory
// is bounded temporally, not by count.
type seenSet struct {
	mu    sync.Mutex
	ttl   time.Duration
	at    map[string]time.Time
	order []string // insertion order, for age-based pruning
}

func newSeenSet(ttl time.Duration) *seenSet {
	return &seenSet{ttl: ttl, at: make(map[string]time.Time)}
}

// prune drops expired entries oldest-first, stopping at the first still-fresh one.
// Caller holds the lock.
func (s *seenSet) prune(now time.Time) {
	i := 0
	for ; i < len(s.order); i++ {
		k := s.order[i]
		t, ok := s.at[k]
		if !ok {
			continue // already removed via a re-insert
		}
		if now.Sub(t) > s.ttl {
			delete(s.at, k)
		} else {
			break
		}
	}
	if i > 0 {
		s.order = append(s.order[:0], s.order[i:]...)
	}
}

// seenAndAdd atomically tests-and-sets: returns true if k was already seen (a
// duplicate/replay); otherwise records it and returns false. The empty key is
// non-dedupable (always unique). `now` zero → time.Now.
func (s *seenSet) seenAndAdd(k string, now time.Time) bool {
	if k == "" {
		return false
	}
	if now.IsZero() {
		now = time.Now()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.prune(now)
	if _, ok := s.at[k]; ok {
		return true
	}
	s.at[k] = now
	s.order = append(s.order, k)
	return false
}

// ── shared low-level helpers ─────────────────────────────────────────────────

// hmacB64URL is the constant-time-composable HMAC-SHA256 of payload under key,
// base64url-encoded. Composed by every signed-state primitive (bridge + Slack).
func hmacB64URL(key []byte, payload string) string {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func abs64(x int64) int64 {
	if x < 0 {
		return -x
	}
	return x
}
