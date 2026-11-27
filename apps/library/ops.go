package library

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"math"
	"mime"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/hanzoai/orm/tenant"

	"github.com/hanzoai/cloud/apps/flags"
	"github.com/hanzoai/cloud/client"
	commercepeer "github.com/hanzoai/cloud/client/commerce"
	flagsplane "github.com/hanzoai/cloud/client/flag"
	projectpeer "github.com/hanzoai/cloud/client/project"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/audit"
	"github.com/hanzoai/cloud/sdk/env"
	"github.com/hanzoai/cloud/sdk/iam"
	"github.com/hanzoai/cloud/sdk/mint"
	"github.com/hanzoai/cloud/sdk/plane"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/zap-proto/zip"
)

// The words an item is filed under.
var (
	kinds   = []string{"image", "video", "audio", "document", "slides", "type", "model", "code", "file"}
	sources = []string{"chat", "dev", "studio", "workflow"}
	scopes  = []string{"", "all", "mine", "shared"}
)

const (
	maxName    = 200
	maxRef     = 200
	maxOrigin  = 512
	maxKey     = 200
	maxMime    = 127
	maxQuery   = 200
	pageSize   = 60
	maxPage    = 200
	maxPixels  = 1 << 20
	maxPages   = 1 << 20
	maxSeconds = 7 * 24 * 3600
)

var projectRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// quota is how many bytes one organization may keep across all members' items
// on monthly plans (100 GiB by default).
var quota = int64(env.Int("LIBRARY_QUOTA_MIB", 100<<10)) << 20

// caller is the org and the person a call acts for. Only a person keeps a
// Library: a credential that names nobody, or an application acting as itself,
// is refused.
func caller(ctx context.Context) (org, me string, err error) {
	org, ok := cloud.Tenant(ctx)
	if !ok {
		if err := principal.RefusedFrom(ctx); err != nil {
			return "", "", err
		}
		return "", "", zip.ErrForbidden("library: no org on the call")
	}
	self, err := iam.SelfFrom(ctx)
	if err != nil {
		return "", "", err
	}
	if self.ID == "" || strings.HasPrefix(self.ID, "app:") {
		return "", "", zip.ErrForbidden("library: the Library keeps what a person made, and this credential names no person")
	}
	return org, self.ID, nil
}

func invalid(msg string) error { return zip.ErrBadRequest("library: " + msg) }

// line holds s to one line of printable text of at most n bytes.
func line(s string, n int) (string, bool) {
	s = strings.TrimSpace(s)
	return s, len(s) <= n && utf8.ValidString(s) && !strings.ContainsFunc(s, unicode.IsControl)
}

// kindOf is the kind a media type is filed under.
func kindOf(m string) string {
	base, _, _ := strings.Cut(strings.ToLower(m), ";")
	base = strings.TrimSpace(base)
	switch {
	case strings.HasPrefix(base, "image/"):
		return "image"
	case strings.HasPrefix(base, "video/"):
		return "video"
	case strings.HasPrefix(base, "audio/"):
		return "audio"
	case strings.HasPrefix(base, "font/"), strings.Contains(base, "font"):
		return "type"
	case strings.HasPrefix(base, "model/"):
		return "model"
	case strings.Contains(base, "presentation"), strings.Contains(base, "powerpoint"), strings.Contains(base, "keynote"):
		return "slides"
	case base == "application/pdf", base == "text/markdown", base == "text/plain", base == "text/csv",
		base == "text/html", base == "application/rtf", strings.Contains(base, "wordprocessing"),
		strings.Contains(base, "msword"), strings.Contains(base, "opendocument.text"), strings.Contains(base, "spreadsheet"):
		return "document"
	case base == "text/x-diff", base == "text/x-patch", strings.HasPrefix(base, "text/x-"),
		base == "application/javascript", base == "application/typescript", base == "application/json",
		base == "application/x-sh", base == "application/xml":
		return "code"
	}
	return "file"
}

// spec is a validated LibraryPutIn.
type spec struct {
	kind, source, name, mime, ref, origin, project, key string
	width, height, pages                                int64
	seconds                                             float64
}

