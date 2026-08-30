// Package security is secret scanning for your code: submit sources, get findings,
// masked never raw.
//
// It serves /v1/security: the pure detect engine finds hardcoded secrets, and
// findings persist masked and fingerprinted — never the raw secret.
package security

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/metering"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/hanzoai/cloud/apps/security/detect"
	"github.com/hanzoai/cloud/audit"
	"github.com/hanzoai/cloud/internal/mint"
	"github.com/zap-proto/zip"
)

// meterKind is the commerce meter key for a scan (product=security). One scan =
// one metered unit; a metering.Client publish bills it when configured.
const meterKind = "security.scan"

// feeEnvPrefix is the operator knob for what one scan costs:
// CLOUD_SECURITY_FEE_CENTS[_SCAN], defaulting to cloud.DefaultResourceFeeCents
// ($1.00) — the same policy default every other provisioned unit resolves through,
// never a price invented here. Set it to 0 to make scanning free (and un-gated).
//
// The surface declared cloud.Metered from the start, and the Meter call below passed
// a literal 0 for the amount. A zero debit posts NO ledger entry, so the promise the
// declaration makes — "a meter downstream of the edge owns the charge" — was kept by
// nothing: the platform required standing to run a scan and then charged for none of
// them. One constant, resolved the one way, closes it.
const feeEnvPrefix = "CLOUD_SECURITY_FEE_CENTS"

// maxFiles / maxBytes bound a single scan submission so one request can't OOM
// the process or wedge the engine. A caller with more source splits it into
// multiple scans.
const (
	maxFiles = 500
	maxBytes = 8 << 20 // 8 MiB of total content per scan
)

// projectRE matches the optional X-Project-Id sub-scope (same shape as the
// other subsystems). Invalid → treated as no sub-scope, not a 400.
var projectRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// state is security's own data; shared deps live in the embedded cloud.Base.
// It holds the findings store, the (nil-safe) audit recorder and the billing
// meter — kept here (not in Base.Bill) because its commerce provider label is
// meterKind ("security.scan"), NOT the subsystem name.
type state struct {
	store *Store
	audit *audit.Recorder
	bill  *cloud.Meter
}

// mounted is the process-wide handle so Shutdown can flush the store (mirrors
// clients/git). Set by Mount, read by Shutdown.
var mounted *cloud.Service[state]

// Mount wires /v1/security/* onto app and opens the per-deployment findings
// store under {DataDir}/security.db. It follows the clients/git contract:
// validate deps, open the store, register routes, return. The store lifecycle
// and package-global handle make this a direct construction (cloud.NewBase),
// not cloud.Use.
func Use(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("security.Use:  nil app")
	}
	if deps.DataDir == "" {
		return fmt.Errorf("security.Use:  empty DataDir")
	}
	store, err := openStore(deps.DataDir)
	if err != nil {
		return fmt.Errorf("security.Use:  open store: %w", err)
	}
	s := &cloud.Service[state]{
		Base: cloud.NewBase(deps, "security"),
		State: state{
			store: store,
			audit: deps.Audit,
			bill:  cloud.NewMeter(deps, meterKind),
		},
	}
	mounted = s

	routes(app, s)

	s.Log.Info("security mounted", "brand", deps.Brand, "rules", detect.RuleCount(),
		"dir", deps.DataDir)
	return nil
}

// routes registers the security surface. Static routes register before the :id
// routes registers the security surface. Static routes register before the :id
// params so a scan id can never shadow /rules or /health (Fiber first-match).
//
// Every op is TYPED: the input and the answer are Go types, so the schema, the
// prose, the MCP tool, the CLI command and every generated SDK method are
// projections of the handler itself. zipdoc lifts the doc comments into
// zipdoc_gen.go, which is the only way prose reaches the published registry — Go
// drops comments at compile time.
//
//go:generate go run github.com/zap-proto/zip/cmd/zipdoc
func routes(app cloud.Router, s *cloud.Service[state]) {
	g := app.Group("/v1/security")
	// cloud.DenyEnvelope BEFORE the leaves, because fiber runs middleware in
	// registration order and one installed after its leaves never runs. Submitting
	// a scan gates on the caller's balance, and the envelope is what makes that
	// refusal the fleet's own nested {"error":{"code","message"}} rather than a
	// second vocabulary for a refusal the platform already has words for.
	g.Use(cloud.DenyEnvelope())
	o := ops{s: s}

	zip.Get(g, "/health", o.health)
	zip.Get(g, "/rules", o.listRules)
	zip.Post(g, "/scans", o.submitScan, zip.WithStatus(http.StatusCreated))
	zip.Get(g, "/scans", o.listScans)
	zip.Get(g, "/findings", o.listFindings)
	zip.Get(g, "/scans/:id", o.getScan)
	zip.Get(g, "/findings/:id", o.getFinding)
}

