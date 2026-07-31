// Package s3 is the Fiber-facing subsystem that exposes an org-scoped S3
// object-storage file manager as /v1/s3/* on the unified Hanzo Cloud binary
// (HIP-0106). It is the DATA plane over the shared object store (SeaweedFS S3
// gateway) — the companion to clients/provisioning, which is the CONTROL plane
// (allocate/list/drop the s3 RESOURCE at /v1/s3 and /v1/s3/:name).
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
//     org-prefixed naming + the guard. The correct hardening is per-request
//     STS/session-policy or per-identity bucket-prefix restriction so the store
//     independently enforces the org boundary (defense in depth). Until then,
//     tenant() requiring a validated principal + the by-construction naming is
//     the sole boundary — kept minimal and auditable for that reason.
//   - Presign has no rate limit: minting is unthrottled (zip/middleware/ratelimit
//     is unwired in serve.go, platform-wide). The 5-minute TTL bounds a minted
//     capability's post-revocation lifetime; a per-route limiter is the platform
//     follow-up.
package storage

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strings"
	"time"

	s3 "github.com/hanzoai/s3-go"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/hanzoai/cloud/apps/provisioning"
	"github.com/hanzoai/cloud/apps/s3admin"
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

// state is storage's own data; shared deps live in the embedded cloud.Base. It
// holds the shared S3 admin connection and the per-org resource gate+meter. The
// meter is kept here (not in Base.Bill) because its commerce product label is "s3",
// NOT the subsystem name "storage". A not-Configured() admin means no credentials
// are present; the subsystem then mounts health/config only and every op fails
// closed 503. A nil/!Enabled() bill makes Gate allow and Meter a no-op.
type state struct {
	admin s3admin.Admin
	bill  *cloud.ResourceMeter
}

// ops carries the mounted Service into a TYPED op. zip fixes a typed handler's
// signature at (context.Context, *In) → (*Out, error), so the Service arrives on
// the receiver rather than as the parameter cloud.Handle passes a raw handler. A
// METHOD, not a wrapped free function, is also what makes the surface
// self-documenting: cmd/zipdoc lifts the doc comment off the function NAMED at
// the registration.
type ops struct{ *cloud.Service[state] }

// noArgs is the input of an op that takes nothing — a read scoped entirely by
// the validated principal the guard already resolved.
type noArgs struct{}

// scope is the two facts every typed op here opens with: the REQUEST behind the
// typed context, and the org the guard resolved onto it. Off the HTTP path (a
// CLI local invoke with no request) there is no principal, so it refuses with the
// same 403 the guard answers — fail-closed with one gate, not two.
func scope(ctx context.Context) (*zip.Ctx, string, error) {
	c, ok := cloud.Request(ctx)
	if !ok {
		return nil, "", zip.ErrForbidden("X-Org-Id required")
	}
	org := reqOrg(c)
	if org == "" {
		return nil, "", zip.ErrForbidden("X-Org-Id required")
	}
	return c, org, nil
}

// zipdoc lifts the doc comment off each typed op and its In/Out fields into
// zipdoc_gen.go, the ONLY way prose reaches the published document, the MCP tool
// list and the CLI help — Go drops comments at compile time.
//
//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

