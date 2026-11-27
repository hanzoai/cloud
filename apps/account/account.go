package account

import (
	"cmp"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/object"
	"github.com/hanzoai/authz"

	"github.com/hanzoai/cloud/sdk/audit"
	"github.com/hanzoai/cloud/sdk/bus"
	"github.com/hanzoai/cloud/sdk/iam"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/zap-proto/zip"

	luxlog "github.com/luxfi/log"
)

// prefix is THE path this capability answers under. HIP-0139 §3: every route a
// capability serves is under /v1/<name>, and a second top-level address is a
// second capability, never an alias. Six of these routes used to answer at the
// bare root — /v1/keys, /v1/orgs, /v1/appearance, /v1/avatar, /v1/csrf,
// /v1/embed — which is what put six lines in openapi/misfiled.txt. One
// constant, so the three groups below cannot drift apart, and because cmd/zipdoc
// resolves a typed op's prefix from the CONSTANT VALUE of the Group argument.
const prefix = "/v1/account"

// adminOrg is THE SuperAdmin / IAM-system org that owns every customer org row —
// standardized as "admin" across the whole stack (IAM, commerce, ai, gateway all
// gate cross-tenant on owner=="admin"). A created customer org is owned by it.
const adminOrg = "admin"

// errNotConfigured is returned by the IAM client when the confidential
// `hanzo-console` credential is unset; handlers map it to a 501 (honest "not
// configured on this deployment", mirroring identity.ts's mintConfigured() gate).
var errNotConfigured = errors.New("iam confidential client not configured")

// errNotFound is a not-present sentinel (e.g. the user row IAM cannot return).
var errNotFound = errors.New("not found")

// state is account's own data; shared deps live in the embedded cloud.Base. The
// CSRF key is the process-wide singleton (csrf.go), so a token minted at the csrf
// route verifies on whatever money write echoes it — including the co-resident commerce
// writes mounted from another package.
type state struct {
	iam      *iamClient
	writesRL *rateLimiter // per-IP abuse cap on the money-write routes (ratelimit.go)
	// vfs is cloud's blob client (deps.VFS) — where a profile photo's bytes live
	// (avatar.go). NewBase does not carry it, so it is taken from deps here, the
	// same way apps/team's files plane takes it.
	vfs object.Blobs
	// trail is the audit trail an act on another person's key is recorded on
	// (keys.go): an org admin revoking a member's key acts with authority over
	// someone else's credential.
	trail *audit.Recorder
}

// keysWriteRatePerMin caps money-write frequency per client IP (mint/rotate/revoke
// key, wallet top-up). Generous enough for real UI bursts, tight enough to blunt
// brute-force / enumeration when a caller reaches cloud directly (gateway bypassed).
const keysWriteRatePerMin = 30

// newService builds the subsystem value. The CSRF key is the process-wide
// singleton (csrf.go), so a token minted here verifies wherever it is echoed — and
// `own` is the boot question about that key this app, as the MINTER, must answer.
func newService(deps cloud.Deps, s3 object.Blobs) (*cloud.Service[state], error) {
	b := cloud.NewBase(deps, "account")
	st := state{iam: newIAMClient(), vfs: s3, trail: deps.Audit}
	st.writesRL = newRateLimiter(keysWriteRatePerMin)
	return &cloud.Service[state]{Base: b, State: st}, nil
}

// Use mounts account, resolving the object store the deployment gives it.
//
// The store used to arrive as a field on Deps, resolved once for the whole fleet
// whether or not a given subsystem stored a byte. It is built here instead,
// because building it is free and because a subsystem that is handed another
// subsystem's handle is a subsystem that cannot be read on its own.
//
// The store is the ONLY thing separating this from useWith, which is what the
// tests drive: the resolution is the deployment's, the behaviour is the app's,
// and neither has to pretend to be the other.
func Use(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return errors.New("account: nil app")
	}
	return useWith(app, deps, cloud.S3(luxlog.Default()))
}

// useWith wires account's self-service routes (order 48) — the ones that must
// win over the IAM /v1/iam/* wildcard (50) and the commerce embed (100).
func useWith(app cloud.Router, deps cloud.Deps, s3 object.Blobs) error {
	s, err := newService(deps, s3)
	if err != nil {
		return err
	}
	if err := routesAccount(s, app); err != nil {
		return err
	}
	s.Log.Info("account self-service surface mounted",
		"iam", s.State.iam.base, "configured", s.State.iam.configured(), "brand", s.Brand)
	return nil
}

