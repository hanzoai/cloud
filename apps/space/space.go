// Copyright (c) 2026 Hanzo AI Inc.

// Package space is where work lives: drives, folders and the files in them.
//
// A SPACE is a namespace owned by an org. It answers "where", while IAM answers
// "who" — org, team, user, agent. Those are separate axes, which is the whole
// reason a space earns its own name: a team works across spaces and a space
// hosts several teams. Billing hangs off the ORG, so a space never fragments an
// invoice; HR and Finance can each have one and still roll up to a single lens.
//
// A DRIVE is a named place inside a space. A FOLDER is emergent from "/" in a
// file's name — there is no folders table, because a second naming system beside
// the keys drifts from them the first time somebody renames one.
//
// # A drive is a prefix, not a bucket
//
// ListBuckets is an unpaginated GET over the WHOLE deployment; apps/s3 filters
// it client-side and says so: the listing is not paged "because an org's bucket
// count is small by construction". One bucket per drive makes that count
// orgs x spaces x drives, and then every tenant's drive list pays for every
// other tenant's drives. One bucket per (org, space) keeps the assumption true,
// and it is the shape every high-cardinality store here already uses —
// team-blobs, org-db, hanzo-sites, recordings.
//
// It also collapses two mechanisms into one: listing the drives IS listing the
// root folder, because a drive is just the first key segment.
//
// # Bytes never pass through this process
//
// Upload and download mint a presigned URL against the public endpoint and the
// caller talks to the store directly. That is not a preference, it is how
// apps/s3 already works, and it keeps file content and store credentials off the
// API entirely.
package space

import (
	"fmt"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/fare"
	"github.com/hanzoai/cloud/apps/s3admin"
)

// presignTTL bounds every presigned upload and download URL. Five minutes: a
// minted capability has no server-side revocation, so the TTL IS the revocation
// window, and it is still long enough for a browser to finish a normal PUT or
// GET. A caller who needs a fresh window re-mints.
const presignTTL = 5 * time.Minute

// maxListKeys caps one listing page so a drive holding millions of files cannot
// exhaust memory or the response. Folder-style navigation reads one level at a
// time, so this is generous.
const maxListKeys = 1000

// feeEnv is the operator knob for the per-operation fee. The effective charge is
// cloud.ResourceFeeCents(feeEnv, "op"): the CLOUD_SPACE_FEE_CENTS override, else
// the platform default. Set it to 0 to make the surface free and therefore
// un-gated.
const feeEnv = "CLOUD_SPACE_FEE_CENTS"

