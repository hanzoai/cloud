package compliance

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/hanzoai/cloud/internal/mint"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/idv"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/hanzoai/cloud/audit"
	"github.com/hanzoai/cloud/openapi"
	"github.com/zap-proto/zip"
)

// maxBody bounds a compliance request body — records are small structured values.
const maxBody = 1 << 20 // 1 MiB

// routePrefix is the ONE address this app answers on. The group composes every
// op's path from it, and bodyCap matches the full paths it produces, so the
// prefix is written once and the two can never disagree about where a route
// lives.
const routePrefix = "/v1/compliance"

// auditActionPrefix scopes the compliance-relevant slice of the shared audit plane.
const auditActionPrefix = "compliance."

// state is compliance's own data; shared deps live in the embedded cloud.Base. It
// holds the sealed record store, the verification provider client (Manual by default;
// a real provider when configured), the OPTIONAL signature-authenticated provider
// webhook receiver (nil unless configured), and the shared audit recorder (nil-safe).
type state struct {
	store   *Store
	idv     idv.Provider
	webhook *idv.Webhook
	// webhookErr is a webhook that was NAMED and did not resolve. It is carried
	// rather than returned from Mount because one route reads it and sixteen do
	// not: a secret for the provider callback is no reason for /records, /audit
	// or the accreditation decisions to answer 503.
	webhookErr error
	audit      *audit.Recorder
}

// mounted is the process-wide handle so Shutdown can close the store (mirrors
// clients/security). Set by Mount, read by Shutdown.
var mounted *cloud.Service[state]

// Mount wires /v1/compliance/* and opens the sealed per-deployment store under
// {DataDir}/compliance.db. The verification provider is resolved from config
// (idv.FromConfig): Manual by default, a real provider when named — and FAIL-CLOSED,
// so a named-but-misconfigured provider fails the mount rather than silently
// downgrading to Manual.
func Mount(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("compliance.Mount: nil app")
	}
	if deps.DataDir == "" {
		return fmt.Errorf("compliance.Mount: empty DataDir")
	}
	provider, err := idv.FromConfig(deps.Secret(), os.Getenv)
	if err != nil {
		return fmt.Errorf("compliance.Mount: idv provider: %w", err)
	}
	// The signature-authenticated provider webhook is FAIL-CLOSED, and stays so: a
	// named-but-unresolvable secret NEVER serves an unauthenticated endpoint, because
	// WebhookFromConfig returns nil on both of its error branches and the route
	// refuses on nil. Nil (the default) means no webhook path is served at all.
	//
	// The error is carried to that route instead of failing the mount, so the refusal
	// names the unresolved ref rather than answering 503 "no instance running" for
	// this whole surface — which, on a Lazy app, is a sentence about nothing.
	webhook, webhookErr := idv.WebhookFromConfig(deps.Secret(), os.Getenv)
	store, err := openStore(deps.DataDir)
	if err != nil {
		return fmt.Errorf("compliance.Mount: open store: %w", err)
	}
	s := &cloud.Service[state]{
		Base:  cloud.NewBase(deps, "compliance"),
		State: state{store: store, idv: provider, webhook: webhook, webhookErr: webhookErr, audit: deps.Audit},
	}
	mounted = s
	routes(app, s)
	s.Log.Info("compliance mounted", "brand", deps.Brand, "provider", provider.Name(), "webhook", webhook != nil, "audit", deps.Audit != nil)
	return nil
}

// zipdoc lifts the doc comment off each typed op and its In/Out fields into
// zipdoc_gen.go, which is the ONLY way that prose reaches the published document
// and the MCP tool list — Go drops comments at compile time. Run by `make -C
// apps/compliance openapi` and by the per-app build chain.
//
//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

// The prose for the ONE operation on this surface zipdoc cannot reach. Every
// other route is a typed op whose doc comment is lifted; the webhook stays an
// untyped handler for the two wire facts routes() states, and Go drops the
// comment on an untyped handler at compile time. Left bare it published an
// operationId and nothing else, so the generated SDKs offered a call named
// "verifications webhook" with no way to learn that it authenticates by
// signature rather than by a principal. Declared through the same registry
// openapi.Register uses, so it renders only while the router serves the route.
func init() {
	openapi.Describe(routePrefix+"/verifications/webhook", http.MethodPost,
		"Provider push that settles a verification, authenticated by HMAC signature",
		"The external PUSH reconcile: a verification provider (or a Hanzo relay) "+
			"signals that a check settled, and the reconciled check comes back. It "+
			"authenticates by an HMAC SIGNATURE over the RAW body bytes rather than by a "+
			"principal — an external caller has no validated org — and the org is then "+
			"resolved FROM the record the signed provider reference matches, so a call can "+
			"only ever touch the one tenant that owns that reference.\n\n"+
			"The body carries NO trusted decision. A valid signature cannot force a "+
			"status: the reference only says WHICH check to re-read, and the status is "+
			"then pulled from the wired provider, which stays the source of truth. With "+
			"no real provider configured a check stays pending, and the only route to a "+
			"passing status is the role-gated, attributed reviewer decision.\n\n"+
			"An unknown reference is a benign 200 `{\"ignored\": ...}` no-op, not an error, "+
			"so a provider replaying stale events neither retry-storms nor learns whether "+
			"a reference exists in some other tenant. Fails closed otherwise: 501 unless a "+
			"webhook secret is configured, 401 on a signature that does not verify, 400 "+
			"with no provider reference, 413 over 1 MiB, and 502 if the secret or the "+
			"provider is unreachable.")
}