// zipdoc lifts the doc comment off each typed op and its In/Out fields into
// zipdoc_gen.go, which is the ONLY way that prose reaches the published document
// and the MCP tool list — Go drops comments at compile time. Run by
// `make -C apps/account openapi`.
//
//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

// routesAccount wires the specific self-service routes (order 48).
func routesAccount(s *cloud.Service[state], app cloud.Router) error {
	// The composer owns cloud.Bridge: the fused host installs it once at its root
	// and the plugin constructor does the same for a plugin program, so no
	// subsystem installs it.

	o := ops{s: s}

	// The request pipelines. The trailing Group is the path prefix these routes
	// share, and each op's identity is that prefix composed with its leaf.
	//
	// Every operation here is registered through the ROUTER the composer handed
	// us, never through the bare zip app underneath it. The router keeps the
	// program's own App and carries this prefix itself, so an op declared through
	// it answers to the rule the composer installed over the whole program
	// (serve.go's Toll) and its path is the address it actually answers at. A
	// group taken off the bare app is a different App with an empty prefix, and an
	// op declared there is outside both.
	//
	// The two middleware values below are for the AVATAR routes, which are raw
	// handlers with no preamble to put a control in. They are not what controls the
	// typed writes: `With` composes around the leaf HANDLER at registration time
	// and around nothing else, while op.invoke and op.direct are built before the
	// wrap and are what MCP, the call plane, the graph and the CLI call. The four
	// typed writes therefore carry both controls themselves — see requestCaller.
	limit, csrf := rateLimit(s.State.writesRL), requireCSRF()
	open := app.Group(prefix)  // reads
	write := app.Group(prefix) // writes: the controls live in the operations

	// The anti-CSRF token the embedded SPA echoes as X-CSRF-Token on every money
	// write (csrf.go). Safe (read-only), same-origin.
	open.Get("/csrf", o.issueCSRFToken)
	// The caller's own API keys. ONE noun, the methods carry the operations, and the
	// key TYPE (publishable | secret) is a FIELD — the concept had four names
	// (/v1/iam/keys/mint, /v1/iam/keys/revoke, /v1/iam/keys,
	// /v1/ingest/keys) and the only honest one 404'd.
	//
	// It lives OUTSIDE /v1/iam on purpose, and that is not cosmetic: api.hanzo.ai
	// routes /v1/iam/* straight to the IAM service (ingress router
	// api-hanzo-ai-iam-api), so the handler below was UNREACHABLE at the only host
	// callers use — every request landed on IAM's Guard and 401'd. A key surface
	// spelled as if it belonged to IAM was answered by IAM. Naming it for the
	// resource instead of the subsystem that stores it is what makes it reachable.
	//
	// Reads are open; every state-changing WRITE carries the anti-CSRF control and
	// the frequency cap in its own preamble (requestCaller). A key is addressed by
	// its id, so each write acts on exactly one key and no other.
	open.Get("/keys", o.listKeys)
	write.Post("/keys", o.createKey)
	write.Patch("/keys/:id", o.editKey)
	write.Delete("/keys/:id", o.revokeKey)
	// Creating the caller's organization, named for the RESOURCE — the same rule
	// that moved the key surface off /v1/iam/keys, applied to the one route it had
	// not reached. It was POST /v1/iam/onboard, which put a cloud handler inside
	// IAM's prefix, and api.hanzo.ai routes /v1/iam/* to IAM: this handler answered
	// nothing in production, measurably (that address returns IAM's own
	// {"status":401,"error":"authentication required"} from server: zip, with no
	// Deprecation header and no x-api-version — it never reached cloud). IAM owns
	// /v1/iam/onboard and serves its own first-run onboarding there. This is the
	// richer operation and it is now reachable for the first time: it also creates
	// an ADDITIONAL org for a caller who already has one, without moving them.
	write.Post("/orgs", o.onboard)
	// Console module embed-entitlement + reachability probe (embed.go).
	open.Get("/embed", o.embedStatus)
	// The crypto wallet top-up (POST /commerce/topup/wallet + GET /commerce/topup/rails)
	// used to mount here. It verified an on-chain transfer and then recorded the credit
	// to commerce at POST /v1/billing/payment — an address NO app in either server repo
	// has EVER registered, in any commit. So the last step of the only path that credited
	// anything always failed, and a customer who had already sent real USDC to the
	// treasury got a 502 for it. It was 501 besides: TOPUP_RAILS is configured in no
	// environment, so `configured()` was false everywhere and the surface never took a
	// cent.
	//
	// It is not a rename and there was nothing to point it at. Money-IN has ONE endpoint
	// (commerce's mint-gated POST /v1/billing/deposit, which requires an
	// X-Idempotency-Key naming the settlement or tx hash that caused the credit), and
	// the fleet deliberately routes NO mint address at the edge — the only two money-in
	// paths manifest.Apps hands to an app are the card ones, both with a
	// server-authoritative amount. Wiring this to the mint would newly expose that
	// surface, which is a money decision and not a routing fix, so the phantom is
	// deleted rather than plumbed. Deciding to accept crypto is a product decision that
	// starts from the mint gate, not from this handler.

	// The signed-in user's profile photo (avatar.go). Both are UNTYPED and cannot be otherwise —
	// multipart in, raw image bytes out — which is why they are the only two names
	// in typed_wire_test.go's refusal list.
	open.Raw(http.MethodPost, "/avatar", limit(csrf(o.putAvatar)))
	open.Raw(http.MethodGet, "/avatar/:org/:user/:digest", o.getAvatar) // no gate: an <img src> from another origin

	// The caller's own appearance preference — text size, density, accent — stored
	// on their IAM account so it follows them across devices and every Hanzo surface
	// (appearance.go). The read is open (a surface applies it on load); the write is
	// CSRF-guarded like the other account writes, and not rate-limited because a
	// person dragging the size/density steps writes faster than a money cap allows.
	open.Get("/appearance", o.getAppearance)
	write.Post("/appearance", o.setAppearance)
	return nil
}

