// Package storage exposes an org-scoped S3 object-storage file manager as
// /v1/s3/* on the cloud binary. It speaks the S3 API to one object store — on a
// laptop, a local S3 server (hanzoai/s3) at 127.0.0.1:9000.
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
// as "o"<orgHash>-<name> (BucketName). The client speaks in FRIENDLY names
// ("photos"); the server maps friendly↔physical and NEVER trusts a
// client-supplied physical name. List filters to the caller's prefix;
// create/delete/object ops re-derive the physical name from the caller's org, so
// one tenant can never address another's bucket — the isolation boundary is by
// construction, not by a checked flag.
//
// FAIL-CLOSED — absent S3_ACCESS_KEY/S3_SECRET_KEY the subsystem mounts
// health-only: /v1/s3/health is an honest 503 and every op returns 503. It never
// fabricates a bucket or object list.
//
// The store holds ONE credential for the whole binary, so the S3 layer enforces
// no tenant boundary: isolation is this subsystem's org-prefixed naming plus the
// validated-principal guard, kept minimal and auditable for that reason.
package storage

import (
	"encoding/json"
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
	"github.com/hanzoai/cloud/clients/principal"
	"github.com/zap-proto/zip"
)

// presignTTL bounds every presigned upload/download URL. Presigned URLs have no
// server-side revocation, so the TTL is the revocation window: short enough that
// a minted capability barely outlives a revoked session, long enough for a
// browser to finish a normal PUT/GET. A caller re-mints for a fresh window.
const presignTTL = 5 * time.Minute

// maxListKeys caps one object-listing page so a bucket with millions of keys
// cannot exhaust memory or the response. Folder-style navigation only needs one
// level at a time, so this is generous.
const maxListKeys = 1000