// check validates what a writer said about an item.
func check(ctx context.Context, org string, in *client.LibraryPutIn) (spec, error) {
	var sp spec
	var ok bool
	if sp.source = strings.ToLower(strings.TrimSpace(in.Source)); !contains(sources, sp.source) {
		return sp, invalid("source must be one of chat, dev, studio, workflow")
	}
	if sp.name, ok = line(in.Name, 4*maxName); !ok || sp.name == "" || utf8.RuneCountInString(sp.name) > maxName {
		return sp, invalid("name is one line of at most 200 characters")
	}
	m := strings.TrimSpace(in.Mime)
	if base, _, err := mime.ParseMediaType(m); err != nil || len(m) > maxMime || !strings.Contains(base, "/") {
		return sp, invalid("mime is not a media type")
	} else {
		sp.mime = base
	}
	switch k := strings.ToLower(strings.TrimSpace(in.Kind)); {
	case k == "":
		sp.kind = kindOf(sp.mime)
	case contains(kinds, k):
		sp.kind = k
	default:
		return sp, invalid("kind must be one of " + strings.Join(kinds, ", "))
	}
	if sp.ref, ok = line(in.Ref, maxRef); !ok {
		return sp, invalid("ref is one line of at most 200 bytes")
	}
	if sp.origin, ok = line(in.Origin, maxOrigin); !ok || !path(sp.origin) {
		return sp, invalid("origin is a path on this site: it starts with one /")
	}
	if sp.key, ok = line(in.Key, maxKey); !ok {
		return sp, invalid("key is one line of at most 200 bytes")
	}
	if sp.project = strings.TrimSpace(in.Project); sp.project != "" {
		if err := ours(ctx, org, sp.project); err != nil {
			return sp, err
		}
	}
	if in.Width < 0 || in.Width > maxPixels || in.Height < 0 || in.Height > maxPixels ||
		in.Pages < 0 || in.Pages > maxPages || in.Seconds < 0 || in.Seconds > maxSeconds ||
		math.IsNaN(in.Seconds) || math.IsInf(in.Seconds, 0) {
		return sp, invalid("width, height, pages and seconds are small non-negative numbers")
	}
	sp.width, sp.height, sp.pages, sp.seconds = in.Width, in.Height, in.Pages, in.Seconds
	if len(in.Preview) > maxStill {
		return sp, invalid("preview is larger than 2 MiB")
	}
	if len(in.Preview) > 0 && !strings.HasPrefix(http.DetectContentType(in.Preview), "image/") {
		return sp, invalid("preview is not an image")
	}
	return sp, nil
}

