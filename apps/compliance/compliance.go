package compliance

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/audit"
	"github.com/hanzoai/cloud/apps/idv"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/zap-proto/zip"
)

// maxBody bounds a compliance request body — records are small structured values.
const maxBody = 1 << 20 // 1 MiB

// auditActionPrefix scopes the compliance-relevant slice of the shared audit plane.
const auditActionPrefix = "compliance."

// state is compliance's own data; shared deps live in the embedded cloud.Base. It
// holds the sealed record store, the verification provider seam (Manual by default;
// a real provider when configured), the OPTIONAL signature-authenticated provider
// webhook receiver (nil unless configured), and the shared audit recorder (nil-safe).
type state struct {
	store   *Store
	idv     idv.Provider
	webhook *idv.Webhook
	audit   *audit.Recorder
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
	if deps.Logger == nil {
		return fmt.Errorf("compliance.Mount: nil deps.Logger")
	}
	if deps.DataDir == "" {
		return fmt.Errorf("compliance.Mount: empty DataDir")
	}
	if err := os.MkdirAll(deps.DataDir, 0o755); err != nil {
		return fmt.Errorf("compliance.Mount: data dir: %w", err)
	}
	provider, err := idv.FromConfig(kmsGetter(deps), os.Getenv)
	if err != nil {
		return fmt.Errorf("compliance.Mount: idv provider: %w", err)
	}
	// The signature-authenticated provider webhook is FAIL-CLOSED at mount, like the
	// provider itself: a named-but-unresolvable secret fails the mount rather than
	// silently serving an unauthenticated endpoint. Nil (the default) means no webhook
	// path is served at all.
	webhook, err := idv.WebhookFromConfig(kmsGetter(deps), os.Getenv)
	if err != nil {
		return fmt.Errorf("compliance.Mount: idv webhook: %w", err)
	}
	store, err := openStore(filepath.Join(deps.DataDir, "compliance.db"))
	if err != nil {
		return fmt.Errorf("compliance.Mount: open store: %w", err)
	}
	s := &cloud.Service[state]{
		Base:  cloud.NewBase(deps, "compliance"),
		State: state{store: store, idv: provider, webhook: webhook, audit: deps.Audit},
	}
	mounted = s
	if err := routes(app, s); err != nil {
		return err
	}
	s.Log.Info("compliance mounted", "brand", deps.Brand, "provider", provider.Name(), "webhook", webhook != nil, "audit", deps.Audit != nil)
	return nil
}

// kmsGetter adapts deps.KMS into the idv.SecretFn the provider uses to resolve its
// sealed key. A nil KMS yields a resolver that errors — so a real provider that
// needs a key fails closed at mount rather than running keyless.
func kmsGetter(deps cloud.Deps) idv.SecretFn {
	if deps.KMS == nil {
		return func(context.Context, string) ([]byte, error) {
			return nil, fmt.Errorf("KMS not available")
		}
	}
	return deps.KMS.GetSecret
}

// routes registers the compliance surface. Static + collection routes register
// before :id params so an id can never shadow a sibling route (Fiber first-match).
func routes(app cloud.Router, s *cloud.Service[state]) error {
	zapp := cloud.ZipApp(app)
	if zapp == nil {
		return fmt.Errorf("compliance.Mount: router is not backed by a *zip.App; typed ops have nowhere to register")
	}
	o := ops{s: s}
	// The bridge FIRST — fiber runs middleware in registration order — bounded to
	// compliance's own subtree; every org-scoped op resolves its tenant through it.
	app.Group("/v1/compliance").Use(cloud.Bridge())

	zip.Get(zapp, "/v1/compliance/health", o.health)
	zip.Get(zapp, "/v1/compliance/status", o.status)
	zip.Get(zapp, "/v1/compliance/records", o.listRecords)
	zip.Get(zapp, "/v1/compliance/audit", o.auditRead)

	zip.Post(zapp, "/v1/compliance/subjects", o.createSubject, zip.WithStatus(http.StatusCreated))
	zip.Get(zapp, "/v1/compliance/subjects", o.listSubjects)
	zip.Get(zapp, "/v1/compliance/subjects/:id", o.getSubject)

	zip.Post(zapp, "/v1/compliance/verifications", o.startVerification, zip.WithStatus(http.StatusCreated))
	zip.Get(zapp, "/v1/compliance/verifications", o.listVerifications)
	// A terminal status is reachable by exactly three orthogonal paths, never a raw
	// client assertion: a SIGNATURE-authenticated provider webhook (push), an internal
	// provider RECONCILE (pull), or a role-gated, attributed reviewer DECISION. The
	// webhook route is static, registered before :id (Fiber first-match).
	//
	// It stays a RAW handler: it authenticates by HMAC over the EXACT request bytes,
	// which a typed op never sees — zip hands the handler a decoded In, and a
	// re-encoded body does not reproduce the signed bytes. Typing it would break
	// signature verification, so it is declared nowhere rather than declared wrong.
	app.Post("/v1/compliance/verifications/webhook", cloud.Handle(s, verificationWebhook))
	zip.Get(zapp, "/v1/compliance/verifications/:id", o.getVerification)
	zip.Post(zapp, "/v1/compliance/verifications/:id/refresh", o.refreshVerification)
	zip.Post(zapp, "/v1/compliance/verifications/:id/decision", o.decideVerification)

	zip.Post(zapp, "/v1/compliance/accreditation", o.createAccreditation, zip.WithStatus(http.StatusCreated))
	zip.Get(zapp, "/v1/compliance/accreditation", o.listAccreditation)
	zip.Get(zapp, "/v1/compliance/accreditation/:id", o.getAccreditation)
	zip.Post(zapp, "/v1/compliance/accreditation/:id/decision", o.decideAccreditation)
	return nil
}