// routes registers the compliance surface. Static + collection routes register
// before :id params so an id can never shadow a sibling route (Fiber first-match).
// Every route except the webhook is a TYPED op declared on the group, so the
// document, the MCP tool, the CLI command and the generated SDK all follow from
// this one registration. The webhook stays an untyped handler on purpose: it
// authenticates by HMAC over the RAW body bytes, which must be verified before any
// parse — a typed op decodes its In first, which would both reorder that check and
// split its two 200 shapes (reconciled check vs benign unknown-reference no-op).
func routes(app cloud.Router, s *cloud.Service[state]) {
	g := app.Group(routePrefix)
	// cloud.Bridge is not installed here: the composer owns it — the fused host
	// installs it once at its root, and the plugin constructor does the same for a
	// plugin program — and typed ops read the validated org it parks on the
	// context. bodyCap precedes the leaves because fiber runs middleware in
	// registration order.
	g.Use(bodyCap())
	o := ops{s: s}

	zip.Get(g, "/health", o.health)
	zip.Get(g, "/status", o.status)
	zip.Get(g, "/records", o.listRecords)
	zip.Get(g, "/audit", o.auditRead)

	zip.Post(g, "/subjects", o.createSubject, zip.WithStatus(http.StatusCreated))
	zip.Get(g, "/subjects", o.listSubjects)
	zip.Get(g, "/subjects/:id", o.getSubject)

	zip.Post(g, "/verifications", o.startVerification, zip.WithStatus(http.StatusCreated))
	zip.Get(g, "/verifications", o.listVerifications)
	// A terminal status is reachable by exactly three orthogonal paths, never a raw
	// client assertion: a SIGNATURE-authenticated provider webhook (push), an internal
	// provider RECONCILE (pull), or a role-gated, attributed reviewer DECISION. The
	// webhook route is static, registered before :id (Fiber first-match).
	//
	// This is the ONE untyped route on this surface, and it stays untyped on two
	// wire facts, each verified against the dependency's own source rather than
	// taken from prose (zip v1.18.6): the HMAC is computed over the EXACT received
	// bytes (apps/idv/webhook.go Verify: mac.Write(body)) while zip's invoke
	// json.Unmarshals the body into In BEFORE the handler runs (typed.go:234), so a
	// re-encoded In is not the signed value and the signature would never match;
	// and the route answers TWO 200 shapes — the reconciled check, or
	// {"ignored": …} for an unknown reference — where an op declares exactly one
	// Out, so unioning them would add zero-valued fields to the no-op body. It is
	// NAMED in untypedByDesign (typed_wire_test.go), which is a GATE:
	// TestEveryRouteIsTypedOrNamed fails on any compliance route that is neither
	// typed nor named there, so the next route added here is typed by default.
	g.Post("/verifications/webhook", cloud.Handle(s, verificationWebhook))
	zip.Get(g, "/verifications/:id", o.getVerification)
	zip.Post(g, "/verifications/:id/refresh", o.refreshVerification)
	zip.Post(g, "/verifications/:id/decision", o.decideVerification)

	zip.Post(g, "/accreditation", o.createAccreditation, zip.WithStatus(http.StatusCreated))
	zip.Get(g, "/accreditation", o.listAccreditation)
	zip.Get(g, "/accreditation/:id", o.getAccreditation)
	zip.Post(g, "/accreditation/:id/decision", o.decideAccreditation)
}

// Shutdown closes the record store. Idempotent; safe if Mount never ran.
func Shutdown() error {
	if mounted == nil || mounted.State.store == nil {
		return nil
	}
	err := mounted.State.store.Close()
	mounted = nil
	return err
}

// ops binds the service to the typed compliance ops. A TypedHandler is
// func(context.Context, *In) (*Out, error) — no parameter for the service — so it
// arrives as a RECEIVER and every op is a method value (o.createSubject), which is
// also the only bound form cmd/zipdoc can lift prose from.
type ops struct{ s *cloud.Service[state] }

// noStore pins Cache-Control: no-store on the response a typed op is serving —
// reached through the request cloud.Bridge parked, because a typed op returns its
// Out and has no response value of its own. Set on SUCCESS paths only, exactly
// where the untyped handlers set it (a PII-bearing or per-org answer must never
// be cached). No-op off the HTTP path, where there is nothing to cache.
func noStore(ctx context.Context) {
	if c, ok := cloud.Request(ctx); ok {
		c.SetHeader("Cache-Control", "no-store")
	}
}

// bodyCap preserves the 1 MiB request-body gate the body-bearing routes have
// always had, as ONE middleware in front of the typed ops — a typed op receives
// its DECODED In, so a size check inside it would run after the parse it exists
// to precede. It guards exactly the routes that read a JSON body (the three
// creates and the two decisions); the webhook keeps its own identical check, and
// refresh — which has never read a body — stays unguarded.
func bodyCap() zip.Handler {
	return func(c *zip.Ctx) error {
		if c.Method() == http.MethodPost && len(c.Fiber().Body()) > maxBody {
			switch p := strings.TrimSuffix(c.Path(), "/"); {
			case p == routePrefix+"/subjects",
				p == routePrefix+"/verifications",
				p == routePrefix+"/accreditation",
				strings.HasSuffix(p, "/decision"):
				return zip.Errorf(http.StatusRequestEntityTooLarge, "request body too large")
			}
		}
		return c.Continue()
	}
}

// ---- health / status ----

// healthView reports liveness and the wired provider.
type healthView struct {
	// Status is "ok" when the subsystem is live.
	Status string `json:"status"`
	// Provider is the wired verification provider's name ("manual" by default).
	Provider string `json:"provider"`
}

// Health reports subsystem liveness and the wired verification provider. Fail-open
// on purpose: it never probes the external provider, so a provider outage cannot
// fail liveness.
func (o ops) health(ctx context.Context, _ *noInput) (*healthView, error) {
	return &healthView{Status: "ok", Provider: o.s.State.idv.Name()}, nil
}

// verificationTally is the per-status verification count.
type verificationTally struct {
	// ByStatus tallies the org's verifications by provider-reported status.
	ByStatus map[string]int `json:"byStatus"`
	// Total is the sum over every status.
	Total int `json:"total"`
}

// statusView is the org's verification posture.
type statusView struct {
	// Provider is the wired verification provider's name.
	Provider string `json:"provider"`
	// Verifications tallies the org's verifications by provider-reported status.
	Verifications verificationTally `json:"verifications"`
	// Disclaimer states that statuses are provider-reported, never a platform
	// assertion of legal or regulatory compliance.
	Disclaimer string `json:"disclaimer"`
}

