// bots.go mounts the Hanzo Cloud BOT surface (/v1/bots) plus the machine
// agent-binding proxies (/v1/machines/:id/{bind-agent,agent-binding},
// /v1/agent-bindings). It is the SIBLING of machines: a Bot is not a new state
// this subsystem owns, it is a composition of two things vm already owns — a
// kind=bot Machine and an AgentBinding. So every route here is a thin, org-scoped
// translation over the SAME Visor client the machines routes use (client.go),
// never a second store.
//
// A Bot = Agent (cloud /v1/agents) + Machine (vm, kind=bot) + the binding between
// them. Composition, one way per verb:
//
//	launch  = vm POST /v1/machines/launch {kind:bot}  THEN vm POST .../bind-agent
//	list    = vm GET  /v1/machines?kind=bot           joined with the org's bindings
//	get     = vm GET  /v1/machines/:id                joined with its binding
//	delete  = vm DELETE .../agent-binding (unbind)    THEN vm DELETE /v1/machines/:id
//	message = the AGENT path: run the bot's bound agent via /v1/agents/:agent/run
//	stop    = vm DELETE .../agent-binding — halt the bot's @hanzo/bot runtime
//	pause   = the same halt: DigitalOcean/vm expose no VM-suspend primitive, so a
//	          bot's stop and pause are one honest capability (detach the agent
//	          runtime); powering the underlying machine off/on is a machine-lifecycle
//	          concern handled by launch/delete, not a fabricated bot state.
//
// Tenancy is identical to machines: the org is the VALIDATED principal
// (principal.Org, taken from the IAM owner claim), forwarded to vm as
// ?owner=<org>, so a caller can only ever read or mutate its OWN bots. No
// validated principal ⇒ 403, before anything reaches vm.
package visor

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/zap-proto/zip"
)

// agentBinding mirrors vm/object.AgentBinding — the record that a machine runs
// the @hanzo/bot runtime for a cloud Agent. Emitted verbatim so vm stays the one
// source of truth for the binding shape (status/message are vm's honest,
// reconciled values, never invented here).
type agentBinding struct {
	Owner       string `json:"owner,omitempty"`
	Name        string `json:"name,omitempty"`
	MachineId   string `json:"machineId,omitempty"`
	Org         string `json:"org,omitempty"`
	AgentName   string `json:"agentName,omitempty"`
	Provider    string `json:"provider,omitempty"`
	PublicIp    string `json:"publicIp,omitempty"`
	BotVersion  string `json:"botVersion,omitempty"`
	Status      string `json:"status,omitempty"`
	Message     string `json:"message,omitempty"`
	CreatedTime string `json:"createdTime,omitempty"`
	UpdatedTime string `json:"updatedTime,omitempty"`
}

// identifies reports whether a binding carries any real identity — used to tell a
// present binding from an empty (no-binding) zero value returned by vm.
func (b agentBinding) identifies() bool {
	return b.Name != "" || b.MachineId != "" || b.AgentName != ""
}

// botView is what /v1/bots emits: the bot's machine (the clean machineView the
// console already consumes) with the bound agent surfaced. binding carries the
// honest, vm-reconciled lifecycle status when present.
type botView struct {
	machineView
	Agent   string        `json:"agent,omitempty"`
	Binding *agentBinding `json:"binding,omitempty"`
}

func toBotView(m visorMachine, b *agentBinding) botView {
	v := botView{machineView: toMachineView(m)}
	if b != nil && b.identifies() {
		v.Agent = b.AgentName
		v.Binding = b
	}
	return v
}

// machineIsBot reports whether a machine is a Bot, from its own tags — the
// read-back of vm's launch-time hanzo-kind:bot stamp (SetKind). It is the same
// signal vm's own ?kind=bot list filter uses, so a get is consistent with a list.
func machineIsBot(m visorMachine) bool {
	for _, t := range strings.Split(m.Tag, ",") {
		if strings.EqualFold(strings.TrimSpace(t), "hanzo-kind:bot") {
			return true
		}
	}
	return false
}

