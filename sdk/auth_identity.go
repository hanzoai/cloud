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
	"crypto"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/hanzoai/authz"
	"github.com/hanzoai/authz/edge"

	"github.com/hanzoai/cloud/sdk/scrub"
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
	// key is the API key's id within its org, read from IAM with the principal it
	// resolves, for the same reason grant is: no token carries it.
	key string
	// keyOrg is the org the key is filed in, when the caller selected another org
	// for the request and IAM admitted it (iamKeys.resolveIn). key names a row
	// THERE, not in subjectOrg, so the key's own policy is addressed by it. Empty
	// when the key acts where it is filed.
	keyOrg string

	// acted is whether the verified token names an actor (`act`), read by
	// validate from the payload the verifier proved. UNEXPORTED AND UNTAGGED
	// like the two above: nothing decodes into it, so no request can set it.
	acted bool
	// operator is the platform operator behind a SuperAdmin impersonation token —
	// IAM's `imp: true`, with the operator named in `act {sub, owner, name}` — and
	// zero on every other token. The token's own claims are the target's, so this
	// is the one field that says someone else is at the keyboard. Read like acted,
	// from the verified payload; act.sub is minted downstream as
	// zip.HeaderActedBy, and the whole of it rides the minted principal.
	operator principal.Act
	// confined is whether the verified token's scope names ai:inference
	// (confined.go): a delegated token, which reaches model calls and nothing else.
	// Read like acted, from the verified claims; nothing decodes into it.
	confined bool
	// agent is who acts FOR the subject when nobody is impersonating them: act.sub
	// of a delegated token (the deployment that runs the person's work) or of an
	// org key's as() token. Recorded beside the subject wherever the act is, never
	// read to admit anything. Empty for a person acting as themselves, and for an
	// impersonation, whose operator is the field above.
	agent string
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
//
// An APPLICATION's is marked. IAM signs a machine's `sub` as its application's
// `<owner>/<name>` — the shape an API key's subject takes for a person — and an
// org admin names the applications their org owns. An application named after a
// member would otherwise hold that member's key-held sandboxes and sessions, and
// the member the application's. `app:` is in no username, no UUID and no row key,
// so the three kinds of subject cannot meet. The token's `sub` itself is carried
// verbatim beside it (Principal.Subject).
func (c *idClaims) userID() string {
	if c.Subject != "" {
		if appPrincipal(c) {
			return appSubject + c.Subject
		}
		return c.Subject
	}
	if c.PreferredUsername != "" {
		return c.PreferredUsername
	}
	return c.Name
}

// appSubject marks an application's user id (userID).
const appSubject = "app:"

// kind is which credential these claims came from, minted as Principal.Kind: an
// API key, which only the key resolver marks (subjectOrg); an application acting
// as itself (appPrincipal), the kind userID marks; and otherwise a person's
// token, whose user id is its `sub`. The three are the kinds userID keeps
// disjoint, so a consumer that serves one of them asks this rather than the
// shape of a subject two kinds share.
func (c *idClaims) kind() principal.Kind {
	switch {
	case c.subjectOrg != "":
		return principal.Key
	case appPrincipal(c):
		return principal.App
	}
	return principal.Person
}

// stable reports whether userID is a subject no two principals share: a
// person's token `sub` (a UUID), an application's marked `app:<owner>/<name>`, or
// — for an API key, whose claims the key resolver built from the IAM user row it
// belongs to (subjectOrg) — that row's `<owner>/<name>`, home key and member's
// key alike. A token with no `sub` is not: userID falls back to its username and
// then to a display name. Minted as Principal.Stable, which a record one person
// holds is keyed by (principal.SubjectFrom).
func (c *idClaims) stable() bool { return c.Subject != "" || c.subjectOrg != "" }

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
// (sdk/meter/wallet.go). Preferring `name` addressed `hanzo/Zach Kelling`,
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