// ops binds the service to the typed account ops. A TypedHandler is
// func(context.Context, *In) (*Out, error) — no parameter for the service — so it
// arrives as a RECEIVER and every op is a method value (o.mintKey), which is also
// the only bound form cmd/zipdoc can lift prose from.
type ops struct{ s *cloud.Service[state] }

// act says what an operation DOES, and that is what decides the controls it must
// pass. It cannot be read off the HTTP method: over MCP, the call plane and the
// graph every operation arrives as one POST, so the method describes the
// TRANSPORT and says nothing about the operation.
type act int

const (
	reads   act = iota // changes nothing
	changes            // changes something: the anti-CSRF control
	rotates            // changes the caller's LIVE CREDENTIAL: that control, and the frequency cap
)

// scoped and unscoped say whether an operation needs the caller's ORG resolved.
// The key ops must act inside one; onboarding, appearance, embed and the token
// mint also serve a first-run user who has none yet.
const (
	scoped   = true
	unscoped = false
)

// requestCaller settles everything an account op needs before it acts: the
// controls its act calls for, then the signed-in caller it acts AS. Every op
// opens with it and none carries a copy, so a write cannot be written here
// without answering what it does.
//
// Account's entire surface is the signed-in caller's OWN account, and resolving
// them needs more of the validated principal than the tenant: the user id
// (X-User-Id), the IAM username (X-User-Name) and validated-ness itself, none of
// which principal.OrgFrom carries. So this package reaches for the REQUEST, in
// this ONE function, and every op asks it rather than reading headers of its own.
//
// THE CONTROLS ARE HERE, and not only on the route, because a route is one of the
// seams that reach an operation and not the only one. zip records the route's
// handler and the op as two fields of one registry entry and wraps only the
// handler, while MCP, the call plane (POST /.well-known/zip/op/<name>, which takes
// a body a page can send as a Blob under a CORS-simple content type, and takes NO
// body at all just as happily), the graph and the CLI call the op DIRECTLY. The
// depth-0 identity middleware still authenticates the caller there, so without
// this a signed-in tab's ambient cookie mints and revokes that person's sk- —
// session-equivalent, per the key note above — from any page the CORS policy
// admits. The route keeps the same control for the untyped avatar write, which has
// no preamble to put it in: one rule, two call sites, never two rules.
//
// The anti-CSRF control runs BEFORE the frequency cap deliberately. Both refuse a
// forged cross-origin write, but the cap keys on the VALIDATED principal — the
// victim's — so charging the forgery to their bucket would let a page lock its
// visitor out of their own key management. Refused first, it costs the victim
// nothing.
//
// It fails closed off the HTTP path (the CLI's LocalInvoke, where there is no
// request): no request, no attested caller, no account — the same refusal an
// anonymous HTTP caller gets, with no second gate to keep in sync.
//
// The *zip.Ctx comes back with the caller because two ops need the request for
// more than identity: issueCSRFToken pins Cache-Control on its response, and
// embedStatus reads the SuperAdmin claim (X-User-IsAdmin) that lives in a header.
func (o ops) requestCaller(ctx context.Context, does act, requireOwner bool, what string) (caller, *zip.Ctx, error) {
	no := func(err error) (caller, *zip.Ctx, error) { return caller{}, nil, err }
	c, ok := cloud.Request(ctx)
	if !ok {
		return no(zip.ErrForbidden("sign in to " + what))
	}
	if does != reads {
		if err := cloud.Intended(c); err != nil {
			return no(err)
		}
	}
	if does == rotates {
		if err := capped(o.s.State.writesRL, c); err != nil {
			return no(err)
		}
	}
	cr, ok := resolveCaller(c, requireOwner)
	if !ok {
		return no(zip.ErrForbidden("sign in to " + what))
	}
	return cr, c, nil
}

