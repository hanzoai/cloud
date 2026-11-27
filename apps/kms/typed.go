package kms

// typed.go is the KMS broker's TYPED plane — the ops that carry In/Out types,
// and so the only KMS routes that reach a schema, an MCP tool, a CLI command and
// a generated SDK method. Before this file the whole subsystem published seven
// operationIds and prose and NOTHING else: no caller could learn from the
// document what a secret listing contains, that a write must name its
// environment, or what the login broker hands back.
//
// ALL SEVEN ARE TYPED. The last two to arrive are the ones that address a secret
// by its sub-path, and they waited on zip being able to say three things about a
// greedy segment: that the registry and the router spell its ADDRESS the same
// (else the fold produces no document at all), that the document DECLARES it as
// a path parameter, and that it is bound under the key the router actually uses.
// zip v1.36.8 says all three. typed_wire_test.go re-measures them rather than
// trusting them, because each is a claim about a dependency.
//
// THE WIRE DID NOT MOVE. Each model spells the map its handler assembled, in
// alphabetical json-tag order, because encoding/json sorts a map's keys — so the
// typed answer is byte-identical to the map it replaces, not merely equal as
// JSON. typed_wire_test.go pins that case by case.
//
// TWO QUESTIONS ON ADMISSION, each asked in ONE function. admit() asks whether a
// caller may use this org's KMS at all — authority, then a storage-safe org,
// then a store that actually holds a master key — and every op asks it. open()
// asks whether this caller may touch this one VALUE, and only the three value
// ops ask it: SuperAdmin holding an access token (audited), or a grant declared
// in git (policy.go). Listing asks a third, in lists(): who may learn what an
// org holds at all, which membership alone does not answer.
//
// THE ONE RULE ABOUT PROSE. This is the credential broker, so a description says
// what an operation DOES and where its answer is scoped, and never implies that
// secret material turns up anywhere but the one response body that exists to
// carry it.

// The package's ONE zipdoc directive. It covers BOTH typed planes — the REST
// ops here and the internal plane's four in secret_rpc.go — because the
// generator walks the whole package, not the file it was triggered from. A
// second directive elsewhere would link and run the generator twice for one
// identical result.
//
//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"

	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/audit"
	"github.com/hanzoai/cloud/sdk/geo"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/hanzoai/authz"
	"github.com/zap-proto/zip"
)

// ops binds the service to the typed KMS ops. A TypedHandler takes no service
// parameter, so the service arrives as a RECEIVER and every op is a method value
// — also the only bound form cmd/zipdoc can lift prose from.
type ops struct{ s *cloud.Service[state] }

// ---- admission -------------------------------------------------------------

// admit is the whole admission every secret operation passes, over the three
// facts it turns on and in the order it has always decided them: the authority a
// caller holds, the org key it resolved, and whether this process can open a
// secret at all. Fail-closed at each step, before any record is touched — 403,
// 400, 503.
//
// It lives INSIDE the op rather than around the route, and that is the whole
// reason it is safe for these routes to be typed ops. A typed op is also an MCP
// tool and a call-plane op, and both of those invoke the handler directly — no
// route middleware runs — so a check written as middleware would guard REST and
// publish an ungated alias beside it. This one place is on every way in.
//
// The scope is the OPERATION'S and is passed in, because reading a secret and
// replacing one are different acts and the admission is where that difference
// belongs.
//
// IT REACHES FOR THE REQUEST because ADMIN-NESS is not the org: cloud.Scope.Admits
// turns on platform sudo and org-admin, two facts the identity middleware parks
// in headers, and neither may become an In field — a caller that could name
// itself an admin would be one. The ORG likewise comes from principal.OrgFrom,
// the value cloud.Bridge parked from the validated claim, and NEVER from
// anything the caller sent: a token names one org, and that is the one whose
// secrets it reaches. Cross-org access exists only IN-PROCESS, through cloud's
// own kms.Client, never over any of these transports.
//
// It fails CLOSED where there is no request at all — an in-process CLI invoke —
// because then there is no principal to be a member or an admin of anything.
func (o ops) admit(ctx context.Context, need cloud.Scope) (string, error) {
	c, ok := cloud.Request(ctx)
	if !ok {
		return "", need.Refusal()
	}
	if !need.Admits(cloud.AuthorityOf(c)) {
		return "", need.Refusal()
	}
	// principal.Acting is the ONE refusal: no principal, or a principal with no org
	// scope, and it says which. Nothing re-asks afterwards, because an org that
	// reaches here has already passed the boundary's OrgHasUnsafeRune — which is
	// Sanitize's "" under another name — so OrgPath cannot answer "" for it. The
	// version that dropped Acting's error and re-checked the org gave a 400 to a
	// caller whose real problem was that it had no scope at all.
	org, err := principal.Acting(ctx)
	if err != nil {
		return "", err
	}
	if !o.s.State.kms.Ready() {
		return "", zip.Errorf(http.StatusServiceUnavailable, "%s", ErrMasterKeyMissing.Error())
	}
	return org, nil
}

