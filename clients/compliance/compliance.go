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
	"github.com/hanzoai/cloud/clients/idv"
	"github.com/hanzoai/cloud/clients/principal"
	"github.com/zap-proto/zip"
)

// maxBody bounds a compliance request body — records are small structured values.
const maxBody = 1 << 20 // 1 MiB

// auditActionPrefix scopes the compliance-relevant slice of the shared audit plane.
const auditActionPrefix = "compliance."

// state is compliance's own data; shared deps live in the embedded cloud.Base. It
// holds the sealed record store, the verification provider seam (Manual by default;
// a real provider when configured), and the shared audit recorder (nil-safe).
type state struct {
	store *Store
	idv   idv.Provider
	audit *audit.Recorder
}

// mounted is the process-wide handle so Shutdown can close the store (mirrors
// clients/security). Set by Mount, read by Shutdown.
var mounted *cloud.Service[state]

// Mount wires /v1/compliance/* and opens the sealed per-deployment store under
// {DataDir}/compliance.db. The verification provider is resolved from config
// (idv.FromConfig): Manual by default, a real provider when named — and FAIL-CLOSED,
// so a named-but-misconfigured provider fails the mount rather than silently
// downgrading to Manual.
func Mount(app *zip.App, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("compliance.Mount: nil zip.App")
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
	store, err := openStore(filepath.Join(deps.DataDir, "compliance.db"))
	if err != nil {
		return fmt.Errorf("compliance.Mount: open store: %w", err)
	}
	s := &cloud.Service[state]{
		Base:  cloud.NewBase(deps, "compliance"),
		State: state{store: store, idv: provider, audit: deps.Audit},
	}
	mounted = s
	routes(app, s)
	s.Log.Info("compliance mounted", "brand", deps.Brand, "provider", provider.Name(), "audit", deps.Audit != nil)
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
func routes(app *zip.App, s *cloud.Service[state]) {
	g := app.Group("/v1/compliance")
	g.Get("/health", cloud.Handle(s, health))
	g.Get("/status", cloud.Handle(s, status))
	g.Get("/records", cloud.Handle(s, listRecords))
	g.Get("/audit", cloud.Handle(s, auditRead))

	g.Post("/subjects", cloud.Handle(s, createSubject))
	g.Get("/subjects", cloud.Handle(s, listSubjects))
	g.Get("/subjects/:id", cloud.Handle(s, getSubject))

	g.Post("/verifications", cloud.Handle(s, startVerification))
	g.Get("/verifications", cloud.Handle(s, listVerifications))
	g.Post("/verifications/callback", cloud.Handle(s, verificationCallback))
	g.Get("/verifications/:id", cloud.Handle(s, getVerification))
	g.Post("/verifications/:id/refresh", cloud.Handle(s, refreshVerification))

	g.Post("/accreditation", cloud.Handle(s, createAccreditation))
	g.Get("/accreditation", cloud.Handle(s, listAccreditation))
	g.Get("/accreditation/:id", cloud.Handle(s, getAccreditation))
	g.Post("/accreditation/:id/decision", cloud.Handle(s, decideAccreditation))
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

// health is fail-open liveness: the subsystem opened its store, so it is live. It
// does NOT probe the external provider (a provider outage must not fail liveness).
func health(s *cloud.Service[state], c *zip.Ctx) error {
	return c.JSON(http.StatusOK, map[string]any{"status": "ok", "provider": s.State.idv.Name()})
}

// status is the org's honest posture read: the wired provider and the per-status
// tally of its verifications. It is deliberately NOT a boolean "compliant" — it
// reports counts of provider-reported states and carries the boundary disclaimer.
func status(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := principal.Org(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	counts, err := s.State.store.StatusCounts(c.Context(), org)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "status: %v", err)
	}
	total := 0
	for _, n := range counts {
		total += n
	}
	c.SetHeader("Cache-Control", "no-store")
	return c.JSON(http.StatusOK, map[string]any{
		"provider":      s.State.idv.Name(),
		"verifications": map[string]any{"byStatus": counts, "total": total},
		"disclaimer":    Disclaimer,
	})
}

// ---- subjects ----

// subjectSummary is the PII-MINIMIZED list projection: it omits the subject's
// name/email so a list read never sprays PII. The full record (with contact PII) is
// returned only on an explicit single-subject GET.
type subjectSummary struct {
	ID        string      `json:"id"`
	Kind      SubjectKind `json:"kind"`
	Ref       string      `json:"ref,omitempty"`
	HasEmail  bool        `json:"hasEmail"`
	CreatedAt int64       `json:"createdAt"`
}

func createSubject(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := principal.Org(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	var body struct {
		Kind  SubjectKind `json:"kind"`
		Ref   string      `json:"ref"`
		Email string      `json:"email"`
		Name  string      `json:"name"`
	}
	if err := decode(c, &body); err != nil {
		return err
	}
	sub, err := newSubject(s, c.Context(), org, body.Kind, body.Ref, body.Email, body.Name)
	if err != nil {
		return err
	}
	emitAudit(s, c, "compliance.subject.create", audit.Resource{Type: "compliance.subject", ID: sub.ID},
		"success", http.StatusCreated, map[string]any{"subjectId": sub.ID, "kind": sub.Kind})
	return c.JSON(http.StatusCreated, sub)
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

func listSubjects(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := principal.Org(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	subs, err := s.State.store.ListSubjects(c.Context(), org, limitOf(c))
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "list subjects: %v", err)
	}
	out := make([]subjectSummary, 0, len(subs))
	for _, sub := range subs {
		out = append(out, subjectSummary{ID: sub.ID, Kind: sub.Kind, Ref: sub.Ref, HasEmail: sub.Email != "", CreatedAt: sub.CreatedAt})
	}
	c.SetHeader("Cache-Control", "no-store")
	return c.JSON(http.StatusOK, map[string]any{"data": out})
}

func getSubject(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := principal.Org(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	sub, err := s.State.store.GetSubject(c.Context(), org, c.Param("id"))
	if err == errNotFound {
		return zip.ErrNotFound("subject not found")
	}
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "get subject: %v", err)
	}
	// This explicit single-subject read is the only surface that returns contact PII,
	// and only to the owning org. No PII cache in any intermediary.
	c.SetHeader("Cache-Control", "no-store")
	return c.JSON(http.StatusOK, sub)
}

// ---- verifications (KYC / KYB) ----

func startVerification(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := principal.Org(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	var body struct {
		SubjectID string      `json:"subjectId"`
		Kind      SubjectKind `json:"kind"`
		Ref       string      `json:"ref"`
		Email     string      `json:"email"`
		Name      string      `json:"name"`
	}
	if err := decode(c, &body); err != nil {
		return err
	}

	// Resolve the subject: an existing one by id, or create one inline from the body.
	var sub Subject
	var err error
	if strings.TrimSpace(body.SubjectID) != "" {
		sub, err = s.State.store.GetSubject(c.Context(), org, body.SubjectID)
		if err == errNotFound {
			return zip.ErrNotFound("subject not found")
		}
		if err != nil {
			return zip.Errorf(http.StatusInternalServerError, "get subject: %v", err)
		}
	} else {
		sub, err = newSubject(s, c.Context(), org, body.Kind, body.Ref, body.Email, body.Name)
		if err != nil {
			return err
		}
	}

	// Start the verification through the provider seam. The returned status is
	// provider-reported and, by the seam's contract, non-terminal on a fresh start —
	// there is NO path here that yields a verified check. A provider error is a 502;
	// it never degrades to verified.
	sess, err := s.State.idv.Start(c.Context(), org, idv.Subject{Kind: sub.Kind, Name: sub.Name, Email: sub.Email, Ref: sub.Ref})
	if err != nil {
		return zip.Errorf(http.StatusBadGateway, "verification start failed")
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
		return zip.Errorf(http.StatusInternalServerError, "rng: %v", err)
	}
	now := time.Now().Unix()
	chk := Check{
		ID: id, Org: org, SubjectID: sub.ID, Kind: sub.Kind,
		Provider: s.State.idv.Name(), ProviderRef: sess.Ref, VerifyURL: sess.VerifyURL,
		Status: initial, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.State.store.CreateCheck(c.Context(), chk); err != nil {
		return zip.Errorf(http.StatusInternalServerError, "create check: %v", err)
	}
	emitAudit(s, c, "compliance.verification.start", audit.Resource{Type: "compliance.check", ID: chk.ID},
		"success", http.StatusCreated,
		map[string]any{"checkId": chk.ID, "subjectId": sub.ID, "provider": chk.Provider, "kind": chk.Kind, "status": chk.Status})
	return c.JSON(http.StatusCreated, checkView(chk))
}

func listVerifications(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := principal.Org(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	chks, err := s.State.store.ListChecks(c.Context(), org, limitOf(c))
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "list checks: %v", err)
	}
	out := make([]map[string]any, 0, len(chks))
	for _, chk := range chks {
		out = append(out, checkView(chk))
	}
	return c.JSON(http.StatusOK, map[string]any{"data": out, "disclaimer": Disclaimer})
}

func getVerification(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := principal.Org(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	chk, err := s.State.store.GetCheck(c.Context(), org, c.Param("id"))
	if err == errNotFound {
		return zip.ErrNotFound("verification not found")
	}
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "get check: %v", err)
	}
	return c.JSON(http.StatusOK, checkView(chk))
}

// refreshVerification polls the provider for the current decision and records it. For
// the Manual provider this stays pending (its decisions arrive via the callback);
// for a hosted provider it reflects the provider's settled status. Provider-reported
// only — a poll error is a 502, never a verification.
func refreshVerification(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := principal.Org(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	chk, err := s.State.store.GetCheck(c.Context(), org, c.Param("id"))
	if err == errNotFound {
		return zip.ErrNotFound("verification not found")
	}
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "get check: %v", err)
	}
	res, err := s.State.idv.Check(c.Context(), org, chk.ProviderRef)
	if err != nil {
		return zip.Errorf(http.StatusBadGateway, "verification refresh failed")
	}
	if res.Status.Valid() && res.Status != chk.Status {
		chk.Status = res.Status
		chk.UpdatedAt = time.Now().Unix()
		decidedBy := ""
		if res.Status.Terminal() {
			decidedBy, chk.DecidedBy = s.State.idv.Name(), s.State.idv.Name()
			chk.DecidedAt = chk.UpdatedAt
		}
		if err := s.State.store.UpdateCheckStatus(c.Context(), org, chk.ID, chk.Status, decidedBy, chk.UpdatedAt, chk.DecidedAt); err != nil {
			return zip.Errorf(http.StatusInternalServerError, "update check: %v", err)
		}
		emitAudit(s, c, "compliance.verification.refresh", audit.Resource{Type: "compliance.check", ID: chk.ID},
			"success", http.StatusOK, map[string]any{"checkId": chk.ID, "provider": chk.Provider, "status": chk.Status})
	}
	return c.JSON(http.StatusOK, checkView(chk))
}