// ops binds the service to compliance's typed handlers: a TypedHandler has no
// parameter for the service, so it arrives as a RECEIVER — also the one bound form
// cmd/zipdoc lifts prose from.
type ops struct{ s *cloud.Service[state] }

// None is the input of an op that takes none: no body, no query, no path param.
type None struct{}

// Page bounds a list read.
type Page struct {
	// Limit caps the rows returned; 0 means the store's own default.
	Limit int `json:"limit"`
}

// tenant resolves the org — the tenant-isolation KEY — that cloud.Bridge carried
// across the typed seam from the validated IAM owner claim. It is never an In
// field: an In field is what the caller says about itself.
func tenant(ctx context.Context) (string, error) {
	org, ok := principal.OrgFrom(ctx)
	if !ok {
		return "", zip.ErrForbidden("X-Org-Id required")
	}
	return org, nil
}

// reviewer admits a role-gated DECISION and returns the reviewer it will be
// attributed to. Fails closed off the HTTP path, where there is no reviewer.
func reviewer(ctx context.Context, what string) (string, error) {
	c, ok := cloud.Request(ctx)
	if !ok {
		return "", zip.ErrForbidden("a " + what + " requires an org admin or platform reviewer")
	}
	if !principal.IsSuperAdmin(c) && !principal.IsOrgAdmin(c) {
		return "", zip.ErrForbidden("a " + what + " requires an org admin or platform reviewer")
	}
	if c.User() == "" {
		return "", zip.ErrForbidden("a " + what + " requires a signed-in reviewer")
	}
	return c.User(), nil
}

// noStore marks a response uncacheable — these reads carry PII or posture. A no-op
// off the HTTP path, where there is no response to mark.
func noStore(ctx context.Context) {
	if c, ok := cloud.Request(ctx); ok {
		c.SetHeader("Cache-Control", "no-store")
	}
}

// capBody holds the request-body bound the raw handlers enforced before decoding.
// A typed op is handed its In already decoded, so the check moves to the top of
// each write op — same limit, same 413.
func capBody(ctx context.Context) error {
	c, ok := cloud.Request(ctx)
	if !ok {
		return nil
	}
	if len(c.Fiber().Body()) > maxBody {
		return zip.Errorf(http.StatusRequestEntityTooLarge, "request body too large")
	}
	return nil
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

// ---- health / status ----

// ProviderHealth is the subsystem's liveness answer.
type ProviderHealth struct {
	// Status is "ok" while the surface is serving.
	Status string `json:"status"`
	// Provider is the wired identity-verification provider.
	Provider string `json:"provider"`
}

// health reports that the compliance surface is serving and which identity
// provider is wired. It does NOT probe that provider — a provider outage must not
// fail liveness.
//
// Response: {"status": "ok", "provider": "manual"}
func (o ops) health(ctx context.Context, _ *None) (*ProviderHealth, error) {
	return &ProviderHealth{Status: "ok", Provider: o.s.State.idv.Name()}, nil
}

// VerificationTally counts the org's verifications by provider-reported status.
type VerificationTally struct {
	// ByStatus is the count per status, one key per status seen.
	ByStatus map[string]int `json:"byStatus"`
	// Total is the sum across statuses.
	Total int `json:"total"`
}

// Posture is the org's honest compliance posture.
type Posture struct {
	// Provider is the wired identity-verification provider.
	Provider string `json:"provider"`
	// Verifications is the per-status tally of the org's verifications.
	Verifications VerificationTally `json:"verifications"`
	// Disclaimer is the boundary made visible on the wire: Hanzo records what
	// licensed providers report; it does not itself determine compliance.
	Disclaimer string `json:"disclaimer"`
}

// status returns the org's compliance posture: the wired provider and the count of
// its verifications per provider-reported status. It is deliberately NOT a boolean
// "compliant" — the platform reports what providers said, nothing more.
//
// Response: {"provider": "manual", "verifications": {"byStatus": {"pending": 2}, "total": 2}, "disclaimer": "..."}
func (o ops) status(ctx context.Context, _ *None) (*Posture, error) {
	s := o.s
	org, err := tenant(ctx)
	if err != nil {
		return nil, err
	}
	counts, err := s.State.store.StatusCounts(ctx, org)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "status: %v", err)
	}
	total := 0
	for _, n := range counts {
		total += n
	}
	noStore(ctx)
	return &Posture{
		Provider:      s.State.idv.Name(),
		Verifications: VerificationTally{ByStatus: counts, Total: total},
		Disclaimer:    Disclaimer,
	}, nil
}

