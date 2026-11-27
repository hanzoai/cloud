package agents

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk/infer"
)

type groupMockAI struct {
	mu    sync.Mutex
	calls []*infer.ChatRequest
	fn    func(req *infer.ChatRequest) (*infer.ChatResponse, error)
}

func (s *groupMockAI) ChatCompletion(_ context.Context, req *infer.ChatRequest) (*infer.ChatResponse, error) {
	s.mu.Lock()
	cp := *req
	cp.Messages = append([]infer.ChatMessage(nil), req.Messages...)
	s.calls = append(s.calls, &cp)
	fn := s.fn
	s.mu.Unlock()
	return fn(req)
}

func (s *groupMockAI) Embed(_ context.Context, _ *infer.EmbedRequest) ([][]float32, error) {
	return nil, nil
}

func (s *groupMockAI) Rerank(context.Context, *infer.RerankRequest) ([]float64, error) {
	return nil, nil
}

func TestBootstrapPrimaryAndSinglePrimaryInvariant(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountApp(t, &fakeAI{content: "ready"})

		// First bootstrap creates "hanzo" as Primary Bot (Layer 0, tools=["*"]).
		code, body := do(t, app, http.MethodPost, "/v1/agent/bootstrap", "acme", map[string]any{})
		if code != http.StatusOK {
			t.Fatalf("bootstrap 1: %d %s", code, body)
		}
		var b1 bootstrapPrimaryOut
		if err := json.Unmarshal(body, &b1); err != nil {
			t.Fatal(err)
		}
		if !b1.Created || b1.Agent.Name != "hanzo" || !b1.Agent.IsPrimary || b1.Agent.Layer != 0 {
			t.Fatalf("unexpected primary bootstrap: %+v", b1)
		}
		if len(b1.Agent.Tools) != 1 || b1.Agent.Tools[0] != "*" {
			t.Fatalf("primary bot must carry tools=[\"*\"], got %+v", b1.Agent.Tools)
		}

		// Second bootstrap is idempotent and returns the same Primary Bot.
		code, body = do(t, app, http.MethodPost, "/v1/agent/bootstrap", "acme", map[string]any{})
		if code != http.StatusOK {
			t.Fatalf("bootstrap 2: %d %s", code, body)
		}
		var b2 bootstrapPrimaryOut
		if err := json.Unmarshal(body, &b2); err != nil {
			t.Fatal(err)
		}
		if b2.Created || b2.Agent.ID != b1.Agent.ID {
			t.Fatalf("expected idempotent return of %s, got %+v", b1.Agent.ID, b2)
		}

		// Creating a second bot with isPrimary=true promotes the new bot and clears
		// isPrimary on the previous one so <= 1 Primary Bot exists per org.
		code, body = do(t, app, http.MethodPost, "/v1/agent", "acme", map[string]any{
			"name":               "director-v2",
			"isPrimary":          true,
			"tools":              []string{"*"},
			"cap_micro_usd":      5000000,
			"max_task_micro_usd": 1000000,
			"period":             "week",
		})
		if code != http.StatusCreated {
			t.Fatalf("create director-v2: %d %s", code, body)
		}

		code, body = do(t, app, http.MethodGet, "/v1/agent", "acme", nil)
		if code != http.StatusOK {
			t.Fatalf("list agents: %d %s", code, body)
		}
		var listed agentList
		if err := json.Unmarshal(body, &listed); err != nil {
			t.Fatal(err)
		}
		primaries := 0
		var primaryName string
		for _, a := range listed.Agents {
			if a.IsPrimary {
				primaries++
				primaryName = a.Name
			}
		}
		if primaries != 1 || primaryName != "director-v2" {
			t.Fatalf("expected exactly 1 primary (director-v2), got %d (%q) in %+v", primaries, primaryName, listed.Agents)
		}
	})
}

