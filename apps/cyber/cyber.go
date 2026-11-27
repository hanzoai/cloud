// Copyright © 2026 Hanzo AI. MIT License.

// Package cyber mounts Hanzo Cyber's native cloud control plane at /v1/cyber/*.
//
// It replaces the standalone Python control backend with a multi-tenant,
// Hanzo IAM-authenticated Go subsystem: engagements, task reasoning graphs
// (GOAL -> INTENT -> FINDING -> HINT), captured HTTP traffic, deterministic
// verbatim-proof finding verification, Kai risk-scored action approvals,
// scope/intercept rules, MITRE ATT&CK mapping, Course of Action remediation,
// and enclave hardening posture.
//
// Access is gated behind Hanzo IAM JWT validation (principal.Acting) AND the
// beta.cyber alpha flag (auto-enabled for Hanzo internal orgs and SuperAdmins,
// explicitly granted per user/org via apps/flags for external evaluators).
package cyber

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/client/flag"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/audit"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/zap-proto/zip"
)

// BetaFlagKey is the principal feature flag controlling Hanzo Cyber alpha access.
const BetaFlagKey = "beta.cyber"

// hanzoInternalOrgs always have Hanzo Cyber alpha enabled without requiring a
// separate flag grant (mirrors @hanzo/app lib/betas.ts HANZO_ADMIN_ORGS).
var hanzoInternalOrgs = map[string]bool{
	"admin":      true,
	"hanzo":      true,
	"hanzoai":    true,
	"hanzo-inc":  true,
	"hanzo-apps": true,
	"platform":   true,
}

type alphaChecker func(ctx context.Context, org string) (bool, error)

type state struct {
	store      *Store
	audit      *audit.Recorder
	alphaCheck alphaChecker
}

var mounted *cloud.Service[state]

// Use wires /v1/cyber/* onto app and opens the deployment's cyber SQL store.
func Use(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return errors.New("cyber: nil app")
	}
	if cloud.DataDir() == "" {
		return fmt.Errorf("cyber.Use: empty DataDir")
	}
	store, err := openStore(cloud.DataDir())
	if err != nil {
		return fmt.Errorf("cyber.Use: open store: %w", err)
	}
	s := &cloud.Service[state]{
		Base: cloud.NewBase(deps, "cyber"),
		State: state{
			store:      store,
			audit:      deps.Audit,
			alphaCheck: defaultAlphaCheck,
		},
	}
	mounted = s
	routes(app, s)
	s.Log.Info("cyber mounted", "brand", cloud.Brand(), "dir", cloud.DataDir())
	return nil
}

// Shutdown closes the cyber store cleanly. Idempotent; safe if Use never ran.
func Shutdown() error {
	if mounted == nil || mounted.State.store == nil {
		return nil
	}
	err := mounted.State.store.Close()
	mounted = nil
	return err
}

// defaultAlphaCheck enforces Hanzo Cyber alpha access:
// SuperAdmins and Hanzo internal orgs always pass; external orgs must hold the
// beta.cyber principal switch via the flags plane.
func defaultAlphaCheck(ctx context.Context, org string) (bool, error) {
	if principal.SuperFrom(ctx) || hanzoInternalOrgs[strings.ToLower(strings.TrimSpace(org))] {
		return true, nil
	}
	res, err := flag.FlagsOn(ctx, &client.FlagIn{Key: BetaFlagKey})
	if err != nil {
		return false, err
	}
	return res != nil && res.On, nil
}

// routes registers every typed Hanzo Cyber endpoint under /v1/cyber.
//
//go:generate go run github.com/zap-proto/zip/cmd/zipdoc
func routes(app cloud.Router, s *cloud.Service[state]) {
	g := app.Group("/v1/cyber")
	g.Use(cloud.DenyEnvelope())
	o := ops{s: s}

	g.Get("/health", o.health)
	g.Get("/dashboard", o.dashboard)

	g.Get("/engagements", o.listEngagements)
	g.Post("/engagements", o.createEngagement, zip.WithStatus(http.StatusCreated))
	g.Post("/engagements/seed-demo", o.seedDemo, zip.WithStatus(http.StatusCreated))

	g.Get("/tasks", o.listTasks)
	g.Post("/tasks", o.createTask, zip.WithStatus(http.StatusCreated))
	g.Get("/tasks/:id/graph", o.getTaskGraph)
	g.Post("/tasks/:id/nodes", o.addGraphNode, zip.WithStatus(http.StatusCreated))

	g.Get("/findings", o.listFindings)
	g.Post("/findings", o.createFinding, zip.WithStatus(http.StatusCreated))
	g.Patch("/findings/:id", o.updateFinding)
	g.Post("/findings/:id/verify", o.verifyFinding)

	g.Get("/traffic", o.listTraffic)
	g.Post("/traffic", o.recordTraffic, zip.WithStatus(http.StatusCreated))

	g.Get("/approvals", o.listApprovals)
	g.Post("/approvals", o.submitApproval, zip.WithStatus(http.StatusCreated))
	g.Post("/approvals/:id/decide", o.decideApproval)

	g.Get("/intercept/rules", o.listRules)
	g.Post("/intercept/rules", o.createRule, zip.WithStatus(http.StatusCreated))
	g.Delete("/intercept/rules/:id", o.deleteRule)

	g.Get("/attack", o.attackMatrix)
	g.Get("/coa", o.courseOfAction)
	g.Get("/hardening", o.hardeningPosture)
}

type ops struct{ s *cloud.Service[state] }

func (o ops) authorize(ctx context.Context) (string, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return "", err
	}
	check := o.s.State.alphaCheck
	if check == nil {
		check = defaultAlphaCheck
	}
	ok, flagErr := check(ctx, org)
	if flagErr != nil || !ok {
		return "", zip.ErrForbidden("Hanzo Cyber alpha requires beta.cyber access")
	}
	return org, nil
}

func (o ops) actor(ctx context.Context, org string) string {
	if sub, ok := principal.SubjectFrom(ctx); ok && strings.TrimSpace(sub) != "" {
		return sub
	}
	return org
}

func (o ops) emitAudit(ctx context.Context, org, action, resType, resID string, status int) {
	if o.s.State.audit == nil {
		return
	}
	sub, _ := principal.SubjectFrom(ctx)
	_, _ = o.s.State.audit.Append(ctx, audit.Record{
		Actor:    audit.Actor{Org: org, Sub: sub},
		Action:   action,
		Resource: audit.Resource{Type: resType, ID: resID},
		Auth:     audit.AuthContext{Method: "gateway", IsAdmin: principal.SuperFrom(ctx)},
		Outcome:  audit.Outcome{Result: "ok", Status: status},
	})
}

func newID(prefix string) string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return prefix + "_" + hex.EncodeToString(b[:])
}

func nowMS() int64 { return time.Now().UnixMilli() }

// ---- Wire Types ----

type noIn struct{}

// healthView reports liveness for the Hanzo Cyber subsystem.
type healthView struct {
	// Status is "ok" when the cyber control store is open and accepting requests.
	Status string `json:"status"`
	// Flag is the principal feature flag gating alpha access.
	Flag string `json:"flag"`
	// Backend names the native cloud runtime serving /v1/cyber.
	Backend string `json:"backend"`
}

// engagementFilter scopes queries to a single engagement when provided.
type engagementFilter struct {
	// EngagementID narrows results to one engagement when non-empty.
	EngagementID string `json:"engagementId"`
}

// dashboardView summarizes an org's cyber posture and operational counts.
type dashboardView struct {
	// Org is the authenticated caller organization.
	Org string `json:"org"`
	// Stage marks the release stage of Hanzo Cyber.
	Stage string `json:"stage"`
	// Engagements is the total number of authorized engagements in the org.
	Engagements int `json:"engagements"`
	// Tasks is the number of cyber tasks across the selected scope.
	Tasks int `json:"tasks"`
	// FindingsTotal is the total number of recorded security findings.
	FindingsTotal int `json:"findingsTotal"`
	// FindingsVerified is the number of findings backed by verbatim captured proof.
	FindingsVerified int `json:"findingsVerified"`
	// Critical is the count of critical-severity findings.
	Critical int `json:"critical"`
	// High is the count of high-severity findings.
	High int `json:"high"`
	// Medium is the count of medium-severity findings.
	Medium int `json:"medium"`
	// Low is the count of low-severity findings.
	Low int `json:"low"`
	// PendingApprovals is the number of Kai-gated actions waiting on human approval.
	PendingApprovals int `json:"pendingApprovals"`
	// TrafficCount is the number of captured HTTP exchanges available for proof binding.
	TrafficCount int `json:"trafficCount"`
	// RulesCount is the number of active scope and intercept governance rules.
	RulesCount int `json:"rulesCount"`
	// VerificationRate is the percentage (0-100) of findings verified verbatim against traffic.
	VerificationRate int `json:"verificationRate"`
}

// engagementView is one authorized cyber engagement workspace.
type engagementView struct {
	// ID uniquely identifies the engagement.
	ID string `json:"id"`
	// Name is the human-readable engagement title.
	Name string `json:"name"`
	// Target is the primary host, CIDR, or enclave under assessment.
	Target string `json:"target"`
	// Scope lists authorized hostnames, CIDRs, or environments.
	Scope []string `json:"scope"`
	// Mode describes the execution posture (enclave, cloud, hybrid, airgap).
	Mode string `json:"mode"`
	// Status is the engagement lifecycle state (active, paused, completed).
	Status string `json:"status"`
	// Synthetic marks lab/demonstration engagements using synthetic targets.
	Synthetic bool `json:"synthetic"`
	// CreatedAt is the creation timestamp in Unix milliseconds.
	CreatedAt int64 `json:"createdAt"`
	// UpdatedAt is the last update timestamp in Unix milliseconds.
	UpdatedAt int64 `json:"updatedAt"`
}

