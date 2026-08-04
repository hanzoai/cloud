package company

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/hanzoai/cloud/openapi"
	"github.com/zap-proto/zip"
)

// The two routes on this surface that cannot be typed ops still have to be
// DESCRIBED. "Cannot carry a zip registry entry" was being read as "publishes
// nothing", and those are opposite facts: both of these rendered as an
// operationId and a tag and no body at all, which is exactly what a route that
// takes no input and returns none publishes. No consumer of the document can tell
// the two apart, so every SDK generated off openapi.yaml offered a deck upload
// with nowhere to put the deck and no return type for either call.
//
// openapi.Register is the seam for the halves that ARE statable — it attaches to
// a route the router already carries, so it can never contradict the router — and
// it does not make these typed ops: there is still no MCP tool, no CLI command and
// no SDK method, because those come from zip's registry. See typed_wire_test.go
// for why each one stays out of it.
//
// What is still NOT declared here, honestly — each a missing capability, not a
// missing edit, so none of them is papered over with prose that overstates:
//
//   - the deck's `?name=` query parameter. The untyped projection derives PATH
//     parameters from the router and has no vocabulary for query.
//   - /payment's 402/503 denial bodies. apply states the success shape under the
//     "2XX" range key only; the denial needs the same declarable-error-body
//     capability that keeps this route untyped in the first place.
//   - field prose on the shapes below. zipdoc lifts doc comments off TYPED ops
//     only, and Register's reflection seam reads Go types, not comments — so
//     deckOut publishes `documentId: string` with no description. formationView
//     is unaffected: it is already described through the typed ops that share it.
func init() {
	openapi.Register("/v1/company/fundraise/deck", "POST", openapi.Binary{}, deckOut{})
	openapi.Describe("/v1/company/fundraise/deck", "POST",
		"Share a pitch deck in the org's data room",
		"Stores the request body as a document in the caller org's data room and "+
			"answers with the data room id to reference it by. The deck is RAW BYTES of "+
			"whatever content type is sent — a PDF, a slide export — not a JSON document: "+
			"the Content-Type header is carried through to the data room as given, and "+
			"`?name=` names the document, defaulting to `pitch-deck`.\n\n"+
			"Scoped to the caller's validated org, and only after incorporation: a "+
			"formation still short of stage `company` is refused 409 and an org that never "+
			"began one is 404. The route is registered AHEAD of the surface's JSON body "+
			"cap deliberately, so a deck's size ceiling is the edge's rather than the "+
			"cap meant for small structured records. An empty body is 400; a data room "+
			"that will not take the bytes is 502.")
	openapi.Register("/v1/company/payment", "POST", nil, formationView{})
	openapi.Describe("/v1/company/payment", "POST",
		"Charge the one-time formation fee and mark the formation paid",
		"Bills the caller's own org the one-time Hanzo Company formation fee — $999 "+
			"unless the deployment sets another — and answers with the formation record "+
			"carrying its paid flag and the charge reference. Takes no body: the org is "+
			"the validated tenant and the amount is the platform's, never the caller's to "+
			"assert.\n\n"+
			"IDEMPOTENT on the formation rather than on the request: an already-paid "+
			"formation answers 200 with the same record and is not charged again, so a "+
			"retry or a double-clicked button costs nothing. Available only at the "+
			"`payment` stage (409 anywhere else) and only for an org that has begun a "+
			"formation (404 otherwise).\n\n"+
			"A refused charge answers the fleet-wide billing contract, not a formation "+
			"error — 402 when the org cannot pay, 503 when metering is unavailable — "+
			"which is exactly why this route is not a typed op.")
}

// company.go mounts the /v1/company surface and wires the state machine to its
// provider seams. The design is decomplected: ACTION endpoints populate the
// formation's data (structure, founders, KYC, payment, documents, esign, genesis,
// import), and ONE transition door — POST /v1/company/advance {to} — runs the
// guarded machine (Advance). Side effects live in the actions; ordering + gates live
// in the machine; the two never braid.
//
// Surface (all org-scoped; /v1 only):
//
//	POST   /v1/company                     begin a formation (201 new, 200 existing)
//	GET    /v1/company                     the formation + next stages
//	PUT    /v1/company/structure           set structure/jurisdiction/name
//	POST   /v1/company/founders            set founders
//	POST   /v1/company/kyc                 start founder KYC (idv seam)
//	POST   /v1/company/kyc/refresh         reconcile founder KYC with the wired provider
//	POST   /v1/company/kyc/decision        reviewer decision on a founder {email,status}
//	POST   /v1/company/payment             charge the $999 formation fee
//	POST   /v1/company/documents           generate formation docs → data room + file
//	POST   /v1/company/esign               request signatures on the docs
//	POST   /v1/company/esign/complete      record signing complete
//	POST   /v1/company/genesis             seed cap table + anchor equity genesis on-chain
//	POST   /v1/company/advance             {to} run the next guarded transition
//	POST   /v1/company/skip                mark already-incorporated (enables import)
//	POST   /v1/company/import/documents    ingest a Drive folder → data room
//	POST   /v1/company/import/captable     ingest a Sheet → captable
//	POST   /v1/company/fundraise/round     record a fundraising round (captable)
//	POST   /v1/company/fundraise/deck      share a deck in the data room
//	POST   /v1/company/fundraise/safe      request signature on a SAFE/note
//
// Every route above is a TYPED op — one registry entry projected to the OpenAPI
// operation, the MCP tool, the CLI command and the generated SDK — except two,
// which cannot be typed without moving the wire and are named at their
// registration in routes(): POST /payment and POST /fundraise/deck.

// maxBody caps a JSON request body. Formation payloads are small structured
// records, so a megabyte is generous; it is enforced ONCE, as the group
// middleware limitBody, rather than re-checked in every handler. The deck upload
// carries document BYTES and is registered ahead of it, so it keeps the edge's
// own ceiling exactly as it always had.
const maxBody = 1 << 20 // 1 MiB