// bucketNameRE is the FRIENDLY bucket name a tenant supplies: a DNS-safe slug, so
// the physical name BucketName produces from it is always a legal S3 bucket name.
var bucketNameRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,38}[a-z0-9])?$`)

// state is storage's own data; shared deps live in the embedded cloud.Base. A
// not-Configured() backend means no credentials are present; the subsystem then
// mounts health only and every op fails closed 503.
type state struct {
	store backend
}

// Mount wires /v1/s3/* onto app. The guard-wrapped, unconditional route set makes
// this a direct construction (cloud.NewBase), not cloud.Mount.
func Mount(app *zip.App, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("s3.Mount: nil zip.App")
	}
	if deps.Logger == nil {
		return fmt.Errorf("s3.Mount: nil deps.Logger")
	}
	s := &cloud.Service[state]{Base: cloud.NewBase(deps, "storage"), State: state{store: newBackend()}}

	// Register the FULL surface unconditionally — even when S3 is unconfigured.
	// The guard fails each op closed with 503 (s.State.store.Configured() is false),
	// so the s3 subsystem always OWNS its route space and answers an honest 503
	// under its own name rather than a 404 from whatever matches next.
	g := app.Group("/v1/s3")
	g.Get("/health", cloud.Handle(s, health))
	g.Get("/buckets", guard(s, cloud.Handle(s, listBuckets)))
	g.Post("/buckets", guard(s, cloud.Handle(s, createBucket)))
	g.Delete("/buckets/:bucket", guard(s, cloud.Handle(s, deleteBucket)))
	g.Get("/buckets/:bucket/objects", guard(s, cloud.Handle(s, listObjects)))
	g.Post("/buckets/:bucket/objects", guard(s, cloud.Handle(s, presignUpload)))
	g.Get("/buckets/:bucket/objects/*", guard(s, cloud.Handle(s, presignDownload)))
	g.Delete("/buckets/:bucket/objects/*", guard(s, cloud.Handle(s, deleteObject)))

	if !s.State.store.Configured() {
		s.Log.Warn("s3 subsystem mounted fail-closed: S3_ACCESS_KEY/S3_SECRET_KEY not set (all ops 503 until set)")
		return nil
	}
	s.Log.Info("s3 subsystem mounted", "prefix", "/v1/s3", "endpoint", s.State.store.endpoint)
	return nil
}

// guard wraps a handler with the org gate + fail-closed check. A request with no
// resolvable org is refused 403 before S3 is touched; an unconfigured store is
// 503. The resolved org is stashed in Locals so handlers read it once.
func guard(s *cloud.Service[state], h zip.Handler) zip.Handler {
	return func(ctx *zip.Ctx) error {
		if !s.State.store.Configured() {
			return zip.Errorf(http.StatusServiceUnavailable, "object storage is not configured")
		}
		org, ok := tenant(ctx)
		if !ok {
			return zip.ErrForbidden("X-Org-Id required")
		}
		ctx.Locals(orgKey, org)
		return h(ctx)
	}
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

// tenant resolves the caller's org as the sanitized slug (cloud.SanitizeOrg) the
// bucket naming keys on.
//
// REQUIRES A VALIDATED PRINCIPAL. SanitizeIdentity sets X-User-Id ONLY when it
// validated a bearer/cookie; a data plane that trusted X-Org-Id alone would let a
// caller name any org. Object storage never serves an unauthenticated principal.
//
// Empty org is allowed only for a validated admin, bucketed under the literal
// "admin" org, never a real tenant's.
func tenant(ctx *zip.Ctx) (string, bool) {
	if !principal.Validated(ctx) {
		return "", false // no validated principal — refuse the forgeable data path
	}
	if org := cloud.SanitizeOrg(ctx.Org()); org != "" {
		return org, true
	}
	if ctx.IsAdmin() {
		return "admin", true
	}
	return "", false
}

// ── bucket name mapping (tenant ↔ physical) ─────────────────────────────────

// physicalBucket maps a caller's FRIENDLY bucket name to its real S3 bucket name,
// namespaced to the caller's org.
func physicalBucket(org, friendly string) string { return BucketName(org, friendly) }

// orgPrefix is the S3-bucket-name prefix that ALL of a caller's buckets share
// ("o"<orgHash>-). List filters to it and strips it to recover friendly names.
func orgPrefix(org string) string { return BucketPrefix(org) }

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

// health is a REAL probe: 200 only when credentials are present (the store is
// reachable in principle); 503 + honest reason in health-only mode. Not
// JWT-gated — liveness must be probe-able without a token.
func health(s *cloud.Service[state], ctx *zip.Ctx) error {
	res := map[string]any{"service": "s3", "status": "ok"}
	if !s.State.store.Configured() {
		res["status"], res["ready"] = "degraded", false
		res["error"] = "S3 credentials not configured"
		return ctx.JSON(http.StatusServiceUnavailable, res)
	}
	res["ready"] = true
	return ctx.JSON(http.StatusOK, res)
}

// ── buckets ─────────────────────────────────────────────────────────────────

type bucketItem struct {
	Name      string `json:"name"`      // friendly name
	CreatedAt int64  `json:"createdAt"` // unix seconds
}

// listBuckets returns ONLY the caller's buckets (physical name has the caller's
// org prefix), with the prefix stripped so the tenant sees friendly names.
func listBuckets(s *cloud.Service[state], ctx *zip.Ctx) error {
	org := reqOrg(ctx)
	cli, err := s.State.store.Client()
	if err != nil {
		return zip.Errorf(http.StatusServiceUnavailable, "object storage unavailable")
	}
	all, err := cli.ListBuckets(ctx.Context())
	if err != nil {
		return zip.Errorf(http.StatusBadGateway, "list buckets: %v", err)
	}
	out := make([]bucketItem, 0, len(all))
	for _, b := range all {
		name, ok := friendlyBucket(org, b.Name)
		if !ok {
			continue // another tenant's bucket — invisible
		}
		out = append(out, bucketItem{Name: name, CreatedAt: b.CreationDate.Unix()})
	}
	return ctx.JSON(http.StatusOK, map[string]any{"buckets": out, "total": len(out)})
}

type createBucketRequest struct {
	Name string `json:"name"`
}

// createBucket makes a new org-scoped bucket. The physical name is derived from
// the caller's org, so a tenant can only ever create in its own namespace.
func createBucket(s *cloud.Service[state], ctx *zip.Ctx) error {
	org := reqOrg(ctx)
	var req createBucketRequest
	if err := json.Unmarshal(ctx.Body(), &req); err != nil {
		return zip.Errorf(http.StatusBadRequest, "invalid JSON body: %v", err)
	}
	// Validate AS-IS (no silent lowercasing) so create and reference agree on the
	// one name shape — a client that creates "Photos" and lists "photos" would be
	// confusing; bucketNameRE requires lowercase, so mixed case is a clean 400.
	name := strings.TrimSpace(req.Name)
	if !bucketNameRE.MatchString(name) {
		return zip.ErrBadRequest("name must match ^[a-z0-9]([a-z0-9-]{0,38}[a-z0-9])?$")
	}
	cli, err := s.State.store.Client()
	if err != nil {
		return zip.Errorf(http.StatusServiceUnavailable, "object storage unavailable")
	}
	physical := physicalBucket(org, name)
	exists, err := cli.BucketExists(ctx.Context(), physical)
	if err != nil {
		return zip.Errorf(http.StatusBadGateway, "bucket check: %v", err)
	}
	if exists {
		return zip.ErrConflict("bucket already exists")
	}
	if err := cli.MakeBucket(ctx.Context(), physical, s3.MakeBucketOptions{Region: s.State.store.region}); err != nil {
		return zip.Errorf(http.StatusBadGateway, "create bucket: %v", err)
	}
	return ctx.JSON(http.StatusCreated, bucketItem{Name: name, CreatedAt: time.Now().Unix()})
}

// deleteBucket removes an EMPTY bucket (S3 refuses a non-empty one — we do not
// cascade a delete of a tenant's objects behind a single bucket call).
func deleteBucket(s *cloud.Service[state], ctx *zip.Ctx) error {
	org := reqOrg(ctx)
	name, ok := friendlyParam(ctx.Param("bucket"))
	if !ok {
		return zip.ErrBadRequest("invalid bucket name")
	}
	cli, err := s.State.store.Client()
	if err != nil {
		return zip.Errorf(http.StatusServiceUnavailable, "object storage unavailable")
	}
	physical := physicalBucket(org, name)
	if err := cli.RemoveBucket(ctx.Context(), physical); err != nil {
		if isNoSuchBucket(err) {
			return zip.ErrNotFound("bucket not found")
		}
		if isBucketNotEmpty(err) {
			return zip.ErrConflict("bucket is not empty")
		}
		return zip.Errorf(http.StatusBadGateway, "delete bucket: %v", err)
	}
	return ctx.NoContent(http.StatusNoContent)
}

// ── objects ─────────────────────────────────────────────────────────────────

type objectItem struct {
	Key          string `json:"key"`          // key RELATIVE to the requested prefix
	IsDir        bool   `json:"isDir"`        // true for a folder (common prefix)
	Size         int64  `json:"size"`         // bytes (0 for a folder)
	LastModified int64  `json:"lastModified"` // unix seconds (0 for a folder)
	ETag         string `json:"etag,omitempty"`
}

// listObjects lists one folder level of a bucket. ?prefix= scopes to a
// sub-"folder"; folder-style by default (a "/" delimiter, so sub-prefixes come
// back as dir entries) unless ?recursive=true, which lists every key flat under
// the prefix. Keys are returned RELATIVE to the requested prefix so the UI
// renders a breadcrumb. The listing is bounded by maxListKeys (both the S3-side
// MaxKeys page and a hard break) so a huge bucket cannot exhaust memory.
func listObjects(s *cloud.Service[state], ctx *zip.Ctx) error {
	org := reqOrg(ctx)
	bname, ok := friendlyParam(ctx.Param("bucket"))
	if !ok {
		return zip.ErrBadRequest("invalid bucket name")
	}
	prefix := cleanPrefix(ctx.Query("prefix"))
	// Folder-style by default (S3 applies a "/" delimiter when Recursive is
	// false, returning sub-prefixes as directory entries — the file-manager view).
	// ?recursive=true lists every key flat under the prefix. The brief's
	// ?delimiter=/ is the default and needs no param; only recursion is opt-in.
	recursive := ctx.Query("recursive") == "true"

	cli, err := s.State.store.Client()
	if err != nil {
		return zip.Errorf(http.StatusServiceUnavailable, "object storage unavailable")
	}
	physical := physicalBucket(org, bname)

	out := make([]objectItem, 0, 64)
	opts := s3.ListObjectsOptions{Prefix: prefix, Recursive: recursive, MaxKeys: maxListKeys}
	for obj := range cli.ListObjects(ctx.Context(), physical, opts) {
		if obj.Err != nil {
			if isNoSuchBucket(obj.Err) {
				return zip.ErrNotFound("bucket not found")
			}
			return zip.Errorf(http.StatusBadGateway, "list objects: %v", obj.Err)
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
	return ctx.JSON(http.StatusOK, map[string]any{
		"bucket": bname, "prefix": prefix, "objects": out, "total": len(out),
	})
}

type presignUploadRequest struct {
	Key string `json:"key"` // object key to upload (relative to the bucket root)
}

type presignResponse struct {
	URL    string `json:"url"`    // presigned URL the browser follows directly
	Method string `json:"method"` // "PUT" (upload) or "GET" (download)
	Key    string `json:"key"`
	Expiry int64  `json:"expiresIn"` // seconds until the URL expires
}

// presignUpload returns a presigned PUT URL the browser uses to upload DIRECTLY
// to S3 (no large body through the server, and the credential never leaves the
// server). The URL is scoped to the exact bucket+key, and it expires (presignTTL). The object key is path-cleaned so a "../" cannot escape
// the bucket.
func presignUpload(s *cloud.Service[state], ctx *zip.Ctx) error {
	org := reqOrg(ctx)
	bname, ok := friendlyParam(ctx.Param("bucket"))
	if !ok {
		return zip.ErrBadRequest("invalid bucket name")
	}
	var req presignUploadRequest
	if err := json.Unmarshal(ctx.Body(), &req); err != nil {
		return zip.Errorf(http.StatusBadRequest, "invalid JSON body: %v", err)
	}
	key, ok := cleanKey(req.Key)
	if !ok {
		return zip.ErrBadRequest("key is required and must be a clean object path")
	}
	pub, err := s.State.store.Client()
	if err != nil {
		return zip.Errorf(http.StatusServiceUnavailable, "object storage unavailable")
	}
	physical := physicalBucket(org, bname)
	u, err := pub.PresignedPutObject(ctx.Context(), physical, key, presignTTL)
	if err != nil {
		return zip.Errorf(http.StatusBadGateway, "presign upload: %v", err)
	}
	return ctx.JSON(http.StatusOK, presignResponse{
		URL: u.String(), Method: http.MethodPut, Key: key, Expiry: int64(presignTTL.Seconds()),
	})
}

// presignDownload returns a presigned GET URL for the object at the trailing
// wildcard path. Same properties as upload: exact key, time-boxed.
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
	pub, err := s.State.store.Client()
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
	cli, err := s.State.store.Client()
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
