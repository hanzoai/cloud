package flow

// dag.go — native Go execution engine for hanzo.flow/v1 unified DAG workflows.
//
// Workflows whose data.schema == "hanzo.flow/v1" run natively in pure Go inside
// POST /v1/flow/runs instead of proxying to the Python Langflow runner. Legacy
// Langflow vertex graphs (any other schema or absent schema) stay untouched on
// the upstream passthrough path.
//
// Execution invariants:
//   - Acyclicity is validated via deterministic Kahn's topological sort
//     (mirroring hanzoai/flow/core's sorted ready-queue) before billing or step
//     execution; cyclic or malformed graphs fail fast with HTTP 400.
//   - Tenant-supplied external HTTP URLs ALWAYS dial through hanzoai/outbound
//     so private/loopback/RFC1918/link-local/metadata/NAT64 addresses are
//     refused at socket dial time (including redirects), and caller Hanzo
//     bearer tokens are NEVER forwarded to external URLs.
//   - Platform calls (AI chat completions, Studio/Comfy media, MCP tools,
//     allowlisted /v1/{notify,tel,decisions,base,...} actions) target only the
//     deployment-configured platform base URL and always stamp X-Org-Id from
//     the validated principal.
//   - Recent native run traces are retained in a bounded, org-scoped in-memory
//     ring so GET /v1/flow/runs?workflow={id} surfaces them alongside upstream
//     vertex_builds without ever crossing org boundaries.

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/hanzoai/outbound"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/env"
	"github.com/hanzoai/cloud/sdk/principal"
)

const (
	// unifiedSchema identifies declarative unified DAGs executed natively in Go.
	unifiedSchema = "hanzo.flow/v1"
	// nativeEngineName stamps run traces produced by this Go executor.
	nativeEngineName = "hanzo-flow-go"
	// defaultAIModel is the canonical Hanzo AI router model when a node omits data.model.
	defaultAIModel = "enso-auto"
	// defaultPlatformURL is the canonical Hanzo API origin when no env override is set.
	defaultPlatformURL = "https://api.hanzo.ai"

	maxDAGNodes        = 256
	maxDAGEdges        = 1024
	maxStepBody        = 4 << 20 // 4 MiB cap on AI/HTTP step responses
	maxMediaBody       = 8 << 20 // 8 MiB cap on media step responses
	maxSyncWait        = 2 * time.Second
	maxRunsPerWorkflow = 50
	maxRunWorkflows    = 1000
)

// dagPlatformClient issues requests to deployment-configured Hanzo platform
// services (AI gateway, Studio media, MCP, internal /v1 actions).
var dagPlatformClient = &http.Client{Timeout: timeoutRun}

// dagOutboundClient issues tenant-supplied external HTTP/connector requests.
// It is hanzoai/outbound's SSRF-guarded client, judging the resolved IP at dial
// time on every connection and redirect hop. Package var only so unit tests can
// substitute an httptest server's client on the happy-path test while keeping
// SSRF tests against the real guard.
var dagOutboundClient = outbound.Client(outbound.Options{Timeout: 30 * time.Second, Redirects: 5})

// ── unified graph types ─────────────────────────────────────────────────────

type dagGraph struct {
	Schema    string         `json:"schema"`
	Version   string         `json:"version,omitempty"`
	Name      string         `json:"name,omitempty"`
	Variables map[string]any `json:"variables,omitempty"`
	Nodes     []dagNode      `json:"nodes"`
	Edges     []dagEdge      `json:"edges"`
}

type dagNode struct {
	ID   string         `json:"id"`
	Type string         `json:"type,omitempty"`
	Kind string         `json:"kind,omitempty"`
	Data map[string]any `json:"data,omitempty"`
}

type dagEdge struct {
	ID           string `json:"id,omitempty"`
	Source       string `json:"source,omitempty"`
	Target       string `json:"target,omitempty"`
	From         string `json:"from,omitempty"`
	To           string `json:"to,omitempty"`
	SourceHandle string `json:"sourceHandle,omitempty"`
	TargetHandle string `json:"targetHandle,omitempty"`
	Condition    string `json:"condition,omitempty"`
	Branch       string `json:"branch,omitempty"`
	Label        string `json:"label,omitempty"`
}

func (e dagEdge) src() string { return strings.TrimSpace(cmp.Or(e.Source, e.From)) }
func (e dagEdge) dst() string { return strings.TrimSpace(cmp.Or(e.Target, e.To)) }
func (e dagEdge) branchTag() string {
	return strings.TrimSpace(cmp.Or(e.SourceHandle, e.Condition, e.Branch, e.Label))
}

func (n dagNode) resolvedKind() string {
	if n.Data != nil {
		if k := strVal(n.Data, "kind"); k != "" {
			return strings.ToLower(k)
		}
	}
	if k := strings.TrimSpace(n.Kind); k != "" {
		return strings.ToLower(k)
	}
	if t := strings.ToLower(strings.TrimSpace(n.Type)); t != "" &&
		t != "default" && t != "custom" && t != "flownode" && t != "hanzonode" && t != "genericnode" {
		return t
	}
	if n.Data != nil {
		if k := cmp.Or(strVal(n.Data, "nodeType"), strVal(n.Data, "type")); k != "" {
			return strings.ToLower(k)
		}
	}
	return "noop"
}

func (n dagNode) label() string {
	if n.Data != nil {
		if l := cmp.Or(strVal(n.Data, "label"), strVal(n.Data, "name"), strVal(n.Data, "title")); l != "" {
			return l
		}
	}
	return n.ID
}

// ── run trace shapes ────────────────────────────────────────────────────────

type dagStepTrace struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	Label     string `json:"label,omitempty"`
	Status    string `json:"status"` // completed | skipped | failed
	LatencyMs int64  `json:"latencyMs"`
	Branch    string `json:"branch,omitempty"`
	Output    any    `json:"output,omitempty"`
	Error     string `json:"error,omitempty"`
}

type dagRunOutput struct {
	NodeID  string         `json:"node_id"`
	Kind    string         `json:"kind,omitempty"`
	Text    string         `json:"text,omitempty"`
	Output  any            `json:"output,omitempty"`
	Results map[string]any `json:"results,omitempty"`
}

type dagRunResponse struct {
	RunID          string         `json:"run_id"`
	WorkflowID     string         `json:"workflow_id"`
	SessionID      string         `json:"session_id"`
	Status         string         `json:"status"` // completed | failed
	Engine         string         `json:"engine"`
	Schema         string         `json:"schema"`
	Steps          []dagStepTrace `json:"steps"`
	Outputs        []dagRunOutput `json:"outputs"`
	Summary        string         `json:"summary,omitempty"`
	Variables      map[string]any `json:"variables,omitempty"`
	TotalLatencyMs int64          `json:"totalLatencyMs"`
	StartedAt      string         `json:"startedAt"`
	CompletedAt    string         `json:"completedAt"`
}

// ── bounded per-org run trace store ─────────────────────────────────────────

type dagRunStore struct {
	mu     sync.RWMutex
	byKey  map[string][]dagRunResponse // org+"\x00"+workflowID → oldest..newest
	keySeq []string                    // insertion order for bounded eviction
}

func newDAGRunStore() *dagRunStore {
	return &dagRunStore{byKey: map[string][]dagRunResponse{}}
}

func dagRunKey(org, workflowID string) string { return org + "\x00" + workflowID }

func (s *dagRunStore) record(org, workflowID string, run dagRunResponse) {
	if s == nil || org == "" || workflowID == "" {
		return
	}
	k := dagRunKey(org, workflowID)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.byKey == nil {
		s.byKey = map[string][]dagRunResponse{}
	}
	if _, exists := s.byKey[k]; !exists {
		if len(s.keySeq) >= maxRunWorkflows {
			evict := s.keySeq[0]
			s.keySeq = s.keySeq[1:]
			delete(s.byKey, evict)
		}
		s.keySeq = append(s.keySeq, k)
	}
	ring := append(s.byKey[k], run)
	if len(ring) > maxRunsPerWorkflow {
		ring = append([]dagRunResponse(nil), ring[len(ring)-maxRunsPerWorkflow:]...)
	}
	s.byKey[k] = ring
}

func (s *dagRunStore) list(org, workflowID string) []dagRunResponse {
	if s == nil || org == "" || workflowID == "" {
		return nil
	}
	k := dagRunKey(org, workflowID)
	s.mu.RLock()
	defer s.mu.RUnlock()
	src := s.byKey[k]
	if len(src) == 0 {
		return nil
	}
	out := make([]dagRunResponse, len(src))
	// Return newest-first so callers see the latest execution at index 0.
	for i := range src {
		out[i] = src[len(src)-1-i]
	}
	return out
}

