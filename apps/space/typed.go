package space

// typed.go is this surface's typed half — one registry entry per operation,
// which is what the OpenAPI operation's schema, the MCP tool, the CLI command and
// every generated SDK method are all projected from. All ten ops are here.
//
// THE SCHEMA NAMESPACE IS FLAT AND FLEET-WIDE. A typed op's Go type name IS its
// schema name across the whole document and openapi.Compose refuses one name
// meaning two things, so the product noun is spelled into every type here:
// `Space`, `Drive` and `File` are the words a customer reads and are exactly the
// generic nouns another app will want. `fileIn` is not used for the same reason
// one level down — apps/code already declares one.

import (
	"context"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	s3 "github.com/hanzos3/go"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/internal/fare"
	"github.com/hanzoai/cloud/s3admin"
	"github.com/zap-proto/zip"
)

// zipdoc lifts the doc comment off each typed op and off every In/Out field into
// zipdoc_gen.go, which is the ONLY way that prose reaches the published document
// and the MCP tool list — Go drops comments at compile time.
//
//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

// ops is the typed-op receiver. A TypedHandler takes no service parameter, and a
// bound METHOD is the only form cmd/zipdoc can lift prose from — a closure
// returned by a factory is a call expression with no doc comment to read.
type ops struct{ s *cloud.Service[state] }

// client is the admin object-store client, or the honest 503 every op answers
// without one.
func (o ops) client() (*s3.Client, error) {
	cli, err := o.s.State.admin.Client()
	if err != nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "object storage unavailable")
	}
	return cli, nil
}

// ── health ──────────────────────────────────────────────────────────────────

// spaceHealth is the probe's ONE shape, answered under both of its statuses.
//
// QUALIFIED, because a typed op's Go type name IS its schema name across the whole
// fleet: "healthReport" is already apps/event's and "s3Health" apps/s3's, each
// with a different shape.
type spaceHealth struct {
	// Service names the subsystem this probe is for. Always "space".
	Service string `json:"service"`
	// Status is "ok" when the store is reachable in principle, "degraded" when it
	// is not. It is the field to read; the HTTP status carries the same fact for a
	// caller that only looks at the code.
	Status string `json:"status"`
	// Ready is whether this deployment can serve drive and file operations at all:
	// true only when object-store credentials are configured.
	Ready bool `json:"ready"`
	// Presign is whether upload and download URLs can be minted, which needs a
	// PUBLIC endpoint on top of the credentials. False does not make the surface
	// degraded — listing spaces, drives and folders still works, only the bytes
	// cannot be reached.
	Presign bool `json:"presign"`
	// Error is why the probe is degraded, in plain words. Absent when it is not.
	Error string `json:"error,omitempty"`
}

// StatusCode makes the ANSWER say which of the declared statuses it is: a
// degraded probe is 503 so an orchestrator reading only the code is told the
// truth, and the body carries the same fact for one that reads further.
func (h *spaceHealth) StatusCode() int {
	if h.Status != "ok" {
		return http.StatusServiceUnavailable
	}
	return http.StatusOK
}

// Health reports whether this deployment can serve spaces, drives and files.
//
// It is a REAL probe rather than a constant: 200 when object-store credentials
// are present, so the store is reachable in principle, and 503 with the reason
// when they are not. It is deliberately NOT gated — liveness has to be probe-able
// without a token — so it is the one operation here that names no space and bills
// nothing.
func (o ops) health(_ context.Context, _ *cloud.Unit) (*spaceHealth, error) {
	r := &spaceHealth{Service: "space", Status: "ok"}
	if !o.s.State.admin.Configured() {
		r.Status, r.Ready = "degraded", false
		r.Error = "S3_ADMIN credentials not configured"
		return r, nil
	}
	r.Ready = true
	r.Presign = o.s.State.admin.PresignConfigured()
	return r, nil
}

// ── spaces ──────────────────────────────────────────────────────────────────