type state struct {
	store *Store
	prov  providerSet
}

var mounted *cloud.Service[state]

// Mount wires the company surface. It keeps a package global for Shutdown, so it
// constructs the Service value directly (the "complex flavour").
func Mount(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("company.Mount: nil app")
	}
	if deps.Logger == nil {
		return fmt.Errorf("company.Mount: nil deps.Logger")
	}
	if deps.DataDir == "" {
		return fmt.Errorf("company.Mount: empty DataDir")
	}
	// A typed op is a route PLUS a registry entry, and the registry lives on the
	// App. A router that cannot reach it would serve every route with no schema,
	// no prose, no MCP tool and no SDK method — so the mount FAILS rather than
	// quietly publishing a surface no projection knows about.
	zapp := cloud.ZipApp(app)
	if zapp == nil {
		return fmt.Errorf("company.Mount: router is not a zip app, so the typed ops have no registry")
	}
	store, err := openStore(deps.DataDir)
	if err != nil {
		return fmt.Errorf("company.Mount: open store: %w", err)
	}
	// Resolve the founder-KYC provider from config (fail-closed: a named-but-
	// misconfigured provider fails the mount, never silently downgrades to manual).
	kyc, err := resolveKYC(deps)
	if err != nil {
		return fmt.Errorf("company.Mount: kyc provider: %w", err)
	}
	b := cloud.NewBase(deps, "company")
	s := &cloud.Service[state]{Base: b, State: state{
		store: store,
		prov: providerSet{
			kyc:      kyc,
			charge:   resourceCharger{bill: b.Bill},
			docs:     dataroomSink{},
			esign:    stubEsign{},
			captable: captableAdapter{},
			anchor:   newEVMAnchor(b.Log),
			filing:   stubFiling{},
			upgrade:  captableUpgrader{},
			google:   newHTTPGoogle(),
		},
	}}
	mounted = s
	routes(app, zapp, s)
	b.Log.Info("company mounted", "brand", deps.Brand, "feeCents", feeCents())
	return nil
}

// zipdoc lifts the doc comment off each typed op and its In/Out fields into
// zipdoc_gen.go, which is the ONLY way that prose reaches the published document
// and the MCP tool list — Go drops comments at compile time. Run by `make describe`.
//
//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

func routes(app cloud.Router, zapp *zip.App, s *cloud.Service[state]) {
	o := ops{s: s}
	g := app.Group("/v1/company")
	// cloud.Bridge is installed by whoever composes the app — the fused host at
	// its root — never here. The root install is also what carries begin's 201
	// back out (cloud.Created writes into the slot that install parked).

	// UNTYPED, and registered HERE so the body cap below never applies to it: the
	// deck is document BYTES, not JSON, and its size ceiling has always been the
	// edge's. Typing it would mean declaring a JSON body it does not take — the
	// decoder would refuse a PDF with 400. Pinned by TestDeckTakesRawBytes.
	g.Post("/fundraise/deck", cloud.Handle(s, fundraiseDeck))

	// The JSON body cap, in ONE place instead of once per handler. Registered
	// after the deck leaf (which is therefore not gated by it) and before every
	// JSON leaf, because fiber runs middleware in registration order.
	g.Use(zip.H(limitBody))

	// The root of the surface. Declared on the App with its whole path, not on the
	// group with an empty leaf: joining "/v1/company" with "" yields
	// "/v1/company/", a DIFFERENT path from the one these two have always served.
	zip.Post(zapp, "/v1/company", o.begin)
	zip.Get(zapp, "/v1/company", o.get)

	// The platform's own book — SuperAdmin operations, cross-tenant, read-only.
	// Registered before the tenant edges so the static paths are unambiguous.
	zip.Get(g, "/register", o.registerList)
	zip.Get(g, "/register/summary", o.registerSummary)
	zip.Get(g, "/review", o.registerReview)
	zip.Put(g, "/structure", o.setStructure)
	zip.Post(g, "/founders", o.setFounders)
	zip.Post(g, "/kyc", o.startKYC)
	zip.Post(g, "/kyc/refresh", o.kycRefresh)
	zip.Post(g, "/kyc/decision", o.kycDecision)
	// UNTYPED: a billing denial answers the fleet-wide 402/503 contract
	// (cloud.DenyResource — {"error":{"code","message"}}), a body zip's error type
	// cannot express. Typing it would silently reshape that error for every
	// metered client, so it stays a raw handler until zip errors can carry a body.
	// Pinned by TestPaymentDenialWire, so that reason is a wire, not a comment.
	g.Post("/payment", cloud.Handle(s, pay))
	zip.Post(g, "/documents", o.generateDocuments)
	zip.Post(g, "/esign", o.requestEsign)
	zip.Post(g, "/esign/complete", o.completeEsign)
	zip.Post(g, "/genesis", o.recordGenesis)
	zip.Post(g, "/advance", o.advance)
	zip.Post(g, "/skip", o.skip)
	zip.Post(g, "/import/documents", o.importDocuments)
	zip.Post(g, "/import/captable", o.importCapTable)
	zip.Post(g, "/fundraise/round", o.fundraiseRound, zip.WithStatus(http.StatusCreated))
	zip.Post(g, "/fundraise/safe", o.fundraiseSafe, zip.WithStatus(http.StatusCreated))
}

// limitBody refuses a request body larger than maxBody with the same 413 every
// handler in this package used to raise for itself. One policy, one place: a
// typed op never sees the request, and a cap re-implemented per handler is a cap
// that eventually differs per handler.
func limitBody(c *zip.Ctx) error {
	if len(c.Fiber().Body()) > maxBody {
		return zip.Errorf(http.StatusRequestEntityTooLarge, "request body too large")
	}
	return c.Continue()
}