func (s *dagRunStore) delete(org, workflowID string) {
	if s == nil || org == "" || workflowID == "" {
		return
	}
	k := dagRunKey(org, workflowID)
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.byKey, k)
	for i, existing := range s.keySeq {
		if existing == k {
			s.keySeq = append(s.keySeq[:i], s.keySeq[i+1:]...)
			break
		}
	}
}

// ── schema detection & Kahn's topological sort ──────────────────────────────

// parseUnifiedGraph inspects an upstream workflow record (from GET /v1/flows/{id})
// and returns the parsed hanzo.flow/v1 graph when data.schema == "hanzo.flow/v1".
// Legacy Langflow records return (nil, false, nil) so they keep proxying verbatim.
func parseUnifiedGraph(raw json.RawMessage) (*dagGraph, bool, error) {
	if len(raw) == 0 {
		return nil, false, nil
	}
	var rec struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(raw, &rec); err != nil || len(rec.Data) == 0 {
		return nil, false, nil
	}
	dataBytes := bytes.TrimSpace(rec.Data)
	if len(dataBytes) == 0 || bytes.Equal(dataBytes, []byte("null")) {
		return nil, false, nil
	}
	// Tolerate double-encoded JSON string payloads.
	if dataBytes[0] == '"' {
		var unquoted string
		if err := json.Unmarshal(dataBytes, &unquoted); err != nil {
			return nil, false, nil
		}
		dataBytes = bytes.TrimSpace([]byte(unquoted))
	}
	if len(dataBytes) == 0 || dataBytes[0] != '{' {
		return nil, false, nil
	}
	var probe struct {
		Schema string `json:"schema"`
	}
	if err := json.Unmarshal(dataBytes, &probe); err != nil {
		return nil, false, nil
	}
	if probe.Schema != unifiedSchema {
		return nil, false, nil
	}
	var g dagGraph
	if err := json.Unmarshal(dataBytes, &g); err != nil {
		return nil, true, fmt.Errorf("invalid %s graph: %w", unifiedSchema, err)
	}
	return &g, true, nil
}

type compiledDAG struct {
	order    []string
	nodes    map[string]dagNode
	outgoing map[string][]dagEdge
	incoming map[string][]dagEdge
}

// compileDAG validates node/edge invariants and computes a deterministic
// topological ordering via Kahn's algorithm (mirroring hanzoai/flow/core's
// sorted ready-queue). Any cycle or dangling edge is refused as HTTP 400.
func compileDAG(g *dagGraph) (*compiledDAG, error) {
	if g == nil {
		return nil, zip.ErrBadRequest("empty workflow graph")
	}
	if len(g.Nodes) > maxDAGNodes {
		return nil, zip.Errorf(http.StatusBadRequest, "workflow exceeds maximum of %d nodes", maxDAGNodes)
	}
	if len(g.Edges) > maxDAGEdges {
		return nil, zip.Errorf(http.StatusBadRequest, "workflow exceeds maximum of %d edges", maxDAGEdges)
	}

	nodes := make(map[string]dagNode, len(g.Nodes))
	indeg := make(map[string]int, len(g.Nodes))
	for _, n := range g.Nodes {
		id := strings.TrimSpace(n.ID)
		if id == "" {
			return nil, zip.ErrBadRequest("flow node has empty id")
		}
		if _, dup := nodes[id]; dup {
			return nil, zip.Errorf(http.StatusBadRequest, "duplicate node id %q", id)
		}
		n.ID = id
		nodes[id] = n
		indeg[id] = 0
	}

	outgoing := make(map[string][]dagEdge, len(g.Nodes))
	incoming := make(map[string][]dagEdge, len(g.Nodes))
	for _, e := range g.Edges {
		src, dst := e.src(), e.dst()
		if src == "" || dst == "" {
			return nil, zip.ErrBadRequest("flow edge requires source and target")
		}
		if _, ok := nodes[src]; !ok {
			return nil, zip.Errorf(http.StatusBadRequest, "flow edge references unknown source node %q", src)
		}
		if _, ok := nodes[dst]; !ok {
			return nil, zip.Errorf(http.StatusBadRequest, "flow edge references unknown target node %q", dst)
		}
		if src == dst {
			return nil, zip.Errorf(http.StatusBadRequest, "flow graph has a self-loop cycle on node %q", src)
		}
		outgoing[src] = append(outgoing[src], e)
		incoming[dst] = append(incoming[dst], e)
		indeg[dst]++
	}

	// Kahn's algorithm with deterministic lexicographical tie-breaking
	// (mirrors hanzoai/flow/core/flow.go topoOrder + schedule.go).
	var ready []string
	for id, d := range indeg {
		if d == 0 {
			ready = append(ready, id)
		}
	}
	sort.Strings(ready)

	order := make([]string, 0, len(nodes))
	for len(ready) > 0 {
		id := ready[0]
		ready = ready[1:]
		order = append(order, id)
		for _, e := range outgoing[id] {
			dst := e.dst()
			indeg[dst]--
			if indeg[dst] == 0 {
				i := sort.SearchStrings(ready, dst)
				ready = append(ready, "")
				copy(ready[i+1:], ready[i:])
				ready[i] = dst
			}
		}
	}

	if len(order) != len(nodes) {
		return nil, zip.Errorf(http.StatusBadRequest,
			"flow graph has a cycle (%d of %d nodes scheduled)", len(order), len(nodes))
	}

	return &compiledDAG{
		order:    order,
		nodes:    nodes,
		outgoing: outgoing,
		incoming: incoming,
	}, nil
}

// ── unified DAG execution ───────────────────────────────────────────────────

type dagExec struct {
	ctx       context.Context
	org       string
	user      string
	project   string
	bearer    string
	workflow  string
	sessionID string
	runID     string
	input     string
	payload   any
	tweaks    map[string]any
	vars      map[string]any
	outputs   map[string]any
	lastText  string
	lastOut   any
}

// callerAuth reads the caller's validated user, project, and relayable bearer
// token for Hanzo platform calls. Never forwarded to third-party external URLs.
func callerAuth(ctx context.Context) (user, project, bearer string) {
	c, ok := cloud.Request(ctx)
	if !ok {
		return "", principal.DefaultProject, ""
	}
	user = c.User()
	project = principal.Project(c)
	if jwt := cloud.CallerBearer(c); jwt != "" {
		return user, project, jwt
	}
	for _, hdr := range []string{"Authorization", "X-Authorization"} {
		v := strings.TrimSpace(c.Header(hdr))
		if len(v) > 7 && strings.EqualFold(v[:7], "Bearer ") {
			if tok := strings.TrimSpace(v[7:]); tok != "" {
				return user, project, tok
			}
		}
	}
	return user, project, ""
}