// path reports whether s is empty or a path on this site — one leading slash,
// so never a scheme, a protocol-relative host or a script.
func path(s string) bool {
	return s == "" || (strings.HasPrefix(s, "/") && !strings.HasPrefix(s, "//") && !strings.Contains(s, "\\"))
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// projectOwned asks the project app whether slug is one of org's projects. A
// value so a test can answer for the app.
var projectOwned = func(ctx context.Context, org, slug string) (bool, error) {
	out, err := projectpeer.ProjectsOwnership(cloud.For(ctx, org), &client.OwnerIn{IDOrSlug: slug})
	if err != nil {
		return false, err
	}
	return out != nil && out.Mine, nil
}

// ours refuses a project that is not the org's. A deployment with no project
// app has nothing to ask, and the slug is then taken on its shape alone.
func ours(ctx context.Context, org, slug string) error {
	if !projectRE.MatchString(slug) {
		return invalid("project is not a project slug")
	}
	mine, err := projectOwned(ctx, org, slug)
	switch {
	case errors.Is(err, plane.ErrNoPeer):
		return nil
	case err != nil:
		return zip.Errorf(http.StatusServiceUnavailable, "library: the project could not be checked: %v", err)
	case !mine:
		return &zip.HTTPError{Status: http.StatusUnprocessableEntity, Code: "invalid_project", Msg: "project " + slug + " is not one of this organization's projects"}
	}
	return nil
}

// effectiveQuota resolves an organization's Library storage quota in bytes:
//  1. A per-organization override set in admin.hanzo.ai (flags.StorageOrgKey(org));
//  2. The highest quota configured for any active subscription plan the organization
//     holds (flags.StoragePlanKey(plan));
//  3. The platform default switch (flags.StorageOrgQuotaGiB);
//  4. The compiled/env fallback (quota, 100 GiB by default).
//
// A configured value of -1 GiB means unlimited storage (math.MaxInt64).
func effectiveQuota(ctx context.Context, org string) int64 {
	if org != "" {
		if v, err := flagsplane.FlagsValue(cloud.For(ctx, principal.Reserved), &client.FlagValueIn{Key: flags.StorageOrgKey(org), Kind: "int"}); err == nil && v != nil && v.Set {
			if v.N == -1 {
				return math.MaxInt64
			}
			if v.N > 0 {
				return int64(min(v.N, flags.MaxStorageQuotaGiB)) << 30
			}
		}
		if subs, err := commercepeer.BillingSubscriptions(cloud.For(ctx, org), &client.SubsIn{}); err == nil && subs != nil {
			var planMax int64
			for _, s := range subs.Rows {
				if !strings.EqualFold(strings.TrimSpace(s.Status), "active") {
					continue
				}
				slugs := []string{s.PlanID, s.Plan.Slug}
				for _, line := range s.Lines {
					if line.Plan != "" {
						slugs = append(slugs, line.Plan)
					}
				}
				for _, slug := range slugs {
					if slug == "" {
						continue
					}
					v, err := flagsplane.FlagsValue(cloud.For(ctx, principal.Reserved), &client.FlagValueIn{Key: flags.StoragePlanKey(slug), Kind: "int"})
					if err != nil || v == nil || !v.Set {
						continue
					}
					if v.N == -1 {
						return math.MaxInt64
					}
					if v.N > 0 {
						if b := int64(min(v.N, flags.MaxStorageQuotaGiB)) << 30; b > planMax {
							planMax = b
						}
					}
				}
			}
			if planMax > 0 {
				return planMax
			}
		}
	}
	if v, err := flagsplane.FlagsValue(cloud.For(ctx, principal.Reserved), &client.FlagValueIn{Key: flags.StorageOrgQuotaGiB, Kind: "int"}); err == nil && v != nil && v.Set {
		if v.N == -1 {
			return math.MaxInt64
		}
		if v.N > 0 {
			return int64(min(v.N, flags.MaxStorageQuotaGiB)) << 30
		}
	}
	return quota
}

// within refuses a write that would take the organization past its quota.
func (s *service) within(ctx context.Context, org string, more int64) error {
	limit := effectiveQuota(ctx, org)
	if limit == math.MaxInt64 {
		return nil
	}
	type total struct {
		N *int64 `db:"n"`
	}
	t, err := s.store.db.Select[total]("items", "SUM(bytes) AS n").Where(tenant.Ne("owner", "")).One(ctx)
	if err != nil {
		return zip.Errorf(http.StatusServiceUnavailable, "library: the store is not reachable: %v", err)
	}
	var held int64
	if t.N != nil {
		held = *t.N
	}
	if held+more > limit {
		return zip.Errorf(http.StatusInsufficientStorage, "library: this would keep more than your organization's %d MiB; delete something first", limit>>20)
	}
	return nil
}

// put keeps one item: its bytes, then its row. The same (owner, source, key)
// answers the item already made, and a race between two writes of one key
// answers the one that won.
func (s *service) put(ctx context.Context, in *client.LibraryPutIn) (*client.LibraryItem, error) {
	org, me, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	if len(in.Data) == 0 {
		return nil, invalid("data is empty")
	}
	if len(in.Data) > maxInline {
		return nil, zip.Errorf(http.StatusRequestEntityTooLarge, "library: data is larger than 10 MiB; reserve it instead")
	}
	sp, err := check(ctx, org, in)
	if err != nil {
		return nil, err
	}
	ctx = scoped(ctx, org)
	if sp.key != "" {
		if r, ok, err := s.store.keyed(ctx, me, sp.source, sp.key); err != nil {
			return nil, zip.Errorf(http.StatusServiceUnavailable, "library: the store is not reachable: %v", err)
		} else if ok {
			return s.present(ctx, me, r)
		}
	}
	if err := s.within(ctx, org, int64(len(in.Data)+len(in.Preview))); err != nil {
		return nil, err
	}
	r := s.row(org, me, sp)
	r.By = byOf(ctx)
	r.Bytes = int64(len(in.Data))
	sum := sha256.Sum256(in.Data)
	r.Sha256 = hex.EncodeToString(sum[:])
	if err := bucket.put(ctx, r.Object, sp.mime, in.Data); err != nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "library: the bytes could not be stored: %v", err)
	}
	if len(in.Preview) > 0 {
		r.Still = stillKey(org, me, r.ID)
		if err := bucket.put(ctx, r.Still, http.DetectContentType(in.Preview), in.Preview); err != nil {
			_ = bucket.remove(ctx, r.Object)
			return nil, zip.Errorf(http.StatusServiceUnavailable, "library: the preview could not be stored: %v", err)
		}
	}
	return s.file(ctx, me, r)
}

