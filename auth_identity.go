package cloud

// In-binary IAM identity — the trust anchor for SanitizeIdentity.
//
// It no longer MIRRORS anything. This file used to say it mirrored the gateway's
// validator and explain why cloud could not import it: the gateway is a heavyweight
// module and already imports cloud, so importing it back would braid a cycle. Both
// halves were true, and the conclusion — write our own — is what produced a third
// independent reading of what an IAM token means.
//
// The reading is hanzoai/authz now, and the check is hanzoai/authz/edge. The leaf is
// 148 packages with one non-stdlib dependency and imports nothing from cloud or the
// gateway, so the cycle argument that justified the copy no longer applies to it.
//
// What is left here is what is genuinely CLOUD's: the trusted-issuer set (the brands
// this binary fronts), the API-key resolver and the org it yields, the memo that keeps
// a replayed ZAP credential from re-verifying per frame, and the policy that turns
// verified claims into a home org.
//
// The copy was not free. It declared a `type` claim IAM emits nowhere and read it as
// the machine discriminator, so every machine principal arrived as a human; and its
// key selection fell back to trying every RSA key in the JWKS, so a token naming one
// key was accepted on a signature from another.

import (
	"encoding/base64"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/hanzoai/authz"
	"github.com/hanzoai/authz/edge"
	"github.com/hanzoai/cloud/apps/principal"
)

// idClaims is what a token proved, plus the one thing cloud resolves itself.
//
// The CLAIMS are authz.Claims — the definition IAM signs — embedded rather than
// restated. This struct used to restate them, and the restatement is where the drift
// lived: it declared a `type` claim IAM emits nowhere and read it as the machine
// discriminator, so every machine principal arrived as a human.
type idClaims struct {
	authz.Claims

	// subjectOrg is the org resolved from the token SUBJECT rather than from any
	// claim — set ONLY by the API-key resolver (iamKeys.lookup), from the IAM user
	// row the accessKey belongs to. It is the machine-credential answer to "whose
	// org is this?", and it exists because an sk- key is not a member of
	// anything: IAM mints no `orgs` claim for one, so the membership set homeOrg
	// reads for a human is legitimately empty here.
	//
	// UNEXPORTED AND UNTAGGED ON PURPOSE. encoding/json cannot populate it, so no
	// token can carry it and no caller can forge it — it is only ever set by the
	// code path that already authenticated the key against IAM. That is the same
	// rule the identity headers follow: never decoded from the request, only written
	// from something verified.
	subjectOrg string

	// grant is what the CREDENTIAL may reach — the key row's own limit, read from
	// IAM alongside the principal it resolves. Distinct from what the holder may
	// reach, which is what every other field here describes.
	//
	// UNEXPORTED AND UNTAGGED for the same reason subjectOrg is: no token can
	// carry it and no caller can forge it. A limit a request could state for
	// itself would be a limit that widens.
	grant Grant
}

// renderProject returns the project id to stamp into X-Project-Id, or "" when the
// header must be OMITTED. The project rides in the validated JWT `project` claim,
// scoped to the caller's org exactly like `owner` — trusted, not forgeable. The
// default project (absent claim, or the literal principal.DefaultProject) writes
// nothing, so X-Project-Id is present iff a non-default project is in scope. This
// mirrors the edge (the edge (hanzoai/authz/edge)) byte-for-byte, so the in-binary
// path binds the same header the gateway would.
func (c *idClaims) renderProject() string {
	if principal.IsDefaultProject(c.Project) {
		return ""
	}
	return strings.TrimSpace(c.Project)
}

// renderBillingAccount returns the funding account to stamp into
// X-Billing-Account-Id, or "" when the header must be OMITTED (a token minted
// before IAM shipped the claim, or one IAM could not attribute).
//
// WHO PAYS IS NOT A CLIENT'S TO NAME. This rides the validated `billing_account`
// claim — IAM's signed statement, resolved at the identity boundary from the real
// grant context — exactly like `owner` and `project`. It mirrors the edge
// (the edge (hanzoai/authz/edge)) byte-for-byte, so the in-binary path binds
// the same header the gateway would, and ai/object.Payer reads the same payer on
// both. The raw client copy is deleted on ingress and NEVER restored.
func (c *idClaims) renderBillingAccount() string {
	return strings.TrimSpace(c.BillingAccount)
}

