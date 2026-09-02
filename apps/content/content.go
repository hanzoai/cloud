package content

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/framework"
	"github.com/hanzoai/cloud/apps/metering"
	"github.com/hanzoai/cloud/apps/principal"
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
// the content automations connector (apps/auto/connector_content.go), so a
// human console, an /v1/automations flow, an MCP tool call, and a headless bot all
// drive the SAME single implementation. The subsystem is a stateless orchestrator over
// framework (which holds the state) + the AI/social edges — it opens no store of its own.
//
// Registration is a one-line cloud.Plugin in apps.Wire() (after framework +
// knowledge, before the AI /v1/* catch-all); the module fixtures + lifecycle hooks are
// registered in doctypes.go's init(), process-global and mount-order-independent.

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
func Use(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("content.Use:  nil app")
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

// zipdoc lifts the doc comment off each typed op and its In/Out fields into
// zipdoc_gen.go, which is the ONLY way that prose reaches the published document,
// the MCP tool list and the generated SDK — Go drops comments at compile time.
// Run by `make describe` and by this app's own `make -C apps/content openapi`.
//
//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

// routes registers the /v1/content/* surface. Static segments (lifecycle/board/
// channels/generate/publish) never collide with the parameterised transition route
// (which is three segments deep), so registration order is not load-bearing here.
//
// The composer owns cloud.Bridge: the fused host installs it once at its root
// and the plugin constructor does the same for a plugin program, so no
// subsystem installs it.
func routes(app cloud.Router, s *cloud.Service[state]) {
	g := app.Group("/v1/content")
	o := contentOps{s: s}
	zip.Get(g, "/lifecycle", o.getLifecycle)
	zip.Get(g, "/board", o.getBoard)
	zip.Get(g, "/channels", o.getChannels)
	// DenyEnvelope renders a gate refusal as the money wire's own bytes — the
	// nested {"error":{"code","message"}} at 402/503 that every Hanzo resource
	// create emits — from the cloud.Denied error a typed op returns. Installed
	// BEFORE the leaves, because middleware runs in registration order.
	//
	// generate used to stay a RAW handler for exactly this envelope, on the belief
	// that a typed op can answer only its Out schema or zip's flat
	// {status,code,error}. cloud.Denied + this middleware is the client that closed
	// that gap (the same pairing apps/projects, apps/datasets and apps/risk use), so
	// the route is typed now and the body is unchanged — pinned both ways, by
	// TestGenerateIsATypedOp and TestGenerate402IsTheMoneyWireBody.
	g.Use(cloud.DenyEnvelope())
	// 402 is DECLARED so a generated client knows the refusal is a shape it can
	// read. It is never the status this op RETURNS — statusFor takes Statuses[0]
	// when the Out states none, so a success is 201; the 402 reaches the wire from
	// DenyEnvelope above.
	zip.Post(g, "/generate", o.postGenerate,
		zip.WithStatus(http.StatusCreated, http.StatusPaymentRequired))
	zip.Post(g, "/publish", o.postPublish)
	zip.Post(g, "/:doctype/:name/transition", o.postTransition)
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
	errPublishBusy        = errors.New("content: another publisher holds this item")
)

// ---- typed ops ----
//
// contentOps binds the service to content's typed ops. A TypedHandler is
// func(context.Context, *In) (*Out, error) — there is no parameter for the service —
// so it arrives as a RECEIVER and every op is a method value (o.getBoard), which is
// also the only bound form cmd/zipdoc can lift prose from.
type contentOps struct{ s *cloud.Service[state] }

// noInput is the In of an op addressed entirely by the caller's principal: it takes
// nothing off the wire. ONE of these for the whole package.
type noInput struct{}

// GetLifecycle returns the ONE marketing-content state machine: the ordered
// lifecycle states, which state a fresh document starts in, which one is publicly
// live, and the legal successors of every state. The console builds its board
// columns and its per-item action buttons from this single answer, so the UI and
// the write-time enforcement hook can never disagree about what is legal.
func (o contentOps) getLifecycle(ctx context.Context, _ *noInput) (*stateGraph, error) {
	if _, err := principal.Acting(ctx); err != nil {
		return nil, err
	}
	g := lifecycleGraph()
	return &g, nil
}

// boardQuery filters one read of the aggregate content board. Every field rides the
// query string and every one is optional.
type boardQuery struct {
	// Status keeps only items in one lifecycle state (draft, in_review, approved,
	// queued, published, archived). An undefined state is refused.
	Status string `json:"status"`
	// Project keeps only items in one brand/site sub-scope.
	Project string `json:"project"`
	// DocType keeps only one content type; omitted, the board spans every
	// publishable type. An unknown type is refused.
	DocType string `json:"doctype"`
	// Limit caps the rows returned, clamped to 1000. Defaults to 200, which is also
	// what a non-positive or unparseable value takes.
	Limit int `json:"limit"`
}

// boardPage is one read of the aggregate content board.
type boardPage struct {
	// Data is the matching items, most recently updated first.
	Data []boardItem `json:"data"`
	// Count is the number of rows in THIS page — never the org's total.
	Count int `json:"count"`
}

// GetBoard aggregates the caller org's marketing content across every publishable
// content type into ONE queue board — the cross-type read the framework's
// per-DocType list cannot give. It never fails on a partial outage: a content type
// the org has not installed, or one whose search errors, is skipped and logged
// rather than failing the whole board.
//
// Example: {"status": "queued", "limit": 50}
func (o contentOps) getBoard(ctx context.Context, in *boardQuery) (*boardPage, error) {
	org, err := principal.Acting(ctx)
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
		id, ok := publishable(only)
		if !ok {
			return nil, zip.ErrNotFound("unknown content type: " + only)
		}
		types = []framework.ID{id}
	}

	items := make([]boardItem, 0, limit)
	for _, dt := range types {
		if !framework.Installed(ctx, org, dt) {
			continue // honest skip — the org has not installed the marketing module
		}
		docs, err := framework.Search(ctx, org, dt, filters, limit)
		if err != nil {
			o.s.Log.Warn("board search degraded", "doctype", dt, "org", org, "err", err)
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
	return &boardPage{Data: items, Count: len(items)}, nil
}

// channelList is a brand's connected distribution channels.
type channelList struct {
	// Data is every social channel the caller's org has connected, disabled ones
	// included (Disabled says which).
	Data []Channel `json:"data"`
}

// GetChannels lists the distribution channels the caller's org has connected — the
// social integrations a publish can target. A deployment with no distribution edge
// wired answers 503 rather than an empty list that would read as "no channels".
func (o contentOps) getChannels(ctx context.Context, _ *noInput) (*channelList, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	chans, err := o.s.State.dist.Channels(ctx, org)
	if err != nil {
		return nil, opErr(err)
	}
	return &channelList{Data: chans}, nil
}

// Publish distributes one CMS content item to the channels recorded on it and
// returns the honest per-channel outcome. The item names itself — its caption,
// media and channel list are read from the stored document, not from this request.
// It is idempotent per channel (a channel already posted for this item is skipped),
// and a publish that loses the per-item lease to a live publisher answers status
// "in_progress" having posted nothing.
//
// Example: {"doctype": "marketing.SocialPost", "name": "spring-teaser"}
func (o contentOps) postPublish(ctx context.Context, in *PublishInput) (*PublishResult, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	body := *in
	body.DocType, body.Name = strings.TrimSpace(body.DocType), strings.TrimSpace(body.Name)
	if body.DocType == "" || body.Name == "" {
		return nil, zip.ErrBadRequest("doctype and name are required")
	}
	res, err := Publish(ctx, org, body)
	if err != nil {
		return nil, opErr(err)
	}
	return &res, nil
}

// transitionIn moves one content item to a new lifecycle state. doctype and name are
// path segments: the URL is the addressing authority, so they bind from there
// whatever a body says.
type transitionIn struct {
	// DocType is the content type to act on, from the path.
	DocType string `json:"doctype"`
	// Name is the document to act on, from the path.
	Name string `json:"name"`
	// To is the lifecycle state to move to. Required, and the move must be a legal
	// edge from the item's current state.
	To string `json:"to"`
	// ScheduleAt is an ISO-8601 go-live time handed to the channel's own scheduler;
	// "" distributes now.
	ScheduleAt string `json:"scheduleAt,omitempty"`
}

// PostTransition moves one content item to a new lifecycle state and, on the move to
// published, fans it out to the item's channels. The edge must be legal for the
// item's current state — an illegal move is refused with 409 — and the status write
// re-validates it at the storage boundary. Distribution is best effort: its honest
// state is reported on the result and a distribution failure never rolls the status
// change back.
//
// Example: {"doctype": "marketing.SocialPost", "name": "spring-teaser", "to": "published"}
func (o contentOps) postTransition(ctx context.Context, in *transitionIn) (*TransitionResult, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	doctype, name := decodeSeg(in.DocType), decodeSeg(in.Name)
	if _, ok := publishable(doctype); !ok {
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

// ---- handlers (free functions bound with cloud.Handle) ----

// Draft a piece of marketing content and file it in the CMS as a draft.
//
// Answers 201 with the created draft's identity — {doctype, name, status} — and the
// document itself lands in the CMS through the SAME validate and lifecycle-hook
// pipeline an ordinary create runs. This is a WRITE, not a preview: there is no
// dry-run, and every call that succeeds leaves a document behind.
//
// `doctype` picks which of two generation planes runs, and they are the only two.
// Campaign and SocialPost are drafted as brand COPY on the platform AI plane (zen5 by
// default, overridable per request with `model` or per deployment); Asset is a studio
// image render the AI plane never sees. Everything else about the call is identical.
//
// MONEY, metered in exactly one place per mode and never both. Copy rides the
// platform's own inference meter — the org's balance is authorised before the model
// call and debited at the exact token cost after — so content never re-bills it. A
// studio render is invisible to that meter, so content is the sole meter for it: the
// org is gated BEFORE the GPU compute and refused 402 when out of funds or over its
// spend cap, and the debit is recorded only once the render actually returns, because
// the billable event is the consumed compute and not the CMS row. `project` rides the
// BODY rather than a server-minted identity claim, so it attributes spend but a
// project-scoped cap stays soft on it — the org is the value that is enforced.
//
// The org is the caller's own, resolved once from the validated principal and never
// read from the body; a caller without one is refused 403. Status is not the
// generator's to choose: a generated item is ALWAYS a draft, and the storage-boundary
// hook enforces that a second time.
//
// It fails closed rather than inventing anything. An unknown content type is 404 and a
// deployment whose marketing module is not installed is 409 naming the install call.
// An AI plane or studio that is unconfigured or unreachable, a graph the studio
// rejects, and a render that does not return in time all degrade to 503 — never
// fabricated copy, never a fake render. A `source_media` that fails the SSRF and
// traversal validator is 400 raised before the billing gate and before the studio is
// contacted, so a hostile source never costs the caller anything.
func (o contentOps) postGenerate(ctx context.Context, in *GenerateInput) (*GenerateResult, error) {
	org, ok := principal.OrgFrom(ctx)
	if !ok {
		return nil, zip.ErrForbidden("valid principal required")
	}
	body := *in
	body.DocType = strings.TrimSpace(body.DocType)
	if body.DocType == "" {
		return nil, zip.ErrBadRequest("doctype is required")
	}
	res, err := Generate(ctx, org, body)
	if err != nil {
		// A studio-render billing denial is a funds/cap outcome, not a server fault.
		// cloud.Denied carries the money wire's own status and body as an ERROR —
		// the one refusal channel a typed op has — and cloud.DenyEnvelope (installed
		// on the group) writes those bytes back verbatim, so this answers the SAME
		// nested {"error":{"code","message"}} every Hanzo resource create emits
		// rather than reshaping it into zip's RFC 9457 problem members. Off the HTTP
		// path (MCP, CLI) deniedErr.Unwrap keeps the status and the sentence.
		if errors.Is(err, metering.ErrInsufficientBalance) || errors.Is(err, metering.ErrSpendCapExceeded) {
			return nil, cloud.Denied(err)
		}
		return nil, opErr(err)
	}
	return &res, nil
}

// ---- exported ops (the ONE implementation; handlers + connector both call these) ----

// TransitionResult is the outcome of a lifecycle move, including any distribution the
// transition triggered (nil when the target state does not distribute).
type TransitionResult struct {
	// DocType is the content type that moved — Campaign, SocialPost or Asset —
	// echoed from the path.
	DocType string `json:"doctype"`
	// Name is the document that moved, echoed from the path.
	Name string `json:"name"`
	// From is the state the item held when it was read. A document carrying no
	// status yet reads as "draft".
	From string `json:"from"`
	// To is the state it holds now. From == To on an idempotent re-transition,
	// which is legal and is where a caller that lost a publish race lands.
	To string `json:"to"`
	// Distribution is the channel fan-out this move triggered. Present ONLY on the
	// move to published, the single edge that distributes — so its absence means
	// no fan-out was attempted, never that one failed quietly. A fan-out that DID
	// fail is present carrying its own honest status, because distribution never
	// rolls the status change back.
	Distribution *PublishResult `json:"distribution,omitempty"`
	// Storefront is the catalog side effect, present only when a published Asset
	// was product imagery — it carries a design and a kind of ecom, product or
	// lifestyle. Absent for everything else, so absence reads as "not catalog
	// imagery" rather than "the catalog failed".
	Storefront *StorefrontResult `json:"storefront,omitempty"`
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
	id, ok := publishable(doctype)
	if !ok {
		return TransitionResult{}, errUnknownDocType
	}
	if !IsStatus(to) {
		return TransitionResult{}, fmt.Errorf("%w: %q", errUnknownStatus, to)
	}

	// On the ONE edge that distributes, the item's publish lease covers this WHOLE
	// function — read, edge-check, status write and fan-out — not just the fan-out.
	//
	// The status write below carries the entire document, external_ids included, and
	// it cannot carry less: UpdateData replaces the document, so a field left out of
	// the map is deleted rather than preserved. The snapshot it writes is read at the
	// top of this function, BEFORE any fan-out. Two concurrent transitions to
	// published therefore interleave as: both read an empty skip-set, A publishes and
	// records its external_ids, B's stale snapshot writes that skip-set back to empty,
	// and B's fan-out — itself correctly leased, correctly re-reading — finds nothing
	// to skip and posts the item a second time. The lease inside Publish cannot see
	// this, because the erasure happens outside it. Measured at ~7% of runs before
	// this widened, which is a gate that reddens at random rather than a bug anyone
	// could reproduce on demand.
	//
	// Holding it here makes the loser's read happen after the winner's record: it sees
	// status=published (a legal no-op edge, CanTransition returns true for from==to),
	// re-stamps the same status, and its fan-out skips every channel already on record.
	// One post, both callers succeed.
	if entersDistribution(to) {
		lease, ok, err := framework.AcquireLease(ctx, org, publishLeaseKey(doctype, name), publishLeaseTTL, publishLeaseWait)
		if err != nil {
			return TransitionResult{}, err
		}
		if !ok {
			// A live publisher held the item for the whole wait window. Refusing is the
			// honest answer and the safe one: this call cannot write the document without
			// erasing ids that publisher is still recording, so it writes nothing and says
			// so. 409, retryable — the caller re-transitions and takes the no-op path.
			return TransitionResult{}, errPublishBusy
		}
		defer func() { _ = lease.Release(ctx) }()
	}

	doc, err := framework.Get(ctx, org, id, name)
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
	if err := framework.UpdateData(withTrustedWrite(ctx), org, id, name, data); err != nil {
		return TransitionResult{}, err
	}

	res := TransitionResult{DocType: doctype, Name: name, From: from, To: to}
	if entersDistribution(to) {
		// publishHeld, not Publish: this call already holds the item's publish lease
		// (above), and the lease is not reentrant.
		pr, perr := publishHeld(ctx, org, PublishInput{DocType: doctype, Name: name, ScheduleAt: scheduleAt})
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
	// DocType is which content type the row came from: Campaign, SocialPost or
	// Asset. The board spans all three at once, so this is what tells them apart.
	DocType string `json:"doctype"`
	// Name is the document within that type. (doctype, name) is the pair every
	// /v1/content write addresses an item by.
	Name string `json:"name"`
	// Title is the item's headline, read from its type's own title field. Empty
	// for a document that has none.
	Title string `json:"title"`
	// Status is the lifecycle state: draft, in_review, approved, queued, published
	// or archived. It decides what a reader may see — the public site pulls
	// exactly "published" and nothing else — so it is a visibility fact, not a
	// workflow label.
	Status string `json:"status"`
	// Project is the brand/site sub-scope within the org. Absent for an item held
	// at org level rather than under one brand.
	Project string `json:"project,omitempty"`
	// UpdatedAt is unix seconds of the document's last write, and the key the
	// board sorts on, newest first.
	UpdatedAt int64 `json:"updatedAt"`
}

func boardItemFrom(dt framework.ID, d framework.Document) boardItem {
	return boardItem{
		DocType:   dt.String(),
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
	maps.Copy(out, in)
	return out
}

func dataString(m map[string]any, key string) string {
	s, _ := m[key].(string)
	return s
}

// configured reports whether an edge was supplied at all — for the mount log line.
// Each real edge fail-closes per call, so being present is the whole question.
func configured(x any) bool { return x != nil }

// limitOf clamps a caller's requested page size to 1..1000, taking def for a
// non-positive one. It reads a NUMBER rather than a request because zip's URL binder
// already turns `?limit=` into the field — and leaves it at zero for a value it
// cannot parse, which is the same "take the default" this has always meant.
func limitOf(n, def int) int {
	if n <= 0 {
		return def
	}
	if n > 1000 {
		return 1000
	}
	return n
}

// decodeSeg percent-decodes a URL path segment (a content type or a document name),
// matching framework's own path decoding so a name with a reserved character
// addresses its stored value.
func decodeSeg(raw string) string {
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
	case errors.Is(err, errPublishBusy):
		// Same 409 the illegal edge answers with, for the same reason: the request
		// conflicts with the item's current state. Nothing was written and nothing was
		// posted, so retrying is safe and is the expected response.
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