// ── caller resolution (the tenancy boundary) ─────────────────────────────────

// caller is the signed-in user resolved from the VALIDATED identity headers. id is
// the `<owner>/<name>` composite IAM's privileged ops parse (GetOwnerAndNameFromId
// requires it — a bare token count of 1 throws "wrong token count"); owner is the
// org (X-Org-Id).
type caller struct {
	id       string // <owner>/<name> (or the bare user id when owner-less)
	owner    string // validated org (may be "" for a zero-org, first-run user)
	name     string // == X-User-Id: the stable user id (a UUID on the direct path)
	username string // IAM username (X-User-Name); the `name` half IAM's user-key ops parse
	home     string // the user's own org (X-User-Owner); differs from owner in an org joined by membership
}

// keyID is the `<owner>/<username>` composite IAM's user-key ops (mint/get/revoke
// user AccessKey) parse via GetOwnerAndNameFromId. It uses the IAM USERNAME, not
// name (== X-User-Id): on the in-binary direct-Bearer path X-User-Id is the UUID
// subject and `<owner>/<uuid>` fails IAM's user lookup ("password or code is
// incorrect"). On the gateway path username==name so keyID()==id — no change.
// Owner-less (first-run) callers can't own a key, so this is only reached with a
// validated owner; it falls back to id defensively.
func (cr caller) keyID() string {
	if cr.owner != "" && cr.username != "" {
		return cr.owner + "/" + cr.username
	}
	return cr.id
}

// member reports that the caller is acting in an org they belong to by membership
// rather than their own.
func (cr caller) member() bool {
	return cr.home != "" && cr.owner != "" && cr.home != cr.owner && cr.username != ""
}

// holder is the user a key this caller mints speaks for: their own row,
// <home>/<username>. At home that is keyID(); in an org joined by membership the
// key is filed under that org and still names them, and IAM resolves it only while
// the membership stands.
func (cr caller) holder() string {
	if cr.member() {
		return cr.home + "/" + cr.username
	}
	return cr.keyID()
}