// engagementList returns an org's engagements.
type engagementList struct {
	// Data holds the engagements ordered newest first.
	Data []engagementView `json:"data"`
}

// createEngagementReq defines a new authorized cyber engagement.
type createEngagementReq struct {
	// Name is the engagement display name.
	Name string `json:"name" validate:"required"`
	// Target is the primary system or enclave host under test.
	Target string `json:"target" validate:"required"`
	// Scope enumerates authorized targets within Rules of Engagement.
	Scope []string `json:"scope"`
	// Mode sets execution mode: enclave, cloud, hybrid, or airgap.
	Mode string `json:"mode"`
	// Synthetic marks lab/demo engagements explicitly.
	Synthetic bool `json:"synthetic"`
}

// seedDemoRes returns the seeded synthetic lab engagement and record counts.
type seedDemoRes struct {
	// Engagement is the seeded synthetic MDTF tactical enclave lab engagement.
	Engagement engagementView `json:"engagement"`
	// Tasks is the number of tasks seeded.
	Tasks int `json:"tasks"`
	// Findings is the number of findings seeded.
	Findings int `json:"findings"`
	// Traffic is the number of captured HTTP traffic records seeded.
	Traffic int `json:"traffic"`
	// Approvals is the number of Kai approval records seeded.
	Approvals int `json:"approvals"`
	// Rules is the number of scope/intercept rules seeded.
	Rules int `json:"rules"`
}

// taskView is an autonomous or operator-guided cyber objective.
type taskView struct {
	// ID uniquely identifies the task.
	ID string `json:"id"`
	// EngagementID links the task to its parent engagement.
	EngagementID string `json:"engagementId"`
	// Title is the concise task name.
	Title string `json:"title"`
	// Goal states the verification objective.
	Goal string `json:"goal"`
	// Status is active, paused, completed, or blocked.
	Status string `json:"status"`
	// Phase is recon, enumerate, verify, govern, or report.
	Phase string `json:"phase"`
	// CreatedAt is the creation timestamp in Unix milliseconds.
	CreatedAt int64 `json:"createdAt"`
	// UpdatedAt is the last update timestamp in Unix milliseconds.
	UpdatedAt int64 `json:"updatedAt"`
}

// taskList returns tasks scoped to an org or engagement.
type taskList struct {
	// Data holds the tasks ordered newest first.
	Data []taskView `json:"data"`
}

// createTaskReq creates a task and its root GOAL graph node.
type createTaskReq struct {
	// EngagementID identifies the parent engagement.
	EngagementID string `json:"engagementId" validate:"required"`
	// Title names the cyber task.
	Title string `json:"title" validate:"required"`
	// Goal states the desired verification outcome.
	Goal string `json:"goal"`
	// Phase sets the starting operational phase (defaults to recon).
	Phase string `json:"phase"`
}

// taskRef addresses a single task by path parameter.
type taskRef struct {
	// ID is the task identifier from the URL path.
	ID string `json:"id"`
}

// graphNodeView is one node in a task reasoning graph.
type graphNodeView struct {
	// ID uniquely identifies the node.
	ID string `json:"id"`
	// TaskID links the node to its task.
	TaskID string `json:"taskId"`
	// Kind is goal, intent, finding, hint, or action.
	Kind string `json:"kind"`
	// Label is the concise node caption rendered on the graph canvas.
	Label string `json:"label"`
	// Detail carries full technical context or rationale.
	Detail string `json:"detail"`
	// Status is active, verified, pending, blocked, or done.
	Status string `json:"status"`
	// Severity carries finding severity when kind is finding.
	Severity string `json:"severity,omitempty"`
	// ParentID links to the upstream node in the reasoning chain.
	ParentID string `json:"parentId,omitempty"`
	// CreatedAt is the creation timestamp in Unix milliseconds.
	CreatedAt int64 `json:"createdAt"`
}

// taskGraphView returns a task together with its ordered reasoning nodes.
type taskGraphView struct {
	// Task is the root task metadata.
	Task taskView `json:"task"`
	// Nodes is the ordered list of GOAL, INTENT, FINDING, HINT, and ACTION nodes.
	Nodes []graphNodeView `json:"nodes"`
}

// addGraphNodeReq appends a node to a task's reasoning graph.
type addGraphNodeReq struct {
	// ID is the task identifier from the URL path.
	ID string `json:"id"`
	// Kind is intent, finding, hint, or action.
	Kind string `json:"kind" validate:"required"`
	// Label is the short node title shown on the graph.
	Label string `json:"label" validate:"required"`
	// Detail provides operator or agent rationale.
	Detail string `json:"detail"`
	// Status sets the node status (defaults to active).
	Status string `json:"status"`
	// Severity sets optional severity for finding nodes.
	Severity string `json:"severity"`
	// ParentID connects this node to an existing upstream node.
	ParentID string `json:"parentId"`
}

// findingFilter filters finding queries.
type findingFilter struct {
	// EngagementID narrows findings to one engagement.
	EngagementID string `json:"engagementId"`
	// Severity narrows findings to critical, high, medium, low, or info.
	Severity string `json:"severity"`
	// Status narrows findings to open, verified, remediated, or false_positive.
	Status string `json:"status"`
}

// findingView is a recorded security finding with verbatim proof state.
type findingView struct {
	// ID uniquely identifies the finding.
	ID string `json:"id"`
	// EngagementID links the finding to its engagement.
	EngagementID string `json:"engagementId"`
	// TaskID links the finding to the originating task when set.
	TaskID string `json:"taskId,omitempty"`
	// Title summarizes the vulnerability or misconfiguration.
	Title string `json:"title"`
	// Severity ranks impact: critical, high, medium, low, or info.
	Severity string `json:"severity"`
	// Category classifies the vulnerability family.
	Category string `json:"category"`
	// CWE records the Common Weakness Enumeration identifier.
	CWE string `json:"cwe,omitempty"`
	// MitreTechnique records the MITRE ATT&CK technique ID (for example T1078).
	MitreTechnique string `json:"mitreTechnique,omitempty"`
	// MitreTactic records the MITRE ATT&CK tactic name.
	MitreTactic string `json:"mitreTactic,omitempty"`
	// Status is open, verified, remediated, or false_positive.
	Status string `json:"status"`
	// Target names the affected endpoint, host, or service.
	Target string `json:"target"`
	// EvidenceQuote is the exact substring expected verbatim in bound HTTP traffic.
	EvidenceQuote string `json:"evidenceQuote,omitempty"`
	// TrafficID references the captured HTTP exchange backing this finding.
	TrafficID string `json:"trafficId,omitempty"`
	// Verified is true only when EvidenceQuote matched bound traffic verbatim.
	Verified bool `json:"verified"`
	// VerificationNote explains the outcome of verbatim proof verification.
	VerificationNote string `json:"verificationNote,omitempty"`
	// Remediation describes concrete fix steps.
	Remediation string `json:"remediation,omitempty"`
	// CreatedAt is the creation timestamp in Unix milliseconds.
	CreatedAt int64 `json:"createdAt"`
	// UpdatedAt is the last update timestamp in Unix milliseconds.
	UpdatedAt int64 `json:"updatedAt"`
}

// findingList returns filtered findings.
type findingList struct {
	// Data holds the matching findings ordered newest first.
	Data []findingView `json:"data"`
}

// createFindingReq records a new finding inside an engagement.
type createFindingReq struct {
	// EngagementID identifies the engagement.
	EngagementID string `json:"engagementId" validate:"required"`
	// TaskID optionally binds the finding to a task and appends a graph node.
	TaskID string `json:"taskId"`
	// Title summarizes the finding.
	Title string `json:"title" validate:"required"`
	// Severity is critical, high, medium, low, or info.
	Severity string `json:"severity"`
	// Category names the weakness class.
	Category string `json:"category"`
	// CWE records the CWE identifier.
	CWE string `json:"cwe"`
	// MitreTechnique records the ATT&CK technique ID.
	MitreTechnique string `json:"mitreTechnique"`
	// MitreTactic records the ATT&CK tactic.
	MitreTactic string `json:"mitreTactic"`
	// Target names the affected host or endpoint.
	Target string `json:"target"`
	// EvidenceQuote is the exact proof substring to verify against traffic.
	EvidenceQuote string `json:"evidenceQuote"`
	// TrafficID binds a captured traffic record for verbatim verification.
	TrafficID string `json:"trafficId"`
	// Remediation records concrete remediation guidance.
	Remediation string `json:"remediation"`
}

// updateFindingReq updates finding workflow status, evidence, or remediation.
type updateFindingReq struct {
	// ID is the finding identifier from the URL path.
	ID string `json:"id"`
	// Status transitions the finding to open, verified, remediated, or false_positive.
	Status string `json:"status"`
	// EvidenceQuote updates the proof substring.
	EvidenceQuote string `json:"evidenceQuote"`
	// TrafficID updates the bound captured HTTP exchange.
	TrafficID string `json:"trafficId"`
	// Remediation updates remediation guidance.
	Remediation string `json:"remediation"`
}

// verifyFindingReq triggers deterministic verbatim proof verification against traffic.
type verifyFindingReq struct {
	// ID is the finding identifier from the URL path.
	ID string `json:"id"`
	// TrafficID optionally binds a captured traffic record before verifying.
	TrafficID string `json:"trafficId"`
	// EvidenceQuote optionally supplies the exact proof quote before verifying.
	EvidenceQuote string `json:"evidenceQuote"`
}

// trafficFilter narrows captured HTTP traffic queries.
type trafficFilter struct {
	// EngagementID narrows records to one engagement.
	EngagementID string `json:"engagementId"`
	// Host filters captured exchanges by hostname.
	Host string `json:"host"`
}