// ops binds the service to the typed ops. A TypedHandler takes no service
// parameter, so the service arrives as a RECEIVER and every op is a method value
// — also the only bound form cmd/zipdoc can lift prose from.
type ops struct{ s *cloud.Service[state] }

// Shutdown closes the findings store. Idempotent; safe if Mount never ran.
func Shutdown() error {
	if mounted == nil || mounted.State.store == nil {
		return nil
	}
	err := mounted.State.store.Close()
	mounted = nil
	return err
}

// ---- request/response shapes ----

// scan is one file to scan. Its content is read in memory and never stored.
type scan struct {
	// Path is where the file lives, recorded on any finding so a result can be
	// located in the tree it came from.
	Path string `json:"path"`
	// Content is the source to scan. It is NEVER stored: what persists is the
	// finding, with a masked preview and a fingerprint.
	Content string `json:"content"`
}

// submitReq is a batch of files to scan for hardcoded secrets.
type submitReq struct {
	// Project names the sub-scope the scan is filed under. It must be a slug; omit
	// it and the caller's project header is used instead.
	Project string `json:"project"`
	// Files is the batch to scan, at most 500 files and 8 MiB of content in total.
	Files []scan `json:"files" validate:"required"`
}

// scanView is one scan and what it found, without any of what it read.
type scanView struct {
	// ID addresses this scan and every finding on it.
	ID string `json:"id"`
	// Project is the sub-scope the scan was filed under.
	Project string `json:"project,omitempty"`
	// Files is how many files the scan read.
	Files int `json:"files"`
	// Findings is how many secrets fired across them.
	Findings int `json:"findings"`
	// Critical is how many findings carry the highest severity.
	Critical int `json:"critical"`
	// High is how many findings rank high.
	High int `json:"high"`
	// Medium is how many findings rank medium.
	Medium int `json:"medium"`
	// Low is how many findings rank low.
	Low int `json:"low"`
	// CreatedAt is when the scan ran, in Unix milliseconds.
	CreatedAt int64 `json:"createdAt"`
}

// findingView is one detected secret, redacted.
type findingView struct {
	// ID addresses this finding.
	ID string `json:"id"`
	// ScanID is the scan that produced it.
	ScanID string `json:"scanId"`
	// RuleID is the detection rule that fired.
	RuleID string `json:"ruleId"`
	// RuleName is that rule's human name.
	RuleName string `json:"ruleName"`
	// Severity ranks the finding: critical, high, medium or low.
	Severity string `json:"severity"`
	// Path is the file the secret was found in.
	Path string `json:"path"`
	// Line is where in that file.
	Line int `json:"line"`
	// Preview is the secret MASKED — first and last characters kept, the middle
	// starred — so a reviewer can recognise it without it being disclosed.
	Preview string `json:"preview"`
	// Fingerprint is the SHA-256 of the raw secret. It is what makes the same
	// secret recognisable across scans and after rotation without the secret ever
	// being written down.
	Fingerprint string `json:"fingerprint"`
	// CreatedAt is when the finding was recorded, in Unix milliseconds.
	CreatedAt int64 `json:"createdAt"`
}

// ruleset is the answer to the liveness read.
type ruleset struct {
	// Status is "ok" whenever the findings store opened.
	Status string `json:"status"`
	// Rules is how many detection rules the engine holds.
	Rules int `json:"rules"`
}

// ruleList is the detection catalog.
type ruleList struct {
	// Data is every rule a scan can fire, each with the id, name and severity a
	// finding cites.
	Data []detect.RuleView `json:"data"`
}

// scanList is the answer to a scan listing.
type scanList struct {
	// Data is the caller org's scans, newest first.
	Data []scanView `json:"data"`
}