func (o ops) executeUnified(ctx context.Context, org, workflowID string, g *dagGraph, compiled *compiledDAG, in *flowRun) (dagRunResponse, error) {
	started := time.Now().UTC()
	runID := uuid.NewString()
	sessionID := strings.TrimSpace(in.Session)
	if sessionID == "" {
		sessionID = uuid.NewString()
	}

	user, project, bearer := callerAuth(ctx)

	var payload any
	trimmedIn := strings.TrimSpace(in.Input)
	if trimmedIn != "" && (trimmedIn[0] == '{' || trimmedIn[0] == '[') {
		var parsed any
		if json.Unmarshal([]byte(trimmedIn), &parsed) == nil {
			payload = parsed
		}
	}

	var tweaks map[string]any
	if len(in.Tweaks) > 0 {
		_ = json.Unmarshal(in.Tweaks, &tweaks)
	}

	vars := make(map[string]any, len(g.Variables))
	for k, v := range g.Variables {
		vars[k] = v
	}

	exec := &dagExec{
		ctx:       ctx,
		org:       org,
		user:      user,
		project:   project,
		bearer:    bearer,
		workflow:  workflowID,
		sessionID: sessionID,
		runID:     runID,
		input:     in.Input,
		payload:   payload,
		tweaks:    tweaks,
		vars:      vars,
		outputs:   make(map[string]any, len(compiled.order)),
		lastText:  in.Input,
	}

	// Track which directed edges are active after branch/condition routing.
	type edgeKey struct {
		src, dst, tag string
	}
	activeEdges := make(map[edgeKey]bool, len(g.Edges))
	steps := make([]dagStepTrace, 0, len(compiled.order))
	var explicitOutputs []dagRunOutput
	var summary string

	for _, id := range compiled.order {
		if err := ctx.Err(); err != nil {
			res := finalizeRun(runID, workflowID, sessionID, "failed", steps, explicitOutputs, summary, exec.vars, started)
			return res, zip.Errorf(http.StatusGatewayTimeout, "flow run cancelled or timed out")
		}

		node := compiled.nodes[id]
		kind := node.resolvedKind()
		label := node.label()

		// A non-root node runs iff at least one incoming edge is active.
		inEdges := compiled.incoming[id]
		if len(inEdges) > 0 {
			activated := false
			for _, ie := range inEdges {
				if activeEdges[edgeKey{src: ie.src(), dst: ie.dst(), tag: ie.branchTag()}] {
					activated = true
					break
				}
			}
			if !activated {
				steps = append(steps, dagStepTrace{
					ID:        id,
					Kind:      kind,
					Label:     label,
					Status:    "skipped",
					LatencyMs: 0,
				})
				continue
			}
		}

		// Merge per-node tweaks (if any) without mutating the compiled graph.
		cfg := mergeNodeData(node.Data, exec.tweaks[id])
		continueOnErr := boolVal(cfg, "continueOnError")

		stepStart := time.Now()
		stepOut, branch, matched, stepErr := exec.runStep(node, kind, cfg, inEdges)
		elapsedMs := time.Since(stepStart).Milliseconds()

		if stepErr != nil {
			steps = append(steps, dagStepTrace{
				ID:        id,
				Kind:      kind,
				Label:     label,
				Status:    "failed",
				LatencyMs: elapsedMs,
				Error:     stepErr.Error(),
			})
			if continueOnErr {
				exec.outputs[id] = map[string]any{"error": stepErr.Error(), "ok": false}
				for _, oe := range compiled.outgoing[id] {
					activeEdges[edgeKey{src: oe.src(), dst: oe.dst(), tag: oe.branchTag()}] = true
				}
				continue
			}
			res := finalizeRun(runID, workflowID, sessionID, "failed", steps, explicitOutputs, summary, exec.vars, started)
			return res, classifyStepError(id, stepErr)
		}

		exec.outputs[id] = stepOut
		exec.lastOut = stepOut
		if txt := extractOutputText(stepOut); txt != "" {
			exec.lastText = txt
		}

		trace := dagStepTrace{
			ID:        id,
			Kind:      kind,
			Label:     label,
			Status:    "completed",
			LatencyMs: elapsedMs,
			Branch:    branch,
			Output:    stepOut,
		}
		steps = append(steps, trace)

		if kind == "output" || kind == "end" || kind == "result" || kind == "response" || kind == "sink" {
			txt := extractOutputText(stepOut)
			explicitOutputs = append(explicitOutputs, dagRunOutput{
				NodeID: id,
				Kind:   kind,
				Text:   txt,
				Output: stepOut,
				Results: map[string]any{
					"message": map[string]any{"text": txt},
				},
			})
			if txt != "" {
				summary = txt
			}
		}

		// Activate matching outgoing edges.
		isBranch := isConditionKind(kind)
		for _, oe := range compiled.outgoing[id] {
			if !isBranch || edgeMatchesBranch(oe, matched, branch) {
				activeEdges[edgeKey{src: oe.src(), dst: oe.dst(), tag: oe.branchTag()}] = true
			}
		}
	}

	// If the graph had no explicit output node, expose leaf step outputs so callers
	// always receive structured outputs + summary.
	if len(explicitOutputs) == 0 {
		for _, s := range steps {
			if s.Status != "completed" {
				continue
			}
			if len(compiled.outgoing[s.ID]) == 0 {
				txt := extractOutputText(s.Output)
				explicitOutputs = append(explicitOutputs, dagRunOutput{
					NodeID: s.ID,
					Kind:   s.Kind,
					Text:   txt,
					Output: s.Output,
					Results: map[string]any{
						"message": map[string]any{"text": txt},
					},
				})
				if txt != "" {
					summary = txt
				}
			}
		}
	}
	if summary == "" {
		summary = exec.lastText
	}

	res := finalizeRun(runID, workflowID, sessionID, "completed", steps, explicitOutputs, summary, exec.vars, started)
	return res, nil
}

func finalizeRun(runID, workflowID, sessionID, status string, steps []dagStepTrace, outputs []dagRunOutput, summary string, vars map[string]any, started time.Time) dagRunResponse {
	completed := time.Now().UTC()
	if outputs == nil {
		outputs = []dagRunOutput{}
	}
	var varsCopy map[string]any
	if len(vars) > 0 {
		varsCopy = make(map[string]any, len(vars))
		for k, v := range vars {
			varsCopy[k] = v
		}
	}
	return dagRunResponse{
		RunID:          runID,
		WorkflowID:     workflowID,
		SessionID:      sessionID,
		Status:         status,
		Engine:         nativeEngineName,
		Schema:         unifiedSchema,
		Steps:          steps,
		Outputs:        outputs,
		Summary:        summary,
		Variables:      varsCopy,
		TotalLatencyMs: time.Since(started).Milliseconds(),
		StartedAt:      started.Format(time.RFC3339Nano),
		CompletedAt:    completed.Format(time.RFC3339Nano),
	}
}

// stepClientError marks an error caused by invalid workflow configuration or a
// refused outbound target (maps to HTTP 400 rather than 502).
type stepClientError struct{ err error }

func (e stepClientError) Error() string { return e.err.Error() }
func (e stepClientError) Unwrap() error { return e.err }

func classifyStepError(nodeID string, err error) error {
	if errors.Is(err, outbound.ErrRefused) {
		return zip.Errorf(http.StatusBadRequest, "step %s: %v", nodeID, err)
	}
	var ce stepClientError
	if errors.As(err, &ce) {
		return zip.Errorf(http.StatusBadRequest, "step %s: %v", nodeID, ce.err)
	}
	return zip.Errorf(http.StatusBadGateway, "step %s failed: %v", nodeID, err)
}

func mergeNodeData(base map[string]any, tweak any) map[string]any {
	out := make(map[string]any, len(base))
	for k, v := range base {
		out[k] = v
	}
	if tm, ok := tweak.(map[string]any); ok {
		for k, v := range tm {
			out[k] = v
		}
	}
	return out
}

func isConditionKind(kind string) bool {
	switch kind {
	case "condition", "branch", "if", "switch", "router", "filter", "gate":
		return true
	}
	return false
}

// ── step dispatcher ─────────────────────────────────────────────────────────

func (e *dagExec) runStep(node dagNode, kind string, rawCfg map[string]any, inEdges []dagEdge) (out any, branch string, matched bool, err error) {
	// Resolve {{...}} templates across the node's config. Condition nodes keep
	// raw expressions too so variable names can be inspected cleanly.
	cfg, _ := e.resolveValue(rawCfg).(map[string]any)
	if cfg == nil {
		cfg = map[string]any{}
	}

	switch kind {
	case "trigger", "start", "input", "webhook", "schedule", "cron", "manual":
		return e.runTrigger(cfg), "", true, nil

	case "wait", "delay", "sleep", "timer", "pause":
		o, err := e.runWait(cfg)
		return o, "", true, err

	case "set", "setvar", "set_var", "var", "variables", "assign", "transform", "map", "template":
		return e.runSet(cfg), "", true, nil

	case "condition", "branch", "if", "switch", "router", "filter", "gate":
		o, br, ok := e.runCondition(rawCfg, cfg)
		return o, br, ok, nil

	case "agent", "llm", "ai", "chat", "enso", "zen", "completion", "prompt", "model":
		o, err := e.runAgent(cfg)
		return o, "", true, err

	case "kai", "decision", "decisions":
		if strVal(cfg, "endpoint") == "/v1/decisions" || cfg["choices"] != nil || cfg["rubric"] != nil || cfg["decision"] != nil {
			o, err := e.runPlatformAction("/v1/decisions", cfg)
			return o, "", true, err
		}
		o, err := e.runAgent(cfg)
		return o, "", true, err

	case "mcp", "tool", "mcp_tool", "function":
		o, err := e.runMCPTool(cfg)
		return o, "", true, err

	case "http", "connector", "action", "request", "webhook_out", "api", "notify", "tel", "base":
		o, err := e.runHTTPOrAction(kind, cfg)
		return o, "", true, err

	case "media", "comfy", "studio", "image", "video", "audio", "gpu", "render":
		o, err := e.runMedia(kind, cfg)
		return o, "", true, err

	case "output", "end", "result", "response", "sink":
		return e.runOutput(cfg, inEdges), "", true, nil

	default:
		// Unknown/pass-through node: preserve resolved config/payload cleanly.
		return map[string]any{
			"kind":   kind,
			"input":  e.lastOut,
			"config": cfg,
		}, "", true, nil
	}
}