// verificationCallback records a provider webhook or an org reviewer's decision — the
// authenticated, audited path to a terminal status for the manual flow. The status
// must be a valid provider status; the acting principal is recorded as DecidedBy, so
// a settled decision is always attributable. This is a privileged action.
func verificationCallback(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := principal.Org(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	var body struct {
		ID     string     `json:"id"`
		Ref    string     `json:"ref"`
		Status idv.Status `json:"status"`
	}
	if err := decode(c, &body); err != nil {
		return err
	}
	if !body.Status.Valid() {
		return zip.ErrBadRequest("status must be one of pending, provider_verified, provider_rejected, manual_review, expired")
	}
	var chk Check
	var err error
	switch {
	case strings.TrimSpace(body.ID) != "":
		chk, err = s.State.store.GetCheck(c.Context(), org, body.ID)
	case strings.TrimSpace(body.Ref) != "":
		chk, err = s.State.store.GetCheckByRef(c.Context(), org, body.Ref)
	default:
		return zip.ErrBadRequest("id or ref is required")
	}
	if err == errNotFound {
		return zip.ErrNotFound("verification not found")
	}
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "get check: %v", err)
	}

	now := time.Now().Unix()
	decidedBy := c.User() // the attributable reviewer/service that recorded the decision
	if decidedBy == "" {
		decidedBy = chk.Provider
	}
	decidedAt := int64(0)
	if body.Status.Terminal() {
		decidedAt = now
	}
	if err := s.State.store.UpdateCheckStatus(c.Context(), org, chk.ID, body.Status, decidedBy, now, decidedAt); err != nil {
		if err == errNotFound {
			return zip.ErrNotFound("verification not found")
		}
		return zip.Errorf(http.StatusInternalServerError, "update check: %v", err)
	}
	chk.Status, chk.DecidedBy, chk.UpdatedAt, chk.DecidedAt = body.Status, decidedBy, now, decidedAt
	emitAudit(s, c, "compliance.verification.decision", audit.Resource{Type: "compliance.check", ID: chk.ID},
		"success", http.StatusOK, map[string]any{"checkId": chk.ID, "status": chk.Status, "decidedBy": decidedBy})
	return c.JSON(http.StatusOK, checkView(chk))
}

