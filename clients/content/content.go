package content

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/commerce/metering"
	"github.com/hanzoai/cloud/clients/framework"
	"github.com/hanzoai/cloud/clients/principal"
	"github.com/zap-proto/zip"
)

// content.go mounts the marketing content-loop control-plane at /v1/content/*. CRUD,
// tenancy, permissions, and install are the framework's generic surface
// (/v1/framework/*, module "marketing"); this subsystem adds ONLY what the generic
// engine cannot be:
//
//	GET  /v1/content/lifecycle                     the ONE state machine (states + edges)
//	GET  /v1/content/board                         cross-DocType queue board (aggregate read)
//	                                               ?status=&project=&doctype=&limit=
//	POST /v1/content/generate                      draft content via zen5 + studio  → 201
//	POST /v1/content/publish                       distribute an item to channels
//	GET  /v1/content/channels                      a brand's connected channels
//	POST /v1/content/:doctype/:name/transition     lifecycle transition (+ distribution)
//
// Every handler resolves its tenant through principal.Org (the ONE boundary) and
// scopes strictly to that org — a caller can only ever touch its own content. Every
// exported op (Generate/Publish/Transition) is called BOTH by the handler here AND by
// the content automations connector (clients/automations/connector_content.go), so a
// human console, an /v1/automations flow, an MCP tool call, and a headless bot all
// drive the SAME single implementation. The subsystem is a stateless orchestrator over
// framework (which holds the state) + the AI/social edges — it opens no store of its own.
//
// Registration is a one-line cloud.MountSpec in subsystems.Wire() (after framework +
// knowledge, before the AI /v1/* catch-all); the module fixtures + lifecycle hooks are
// registered in doctypes.go's init(), process-global and mount-order-independent.

// state is content's own data: the two swappable edges. The shared deps (logger,
// billing meter, KMS, brand) live in the embedded cloud.Base, reached as s.Log / s.Bill.
type state struct {
	gen  Generator
	dist Distributor
}

// mounted is the process singleton the exported ops (Generate/Publish/Transition) run
// on — the same pattern framework uses for its in-process API. Set by Mount.
var mounted *cloud.Service[state]

// Mount wires the content control-plane onto app. It is a "complex" mount (a package
// global for the exported ops the connector calls), so it builds the Service value
// directly per the cloud.Service convention.
func Mount(app *zip.App, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("content.Mount: nil zip.App")
	}
	if deps.Logger == nil {
		return fmt.Errorf("content.Mount: nil deps.Logger")
	}
	b := cloud.NewBase(deps, "content")

	// This is the ONE place the edges are selected. The generator is REAL (newGenerator:
	// zen5 copy via the metered deps.AI + studio assets metered through b.Bill); it
	// fail-closes per-mode when a backend is unconfigured, so a deployment without an AI
	// plane or a reachable studio degrades to an honest 503 rather than fake output. The
	// distributor (hanzoai/social) is still the fail-closed default — a transition into
	// distribution records "not_configured" WITHOUT failing the status change — until it
	// is wired in its own follow-up.
	st := state{
		gen:  newGenerator(deps, b),
		dist: newDistributor(),
	}
	s := &cloud.Service[state]{Base: b, State: st}
	mounted = s
	routes(app, s)

	b.Log.Info("content mounted", "brand", b.Brand,
		"generator", configured(st.gen), "distributor", configured(st.dist))
	return nil
}

// routes registers the /v1/content/* surface. Static segments (lifecycle/board/
// channels/generate/publish) never collide with the parameterised transition route
// (which is three segments deep), so registration order is not load-bearing here.
func routes(app *zip.App, s *cloud.Service[state]) {
	app.Get("/v1/content/lifecycle", cloud.Handle(s, getLifecycle))
	app.Get("/v1/content/board", cloud.Handle(s, getBoard))
	app.Get("/v1/content/channels", cloud.Handle(s, getChannels))
	app.Post("/v1/content/generate", cloud.Handle(s, postGenerate))
	app.Post("/v1/content/publish", cloud.Handle(s, postPublish))
	app.Post("/v1/content/:doctype/:name/transition", cloud.Handle(s, postTransition))
}

// Shutdown releases the mounted singleton. Idempotent; content owns no store, so this
// only clears the pointer (kept for symmetry with the store-owning lanes + test hygiene).
func Shutdown() error { mounted = nil; return nil }

