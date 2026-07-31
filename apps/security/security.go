package security

//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/audit"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/hanzoai/cloud/apps/security/detect"
	"github.com/zap-proto/zip"
)

// meterKind is the commerce meter key for a scan (product=security). One scan =
// one metered unit; a metering.Client publish bills it when configured.
const meterKind = "security.scan"

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
	bill  *cloud.ResourceMeter
}

// mounted is the process-wide handle so Shutdown can flush the store (mirrors
// clients/git). Set by Mount, read by Shutdown.
var mounted *cloud.Service[state]

// Mount wires /v1/security/* onto app and opens the per-deployment findings
// store under {DataDir}/security.db. It follows the clients/git contract:
// validate deps, open the store, register routes, return. The store lifecycle
// and package-global handle make this a direct construction (cloud.NewBase),
// not cloud.Mount.
func Mount(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("security.Mount: nil app")
	}
	if deps.Logger == nil {
		return fmt.Errorf("security.Mount: nil deps.Logger")
	}
	if deps.DataDir == "" {
		return fmt.Errorf("security.Mount: empty DataDir")
	}
	if err := os.MkdirAll(deps.DataDir, 0o755); err != nil {
		return fmt.Errorf("security.Mount: data dir: %w", err)
	}
	store, err := openStore(filepath.Join(deps.DataDir, "security.db"))
	if err != nil {
		return fmt.Errorf("security.Mount: open store: %w", err)
	}
	s := &cloud.Service[state]{
		Base: cloud.NewBase(deps, "security"),
		State: state{
			store: store,
			audit: deps.Audit,
			bill:  cloud.NewResourceMeter(deps, meterKind),
		},
	}
	mounted = s

	routes(app, s)

	s.Log.Info("security mounted", "brand", deps.Brand, "rules", detect.RuleCount(),
		"db", filepath.Join(deps.DataDir, "security.db"))
	return nil
}

// routes registers the security surface. Static routes register before the :id
// params so a scan id can never shadow /rules or /health (Fiber first-match).
// Every route is a zip TYPED op, so the REST route, the OpenAPI document, the MCP
// tool and the CLI command all come from the one declaration; the bridge goes on
// FIRST because a typed op is handed only a context, so the request (and the
// validated principal it proves) is parked there.
func routes(app cloud.Router, s *cloud.Service[state]) {
	o := ops{s: s}
	z := cloud.ZipApp(app)
	app.Group("/v1/security").Use(cloud.Bridge())

	zip.Get(z, "/v1/security/health", o.health, zip.WithOperationID("securityHealth"))
	zip.Get(z, "/v1/security/rules", o.listRules, zip.WithOperationID("listSecurityRules"))
	zip.Post(z, "/v1/security/scans", o.submitScan, zip.WithOperationID("submitScan"), zip.WithStatus(201))
	zip.Get(z, "/v1/security/scans", o.listScans, zip.WithOperationID("listScans"))
	zip.Get(z, "/v1/security/findings", o.listFindings, zip.WithOperationID("listFindings"))
	zip.Get(z, "/v1/security/scans/:id", o.getScan, zip.WithOperationID("getScan"))
	zip.Get(z, "/v1/security/findings/:id", o.getFinding, zip.WithOperationID("getFinding"))
}

// ops binds the service to security's typed handlers. A TypedHandler is
// func(context.Context, *In) (*Out, error) — no parameter for the service — so it
// arrives as a RECEIVER and every op is a method value, the only bound form
// cmd/zipdoc can lift prose from.
type ops struct{ s *cloud.Service[state] }

// tenant resolves the org — the tenant-isolation KEY, taken verbatim from the
// validated IAM owner claim. An op that cannot name its tenant refuses with 403
// rather than reading across orgs.
func tenant(ctx context.Context) (string, error) {
	org, ok := principal.OrgFrom(ctx)
	if !ok {
		return "", zip.ErrForbidden("X-Org-Id required")
	}
	return org, nil
}

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