// userID resolves the canonical user id: sub, then preferred_username, then
// name. IAM may leave sub empty. This is the STABLE identifier (a UUID when IAM
// sets sub) stamped as X-User-Id and consumed as the attribution key everywhere.
func (c *idClaims) userID() string {
	if c.Subject != "" {
		return c.Subject
	}
	if c.PreferredUsername != "" {
		return c.PreferredUsername
	}
	return c.Name
}

// username resolves the IAM USERNAME — the `name` half of the `<owner>/<name>`
// key IAM's privileged ops (mint-user-keys, get-user) parse. Prefers the `name`
// claim (IAM's canonical username, e.g. "z"), then preferred_username. It NEVER
// returns the subject: sub is a UUID, and `<owner>/<uuid>` fails IAM's
// GetOwnerAndNameFromId user lookup ("password or code is incorrect"). This is
// the distinct-from-userID() value stamped as X-User-Name so the direct-Bearer
// path builds owner/name correctly — the gateway historically wrote
// X-User-Id==name, which userID() (sub-first) breaks on the in-binary path.
// PREFERRED_USERNAME FIRST, and `name` only as a legacy fallback. The order used
// to be reversed on the belief that IAM's `name` claim carried IAM's canonical
// username. It does not: OIDC gives `name` DISPLAY semantics and IAM fills it from
// User.DisplayName, so a real token read `name = "Zach Kelling"` — a human label
// with a space in it, not the `<name>` half of `<owner>/<name>`.
//
// The cost landed on the money path, which addresses a wallet as `<org>/<username>`
// (apps/principal/wallet.go). Preferring `name` addressed `hanzo/Zach Kelling`,
// a wallet no funding path can name, while the balance sat in `hanzo/z`. Every
// signed-in completion 402'd against a funded account. That file already documents
// three prior recurrences of one bug — "two layers derived the same address two
// ways"; this is the same bug arriving through the claim rather than the header.
//
// Fallback is retained, not removed: a token minted before IAM emitted
// preferred_username has only `name`, and for those the old reading is still the
// best available answer. New tokens carry the username explicitly, so the fallback
// stops being reached as they roll over rather than needing a flag day.
func (c *idClaims) username() string {
	// authz.Username is the estate rule and it NEVER falls back to the display name:
	// addressing a wallet by one names a wallet nothing can fund.
	if u := c.Claims.Username(); u != "" {
		return u
	}
	// The named exception, kept deliberately: a token minted before IAM emitted
	// preferred_username has only `name`, and for those it is the best available
	// answer. It stops being reached as tokens roll over, rather than needing a flag
	// day. This EXTENDS the one rule; it does not restate it.
	return c.Name
}

// identityValidator validates an IAM JWT against a cached JWKS. Issuer (any of a
// trusted SET) + audience + expiry are always enforced.
//
// The issuer is a SET so ONE cloud binary validates every white-label brand's
// tokens (hanzo iss=hanzo.id AND lux iss=lux.id, ...). Signature verification is
// unaffected: the in-cluster IAM serves EVERY brand's signing cert in one JWKS
// (cert-hanzo/cert-lux/cert-zoo/...), keyed by the token kid, so a single
// jwksURL verifies all brands. Only the issuer-string comparison had to widen.
type identityValidator struct {
	// verifier is the estate's ONE credential check (hanzoai/authz/edge): the JWKS
	// reader, the algorithm allowlist, kid-bound key selection, the issuer allowlist
	// and expiry. cloud used to hold its own of each, and each was a chance to differ
	// from the edge about what a token proves.
	verifier *edge.Verifier
	keys     keyResolver // resolves an opaque API key to a principal; nil ⟹ keys stay anonymous
	// claims memoizes token ⇒ verified claims so an already-authenticated caller
	// (notably a ZAP socket, which replays its credential on every frame) is not
	// signature-verified again per call. See identity_cache.go.
	claims *identityCache
}