// Shutdown closes the store. Idempotent. Matches cloud.ShutdownFunc so Wire can
// reference it directly (like captable/dataroom/sign).
func Shutdown(context.Context) error {
	if mounted == nil || mounted.State.store == nil {
		return nil
	}
	err := mounted.State.store.Close()
	mounted = nil
	return err
}

// ---- shared helpers ----

// ops binds the service to the typed ops. A TypedHandler is
// func(context.Context, *In) (*Out, error) — no parameter for the service — so it
// arrives as a RECEIVER and every op is a method value (o.begin), which is also
// the only bound form cmd/zipdoc can lift prose from.
type ops struct{ s *cloud.Service[state] }

// genID returns a prefixed, collision-resistant id (prefix + 128 random bits).
func genID(prefix string) (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return prefix + "_" + hex.EncodeToString(b[:]), nil
}

// feeCents is the formation fee, overridable by ops via CLOUD_COMPANY_FEE_CENTS.
func feeCents() int64 {
	if v := strings.TrimSpace(os.Getenv("CLOUD_COMPANY_FEE_CENTS")); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n >= 0 {
			return n
		}
	}
	return formationFeeCents
}

// tenant is the VALIDATED org for this request — the one the gateway asserted and
// cloud.Bridge parked on the context, never a field of In. An In field is
// caller-supplied, so a tenant key read from one is a cross-tenant read the
// caller asserted for itself. It fails closed off the HTTP path, where nothing
// parked an org.
func tenant(ctx context.Context) (string, error) {
	org, ok := principal.OrgFrom(ctx)
	if !ok {
		return "", zip.ErrForbidden("X-Org-Id required")
	}
	return org, nil
}

// load resolves the caller's org and loads its formation, or returns the right error.
func load(ctx context.Context, s *cloud.Service[state]) (*Formation, string, error) {
	org, err := tenant(ctx)
	if err != nil {
		return nil, "", err
	}
	f, err := s.State.store.Get(ctx, org)
	if errors.Is(err, errNotFound) {
		return nil, org, zip.ErrNotFound("no formation for this org — POST /v1/company to begin")
	}
	if err != nil {
		return nil, org, zip.Errorf(http.StatusInternalServerError, "load: %v", err)
	}
	return f, org, nil
}

func save(ctx context.Context, s *cloud.Service[state], f *Formation) error {
	f.UpdatedAt = time.Now().Unix()
	if err := s.State.store.Put(ctx, f); err != nil {
		return zip.Errorf(http.StatusInternalServerError, "save: %v", err)
	}
	return nil
}

// requireStage returns a 409 if the formation is not at one of the allowed stages —
// so an action can only run at the step it belongs to.
func requireStage(f *Formation, allowed ...Stage) error {
	for _, s := range allowed {
		if f.Stage == s {
			return nil
		}
	}
	return zip.Errorf(http.StatusConflict, "action not available at stage %q", f.Stage)
}

// noInput is the In of an op that takes nothing off the wire — it is addressed
// entirely by the caller's validated principal.
type noInput struct{}

// formationView is a formation plus the machine's out-edges, for the UI to render
// "what's next". It is what every action on the formation answers with.
type formationView struct {
	// Formation is the org's one incorporation record.
	Formation *Formation `json:"formation"`
	// NextStages are the stages reachable from the formation's current stage,
	// whether or not their guards are satisfied yet.
	NextStages []string `json:"nextStages"`
}

// view renders a formation plus the machine's out-edges for the UI.
func view(f *Formation) *formationView {
	next := NextStages(f)
	ns := make([]string, len(next))
	for i, s := range next {
		ns[i] = string(s)
	}
	return &formationView{Formation: f, NextStages: ns}
}

// ---- begin / get ----

// beginIn opens a formation. Every field is optional here — the structure gate is
// PUT /v1/company/structure, and this only records what the caller already knows.
type beginIn struct {
	// Structure is the legal entity to form: c-corp, llc or dao-llc.
	Structure Structure `json:"structure"`
	// Jurisdiction is the state of formation: DE or WY.
	Jurisdiction Jurisdiction `json:"jurisdiction"`
	// Name is the proposed company name.
	Name string `json:"name"`
	// AlreadyIncorporated declares an org that already has an entity, which takes
	// the import path (POST /v1/company/skip) instead of the formation path.
	AlreadyIncorporated bool `json:"alreadyIncorporated"`
}

// Begin starts the org's one formation and returns it with the stages reachable
// from it. It is idempotent: an org that already has a formation gets that one
// back with 200, while a first call creates it and answers 201.
//
// Example: {"structure": "c-corp", "jurisdiction": "DE", "name": "Acme Inc."}
func (o ops) begin(ctx context.Context, in *beginIn) (*formationView, error) {
	org, err := tenant(ctx)
	if err != nil {
		return nil, err
	}
	if existing, err := o.s.State.store.Get(ctx, org); err == nil {
		return view(existing), nil // idempotent: one formation per org
	} else if !errors.Is(err, errNotFound) {
		return nil, zip.Errorf(http.StatusInternalServerError, "load: %v", err)
	}
	now := time.Now().Unix()
	f := &Formation{
		Org: org, Stage: StageStructure,
		Structure: in.Structure, Jurisdiction: in.Jurisdiction, Name: strings.TrimSpace(in.Name),
		AlreadyIncorporated: in.AlreadyIncorporated,
		CreatedAt:           now, UpdatedAt: now,
	}
	if err := save(ctx, o.s, f); err != nil {
		return nil, err
	}
	// 201 only on the CREATE branch — the idempotent return above answers 200,
	// which is the correct REST distinction and the reason this op cannot declare
	// a single zip.WithStatus. See LLM.md: the conditional-status class waits for
	// multi-status responses in zip.
	cloud.Created(ctx)
	return view(f), nil
}

// Get returns the caller org's formation and the stages reachable from it, or 404
// when the org has not begun one.
func (o ops) get(ctx context.Context, _ *noInput) (*formationView, error) {
	f, _, err := load(ctx, o.s)
	if err != nil {
		return nil, err
	}
	return view(f), nil
}

