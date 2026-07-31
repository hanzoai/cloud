// Package prompts mounts the Hanzo Cloud /v1/prompts surface: a per-org,
// versioned prompt library. Every prompt belongs to exactly one org (the
// gateway-minted X-Org-Id, HIP-0026); tenant isolation is the org column,
// enforced on every query, so one tenant can never read or mutate another's
// prompts. Creating a prompt whose name already exists appends a new version —
// real, inspectable history, never a fabricated rollup.
//
// Surface (all org-scoped; the shape console's PromptsModule consumes):
//
//	GET    /v1/prompts            list current prompts for the org   -> {data:[PromptMeta]}
//	POST   /v1/prompts            create or add-a-version            -> PromptDetail
//	GET    /v1/prompts/metrics    real per-prompt stats              -> {data:[...]}
//	GET    /v1/prompts/:name      prompt detail + version history    -> PromptDetail
//	DELETE /v1/prompts/:name      delete a prompt (+ its versions)
//
// The store is SQLite in deps.DataDir (Base/SQLite-only mandate); it holds only
// template text + taxonomy, never a secret.
package prompts

//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
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
	Name          string   `json:"name"`
	Type          string   `json:"type"`
	Versions      []int    `json:"versions"`
	Labels        []string `json:"labels"`
	Tags          []string `json:"tags"`
	LastUpdatedAt string   `json:"lastUpdatedAt"`
}

// promptDetail is the single-prompt shape: current content + full history.
type promptDetail struct {
	Name      string        `json:"name"`
	Type      string        `json:"type"`
	Prompt    string        `json:"prompt"`
	Version   int           `json:"version"`
	Labels    []string      `json:"labels"`
	Tags      []string      `json:"tags"`
	Versions  []promptVersion `json:"versionHistory"`
	CreatedAt string        `json:"createdAt"`
	UpdatedAt string        `json:"lastUpdatedAt"`
}

// promptVersion is history METADATA only — no per-version content (Red MED-1), so
// the detail response stays small regardless of the append history.
type promptVersion struct {
	Version   int    `json:"version"`
	Type      string `json:"type"`
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
	hist := make([]promptVersion, 0, len(vs))
	for _, v := range vs {
		hist = append(hist, promptVersion{Version: v.Version, Type: v.Type, CreatedAt: rfc3339(v.CreatedAt)})
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
	if deps.Logger == nil {
		return fmt.Errorf("prompts.Mount: nil deps.Logger")
	}
	if deps.DataDir == "" {
		return fmt.Errorf("prompts.Mount: empty DataDir")
	}
	if err := os.MkdirAll(deps.DataDir, 0o755); err != nil {
		return fmt.Errorf("prompts.Mount: data dir: %w", err)
	}
	store, err := openStore(filepath.Join(deps.DataDir, "prompts.db"))
	if err != nil {
		return fmt.Errorf("prompts.Mount: open store: %w", err)
	}
	s := &cloud.Service[state]{Base: cloud.NewBase(deps, "prompts"), State: state{store: store}}
	mounted = s
	routes(app, s)
	s.Log.Info("prompts mounted", "brand", s.Brand)
	return nil
}

// ops binds the prompts state to the typed handlers. A zip TypedHandler is
// func(context.Context, *In) (*Out, error) — no parameter for the service — so the
// service arrives as a RECEIVER and every op is a method value.
type ops struct{ s *cloud.Service[state] }

// routes registers the prompts surface. Every route is a zip TYPED op, so the REST
// route, the OpenAPI document, the MCP tool and the CLI command all derive from ONE
// declaration. Static sub-routes are registered before the :name param route so a
// real prompt can never shadow /metrics (and "metrics"/"new" are reserved names).
func routes(app cloud.Router, s *cloud.Service[state]) {
	o := ops{s: s}
	z := cloud.ZipApp(app)
	// The bridge FIRST: fiber runs middleware in registration order, so one
	// installed after these leaves would never run — and every org-scoped op below
	// resolves its tenant through the request it parks.
	app.Group("/v1/prompts").Use(cloud.Bridge())

	// Root routes stay flat: Group("/v1/prompts").<M>("") would register "/v1/prompts/".
	zip.Get(z, "/v1/prompts", o.list)
	zip.Post(z, "/v1/prompts", o.create, zip.WithStatus(http.StatusCreated))
	zip.Get(z, "/v1/prompts/metrics", o.metrics)
	zip.Get(z, "/v1/prompts/catalog", o.catalog)
	zip.Get(z, "/v1/prompts/:name", o.get)
	zip.Delete(z, "/v1/prompts/:name", o.del)
}

// ---- handlers ----

type createPromptReq struct {
	// Name is the org-unique handle AND the URL segment a later GET addresses.
	// Must match ^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$ and not be a reserved name.
	Name string `json:"name"`
	// Type is the prompt kind, e.g. text or chat; empty defaults to text.
	Type string `json:"type"`
	// Prompt is the template content, at most 64KiB.
	Prompt string `json:"prompt"`
	// Labels are free-form taxonomy strings; trimmed, de-duplicated, capped at 32.
	Labels []string `json:"labels"`
	// Tags are free-form taxonomy strings; trimmed, de-duplicated, capped at 32.
	Tags []string `json:"tags"`
}

// promptRef addresses one prompt by its org-unique name.
type promptRef struct {
	// Name is the prompt name from the path.
	Name string `json:"name"`
}

// promptList is the list envelope the console's prompt library reads.
type promptList struct {
	// Data is one row per prompt in the caller's org, with its version numbers.
	Data []promptMeta `json:"data"`
}

// metricsOut is the per-prompt statistics envelope.
type metricsOut struct {
	// Data is one statistics row per prompt in the caller's org.
	Data []metricRow `json:"data"`
}

// catalogOut is the read-only starter library envelope.
type catalogOut struct {
	// Data is the vendored starter prompts, each importable verbatim via create.
	Data []starterPrompt `json:"data"`
}

// create saves a prompt in the caller's org and returns it with its full history.
// A name that already exists appends a new version and advances the current one,
// so create is the ONE write verb — there is no separate update.
//
// Example: {"name": "triage", "type": "chat", "prompt": "Classify the ticket: {{body}}", "tags": ["support"], "labels": ["production"]}
func (o ops) create(ctx context.Context, in *createPromptReq) (*promptDetail, error) {
	org, err := o.tenant(ctx)
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
	saved, err := o.s.State.store.Upsert(ctx, p)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "persist: %v", err)
	}
	vs, err := o.s.State.store.Versions(ctx, org, name)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "versions: %v", err)
	}
	out := toDetail(saved, vs)
	return &out, nil
}