// Mount wires /v1/s3/* onto app. The "s3"-product meter and the guard-wrapped,
// unconditional route set make this a direct construction (cloud.NewBase), not
// cloud.Mount.
func Mount(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("s3.Mount: nil app")
	}
	if deps.Logger == nil {
		return fmt.Errorf("s3.Mount: nil deps.Logger")
	}
	s := &cloud.Service[state]{Base: cloud.NewBase(deps, "storage"), State: state{admin: s3admin.New(), bill: cloud.NewResourceMeter(deps, "s3")}}

	// A typed op is a route PLUS a registry entry, and the registry lives on the
	// App. A router that cannot reach it must fail the mount rather than serve
	// routes no projection knows about.
	zapp := cloud.ZipApp(app)
	if zapp == nil {
		return fmt.Errorf("s3.Mount: router carries no typed-op registry")
	}
	o := ops{s}

	// Register the FULL surface unconditionally — even when S3 is unconfigured.
	// The guard fails each op closed with 503 (s.State.admin.Configured() is false),
	// so the s3 subsystem always OWNS its route space. If the routes were mounted only
	// when configured, an unconfigured deployment would leak /v1/s3/buckets and
	// /v1/s3/objects to provisioning's GET /v1/s3/:name (a 404 "resource not
	// found") instead of the honest 503 — the file-manager surface must fail closed
	// under its own name, never fall through to a different subsystem's handler.
	//
	// Bridge carries into a typed op the request facts its signature drops — here
	// the org the guard resolved. It installs BEFORE the leaves it serves.
	g := app.Group("/v1/s3")
	g.Use(cloud.Bridge())
	// gated wraps each typed op in the SAME guard the raw routes carry, so a typed
	// op is gated exactly as the untyped route beside it.
	gated := zapp.With(guarded(s))

	// RAW: health answers 200 or a 503 whose body is the probe's contract
	// ({service, status, ready, error}), and a typed op has one success shape and
	// no vocabulary for a non-2xx one.
	g.Get("/health", cloud.Handle(s, health))
	zip.Get(gated, "/v1/s3/buckets", o.listBuckets)
	zip.Post(gated, "/v1/s3/buckets", o.createBucket, zip.WithStatus(http.StatusCreated))
	zip.Delete(gated, "/v1/s3/buckets/:bucket", o.deleteBucket)
	zip.Get(gated, "/v1/s3/buckets/:bucket/objects", o.listObjects)
	zip.Post(gated, "/v1/s3/buckets/:bucket/objects", o.presignUpload)
	// RAW: an object key is a PATH — it carries "/" — so these address it with a
	// trailing wildcard, and a typed op may not: zip renders "*" verbatim while the
	// document renders it as {wildcard1}, and the two spellings not matching is a
	// hard projection failure for the whole subset.
	g.Get("/buckets/:bucket/objects/*", guard(s, cloud.Handle(s, presignDownload)))
	g.Delete("/buckets/:bucket/objects/*", guard(s, cloud.Handle(s, deleteObject)))

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

// guard wraps a handler with the org gate + fail-closed check, and is the ONE
// place the s3 data plane meters per-org spend. A request with no resolvable org
// is refused 403 before S3 is touched; an unconfigured admin is 503. The resolved
// org is stashed in Locals so handlers read it once.
//
// Billing (fail-closed, per-org, single place): every guarded data-plane op is a
// billable object-storage operation. Before the handler runs, Gate checks the
// caller's balance — an unfunded org (402) or, in the default fail-closed
// posture, an unreachable commerce (503) is refused with NOTHING touched (no free
// storage op). After the handler SUCCEEDS, Meter debits the caller's org ledger
// (per-op fee, product "s3", async best-effort so the debit never blocks the
// response). A handler error is surfaced and NOT billed — mirrors the edge gate
// ("do not bill failed work"). fee==0 or unconfigured billing makes both no-ops.
func guard(s *cloud.Service[state], h zip.Handler) zip.Handler {
	return func(ctx *zip.Ctx) error {
		if !s.State.admin.Configured() {
			return zip.Errorf(http.StatusServiceUnavailable, "object storage is not configured")
		}
		org, ok := tenant(ctx)
		if !ok {
			return zip.ErrForbidden("X-Org-Id required")
		}
		ctx.Locals(orgKey, org)

		fee := cloud.ResourceFeeCents(opFeeEnvPrefix, "op")
		project, projectValidated := principal.ValidatedProject(ctx)
		if err := s.State.bill.Gate(ctx.Context(), principal.Ledger(ctx), project, projectValidated, "op", fee); err != nil {
			return cloud.DenyResource(ctx, err)
		}
		if err := h(ctx); err != nil {
			return err // handler failed — surface it; do not bill failed work.
		}
		s.State.bill.Meter(principal.Ledger(ctx), principal.Project(ctx), "op", fee, ctx.RequestID(), cloud.ClientIP(ctx))
		return nil
	}
}

// guarded is guard in middleware form — the SAME wrapper, partially applied, so
// zapp.With(guarded(s)) gates a typed op exactly as guard(s, h) gates the raw
// route beside it. One implementation, two call shapes.
func guarded(s *cloud.Service[state]) zip.Middleware {
	return func(next zip.Handler) zip.Handler { return guard(s, next) }
}

// orgKey is the Locals key the guard uses to hand the resolved org to handlers.
type ctxKey string

const orgKey ctxKey = "s3.org"

func reqOrg(ctx *zip.Ctx) string {
	if v, ok := ctx.Locals(orgKey).(string); ok {
		return v
	}
	return ""
}

