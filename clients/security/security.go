package security

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/audit"
	"github.com/hanzoai/cloud/clients/principal"
	"github.com/hanzoai/cloud/clients/security/detect"
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
func Mount(app *zip.App, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("security.Mount: nil zip.App")
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
func routes(app *zip.App, s *cloud.Service[state]) {
	app.Get("/v1/security/health", cloud.Handle(s, health))
	app.Get("/v1/security/rules", cloud.Handle(s, listRules))
	app.Post("/v1/security/scans", cloud.Handle(s, submitScan))
	app.Get("/v1/security/scans", cloud.Handle(s, listScans))
	app.Get("/v1/security/findings", cloud.Handle(s, listFindings))
	app.Get("/v1/security/scans/:id", cloud.Handle(s, getScan))
	app.Get("/v1/security/findings/:id", cloud.Handle(s, getFinding))
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

func init() {
	// cloud.HealthOwner: security serves its own /v1/security/health (Mount), which
	// reports the live detection-rule count — the generic always-ok route would
	// shadow it and drop that field, so Serve skips the generic one.
	cloud.RegisterWithShutdown("security", 136, cloud.Typed(Mount),
		func(ctx context.Context) error { return Shutdown() },
		cloud.HealthOwner,
	)
}

// ---- request/response shapes ----

type fileInput struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

type submitReq struct {
	Project string      `json:"project"`
	Files   []fileInput `json:"files"`
}

type scanView struct {
	ID        string `json:"id"`
	Project   string `json:"project,omitempty"`
	Files     int    `json:"files"`
	Findings  int    `json:"findings"`
	Critical  int    `json:"critical"`
	High      int    `json:"high"`
	Medium    int    `json:"medium"`
	Low       int    `json:"low"`
	CreatedAt int64  `json:"createdAt"`
}

type findingView struct {
	ID          string `json:"id"`
	ScanID      string `json:"scanId"`
	RuleID      string `json:"ruleId"`
	RuleName    string `json:"ruleName"`
	Severity    string `json:"severity"`
	Path        string `json:"path"`
	Line        int    `json:"line"`
	Preview     string `json:"preview"`
	Fingerprint string `json:"fingerprint"`
	CreatedAt   int64  `json:"createdAt"`
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

// health is fail-open: the subsystem has no external dependency, so it reports
// ok whenever the store opened. Registered before the generic liveness route so
// the real probe is not shadowed (mirrors s3svc/kms).
func health(s *cloud.Service[state], c *zip.Ctx) error {
	return c.JSON(200, map[string]any{"status": "ok", "rules": detect.RuleCount()})
}

// listRules serves the detection catalog. No tenant scope — the catalog is the
// same for everyone and discloses nothing tenant-specific.
func listRules(s *cloud.Service[state], c *zip.Ctx) error {
	return c.JSON(200, map[string]any{"data": detect.Rules()})
}

// submitScan runs the engine over the submitted files, persists the scan +
// redacted findings, meters one unit, emits an audit event, and returns the
// scan summary. The raw content is scanned in memory and never stored.
func submitScan(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := principal.Tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	var body submitReq
	if err := c.Bind(&body); err != nil {
		return err
	}
	if len(body.Files) == 0 {
		return zip.ErrBadRequest("files is required (at least one {path,content})")
	}
	if len(body.Files) > maxFiles {
		return zip.ErrBadRequest(fmt.Sprintf("too many files (max %d)", maxFiles))
	}
	project := strings.TrimSpace(body.Project)
	if project == "" {
		project = projectScope(c)
	} else if !projectRE.MatchString(project) {
		return zip.ErrBadRequest("project must match ^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$")
	}

	var total int
	for _, f := range body.Files {
		total += len(f.Content)
	}
	if total > maxBytes {
		return zip.ErrBadRequest(fmt.Sprintf("scan too large (%d bytes, max %d)", total, maxBytes))
	}

	scanID, err := genID("scan")
	if err != nil {
		return zip.Errorf(500, "rng: %v", err)
	}
	now := time.Now().UTC().UnixMilli()

	sc := Scan{ID: scanID, Org: org, Project: project, Files: len(body.Files), CreatedAt: now}
	var stored []StoredFinding
	for _, f := range body.Files {
		for _, fnd := range detect.ScanContent(f.Path, f.Content) {
			id, err := genID("fnd")
			if err != nil {
				return zip.Errorf(500, "rng: %v", err)
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

	if err := s.State.store.SaveScan(c.Context(), sc, stored); err != nil {
		return zip.Errorf(500, "save scan: %v", err)
	}

	// One metered unit per scan (product=security). Nil/disabled meter → no-op.
	s.State.bill.Meter(org, principal.Project(c), meterKind, 0, c.RequestID(), clientIP(c))

	// Audit: the scan happened, by whom, with what tally. The redacted findings
	// (never the secrets) are the evidence; the tally is the AU-3 outcome.
	emitAudit(s, c, org, sc)

	return c.JSON(201, toScanView(sc))
}

func listScans(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := principal.Tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	limit, _ := strconv.Atoi(c.Query("limit"))
	rows, err := s.State.store.ListScans(c.Context(), org, limit)
	if err != nil {
		return zip.Errorf(500, "list scans: %v", err)
	}
	out := make([]scanView, 0, len(rows))
	for _, r := range rows {
		out = append(out, toScanView(r))
	}
	return c.JSON(200, map[string]any{"data": out})
}

func getScan(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := principal.Tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	sc, err := s.State.store.GetScan(c.Context(), org, c.Param("id"))
	if err == errNotFound {
		return zip.ErrNotFound("scan not found")
	}
	if err != nil {
		return zip.Errorf(500, "get scan: %v", err)
	}
	// Include this scan's findings so the detail view is one round-trip.
	fs, err := s.State.store.ListFindings(c.Context(), org, sc.ID, "", 0)
	if err != nil {
		return zip.Errorf(500, "list findings: %v", err)
	}
	fv := make([]findingView, 0, len(fs))
	for _, f := range fs {
		fv = append(fv, toFindingView(f))
	}
	resp := toScanView(sc)
	return c.JSON(200, map[string]any{"scan": resp, "findings": fv})
}

func listFindings(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := principal.Tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	limit, _ := strconv.Atoi(c.Query("limit"))
	minSev := strings.ToLower(strings.TrimSpace(c.Query("minSeverity")))
	if minSev != "" && detect.SeverityRank(minSev) == 0 {
		return zip.ErrBadRequest("minSeverity must be one of critical|high|medium|low")
	}
	fs, err := s.State.store.ListFindings(c.Context(), org, strings.TrimSpace(c.Query("scanId")), minSev, limit)
	if err != nil {
		return zip.Errorf(500, "list findings: %v", err)
	}
	out := make([]findingView, 0, len(fs))
	for _, f := range fs {
		out = append(out, toFindingView(f))
	}
	return c.JSON(200, map[string]any{"data": out})
}

func getFinding(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := principal.Tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	f, err := s.State.store.GetFinding(c.Context(), org, c.Param("id"))
	if err == errNotFound {
		return zip.ErrNotFound("finding not found")
	}
	if err != nil {
		return zip.Errorf(500, "get finding: %v", err)
	}
	return c.JSON(200, toFindingView(f))
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

// projectScope resolves the optional X-Project-Id sub-scope; empty is valid,
// an invalid header is ignored (an OPTIONAL narrowing, not a 400).
func projectScope(c *zip.Ctx) string {
	p := strings.TrimSpace(c.Header("X-Project-Id"))
	if p == "" || len(p) > 128 || !projectRE.MatchString(p) {
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