// resolveCaller derives the caller from the validated identity, or (zero,false)
// when there is no validated principal. requireOwner=true refuses a user with no
// org yet (used by the key ops, which must act scoped); onboarding passes
// requireOwner=false so a first-run zero-org user can create their first org. The id
// is ALWAYS derived from the validated claims, never a request value.
func resolveCaller(c *zip.Ctx, requireOwner bool) (caller, bool) {
	if !principal.Validated(c) {
		return caller{}, false // no gateway-minted, IAM-verified principal — refuse
	}
	name := strings.TrimSpace(c.User())
	if name == "" {
		return caller{}, false
	}
	owner := strings.TrimSpace(c.Org())
	if requireOwner && owner == "" {
		return caller{}, false
	}
	// IAM parses `<owner>/<name>`; prefer it, fall back to the bare id for an
	// owner-less (first-run) user. Same id semantics as identity.ts.
	id := name
	if owner != "" {
		id = owner + "/" + name
	}
	// username is the IAM USERNAME, kept DISTINCT from name (== X-User-Id) so the
	// billing/topup subjects (which key on name) are byte-identical to today — this
	// value narrows the blast radius to the IAM user-key ops alone (keyID()). It
	// prefers X-User-Name (stamped from the validated `name` claim by
	// SanitizeIdentity), because on the in-binary direct-Bearer path X-User-Id is the
	// UUID subject and <owner>/<uuid> fails IAM's mint-user-keys/get-user lookup.
	// Falls back to name for the gateway path (which mints X-User-Id==username). Both
	// inputs are gateway/SanitizeIdentity-minted from a verified principal.
	username := strings.TrimSpace(c.Header("X-User-Name"))
	if username == "" {
		username = name
	}
	home := strings.TrimSpace(c.Header("X-User-Owner"))
	// A subject that names its row names the person, wherever they act. A member's
	// key arrives as "<home>/<name>" with X-User-Owner set to the org it acts in, so
	// the username alone would name that org's same-named user.
	if h, u, ok := strings.Cut(name, "/"); ok && h != "" && u != "" {
		home, username = h, u
	}
	return caller{id: id, owner: owner, name: name, username: username, home: home}, true
}

// ── onboard (create the caller's org) ────────────────────────────────────────

type onboardReq struct {
	// Name is the organization's display name. Ignored when personal is true, which
	// derives the name from the caller's own username instead.
	Name string `json:"name"`
	// Personal marks the organization as the caller's own space: the name is
	// derived from their username and the slug auto-suffixes to stay unique.
	Personal bool `json:"personal"`
}

type onboardResp struct {
	// Org is the created organization's slug, which is what X-Org-Id carries.
	Org string `json:"org"`
	// DisplayName is the organization's human name.
	DisplayName string `json:"displayName"`
	// AccessKey is the publishable half of the organization's default API key,
	// held by its creator. Absent when the key could not be minted; the creator
	// then mints one from the keys page.
	AccessKey string `json:"accessKey,omitempty"`
	// AccessSecret is that credential's confidential half, returned ONCE — on the
	// response that mints it and never again. IAM keeps only its argon2id digest
	// and blanks the plaintext, so this is the single moment it exists in a form
	// its owner can read.
	AccessSecret string `json:"accessSecret,omitempty"`
}

