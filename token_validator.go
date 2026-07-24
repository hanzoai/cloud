package cloud

// TokenValidator — the identity boundary's verdict, callable by a subsystem.
//
// SanitizeIdentity validates an IAM access token on the way IN, on every request,
// and turns it into the identity headers c.Org()/c.User()/c.IsAdmin() read. A
// subsystem that MINTS a session (clients/deploy signs a browser in against IAM and
// stores the access token in the session cookie) needs the same verdict at a
// different moment: BEFORE it hands the token to a browser. Without it the subsystem
// is reduced to reading unverified claims, and — worse — it can mint a cookie this
// deployment's boundary will refuse, which is not a security hole (the boundary still
// says no) but a redirect loop: mint → refused → bounce to sign-in → mint again.
//
// So this exports the SAME validator, built the SAME way, rather than letting each
// caller assemble its own. One trust anchor, two moments.

import (
	"fmt"
	"strings"
	"time"

	model "github.com/hanzoai/iam/pkg/model"
)

// VerifiedIdentity is what a token PROVED, after signature, issuer and expiry all
// checked out. It is deliberately the small subset a caller can act on;
// the authorization decision still belongs to the caller (deploy compares Owner to
// the admin org) and, independently, to SanitizeIdentity on every later request.
type VerifiedIdentity struct {
	// Owner is the validated `owner` claim — the IAM org. This is the value the
	// SuperAdmin predicate compares against the reserved admin org.
	Owner string
	// User is the canonical user id (sub, then preferred_username, then name).
	User string
	// Username is the IAM username — the `name` half of `<owner>/<name>`.
	Username string
	// Email is the validated `email` claim, when present.
	Email string
	// IsAdmin is IAM's `isAdmin` bit: admin OF ONE'S OWN ORG. It is NOT the
	// SuperAdmin predicate — that is Owner == the reserved admin org, and
	// conflating the two is a privilege escalation. Reported so a caller can tell
	// an org admin from a plain member, never as the platform gate.
	IsAdmin bool
	// Orgs is the validated `orgs` membership-set claim — every org the subject
	// may act in (the HOME org first, then explicit team memberships), each with
	// its coarse role (owner | admin | member). It is the Slack-model tenancy set
	// a caller enumerates cross-org surfaces against (hanzo.team unions a user's
	// workspaces across it) with NO IAM round-trip. Empty on a token minted before
	// the claim shipped (iam < 1.31.34); a reader then falls back to the single
	// Owner org. Verified off the SAME signed token as Owner — never trusted raw.
	Orgs []model.OrgRef
	// Expiry is the token's own `exp`. A session built on this token must not
	// outlive it.
	Expiry time.Time
}

// TokenValidator verifies IAM access tokens exactly as the identity boundary does.
// Safe for concurrent use; the underlying JWKS cache is shared and stale-on-error.
type TokenValidator struct{ v *identityValidator }

// NewTokenValidator builds a validator bound to issuer, with the SAME JWKS endpoint
// SanitizeIdentity uses — jwksURLFor is the single source for both, so a token this
// accepts is a token the boundary accepts, and the two can never drift apart into a
// mint-then-refuse loop.
func NewTokenValidator(issuer string) *TokenValidator {
	issuer = strings.TrimRight(strings.TrimSpace(issuer), "/")
	return &TokenValidator{v: newIdentityValidator(issuer, jwksURLFor(issuer), 0)}
}

// Validate verifies raw and returns what it proved. The error is the real reason
// (untrusted issuer, audience, expired, no matching key) so an operator reading a
// failed sign-in learns which knob is wrong instead of seeing a bare refusal.
//
// Fails closed on every path: a nil validator, an unparseable token, a token whose
// claims do not check out. It NEVER returns a partially-trusted identity.
func (t *TokenValidator) Validate(raw string) (VerifiedIdentity, error) {
	if t == nil || t.v == nil {
		return VerifiedIdentity{}, fmt.Errorf("no identity validator configured")
	}
	claims, err := t.v.validate(raw)
	if err != nil {
		return VerifiedIdentity{}, err
	}
	id := VerifiedIdentity{
		Owner:    claims.Owner,
		User:     claims.userID(),
		Username: claims.username(),
		Email:    claims.Email,
		IsAdmin:  claims.IsAdmin,
		Orgs:     claims.Orgs,
	}
	if claims.Expiry != nil {
		id.Expiry = claims.Expiry.Time()
	}
	// An org bearing a whitespace/control/format rune is not an injective org
	// identifier — the identity boundary refuses to grant scoping from one
	// (OrgHasUnsafeRune), so it must not be reported as a usable org here either,
	// or a caller could compare a folded name against its admin org.
	if OrgHasUnsafeRune(id.Owner) {
		return VerifiedIdentity{}, fmt.Errorf("owner claim carries an unsafe rune")
	}
	return id, nil
}

// jwksURLFor resolves the JWKS endpoint for an issuer: the CLOUD_JWKS_URL override
// when set, else the HIP-0111 convention {issuer}/v1/iam/.well-known/jwks. ONE
// derivation, shared by Config load and NewTokenValidator, so a deployment that
// pins a custom JWKS pins it for both.
func jwksURLFor(issuer string) string {
	if override := strings.TrimSpace(getenv("CLOUD_JWKS_URL", "")); override != "" {
		return override
	}
	return strings.TrimRight(issuer, "/") + "/v1/iam/.well-known/jwks"
}
