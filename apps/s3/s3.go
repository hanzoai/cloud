// Package s3 is object storage: your buckets and the files in them, with
// signed URLs for upload and download.
//
// It serves an org's buckets and objects at /v1/s3 — list, create, delete, and
// presigned upload/download URLs — over the shared SeaweedFS S3 gateway.
//
// It is the DATA plane over that store — the companion to apps/provisioning,
// which is the CONTROL plane (allocate/list/drop the s3 RESOURCE at /v1/s3 and
// /v1/s3/:name).
//
//	GET    /v1/s3/health                              — real probe (503 fail-closed); public
//	GET    /v1/s3/buckets                             — list the caller's buckets;       JWT, org-scoped
//	POST   /v1/s3/buckets            {name}           — create a bucket;                  JWT, org-scoped
//	DELETE /v1/s3/buckets/:bucket                     — delete an EMPTY bucket;           JWT, org-scoped
//	GET    /v1/s3/buckets/:bucket/objects?prefix=&delimiter=/  — list objects (folders);  JWT, org-scoped
//	POST   /v1/s3/buckets/:bucket/objects  {key}      — presigned PUT url (upload);       JWT, org-scoped
//	GET    /v1/s3/buckets/:bucket/objects/*           — presigned GET url (download);     JWT, org-scoped
//	DELETE /v1/s3/buckets/:bucket/objects/*           — delete one object;                JWT, org-scoped
//
// ORG SCOPING — every tenant only ever sees or touches its OWN namespace. A
// bucket's PHYSICAL name is derived server-side from the caller's validated org
// as "o"<orgHash>_<name> (provisioning.PhysicalName — the SAME scheme the control
// plane allocates with, so a provisioned bucket is browsable here and vice-versa).
// The client speaks in FRIENDLY names ("photos"); the server maps friendly↔
// physical and NEVER trusts a client-supplied physical name. List filters to the
// caller's prefix; create/delete/object ops re-derive the physical name from the
// caller's org, so one tenant can never address another's bucket — the isolation
// boundary is by construction, not by a checked flag.
//
// FAIL-CLOSED — absent S3_ADMIN_* credentials the subsystem mounts
// health-only: /v1/s3/health is an honest 503 and every op returns 503. It never
// fabricates a bucket or object list.
//
// ROUTE ORDERING — registered as id "s3" with cloud.HealthOwner, at order 118
// (< provisioning's 120). Two independent concerns: (1) health — this subsystem
// serves its OWN fail-closed /v1/s3/health (Mount); cloud.HealthOwner makes Serve
// skip the generic always-ok /v1/<name>/health so it never shadows the real probe
// with a fake 200 (the same flag clients/kms and clients/paas use). (2) routing —
// Fiber v3 matches routes by an ORDERED scan and takes the first match, so the
// static GET /v1/s3/buckets and GET /v1/s3/health must register BEFORE
// provisioning's GET /v1/s3/:name (order 120) to win — hence order 118.
//
// RESIDUAL RISKS THIS SUBSYSTEM RIDES (documented after adversarial review; not
// fixable inside the subsystem, escalated to the platform):
//   - Single S3 identity: the SeaweedFS gateway uses ONE admin identity
//     (universe infra/k8s/storage/s3.yaml) for the whole binary. So the S3 LAYER
//     enforces no tenant boundary — isolation is 100% this subsystem's
//     org-prefixed naming + admit. The correct hardening is per-request
//     STS/session-policy or per-identity bucket-prefix restriction so the store
//     independently enforces the org boundary (defense in depth). Until then,
//     admit's cloud.Member check + the by-construction naming is the sole
//     boundary — kept minimal and auditable for that reason.
//   - Presign has no rate limit: minting is unthrottled (zip/middleware/ratelimit
//     is unwired in serve.go, platform-wide). The 5-minute TTL bounds a minted
//     capability's post-revocation lifetime; a per-route limiter is the platform
//     follow-up.
package s3

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path"
	"regexp"
	"strings"
	"time"

	s3 "github.com/hanzos3/go"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/account"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/hanzoai/cloud/apps/provisioning"
	"github.com/hanzoai/cloud/apps/s3admin"
	"github.com/hanzoai/namespace"
	"github.com/zap-proto/zip"
)