// newIdentityValidator builds a validator whose trusted-issuer set is the primary
// issuer UNIONED with every white-label brand issuer (BrandIssuers) plus any
// WHITELABEL_ISSUERS override. ttl<=0 uses the 15m JWKS default. The union is
// fail-secure: it only ADDS the known-good brand issuers, never an arbitrary one.
//
// Trust is IAM-native: signature (JWKS) + issuer (this set) + expiry. There is NO
// per-app audience allowlist — the `aud` (a minting app's client_id) is IAM's to
// assign, not cloud's to mirror, so a new first-party app needs zero cloud change.
func newIdentityValidator(issuer, jwksURL string, ttl time.Duration) *identityValidator {
	return &identityValidator{
		verifier: edge.NewVerifier(jwksURL, trustedIssuers(issuer), nil, ttl),
		claims:   newIdentityCache(),
		keys:     sharedKeys(), // ONE resolver+cache, shared with OrgForKey (analytics capture)
	}
}

// kmsMachineAudSuffix is the fixed suffix of a per-org PaaS-KMS sync machine
// identity's audience. Each org's KMS sync authenticates as a dedicated, NON-shared
// IAM application named "<org>-platform-kms" (Organization=<org>, client_credentials
// grant), so IAM stamps the token's aud == the app's own clientId == "<org>-platform-kms"
// (object/token_jwt.go tokenAudience) and owner == <org> (object/token_oauth.go
// GetClientCredentialsToken sets owner = app.Organization).
//
// Validation no longer consults the audience at all (trust is signature + issuer +
// expiry), so a machine token clears validate() like any other. This suffix survives
// for the OPPOSITE reason: to RECOGNISE a machine principal (isKMSMachinePrincipal) so
// SanitizeIdentity can DENY it SuperAdmin even when it carries owner==adminOrg — a
// client_credentials machine identity must never wield platform-admin. The match is
// bound to the token's OWN owner claim (<owner>-platform-kms), so it certifies "the
// KMS sync identity for its own org" and grants nothing wider.
const kmsMachineAudSuffix = "-platform-kms"

// kmsMachineAudience returns the audience an org org's PaaS-KMS sync identity
// carries: "<owner>-platform-kms". An empty owner yields empty — no machine
// audience is ever granted to an org-less token (fail closed).
func kmsMachineAudience(owner string) string {
	if owner == "" {
		return ""
	}
	return owner + kmsMachineAudSuffix
}

// KMSMachineClientID is the clientId an org's dedicated PaaS-KMS sync
// application carries — which is also, by the contract above, the audience its
// tokens are stamped with. Exported for the provisioner (clients/platform), so
// "<org>-platform-kms" is derived in exactly one place: here, where the
// recognition side (isKMSMachinePrincipal) reads it back.
func KMSMachineClientID(org string) string { return kmsMachineAudience(org) }