// ---- structure / founders ----

// structureIn names the entity to form. All three fields are required and
// validated against the closed vocabularies the machine accepts.
type structureIn struct {
	// Structure is the legal entity: c-corp, llc or dao-llc.
	Structure Structure `json:"structure"`
	// Jurisdiction is the state of formation: DE or WY.
	Jurisdiction Jurisdiction `json:"jurisdiction"`
	// Name is the proposed company name.
	Name string `json:"name"`
}

// SetStructure records the entity kind, the state of formation and the proposed
// name. Available only at the structure stage; an unknown structure or
// jurisdiction, or an empty name, is refused with 400.
//
// Example: {"structure": "c-corp", "jurisdiction": "DE", "name": "Acme Inc."}
func (o ops) setStructure(ctx context.Context, in *structureIn) (*formationView, error) {
	f, _, err := load(ctx, o.s)
	if err != nil {
		return nil, err
	}
	if err := requireStage(f, StageStructure); err != nil {
		return nil, err
	}
	if !validStructures[in.Structure] {
		return nil, zip.ErrBadRequest("structure must be one of c-corp, llc, dao-llc")
	}
	if !validJurisdictions[in.Jurisdiction] {
		return nil, zip.ErrBadRequest("jurisdiction must be DE or WY")
	}
	if strings.TrimSpace(in.Name) == "" {
		return nil, zip.ErrBadRequest("name is required")
	}
	f.Structure, f.Jurisdiction, f.Name = in.Structure, in.Jurisdiction, strings.TrimSpace(in.Name)
	if err := save(ctx, o.s, f); err != nil {
		return nil, err
	}
	return view(f), nil
}

// foundersIn is the founding cap table, in full: the list REPLACES any founders
// already recorded.
type foundersIn struct {
	// Founders is every founding stakeholder. Each needs a name and an email, and
	// equityBps between 0 and 10000 (1% == 100 bps).
	Founders []Founder `json:"founders"`
}

// SetFounders replaces the formation's founders. Each founder needs a name, an
// email and an equity share in basis points; every founder is (re)set to pending
// KYC, so a previously settled decision does not survive a change of the list.
//
// Example: {"founders": [{"name": "Ada", "email": "ada@acme.com", "equityBps": 10000}]}
func (o ops) setFounders(ctx context.Context, in *foundersIn) (*formationView, error) {
	f, _, err := load(ctx, o.s)
	if err != nil {
		return nil, err
	}
	if err := requireStage(f, StageStructure, StageFounders); err != nil {
		return nil, err
	}
	if len(in.Founders) == 0 {
		return nil, zip.ErrBadRequest("at least one founder is required")
	}
	founders := make([]Founder, 0, len(in.Founders))
	for _, fo := range in.Founders {
		if strings.TrimSpace(fo.Email) == "" || strings.TrimSpace(fo.Name) == "" {
			return nil, zip.ErrBadRequest("each founder needs a name and email")
		}
		if fo.EquityBps < 0 || fo.EquityBps > 10000 {
			return nil, zip.ErrBadRequest("equityBps must be between 0 and 10000")
		}
		founders = append(founders, Founder{
			Name: strings.TrimSpace(fo.Name), Email: strings.TrimSpace(fo.Email),
			EquityBps: fo.EquityBps, KYCStatus: KYCPending,
		})
	}
	f.Founders = founders
	if err := save(ctx, o.s, f); err != nil {
		return nil, err
	}
	return view(f), nil
}

// ---- KYC (idv seam) ----

// kycSession is one founder's identity-verification session, as the wired
// provider opened it.
type kycSession struct {
	// Email is the founder the session belongs to.
	Email string `json:"email"`
	// Ref is the provider's reference for the session.
	Ref string `json:"ref"`
	// VerifyURL is the hosted flow the founder visits; empty for the manual provider.
	VerifyURL string `json:"verifyUrl"`
	// Status is the session's status at start, which is always pending.
	Status string `json:"status"`
}

// kycStartOut reports the sessions opened, plus the formation they belong to.
type kycStartOut struct {
	// Provider is the wired identity-verification provider's name.
	Provider string `json:"provider"`
	// Sessions is one entry per founder, in the order the founders are recorded.
	Sessions []kycSession `json:"sessions"`
	// Formation is the org's incorporation record, with each founder's session
	// reference and status recorded on it.
	Formation *Formation `json:"formation"`
}

// StartKYC opens an identity-verification session for every founder with the
// wired provider and records each session's reference on the formation.
//
// A start is never a decision: any terminal status the provider reports at
// inquiry time is clamped back to pending, so the payment gate can never open
// here. A terminal status arrives only from POST /v1/company/kyc/refresh (the
// provider) or POST /v1/company/kyc/decision (a Hanzo platform reviewer).
func (o ops) startKYC(ctx context.Context, _ *noInput) (*kycStartOut, error) {
	f, org, err := load(ctx, o.s)
	if err != nil {
		return nil, err
	}
	if err := requireStage(f, StageFounders); err != nil {
		return nil, err
	}
	if len(f.Founders) == 0 {
		return nil, zip.ErrBadRequest("add founders before starting KYC")
	}
	sessions := make([]kycSession, 0, len(f.Founders))
	for i := range f.Founders {
		ref, url, status, err := o.s.State.prov.kyc.Start(ctx, org, f.Founders[i])
		if err != nil {
			return nil, zip.Errorf(http.StatusBadGateway, "kyc start for %s: %v", f.Founders[i].Email, err)
		}
		// A start is never a decision: clamp any terminal status a provider returns at
		// inquiry time back to pending (belt-and-suspenders over the idv seam's own
		// downgrade), so the payment gate can never open at start. A terminal status
		// arrives only via kycRefresh (provider) or kycDecision (reviewer), each of
		// which records a decider.
		if status == KYCVerified || status == KYCReviewerConfirmed || status == KYCFailed {
			status = KYCPending
		}
		f.Founders[i].KYCRef = ref
		f.Founders[i].KYCStatus = status
		sessions = append(sessions, kycSession{Email: f.Founders[i].Email, Ref: ref, VerifyURL: url, Status: status})
	}
	if err := save(ctx, o.s, f); err != nil {
		return nil, err
	}
	return &kycStartOut{Provider: o.s.State.prov.kyc.Name(), Sessions: sessions, Formation: f}, nil
}