// ── trigger / wait ──────────────────────────────────────────────────────────

func (e *dagExec) runTrigger(cfg map[string]any) map[string]any {
	out := map[string]any{
		"triggered": true,
		"input":     e.input,
	}
	mergedPayload := map[string]any{}
	if defaults, ok := cfg["defaults"].(map[string]any); ok {
		for k, v := range defaults {
			mergedPayload[k] = v
		}
	}
	if cfgPayload, ok := cfg["payload"].(map[string]any); ok {
		for k, v := range cfgPayload {
			mergedPayload[k] = v
		}
	}
	if pm, ok := e.payload.(map[string]any); ok {
		for k, v := range pm {
			mergedPayload[k] = v
		}
	}
	if len(mergedPayload) > 0 {
		out["payload"] = mergedPayload
		for k, v := range mergedPayload {
			if _, exists := out[k]; !exists {
				out[k] = v
			}
			if _, exists := e.vars[k]; !exists {
				e.vars[k] = v
			}
		}
	} else if e.payload != nil {
		out["payload"] = e.payload
	} else if e.input != "" {
		out["text"] = e.input
	}
	for _, meta := range []string{"schedule", "cron", "event", "triggerType", "source", "timezone"} {
		if v := strVal(cfg, meta); v != "" {
			out[meta] = v
		}
	}
	return out
}

func (e *dagExec) runWait(cfg map[string]any) (map[string]any, error) {
	schedule := cmp.Or(strVal(cfg, "schedule"), strVal(cfg, "cron"))
	until := strVal(cfg, "until")
	reason := strVal(cfg, "reason")

	delayMs := int64(numVal(cfg, "delayMs", 0))
	if delayMs == 0 {
		delayMs = int64(numVal(cfg, "durationMs", 0))
	}
	if delayMs == 0 {
		if secs := numVal(cfg, "seconds", 0); secs > 0 {
			delayMs = int64(secs * 1000)
		}
	}

	out := map[string]any{
		"waited": true,
	}
	if schedule != "" {
		out["schedule"] = schedule
	}
	if until != "" {
		out["until"] = until
	}
	if reason != "" {
		out["reason"] = reason
	}
	if delayMs > 0 {
		out["delayMs"] = delayMs
	}

	// Only sleep synchronously for short bounded delays; longer waits or cron
	// schedules evaluate as declarative metadata without stalling the HTTP run.
	if delayMs > 0 && schedule == "" && until == "" {
		d := time.Duration(delayMs) * time.Millisecond
		if d <= maxSyncWait {
			timer := time.NewTimer(d)
			select {
			case <-timer.C:
				out["mode"] = "sync"
			case <-e.ctx.Done():
				timer.Stop()
				return nil, e.ctx.Err()
			}
		} else {
			out["waited"] = false
			out["scheduled"] = true
			out["mode"] = "metadata"
		}
	} else {
		out["mode"] = "metadata"
	}
	return out, nil
}

// ── set / transform ─────────────────────────────────────────────────────────

func (e *dagExec) runSet(cfg map[string]any) map[string]any {
	out := map[string]any{}
	assign := func(k string, v any) {
		k = strings.TrimSpace(k)
		if k == "" {
			return
		}
		e.vars[k] = v
		out[k] = v
	}

	for _, mapKey := range []string{"variables", "values", "set", "fields"} {
		if m, ok := cfg[mapKey].(map[string]any); ok {
			for k, v := range m {
				assign(k, v)
			}
		}
	}
	if list, ok := cfg["assignments"].([]any); ok {
		for _, item := range list {
			if m, ok := item.(map[string]any); ok {
				k := cmp.Or(strVal(m, "key"), strVal(m, "name"))
				if v, exists := m["value"]; exists {
					assign(k, v)
				}
			}
		}
	}
	if k := cmp.Or(strVal(cfg, "key"), strVal(cfg, "name")); k != "" {
		if v, exists := cfg["value"]; exists {
			assign(k, v)
		}
	}
	if tmpl, ok := cfg["template"].(string); ok && tmpl != "" {
		target := cmp.Or(strVal(cfg, "outputKey"), "text")
		assign(target, tmpl)
	}
	return out
}

// ── condition / branch evaluator ────────────────────────────────────────────

func (e *dagExec) runCondition(rawCfg, cfg map[string]any) (map[string]any, string, bool) {
	// Switch mode: route directly by resolved value when kind/mode uses cases.
	if swKey := cmp.Or(strVal(cfg, "switchOn"), strVal(cfg, "branchField")); swKey != "" {
		val := e.resolveOperand(rawCfg["switchOn"], cfg["switchOn"])
		if val == nil {
			val = e.resolveOperand(rawCfg["branchField"], cfg["branchField"])
		}
		branch := strings.TrimSpace(stringify(val))
		matched := isTruthy(val)
		return map[string]any{
			"matched": matched,
			"result":  matched,
			"branch":  branch,
			"value":   val,
		}, branch, matched
	}

	// Expression mode: "score >= 80 && tier == 'enterprise'".
	if rawExpr := cmp.Or(strVal(rawCfg, "expression"), strVal(rawCfg, "condition")); rawExpr != "" {
		matched := e.evalExpression(rawExpr)
		branch := branchName(cfg, matched)
		return map[string]any{
			"matched":    matched,
			"result":     matched,
			"branch":     branch,
			"expression": rawExpr,
		}, branch, matched
	}

	// Structured comparison mode: left / operator / right.
	op := strings.ToLower(strings.TrimSpace(cmp.Or(strVal(cfg, "operator"), strVal(cfg, "op"))))
	if op == "" {
		op = "truthy"
	}

	var left any
	switch {
	case rawCfg["field"] != nil:
		left = e.resolveFieldRef(strVal(cfg, "field"))
	case rawCfg["variable"] != nil:
		left = e.resolveFieldRef(strVal(cfg, "variable"))
	case rawCfg["left"] != nil:
		left = e.resolveOperand(rawCfg["left"], cfg["left"])
	case rawCfg["value"] != nil:
		left = e.resolveOperand(rawCfg["value"], cfg["value"])
	default:
		left = e.lastOut
	}

	var right any
	for _, rk := range []string{"right", "compare", "expected", "target"} {
		if rawCfg[rk] != nil {
			right = e.resolveOperand(rawCfg[rk], cfg[rk])
			break
		}
	}

	matched := compareValues(left, op, right, boolVal(cfg, "caseInsensitive"))
	branch := branchName(cfg, matched)
	return map[string]any{
		"matched":  matched,
		"result":   matched,
		"branch":   branch,
		"operator": op,
		"left":     left,
		"right":    right,
	}, branch, matched
}

func branchName(cfg map[string]any, matched bool) string {
	if matched {
		return cmp.Or(strVal(cfg, "trueBranch"), "true")
	}
	return cmp.Or(strVal(cfg, "falseBranch"), "false")
}

func edgeMatchesBranch(edge dagEdge, matched bool, branch string) bool {
	tag := strings.ToLower(strings.TrimSpace(edge.branchTag()))
	if tag == "" || tag == "default" || tag == "out" || tag == "output" || tag == "next" {
		return true
	}
	if branch != "" && tag == strings.ToLower(strings.TrimSpace(branch)) {
		return true
	}
	switch tag {
	case "true", "yes", "pass", "match", "then", "ontrue", "1":
		return matched
	case "false", "no", "fail", "else", "otherwise", "onfalse", "0":
		return !matched
	default:
		return false
	}
}

func (e *dagExec) resolveFieldRef(name string) any {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil
	}
	if v, ok := e.lookupPath(name); ok {
		return v
	}
	return name
}

func (e *dagExec) resolveOperand(raw, resolved any) any {
	// If raw was a {{...}} template, resolved already holds the typed value.
	if s, ok := raw.(string); ok {
		trimmed := strings.TrimSpace(s)
		if !strings.Contains(trimmed, "{{") {
			// Bare variable/step path (e.g. "score" or "vars.tier") resolves if known.
			if v, found := e.lookupPath(trimmed); found {
				return v
			}
		}
	}
	return resolved
}

