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
// THIS FILE ANSWERS ONE HALF OF THAT AND THE SENSOR ANSWERS THE OTHER. Reading a
// request for its credential CLASS needs the request, so it is here. Turning a
// class plus a traffic pattern into a LANE needs the pattern, so it is
// edge.Lane — inside the observation that produced the counts, which is the only
// place that can compute it before it is counted. Splitting it the other way is
// what made every request in the lane report land in "unknown": the gate had to
// state the lane before it had asked what the caller had been doing.
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
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/zap-proto/zip"
)

// credentialClass reads the class off a request. It looks at the Authorization
// header for the credential's SHAPE and at the validated principal for whether
// the identity boundary accepted it — never at the body, never at a client
// header the boundary does not mint. The vocabulary is edge's (edge.CredSecret
// and friends), because the lane rule that consumes it lives there.
//
// A credential that was presented and did NOT validate is anonymous, not
// secret: possession of a string that fails is possession of nothing.
func credentialClass(c *zip.Ctx) string {
	tok := callerCredential(c)
	switch {
	case tok == "":
		return edge.CredAnonymous
	case IsPublishableKey(tok):
		// A pk- names an org and no principal. It is a tenant label, not an
		// authentication, so it stays publishable whether or not an org resolved.
		return edge.CredPublishable
	case !principalValidated(c):
		return edge.CredAnonymous
	case isAPIKey(tok):
		return edge.CredSecret
	default:
		return edge.CredSession
	}
}

// principalValidated reports whether the identity boundary VERIFIED a principal
// for this request — read from the boundary's own attestation (principal.Minted),
// never from a header.
//
// It used to read `c.Org() != "" || c.User() != ""`, and both disjuncts were
// forgeable:
//
//   - X-Org-Id survives the boundary on the anonymous path by design (the Phase-1
//     data passthrough, documented in middleware_identity.go), so ANY caller can
//     make c.Org() non-empty by sending the header;
//   - in a process where the boundary is not installed at all — a hand-written
//     plugin main — nothing strips either header, so X-User-Id is the client's
//     too.
//
// Either one, plus an sk--shaped string in Authorization that never validated,
// moved a caller from the anonymous lane into the AGENT lane: the lane whose
// whole meaning is "we minted this credential to a named tenant and can revoke
// it". A differentiator a client can set is not a differentiator.
//
// The attestation is absent when no boundary ran, which resolves to anonymous —
// the fail-closed direction for a classifier: unattributable traffic is judged on
// its shape, and only a credential WE resolved buys the agent lane.
func principalValidated(c *zip.Ctx) bool {
	p, ok := principal.Minted(c)
	return ok && p.User != ""
}

// verifiedOrg is the tenant the identity boundary resolved for this request, and
// "" for an anonymous caller or a process with no boundary. It is the sensor's
// keyspace index, so it must be the SERVER's answer: an org taken from a header
// would let one caller write into — and evict from — another tenant's state.
func verifiedOrg(c *zip.Ctx) string {
	p, ok := principal.Minted(c)
	if !ok || p.User == "" {
		return ""
	}
	return p.Org
}

// observation is the ONE place an observation is built from a request, and the reason
// it is one place is that two of its fields are the same fingerprint under
// different trust:
//
//	Cred      — set ONLY when the identity boundary validated the credential. It
//	            is what the sensor keys on, so it must be a fact we stated. A
//	            caller keyed on a string it chooses can leave its own hold by
//	            typing a different one, and can open a table entry per request.
//	Presented — set for whatever the request carried, valid or not. It is counted
//	            only as spread, because a wall of invalid credentials from one
//	            address IS the stuffing signature and refusing to count it would
//	            blind the sensor to the attack it exists to see.
//
// Building this anywhere else would mean deciding that trust question a second
// time, and the second answer is the one that would be wrong.
func observation(c *zip.Ctx, path string) edge.Signal {
	presented := credentialOf(c)
	s := edge.Signal{
		Org:       verifiedOrg(c),
		Presented: presented,
		IP:        ClientIP(c),
		Path:      path,
		Class:     credentialClass(c),
	}
	if principalValidated(c) {
		s.Cred = presented
	}
	return s
}

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

// callerCredential is the credential this request presented, in the SAME
// precedence the identity boundary trusts — callerToken, the one token resolution
// SanitizeIdentity and CallerBearer already share. Reading the Authorization
// header directly would be a second, drifting answer to "which credential
// identifies this caller": a client authenticating with X-Authorization or a
// session cookie would validate upstream and then be counted here as anonymous,
// so its traffic would be pooled under its address instead of under itself.
//
// X-Api-Key is checked after it, because that header is not part of the identity
// boundary's precedence but IS a spelling several SDKs send; a caller the boundary
// could not identify is still a caller this sensor must be able to tell apart from
// the next one.
func callerCredential(c *zip.Ctx) string {
	if tok := callerToken(c); tok != "" {
		return tok
	}
	return strings.TrimSpace(c.Header("X-Api-Key"))
}

// credentialOf returns the fingerprint of whatever credential a request
// presented, and "" when it presented none.
func credentialOf(c *zip.Ctx) string { return Fingerprint(callerCredential(c)) }