// kycRefreshOut is the formation after reconciliation, with the provider that
// answered.
type kycRefreshOut struct {
	// Provider is the identity-verification provider that was consulted.
	Provider string `json:"provider"`
	// Formation is the org's incorporation record with each founder's reconciled status.
	Formation *Formation `json:"formation"`
}

// RefreshKYC reconciles each pending founder's KYC with the WIRED provider — the
// PULL path to a provider-reported terminal status. For the manual provider the
// check stays pending; for a real provider it reflects the settled decision,
// ATTRIBUTED to the provider.
//
// It NEVER trusts a client-asserted status — the status comes from the provider
// seam — so a client cannot force a pass here, and an already-passing founder
// (e.g. a reviewer confirmation) is left untouched.
func (o ops) kycRefresh(ctx context.Context, _ *noInput) (*kycRefreshOut, error) {
	f, _, err := load(ctx, o.s)
	if err != nil {
		return nil, err
	}
	if err := requireStage(f, StageFounders); err != nil {
		return nil, err
	}
	provider := o.s.State.prov.kyc.Name()
	changed := false
	for i := range f.Founders {
		fo := &f.Founders[i]
		if fo.KYCRef == "" || kycPass(*fo) {
			continue // not started, or already settled to a pass — never overwrite a pass
		}
		status, err := o.s.State.prov.kyc.Check(ctx, fo.KYCRef)
		if err != nil {
			return nil, zip.Errorf(http.StatusBadGateway, "kyc check for %s: %v", fo.Email, err)
		}
		// Only a provider PASS or FAIL settles the founder; anything else stays pending.
		// A pass is attributed to the provider that reported it.
		switch status {
		case KYCVerified:
			fo.KYCStatus, fo.DecidedBy, changed = KYCVerified, provider, true
		case KYCFailed:
			fo.KYCStatus, fo.DecidedBy, changed = KYCFailed, provider, true
		}
	}
	if changed {
		if err := save(ctx, o.s, f); err != nil {
			return nil, err
		}
	}
	return &kycRefreshOut{Provider: provider, Formation: f}, nil
}

// decisionIn is a reviewer's manual decision on one founder's KYC.
type decisionIn struct {
	// Email identifies the founder on the formation.
	Email string `json:"email"`
	// Status is the decision: reviewer_confirmed or failed. Nothing else is accepted.
	Status string `json:"status"`
}

// DecideKYC records a privileged reviewer's MANUAL decision on a founder's KYC —
// the human-in-the-loop path, and the ONLY route to a pass when no real provider
// is wired. It produces a DISTINCT reviewer_confirmed, never a provider
// "verified".
//
// Because Hanzo forms the entity and carries the formation KYC/AML obligation,
// the reviewer is a HANZO platform reviewer (SuperAdmin), and the decision is
// ATTRIBUTED to them.
//
// Example: {"email": "ada@acme.com", "status": "reviewer_confirmed"}
func (o ops) kycDecision(ctx context.Context, in *decisionIn) (*formationView, error) {
	f, _, err := load(ctx, o.s)
	if err != nil {
		return nil, err
	}
	who, ok := reviewer(ctx)
	if !ok {
		return nil, zip.ErrForbidden("a founder KYC decision requires a Hanzo platform reviewer")
	}
	if who == "" {
		return nil, zip.ErrForbidden("a founder KYC decision requires a signed-in reviewer")
	}
	status := strings.ToLower(strings.TrimSpace(in.Status))
	if status != KYCReviewerConfirmed && status != KYCFailed {
		return nil, zip.ErrBadRequest("status must be reviewer_confirmed or failed")
	}
	email := strings.TrimSpace(in.Email)
	found := false
	for i := range f.Founders {
		if f.Founders[i].Email == email {
			f.Founders[i].KYCStatus, f.Founders[i].DecidedBy = status, who
			found = true
		}
	}
	if !found {
		return nil, zip.ErrNotFound("no founder with that email")
	}
	if err := save(ctx, o.s, f); err != nil {
		return nil, err
	}
	return view(f), nil
}

// ---- payment (the $999 gate) ----

// pay charges the one-time formation fee. It is the one action on this surface
// that is NOT a typed op: a denial answers the fleet-wide billing contract
// (cloud.DenyResource — 402 insufficient_balance / spend_cap_exceeded, 503
// balance_unavailable, each a {"error":{"code","message"}} body), and zip's error
// type renders a flat {status,code,error}. Typing it would reshape that error for
// every metered client, so it stays a raw handler — see routes().
//
// The gate is the LAST thing it does, after the stage check and the paid
// short-circuit, so a caller the machine is about to refuse is never charged.
// That ordering is why the gate cannot lift into middleware, where it would run
// first. Both facts are pinned: TestPaymentDenialWire, TestPaymentChargesLast.
func pay(s *cloud.Service[state], c *zip.Ctx) error {
	ctx := c.Context()
	f, org, err := load(ctx, s)
	if err != nil {
		return err
	}
	if err := requireStage(f, StagePayment); err != nil {
		return err
	}
	if f.Paid {
		return c.JSON(http.StatusOK, view(f)) // idempotent — already paid
	}
	ref, err := s.State.prov.charge.Charge(ctx, org, feeCents(), "Hanzo Company formation fee")
	if err != nil {
		// Map the metering error to the canonical 402/503 billing contract.
		return cloud.DenyResource(c, err)
	}
	f.Paid, f.PaymentRef = true, ref
	if err := save(ctx, s, f); err != nil {
		return err
	}
	return c.JSON(http.StatusOK, view(f))
}