// Status is the org's honest posture read: the wired provider and the per-status
// tally of its verifications. It is deliberately NOT a boolean "compliant" — it
// reports counts of provider-reported states and carries the boundary disclaimer.
func (o ops) status(ctx context.Context, _ *noInput) (*statusView, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	counts, err := o.s.State.store.StatusCounts(ctx, org)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "status: %v", err)
	}
	total := 0
	for _, n := range counts {
		total += n
	}
	noStore(ctx)
	return &statusView{
		Provider:      o.s.State.idv.Name(),
		Verifications: verificationTally{ByStatus: counts, Total: total},
		Disclaimer:    Disclaimer,
	}, nil
}

// ---- subjects ----

// subjectSummary is the PII-MINIMIZED list projection: it omits the subject's
// name/email so a list read never sprays PII. The full record (with contact PII) is
// returned only on an explicit single-subject GET.
type subjectSummary struct {
	// ID is the subject's opaque id.
	ID string `json:"id"`
	// Kind is the party type: "individual" (KYC) or "business" (KYB).
	Kind SubjectKind `json:"kind"`
	// Ref is the org's own opaque external id for this subject.
	Ref string `json:"ref,omitempty"`
	// HasEmail reports whether a contact email is on file, without exposing it.
	HasEmail bool `json:"hasEmail"`
	// CreatedAt is the unix second the subject was recorded.
	CreatedAt int64 `json:"createdAt"`
}

// noInput is the In of an op addressed entirely by the caller's principal: it
// takes nothing off the wire.
type noInput struct{}

// listIn bounds a list read.
type listIn struct {
	// Limit caps the rows returned; non-positive means the server default.
	Limit int `json:"limit"`
}

// subjectReq is a new-subject record. A subject needs an email or a ref so a
// verification can later address it; contact PII is sealed at rest and never
// logged.
type subjectReq struct {
	// Kind is the party type: "individual" (KYC) or "business" (KYB).
	Kind SubjectKind `json:"kind"`
	// Ref is the org's own opaque external id for this subject.
	Ref string `json:"ref"`
	// Email is the subject's contact email, sealed at rest.
	Email string `json:"email"`
	// Name is the subject's name, sealed at rest.
	Name string `json:"name"`
}

// CreateSubject records a party the org is verifying as part of its own
// onboarding/compliance — a team member, vendor, customer, or counterparty. The
// subject's contact PII (name/email) is sealed at rest and returned only to the
// owning org; downstream records reference the subject by opaque id.
//
// Example: {"kind": "individual", "email": "founder@example.com", "name": "Ada"}
func (o ops) createSubject(ctx context.Context, in *subjectReq) (*Subject, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	sub, err := newSubject(o.s, ctx, org, in.Kind, in.Ref, in.Email, in.Name)
	if err != nil {
		return nil, err
	}
	emitAudit(o.s, ctx, "compliance.subject.create", audit.Resource{Type: "compliance.subject", ID: sub.ID},
		"success", http.StatusCreated, map[string]any{"subjectId": sub.ID, "kind": sub.Kind})
	return &sub, nil
}

// newSubject validates and persists a subject. PII (name/email) is sealed at rest by
// the cek store; nothing here logs it. A free function (not a method) — Go forbids
// methods on cloud.Service, a type owned by another package.
func newSubject(s *cloud.Service[state], ctx context.Context, org string, kind SubjectKind, ref, email, name string) (Subject, error) {
	if !idv.ValidKind(kind) {
		return Subject{}, zip.ErrBadRequest("kind must be individual or business")
	}
	email, name, ref = strings.TrimSpace(email), strings.TrimSpace(name), strings.TrimSpace(ref)
	if email == "" && ref == "" {
		return Subject{}, zip.ErrBadRequest("subject needs an email or a ref")
	}
	id := mint.ID("sub")
	now := time.Now().Unix()
	sub := Subject{ID: id, Org: org, Kind: kind, Ref: ref, Email: email, Name: name, CreatedAt: now, UpdatedAt: now}
	if err := s.State.store.CreateSubject(ctx, sub); err != nil {
		return Subject{}, zip.Errorf(http.StatusInternalServerError, "create subject: %v", err)
	}
	return sub, nil
}

// subjectList is a page of the org's subjects, PII-minimized.
type subjectList struct {
	// Data is the org's subjects, newest first, without contact PII.
	Data []subjectSummary `json:"data"`
}

// ListSubjects returns the org's subjects as PII-MINIMIZED summaries — no name or
// email, only whether an email is on file. The full record is returned only by the
// explicit single-subject read.
func (o ops) listSubjects(ctx context.Context, in *listIn) (*subjectList, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	subs, err := o.s.State.store.ListSubjects(ctx, org, in.Limit)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list subjects: %v", err)
	}
	out := make([]subjectSummary, 0, len(subs))
	for _, sub := range subs {
		out = append(out, subjectSummary{ID: sub.ID, Kind: sub.Kind, Ref: sub.Ref, HasEmail: sub.Email != "", CreatedAt: sub.CreatedAt})
	}
	noStore(ctx)
	return &subjectList{Data: out}, nil
}

// subjectRef addresses one subject. The id is the path segment: the URL is the
// addressing authority, so it binds from there whatever a body says.
type subjectRef struct {
	// ID is the subject to read, from the path.
	ID string `json:"id"`
}

// GetSubject returns one subject WITH its contact PII — the only surface that
// returns it, and only to the owning org. The response is never cached by any
// intermediary.
//
// Example: {"id": "sub_1"}
func (o ops) getSubject(ctx context.Context, in *subjectRef) (*Subject, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	sub, err := o.s.State.store.GetSubject(ctx, org, in.ID)
	if err == errNotFound {
		return nil, zip.ErrNotFound("subject not found")
	}
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "get subject: %v", err)
	}
	// This explicit single-subject read is the only surface that returns contact PII,
	// and only to the owning org. No PII cache in any intermediary.
	noStore(ctx)
	return &sub, nil
}

// ---- verifications (KYC / KYB) ----