// need is the authority a value op asks admit for. Under enforcement it is
// membership for both verbs, because the grant decides. Before enforcement a
// write still asks for org admin — the rule the grant replaces — so loading a
// declaration with `enforce: false` changes no verdict until its line is
// deleted.
func need(pol *Policy, v Verb) cloud.Scope {
	if v == Write && !pol.Enforcing() {
		return cloud.Admin
	}
	return cloud.Member
}

// The four verdicts one value operation can reach, as the access log and the
// audit chain spell them.
const (
	verdictGrant     = "grant"
	verdictSuper     = "super"
	verdictDeny      = "deny"
	verdictWouldDeny = "would-deny"
	// verdictAdmin is a listing an admin of the org made. It is recorded for
	// listings alone: org admin opens no value.
	verdictAdmin = "admin"
)

// List is the verb a listing is recorded under. No grant declares it: a
// listing shows a caller the values its grants already open.
const List Verb = "list"

// lists is the admission a listing passes after admit: whether this caller may
// learn what the org holds, and whether it sees every name. A SuperAdmin holding
// an access token (audited, as a value is) sees every name. So does an admin of
// the org holding an access token of their own — not an id_token, a key, or a
// token that acts for someone else — except in an org the platform runs
// (authz.IsPlatformOrg): a brand's store is the platform's, and org admin there is
// a self-service role, not platform privilege. A customer org whose name is held
// because the platform funds it (adnexus, bootnode, osage) is its admins' as any
// customer's is. Any other caller sees only the names its own grants
// open (Policy.Opens), and is refused when none of its grants lies inside the
// subtree it asked about. Membership alone shows nothing, not even a name: a
// sign-up is a member of its brand's org.
//
// It is decided before the store is read, and every verdict is recorded, so the
// trail says who learned what an org holds.
func (o ops) lists(ctx context.Context, pol *Policy, org, sub, env string) (whole bool, holder string, err error) {
	c, _ := cloud.Request(ctx)
	var p principal.Principal
	if c != nil {
		p, _ = principal.Minted(c)
	}
	a := cloud.AuthorityIn(ctx)
	verdict := verdictDeny
	switch {
	case a.Super && p.Holder != "":
		verdict, whole = verdictSuper, true
	case a.OrgAdmin && p.Holder != "" && !authz.IsPlatformOrg(org):
		verdict, whole = verdictAdmin, true
	case pol.Reaches(p.Holder, org, sub, env):
		verdict = verdictGrant
	}
	ref := normalizePath(sub) + "@" + cmp.Or(env, "*")
	if err := o.record(ctx, c, p, org, List, ref, verdict); err != nil {
		return false, "", err
	}
	if verdict == verdictDeny {
		return false, "", zip.ErrForbidden(fmt.Sprintf("kms: listing org %s's secrets takes an admin of the org or a grant there", org))
	}
	return whole, p.Holder, nil
}

// open is the value half of admission: whether THIS caller may touch THIS
// secret. It runs after admit and after the address is validated, and before
// the store is touched, so a refused caller learns nothing about whether the
// secret exists and no value is ever read on its behalf. It answers the store
// path to act on.
//
// Two things open a value. SuperAdmin holding an ACCESS TOKEN — never an
// id_token or an sk- key, which name no holder — and every such act is written
// to the audit chain first; an act that cannot be recorded is refused. Or a
// grant in the declaration covers (holder, org, verb, path, key, env) exactly.
// Membership and org admin open nothing: administering an org is self-service,
// and reading its API keys is not part of that.
//
// The holder is the one the identity boundary minted (principal.Mint), never a
// value the request carried, and the org is the one admit resolved.
//
// A custodied value (custody.go) is decided as though the declaration enforced,
// whatever it says, and for every verb: the app that sealed it reads, replaces
// and removes it on the plane, and over REST it opens — to read, write or delete
// — for SuperAdmin and a declared grant, and is refused to everyone else. In a
// sync subtree the org's own KMS-sync identity also READS it, which is what it
// projects into the org's namespace; it writes nothing there.
func (o ops) open(ctx context.Context, pol *Policy, org string, v Verb, sub, name, env string) (string, error) {
	c, _ := cloud.Request(ctx)
	var p principal.Principal
	if c != nil {
		p, _ = principal.Minted(c)
	}
	granted := pol.Allow(p.Holder, org, v, sub, name, env)
	keeper, held := custodyOf(sub)
	verdict := verdictGrant
	switch {
	case cloud.AuthorityIn(ctx).Super && p.Holder != "":
		verdict = verdictSuper
	case granted:
	case held && keeper.sync && v == Read && p.Holder == cloud.KMSMachineClientID(org):
	case pol.Enforcing() || held || p.Kind != principal.App:
		// While the declaration measures, only an application acting as itself
		// is measured. A sign-up lands in the brand org as a member of it, so a
		// person, or a key a person holds, opens no value a grant does not name.
		verdict = verdictDeny
	default:
		verdict = verdictWouldDeny
	}
	ref := fmt.Sprintf("%s@%s", strings.TrimSuffix(normalizePath(sub), "/")+"/"+name, env)
	if err := o.record(ctx, c, p, org, v, ref, verdict); err != nil {
		return "", err
	}
	if verdict == verdictDeny {
		holder := p.Holder
		if holder == "" {
			holder = "a credential that names no holder"
		}
		if held {
			return "", zip.ErrForbidden(fmt.Sprintf("kms: %s may not %s %s in org %s: %s holds it on the org's behalf", holder, v, ref, org, keeper.app))
		}
		return "", zip.ErrForbidden(fmt.Sprintf("kms: %s may not %s %s in org %s", holder, v, ref, org))
	}
	path := OrgPath(org, sub)
	if path == "" {
		return "", zip.ErrBadRequest("org names no secret store")
	}
	return path, nil
}