// Boundary validates an IAM JWT against a cached JWKS. Issuer (any of a
// trusted SET) + audience + expiry are always enforced.
//
// The issuer is a SET so ONE cloud binary validates every white-label brand's
// tokens (hanzo iss=hanzo.id AND lux iss=lux.id, ...). Signature verification is
// unaffected: the in-cluster IAM serves EVERY brand's signing cert in one JWKS
// (cert-hanzo/cert-lux/cert-zoo/...), keyed by the token kid, so a single
// jwksURL verifies all brands. Only the issuer-string comparison had to widen.
type Boundary struct {
	// verifier is the estate's ONE credential check (hanzoai/authz/edge): the JWKS
	// reader, the algorithm allowlist, kid-bound key selection, the issuer allowlist
	// and expiry. cloud used to hold its own of each, and each was a chance to differ
	// from the edge about what a token proves.
	//
	// One verifier per provider whose discovery document names it, built from that
	// document: a token is routed by the issuer it claims (unverified, and only to pick
	// the verifier), and the verifier then holds it to that issuer and to the keys the
	// provider's own document points at.
	sources []*Discovery
	jwksTTL time.Duration
	vmu     sync.Mutex
	held    map[string]heldVerifier
	// issuers is the trusted-issuer set, kept for the one other key a token here may
	// be signed by: IAM's delegation key, which the public JWKS never carries
	// (delegation.go), and against which a confined token — and nothing else — is
	// verified. It is the issuers the discovery URLs imply, known without a fetch.
	issuers    []string
	delegation *delegationKeys
	// live asks IAM whether a confined token's grant still stands (delegation.go):
	// the kill switch a token verified offline would otherwise not have.
	live *liveness
	keys keyResolver // resolves an opaque API key to a principal; nil ⟹ keys stay anonymous
	// claims memoizes token ⇒ verified claims so an already-authenticated caller
	// (notably a ZAP socket, which replays its credential on every frame) is not
	// signature-verified again per call. See identity_cache.go.
	claims *identityCache
}

// heldVerifier is a verifier and the JWKS URL it was built for.
type heldVerifier struct {
	jwks string
	v    *edge.Verifier
}

// newBoundary builds the validator for the providers at urls, each by its OIDC
// discovery document (discovery.go). Trust is IAM-native: signature (the provider's
// JWKS) + issuer (the one its document names) + expiry. There is NO per-app audience
// allowlist — the `aud` (a minting app's client_id) is IAM's to assign, not cloud's
// to mirror, so a new first-party app needs zero cloud change. ttl<=0 uses the 15m
// JWKS cache default.
func newBoundary(urls []string, client *http.Client, ttl time.Duration) (*Boundary, error) {
	b := &Boundary{
		jwksTTL:    ttl,
		held:       map[string]heldVerifier{},
		delegation: &delegationKeys{},
		live:       &liveness{},
		claims:     newIdentityCache(),
		keys:       sharedKeys(), // ONE resolver+cache, shared with OrgForKey (analytics capture)
	}
	for _, u := range urls {
		d, err := DiscoveryFor(u, client)
		if err != nil {
			return nil, err
		}
		if slices.Contains(b.issuers, d.Issuer()) {
			continue
		}
		b.sources = append(b.sources, d)
		b.issuers = append(b.issuers, d.Issuer())
	}
	if len(b.sources) == 0 {
		return nil, errors.New("identity: no OIDC discovery URL, so no issuer to trust")
	}
	return b, nil
}

// verify checks raw against the provider whose issuer it claims. A claim no
// provider here owns is refused before any key is read; a provider that has never
// published a document fails closed.
func (v *Boundary) verify(raw string) (*authz.Claims, error) {
	iss := claimedIssuer(raw)
	var src *Discovery
	for _, d := range v.sources {
		if d.Issuer() == iss {
			src = d
		}
	}
	if src == nil {
		return nil, fmt.Errorf("identity: issuer %q is not trusted", iss)
	}
	doc, err := src.Document()
	if err != nil {
		return nil, err
	}
	v.vmu.Lock()
	h, ok := v.held[iss]
	if !ok || h.jwks != doc.JWKSURI {
		h = heldVerifier{jwks: doc.JWKSURI, v: edge.NewVerifier(doc.JWKSURI, []string{iss}, nil, v.jwksTTL)}
		v.held[iss] = h
	}
	v.vmu.Unlock()
	return h.v.VerifyRaw(raw)
}