// spaceItem is one space of the caller's org.
type spaceItem struct {
	// Name is the space's name, as the org created it.
	Name string `json:"name"`
	// CreatedAt is when the space was made, in unix seconds.
	CreatedAt int64 `json:"createdAt"`
}

// spaceList is the caller org's own spaces. Never another org's: a space is
// physically named under a per-org prefix, and one that does not carry the
// caller's is not merely filtered out of this list — it is invisible to every
// operation here.
type spaceList struct {
	// Spaces are the caller org's spaces, oldest first as the store returns them.
	Spaces []spaceItem `json:"spaces"`
	// Total is how many spaces this org has. It equals len(spaces): the listing is
	// not paged, because one bucket per (org, space) keeps an org's count small by
	// construction, which is the whole reason a drive is a prefix and not a bucket.
	Total int `json:"total"`
}

// ListSpaces lists the caller org's own spaces.
//
// Only the caller's: every space is physically named under a per-org prefix and
// the listing strips that prefix, so another org's spaces are not in the answer at
// all. Another org's space is not refused but INVISIBLE, so this cannot be used to
// learn that a name is taken elsewhere.
//
// Billed per call: the balance is checked BEFORE anything is touched, so an
// unfunded org is refused with nothing done, and the debit lands only once the
// work has succeeded.
func (o ops) listSpaces(ctx context.Context, _ *cloud.Unit) (*spaceList, error) {
	org, err := fare.Org(ctx)
	if err != nil {
		return nil, err
	}
	cli, err := o.client()
	if err != nil {
		return nil, err
	}
	all, err := cli.ListBuckets(ctx)
	if err != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "list spaces: %v", err)
	}
	out := make([]spaceItem, 0, len(all))
	for _, b := range all {
		name, ok := spaceOf(org, b.Name)
		if !ok {
			continue // another org's bucket — invisible
		}
		out = append(out, spaceItem{Name: name, CreatedAt: b.CreationDate.Unix()})
	}
	return &spaceList{Spaces: out, Total: len(out)}, nil
}

// spaceIn names a space to create.
type spaceIn struct {
	// Name is the space's name, matching ^[a-z0-9]([a-z0-9-]{0,38}[a-z0-9])?$ — the
	// shape a drive name takes too, so a caller learns one rule. It is validated AS
	// GIVEN and never lower-cased for you: a client that creates "Photos" and then
	// lists "photos" would be reading a space it did not make, so mixed case is a
	// clean 400.
	Name string `json:"name" url:"-"`
}

// CreateSpace makes a new space for the caller's org and answers 201 with it.
//
// The one bucket a space's files live in is derived from the caller's VALIDATED
// org, so an org can only ever create inside its own namespace and no request
// field can redirect that. A name already taken in the org is 409.
//
// Billed per call: the balance is checked BEFORE anything is touched, so an
// unfunded org is refused with nothing created, and the debit lands only once the
// space exists.
func (o ops) createSpace(ctx context.Context, in *spaceIn) (*spaceItem, error) {
	org, err := fare.Org(ctx)
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(in.Name)
	if !nameRE.MatchString(name) {
		return nil, zip.ErrBadRequest("name must match ^[a-z0-9]([a-z0-9-]{0,38}[a-z0-9])?$")
	}
	cli, err := o.client()
	if err != nil {
		return nil, err
	}
	b := bucket(org, name)
	exists, err := cli.BucketExists(ctx, b)
	if err != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "space check: %v", err)
	}
	if exists {
		return nil, zip.ErrConflict("space already exists")
	}
	if err := cli.MakeBucket(ctx, b, s3.MakeBucketOptions{Region: o.s.State.admin.Region()}); err != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "create space: %v", err)
	}
	return &spaceItem{Name: name, CreatedAt: time.Now().Unix()}, nil
}

// ── drives ──────────────────────────────────────────────────────────────────

// spaceRef addresses one space. The name is the path segment: the URL is the
// addressing authority.
type spaceRef struct {
	// Space is the space's name, from the path.
	Space string `json:"space"`
}