// record writes one access line for every value operation to the deployment's
// audit trail — the one store /v1/admin/audit reads, which admin holds — and every
// verdict goes there, served reads included.
//
// An access the trail did not take is refused (AU-5): every verdict that opens a
// name or a value — super, admin, grant and would-deny — answers 503 when its
// record did not land, so nothing is learned from a secret store with no record
// of who learned it. A deny is refused regardless, and its failed record is
// logged.
//
// The record names the holder and the CLIENT its token was issued to (azp), so
// a SuperAdmin token a confidential client minted on someone's behalf is
// distinguishable from one they obtained at their own keyboard.
func (o ops) record(ctx context.Context, c *zip.Ctx, p principal.Principal, org string, v Verb, ref, verdict string) error {
	o.s.Log.Info("kms_access", "holder", p.Holder, "org", org, "verb", string(v), "ref", ref, "verdict", verdict, "client", p.Client)
	rec := audit.Access(org, string(v), ref, verdict, p.Holder, p.Client, p.User, cloud.AuthorityIn(ctx).Super)
	if c != nil {
		rec.Method, rec.Path, rec.RequestID, rec.SourceIP = c.Method(), c.Path(), c.RequestID(), geo.IP(c)
	}

	kept := errors.New("no audit trail")
	if chain := o.s.State.audit; chain != nil {
		_, kept = chain.Append(ctx, rec)
	}
	o.s.State.trailed.set(kept)
	if kept == nil {
		return nil
	}
	o.s.Log.Error("kms: audit append failed", "verdict", verdict, "org", org, "ref", ref, "err", kept)
	if verdict == verdictDeny {
		return nil
	}
	return zip.Errorf(http.StatusServiceUnavailable, "kms: access to secrets is audited, and the audit trail did not take this one")
}

// trailed is whether the audit trail took the last record kms handed it, for
// health. Nil answers as never asked.
type trailed struct{ failing atomic.Bool }

func (t *trailed) set(err error) {
	if t != nil {
		t.failing.Store(err != nil)
	}
}

func (t *trailed) failed() bool { return t != nil && t.failing.Load() }

// ---- models ----------------------------------------------------------------

// kmsHealth is the broker's readiness report — its configuration state, and
// nothing about any tenant or any key.
type kmsHealth struct {
	// Audit is the state of the audit trail every access is recorded to: `ok`,
	// `failing` when it did not take the last record, or `absent` when this
	// process has none. Every access is refused in the last two. Absent from the
	// answer when there is no in-process client to audit.
	Audit string `json:"audit,omitempty"`
	// Enforcing is whether a secret value the declaration in force does not
	// grant is refused. False while it says `enforce: false` and while none has
	// loaded; every such value is then logged and audited as would-deny.
	Enforcing bool `json:"enforcing"`
	// Error is the honest reason readiness is false: no in-process KMS client,
	// or no master key. Absent when ready.
	Error string `json:"error,omitempty"`
	// Policy is the sha256 of the grant declaration in force, so a probe can
	// prove which one is live. Absent when none has ever loaded, which measures:
	// the rule each caller had before still decides.
	Policy string `json:"policy,omitempty"`
	// Ready is whether a secret operation would actually succeed right now.
	// These are exactly the states in which the secret operations refuse.
	Ready bool `json:"ready"`
	// Refused is what the MOUNTED declaration reads as — its sha256, or "absent"
	// or "unreadable" — when it is not the one in force: a document cloud refused
	// to parse, or a mount that went away. The declaration in force stays, so a
	// bad edit changes nothing, and this is where it shows.
	Refused string `json:"refused,omitempty"`
	// Service names the subsystem answering, `kms`.
	Service string `json:"service"`
	// Signing reports whether signing keys are configured. Absent when there is
	// no in-process client to ask.
	Signing *bool `json:"signing,omitempty"`
	// Status is `ok` or `degraded`, the one-word form of Ready.
	Status string `json:"status"`
}