// byOf is how a reader names the caller: the name the identity boundary carried,
// else the address, held to one line.
func byOf(ctx context.Context) string {
	who := zip.CallerOf(ctx)
	by, ok := line(cmp.Or(who.Name, who.Email), 4*maxName)
	if !ok || utf8.RuneCountInString(by) > maxName {
		return ""
	}
	return by
}

// row is a new item's row, before its bytes are counted.
func (s *service) row(org, me string, sp spec) row {
	id := mint.ID("lib")
	at := now()
	key := sp.key
	if key == "" {
		key = id
	}
	return row{
		ID: id, Owner: me, State: stateReady, Kind: sp.kind, Source: sp.source, Name: sp.name, Mime: sp.mime,
		Width: sp.width, Height: sp.height, Seconds: sp.seconds, Pages: sp.pages,
		Project: sp.project, Ref: sp.ref, Origin: sp.origin, Key: key,
		Object: objectKey(org, me, id, sp.name), Created: at, Updated: at,
	}
}

// file inserts r, whose bytes are stored, and answers it. A write that lost a
// race on its key gives its bytes back and answers the winner.
func (s *service) file(ctx context.Context, me string, r row) (*client.LibraryItem, error) {
	err := s.store.insert(ctx, r)
	if err == nil {
		return s.present(ctx, me, r)
	}
	_ = bucket.remove(ctx, r.Object, r.Still)
	if tenant.Duplicate(err) {
		if won, ok, kerr := s.store.keyed(ctx, me, r.Source, r.Key); kerr == nil && ok {
			return s.present(ctx, me, won)
		}
	}
	return nil, zip.Errorf(http.StatusServiceUnavailable, "library: the item could not be kept: %v", err)
}

// present is a row as its reader sees it, with URLs signed for the hour.
func (s *service) present(ctx context.Context, me string, r row) (*client.LibraryItem, error) {
	it := &client.LibraryItem{
		ID: r.ID, Kind: r.Kind, Source: r.Source, Name: r.Name, Mime: r.Mime, Bytes: r.Bytes,
		Width: r.Width, Height: r.Height, Seconds: r.Seconds, Pages: r.Pages,
		Owner: r.Owner, Mine: r.Owner == me, Shared: r.Shared, Project: r.Project,
		Ref: r.Ref, Origin: r.Origin, Created: stamp(r.Created), Updated: stamp(r.Updated), By: r.By,
	}
	if r.State != stateReady {
		return it, nil
	}
	var err error
	if it.URL, err = bucket.sign(ctx, r.Object, r.Mime, r.Name, false); err != nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "library: the bytes are not reachable: %v", err)
	}
	if it.Download, err = bucket.sign(ctx, r.Object, r.Mime, r.Name, true); err != nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "library: the bytes are not reachable: %v", err)
	}
	switch {
	case r.Still != "":
		if it.Preview, err = bucket.sign(ctx, r.Still, "image/png", r.Name, false); err != nil {
			return nil, zip.Errorf(http.StatusServiceUnavailable, "library: the preview is not reachable: %v", err)
		}
	case r.Kind == "image" && inline(r.Mime) == r.Mime:
		it.Preview = it.URL
	}
	it.Expires = time.Now().Add(signed).UTC().Format(time.RFC3339)
	return it, nil
}

// ── HTTP ──────────────────────────────────────────────────────────────────────

// listQuery narrows a list. Every field is optional.
type listQuery struct {
	// Kind keeps one kind: image, video, audio, document, slides, type, model, code or file.
	Kind string `json:"kind"`
	// Source keeps what one product made: chat, dev, studio or workflow.
	Source string `json:"source"`
	// Ref keeps what one conversation, run or Studio run made.
	Ref string `json:"ref"`
	// Project keeps one project's items.
	Project string `json:"project"`
	// Q keeps the items whose name contains it, ignoring case.
	Q string `json:"q"`
	// Since and Until bound when it was made: RFC 3339, or a date (YYYY-MM-DD).
	Since string `json:"since"`
	Until string `json:"until"`
	// Scope is all (yours and what your org shared, the default), mine, or shared.
	Scope string `json:"scope"`
	// Limit bounds the page: default 60, at most 200.
	Limit int `json:"limit"`
	// Cursor continues a list from the page before's `next`.
	Cursor string `json:"cursor"`
}