// Onboard creates an organization and makes the caller its owner.
//
// The caller's own account is not touched: who they are, the org their account
// lives in and how they sign in are the same before and after. The new org is a
// separate organization they belong to by membership, carried in the signed
// `orgs` claim of their next token, and reached by selecting it (X-Org-Id, the
// org switcher). Teammates join it the same way. The org starts at a zero
// balance, and its default API key is minted for the creator and returned once.
//
// Moving the creator into the org instead re-keyed their identity to
// "<org>/<name>", and sign-in, which resolves an address in the signup org,
// then found nobody: the person was offered a second account for their own
// address.
//
// Example: {"name": "Acme"}
func (o ops) onboard(ctx context.Context, in *onboardReq) (*onboardResp, error) {
	// unscoped: a caller may found an org from anywhere they act. rotates: it mints
	// a credential the caller will hold, so it takes the frequency cap too.
	cr, c, err := o.requestCaller(ctx, rotates, unscoped, "create an organization")
	if err != nil {
		return nil, err
	}
	founder, err := founderOf(c)
	if err != nil {
		return nil, err
	}
	s := o.s
	if !s.State.iam.configured() {
		return nil, notConfigured("organization creation")
	}
	body := *in
	rctx := c.Context()

	baseSlug, displayName, herr := resolveOnboardName(s, body, cr)
	if herr != nil {
		return nil, herr
	}
	// Resolve a unique slug. Personal orgs auto-suffix to stay unique; an explicit
	// name that's taken is an honest conflict the user resolves by renaming.
	slug, herr := uniqueSlug(s, rctx, baseSlug, body.Personal)
	if herr != nil {
		return nil, herr
	}

	// The switch admits only an org named in the caller's signed `orgs`, which IAM
	// builds from their home org plus their membership rows. So the creator is
	// recorded as the org's owner, under the holder of the verified access token
	// (founderOf), or there is no org: foundOrg removes it when the membership
	// cannot be written. Nothing in the request body names the member or reaches
	// the org row beyond its name and the personal flag.
	org := buildOrg(s, rctx, slug, displayName, body.Personal, cr.owner)
	if err := s.State.iam.createOrganization(rctx, org); err != nil {
		// IAM decides a race for the name: the loser is told it is taken.
		if errors.Is(err, iam.ErrConflict) {
			return nil, zip.Errorf(http.StatusConflict, "“%s” is taken; choose a different name", slug)
		}
		return nil, zip.Errorf(http.StatusBadGateway, "could not create the organization: %v", err)
	}
	if err := foundOrg(rctx, s.State.iam, s.Log.Error, founder, slug); err != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "could not make you the organization's owner: %v", err)
	}
	giveOrgAnAgent(rctx, s.State.iam, s.Log.Error, slug)
	out := onboardResp{Org: slug, DisplayName: displayName}
	// The org's default key, held by its creator. The org stands whether or not it
	// mints: a creator answered with an error here would retry into a name that is
	// now theirs and be told it is taken.
	row, secret, err := s.State.iam.createKey(rctx, newKey{
		Owner: slug, Name: defaultKeyName, Holder: founder, Label: defaultKeyLabel, Type: keyTypeSecret,
	})
	if err != nil {
		s.Log.Error("org created without its default key", "org", slug, "err", err)
	} else {
		out.AccessKey, out.AccessSecret = row.AccessKey, secret
	}
	go announce("org", slug, slug, "Org created", displayName)
	return &out, nil
}

// The default key an org is created with: the row "<org>/default", labelled
// "Default", the same address the key always had.
const (
	defaultKeyName  = "default"
	defaultKeyLabel = "Default"
)

// founderOf is who an org created on this request belongs to: the holder of the
// access token the identity boundary verified, "<home>/<username>". Nothing else
// can found an org. An application, an API key, an id_token and a token acting for
// someone else hold nothing (holderOf in the boundary), and a request no boundary
// attested has nobody, so each is refused before anything is written. So is a
// SuperAdmin: a platform operator holds no role in a customer org, and IAM refuses
// to record one there.
func founderOf(c *zip.Ctx) (string, error) {
	p, ok := principal.Minted(c)
	if !ok || p.Kind != principal.Person || p.Holder == "" {
		return "", zip.ErrForbidden("only a person signed in with an access token can create an organization")
	}
	if principal.Super(c) {
		return "", zip.ErrForbidden("a SuperAdmin cannot own a customer organization; create it in IAM for the customer")
	}
	return p.Holder, nil
}

// resolveOnboardName derives the base slug + display name from the request, or a
// mapped HTTP error. Personal orgs derive from the IAM username (cr.username), never
// from cr.name: on the direct-Bearer path that is the UUID subject, and a personal
// org named after it reads as a random string. A named org validates through the
// shared policy (onboarding.go).
func resolveOnboardName(s *cloud.Service[state], body onboardReq, cr caller) (baseSlug, displayName string, err error) {
	if body.Personal {
		baseSlug = personalOrgSlug(cr.username)
		if len(baseSlug) < minOrgSlug || authz.IsHeldOrg(baseSlug) {
			baseSlug = "org-" + cmp.Or(slugifyOrg(cr.username), "space")
		}
		return baseSlug, humanize(cr.username), nil
	}
	v := validateOrgName(body.Name)
	if !v.ok {
		return "", "", zip.ErrBadRequest(v.error)
	}
	return v.slug, v.display, nil
}

// uniqueSlug returns a free slug at/after base. A named org that's taken is a 409;
// a personal org auto-suffixes (base, base-2, …) up to a small bound.
func uniqueSlug(s *cloud.Service[state], ctx context.Context, base string, personal bool) (string, error) {
	existing, err := s.State.iam.getOrganization(ctx, base)
	if err != nil {
		return "", zip.Errorf(http.StatusBadGateway, "could not check organization availability: %v", err)
	}
	if existing == nil {
		return base, nil
	}
	if !personal {
		return "", zip.Errorf(http.StatusConflict, "“%s” is taken; choose a different name", base)
	}
	free, err := freeSlug(s, ctx, base)
	if err != nil {
		return "", err
	}
	if free == "" {
		return "", zip.Errorf(http.StatusConflict, "could not find an available name")
	}
	return free, nil
}