// StatusCode says which of the two declared statuses this answer is. A probe
// that is not ready rides a 503 with the SAME body, which is why it is a
// declared status rather than a returned error: the reason is the payload.
func (h *kmsHealth) StatusCode() int {
	if h.Ready {
		return http.StatusOK
	}
	return http.StatusServiceUnavailable
}

// kmsConfig is what the KMS console needs before anyone has signed in.
type kmsConfig struct {
	// APIBase is this subsystem's own prefix, `/v1/kms`.
	APIBase string `json:"apiBase"`
	// Brand is the deployment's brand, so the console renders as the right
	// product.
	Brand string `json:"brand"`
	// Issuer is the OIDC issuer the console authenticates against.
	Issuer string `json:"issuer"`
	// LoginPath is the credential exchange's address.
	LoginPath string `json:"loginPath"`
}

// kmsSecrets is a listing of the org's secrets — their descriptors, never their
// values.
type kmsSecrets struct {
	// Names is the same listing reduced to bare names, which is the shape the
	// KMS operator reads. Both are emitted so either consumer keeps working.
	Names []string `json:"names"`
	// Secrets are the descriptors: name, path, environment and sealing scheme.
	// No value and no ciphertext appears here.
	Secrets []SecretMeta `json:"secrets"`
	// Total is how many descriptors this listing carries.
	Total int `json:"total"`
}

// kmsStored is the receipt a write answers with. It confirms the coordinate and
// does not echo the value.
type kmsStored struct {
	// Env is the environment the secret was written under.
	Env string `json:"env"`
	// Name is the secret's name.
	Name string `json:"name"`
	// Stored is true; a write confirms by not failing.
	Stored bool `json:"stored"`
}

// kmsSecret is the opened value one secret read answers with.
type kmsSecret struct {
	// Env is the environment the value was resolved under.
	Env string `json:"env"`
	// Name is the secret's name.
	Name string `json:"name"`
	// Value is the opened plaintext. This body is the ONLY place it appears.
	Value string `json:"value"`
}

// kmsRemoved is the receipt one secret deletion answers with.
type kmsRemoved struct {
	// Deleted is true; a delete confirms by not failing.
	Deleted bool `json:"deleted"`
	// Env is the environment the secret was removed from.
	Env string `json:"env"`
	// Name is the secret's name.
	Name string `json:"name"`
}

// kmsList narrows a secret listing. Both spellings of each parameter are
// accepted because two callers already use two: this plane's own clients say
// `env` and `path`, the KMS operator says `environment` and `secretPath`.
//
// Every field carries a json name AS WELL AS its url name, and the json half is
// what carries the PROSE. zipdoc files a field's description under
// `<Type>.<json name>` and skips a field whose json name is `-`
// (zipdoc/extract.go:670,678), while zip looks a published parameter's
// description up under `<In>.<url name>` (zip/openapi.go:163) — so under
// `json:"-"` the four query parameters this op publishes reached openapi.yaml,
// every generated SDK and the MCP inputSchema saying nothing about themselves,
// with the prose written here and dropped in between.
//
// It cannot widen the REST wire. This is a GET, and the handler reads no body
// for a method hasBody says carries none (zip/typed.go:517), so there is no
// body for a json name to bind from and no requestBody is published
// (hasRequestBody, zip/openapi.go:405). What it does reach is the other two
// projections: MCP and the CLI pass their arguments AS the body with no query
// (zip/typed.go:485-489), so an agent calling this op could name no filter at
// all and every argument it sent was silently discarded.
type kmsList struct {
	// Env selects the environment, which is part of a secret's storage key.
	// OMITTED means EVERY environment — this is the enumeration surface, so it
	// must be able to answer "what is in here" without being told where to look.
	Env string `json:"env" url:"env"`
	// Environment is the KMS operator's spelling of Env, accepted so one caller
	// need not learn the other's vocabulary. Env wins when both are sent.
	Environment string `json:"environment" url:"environment"`
	// Path narrows the listing to one subtree beneath the caller's org root, as
	// a `/`-separated path such as `/ci`. OMITTED means the whole org.
	Path string `json:"path" url:"path"`
	// SecretPath is the KMS operator's spelling of Path. Path wins when both are
	// sent.
	SecretPath string `json:"secretPath" url:"secretPath"`
}