// trafficView is one captured HTTP request/response exchange.
type trafficView struct {
	// ID uniquely identifies the captured traffic record.
	ID string `json:"id"`
	// EngagementID links the exchange to its engagement.
	EngagementID string `json:"engagementId"`
	// TaskID links the exchange to a task when present.
	TaskID string `json:"taskId,omitempty"`
	// Method is the HTTP verb.
	Method string `json:"method"`
	// URL is the full request URL.
	URL string `json:"url"`
	// Host is the extracted target hostname.
	Host string `json:"host"`
	// StatusCode is the HTTP response status code.
	StatusCode int `json:"statusCode"`
	// RequestHeaders holds raw request headers.
	RequestHeaders string `json:"requestHeaders"`
	// RequestBody holds raw request payload.
	RequestBody string `json:"requestBody"`
	// ResponseHeaders holds raw response headers.
	ResponseHeaders string `json:"responseHeaders"`
	// ResponseBody holds raw response payload used for verbatim proof checks.
	ResponseBody string `json:"responseBody"`
	// LatencyMS is the observed round-trip latency in milliseconds.
	LatencyMS int `json:"latencyMs"`
	// CreatedAt is the capture timestamp in Unix milliseconds.
	CreatedAt int64 `json:"createdAt"`
}

// trafficList returns captured HTTP exchanges.
type trafficList struct {
	// Data holds captured exchanges ordered newest first.
	Data []trafficView `json:"data"`
}

// recordTrafficReq ingests a captured HTTP exchange for inspection and proof binding.
type recordTrafficReq struct {
	// EngagementID identifies the engagement.
	EngagementID string `json:"engagementId" validate:"required"`
	// TaskID optionally links the exchange to a task.
	TaskID string `json:"taskId"`
	// Method is the HTTP verb (defaults to GET).
	Method string `json:"method"`
	// URL is the target URL.
	URL string `json:"url" validate:"required"`
	// Host overrides the parsed hostname when provided.
	Host string `json:"host"`
	// StatusCode is the HTTP status code (defaults to 200).
	StatusCode int `json:"statusCode"`
	// RequestHeaders captures serialized request headers.
	RequestHeaders string `json:"requestHeaders"`
	// RequestBody captures the request body.
	RequestBody string `json:"requestBody"`
	// ResponseHeaders captures serialized response headers.
	ResponseHeaders string `json:"responseHeaders"`
	// ResponseBody captures the response body.
	ResponseBody string `json:"responseBody"`
	// LatencyMS records request duration in milliseconds.
	LatencyMS int `json:"latencyMs"`
}

// approvalFilter narrows Kai approval queue queries.
type approvalFilter struct {
	// EngagementID narrows approvals to one engagement.
	EngagementID string `json:"engagementId"`
	// Status narrows approvals to pending, approved, denied, or always_allow.
	Status string `json:"status"`
}

// approvalView is one Kai risk-scored action gate.
type approvalView struct {
	// ID uniquely identifies the approval request.
	ID string `json:"id"`
	// EngagementID links the request to its engagement.
	EngagementID string `json:"engagementId"`
	// TaskID links the request to its task when present.
	TaskID string `json:"taskId,omitempty"`
	// Action describes the proposed command, probe, or mutation.
	Action string `json:"action"`
	// Target names the host or endpoint affected.
	Target string `json:"target"`
	// Tool names the OpenCyber MCP tool or module requesting execution.
	Tool string `json:"tool"`
	// RiskScore is Kai's deterministic risk score from 0 to 100.
	RiskScore int `json:"riskScore"`
	// RiskLevel categorizes the score as low, medium, high, or critical.
	RiskLevel string `json:"riskLevel"`
	// KaiRationale explains why Kai assigned the risk score and required review.
	KaiRationale string `json:"kaiRationale"`
	// Status is pending, approved, denied, or always_allow.
	Status string `json:"status"`
	// DecidedBy records the authenticated IAM subject who made the decision.
	DecidedBy string `json:"decidedBy,omitempty"`
	// DecidedAt records when the decision occurred in Unix milliseconds.
	DecidedAt int64 `json:"decidedAt,omitempty"`
	// CreatedAt is when the action entered the queue in Unix milliseconds.
	CreatedAt int64 `json:"createdAt"`
}

// approvalList returns Kai approvals.
type approvalList struct {
	// Data holds approval items ordered newest first.
	Data []approvalView `json:"data"`
}

// submitApprovalReq queues an action for Kai deterministic risk scoring.
type submitApprovalReq struct {
	// EngagementID identifies the engagement.
	EngagementID string `json:"engagementId" validate:"required"`
	// TaskID optionally links the action to a task.
	TaskID string `json:"taskId"`
	// Action describes the exact command or operation to evaluate.
	Action string `json:"action" validate:"required"`
	// Target names the host, service, or asset.
	Target string `json:"target" validate:"required"`
	// Tool names the tool requesting execution.
	Tool string `json:"tool"`
}

// decideApprovalReq records a human decision on a Kai-gated action.
type decideApprovalReq struct {
	// ID is the approval identifier from the URL path.
	ID string `json:"id"`
	// Decision must be approved, denied, or always_allow.
	Decision string `json:"decision" validate:"required"`
}

// ruleView is one scope or intercept governance rule.
type ruleView struct {
	// ID uniquely identifies the rule.
	ID string `json:"id"`
	// EngagementID scopes the rule to an engagement, or empty for org-wide.
	EngagementID string `json:"engagementId,omitempty"`
	// Name labels the rule.
	Name string `json:"name"`
	// MatchHost is the hostname pattern matched by this rule.
	MatchHost string `json:"matchHost"`
	// MatchPath is the URL path prefix or wildcard matched by this rule.
	MatchPath string `json:"matchPath"`
	// Action is allow, block, require_approval, or tag.
	Action string `json:"action"`
	// Enabled reports whether the rule is enforced.
	Enabled bool `json:"enabled"`
	// CreatedAt is the creation timestamp in Unix milliseconds.
	CreatedAt int64 `json:"createdAt"`
}

// ruleList returns intercept and scope rules.
type ruleList struct {
	// Data holds rules ordered newest first.
	Data []ruleView `json:"data"`
}

// createRuleReq creates a scope or intercept rule.
type createRuleReq struct {
	// EngagementID optionally limits the rule to one engagement.
	EngagementID string `json:"engagementId"`
	// Name describes the rule.
	Name string `json:"name" validate:"required"`
	// MatchHost sets the target host pattern (defaults to *).
	MatchHost string `json:"matchHost"`
	// MatchPath sets the target path pattern (defaults to /*).
	MatchPath string `json:"matchPath"`
	// Action is allow, block, require_approval, or tag.
	Action string `json:"action"`
}

// ruleRef addresses a single intercept rule by path parameter.
type ruleRef struct {
	// ID is the rule identifier from the URL path.
	ID string `json:"id"`
}

// deleteRes confirms deletion of a record.
type deleteRes struct {
	// Deleted is true when the record was removed.
	Deleted bool `json:"deleted"`
	// ID is the deleted record identifier.
	ID string `json:"id"`
}

// attackTechniqueView maps observed findings to one MITRE ATT&CK technique.
type attackTechniqueView struct {
	// TechniqueID is the MITRE ATT&CK technique ID (for example T1190).
	TechniqueID string `json:"techniqueId"`
	// Tactic is the parent MITRE ATT&CK tactic.
	Tactic string `json:"tactic"`
	// Name summarizes the observed technique or finding class.
	Name string `json:"name"`
	// FindingCount is the number of findings mapped to this technique.
	FindingCount int `json:"findingCount"`
	// VerifiedCount is the number of verbatim-verified findings mapped here.
	VerifiedCount int `json:"verifiedCount"`
	// MaxSeverity is the highest severity observed for this technique.
	MaxSeverity string `json:"maxSeverity"`
}

// attackMatrixView summarizes MITRE ATT&CK coverage across an org or engagement.
type attackMatrixView struct {
	// Tactics lists unique MITRE ATT&CK tactics observed.
	Tactics []string `json:"tactics"`
	// Techniques lists observed ATT&CK techniques ranked by severity.
	Techniques []attackTechniqueView `json:"techniques"`
}

// coaItemView is a prioritized Course of Action remediation option.
type coaItemView struct {
	// ID identifies the course of action.
	ID string `json:"id"`
	// Title names the remediation action.
	Title string `json:"title"`
	// Priority is P0, P1, P2, or P3.
	Priority string `json:"priority"`
	// RiskReduction estimates percentage risk reduction (1-100).
	RiskReduction int `json:"riskReduction"`
	// Effort estimates implementation complexity (low, medium, high).
	Effort string `json:"effort"`
	// MitreTechnique names the ATT&CK technique mitigated.
	MitreTechnique string `json:"mitreTechnique,omitempty"`
	// FindingIDs lists finding identifiers closed by this action.
	FindingIDs []string `json:"findingIds"`
	// Summary describes the concrete operational change.
	Summary string `json:"summary"`
}

// coaView returns prioritized Courses of Action computed from open findings.
type coaView struct {
	// Data holds prioritized courses of action ordered highest priority first.
	Data []coaItemView `json:"data"`
}

// hardeningCheckView reports one enclave hardening or governance control check.
type hardeningCheckView struct {
	// ID uniquely identifies the control check.
	ID string `json:"id"`
	// Control names the security control or NIST/DoD baseline check.
	Control string `json:"control"`
	// Status is pass, warn, or fail.
	Status string `json:"status"`
	// Detail explains the live evidence behind the check status.
	Detail string `json:"detail"`
}