// ---- documents (generate → data room + state filing) ----

// GenerateDocuments renders the formation documents for the chosen structure and
// jurisdiction, ingests each into the org's data room, and submits the state
// filing through the filing seam.
//
// With no filing partner wired the filing is recorded honestly as "manual" — no
// filing id is fabricated. Available only at the documents stage.
func (o ops) generateDocuments(ctx context.Context, _ *noInput) (*formationView, error) {
	f, org, err := load(ctx, o.s)
	if err != nil {
		return nil, err
	}
	if err := requireStage(f, StageDocuments); err != nil {
		return nil, err
	}
	docs, err := renderFormationDocs(f)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "render docs: %v", err)
	}
	ids := make([]string, 0, len(docs))
	for _, d := range docs {
		id, err := o.s.State.prov.docs.Ingest(ctx, org, d.Name, d.ContentType, d.Data)
		if err != nil {
			return nil, zip.Errorf(http.StatusBadGateway, "data room ingest %s: %v", d.Name, err)
		}
		ids = append(ids, id)
	}
	f.DocumentIDs = ids
	// Submit the state filing (honest stub records "manual" — no fake filing).
	filing, err := o.s.State.prov.filing.Submit(ctx, f)
	if err != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "state filing: %v", err)
	}
	f.Filing = filing
	if err := save(ctx, o.s, f); err != nil {
		return nil, err
	}
	return view(f), nil
}

// ---- esign ----

// esignOut is the signature request, plus the formation it was raised against.
type esignOut struct {
	// Provider is the wired e-signature provider's name.
	Provider string `json:"provider"`
	// EsignRef is the provider's reference for the signature request.
	EsignRef string `json:"esignRef"`
	// Formation is the org's incorporation record with the reference recorded on it.
	Formation *Formation `json:"formation"`
}

// RequestEsign sends the generated formation documents for signature by every
// founder and records the provider's reference on the formation. Available only
// at the esign stage.
func (o ops) requestEsign(ctx context.Context, _ *noInput) (*esignOut, error) {
	f, org, err := load(ctx, o.s)
	if err != nil {
		return nil, err
	}
	if err := requireStage(f, StageEsign); err != nil {
		return nil, err
	}
	signers := make([]Signer, 0, len(f.Founders))
	for _, fo := range f.Founders {
		signers = append(signers, Signer{Name: fo.Name, Email: fo.Email})
	}
	ref, err := o.s.State.prov.esign.Request(ctx, org, f.DocumentIDs, signers)
	if err != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "esign request: %v", err)
	}
	f.EsignRef = ref
	if err := save(ctx, o.s, f); err != nil {
		return nil, err
	}
	return &esignOut{Provider: o.s.State.prov.esign.Name(), EsignRef: ref, Formation: f}, nil
}

// esignCompleteIn optionally overrides the provider's own answer.
type esignCompleteIn struct {
	// Signed, when present, overrides what the provider reports — the manual path
	// for a provider whose webhook is not wired. Omit it to take the provider's answer.
	Signed *bool `json:"signed"`
}

// CompleteEsign records whether the formation documents have been signed. It
// consults the e-signature provider, which a real provider's webhook drives; the
// signal is idempotent.
//
// An explicit `signed` in the request overrides the provider's answer, which is
// the manual path for the stub provider that never self-completes.
//
// Example: {"signed": true}
func (o ops) completeEsign(ctx context.Context, in *esignCompleteIn) (*formationView, error) {
	f, org, err := load(ctx, o.s)
	if err != nil {
		return nil, err
	}
	if err := requireStage(f, StageEsign); err != nil {
		return nil, err
	}
	if f.EsignRef == "" {
		return nil, zip.ErrBadRequest("no esign request to complete")
	}
	// A real provider's webhook drives completion; the complete signal is idempotent.
	complete, err := o.s.State.prov.esign.Status(ctx, org, f.EsignRef)
	if err != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "esign status: %v", err)
	}
	// Honor an explicit override for the manual/stub provider (webhook not wired).
	if in.Signed != nil {
		complete = *in.Signed
	}
	f.Signed = complete
	if err := save(ctx, o.s, f); err != nil {
		return nil, err
	}
	return view(f), nil
}

// ---- genesis (seed cap table + anchor on-chain) ----

// RecordGenesis seeds the canonical cap table with the founding allocation
// (stakeholders, a common share class, issued shares) and anchors the
// deterministic equity-genesis root on-chain.
//
// It is idempotent: once a root is recorded the cap table is NOT re-seeded, which
// would double-issue founder share certificates. The root is persisted even when
// the on-chain submit fails, because the root is the tamper-evident witness and
// must not be recomputed on retry. Available only at the genesis stage.
func (o ops) recordGenesis(ctx context.Context, _ *noInput) (*formationView, error) {
	f, org, err := load(ctx, o.s)
	if err != nil {
		return nil, err
	}
	if err := requireStage(f, StageGenesis); err != nil {
		return nil, err
	}
	// Idempotent: once the genesis root is recorded, do NOT re-seed (which would
	// double-issue founder share certificates) — just return the recorded state.
	if f.Genesis != nil && f.Genesis.Root != "" {
		return view(f), nil
	}
	// Seed the canonical cap table with the founding allocation (stakeholders +
	// common class + issued shares), then anchor the deterministic root on-chain.
	if err := o.s.State.prov.captable.SeedFounders(ctx, org, f.Name, f.Founders); err != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "seed cap table: %v", err)
	}
	g, anchorErr := o.s.State.prov.anchor.Anchor(ctx, f)
	if g != nil {
		// Persist the computed root even if the on-chain submit failed — the root is
		// the tamper-evident witness and must not be recomputed/re-seeded on retry.
		f.Genesis = g
		if serr := save(ctx, o.s, f); serr != nil {
			return nil, serr
		}
	}
	if anchorErr != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "anchor genesis: %v", anchorErr)
	}
	return view(f), nil
}