// claimedIssuer is the `iss` a token CLAIMS, read without trusting it: it only
// chooses which provider's keys the token is then verified against.
func claimedIssuer(raw string) string {
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return ""
	}
	b, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	var p struct {
		Iss string `json:"iss"`
	}
	if json.Unmarshal(b, &p) != nil {
		return ""
	}
	return p.Iss
}

// claimedAudience reads the primary `aud` (or `azp`) from a JWT's payload. Only
// called on a bearer that has already passed signature/issuer/audience checks.
func claimedAudience(raw string) string {
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return ""
	}
	b, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	var p struct {
		Aud any    `json:"aud"`
		Azp string `json:"azp"`
	}
	if json.Unmarshal(b, &p) != nil {
		return ""
	}
	switch v := p.Aud.(type) {
	case string:
		if v != "" {
			return v
		}
	case []any:
		for _, item := range v {
			if s, ok := item.(string); ok && s != "" {
				if p.Azp != "" && s == p.Azp {
					return s
				}
			}
		}
		for _, item := range v {
			if s, ok := item.(string); ok && s != "" {
				return s
			}
		}
	}
	return p.Azp
}

// kmsMachineAudSuffix is the fixed suffix of a per-org PaaS-KMS sync machine
// identity's audience. Each org's KMS sync authenticates as a dedicated, NON-shared
// IAM application named "<org>-platform-kms" (Organization=<org>, client_credentials
// grant), so IAM stamps the token's aud == the app's own clientId == "<org>-platform-kms"
// (object/token_jwt.go tokenAudience) and owner == <org> (object/token_oauth.go
// GetClientCredentialsToken sets owner = app.Organization).
//
// Validation does not consult the audience, and neither does anything else: a
// machine is one because IAM signs `type: application`, and Sudo refuses every
// machine, so a client_credentials identity cannot wield platform-admin whatever
// its token is addressed to. This suffix survives only as the NAME the provisioner
// gives such an application. The match is bound to the token's OWN owner claim
// (<owner>-platform-kms), so it certifies "the
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
// "<org>-platform-kms" is derived in exactly one place: here.
func KMSMachineClientID(org string) string { return kmsMachineAudience(org) }

// homeOrg returns the USER's own organization — the tenant whose ledger pays and
// the `owner` the SuperAdmin rule (sudo) compares. It reads the FIRST entry of the
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
// (/v1/agent, /v1/gpus, /v1/billing/*) in v1.801.244 while leaving unscoped
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
// boundary — the kind denies the admin grant and nothing else, so that access
// keeps working).
// member reports a key principal acting in an org its holder belongs to by
// membership rather than their own.
func (c *idClaims) member() bool {
	return c != nil && c.subjectOrg != "" && c.subjectOrg != c.Owner
}

func (c *idClaims) homeOrg() string {
	if c.subjectOrg != "" {
		return c.subjectOrg // API key: resolved from the subject
	}
	if appPrincipal(c) {
		return c.Owner // machine JWT: the app IS the principal
	}
	// Everything else is the estate rule: the first entry of the signed membership
	// set, or empty. Stated once, in the leaf; the two branches above are the facts
	// only cloud has, because only cloud authenticated the credential that carries
	// them.
	return c.Claims.Home()
}