// driveItem is one drive of a space.
//
// It carries a NAME and nothing else, and that is the honest shape rather than a
// thin one: a drive is the first segment of a key, so the store holds no creation
// time, size or owner for it to report. It is an object rather than a bare string
// so a later fact about a drive is an added field instead of a wire break.
type driveItem struct {
	// Name is the drive's name — the first segment of every key it holds.
	Name string `json:"name"`
}

// driveList is a space's drives, which is the same answer as listing its root
// folder: a drive IS the first key segment, so the two questions have one answer
// and there is no drives table to disagree with the keys.
type driveList struct {
	// Space is the space that was listed.
	Space string `json:"space"`
	// Drives are the drives at the space's root.
	Drives []driveItem `json:"drives"`
	// Total is how many drives came back. The listing is BOUNDED, so it is what
	// came back and not a count of what the space holds.
	Total int `json:"total"`
}

// ListDrives lists a space's drives.
//
// Listing the drives IS listing the space's root folder, because a drive is the
// first segment of a key and nothing else — so the two can never disagree the way
// a drives table and the keys under it would. A space the caller's org does not
// own is the same 404 an unknown name gives.
//
// Billed per call: the balance is checked BEFORE anything is touched, so an
// unfunded org is refused with nothing read, and the debit lands only once the
// listing has succeeded.
func (o ops) listDrives(ctx context.Context, in *spaceRef) (*driveList, error) {
	org, err := fare.Org(ctx)
	if err != nil {
		return nil, err
	}
	space, ok := named(in.Space)
	if !ok {
		return nil, zip.ErrBadRequest("invalid space name")
	}
	cli, err := o.client()
	if err != nil {
		return nil, err
	}
	out := make([]driveItem, 0, 16)
	opts := s3.ListObjectsOptions{Recursive: false, MaxKeys: maxListKeys}
	for obj := range cli.ListObjects(ctx, bucket(org, space), opts) {
		if obj.Err != nil {
			if s3admin.NoSuchBucket(obj.Err) {
				return nil, zip.ErrNotFound("space not found")
			}
			return nil, zip.Errorf(http.StatusBadGateway, "list drives: %v", obj.Err)
		}
		// Only a folder entry is a drive. A bare key at the space root is a file
		// somebody wrote outside every drive, and naming it here would offer an
		// address that lists nothing.
		if !strings.HasSuffix(obj.Key, "/") {
			continue
		}
		name := strings.TrimSuffix(obj.Key, "/")
		if !nameRE.MatchString(name) {
			continue
		}
		out = append(out, driveItem{Name: name})
		if len(out) >= maxListKeys {
			break
		}
	}
	return &driveList{Space: space, Drives: out, Total: len(out)}, nil
}

// driveIn names a drive to create inside a space.
type driveIn struct {
	// Space is the space to create the drive in, from the path. It carries NO
	// `url:"-"`, unlike the field below it, and the difference is the whole reason
	// both tags are written out: zip's binder skips a field tagged "-" for EVERY
	// URL source, path params included, so a path-borne value that carried it
	// would arrive empty and the create would refuse a perfectly good address.
	Space string `json:"space"`
	// Name is the drive's name, matching the same shape a space name does. It
	// becomes the FIRST SEGMENT of every key the drive holds, which is why it may
	// carry no "/".
	Name string `json:"name" url:"-"`
}

