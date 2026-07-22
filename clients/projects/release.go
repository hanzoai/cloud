// release.go — how content GETS to a site's serving prefix.
//
// The agentic builder produces build output INSIDE our object store (its code
// execution writes there). Pushing those bytes back out through an HTTP upload
// API and in again would be pure waste, so publishing is a SERVER-SIDE PROMOTE:
// no bytes traverse the API, and no client ever holds an S3 credential.
//
// The model is values plus a pointer (HIP-0014, "Static Sites: Releases and
// Pointers"): a build output is promoted into a Release, and the site's pointer
// is flipped to it.
//
//   - A Release is a VALUE: an immutable prefix whose id is a digest of the
//     object manifest it was built from. Identical bytes ⇒ identical id, so
//     re-publishing the same build is idempotent by construction rather than by
//     a remembered request key. Different bytes can never reuse an id.
//   - The pointer is projects.current_release. Serving reads THROUGH it
//     (siteResolver.Resolve), so activation is one atomic UPDATE and rollback is
//     the same flip aimed at an older release — free, because releases are
//     immutable and retained.
//
// TENANT ISOLATION — the make-or-break property, and the reason the source is
// NOT an S3 URL. A caller names a path RELATIVE to their own org's storage
// space; the org segment is prepended server-side from the VALIDATED principal
// (org(c), the one org rule this package already uses for every site prefix),
// and the bucket is server-owned and never appears in the request at all. So the
// worst a hostile source string can address is something the caller's own org
// already owns — "copy an arbitrary prefix" is not a reachable state, and the
// server-side copy is therefore not an exfiltration primitive. Traversal is
// killed by safeRel, the SAME rooted-clean rule the artifact walker uses.
package projects

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	minio "github.com/hanzoai/s3-go"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/sites"
	"github.com/zap-proto/zip"
)

// Release guards. The object-count and total-byte caps are the SAME budget the
// artifact deploy path enforces (maxFiles / maxTotalBytes in blob.go) — one
// "how big is a site" policy for every way content arrives, not a second set of
// numbers to drift. They are enforced WHILE listing, so a hostile prefix with
// millions of objects aborts early instead of being enumerated.
const (
	// maxSourceLen bounds the caller-supplied source path before any parsing.
	maxSourceLen = 512
	// maxReleaseList bounds the rollback menu one request can ask for.
	maxReleaseList = 200
)

// releaseIDRE is the release-id grammar: "rel_" + 128 bits of the manifest
// digest in lowercase hex. It is the traversal guard for the id, which is BOTH a
// URL path segment and an S3 key segment. It is applied on the way in (the route
// param) AND on the way out (serving, in siteResolver.Resolve), so an id can
// never widen a prefix even if a row were somehow poisoned.
var releaseIDRE = regexp.MustCompile(`^rel_[0-9a-f]{32}$`)

var (
	errBadSource     = errors.New("projects: source must be a relative path inside your org's storage")
	errEmptySource   = errors.New("projects: source prefix contains no objects")
	errUnsafeKey     = errors.New("projects: source contains an unsafe object key")
	errNoIndex       = errors.New("projects: source has no index.html at its root")
	errTooManyFiles  = errors.New("projects: source exceeds the release object limit")
	errTooLarge      = errors.New("projects: source exceeds the release size limit")
	errSourceChanged = errors.New("projects: source changed while publishing; retry")
)

// releaseSpace is the per-org namespace holding every release of every site the
// org owns. It is a SIBLING of the live site prefixes, never a child of one:
// `<org>/.releases/<slug>/<id>/`. Two properties follow, and both matter.
//
//   - A full-artifact deploy purges `<org>/<slug>/` (blob.uploadSite) and a
//     project delete purges the same. Releases sit outside that subtree, so
//     neither can shred a release the pointer still names.
//   - `.releases` can never collide with a site: slugRE forbids '.', so no
//     project slug can ever be the string ".releases".
//
// The site's own URL space therefore never exposes the release store either —
// requests are served from the ONE prefix the resolver returns, not by walking.
func releaseSpace(org string) string { return org + "/.releases" }

// releasePrefix is the immutable S3 prefix of one release. Every segment is
// server-owned: org from the validated principal, slug from the tenant-scoped
// project row, id from releaseIDRE-shaped digest.
func releasePrefix(org, slug, id string) string {
	return releaseSpace(org) + "/" + slug + "/" + id
}