// evalExpression evaluates a boolean expression with ||, &&, !, and comparisons
// (==, !=, >=, <=, >, <, contains, not_contains, truthy).
func (e *dagExec) evalExpression(expr string) bool {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return false
	}
	// Lowest precedence: logical OR.
	if parts := splitTopLevel(expr, "||"); len(parts) > 1 {
		for _, p := range parts {
			if e.evalExpression(p) {
				return true
			}
		}
		return false
	}
	// Next precedence: logical AND.
	if parts := splitTopLevel(expr, "&&"); len(parts) > 1 {
		for _, p := range parts {
			if !e.evalExpression(p) {
				return false
			}
		}
		return true
	}
	// Strip matching outer parentheses.
	if strings.HasPrefix(expr, "(") && strings.HasSuffix(expr, ")") && parensBalanced(expr[1:len(expr)-1]) {
		return e.evalExpression(expr[1 : len(expr)-1])
	}
	// Unary negation (avoid matching "!=").
	if strings.HasPrefix(expr, "!") && !strings.HasPrefix(expr, "!=") {
		return !e.evalExpression(expr[1:])
	}

	// Binary comparison operators, longest match first.
	for _, op := range []string{"===", "!==", ">=", "<=", "==", "!=", ">", "<", " not_contains ", " contains "} {
		if lhs, rhs, ok := splitTopLevelOnce(expr, op); ok {
			lv := e.parseExprToken(lhs)
			rv := e.parseExprToken(rhs)
			return compareValues(lv, strings.TrimSpace(op), rv, false)
		}
	}

	return isTruthy(e.parseExprToken(expr))
}

func (e *dagExec) parseExprToken(tok string) any {
	tok = strings.TrimSpace(tok)
	if tok == "" {
		return ""
	}
	// Single {{path}} template.
	if m := dagWholeRefRE.FindStringSubmatch(tok); m != nil {
		if v, ok := e.lookupPath(m[1]); ok {
			return v
		}
		return nil
	}
	// Quoted string literal (single or double quotes), with embedded {{...}} resolved.
	if len(tok) >= 2 && ((tok[0] == '"' && tok[len(tok)-1] == '"') || (tok[0] == '\'' && tok[len(tok)-1] == '\'')) {
		return e.resolveString(tok[1 : len(tok)-1])
	}
	// Embedded template string.
	if strings.Contains(tok, "{{") {
		return e.resolveString(tok)
	}
	switch strings.ToLower(tok) {
	case "true":
		return true
	case "false":
		return false
	case "null", "nil":
		return nil
	}
	if f, err := strconv.ParseFloat(tok, 64); err == nil {
		return f
	}
	if v, ok := e.lookupPath(tok); ok {
		return v
	}
	return tok
}

func compareValues(left any, op string, right any, caseInsensitive bool) bool {
	switch strings.ToLower(strings.TrimSpace(op)) {
	case "truthy", "exists":
		return isTruthy(left)
	case "falsy", "empty", "not_exists":
		return !isTruthy(left)
	case "==", "===", "eq", "equals":
		return valuesEqual(left, right, caseInsensitive)
	case "!=", "!==", "ne", "neq", "not_equals":
		return !valuesEqual(left, right, caseInsensitive)
	case ">", "gt":
		return compareOrdered(left, right) > 0
	case ">=", "gte":
		return compareOrdered(left, right) >= 0
	case "<", "lt":
		return compareOrdered(left, right) < 0
	case "<=", "lte":
		return compareOrdered(left, right) <= 0
	case "contains":
		return valueContains(left, right, caseInsensitive)
	case "not_contains":
		return !valueContains(left, right, caseInsensitive)
	case "startswith", "starts_with":
		ls, rs := stringify(left), stringify(right)
		if caseInsensitive {
			ls, rs = strings.ToLower(ls), strings.ToLower(rs)
		}
		return strings.HasPrefix(ls, rs)
	case "endswith", "ends_with":
		ls, rs := stringify(left), stringify(right)
		if caseInsensitive {
			ls, rs = strings.ToLower(ls), strings.ToLower(rs)
		}
		return strings.HasSuffix(ls, rs)
	default:
		return isTruthy(left)
	}
}

func isTruthy(v any) bool {
	if v == nil {
		return false
	}
	switch t := v.(type) {
	case bool:
		return t
	case string:
		s := strings.ToLower(strings.TrimSpace(t))
		return s != "" && s != "false" && s != "0" && s != "null" && s != "nil"
	case float64:
		return t != 0 && !math.IsNaN(t)
	case int:
		return t != 0
	case int64:
		return t != 0
	case json.Number:
		f, err := t.Float64()
		return err == nil && f != 0
	case []any:
		return len(t) > 0
	case map[string]any:
		return len(t) > 0
	default:
		return true
	}
}

func asFloat(v any) (float64, bool) {
	switch t := v.(type) {
	case float64:
		return t, !math.IsNaN(t)
	case float32:
		return float64(t), !math.IsNaN(float64(t))
	case int:
		return float64(t), true
	case int64:
		return float64(t), true
	case json.Number:
		f, err := t.Float64()
		return f, err == nil && !math.IsNaN(f)
	case string:
		s := strings.TrimSpace(t)
		if s == "" {
			return 0, false
		}
		f, err := strconv.ParseFloat(s, 64)
		return f, err == nil && !math.IsNaN(f)
	default:
		return 0, false
	}
}

func valuesEqual(a, b any, caseInsensitive bool) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	if ab, ok1 := a.(bool); ok1 {
		if bb, ok2 := b.(bool); ok2 {
			return ab == bb
		}
	}
	if af, ok1 := asFloat(a); ok1 {
		if bf, ok2 := asFloat(b); ok2 {
			return af == bf
		}
	}
	as, bs := stringify(a), stringify(b)
	if caseInsensitive {
		return strings.EqualFold(as, bs)
	}
	return as == bs
}

func compareOrdered(a, b any) int {
	if af, ok1 := asFloat(a); ok1 {
		if bf, ok2 := asFloat(b); ok2 {
			return cmp.Compare(af, bf)
		}
	}
	return strings.Compare(stringify(a), stringify(b))
}

func valueContains(left, right any, caseInsensitive bool) bool {
	switch t := left.(type) {
	case []any:
		for _, item := range t {
			if valuesEqual(item, right, caseInsensitive) {
				return true
			}
		}
		return false
	case map[string]any:
		key := stringify(right)
		if !caseInsensitive {
			_, ok := t[key]
			return ok
		}
		for k := range t {
			if strings.EqualFold(k, key) {
				return true
			}
		}
		return false
	default:
		ls, rs := stringify(left), stringify(right)
		if caseInsensitive {
			ls, rs = strings.ToLower(ls), strings.ToLower(rs)
		}
		return strings.Contains(ls, rs)
	}
}

func splitTopLevel(s, sep string) []string {
	var parts []string
	var quote byte
	depth := 0
	start := 0
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if quote != 0 {
			if ch == '\\' && i+1 < len(s) {
				i++
				continue
			}
			if ch == quote {
				quote = 0
			}
			continue
		}
		switch ch {
		case '"', '\'':
			quote = ch
		case '(':
			depth++
		case ')':
			if depth > 0 {
				depth--
			}
		default:
			if depth == 0 && strings.HasPrefix(s[i:], sep) {
				parts = append(parts, s[start:i])
				i += len(sep) - 1
				start = i + 1
			}
		}
	}
	parts = append(parts, s[start:])
	return parts
}

func splitTopLevelOnce(s, sep string) (string, string, bool) {
	var quote byte
	depth := 0
	lowerS := strings.ToLower(s)
	lowerSep := strings.ToLower(sep)
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if quote != 0 {
			if ch == '\\' && i+1 < len(s) {
				i++
				continue
			}
			if ch == quote {
				quote = 0
			}
			continue
		}
		switch ch {
		case '"', '\'':
			quote = ch
		case '(':
			depth++
		case ')':
			if depth > 0 {
				depth--
			}
		default:
			if depth == 0 && strings.HasPrefix(lowerS[i:], lowerSep) {
				return s[:i], s[i+len(sep):], true
			}
		}
	}
	return "", "", false
}

func parensBalanced(s string) bool {
	depth := 0
	var quote byte
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if quote != 0 {
			if ch == quote {
				quote = 0
			}
			continue
		}
		switch ch {
		case '"', '\'':
			quote = ch
		case '(':
			depth++
		case ')':
			depth--
			if depth < 0 {
				return false
			}
		}
	}
	return depth == 0
}

