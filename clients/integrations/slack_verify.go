package integrations

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"strconv"
	"strings"
	"sync"
	"time"
)

// This file ports the team-go/pkg/slack security primitives (verify.go) into the
// unified cloud binary, unchanged in behavior:
//
//   - Slack request-signature verification: constant-time HMAC-SHA256 over the
//     EXACT raw body, with a strict 5-minute anti-replay timestamp window. The
//     same gate protects the Events webhook AND the slash-command endpoint.
//   - A signed, TTL'd, single-use "subject state" primitive used by the per-user
//     link flow for browser continuity (leg1↔leg2 nonce) and the (team,user)
//     link cookie. This is ORTHOGONAL to the OAuth-connect state in state.go
//     (statePayload{org,provider,nonce}); both HMAC with the SAME s.stateKey —
//     one signing key, two named subjects.

// ── Slack request signing (webhook + slash) ─────────────────────────────────

// slackSigVersion is Slack's request-signing version prefix. Slack computes
//
//	v0=HMAC_SHA256(signingSecret, "v0:<timestamp>:<rawBody>")
//
// and sends it in X-Slack-Signature with X-Slack-Request-Timestamp.
// https://api.slack.com/authentication/verifying-requests-from-slack
const slackSigVersion = "v0"

// slackMaxSkewSec is the anti-replay window: a request whose timestamp is older
// or newer than this is rejected even when the MAC is valid, so a captured
// request replayed past the window fails.
const slackMaxSkewSec = 60 * 5 // 5 minutes

// verifySlackSignature returns true iff the HMAC matches over the EXACT raw body
// AND the timestamp is within the replay window. Constant-time compare; never
// panics on malformed input (returns false). `now`=0 → time.Now (injectable for
// tests). The signingSecret is Slack's per-app SLACK_SIGNING_SECRET — never one
// of ours.
func verifySlackSignature(signingSecret, signature, timestamp, rawBody string, now int64) bool {
	if signingSecret == "" || signature == "" || timestamp == "" {
		return false
	}
	ts, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return false
	}
	if now == 0 {
		now = time.Now().Unix()
	}
	if abs64(now-ts) > slackMaxSkewSec {
		return false
	}
	mac := hmac.New(sha256.New, []byte(signingSecret))
	mac.Write([]byte(slackSigVersion + ":" + timestamp + ":" + rawBody))
	expected := slackSigVersion + "=" + hex.EncodeToString(mac.Sum(nil))
	// hmac.Equal is constant-time and length-safe.
	return hmac.Equal([]byte(signature), []byte(expected))
}

// ── signed single-use link state (the ONE link primitive) ───────────────────

// slackLinkTTLSec is the link-state lifetime — legs 1→3 must complete within it.
// It is ALSO the single-use seen-set TTL, so a signed link state redeems exactly
// once within its validity.
const slackLinkTTLSec = 60 * 10 // 10 minutes

// signSlackSubject binds an opaque subject into a signed, TTL'd, single-use-
// noncable state:
//
//	base64url("<subject>.<exp>.<nonce>") + "." + base64url(hmac)
//
// The subject MUST NOT contain '.' (the field separator). Returns an error only
// if the CSPRNG fails — a predictable nonce would let a redeemed state replay
// once its seen-set entry expires. `now`=0 → time.Now.
func signSlackSubject(key []byte, subject string, now int64) (string, error) {
	if now == 0 {
		now = time.Now().Unix()
	}
	exp := now + slackLinkTTLSec
	nonce, err := genToken() // 128-bit CSPRNG hex (integrations.genToken)
	if err != nil {
		return "", err
	}
	payload := base64.RawURLEncoding.EncodeToString(
		[]byte(subject + "." + strconv.FormatInt(exp, 10) + "." + nonce))
	return payload + "." + hmacB64URL(key, payload), nil
}

// verifySlackSubject verifies a signed subject state and returns the bound
// subject + single-use nonce. The constant-time MAC is checked BEFORE the payload
// is parsed (a tampered payload never reaches the split/parse). ok=false on any
// MAC / format / expiry failure. `now`=0 → time.Now.
func verifySlackSubject(key []byte, state string, now int64) (subject, nonce string, ok bool) {
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

// slackLinkSep joins the Slack (team,user) into the link-cookie subject. Slack
// team/user ids are [A-Z0-9] (no ':' and no '.'), so the composite is unambiguous
// under both this split and the '.' split in verifySlackSubject.
const slackLinkSep = ":"

// signSlackLink binds the Slack-VERIFIED (team,user) into a signed, single-use
// link state, so the hanzo.id OIDC leg proves it originated from a server-minted
// prompt gated by a verified Slack sign-in — a forged (team,user) cannot smuggle in.
func signSlackLink(key []byte, teamID, slackUserID string, now int64) (string, error) {
	return signSlackSubject(key, teamID+slackLinkSep+slackUserID, now)
}

// verifySlackLink verifies a link state and recovers the bound (team,user) +
// nonce. ok=false on any MAC / format / expiry failure.
func verifySlackLink(key []byte, state string, now int64) (teamID, slackUserID, nonce string, ok bool) {
	subject, n, sok := verifySlackSubject(key, state, now)
	if !sok {
		return "", "", "", false
	}
	parts := strings.SplitN(subject, slackLinkSep, 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", "", false
	}
	return parts[0], parts[1], n, true
}

// ── in-process single-use seen-set (link-state nonces) ──────────────────────

// seenSet is an age-based single-use / seen-set with an atomic test-and-set. It
// backs the per-user link's single-use guarantee: a signed link state is redeemed
// exactly once within its TTL.
//
// SCOPE: per-PROCESS. In a multi-replica deployment the single-use guarantee here
// is DEFENSE-IN-DEPTH: the PRIMARY single-use guarantee is Slack's own server-side
// single-use OAuth `code` (a second exchange of the same code fails at Slack) AND
// hanzo.id's single-use OIDC `code`, plus the state's HMAC + browser-bound cookie
// + short TTL. Eviction is strictly age-based (a within-TTL entry is NEVER
// evicted), which is what forbids an evict-then-replay attack; memory is bounded
// temporally, not by count.
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

// ── helpers ──────────────────────────────────────────────────────────────────

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