// itemPage is a page of items.
type itemPage struct {
	// Data are the items, newest first.
	Data []client.LibraryItem `json:"data"`
	// Next continues the list as `cursor`; empty at its end.
	Next string `json:"next"`
}

// when reads a list's time bound.
func when(s string) (int64, bool) {
	if s = strings.TrimSpace(s); s == "" {
		return 0, true
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UnixMilli(), true
	}
	if t, err := time.Parse(time.DateOnly, s); err == nil {
		return t.UnixMilli(), true
	}
	return 0, false
}

// ListItems lists what you made and what members of your organization shared
// with you, newest first. Narrow it by kind, source, ref, project, a name search
// and a time range; `scope` mine keeps only yours and shared only what others
// shared. Each item carries signed URLs that work for an hour.
//
// Example: {"kind": "image", "source": "studio", "limit": 24}
func (o ops) listItems(ctx context.Context, in *listQuery) (*itemPage, error) {
	org, me, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	f := filter{me: me, limit: pageSize}
	f.scope = strings.ToLower(strings.TrimSpace(in.Scope))
	if !contains(scopes, f.scope) {
		return nil, invalid("scope must be all, mine or shared")
	}
	if f.kind = strings.ToLower(strings.TrimSpace(in.Kind)); f.kind != "" && !contains(kinds, f.kind) {
		return nil, invalid("kind must be one of " + strings.Join(kinds, ", "))
	}
	if f.source = strings.ToLower(strings.TrimSpace(in.Source)); f.source != "" && !contains(sources, f.source) {
		return nil, invalid("source must be one of chat, dev, studio, workflow")
	}
	var ok bool
	if f.ref, ok = line(in.Ref, maxRef); !ok {
		return nil, invalid("ref is one line of at most 200 bytes")
	}
	if f.project = strings.TrimSpace(in.Project); f.project != "" && !projectRE.MatchString(f.project) {
		return nil, invalid("project is not a project slug")
	}
	if f.q, ok = line(in.Q, maxQuery); !ok {
		return nil, invalid("q is one line of at most 200 bytes")
	}
	if f.since, ok = when(in.Since); !ok {
		return nil, invalid("since is RFC 3339 or YYYY-MM-DD")
	}
	if f.until, ok = when(in.Until); !ok {
		return nil, invalid("until is RFC 3339 or YYYY-MM-DD")
	}
	if in.Limit < 0 {
		return nil, invalid("limit is not negative")
	}
	if in.Limit > 0 {
		f.limit = min(in.Limit, maxPage)
	}
	if f.after, ok = parseCursor(strings.TrimSpace(in.Cursor)); !ok {
		return nil, invalid("cursor is not one this list answered")
	}
	ctx = scoped(ctx, org)
	rows, next, err := o.s.store.list(ctx, f)
	if err != nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "library: the store is not reachable: %v", err)
	}
	out := &itemPage{Data: make([]client.LibraryItem, 0, len(rows)), Next: next}
	for _, r := range rows {
		it, err := o.s.present(ctx, me, r)
		if err != nil {
			return nil, err
		}
		out.Data = append(out.Data, *it)
	}
	return out, nil
}

// itemRef names one item by its id, from the path.
type itemRef struct {
	// ID is the item, from the path.
	ID string `json:"id"`
}

// GetItem reads one item with fresh signed URLs. An item that is not yours and
// was not shared with your organization answers 404, as one that does not exist.
//
// Example: {"id": "lib_0f3c…"}
func (o ops) getItem(ctx context.Context, in *itemRef) (*client.LibraryItem, error) {
	org, me, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	ctx = scoped(ctx, org)
	r, err := o.s.store.one(ctx, me, strings.TrimSpace(in.ID))
	if noRows(err) {
		return nil, zip.ErrNotFound("library: no such item")
	}
	if err != nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "library: the store is not reachable: %v", err)
	}
	return o.s.present(ctx, me, r)
}