// kmsPut is one secret to seal and store. Every field carries url:"-" because
// the untyped handler read the body and nothing else, and a query string that
// could supply `name` or `value` would let a URL — which is logged in more
// places than a body is — carry secret material.
type kmsPut struct {
	// Env is the environment to write under. REQUIRED, with no default: it is
	// part of the storage key, so a silently defaulted write lands in a bucket
	// the readers that resolve project, environment and path never look in, and
	// the stale value keeps being served.
	Env string `json:"env" url:"-"`
	// Name is the secret's name. Required.
	Name string `json:"name" url:"-"`
	// Path is an optional subpath beneath the org root, e.g. "/ci". It never
	// names an org: the root is already `/orgs/<org>`, taken from the
	// credential, so a path that starts with `/orgs/` is refused rather than
	// stored at `/orgs/<org>/orgs/…`.
	Path string `json:"path" url:"-"`
	// Value is the secret itself. It is sealed under a fresh per-secret data key
	// before storage, so plaintext never reaches disk, and it is never echoed
	// back, logged, or carried in an error.
	Value string `json:"value" url:"-"`
}

// kmsRef addresses ONE secret. It is the In of both value operations, which take
// the same address and differ only in what they do with it.
//
// Secret is tagged `url:"+1"`, fiber's own key for the required greedy segment
// this route matches on, so zip's binder fills it from the matched tail. That
// tag is what makes the URL the addressing authority STRUCTURALLY rather than by
// convention: the binder fills from the body, then the query, then the path, so
// a caller that also sends `?secret=`, `?+1=` or a body naming another secret is
// overwritten by the segment the router actually matched. It is spelled `secret`
// on the wire, which is the name an MCP or call-plane caller sends, and is where
// this field's prose is filed — under `json:"-"` the tool schema carries no field
// for the address at all and such a call acts on an empty one.
type kmsRef struct {
	// Env selects the environment to resolve the secret in. It is part of the
	// storage key, so one name in two environments is two secrets. OMITTED means
	// the `default` environment — where a WRITE refuses to default, because a
	// misplaced write strands a value no reader looks for, a read or a delete
	// aimed at the wrong environment simply answers 404 and the caller learns.
	Env string `json:"env" url:"env"`
	// Secret is the coordinate beneath the caller's own org root: an optional
	// `/`-separated subpath and then the name, such as `ci/deploy/token`. Over
	// HTTP it is the trailing path itself, and the trailing path WINS over any
	// other spelling sent with it. There is no org in it — the tenant comes from
	// the validated claim — so another tenant's secret is not merely refused, it
	// is unnameable. OMITTED is refused with a 400: there is no secret named
	// "everything", and a blank address must not read as one.
	Secret string `json:"secret" url:"+1"`
}

// ---- ops -------------------------------------------------------------------

// health reports whether this broker can actually serve secrets.
//
// A real readiness probe, not a liveness stub: 200 only when the store is open
// AND a master key is configured, with `signing` reporting whether signing keys
// are set up too. Anything less answers 503 with `ready:false` and the reason —
// no in-process store, or no master key — which are exactly the two states in
// which the secret operations refuse.
//
// Not token-gated, because the platform must be able to probe it without a
// credential. It reports the broker's configuration state only; no secret, no
// key material and no tenant name appears in it.
func (o ops) health(_ context.Context, _ *cloud.Unit) (*kmsHealth, error) {
	out := &kmsHealth{Service: "kms", Status: "ok"}
	if o.s.State.kms == nil {
		out.Status, out.Ready = "degraded", false
		out.Error = "no in-process KMS client (secrets served out-of-process or disabled)"
		return out, nil
	}
	out.Policy, out.Enforcing, out.Refused = o.s.State.grants.status()
	signing := o.s.State.kms.SigningConfigured()
	out.Signing = &signing
	switch {
	case o.s.State.audit == nil:
		out.Audit = "absent"
	case o.s.State.trailed.failed():
		out.Audit = "failing"
	default:
		out.Audit = "ok"
	}
	if !o.s.State.kms.Ready() {
		out.Status, out.Ready = "degraded", false
		out.Error = ErrMasterKeyMissing.Error()
		return out, nil
	}
	if out.Audit != "ok" {
		out.Status, out.Ready = "degraded", false
		out.Error = "the audit trail is " + out.Audit + ", and every access to a secret is refused until it takes one"
		return out, nil
	}
	out.Ready = true
	return out, nil
}

// config returns the runtime configuration for the KMS console.
//
// What the console needs before anyone has signed in: the brand, the OIDC issuer
// it authenticates against, the API base for this subsystem and the path of the
// login exchange.
//
// Public on purpose, and it holds nothing sensitive — it is deliberately kept
// under this subsystem's own namespace rather than under an admin prefix, so a
// gateway that admin-gates the admin routes cannot break the console's
// legitimate pre-login fetch.
func (o ops) config(_ context.Context, _ *cloud.Unit) (*kmsConfig, error) {
	return &kmsConfig{
		APIBase:   prefix,
		Brand:     o.s.State.brand,
		Issuer:    o.s.State.issuer,
		LoginPath: prefix + "/auth/login",
	}, nil
}

