// Package prompts is your prompt library, versioned, so nothing changes silently.
//
// Every prompt belongs to exactly one org (the gateway-minted X-Org-Id,
// HIP-0026); tenant isolation is the org column, enforced on every query, so
// one tenant can never read or mutate another's prompts. Creating a prompt
// whose name already exists appends a new version — real, inspectable history,
// never a fabricated rollup.
//
// Surface (all org-scoped; the shape console's PromptsModule consumes):
//
//	GET    /v1/prompts            list current prompts for the org   -> {data:[PromptMeta]}
//	POST   /v1/prompts            create or add-a-version            -> PromptDetail
//	GET    /v1/prompts/metrics    real per-prompt stats              -> {data:[...]}
//	GET    /v1/prompts/catalog    the embedded read-only starter set -> {data:[...]}
//	GET    /v1/prompts/:name      prompt detail + version history    -> PromptDetail
//	DELETE /v1/prompts/:name      delete a prompt (+ its versions)
//
// The store is SQLite in deps.DataDir (Base/SQLite-only mandate); it holds only
// template text + taxonomy, never a secret.
package prompts

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/zap-proto/zip"
)

// nameRE constrains a prompt name to a safe identifier. The name is the
// org-unique handle AND the URL path segment (/v1/prompts/:name), so this is
// the injection/traversal guard at the boundary.
var nameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// reserved names would collide with static sub-routes under /v1/prompts.
var reserved = map[string]bool{"metrics": true, "new": true, "catalog": true}

const (
	// maxContent caps a single prompt version's body (Red MED-1). Templates are
	// small; this bounds both storage and the detail-response size.
	maxContent = 64 * 1024
	// versionHistoryLimit bounds how many historical versions a detail response
	// returns (metadata only) so an unbounded append can't amplify one response.
	versionHistoryLimit = 100
)

// state is prompts's own data; shared deps live in the embedded cloud.Base.
type state struct {
	store *Store
}

// mounted is the active service so Shutdown can release the store.
var mounted *cloud.Service[state]

// ---- HTTP response shapes (the published contract console consumes) ----

// promptMeta is the list-row shape (console PromptMeta): name + version
// numbers + taxonomy + last-updated.
type promptMeta struct {
	// Name is the prompt's org-unique handle and the URL segment it is fetched by:
	// GET /v1/prompts/<name>.
	Name string `json:"name"`
	// Type labels the template's kind, "text" unless the creator said otherwise. It
	// is the CURRENT version's type; earlier versions may carry a different one.
	Type string `json:"type"`
	// Versions lists every version NUMBER this prompt has, newest first, capped at
	// the last 100. The highest is the current one. (On a metrics row the same key
	// is a count, not a list.)
	Versions []int `json:"versions"`
	// Labels is the creator's free-form taxonomy, stored as given after trimming and
	// de-duplication. Always present, `[]` when none — never null.
	Labels []string `json:"labels"`
	// Tags is the second free-form taxonomy under the same rules as Labels. Nothing
	// in this service interprets either; they are yours to organize by.
	Tags []string `json:"tags"`
	// LastUpdatedAt is when the newest version was appended, RFC 3339 UTC. Empty
	// only if the record carries no timestamp at all.
	LastUpdatedAt string `json:"lastUpdatedAt"`
}