// ---- subjects ----

// subjectSummary is the PII-MINIMIZED list projection: it omits the subject's
// name/email so a list read never sprays PII. The full record (with contact PII) is
// returned only on an explicit single-subject GET.
type subjectSummary struct {
	// ID is the subject id, the value every verification and accreditation names.
	ID string `json:"id"`
	// Kind is individual or business.
	Kind SubjectKind `json:"kind"`
	// Ref is the org's own opaque external id for this subject.
	Ref string `json:"ref,omitempty"`
	// HasEmail reports whether contact email is on file, without disclosing it.
	HasEmail bool `json:"hasEmail"`
	// CreatedAt is the unix second the subject was recorded.
	CreatedAt int64 `json:"createdAt"`
}

// SubjectRequest records a person or business to verify.
type SubjectRequest struct {
	// Kind is individual or business, required.
	Kind SubjectKind `json:"kind"`
	// Ref is the org's own opaque external id; required when no email is given.
	Ref string `json:"ref"`
	// Email is contact PII, sealed at rest and returned only on a single-subject
	// read to the owning org. Required when no ref is given.
	Email string `json:"email"`
	// Name is contact PII, sealed at rest.
	Name string `json:"name"`
}

// createSubject records a person or business to verify. It needs an email or the
// org's own ref. Contact PII is sealed at rest and never returned by a list read.
//
// Example: {"kind": "individual", "ref": "cust_814", "email": "ada@acme.com", "name": "Ada Lovelace"}
func (o ops) createSubject(ctx context.Context, in *SubjectRequest) (*Subject, error) {
	s := o.s
	org, err := tenant(ctx)
	if err != nil {
		return nil, err
	}
	if err := capBody(ctx); err != nil {
		return nil, err
	}
	sub, err := newSubject(s, ctx, org, in.Kind, in.Ref, in.Email, in.Name)
	if err != nil {
		return nil, err
	}
	emitAudit(s, ctx, "compliance.subject.create", audit.Resource{Type: "compliance.subject", ID: sub.ID},
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
	id, err := genID("sub")
	if err != nil {
		return Subject{}, zip.Errorf(http.StatusInternalServerError, "rng: %v", err)
	}
	now := time.Now().Unix()
	sub := Subject{ID: id, Org: org, Kind: kind, Ref: ref, Email: email, Name: name, CreatedAt: now, UpdatedAt: now}
	if err := s.State.store.CreateSubject(ctx, sub); err != nil {
		return Subject{}, zip.Errorf(http.StatusInternalServerError, "create subject: %v", err)
	}
	return sub, nil
}

// SubjectList is the org's subjects, PII-minimized.
type SubjectList struct {
	// Data is one row per subject. It carries no name or email — only whether an
	// email is on file — so a list read never sprays PII.
	Data []subjectSummary `json:"data"`
}

// listSubjects returns the caller org's subjects without their contact PII: a row
// says whether an email is on file, never what it is.
//
// Example: {"limit": 50}
func (o ops) listSubjects(ctx context.Context, in *Page) (*SubjectList, error) {
	s := o.s
	org, err := tenant(ctx)
	if err != nil {
		return nil, err
	}
	subs, err := s.State.store.ListSubjects(ctx, org, in.Limit)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list subjects: %v", err)
	}
	out := make([]subjectSummary, 0, len(subs))
	for _, sub := range subs {
		out = append(out, subjectSummary{ID: sub.ID, Kind: sub.Kind, Ref: sub.Ref, HasEmail: sub.Email != "", CreatedAt: sub.CreatedAt})
	}
	noStore(ctx)
	return &SubjectList{Data: out}, nil
}

// SubjectRef addresses one of the caller org's subjects by id.
type SubjectRef struct {
	// ID is the subject id from the path, as returned by create.
	ID string `json:"id"`
}

