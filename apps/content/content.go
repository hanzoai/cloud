package content

//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/framework"
	"github.com/hanzoai/cloud/apps/metering"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/hanzoai/cloud/openapi"
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
// Registration is a one-line cloud.Plugin in apps.Wire() (after framework +
// knowledge, before the AI /v1/* catch-all); the module fixtures + lifecycle hooks are
// registered in doctypes.go's init(), process-global and mount-order-independent.

// POST /v1/content/generate declares its body and reply through the schema-only bridge
// rather than as a typed op: a studio-render billing denial answers a caller who is out
// of funds or over a spend cap with the shared `{error:{code,message}}` envelope written
// straight onto the response (cloud.DenyResource), and a typed handler — which returns
// (*Out, error) and lets zip render the error — cannot reproduce that wire shape. The
// body and the success reply are still exactly these two types.
func init() {
	openapi.Register("/v1/content/generate", "POST", GenerateInput{}, GenerateResult{})
}

// state is content's own data: the swappable edges. The shared deps (logger,
// billing meter, KMS, brand) live in the embedded cloud.Base, reached as s.Log / s.Bill.
type state struct {
	gen  Generator
	dist Distributor
	sf   Storefront
}

// mounted is the process singleton the exported ops (Generate/Publish/Transition) run
// on — the same pattern framework uses for its in-process API. Set by Mount.
var mounted *cloud.Service[state]

// Mount wires the content control-plane onto app. It is a "complex" mount (a package
// global for the exported ops the connector calls), so it builds the Service value
// directly per the cloud.Service convention.
func Mount(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("content.Mount: nil app")
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
		sf:   newStorefront(),
	}
	s := &cloud.Service[state]{Base: b, State: st}
	mounted = s
	routes(app, s)

	b.Log.Info("content mounted", "brand", b.Brand,
		"generator", configured(st.gen), "distributor", configured(st.dist),
		"storefront", configured(st.sf))
	return nil
}

// routes registers the /v1/content/* surface. Static segments (lifecycle/board/
// channels/generate/publish) never collide with the parameterised transition route
// (which is three segments deep), so registration order is not load-bearing here.
func routes(app cloud.Router, s *cloud.Service[state]) {
	o := ops{s: s}
	z := cloud.ZipApp(app)
	g := app.Group("/v1/content")
	// The bridge FIRST: fiber runs middleware in registration order, so one installed
	// after these leaves would never run — and every op below resolves its tenant
	// through the request it parks.
	g.Use(cloud.Bridge())

	zip.Get(z, "/v1/content/lifecycle", o.getLifecycle)
	zip.Get(z, "/v1/content/board", o.getBoard)
	zip.Get(z, "/v1/content/channels", o.getChannels)
	// generate stays a raw handler — see the openapi.Register note above.
	g.Post("/generate", cloud.Handle(s, postGenerate))
	zip.Post(z, "/v1/content/publish", o.postPublish)
	zip.Post(z, "/v1/content/:doctype/:name/transition", o.postTransition)
}

// ops binds the service to content's typed handlers. A TypedHandler is
// func(context.Context, *In) (*Out, error) — no parameter for the service — so it
// arrives as a RECEIVER and every op is a method value (o.getBoard), which is also the
// only bound form cmd/zipdoc can lift prose from.
type ops struct{ s *cloud.Service[state] }

// orgOf resolves the org — the tenant-isolation KEY — for a typed op. The org is
// EXACTLY what SanitizeIdentity minted from the validated IAM owner claim, carried
// across the typed seam by cloud.Bridge, never read from the input.
func orgOf(ctx context.Context) (string, error) {
	org, ok := principal.OrgFrom(ctx)
	if !ok {
		return "", zip.ErrForbidden("valid principal required")
	}
	return org, nil
}

// BoardQuery filters the cross-DocType content board.
type BoardQuery struct {
	// Status narrows to one lifecycle state; an unknown state is rejected.
	Status string `json:"status"`
	// Project narrows to one brand/site sub-scope.
	Project string `json:"project"`
	// DocType narrows to one content type; anything not publishable is rejected.
	DocType string `json:"doctype"`
	// Limit caps the items returned; 0 means 200 and nothing above 1000 is honoured.
	Limit int `json:"limit"`
}

// BoardView is the merged content queue across every publishable DocType.
type BoardView struct {
	// Data is the items, most recently updated first.
	Data []boardItem `json:"data"`
	// Count is how many items are in Data.
	Count int `json:"count"`
}

// ChannelList is the org's connected distribution channels.
type ChannelList struct {
	// Data is the channels a publish can target.
	Data []Channel `json:"data"`
}

