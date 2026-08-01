package cloud

// Agency — telling a customer's automation apart from a bad bot.
//
// This is the question a generic bot filter cannot answer and we can, because
// the answer is a fact about OUR OWN issuance rather than a guess about the
// client. A user-agent string is whatever the caller typed; a credential is
// something we minted, to a named principal, in a named tenant, that we meter
// and can revoke. So the classification here reads the credential, never the
// client's self-description. There is no user-agent heuristic in this file and
// there must not be one: an agent that lies about its user-agent is still
// holding our key, and a scraper that copies Chrome's is still holding nothing.
//
// FOUR LANES, and the boundary between them is attributability:
//
//	agent   — an attributable machine credential (sk-/hk-, or a machine JWT).
//	          Programmatic traffic that a named org pays for and we can switch
//	          off. This is the lane our own agents run in, and it is the lane a
//	          customer's automation runs in. It gets judged on VOLUME PATTERN,
//	          not on being automated: being automated is the product.
//	human   — a browser session bearer. A person at a keyboard.
//	bot     — unattributable traffic already showing an abuse shape: no
//	          credential (or a publishable one, the kind that ships in a browser
//	          bundle and is therefore the kind that gets copied) TOGETHER WITH a
//	          pattern no legitimate client produces — many keys from one address,
//	          a wall of auth failures, a path sweep.
//	unknown — unattributable but unremarkable. Scored normally. Most anonymous
//	          traffic is here and stays here, which is the point: "anonymous" is
//	          not "malicious".
//
// The gate's classification is a PRIOR. It is sent to the scorer as a signal and
// the scorer — which holds the agent registry, the session plane and the metered
// shape — may overrule it. Its answer is the authoritative one. That split is
// deliberate: the edge must classify in nanoseconds off facts already in hand,
// and must not learn to score.

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"strings"

	"github.com/hanzoai/cloud/apps/gateway/edge"
	"github.com/zap-proto/zip"
)

// The credential classes. A syntactic fact about HOW a caller authenticated —
// derived from the credential's own shape plus whether the identity boundary
// validated it. Nothing here is a judgement.
const (
	// CredSession is a validated bearer that is not an opaque key: a browser or
	// CLI session minted by IAM for a person.
	CredSession = "session"
	// CredSecret is an sk-/hk- key: a machine credential issued to a principal.
	// It may not be shipped to a browser, so possession attributes.
	CredSecret = "secret"
	// CredPublishable is a pk- key: org-only by design, shipped in client
	// bundles, and therefore trivially copied. It names a tenant, not a caller.
	CredPublishable = "publishable"
	// CredAnonymous is no credential, or one the identity boundary refused.
	CredAnonymous = "anonymous"
)

// The lanes. Values, not booleans, because "we could not tell" is a distinct
// state from "we decided it is a bot", and collapsing them is how a false
// positive becomes a blocked customer.
const (
	AgencyAgent   = "agent"
	AgencyHuman   = "human"
	AgencyBot     = "bot"
	AgencyUnknown = "unknown"
)

// credentialClass reads the class off a request. It looks at the Authorization
// header for the credential's SHAPE and at the validated principal for whether
// the identity boundary accepted it — never at the body, never at a client
// header the boundary does not mint.
//
// A credential that was presented and did NOT validate is anonymous, not
// secret: possession of a string that fails is possession of nothing.
func credentialClass(c *zip.Ctx) string {
	tok := bearerFromAuth(c.Header("Authorization"))
	if tok == "" {
		tok = strings.TrimSpace(c.Header("X-Api-Key"))
	}
	switch {
	case tok == "":
		return CredAnonymous
	case IsPublishableKey(tok):
		// A pk- names an org and no principal. It is a tenant label, not an
		// authentication, so it stays publishable whether or not an org resolved.
		return CredPublishable
	case !principalValidated(c):
		return CredAnonymous
	case isAPIKey(tok):
		return CredSecret
	default:
		return CredSession
	}
}