// scanDetail is one scan together with everything it found.
type scanDetail struct {
	// Scan is the summary.
	Scan scanView `json:"scan"`
	// Findings is every finding on that scan, so the detail view is one round-trip.
	Findings []findingView `json:"findings"`
}

// findingList is the answer to a finding listing.
type findingList struct {
	// Data is the caller org's findings, newest first.
	Data []findingView `json:"data"`
}

// noIn is the input of an op that takes nothing: no body, no path parameter, no
// query.
type noIn struct{}

// scanPage bounds a scan listing.
type scanPage struct {
	// Limit caps the page.
	Limit int `json:"limit"`
}

// scanRef addresses one of the caller org's scans.
type scanRef struct {
	// ID is the scan the URL names.
	ID string `json:"id"`
}

// findingRef addresses one of the caller org's findings.
type findingRef struct {
	// ID is the finding the URL names.
	ID string `json:"id"`
}

// findingFilter narrows a finding listing.
type findingFilter struct {
	// ScanID narrows to a single scan.
	ScanID string `json:"scanId"`
	// MinSeverity drops everything below that rank: critical, high, medium or low.
	// A value outside that set is refused rather than quietly ignored, so a filter
	// typo cannot read as "no findings".
	MinSeverity string `json:"minSeverity"`
	// Limit caps the page.
	Limit int `json:"limit"`
}

func toScanView(s Scan) scanView {
	return scanView{
		ID: s.ID, Project: s.Project, Files: s.Files, Findings: s.Findings,
		Critical: s.Critical, High: s.High, Medium: s.Medium, Low: s.Low,
		CreatedAt: s.CreatedAt,
	}
}

func toFindingView(f StoredFinding) findingView {
	return findingView{
		ID: f.ID, ScanID: f.ScanID, RuleID: f.RuleID, RuleName: f.RuleName,
		Severity: f.Severity, Path: f.Path, Line: f.Line, Preview: f.Preview,
		Fingerprint: f.Fingerprint, CreatedAt: f.CreatedAt,
	}
}

// ---- handlers ----

// health reports that the scanning subsystem is serving and how many
// secret-detection rules the engine holds.
//
// It has no external dependency — the answer is ok whenever the findings store
// opened — so it measures this process rather than anything downstream. It reads
// no tenant: a prober that sends no principal is answered, not refused.
func (o ops) health(ctx context.Context, _ *noIn) (*ruleset, error) {
	return &ruleset{Status: "ok", Rules: detect.RuleCount()}, nil
}

// listRules is the secret-detection catalog the engine scans with.
//
// It returns every rule a scan can fire — the id, name and severity a finding
// cites — so a caller can render or triage results without hard-coding the
// catalog. It is the same for everyone and discloses nothing tenant-specific, so
// it carries no org scope.
func (o ops) listRules(ctx context.Context, _ *noIn) (*ruleList, error) {
	return &ruleList{Data: detect.Rules()}, nil
}