// ---- bots ----

func listBots(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	var machines []visorMachine
	if err := s.State.cl.call(c, http.MethodGet, "/v1/machines", q("owner", org, "kind", "bot"), nil, &machines); err != nil {
		return err
	}
	// Join the org's bindings ONCE (O(1), not N+1), keyed by machine id — the same
	// id vm binds a machine by. Enrichment only: a bindings read failure never
	// blanks the list (a bot still lists without its reconciled status).
	byMachine := map[string]*agentBinding{}
	var bindings []agentBinding
	if err := s.State.cl.call(c, http.MethodGet, "/v1/agent-bindings", q("owner", org), nil, &bindings); err == nil {
		for i := range bindings {
			byMachine[bindings[i].Name] = &bindings[i]
		}
	}
	out := make([]botView, 0, len(machines))
	for _, m := range machines {
		out = append(out, toBotView(m, byMachine[firstNonEmpty(m.Id, m.Name)]))
	}
	return c.JSON(http.StatusOK, map[string]any{"bots": out})
}

// botLaunchReq is the POST /v1/bots/launch body. A bot needs a machine size and,
// for a real launch, a name; agent is the cloud /v1/agents identity the bot runs
// (defaulting to the bot's name so a bot is self-named by default). Model and
// Instructions configure the auto-created bound agent — both optional: an empty
// Model takes the deployment default (a valid catalog model) at agent create,
// and Instructions is the bot's system prompt.
type botLaunchReq struct {
	Name         string `json:"name"`
	Agent        string `json:"agent"`
	Model        string `json:"model"`
	Instructions string `json:"instructions"`
	Size         string `json:"size"`
	InstanceType string `json:"instanceType"`
	Region       string `json:"region"`
	BotVersion   string `json:"botVersion"`
	DryRun       bool   `json:"dryRun"`
}

func launchBot(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	var body botLaunchReq
	if err := c.Bind(&body); err != nil {
		return err
	}
	size := strings.TrimSpace(firstNonEmpty(body.Size, body.InstanceType))
	if size == "" {
		return zip.ErrBadRequest("size is required")
	}
	name := strings.TrimSpace(body.Name)

	// dryRun is a price quote only — it launches nothing, binds nothing and
	// creates no agent (spends nothing), so it short-circuits before the agent
	// half. Pass vm's quote through unchanged (the authoritative price).
	if body.DryRun {
		launch := map[string]any{"name": name, "size": size, "region": body.Region, "kind": "bot", "dryRun": true}
		var data json.RawMessage
		if err := s.State.cl.call(c, http.MethodPost, "/v1/machines/launch", q("owner", org), launch, &data); err != nil {
			return err
		}
		var quote any
		if len(data) > 0 {
			_ = json.Unmarshal(data, &quote)
		}
		return c.JSON(http.StatusOK, quote)
	}

	if name == "" {
		return zip.ErrBadRequest("name is required to launch a bot")
	}

	// The agent half, FIRST: create-if-absent the cloud Agent this bot runs, so a
	// launched bot is immediately messageable — messageBot runs the bound agent
	// via /v1/agents/:agent/run, which Resolves it from the store and 404s "agent
	// not found" if it was never created (the gap this closes). Doing it before
	// the machine launch also fails a bad request (e.g. a non-catalog model → 400)
	// BEFORE any metered machine is provisioned. org is the validated tenant.
	agent := firstNonEmpty(strings.TrimSpace(body.Agent), name)
	if err := ensureAgent(s, c, agent, body.Model, body.Instructions); err != nil {
		return err
	}

	// The machine half: launch a kind=bot machine. vm stamps hanzo-kind:bot and
	// bootstraps the @hanzo/bot runtime cloud-init for a bot spec (specIsBot).
	launch := map[string]any{"name": name, "size": size, "region": body.Region, "kind": "bot", "dryRun": false}
	var data json.RawMessage
	if err := s.State.cl.call(c, http.MethodPost, "/v1/machines/launch", q("owner", org), launch, &data); err != nil {
		return err
	}

	// vm returns {machine, quote[, meteringError]} — extract the launched machine.
	var wrap struct {
		Machine visorMachine `json:"machine"`
	}
	_ = json.Unmarshal(data, &wrap)
	if wrap.Machine.Name == "" && wrap.Machine.Id == "" {
		_ = json.Unmarshal(data, &wrap.Machine)
	}
	machineID := firstNonEmpty(wrap.Machine.Id, wrap.Machine.Name)
	if machineID == "" {
		return zip.Errorf(http.StatusBadGateway, "bot launch: vm returned no machine")
	}

	// Bind the (now-existing) cloud Agent to the freshly-launched machine. org is
	// the validated tenant (never a client field); agent defaults to the bot name.
	var binding agentBinding
	if err := s.State.cl.call(c, http.MethodPost, "/v1/machines/"+url.PathEscape(machineID)+"/bind-agent",
		q("owner", org),
		map[string]any{"org": org, "agentName": agent, "botVersion": body.BotVersion},
		&binding); err != nil {
		return err
	}
	return c.JSON(http.StatusCreated, toBotView(wrap.Machine, &binding))
}