// CreateDrive makes a new drive in a space and answers 201 with it.
//
// A drive is a PREFIX and not a bucket, so making one writes a zero-byte marker
// at "<name>/" — which is what makes an empty drive visible to a listing that has
// no other key to find. A name already taken in the space is 409.
//
// Billed per call: the balance is checked BEFORE anything is touched, so an
// unfunded org is refused with nothing created, and the debit lands only once the
// drive exists.
func (o ops) createDrive(ctx context.Context, in *driveIn) (*driveItem, error) {
	org, err := fare.Org(ctx)
	if err != nil {
		return nil, err
	}
	space, ok := named(in.Space)
	if !ok {
		return nil, zip.ErrBadRequest("invalid space name")
	}
	name := strings.TrimSpace(in.Name)
	if !nameRE.MatchString(name) {
		return nil, zip.ErrBadRequest("name must match ^[a-z0-9]([a-z0-9-]{0,38}[a-z0-9])?$")
	}
	cli, err := o.client()
	if err != nil {
		return nil, err
	}
	b := bucket(org, space)
	held, err := o.holds(ctx, cli, b, name, 1)
	if err != nil {
		return nil, err
	}
	if len(held) > 0 {
		return nil, zip.ErrConflict("drive already exists")
	}
	if _, err := cli.PutObject(ctx, b, driveKey(name), strings.NewReader(""), 0, s3.PutObjectOptions{}); err != nil {
		if s3admin.NoSuchBucket(err) {
			return nil, zip.ErrNotFound("space not found")
		}
		return nil, zip.Errorf(http.StatusBadGateway, "create drive: %v", err)
	}
	return &driveItem{Name: name}, nil
}

// driveRef addresses one drive of one space, both from the path.
type driveRef struct {
	// Space is the space's name, from the path.
	Space string `json:"space"`
	// Drive is the drive's name, from the path.
	Drive string `json:"drive"`
}

// DeleteDrive removes an EMPTY drive and answers 204.
//
// A drive holding files is 409 rather than a cascade: deleting an org's files
// behind a single drive call is not a thing this surface will do silently. A
// drive or space the caller's org does not own is the same 404 an unknown name
// gives.
//
// Billed per call: the balance is checked BEFORE anything is touched, so an
// unfunded org is refused with nothing deleted, and the debit lands only once the
// drive is gone.
func (o ops) deleteDrive(ctx context.Context, in *driveRef) (*cloud.Unit, error) {
	org, err := fare.Org(ctx)
	if err != nil {
		return nil, err
	}
	space, ok := named(in.Space)
	if !ok {
		return nil, zip.ErrBadRequest("invalid space name")
	}
	drive, ok := named(in.Drive)
	if !ok {
		return nil, zip.ErrBadRequest("invalid drive name")
	}
	cli, err := o.client()
	if err != nil {
		return nil, err
	}
	b := bucket(org, space)
	// Two keys is all it takes to decide: none means the drive is not there, one
	// that is the marker means it is empty, and anything else means it holds files.
	held, err := o.holds(ctx, cli, b, drive, 2)
	if err != nil {
		return nil, err
	}
	if len(held) == 0 {
		return nil, zip.ErrNotFound("drive not found")
	}
	if len(held) > 1 || held[0] != driveKey(drive) {
		return nil, zip.ErrConflict("drive is not empty")
	}
	if err := cli.RemoveObject(ctx, b, driveKey(drive), s3.RemoveObjectOptions{}); err != nil {
		if s3admin.NoSuchBucket(err) {
			return nil, zip.ErrNotFound("space not found")
		}
		return nil, zip.Errorf(http.StatusBadGateway, "delete drive: %v", err)
	}
	return nil, nil
}

// holds reads up to max keys a drive holds, marker included, and is the ONE
// question "what is under this drive" is asked with — so create's "is it taken"
// and delete's "is it empty" cannot answer differently about one drive.
func (o ops) holds(ctx context.Context, cli *s3.Client, b, drive string, max int) ([]string, error) {
	var keys []string
	opts := s3.ListObjectsOptions{Prefix: driveKey(drive), Recursive: true, MaxKeys: max}
	for obj := range cli.ListObjects(ctx, b, opts) {
		if obj.Err != nil {
			if s3admin.NoSuchBucket(obj.Err) {
				return nil, zip.ErrNotFound("space not found")
			}
			return nil, zip.Errorf(http.StatusBadGateway, "read drive: %v", obj.Err)
		}
		keys = append(keys, obj.Key)
		if len(keys) >= max {
			break
		}
	}
	return keys, nil
}

// ── files ───────────────────────────────────────────────────────────────────

