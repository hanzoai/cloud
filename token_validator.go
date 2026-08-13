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

	"github.com/hanzoai/authz"
	"github.com/hanzoai/cloud/internal/environ"
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
	//
	// IT IS AN ATTRIBUTION KEY, NOT AN IDENTITY KEY. The fallback chain is what
	// makes it useful for a log line and unusable for a lookup: a token with no
	// `sub` resolves it to preferred_username, so two DIFFERENT subjects can
	// present the same User, and a consumer that keys a record on it can be handed
	// one subject's token and address another's row. Anything that resolves an
	// account, a wallet or a member row keys on Subject and refuses it empty.
	User string
	// Subject is the `sub` claim VERBATIM — no fallback, empty when the token
	// carries none. It is the one value that identifies exactly one IAM identity,
	// so it is the key every account lookup uses.
	Subject string
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
	// Audience is the validated `aud` claim — WHICH registered app IAM minted this
	// token for. Validate does not gate on it (see below), so it is published for a
	// consumer that must: a resource server narrower than the boundary — one whose
	// credential is a SESSION rather than an API call — decides for itself which
	// apps' tokens it accepts as one.
	Audience []string
	// TokenType is IAM's `tokenType` claim, which is one of the THREE things that
	// distinguish an access token from an id_token — IAM's signer emits the same
	// claim set into both but for aud/tokenType/nonce (middleware_identity.go), so
	// signature and issuer alone cannot tell them apart. A consumer that accepts a
	// bearer as a session must say which of them it means.
	TokenType string
}

// TokenValidator verifies IAM access tokens exactly as the identity boundary does.
// Safe for concurrent use; the underlying JWKS cache is shared and stale-on-error.
type TokenValidator struct{ v *identityValidator }

// NewTokenValidator builds a validator bound to issuer, with the SAME JWKS endpoint
// SanitizeIdentity uses — JWKSURLFor is the single source for both, so a token this
// accepts is a token the boundary accepts, and the two can never drift apart into a
// mint-then-refuse loop.
func NewTokenValidator(issuer string) *TokenValidator {
	issuer = strings.TrimRight(strings.TrimSpace(issuer), "/")
	return &TokenValidator{v: newIdentityValidator(issuer, JWKSURLFor(issuer), 0)}
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
		Owner:     claims.Owner,
		User:      claims.userID(),
		Subject:   strings.TrimSpace(claims.Subject),
		Username:  claims.username(),
		Email:     claims.Email,
		IsAdmin:   claims.IsAdmin,
		TokenType: strings.TrimSpace(claims.TokenType),
		// The published shape stays []model.OrgRef: clients/team copies it verbatim into
		// a session, so this is cloud's outward contract, not a second reading of the
		// claim. ONE conversion, at the surface that publishes it.
		Orgs: orgRefs(claims.Orgs),
	}
	id.Audience = append(id.Audience, claims.Audience...)
	if claims.ExpiresAt != nil {
		id.Expiry = claims.ExpiresAt.Time
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

// Home is the tenant this identity acts for: the FIRST entry of the signed
// membership set.
//
// IT IS NOT Owner, and the difference is a live defect class rather than a
// preference. `owner` carries the APPLICATION's org, so it is chosen by whichever
// app the caller authenticated through — a hanzo user arriving via lux-cloud
// presents owner="lux". The identity boundary refuses to derive a tenant from it
// for exactly that reason (idClaims.homeOrg, auth_identity.go), and a consumer that
// reads Owner as the tenant has re-opened what that accessor closed: it would scope
// every store query to an org the caller selected.
//
// Empty is a REFUSAL, not a default. A token carrying no membership set is either
// pre-v1.33.0 or a MACHINE credential (a client_credentials app or an API key is a
// member of nothing), and neither is a person with a home tenant. A caller that
// needs one must fail closed on empty rather than fall back to Owner — falling back
// is the defect, spelled slightly differently.
func (v VerifiedIdentity) Home() string {
	if len(v.Orgs) == 0 {
		return ""
	}
	return strings.TrimSpace(v.Orgs[0].Org)
}

// JWKSURLFor resolves the JWKS endpoint for an issuer: the CLOUD_JWKS_URL
// override when set, else the HIP-0111 convention
// {issuer}/v1/iam/.well-known/jwks. ONE derivation — the whole binary reads it,
// so a deployment that pins a custom JWKS pins it everywhere.
//
// It claimed to be that already while being unexported, so the two subsystems
// that could not reach it rebuilt the URL inline instead — durable.go's gated
// ZAP listener and apps/base's per-app pool. Both concatenated the suffix
// themselves and therefore ignored CLOUD_JWKS_URL outright: an operator who
// pinned a JWKS pinned it for the edge validator and NOT for the two planes
// that verify the same tokens, which is a fleet validating one set of signing
// keys at the front door and a different set behind it.
func JWKSURLFor(issuer string) string {
	if override := environ.Or("CLOUD_JWKS_URL", ""); override != "" {
		return override
	}
	return strings.TrimRight(issuer, "/") + "/v1/iam/.well-known/jwks"
}

// orgRefs renders a signed membership set in the shape VerifiedIdentity publishes.
// Order is preserved (home org first, as IAM builds it) because a consumer reads
// [0] as the home org.
func orgRefs(orgs []authz.Membership) []model.OrgRef {
	if len(orgs) == 0 {
		return nil
	}
	out := make([]model.OrgRef, 0, len(orgs))
	for _, o := range orgs {
		out = append(out, model.OrgRef{Org: o.Org, Role: string(o.Role)})
	}
	return out
}