// listSecrets lists the secrets your org holds, without their values.
//
// Returns the METADATA of the caller's own secrets: each one's name, path,
// environment and sealing scheme. No value and no ciphertext is included — this
// operation exists to enumerate what is held, and reading a value is a separate,
// per-secret call.
//
// Scoped to the caller's own org and nothing else, structurally: there is no org
// in the path, the store root is derived from the validated org claim, and a
// caller therefore has no way to name another tenant's namespace. `path` narrows
// to a subpath and `env` selects the environment; both are also accepted under
// the operator's spellings, `secretPath` and `environment`. An omitted `env`
// means every environment and an omitted `path` means the whole org, because a
// default here reported a populated store as empty.
//
// Who sees what: a SuperAdmin holding an access token (audited) sees every name,
// and so does an admin of the org holding their own access token, outside the
// brand and platform orgs, whose stores are the platform's. Any other caller sees
// only the names a declared grant lets it read or write, and is refused with a 403
// when none of its grants lies inside the subtree asked about. Membership alone
// shows nothing.
//
// Admission is fail-closed and in order: a validated member, an org that is a
// DNS-1123 label, a store holding a master key, well-formed input, then who may
// list — 403, 400, 503, 400 and 403 respectively, all decided before any record
// is touched.
func (o ops) listSecrets(ctx context.Context, in *kmsList) (*kmsSecrets, error) {
	org, err := o.admit(ctx, cloud.Member)
	if err != nil {
		return nil, err
	}
	env := strings.TrimSpace(cmp.Or(in.Env, in.Environment))
	if env != "" && !validEnv(env) {
		return nil, zip.ErrBadRequest("'env' must not contain '/', control characters, or exceed 63 bytes")
	}
	sub := cmp.Or(in.Path, in.SecretPath)
	if !ValidSubpath(sub) {
		return nil, zip.ErrBadRequest("'path' must be '/'-separated non-empty segments without '.', '..', or control characters")
	}
	pol := o.s.State.grants.policy()
	whole, holder, err := o.lists(ctx, pol, org, sub, env)
	if err != nil {
		return nil, err
	}
	// Find, not List: the path is a subtree root here, so listing an org returns
	// the org. List is exact-coordinate and stays that way for the credential
	// broker, which must not have its scope widened by a listing change.
	found, err := o.s.State.kms.Find(OrgPath(org, sub), env)
	if err != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "%v", err)
	}
	root := OrgPath(org)
	metas := make([]SecretMeta, 0, len(found))
	names := make([]string, 0, len(found))
	for _, m := range found {
		if !whole && !pol.Opens(holder, org, strings.TrimPrefix(m.Path, root), m.Name, m.Env) {
			continue
		}
		metas = append(metas, m)
		names = append(names, m.Name)
	}
	return &kmsSecrets{Names: names, Secrets: metas, Total: len(metas)}, nil
}