func TestMultiLayerBotGroupsAndQuasarConsensus(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		ai := &groupMockAI{
			fn: func(req *infer.ChatRequest) (*infer.ChatResponse, error) {
				txt := req.Text()
				switch {
				case strings.Contains(txt, "Consensus Ballot"):
					// Every validator attests to Candidate 0 in ballot rounds when needed.
					return &infer.ChatResponse{Content: `{"choice": 0, "confidence": 0.96}`}, nil
				case strings.Contains(txt, "You are sec-3"):
					return &infer.ChatResponse{Content: "Dissenting view: defer patch."}, nil
				case strings.Contains(txt, "You are sec-1"), strings.Contains(txt, "You are sec-2"):
					return &infer.ChatResponse{Content: "Ship the mTLS rotation immediately."}, nil
				case strings.Contains(txt, "[Escalation from Bot Group"):
					return &infer.ChatResponse{Content: "Primary hanzo tie-break: isolate node and rotate keys."}, nil
				case strings.Contains(txt, "You are split-a"):
					return &infer.ChatResponse{Content: "Option Alpha"}, nil
				case strings.Contains(txt, "You are split-b"):
					return &infer.ChatResponse{Content: "Option Beta"}, nil
				default:
					return &infer.ChatResponse{Content: "Synthesized answer."}, nil
				}
			},
		}
		app := mountApp(t, ai)

		// Bootstrap Primary Bot "hanzo" at Layer 0.
		if code, body := do(t, app, http.MethodPost, "/v1/agent/bootstrap", "acme", map[string]any{}); code != http.StatusOK {
			t.Fatalf("bootstrap: %d %s", code, body)
		}

		mkBot := func(name string) {
			t.Helper()
			code, body := do(t, app, http.MethodPost, "/v1/agent", "acme", map[string]any{
				"name":               name,
				"instructions":       "You are " + name + ".",
				"cap_micro_usd":      5000000,
				"max_task_micro_usd": 1000000,
				"period":             "week",
			})
			if code != http.StatusCreated {
				t.Fatalf("create %s: %d %s", name, code, body)
			}
		}
		for _, n := range []string{"sec-1", "sec-2", "sec-3", "split-a", "split-b"} {
			mkBot(n)
		}

		// Layer 1 Group with Lux Quasar Polaris (k=3, alpha=0.67, beta=2).
		code, body := do(t, app, http.MethodPost, "/v1/agent/groups", "acme", map[string]any{
			"name":        "security-quorum",
			"description": "Security consensus ring",
			"layer":       1,
			"members":     []string{"sec-1", "sec-2", "sec-3"},
			"policy": map[string]any{
				"kind":          "quasar",
				"quasarProfile": "polaris",
				"sampleK":       3,
				"alpha":         0.66,
				"beta":          2,
			},
		})
		if code != http.StatusCreated {
			t.Fatalf("create group: %d %s", code, body)
		}
		var g1 groupView
		if err := json.Unmarshal(body, &g1); err != nil {
			t.Fatal(err)
		}
		if g1.Layer != 1 || g1.Policy.Kind != "quasar" || g1.Policy.QuasarProfile != "polaris" {
			t.Fatalf("unexpected group view: %+v", g1)
		}

		// Nested Layer 2 Sub-group inheriting layer = parent.Layer + 1 = 2, with
		// unanimous policy + escalation to Primary Bot "hanzo".
		code, body = do(t, app, http.MethodPost, "/v1/agent/groups", "acme", map[string]any{
			"name":          "incident-subring",
			"parentGroupId": g1.ID,
			"members":       []string{"split-a", "split-b"},
			"policy": map[string]any{
				"kind":              "quasar",
				"quasarProfile":     "polaris",
				"alpha":             0.95,
				"beta":              2,
				"escalateToPrimary": true,
			},
		})
		if code != http.StatusCreated {
			t.Fatalf("create subgroup: %d %s", code, body)
		}
		var g2 groupView
		if err := json.Unmarshal(body, &g2); err != nil {
			t.Fatal(err)
		}
		if g2.Layer != 2 || g2.ParentGroupID != g1.ID {
			t.Fatalf("expected nested subgroup at Layer 2 under %s, got %+v", g1.ID, g2)
		}

		// Run Layer 1 Lux Quasar group -> 2/3 agreement + ballot achieves finality!
		code, body = do(t, app, http.MethodPost, "/v1/agent/groups/security-quorum/run", "acme", map[string]any{
			"input": "Should we rotate mTLS certs now?",
		})
		if code != http.StatusOK {
			t.Fatalf("run quasar group: %d %s", code, body)
		}
		var r1 runGroupOut
		if err := json.Unmarshal(body, &r1); err != nil {
			t.Fatal(err)
		}
		if r1.Status != "ok" || !strings.Contains(r1.Output, "Ship the mTLS rotation immediately") {
			t.Fatalf("unexpected quasar output: %+v", r1)
		}
		if r1.Quasar == nil || !r1.Quasar.Finalized || len(r1.Quasar.ThresholdDigest) != 96 || len(r1.Quasar.CheckpointDigest) != 96 {
			t.Fatalf("expected finalized Lux Quasar SHA3-384 cert, got %+v", r1.Quasar)
		}

		// Force split ballot on incident-subring so alpha=0.95 fails finality and
		// escalates to Primary Bot "hanzo"!
		ai.mu.Lock()
		origFn := ai.fn
		ai.fn = func(req *infer.ChatRequest) (*infer.ChatResponse, error) {
			txt := req.Text()
			if strings.Contains(txt, "Consensus Ballot") {
				// Alternate choices so 0.95 supermajority is not met when split.
				return &infer.ChatResponse{Content: "no json vote"}, nil
			}
			return origFn(req)
		}
		ai.mu.Unlock()

		code, body = do(t, app, http.MethodPost, "/v1/agent/groups/incident-subring/run", "acme", map[string]any{
			"input": "Pick mitigation option",
		})
		if code != http.StatusOK {
			t.Fatalf("run split group: %d %s", code, body)
		}
		var r2 runGroupOut
		if err := json.Unmarshal(body, &r2); err != nil {
			t.Fatal(err)
		}
		if r2.Quasar == nil || r2.Quasar.Finalized || !r2.Quasar.Escalated || !strings.Contains(r2.Output, "Primary hanzo tie-break") {
			t.Fatalf("expected unfinalized split to escalate to Primary Bot hanzo, got %+v (quasar=%+v)", r2, r2.Quasar)
		}
	})
}