// tenant resolves the caller's org exactly as clients/provisioning does — the
// SAME sanitized slug the control plane keys on, so buckets allocated there and
// operated on here share one org tag.
//
// REQUIRES A VALIDATED PRINCIPAL (RED HIGH). SanitizeIdentity sets X-User-Id ONLY
// when it validated a bearer/cookie; on the no-principal "Phase-1 data" path it
// RESTORES the client's raw X-Org-Id but leaves X-User-Id empty. A pure data
// plane that trusted X-Org-Id alone would let an in-cluster caller (a co-namespace
// pod within the cloud-api NetworkPolicy) forge `X-Org-Id: victim` with NO bearer
// and get cross-tenant object CRUD. So we gate on ctx.User() (X-User-Id) being
// present: every legitimate caller reaches this through the console BFF /cloud
// proxy, which mints a user-bound bearer (→ X-User-Id is set), so this refuses
// ONLY the anonymous-forge path and breaks no real client. Object storage is a
// data plane; it never serves an unauthenticated principal.
//
// Empty org is allowed only for a validated admin, bucketed under the literal
// "admin" org (a forged X-User-IsAdmin cannot exist without a validated principal
// either — SanitizeIdentity sets it only for a JWT-verified SuperAdmin, HIP-0026
// — and even then reaches only the admin bucket, never a real tenant's).
//
// NORMALIZATION — this uses provisioning.SanitizeOrg (case-folds to a DNS slug),
// NOT KMS's exact-match, ON PURPOSE: the S3 bucket name is derived through
// provisioning's SAME sanitized slug (BucketName), so a bucket provisioned via
// POST /v1/object is findable here — exact-match would break that lockstep. A real
// IAM owner claim is already a lowercase DNS label, so the fold is a no-op on
// validated input (and, post the principal gate above, only a validated principal
// reaches it). The divergence from KMS is intentional per-subsystem, not drift.
func tenant(ctx *zip.Ctx) (string, bool) {
	if !principal.Validated(ctx) {
		return "", false // no validated principal — refuse the forgeable data path
	}
	if org := provisioning.SanitizeOrg(ctx.Org()); org != "" {
		return org, true
	}
	if ctx.IsAdmin() {
		return "admin", true
	}
	return "", false
}

// ── bucket name mapping (tenant ↔ physical) ─────────────────────────────────

// physicalBucket maps a caller's FRIENDLY bucket name to its real S3 bucket name,
// namespaced to the caller's org. This is the SAME derivation
// provisioning.BucketName uses (bucketName(physicalName(org,name)) — org-hash
// prefixed AND '_'→'-' folded to a DNS-safe S3 name), so a bucket provisioned via
// POST /v1/object is browsable here and a bucket created here is a valid S3 name.
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

// ── health ──────────────────────────────────────────────────────────────────

// health is a REAL probe: 200 only when admin credentials are present (the store
// is reachable in principle); 503 + honest reason in health-only mode. Not
// JWT-gated — liveness must be probe-able without a token.
func health(s *cloud.Service[state], ctx *zip.Ctx) error {
	res := map[string]any{"service": "s3", "status": "ok"}
	if !s.State.admin.Configured() {
		res["status"], res["ready"] = "degraded", false
		res["error"] = "S3_ADMIN credentials not configured"
		return ctx.JSON(http.StatusServiceUnavailable, res)
	}
	res["ready"] = true
	res["presign"] = s.State.admin.PresignConfigured()
	return ctx.JSON(http.StatusOK, res)
}

// ── buckets ─────────────────────────────────────────────────────────────────

type bucketItem struct {
	// Name is the bucket's FRIENDLY name — what the tenant created it as, with the
	// server-side org prefix stripped.
	Name string `json:"name"`
	// CreatedAt is when the bucket was made, unix seconds.
	CreatedAt int64 `json:"createdAt"`
}

// bucketList is one page of the caller's own buckets.
type bucketList struct {
	// Buckets is every bucket in the caller's org namespace; another tenant's
	// buckets are invisible rather than forbidden.
	Buckets []bucketItem `json:"buckets"`
	// Total is how many buckets came back — the listing is not paginated.
	Total int `json:"total"`
}