// keepIn is one thing to keep, its bytes base64 in `data` or reserved by size in `bytes`.
type keepIn struct {
	// Name is what it is called, a file name. Required.
	Name string `json:"name"`
	// Mime is its media type. Required.
	Mime string `json:"mime"`
	// Data is the bytes, base64, at most 10 MiB; omit when reserving with bytes.
	Data []byte `json:"data"`
	// Bytes reserves an upload of this size, at most 2 GiB, when data is empty, and returns a signed PUT in upload.
	Bytes int64 `json:"bytes"`
	// Source is the product it came from: chat, dev, studio or workflow. Required.
	Source string `json:"source"`
	// Kind is image, video, audio, document, slides, type, model, code or file;
	// absent derives it from mime.
	Kind string `json:"kind"`
	// Ref is what made it: the conversation, the run.
	Ref string `json:"ref"`
	// Origin is the path on this site back to what made it, as /chat?thread=….
	Origin string `json:"origin"`
	// Project is one of the organization's projects, by slug.
	Project string `json:"project"`
	// Preview is a still that shows it, base64, at most 2 MiB.
	Preview []byte `json:"preview"`
	// Width, Height, Seconds and Pages describe it when known.
	Width   int64   `json:"width"`
	Height  int64   `json:"height"`
	Seconds float64 `json:"seconds"`
	Pages   int64   `json:"pages"`
	// Key makes the save idempotent: the same source and key answer the item
	// already kept.
	Key string `json:"key"`
}

// KeepItem keeps one thing you made in your Library: its bytes go to your own
// prefix of the Library's store, and it is listed to you alone until you share
// it. Send `data` up to 10 MiB inline, or send `bytes` up to 2 GiB to reserve
// a signed `upload` PUT and finish it with `PATCH /v1/library/:id` `{"commit": true}`.
// A save with the `key` of one already kept answers that item.
//
// Example: {"name": "poster.png", "mime": "image/png", "data": "iVBORw0…", "source": "chat", "ref": "conv_1", "origin": "/chat?thread=conv_1"}
func (o ops) keepItem(ctx context.Context, in *keepIn) (*client.LibraryItem, error) {
	put := &client.LibraryPutIn{
		Kind: in.Kind, Source: in.Source, Name: in.Name, Mime: in.Mime, Data: in.Data, Bytes: in.Bytes, Preview: in.Preview,
		Ref: in.Ref, Origin: in.Origin, Project: in.Project, Key: in.Key,
		Width: in.Width, Height: in.Height, Seconds: in.Seconds, Pages: in.Pages,
	}
	if len(in.Data) == 0 && in.Bytes != 0 {
		res, err := o.s.reserve(ctx, put)
		if err != nil {
			return nil, err
		}
		if res.Upload.URL != "" {
			res.Item.Upload = &res.Upload
		}
		return &res.Item, nil
	}
	return o.s.put(ctx, put)
}

// itemPatch changes one of your items. Absent fields stay as they are.
type itemPatch struct {
	// ID is the item, from the path.
	ID string `json:"id"`
	// Name renames it.
	Name *string `json:"name"`
	// Shared shows it to every member of your organization, or stops.
	Shared *bool `json:"shared"`
	// Project moves it to one of the organization's projects; "" moves it out.
	Project *string `json:"project"`
	// Commit finishes a reserved upload once its bytes have been PUT.
	Commit *bool `json:"commit"`
}

// ChangeItem renames one of your items, shares it with your organization or
// stops sharing it, moves it to a project, or commits a reserved upload. Only
// its owner changes an item; a member's shared item answers 404 here as one
// that does not exist.
//
// Example: {"id": "lib_0f3c…", "shared": true}
func (o ops) changeItem(ctx context.Context, in *itemPatch) (*client.LibraryItem, error) {
	org, me, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	id := strings.TrimSpace(in.ID)
	var committed *client.LibraryItem
	if in.Commit != nil && *in.Commit {
		committed, err = o.s.commit(ctx, &client.LibraryRef{ID: id})
		if err != nil {
			return nil, err
		}
	}
	set := tenant.Row{}
	if in.Name != nil {
		name, ok := line(*in.Name, 4*maxName)
		if !ok || name == "" || utf8.RuneCountInString(name) > maxName {
			return nil, invalid("name is one line of at most 200 characters")
		}
		set["name"] = name
	}
	if in.Shared != nil {
		set["shared"] = *in.Shared
	}
	if in.Project != nil {
		p := strings.TrimSpace(*in.Project)
		if p != "" {
			if err := ours(ctx, org, p); err != nil {
				return nil, err
			}
		}
		set["project"] = p
	}
	if len(set) == 0 {
		if committed != nil {
			return committed, nil
		}
		return nil, invalid("say what to change: name, shared, project or commit")
	}
	ctx = scoped(ctx, org)
	if err := o.s.store.change(ctx, me, id, set); noRows(err) {
		return nil, zip.ErrNotFound("library: no such item")
	} else if err != nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "library: the store is not reachable: %v", err)
	}
	if in.Shared != nil {
		verb := "library.share"
		if !*in.Shared {
			verb = "library.unshare"
		}
		o.s.record(ctx, org, me, verb, id)
	}
	r, err := o.s.store.owned(ctx, me, id)
	if err != nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "library: the store is not reachable: %v", err)
	}
	return o.s.present(ctx, me, r)
}