// hardeningView summarizes enclave hardening, governance, and SBOM posture.
type hardeningView struct {
	// Score is the aggregate hardening readiness score from 0 to 100.
	Score int `json:"score"`
	// ScopeEnforced reports whether explicit intercept/scope rules are configured.
	ScopeEnforced bool `json:"scopeEnforced"`
	// VerbatimVerified reports how many findings carry verbatim captured proof.
	VerbatimVerified int `json:"verbatimVerified"`
	// OpenCritical reports how many unmitigated critical findings remain.
	OpenCritical int `json:"openCritical"`
	// Checks lists individual posture control evaluations.
	Checks []hardeningCheckView `json:"checks"`
}

// ---- Conversions ----

func toEngagementView(e Engagement) engagementView {
	scope := e.Scope
	if scope == nil {
		scope = []string{}
	}
	return engagementView{
		ID: e.ID, Name: e.Name, Target: e.Target, Scope: scope, Mode: e.Mode,
		Status: e.Status, Synthetic: e.Synthetic, CreatedAt: e.CreatedAt, UpdatedAt: e.UpdatedAt,
	}
}

func toTaskView(t Task) taskView {
	return taskView{
		ID: t.ID, EngagementID: t.EngagementID, Title: t.Title, Goal: t.Goal,
		Status: t.Status, Phase: t.Phase, CreatedAt: t.CreatedAt, UpdatedAt: t.UpdatedAt,
	}
}

func toNodeView(n GraphNode) graphNodeView {
	return graphNodeView{
		ID: n.ID, TaskID: n.TaskID, Kind: n.Kind, Label: n.Label, Detail: n.Detail,
		Status: n.Status, Severity: n.Severity, ParentID: n.ParentID, CreatedAt: n.CreatedAt,
	}
}

func toFindingView(f Finding) findingView {
	return findingView{
		ID: f.ID, EngagementID: f.EngagementID, TaskID: f.TaskID, Title: f.Title,
		Severity: f.Severity, Category: f.Category, CWE: f.CWE,
		MitreTechnique: f.MitreTechnique, MitreTactic: f.MitreTactic,
		Status: f.Status, Target: f.Target, EvidenceQuote: f.EvidenceQuote,
		TrafficID: f.TrafficID, Verified: f.Verified, VerificationNote: f.VerificationNote,
		Remediation: f.Remediation, CreatedAt: f.CreatedAt, UpdatedAt: f.UpdatedAt,
	}
}

func toTrafficView(tr TrafficRecord) trafficView {
	return trafficView{
		ID: tr.ID, EngagementID: tr.EngagementID, TaskID: tr.TaskID, Method: tr.Method,
		URL: tr.URL, Host: tr.Host, StatusCode: tr.StatusCode,
		RequestHeaders: tr.RequestHeaders, RequestBody: tr.RequestBody,
		ResponseHeaders: tr.ResponseHeaders, ResponseBody: tr.ResponseBody,
		LatencyMS: tr.LatencyMS, CreatedAt: tr.CreatedAt,
	}
}

func toApprovalView(a Approval) approvalView {
	return approvalView{
		ID: a.ID, EngagementID: a.EngagementID, TaskID: a.TaskID, Action: a.Action,
		Target: a.Target, Tool: a.Tool, RiskScore: a.RiskScore, RiskLevel: a.RiskLevel,
		KaiRationale: a.KaiRationale, Status: a.Status, DecidedBy: a.DecidedBy,
		DecidedAt: a.DecidedAt, CreatedAt: a.CreatedAt,
	}
}

func toRuleView(r InterceptRule) ruleView {
	return ruleView{
		ID: r.ID, EngagementID: r.EngagementID, Name: r.Name, MatchHost: r.MatchHost,
		MatchPath: r.MatchPath, Action: r.Action, Enabled: r.Enabled, CreatedAt: r.CreatedAt,
	}
}

func severityRank(s string) int {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "critical":
		return 5
	case "high":
		return 4
	case "medium":
		return 3
	case "low":
		return 2
	case "info":
		return 1
	default:
		return 0
	}
}

func normalizeSeverity(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "critical", "high", "medium", "low", "info":
		return strings.ToLower(strings.TrimSpace(s))
	default:
		return "medium"
	}
}

// scoreKaiRisk deterministically scores a proposed cyber action before execution.
func scoreKaiRisk(action, target, tool string) (int, string, string) {
	blob := strings.ToLower(action + " " + target + " " + tool)
	score := 25
	reasons := []string{}

	highTokens := []string{"exploit", "payload", "rce", "exec", "shell", "sqlmap", "write", "delete", "drop", "inject", "credential", "dump", "modify", "patch", "lateral"}
	for _, tok := range highTokens {
		if strings.Contains(blob, tok) {
			score += 25
			reasons = append(reasons, fmt.Sprintf("active mutation/exploitation signal (%s)", tok))
		}
	}
	medTokens := []string{"brute", "fuzz", "post", "put", "scan", "enum", "nuclei", "nikto", "ffuf", "c2", "admin", "auth"}
	for _, tok := range medTokens {
		if strings.Contains(blob, tok) {
			score += 12
			reasons = append(reasons, fmt.Sprintf("active probing on sensitive surface (%s)", tok))
		}
	}
	if score > 98 {
		score = 98
	}

	level := "low"
	switch {
	case score >= 85:
		level = "critical"
	case score >= 65:
		level = "high"
	case score >= 40:
		level = "medium"
	}
	if len(reasons) == 0 {
		reasons = append(reasons, "read-only reconnaissance within declared engagement scope")
	}
	return score, level, "Kai risk gate: " + strings.Join(reasons, "; ")
}

// ---- Typed Handlers ----

// health returns liveness status for the native Hanzo Cyber cloud subsystem.
func (o ops) health(_ context.Context, _ *noIn) (*healthView, error) {
	return &healthView{
		Status:  "ok",
		Flag:    BetaFlagKey,
		Backend: "hanzo-cloud",
	}, nil
}

// dashboard aggregates engagements, findings, verbatim proof coverage, Kai approvals, and traffic counts for the authenticated org.
func (o ops) dashboard(ctx context.Context, in *engagementFilter) (*dashboardView, error) {
	org, err := o.authorize(ctx)
	if err != nil {
		return nil, err
	}
	engID := strings.TrimSpace(in.EngagementID)
	engs, err := o.s.State.store.ListEngagements(ctx, org)
	if err != nil {
		return nil, zip.Errorf(500, "list engagements: %v", err)
	}
	tasks, err := o.s.State.store.ListTasks(ctx, org, engID)
	if err != nil {
		return nil, zip.Errorf(500, "list tasks: %v", err)
	}
	findings, err := o.s.State.store.ListFindings(ctx, org, engID, "", "")
	if err != nil {
		return nil, zip.Errorf(500, "list findings: %v", err)
	}
	approvals, err := o.s.State.store.ListApprovals(ctx, org, engID, "pending")
	if err != nil {
		return nil, zip.Errorf(500, "list approvals: %v", err)
	}
	traffic, err := o.s.State.store.ListTraffic(ctx, org, engID, "")
	if err != nil {
		return nil, zip.Errorf(500, "list traffic: %v", err)
	}
	rules, err := o.s.State.store.ListRules(ctx, org, engID)
	if err != nil {
		return nil, zip.Errorf(500, "list rules: %v", err)
	}

	out := &dashboardView{
		Org:              org,
		Stage:            "alpha",
		Engagements:      len(engs),
		Tasks:            len(tasks),
		FindingsTotal:    len(findings),
		PendingApprovals: len(approvals),
		TrafficCount:     len(traffic),
		RulesCount:       len(rules),
	}
	for _, f := range findings {
		if f.Verified {
			out.FindingsVerified++
		}
		switch f.Severity {
		case "critical":
			out.Critical++
		case "high":
			out.High++
		case "medium":
			out.Medium++
		case "low":
			out.Low++
		}
	}
	if out.FindingsTotal > 0 {
		out.VerificationRate = (out.FindingsVerified * 100) / out.FindingsTotal
	}
	return out, nil
}