// getSubject returns one of the caller org's subjects INCLUDING its contact PII.
// This is the only surface that discloses it, and only to the owning org.
//
// Example: {"id": "sub_4c1e9b7a2d6f0538e4a7c9b1d3f5027a"}
func (o ops) getSubject(ctx context.Context, in *SubjectRef) (*Subject, error) {
	s := o.s
	org, err := tenant(ctx)
	if err != nil {
		return nil, err
	}
	sub, err := s.State.store.GetSubject(ctx, org, in.ID)
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

// StartVerificationRequest opens a KYC/KYB verification. It names an existing
// subject, or supplies the fields to create one inline.
type StartVerificationRequest struct {
	// SubjectID verifies an existing subject. When empty, one is created inline
	// from kind/ref/email/name.
	SubjectID string `json:"subjectId"`
	// Kind is individual or business, for an inline subject.
	Kind SubjectKind `json:"kind"`
	// Ref is the org's own opaque external id, for an inline subject.
	Ref string `json:"ref"`
	// Email is contact PII, for an inline subject; sealed at rest.
	Email string `json:"email"`
	// Name is contact PII, for an inline subject; sealed at rest.
	Name string `json:"name"`
}

// startVerification opens a KYC/KYB verification for a subject through the wired
// provider, creating the subject inline when no subjectId is given. A start is
// never a decision: the record comes back pending even if the provider reports
// otherwise, so no path here yields a verified check.
//
// Example: {"kind": "individual", "email": "ada@acme.com", "name": "Ada Lovelace"}
func (o ops) startVerification(ctx context.Context, in *StartVerificationRequest) (*CheckView, error) {
	s := o.s
	org, err := tenant(ctx)
	if err != nil {
		return nil, err
	}
	if err := capBody(ctx); err != nil {
		return nil, err
	}

	// Resolve the subject: an existing one by id, or create one inline from the body.
	var sub Subject
	if strings.TrimSpace(in.SubjectID) != "" {
		sub, err = s.State.store.GetSubject(ctx, org, in.SubjectID)
		if err == errNotFound {
			return nil, zip.ErrNotFound("subject not found")
		}
		if err != nil {
			return nil, zip.Errorf(http.StatusInternalServerError, "get subject: %v", err)
		}
	} else {
		sub, err = newSubject(s, ctx, org, in.Kind, in.Ref, in.Email, in.Name)
		if err != nil {
			return nil, err
		}
	}

	// Start the verification through the provider seam. The returned status is
	// provider-reported and, by the seam's contract, non-terminal on a fresh start —
	// there is NO path here that yields a verified check. A provider error is a 502;
	// it never degrades to verified.
	sess, err := s.State.idv.Start(ctx, org, idv.Subject{Kind: sub.Kind, Name: sub.Name, Email: sub.Email, Ref: sub.Ref})
	if err != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "verification start failed")
	}
	// A "start" is never a decision. Clamp any terminal status the provider returns
	// here to pending at the product boundary — belt-and-suspenders over the idv
	// seam's own downgrade, so "creating a verification can never yield a verified
	// record" holds even against a misbehaving or compromised provider adapter.
	initial := sess.Status
	if initial.Terminal() {
		initial = idv.StatusPending
	}
	id, err := genID("chk")
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "rng: %v", err)
	}
	now := time.Now().Unix()
	chk := Check{
		ID: id, Org: org, SubjectID: sub.ID, Kind: sub.Kind,
		Provider: s.State.idv.Name(), ProviderRef: sess.Ref, VerifyURL: sess.VerifyURL,
		Status: initial, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.State.store.CreateCheck(ctx, chk); err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "create check: %v", err)
	}
	emitAudit(s, ctx, "compliance.verification.start", audit.Resource{Type: "compliance.check", ID: chk.ID},
		"success", http.StatusCreated,
		map[string]any{"checkId": chk.ID, "subjectId": sub.ID, "provider": chk.Provider, "kind": chk.Kind, "status": chk.Status})
	v := checkView(chk)
	return &v, nil
}

// VerificationList is the org's verifications.
type VerificationList struct {
	// Data is one row per verification, each carrying only opaque ids and the
	// provider-reported status — never subject PII.
	Data []CheckView `json:"data"`
	// Disclaimer is the boundary made visible on the wire.
	Disclaimer string `json:"disclaimer"`
}

// listVerifications returns the caller org's verifications, each with its
// provider-reported status and no subject PII.
//
// Example: {"limit": 50}
func (o ops) listVerifications(ctx context.Context, in *Page) (*VerificationList, error) {
	s := o.s
	org, err := tenant(ctx)
	if err != nil {
		return nil, err
	}
	chks, err := s.State.store.ListChecks(ctx, org, in.Limit)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list checks: %v", err)
	}
	out := make([]CheckView, 0, len(chks))
	for _, chk := range chks {
		out = append(out, checkView(chk))
	}
	return &VerificationList{Data: out, Disclaimer: Disclaimer}, nil
}

// CheckRef addresses one of the caller org's verifications by id.
type CheckRef struct {
	// ID is the verification id from the path, as returned by start.
	ID string `json:"id"`
}

// getVerification returns one of the caller org's verifications with its current
// recorded status.
//
// Example: {"id": "chk_4c1e9b7a2d6f0538e4a7c9b1d3f5027a"}
func (o ops) getVerification(ctx context.Context, in *CheckRef) (*CheckView, error) {
	s := o.s
	org, err := tenant(ctx)
	if err != nil {
		return nil, err
	}
	chk, err := s.State.store.GetCheck(ctx, org, in.ID)
	if err == errNotFound {
		return nil, zip.ErrNotFound("verification not found")
	}
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "get check: %v", err)
	}
	v := checkView(chk)
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