// releaseObject is one entry of a release manifest: the site-relative key plus
// the store's own content identity for it (size + ETag). The manifest — not the
// bytes — is what gets digested into the release id.
type releaseObject struct {
	rel  string
	size int64
	etag string
}

// sourcePrefix resolves a caller-supplied source into an absolute S3 prefix
// INSIDE the caller's own org space. This is the isolation boundary for the
// server-side copy, and it holds structurally rather than by inspection:
//
//  1. org is prepended by the SERVER from the validated principal. The caller
//     cannot express an org segment — there is no syntax for it, because the
//     value they supply is always interpreted relative to their own root.
//  2. safeRel (the same rooted-clean rule the artifact walker applies to every
//     archive entry) rejects absolute paths and any "..'" that escapes, so the
//     result provably stays under `<org>/`.
//  3. A scheme or an empty/root path is refused outright: a build output is a
//     specific directory, never a URL and never the caller's whole tenant.
//
// The bucket is NOT derived from the request at any point — it is the process's
// own projects bucket — so there is no cross-bucket reach either.
func sourcePrefix(org, raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" || len(s) > maxSourceLen {
		return "", errBadSource
	}
	if strings.Contains(s, "://") {
		return "", errBadSource // a source is a path in your own space, never a URL
	}
	if strings.ContainsFunc(s, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return "", errBadSource
	}
	rel, ok := safeRel(s)
	if !ok || rel == "" {
		return "", errBadSource // absolute, escaping, or the whole-org root
	}
	return org + "/" + rel, nil
}

// scanSource lists the source prefix and builds the release manifest, enforcing
// every guard as it goes. It returns the manifest sorted by key (so the digest
// is independent of the backend's listing order) and the total byte count.
//
// Bounded by construction: it stops at the first object past the cap instead of
// enumerating an arbitrarily large prefix, and it only ever holds the manifest
// (keys + sizes + etags), never object bodies — a release of any size costs the
// same memory here.
func scanSource(ctx context.Context, cli *minio.Client, bucket, src string) ([]releaseObject, int64, error) {
	var (
		objs  []releaseObject
		total int64
	)
	// A trailing slash makes this a strict directory listing: `<src>/` can never
	// match a sibling prefix that merely shares a name stem (e.g. "build" must not
	// pull in "build-old").
	lister := cli.ListObjects(ctx, bucket, minio.ListObjectsOptions{Prefix: src + "/", Recursive: true})
	for obj := range lister {
		if obj.Err != nil {
			return nil, 0, fmt.Errorf("list source: %w", obj.Err)
		}
		rel := strings.TrimPrefix(obj.Key, src+"/")
		if rel == "" || strings.HasSuffix(rel, "/") {
			continue // the prefix placeholder / a directory marker carries no bytes
		}
		// S3 is a flat keyspace, so a key MAY literally contain a ".." segment. We
		// refuse the release rather than rewriting the key: silently normalizing
		// could collapse two distinct source keys onto one destination.
		clean, ok := safeRel(rel)
		if !ok || clean != rel {
			return nil, 0, errUnsafeKey
		}
		if len(objs) >= maxFiles {
			return nil, 0, errTooManyFiles
		}
		total += obj.Size
		if total > maxTotalBytes {
			return nil, 0, errTooLarge
		}
		objs = append(objs, releaseObject{rel: rel, size: obj.Size, etag: strings.Trim(obj.ETag, `"`)})
	}
	if len(objs) == 0 {
		return nil, 0, errEmptySource
	}
	sort.Slice(objs, func(i, j int) bool { return objs[i].rel < objs[j].rel })
	// The same contract the artifact path enforces (finalizeSite): a site is a
	// thing with an index. One deploy contract, however the content arrived.
	if !hasIndex(objs) {
		return nil, 0, errNoIndex
	}
	return objs, total, nil
}

func hasIndex(objs []releaseObject) bool {
	for _, o := range objs {
		if o.rel == "index.html" {
			return true
		}
	}
	return false
}