// promptDetail is the single-prompt shape: current content + full history.
type promptDetail struct {
	// Name is the prompt's org-unique handle and the URL segment it is addressed by.
	Name string `json:"name"`
	// Type labels the current version's kind; "text" unless the creator said
	// otherwise.
	Type string `json:"type"`
	// Prompt is the CURRENT version's template body — the only content this service
	// returns. Earlier versions are listed in versionHistory by number and date, and
	// their bodies are not served in bulk.
	Prompt string `json:"prompt"`
	// Version is the current version number, starting at 1 and incremented by one on
	// every create against an existing name.
	Version int `json:"version"`
	// Labels is the current version's free-form taxonomy. `[]` when none, never
	// null.
	Labels []string `json:"labels"`
	// Tags is the second free-form taxonomy, same rules as Labels.
	Tags []string `json:"tags"`
	// Versions is the history METADATA, newest first, capped at the last 100 — no
	// bodies, so a long history cannot inflate this response. It always includes the
	// current version as its first entry.
	Versions []versionView `json:"versionHistory"`
	// CreatedAt is when version 1 was written, RFC 3339 UTC. Appending a version
	// does not move it.
	CreatedAt string `json:"createdAt"`
	// UpdatedAt is when the current version was appended, RFC 3339 UTC. Equal to
	// createdAt for a prompt that has only ever had one version.
	UpdatedAt string `json:"lastUpdatedAt"`
}

// versionView is history METADATA only — no per-version content (Red MED-1), so
// the detail response stays small regardless of the append history.
type versionView struct {
	// Version is this revision's number, 1 for the first. Numbers are dense and
	// never reused: deleting the prompt drops the whole history with it.
	Version int `json:"version"`
	// Type is the kind this revision was written with, which may differ from the
	// current one.
	Type string `json:"type"`
	// CreatedAt is when this revision was appended, RFC 3339 UTC.
	CreatedAt string `json:"createdAt"`
}

func rfc3339(unix int64) string {
	if unix == 0 {
		return ""
	}
	return time.Unix(unix, 0).UTC().Format(time.RFC3339)
}

func versionNums(vs []Version) []int {
	out := make([]int, 0, len(vs))
	for _, v := range vs {
		out = append(out, v.Version)
	}
	return out
}

func toMeta(p Prompt, versions []int) promptMeta {
	return promptMeta{
		Name: p.Name, Type: p.Type, Versions: versions,
		Labels: nonNil(p.Labels), Tags: nonNil(p.Tags), LastUpdatedAt: rfc3339(p.UpdatedAt),
	}
}

func toDetail(p Prompt, vs []Version) promptDetail {
	hist := make([]versionView, 0, len(vs))
	for _, v := range vs {
		hist = append(hist, versionView{Version: v.Version, Type: v.Type, CreatedAt: rfc3339(v.CreatedAt)})
	}
	return promptDetail{
		Name: p.Name, Type: p.Type, Prompt: p.Content, Version: p.Version,
		Labels: nonNil(p.Labels), Tags: nonNil(p.Tags), Versions: hist,
		CreatedAt: rfc3339(p.CreatedAt), UpdatedAt: rfc3339(p.UpdatedAt),
	}
}

func nonNil(xs []string) []string {
	if xs == nil {
		return []string{}
	}
	return xs
}

// Mount wires the prompts surface onto app per HIP-0106. Complex flavour: it
// holds a package-global (mounted) so Shutdown can release the store, so it
// constructs the Service value directly rather than via cloud.Mount.
func Mount(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("prompts.Mount: nil app")
	}
	if deps.DataDir == "" {
		return fmt.Errorf("prompts.Mount: empty DataDir")
	}
	store, err := openStore(deps.DataDir)
	if err != nil {
		return fmt.Errorf("prompts.Mount: open store: %w", err)
	}
	s := &cloud.Service[state]{Base: cloud.NewBase(deps, "prompts"), State: state{store: store}}
	mounted = s
	if err := routes(app, s); err != nil {
		return err
	}
	s.Log.Info("prompts mounted", "brand", s.Brand)
	return nil
}

// zipdoc lifts the doc comment off each typed op and its In/Out fields into
// zipdoc_gen.go, which is the ONLY way that prose reaches the published document,
// the MCP tool list and the generated SDK — Go drops comments at compile time.
//
//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