// homeOrg returns the USER's own organization — the tenant whose ledger pays and
// whose membership decides platform authority. It reads the FIRST entry of the
// signed `orgs` claim, which IAM builds home-first by construction from the
// authoritative user row (store.MemberOrgRefs: `refs := []OrgRef{{Org: user.Owner,
// …}}`, then explicit membership rows, deduped home-wins).
//
// IT IS DELIBERATELY NOT claims.Owner. The `owner` claim has never carried the
// user's org: IAM stamps the APPLICATION's org into it (oidc/jwt.go Sign:
// `Owner: app.Organization`). Same user, same password, two apps ⇒ two different
// `owner` values. That read as correct for years only because, pre-onboarding, the
// app org and the user org were both "hanzo"; onboarding broke the coincidence, not
// the claim. IAM knew — internal/authz/authz.go refuses to trust these claims
// internally and says the organization "comes from the token SUBJECT … never from
// the token's `owner`/`organization` claims" — and the Sign/SignUserToken pair
// documents the divergence while naming cloud's SanitizeIdentity as the consumer.
//
// Consuming `owner` made the tenant CALLER-SELECTABLE: a user picked which org's
// ledger to spend by choosing which app to authenticate through (a hanzo user via
// lux-cloud billed lux), and — because the same value gated SuperAdmin — a token
// from any app owned by the reserved admin org conferred platform admin. One
// poisoned value, two defects; one accessor, both closed.
//
// TWO PRINCIPAL KINDS, TWO SOURCES — they are different questions, so they are two
// branches rather than one fallback chain:
//
//   - A MACHINE credential (sk- API key) is a member of nothing, so IAM mints it
//     no `orgs` claim at all. Its org comes from the token SUBJECT: iamKeys.lookup
//     resolves the accessKey to its IAM user row and records that row's owner in
//     subjectOrg. Reading it here is not a fallback to `owner` — it never passed
//     through an application, so it carries none of the app-selection hazard.
//
//   - A HUMAN token carries the membership set, and its first entry is the home org.
//
// EMPTY STILL MEANS EMPTY for a human. A token with neither (minted before IAM
// v1.33.0) resolves NO home org and the caller must fail closed — an org-less
// request, every org() gate 403. It must never fall back to `owner`, which is
// precisely the app-selected value this exists to stop trusting. Callers log that
// case so a real legacy principal is visible rather than silently denied.
//
// Order matters: subjectOrg is checked FIRST because a key principal's empty `orgs`
// is correct-by-design, not a degraded token. Reading the membership set first and
// failing closed on it is what 403'd every customer API key on org-scoped routes
// (/v1/agents, /v1/gpus, /v1/billing/*) in v1.801.244 while leaving unscoped
// /v1/models working — which is why the pre-pin probe, which only ever asserted
// /v1/models, could not see it.
// A MACHINE JWT (client_credentials, IAM `type` == "application" — e.g. the
// per-org "<org>-platform-kms" sync identity) is the third case, and it reads
// `owner` DELIBERATELY. That is not the hazard this function exists to remove: the
// hazard was a HUMAN whose org followed whichever app they logged in through, and a
// machine cannot choose — it IS the application, its `owner` is that application's
// own organization, and obtaining the token at all requires that app's client
// secret. There is no user to mis-attribute. Omitting this branch would fail closed
// on the KMS sync identity, whose org-scoped data access runs through this same
// boundary (see isKMSMachinePrincipal, which gates ONLY the admin grant precisely so
// that access keeps working).
func (c *idClaims) homeOrg() string {
	if c.subjectOrg != "" {
		return c.subjectOrg // API key: resolved from the subject
	}
	if isKMSMachinePrincipal(c) || isClientCredentialsPrincipal(c) {
		return c.Owner // machine JWT: the app IS the principal
	}
	// Everything else is the estate rule: the first entry of the signed membership
	// set, or empty. Stated once, in the leaf; the two branches above are the facts
	// only cloud has, because only cloud authenticated the credential that carries
	// them.
	return c.Claims.Home()
}

// isClientCredentialsPrincipal reports whether a validated token was minted by the
// client_credentials grant — an application authenticating AS ITSELF, with no user
// behind it.
//
// It is the same fact isKMSMachinePrincipal establishes, for every other app. KMS
// could be recognised by audience alone because its client id is DERIVED from the org
// it belongs to ("<org>-platform-kms"), so the audience proves the pairing. No other
// app's id is derivable that way, so the recognition has to come from the token's
// SHAPE instead.
//
// The shape is not forgeable by a human token. In a client_credentials token the
// client IS the subject: IAM sets sub to "<org>/<app>", and azp — the authorized
// party, i.e. the client that obtained the token — equals the sole audience, because
// the app requested a token for itself. A human's token cannot look like that: its
// subject is the user, and azp names whichever app they signed in through, which is
// the very mis-attribution homeOrg exists to prevent. And every field read here is
// signed by IAM; none is a header a caller can set.
//
// WHY THIS MATTERS, measured. studio authenticates this way. Its token carries
// owner=hanzo and organization=hanzo but no `orgs` — correct-by-design for a machine,
// exactly as an sk- key carries none — so Home() returned "" and SanitizeIdentity
// minted X-User-Id with no X-Org-Id. Every org-scoped gate then refused it, and the
// one that mattered was the durable queue: `POST /v1/tasks/.../activities` answered
// 403 "identity required", so no render could be enqueued at all. Thirteen jobs sat
// `queued` in studio's worklog for up to 19 hours while both GPUs polled an empty
// namespace every two seconds and reported themselves healthy.
//
// It is NOT a widening of who may cross tenants. This resolves an org for a principal
// that already has exactly one and can no more choose it than an API key can: `owner`
// is the application's own organization, set by IAM when the app was created, and
// obtaining the token at all requires that application's client secret. A human with
// no `orgs` still resolves nothing and still fails closed — the case the estate rule
// exists for is untouched.
func isClientCredentialsPrincipal(c *idClaims) bool {
	if c == nil || c.Owner == "" || c.Azp == "" {
		return false
	}
	// The client obtained a token FOR ITSELF: one audience, and it is the client.
	if len(c.Audience) != 1 || c.Audience[0] != c.Azp {
		return false
	}
	// And it IS the subject: "<org>/<app>", naming that same client.
	sub := c.Subject
	i := strings.LastIndex(sub, "/")
	return i > 0 && sub[i+1:] == c.Azp
}