// releaseID is the content address: SHA-256 over the sorted manifest of
// (key, size, etag) triples, truncated to 128 bits and hex-encoded.
//
// Including the per-object ETag is what makes this a CONTENT address rather than
// a name address: two builds that differ only in bytes produce different ETags,
// hence a different release, so a publish can never be deduplicated onto a
// release holding different content. The fields are NUL-separated and newline-
// terminated so no combination of key and size can be re-parsed as another.
//
// The ETag is the store's content identity (MD5 for a single-part PUT), so this
// is an idempotency key, not a security boundary — and it does not need to be
// one: the source always lies inside the caller's own org, where they may
// already write whatever they like.
func releaseID(objs []releaseObject) string {
	h := sha256.New()
	for _, o := range objs {
		_, _ = h.Write([]byte(o.rel))
		_, _ = h.Write([]byte{0})
		_, _ = h.Write([]byte(strconv.FormatInt(o.size, 10)))
		_, _ = h.Write([]byte{0})
		_, _ = h.Write([]byte(o.etag))
		_, _ = h.Write([]byte{'\n'})
	}
	return "rel_" + hex.EncodeToString(h.Sum(nil)[:16])
}

// copyRelease server-side-copies every manifest object from the source prefix to
// the release prefix. No object body ever enters this process — the store moves
// the bytes itself.
//
// Each copy is CONDITIONAL on the exact ETag the manifest was digested from
// (x-amz-copy-source-if-match). That closes the window between listing and
// copying: if a concurrent writer mutates the build output mid-publish, the copy
// fails and the whole release is abandoned, so a release's bytes always match
// the digest that names it. The destination's returned ETag is verified too, so
// the guarantee survives a backend that ignores the conditional header.
//
// Metadata is REPLACED on the destination with the same content-type and cache
// policy the artifact upload path writes (sites.CacheControlFor), so a promoted
// site and an uploaded one serve identically.
func copyRelease(ctx context.Context, cli *minio.Client, bucket, src, dst, htmlOverride string, objs []releaseObject) error {
	for _, o := range objs {
		ct := mime.TypeByExtension(path.Ext(o.rel))
		if ct == "" {
			ct = "application/octet-stream"
		}
		info, err := cli.CopyObject(ctx,
			minio.CopyDestOptions{
				Bucket: bucket, Object: dst + "/" + o.rel,
				ReplaceMetadata: true,
				ContentType:     ct,
				CacheControl:    sites.CacheControlFor(o.rel, htmlOverride),
			},
			minio.CopySrcOptions{
				Bucket: bucket, Object: src + "/" + o.rel,
				MatchETag: o.etag,
			})
		if err != nil {
			// A precondition failure means the source moved under us; anything else is
			// a real store error. Both abandon the release — nothing is ever activated.
			if isPreconditionFailed(err) {
				return errSourceChanged
			}
			return fmt.Errorf("copy %q: %w", o.rel, err)
		}
		if got := strings.Trim(info.ETag, `"`); got != "" && got != o.etag {
			return errSourceChanged
		}
	}
	return nil
}

// isPreconditionFailed reports whether a store error is the conditional-copy
// rejection (HTTP 412 / PreconditionFailed), i.e. the source object no longer
// carries the ETag the manifest was built from.
func isPreconditionFailed(err error) bool {
	var resp minio.ErrorResponse
	if errors.As(err, &resp) {
		return resp.StatusCode == http.StatusPreconditionFailed || resp.Code == "PreconditionFailed"
	}
	return false
}

// promote is the ONE release-creation core: validate the source inside the
// caller's org, scan it into a manifest, content-address it, copy it into a NEW
// immutable prefix, and only then record the release row.
//
// Ordering is the atomicity argument. The row is written LAST, so:
//   - a partially-copied prefix has no row, and ActivateRelease cannot flip to a
//     release with no row — a partial copy is unreachable, not merely unlikely;
//   - a crash mid-copy leaves orphan objects that nothing points at, and the
//     retry recomputes the SAME id (same bytes ⇒ same digest), re-copies over
//     them, and converges. The failure mode is wasted space, never a bad serve.
//
// Re-publishing an unchanged source short-circuits on the existing row: same
// digest, same id, no copy at all.
func promote(s *cloud.Service[state], ctx context.Context, org string, p Project, source string) (Release, error) {
	src, err := sourcePrefix(org, source)
	if err != nil {
		return Release{}, err
	}
	cli, err := s.State.blob.client()
	if err != nil {
		return Release{}, fmt.Errorf("s3 connect: %w", err)
	}
	if err := s.State.blob.ensureBucket(ctx, cli); err != nil {
		return Release{}, err
	}
	objs, total, err := scanSource(ctx, cli, s.State.blob.bucket, src)
	if err != nil {
		return Release{}, err
	}
	id := releaseID(objs)

	// Idempotent re-publish: this exact content is already a complete release for
	// this site. The row's existence is the promise the copy finished.
	if existing, gErr := s.State.store.GetRelease(ctx, org, p.Slug, id); gErr == nil {
		return existing, nil
	} else if !errors.Is(gErr, errNotFound) {
		return Release{}, gErr
	}

	dst := releasePrefix(org, p.Slug, id)
	if err := copyRelease(ctx, cli, s.State.blob.bucket, src, dst, p.CacheControl, objs); err != nil {
		return Release{}, err
	}

	r := Release{
		ID: id, Org: org, Slug: p.Slug, Prefix: dst, Source: strings.TrimSpace(source),
		Objects: len(objs), Bytes: total, CreatedAt: time.Now().Unix(),
	}
	if err := s.State.store.PutRelease(ctx, r); err != nil {
		return Release{}, err
	}
	return r, nil
}