// DeleteItem deletes one of your items and its bytes. Only its owner deletes an
// item.
//
// Example: {"id": "lib_0f3c…"}
func (o ops) deleteItem(ctx context.Context, in *itemRef) (*cloud.Unit, error) {
	org, me, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	ctx = scoped(ctx, org)
	id := strings.TrimSpace(in.ID)
	r, err := o.s.store.owned(ctx, me, id)
	if noRows(err) {
		return nil, zip.ErrNotFound("library: no such item")
	}
	if err != nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "library: the store is not reachable: %v", err)
	}
	if err := o.s.store.remove(ctx, me, id); err != nil && !noRows(err) {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "library: the item could not be deleted: %v", err)
	}
	if err := bucket.remove(ctx, r.Object, r.Still); err != nil {
		o.s.log.Warn("library: the bytes of a deleted item stayed", "id", id, "err", err)
	}
	o.s.record(ctx, org, me, "library.delete", id)
	return &cloud.Unit{}, nil
}

// unownedQuery bounds the SuperAdmin's view.
type unownedQuery struct {
	// Limit bounds the page: default 60, at most 200.
	Limit int `json:"limit"`
}

// ListUnowned reads, for a SuperAdmin alone, the organization's items that record
// no owner. Such an item is nobody's: no member lists it, reads it
// or changes it, and this view neither changes it nor signs its bytes. Every
// read of it is written to the audit trail.
func (o ops) listUnowned(ctx context.Context, in *unownedQuery) (*itemPage, error) {
	if !principal.SuperFrom(ctx) {
		return nil, zip.ErrForbidden("library: only a SuperAdmin reads items no one owns")
	}
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	who, _ := principal.SubjectFrom(ctx)
	limit := pageSize
	if in.Limit > 0 {
		limit = min(in.Limit, maxPage)
	}
	ctx = scoped(ctx, org)
	rows, err := o.s.store.unowned(ctx, limit)
	if err != nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "library: the store is not reachable: %v", err)
	}
	o.s.record(ctx, org, who, "library.unowned.read", "")
	out := &itemPage{Data: make([]client.LibraryItem, 0, len(rows))}
	for _, r := range rows {
		out.Data = append(out.Data, client.LibraryItem{
			ID: r.ID, Kind: r.Kind, Source: r.Source, Name: r.Name, Mime: r.Mime, Bytes: r.Bytes,
			Project: r.Project, Ref: r.Ref, Origin: r.Origin, Created: stamp(r.Created), Updated: stamp(r.Updated),
		})
	}
	return out, nil
}

// record writes one act to the audit trail. A trail that did not take it is
// logged; the act itself has already happened.
func (s *service) record(ctx context.Context, org, who, action, id string) {
	if s.audit == nil {
		return
	}
	rec := audit.Record{
		Actor:    audit.Actor{Org: org, Sub: who},
		Action:   action,
		Resource: audit.Resource{Type: "library.item", ID: id},
		Auth:     audit.AuthContext{Method: "gateway", IsAdmin: principal.SuperFrom(ctx)},
		Outcome:  audit.Outcome{Result: "ok", Status: http.StatusOK},
	}
	if _, err := s.audit.Append(ctx, rec); err != nil {
		s.log.Warn("library: audit append failed", "action", action, "err", err)
	}
}

// ── plane ─────────────────────────────────────────────────────────────────────