// listBuckets returns the caller org's buckets by their friendly names.
//
// Response: {"buckets": [{"name": "photos", "createdAt": 1780000000}], "total": 1}
func (o ops) listBuckets(ctx context.Context, _ *noArgs) (*bucketList, error) {
	_, org, err := scope(ctx)
	if err != nil {
		return nil, err
	}
	cli, err := o.State.admin.Client()
	if err != nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "object storage unavailable")
	}
	all, err := cli.ListBuckets(ctx)
	if err != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "list buckets: %v", err)
	}
	out := make([]bucketItem, 0, len(all))
	for _, b := range all {
		name, ok := friendlyBucket(org, b.Name)
		if !ok {
			continue // another tenant's bucket — invisible
		}
		out = append(out, bucketItem{Name: name, CreatedAt: b.CreationDate.Unix()})
	}
	return &bucketList{Buckets: out, Total: len(out)}, nil
}

type createBucketRequest struct {
	// Name is the friendly bucket name, lowercase DNS-style
	// (^[a-z0-9]([a-z0-9-]{0,38}[a-z0-9])?$). The physical name is derived from the
	// caller's org server-side.
	Name string `json:"name"`
}

// createBucket makes a new bucket in the caller org's namespace and answers 201.
//
// Example: {"name": "photos"}
// Response: {"name": "photos", "createdAt": 1780000000}
func (o ops) createBucket(ctx context.Context, in *createBucketRequest) (*bucketItem, error) {
	_, org, err := scope(ctx)
	if err != nil {
		return nil, err
	}
	// Validate AS-IS (no silent lowercasing) so create and reference agree on the
	// one name shape — a client that creates "Photos" and lists "photos" would be
	// confusing; bucketNameRE requires lowercase, so mixed case is a clean 400.
	name := strings.TrimSpace(in.Name)
	if !bucketNameRE.MatchString(name) {
		return nil, zip.ErrBadRequest("name must match ^[a-z0-9]([a-z0-9-]{0,38}[a-z0-9])?$")
	}
	cli, err := o.State.admin.Client()
	if err != nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "object storage unavailable")
	}
	physical := physicalBucket(org, name)
	exists, err := cli.BucketExists(ctx, physical)
	if err != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "bucket check: %v", err)
	}
	if exists {
		return nil, zip.ErrConflict("bucket already exists")
	}
	if err := cli.MakeBucket(ctx, physical, s3.MakeBucketOptions{Region: o.State.admin.Region()}); err != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "create bucket: %v", err)
	}
	return &bucketItem{Name: name, CreatedAt: time.Now().Unix()}, nil
}

// bucketRef addresses one of the caller's buckets by its friendly name.
type bucketRef struct {
	// Bucket is the friendly bucket name from the path, as listBuckets returns it.
	Bucket string `json:"bucket"`
}

// deleteBucket removes an EMPTY bucket of the caller's org and answers 204. A
// bucket that still holds objects is refused rather than cascaded.
//
// Example: {"bucket": "photos"}
func (o ops) deleteBucket(ctx context.Context, in *bucketRef) (*struct{}, error) {
	_, org, err := scope(ctx)
	if err != nil {
		return nil, err
	}
	name, ok := friendlyParam(in.Bucket)
	if !ok {
		return nil, zip.ErrBadRequest("invalid bucket name")
	}
	cli, err := o.State.admin.Client()
	if err != nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "object storage unavailable")
	}
	physical := physicalBucket(org, name)
	if err := cli.RemoveBucket(ctx, physical); err != nil {
		if isNoSuchBucket(err) {
			return nil, zip.ErrNotFound("bucket not found")
		}
		if isBucketNotEmpty(err) {
			return nil, zip.ErrConflict("bucket is not empty")
		}
		return nil, zip.Errorf(http.StatusBadGateway, "delete bucket: %v", err)
	}
	return nil, nil
}

// ── objects ─────────────────────────────────────────────────────────────────

type objectItem struct {
	// Key is the object key RELATIVE to the requested prefix, so a UI can render a
	// breadcrumb without re-splitting the full key.
	Key string `json:"key"`
	// IsDir is true for a folder (an S3 common prefix), false for a real object.
	IsDir bool `json:"isDir"`
	// Size is the object's bytes; 0 for a folder.
	Size int64 `json:"size"`
	// LastModified is unix seconds; 0 for a folder.
	LastModified int64 `json:"lastModified"`
	// ETag is the store's entity tag, quotes stripped; absent for a folder.
	ETag string `json:"etag,omitempty"`
}