// routes registers the prompts surface. Static sub-routes are registered before
// the :name param route so a real prompt can never shadow /metrics (and
// "metrics"/"new" are reserved names).
//
// A typed op receives a context.Context and its decoded In and nothing else, so
// the validated org crosses on the context. cloud.Bridge parks it there, and the
// COMPOSER installs it, not this subsystem: the fused host once at its root
// (serve.go), and a plugin program's constructor likewise. An install here would
// hang middleware on prefixes with no routes beneath them, a program zip refuses
// to compose.
//
// Every op is registered on the App with its WHOLE path rather than on a group: the
// collection routes ARE /v1/prompts, and a group prefix composed with an empty leaf
// yields "/v1/prompts/" — a different address. One registrar for all six keeps each
// op's published path exactly the path the router matches.
func routes(app cloud.Router, s *cloud.Service[state]) error {
	za := cloud.ZipApp(app)
	if za == nil {
		return fmt.Errorf("prompts.Mount: router exposes no op registry")
	}
	o := promptOps{s: s}
	zip.Get(za, "/v1/prompts", o.list)
	zip.Post(za, "/v1/prompts", o.create, zip.WithStatus(http.StatusCreated))
	zip.Get(za, "/v1/prompts/metrics", o.metrics)
	zip.Get(za, "/v1/prompts/catalog", o.catalog)
	zip.Get(za, "/v1/prompts/:name", o.get)
	zip.Delete(za, "/v1/prompts/:name", o.del)
	return nil
}

// promptOps binds the service to prompts's typed ops. A TypedHandler is
// func(context.Context, *In) (*Out, error) — no parameter for the service — so it
// arrives as a RECEIVER and every op is a method value, which is also the only bound
// form cmd/zipdoc can lift prose from.
type promptOps struct{ s *cloud.Service[state] }

// noInput is the In of an op addressed entirely by the caller's principal: it takes
// nothing off the wire. ONE of these for the whole package.
type noInput struct{}

// noContent is the Out of an op that answers 204 with an empty body. It is an ALIAS
// for the unnamed empty struct, not a definition: zip keys the response on 204 only
// when the Out type has no name, so a defined type here would publish "200 with a
// body" about a route that answers 204 with none.
type noContent = struct{}

// promptRef addresses one prompt by name. The name is the path segment: the URL is
// the addressing authority, so it binds from there whatever else arrives.
type promptRef struct {
	// Name is the prompt to act on, from the path.
	Name string `json:"name"`
}

// promptList is the org's current prompts.
type promptList struct {
	// Data is one row per prompt the org owns, each with its version numbers and
	// taxonomy — never the template bodies.
	Data []promptMeta `json:"data"`
}

// metricList is the org's per-prompt statistics.
type metricList struct {
	// Data is one row per prompt the org owns.
	Data []metricRow `json:"data"`
}

// catalogList is the read-only starter library.
type catalogList struct {
	// Data is every starter prompt, each importable as-is with POST /v1/prompts.
	Data []CatalogEntry `json:"data"`
}

// ---- handlers ----

// promptReq creates a prompt, or appends a version to one that already exists.
//
// Named for the RECORD, not for the verb: a schema name is GLOBAL in the woven fleet
// document, so "createReq" is a name several subsystems would each mean something
// different by — and openapi.Weave refuses that outright rather than let one generated
// SDK bind whichever shape it read last. apps/git already publishes one.
type promptReq struct {
	// Name is the org-unique handle AND the URL segment the prompt is addressed by:
	// 1-64 characters matching ^[A-Za-z0-9][A-Za-z0-9._-]*$. "metrics", "new" and
	// "catalog" are reserved. A name that already exists appends a new version.
	Name string `json:"name"`
	// Type labels the template's kind; defaults to "text".
	Type string `json:"type"`
	// Prompt is the template body, capped at 64 KiB. It holds template text only —
	// never a secret.
	Prompt string `json:"prompt"`
	// Labels is free-form taxonomy, each up to 64 characters, capped at 32 entries.
	Labels []string `json:"labels"`
	// Tags is free-form taxonomy under the same bounds as Labels.
	Tags []string `json:"tags"`
}