// defaultOrg is the org a request acts in when it selects none. A key acts in the
// org IAM resolved it to and an application in its own, and both arrive here as
// owner. IAM names a key's org only while its holder's membership opens it — the
// rule authz.Claims.opens states for a token, asked of the holder's row
// (iam store.Acts) — and refuses the key otherwise (keyRefusal), so the org taken
// here is never one the holder lost. A person acts in the first org their signed
// membership set opens (authz.Claims.Default): the home for most, a directory
// customer's personal org, and nothing at all for a plain member of an org the
// platform runs, whose every org gate then refuses.
func (c *idClaims) defaultOrg(owner string) string {
	// IAM has already authorized this delegation for exactly one org. It strips
	// administrative roles from the token, so Default's ordinary membership
	// rule would erase a platform-org operator's billing tenant. The verified
	// delegation opens model calls only; it confers no administrative access.
	if c.confined {
		return owner
	}
	if owner == "" || c.subjectOrg != "" || appPrincipal(c) {
		return owner
	}
	return c.Claims.Default()
}

// appPrincipal reports whether these claims belong to an APPLICATION acting as
// itself — a client_credentials token, whose organization is the application's own
// (`owner`, set by IAM when the app was created) and is not selectable: IAM mints
// such a token no membership set, so SanitizeIdentity's org-switch admits nothing
// and the effective org is always that owner.
//
// It is the fact homeOrg's machine branch already turns on, named ONCE so the
// boundary can also STATE it (HeaderUserIsApp). Two readings of one fact is the
// condition this file exists to end.
//
// It is a KIND, not a role, and it grants nothing by itself. An application holds
// neither admin scope — authz.Claims.OrgAdmin refuses every machine by
// construction, because an app is issued for a purpose and is not handed an org's
// self-service surface, and sudo refuses every machine. What
// it does carry is one org it cannot choose, which is what an endpoint whose act
// IS a purpose needs to bound that act.
//
// An sk- API KEY is deliberately NOT one, even though authz reads it as a machine
// for want of an `orgs` claim. A key resolves to a PERSON'S row — that is what
// subjectOrg records — so its authority is that person's, and reading it as an
// application would hand every member of an org whatever the org's own machine
// identity may do.
// An application is a MACHINE first, asked through the predicate authz publishes,
// and only then one acting as itself. That ordering is what keeps this kind and
// platform sudo mutually exclusive by construction rather than by coincidence:
// sudo refuses every machine, so nothing can be both. Recognising the shape
// alone would leave the two merely unlikely to coincide, which is a property that
// holds until some issuer mints a token nobody anticipated.
// IAM SAYS SO. `type: application` is a signed claim stamped in one place — the
// client_credentials grant — and left empty for every person, so the kind is the
// issuer's statement and this file does not infer it.
//
// THE HEADER OF THIS FILE WARNS AGAINST EXACTLY THIS, and the warning was right
// when it was written: an earlier copy declared a `type` claim IAM emitted
// nowhere and read it as the discriminator, so every machine arrived as a human.
// What changed is the issuer, not the appetite for reading claims. Checked
// there rather than here: iam declares it (internal/oidc/jwt.go), names it
// (pkg/schema/user.go), and sets it at one call site inside
// clientCredentialsGrant — and the version this binary embeds emits it, which
// is the question the old copy never asked.
//
// There is nothing else to ask. Two shape readings used to stand here, one
// matching an owner-bound KMS audience and one matching sub/azp/aud; both were
// answering the question the claim now answers, and one of them answered it
// WRONG — it required the sole audience to equal azp, which a machine token
// minted with `?resource=` does not satisfy, so a legitimate program was refused
// for naming what it was for.
//
// The API key stays out by its own fact: an sk- key resolves to a PERSON's row,
// which is what subjectOrg records, so its authority is that person's and reading
// it as an application would hand every member of an org whatever the org's own
// machine identity may do.
func appPrincipal(c *idClaims) bool {
	return c != nil && c.subjectOrg == "" && c.Program()
}