// listObjectsRequest addresses one folder level inside one of the caller's buckets.
type listObjectsRequest struct {
	// Bucket is the friendly bucket name from the path.
	Bucket string `json:"bucket"`
	// Prefix scopes the listing to a sub-folder; empty lists the bucket root. An
	// absolute or traversing prefix is coerced to the root rather than refused.
	Prefix string `json:"prefix"`
	// Recursive lists every key flat under prefix instead of one folder level.
	Recursive bool `json:"recursive"`
}

// objectList is one bounded page of a folder level.
type objectList struct {
	// Bucket is the friendly bucket name that was listed.
	Bucket string `json:"bucket"`
	// Prefix is the folder that was listed, after normalization.
	Prefix string `json:"prefix"`
	// Objects is the folder's entries, at most 1000 per call.
	Objects []objectItem `json:"objects"`
	// Total is how many entries came back.
	Total int `json:"total"`
}

// listObjects lists one folder level of a bucket, keys relative to the prefix.
// The page is capped at 1000 entries, so a huge bucket cannot exhaust memory.
//
// Example: {"bucket": "photos", "prefix": "2026/", "recursive": false}
// Response: {"bucket": "photos", "prefix": "2026/", "objects": [{"key": "may/", "isDir": true, "size": 0, "lastModified": 0}], "total": 1}
func (o ops) listObjects(ctx context.Context, in *listObjectsRequest) (*objectList, error) {
	_, org, err := scope(ctx)
	if err != nil {
		return nil, err
	}
	bname, ok := friendlyParam(in.Bucket)
	if !ok {
		return nil, zip.ErrBadRequest("invalid bucket name")
	}
	prefix := cleanPrefix(in.Prefix)
	// Folder-style by default (SeaweedFS applies a "/" delimiter when Recursive is
	// false, returning sub-prefixes as directory entries — the file-manager view).
	// recursive=true lists every key flat under the prefix. The brief's
	// ?delimiter=/ is the default and needs no param; only recursion is opt-in.
	cli, err := o.State.admin.Client()
	if err != nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "object storage unavailable")
	}
	physical := physicalBucket(org, bname)

	out := make([]objectItem, 0, 64)
	opts := s3.ListObjectsOptions{Prefix: prefix, Recursive: in.Recursive, MaxKeys: maxListKeys}
	for obj := range cli.ListObjects(ctx, physical, opts) {
		if obj.Err != nil {
			if isNoSuchBucket(obj.Err) {
				return nil, zip.ErrNotFound("bucket not found")
			}
			return nil, zip.Errorf(http.StatusBadGateway, "list objects: %v", obj.Err)
		}
		rel := strings.TrimPrefix(obj.Key, prefix)
		if rel == "" {
			continue // the prefix "folder" placeholder itself
		}
		isDir := strings.HasSuffix(obj.Key, "/")
		out = append(out, objectItem{
			Key:          rel,
			IsDir:        isDir,
			Size:         obj.Size,
			LastModified: modTime(obj.LastModified),
			ETag:         strings.Trim(obj.ETag, `"`),
		})
		if len(out) >= maxListKeys {
			break
		}
	}
	return &objectList{Bucket: bname, Prefix: prefix, Objects: out, Total: len(out)}, nil
}

type presignUploadRequest struct {
	// Bucket is the friendly bucket name from the path.
	Bucket string `json:"bucket"`
	// Key is the object key to upload, relative to the bucket root. It may contain
	// "/" but must not be absolute, empty, a folder marker or traverse with "..".
	Key string `json:"key"`
}

type presignResponse struct {
	// URL is the presigned URL the browser follows directly, signed against the
	// PUBLIC endpoint and scoped to this exact bucket and key.
	URL string `json:"url"`
	// Method is the verb the URL is signed for: PUT to upload, GET to download.
	Method string `json:"method"`
	// Key is the object key the URL addresses, after cleaning.
	Key string `json:"key"`
	// Expiry is how many seconds the URL stays valid.
	Expiry int64 `json:"expiresIn"`
}