// expose publishes the Library's write path to its siblings. The org and the
// owner of every op are the caller's.
func expose(s *service) {
	p := cloud.Plane()
	p.Post("/library/put", s.put,
		zip.WithOperationID(client.LibraryPut),
		zip.WithSummary("Keep one thing a person made in their Library"))
	p.Post("/library/reserve", s.reserve,
		zip.WithOperationID(client.LibraryReserve),
		zip.WithSummary("Reserve a Library item too large to send inline, and where its bytes go"))
	p.Post("/library/commit", s.commit,
		zip.WithOperationID(client.LibraryCommit),
		zip.WithSummary("Finish a reserved Library item once its bytes are uploaded"))
	p.Post("/library/get", s.get,
		zip.WithOperationID(client.LibraryGet),
		zip.WithSummary("Read one Library item the caller may see, with fresh signed URLs"))
}

// reserve opens an item whose bytes the caller PUTs itself.
func (s *service) reserve(ctx context.Context, in *client.LibraryPutIn) (*client.LibraryReserved, error) {
	org, me, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	if len(in.Data) > 0 {
		return nil, invalid("a reservation carries no data; send it with library_put")
	}
	if in.Bytes <= 0 || in.Bytes > maxReserved {
		return nil, invalid("bytes is the size to upload, at most 2 GiB")
	}
	sp, err := check(ctx, org, in)
	if err != nil {
		return nil, err
	}
	ctx = scoped(ctx, org)
	if sp.key != "" {
		if r, ok, err := s.store.keyed(ctx, me, sp.source, sp.key); err != nil {
			return nil, zip.Errorf(http.StatusServiceUnavailable, "library: the store is not reachable: %v", err)
		} else if ok && r.State == stateReady {
			it, err := s.present(ctx, me, r)
			if err != nil {
				return nil, err
			}
			return &client.LibraryReserved{Item: *it}, nil
		} else if ok {
			return s.reopen(ctx, me, r)
		}
	}
	if err := s.within(ctx, org, in.Bytes); err != nil {
		return nil, err
	}
	r := s.row(org, me, sp)
	r.By = byOf(ctx)
	r.State, r.Bytes = statePending, in.Bytes
	if err := s.store.insert(ctx, r); err != nil {
		if tenant.Duplicate(err) {
			if won, ok, kerr := s.store.keyed(ctx, me, r.Source, r.Key); kerr == nil && ok {
				return s.reopen(ctx, me, won)
			}
		}
		return nil, zip.Errorf(http.StatusServiceUnavailable, "library: the item could not be reserved: %v", err)
	}
	return s.reopen(ctx, me, r)
}

// reopen answers a pending item and a fresh PUT for its bytes.
func (s *service) reopen(ctx context.Context, me string, r row) (*client.LibraryReserved, error) {
	u, headers, err := bucket.upload(ctx, r.Object, r.Mime)
	if err != nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "library: the upload could not be signed: %v", err)
	}
	it, err := s.present(ctx, me, r)
	if err != nil {
		return nil, err
	}
	return &client.LibraryReserved{Item: *it, Upload: client.LibraryUpload{
		URL: u, Headers: headers, Expires: time.Now().Add(signed).UTC().Format(time.RFC3339),
	}}, nil
}

// commit makes a reserved item ready once its bytes are there, held to the size
// reserved.
func (s *service) commit(ctx context.Context, in *client.LibraryRef) (*client.LibraryItem, error) {
	org, me, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	ctx = scoped(ctx, org)
	r, err := s.store.owned(ctx, me, strings.TrimSpace(in.ID))
	if noRows(err) {
		return nil, zip.ErrNotFound("library: no such item")
	}
	if err != nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "library: the store is not reachable: %v", err)
	}
	if r.State == stateReady {
		return s.present(ctx, me, r)
	}
	size, err := bucket.size(ctx, r.Object)
	if err != nil {
		return nil, &zip.HTTPError{Status: http.StatusConflict, Code: "not_uploaded", Msg: "library: the bytes have not been uploaded"}
	}
	if size == 0 || size > r.Bytes {
		_ = bucket.remove(ctx, r.Object)
		return nil, &zip.HTTPError{Status: http.StatusUnprocessableEntity, Code: "size", Msg: "library: the upload is empty or larger than reserved"}
	}
	if err := s.store.change(ctx, me, r.ID, tenant.Row{"state": stateReady, "bytes": size}); err != nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "library: the item could not be finished: %v", err)
	}
	r.State, r.Bytes = stateReady, size
	return s.present(ctx, me, r)
}

// get reads one item for a sibling, as GET /v1/library/:id does for a person.
func (s *service) get(ctx context.Context, in *client.LibraryRef) (*client.LibraryItem, error) {
	return ops{s: s}.getItem(ctx, &itemRef{ID: in.ID})
}