// Create records a prompt for the caller's org and answers 201 with it. A name the
// org already uses is NOT an error and NOT an overwrite: it appends a new version,
// so the library keeps real, inspectable history and the response carries the whole
// version list. The name is also the URL segment the prompt is fetched by, which is
// why its shape is constrained and a handful of names are reserved.
//
// Example: {"name": "greeting", "prompt": "You are a helpful assistant.", "tags": ["support"]}
func (o promptOps) create(ctx context.Context, in *promptReq) (*promptDetail, error) {
	s := o.s
	org, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	body := *in
	name := strings.TrimSpace(body.Name)
	if name == "" {
		return nil, zip.ErrBadRequest("name is required")
	}
	if reserved[strings.ToLower(name)] {
		return nil, zip.ErrBadRequest("name is reserved")
	}
	if !nameRE.MatchString(name) {
		return nil, zip.ErrBadRequest("name must match ^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$")
	}
	typ := strings.TrimSpace(body.Type)
	if typ == "" {
		typ = "text"
	}
	// Cap content (Red MED-1): unbounded prompt bodies amplify the shared DB and
	// blow up the detail response. A prompt is a template, not a blob.
	if len(body.Prompt) > maxContent {
		return nil, zip.ErrBadRequest("prompt content too large (max 64KiB)")
	}
	id, err := genID("prompt")
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "rng: %v", err)
	}
	now := time.Now().Unix()
	p := Prompt{
		ID: id, Org: org, Name: name, Type: typ, Content: body.Prompt,
		Labels: cleanList(body.Labels), Tags: cleanList(body.Tags), UpdatedAt: now,
	}
	saved, err := s.State.store.Upsert(ctx, p)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "persist: %v", err)
	}
	vs, err := s.State.store.Versions(ctx, org, name)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "versions: %v", err)
	}
	d := toDetail(saved, vs)
	return &d, nil
}

// List returns the caller org's prompt library as one row per prompt: its name,
// type, every version number it has, its taxonomy and when it last changed. The
// template bodies are deliberately absent — fetch one prompt to read its text.
func (o promptOps) list(ctx context.Context, _ *noInput) (*promptList, error) {
	s := o.s
	org, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.State.store.List(ctx, org)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list: %v", err)
	}
	out := make([]promptMeta, 0, len(rows))
	for _, p := range rows {
		vs, err := s.State.store.Versions(ctx, org, p.Name)
		if err != nil {
			return nil, zip.Errorf(http.StatusInternalServerError, "versions: %v", err)
		}
		out = append(out, toMeta(p, versionNums(vs)))
	}
	return &promptList{Data: out}, nil
}

// Get returns one of the caller org's prompts: its CURRENT template text plus the
// metadata of every version it has had. The history carries version numbers, types
// and timestamps only — not each version's body — so a long history cannot inflate
// this response. A name the caller's org does not own is 404, whoever owns it.
//
// Example: {"name": "greeting"}
func (o promptOps) get(ctx context.Context, in *promptRef) (*promptDetail, error) {
	s := o.s
	org, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(in.Name)
	p, err := s.State.store.Get(ctx, org, name)
	if err == errNotFound {
		return nil, zip.ErrNotFound("prompt not found")
	}
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "get: %v", err)
	}
	vs, err := s.State.store.Versions(ctx, org, name)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "versions: %v", err)
	}
	d := toDetail(p, vs)
	return &d, nil
}

// Delete removes one of the caller org's prompts and every version of it, answering
// 204. It is scoped to the caller's org, so a name another tenant owns is the same
// 404 an unknown name gives. There is no undo: the version history goes with it.
//
// Example: {"name": "greeting"}
func (o promptOps) del(ctx context.Context, in *promptRef) (*noContent, error) {
	org, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	deleted, err := o.s.State.store.Delete(ctx, org, strings.TrimSpace(in.Name))
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "delete: %v", err)
	}
	if !deleted {
		return nil, zip.ErrNotFound("prompt not found")
	}
	return nil, nil
}