// activate points the site at a release and runs the go-live side effects.
//
// The flip is the store's single atomic statement and NOTHING here writes the
// pointer again: the follow-up only refreshes denormalized display fields
// (MarkLive), so two concurrent activations can never leave the pointer
// disagreeing with whichever flip won. Everything after the flip is best-effort
// — the site is already serving the new release, so no edge-hygiene failure may
// turn a completed activation into an error.
func activate(s *cloud.Service[state], ctx context.Context, org string, p Project, id string) error {
	if err := s.State.store.ActivateRelease(ctx, org, p.Slug, id, time.Now().Unix()); err != nil {
		return err
	}
	// Claim the public host + purge the edge, exactly as a deploy does — a release
	// that is live must be reachable and must not serve a cached predecessor.
	onPublish(s, ctx, org, &p)
	if err := s.State.store.MarkLive(ctx, org, p.Slug, siteURL(s, org, p.Slug),
		s.State.blob.bucket, p.LastPurgeAt, time.Now().Unix()); err != nil {
		s.Log.Warn("release activated but project metadata update failed",
			"org", org, "slug", p.Slug, "release", id, "err", err)
	}
	return nil
}

// ---- HTTP ----

// releaseView is the published shape of a release, used by every release route
// so create, activate, and list describe the resource identically.
type releaseView struct {
	ReleaseID string `json:"releaseId"`
	Slug      string `json:"slug"`
	Objects   int    `json:"objects"`
	Bytes     int64  `json:"bytes"`
	Source    string `json:"source,omitempty"`
	Active    bool   `json:"active"`
	URL       string `json:"url,omitempty"`
	CreatedAt int64  `json:"createdAt"`
}

func toReleaseView(s *cloud.Service[state], r Release, active bool) releaseView {
	v := releaseView{
		ReleaseID: r.ID, Slug: r.Slug, Objects: r.Objects, Bytes: r.Bytes,
		Source: r.Source, Active: active, CreatedAt: r.CreatedAt,
	}
	if active {
		v.URL = siteURL(s, r.Org, r.Slug)
	}
	return v
}

type releaseReq struct {
	Source string `json:"source"`
}

// releaseSite resolves the target site for a release route: the tenant from the
// validated principal, then the project keyed by (org, slug).
//
// Cross-tenant reach dies here, once, for every release route. The org is never
// read from the request, and the project lookup is keyed by it — so a foreign
// slug misses exactly like a nonexistent one and both render the same 404. There
// is no branch that can tell a caller "this site exists, but not for you".
//
// A reserved label is refused with that same 404 before any lookup. It is
// belt-and-braces (createProject already refuses to mint a reserved slug, so no
// row can exist) but it means the guarantee does not rest on create-time history.
func releaseSite(s *cloud.Service[state], c *zip.Ctx) (string, Project, error) {
	org, ok := org(c)
	if !ok {
		return "", Project{}, zip.ErrForbidden("X-Org-Id required")
	}
	slug := slugParam(c)
	if slug == "" || !slugRE.MatchString(slug) || sites.IsReserved(slug) {
		return "", Project{}, zip.ErrNotFound("site not found")
	}
	p, err := s.State.store.GetProject(c.Context(), org, slug)
	if errors.Is(err, errNotFound) {
		return "", Project{}, zip.ErrNotFound("site not found")
	}
	if err != nil {
		return "", Project{}, zip.Errorf(http.StatusInternalServerError, "get: %v", err)
	}
	return org, p, nil
}