func TestPrimaryCoordinateToolAndComputerVisionHandoff(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		sb := serveSandboxes(t)
		sb.mu.Lock()
		sb.live["m_desk99"] = true
		sb.files["m_desk99"] = map[string]string{}
		sb.mu.Unlock()

		round := 0
		var sawScreenshotInVision bool
		ai := &groupMockAI{
			fn: func(req *infer.ChatRequest) (*infer.ChatResponse, error) {
				for _, m := range req.Messages {
					if len(m.Images) > 0 && strings.HasPrefix(m.Images[0], "data:image/jpeg;base64,") {
						sawScreenshotInVision = true
					}
				}
				txt := req.Text()
				if strings.Contains(txt, "You are worker-1") {
					return &infer.ChatResponse{Content: "Worker completed UI check."}, nil
				}
				round++
				switch round {
				case 1:
					return &infer.ChatResponse{
						ToolCalls: []infer.ToolCall{{
							ID:        "tc_coord",
							Name:      "coordinate",
							Arguments: `{"action":"delegate_bot","target":"worker-1","input":"verify login button"}`,
						}},
					}, nil
				case 2:
					return &infer.ChatResponse{
						ToolCalls: []infer.ToolCall{{
							ID:        "tc_comp",
							Name:      "computer_in_sandbox",
							Arguments: `{"action":"screenshot"}`,
						}},
					}, nil
				default:
					return &infer.ChatResponse{Content: "Coordinated worker-1 and inspected desktop screen."}, nil
				}
			},
		}

		// Install fake toolPlane returning a large >16KB base64 JPEG screenshot to
		// verify extractComputerMedia keeps RoleTool compact and attaches Images!
		prevTools := runTools
		fakeImg := "data:image/jpeg;base64," + strings.Repeat("A", 24000)
		runTools = fakeMediaPlane{img: fakeImg}
		t.Cleanup(func() { runTools = prevTools })

		app := mountApp(t, ai)

		if code, body := do(t, app, http.MethodPost, "/v1/agent/bootstrap", "acme", map[string]any{}); code != http.StatusOK {
			t.Fatalf("bootstrap: %d %s", code, body)
		}
		if code, body := do(t, app, http.MethodPost, "/v1/agent", "acme", map[string]any{
			"name":               "worker-1",
			"instructions":       "You are worker-1.",
			"layer":              1,
			"cap_micro_usd":      5000000,
			"max_task_micro_usd": 1000000,
			"period":             "week",
		}); code != http.StatusCreated {
			t.Fatalf("create worker-1: %d %s", code, body)
		}

		// Bind Primary Bot "hanzo" to an active desktop ComputeRef so boxCall fills
		// id="m_desk99" without needing a live sandbox peer socket.
		if code, body := do(t, app, http.MethodPatch, "/v1/agent/hanzo", "acme", map[string]any{
			"computeRef": "m_desk99",
		}); code != http.StatusOK {
			t.Fatalf("bind computeRef: %d %s", code, body)
		}

		code, body := do(t, app, http.MethodPost, "/v1/agent/hanzo/run", "acme", map[string]any{
			"input": "Coordinate worker-1 and inspect the desktop.",
		})
		if code != http.StatusOK {
			t.Fatalf("run primary hanzo: %d %s", code, body)
		}
		if !sawScreenshotInVision {
			t.Fatal("expected model to receive untruncated screenshot in ChatMessage.Images")
		}
		sb.mu.Lock()
		ended := len(sb.ends)
		parked := len(sb.parks)
		sb.mu.Unlock()
		if ended != 0 || parked != 0 {
			t.Fatalf("bound desktop ComputeRef m_desk99 must stay live after turn, got ends=%d parks=%d", ended, parked)
		}
	})
}