// folderRef addresses one folder level of a drive.
type folderRef struct {
	// Space is the space to list in, from the path.
	Space string `json:"space"`
	// Drive is the drive to list, from the path.
	Drive string `json:"drive"`
	// Folder scopes the listing to a sub-folder of the drive. Names come back
	// RELATIVE to it, so a UI renders a breadcrumb without trimming anything
	// itself. A folder is emergent from "/" in a file's name — there is nothing
	// else to create or delete.
	Folder string `json:"-" url:"folder"`
	// Recursive lists every file flat under the folder instead of one level. It is
	// compared to the literal "true": the folder view is the default and only
	// recursion is opt-in, so any other value lists one level.
	Recursive string `json:"-" url:"recursive"`
}

// fileItem is one entry of a folder listing — a file, or the folder that holds
// more of them.
type fileItem struct {
	// Name is the entry's name RELATIVE to the folder that was listed.
	Name string `json:"name"`
	// Folder is true for a folder entry, which is emergent from "/" in the names
	// beneath it rather than a thing that was created.
	Folder bool `json:"isFolder"`
	// Size is the file's size in bytes, and 0 for a folder.
	Size int64 `json:"size"`
	// ModifiedAt is when the file was last written, in unix seconds, and 0 for a
	// folder.
	ModifiedAt int64 `json:"modifiedAt"`
	// ETag is the store's entity tag for the bytes currently at this name, with the
	// quotes the store wraps it in stripped. It is an opaque VERSION and not a
	// checksum to verify against: a single-part upload's tag happens to be the MD5
	// of the content and a multipart upload's is not, and nothing here says which
	// this was. Compare two reads of one file to learn whether it changed; absent
	// for a folder, and for a file the store reports none for.
	ETag string `json:"etag,omitempty"`
}

// fileList is one folder level of a drive.
type fileList struct {
	// Space is the space that was listed.
	Space string `json:"space"`
	// Drive is the drive that was listed.
	Drive string `json:"drive"`
	// Folder is the sub-folder the listing was scoped to, cleaned. Empty for the
	// drive's own root.
	Folder string `json:"folder"`
	// Files are the entries at this level, names RELATIVE to Folder.
	Files []fileItem `json:"files"`
	// Total is how many entries came back. The listing is BOUNDED, so a drive with
	// more files than the cap answers the cap and this says so — it is not a count
	// of what the drive holds.
	Total int `json:"total"`
}

// ListFiles lists one folder level of a drive.
//
// Folder-style by default: sub-folders come back as folder entries, which is the
// file-manager view. `?recursive=true` lists every file flat under the folder
// instead. Names are RELATIVE to `?folder=`, and the listing is bounded so a huge
// drive cannot exhaust memory — Total is what came back, not what the drive holds.
//
// Billed per call: the balance is checked BEFORE anything is touched, so an
// unfunded org is refused with nothing read, and the debit lands only once the
// listing has succeeded.
func (o ops) listFiles(ctx context.Context, in *folderRef) (*fileList, error) {
	org, err := fare.Org(ctx)
	if err != nil {
		return nil, err
	}
	space, ok := named(in.Space)
	if !ok {
		return nil, zip.ErrBadRequest("invalid space name")
	}
	drive, ok := named(in.Drive)
	if !ok {
		return nil, zip.ErrBadRequest("invalid drive name")
	}
	cli, err := o.client()
	if err != nil {
		return nil, err
	}
	folder := cleanFolder(in.Folder)
	prefix := driveKey(drive) + folder
	out := make([]fileItem, 0, 64)
	opts := s3.ListObjectsOptions{Prefix: prefix, Recursive: in.Recursive == "true", MaxKeys: maxListKeys}
	for obj := range cli.ListObjects(ctx, bucket(org, space), opts) {
		if obj.Err != nil {
			if s3admin.NoSuchBucket(obj.Err) {
				return nil, zip.ErrNotFound("space not found")
			}
			return nil, zip.Errorf(http.StatusBadGateway, "list files: %v", obj.Err)
		}
		rel := strings.TrimPrefix(obj.Key, prefix)
		if rel == "" {
			continue // the folder marker itself, and the drive's own marker
		}
		out = append(out, fileItem{
			Name:       rel,
			Folder:     strings.HasSuffix(obj.Key, "/"),
			Size:       obj.Size,
			ModifiedAt: modTime(obj.LastModified),
			ETag:       strings.Trim(obj.ETag, `"`),
		})
		if len(out) >= maxListKeys {
			break
		}
	}
	return &fileList{Space: space, Drive: drive, Folder: folder, Files: out, Total: len(out)}, nil
}