// releaseErr maps a promote failure to its honest status. Guard rejections are
// the caller's fault (400/413), a moved source is a conflict (409), and anything
// else is ours (502 — the store failed, not the request).
func releaseErr(err error) error {
	switch {
	case errors.Is(err, errBadSource), errors.Is(err, errEmptySource),
		errors.Is(err, errUnsafeKey), errors.Is(err, errNoIndex):
		return zip.ErrBadRequest(strings.TrimPrefix(err.Error(), "projects: "))
	case errors.Is(err, errTooManyFiles), errors.Is(err, errTooLarge):
		return zip.Errorf(http.StatusRequestEntityTooLarge, "%s", strings.TrimPrefix(err.Error(), "projects: "))
	case errors.Is(err, errSourceChanged):
		return zip.ErrConflict(strings.TrimPrefix(err.Error(), "projects: "))
	default:
		return zip.Errorf(http.StatusBadGateway, "publish failed: %v", err)
	}
}

// createRelease promotes a build output into a new immutable release WITHOUT
// serving it. This is the staged half of publishing: create now, activate after
// whatever check you want to run against the release first.
func createRelease(s *cloud.Service[state], c *zip.Ctx) error {
	org, p, err := releaseSite(s, c)
	if err != nil {
		return err
	}
	if !s.State.blob.configured() {
		return zip.Errorf(http.StatusServiceUnavailable, "object storage not configured (set S3_ADMIN_*)")
	}
	var body releaseReq
	if err := c.Bind(&body); err != nil {
		return err
	}
	// Same fail-closed hosting gate as a deploy, BEFORE any copy: promoting bytes
	// is the billable work, so this path can never become a way to deploy for free.
	fee, gErr := gateHosting(s, c)
	if gErr != nil {
		return cloud.DenyResource(c, gErr)
	}
	r, err := promote(s, c.Context(), org, p, body.Source)
	if err != nil {
		return releaseErr(err)
	}
	meterDeploy(s, c, fee)
	return c.JSON(http.StatusCreated, toReleaseView(s, r, p.CurrentRelease == r.ID))
}

// activateRelease flips the site's pointer to an existing release — the go-live,
// and equally the ROLLBACK (aim it at an older release; releases are immutable
// and retained, so nothing is rebuilt or re-copied). Not billed: no new content
// is produced, only a pointer moved.
func activateRelease(s *cloud.Service[state], c *zip.Ctx) error {
	org, p, err := releaseSite(s, c)
	if err != nil {
		return err
	}
	id := strings.TrimSpace(c.Param("release"))
	if !releaseIDRE.MatchString(id) {
		return zip.ErrNotFound("release not found")
	}
	if err := activate(s, c.Context(), org, p, id); err != nil {
		if errors.Is(err, errNotFound) {
			return zip.ErrNotFound("release not found")
		}
		return zip.Errorf(http.StatusInternalServerError, "activate: %v", err)
	}
	r, err := s.State.store.GetRelease(c.Context(), org, p.Slug, id)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "get release: %v", err)
	}
	return c.JSON(http.StatusOK, toReleaseView(s, r, true))
}

// publishSiteRelease is create+activate: the 99% path, one call. It is exactly
// the two halves in sequence with no extra semantics, so the staged flow and the
// one-shot flow can never drift apart.
func publishSiteRelease(s *cloud.Service[state], c *zip.Ctx) error {
	org, p, err := releaseSite(s, c)
	if err != nil {
		return err
	}
	if !s.State.blob.configured() {
		return zip.Errorf(http.StatusServiceUnavailable, "object storage not configured (set S3_ADMIN_*)")
	}
	var body releaseReq
	if err := c.Bind(&body); err != nil {
		return err
	}
	fee, gErr := gateHosting(s, c)
	if gErr != nil {
		return cloud.DenyResource(c, gErr)
	}
	r, err := promote(s, c.Context(), org, p, body.Source)
	if err != nil {
		return releaseErr(err)
	}
	if err := activate(s, c.Context(), org, p, r.ID); err != nil {
		return zip.Errorf(http.StatusInternalServerError, "activate: %v", err)
	}
	meterDeploy(s, c, fee)
	return c.JSON(http.StatusOK, toReleaseView(s, r, true))
}

// listReleases returns a site's releases newest-first, marking the active one —
// the rollback menu.
func listReleases(s *cloud.Service[state], c *zip.Ctx) error {
	org, p, err := releaseSite(s, c)
	if err != nil {
		return err
	}
	rows, err := s.State.store.ListReleases(c.Context(), org, p.Slug, maxReleaseList)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "list releases: %v", err)
	}
	out := make([]releaseView, 0, len(rows))
	for _, r := range rows {
		out = append(out, toReleaseView(s, r, r.ID == p.CurrentRelease))
	}
	return c.JSON(http.StatusOK, out)
}