// ---- sentinel errors (classified to honest HTTP codes by opErr) ----

var (
	errNotMounted         = errors.New("content: subsystem not mounted")
	errNotConfigured      = errors.New("content: feature not configured")
	errUnknownDocType     = errors.New("content: unknown content type")
	errUnknownStatus      = errors.New("content: unknown status")
	errIllegalTransition  = errors.New("content: illegal transition")
	errModuleNotInstalled = errors.New("content: marketing module not installed for org")
	errInvalidSource      = errors.New("content: invalid source_media")
)

// ---- handlers (free functions bound with cloud.Handle) ----

func getLifecycle(_ *cloud.Service[state], c *zip.Ctx) error {
	if _, ok := principal.Org(c); !ok {
		return zip.ErrForbidden("valid principal required")
	}
	return c.JSON(http.StatusOK, lifecycleGraph())
}

// getBoard aggregates the marketing content items across DocTypes into ONE queue board
// — the cross-DocType read the framework's per-DocType list cannot give. It never 5xxs
// on a partial failure: an un-installed or erroring DocType is skipped, not fatal.
func getBoard(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := principal.Org(c)
	if !ok {
		return zip.ErrForbidden("valid principal required")
	}
	limit := limitOf(c, 200)

	filters := map[string]string{}
	if status := strings.TrimSpace(c.Query("status")); status != "" {
		if !IsStatus(status) {
			return zip.ErrBadRequest("unknown status: " + status)
		}
		filters[StatusField] = status
	}
	if project := strings.TrimSpace(c.Query("project")); project != "" {
		filters["project"] = project
	}

	types := publishableDocTypes
	if only := strings.TrimSpace(c.Query("doctype")); only != "" {
		if !isPublishableDocType(only) {
			return zip.ErrNotFound("unknown content type: " + only)
		}
		types = []string{only}
	}

	items := make([]boardItem, 0, limit)
	for _, dt := range types {
		if !framework.Installed(c.Context(), org, dt) {
			continue // honest skip — the org has not installed the marketing module
		}
		docs, err := framework.Search(c.Context(), org, dt, filters, limit)
		if err != nil {
			s.Log.Warn("board search degraded", "doctype", dt, "org", org, "err", err)
			continue // one DocType's failure never fails the whole board
		}
		for _, d := range docs {
			items = append(items, boardItemFrom(dt, d))
		}
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].UpdatedAt > items[j].UpdatedAt })
	if len(items) > limit {
		items = items[:limit]
	}
	return c.JSON(http.StatusOK, map[string]any{"data": items, "count": len(items)})
}

func getChannels(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := principal.Org(c)
	if !ok {
		return zip.ErrForbidden("valid principal required")
	}
	chans, err := s.State.dist.Channels(c.Context(), org)
	if err != nil {
		return opErr(err)
	}
	return c.JSON(http.StatusOK, map[string]any{"data": chans})
}

func postGenerate(_ *cloud.Service[state], c *zip.Ctx) error {
	org, ok := principal.Org(c)
	if !ok {
		return zip.ErrForbidden("valid principal required")
	}
	var in GenerateInput
	if err := c.Bind(&in); err != nil {
		return err
	}
	in.DocType = strings.TrimSpace(in.DocType)
	if in.DocType == "" {
		return zip.ErrBadRequest("doctype is required")
	}
	res, err := Generate(c.Context(), org, in)
	if err != nil {
		// A studio-render billing denial is a funds/cap outcome, not a server fault:
		// render it as the SAME 402/503 contract every Hanzo resource create emits.
		if errors.Is(err, metering.ErrInsufficientBalance) || errors.Is(err, metering.ErrSpendCapExceeded) {
			return cloud.DenyResource(c, err)
		}
		return opErr(err)
	}
	return c.JSON(http.StatusCreated, res)
}

func postPublish(_ *cloud.Service[state], c *zip.Ctx) error {
	org, ok := principal.Org(c)
	if !ok {
		return zip.ErrForbidden("valid principal required")
	}
	var in PublishInput
	if err := c.Bind(&in); err != nil {
		return err
	}
	in.DocType, in.Name = strings.TrimSpace(in.DocType), strings.TrimSpace(in.Name)
	if in.DocType == "" || in.Name == "" {
		return zip.ErrBadRequest("doctype and name are required")
	}
	res, err := Publish(c.Context(), org, in)
	if err != nil {
		return opErr(err)
	}
	return c.JSON(http.StatusOK, res)
}