// ---- the transition door ----

// advanceIn names the stage to move to.
type advanceIn struct {
	// To is the target stage: structure, founders, payment, documents, esign,
	// genesis, import or company.
	To Stage `json:"to"`
}

// Advance runs the ONE guarded transition of the formation machine. It is the
// only door between stages: the actions populate data, this decides ordering.
//
// An edge the machine does not define answers 409; an edge whose guard is not yet
// satisfied answers 422 naming what is missing. Reaching the terminal `company`
// stage also records the incorporation on the canonical cap table, and that must
// succeed before the transition is persisted.
//
// Example: {"to": "founders"}
func (o ops) advance(ctx context.Context, in *advanceIn) (*formationView, error) {
	f, _, err := load(ctx, o.s)
	if err != nil {
		return nil, err
	}
	if in.To == "" {
		return nil, zip.ErrBadRequest("to is required")
	}
	if aerr := Advance(f, in.To); aerr != nil {
		if errors.Is(aerr, errIllegalTransition) {
			return nil, zip.Errorf(http.StatusConflict, "%v", aerr)
		}
		return nil, zip.Errorf(http.StatusUnprocessableEntity, "%v", aerr)
	}
	// Reaching the terminal company stage upgrades the org (records the
	// incorporation on the canonical cap table). It must succeed before the
	// transition is persisted.
	if f.Stage == StageCompany {
		if uerr := o.s.State.prov.upgrade.MarkCompany(ctx, f); uerr != nil {
			return nil, zip.Errorf(http.StatusBadGateway, "upgrade org to company: %v", uerr)
		}
	}
	if err := save(ctx, o.s, f); err != nil {
		return nil, err
	}
	return view(f), nil
}

// ---- skip / import ----

// Skip marks the org as already incorporated and moves it onto the import path,
// so an existing company brings its documents and cap table in instead of forming
// a new entity. Available only at the structure stage.
func (o ops) skip(ctx context.Context, _ *noInput) (*formationView, error) {
	f, _, err := load(ctx, o.s)
	if err != nil {
		return nil, err
	}
	if err := requireStage(f, StageStructure); err != nil {
		return nil, err
	}
	f.AlreadyIncorporated = true
	if aerr := Advance(f, StageImport); aerr != nil {
		return nil, zip.Errorf(http.StatusUnprocessableEntity, "%v", aerr)
	}
	if err := save(ctx, o.s, f); err != nil {
		return nil, err
	}
	return view(f), nil
}

// importDocumentsIn names the Drive folder to ingest.
type importDocumentsIn struct {
	// FolderID is a Google Drive folder id. Required.
	FolderID string `json:"folderId"`
}

// importDocumentsOut counts what was ingested, with the formation it was recorded on.
type importDocumentsOut struct {
	// Ingested is how many files this call put in the data room.
	Ingested int `json:"ingested"`
	// Formation is the org's incorporation record with the imported document ids.
	Formation *Formation `json:"formation"`
}

// ImportDocuments ingests an existing company's corporate documents from a Google
// Drive folder into the org's data room. The import is shallow — sub-folders are
// skipped, not walked — and available only at the import stage.
//
// Example: {"folderId": "1AbCdEfGhIjKlMnOpQrStUvWxYz"}
func (o ops) importDocuments(ctx context.Context, in *importDocumentsIn) (*importDocumentsOut, error) {
	f, org, err := load(ctx, o.s)
	if err != nil {
		return nil, err
	}
	if err := requireStage(f, StageImport); err != nil {
		return nil, err
	}
	if strings.TrimSpace(in.FolderID) == "" {
		return nil, zip.ErrBadRequest("folderId (a Google Drive folder id) is required")
	}
	files, err := o.s.State.prov.google.ListFolder(ctx, org, in.FolderID)
	if err != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "list drive folder: %v", err)
	}
	ingested := make([]string, 0, len(files))
	for _, file := range files {
		if strings.HasPrefix(file.MimeType, "application/vnd.google-apps.folder") {
			continue // skip sub-folders in this shallow import
		}
		data, ct, err := o.s.State.prov.google.Download(ctx, org, file)
		if err != nil {
			return nil, zip.Errorf(http.StatusBadGateway, "download %s: %v", file.Name, err)
		}
		id, err := o.s.State.prov.docs.Ingest(ctx, org, file.Name, ct, data)
		if err != nil {
			return nil, zip.Errorf(http.StatusBadGateway, "data room ingest %s: %v", file.Name, err)
		}
		ingested = append(ingested, id)
	}
	f.ImportedDocs = append(f.ImportedDocs, ingested...)
	if len(f.ImportedDocs) > 0 {
		f.Imported = true
	}
	if err := save(ctx, o.s, f); err != nil {
		return nil, err
	}
	return &importDocumentsOut{Ingested: len(ingested), Formation: f}, nil
}

// importCapTableIn names the Sheet to read the cap table from.
type importCapTableIn struct {
	// SpreadsheetID is a Google Sheets id. Required.
	SpreadsheetID string `json:"spreadsheetId"`
	// Range is an optional A1 range within the sheet; empty reads the default range.
	Range string `json:"range"`
}

// importCapTableOut counts what was imported, with the formation it was recorded on.
type importCapTableOut struct {
	// StakeholdersImported is how many stakeholders the cap table accepted.
	StakeholdersImported int `json:"stakeholdersImported"`
	// Rows is how many rows were read from the sheet, header included.
	Rows int `json:"rows"`
	// Formation is the org's incorporation record, now marked cap-table-imported.
	Formation *Formation `json:"formation"`
}