// putSecret stores or replaces one secret in your org.
//
// Upserts one secret under the caller's own org. The value is sealed before it
// is written — a fresh per-secret data key, itself wrapped by the master key —
// so plaintext never reaches disk. The receipt confirms the name and environment
// that were written and does not echo the value.
//
// `env` is REQUIRED on a write and has no default, which is the rule most easily
// got wrong here: reads and deletes still fall back to the default environment
// for older callers, but a write must not, because the environment is part of
// the storage key. A silently defaulted write lands in a bucket the readers that
// resolve project, environment and path never look in, and the stale value keeps
// being served — so the write fails loudly instead.
//
// `name` is required, `path` is an optional subpath beneath the org root, and
// the org is taken from the validated claim rather than the body. A `path` that
// starts with `/orgs/` is refused with a 400 that names where it would have
// landed: the root is already `/orgs/<org>`, so `/orgs/hanzo` written in org
// admin is stored at `/orgs/admin/orgs/hanzo`, which no reader of org hanzo
// looks in. The org is chosen with the credential (the CLI's `--as`).
//
// Opens only for SuperAdmin holding an access token, audited, or for a holder
// a declared WRITE grant covers at this exact path, key and environment. Org
// admin confers nothing here. Fail-closed admission, in order: a validated
// member, well-formed org, master key present, well-formed input, then the
// grant — 401/403, 400, 503, 400 and 403, all decided before any record is
// touched.
//
// A value this deployment seals on the org's behalf — an MCP server's
// credential, a wallet's signing key, a delivery provider's token — is written
// by the app that holds it, through that app's own operation, and is never
// replaced here for membership or org admin, whatever the grant declaration
// says about enforcement.
//
// Example: {"path": "/ci", "name": "deploy-token", "env": "prod", "value": "s3cr3t"}
func (o ops) putSecret(ctx context.Context, in *kmsPut) (*kmsStored, error) {
	pol := o.s.State.grants.policy()
	org, err := o.admit(ctx, need(pol, Write))
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(in.Name)
	if !validName(name) {
		return nil, zip.ErrBadRequest("'name' is required and must not contain '/', control characters, or exceed 253 bytes")
	}
	if in.Value == "" {
		return nil, zip.ErrBadRequest("'value' is required")
	}
	env := strings.TrimSpace(in.Env)
	if env == "" {
		return nil, zip.ErrBadRequest(`'env' is required — there is no default. A silent default would split this write from the project/env/path record that readers resolve.`)
	}
	if !validEnv(env) {
		return nil, zip.ErrBadRequest("'env' must not contain '/', control characters, or exceed 63 bytes")
	}
	if !ValidSubpath(in.Path) {
		return nil, zip.ErrBadRequest("'path' must be '/'-separated non-empty segments without '.', '..', or control characters")
	}
	if namesAnOrg(in.Path) {
		return nil, zip.ErrBadRequest(fmt.Sprintf("'path' %s is beneath your org's root %s, so it would be stored at %s. "+
			"The org comes from the credential (the CLI's --as <org>): drop the /orgs/<org> prefix.",
			normalizePath(in.Path), OrgPath(org), OrgPath(org, in.Path)))
	}
	path, err := o.open(ctx, pol, org, Write, in.Path, name, env)
	if err != nil {
		return nil, err
	}
	if err := o.s.State.kms.Put(path, name, env, []byte(in.Value)); err != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "%v", err)
	}
	return &kmsStored{Env: env, Name: name, Stored: true}, nil
}

// This subsystem's address, and where one secret sits under it.
//
// `+` is fiber's REQUIRED greedy segment. A secret is named by a SUB-PATH, so
// the address is a TAIL rather than one segment; `*` would also match the empty
// tail and swallow the bare listing path, answering it from the read op. The
// tail's binder key is fiber's own `+1`, which is what kmsRef.Secret carries.
const (
	prefix     = "/v1/kms"
	secretLeaf = "/secrets/+"
)

// getSecret reads one secret's value from your org.
//
// Opens one sealed secret belonging to the caller's own org and returns its
// value, with the name and environment it was resolved under. This is the
// broker's purpose, and the response body is the ONLY place the value appears —
// it is not logged, and it is never carried in an error.
//
// `secret` is the coordinate beneath the caller's org root, subpath and name
// together, and over HTTP it is the trailing path itself. `env` selects the
// environment and falls back to the default when omitted. A secret that is not
// there is a plain 404 that names nothing about the store.
//
// Scoped to the caller's own org and nothing else: there is no org in the
// address, so another tenant's secret is not merely refused, it is unnameable.
// Within that org the value opens only for SuperAdmin holding an access token,
// audited, or for a holder a declared READ grant covers at this exact path, key
// and environment; membership and org admin alone open nothing. Admission is
// fail-closed and in order — a validated member, an org that is a DNS-1123
// label, a store holding a master key, a well-formed address, then the grant —
// 401/403, 400, 503, 400 and 403, all decided before any record is touched, so
// a refused caller learns nothing about whether the secret exists.
//
// A value this deployment seals on the org's behalf — an MCP server's
// credential, a repository's environment secret, a connected account's token —
// is read by the app that sealed it, and never opens here for membership or org
// admin, whatever the grant declaration says about enforcement. A deployed
// app's or a validator's own folder also opens for the org's KMS-sync identity,
// which projects it into the org's namespace.
//
// Example: {"secret": "ci/deploy/token", "env": "prod"}
func (o ops) getSecret(ctx context.Context, in *kmsRef) (*kmsSecret, error) {
	pol := o.s.State.grants.policy()
	org, err := o.admit(ctx, need(pol, Read))
	if err != nil {
		return nil, err
	}
	env := envOr(in.Env)
	if !validEnv(env) {
		return nil, zip.ErrBadRequest("'env' must not contain '/', control characters, or exceed 63 bytes")
	}
	sub, name, ok := split(in.Secret)
	if !ok {
		return nil, zip.ErrBadRequest("secret name is required and must be a clean '/'-separated path")
	}
	path, err := o.open(ctx, pol, org, Read, sub, name, env)
	if err != nil {
		return nil, err
	}
	val, err := o.s.State.kms.Get(path, name, env)
	if err != nil {
		if errors.Is(err, ErrSecretNotFound) {
			return nil, zip.ErrNotFound("secret not found")
		}
		return nil, zip.Errorf(http.StatusBadGateway, "%v", err)
	}
	return &kmsSecret{Env: env, Name: name, Value: string(val)}, nil
}