// isKMSMachinePrincipal reports whether a validated token is a per-org KMS-sync
// machine identity: its audience set contains the owner-bound machine audience
// (<owner>-platform-kms). Such a principal is a client_credentials machine identity
// scoped to exactly one org. SanitizeIdentity uses this to DENY it SuperAdmin
// authority even if it somehow carries isAdmin=true and owner==adminOrg, so V6's
// audience widening can never be leveraged (via an admin-org machine token) into a
// cross-org read. Its org-scoped data access is unaffected — this gates ONLY the
// admin grant, keeping the machine path decoupled from admin inside cloud (rather
// than resting on the external invariant "IAM never stamps isAdmin=true on a
// machine-aud token", which cloud cannot see or enforce).
func isKMSMachinePrincipal(claims *idClaims) bool {
	mach := kmsMachineAudience(claims.Owner)
	if mach == "" {
		return false
	}
	return slices.Contains(claims.Audience, mach)
}

// platformSudo and orgAdmin are cloud's reading of the two admin scopes. Each is
// the PUBLISHED predicate (authz.Claims — the issuer's own statement of what its
// claims mean) narrowed by the one denial only cloud can make.
//
// The grant is never restated here, and that is the point. cloud used to derive
// both scopes itself; the derivation drifted from the contract in the direction
// that matters. Platform sudo asked `homeOrg == adminOrg` — Orgs[0], a POSITIONAL
// read — while IAM always writes the user's own org at index 0, so an operator
// granted admin-org membership (the deliberate, signed, revocable way operators
// are made) could never satisfy it. authz.Claims.Sudo asks the question
// that was meant: is the reserved org anywhere in the signed set.
//
// THE NARROWING is the per-org KMS-sync machine, named by its owner-bound
// audience. authz decides machine-ness from the membership set — a
// client_credentials token carries none, which is correct for every machine IAM
// mints today — so a machine that DID carry memberships would read as a person
// there. cloud can name that identity and therefore denies it explicitly, on both
// scopes. It is a DENIAL layered over the grant, never a second route to one:
// removing it can only ever refuse more, never admit more.
//
// FAIL-CLOSED, and it costs something. A human token carrying no `orgs` — minted
// before that claim shipped — is not positively a person and loses both admin
// scopes. That is an availability cost bounded by the token TTL, taken deliberately
// over the alternative: admitting an unidentifiable principal to the only
// cross-tenant scope in the system.
func platformSudo(claims *idClaims) bool {
	return claims.Sudo() && !isKMSMachinePrincipal(claims)
}

// orgAdmin reports whether claims administer the org the request ACTS in. See
// platformSudo for why the grant is authz's and the denial is cloud's.
func orgAdmin(claims *idClaims, org string) bool {
	return claims.OrgAdmin(org) && !isKMSMachinePrincipal(claims)
}

// isMember reports whether org is in the token's signed membership set — the
// `orgs` claim IAM mints for a USER token, home org first. It is the ONE test that
// turns a client's org SELECTION into an effective org (SanitizeIdentity), and
// therefore into the ledger that pays (principal.BillingOrg).
//
// The comparison is VERBATIM, no folding, for the same reason the owner claim is
// taken verbatim: "acme" and "ACME" are DISTINCT orgs in IAM, and a fold would let
// a member of one select the other. An empty org is never a member, so an absent
// selection leaves the caller in their home org. An empty set (a legacy token, an
// opaque key, a machine principal — IAM never mints `orgs` for a client_credentials
// token) admits nothing, which is exactly the pre-claim behavior.
// The org-admin fact is authz.Claims.OrgAdmin's to state (see orgAdmin above).
// cloud used to re-derive it here, folding the role vocabulary itself; that
// derivation is deleted rather than kept beside the published one, because two
// readings of one claim is exactly the condition this package exists to end.
// Its history is worth keeping: matching only "admin" once locked every
// self-serve founder out of their own org, since IAM writes RoleOwner for
// whoever CREATES an org (EnsureMembership(..., RoleOwner), so a new org is not
// "born with nobody on it"). Role.Admits folds owner into admin, in the one
// place that vocabulary is defined. TestOrgAdminAdmitsOwner pins it end to end.