// holderOf names who holds an ACCESS TOKEN, and the client it was issued to,
// for the one reader that keys authority on the holder rather than on the org:
// KMS's grant rule (apps/kms/policy.go).
//
// An application is its clientId, which IAM signs into `azp` for both machine
// grants — client_credentials and the workload assertion — so the name a grant
// declares is the name IAM minted against. A person is "<home org>/<username>",
// the `<owner>/<name>` key IAM itself uses, read from preferred_username and
// never from the display name: a holder is an identity key, and a display name
// is neither unique nor stable.
//
// Three credentials hold nothing, by construction. An id_token is proof of a
// sign-in addressed to a client, not a credential to act with. An sk- key
// resolves to a person's row, and naming it as that person would hand every
// leaked key whatever the person was granted. And a token that names an ACTOR
// (RFC 8693 `act`, IAM's as()) is an org key acting for a member: its claims
// are the member's, so naming it as the member would let any org key an org
// admin mints exercise that member's grants. A confined token (confined.go) holds
// nothing either: it was delegated for model calls, and a secret is not one.
func holderOf(c *idClaims) (holder, client string) {
	if c == nil || c.acted || c.confined || c.TokenType != "access-token" || c.subjectOrg != "" {
		return "", ""
	}
	client = strings.TrimSpace(c.Azp)
	if appPrincipal(c) {
		if client == "" {
			return "", ""
		}
		return client, client
	}
	if h, u := c.Claims.Home(), c.Claims.Username(); h != "" && u != "" {
		return h + "/" + u, client
	}
	return "", ""
}

// actor is what a VERIFIED token says about who acted: whether it names an actor
// at all — RFC 8693's `act`, which IAM signs into an as() token beside the
// member's own claims — and, for a SuperAdmin impersonation token (`imp: true`),
// the operator `act` names.
type actor struct {
	acted bool
	imp   bool
	// sub is `act.sub`, or `act` itself when IAM signs it as a bare string.
	sub string
	// owner and name are `act.owner` and `act.name`: the operator's IAM row.
	owner, name string
}

// actorOf reads `act` and `imp` off a VERIFIED token. The verifier has already
// proven the signature, so the payload read here is the one IAM signed; one that
// does not decode names an actor, so the answer fails toward holding nothing.
func actorOf(raw string) actor {
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return actor{acted: true}
	}
	b, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return actor{acted: true}
	}
	var p struct {
		Act json.RawMessage `json:"act"`
		Imp json.RawMessage `json:"imp"`
	}
	if json.Unmarshal(b, &p) != nil {
		return actor{acted: true}
	}
	a := actor{acted: len(p.Act) > 0 && string(p.Act) != "null"}
	// `imp` is a boolean in IAM's contract. Read raw, anything but absent, null or
	// false is an impersonation: a token whose impersonation is merely spelled
	// differently must not pass as the target acting alone.
	switch string(p.Imp) {
	case "", "null", "false":
	default:
		a.imp = true
	}
	var named struct {
		Sub   string `json:"sub"`
		Owner string `json:"owner"`
		Name  string `json:"name"`
	}
	if json.Unmarshal(p.Act, &named) == nil {
		a.sub = strings.TrimSpace(named.Sub)
		a.owner, a.name = strings.TrimSpace(named.Owner), strings.TrimSpace(named.Name)
	} else {
		var bare string
		if json.Unmarshal(p.Act, &bare) == nil {
			a.sub = strings.TrimSpace(bare)
		}
	}
	return a
}

// The impersonation rule, IAM's own (iam LLM.md "Impersonation") asked again of
// the token: a SuperAdmin signed in AS a person in a tenant. A token outside it
// authenticates no one. With no operator every act is the target's and nobody
// answers for it; an operator outside the admin org is no SuperAdmin; a target
// with platform authority, or no membership set, hands one operator another's
// authority under a mark that reads as support.
var (
	errAnonymousOperator = errors.New("identity: impersonation token names no operator in act")
	errOperatorNotSuper  = errors.New("identity: impersonation token names an operator outside the admin org")
	errImpersonatedSuper = errors.New("identity: impersonation token targets a SuperAdmin or a machine")
)