// ensureAgent create-if-absent brings the bot's bound cloud Agent into being so a
// launched bot is immediately messageable. It self-calls the SAME POST /v1/agents
// the console uses — one create path, never a second store — forwarding the
// caller's validated identity so the agent is created in the caller's OWN org
// (IDOR-safe: the agents surface scopes the create by the same principal.Org).
// An empty model is passed through: agent-create fills the deployment default (a
// valid catalog model), so a bot launched without a model still runs.
//
// Idempotent: an agent that already exists (409) is reused, not an error — a
// relaunch, or an explicit agent shared by several bots, is fine. A genuine
// rejection (e.g. a non-catalog model → 400) is surfaced verbatim so a bad launch
// fails fast with the real reason, before any machine is provisioned.
func ensureAgent(s *cloud.Service[state], c *zip.Ctx, agent, model, instructions string) error {
	payload, err := json.Marshal(map[string]any{
		"name":         agent,
		"model":        strings.TrimSpace(model),
		"instructions": instructions,
	})
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "bots: encode agent-create: %v", err)
	}
	req, err := http.NewRequestWithContext(c.Context(), http.MethodPost, agentsBase()+"/v1/agents", bytes.NewReader(payload))
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "bots: build agent-create request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	for _, h := range selfIdentityHeaders {
		if v := c.Header(h); v != "" {
			req.Header.Set(h, v)
		}
	}
	resp, err := selfClient.Do(req)
	if err != nil {
		return zip.Errorf(http.StatusBadGateway, "bots: agent-create unreachable: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	rb, _ := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	switch resp.StatusCode {
	case http.StatusCreated, http.StatusConflict:
		return nil // created, or already exists (idempotent create-if-absent)
	default:
		// Surface the agent surface's own status + reason (e.g. a 400 for a
		// non-catalog model) so the launch fails fast with the real cause.
		return zip.Errorf(resp.StatusCode, "bots: agent-create %d: %s", resp.StatusCode, snippet(rb))
	}
}

func getBot(s *cloud.Service[state], c *zip.Ctx) error {
	org, id, err := botScope(s, c)
	if err != nil {
		return err
	}
	var m visorMachine
	if err := s.State.cl.call(c, http.MethodGet, "/v1/machines/"+url.PathEscape(id), q("owner", org), nil, &m); err != nil {
		return err
	}
	if m.Name == "" && m.Id == "" {
		return zip.ErrNotFound("bot not found")
	}
	// Attach the binding (best-effort). A machine is a Bot if it carries the
	// hanzo-kind:bot tag OR has an agent binding — either signal is authoritative,
	// so a bot resolves even before its cloud-init has stamped every tag.
	var binding agentBinding
	_ = s.State.cl.call(c, http.MethodGet, "/v1/machines/"+url.PathEscape(id)+"/agent-binding", q("owner", org), nil, &binding)
	if !machineIsBot(m) && !binding.identifies() {
		return zip.ErrNotFound("bot not found")
	}
	return c.JSON(http.StatusOK, toBotView(m, &binding))
}