// TransitionReq moves one content item to a new lifecycle state.
type TransitionReq struct {
	// DocType is the content type from the path.
	DocType string `json:"doctype"`
	// Name is the document name from the path.
	Name string `json:"name"`
	// To is the target lifecycle state. Required, and the edge from the current state must be legal.
	To string `json:"to" validate:"required"`
	// ScheduleAt is an RFC3339 instant to distribute at; empty distributes now.
	ScheduleAt string `json:"scheduleAt,omitempty"`
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

// getLifecycle returns the content state machine — every state and its legal moves. It
// is the same graph the transition endpoint enforces, so a
// client can render only the moves that will be accepted.
//
// Response: {"states": ["draft", "review", "scheduled", "queued", "published", "archived"], "initial": "draft", "live": "published", "transitions": {"draft": ["review", "archived"]}}
func (o ops) getLifecycle(ctx context.Context, _ *struct{}) (*stateGraph, error) {
	if _, err := orgOf(ctx); err != nil {
		return nil, err
	}
	g := lifecycleGraph()
	return &g, nil
}

// getBoard merges the org's content items across every publishable type into one queue.
// It is the cross-type read the framework's per-type list cannot give, and never 5xxs on a
// partial failure: a type the org has not installed, or one whose read errors, is
// skipped rather than fatal.
//
// Example: {"status": "review", "limit": 50}
func (o ops) getBoard(ctx context.Context, in *BoardQuery) (*BoardView, error) {
	s := o.s
	org, err := orgOf(ctx)
	if err != nil {
		return nil, err
	}
	limit := limitOf(in.Limit, 200)

	filters := map[string]string{}
	if status := strings.TrimSpace(in.Status); status != "" {
		if !IsStatus(status) {
			return nil, zip.ErrBadRequest("unknown status: " + status)
		}
		filters[StatusField] = status
	}
	if project := strings.TrimSpace(in.Project); project != "" {
		filters["project"] = project
	}

	types := publishableDocTypes
	if only := strings.TrimSpace(in.DocType); only != "" {
		if !isPublishableDocType(only) {
			return nil, zip.ErrNotFound("unknown content type: " + only)
		}
		types = []string{only}
	}

	items := make([]boardItem, 0, limit)
	for _, dt := range types {
		if !framework.Installed(ctx, org, dt) {
			continue // honest skip — the org has not installed the marketing module
		}
		docs, err := framework.Search(ctx, org, dt, filters, limit)
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
	return &BoardView{Data: items, Count: len(items)}, nil
}

// getChannels returns the distribution channels the org has connected. They are the
// targets a publish fans out to, and a disabled channel is listed and skipped on send.
//
// Response: {"data": [{"id": "int_9f2", "provider": "x", "name": "@acme", "disabled": false}]}
func (o ops) getChannels(ctx context.Context, _ *struct{}) (*ChannelList, error) {
	s := o.s
	org, err := orgOf(ctx)
	if err != nil {
		return nil, err
	}
	chans, err := s.State.dist.Channels(ctx, org)
	if err != nil {
		return nil, opErr(err)
	}
	return &ChannelList{Data: chans}, nil
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

// postPublish distributes one content item to the org's connected channels. Publishing
// is best effort per channel: the reply carries the honest per-channel outcome, and a
// send failure is reported rather than raised, so a partial fan-out is visible instead
// of lost. A scheduleAt in the future queues the send instead of making it now.
//
// Example: {"doctype": "SocialPost", "name": "spring-launch", "scheduleAt": "2026-04-01T15:00:00Z"}
func (o ops) postPublish(ctx context.Context, in *PublishInput) (*PublishResult, error) {
	org, err := orgOf(ctx)
	if err != nil {
		return nil, err
	}
	in.DocType, in.Name = strings.TrimSpace(in.DocType), strings.TrimSpace(in.Name)
	if in.DocType == "" || in.Name == "" {
		return nil, zip.ErrBadRequest("doctype and name are required")
	}
	res, err := Publish(ctx, org, *in)
	if err != nil {
		return nil, opErr(err)
	}
	return &res, nil
}

// postTransition moves one content item to a new lifecycle state. It fans the item out
// to the org's channels when the target state distributes, and the edge is checked here and
// re-checked at the storage boundary, so an illegal move is refused twice; the
// distribution side effect is best effort and never rolls back the status change.
//
// Example: {"doctype": "SocialPost", "name": "spring-launch", "to": "published"}
func (o ops) postTransition(ctx context.Context, in *TransitionReq) (*TransitionResult, error) {
	org, err := orgOf(ctx)
	if err != nil {
		return nil, err
	}
	doctype := unescapePath(in.DocType)
	name := unescapePath(in.Name)
	if !isPublishableDocType(doctype) {
		return nil, zip.ErrNotFound("unknown content type: " + doctype)
	}
	to := strings.TrimSpace(in.To)
	if to == "" {
		return nil, zip.ErrBadRequest("to is required")
	}
	res, err := Transition(ctx, org, doctype, name, to, strings.TrimSpace(in.ScheduleAt))
	if err != nil {
		return nil, opErr(err)
	}
	return &res, nil
}

// ---- exported ops (the ONE implementation; handlers + connector both call these) ----

// TransitionResult is the outcome of a lifecycle move, including any distribution the
// transition triggered (nil when the target state does not distribute).
type TransitionResult struct {
	DocType      string            `json:"doctype"`
	Name         string            `json:"name"`
	From         string            `json:"from"`
	To           string            `json:"to"`
	Distribution *PublishResult    `json:"distribution,omitempty"`
	Storefront   *StorefrontResult `json:"storefront,omitempty"`
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
		// Catalog fan-out: a published product Asset becomes the storefront product
		// image (Listing headerImage), keyed by design == slug. Best-effort and skipped
		// for non-catalog items (StorefrontPublish returns nil) — never fatal.
		res.Storefront = StorefrontPublish(ctx, org, doctype, name)
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
	case notConfiguredGenerator, notConfiguredDistributor, notConfiguredStorefront, nil:
		return false
	}
	return true
}

// limitOf reads ?limit (1..1000), defaulting to def.
func limitOf(n, def int) int {
	if n > 0 {
		if n > 1000 {
			n = 1000
		}
		return n
	}
	return def
}

// unescapePath reads and percent-decodes a URL path segment (a content type or a
// document name), matching framework's own path decoding so a name with a reserved
// character addresses its stored value. Undecodable input is used as-is, never rejected.
func unescapePath(raw string) string {
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