type fakeMediaPlane struct {
	img string
}

func (f fakeMediaPlane) catalog(_ context.Context, _, _ string, _ []string) []infer.ToolDef {
	return []infer.ToolDef{{
		Name:        "computer_in_sandbox",
		Description: "Desktop computer use",
		Schema:      json.RawMessage(`{"type":"object"}`),
	}}
}

func (f fakeMediaPlane) call(_ context.Context, _, _, name, args string) (string, error) {
	if name == "computer_in_sandbox" {
		b, _ := json.Marshal(client.ComputerOut{OK: true, Text: "screen captured", Image: f.img})
		return string(b), nil
	}
	return "ok:" + args, nil
}

// Regression test for hanzo-inc/cloud#937: topology instructions must use neutral
// operational tool framing (not persona-override "Primary Bot (Layer 0)" prose)
// and must only be appended when the org actually has agents or groups to coordinate.
func TestTopologySummaryUsesOperationalFramingNotPersonaOverride(t *testing.T) {
	self := Agent{Name: "hanzo", IsPrimary: true, Layer: 0}
	bots := []Agent{
		self,
		{Name: "dev", Layer: 1, Status: "ready"},
		{Name: "old", Layer: 1, Status: retired},
	}
	groups := []Group{{
		Name:        "eng",
		Description: "Engineering team",
		Layer:       1,
		Members:     []string{"dev"},
		Policy:      groupPolicy{Kind: PolicyLeader, Leader: "dev"},
	}}
	summary := buildTopologySummary(self, bots, groups)
	for _, forbidden := range []string{
		"Primary Bot (Layer 0)",
		"You are `hanzo`, the Primary Bot",
		"Subordinate bots",
	} {
		if strings.Contains(summary, forbidden) {
			t.Errorf("topology summary must not contain persona-override framing %q; got:\n%s", forbidden, summary)
		}
	}
	for _, want := range []string{
		"Coordination tool: `coordinate`",
		"`eng`",
		"Available agents: `dev`",
	} {
		if !strings.Contains(summary, want) {
			t.Errorf("topology summary missing operational guidance %q; got:\n%s", want, summary)
		}
	}
	if strings.Contains(summary, "`old`") {
		t.Errorf("retired agent must not appear in topology summary: %s", summary)
	}
}

func TestWithCoordinationSkipsEmptyTopologyAppend(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		var sawMsgs []infer.ChatMessage
		ai := &groupMockAI{
			fn: func(req *infer.ChatRequest) (*infer.ChatResponse, error) {
				sawMsgs = append([]infer.ChatMessage(nil), req.Messages...)
				return &infer.ChatResponse{Content: "done"}, nil
			},
		}
		app := mountApp(t, ai)

		// Bootstrap only "hanzo" with no other bots or groups.
		if code, body := do(t, app, http.MethodPost, "/v1/agent/bootstrap", "solo-org", map[string]any{}); code != http.StatusOK {
			t.Fatalf("bootstrap: %d %s", code, body)
		}
		if code, body := do(t, app, http.MethodPost, "/v1/agent/hanzo/run", "solo-org", map[string]any{
			"input": "fix the docs bug",
		}); code != http.StatusOK {
			t.Fatalf("run: %d %s", code, body)
		}
		if len(sawMsgs) != 2 {
			t.Fatalf("want system + user messages, got %d: %+v", len(sawMsgs), sawMsgs)
		}
		if sawMsgs[0].Role != infer.RoleSystem {
			t.Fatalf("instructions must be RoleSystem, got %q", sawMsgs[0].Role)
		}
		if strings.Contains(sawMsgs[0].Content, "Primary Bot (Layer 0)") {
			t.Fatalf("system prompt must not contain persona-override Primary Bot (Layer 0) text")
		}
	})
}