// listEngagements returns every authorized cyber engagement belonging to the caller's org.
func (o ops) listEngagements(ctx context.Context, _ *noIn) (*engagementList, error) {
	org, err := o.authorize(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := o.s.State.store.ListEngagements(ctx, org)
	if err != nil {
		return nil, zip.Errorf(500, "list engagements: %v", err)
	}
	out := make([]engagementView, 0, len(rows))
	for _, r := range rows {
		out = append(out, toEngagementView(r))
	}
	return &engagementList{Data: out}, nil
}

// createEngagement creates a new org-scoped cyber engagement workspace with explicit target scope.
func (o ops) createEngagement(ctx context.Context, in *createEngagementReq) (*engagementView, error) {
	org, err := o.authorize(ctx)
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(in.Name)
	target := strings.TrimSpace(in.Target)
	if name == "" || target == "" {
		return nil, zip.ErrBadRequest("name and target are required")
	}
	mode := strings.TrimSpace(in.Mode)
	if mode == "" {
		mode = "enclave"
	}
	scope := in.Scope
	if len(scope) == 0 {
		scope = []string{target}
	}
	now := nowMS()
	eng := Engagement{
		ID: newID("eng"), Org: org, Name: name, Target: target,
		Scope: scope, Mode: mode, Status: "active", Synthetic: in.Synthetic,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := o.s.State.store.SaveEngagement(ctx, eng); err != nil {
		return nil, zip.Errorf(500, "save engagement: %v", err)
	}
	o.emitAudit(ctx, org, "cyber.engagement.create", "cyber.engagement", eng.ID, http.StatusCreated)
	v := toEngagementView(eng)
	return &v, nil
}

// seedDemo idempotently seeds a synthetic MDTF Tactical Enclave (Lab) engagement for internal testing and defense demos.
func (o ops) seedDemo(ctx context.Context, _ *noIn) (*seedDemoRes, error) {
	org, err := o.authorize(ctx)
	if err != nil {
		return nil, err
	}
	now := nowMS()
	engID := "eng_mdtf_lab_" + org
	taskID := "tsk_mdtf_audit_" + org
	tr1ID := "trf_mdtf_tak_" + org
	tr2ID := "trf_mdtf_c2_" + org
	tr3ID := "trf_mdtf_enclave_" + org

	eng := Engagement{
		ID: engID, Org: org, Name: "MDTF Tactical Enclave (Lab)",
		Target: "enclave.mdtf.test",
		Scope:  []string{"enclave.mdtf.test", "c2.mdtf.test", "tak.mdtf.test"},
		Mode:   "airgap", Status: "active", Synthetic: true,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := o.s.State.store.SaveEngagement(ctx, eng); err != nil {
		return nil, zip.Errorf(500, "seed engagement: %v", err)
	}

	task := Task{
		ID: taskID, Org: org, EngagementID: engID,
		Title: "Tactical C2 & TAK Gateway Zero-Trust Verification",
		Goal:  "Verify authentication boundaries, token scopes, and lateral movement resistance across enclave.mdtf.test, c2.mdtf.test, and tak.mdtf.test with verbatim HTTP proof.",
		Status: "active", Phase: "verify", CreatedAt: now, UpdatedAt: now,
	}
	if err := o.s.State.store.SaveTask(ctx, task); err != nil {
		return nil, zip.Errorf(500, "seed task: %v", err)
	}

	nodes := []GraphNode{
		{ID: "nod_goal_" + org, Org: org, TaskID: taskID, Kind: "goal", Label: "Verify MDTF Tactical Enclave Zero-Trust Boundary", Detail: task.Goal, Status: "active", CreatedAt: now},
		{ID: "nod_int1_" + org, Org: org, TaskID: taskID, Kind: "intent", Label: "Enumerate TAK Relay Federation Endpoints", Detail: "Inspect /api/v1/relay/federation and mTLS enforcement on tak.mdtf.test", Status: "done", ParentID: "nod_goal_" + org, CreatedAt: now + 1},
		{ID: "nod_int2_" + org, Org: org, TaskID: taskID, Kind: "intent", Label: "Audit C2 Dispatch Role Enforcement", Detail: "Validate operator JWT audience & role enforcement on c2.mdtf.test/v1/dispatch", Status: "done", ParentID: "nod_goal_" + org, CreatedAt: now + 2},
		{ID: "nod_fnd1_" + org, Org: org, TaskID: taskID, Kind: "finding", Label: "Unauthenticated TAK Telemetry Replay Endpoint", Detail: "Verbatim response returned federation key metadata without client certificate.", Status: "verified", Severity: "critical", ParentID: "nod_int1_" + org, CreatedAt: now + 3},
		{ID: "nod_fnd2_" + org, Org: org, TaskID: taskID, Kind: "finding", Label: "Over-Privileged C2 Service Token Scope", Detail: "Read-only observer JWT accepted on /v1/dispatch/override dry-run.", Status: "verified", Severity: "high", ParentID: "nod_int2_" + org, CreatedAt: now + 4},
		{ID: "nod_hnt1_" + org, Org: org, TaskID: taskID, Kind: "hint", Label: "Kai Gate: Active Relay Fuzzing Held for Operator Sign-Off", Detail: "Risk score 88/100 (critical): requires human commander authorization before active state mutation.", Status: "pending", ParentID: "nod_fnd1_" + org, CreatedAt: now + 5},
	}
	for _, n := range nodes {
		if err := o.s.State.store.SaveNode(ctx, n); err != nil {
			return nil, zip.Errorf(500, "seed node: %v", err)
		}
	}

	traffics := []TrafficRecord{
		{
			ID: tr1ID, Org: org, EngagementID: engID, TaskID: taskID,
			Method: "GET", URL: "https://tak.mdtf.test/api/v1/relay/federation/status", Host: "tak.mdtf.test",
			StatusCode: 200,
			RequestHeaders:  "Host: tak.mdtf.test\nUser-Agent: Hanzo-Cyber-Verifier/1.0\nAccept: application/json",
			ResponseHeaders: "HTTP/1.1 200 OK\nContent-Type: application/json\nX-Enclave-Node: tak-relay-01",
			ResponseBody:    `{"enclave":"mdtf-lab","mtls_required":false,"federation_peer":"c2.mdtf.test","signing_kid":"tak-sim-key-01","status":"open_relay"}`,
			LatencyMS: 18, CreatedAt: now,
		},
		{
			ID: tr2ID, Org: org, EngagementID: engID, TaskID: taskID,
			Method: "POST", URL: "https://c2.mdtf.test/v1/dispatch/override?dryRun=true", Host: "c2.mdtf.test",
			StatusCode: 200,
			RequestHeaders:  "Host: c2.mdtf.test\nAuthorization: Bearer <redacted-observer-token>\nContent-Type: application/json",
			RequestBody:     `{"mission":"sim-alpha","mode":"dry_run"}`,
			ResponseHeaders: "HTTP/1.1 200 OK\nContent-Type: application/json",
			ResponseBody:    `{"accepted":true,"caller_role":"observer","enforced_role":"commander","bypass_reason":"legacy_dryrun_allowlist"}`,
			LatencyMS: 24, CreatedAt: now + 1,
		},
		{
			ID: tr3ID, Org: org, EngagementID: engID, TaskID: taskID,
			Method: "GET", URL: "https://enclave.mdtf.test/.well-known/security-posture", Host: "enclave.mdtf.test",
			StatusCode: 200,
			RequestHeaders:  "Host: enclave.mdtf.test\nAccept: application/json",
			ResponseHeaders: "HTTP/1.1 200 OK\nContent-Type: application/json\nStrict-Transport-Security: max-age=31536000",
			ResponseBody:    `{"enclave":"mdtf-lab","fips_mode":true,"pqc_kem":"ML-KEM-768","debug_metrics_exposed":true}`,
			LatencyMS: 11, CreatedAt: now + 2,
		},
	}
	for _, tr := range traffics {
		if err := o.s.State.store.SaveTraffic(ctx, tr); err != nil {
			return nil, zip.Errorf(500, "seed traffic: %v", err)
		}
	}

	findings := []Finding{
		{
			ID: "fnd_mdtf_1_" + org, Org: org, EngagementID: engID, TaskID: taskID,
			Title: "Unauthenticated TAK Federation Relay Metadata Exposure",
			Severity: "critical", Category: "Broken Authentication", CWE: "CWE-306",
			MitreTechnique: "T1190", MitreTactic: "Initial Access",
			Status: "verified", Target: "https://tak.mdtf.test/api/v1/relay/federation/status",
			EvidenceQuote: `"mtls_required":false,"federation_peer":"c2.mdtf.test"`,
			TrafficID: tr1ID, Verified: true,
			VerificationNote: "Verbatim substring confirmed in response body of " + tr1ID,
			Remediation: "Enforce mutual TLS (client cert pinning) on all TAK relay endpoints and disable unauthenticated federation status disclosure.",
			CreatedAt: now, UpdatedAt: now,
		},
		{
			ID: "fnd_mdtf_2_" + org, Org: org, EngagementID: engID, TaskID: taskID,
			Title: "Observer JWT Accepted on C2 Dispatch Override Route",
			Severity: "high", Category: "Broken Access Control", CWE: "CWE-863",
			MitreTechnique: "T1078", MitreTactic: "Privilege Escalation",
			Status: "verified", Target: "https://c2.mdtf.test/v1/dispatch/override",
			EvidenceQuote: `"caller_role":"observer","enforced_role":"commander","bypass_reason":"legacy_dryrun_allowlist"`,
			TrafficID: tr2ID, Verified: true,
			VerificationNote: "Verbatim substring confirmed in response body of " + tr2ID,
			Remediation: "Remove legacy_dryrun_allowlist and require explicit commander role claim via Hanzo IAM / Zanzibar Authz before route entry.",
			CreatedAt: now + 1, UpdatedAt: now + 1,
		},
		{
			ID: "fnd_mdtf_3_" + org, Org: org, EngagementID: engID, TaskID: taskID,
			Title: "Enclave Debug Metrics Flag Enabled on Edge Gateway",
			Severity: "medium", Category: "Security Misconfiguration", CWE: "CWE-215",
			MitreTechnique: "T1082", MitreTactic: "Discovery",
			Status: "open", Target: "https://enclave.mdtf.test/.well-known/security-posture",
			EvidenceQuote: `"debug_metrics_exposed":true`,
			TrafficID: tr3ID, Verified: false,
			VerificationNote: "Awaiting operator verbatim verification click",
			Remediation: "Disable debug_metrics_exposed in production enclave configuration and bind diagnostics to loopback only.",
			CreatedAt: now + 2, UpdatedAt: now + 2,
		},
	}
	for _, f := range findings {
		if err := o.s.State.store.SaveFinding(ctx, f); err != nil {
			return nil, zip.Errorf(500, "seed finding: %v", err)
		}
	}

	approvals := []Approval{
		{
			ID: "apr_mdtf_1_" + org, Org: org, EngagementID: engID, TaskID: taskID,
			Action: "Run active mTLS downgrade & relay replay verification payload against tak.mdtf.test/api/v1/relay",
			Target: "tak.mdtf.test", Tool: "opencyber-mcp:http_replay_verify",
			RiskScore: 88, RiskLevel: "critical",
			KaiRationale: "Kai risk gate: active mutation/exploitation signal (payload); active probing on sensitive surface (c2/relay); requires human operator approval.",
			Status: "pending", CreatedAt: now,
		},
		{
			ID: "apr_mdtf_2_" + org, Org: org, EngagementID: engID, TaskID: taskID,
			Action: "Passive TLS cipher suite & ML-KEM-768 PQC handshake inspection on enclave.mdtf.test",
			Target: "enclave.mdtf.test", Tool: "opencyber-mcp:tls_audit",
			RiskScore: 18, RiskLevel: "low",
			KaiRationale: "Kai risk gate: read-only reconnaissance within declared engagement scope.",
			Status: "approved", DecidedBy: o.actor(ctx, org), DecidedAt: now, CreatedAt: now - 60000,
		},
	}
	for _, a := range approvals {
		if err := o.s.State.store.SaveApproval(ctx, a); err != nil {
			return nil, zip.Errorf(500, "seed approval: %v", err)
		}
	}

	rules := []InterceptRule{
		{ID: "rul_mdtf_1_" + org, Org: org, EngagementID: engID, Name: "Enclave Authorized Scope Allowlist", MatchHost: "*.mdtf.test", MatchPath: "/*", Action: "allow", Enabled: true, CreatedAt: now},
		{ID: "rul_mdtf_2_" + org, Org: org, EngagementID: engID, Name: "Require Kai Human Sign-Off on C2 Write Paths", MatchHost: "c2.mdtf.test", MatchPath: "/v1/dispatch/*", Action: "require_approval", Enabled: true, CreatedAt: now + 1},
		{ID: "rul_mdtf_3_" + org, Org: org, EngagementID: engID, Name: "Block Out-of-Scope Egress", MatchHost: "!*.mdtf.test", MatchPath: "/*", Action: "block", Enabled: true, CreatedAt: now + 2},
	}
	for _, r := range rules {
		if err := o.s.State.store.SaveRule(ctx, r); err != nil {
			return nil, zip.Errorf(500, "seed rule: %v", err)
		}
	}

	o.emitAudit(ctx, org, "cyber.engagement.seed_demo", "cyber.engagement", engID, http.StatusCreated)
	return &seedDemoRes{
		Engagement: toEngagementView(eng),
		Tasks:      1,
		Findings:   len(findings),
		Traffic:    len(traffics),
		Approvals:  len(approvals),
		Rules:      len(rules),
	}, nil
}

// listTasks returns cyber tasks belonging to the caller's org, optionally filtered by engagementId.
func (o ops) listTasks(ctx context.Context, in *engagementFilter) (*taskList, error) {
	org, err := o.authorize(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := o.s.State.store.ListTasks(ctx, org, strings.TrimSpace(in.EngagementID))
	if err != nil {
		return nil, zip.Errorf(500, "list tasks: %v", err)
	}
	out := make([]taskView, 0, len(rows))
	for _, r := range rows {
		out = append(out, toTaskView(r))
	}
	return &taskList{Data: out}, nil
}

// createTask creates a cyber task and initializes its root GOAL node in the reasoning graph.
func (o ops) createTask(ctx context.Context, in *createTaskReq) (*taskView, error) {
	org, err := o.authorize(ctx)
	if err != nil {
		return nil, err
	}
	engID := strings.TrimSpace(in.EngagementID)
	title := strings.TrimSpace(in.Title)
	if engID == "" || title == "" {
		return nil, zip.ErrBadRequest("engagementId and title are required")
	}
	if _, err := o.s.State.store.GetEngagement(ctx, org, engID); err != nil {
		if errors.Is(err, errNotFound) {
			return nil, zip.ErrNotFound("engagement not found")
		}
		return nil, zip.Errorf(500, "get engagement: %v", err)
	}
	phase := strings.TrimSpace(in.Phase)
	if phase == "" {
		phase = "recon"
	}
	now := nowMS()
	task := Task{
		ID: newID("tsk"), Org: org, EngagementID: engID,
		Title: title, Goal: strings.TrimSpace(in.Goal),
		Status: "active", Phase: phase, CreatedAt: now, UpdatedAt: now,
	}
	if err := o.s.State.store.SaveTask(ctx, task); err != nil {
		return nil, zip.Errorf(500, "save task: %v", err)
	}
	root := GraphNode{
		ID: newID("nod"), Org: org, TaskID: task.ID, Kind: "goal",
		Label: task.Title, Detail: task.Goal, Status: "active", CreatedAt: now,
	}
	if err := o.s.State.store.SaveNode(ctx, root); err != nil {
		return nil, zip.Errorf(500, "save root node: %v", err)
	}
	v := toTaskView(task)
	return &v, nil
}

// getTaskGraph returns a task together with its ordered GOAL -> INTENT -> FINDING -> HINT reasoning graph.
func (o ops) getTaskGraph(ctx context.Context, in *taskRef) (*taskGraphView, error) {
	org, err := o.authorize(ctx)
	if err != nil {
		return nil, err
	}
	t, err := o.s.State.store.GetTask(ctx, org, strings.TrimSpace(in.ID))
	if errors.Is(err, errNotFound) {
		return nil, zip.ErrNotFound("task not found")
	}
	if err != nil {
		return nil, zip.Errorf(500, "get task: %v", err)
	}
	nodes, err := o.s.State.store.ListNodes(ctx, org, t.ID)
	if err != nil {
		return nil, zip.Errorf(500, "list nodes: %v", err)
	}
	nv := make([]graphNodeView, 0, len(nodes))
	for _, n := range nodes {
		nv = append(nv, toNodeView(n))
	}
	return &taskGraphView{Task: toTaskView(t), Nodes: nv}, nil
}

// addGraphNode appends an intent, finding, hint, or action node to a task's reasoning graph.
func (o ops) addGraphNode(ctx context.Context, in *addGraphNodeReq) (*graphNodeView, error) {
	org, err := o.authorize(ctx)
	if err != nil {
		return nil, err
	}
	t, err := o.s.State.store.GetTask(ctx, org, strings.TrimSpace(in.ID))
	if errors.Is(err, errNotFound) {
		return nil, zip.ErrNotFound("task not found")
	}
	if err != nil {
		return nil, zip.Errorf(500, "get task: %v", err)
	}
	kind := strings.ToLower(strings.TrimSpace(in.Kind))
	switch kind {
	case "goal", "intent", "finding", "hint", "action":
	default:
		return nil, zip.ErrBadRequest("kind must be goal|intent|finding|hint|action")
	}
	label := strings.TrimSpace(in.Label)
	if label == "" {
		return nil, zip.ErrBadRequest("label is required")
	}
	status := strings.TrimSpace(in.Status)
	if status == "" {
		status = "active"
	}
	node := GraphNode{
		ID: newID("nod"), Org: org, TaskID: t.ID, Kind: kind,
		Label: label, Detail: strings.TrimSpace(in.Detail), Status: status,
		Severity: strings.TrimSpace(in.Severity), ParentID: strings.TrimSpace(in.ParentID),
		CreatedAt: nowMS(),
	}
	if err := o.s.State.store.SaveNode(ctx, node); err != nil {
		return nil, zip.Errorf(500, "save node: %v", err)
	}
	v := toNodeView(node)
	return &v, nil
}

// listFindings returns org-scoped findings optionally filtered by engagementId, severity, and status.
func (o ops) listFindings(ctx context.Context, in *findingFilter) (*findingList, error) {
	org, err := o.authorize(ctx)
	if err != nil {
		return nil, err
	}
	sev := strings.ToLower(strings.TrimSpace(in.Severity))
	if sev != "" && severityRank(sev) == 0 {
		return nil, zip.ErrBadRequest("severity must be critical|high|medium|low|info")
	}
	rows, err := o.s.State.store.ListFindings(ctx, org, strings.TrimSpace(in.EngagementID), sev, strings.TrimSpace(in.Status))
	if err != nil {
		return nil, zip.Errorf(500, "list findings: %v", err)
	}
	out := make([]findingView, 0, len(rows))
	for _, r := range rows {
		out = append(out, toFindingView(r))
	}
	return &findingList{Data: out}, nil
}

// createFinding records a security finding inside an authorized engagement.
func (o ops) createFinding(ctx context.Context, in *createFindingReq) (*findingView, error) {
	org, err := o.authorize(ctx)
	if err != nil {
		return nil, err
	}
	engID := strings.TrimSpace(in.EngagementID)
	title := strings.TrimSpace(in.Title)
	if engID == "" || title == "" {
		return nil, zip.ErrBadRequest("engagementId and title are required")
	}
	if _, err := o.s.State.store.GetEngagement(ctx, org, engID); err != nil {
		if errors.Is(err, errNotFound) {
			return nil, zip.ErrNotFound("engagement not found")
		}
		return nil, zip.Errorf(500, "get engagement: %v", err)
	}
	now := nowMS()
	f := Finding{
		ID: newID("fnd"), Org: org, EngagementID: engID, TaskID: strings.TrimSpace(in.TaskID),
		Title: title, Severity: normalizeSeverity(in.Severity),
		Category: strings.TrimSpace(in.Category), CWE: strings.TrimSpace(in.CWE),
		MitreTechnique: strings.TrimSpace(in.MitreTechnique), MitreTactic: strings.TrimSpace(in.MitreTactic),
		Status: "open", Target: strings.TrimSpace(in.Target),
		EvidenceQuote: strings.TrimSpace(in.EvidenceQuote), TrafficID: strings.TrimSpace(in.TrafficID),
		Remediation: strings.TrimSpace(in.Remediation), CreatedAt: now, UpdatedAt: now,
	}
	if err := o.s.State.store.SaveFinding(ctx, f); err != nil {
		return nil, zip.Errorf(500, "save finding: %v", err)
	}
	o.emitAudit(ctx, org, "cyber.finding.create", "cyber.finding", f.ID, http.StatusCreated)
	v := toFindingView(f)
	return &v, nil
}

// updateFinding updates a finding's status, bound traffic, evidence quote, or remediation text.
func (o ops) updateFinding(ctx context.Context, in *updateFindingReq) (*findingView, error) {
	org, err := o.authorize(ctx)
	if err != nil {
		return nil, err
	}
	f, err := o.s.State.store.GetFinding(ctx, org, strings.TrimSpace(in.ID))
	if errors.Is(err, errNotFound) {
		return nil, zip.ErrNotFound("finding not found")
	}
	if err != nil {
		return nil, zip.Errorf(500, "get finding: %v", err)
	}
	if st := strings.TrimSpace(in.Status); st != "" {
		switch st {
		case "open", "verified", "remediated", "false_positive":
			f.Status = st
		default:
			return nil, zip.ErrBadRequest("status must be open|verified|remediated|false_positive")
		}
	}
	if in.EvidenceQuote != "" {
		f.EvidenceQuote = strings.TrimSpace(in.EvidenceQuote)
		f.Verified = false
	}
	if in.TrafficID != "" {
		f.TrafficID = strings.TrimSpace(in.TrafficID)
		f.Verified = false
	}
	if in.Remediation != "" {
		f.Remediation = strings.TrimSpace(in.Remediation)
	}
	f.UpdatedAt = nowMS()
	if err := o.s.State.store.SaveFinding(ctx, f); err != nil {
		return nil, zip.Errorf(500, "save finding: %v", err)
	}
	v := toFindingView(f)
	return &v, nil
}

// verifyFinding performs deterministic verbatim substring verification of a finding's evidenceQuote against its bound captured HTTP traffic record.
func (o ops) verifyFinding(ctx context.Context, in *verifyFindingReq) (*findingView, error) {
	org, err := o.authorize(ctx)
	if err != nil {
		return nil, err
	}
	f, err := o.s.State.store.GetFinding(ctx, org, strings.TrimSpace(in.ID))
	if errors.Is(err, errNotFound) {
		return nil, zip.ErrNotFound("finding not found")
	}
	if err != nil {
		return nil, zip.Errorf(500, "get finding: %v", err)
	}
	if strings.TrimSpace(in.TrafficID) != "" {
		f.TrafficID = strings.TrimSpace(in.TrafficID)
	}
	if strings.TrimSpace(in.EvidenceQuote) != "" {
		f.EvidenceQuote = strings.TrimSpace(in.EvidenceQuote)
	}
	if f.TrafficID == "" || f.EvidenceQuote == "" {
		return nil, zip.ErrBadRequest("trafficId and evidenceQuote are required for verbatim verification")
	}
	tr, err := o.s.State.store.GetTraffic(ctx, org, f.TrafficID)
	if errors.Is(err, errNotFound) {
		return nil, zip.ErrNotFound("bound traffic record not found")
	}
	if err != nil {
		return nil, zip.Errorf(500, "get traffic: %v", err)
	}

	corpus := tr.RequestHeaders + "\n" + tr.RequestBody + "\n" + tr.ResponseHeaders + "\n" + tr.ResponseBody
	if strings.Contains(corpus, f.EvidenceQuote) {
		f.Verified = true
		f.Status = "verified"
		f.VerificationNote = fmt.Sprintf("Verbatim substring confirmed in captured traffic %s (%s %s)", tr.ID, tr.Method, tr.URL)
	} else {
		f.Verified = false
		f.VerificationNote = fmt.Sprintf("Verification failed: exact evidenceQuote not present in traffic %s", tr.ID)
	}
	f.UpdatedAt = nowMS()
	if err := o.s.State.store.SaveFinding(ctx, f); err != nil {
		return nil, zip.Errorf(500, "save finding: %v", err)
	}
	o.emitAudit(ctx, org, "cyber.finding.verify", "cyber.finding", f.ID, http.StatusOK)
	v := toFindingView(f)
	return &v, nil
}

// listTraffic returns captured HTTP exchanges belonging to the caller's org.
func (o ops) listTraffic(ctx context.Context, in *trafficFilter) (*trafficList, error) {
	org, err := o.authorize(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := o.s.State.store.ListTraffic(ctx, org, strings.TrimSpace(in.EngagementID), strings.TrimSpace(in.Host))
	if err != nil {
		return nil, zip.Errorf(500, "list traffic: %v", err)
	}
	out := make([]trafficView, 0, len(rows))
	for _, r := range rows {
		out = append(out, toTrafficView(r))
	}
	return &trafficList{Data: out}, nil
}

// recordTraffic stores a captured HTTP exchange for inspection and verbatim finding proof binding.
func (o ops) recordTraffic(ctx context.Context, in *recordTrafficReq) (*trafficView, error) {
	org, err := o.authorize(ctx)
	if err != nil {
		return nil, err
	}
	engID := strings.TrimSpace(in.EngagementID)
	rawURL := strings.TrimSpace(in.URL)
	if engID == "" || rawURL == "" {
		return nil, zip.ErrBadRequest("engagementId and url are required")
	}
	if _, err := o.s.State.store.GetEngagement(ctx, org, engID); err != nil {
		if errors.Is(err, errNotFound) {
			return nil, zip.ErrNotFound("engagement not found")
		}
		return nil, zip.Errorf(500, "get engagement: %v", err)
	}
	host := strings.TrimSpace(in.Host)
	if host == "" {
		if u, parseErr := url.Parse(rawURL); parseErr == nil {
			host = u.Hostname()
		}
	}
	method := strings.ToUpper(strings.TrimSpace(in.Method))
	if method == "" {
		method = "GET"
	}
	statusCode := in.StatusCode
	if statusCode == 0 {
		statusCode = 200
	}
	tr := TrafficRecord{
		ID: newID("trf"), Org: org, EngagementID: engID, TaskID: strings.TrimSpace(in.TaskID),
		Method: method, URL: rawURL, Host: host, StatusCode: statusCode,
		RequestHeaders: in.RequestHeaders, RequestBody: in.RequestBody,
		ResponseHeaders: in.ResponseHeaders, ResponseBody: in.ResponseBody,
		LatencyMS: in.LatencyMS, CreatedAt: nowMS(),
	}
	if err := o.s.State.store.SaveTraffic(ctx, tr); err != nil {
		return nil, zip.Errorf(500, "save traffic: %v", err)
	}
	v := toTrafficView(tr)
	return &v, nil
}

// listApprovals returns Kai risk-scored action approvals for the caller's org.
func (o ops) listApprovals(ctx context.Context, in *approvalFilter) (*approvalList, error) {
	org, err := o.authorize(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := o.s.State.store.ListApprovals(ctx, org, strings.TrimSpace(in.EngagementID), strings.TrimSpace(in.Status))
	if err != nil {
		return nil, zip.Errorf(500, "list approvals: %v", err)
	}
	out := make([]approvalView, 0, len(rows))
	for _, r := range rows {
		out = append(out, toApprovalView(r))
	}
	return &approvalList{Data: out}, nil
}

// submitApproval evaluates a proposed cyber action with Kai deterministic risk scoring and queues it for human approval.
func (o ops) submitApproval(ctx context.Context, in *submitApprovalReq) (*approvalView, error) {
	org, err := o.authorize(ctx)
	if err != nil {
		return nil, err
	}
	engID := strings.TrimSpace(in.EngagementID)
	action := strings.TrimSpace(in.Action)
	target := strings.TrimSpace(in.Target)
	if engID == "" || action == "" || target == "" {
		return nil, zip.ErrBadRequest("engagementId, action, and target are required")
	}
	if _, err := o.s.State.store.GetEngagement(ctx, org, engID); err != nil {
		if errors.Is(err, errNotFound) {
			return nil, zip.ErrNotFound("engagement not found")
		}
		return nil, zip.Errorf(500, "get engagement: %v", err)
	}
	score, level, rationale := scoreKaiRisk(action, target, in.Tool)
	a := Approval{
		ID: newID("apr"), Org: org, EngagementID: engID, TaskID: strings.TrimSpace(in.TaskID),
		Action: action, Target: target, Tool: strings.TrimSpace(in.Tool),
		RiskScore: score, RiskLevel: level, KaiRationale: rationale,
		Status: "pending", CreatedAt: nowMS(),
	}
	if err := o.s.State.store.SaveApproval(ctx, a); err != nil {
		return nil, zip.Errorf(500, "save approval: %v", err)
	}
	o.emitAudit(ctx, org, "cyber.approval.submit", "cyber.approval", a.ID, http.StatusCreated)
	v := toApprovalView(a)
	return &v, nil
}

// decideApproval records a human Approve, Deny, or Always-Allow decision on a queued Kai approval.
func (o ops) decideApproval(ctx context.Context, in *decideApprovalReq) (*approvalView, error) {
	org, err := o.authorize(ctx)
	if err != nil {
		return nil, err
	}
	a, err := o.s.State.store.GetApproval(ctx, org, strings.TrimSpace(in.ID))
	if errors.Is(err, errNotFound) {
		return nil, zip.ErrNotFound("approval not found")
	}
	if err != nil {
		return nil, zip.Errorf(500, "get approval: %v", err)
	}
	dec := strings.ToLower(strings.TrimSpace(in.Decision))
	switch dec {
	case "approved", "denied", "always_allow":
	default:
		return nil, zip.ErrBadRequest("decision must be approved|denied|always_allow")
	}
	now := nowMS()
	a.Status = dec
	a.DecidedBy = o.actor(ctx, org)
	a.DecidedAt = now
	if err := o.s.State.store.SaveApproval(ctx, a); err != nil {
		return nil, zip.Errorf(500, "save approval: %v", err)
	}
	if dec == "always_allow" {
		_ = o.s.State.store.SaveRule(ctx, InterceptRule{
			ID: newID("rul"), Org: org, EngagementID: a.EngagementID,
			Name: "Always-Allow: " + a.Tool, MatchHost: a.Target, MatchPath: "/*",
			Action: "allow", Enabled: true, CreatedAt: now,
		})
	}
	o.emitAudit(ctx, org, "cyber.approval."+dec, "cyber.approval", a.ID, http.StatusOK)
	v := toApprovalView(a)
	return &v, nil
}

// listRules returns scope and intercept governance rules for the caller's org.
func (o ops) listRules(ctx context.Context, in *engagementFilter) (*ruleList, error) {
	org, err := o.authorize(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := o.s.State.store.ListRules(ctx, org, strings.TrimSpace(in.EngagementID))
	if err != nil {
		return nil, zip.Errorf(500, "list rules: %v", err)
	}
	out := make([]ruleView, 0, len(rows))
	for _, r := range rows {
		out = append(out, toRuleView(r))
	}
	return &ruleList{Data: out}, nil
}

// createRule adds a scope or intercept governance rule for the caller's org.
func (o ops) createRule(ctx context.Context, in *createRuleReq) (*ruleView, error) {
	org, err := o.authorize(ctx)
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return nil, zip.ErrBadRequest("name is required")
	}
	action := strings.ToLower(strings.TrimSpace(in.Action))
	if action == "" {
		action = "require_approval"
	}
	switch action {
	case "allow", "block", "require_approval", "tag":
	default:
		return nil, zip.ErrBadRequest("action must be allow|block|require_approval|tag")
	}
	host := strings.TrimSpace(in.MatchHost)
	if host == "" {
		host = "*"
	}
	path := strings.TrimSpace(in.MatchPath)
	if path == "" {
		path = "/*"
	}
	rule := InterceptRule{
		ID: newID("rul"), Org: org, EngagementID: strings.TrimSpace(in.EngagementID),
		Name: name, MatchHost: host, MatchPath: path, Action: action,
		Enabled: true, CreatedAt: nowMS(),
	}
	if err := o.s.State.store.SaveRule(ctx, rule); err != nil {
		return nil, zip.Errorf(500, "save rule: %v", err)
	}
	v := toRuleView(rule)
	return &v, nil
}

// deleteRule removes an org-scoped intercept rule.
func (o ops) deleteRule(ctx context.Context, in *ruleRef) (*deleteRes, error) {
	org, err := o.authorize(ctx)
	if err != nil {
		return nil, err
	}
	id := strings.TrimSpace(in.ID)
	err = o.s.State.store.DeleteRule(ctx, org, id)
	if errors.Is(err, errNotFound) {
		return nil, zip.ErrNotFound("rule not found")
	}
	if err != nil {
		return nil, zip.Errorf(500, "delete rule: %v", err)
	}
	return &deleteRes{Deleted: true, ID: id}, nil
}

// attackMatrix projects the org's findings onto MITRE ATT&CK tactics and techniques.
func (o ops) attackMatrix(ctx context.Context, in *engagementFilter) (*attackMatrixView, error) {
	org, err := o.authorize(ctx)
	if err != nil {
		return nil, err
	}
	findings, err := o.s.State.store.ListFindings(ctx, org, strings.TrimSpace(in.EngagementID), "", "")
	if err != nil {
		return nil, zip.Errorf(500, "list findings: %v", err)
	}
	byTech := map[string]*attackTechniqueView{}
	tacticsSet := map[string]bool{}
	for _, f := range findings {
		tid := strings.TrimSpace(f.MitreTechnique)
		if tid == "" {
			tid = "UNMAPPED"
		}
		tactic := strings.TrimSpace(f.MitreTactic)
		if tactic == "" {
			tactic = "Discovery"
		}
		tacticsSet[tactic] = true
		cur, ok := byTech[tid]
		if !ok {
			cur = &attackTechniqueView{
				TechniqueID: tid, Tactic: tactic, Name: f.Category, MaxSeverity: f.Severity,
			}
			if cur.Name == "" {
				cur.Name = f.Title
			}
			byTech[tid] = cur
		}
		cur.FindingCount++
		if f.Verified {
			cur.VerifiedCount++
		}
		if severityRank(f.Severity) > severityRank(cur.MaxSeverity) {
			cur.MaxSeverity = f.Severity
		}
	}
	tactics := make([]string, 0, len(tacticsSet))
	for k := range tacticsSet {
		tactics = append(tactics, k)
	}
	sort.Strings(tactics)
	techs := make([]attackTechniqueView, 0, len(byTech))
	for _, v := range byTech {
		techs = append(techs, *v)
	}
	sort.Slice(techs, func(i, j int) bool {
		ri, rj := severityRank(techs[i].MaxSeverity), severityRank(techs[j].MaxSeverity)
		if ri != rj {
			return ri > rj
		}
		return techs[i].TechniqueID < techs[j].TechniqueID
	})
	return &attackMatrixView{Tactics: tactics, Techniques: techs}, nil
}

// courseOfAction computes prioritized remediation Courses of Action from active findings.
func (o ops) courseOfAction(ctx context.Context, in *engagementFilter) (*coaView, error) {
	org, err := o.authorize(ctx)
	if err != nil {
		return nil, err
	}
	findings, err := o.s.State.store.ListFindings(ctx, org, strings.TrimSpace(in.EngagementID), "", "")
	if err != nil {
		return nil, zip.Errorf(500, "list findings: %v", err)
	}
	items := make([]coaItemView, 0, len(findings))
	for _, f := range findings {
		if f.Status == "remediated" || f.Status == "false_positive" {
			continue
		}
		priority, reduction, effort := "P2", 45, "medium"
		switch f.Severity {
		case "critical":
			priority, reduction, effort = "P0", 92, "low"
		case "high":
			priority, reduction, effort = "P1", 76, "medium"
		case "low", "info":
			priority, reduction, effort = "P3", 20, "low"
		}
		summary := f.Remediation
		if summary == "" {
			summary = fmt.Sprintf("Patch %s on %s and re-run verbatim proof verification.", f.Title, f.Target)
		}
		items = append(items, coaItemView{
			ID: "coa_" + f.ID, Title: "Remediate: " + f.Title, Priority: priority,
			RiskReduction: reduction, Effort: effort, MitreTechnique: f.MitreTechnique,
			FindingIDs: []string{f.ID}, Summary: summary,
		})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Priority != items[j].Priority {
			return items[i].Priority < items[j].Priority
		}
		return items[i].RiskReduction > items[j].RiskReduction
	})
	return &coaView{Data: items}, nil
}

// hardeningPosture evaluates enclave scope enforcement, verbatim proof coverage, and unresolved critical risk.
func (o ops) hardeningPosture(ctx context.Context, in *engagementFilter) (*hardeningView, error) {
	org, err := o.authorize(ctx)
	if err != nil {
		return nil, err
	}
	engID := strings.TrimSpace(in.EngagementID)
	findings, err := o.s.State.store.ListFindings(ctx, org, engID, "", "")
	if err != nil {
		return nil, zip.Errorf(500, "list findings: %v", err)
	}
	rules, err := o.s.State.store.ListRules(ctx, org, engID)
	if err != nil {
		return nil, zip.Errorf(500, "list rules: %v", err)
	}
	approvals, err := o.s.State.store.ListApprovals(ctx, org, engID, "pending")
	if err != nil {
		return nil, zip.Errorf(500, "list approvals: %v", err)
	}

	verifiedCount, openCrit := 0, 0
	for _, f := range findings {
		if f.Verified {
			verifiedCount++
		}
		if f.Severity == "critical" && f.Status != "remediated" && f.Status != "false_positive" {
			openCrit++
		}
	}
	scopeEnforced := len(rules) > 0
	score := 100
	if !scopeEnforced {
		score -= 20
	}
	score -= openCrit * 25
	if len(approvals) > 0 {
		score -= 10
	}
	if score < 0 {
		score = 0
	}

	checks := []hardeningCheckView{
		{
			ID: "chk_iam", Control: "Hanzo IAM (hanzo.id) JWT & Tenant Isolation", Status: "pass",
			Detail: fmt.Sprintf("Every /v1/cyber operation authenticated via Hanzo IAM and scoped to org %q.", org),
		},
		{
			ID: "chk_scope", Control: "Rules of Engagement Scope & Intercept Guardrails",
			Status: map[bool]string{true: "pass", false: "warn"}[scopeEnforced],
			Detail: fmt.Sprintf("%d active scope/intercept governance rule(s) enforced.", len(rules)),
		},
		{
			ID: "chk_proof", Control: "Deterministic Verbatim Proof Verification",
			Status: map[bool]string{true: "pass", false: "warn"}[len(findings) == 0 || verifiedCount > 0],
			Detail: fmt.Sprintf("%d/%d finding(s) cryptographically/verbatim verified against captured HTTP traffic.", verifiedCount, len(findings)),
		},
		{
			ID: "chk_crit", Control: "Zero Unmitigated Critical Findings",
			Status: map[bool]string{true: "pass", false: "fail"}[openCrit == 0],
			Detail: fmt.Sprintf("%d unmitigated critical finding(s) remaining.", openCrit),
		},
		{
			ID: "chk_kai", Control: "Kai Human-in-the-Loop Risk Gate",
			Status: map[bool]string{true: "pass", false: "warn"}[len(approvals) == 0],
			Detail: fmt.Sprintf("%d high-risk action(s) currently held for operator approval.", len(approvals)),
		},
	}

	return &hardeningView{
		Score:            score,
		ScopeEnforced:    scopeEnforced,
		VerbatimVerified: verifiedCount,
		OpenCritical:     openCrit,
		Checks:           checks,
	}, nil
}