// fileRef addresses ONE file: a space and a drive by name, and a file name that
// is the whole trailing path.
//
// File rides the route's greedy capture, which fiber names "+1" — the first plus
// parameter, numbered over plus parameters rather than segments, so the two named
// params ahead of it do not shift the number. The two tags answer two questions
// and both are needed. `url:` says where the value comes from over HTTP, and it
// must be fiber's key or the segment binds nothing. `json:` keeps the field's
// ordinary name for a caller that addresses the operation BY NAME — MCP, the call
// plane and the CLI hand their arguments across as one JSON object with no path to
// read, so `json:"-"` would leave them no way to say which file they mean.
//
// The URL WINS over both other sources: zip binds body, then query, then path, so
// a query or body naming a different space, drive or file is overwritten by the
// address that was matched. The operation therefore acts on what the URL named,
// which is also what the preamble admitted and what the ledger is debited for.
type fileRef struct {
	// Space is the space's name, from the path.
	Space string `json:"space"`
	// Drive is the drive's name, from the path.
	Drive string `json:"drive"`
	// File is the file's name within the drive — everything after that drive's
	// /files/. It MAY contain "/", because a folder is emergent from the name and
	// this segment is captured whole: "2019/summer/a.jpg" is one file in two
	// folders, not three names. It is path-cleaned before use, so "../" reaches
	// nothing outside the drive, and a name that is empty, absolute or a bare
	// folder marker is refused 400.
	File string `json:"file" url:"+1"` // "+1" is fiber's key for the route's `+` capture; see space.go.
}

// fileURL is a minted, expiring URL the caller follows to the store DIRECTLY.
type fileURL struct {
	// URL is the presigned URL, signed against the PUBLIC host so a browser can
	// follow it.
	URL string `json:"url"`
	// Method is the verb the URL is signed for — "GET" to read, "PUT" to write.
	Method string `json:"method"`
	// File is the file name the URL was signed for, relative to the drive and
	// path-cleaned — so it is what the store will actually read or write, which is
	// not always the string the caller sent. The signature covers this one space,
	// this one drive and this one file: a URL minted here reaches nothing else.
	File string `json:"file"`
	// Expiry is how many seconds the URL stays valid. A presigned URL has no
	// server-side revocation, so this IS its revocation window.
	Expiry int64 `json:"expiresIn"`
}

// ReadFile mints a URL the caller downloads the file from DIRECTLY.
//
// The bytes never pass through this binary and the store credential never leaves
// the server: the URL is signed against the PUBLIC host, scoped to exactly this
// space, drive and file, and expires. It carries a content disposition of
// attachment naming the file, so a browser following it saves the file rather
// than rendering it in place. A deployment with no public endpoint configured
// cannot mint one and answers 503 rather than a URL that will not work.
//
// Billed per call — for MINTING the URL, which is the work this operation does;
// the download that follows comes straight from the store and is not seen here.
// The balance is checked BEFORE anything is touched, so an unfunded org is
// refused with no URL issued.
func (o ops) readFile(ctx context.Context, in *fileRef) (*fileURL, error) {
	org, space, drive, file, err := o.address(ctx, in)
	if err != nil {
		return nil, err
	}
	pub, err := o.presigner()
	if err != nil {
		return nil, err
	}
	params := url.Values{}
	params.Set("response-content-disposition", "attachment; filename=\""+path.Base(file)+"\"")
	u, err := pub.PresignedGetObject(ctx, bucket(org, space), driveKey(drive)+file, presignTTL, params)
	if err != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "sign download: %v", err)
	}
	return &fileURL{URL: u.String(), Method: http.MethodGet, File: file, Expiry: int64(presignTTL.Seconds())}, nil
}