// freeSlug finds the first free slug at/after base (base, base-2, … base-20), or ""
// if all are taken. Mirrors identity.ts's freeSlug bound of 20.
func freeSlug(s *cloud.Service[state], ctx context.Context, base string) (string, error) {
	for i := 2; i <= 20; i++ {
		trimmed := base
		if len(trimmed) > maxOrgSlug-3 {
			trimmed = trimmed[:maxOrgSlug-3]
		}
		candidate := strings.Trim(fmt.Sprintf("%s-%d", strings.TrimRight(trimmed, "-"), i), "-")
		if len(candidate) < minOrgSlug || authz.IsHeldOrg(candidate) {
			continue
		}
		existing, err := s.State.iam.getOrganization(ctx, candidate)
		if err != nil {
			return "", zip.Errorf(http.StatusBadGateway, "could not check organization availability: %v", err)
		}
		if existing == nil {
			return candidate, nil
		}
	}
	return "", nil
}

// buildOrg assembles the new customer org owned by the `admin` org, cloning
// password/locale settings from the caller's current org (best-effort; a nil source
// just yields a minimal org IAM completes with its defaults) and clearing all
// instance-specific material. Mirrors identity.ts's createOrganization body.
func buildOrg(s *cloud.Service[state], ctx context.Context, slug, displayName string, personal bool, sourceOwner string) iamOrg {
	org := iamOrg{Owner: adminOrg, Name: slug, DisplayName: displayName, IsPersonal: personal}
	if sourceOwner == "" {
		return org
	}
	src, err := s.State.iam.getOrganization(ctx, sourceOwner)
	if err != nil || src == nil {
		return org // clone is best-effort; IAM applies its org defaults otherwise
	}
	org.PasswordType = src.PasswordType
	org.PasswordSalt = src.PasswordSalt
	org.PasswordObfuscatorType = src.PasswordObfuscatorType
	org.PasswordObfuscatorKey = src.PasswordObfuscatorKey
	org.PasswordOptions = src.PasswordOptions
	org.CountryCodes = src.CountryCodes
	org.Languages = src.Languages
	org.DefaultAvatar = src.DefaultAvatar
	return org
}

// ── shared helpers ────────────────────────────────────────────────────────────

// notConfigured is the honest 501 for a surface whose confidential client is
// unwired — the deployment simply lacks the `hanzo-console` credential.
func notConfigured(surface string) error {
	return zip.Errorf(http.StatusNotImplemented, "%s is not configured on this deployment (IAM client unset)", surface)
}

// humanize title-cases the base of a username for a personal org's display name
// (dave.smith@x.com → "Dave Smith"). Mirrors identity/onboard humanize().
func humanize(username string) string {
	base := username
	// Split on '@' anywhere (mirrors identity.ts humanize's `includes('@')`), so a
	// bare "@" collapses to "" → "Personal". (personalOrgSlug intentionally uses
	// `> 0` instead, matching its own TS source.)
	if at := strings.IndexByte(base, '@'); at >= 0 {
		base = base[:at]
	}
	base = strings.TrimSpace(strings.Map(func(r rune) rune {
		if r == '.' || r == '_' || r == '-' {
			return ' '
		}
		return r
	}, base))
	if base == "" {
		return "Personal"
	}
	parts := strings.Fields(base)
	for i, p := range parts {
		parts[i] = strings.ToUpper(p[:1]) + p[1:]
	}
	return strings.Join(parts, " ")
}

func basicToken(id, secret string) string {
	return base64.StdEncoding.EncodeToString([]byte(id + ":" + secret))
}

// announce puts a new customer or org on the SuperAdmin feed. Best effort: the
// organization exists whatever the bus answers.
func announce(kind, key, org, title, detail string) {
	_ = bus.Announce(bus.Pulse{Kind: kind, Key: key, Org: org, Title: title, Detail: detail})
}
