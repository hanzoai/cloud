package provider

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/cloud/sdk/coord"
	"github.com/zap-proto/zip"
)

// channel_state.go holds the PLATFORM-AGNOSTIC signed-state primitive every
// account-link flow shares: a signed, TTL'd, single-use "subject state". It is
// provider-blind — Slack's (team,user), Discord's (guild,user), Teams' (tenant,user)
// and Telegram's (chat,user) link all compose their subject and sign it here.
// Orthogonal to the OAuth-connect state in state.go (statePayload{org,provider,nonce});
// both HMAC with the SAME s.State.stateKey — one signing key, distinct named subjects.
//
// The constant-time hmacB64URL, the single-use seenSet and abs64 live here too, so
// the algebra and the providers it composes have one home.

// linkStateTTLSec is the account-link state lifetime — the browser legs must
// complete within it. It is ALSO the single-use seen-set TTL (channelSeen), so a
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
	nonce, err := genToken() // 128-bit CSPRNG hex (provider.genToken)
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

// ── single-use seen-set (link-state nonces) ────────────────────────────────

// seenSet is a single-use seen-set: a signed link state is redeemed exactly once
// within its TTL, across every pod the deployment runs (coord.Once over the shared
// KV; within this process when there is none). It backs every platform's per-user
// link single-use guarantee (channelSeen for the generalized adapters,
// slackUsedStates for Slack). The platform's own single-use OAuth `code` and
// hanzo.id's single-use OIDC `code` stay the primary guarantee, with the state's
// HMAC, browser-bound cookie and short TTL; this is the guarantee cloud keeps for
// its own states.
type seenSet struct {
	name string
	ttl  time.Duration
}

func newSeenSet(name string, ttl time.Duration) *seenSet {
	return &seenSet{name: name, ttl: ttl}
}

// redeem spends the single-use k, and answers whether this was its first use. A
// KV that cannot answer is a 503: a state that cannot be proven unused is not used,
// and the caller is told to try again rather than that its link is spent. The
// empty key is never deduplicated.
func (s *seenSet) redeem(k string) (bool, error) {
	if k == "" {
		return true, nil
	}
	first, err := coord.Default().Once(context.Background(), s.name, k, s.ttl)
	if err != nil {
		return false, zip.Errorf(http.StatusServiceUnavailable, "this link cannot be checked right now; try again")
	}
	return first, nil
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