// refreshVerification is the internal PULL reconcile: an org member triggers a poll of
// the wired provider for the current decision. For the Manual provider this stays
// pending; for a hosted provider it reflects the provider's settled status,
// provider-attributed. A poll error is a 502, never a verification.
// refreshVerification polls the wired provider for a verification's current
// decision and records it, attributed to the provider. With the manual provider it
// stays pending; a poll error is a 502 and never a verification.
//
// Example: {"id": "chk_4c1e9b7a2d6f0538e4a7c9b1d3f5027a"}
func (o ops) refreshVerification(ctx context.Context, in *CheckRef) (*CheckView, error) {
	s := o.s
	org, err := tenant(ctx)
	if err != nil {
		return nil, err
	}
	chk, err := s.State.store.GetCheck(ctx, org, in.ID)
	if err == errNotFound {
		return nil, zip.ErrNotFound("verification not found")
	}
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "get check: %v", err)
	}
	chk, changed, err := reconcileCheck(s, ctx, chk)
	if err != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "verification refresh failed")
	}
	if changed {
		emitAudit(s, ctx, "compliance.verification.refresh", audit.Resource{Type: "compliance.check", ID: chk.ID},
			"success", http.StatusOK, map[string]any{"checkId": chk.ID, "provider": chk.Provider, "status": chk.Status})
	}
	v := checkView(chk)
	return &v, nil
}

// verificationWebhook is the external PUSH reconcile: a provider (or a Hanzo relay)
// signals that a verification settled. It authenticates by HMAC SIGNATURE (not an
// internal principal — an external caller has no validated org), locates the check by
// the provider reference the signed payload names, and RECONCILES the status from the
// provider API. The body carries no trusted decision, so a valid signature cannot
// force a status — the wired provider is the source of truth, and Manual stays
// pending. Disabled (501) unless a webhook secret is configured.
func verificationWebhook(s *cloud.Service[state], c *zip.Ctx) error {
	if s.State.webhook == nil {
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
	return c.JSON(http.StatusOK, checkView(chk))
}

// decideVerification records a privileged reviewer's MANUAL decision on a verification.
// It is the human-in-the-loop path — the ONLY route to a passing status when no real
// provider is wired — and it produces a DISTINCT reviewer_confirmed, NEVER a
// provider_verified (a provider decision is the provider's to report, via the webhook
// or a reconcile). It is ROLE-GATED (an org admin or platform reviewer) AND ATTRIBUTED
// (the reviewer's user id is DecidedBy), so a manual pass is always accountable.
// CheckDecision is a reviewer's manual decision on a verification.
type CheckDecision struct {
	// ID is the verification id from the path; a body value is ignored.
	ID string `json:"id"`
	// Status must be reviewer_confirmed (a pass) or manual_review (withhold). A
	// provider_verified or provider_rejected status is the provider's to report and
	// is refused here.
	Status idv.Status `json:"status"`
}

// decideVerification records a reviewer's manual decision on a verification — the
// human-in-the-loop path, and the only route to a passing status when no real
// provider is wired. It requires an org admin or platform reviewer and is
// attributed to them, and it can only record reviewer_confirmed or manual_review.
//
// Example: {"id": "chk_4c1e9b7a2d6f0538e4a7c9b1d3f5027a", "status": "reviewer_confirmed"}
func (o ops) decideVerification(ctx context.Context, in *CheckDecision) (*CheckView, error) {
	s := o.s
	org, err := tenant(ctx)
	if err != nil {
		return nil, err
	}
	who, err := reviewer(ctx, "verification decision")
	if err != nil {
		return nil, err
	}
	if err := capBody(ctx); err != nil {
		return nil, err
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
	if err := s.State.store.UpdateCheckStatus(ctx, org, in.ID, in.Status, who, now, decidedAt); err != nil {
		if err == errNotFound {
			return nil, zip.ErrNotFound("verification not found")
		}
		return nil, zip.Errorf(http.StatusInternalServerError, "update check: %v", err)
	}
	chk, err := s.State.store.GetCheck(ctx, org, in.ID)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "get check: %v", err)
	}
	emitAudit(s, ctx, "compliance.verification.decision", audit.Resource{Type: "compliance.check", ID: chk.ID},
		"success", http.StatusOK, map[string]any{"checkId": chk.ID, "status": chk.Status, "decidedBy": who})
	v := checkView(chk)
	return &v, nil
}

// ---- accreditation (state tracking) ----

// AccreditationRequest records an ASSERTED accreditation state for a subject.
type AccreditationRequest struct {
	// SubjectID is the subject the assertion is about; it must exist in the org.
	SubjectID string `json:"subjectId"`
	// Method is how the state was established: self_attested, third_party_letter
	// or provider_verified.
	Method AccreditationMethod `json:"method"`
	// Basis is what qualifies the subject: income, net_worth,
	// professional_license or entity.
	Basis AccreditationBasis `json:"basis"`
	// Status may only be asserted (or empty, which means asserted). Every
	// confirmed, rejected or expired state is set through the decision endpoint,
	// so it always carries the reviewer who recorded it.
	Status AccreditationStatus `json:"status"`
	// EvidenceDocID references the supporting document, if any.
	EvidenceDocID string `json:"evidenceDocId"`
	// Note is a non-PII operator note.
	Note string `json:"note"`
	// ExpiresAt is the unix second the assertion lapses, 0 for none.
	ExpiresAt int64 `json:"expiresAt"`
}