// WriteFile mints a URL the caller uploads the file to DIRECTLY.
//
// The bytes never pass through this binary and the store credential never leaves
// the server: the URL is signed against the PUBLIC host, scoped to exactly this
// space, drive and file, and expires. Writing into a folder that does not exist
// is fine and creates nothing — a folder is emergent from "/" in the name. A
// deployment with no public endpoint configured cannot mint a URL and answers 503
// rather than one that will not work.
//
// Billed per call — for MINTING the URL, which is the work this operation does;
// the upload that follows goes straight to the store and is not seen here. The
// balance is checked BEFORE anything is touched, so an unfunded org is refused
// with no URL issued.
func (o ops) writeFile(ctx context.Context, in *fileRef) (*fileURL, error) {
	org, space, drive, file, err := o.address(ctx, in)
	if err != nil {
		return nil, err
	}
	pub, err := o.presigner()
	if err != nil {
		return nil, err
	}
	u, err := pub.PresignedPutObject(ctx, bucket(org, space), driveKey(drive)+file, presignTTL)
	if err != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "sign upload: %v", err)
	}
	return &fileURL{URL: u.String(), Method: http.MethodPut, File: file, Expiry: int64(presignTTL.Seconds())}, nil
}

// DeleteFile removes one file and answers 204.
//
// It removes ONE file and never a folder: a name that looks like a folder is
// refused, because a folder is emergent from the names beneath it and deleting
// one would have to mean deleting them. The name is path-cleaned first, so the
// delete cannot reach outside the drive it names, and a space the caller's org
// does not own is the same 404 an unknown name gives.
//
// Billed per call: the balance is checked BEFORE anything is touched, so an
// unfunded org is refused with nothing deleted, and the debit lands only once the
// file is gone.
func (o ops) deleteFile(ctx context.Context, in *fileRef) (*cloud.Unit, error) {
	org, space, drive, file, err := o.address(ctx, in)
	if err != nil {
		return nil, err
	}
	cli, err := o.client()
	if err != nil {
		return nil, err
	}
	if err := cli.RemoveObject(ctx, bucket(org, space), driveKey(drive)+file, s3.RemoveObjectOptions{}); err != nil {
		if s3admin.NoSuchBucket(err) {
			return nil, zip.ErrNotFound("space not found")
		}
		return nil, zip.Errorf(http.StatusBadGateway, "delete file: %v", err)
	}
	return nil, nil
}

// address resolves the four values every file operation needs, in the one order
// they have to be resolved in: the admitted org first, because without it there is
// no namespace to address, then the three names off the URL. ONE function, so the
// read, the write and the delete cannot disagree about what a caller named.
func (o ops) address(ctx context.Context, in *fileRef) (org, space, drive, file string, err error) {
	if org, err = fare.Org(ctx); err != nil {
		return "", "", "", "", err
	}
	space, ok := named(in.Space)
	if !ok {
		return "", "", "", "", zip.ErrBadRequest("invalid space name")
	}
	drive, ok = named(in.Drive)
	if !ok {
		return "", "", "", "", zip.ErrBadRequest("invalid drive name")
	}
	raw, ok := remainder(in.File)
	if !ok {
		return "", "", "", "", zip.ErrBadRequest("file name is not a decodable path")
	}
	file, ok = cleanFile(raw)
	if !ok {
		return "", "", "", "", zip.ErrBadRequest("file name is required and must be a clean path")
	}
	return org, space, drive, file, nil
}

// presigner is the PUBLIC-host client, or the honest 503 when this deployment
// configured no public endpoint. Presigning is a pure signature over that
// client's endpoint and makes no network call, which is why the signed host is
// the browser-routable one and not the in-cluster admin address.
func (o ops) presigner() (*s3.Client, error) {
	if !o.s.State.admin.PresignConfigured() {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "signed file URLs are not available (no public endpoint configured)")
	}
	pub, err := o.s.State.admin.PublicClient()
	if err != nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "object storage unavailable")
	}
	return pub, nil
}
