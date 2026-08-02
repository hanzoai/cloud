// Package security is secret scanning for your code: submit sources, get findings,
// masked never raw.
//
// It serves /v1/security: the pure detect engine finds hardcoded secrets, and
// findings persist masked and fingerprinted — never the raw secret.
package security

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/hanzoai/cloud/apps/security/detect"
	"github.com/hanzoai/cloud/audit"
	"github.com/hanzoai/cloud/openapi"
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
func routes(app cloud.Router, s *cloud.Service[state]) {
	g := app.Group("/v1/security")
	g.Get("/health", cloud.Handle(s, health))
	g.Get("/rules", cloud.Handle(s, listRules))
	g.Post("/scans", cloud.Handle(s, submitScan))
	g.Get("/scans", cloud.Handle(s, listScans))
	g.Get("/findings", cloud.Handle(s, listFindings))
	g.Get("/scans/:id", cloud.Handle(s, getScan))
	g.Get("/findings/:id", cloud.Handle(s, getFinding))
}

// The surface's prose, declared beside the route table it describes.
//
// Every handler here is a cloud.Handle relay rather than a typed op, so zipdoc has
// no doc comment to lift and the document would otherwise publish seven
// operationIds and nothing else — seven SDK methods that cannot explain themselves
// and seven CLI commands with no help text. openapi.Describe is the seam for
// exactly that, and it stays drift-proof the same way Register does: a description
// whose route is not in the router simply never renders.
func init() {
	openapi.Describe("/v1/security/health", http.MethodGet,
		"Liveness, and how many detection rules are loaded",
		"Reports that the scanning subsystem is serving and how many secret-detection rules "+
			"the engine holds. It has no external dependency — the answer is ok whenever the "+
			"findings store opened — so it measures this process rather than anything "+
			"downstream. Reads no tenant: a prober that sends no principal is answered, not "+
			"refused.")

	openapi.Describe("/v1/security/rules", http.MethodGet,
		"The secret-detection catalog the engine scans with",
		"Returns every rule a scan can fire — the id, name and severity a finding cites — so "+
			"a caller can render or triage results without hard-coding the catalog. It is the "+
			"same for everyone and discloses nothing tenant-specific, so it carries no org "+
			"scope.")

	openapi.Describe("/v1/security/scans", http.MethodPost,
		"Scan submitted source for hardcoded secrets",
		"Runs the detection engine over a batch of {path, content} files and answers 201 with "+
			"the scan summary: how many files were read, how many findings fired, and the tally "+
			"by severity.\n\n"+
			"THE SUBMITTED CONTENT IS NEVER STORED. It is scanned in memory; what persists is "+
			"the finding — its rule, its path and line, a MASKED preview (first and last "+
			"characters kept, the middle starred) and the SHA-256 fingerprint of the raw secret. "+
			"The fingerprint is what makes the same secret recognisable across scans and after "+
			"rotation without the secret ever being written down.\n\n"+
			"Requires a validated org, which scopes the stored scan and every finding on it; a "+
			"caller with no org is refused. `project` in the body names the sub-scope and is "+
			"refused with 400 if it is not a valid slug; omit it and the caller's project header "+
			"is used instead, where an unusable value is simply ignored. Bounded at 500 files "+
			"and 8 MiB of total "+
			"content per submission — split a larger tree across scans. One scan is one metered "+
			"unit, and the scan is recorded in the audit log with its tally, never with its "+
			"findings.")

	openapi.Describe("/v1/security/scans", http.MethodGet,
		"The org's scan history",
		"Lists the caller org's scans, newest first, each as the same summary the submission "+
			"answered — files read, findings fired, tally by severity. `limit` caps the page. "+
			"Strictly org-scoped: a caller only ever sees its own scans, and one with no "+
			"validated org is refused.")

	openapi.Describe("/v1/security/scans/:id", http.MethodGet,
		"One scan and every finding on it",
		"Returns the scan summary together with all of its findings, so the detail view is one "+
			"round-trip rather than a list call per scan. The findings carry masked previews and "+
			"fingerprints, never secrets.\n\n"+
			"Scoped to the caller's org: a scan id belonging to another org is the same 404 as "+
			"an id that never existed, so a probe learns nothing about what exists elsewhere. No "+
			"validated org is refused.")

	openapi.Describe("/v1/security/findings", http.MethodGet,
		"The org's findings, across scans or within one",
		"Lists the caller org's findings — rule, severity, path, line, masked preview and "+
			"fingerprint — newest first. `scanId` narrows to a single scan, `minSeverity` "+
			"(critical | high | medium | low) drops everything below that rank, and `limit` caps "+
			"the page; a minSeverity outside that set is refused with 400 rather than quietly "+
			"ignored, so a filter typo cannot read as \"no findings\". Strictly org-scoped, and "+
			"a caller with no validated org is refused.")

	openapi.Describe("/v1/security/findings/:id", http.MethodGet,
		"One finding",
		"Returns a single finding: which rule fired, where (path and line), the masked preview "+
			"and the SHA-256 fingerprint of the secret — the raw secret is not stored and cannot "+
			"be read back. Scoped to the caller's org, and a finding belonging to another org is "+
			"the same 404 as one that never existed.")
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
	org, ok := principal.Org(c)
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
	s.State.bill.Meter(principal.Ledger(c), principal.Project(c), meterKind, 0, c.RequestID(), clientIP(c))

	// Audit: the scan happened, by whom, with what tally. The redacted findings
	// (never the secrets) are the evidence; the tally is the AU-3 outcome.
	emitAudit(s, c, org, sc)

	return c.JSON(201, toScanView(sc))
}

func listScans(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := principal.Org(c)
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
	org, ok := principal.Org(c)
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
	org, ok := principal.Org(c)
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
	org, ok := principal.Org(c)
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

// genID mints a prefixed random id (mirrors clients/git.genID).
func genID(prefix string) (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return prefix + "_" + hex.EncodeToString(b[:]), nil
}