// ImportCapTable reads an existing company's cap table from a Google Sheet and
// adds its stakeholders to the canonical cap table.
//
// The first row is a header and columns are matched by name (case-insensitive):
// name and email are required, type/relationship/institution optional. A sheet
// without name and email columns, or with no usable data rows, is refused with
// 400. Available only at the import stage.
//
// Example: {"spreadsheetId": "1AbCdEfGhIjKlMnOpQrStUvWxYz", "range": "Cap Table!A1:E100"}
func (o ops) importCapTable(ctx context.Context, in *importCapTableIn) (*importCapTableOut, error) {
	f, org, err := load(ctx, o.s)
	if err != nil {
		return nil, err
	}
	if err := requireStage(f, StageImport); err != nil {
		return nil, err
	}
	if strings.TrimSpace(in.SpreadsheetID) == "" {
		return nil, zip.ErrBadRequest("spreadsheetId (a Google Sheets id) is required")
	}
	rows, err := o.s.State.prov.google.SheetValues(ctx, org, in.SpreadsheetID, in.Range)
	if err != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "read sheet: %v", err)
	}
	holders, err := parseCapTableRows(rows)
	if err != nil {
		return nil, zip.ErrBadRequest(err.Error())
	}
	inserted, err := o.s.State.prov.captable.AddStakeholders(ctx, org, holders)
	if err != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "captable import: %v", err)
	}
	f.CapTableImported = true
	if err := save(ctx, o.s, f); err != nil {
		return nil, err
	}
	return &importCapTableOut{StakeholdersImported: inserted, Rows: len(rows), Formation: f}, nil
}

// ---- fundraising ----

// roundOut is the recorded round's id on the canonical cap table.
type roundOut struct {
	// RoundID is the cap table's id for the recorded round.
	RoundID string `json:"roundId"`
}

// RecordRound records a fundraising round on the org's canonical cap table.
// Available only after incorporation (stage company); roundType defaults to
// PRICED.
//
// Example: {"name": "Seed", "roundType": "PRICED", "targetAmount": 2000000}
func (o ops) fundraiseRound(ctx context.Context, in *RoundInput) (*roundOut, error) {
	f, org, err := load(ctx, o.s)
	if err != nil {
		return nil, err
	}
	if f.Stage != StageCompany {
		return nil, zip.Errorf(http.StatusConflict, "fundraising is available after incorporation (stage company)")
	}
	body := *in
	if strings.TrimSpace(body.Name) == "" {
		return nil, zip.ErrBadRequest("round name is required")
	}
	if body.RoundType == "" {
		body.RoundType = "PRICED"
	}
	id, err := o.s.State.prov.captable.RecordRound(ctx, org, body)
	if err != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "record round: %v", err)
	}
	return &roundOut{RoundID: id}, nil
}

// deckOut is the data room's receipt for an ingested pitch deck. It exists as a
// named type so the response can be DECLARED (openapi.Register, see init) and so
// the declaration and the value the handler returns are the same shape — a map
// literal here and a struct in the document is how the two drift apart.
type deckOut struct {
	// DocumentID is the org data room's id for the stored deck.
	DocumentID string `json:"documentId"`
}

// fundraiseDeck shares a pitch deck in the org's data room. It is the second
// action on this surface that is NOT a typed op: the deck is the raw request
// BODY (any content type, named by ?name=), not a JSON document, so a typed In
// would declare a request shape the route does not take — see routes(). Its byte
// request and this response ARE declared, through openapi.Register (see init).
func fundraiseDeck(s *cloud.Service[state], c *zip.Ctx) error {
	ctx := c.Context()
	f, org, err := load(ctx, s)
	if err != nil {
		return err
	}
	if f.Stage != StageCompany {
		return zip.Errorf(http.StatusConflict, "fundraising is available after incorporation (stage company)")
	}
	raw := c.Fiber().Body()
	if len(raw) == 0 {
		return zip.ErrBadRequest("send the deck bytes as the request body")
	}
	name := c.Query("name")
	if name == "" {
		name = "pitch-deck"
	}
	ct := c.Header("Content-Type")
	id, err := s.State.prov.docs.Ingest(ctx, org, name, ct, raw)
	if err != nil {
		return zip.Errorf(http.StatusBadGateway, "data room ingest: %v", err)
	}
	return c.JSON(http.StatusCreated, deckOut{DocumentID: id})
}

// safeIn names the documents to sign and who signs them.
type safeIn struct {
	// DocumentIDs are data room document ids to raise a signature request over. Required.
	DocumentIDs []string `json:"documentIds"`
	// Signers are the recipients, each a name and an email. Required.
	Signers []Signer `json:"signers"`
}

// safeOut is the signature request the e-signature provider opened.
type safeOut struct {
	// EsignRef is the provider's reference for the signature request.
	EsignRef string `json:"esignRef"`
	// Provider is the wired e-signature provider's name.
	Provider string `json:"provider"`
}

// RequestSafe raises an e-signature request over documents already in the org's
// data room — a SAFE, a convertible note, or any other fundraising paper.
// Available only after incorporation (stage company).
//
// Example: {"documentIds": ["doc_safe"], "signers": [{"name": "Ada", "email": "ada@acme.com"}]}
func (o ops) fundraiseSafe(ctx context.Context, in *safeIn) (*safeOut, error) {
	f, org, err := load(ctx, o.s)
	if err != nil {
		return nil, err
	}
	if f.Stage != StageCompany {
		return nil, zip.Errorf(http.StatusConflict, "fundraising is available after incorporation (stage company)")
	}
	if len(in.DocumentIDs) == 0 || len(in.Signers) == 0 {
		return nil, zip.ErrBadRequest("documentIds and signers are required")
	}
	ref, err := o.s.State.prov.esign.Request(ctx, org, in.DocumentIDs, in.Signers)
	if err != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "esign request: %v", err)
	}
	return &safeOut{EsignRef: ref, Provider: o.s.State.prov.esign.Name()}, nil
}