// checkView renders a verification WITHOUT any subject PII (only the opaque
// subjectId + provider-reported status). This is the shape every list/detail/record
// surface uses, so no PII path can leak through a verification response.
type checkView struct {
	// ID is the verification's opaque id.
	ID string `json:"id"`
	// SubjectID is the opaque id of the subject under verification.
	SubjectID string `json:"subjectId"`
	// Kind is the party type: "individual" (KYC) or "business" (KYB).
	Kind SubjectKind `json:"kind"`
	// Provider is the verification provider this check runs through.
	Provider string `json:"provider"`
	// Status is the check's state: pending, provider_verified, provider_rejected,
	// manual_review, or expired (provider-reported), or reviewer_confirmed — the
	// one value a privileged human reviewer records, never a provider.
	Status idv.Status `json:"status"`
	// VerifyURL is the provider's hosted verification flow for the subject, when one exists.
	VerifyURL string `json:"verifyUrl,omitempty"`
	// DecidedBy records who settled a terminal status: the provider name, or a
	// reviewer's user id for a recorded manual decision.
	DecidedBy string `json:"decidedBy,omitempty"`
	// DecidedAt is the unix second a terminal status was recorded.
	DecidedAt int64 `json:"decidedAt,omitempty"`
	// CreatedAt is the unix second the verification was started.
	CreatedAt int64 `json:"createdAt"`
	// UpdatedAt is the unix second the verification last changed.
	UpdatedAt int64 `json:"updatedAt"`
}

func checkViewOf(chk Check) checkView {
	return checkView{
		ID:        chk.ID,
		SubjectID: chk.SubjectID,
		Kind:      chk.Kind,
		Provider:  chk.Provider,
		Status:    chk.Status,
		VerifyURL: chk.VerifyURL,
		DecidedBy: chk.DecidedBy,
		DecidedAt: chk.DecidedAt,
		CreatedAt: chk.CreatedAt,
		UpdatedAt: chk.UpdatedAt,
	}
}

// verificationReq starts a verification: of an existing subject by id, or of one
// created inline from the remaining fields.
type verificationReq struct {
	// SubjectID names an existing subject to verify; empty creates one inline.
	SubjectID string `json:"subjectId"`
	// Kind is an inline subject's party type: "individual" (KYC) or "business" (KYB).
	Kind SubjectKind `json:"kind"`
	// Ref is the org's own opaque external id for an inline subject.
	Ref string `json:"ref"`
	// Email is an inline subject's contact email, sealed at rest.
	Email string `json:"email"`
	// Name is an inline subject's name, sealed at rest.
	Name string `json:"name"`
}

// StartVerification begins a KYC/KYB verification of a subject through the wired
// provider — an existing subject by id, or one created inline from the request.
// The returned status is provider-reported and never terminal on a fresh start:
// starting a verification can never yield a verified record, and a provider error
// is a 502, never a verification.
//
// Example: {"subjectId": "sub_1"}
func (o ops) startVerification(ctx context.Context, in *verificationReq) (*checkView, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}

	// Resolve the subject: an existing one by id, or create one inline from the body.
	var sub Subject
	if strings.TrimSpace(in.SubjectID) != "" {
		sub, err = o.s.State.store.GetSubject(ctx, org, in.SubjectID)
		if err == errNotFound {
			return nil, zip.ErrNotFound("subject not found")
		}
		if err != nil {
			return nil, zip.Errorf(http.StatusInternalServerError, "get subject: %v", err)
		}
	} else {
		sub, err = newSubject(o.s, ctx, org, in.Kind, in.Ref, in.Email, in.Name)
		if err != nil {
			return nil, err
		}
	}

	// Start the verification through the provider client. The returned status is
	// provider-reported and, by the client's contract, non-terminal on a fresh start —
	// there is NO path here that yields a verified check. A provider error is a 502;
	// it never degrades to verified.
	// An inquiry is opened on the DEPLOYMENT's provider key and the vendor bills for
	// it whether or not the person finishes. Authorized before it is opened. See
	// meter.go.
	ch, err := afford(o.s, ctx)
	if err != nil {
		return nil, cloud.Denied(err)
	}
	defer ch.Release()
	sess, err := o.s.State.idv.Start(ctx, org, idv.Subject{Kind: sub.Kind, Name: sub.Name, Email: sub.Email, Ref: sub.Ref})
	if err != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "verification start failed")
	}
	charge(ch)
	// A "start" is never a decision. Clamp any terminal status the provider returns
	// here to pending at the product boundary — belt-and-suspenders over the idv
	// client's own downgrade, so "creating a verification can never yield a verified
	// record" holds even against a misbehaving or compromised provider adapter.
	initial := sess.Status
	if initial.Terminal() {
		initial = idv.StatusPending
	}
	id := mint.ID("chk")
	now := time.Now().Unix()
	chk := Check{
		ID: id, Org: org, SubjectID: sub.ID, Kind: sub.Kind,
		Provider: o.s.State.idv.Name(), ProviderRef: sess.Ref, VerifyURL: sess.VerifyURL,
		Status: initial, CreatedAt: now, UpdatedAt: now,
	}
	if err := o.s.State.store.CreateCheck(ctx, chk); err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "create check: %v", err)
	}
	emitAudit(o.s, ctx, "compliance.verification.start", audit.Resource{Type: "compliance.check", ID: chk.ID},
		"success", http.StatusCreated,
		map[string]any{"checkId": chk.ID, "subjectId": sub.ID, "provider": chk.Provider, "kind": chk.Kind, "status": chk.Status})
	v := checkViewOf(chk)
	return &v, nil
}

// checkList is a page of the org's verifications.
type checkList struct {
	// Data is the org's verifications, newest first, without subject PII.
	Data []checkView `json:"data"`
	// Disclaimer states that statuses are provider-reported, never a platform
	// assertion of legal or regulatory compliance.
	Disclaimer string `json:"disclaimer"`
}

// ListVerifications returns the org's KYC/KYB verifications, newest first — opaque
// subject references and provider-reported statuses only, no subject PII.
func (o ops) listVerifications(ctx context.Context, in *listIn) (*checkList, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	chks, err := o.s.State.store.ListChecks(ctx, org, in.Limit)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list checks: %v", err)
	}
	out := make([]checkView, 0, len(chks))
	for _, chk := range chks {
		out = append(out, checkViewOf(chk))
	}
	return &checkList{Data: out, Disclaimer: Disclaimer}, nil
}