// submitScan runs the detection engine over a batch of files and answers 201 with
// the scan summary: how many files were read, how many findings fired, and the
// tally by severity.
//
// THE SUBMITTED CONTENT IS NEVER STORED. It is scanned in memory; what persists is
// the finding — its rule, its path and line, a MASKED preview (first and last
// characters kept, the middle starred) and the SHA-256 fingerprint of the raw
// secret. The fingerprint is what makes the same secret recognisable across scans
// and after rotation without the secret ever being written down.
//
// It requires a validated org, which scopes the stored scan and every finding on
// it; a caller with no org is refused. Bounded at 500 files and 8 MiB of total
// content per submission — split a larger tree across scans. One scan is one
// metered unit, and the scan is recorded in the audit log with its tally, never
// with its findings.
func (o ops) submitScan(ctx context.Context, in *submitReq) (*scanView, error) {
	s := o.s
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	// The billing gate, the meter and the audit record all need the REQUEST — the
	// payer, the request id and the caller's address are facts principal.OrgFrom
	// does not carry. Off the HTTP path there is no request and so no payer, which
	// is a refusal rather than an unbilled scan.
	c, onHTTP := cloud.Request(ctx)
	if !onHTTP {
		return nil, cloud.Denied(cloud.ErrNoLedger)
	}
	if len(in.Files) == 0 {
		return nil, zip.ErrBadRequest("files is required (at least one {path,content})")
	}
	if len(in.Files) > maxFiles {
		return nil, zip.ErrBadRequest(fmt.Sprintf("too many files (max %d)", maxFiles))
	}
	project := strings.TrimSpace(in.Project)
	if project == "" {
		project = projectScope(c)
	} else if !projectRE.MatchString(project) {
		return nil, zip.ErrBadRequest("project must match ^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$")
	}

	var total int
	for _, f := range in.Files {
		total += len(f.Content)
	}
	if total > maxBytes {
		return nil, zip.ErrBadRequest(fmt.Sprintf("scan too large (%d bytes, max %d)", total, maxBytes))
	}

	// Prepaid: the balance covers the scan BEFORE the engine runs, never after. A
	// gate downstream of the work is a bill for compute already spent.
	fee := cloud.ResourceFeeCents(feeEnvPrefix, "scan")
	scopeProject, projectValidated := principal.ValidatedProject(c)
	if err := s.State.bill.Authorize(ctx, principal.Payer(c), scopeProject, projectValidated, meterKind, fee); err != nil {
		return nil, cloud.Denied(err)
	}

	scanID := mint.ID("scan")
	now := time.Now().UTC().UnixMilli()

	sc := Scan{ID: scanID, Org: org, Project: project, Files: len(in.Files), CreatedAt: now}
	var stored []StoredFinding
	for _, f := range in.Files {
		for _, fnd := range detect.ScanContent(f.Path, f.Content) {
			stored = append(stored, StoredFinding{
				ID: mint.ID("fnd"), ScanID: scanID, Org: org,
				RuleID: fnd.RuleID, RuleName: fnd.RuleName, Severity: fnd.Severity,
				Path: fnd.Path, Line: fnd.Line, Preview: fnd.Preview,
				Fingerprint: fnd.Fingerprint, CreatedAt: now,
			})
			switch fnd.Severity {
			case detect.SeverityCritical:
				sc.Critical++
			case detect.SeverityHigh:
				sc.High++
			case detect.SeverityMedium:
				sc.Medium++
			case detect.SeverityLow:
				sc.Low++
			}
		}
	}
	sc.Findings = len(stored)

	if err := s.State.store.SaveScan(ctx, sc, stored); err != nil {
		return nil, zip.Errorf(500, "save scan: %v", err)
	}

	// One metered unit per scan (product=security), at the fee the Gate above
	// authorized — the same number, read once, so the charge can never exceed what
	// the balance was checked against. Nil/disabled meter → no-op.
	s.State.bill.Record(principal.Payer(c), meterKind, metering.Usage{
		Model:       meterKind,
		AmountCents: fee,
		Project:     principal.Project(c),
		RequestID:   c.RequestID(),
		ClientIP:    clientIP(c),
	})

	// Audit: the scan happened, by whom, with what tally. The redacted findings
	// (never the secrets) are the evidence; the tally is the AU-3 outcome.
	emitAudit(s, c, org, sc)

	out := toScanView(sc)
	return &out, nil
}

// listScans is the org's scan history, newest first, each as the same summary the
// submission answered — files read, findings fired, tally by severity.
//
// Strictly org-scoped: a caller only ever sees its own scans, and one with no
// validated org is refused.
func (o ops) listScans(ctx context.Context, in *scanPage) (*scanList, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := o.s.State.store.ListScans(ctx, org, in.Limit)
	if err != nil {
		return nil, zip.Errorf(500, "list scans: %v", err)
	}
	out := make([]scanView, 0, len(rows))
	for _, r := range rows {
		out = append(out, toScanView(r))
	}
	return &scanList{Data: out}, nil
}

// getScan returns one scan together with every finding on it, so the detail view
// is one round-trip rather than a list call per scan. The findings carry masked
// previews and fingerprints, never secrets.
//
// Scoped to the caller's org: a scan id belonging to another org is the same 404
// as an id that never existed, so a ruleset learns nothing about what exists
// elsewhere. No validated org is refused.
func (o ops) getScan(ctx context.Context, in *scanRef) (*scanDetail, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	sc, err := o.s.State.store.GetScan(ctx, org, in.ID)
	if err == errNotFound {
		return nil, zip.ErrNotFound("scan not found")
	}
	if err != nil {
		return nil, zip.Errorf(500, "get scan: %v", err)
	}
	fs, err := o.s.State.store.ListFindings(ctx, org, sc.ID, "", 0)
	if err != nil {
		return nil, zip.Errorf(500, "list findings: %v", err)
	}
	fv := make([]findingView, 0, len(fs))
	for _, f := range fs {
		fv = append(fv, toFindingView(f))
	}
	return &scanDetail{Scan: toScanView(sc), Findings: fv}, nil
}