func isMember(orgs []authz.Membership, org string) bool {
	if org == "" {
		return false
	}
	for _, o := range orgs {
		if o.Org == org {
			return true
		}
	}
	return false
}

// validate parses raw, verifies its signature against the JWKS, and enforces
// issuer/audience/expiry. Returns the claims on success, an error otherwise.
func (v *identityValidator) validate(raw string) (*idClaims, error) {
	// Everything a token has to prove is proved in ONE place. This function used to
	// hold its own algorithm allowlist, its own key selection, its own issuer set
	// comparison and its own expiry check — four opportunities to answer differently
	// from the edge about the same token, and the key selection did: it fell back to
	// trying every RSA key in the JWKS, so a token naming one key was accepted on a
	// signature from another.
	//
	// Audience is deliberately NOT a gate here, which is why the verifier is built
	// with no allowlist. A valid signature from a trusted issuer already proves IAM
	// minted the token for one of ITS OWN registered apps; `aud` merely names which.
	// Cloud kept no mirror of IAM's app registry because that mirror drifted and
	// silently 401'd every new first-party app until someone hand-edited it.
	claims, err := v.verifier.VerifyRaw(raw)
	if err != nil {
		return nil, err
	}
	return &idClaims{Claims: *claims}, nil
}

// APIKeyPrefixes is every opaque-key spelling cloud recognizes at the door.
// This is the ONE authority. Admission mirrors it rather than importing it (it
// stays free of cloud-internal imports); if this list changes, that copy must too.
var APIKeyPrefixes = []string{"pk-", "sk-"}

// PublishablePrefix is the ONE publishable spelling: pk- is the key you may ship
// in a browser bundle, sk- is the one you may not. Stripe's split, same reason.
const PublishablePrefix = "pk-"

func IsPublishableKey(tok string) bool {
	return strings.HasPrefix(strings.TrimSpace(tok), PublishablePrefix)
}

// isAPIKey reports whether tok is an opaque, backend-validated key rather than a
// JWT, so the sanitizer skips JWT parsing for it.
func isAPIKey(tok string) bool {
	for _, p := range APIKeyPrefixes {
		if strings.HasPrefix(tok, p) {
			return true
		}
	}
	return false
}

// bearerFromAuth extracts the token from a "Bearer <token>" header value.
func bearerFromAuth(auth string) string {
	if auth == "" {
		return ""
	}
	parts := strings.SplitN(auth, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return ""
	}
	return strings.TrimSpace(parts[1])
}

// basicFromAuth extracts the token from an HTTP Basic header value: the password
// field (the go/.netrc proxy idiom), falling back to the username when empty.
func basicFromAuth(auth string) string {
	if auth == "" {
		return ""
	}
	parts := strings.SplitN(auth, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Basic") {
		return ""
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(parts[1]))
	if err != nil {
		return ""
	}
	user, pass, ok := strings.Cut(string(raw), ":")
	if !ok {
		return ""
	}
	if pass != "" {
		return pass
	}
	return user
}

// trustedIssuers returns the full trusted-issuer set for the in-binary validator:
// the PRIMARY issuer (the deployment's own brand, cfg.IAMIssuer) UNIONED with every
// white-label brand issuer (BrandIssuers) and any WHITELABEL_ISSUERS override
// (comma-separated). Fail-secure: it only ADDS known-good issuers; a nil/empty
// result is impossible when a primary is set, so the issuer check is always
// enforced. Duplicates are removed; order is primary-first.
func trustedIssuers(primary string) []string {
	out := make([]string, 0, 6)
	add := func(v string) {
		v = strings.TrimSpace(v)
		if v == "" {
			return
		}
		if slices.Contains(out, v) {
			return
		}
		out = append(out, v)
	}
	add(primary)
	for _, iss := range BrandIssuers() {
		add(iss)
	}
	for _, iss := range splitTrim(os.Getenv("WHITELABEL_ISSUERS")) {
		add(iss)
	}
	return out
}

// issuerAllowed reports whether iss is one of the trusted issuers. It is
// fail-secure in BOTH directions: an empty trusted set matches NOTHING (deny), so
// a misconfiguration that empties the issuer allowlist rejects every token instead
// of silently disabling the check (I2); a non-empty set rejects any iss not in it.
func issuerAllowed(iss string, trusted []string) bool {
	return slices.Contains(trusted, iss)
}