// SourceFile is one file submitted for scanning. Content is scanned in memory
// and never stored.
type SourceFile struct {
	// Path is the file's path, reported verbatim on any finding it produces.
	Path string `json:"path"`
	// Content is the file's raw text. It is scanned in memory and never persisted.
	Content string `json:"content"`
}

// ScanRequest is one scan submission: the files to scan and the optional project
// sub-scope they belong to.
type ScanRequest struct {
	// Project is the optional sub-scope within the org; defaults to the caller's
	// X-Project-Id, and must match ^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$.
	Project string `json:"project"`
	// Files are the files to scan; at least one, at most 500, 8 MiB of content
	// in total.
	Files []SourceFile `json:"files"`
}

// Scan summarises one completed scan: how much was scanned and the severity tally.
type ScanSummary struct {
	// ID is the scan id, the key for its findings.
	ID string `json:"id"`
	// Project is the sub-scope the scan ran under, absent at org level.
	Project string `json:"project,omitempty"`
	// Files counts the files submitted.
	Files int `json:"files"`
	// Findings counts every finding across all severities.
	Findings int `json:"findings"`
	// Critical counts the critical-severity findings.
	Critical int `json:"critical"`
	// High counts the high-severity findings.
	High int `json:"high"`
	// Medium counts the medium-severity findings.
	Medium int `json:"medium"`
	// Low counts the low-severity findings.
	Low int `json:"low"`
	// CreatedAt is the scan time, unix milliseconds.
	CreatedAt int64 `json:"createdAt"`
}

// Finding is one detected secret, redacted: it pins where and which rule fired,
// and carries a masked preview plus the raw secret's fingerprint, never the secret.
type Finding struct {
	// ID is the finding id.
	ID string `json:"id"`
	// ScanID is the scan this finding came from.
	ScanID string `json:"scanId"`
	// RuleID is the detection rule that fired.
	RuleID string `json:"ruleId"`
	// RuleName is that rule's human name.
	RuleName string `json:"ruleName"`
	// Severity is critical, high, medium or low.
	Severity string `json:"severity"`
	// Path is the file the secret was found in.
	Path string `json:"path"`
	// Line is the 1-based line the secret was found on.
	Line int `json:"line"`
	// Preview is the masked excerpt; it never carries the secret.
	Preview string `json:"preview"`
	// Fingerprint is the SHA-256 of the raw secret, for dedupe across scans.
	Fingerprint string `json:"fingerprint"`
	// CreatedAt is the finding's scan time, unix milliseconds.
	CreatedAt int64 `json:"createdAt"`
}

func toScanView(s Scan) ScanSummary {
	return ScanSummary{
		ID: s.ID, Project: s.Project, Files: s.Files, Findings: s.Findings,
		Critical: s.Critical, High: s.High, Medium: s.Medium, Low: s.Low,
		CreatedAt: s.CreatedAt,
	}
}

func toFindingView(f StoredFinding) Finding {
	return Finding{
		ID: f.ID, ScanID: f.ScanID, RuleID: f.RuleID, RuleName: f.RuleName,
		Severity: f.Severity, Path: f.Path, Line: f.Line, Preview: f.Preview,
		Fingerprint: f.Fingerprint, CreatedAt: f.CreatedAt,
	}
}

// ---- handlers ----

// Health is the subsystem's liveness answer plus the size of its rule catalog.
type Health struct {
	// Status is ok whenever the findings store opened.
	Status string `json:"status"`
	// Rules is the number of detection rules loaded.
	Rules int `json:"rules"`
}

// health reports whether the scanner is up and how many rules it loaded.
//
// It is fail-open: the subsystem has no external dependency, so it reports ok
// whenever the store opened.
// Registered before the generic liveness route so the real probe is not shadowed.
//
// Response: {"status": "ok", "rules": 42}
func (o ops) health(ctx context.Context, _ *struct{}) (*Health, error) {
	return &Health{Status: "ok", Rules: detect.RuleCount()}, nil
}