// presignUpload mints a short-lived presigned PUT URL so the browser uploads
// straight to the object store — no body through this binary, and the admin
// credential never leaves the server.
//
// Example: {"bucket": "photos", "key": "2026/may/cover.jpg"}
// Response: {"url": "https://s3.hanzo.ai/o1234-photos/2026/may/cover.jpg?X-Amz-Signature=...", "method": "PUT", "key": "2026/may/cover.jpg", "expiresIn": 300}
func (o ops) presignUpload(ctx context.Context, in *presignUploadRequest) (*presignResponse, error) {
	_, org, err := scope(ctx)
	if err != nil {
		return nil, err
	}
	bname, ok := friendlyParam(in.Bucket)
	if !ok {
		return nil, zip.ErrBadRequest("invalid bucket name")
	}
	key, ok := cleanKey(in.Key)
	if !ok {
		return nil, zip.ErrBadRequest("key is required and must be a clean object path")
	}
	if !o.State.admin.PresignConfigured() {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "presigned upload is not available (no public endpoint configured)")
	}
	pub, err := o.State.admin.PublicClient()
	if err != nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "object storage unavailable")
	}
	physical := physicalBucket(org, bname)
	u, err := pub.PresignedPutObject(ctx, physical, key, presignTTL)
	if err != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "presign upload: %v", err)
	}
	return &presignResponse{
		URL: u.String(), Method: http.MethodPut, Key: key, Expiry: int64(presignTTL.Seconds()),
	}, nil
}

// presignDownload returns a presigned GET URL for the object at the trailing
// wildcard path. Same properties as upload: public host, exact key, time-boxed.
// The Content-Disposition is set to attachment(filename) so a browser downloads
// rather than renders.
func presignDownload(s *cloud.Service[state], ctx *zip.Ctx) error {
	org := reqOrg(ctx)
	bname, ok := friendlyParam(ctx.Param("bucket"))
	if !ok {
		return zip.ErrBadRequest("invalid bucket name")
	}
	key, ok := cleanKey(reqWildcard(ctx))
	if !ok {
		return zip.ErrBadRequest("object key is required and must be a clean path")
	}
	if !s.State.admin.PresignConfigured() {
		return zip.Errorf(http.StatusServiceUnavailable, "presigned download is not available (no public endpoint configured)")
	}
	pub, err := s.State.admin.PublicClient()
	if err != nil {
		return zip.Errorf(http.StatusServiceUnavailable, "object storage unavailable")
	}
	physical := physicalBucket(org, bname)
	params := url.Values{}
	params.Set("response-content-disposition", "attachment; filename=\""+path.Base(key)+"\"")
	u, err := pub.PresignedGetObject(ctx.Context(), physical, key, presignTTL, params)
	if err != nil {
		return zip.Errorf(http.StatusBadGateway, "presign download: %v", err)
	}
	return ctx.JSON(http.StatusOK, presignResponse{
		URL: u.String(), Method: http.MethodGet, Key: key, Expiry: int64(presignTTL.Seconds()),
	})
}

// deleteObject removes one object at the trailing wildcard path.
func deleteObject(s *cloud.Service[state], ctx *zip.Ctx) error {
	org := reqOrg(ctx)
	bname, ok := friendlyParam(ctx.Param("bucket"))
	if !ok {
		return zip.ErrBadRequest("invalid bucket name")
	}
	key, ok := cleanKey(reqWildcard(ctx))
	if !ok {
		return zip.ErrBadRequest("object key is required and must be a clean path")
	}
	cli, err := s.State.admin.Client()
	if err != nil {
		return zip.Errorf(http.StatusServiceUnavailable, "object storage unavailable")
	}
	physical := physicalBucket(org, bname)
	if err := cli.RemoveObject(ctx.Context(), physical, key, s3.RemoveObjectOptions{}); err != nil {
		if isNoSuchBucket(err) {
			return zip.ErrNotFound("bucket not found")
		}
		return zip.Errorf(http.StatusBadGateway, "delete object: %v", err)
	}
	return ctx.NoContent(http.StatusNoContent)
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

// reqWildcard returns the trailing "*" segment of an /objects/* route, trimmed of
// a leading slash. This is the object key (may contain "/"—a nested path).
func reqWildcard(ctx *zip.Ctx) string {
	return strings.TrimPrefix(strings.TrimSpace(ctx.Param("*")), "/")
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
	var resp s3.ErrorResponse
	if errors.As(err, &resp) {
		return resp.Code == "NoSuchBucket"
	}
	return false
}

func isBucketNotEmpty(err error) bool {
	var resp s3.ErrorResponse
	if errors.As(err, &resp) {
		return resp.Code == "BucketNotEmpty"
	}
	return false
}