// presignTTL bounds every presigned upload/download URL. 5 minutes: short enough
// that a minted capability barely outlives a revoked session/role (RED MED —
// presigned URLs have no server-side revocation, so the TTL IS the revocation
// window), long enough for a browser to complete a normal PUT/GET. A caller who
// needs a fresh window simply re-mints (the console does so per action). NOTE:
// presign minting is not yet rate-limited — that is a platform-wide gap
// (zip/middleware/ratelimit exists but is unwired in serve.go); flagged to the
// platform, not fixable inside this subsystem.
const presignTTL = 5 * time.Minute

// maxListKeys caps one object-listing page so a bucket with millions of keys
// cannot exhaust memory or the response. Folder-style navigation only needs one
// level at a time, so this is generous.
const maxListKeys = 1000

// bucketNameRE is the FRIENDLY bucket name a tenant supplies. Same shape as
// provisioning's nameRE (DNS/identifier-safe slug) so the friendly↔physical map
// round-trips: the physical name provisioning.PhysicalName produces from a
// slug-valid name is always a legal S3 bucket name.
var bucketNameRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,38}[a-z0-9])?$`)

// opFeeEnvPrefix is the operator knob for the per-operation object-storage fee.
// The effective fee is cloud.ResourceFeeCents(opFeeEnvPrefix, "op"): the global
// CLOUD_S3_FEE_CENTS override, else the $1.00 default. Set it to 0 to make S3
// data-plane ops free (and therefore un-gated). Object storage has no live-size
// source in this data plane, so it is billed per-OPERATION (the S3 request-price
// model) via the ONE shared cloud.ResourceMeter (product "s3"); GB-month storage
// footprint reuses the SAME meter with a usage-derived amount once a live-size
// source exists — there is no second metering path.
const opFeeEnvPrefix = "CLOUD_S3_FEE_CENTS"

// state is s3's own data; shared deps live in the embedded cloud.Base. It holds
// the shared S3 admin connection and nothing else: the per-org gate+meter is
// Base.Bill, whose commerce product label is the subsystem name, and that name
// is "s3" — the label the ledger has always carried for this plane. There was a
// second ResourceMeter here only because the package was called storage and the
// label was not; one name for the app removes the second meter with it. A
// not-Configured() admin means no credentials are present; the subsystem then
// mounts health/config only and every op fails closed 503. A nil/!Enabled() Bill
// makes Gate allow and Meter a no-op.
type state struct {
	admin s3admin.Admin
}

// Mount wires /v1/s3/* onto app. The unconditional route set, each operation
// carrying its own preamble, makes this a direct construction (cloud.NewBase),
// not cloud.Mount.
func Mount(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("s3.Mount: nil app")
	}
	// The anti-forgery token admit asks for is minted by another process, so this
	// one verifies MACs it did not write: a key it invented itself matches none of
	// them and every ambient-cookie call would 403 for as long as the pod ran.
	// Asked at boot, in one line, rather than discovered on the wire.
	if err := account.Shared(); err != nil {
		return fmt.Errorf("s3.Mount: %w", err)
	}
	s := &cloud.Service[state]{Base: cloud.NewBase(deps, "s3"), State: state{admin: s3admin.New()}}

	// Register the FULL surface unconditionally — even when S3 is unconfigured.
	// admit fails each operation closed with 503 (s.State.admin.Configured() is
	// false), so the s3 subsystem always OWNS its route space. If the routes were
	// mounted only when configured, an unconfigured deployment would leak
	// /v1/s3/buckets and /v1/s3/objects to provisioning's GET /v1/s3/:name (a 404
	// "resource not found") instead of the honest 503 — the file-manager surface
	// must fail closed under its own name, never fall through to a different
	// subsystem's handler.
	//
	// Routes go on the concrete app so zip's typed registrars and cmd/zipdoc can
	// both resolve the prefix; the scoped Router still owns any middleware, which
	// is where the ownership guard applies. Same shape apps/meet and apps/blueprint
	// use.
	zapp := cloud.ZipApp(app)
	g := zapp.Group("/v1/s3")
	o := ops{s: s}

	// Bridge is this subsystem's OWN, on its own group and ahead of every leaf: a
	// typed op is handed a context, and the request its gate reads is what this
	// parks there. Serve installs one app-wide, which is what carries the arms that
	// never touch a route — MCP, the call plane — and no package's test harness runs
	// Serve, so an app that does not install its own resolves no request on its own
	// routes and 403s in tests alone. It gates nothing, so /health, registered on
	// this group below and deliberately ungated, is unaffected.
	g.Use(cloud.Bridge())

	// The probe is NOT gated — liveness has to be probe-able without a token — and
	// it declares BOTH of its statuses, so the answer says which one it is.
	zip.Get(g, "/health", o.health, zip.WithStatus(http.StatusOK, http.StatusServiceUnavailable))

	// Everything else opens with admit and closes with settle, composed onto the
	// HANDLER. Nothing is installed at the prefix, so the probe registered above at
	// the same address stays ungated without a second scope to keep the two apart.
	//
	// THE LAST TWO SHADOW THE LISTING ABOVE THEM. fiber's `*` matches the EMPTY
	// remainder, so GET /v1/s3/buckets/photos/objects is answered by the download
	// rather than by listObjects and reads as 400 "object key is required" — with a
	// query string, with a trailing separator, every spelling. listObjects is
	// reachable only BY NAME, where dispatch is on the operation id and no route is
	// consulted.
	//
	// It is a property of the greedy capture and NOT of this order: the listing is
	// registered first and still loses, measured, on this exact route set. Nor is it
	// a cost of typing these two — the same requests answer the same way with them
	// raw. The narrowing fix is `+`, which is greedy but demands at least one
	// segment, and it is a WIRE change (that address would begin listing) plus a
	// change of fiber's key from "*1" to "+1", so it is a decision rather than a
	// tidy-up. Written down because the registration order reads as though the more
	// specific address wins, and it does not.
	zip.Get(g, "/buckets", paid(s, o.listBuckets))
	zip.Post(g, "/buckets", paid(s, o.createBucket), zip.WithStatus(http.StatusCreated))
	zip.Delete(g, "/buckets/:bucket", paid(s, o.deleteBucket))
	zip.Get(g, "/buckets/:bucket/objects", paid(s, o.listObjects))
	zip.Post(g, "/buckets/:bucket/objects", paid(s, o.presignUpload))
	zip.Get(g, "/buckets/:bucket/objects/*", paid(s, o.presignDownload))
	zip.Delete(g, "/buckets/:bucket/objects/*", paid(s, o.deleteObject))

	if !s.State.admin.Configured() {
		s.Log.Warn("s3 subsystem mounted fail-closed: S3_ADMIN_ACCESS_KEY/SECRET_KEY not set (all ops 503 until provisioned)")
		return nil
	}
	s.Log.Info("s3 subsystem mounted",
		"prefix", "/v1/s3",
		"presign", s.State.admin.PresignConfigured(),
		"brand", deps.Brand,
		"env", deps.Env,
	)
	return nil
}

// fee is what one object-storage operation costs, from operator config. Read per
// call rather than once at mount, so the knob takes effect without a restart.
func fee() int64 { return cloud.ResourceFeeCents(opFeeEnvPrefix, "op") }

// admit is the sentence every data-plane operation opens with, and it answers the
// caller's org so the operation can address storage inside it.
//
// Four refusals in the order they have to be asked. A subsystem that cannot serve
// anyone says so before it says who it serves, so the readiness check comes first.
// Then the tenant boundary — cloud.Member (a validated principal, HIP-0519's one
// predicate set) and an org to act for — because billing an unauthenticated caller
// would read an empty ledger and answer a money question about nobody. Then the
// anti-forgery token, immediately before the money, because that is what it is
// about: this plane spends the caller's balance on a READ, so a page the caller
// never visited must not be able to spend it for them by sending them here with a
// cookie they already hold. Then the balance itself: an unfunded org is 402 and,
// in the fail-closed posture, an unreachable commerce is 503, both with NOTHING
// touched.
//
// THE TOKEN IS ASKED HERE RATHER THAN ON THE ROUTE, and it is free to ask here.
// account's control refuses a caller with no request, and this plane has none to
// serve: every operation resolves its tenant from the request, so a caller without
// one is already refused above. Asking in the preamble therefore costs no CLI, no
// agent and no service caller, and it covers the seams a route cannot — the same
// reason the money moved here. account's gate is a no-op the moment a caller
// PRESENTS a credential (Bearer, gateway, API key), which is every client that
// reaches this surface; only the ambient-cookie path is asked for the echoed token.
//
// The denial travels as an ERROR (cloud.Denied), which is the one refusal channel
// every shape here shares — serve.go installs DenyEnvelope app-wide, so a REST
// caller reads the money wire's own nested {"error":{"code","message"}} bytes, and
// off the HTTP path deniedErr.Unwrap keeps the same status and sentence.
func admit(s *cloud.Service[state], c *zip.Ctx) (string, error) {
	if !s.State.admin.Configured() {
		return "", zip.Errorf(http.StatusServiceUnavailable, "object storage is not configured")
	}
	if !cloud.Member.Admits(cloud.AuthorityOf(c)) {
		return "", cloud.Member.Refusal()
	}
	org, ok := tenant(c)
	if !ok {
		return "", principal.Refused(c)
	}
	if err := account.CSRF(c.Context()); err != nil {
		return "", err
	}
	project, projectValidated := principal.ValidatedProject(c)
	if err := s.Bill.Gate(c.Context(), principal.Ledger(c), project, projectValidated, "op", fee()); err != nil {
		return "", cloud.Denied(err)
	}
	return org, nil
}

// settle debits the caller's ledger for one operation, and runs only after the
// work succeeded — a failed operation is surfaced and not billed, which is the
// edge gate's rule ("do not bill failed work"). The debit is async best-effort, so
// it never blocks the answer; fee==0 or unconfigured billing makes it a no-op.
func settle(s *cloud.Service[state], c *zip.Ctx) {
	s.Bill.Meter(principal.Ledger(c), principal.Project(c), "op", fee(), c.RequestID(), cloud.ClientIP(c))
}

// paid composes admit and settle onto an operation. Every operation on this
// surface but the probe is registered through it, so there is one decision about
// money and tenancy and no second place to keep in step with it.
//
// IT WRAPS THE HANDLER, WHICH IS THE WHOLE POINT. zip records a typed op and its
// route's fiber handler as two fields of one entry and wraps only the second, so a
// gate handed to Group or composed through With runs for REST and for nothing
// else — while MCP, the call plane, the graph and the CLI invoke the op directly
// and the depth-0 identity middleware has already authenticated whoever is
// calling. The handler is the one value every way in dispatches to, so an
// operation that costs money asks for it there.
//
// The org travels DOWN in the context, put there by the same call that took the
// money, which is what makes [orgOf] the only way an operation here learns one: an
// operation registered without paid can name no tenant and so can touch nothing.
func paid[In, Out any](s *cloud.Service[state], core zip.TypedHandler[In, Out]) zip.TypedHandler[In, Out] {
	return func(ctx context.Context, in *In) (*Out, error) {
		c, ok := cloud.Request(ctx)
		if !ok {
			return nil, principal.RefusedFrom(ctx) // no request: no credential, no tenant
		}
		org, err := admit(s, c)
		if err != nil {
			return nil, err
		}
		out, err := core(context.WithValue(ctx, orgKey{}, org), in)
		if err != nil {
			return nil, err
		}
		settle(s, c)
		return out, nil
	}
}

// orgKey names the request-scoped slot the admitted org travels in. Unexported
// zero-size type: unforgeable from another package.
type orgKey struct{}

// orgOf is the caller's org as [admit] resolved it, and the ONLY way a typed
// operation here learns one. It is not a second resolution — it reads the value
// [paid] carried down, so the org an operation addresses storage under is by
// construction the org whose balance was checked and whose ledger is debited.
//
// FAILS CLOSED. An operation reached with no admission — off the HTTP path, or
// registered without paid — has no org and refuses rather than defaulting to one.
func orgOf(ctx context.Context) (string, error) {
	org, _ := ctx.Value(orgKey{}).(string)
	if org == "" {
		return "", principal.RefusedFrom(ctx)
	}
	return org, nil
}

// tenant resolves the caller's org exactly as clients/provisioning does — the
// SAME sanitized slug the control plane keys on, so buckets allocated there and
// operated on here share one org tag.
//
// A VALIDATED PRINCIPAL IS ALREADY ESTABLISHED (RED HIGH): admit asks
// cloud.Member before this, so the forgeable data path is closed before the org
// is read. SanitizeIdentity sets X-User-Id ONLY when it validated a bearer/cookie;
// on the no-principal "Phase-1 data" path it RESTORES the client's raw X-Org-Id
// but leaves X-User-Id empty. A pure data plane that trusted X-Org-Id alone would
// let an in-cluster caller (a co-namespace pod within the cloud-api NetworkPolicy)
// forge `X-Org-Id: victim` with NO bearer and get cross-tenant object CRUD. Every
// legitimate caller reaches this through the console BFF /cloud proxy, which mints
// a user-bound bearer, so the gate refuses ONLY the anonymous-forge path and
// breaks no real client. Object storage is a data plane; it never serves an
// unauthenticated principal.
//
// Empty org falls back to the literal "admin" bucket for a SuperAdmin, and only
// for one: SanitizeIdentity mints X-User-IsAdmin solely for a JWT-verified
// SuperAdmin (HIP-0026), and that fallback reaches the admin bucket, never a real
// tenant's.
//
// NORMALIZATION — this uses namespace.Sanitize (case-folds to a DNS slug),
// NOT KMS's exact-match, ON PURPOSE: the S3 bucket name is derived through
// provisioning's SAME sanitized slug (BucketName), so a bucket provisioned via
// POST /v1/s3 is findable here — exact-match would break that lockstep. A real
// IAM owner claim is already a lowercase DNS label, so the fold is a no-op on
// validated input (and, post the gate, only a validated principal reaches it).
// The divergence from KMS is intentional per-subsystem, not drift.
func tenant(ctx *zip.Ctx) (string, bool) {
	if org := namespace.Sanitize(ctx.Org()); org != "" {
		return org, true
	}
	if principal.IsSuperAdmin(ctx) {
		return "admin", true
	}
	return "", false
}

// ── bucket name mapping (tenant ↔ physical) ─────────────────────────────────

// physicalBucket maps a caller's FRIENDLY bucket name to its real S3 bucket name,
// namespaced to the caller's org. This is the SAME derivation
// provisioning.BucketName uses (bucketName(physicalName(org,name)) — org-hash
// prefixed AND '_'→'-' folded to a DNS-safe S3 name), so a bucket provisioned via
// POST /v1/s3 is browsable here and a bucket created here is a valid S3 name.
func physicalBucket(org, friendly string) string { return provisioning.BucketName(org, friendly) }

// orgPrefix is the S3-bucket-name prefix that ALL of a caller's buckets share
// ("o"<orgHash>-). List filters to it and strips it to recover friendly names.
// Derived through provisioning.BucketPrefix so it matches the real bucket names
// exactly (including the '_'→'-' fold of the org-hash separator).
func orgPrefix(org string) string { return provisioning.BucketPrefix(org) }

// friendlyBucket recovers the friendly name from a physical bucket owned by org,
// or ("",false) when the bucket is NOT in the caller's namespace (so listing
// skips other tenants' buckets — an existence-oracle guard). The recovered name
// is RE-VALIDATED against the friendly-name shape (RED LOW defense-in-depth): a
// bucket that carries the org prefix but a non-conforming name — one that could
// only exist via an out-of-band route (a manual `mc mb`, a future admin tool),
// never through createBucket — is treated as not-owned rather than echoed raw to
// the UI. So listBuckets always returns names a subsequent request can address.
func friendlyBucket(org, physical string) (string, bool) {
	pfx := orgPrefix(org)
	if !strings.HasPrefix(physical, pfx) {
		return "", false
	}
	name := strings.TrimPrefix(physical, pfx)
	if !bucketNameRE.MatchString(name) {
		return "", false
	}
	return name, true
}

// ── buckets ─────────────────────────────────────────────────────────────────

type bucketItem struct {
	Name      string `json:"name"`      // friendly name
	CreatedAt int64  `json:"createdAt"` // unix seconds
}

// ── objects ─────────────────────────────────────────────────────────────────

type objectItem struct {
	Key          string `json:"key"`          // key RELATIVE to the requested prefix
	IsDir        bool   `json:"isDir"`        // true for a folder (common prefix)
	Size         int64  `json:"size"`         // bytes (0 for a folder)
	LastModified int64  `json:"lastModified"` // unix seconds (0 for a folder)
	// ETag is the store's entity tag for the bytes currently at this key, with the
	// quotes the store wraps it in stripped. It is an opaque VERSION and not a
	// checksum to verify against: a single-part upload's tag happens to be the MD5
	// of the content and a multipart upload's is not, and nothing here says which
	// this was. Compare two reads of one key to learn whether the object changed;
	// absent for a folder entry, and for an object the store reports none for.
	ETag string `json:"etag,omitempty"`
}

type presignResponse struct {
	URL    string `json:"url"`    // presigned URL the browser follows directly
	Method string `json:"method"` // "PUT" (upload) or "GET" (download)
	// Key is the object key the URL was signed for, relative to the bucket root
	// and path-cleaned — so it is what the store will actually read or write, which
	// is not always the string the caller sent. The signature covers this one bucket
	// and this one key: a URL minted here reaches nothing else.
	Key    string `json:"key"`
	Expiry int64  `json:"expiresIn"` // seconds until the URL expires
}

// ── validation + helpers ────────────────────────────────────────────────────

// friendlyParam validates a bucket path param against the friendly-name shape.
// A malformed param is rejected 400 before any physical name is derived. The
// name is validated AS-IS (not lowercased): the friendly name in a URL must
// match exactly what listBuckets returned, and bucketNameRE already requires
// lowercase — silently coercing "Photos"→"photos" would let a client address a
// bucket by a name the listing never showed, so an out-of-shape param is a 400.
func friendlyParam(raw string) (string, bool) {
	name := strings.TrimSpace(raw)
	if !bucketNameRE.MatchString(name) {
		return "", false
	}
	return name, true
}

// remainder is the trailing path of an /objects/* address as an object key: the
// greedy capture with a leading separator taken off, so a doubled separator in
// the URL (…/objects//a.txt) addresses the same key one separator does. It runs
// before cleanKey and NOT inside it, because a leading separator reaching cleanKey
// by any other route — an upload naming "/a.txt" in its body — is a caller writing
// an absolute key, which is a 400.
//
// It is applied to the BOUND FIELD rather than read off the request, so a caller
// that addresses the operation by name is normalized by the same rule as one that
// addresses it by URL.
func remainder(raw string) string {
	return strings.TrimPrefix(strings.TrimSpace(raw), "/")
}

// cleanKey normalizes an object key and rejects any traversal or unsafe byte. An
// object key may contain "/" (nested paths) but must not be absolute, empty, a
// directory marker, or escape via "..". It must also contain no control byte
// (\x00–\x1f) or backslash: those never appear in a legitimate key from the
// console and are a smell (a null byte serializes as %00 in the presigned URL
// and could C-string-truncate a downstream consumer; '\' is a Windows-style
// separator a non-Go backend might treat as a path split). RED LOW hardening.
// path.Clean collapses "a/../b"; we then reject any residual "../" or leading "/".
func cleanKey(raw string) (string, bool) {
	k := strings.TrimSpace(raw)
	if k == "" || strings.HasPrefix(k, "/") || strings.HasSuffix(k, "/") {
		return "", false
	}
	for _, r := range k {
		if r < 0x20 || r == '\\' {
			return "", false
		}
	}
	clean := path.Clean(k)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || strings.Contains(clean, "/../") {
		return "", false
	}
	// path.Clean can only shorten a valid relative key; if it changed the meaning
	// (e.g. stripped a "./"), the cleaned form is still the correct object key.
	return clean, true
}

// cleanPrefix normalizes a listing prefix. A prefix MAY be empty (bucket root)
// and MAY end with "/" (a folder). It must not be absolute or contain traversal.
// An unsafe prefix is coerced to "" (root) rather than erroring — a listing of a
// bad prefix simply lists the root, never another location.
func cleanPrefix(raw string) string {
	p := strings.TrimSpace(raw)
	if p == "" {
		return ""
	}
	if strings.HasPrefix(p, "/") || strings.Contains(p, "..") {
		return ""
	}
	return p
}

// modTime returns unix seconds, or 0 for the zero time (a folder placeholder).
func modTime(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}

// isNoSuchBucket / isBucketNotEmpty classify the S3 error codes we map to a
// clean 404/409 instead of a generic 502.
func isNoSuchBucket(err error) bool {
	if resp, ok := errors.AsType[s3.ErrorResponse](err); ok {
		return resp.Code == "NoSuchBucket"
	}
	return false
}

func isBucketNotEmpty(err error) bool {
	if resp, ok := errors.AsType[s3.ErrorResponse](err); ok {
		return resp.Code == "BucketNotEmpty"
	}
	return false
}