// RuleList is the detection catalog.
type RuleList struct {
	// Data is the rule catalog, worst severity first.
	Data []detect.RuleView `json:"data"`
}

// listRules returns the secret-detection rule catalog.
//
// There is no tenant scope: the catalog is the same for every caller and
// discloses nothing tenant-specific.
//
// Response: {"data": [{"id": "aws-access-key", "name": "AWS Access Key ID", "severity": "critical", "description": "AWS access key identifier"}]}
func (o ops) listRules(ctx context.Context, _ *struct{}) (*RuleList, error) {
	return &RuleList{Data: detect.Rules()}, nil
}

// submitScan scans the submitted files for secrets and records the redacted findings.
//
// The raw content is scanned in memory and never stored; only the masked preview
// and the secret's fingerprint are persisted.
// One scan is one metered unit, and the scan's tally is written to the audit log.
//
// Example: {"project": "api", "files": [{"path": "config.py", "content": "aws_key = \"AKIAIOSFODNN7EXAMPLE\""}]}
// Response: {"id": "scan_9f2c", "project": "api", "files": 1, "findings": 1, "critical": 1, "high": 0, "medium": 0, "low": 0, "createdAt": 1780000000000}
func (o ops) submitScan(ctx context.Context, in *ScanRequest) (*ScanSummary, error) {
	s := o.s
	org, err := tenant(ctx)
	if err != nil {
		return nil, err
	}
	// The meter, the audit record and the project sub-scope are all facts of the
	// REQUEST, not of the input — a tenant key read from an input is a key the
	// caller asserted for itself.
	c, ok := cloud.Request(ctx)
	if !ok {
		return nil, zip.ErrForbidden("X-Org-Id required")
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

	scanID, err := genID("scan")
	if err != nil {
		return nil, zip.Errorf(500, "rng: %v", err)
	}
	now := time.Now().UTC().UnixMilli()

	sc := Scan{ID: scanID, Org: org, Project: project, Files: len(in.Files), CreatedAt: now}
	var stored []StoredFinding
	for _, f := range in.Files {
		for _, fnd := range detect.ScanContent(f.Path, f.Content) {
			id, err := genID("fnd")
			if err != nil {
				return nil, zip.Errorf(500, "rng: %v", err)
			}
			stored = append(stored, StoredFinding{
				ID: id, ScanID: scanID, Org: org,
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

	// One metered unit per scan (product=security). Nil/disabled meter → no-op.
	s.State.bill.Meter(principal.Ledger(c), principal.Project(c), meterKind, 0, c.RequestID(), clientIP(c))

	// Audit: the scan happened, by whom, with what tally. The redacted findings
	// (never the secrets) are the evidence; the tally is the AU-3 outcome.
	emitAudit(s, c, org, sc)

	out := toScanView(sc)
	return &out, nil
}

// ScanQuery bounds a scan listing.
type ScanQuery struct {
	// Limit caps the rows returned; 0 means the store's default.
	Limit int `json:"limit"`
}

// ScanList is a page of the caller org's scans.
type ScanList struct {
	// Data is the scan summaries, most recent first.
	Data []ScanSummary `json:"data"`
}

// listScans returns the caller org's scans, most recent first.
//
// Response: {"data": [{"id": "scan_9f2c", "files": 1, "findings": 1, "critical": 1, "high": 0, "medium": 0, "low": 0, "createdAt": 1780000000000}]}
func (o ops) listScans(ctx context.Context, in *ScanQuery) (*ScanList, error) {
	org, err := tenant(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := o.s.State.store.ListScans(ctx, org, in.Limit)
	if err != nil {
		return nil, zip.Errorf(500, "list scans: %v", err)
	}
	out := make([]ScanSummary, 0, len(rows))
	for _, r := range rows {
		out = append(out, toScanView(r))
	}
	return &ScanList{Data: out}, nil
}

// ScanRef addresses one scan by id.
type ScanRef struct {
	// ID is the scan id returned by submit.
	ID string `json:"id"`
}

// ScanDetail is one scan with the findings it produced.
type ScanDetail struct {
	// Scan is the scan's summary and severity tally.
	Scan ScanSummary `json:"scan"`
	// Findings are every finding this scan produced, redacted.
	Findings []Finding `json:"findings"`
}

// getScan returns one of the caller org's scans with its findings.
//
// The findings ride along so the detail view is one round-trip; a scan belonging
// to another org answers 404, never a peek.
//
// Example: {"id": "scan_9f2c"}
// Response: {"scan": {"id": "scan_9f2c", "files": 1, "findings": 1, "critical": 1, "high": 0, "medium": 0, "low": 0, "createdAt": 1780000000000}, "findings": []}
func (o ops) getScan(ctx context.Context, in *ScanRef) (*ScanDetail, error) {
	org, err := tenant(ctx)
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
	// Include this scan's findings so the detail view is one round-trip.
	fs, err := o.s.State.store.ListFindings(ctx, org, sc.ID, "", 0)
	if err != nil {
		return nil, zip.Errorf(500, "list findings: %v", err)
	}
	fv := make([]Finding, 0, len(fs))
	for _, f := range fs {
		fv = append(fv, toFindingView(f))
	}
	return &ScanDetail{Scan: toScanView(sc), Findings: fv}, nil
}

// FindingQuery narrows a finding listing.
type FindingQuery struct {
	// ScanID restricts the listing to one scan.
	ScanID string `json:"scanId"`
	// MinSeverity drops anything below it: critical, high, medium or low.
	MinSeverity string `json:"minSeverity"`
	// Limit caps the rows returned; 0 means the store's default.
	Limit int `json:"limit"`
}

// FindingList is a page of the caller org's findings.
type FindingList struct {
	// Data is the findings, worst severity first.
	Data []Finding `json:"data"`
}

// listFindings returns the caller org's findings, optionally narrowed by scan and severity.
//
// Response: {"data": [{"id": "fnd_1", "scanId": "scan_9f2c", "ruleId": "aws-access-key", "ruleName": "AWS Access Key ID", "severity": "critical", "path": "config.py", "line": 1, "preview": "aws_key = \"AKIA****\"", "fingerprint": "9c1185a5c5e9fc54", "createdAt": 1780000000000}]}
func (o ops) listFindings(ctx context.Context, in *FindingQuery) (*FindingList, error) {
	org, err := tenant(ctx)
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
	out := make([]Finding, 0, len(fs))
	for _, f := range fs {
		out = append(out, toFindingView(f))
	}
	return &FindingList{Data: out}, nil
}

// FindingRef addresses one finding by id.
type FindingRef struct {
	// ID is the finding id.
	ID string `json:"id"`
}

// getFinding returns one of the caller org's findings, redacted.
//
// A finding belonging to another org answers 404, never a peek.
//
// Example: {"id": "fnd_1"}
// Response: {"id": "fnd_1", "scanId": "scan_9f2c", "ruleId": "aws-access-key", "ruleName": "AWS Access Key ID", "severity": "critical", "path": "config.py", "line": 1, "preview": "aws_key = \"AKIA****\"", "fingerprint": "9c1185a5c5e9fc54", "createdAt": 1780000000000}
func (o ops) getFinding(ctx context.Context, in *FindingRef) (*Finding, error) {
	org, err := tenant(ctx)
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

// clientIP is the best-effort source IP for audit/metering. Prefers the
// gateway-forwarded header, falls back to the socket peer.
func clientIP(c *zip.Ctx) string {
	if xff := c.Header("X-Forwarded-For"); xff != "" {
		if i := strings.IndexByte(xff, ','); i >= 0 {
			return strings.TrimSpace(xff[:i])
		}
		return strings.TrimSpace(xff)
	}
	return c.Header("X-Real-Ip")
}

// genID mints a prefixed random id (mirrors clients/git.genID).
func genID(prefix string) (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return prefix + "_" + hex.EncodeToString(b[:]), nil
}
