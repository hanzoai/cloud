package account

// The signed-in user's profile photo.
//
// There was no way to set one. IAM carries an `avatar` on every user row and the
// console renders it, but the only writers were FEDERATION (a GitHub avatar_url, an
// OIDC `picture` claim) and SCIM — so a user who signed up with a password had a
// monogram and no way to replace it, and the console's Profile card answered the
// attempt with "Edit in IAM", which links to an IAM that cannot do it either.
// Production agreed: the address was a 404 while the key surface beside it was a 403.
//
// STORAGE IS deps.VFS — the existing S3 client (SeaweedFS via clients/s3vfs), which
// was chosen for exactly this: "an adapter+crypto is needless complexity for small
// avatars". No new store, no second blob path.
//
// CONTENT-ADDRESSED. The key ends in the sha256 of the bytes, so a photo has ONE
// address that never means anything else. That is what makes the read cacheable
// forever and what makes replacing a photo a new URL rather than a stale one every
// cache in the path still believes — the bug you cannot fix from the server if the
// address is a mutable "…/me.png".
//
// A REPLACED PHOTO IS NOT DELETED. The old key is left behind deliberately: the
// previous URL is already inside issued tokens and rendered pages, and an object
// store costs bytes where a broken face costs a person their profile. Orphans are
// a GC concern, not a correctness one.
//
// THE READ IS UNAUTHENTICATED, AND MUST BE. The URL's whole job is to be an
// <img src> from console.hanzo.ai — a different origin from api.hanzo.ai, which
// sends no cookies and cannot carry an Authorization header. So the address IS the
// capability: 64 hex of sha256 that a caller can only produce by already holding
// the image. This is what every avatar system does, and it is the honest reason,
// not an oversight. What it is NOT is a way to read anything else: the digest is
// verified to be a digest, the org and user are refused unless they are plain
// identifiers, and the response is served only if the STORED BYTES are one of four
// raster formats — so a key cannot address another subsystem's blob and a stored
// object cannot be talked into executing in this origin.

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/hanzoai/cloud/internal/magic"
	"github.com/hanzoai/cloud/openapi"
	"github.com/hanzoai/cloud/types"
	"github.com/zap-proto/zip"
)

// maxAvatarSize caps one upload. A profile photo is small by nature; this is
// generous enough for a phone camera original and tight enough that the route
// cannot be used as free object storage. The console downscales before sending,
// so this is the backstop, not the working limit.
const maxAvatarSize = 8 << 20

// avatarPrefix is this subsystem's box in the shared blob bucket. deps.VFS is ONE
// bucket keyed by whatever the consumer supplies (clients/s3vfs), so the prefix is
// what keeps account's objects from colliding with team's.
const avatarPrefix = "account/avatars/"

// registerAvatar wires the two routes. They are the only UNTYPED operations in this
// package and cannot be otherwise: the request is a multipart form and the response
// is raw image bytes under a byte-derived Content-Type — neither is a shape a typed
// In/Out can carry (see typed_wire_test.go, which holds that as a closed list).
//
// The write takes the same gates as the other writes here — requireCSRF, because
// the console authenticates with an ambient cookie, and the rate limiter, because
// this one lands bytes in an object store.
func registerAvatar(o ops, open zip.Router, limit, csrf zip.Middleware) {
	open.Post("/avatar", limit(csrf(o.putAvatar)))
	// The read is deliberately on `open` with no gate: see the file header.
	open.Get("/avatar/:org/:user/:digest", o.getAvatar)
}

func init() {
	openapi.Describe(prefix+"/avatar", http.MethodPost,
		"Set your profile photo",
		"Stores one image as the signed-in user's profile photo and answers the URL it is "+
			"served from, which is also written to the user's IAM record — so every surface "+
			"that already renders `avatar` picks it up with no further call.\n\n"+
			"The body is a multipart form with a `file` part. The format is decided by the "+
			"BYTES, never the filename or the part's Content-Type: png, jpeg, gif and webp are "+
			"accepted and everything else is refused with 415, so an SVG cannot be stored as a "+
			"picture and later served as a program. Over 8 MiB is 413; empty is 400.\n\n"+
			"The photo is addressed by the sha256 of its bytes, so setting a new one yields a "+
			"new URL rather than a stale cache of the old face. The caller is taken from the "+
			"validated identity ONLY — there is no way to name a different subject — so this "+
			"always sets your own photo, and a caller with no organization yet is refused.")
	openapi.Describe(prefix+"/avatar/:org/:user/:digest", http.MethodGet,
		"Fetch a profile photo",
		"Streams a profile photo's raw BYTES. This is the address stored on the user's IAM "+
			"record and rendered directly by an `<img>`, so it takes no credentials — the "+
			"64-hex content digest in the path is the capability, and it can only be produced "+
			"by someone who already has the image.\n\n"+
			"The Content-Type is derived from the stored bytes and the response carries "+
			"nosniff, so only a real raster image is ever served and only under its true type. "+
			"Anything else — a miss, a malformed path, an object that is not an image — is one "+
			"404, and a hit caches for a year because the address is the content.")
}