// principalValidated reports whether the identity boundary minted a principal
// for this request. Read through the sanitized header the boundary writes, which
// a client cannot forge — the raw copy is deleted on ingress.
func principalValidated(c *zip.Ctx) bool { return c.Org() != "" || c.User() != "" }

// agency classes a request into a lane from its credential class and the pattern
// its caller has been showing. Pure and total: same inputs, same lane, no clock,
// no I/O — which is what makes the table test in agency_test.go the whole
// specification.
//
// The bot rule is deliberately CONJUNCTIVE. Unattributable alone is not bot:
// every first request from every new integration is unattributable. It takes an
// abuse SHAPE as well — many credentials from one address (stuffing), a wall of
// refusals (guessing), or a path sweep (scraping) — and then the caller is
// judged, not the anonymity.
func agency(class string, p edge.Pattern) string {
	switch class {
	case CredSecret:
		return AgencyAgent
	case CredSession:
		return AgencyHuman
	case CredPublishable, CredAnonymous:
		if abusive(p) {
			return AgencyBot
		}
		return AgencyUnknown
	}
	return AgencyUnknown
}

// The shapes that make unattributable traffic a bot. Constants, not per-org
// configuration: they are properties of the protocol, not preferences of a
// tenant, and a knob here is a knob an attacker's target can be talked into
// widening. They are deliberately far above anything a normal client produces.
const (
	// stuffPeers — distinct credentials presented from one address in a window.
	// A browser presents one. A CI runner presents one. Eight is a script.
	stuffPeers = 8
	// guessFailures — 401/403 outcomes in a window. A client with a stale token
	// retries a few times and stops; twenty-five is someone trying keys.
	guessFailures = 25
	// sweepPaths — distinct paths one unattributable caller touched in a window.
	// A real integration walks a handful of endpoints; a crawler walks the map.
	sweepPaths = 40
)

func abusive(p edge.Pattern) bool {
	return p.Peers >= stuffPeers || p.Failures >= guessFailures || p.Paths >= sweepPaths
}

// Agency reports the lane the edge places a request in, given what the sensor
// already knows about its caller. The exported entry point, so a subsystem
// reporting on traffic names the lane the same way the gate did.
func Agency(c *zip.Ctx, p edge.Pattern) string { return agency(credentialClass(c), p) }

// fingerprintSalt is a per-PROCESS secret. A credential fingerprint is only ever
// compared with another fingerprint from the same process and the same window,
// so the salt never needs to be shared, persisted or rotated — and because it is
// never shared, a fingerprint that escapes in a log or a report cannot be tested
// against a candidate key anywhere else. Generated from crypto/rand at init; a
// generator failure is fatal at start rather than silently downgrading to a
// guessable salt.
var fingerprintSalt = mustSalt()

func mustSalt() []byte {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic("cloud: no entropy for the credential fingerprint salt: " + err.Error())
	}
	return b
}

// fingerprintLen is how much of the digest is kept: 12 base64url characters, 72
// bits. Enough that two live credentials colliding is not a thing that happens,
// short enough to read in a report.
const fingerprintLen = 12

// Fingerprint turns a credential into a stable per-process handle. It is what
// the sensor counts under and what a traffic report shows — the credential
// itself never enters a counter, a log, a record or a response.
//
// HMAC-SHA256 rather than a bare hash: with a bare hash, anyone holding a
// candidate key could confirm it against a published fingerprint. With a keyed
// digest under a salt that never leaves the process, they cannot.
func Fingerprint(cred string) string {
	cred = strings.TrimSpace(cred)
	if cred == "" {
		return ""
	}
	m := hmac.New(sha256.New, fingerprintSalt)
	_, _ = m.Write([]byte(cred))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))[:fingerprintLen]
}

// credentialOf returns the fingerprint of whatever credential a request
// presented, and "" when it presented none.
func credentialOf(c *zip.Ctx) string {
	tok := bearerFromAuth(c.Header("Authorization"))
	if tok == "" {
		tok = strings.TrimSpace(c.Header("X-Api-Key"))
	}
	return Fingerprint(tok)
}