// verificationRef addresses one verification. The id is the path segment: the URL
// is the addressing authority, so it binds from there whatever a body says.
type verificationRef struct {
	// ID is the verification to act on, from the path.
	ID string `json:"id"`
}

// GetVerification returns one verification — its opaque subject reference and
// provider-reported status, no subject PII.
//
// Example: {"id": "chk_1"}
func (o ops) getVerification(ctx context.Context, in *verificationRef) (*checkView, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	chk, err := o.s.State.store.GetCheck(ctx, org, in.ID)
	if err == errNotFound {
		return nil, zip.ErrNotFound("verification not found")
	}
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "get check: %v", err)
	}
	v := checkViewOf(chk)
	return &v, nil
}

// reconcileCheck is the ONE provider-consult core: it reads the wired provider's
// settled status for a check and records it, ATTRIBUTED to the provider. It is the
// single source of a provider_verified/provider_rejected record — used by BOTH the
// internal poll (refreshVerification) and the signature-authenticated webhook, which
// differ only in how they authenticate and locate the check. For the Manual provider
// Check stays pending, so no provider terminal is ever manufactured. Returns the
// (possibly updated) check and whether it changed.
func reconcileCheck(s *cloud.Service[state], ctx context.Context, chk Check) (Check, bool, error) {
	res, err := s.State.idv.Check(ctx, chk.Org, chk.ProviderRef)
	if err != nil {
		return chk, false, err
	}
	if !res.Status.Valid() || res.Status == chk.Status {
		return chk, false, nil
	}
	now := time.Now().Unix()
	decidedBy := ""
	decidedAt := int64(0)
	if res.Status.Terminal() {
		decidedBy = s.State.idv.Name() // provider-attributed: the terminal came from the provider API
		decidedAt = now
	}
	if err := s.State.store.UpdateCheckStatus(ctx, chk.Org, chk.ID, res.Status, decidedBy, now, decidedAt); err != nil {
		return chk, false, err
	}
	chk.Status, chk.DecidedBy, chk.UpdatedAt, chk.DecidedAt = res.Status, decidedBy, now, decidedAt
	return chk, true, nil
}

// RefreshVerification polls the wired provider for its current decision and
// records it, ATTRIBUTED to the provider — the internal PULL reconcile. For the
// Manual provider the check stays pending; for a hosted provider it reflects the
// provider's settled status. A poll error is a 502, never a verification.
//
// Example: {"id": "chk_1"}
func (o ops) refreshVerification(ctx context.Context, in *verificationRef) (*checkView, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	chk, err := o.s.State.store.GetCheck(ctx, org, in.ID)
	if err == errNotFound {
		return nil, zip.ErrNotFound("verification not found")
	}
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "get check: %v", err)
	}
	chk, changed, err := reconcileCheck(o.s, ctx, chk)
	if err != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "verification refresh failed")
	}
	if changed {
		emitAudit(o.s, ctx, "compliance.verification.refresh", audit.Resource{Type: "compliance.check", ID: chk.ID},
			"success", http.StatusOK, map[string]any{"checkId": chk.ID, "provider": chk.Provider, "status": chk.Status})
	}
	v := checkViewOf(chk)
	return &v, nil
}

// verificationWebhook is the external PUSH reconcile: a provider (or a Hanzo relay)
// signals that a verification settled. It authenticates by HMAC SIGNATURE (not an
// internal principal — an external caller has no validated org), locates the check by
// the provider reference the signed payload names, and RECONCILES the status from the
// provider API. The body carries no trusted decision, so a valid signature cannot
// force a status — the wired provider is the source of truth, and Manual stays
// pending. Disabled (501) unless a webhook secret is configured.
//
// UNTYPED on purpose: the HMAC is computed over the RAW body bytes and verified
// BEFORE anything parses, and an unknown reference answers a benign 200 no-op whose
// shape differs from the reconciled check. A typed op would decode its In first —
// reordering the authentication — and cannot answer two 200 shapes.
func verificationWebhook(s *cloud.Service[state], c *zip.Ctx) error {
	if s.State.webhook == nil {
		// A webhook that was named and did not resolve is a different answer from one
		// that was never configured, and the caller cannot fix what it cannot see.
		if s.State.webhookErr != nil {
			return zip.Errorf(http.StatusBadGateway, "verification webhook: %v", s.State.webhookErr)
		}
		return zip.Errorf(http.StatusNotImplemented, "verification webhook is not configured")
	}
	body := c.Fiber().Body()
	if len(body) > maxBody {
		return zip.Errorf(http.StatusRequestEntityTooLarge, "request body too large")
	}
	valid, err := s.State.webhook.Verify(c.Context(), c.Header(idv.WebhookRefHeader), body)
	if err != nil {
		return zip.Errorf(http.StatusBadGateway, "webhook secret unavailable")
	}
	if !valid {
		return zip.Errorf(http.StatusUnauthorized, "invalid signature")
	}
	ref, ok := s.State.webhook.Reference(body)
	if !ok {
		return zip.ErrBadRequest("a provider reference is required")
	}
	// The org is resolved FROM the matched record (the reference is globally unique),
	// so the webhook can only ever touch the one tenant that owns it. An unknown
	// reference is a benign 200 no-op — no retry-storm, no cross-tenant probe.
	chk, err := s.State.store.GetCheckByProviderRef(c.Context(), ref)
	if err == errNotFound {
		return c.JSON(http.StatusOK, map[string]any{"ignored": "unknown reference"})
	}
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "get check: %v", err)
	}
	chk, changed, err := reconcileCheck(s, c.Context(), chk)
	if err != nil {
		return zip.Errorf(http.StatusBadGateway, "verification reconcile failed")
	}
	if changed {
		emitWebhookAudit(s, c, chk)
	}
	return c.JSON(http.StatusOK, checkViewOf(chk))
}