// ---- accreditation (state tracking) ----

func createAccreditation(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := principal.Org(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	var body struct {
		SubjectID     string              `json:"subjectId"`
		Method        AccreditationMethod `json:"method"`
		Basis         AccreditationBasis  `json:"basis"`
		Status        AccreditationStatus `json:"status"`
		EvidenceDocID string              `json:"evidenceDocId"`
		Note          string              `json:"note"`
		ExpiresAt     int64               `json:"expiresAt"`
	}
	if err := decode(c, &body); err != nil {
		return err
	}
	if !validAccMethod(body.Method) {
		return zip.ErrBadRequest("method must be self_attested, third_party_letter, or provider_verified")
	}
	if !validAccBasis(body.Basis) {
		return zip.ErrBadRequest("basis must be income, net_worth, professional_license, or entity")
	}
	// A create records an ASSERTED or provider_verified state only. reviewer_confirmed
	// / rejected / expired are REVIEWER decisions — they go through the decision
	// endpoint so a confirmation is always attributed to a human reviewer.
	st := body.Status
	if st == "" {
		st = AccAsserted
	}
	if st != AccAsserted && st != AccProviderVerified {
		return zip.ErrBadRequest("on create, status may be asserted or provider_verified; a reviewer_confirmed/rejected/expired state is set via the decision endpoint")
	}
	// The subject must exist within the org.
	if _, err := s.State.store.GetSubject(c.Context(), org, body.SubjectID); err == errNotFound {
		return zip.ErrNotFound("subject not found")
	} else if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "get subject: %v", err)
	}
	id, err := genID("acc")
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "rng: %v", err)
	}
	now := time.Now().Unix()
	a := Accreditation{
		ID: id, Org: org, SubjectID: body.SubjectID, Method: body.Method, Basis: body.Basis,
		Status: st, EvidenceDocID: strings.TrimSpace(body.EvidenceDocID), Note: strings.TrimSpace(body.Note),
		ExpiresAt: body.ExpiresAt, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.State.store.CreateAccreditation(c.Context(), a); err != nil {
		return zip.Errorf(http.StatusInternalServerError, "create accreditation: %v", err)
	}
	emitAudit(s, c, "compliance.accreditation.create", audit.Resource{Type: "compliance.accreditation", ID: a.ID},
		"success", http.StatusCreated, map[string]any{"accreditationId": a.ID, "subjectId": a.SubjectID, "method": a.Method, "basis": a.Basis, "status": a.Status})
	return c.JSON(http.StatusCreated, accView(a))
}