func deleteBot(s *cloud.Service[state], c *zip.Ctx) error {
	org, id, err := botScope(s, c)
	if err != nil {
		return err
	}
	// Tear down both halves: unbind the agent first (best-effort — a bot with no
	// binding still deletes), then terminate the machine.
	_ = s.State.cl.call(c, http.MethodDelete, "/v1/machines/"+url.PathEscape(id)+"/agent-binding", q("owner", org), nil, nil)
	if err := s.State.cl.call(c, http.MethodDelete, "/v1/machines/"+url.PathEscape(id), q("owner", org), nil, nil); err != nil {
		return err
	}
	return c.NoContent(http.StatusNoContent)
}

// botAction dispatches /v1/bots/:id/:action. message routes to the AGENT path;
// stop and pause both halt the bot's agent runtime (one honest capability — see
// the package doc). An unknown action is a clean 400, never a silent no-op.
func botAction(s *cloud.Service[state], c *zip.Ctx) error {
	org, id, err := botScope(s, c)
	if err != nil {
		return err
	}
	switch strings.ToLower(strings.TrimSpace(c.Param("action"))) {
	case "message":
		return messageBot(s, c, org, id)
	case "stop", "pause":
		return stopBot(s, c, org, id)
	default:
		return zip.ErrBadRequest("unknown bot action (want stop|pause|message)")
	}
}

// stopBot halts the bot's runtime by unbinding its agent — the machine stays
// (re-bind to resume, or DELETE /v1/bots/:id to tear it down). Idempotent: a bot
// with no binding still reports stopped.
func stopBot(s *cloud.Service[state], c *zip.Ctx, org, id string) error {
	if err := s.State.cl.call(c, http.MethodDelete, "/v1/machines/"+url.PathEscape(id)+"/agent-binding", q("owner", org), nil, nil); err != nil {
		return err
	}
	return c.JSON(http.StatusOK, map[string]any{"id": id, "status": "stopped"})
}

// messageBot runs the bot's bound agent with the caller's message. It resolves
// the agent from the machine's binding, then forwards the body to the ONE agent
// runner (/v1/agents/:agent/run) so a message is a real agent run — recorded,
// billed and traced exactly like any other. The caller's identity is forwarded so
// the run is scoped + gated as the same principal (never a fabricated identity).
func messageBot(s *cloud.Service[state], c *zip.Ctx, org, id string) error {
	var binding agentBinding
	if err := s.State.cl.call(c, http.MethodGet, "/v1/machines/"+url.PathEscape(id)+"/agent-binding", q("owner", org), nil, &binding); err != nil {
		return err
	}
	agent := strings.TrimSpace(binding.AgentName)
	if agent == "" {
		return zip.ErrBadRequest("bot has no bound agent to message")
	}
	target := agentsBase() + "/v1/agents/" + url.PathEscape(agent) + "/run"
	req, err := http.NewRequestWithContext(c.Context(), http.MethodPost, target, bytes.NewReader(c.Body()))
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "bots: build agent request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	for _, h := range selfIdentityHeaders {
		if v := c.Header(h); v != "" {
			req.Header.Set(h, v)
		}
	}
	resp, err := selfClient.Do(req)
	if err != nil {
		return zip.Errorf(http.StatusBadGateway, "bots: agent path unreachable: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	rb, _ := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if ct := resp.Header.Get("Content-Type"); ct != "" {
		c.SetHeader("Content-Type", ct)
	}
	return c.Bytes(resp.StatusCode, rb)
}