// impersonation is nil when a VERIFIED `imp` token meets the rule above.
func impersonation(c *authz.Claims, a actor) error {
	switch {
	case a.sub == "":
		return errAnonymousOperator
	case a.owner != authz.AdminOrg:
		return errOperatorNotSuper
	case sudo(c) || c.Machine():
		return errImpersonatedSuper
	}
	return nil
}

// sudo is the SuperAdmin rule, decided on the token itself: owner == "admin".
//
// The owner is the org IAM keeps the person's user row in, which IAM signs as the
// first entry of `orgs` (store.MemberOrgRefs opens with {user.Owner, …}) — the same
// `owner` IAM's store.IsSuperAdmin compares. It is not the `owner` CLAIM, which is
// the org of whichever application minted the token.
//
// Membership of the reserved org is not it. A person whose row lives in a brand org
// and who holds an admin-org membership is an ordinary member of that org, and
// platform authority goes to named users provisioned in the admin org itself.
//
// A machine is never one: authz.Claims.Machine is true for a client_credentials
// application, for a confidential client and for any token without a membership
// set — which is also every API key, since a key resolves to a row and carries no
// `orgs`. That fails closed: a person's token that lost its membership set loses
// platform authority rather than gaining it.
//
// This is the only reading of the rule on a token. The boundary writes
// X-User-IsAdmin from it and principal.Super reads that header, so the question is
// asked in one place and answered in one place. The org is compared verbatim:
// folding case or space would make an org someone can self-serve the reserved one.
func sudo(c *authz.Claims) bool {
	return c != nil && !c.Machine() && c.Home() == authz.AdminOrg
}

// Acts reports whether a validated token may act in org — the rule for the org a
// caller SELECTS, asked by the identity boundary for X-Org-Id and by any surface
// that runs a request in an org its caller named.
//
// A SuperAdmin may act in any org. Anyone else may act in an org its signed
// membership set opens (authz.Claims.Tenancy): never the reserved admin org, never
// a brand's directory, and an org the platform runs only as its owner or admin.
// A membership a person holds there as a plain member — a customer who landed in a
// brand org — selects nothing, so the brand's data stays its staff's. An org that
// is empty or bears an unsafe rune is refused outright.
//
// It is authz.Claims.EffectiveOrg asked of one org, so the boundary and every
// surface that names an org read the one rule.
func Acts(c *authz.Claims, org string) bool {
	if c == nil || org == "" || authz.HasUnsafeRune(org) {
		return false
	}
	got, _ := c.EffectiveOrg(org)
	return got == org
}

// orgAdmin reports whether claims administer the org the request ACTS in: the
// published authz.Claims.OrgAdmin, which refuses every machine and scopes the IAM
// `isAdmin` bit to the home org.
func orgAdmin(claims *idClaims, org string) bool {
	return claims.OrgAdmin(org)
}

// The org-admin fact is authz.Claims.OrgAdmin's to state (see orgAdmin above).
// cloud used to re-derive it here, folding the role vocabulary itself; that
// derivation is deleted rather than kept beside the published one, because two
// readings of one claim is exactly the condition this package exists to end.
// Its history is worth keeping: matching only "admin" once locked every
// self-serve founder out of their own org, since IAM writes RoleOwner for
// whoever CREATES an org (EnsureMembership(..., RoleOwner), so a new org is not
// "born with nobody on it"). Role.Admits folds owner into admin, in the one
// place that vocabulary is defined. TestOrgAdminAdmitsOwner pins it end to end.

