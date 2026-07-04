package integrations

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"time"
)

// slack_verify.go holds Slack's ADAPTER-specific inbound trust boundary — the
// Slack request-signature verification — plus the Slack (team,user) composition
// over the shared, provider-agnostic link-subject primitives (signSubject /
// verifySubject in bridge_state.go). The generic single-use state + seen-set live
// in the ChatBridge core; only Slack's HMAC scheme and its (team,user) subject
// shape live here.

// ── Slack request signing (webhook + slash) ─────────────────────────────────

// slackSigVersion is Slack's request-signing version prefix. Slack computes
//
//	v0=HMAC_SHA256(signingSecret, "v0:<timestamp>:<rawBody>")
//
// and sends it in X-Slack-Signature with X-Slack-Request-Timestamp.
// https://api.slack.com/authentication/verifying-requests-from-slack
const slackSigVersion = "v0"

// slackMaxSkewSec is the anti-replay window: a request whose timestamp is older or
// newer than this is rejected even when the MAC is valid, so a captured request
// replayed past the window fails.
const slackMaxSkewSec = 60 * 5 // 5 minutes

// verifySlackSignature returns true iff the HMAC matches over the EXACT raw body
// AND the timestamp is within the replay window. Constant-time compare; never
// panics on malformed input (returns false). `now`=0 → time.Now (injectable for
// tests). The signingSecret is Slack's per-app SLACK_SIGNING_SECRET — never one of
// ours.
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

// ── Slack (team,user) link subject (over the shared bridge_state primitives) ─

// slackLinkSep joins the Slack (team,user) into the link-cookie subject. Slack
// team/user ids are [A-Z0-9] (no ':' and no '.'), so the composite is unambiguous
// under both this split and the '.' split in verifySubject.
const slackLinkSep = ":"

// signSlackLink binds the Slack-VERIFIED (team,user) into a signed, single-use link
// state, so the hanzo.id OIDC leg proves it originated from a server-minted prompt
// gated by a verified Slack sign-in — a forged (team,user) cannot smuggle in.
func signSlackLink(key []byte, teamID, slackUserID string, now int64) (string, error) {
	return signSubject(key, teamID+slackLinkSep+slackUserID, now)
}

// verifySlackLink verifies a link state and recovers the bound (team,user) + nonce.
// ok=false on any MAC / format / expiry failure.
func verifySlackLink(key []byte, state string, now int64) (teamID, slackUserID, nonce string, ok bool) {
	subject, n, sok := verifySubject(key, state, now)
	if !sok {
		return "", "", "", false
	}
	parts := strings.SplitN(subject, slackLinkSep, 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", "", false
	}
	return parts[0], parts[1], n, true
}