// listFindings is the org's findings — rule, severity, path, line, masked preview
// and fingerprint — newest first, across scans or within one.
//
// A minSeverity outside critical|high|medium|low is refused rather than quietly
// ignored, so a filter typo cannot read as "no findings". Strictly org-scoped, and
// a caller with no validated org is refused.
func (o ops) listFindings(ctx context.Context, in *findingFilter) (*findingList, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	minSev := strings.ToLower(strings.TrimSpace(in.MinSeverity))
	if minSev != "" && detect.SeverityRank(minSev) == 0 {
		return nil, zip.ErrBadRequest("minSeverity must be one of critical|high|medium|low")
	}
	fs, err := o.s.State.store.ListFindings(ctx, org, strings.TrimSpace(in.ScanID), minSev, in.Limit)
	if err != nil {
		return nil, zip.Errorf(500, "list findings: %v", err)
	}
	out := make([]findingView, 0, len(fs))
	for _, f := range fs {
		out = append(out, toFindingView(f))
	}
	return &findingList{Data: out}, nil
}

// getFinding returns a single finding: which rule fired, where (path and line),
// the masked preview and the SHA-256 fingerprint of the secret — the raw secret is
// not stored and cannot be read back.
//
// Scoped to the caller's org, and a finding belonging to another org is the same
// 404 as one that never existed.
func (o ops) getFinding(ctx context.Context, in *findingRef) (*findingView, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	f, err := o.s.State.store.GetFinding(ctx, org, in.ID)
	if err == errNotFound {
		return nil, zip.ErrNotFound("finding not found")
	}
	if err != nil {
		return nil, zip.Errorf(500, "get finding: %v", err)
	}
	out := toFindingView(f)
	return &out, nil
}

// ---- helpers ----

// emitAudit appends a tamper-evident record for a scan. Nil recorder → no-op
// (an unconfigured deployment is never blocked). The record carries the tally,
// not the findings, and certainly not the secrets.
func emitAudit(s *cloud.Service[state], c *zip.Ctx, org string, sc Scan) {
	if s.State.audit == nil {
		return
	}
	rec := audit.Record{
		Actor:     audit.Actor{Org: org, Sub: c.User(), Email: c.UserEmail()},
		Action:    "security.scan",
		Resource:  audit.Resource{Type: "security.scan", ID: sc.ID},
		Auth:      audit.AuthContext{Method: "gateway", IsAdmin: c.IsAdmin()},
		Outcome:   audit.Outcome{Result: "ok", Status: 201},
		Method:    c.Method(),
		Path:      c.Path(),
		SourceIP:  clientIP(c),
		RequestID: c.RequestID(),
	}
	if _, err := s.State.audit.Append(c.Context(), rec); err != nil {
		s.Log.Warn("audit append failed", "err", err, "scan", sc.ID)
	}
}

// projectScope resolves the optional X-Project-Id sub-scope through principal.Project
// (the ONE project accessor). The default scope — an absent header OR the literal
// "default" (principal.IsDefaultProject) — keys with NO project segment, so today's
// org-level keys stay un-suffixed. An invalid non-default header is ignored (an
// OPTIONAL narrowing, not a 400).
func projectScope(c *zip.Ctx) string {
	p := principal.Project(c)
	if principal.IsDefaultProject(p) || len(p) > 128 || !projectRE.MatchString(p) {
		return ""
	}
	return p
}

// clientIP is the caller's address, by the ONE rule — cloud.ClientIP. It lands in
// a durable audit record, and the LEFT-most X-Forwarded-For entry (and X-Real-Ip)
// are values the client writes: an address chosen by the party being audited is
// not evidence.
func clientIP(c *zip.Ctx) string { return cloud.ClientIP(c) }