// orgSlugs renders a membership set as the comma-separated slug list
// HeaderUserOrgs carries, in IAM's own order (home first).
//
// It is a RENDERING and not a decision: every entry is kept, roles are dropped
// because the header answers "which orgs may this caller act in" and a role is a
// separate question each org's own surface asks. An empty or duplicate slug is
// skipped so the value cannot claim an org twice or name an empty one — the two
// shapes a consumer would have to defend against otherwise.
func orgSlugs(orgs []authz.Membership) string {
	seen := make(map[string]bool, len(orgs))
	out := make([]string, 0, len(orgs))
	for _, o := range orgs {
		if o.Org == "" || seen[o.Org] {
			continue
		}
		seen[o.Org] = true
		out = append(out, o.Org)
	}
	return strings.Join(out, ",")
}

// validate parses raw, verifies its signature against the JWKS, and enforces
// issuer/audience/expiry. Returns the claims on success, an error otherwise.
func (v *Boundary) validate(raw string) (*idClaims, error) {
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
	// A confined token is verified against IAM's delegation key and nothing else,
	// and nothing else is verified against that key (confined.go).
	if presentsConfined(raw) {
		return v.validateConfined(raw)
	}
	claims, err := v.verify(raw)
	if err != nil {
		return nil, err
	}
	a := actorOf(raw)
	c := &idClaims{Claims: *claims, acted: a.acted}
	if a.acted && !a.imp {
		c.agent = a.sub
	}
	if a.imp {
		if err := impersonation(claims, a); err != nil {
			return nil, err
		}
		c.operator = principal.Act{Sub: a.sub, Owner: a.owner, Name: a.name}
	}
	return c, nil
}

// validateConfined verifies a token whose payload names the inference scope: its
// signature against IAM's DELEGATION key only (delegation.go) — a confined token
// signed by a published key was not minted by a delegation, and is refused — its
// audience against this deployment's own API origin, and an actor named in `act`
// (the deployment that delegated). errDelegationKeys means there was nothing to
// verify against, which the boundary answers differently from a bad token.
func (v *Boundary) validateConfined(raw string) (*idClaims, error) {
	unreadable := false
	keys := func(kid string) []crypto.PublicKey {
		found, err := v.delegation.resolve(kid)
		if err != nil {
			unreadable = true
		}
		return found
	}
	claims, err := authz.Verify(raw, keys, v.issuers)
	if err != nil {
		if unreadable {
			return nil, errDelegationKeys
		}
		return nil, err
	}
	if !confinedScope(claims.Scope) {
		return nil, errors.New("identity: a token under the delegation key that is not confined")
	}
	if len(claims.Orgs) != 1 || claims.Home() == "" {
		return nil, errors.New("identity: a delegated token must name exactly one organization")
	}
	if !slices.Contains(claims.Audience, delegatedAudience()) {
		return nil, errors.New("identity: a delegated token addressed to another API")
	}
	a := actorOf(raw)
	if !a.acted || a.imp || a.sub == "" {
		return nil, errors.New("identity: a delegated token names no delegating actor")
	}
	return &idClaims{Claims: *claims, acted: true, confined: true, agent: a.sub}, nil
}

// APIKeyPrefixes is every opaque-key spelling cloud recognizes at the edge, and
// the value itself lives in sdk/scrub — a leaf, so a caller that must not
// link package cloud can still read the authority rather than keeping a copy in
// step by hand. apps/admission used to keep such a copy, and this comment used to
// ask the next reader to remember it; admission retired it (middleware.go says
// why), and the leaf is what makes remembering unnecessary for the next one.
var APIKeyPrefixes = scrub.Prefixes

// PublishablePrefix is the ONE publishable spelling: pk- is the key you may ship
// in a browser bundle, sk- is the one you may not. Stripe's split, same reason.
const PublishablePrefix = "pk-"

func IsPublishableKey(tok string) bool {
	return strings.HasPrefix(strings.TrimSpace(tok), PublishablePrefix)
}

// IsAPIKey reports whether tok is an opaque, backend-validated key rather than a
// JWT, so the sanitizer skips JWT parsing for it.
func IsAPIKey(tok string) bool { return scrub.IsKey(tok) }

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