// metricRow is a real per-prompt statistic (never fabricated): the number of
// versions, taxonomy, and timestamps for the org's prompts.
type metricRow struct {
	// Name is the prompt this row is about — its org-unique handle.
	Name string `json:"name"`
	// Type is the current version's kind.
	Type string `json:"type"`
	// Versions is how many revisions the prompt has, COUNTED in the store and
	// uncapped — so it can exceed the 100 entries a list row or a detail response
	// carries. Note the type: here `versions` is a number, while on a list row it is
	// the list of version numbers.
	Versions int `json:"versions"`
	// CurrentVer is the version number served as current. It always equals
	// `versions`: numbering is dense from 1, and deleting a prompt takes its whole
	// history with it rather than leaving a gap.
	CurrentVer int `json:"currentVersion"`
	// CreatedAt is when version 1 was written, RFC 3339 UTC.
	CreatedAt string `json:"createdAt"`
	// LastUpdatedAt is when the newest version was appended, RFC 3339 UTC — the age
	// of the template you would get today.
	LastUpdatedAt string `json:"lastUpdatedAt"`
}

// Metrics returns real per-prompt statistics for the caller's org: how many versions
// each prompt has, which one is current, and when it was created and last changed.
// Every number is counted from the store — nothing here is estimated or fabricated.
func (o promptOps) metrics(ctx context.Context, _ *noInput) (*metricList, error) {
	s := o.s
	org, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.State.store.List(ctx, org)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "metrics: %v", err)
	}
	out := make([]metricRow, 0, len(rows))
	for _, p := range rows {
		n, err := s.State.store.CountVersions(ctx, org, p.Name)
		if err != nil {
			return nil, zip.Errorf(http.StatusInternalServerError, "versions: %v", err)
		}
		out = append(out, metricRow{
			Name: p.Name, Type: p.Type, Versions: n, CurrentVer: p.Version,
			CreatedAt: rfc3339(p.CreatedAt), LastUpdatedAt: rfc3339(p.UpdatedAt),
		})
	}
	return &metricList{Data: out}, nil
}

// ---- helpers ----

// tenantOf resolves the org — the tenant isolation KEY — for a typed op. It is the
// value principal.Org decided at the identity boundary, which cloud.Bridge parked on
// the context: EXACTLY as SanitizeIdentity minted it from the validated IAM owner
// claim (HIP-0026), never lowercased, stripped, or truncated. Normalizing the key
// would collapse DISTINCT owners into one storage bucket — a cross-tenant break (Red
// HIGH-1: "acme"/"ACME"/"acme!"/32-char-prefix all shared data). Reject only empty or
// pathologically long; never transform. There is NO magic "admin" bucket: a
// SuperAdmin operating on per-org data carries an explicit org (SanitizeIdentity sets
// X-Org-Id on the admin path), so an empty org is a true 403, never a bucket a real
// org named "admin"/"Admin" could land in.
//
// It is never an In field: an In field is caller-supplied, so a tenant key read from
// one is a cross-tenant read the caller asserted for itself.
func tenantOf(ctx context.Context) (string, error) {
	org, ok := principal.OrgFrom(ctx)
	if !ok {
		return "", zip.ErrForbidden("X-Org-Id required")
	}
	return org, nil
}

// cleanList trims, drops empties, caps each element, and de-dups a taxonomy
// slice so labels/tags stay tidy identifiers.
func cleanList(xs []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, x := range xs {
		x = strings.TrimSpace(x)
		if x == "" || len(x) > 64 || seen[x] {
			continue
		}
		seen[x] = true
		out = append(out, x)
		if len(out) >= 32 {
			break
		}
	}
	return out
}

// genID returns a prefixed, collision-resistant id (prefix + 128 random bits).
func genID(prefix string) (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return prefix + "_" + hex.EncodeToString(b[:]), nil
}

// Shutdown closes the prompts store. Idempotent.
func Shutdown() error {
	if mounted == nil || mounted.State.store == nil {
		return nil
	}
	err := mounted.State.store.Close()
	mounted = nil
	return err
}
