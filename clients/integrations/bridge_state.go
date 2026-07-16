package integrations

import (
	"crypto/hmac"
	"encoding/base64"
	"strconv"
	"strings"
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
// signSlackSubject/verifySlackSubject; the shared low-level primitives it composes —
// the constant-time hmacB64URL, the single-use seenSet, abs64 — live ONCE in
// slack_verify.go and are reused here (no redeclaration).

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