func listAccreditation(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := principal.Org(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	rows, err := s.State.store.ListAccreditation(c.Context(), org, limitOf(c))
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "list accreditation: %v", err)
	}
	out := make([]map[string]any, 0, len(rows))
	for _, a := range rows {
		out = append(out, accView(a))
	}
	return c.JSON(http.StatusOK, map[string]any{"data": out, "disclaimer": Disclaimer})
}

func getAccreditation(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := principal.Org(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	a, err := s.State.store.GetAccreditation(c.Context(), org, c.Param("id"))
	if err == errNotFound {
		return zip.ErrNotFound("accreditation not found")
	}
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "get accreditation: %v", err)
	}
	return c.JSON(http.StatusOK, accView(a))
}

// decideAccreditation records an org reviewer's decision on an accreditation record.
// The status must be a reviewer decision; the reviewer's identity is recorded and the
// action is audited. Human-in-the-loop — the platform never confirms on its own.
func decideAccreditation(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := principal.Org(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	var body struct {
		Status AccreditationStatus `json:"status"`
	}
	if err := decode(c, &body); err != nil {
		return err
	}
	if body.Status != AccReviewerConfirmed && body.Status != AccRejected && body.Status != AccExpired {
		return zip.ErrBadRequest("decision status must be reviewer_confirmed, rejected, or expired")
	}
	reviewer := c.User()
	if reviewer == "" {
		return zip.ErrForbidden("a reviewer decision requires a signed-in reviewer")
	}
	now := time.Now().Unix()
	if err := s.State.store.UpdateAccreditationDecision(c.Context(), org, c.Param("id"), body.Status, reviewer, now); err != nil {
		if err == errNotFound {
			return zip.ErrNotFound("accreditation not found")
		}
		return zip.Errorf(http.StatusInternalServerError, "update accreditation: %v", err)
	}
	a, err := s.State.store.GetAccreditation(c.Context(), org, c.Param("id"))
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "get accreditation: %v", err)
	}
	emitAudit(s, c, "compliance.accreditation.decision", audit.Resource{Type: "compliance.accreditation", ID: a.ID},
		"success", http.StatusOK, map[string]any{"accreditationId": a.ID, "status": a.Status, "reviewerSub": reviewer})
	return c.JSON(http.StatusOK, accView(a))
}