// verificationDecision is a reviewer's manual decision on one verification.
type verificationDecision struct {
	// ID is the verification to decide, from the path.
	ID string `json:"id"`
	// Status is the reviewer's decision: "reviewer_confirmed" (a pass) or
	// "manual_review" (withheld for review) — never a provider status.
	Status idv.Status `json:"status"`
}

// DecideVerification records a privileged reviewer's MANUAL decision on a
// verification — the human-in-the-loop path, and the ONLY route to a passing status
// when no real provider is wired. It produces a DISTINCT reviewer_confirmed, never
// a provider_verified (a provider decision is the provider's to report, via the
// webhook or a reconcile), and it is ROLE-GATED (an org admin or platform reviewer)
// AND ATTRIBUTED (the reviewer's user id is DecidedBy), so a manual pass is always
// accountable.
//
// Example: {"id": "chk_1", "status": "reviewer_confirmed"}
func (o ops) decideVerification(ctx context.Context, in *verificationDecision) (*checkView, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	c, ok := cloud.Request(ctx)
	if !ok || (!principal.IsSuperAdmin(c) && !principal.IsOrgAdmin(c)) {
		return nil, zip.ErrForbidden("a verification decision requires an org admin or platform reviewer")
	}
	reviewer := c.User()
	if reviewer == "" {
		return nil, zip.ErrForbidden("a verification decision requires a signed-in reviewer")
	}
	// A reviewer CONFIRMS (a pass) or WITHHOLDS to review — never asserts a provider
	// decision. provider_verified/provider_rejected belong to the provider paths.
	if in.Status != idv.StatusReviewerConfirmed && in.Status != idv.StatusReview {
		return nil, zip.ErrBadRequest("decision status must be reviewer_confirmed or manual_review")
	}
	now := time.Now().Unix()
	decidedAt := int64(0)
	if in.Status.Terminal() {
		decidedAt = now
	}
	if err := o.s.State.store.UpdateCheckStatus(ctx, org, in.ID, in.Status, reviewer, now, decidedAt); err != nil {
		if err == errNotFound {
			return nil, zip.ErrNotFound("verification not found")
		}
		return nil, zip.Errorf(http.StatusInternalServerError, "update check: %v", err)
	}
	chk, err := o.s.State.store.GetCheck(ctx, org, in.ID)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "get check: %v", err)
	}
	emitAudit(o.s, ctx, "compliance.verification.decision", audit.Resource{Type: "compliance.check", ID: chk.ID},
		"success", http.StatusOK, map[string]any{"checkId": chk.ID, "status": chk.Status, "decidedBy": reviewer})
	v := checkViewOf(chk)
	return &v, nil
}

// ---- accreditation (state tracking) ----

// accView renders one tracked accreditation-state record.
type accView struct {
	// ID is the accreditation record's opaque id.
	ID string `json:"id"`
	// SubjectID is the opaque id of the subject the record is about.
	SubjectID string `json:"subjectId"`
	// Method is how the state was established: self_attested, third_party_letter,
	// or provider_verified.
	Method AccreditationMethod `json:"method"`
	// Basis is the qualification category: income, net_worth, professional_license,
	// or entity.
	Basis AccreditationBasis `json:"basis"`
	// Status is the tracked state: asserted, provider_verified, reviewer_confirmed,
	// rejected, or expired.
	Status AccreditationStatus `json:"status"`
	// EvidenceDocID references an evidence document in the org's sealed data room.
	EvidenceDocID string `json:"evidenceDocId,omitempty"`
	// ReviewerSub is the org user who recorded a decision on this record.
	ReviewerSub string `json:"reviewerSub,omitempty"`
	// Note is a non-PII operator note.
	Note string `json:"note,omitempty"`
	// ExpiresAt is the unix second a confirmation ages out; 0 means none.
	ExpiresAt int64 `json:"expiresAt,omitempty"`
	// CreatedAt is the unix second the record was created.
	CreatedAt int64 `json:"createdAt"`
	// UpdatedAt is the unix second the record last changed.
	UpdatedAt int64 `json:"updatedAt"`
}

func accViewOf(a Accreditation) accView {
	return accView{
		ID:            a.ID,
		SubjectID:     a.SubjectID,
		Method:        a.Method,
		Basis:         a.Basis,
		Status:        a.Status,
		EvidenceDocID: a.EvidenceDocID,
		ReviewerSub:   a.ReviewerSub,
		Note:          a.Note,
		ExpiresAt:     a.ExpiresAt,
		CreatedAt:     a.CreatedAt,
		UpdatedAt:     a.UpdatedAt,
	}
}

// accreditationReq records an asserted accreditation state.
type accreditationReq struct {
	// SubjectID names the subject this record is about; it must exist within the org.
	SubjectID string `json:"subjectId"`
	// Method is how the state was established: self_attested, third_party_letter,
	// or provider_verified.
	Method AccreditationMethod `json:"method"`
	// Basis is the qualification category: income, net_worth, professional_license,
	// or entity.
	Basis AccreditationBasis `json:"basis"`
	// Status may only be "asserted" (empty reads as asserted); every confirmed,
	// rejected or expired state is recorded via the decision endpoint.
	Status AccreditationStatus `json:"status"`
	// EvidenceDocID references an evidence document in the org's sealed data room.
	EvidenceDocID string `json:"evidenceDocId"`
	// Note is a non-PII operator note.
	Note string `json:"note"`
	// ExpiresAt is the unix second a confirmation ages out; 0 means none.
	ExpiresAt int64 `json:"expiresAt"`
}