// ── AI / LLM step ───────────────────────────────────────────────────────────

func normalizePlatformBase(raw string) string {
	b := strings.TrimRight(strings.TrimSpace(raw), "/")
	b = strings.TrimSuffix(b, "/v1")
	if b == "" {
		return defaultPlatformURL
	}
	return b
}

func aiBaseURL() string {
	return normalizePlatformBase(cmp.Or(
		env.Or("HANZO_AI_URL", ""),
		env.Or("CLOUD_AI_BASE_URL", ""),
		defaultPlatformURL,
	))
}

func platformBaseURL() string {
	return normalizePlatformBase(cmp.Or(
		env.Or("HANZO_API_URL", ""),
		env.Or("HANZO_AI_URL", ""),
		env.Or("CLOUD_AI_BASE_URL", ""),
		defaultPlatformURL,
	))
}

func studioBaseURL() string {
	return normalizePlatformBase(cmp.Or(
		env.Or("HANZO_STUDIO_URL", ""),
		env.Or("FLOW_STUDIO_URL", ""),
		env.Or("HANZO_AI_URL", ""),
		env.Or("CLOUD_AI_BASE_URL", ""),
		defaultPlatformURL,
	))
}

func (e *dagExec) platformToken() string {
	if e.bearer != "" {
		return e.bearer
	}
	return cmp.Or(env.Or("HANZO_AI_API_KEY", ""), env.Or("CLOUD_AI_API_KEY", ""))
}

func (e *dagExec) doPlatformJSON(method, fullURL string, body any, to time.Duration, limit int64) (int, []byte, error) {
	ctx, cancel := context.WithTimeout(e.ctx, to)
	defer cancel()
	var rdr io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		rdr = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, fullURL, rdr)
	if err != nil {
		return 0, nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	// Always stamp the validated org; never trust tenant data for org routing.
	if e.org != "" {
		req.Header.Set("X-Org-Id", e.org)
	}
	if e.project != "" && !principal.IsDefaultProject(e.project) {
		req.Header.Set("X-Project-Id", e.project)
	}
	if tok := e.platformToken(); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := dagPlatformClient.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("platform request failed")
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, limit))
	if err != nil {
		return resp.StatusCode, nil, fmt.Errorf("platform response read failed")
	}
	return resp.StatusCode, raw, nil
}