// deleteSecret removes one secret from your org.
//
// Forgets one secret belonging to the caller's own org and confirms the name and
// environment that were removed. Deleting a secret that is not there is a 404,
// not a silent success, so a caller can tell a real deletion from a typo.
//
// `secret` is the coordinate beneath the caller's org root, subpath and name
// together, and over HTTP it is the trailing path itself. `env` selects the
// environment and falls back to the default when omitted. The org comes from the
// validated claim, never from the request.
//
// A delete is a WRITE to the grant rule, like the put: destroying a secret is as
// consequential as replacing it, and a credential distributed to READ one must
// not be able to remove it. It opens only for SuperAdmin holding an access
// token, audited, or for a holder a declared WRITE grant covers. Fail-closed
// admission, in order: a validated member, well-formed org, master key present,
// well-formed address, then the grant — 401/403, 400, 503, 400 and 403, all
// decided before any record is touched.
//
// A value this deployment seals on the org's behalf is removed by the app that
// holds it, and never here for membership or org admin, whatever the grant
// declaration says about enforcement: deleting a wallet's signing key or an MCP
// server's credential destroys what the app acts with.
//
// Example: {"secret": "ci/deploy/token", "env": "prod"}
func (o ops) deleteSecret(ctx context.Context, in *kmsRef) (*kmsRemoved, error) {
	pol := o.s.State.grants.policy()
	org, err := o.admit(ctx, need(pol, Write))
	if err != nil {
		return nil, err
	}
	env := envOr(in.Env)
	if !validEnv(env) {
		return nil, zip.ErrBadRequest("'env' must not contain '/', control characters, or exceed 63 bytes")
	}
	sub, name, ok := split(in.Secret)
	if !ok {
		return nil, zip.ErrBadRequest("secret name is required and must be a clean '/'-separated path")
	}
	path, err := o.open(ctx, pol, org, Write, sub, name, env)
	if err != nil {
		return nil, err
	}
	if err := o.s.State.kms.Delete(path, name, env); err != nil {
		if errors.Is(err, ErrSecretNotFound) {
			return nil, zip.ErrNotFound("secret not found")
		}
		return nil, zip.Errorf(http.StatusBadGateway, "%v", err)
	}
	return &kmsRemoved{Deleted: true, Env: env, Name: name}, nil
}

// login exchanges a machine credential for an IAM bearer token.
//
// Takes a tenant's machine credential — a client id and client secret — and
// returns an owner-scoped IAM access token with its lifetime, which is the
// bearer the caller then carries on the org-scoped secret operations.
//
// It is deliberately public and unauthenticated, because it IS the credential
// exchange and runs before any principal exists. That makes it the one route in
// this subsystem rate-limited PER SOURCE IP, keyed on the real TCP peer rather
// than on any caller-supplied header, and body-capped in the same place.
//
// The submitted secret is never logged and never echoed, and failures collapse
// to one clean status with no upstream detail: 401 when the credential does not
// authenticate, 502 when the identity provider is unreachable, 503 when no
// issuer is configured. That is on purpose — a richer error would be a validity
// oracle for guessed credentials.
//
// Example: {"clientId": "kms-operator", "clientSecret": "…"}
func (o ops) login(ctx context.Context, in *kmsLogin) (*kmsToken, error) {
	if o.s.State.iamTokenURL == "" {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "kms login unavailable: no IAM issuer configured")
	}
	cid := strings.TrimSpace(in.ClientID)
	csec := strings.TrimSpace(in.ClientSecret)
	if cid == "" || csec == "" {
		return nil, zip.ErrBadRequest("clientId and clientSecret are required")
	}
	if len(cid) > maxCredLen || len(csec) > maxCredLen || hasCtrlByte(cid) || hasCtrlByte(csec) {
		return nil, zip.ErrBadRequest("credentials malformed")
	}
	tok, expiresIn, status := brokerIAMToken(o.s, ctx, cid, csec)
	if status != http.StatusOK {
		// One clean status, no upstream detail. 401 = auth failed; 502 = IAM down.
		return nil, zip.Errorf(status, "kms login failed")
	}
	return &kmsToken{AccessToken: tok, ExpiresIn: expiresIn, TokenType: "Bearer"}, nil
}

// cap refuses an oversized body on arrival, before anything reads it.
//
// It lives beside the rate limiter for the same reason the limiter lives there:
// both are what this PUBLIC, pre-identity route accepts, decided before any
// principal exists. A typed op cannot make this decision — it is handed a
// decoded value, and by then a multi-megabyte body has already been parsed — so
// the cap is a property of the route rather than of the operation, which is what
// it always was.
func capBody(max int) zip.Handler {
	return func(c *zip.Ctx) error {
		if len(c.Body()) > max {
			return zip.ErrBadRequest("login body too large")
		}
		return c.Continue()
	}
}