// createAccreditation records an ASSERTED accreditation state for one of the org's
// subjects. A create can only ever record an assertion — every confirmed, rejected
// or expired state goes through the decision endpoint, so it always names the
// reviewer who recorded it.
//
// Example: {"subjectId": "sub_4c1e9b7a2d6f0538e4a7c9b1d3f5027a", "method": "self_attested", "basis": "income"}
func (o ops) createAccreditation(ctx context.Context, in *AccreditationRequest) (*AccreditationView, error) {
	s := o.s
	org, err := tenant(ctx)
	if err != nil {
		return nil, err
	}
	if err := capBody(ctx); err != nil {
		return nil, err
	}
	body := *in
	if !validAccMethod(body.Method) {
		return nil, zip.ErrBadRequest("method must be self_attested, third_party_letter, or provider_verified")
	}
	if !validAccBasis(body.Basis) {
		return nil, zip.ErrBadRequest("basis must be income, net_worth, professional_license, or entity")
	}
	// A create records only an ASSERTED state — the subject's own assertion, with no
	// verifier. Every CONFIRMED state (provider_verified, reviewer_confirmed) and every
	// rejected/expired state is a DECISION that goes through the decision endpoint, so
	// it is always ATTRIBUTED to the reviewer who recorded it — a create can never
	// stamp a confirmation with no verifier.
	st := body.Status
	if st == "" {
		st = AccAsserted
	}
	if st != AccAsserted {
		return nil, zip.ErrBadRequest("on create, status may only be asserted; a provider_verified/reviewer_confirmed/rejected/expired state is set via the decision endpoint")
	}
	// The subject must exist within the org.
	if _, err := s.State.store.GetSubject(ctx, org, body.SubjectID); err == errNotFound {
		return nil, zip.ErrNotFound("subject not found")
	} else if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "get subject: %v", err)
	}
	id, err := genID("acc")
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "rng: %v", err)
	}
	now := time.Now().Unix()
	a := Accreditation{
		ID: id, Org: org, SubjectID: body.SubjectID, Method: body.Method, Basis: body.Basis,
		Status: st, EvidenceDocID: strings.TrimSpace(body.EvidenceDocID), Note: strings.TrimSpace(body.Note),
		ExpiresAt: body.ExpiresAt, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.State.store.CreateAccreditation(ctx, a); err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "create accreditation: %v", err)
	}
	emitAudit(s, ctx, "compliance.accreditation.create", audit.Resource{Type: "compliance.accreditation", ID: a.ID},
		"success", http.StatusCreated, map[string]any{"accreditationId": a.ID, "subjectId": a.SubjectID, "method": a.Method, "basis": a.Basis, "status": a.Status})
	v := accView(a)
	return &v, nil
}

// AccreditationList is the org's accreditation records.
type AccreditationList struct {
	// Data is one row per accreditation record.
	Data []AccreditationView `json:"data"`
	// Disclaimer is the boundary made visible on the wire.
	Disclaimer string `json:"disclaimer"`
}

// listAccreditation returns the caller org's accreditation records with their
// method, basis and current state.
//
// Example: {"limit": 50}
func (o ops) listAccreditation(ctx context.Context, in *Page) (*AccreditationList, error) {
	s := o.s
	org, err := tenant(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.State.store.ListAccreditation(ctx, org, in.Limit)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list accreditation: %v", err)
	}
	out := make([]AccreditationView, 0, len(rows))
	for _, a := range rows {
		out = append(out, accView(a))
	}
	return &AccreditationList{Data: out, Disclaimer: Disclaimer}, nil
}

// AccreditationRef addresses one of the caller org's accreditation records by id.
type AccreditationRef struct {
	// ID is the accreditation id from the path, as returned by create.
	ID string `json:"id"`
}

// getAccreditation returns one of the caller org's accreditation records.
//
// Example: {"id": "acc_4c1e9b7a2d6f0538e4a7c9b1d3f5027a"}
func (o ops) getAccreditation(ctx context.Context, in *AccreditationRef) (*AccreditationView, error) {
	s := o.s
	org, err := tenant(ctx)
	if err != nil {
		return nil, err
	}
	a, err := s.State.store.GetAccreditation(ctx, org, in.ID)
	if err == errNotFound {
		return nil, zip.ErrNotFound("accreditation not found")
	}
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "get accreditation: %v", err)
	}
	v := accView(a)
	return &v, nil
}

// decideAccreditation records an org reviewer's decision on an accreditation record.
// The status must be a decision the reviewer is recording — a reviewer confirmation, a
// provider verification the reviewer has evidence of (a CPA/attorney letter, a
// verifier report), a rejection, or an expiry — and the reviewer's identity is
// recorded as ReviewerSub and audited. Human-in-the-loop: the platform never confirms
// on its own, and even a provider_verified state carries the reviewer who recorded it.
// AccreditationDecision is a reviewer's decision on an accreditation record.
type AccreditationDecision struct {
	// ID is the accreditation id from the path; a body value is ignored.
	ID string `json:"id"`
	// Status must be reviewer_confirmed, provider_verified, rejected or expired.
	Status AccreditationStatus `json:"status"`
}