// botScope validates the principal and extracts the bot id in one place, so every
// bot :id handler gates identically (403 before vm) and rejects an empty id.
func botScope(s *cloud.Service[state], c *zip.Ctx) (org, id string, err error) {
	org, ok := tenant(c)
	if !ok {
		return "", "", zip.ErrForbidden("X-Org-Id required")
	}
	id = strings.TrimSpace(c.Param("id"))
	if id == "" {
		return "", "", zip.ErrBadRequest("bot id required")
	}
	return org, id, nil
}

// ---- machine agent-binding proxies (thin, mirror vm exactly) ----

type bindAgentReq struct {
	AgentName  string `json:"agentName"`
	BotVersion string `json:"botVersion"`
}

func bindMachineAgent(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	id := strings.TrimSpace(c.Param("id"))
	if id == "" {
		return zip.ErrBadRequest("machine id required")
	}
	var body bindAgentReq
	if err := c.Bind(&body); err != nil {
		return err
	}
	if strings.TrimSpace(body.AgentName) == "" {
		return zip.ErrBadRequest("agentName is required")
	}
	// org is the validated tenant (never a client field) — vm records it as the
	// Agent's owning org and scopes the machine by ?owner.
	var binding agentBinding
	if err := s.State.cl.call(c, http.MethodPost, "/v1/machines/"+url.PathEscape(id)+"/bind-agent",
		q("owner", org),
		map[string]any{"org": org, "agentName": body.AgentName, "botVersion": body.BotVersion},
		&binding); err != nil {
		return err
	}
	return c.JSON(http.StatusOK, binding)
}

func getMachineAgentBinding(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	id := strings.TrimSpace(c.Param("id"))
	if id == "" {
		return zip.ErrBadRequest("machine id required")
	}
	var binding agentBinding
	if err := s.State.cl.call(c, http.MethodGet, "/v1/machines/"+url.PathEscape(id)+"/agent-binding", q("owner", org), nil, &binding); err != nil {
		return err
	}
	if !binding.identifies() {
		return zip.ErrNotFound("no agent binding for machine")
	}
	return c.JSON(http.StatusOK, binding)
}

func unbindMachineAgent(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	id := strings.TrimSpace(c.Param("id"))
	if id == "" {
		return zip.ErrBadRequest("machine id required")
	}
	if err := s.State.cl.call(c, http.MethodDelete, "/v1/machines/"+url.PathEscape(id)+"/agent-binding", q("owner", org), nil, nil); err != nil {
		return err
	}
	return c.NoContent(http.StatusNoContent)
}

func listAgentBindings(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	var bindings []agentBinding
	if err := s.State.cl.call(c, http.MethodGet, "/v1/agent-bindings", q("owner", org), nil, &bindings); err != nil {
		return err
	}
	if bindings == nil {
		bindings = []agentBinding{}
	}
	return c.JSON(http.StatusOK, map[string]any{"agentBindings": bindings})
}

// ---- agent path (self) ----

// selfIdentityHeaders are the gateway-minted identity a bot message forwards to
// the agent run so the run is scoped + gated as the SAME principal.
var selfIdentityHeaders = []string{
	"Authorization", "X-Org-Id", "X-User-Id", "X-User-Email", "X-Project-Id", "X-Environment",
}

// selfClient reaches the cloud binary's OWN agent surface. A bot message is a real
// agent run — the run path (records, billing, tracing) is not re-implemented here.
var selfClient = &http.Client{Timeout: 60 * time.Second}

// agentsBase is the base of the /v1/agents surface. In the unified binary the
// agents subsystem is mounted on THIS process's app listener, so the default is
// self (CLOUD_LISTEN :8000); CLOUD_AGENTS_URL overrides it (a split deploy, or a
// test's fake agents server).
func agentsBase() string {
	if v := strings.TrimSpace(os.Getenv("CLOUD_AGENTS_URL")); v != "" {
		return strings.TrimRight(v, "/")
	}
	return "http://127.0.0.1:8000"
}