// ---- records / audit ----

// listRecords is the unified compliance-record view for the org: its verifications
// and accreditation records together, each provider-reported/tracked, never platform-
// asserted. PII stays in the subject store; records carry only opaque ids + statuses.
func listRecords(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := principal.Org(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	chks, err := s.State.store.ListChecks(c.Context(), org, limitOf(c))
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "list checks: %v", err)
	}
	accs, err := s.State.store.ListAccreditation(c.Context(), org, limitOf(c))
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "list accreditation: %v", err)
	}
	cv := make([]map[string]any, 0, len(chks))
	for _, chk := range chks {
		cv = append(cv, checkView(chk))
	}
	av := make([]map[string]any, 0, len(accs))
	for _, a := range accs {
		av = append(av, accView(a))
	}
	return c.JSON(http.StatusOK, map[string]any{"verifications": cv, "accreditation": av, "disclaimer": Disclaimer})
}

// auditRead is the compliance-scoped read of the SHARED tamper-evident audit plane —
// the SOC 2 posture surface (privileged actions: who started/decided what, when). The
// org is PINNED to the caller's validated org (a client `org` param is ignored) and
// the rows are narrowed to compliance.* actions. Fail-closed: no principal → 403, no
// store → 501.
func auditRead(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := principal.Org(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	if s.State.audit == nil {
		return zip.Errorf(http.StatusNotImplemented, "audit trail is not configured")
	}
	f := audit.Filter{Org: org, Result: strings.TrimSpace(c.Query("result")), Limit: 1000}
	rows, _, err := s.State.audit.Query(c.Context(), f)
	if err != nil {
		return zip.Errorf(http.StatusBadGateway, "audit query failed")
	}
	out := make([]audit.Wire, 0, len(rows))
	for _, r := range rows {
		if strings.HasPrefix(r.Action, auditActionPrefix) {
			out = append(out, r.ToWire())
		}
	}
	c.SetHeader("Cache-Control", "no-store")
	return c.JSON(http.StatusOK, map[string]any{"data": out, "disclaimer": Disclaimer})
}

// ---- views ----

// checkView renders a verification WITHOUT any subject PII (only the opaque subjectId
// + provider-reported status). This is the shape every list/detail/record surface
// uses, so no PII path can leak through a verification response.
func checkView(chk Check) map[string]any {
	v := map[string]any{
		"id":        chk.ID,
		"subjectId": chk.SubjectID,
		"kind":      chk.Kind,
		"provider":  chk.Provider,
		"status":    chk.Status,
		"createdAt": chk.CreatedAt,
		"updatedAt": chk.UpdatedAt,
	}
	if chk.VerifyURL != "" {
		v["verifyUrl"] = chk.VerifyURL
	}
	if chk.DecidedBy != "" {
		v["decidedBy"] = chk.DecidedBy
	}
	if chk.DecidedAt != 0 {
		v["decidedAt"] = chk.DecidedAt
	}
	return v
}

func accView(a Accreditation) map[string]any {
	v := map[string]any{
		"id":        a.ID,
		"subjectId": a.SubjectID,
		"method":    a.Method,
		"basis":     a.Basis,
		"status":    a.Status,
		"createdAt": a.CreatedAt,
		"updatedAt": a.UpdatedAt,
	}
	if a.EvidenceDocID != "" {
		v["evidenceDocId"] = a.EvidenceDocID
	}
	if a.ReviewerSub != "" {
		v["reviewerSub"] = a.ReviewerSub
	}
	if a.Note != "" {
		v["note"] = a.Note
	}
	if a.ExpiresAt != 0 {
		v["expiresAt"] = a.ExpiresAt
	}
	return v
}

// ---- shared helpers ----

// emitAudit appends a compliance action to the shared tamper-evident trail. Nil
// recorder → no-op (an unconfigured deployment is never blocked). The `after` map
// carries opaque ids + statuses ONLY — NEVER subject PII — and is redacted as a
// second layer of defense. The actor is the validated principal (the acting user),
// never a subject.
func emitAudit(s *cloud.Service[state], c *zip.Ctx, action string, res audit.Resource, result string, statusCode int, after map[string]any) {
	if s.State.audit == nil {
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