// nameRE is the shape of a SPACE name and of a DRIVE name alike, and one rule
// for both is deliberate rather than lazy. A space name becomes an S3 bucket
// name, so it can be no looser than this; a drive name becomes the FIRST KEY
// SEGMENT, so a name carrying "/" would silently become two drives and a name
// with a trailing separator would collide with the marker that makes the drive
// exist. One shape, so a caller learns it once and neither addressing rule can
// drift from the other.
var nameRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,38}[a-z0-9])?$`)

// state is what one mounted instance holds: the shared object-store connection
// and nothing else. The per-org gate and meter are Base.Bill, whose commerce
// product label is this subsystem's name.
type state struct {
	admin s3admin.Admin
}

// Ready is nil when the object store is configured, and the honest refusal
// otherwise. It is what lets the whole route set mount unconditionally and still
// fail closed under its own name.
func (st state) Ready() error {
	if !st.admin.Configured() {
		return zip.Errorf(http.StatusServiceUnavailable, "object storage is not configured")
	}
	return nil
}

// Fee is what one operation on this surface costs, from operator config. Read per
// call rather than once at mount, so the knob takes effect without a restart.
func (st state) Fee() (string, int64) { return "op", cloud.ResourceFeeCents(feeEnv, "op") }

// Use mounts the space subsystem.
//
// `Use`, not `Mount`: the tree renamed the entry point and the plugin mains have
// not caught up yet.
func Use(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("space.Use: nil app")
	}
	s := &cloud.Service[state]{Base: cloud.NewBase(deps, "space"), State: state{admin: s3admin.New()}}
	if !s.State.admin.Configured() {
		// Not fatal. The store is reachable through env this deployment may not
		// set, and refusing to mount would take the routes down rather than let
		// them answer that storage is unavailable.
		s.Log.Warn("object store is not configured; drives will answer unavailable")
	}

	// Routes go on the concrete app so zip's typed registrars and cmd/zipdoc can
	// both resolve the prefix; the scoped Router still owns any middleware, which
	// is where the ownership guard applies.
	//
	// The FULL surface registers unconditionally, even with no credentials: the
	// preamble fails each operation closed with 503, so this subsystem always OWNS
	// its route space. Mounted only when configured, an unconfigured deployment
	// would leak /v1/space/* to whatever answers the /v1 remainder — a 404 from a
	// different subsystem instead of the honest 503 that says storage is down.
	sp := cloud.ZipApp(app).Group("/v1/space")
	o := ops{s: s}

	// Bridge is this subsystem's OWN, on its own group and ahead of every leaf: a
	// typed op is handed a context, and the request its preamble reads is what this
	// parks there. Serve installs one app-wide, which is what carries the arms that
	// never touch a route — MCP, the call plane — and no package's test harness runs
	// Serve, so an app that does not install its own resolves no request on its own
	// routes and 403s in tests alone. It gates nothing, so /health below stays
	// ungated.
	sp.Use(cloud.Bridge())

	// The probe is NOT wrapped — liveness has to be probe-able without a token —
	// and it declares BOTH of its statuses, so the answer says which one it is.
	zip.Get(sp, "/health", o.health, zip.WithStatus(http.StatusOK, http.StatusServiceUnavailable))

	// Everything below is registered through fare.Paid, which is what admits the
	// caller, resolves the org and takes the fee — and the org it resolved is the
	// ONLY one an operation can reach, so an op registered without it can name no
	// org and therefore touch nothing.
	zip.Get(sp, "/spaces", fare.Paid(s, o.listSpaces))
	zip.Post(sp, "/spaces", fare.Paid(s, o.createSpace), zip.WithStatus(http.StatusCreated))

	zip.Get(sp, "/:space/drives", fare.Paid(s, o.listDrives))
	zip.Post(sp, "/:space/drives", fare.Paid(s, o.createDrive), zip.WithStatus(http.StatusCreated))
	zip.Delete(sp, "/:space/drives/:drive", fare.Paid(s, o.deleteDrive))

	// Listing is its own address. Overloading one route with "list a folder OR
	// fetch a file" leaves the caller unable to tell which it is getting before
	// it renders, and the two answers have nothing in common.
	zip.Get(sp, "/:space/drives/:drive/files", fare.Paid(s, o.listFiles))

	// `+` and never `*`. Fiber's greedy wildcard matches the EMPTY remainder and
	// beats an exact sibling in any registration order, which is what once made
	// apps/s3's listing unreachable by every spelling. `+` demands at least one
	// character, so the sibling above keeps its own address.
	zip.Get(sp, "/:space/drives/:drive/files/+", fare.Paid(s, o.readFile))
	zip.Put(sp, "/:space/drives/:drive/files/+", fare.Paid(s, o.writeFile))
	zip.Delete(sp, "/:space/drives/:drive/files/+", fare.Paid(s, o.deleteFile))

	s.Log.Info("space mounted",
		"prefix", "/v1/space",
		"presign", s.State.admin.PresignConfigured(),
		"brand", deps.Brand,
		"env", deps.Env,
	)
	return nil
}

// ── the two derivations ─────────────────────────────────────────────────────

// bucket is the ONE bucket a space's files live in. It is derived from the
// caller's VALIDATED org, so no request field can redirect it, and it is
// s3admin's derivation rather than a second one — apps/provisioning allocates
// with the same rule, so a bucket made here is findable there and vice versa.
func bucket(org, space string) string { return s3admin.BucketName(org, space) }

// spacePrefix is the string every one of an org's buckets begins with, which is
// what makes the deployment-wide bucket listing filterable back to one org.
func spacePrefix(org string) string { return s3admin.BucketPrefix(org) }

// spaceOf recovers the space name from a physical bucket owned by org, or
// ("",false) when the bucket is NOT in that org's namespace — so listing skips
// every other org's buckets rather than refusing them, which would turn the
// listing into an existence oracle. The recovered name is RE-VALIDATED: a bucket
// carrying the org prefix but a name this API could never have created (a manual
// `mc mb`, some future admin tool) is treated as not-ours rather than echoed back
// as a space a later request cannot address.
func spaceOf(org, physical string) (string, bool) {
	pfx := spacePrefix(org)
	if !strings.HasPrefix(physical, pfx) {
		return "", false
	}
	name := strings.TrimPrefix(physical, pfx)
	if !nameRE.MatchString(name) {
		return "", false
	}
	return name, true
}

// driveKey is a drive's whole existence: its name plus one separator, which is
// both the key prefix its files live under and the key of the zero-byte marker
// that makes an empty drive visible. ONE function, because a drive whose marker
// and whose prefix disagreed would be a drive you could create and never list.
func driveKey(drive string) string { return drive + "/" }

// ── decoding and validation ─────────────────────────────────────────────────

// decode is THE ONE place a path capture is percent-decoded, and every capture
// this subsystem reads goes through it.
//
// The router hands a captured segment over exactly as it arrived — measured, it
// decodes nothing, not %2F and not %20 — while every client generated from this
// API percent-encodes a path parameter (Go url.PathEscape, Python quote(safe=""),
// JS encodeURIComponent all render "2019/summer/a.jpg" as
// "2019%2Fsummer%2Fa.jpg"). Undecoded, two spellings of one file address two
// different keys and BOTH answer success: a delete sent by any SDK removes a key
// nobody stored and reports 204 while the file it named survives.
// apps/framework/spacepath_test.go is the same trap caught one subsystem over,
// where a DocType named "Sales Invoice" was created and then unreachable.
//
// ok is false for a malformed escape ("%zz"), which is a 400 rather than a name
// carrying a stray percent — the caller wrote an address that does not decode.
func decode(raw string) (string, bool) {
	dec, err := url.PathUnescape(strings.TrimSpace(raw))
	if err != nil {
		return "", false
	}
	return dec, true
}

// named decodes a :space or :drive capture and validates it AS GIVEN. It is not
// lower-cased for you: a caller that creates "Photos" and then lists "photos"
// would be reading a drive it did not make, so mixed case is a clean 400.
func named(raw string) (string, bool) {
	dec, ok := decode(raw)
	if !ok || !nameRE.MatchString(dec) {
		return "", false
	}
	return dec, true
}

// remainder is the trailing path of a /files/+ address as a file name: decoded,
// then with a leading separator taken off, so a doubled separator in the URL
// (…/files//a.txt) addresses the same file one separator does. The trim runs
// here and NOT inside cleanFile, because a leading separator arriving any other
// way is a caller writing an absolute name, which is a 400.
func remainder(raw string) (string, bool) {
	dec, ok := decode(raw)
	if !ok {
		return "", false
	}
	return strings.TrimPrefix(dec, "/"), true
}

// cleanFile normalises a file name within its drive and rejects any traversal or
// unsafe byte. A name MAY contain "/" — that is what makes a folder — but must
// not be absolute, empty, a bare folder marker, or escape via "..". It must carry
// no control byte and no backslash: neither appears in a legitimate name from a
// browser, a null byte serialises as %00 into the presigned URL and could
// C-string-truncate a downstream consumer, and '\' is a separator a non-Go
// backend might split on.
func cleanFile(raw string) (string, bool) {
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
	return clean, true
}

// cleanFolder normalises a listing folder. A folder MAY be empty (the drive
// root) and MAY end with "/". An unsafe one is coerced to the drive root rather
// than refused — listing a bad folder lists the root, never another location.
func cleanFolder(raw string) string {
	p := strings.TrimSpace(raw)
	if p == "" {
		return ""
	}
	if strings.HasPrefix(p, "/") || strings.Contains(p, "..") {
		return ""
	}
	if !strings.HasSuffix(p, "/") {
		p += "/"
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