// CreateAccreditation records an ASSERTED accreditation state for a subject — the
// subject's own assertion, with no verifier. Every CONFIRMED state
// (provider_verified, reviewer_confirmed) and every rejected/expired state is a
// DECISION recorded via the decision endpoint, attributed to the reviewer — a
// create can never stamp a confirmation. The underlying figures (income, net
// worth) are never stored; only the method, category, and state.
//
// Example: {"subjectId": "sub_1", "method": "self_attested", "basis": "income"}
func (o ops) createAccreditation(ctx context.Context, in *accreditationReq) (*accView, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	if !validAccMethod(in.Method) {
		return nil, zip.ErrBadRequest("method must be self_attested, third_party_letter, or provider_verified")
	}
	if !validAccBasis(in.Basis) {
		return nil, zip.ErrBadRequest("basis must be income, net_worth, professional_license, or entity")
	}
	// A create records only an ASSERTED state — the subject's own assertion, with no
	// verifier. Every CONFIRMED state (provider_verified, reviewer_confirmed) and every
	// rejected/expired state is a DECISION that goes through the decision endpoint, so
	// it is always ATTRIBUTED to the reviewer who recorded it — a create can never
	// stamp a confirmation with no verifier.
	st := in.Status
	if st == "" {
		st = AccAsserted
	}
	if st != AccAsserted {
		return nil, zip.ErrBadRequest("on create, status may only be asserted; a provider_verified/reviewer_confirmed/rejected/expired state is set via the decision endpoint")
	}
	// The subject must exist within the org.
	if _, err := o.s.State.store.GetSubject(ctx, org, in.SubjectID); err == errNotFound {
		return nil, zip.ErrNotFound("subject not found")
	} else if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "get subject: %v", err)
	}
	id := mint.ID("acc")
	now := time.Now().Unix()
	a := Accreditation{
		ID: id, Org: org, SubjectID: in.SubjectID, Method: in.Method, Basis: in.Basis,
		Status: st, EvidenceDocID: strings.TrimSpace(in.EvidenceDocID), Note: strings.TrimSpace(in.Note),
		ExpiresAt: in.ExpiresAt, CreatedAt: now, UpdatedAt: now,
	}
	if err := o.s.State.store.CreateAccreditation(ctx, a); err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "create accreditation: %v", err)
	}
	emitAudit(o.s, ctx, "compliance.accreditation.create", audit.Resource{Type: "compliance.accreditation", ID: a.ID},
		"success", http.StatusCreated, map[string]any{"accreditationId": a.ID, "subjectId": a.SubjectID, "method": a.Method, "basis": a.Basis, "status": a.Status})
	v := accViewOf(a)
	return &v, nil
}

// accList is a page of the org's accreditation records.
type accList struct {
	// Data is the org's tracked accreditation records, newest first.
	Data []accView `json:"data"`
	// Disclaimer states that statuses are tracked or provider-reported, never a
	// platform assertion of legal or regulatory compliance.
	Disclaimer string `json:"disclaimer"`
}

// ListAccreditation returns the org's tracked accreditation-state records, newest
// first — evidence entries the org keeps, never a platform certification.
func (o ops) listAccreditation(ctx context.Context, in *listIn) (*accList, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := o.s.State.store.ListAccreditation(ctx, org, in.Limit)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list accreditation: %v", err)
	}
	out := make([]accView, 0, len(rows))
	for _, a := range rows {
		out = append(out, accViewOf(a))
	}
	return &accList{Data: out, Disclaimer: Disclaimer}, nil
}

// accreditationRef addresses one accreditation record. The id is the path segment:
// the URL is the addressing authority, so it binds from there whatever a body says.
type accreditationRef struct {
	// ID is the accreditation record to read, from the path.
	ID string `json:"id"`
}

// GetAccreditation returns one tracked accreditation record.
//
// Example: {"id": "acc_1"}
func (o ops) getAccreditation(ctx context.Context, in *accreditationRef) (*accView, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	a, err := o.s.State.store.GetAccreditation(ctx, org, in.ID)
	if err == errNotFound {
		return nil, zip.ErrNotFound("accreditation not found")
	}
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "get accreditation: %v", err)
	}
	v := accViewOf(a)
	return &v, nil
}

// accreditationDecision is a reviewer's decision on one accreditation record.
type accreditationDecision struct {
	// ID is the accreditation record to decide, from the path.
	ID string `json:"id"`
	// Status is the decision being recorded: reviewer_confirmed, provider_verified,
	// rejected, or expired.
	Status AccreditationStatus `json:"status"`
}

// DecideAccreditation records an org reviewer's decision on an accreditation
// record — a reviewer confirmation, a provider verification the reviewer has
// evidence of (a CPA/attorney letter, a verifier report), a rejection, or an
// expiry. ROLE-GATED (an org admin or platform reviewer) and ATTRIBUTED: the
// reviewer's identity is recorded as ReviewerSub and audited. Human-in-the-loop:
// the platform never confirms on its own, and even a provider_verified state
// carries the reviewer who recorded it.
//
// Example: {"id": "acc_1", "status": "reviewer_confirmed"}
func (o ops) decideAccreditation(ctx context.Context, in *accreditationDecision) (*accView, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	c, ok := cloud.Request(ctx)
	if !ok || (!principal.IsSuperAdmin(c) && !principal.IsOrgAdmin(c)) {
		return nil, zip.ErrForbidden("an accreditation decision requires an org admin or platform reviewer")
	}
	if in.Status != AccReviewerConfirmed && in.Status != AccProviderVerified && in.Status != AccRejected && in.Status != AccExpired {
		return nil, zip.ErrBadRequest("decision status must be reviewer_confirmed, provider_verified, rejected, or expired")
	}
	reviewer := c.User()
	if reviewer == "" {
		return nil, zip.ErrForbidden("a reviewer decision requires a signed-in reviewer")
	}
	now := time.Now().Unix()
	if err := o.s.State.store.UpdateAccreditationDecision(ctx, org, in.ID, in.Status, reviewer, now); err != nil {
		if err == errNotFound {
			return nil, zip.ErrNotFound("accreditation not found")
		}
		return nil, zip.Errorf(http.StatusInternalServerError, "update accreditation: %v", err)
	}
	a, err := o.s.State.store.GetAccreditation(ctx, org, in.ID)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "get accreditation: %v", err)
	}
	emitAudit(o.s, ctx, "compliance.accreditation.decision", audit.Resource{Type: "compliance.accreditation", ID: a.ID},
		"success", http.StatusOK, map[string]any{"accreditationId": a.ID, "status": a.Status, "reviewerSub": reviewer})
	v := accViewOf(a)
	return &v, nil
}

// ---- records / audit ----