// decideAccreditation records a reviewer's decision on an accreditation record and
// attributes it to them. It requires an org admin or platform reviewer: the
// platform never confirms on its own, and even a provider_verified state carries
// the reviewer who recorded the evidence.
//
// Example: {"id": "acc_4c1e9b7a2d6f0538e4a7c9b1d3f5027a", "status": "reviewer_confirmed"}
func (o ops) decideAccreditation(ctx context.Context, in *AccreditationDecision) (*AccreditationView, error) {
	s := o.s
	org, err := tenant(ctx)
	if err != nil {
		return nil, err
	}
	who, err := reviewer(ctx, "accreditation decision")
	if err != nil {
		return nil, err
	}
	if err := capBody(ctx); err != nil {
		return nil, err
	}
	if in.Status != AccReviewerConfirmed && in.Status != AccProviderVerified && in.Status != AccRejected && in.Status != AccExpired {
		return nil, zip.ErrBadRequest("decision status must be reviewer_confirmed, provider_verified, rejected, or expired")
	}
	now := time.Now().Unix()
	if err := s.State.store.UpdateAccreditationDecision(ctx, org, in.ID, in.Status, who, now); err != nil {
		if err == errNotFound {
			return nil, zip.ErrNotFound("accreditation not found")
		}
		return nil, zip.Errorf(http.StatusInternalServerError, "update accreditation: %v", err)
	}
	a, err := s.State.store.GetAccreditation(ctx, org, in.ID)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "get accreditation: %v", err)
	}
	emitAudit(s, ctx, "compliance.accreditation.decision", audit.Resource{Type: "compliance.accreditation", ID: a.ID},
		"success", http.StatusOK, map[string]any{"accreditationId": a.ID, "status": a.Status, "reviewerSub": who})
	v := accView(a)
	return &v, nil
}

// ---- records / audit ----

// listRecords is the unified compliance-record view for the org: its verifications
// and accreditation records together, each provider-reported/tracked, never platform-
// asserted. PII stays in the subject store; records carry only opaque ids + statuses.
// Records is the org's verifications and accreditation records together.
type Records struct {
	// Verifications are the org's KYC/KYB checks, provider-reported.
	Verifications []CheckView `json:"verifications"`
	// Accreditation is the org's accreditation records, tracked.
	Accreditation []AccreditationView `json:"accreditation"`
	// Disclaimer is the boundary made visible on the wire.
	Disclaimer string `json:"disclaimer"`
}

// listRecords returns the caller org's verifications and accreditation records
// together. Every row carries opaque ids and statuses only — subject PII stays in
// the subject store.
//
// Example: {"limit": 50}
func (o ops) listRecords(ctx context.Context, in *Page) (*Records, error) {
	s := o.s
	org, err := tenant(ctx)
	if err != nil {
		return nil, err
	}
	chks, err := s.State.store.ListChecks(ctx, org, in.Limit)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list checks: %v", err)
	}
	accs, err := s.State.store.ListAccreditation(ctx, org, in.Limit)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list accreditation: %v", err)
	}
	cv := make([]CheckView, 0, len(chks))
	for _, chk := range chks {
		cv = append(cv, checkView(chk))
	}
	av := make([]AccreditationView, 0, len(accs))
	for _, a := range accs {
		av = append(av, accView(a))
	}
	return &Records{Verifications: cv, Accreditation: av, Disclaimer: Disclaimer}, nil
}

// auditRead is the compliance-scoped read of the SHARED tamper-evident audit plane —
// the SOC 2 posture surface (privileged actions: who started/decided what, when). The
// org is PINNED to the caller's validated org (a client `org` param is ignored) and
// the rows are narrowed to compliance.* actions. Fail-closed: no principal → 403, no
// store → 501.
// AuditFilter narrows the compliance-scoped audit read.
type AuditFilter struct {
	// Result keeps only rows with that outcome (success, denied, ...).
	Result string `json:"result"`
}

// AuditTrail is the compliance slice of the shared tamper-evident audit plane.
type AuditTrail struct {
	// Data is one row per privileged compliance action: who started or decided
	// what, and when. It is pinned to the caller's own org.
	Data []audit.Wire `json:"data"`
	// Disclaimer is the boundary made visible on the wire.
	Disclaimer string `json:"disclaimer"`
}

// auditRead returns the caller org's compliance actions from the shared
// tamper-evident audit trail — who started or decided what, and when. The org is
// pinned to the caller's own; 501 when no audit store is configured.
//
// Example: {"result": "success"}
func (o ops) auditRead(ctx context.Context, in *AuditFilter) (*AuditTrail, error) {
	s := o.s
	org, err := tenant(ctx)
	if err != nil {
		return nil, err
	}
	if s.State.audit == nil {
		return nil, zip.Errorf(http.StatusNotImplemented, "audit trail is not configured")
	}
	f := audit.Filter{Org: org, Result: strings.TrimSpace(in.Result), Limit: 1000}
	rows, _, err := s.State.audit.Query(ctx, f)
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
	return &AuditTrail{Data: out, Disclaimer: Disclaimer}, nil
}