// avatarKey is the physical blob address: org and user come from the VALIDATED
// identity (never a request value), and the digest is computed here, so every
// component is server-chosen.
func avatarKey(org, user, digest string) string {
	return avatarPrefix + org + "/" + user + "/" + digest
}

// safe reports whether a path component may be used verbatim in a blob key.
//
// It REFUSES rather than sanitizes, and that distinction is the tenancy boundary.
// The sanitizing form of this function (apps/team's seg) folds — "a/b" and "a_b"
// both become "a_b" — and a fold in a key is two tenants sharing one address. A
// refusal cannot collide. These values come from validated IAM claims, so a
// rejection means something upstream is wrong and failing closed is the answer.
func safe(s string) bool {
	if s == "" || s == "." || s == ".." || len(s) > 128 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
		default:
			return false
		}
	}
	return true
}

// digest reports whether s is exactly a sha256 in lowercase hex. The read path
// checks this before touching the store so a caller cannot use the digest segment
// to address something that is not an avatar.
func digest(s string) bool {
	if len(s) != sha256.Size*2 {
		return false
	}
	for _, r := range s {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}

// putAvatar stores the upload and records its URL on the caller's IAM user row.
func (o ops) putAvatar(c *zip.Ctx) error {
	cr, ok := resolveCaller(c, true) // requireOwner: the key is org-scoped
	if !ok {
		return zip.ErrUnauthorized("sign in to set a profile photo")
	}
	if o.s.State.vfs == nil {
		return zip.Errorf(http.StatusNotImplemented, "photo storage is not configured on this deployment")
	}
	if !safe(cr.owner) || !safe(cr.name) {
		// Validated claims that cannot address a blob. Fail closed rather than fold
		// two identities onto one key.
		return zip.Errorf(http.StatusUnprocessableEntity, "this account's identity cannot address a photo")
	}

	fh, err := c.Fiber().FormFile("file")
	if err != nil || fh == nil {
		return zip.ErrBadRequest(`multipart field "file" required`)
	}
	if fh.Size > maxAvatarSize {
		return zip.Errorf(http.StatusRequestEntityTooLarge, "photo too large (max %d bytes)", maxAvatarSize)
	}
	f, err := fh.Open()
	if err != nil {
		return zip.ErrBadRequest("cannot read upload")
	}
	defer func() { _ = f.Close() }()
	data := make([]byte, 0, fh.Size)
	buf := make([]byte, 32<<10)
	for len(data) <= maxAvatarSize {
		n, rerr := f.Read(buf)
		data = append(data, buf[:n]...)
		if rerr != nil {
			break
		}
	}
	if len(data) == 0 {
		return zip.ErrBadRequest("empty upload")
	}
	if len(data) > maxAvatarSize {
		return zip.Errorf(http.StatusRequestEntityTooLarge, "photo too large (max %d bytes)", maxAvatarSize)
	}

	// The format is decided by the BYTES. A name and a part Content-Type are the
	// client's to choose, so neither may decide what this origin later serves.
	// A photo is a PICTURE: magic names other things it can serve safely, and this
	// route wants none of them.
	kind := magic.Type(data)
	if !strings.HasPrefix(kind, "image/") {
		return zip.Errorf(http.StatusUnsupportedMediaType,
			"a profile photo must be a PNG, JPEG, GIF or WebP image")
	}

	sum := sha256.Sum256(data)
	dg := hex.EncodeToString(sum[:])
	key := avatarKey(cr.owner, cr.name, dg)
	if err := o.s.State.vfs.Put(c.Context(), key, data); err != nil {
		// deps.VFS is the fail-closed stub unless an object store is wired: an honest
		// 502, never a success we did not perform.
		o.s.Log.Error("avatar: blob store write failed", "key", key, "err", err)
		return zip.Errorf(http.StatusBadGateway, "photo storage unavailable")
	}

	url := o.avatarURL(c, cr.owner, cr.name, dg)
	// IAM is the system of record for `avatar` — every surface already reads it from
	// there, so writing it here is what makes the photo appear everywhere instead of
	// only in whatever called this.
	//
	// keyID(), not id: IAM's user ops parse `<owner>/<name>` through
	// GetOwnerAndNameFromId, and on the direct-Bearer path X-User-Id is a UUID, so
	// `<owner>/<uuid>` is not a user IAM can find. Measured in production —
	// `iam non-envelope response (400)` for id hanzo/2d4d67ab-…, the photo stored
	// and the profile not updated. keyID() is the same composite the key ops
	// already use for the same reason; on the gateway path the two are identical.
	if err := o.s.State.iam.setAvatar(c.Context(), cr.keyID(), url); err != nil {
		switch {
		case errors.Is(err, errNotConfigured):
			return zip.Errorf(http.StatusNotImplemented, "identity service is not configured on this deployment")
		case errors.Is(err, errNotFound):
			return zip.ErrNotFound("no such user")
		}
		o.s.Log.Error("avatar: iam update failed", "id", cr.id, "err", err)
		// The bytes landed but the record did not, so the photo is stored and not
		// shown. Say that, rather than reporting a success the user cannot see.
		return zip.Errorf(http.StatusBadGateway, "photo stored but the profile could not be updated; try again")
	}
	return c.JSON(http.StatusOK, map[string]string{"avatar": url})
}

// avatarURL builds the absolute address the photo is served from. It must be
// absolute: it is written into IAM and rendered by an <img> on OTHER origins
// (console.hanzo.ai), where a relative path would resolve against the wrong host.
// Domain is the deployment's own public API host (CLOUD_DOMAIN, api.hanzo.ai),
// falling back to the request's host so a non-default deployment still answers with
// itself rather than with production.
func (o ops) avatarURL(c *zip.Ctx, org, user, dg string) string {
	host := strings.TrimSpace(o.s.Domain)
	if host == "" {
		host = strings.TrimSpace(c.Host())
	}
	scheme := "https://"
	if strings.HasPrefix(host, "localhost") || strings.HasPrefix(host, "127.0.0.1") {
		scheme = "http://"
	}
	return scheme + host + prefix + "/avatar/" + org + "/" + user + "/" + dg
}

// getAvatar streams a stored photo. No credentials — see the file header.
func (o ops) getAvatar(c *zip.Ctx) error {
	org, user, dg := c.Param("org"), c.Param("user"), c.Param("digest")
	// Every denial below is the SAME 404: a malformed path, a miss and a key that
	// belongs to nothing all reveal exactly nothing about what exists.
	if !safe(org) || !safe(user) || !digest(dg) {
		return zip.ErrNotFound("no such photo")
	}
	if o.s.State.vfs == nil {
		return zip.ErrNotFound("no such photo")
	}
	data, err := o.s.State.vfs.Get(c.Context(), avatarKey(org, user, dg))
	switch {
	case errors.Is(err, types.ErrBlobNotFound), err == nil && data == nil:
		return zip.ErrNotFound("no such photo")
	case err != nil:
		// Backend unavailable → fail closed with 502, never an empty 200 a browser
		// would cache as "this user has no face".
		return zip.Errorf(http.StatusBadGateway, "photo storage unavailable")
	}
	// Defense in depth: the upload already refused anything that is not a raster
	// image, so this can only fire on an object written by some other path. Serving
	// it inline under a guessed type is the XSS the allow-list exists to prevent.
	kind := magic.Type(data)
	if !strings.HasPrefix(kind, "image/") {
		return zip.ErrNotFound("no such photo")
	}
	c.SetHeader("Content-Type", kind)
	c.SetHeader("X-Content-Type-Options", "nosniff")
	// The address IS the content, so it can never go stale. `public` because the
	// route takes no credentials — a shared cache holds nothing private that the
	// URL itself did not already grant.
	c.SetHeader("Cache-Control", "public, max-age=31536000, immutable")
	return c.Bytes(http.StatusOK, data)
}

// avatarFor is the URL a stored digest is served from, used by tests and by any
// caller that needs to name a photo it did not just upload.
func avatarFor(domain, org, user, dg string) string {
	return fmt.Sprintf("https://%s%s/avatar/%s/%s/%s", domain, prefix, org, user, dg)
}