func (e *dagExec) runAgent(cfg map[string]any) (map[string]any, error) {
	model := cmp.Or(strVal(cfg, "model"), defaultAIModel)

	var messages []any
	if rawMsgs, ok := cfg["messages"].([]any); ok && len(rawMsgs) > 0 {
		messages = rawMsgs
	} else {
		if sys := cmp.Or(strVal(cfg, "system"), strVal(cfg, "systemPrompt"), strVal(cfg, "instructions")); sys != "" {
			messages = append(messages, map[string]any{"role": "system", "content": sys})
		}
		prompt := cmp.Or(
			strVal(cfg, "prompt"),
			strVal(cfg, "userPrompt"),
			strVal(cfg, "input"),
			strVal(cfg, "message"),
			e.lastText,
			e.input,
		)
		messages = append(messages, map[string]any{"role": "user", "content": prompt})
	}

	reqBody := map[string]any{
		"model":    model,
		"messages": messages,
		"stream":   false,
	}
	if _, ok := cfg["temperature"]; ok {
		reqBody["temperature"] = numVal(cfg, "temperature", 0.7)
	}
	if mt := int(numVal(cfg, "max_tokens", numVal(cfg, "maxTokens", 0))); mt > 0 {
		reqBody["max_tokens"] = mt
	}
	if tools, ok := cfg["tools"]; ok && tools != nil {
		reqBody["tools"] = tools
	}
	if rf, ok := cfg["response_format"]; ok && rf != nil {
		reqBody["response_format"] = rf
	}

	to := stepTimeout(cfg, 60*time.Second, 120*time.Second)
	status, raw, err := e.doPlatformJSON(http.MethodPost, aiBaseURL()+"/v1/chat/completions", reqBody, to, maxStepBody)
	if err != nil {
		return nil, fmt.Errorf("ai gateway: %w", err)
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("ai gateway returned %d: %s", status, sanitizeDetail(raw))
	}

	var parsed struct {
		ID      string `json:"id"`
		Model   string `json:"model"`
		Usage   any    `json:"usage"`
		Choices []struct {
			Message struct {
				Role      string `json:"role"`
				Content   string `json:"content"`
				ToolCalls any    `json:"tool_calls"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("ai gateway: malformed response")
	}

	var content string
	var toolCalls any
	var finishReason string
	if len(parsed.Choices) > 0 {
		content = parsed.Choices[0].Message.Content
		toolCalls = parsed.Choices[0].Message.ToolCalls
		finishReason = parsed.Choices[0].FinishReason
	}
	out := map[string]any{
		"text":    content,
		"content": content,
		"model":   cmp.Or(parsed.Model, model),
	}
	if parsed.ID != "" {
		out["id"] = parsed.ID
	}
	if finishReason != "" {
		out["finish_reason"] = finishReason
	}
	if parsed.Usage != nil {
		out["usage"] = parsed.Usage
	}
	if toolCalls != nil {
		out["tool_calls"] = toolCalls
	}
	return out, nil
}

// ── MCP / tool step ─────────────────────────────────────────────────────────

func (e *dagExec) runMCPTool(cfg map[string]any) (map[string]any, error) {
	toolName := cmp.Or(strVal(cfg, "tool"), strVal(cfg, "toolName"), strVal(cfg, "name"))
	if toolName == "" {
		return nil, stepClientError{fmt.Errorf("mcp/tool step requires tool name")}
	}
	args, _ := cfg["arguments"].(map[string]any)
	if args == nil {
		args, _ = cfg["args"].(map[string]any)
	}
	if args == nil {
		args, _ = cfg["input"].(map[string]any)
	}
	if args == nil {
		args = map[string]any{}
	}

	endpoint := strVal(cfg, "endpoint")
	if endpoint == "" {
		endpoint = "/mcp"
	} else {
		var err error
		endpoint, err = validatePlatformPath(endpoint)
		if err != nil {
			return nil, stepClientError{err}
		}
	}

	rpcBody := map[string]any{
		"jsonrpc": "2.0",
		"id":      e.runID,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      toolName,
			"arguments": args,
		},
	}
	to := stepTimeout(cfg, 30*time.Second, 90*time.Second)
	status, raw, err := e.doPlatformJSON(http.MethodPost, platformBaseURL()+endpoint, rpcBody, to, maxStepBody)
	if err != nil {
		return nil, fmt.Errorf("mcp tool %q: %w", toolName, err)
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("mcp tool %q returned %d: %s", toolName, status, sanitizeDetail(raw))
	}
	var resp map[string]any
	if json.Unmarshal(raw, &resp) == nil {
		if errObj, hasErr := resp["error"]; hasErr && errObj != nil {
			return nil, fmt.Errorf("mcp tool %q error: %v", toolName, errObj)
		}
		out := map[string]any{"tool": toolName, "ok": true}
		if result, ok := resp["result"]; ok {
			out["result"] = result
			if txt := extractOutputText(result); txt != "" {
				out["text"] = txt
			}
		}
		return out, nil
	}
	return map[string]any{"tool": toolName, "ok": true, "body": string(raw)}, nil
}

// ── HTTP / connector / platform action step ─────────────────────────────────

// allowedPlatformPrefixes restricts relative platform action paths so a tenant
// workflow can dispatch to safe product APIs (/v1/notify, /v1/tel, /v1/decisions,
// /v1/base, etc.) and can NEVER reach privileged admin/KMS/IAM surfaces.
var allowedPlatformPrefixes = []string{
	"/v1/notify",
	"/v1/tel",
	"/v1/decisions",
	"/v1/base",
	"/v1/tool",
	"/v1/tools",
	"/v1/chat",
	"/v1/images",
	"/v1/audio",
	"/v1/videos",
	"/v1/3d",
	"/v1/studio",
	"/v1/prompt",
	"/v1/crm",
	"/v1/auto",
	"/v1/event",
	"/v1/ai",
}

func validatePlatformPath(raw string) (string, error) {
	p := strings.TrimSpace(raw)
	if p == "/mcp" {
		return p, nil
	}
	if !strings.HasPrefix(p, "/v1/") {
		return "", fmt.Errorf("platform endpoint %q must start with /v1/", p)
	}
	if strings.Contains(p, "..") || strings.Contains(p, "://") ||
		strings.ContainsAny(p, "@\\\r\n \t") {
		return "", fmt.Errorf("invalid platform endpoint %q", p)
	}
	pathOnly := p
	if idx := strings.IndexByte(pathOnly, '?'); idx >= 0 {
		pathOnly = pathOnly[:idx]
	}
	for _, prefix := range allowedPlatformPrefixes {
		if pathOnly == prefix || strings.HasPrefix(pathOnly, prefix+"/") {
			return p, nil
		}
	}
	return "", fmt.Errorf("platform endpoint %q is not in the allowlisted action planes", pathOnly)
}

var safeCollectionRE = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

func (e *dagExec) runHTTPOrAction(kind string, cfg map[string]any) (map[string]any, error) {
	// 1. Explicit relative platform endpoint (/v1/notify, /v1/tel, /v1/decisions, /v1/base, ...).
	endpoint := cmp.Or(strVal(cfg, "endpoint"), strVal(cfg, "path"))
	service := strings.ToLower(cmp.Or(strVal(cfg, "service"), strVal(cfg, "action")))
	rawURL := strVal(cfg, "url")

	if rawURL == "" && (endpoint != "" || kind == "notify" || kind == "tel" || kind == "base" ||
		service == "notify" || service == "tel" || service == "decisions" || service == "base") {
		if endpoint == "" {
			switch cmp.Or(service, kind) {
			case "notify":
				switch strings.ToLower(strVal(cfg, "channel")) {
				case "sms":
					endpoint = "/v1/notify/send/sms"
				case "email":
					endpoint = "/v1/notify/send/email"
				default:
					endpoint = "/v1/notify/send"
				}
			case "tel":
				if strings.ToLower(strVal(cfg, "operation")) == "call" {
					endpoint = "/v1/tel/calls"
				} else {
					endpoint = "/v1/tel/messages"
				}
			case "decisions":
				endpoint = "/v1/decisions"
			case "base":
				endpoint = "/v1/base"
				if col := strVal(cfg, "collection"); col != "" {
					if !safeCollectionRE.MatchString(col) {
						return nil, stepClientError{fmt.Errorf("invalid base collection %q", col)}
					}
					endpoint = "/v1/base/" + col
				}
			}
		}
		return e.runPlatformAction(endpoint, cfg)
	}

	// 2. Tenant-supplied external HTTP / connector URL — strictly via hanzoai/outbound.
	if rawURL == "" {
		return nil, stepClientError{fmt.Errorf("http step requires url or platform endpoint")}
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return nil, stepClientError{fmt.Errorf("http step url must be an absolute http(s) URL")}
	}

	method := strings.ToUpper(strVal(cfg, "method"))
	bodyVal, hasBody := cfg["body"]
	if !hasBody {
		bodyVal, hasBody = cfg["payload"]
	}
	if method == "" {
		if hasBody && bodyVal != nil {
			method = http.MethodPost
		} else {
			method = http.MethodGet
		}
	}
	switch method {
	case http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodHead:
	default:
		return nil, stepClientError{fmt.Errorf("unsupported http method %q", method)}
	}

	var reqBody io.Reader
	var defaultCT string
	if hasBody && bodyVal != nil && method != http.MethodGet && method != http.MethodHead {
		switch b := bodyVal.(type) {
		case string:
			reqBody = strings.NewReader(b)
			if strings.HasPrefix(strings.TrimSpace(b), "{") || strings.HasPrefix(strings.TrimSpace(b), "[") {
				defaultCT = "application/json"
			}
		default:
			encoded, err := json.Marshal(b)
			if err != nil {
				return nil, stepClientError{fmt.Errorf("marshal http body: %w", err)}
			}
			reqBody = bytes.NewReader(encoded)
			defaultCT = "application/json"
		}
	}

	to := stepTimeout(cfg, 20*time.Second, 60*time.Second)
	ctx, cancel := context.WithTimeout(e.ctx, to)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, method, parsed.String(), reqBody)
	if err != nil {
		return nil, stepClientError{fmt.Errorf("http request: %w", err)}
	}
	if defaultCT != "" {
		req.Header.Set("Content-Type", defaultCT)
	}
	// Only send headers explicitly stated on the node — NEVER forward caller's
	// Hanzo IAM bearer token or platform secrets to external URLs.
	if hdrs, ok := cfg["headers"].(map[string]any); ok {
		for k, v := range hdrs {
			if sv := stringify(v); k != "" && sv != "" {
				req.Header.Set(k, sv)
			}
		}
	}

	resp, err := dagOutboundClient.Do(req)
	if err != nil {
		if errors.Is(err, outbound.ErrRefused) {
			return nil, fmt.Errorf("outbound request refused: %w", err)
		}
		return nil, fmt.Errorf("http request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxStepBody))
	if err != nil {
		return nil, fmt.Errorf("http response read failed")
	}

	out := map[string]any{
		"status": resp.StatusCode,
		"ok":     resp.StatusCode >= 200 && resp.StatusCode < 300,
		"body":   string(raw),
	}
	var parsedJSON any
	if len(raw) > 0 && json.Unmarshal(raw, &parsedJSON) == nil {
		out["json"] = parsedJSON
	}
	if resp.StatusCode >= 400 && boolVal(cfg, "failOnError") {
		return nil, fmt.Errorf("http %s returned %d: %s", method, resp.StatusCode, sanitizeDetail(raw))
	}
	return out, nil
}

func (e *dagExec) runPlatformAction(rawPath string, cfg map[string]any) (map[string]any, error) {
	safePath, err := validatePlatformPath(rawPath)
	if err != nil {
		return nil, stepClientError{err}
	}
	method := strings.ToUpper(cmp.Or(strVal(cfg, "method"), http.MethodPost))
	switch method {
	case http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
	default:
		return nil, stepClientError{fmt.Errorf("unsupported action method %q", method)}
	}
	var body any
	if method != http.MethodGet && method != http.MethodDelete {
		if b, ok := cfg["body"]; ok && b != nil {
			body = b
		} else if p, ok := cfg["payload"]; ok && p != nil {
			body = p
		} else {
			// Pass non-control config keys as the action payload.
			m := make(map[string]any, len(cfg))
			for k, v := range cfg {
				switch k {
				case "kind", "type", "nodeType", "label", "endpoint", "path", "method", "service", "action", "timeoutMs", "continueOnError":
					continue
				}
				m[k] = v
			}
			body = m
		}
	}

	to := stepTimeout(cfg, 30*time.Second, 90*time.Second)
	status, raw, err := e.doPlatformJSON(method, platformBaseURL()+safePath, body, to, maxStepBody)
	if err != nil {
		return nil, fmt.Errorf("platform action %s: %w", safePath, err)
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("platform action %s returned %d: %s", safePath, status, sanitizeDetail(raw))
	}
	out := map[string]any{
		"endpoint": safePath,
		"status":   status,
		"ok":       true,
	}
	var parsed any
	if len(raw) > 0 && json.Unmarshal(raw, &parsed) == nil {
		out["data"] = parsed
		if txt := extractOutputText(parsed); txt != "" {
			out["text"] = txt
		}
	} else if len(raw) > 0 {
		out["body"] = string(raw)
	}
	return out, nil
}

// ── Studio / Comfy GPU media step (HTTP-isolated) ───────────────────────────

func (e *dagExec) runMedia(kind string, cfg map[string]any) (map[string]any, error) {
	isComfy := kind == "comfy" ||
		strings.EqualFold(strVal(cfg, "mode"), "comfy") ||
		strVal(cfg, "endpoint") == "/v1/prompt" ||
		cfg["prompt_graph"] != nil ||
		cfg["workflow"] != nil

	var endpoint string
	var reqBody map[string]any
	if isComfy {
		endpoint = "/v1/prompt"
		graph := cfg["prompt_graph"]
		if graph == nil {
			graph = cfg["workflow"]
		}
		if graph == nil {
			graph = cfg["prompt"]
		}
		reqBody = map[string]any{
			"prompt":    graph,
			"client_id": e.sessionID,
		}
		if extra, ok := cfg["extra_data"]; ok && extra != nil {
			reqBody["extra_data"] = extra
		}
	} else {
		if ep := strVal(cfg, "endpoint"); ep != "" {
			safe, err := validatePlatformPath(ep)
			if err != nil {
				return nil, stepClientError{err}
			}
			endpoint = safe
		} else {
			mediaType := strings.ToLower(cmp.Or(strVal(cfg, "mediaType"), kind))
			switch mediaType {
			case "audio", "speech", "voice":
				endpoint = "/v1/audio/speech"
			case "video":
				endpoint = "/v1/videos/generations"
			default:
				endpoint = "/v1/images/generations"
			}
		}
		prompt := cmp.Or(strVal(cfg, "prompt"), e.lastText, e.input)
		reqBody = map[string]any{
			"model":  cmp.Or(strVal(cfg, "model"), "iro"),
			"prompt": prompt,
		}
		if endpoint == "/v1/audio/speech" {
			reqBody["input"] = prompt
			if voice := strVal(cfg, "voice"); voice != "" {
				reqBody["voice"] = voice
			}
		} else {
			if n := int(numVal(cfg, "n", 1)); n > 0 {
				reqBody["n"] = min(n, 4)
			}
			if sz := strVal(cfg, "size"); sz != "" {
				reqBody["size"] = sz
			}
			if rf := strVal(cfg, "response_format"); rf != "" {
				reqBody["response_format"] = rf
			}
		}
	}

	to := stepTimeout(cfg, 90*time.Second, 180*time.Second)
	status, raw, err := e.doPlatformJSON(http.MethodPost, studioBaseURL()+endpoint, reqBody, to, maxMediaBody)
	if err != nil {
		return nil, fmt.Errorf("media %s: %w", endpoint, err)
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("media %s returned %d: %s", endpoint, status, sanitizeDetail(raw))
	}

	out := map[string]any{
		"endpoint": endpoint,
		"status":   status,
		"ok":       true,
	}
	var parsed map[string]any
	if json.Unmarshal(raw, &parsed) == nil {
		for k, v := range parsed {
			out[k] = v
		}
	} else if len(raw) > 0 {
		out["body"] = string(raw)
	}
	return out, nil
}

// ── output aggregation step ─────────────────────────────────────────────────

func (e *dagExec) runOutput(cfg map[string]any, inEdges []dagEdge) map[string]any {
	out := map[string]any{}
	if v, ok := cfg["value"]; ok && v != nil {
		out["value"] = v
		out["text"] = stringify(v)
		return out
	}
	if tmpl, ok := cfg["template"].(string); ok && tmpl != "" {
		out["text"] = tmpl
		out["value"] = tmpl
		return out
	}
	if fields, ok := cfg["fields"].(map[string]any); ok && len(fields) > 0 {
		for k, v := range fields {
			out[k] = v
		}
		if _, hasText := out["text"]; !hasText && e.lastText != "" {
			out["text"] = e.lastText
		}
		return out
	}

	// Aggregate outputs from executed upstream predecessors.
	collected := map[string]any{}
	var lastPredText string
	for _, ie := range inEdges {
		src := ie.src()
		if v, executed := e.outputs[src]; executed {
			collected[src] = v
			if t := extractOutputText(v); t != "" {
				lastPredText = t
			}
		}
	}
	if len(collected) > 0 {
		out["steps"] = collected
	}
	text := cmp.Or(lastPredText, e.lastText)
	if text != "" {
		out["text"] = text
	}
	if len(e.vars) > 0 {
		varsCopy := make(map[string]any, len(e.vars))
		for k, v := range e.vars {
			varsCopy[k] = v
		}
		out["variables"] = varsCopy
	}
	return out
}

// ── template interpolation & path lookup ────────────────────────────────────

var (
	dagRefRE      = regexp.MustCompile(`\{\{\s*([^}]+?)\s*\}\}`)
	dagWholeRefRE = regexp.MustCompile(`^\{\{\s*([^}]+?)\s*\}\}$`)
)

func (e *dagExec) resolveValue(v any) any {
	switch t := v.(type) {
	case string:
		return e.resolveString(t)
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, x := range t {
			out[k] = e.resolveValue(x)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, x := range t {
			out[i] = e.resolveValue(x)
		}
		return out
	default:
		return v
	}
}

func (e *dagExec) resolveString(s string) any {
	if !strings.Contains(s, "{{") {
		return s
	}
	if m := dagWholeRefRE.FindStringSubmatch(strings.TrimSpace(s)); m != nil {
		if val, ok := e.lookupPath(m[1]); ok {
			return val
		}
		return s
	}
	return dagRefRE.ReplaceAllStringFunc(s, func(match string) string {
		sub := dagRefRE.FindStringSubmatch(match)
		if val, ok := e.lookupPath(sub[1]); ok {
			return stringify(val)
		}
		return match
	})
}

func (e *dagExec) lookupPath(path string) (any, bool) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, false
	}
	parts := strings.Split(path, ".")
	switch parts[0] {
	case "input":
		if len(parts) == 1 {
			return e.input, true
		}
		return walkAny(e.payload, parts[1:])
	case "payload":
		if len(parts) == 1 {
			if e.payload != nil {
				return e.payload, true
			}
			return e.input, true
		}
		return walkAny(e.payload, parts[1:])
	case "vars":
		return walkAny(e.vars, parts[1:])
	case "steps", "nodes":
		if len(parts) < 2 {
			return e.outputs, true
		}
		stepOut, ok := e.outputs[parts[1]]
		if !ok {
			return nil, false
		}
		return walkAny(stepOut, parts[2:])
	case "session_id", "session":
		return e.sessionID, true
	case "workflow_id", "workflow":
		return e.workflow, true
	case "run_id":
		return e.runID, true
	case "org":
		return e.org, true
	case "last", "prev":
		if len(parts) == 2 && parts[1] == "text" {
			return e.lastText, true
		}
		return walkAny(e.lastOut, parts[1:])
	}

	// Direct step output lookup: {{stepID.field}}.
	if stepOut, ok := e.outputs[parts[0]]; ok {
		return walkAny(stepOut, parts[1:])
	}
	// Direct variable lookup: {{varName}} or {{varName.sub}}.
	if val, ok := e.vars[parts[0]]; ok {
		return walkAny(val, parts[1:])
	}
	// Direct payload field lookup when input was JSON.
	if pm, ok := e.payload.(map[string]any); ok {
		if val, exists := pm[parts[0]]; exists {
			return walkAny(val, parts[1:])
		}
	}
	return nil, false
}

func walkAny(cur any, parts []string) (any, bool) {
	for _, p := range parts {
		switch t := cur.(type) {
		case map[string]any:
			v, ok := t[p]
			if !ok {
				return nil, false
			}
			cur = v
		case []any:
			idx, err := strconv.Atoi(p)
			if err != nil || idx < 0 || idx >= len(t) {
				return nil, false
			}
			cur = t[idx]
		default:
			return nil, false
		}
	}
	return cur, true
}

// ── small value & formatting helpers ────────────────────────────────────────

func strVal(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	if s, ok := m[key].(string); ok {
		return strings.TrimSpace(s)
	}
	return ""
}

func boolVal(m map[string]any, key string) bool {
	if m == nil {
		return false
	}
	if b, ok := m[key].(bool); ok {
		return b
	}
	return false
}

func numVal(m map[string]any, key string, def float64) float64 {
	if m == nil {
		return def
	}
	if f, ok := asFloat(m[key]); ok {
		return f
	}
	return def
}

func stepTimeout(cfg map[string]any, def, ceiling time.Duration) time.Duration {
	ms := int64(numVal(cfg, "timeoutMs", 0))
	if ms <= 0 {
		return def
	}
	d := time.Duration(ms) * time.Millisecond
	if d > ceiling {
		return ceiling
	}
	return d
}

func stringify(v any) string {
	if v == nil {
		return ""
	}
	switch t := v.(type) {
	case string:
		return t
	case float64:
		if t == math.Trunc(t) && math.Abs(t) < 1e15 {
			return strconv.FormatInt(int64(t), 10)
		}
		return strconv.FormatFloat(t, 'f', -1, 64)
	case int:
		return strconv.Itoa(t)
	case int64:
		return strconv.FormatInt(t, 10)
	case bool:
		return strconv.FormatBool(t)
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return fmt.Sprint(v)
		}
		return string(b)
	}
}

func extractOutputText(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case map[string]any:
		for _, k := range []string{"text", "content", "summary", "url", "prompt_id", "body"} {
			if s, ok := t[k].(string); ok && strings.TrimSpace(s) != "" {
				return s
			}
		}
		if v2, ok := t["value"]; ok {
			if s := stringify(v2); s != "" {
				return s
			}
		}
	}
	return ""
}

func sanitizeDetail(body []byte) string {
	msg := strings.TrimSpace(detail(body))
	const limit = 256
	if len(msg) > limit {
		msg = msg[:limit]
	}
	return msg
}