// recordList is the org's unified compliance-record view.
type recordList struct {
	// Verifications is the org's KYC/KYB checks, provider-reported statuses only.
	Verifications []checkView `json:"verifications"`
	// Accreditation is the org's tracked accreditation-state records.
	Accreditation []accView `json:"accreditation"`
	// Disclaimer states that statuses are provider-reported or tracked, never a
	// platform assertion of legal or regulatory compliance.
	Disclaimer string `json:"disclaimer"`
}

// ListRecords is the unified compliance-record view for the org: its verifications
// and accreditation records together, each provider-reported or tracked, never
// platform-asserted. PII stays in the subject store; records carry only opaque ids
// and statuses.
func (o ops) listRecords(ctx context.Context, in *listIn) (*recordList, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	chks, err := o.s.State.store.ListChecks(ctx, org, in.Limit)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list checks: %v", err)
	}
	accs, err := o.s.State.store.ListAccreditation(ctx, org, in.Limit)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list accreditation: %v", err)
	}
	cv := make([]checkView, 0, len(chks))
	for _, chk := range chks {
		cv = append(cv, checkViewOf(chk))
	}
	av := make([]accView, 0, len(accs))
	for _, a := range accs {
		av = append(av, accViewOf(a))
	}
	return &recordList{Verifications: cv, Accreditation: av, Disclaimer: Disclaimer}, nil
}

// auditIn narrows the compliance audit read.
type auditIn struct {
	// Result filters rows by outcome result: success, deny, or error; empty means all.
	Result string `json:"result"`
}

// auditList is the compliance slice of the shared audit trail.
type auditList struct {
	// Data is the org's compliance.* audit rows, newest first.
	Data []audit.Wire `json:"data"`
	// Disclaimer states that statuses are provider-reported or tracked, never a
	// platform assertion of legal or regulatory compliance.
	Disclaimer string `json:"disclaimer"`
}

// AuditRead is the compliance read of the SHARED tamper-evident audit plane —
// the SOC 2 posture surface (privileged actions: who started/decided what, when). The
// org is PINNED to the caller's validated org and the rows are narrowed to
// compliance.* actions. Fail-closed: no principal is a 403, no configured audit
// store a 501.
func (o ops) auditRead(ctx context.Context, in *auditIn) (*auditList, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	if o.s.State.audit == nil {
		return nil, zip.Errorf(http.StatusNotImplemented, "audit trail is not configured")
	}
	f := audit.Filter{Org: org, Result: strings.TrimSpace(in.Result), Limit: 1000}
	rows, _, err := o.s.State.audit.Query(ctx, f)
	if err != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "audit query failed")
	}
	out := make([]audit.Wire, 0, len(rows))
	for _, r := range rows {
		if strings.HasPrefix(r.Action, auditActionPrefix) {
			out = append(out, r.ToWire())
		}
	}
	noStore(ctx)
	return &auditList{Data: out, Disclaimer: Disclaimer}, nil
}

// ---- shared helpers ----

// emitAudit appends a compliance action to the shared tamper-evident trail. Nil
// recorder → no-op (an unconfigured deployment is never blocked). The `after` map
// carries opaque ids + statuses ONLY — NEVER subject PII — and is redacted as a
// second layer of defense. The actor is the validated principal (the acting user),
// never a subject; it is read off the request cloud.Bridge parked on the context.
// Off the HTTP path there is no request and no actor to attribute — and no caller
// either: every emitting op sits behind the org gate, which refuses there first.
func emitAudit(s *cloud.Service[state], ctx context.Context, action string, res audit.Resource, result string, statusCode int, after map[string]any) {
	if s.State.audit == nil {
		return
	}
	c, ok := cloud.Request(ctx)
	if !ok {
		return
	}
	rec := audit.Record{
		Actor:     audit.Actor{Org: mustOrg(c), Sub: c.User(), Email: c.UserEmail()},
		Action:    action,
		Resource:  res,
		Auth:      audit.AuthContext{Method: "gateway", IsAdmin: c.IsAdmin()},
		Outcome:   audit.Outcome{Result: result, Status: statusCode},
		Method:    c.Method(),
		Path:      c.Path(),
		SourceIP:  clientIP(c),
		RequestID: c.RequestID(),
		After:     audit.Redact(mustJSON(after)),
	}
	if _, err := s.State.audit.Append(c.Context(), rec); err != nil {
		s.Log.Warn("compliance audit append failed", "err", err, "action", action)
	}
}

// emitWebhookAudit records a signature-authenticated provider reconcile. There is NO
// user principal (the caller authenticated by HMAC, not a session), so the actor is
// the record's OWN org + the provider name — never a client-supplied org — and the
// auth method is recorded as "webhook", not "gateway". Nil recorder → no-op.
func emitWebhookAudit(s *cloud.Service[state], c *zip.Ctx, chk Check) {
	if s.State.audit == nil {
		return
	}
	rec := audit.Record{
		Actor:     audit.Actor{Org: chk.Org, Sub: chk.Provider},
		Action:    "compliance.verification.webhook",
		Resource:  audit.Resource{Type: "compliance.check", ID: chk.ID},
		Auth:      audit.AuthContext{Method: "webhook"},
		Outcome:   audit.Outcome{Result: "success", Status: http.StatusOK},
		Method:    c.Method(),
		Path:      c.Path(),
		SourceIP:  clientIP(c),
		RequestID: c.RequestID(),
		After:     audit.Redact(mustJSON(map[string]any{"checkId": chk.ID, "provider": chk.Provider, "status": chk.Status, "decidedBy": chk.DecidedBy})),
	}
	if _, err := s.State.audit.Append(c.Context(), rec); err != nil {
		s.Log.Warn("compliance webhook audit append failed", "err", err)
	}
}

func mustOrg(c *zip.Ctx) string {
	org, _ := principal.Org(c)
	return org
}

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return b
}

// clientIP is the caller's address, by the ONE rule — cloud.ClientIP. It lands in
// a durable audit record, and the LEFT-most X-Forwarded-For entry (and X-Real-Ip)
// are values the client writes: an address chosen by the party being audited is
// not evidence.
func clientIP(c *zip.Ctx) string { return cloud.ClientIP(c) }