// list lists the caller's org's prompts as metadata. Name, type, taxonomy and the
// version numbers on record are returned; content is not returned in bulk, so read
// one prompt for that.
//
// Response: {"data": [{"name": "triage", "type": "chat", "versions": [1, 2], "labels": ["production"], "tags": ["support"], "lastUpdatedAt": "2026-07-29T11:00:00Z"}]}
func (o ops) list(ctx context.Context, _ *struct{}) (*promptList, error) {
	org, err := o.tenant(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := o.s.State.store.List(ctx, org)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list: %v", err)
	}
	out := make([]promptMeta, 0, len(rows))
	for _, p := range rows {
		vs, err := o.s.State.store.Versions(ctx, org, p.Name)
		if err != nil {
			return nil, zip.Errorf(http.StatusInternalServerError, "versions: %v", err)
		}
		out = append(out, toMeta(p, versionNums(vs)))
	}
	return &promptList{Data: out}, nil
}

// get reads one prompt of the caller's org. It returns the current content plus the
// metadata of every revision on record, and a name in another org is not-found.
//
// Example: {"name": "triage"}
func (o ops) get(ctx context.Context, in *promptRef) (*promptDetail, error) {
	org, err := o.tenant(ctx)
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(in.Name)
	p, err := o.s.State.store.Get(ctx, org, name)
	if err == errNotFound {
		return nil, zip.ErrNotFound("prompt not found")
	}
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "get: %v", err)
	}
	vs, err := o.s.State.store.Versions(ctx, org, name)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "versions: %v", err)
	}
	out := toDetail(p, vs)
	return &out, nil
}

// del deletes one prompt of the caller's org, with its whole version history.
// A name in another org is reported not-found, never deleted.
//
// Example: {"name": "triage"}
func (o ops) del(ctx context.Context, in *promptRef) (*struct{}, error) {
	org, err := o.tenant(ctx)
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
	Name          string `json:"name"`
	Type          string `json:"type"`
	Versions      int    `json:"versions"`
	CurrentVer    int    `json:"currentVersion"`
	CreatedAt     string `json:"createdAt"`
	LastUpdatedAt string `json:"lastUpdatedAt"`
}

// metrics reports one real statistics row per prompt in the caller's org. Version
// count, current version and timestamps are all read from the store — nothing here
// is fabricated, and a prompt with no history reports the history it has.
//
// Response: {"data": [{"name": "triage", "type": "chat", "versions": 2, "currentVersion": 2, "createdAt": "2026-07-01T09:00:00Z", "lastUpdatedAt": "2026-07-29T11:00:00Z"}]}
func (o ops) metrics(ctx context.Context, _ *struct{}) (*metricsOut, error) {
	org, err := o.tenant(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := o.s.State.store.List(ctx, org)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "metrics: %v", err)
	}
	out := make([]metricRow, 0, len(rows))
	for _, p := range rows {
		n, err := o.s.State.store.CountVersions(ctx, org, p.Name)
		if err != nil {
			return nil, zip.Errorf(http.StatusInternalServerError, "versions: %v", err)
		}
		out = append(out, metricRow{
			Name: p.Name, Type: p.Type, Versions: n, CurrentVer: p.Version,
			CreatedAt: rfc3339(p.CreatedAt), LastUpdatedAt: rfc3339(p.UpdatedAt),
		})
	}
	return &metricsOut{Data: out}, nil
}

// ---- helpers ----

// tenant resolves the org — the tenant isolation KEY — for a request. It uses
// c.Org() EXACTLY as SanitizeIdentity minted it from the validated IAM owner
// claim (HIP-0026): never lowercased, stripped, or truncated. Normalizing the
// key would collapse DISTINCT owners into one storage bucket — a cross-tenant
// break (Red HIGH-1: "acme"/"ACME"/"acme!"/32-char-prefix all shared data).
// Reject only empty or pathologically long; never transform. There is NO magic
// "admin" bucket: a SuperAdmin operating on per-org data carries an explicit
// org (SanitizeIdentity sets X-Org-Id on the admin path), so an empty org is a
// true 403, never a bucket a real org named "admin"/"Admin" could land in.
//
// The org NEVER comes from an In field — an In field is caller-supplied, so a
// tenant key read from one is a cross-tenant read the caller asserted for itself.
// It comes from the request cloud.Bridge parked; off the HTTP path there is none,
// so the op refuses.
func (o ops) tenant(ctx context.Context) (string, error) {
	c, ok := cloud.Request(ctx)
	if !ok {
		return "", zip.ErrForbidden("X-Org-Id required")
	}
	org, ok := principal.Org(c)
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