func postTransition(_ *cloud.Service[state], c *zip.Ctx) error {
	org, ok := principal.Org(c)
	if !ok {
		return zip.ErrForbidden("valid principal required")
	}
	doctype := pathParam(c, "doctype")
	name := pathParam(c, "name")
	if !isPublishableDocType(doctype) {
		return zip.ErrNotFound("unknown content type: " + doctype)
	}
	var body struct {
		To         string `json:"to"`
		ScheduleAt string `json:"scheduleAt,omitempty"`
	}
	if err := c.Bind(&body); err != nil {
		return err
	}
	to := strings.TrimSpace(body.To)
	if to == "" {
		return zip.ErrBadRequest("to is required")
	}
	res, err := Transition(c.Context(), org, doctype, name, to, strings.TrimSpace(body.ScheduleAt))
	if err != nil {
		return opErr(err)
	}
	return c.JSON(http.StatusOK, res)
}

// ---- exported ops (the ONE implementation; handlers + connector both call these) ----

// TransitionResult is the outcome of a lifecycle move, including any distribution the
// transition triggered (nil when the target state does not distribute).
type TransitionResult struct {
	DocType      string         `json:"doctype"`
	Name         string         `json:"name"`
	From         string         `json:"from"`
	To           string         `json:"to"`
	Distribution *PublishResult `json:"distribution,omitempty"`
}

// Transition moves a content item to a new lifecycle state and, when that state
// distributes (queued/published), fans it out to channels. The status write goes
// through framework.UpdateData, so the before_save lifecycle hook re-validates the edge
// at the storage boundary — defence in depth around the CanTransition check here. The
// distribution side effect is best effort: it records its honest state on the result
// but NEVER rolls back the status change or raises a 5xx. org MUST be the validated
// tenant. It is the ONE transition path — the HTTP handler and the content_transition
// automation action both call it.
func Transition(ctx context.Context, org, doctype, name, to, scheduleAt string) (TransitionResult, error) {
	s := mounted
	if s == nil {
		return TransitionResult{}, errNotMounted
	}
	if !isPublishableDocType(doctype) {
		return TransitionResult{}, errUnknownDocType
	}
	if !IsStatus(to) {
		return TransitionResult{}, fmt.Errorf("%w: %q", errUnknownStatus, to)
	}

	doc, err := framework.Get(ctx, org, doctype, name)
	if err != nil {
		return TransitionResult{}, err
	}
	from := statusOf(&doc)
	if from == "" {
		from = StatusDraft
	}
	if !CanTransition(from, to) {
		return TransitionResult{}, fmt.Errorf("%w %q → %q", errIllegalTransition, from, to)
	}

	data := cloneData(doc.Data)
	data[StatusField] = to
	stampTimestamps(data, to, scheduleAt)
	// TRUSTED write: Transition is the server op that stamps the server-managed
	// published_at (and preserves external_ids), so it wraps the context to pass the
	// enforceServerOwned gate. The enforceLifecycle gate still runs — the trusted marker
	// exempts ONLY the server-owned-field check, never edge legality (defence in depth).
	if err := framework.UpdateData(withTrustedWrite(ctx), org, doctype, name, data); err != nil {
		return TransitionResult{}, err
	}

	res := TransitionResult{DocType: doctype, Name: name, From: from, To: to}
	if entersDistribution(to) {
		pr, perr := Publish(ctx, org, PublishInput{DocType: doctype, Name: name, ScheduleAt: scheduleAt})
		if perr != nil {
			// Never fatal — the status IS updated; distribution can be retried.
			s.Log.Warn("distribution on transition failed (status updated)",
				"doctype", doctype, "name", name, "to", to, "err", perr)
			pr = PublishResult{Status: distributionState(perr)}
		}
		res.Distribution = &pr
	}
	return res, nil
}

// ---- helpers ----

// boardItem is one row of the aggregate content board.
type boardItem struct {
	DocType   string `json:"doctype"`
	Name      string `json:"name"`
	Title     string `json:"title"`
	Status    string `json:"status"`
	Project   string `json:"project,omitempty"`
	UpdatedAt int64  `json:"updatedAt"`
}