// ---- views ----

// CheckView is a verification WITHOUT any subject PII — only the opaque subjectId
// and the provider-reported status. It is the shape every list, detail and record
// surface uses, so no PII path can leak through a verification response.
type CheckView struct {
	// ID is the verification id.
	ID string `json:"id"`
	// SubjectID is the opaque subject this verifies.
	SubjectID string `json:"subjectId"`
	// Kind is individual or business.
	Kind SubjectKind `json:"kind"`
	// Provider is the identity provider that holds the verification.
	Provider string `json:"provider"`
	// Status is provider-reported or pending — never platform-asserted.
	Status idv.Status `json:"status"`
	// CreatedAt is the unix second the verification was started.
	CreatedAt int64 `json:"createdAt"`
	// UpdatedAt is the unix second of the last recorded change.
	UpdatedAt int64 `json:"updatedAt"`
	// VerifyURL is where the subject completes verification, while one is open.
	VerifyURL string `json:"verifyUrl,omitempty"`
	// DecidedBy names who settled it: the provider, or the reviewer's user id.
	DecidedBy string `json:"decidedBy,omitempty"`
	// DecidedAt is the unix second it reached a terminal status.
	DecidedAt int64 `json:"decidedAt,omitempty"`
}

// AccreditationView is one accreditation record on the wire.
type AccreditationView struct {
	// ID is the accreditation id.
	ID string `json:"id"`
	// SubjectID is the opaque subject the record is about.
	SubjectID string `json:"subjectId"`
	// Method is how the state was established.
	Method AccreditationMethod `json:"method"`
	// Basis is what qualifies the subject.
	Basis AccreditationBasis `json:"basis"`
	// Status is the tracked state: asserted until a reviewer decides.
	Status AccreditationStatus `json:"status"`
	// CreatedAt is the unix second the record was created.
	CreatedAt int64 `json:"createdAt"`
	// UpdatedAt is the unix second of the last recorded change.
	UpdatedAt int64 `json:"updatedAt"`
	// EvidenceDocID references the supporting document, when one is named.
	EvidenceDocID string `json:"evidenceDocId,omitempty"`
	// ReviewerSub is the org user who recorded the decision, once decided.
	ReviewerSub string `json:"reviewerSub,omitempty"`
	// Note is a non-PII operator note, when one is set.
	Note string `json:"note,omitempty"`
	// ExpiresAt is the unix second the record lapses, when one is set.
	ExpiresAt int64 `json:"expiresAt,omitempty"`
}

// checkView projects a verification onto the PII-free wire shape.
func checkView(chk Check) CheckView {
	return CheckView{
		ID: chk.ID, SubjectID: chk.SubjectID, Kind: chk.Kind, Provider: chk.Provider,
		Status: chk.Status, CreatedAt: chk.CreatedAt, UpdatedAt: chk.UpdatedAt,
		VerifyURL: chk.VerifyURL, DecidedBy: chk.DecidedBy, DecidedAt: chk.DecidedAt,
	}
}

func accView(a Accreditation) AccreditationView {
	return AccreditationView{
		ID: a.ID, SubjectID: a.SubjectID, Method: a.Method, Basis: a.Basis, Status: a.Status,
		CreatedAt: a.CreatedAt, UpdatedAt: a.UpdatedAt, EvidenceDocID: a.EvidenceDocID,
		ReviewerSub: a.ReviewerSub, Note: a.Note, ExpiresAt: a.ExpiresAt,
	}
}

// ---- shared helpers ----

// emitAudit appends a compliance action to the shared tamper-evident trail. Nil
// recorder → no-op (an unconfigured deployment is never blocked). The `after` map
// carries opaque ids + statuses ONLY — NEVER subject PII — and is redacted as a
// second layer of defense. The actor is the validated principal (the acting user),
// never a subject.
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
	if _, err := s.State.audit.Append(ctx, rec); err != nil {
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

// decode reads and size-limits the JSON request body. An empty body decodes to the
// zero value (so an optional-body POST is fine).
func decode(c *zip.Ctx, v any) error {
	raw := c.Fiber().Body()
	if len(raw) == 0 {
		return nil
	}
	if len(raw) > maxBody {
		return zip.Errorf(http.StatusRequestEntityTooLarge, "request body too large")
	}
	return c.Bind(v)
}

func limitOf(c *zip.Ctx) int {
	n, _ := strconv.Atoi(strings.TrimSpace(c.Query("limit")))
	return n
}

// clientIP is the best-effort source IP for the audit record.
func clientIP(c *zip.Ctx) string {
	if xff := c.Header("X-Forwarded-For"); xff != "" {
		if i := strings.IndexByte(xff, ','); i >= 0 {
			return strings.TrimSpace(xff[:i])
		}
		return strings.TrimSpace(xff)
	}
	return c.Header("X-Real-Ip")
}

// genID mints a prefixed, collision-resistant id (prefix + 128 random bits).
func genID(prefix string) (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return prefix + "_" + hex.EncodeToString(b[:]), nil
}