func boardItemFrom(dt string, d framework.Document) boardItem {
	return boardItem{
		DocType:   dt,
		Name:      d.Name,
		Title:     dataString(d.Data, "title"),
		Status:    dataString(d.Data, StatusField),
		Project:   dataString(d.Data, "project"),
		UpdatedAt: d.UpdatedAt,
	}
}

// stampTimestamps records the lifecycle timestamps for a transition. published → sets
// published_at=now; queued with a schedule → sets scheduled_at. A DocType that does not
// declare a field simply ignores it (framework drops unknown keys), so this is safe for
// every content type.
func stampTimestamps(data map[string]any, to, scheduleAt string) {
	switch to {
	case StatusPublished:
		data["published_at"] = time.Now().UTC().Format(time.RFC3339)
	case StatusQueued:
		if scheduleAt != "" {
			data["scheduled_at"] = scheduleAt
		}
	}
}

// distributionState maps a publish error to the honest distribution status string.
func distributionState(err error) string {
	if errors.Is(err, errNotConfigured) {
		return "not_configured"
	}
	return "failed"
}

// cloneData shallow-copies a document's field map so a transition can mutate status/
// timestamps without touching the value read from the store.
func cloneData(in map[string]any) map[string]any {
	out := make(map[string]any, len(in)+2)
	for k, v := range in {
		out[k] = v
	}
	return out
}

func dataString(m map[string]any, key string) string {
	s, _ := m[key].(string)
	return s
}

// configured reports whether an edge is a real implementation (not the fail-closed
// default) — for the mount log line.
func configured(x any) bool {
	switch x.(type) {
	case notConfiguredGenerator, notConfiguredDistributor, nil:
		return false
	}
	return true
}

// limitOf reads ?limit (1..1000), defaulting to def.
func limitOf(c *zip.Ctx, def int) int {
	if raw := strings.TrimSpace(c.Query("limit")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			if n > 1000 {
				n = 1000
			}
			return n
		}
	}
	return def
}

// pathParam reads and percent-decodes a URL path segment (a content type or a document
// name), matching framework's own path decoding so a name with a reserved character
// addresses its stored value.
func pathParam(c *zip.Ctx, name string) string {
	raw := c.Param(name)
	if dec, err := url.PathUnescape(raw); err == nil {
		raw = dec
	}
	return strings.TrimSpace(raw)
}

// opErr maps an exported-op error to an honest HTTP status. Every foreseeable
// condition is a 4xx or a fail-closed 503 — never a 5xx from a bug — and a genuine
// unexpected store error is the only path that reaches 500 (honest, not a panic).
func opErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, errNotConfigured):
		return zip.Errorf(http.StatusServiceUnavailable, "content feature not configured for this deployment")
	case errors.Is(err, errUpstream):
		return zip.Errorf(http.StatusServiceUnavailable, "distribution edge temporarily unavailable")
	case errors.Is(err, errNotMounted):
		return zip.Errorf(http.StatusServiceUnavailable, "content subsystem unavailable")
	case errors.Is(err, errModuleNotInstalled):
		return zip.Errorf(http.StatusConflict, "install the marketing module first: POST /v1/framework/modules/marketing/install")
	case errors.Is(err, errUnknownDocType):
		return zip.ErrNotFound("unknown content type")
	case errors.Is(err, errUnknownStatus):
		return zip.ErrBadRequest("unknown status")
	case errors.Is(err, errInvalidSource):
		// A caller-supplied source_media that fails the SSRF/traversal validator is a
		// bad request — an honest 400 that never reaches the studio, never bills.
		return zip.ErrBadRequest(err.Error())
	case errors.Is(err, errIllegalTransition):
		return zip.Errorf(http.StatusConflict, "%v", err)
	case errors.Is(err, framework.ErrNotFound):
		return zip.ErrNotFound("content item not found")
	case errors.Is(err, framework.ErrConflict):
		return zip.ErrConflict("content item already exists")
	case errors.Is(err, framework.ErrBadRef):
		return zip.Errorf(http.StatusUnprocessableEntity, "referenced record not found in org")
	case framework.IsValidationError(err):
		return zip.ErrBadRequest(err.Error())
	default:
		return zip.Errorf(http.StatusInternalServerError, "%v", err)
	}
}
