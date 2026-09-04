// bots.go mounts the bot-MACHINE surface (/v1/compute/bots) plus a
// machine's AGENT (/v1/compute/machines/agents, /v1/compute/machines/:id/agent). It
// is the SIBLING of
// machines: a bot machine is not a new
// state this subsystem owns, it is a composition of two things vm already owns — a
// kind=bot Machine and an AgentBinding. So every route here is a thin, org-scoped
// translation over the SAME Visor client the machines routes use (client.go),
// never a second store.
//
// The noun is the MACHINE that hosts a bot runtime — distinct from the bot RUN at
// /v1/bot (clients/bots), which is a task the runtime executes. Two values, two
// namespaces: this one nests under /v1/compute because what it rents you
// is compute.
//
// A bot machine = Agent (cloud /v1/agent) + Machine (vm, kind=bot) + the binding
// between them. Composition, one way per verb:
//
//	launch  = vm POST /v1/machines {kind:bot}         THEN vm PUT .../agent
//	list    = vm GET  /v1/machines?kind=bot           joined with the org's bindings
//	get     = vm GET  /v1/machines/:owner/:name       joined with its binding
//	delete  = vm DELETE .../agent (unbind)            THEN vm DELETE /v1/machines/:owner/:name
//	message = the AGENT path: run the bot's bound agent via /v1/agent/:agent/run
//	stop    = vm DELETE .../agent — halt the bot's @hanzo/bot runtime
//	pause   = the same halt: DigitalOcean/vm expose no VM-suspend primitive, so a
//	          bot's stop and pause are one honest capability (detach the agent
//	          runtime); powering the underlying machine off/on is a machine-lifecycle
//	          concern handled by launch/delete, not a fabricated bot state.
//
// Tenancy is identical to machines: the org is the VALIDATED principal
// (principal.Org, taken from the IAM owner claim), forwarded to vm as
// ?owner=<org>, so a caller can only ever read or mutate its OWN bots. No
// validated principal ⇒ 403, before anything reaches vm.

package compute

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/hanzoai/cloud/internal/environ"
	"io"
	"net/http"
	"net/url"
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
	// Owner is the tenant vm filed the binding under, resolved from the ?owner it
	// was called with — which is the caller's validated org and never a body field.
	Owner string `json:"owner,omitempty"`
	// Name is the binding's own key, which is the machine's id: a machine hosts at
	// most one agent, so the binding is named for it. This is the key a bots list
	// joins bindings onto machines by.
	Name string `json:"name,omitempty"`
	// MachineId is the bound machine as vm addresses it, owner-qualified
	// ("<org>/<machine>"). The unqualified half is what this surface's :id routes
	// take.
	MachineId string `json:"machineId,omitempty"`
	// Org is the Hanzo tenant the binding belongs to.
	Org string `json:"org,omitempty"`
	// AgentName is the cloud Agent (/v1/agent) this machine runs — the agent a
	// message to the bot is actually run against. It is the one field that decides
	// what the bot DOES.
	AgentName string `json:"agentName,omitempty"`
	// Provider is the cloud the bound machine runs on, carried here so a bindings
	// list says where each bot lives without a second read per machine.
	Provider string `json:"provider,omitempty"`
	// PublicIp is the bound machine's public address as vm recorded it on the
	// binding. Empty while the machine has none yet.
	PublicIp string `json:"publicIp,omitempty"`
	// BotVersion pins the @hanzo/bot runtime version the machine runs. Empty means
	// the machine took the default in force when it was bound.
	BotVersion string `json:"botVersion,omitempty"`
	// Status is the binding's lifecycle in VM's OWN words — "Pending" while the
	// machine provisions and the runtime is unconfirmed, "running" once vm has
	// confirmed it. The vocabulary is vm's and passes through unmapped, which is
	// why its capitalization does not match the machine states beside it, and it is
	// vm's reconciled reading rather than anything asserted here.
	Status string `json:"status,omitempty"`
	// Message is vm's human-readable detail on Status ("machine provisioning;
	// @hanzo/bot runtime not yet confirmed") — the reason behind the state, not a
	// second state.
	Message string `json:"message,omitempty"`
	// CreatedTime is when the binding was first made.
	CreatedTime string `json:"createdTime,omitempty"`
	// UpdatedTime is when vm last reconciled it — the age of Status.
	UpdatedTime string `json:"updatedTime,omitempty"`
}

// identifies reports whether a binding carries any real identity — used to tell a
// present binding from an empty (no-binding) zero value returned by vm.
func (b agentBinding) identifies() bool {
	return b.Name != "" || b.MachineId != "" || b.AgentName != ""
}

// botView is what /v1/compute/bots emits: the bot's machine (the clean machineView the
// console already consumes) with the bound agent surfaced. binding carries the
// honest, vm-reconciled lifecycle status when present.
func toBotView(m visorMachine, b *agentBinding) machineView {
	v := toMachineView(m)
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
	for t := range strings.SplitSeq(m.Tag, ",") {
		if strings.EqualFold(strings.TrimSpace(t), "hanzo-kind:bot") {
			return true
		}
	}
	return false
}

// ---- bots ----

// botRef addresses ONE bot machine.
type botRef struct {
	// ID is the bot machine's id — the same id the machines surface addresses it
	// by. Scoped to the caller's org upstream, so another tenant's id is 404.
	ID string `json:"id"`
}

// botLaunchReq is the POST /v1/compute/bots/launch body. A bot needs a machine size and,
// for a real launch, a name; agent is the cloud /v1/agent identity the bot runs
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

// launchBotWith is the bot half of POST /v1/compute/machines, entered when the
// body names kind "bot". It takes the ALREADY-PARSED body because its caller has
// bound the request once: binding twice would consume a body that is gone.
func launchBotWith(s *cloud.Service[state], c *zip.Ctx, org string, body botLaunchReq) error {
	size := cmp.Or(strings.TrimSpace(body.Size), strings.TrimSpace(body.InstanceType))
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
		if err := s.State.cl.call(c, http.MethodPost, "/v1/machines", q("owner", org), launch, &data); err != nil {
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
	// via /v1/agent/:agent/run, which Resolves it from the store and 404s "agent
	// not found" if it was never created (the gap this closes). Doing it before
	// the machine launch also fails a bad request (e.g. a non-catalog model → 400)
	// BEFORE any metered machine is provisioned. org is the validated tenant.
	agent := cmp.Or(strings.TrimSpace(body.Agent), name)
	if err := ensureAgent(s, c, agent, body.Model, body.Instructions); err != nil {
		return err
	}

	// The machine half: launch a kind=bot machine. vm stamps hanzo-kind:bot and
	// bootstraps the @hanzo/bot runtime cloud-init for a bot spec (specIsBot).
	launch := map[string]any{"name": name, "size": size, "region": body.Region, "kind": "bot", "dryRun": false}
	var data json.RawMessage
	if err := s.State.cl.call(c, http.MethodPost, "/v1/machines", q("owner", org), launch, &data); err != nil {
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
	machineID := cmp.Or(strings.TrimSpace(wrap.Machine.Id), strings.TrimSpace(wrap.Machine.Name))
	if machineID == "" {
		return zip.Errorf(http.StatusBadGateway, "bot launch: vm returned no machine")
	}

	// Bind the (now-existing) cloud Agent to the freshly-launched machine. org is
	// the validated tenant (never a client field); agent defaults to the bot name.
	var binding agentBinding
	if err := s.State.cl.op(c, http.MethodPut, machine(org, machineID)+"/agent",
		"",
		map[string]any{"agentName": agent, "botVersion": body.BotVersion},
		&binding); err != nil {
		return err
	}
	return c.JSON(http.StatusCreated, toBotView(wrap.Machine, &binding))
}

// ensureAgent create-if-absent brings the bot's bound cloud Agent into being so a
// launched bot is immediately messageable. It self-calls the SAME POST /v1/agent
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
	req, err := http.NewRequestWithContext(c.Context(), http.MethodPost, agentsBase()+"/v1/agent", bytes.NewReader(payload))
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

// botAction dispatches /v1/compute/bots/:id/:action. message routes to the AGENT path;
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
// (re-bind to resume, or DELETE /v1/compute/bots/:id to tear it down).
// Idempotent: a bot with no binding still reports stopped.
func stopBot(s *cloud.Service[state], c *zip.Ctx, org, id string) error {
	if err := s.State.cl.op(c, http.MethodDelete, machine(org, id)+"/agent", "", nil, nil); err != nil {
		return err
	}
	return c.JSON(http.StatusOK, map[string]any{"id": id, "status": "stopped"})
}

// messageBot runs the bot's bound agent with the caller's message. It resolves
// the agent from the machine's binding, then forwards the body to the ONE agent
// runner (/v1/agent/:agent/run) so a message is a real agent run — recorded,
// billed and traced exactly like any other. The caller's identity is forwarded so
// the run is scoped + gated as the same principal (never a fabricated identity).
func messageBot(s *cloud.Service[state], c *zip.Ctx, org, id string) error {
	var binding agentBinding
	// A machine with no binding is a 404 from vm, and it is not a fault here: the
	// honest answer is the 400 below, which says what the caller can do about it.
	// Anything else is a real upstream failure and is surfaced.
	if err := s.State.cl.op(c, http.MethodGet, machine(org, id)+"/agent", "", nil, &binding); err != nil && !notFound(err) {
		return err
	}
	agent := strings.TrimSpace(binding.AgentName)
	if agent == "" {
		return zip.ErrBadRequest("bot has no bound agent to message")
	}
	target := agentsBase() + "/v1/agent/" + url.PathEscape(agent) + "/run"
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
		return "", "", principal.Refused(c)
	}
	id = strings.TrimSpace(c.Param("id"))
	if id == "" {
		return "", "", zip.ErrBadRequest("bot id required")
	}
	return org, id, nil
}

// botOp is botScope for a TYPED op: the same gate, reading the id from the bound
// input rather than the route. Two entry points because a typed op is handed its
// id already decoded; one rule, stated once each way.
func botOp(ctx context.Context, in *botRef) (c *zip.Ctx, org, id string, err error) {
	c, org, err = scope(ctx)
	if err != nil {
		return nil, "", "", err
	}
	id = strings.TrimSpace(in.ID)
	if id == "" {
		return nil, "", "", zip.ErrBadRequest("bot id required")
	}
	return c, org, id, nil
}

// ---- a machine's agent (thin proxies over vm's binding surface) ----
//
// ONE address, both sides: /v1/compute/machines/agents and
// /v1/compute/machines/:id/agent, the method carrying the verb. vm answers the same
// shape under its own /v1/machines, so there is no translation left here to keep
// in step — it used to spell create at .../bind-agent and read/delete at
// .../agent-binding, and the two spellings were collapsed in vm and here together.
//
// These are vm's TYPED ops, so they go through cl.op and not cl.call: no
// envelope, 204 from the unbind, 404 from a read of a machine that runs no bot.
// A bind still resolves the machine at the provider, so it is the one of the
// four that can fail for a reason that is not the caller's.

// bindAgentReq marks a machine as running the @hanzo/bot runtime for a cloud Agent.
// ID is flat rather than an embedded machineRef because zip binds a path segment
// onto a TOP-LEVEL field only — nested, it would silently never bind.
type bindAgentReq struct {
	// ID is the machine to bind, from the URL path.
	ID string `json:"id"`
	// AgentName is the cloud Agent (/v1/agent) the machine will run. Required.
	AgentName string `json:"agentName"`
	// BotVersion pins the @hanzo/bot runtime version; empty takes the default.
	BotVersion string `json:"botVersion"`
}

// bindAgent binds a cloud Agent to one of the caller org's machines: the
// machine is recorded as running that Agent's @hanzo/bot runtime. The owning org is
// the validated tenant, never a client field.
//
// Example: {"agentName":"bot-a","botVersion":"1.4.0"}
// Response: {"machineId":"drop-a","agentName":"bot-a","status":"binding"}
func (o ops) bindAgent(ctx context.Context, in *bindAgentReq) (*agentBinding, error) {
	c, org, err := scope(ctx)
	if err != nil {
		return nil, err
	}
	id := strings.TrimSpace(in.ID)
	if id == "" {
		return nil, zip.ErrBadRequest("machine id required")
	}
	if strings.TrimSpace(in.AgentName) == "" {
		return nil, zip.ErrBadRequest("agentName is required")
	}
	// org is the validated tenant (never a client field), and it is passed ONCE —
	// as ?owner. vm derives the binding's owning org from that same resolved
	// principal, so a body field repeating it would be a second place to say one
	// thing and a field a caller could disagree with.
	var binding agentBinding
	if err := o.State.cl.op(c, http.MethodPut, machine(org, id)+"/agent",
		"",
		map[string]any{"agentName": in.AgentName, "botVersion": in.BotVersion},
		&binding); err != nil {
		return nil, err
	}
	return &binding, nil
}

// getAgent returns the agent binding of one of the caller org's
// machines, or 404 when the machine runs no bot runtime.
//
// Response: {"machineId":"drop-a","agentName":"bot-a","status":"running","botVersion":"1.4.0"}
func (o ops) getAgent(ctx context.Context, in *machineRef) (*agentBinding, error) {
	c, org, err := scope(ctx)
	if err != nil {
		return nil, err
	}
	id := strings.TrimSpace(in.ID)
	if id == "" {
		return nil, zip.ErrBadRequest("machine id required")
	}
	var binding agentBinding
	if err := o.State.cl.op(c, http.MethodGet, machine(org, id)+"/agent", "", nil, &binding); err != nil {
		// vm answers 404 for a machine that runs no bot. That is this route's own
		// answer too, said in this route's words rather than passed through with
		// vm's prose attached.
		if notFound(err) {
			return nil, zip.ErrNotFound("no agent binding for machine")
		}
		return nil, err
	}
	return &binding, nil
}

// unbindAgent detaches the agent runtime from one of the caller org's
// machines. The machine stays — this halts the bot, it does not terminate the
// compute. Answers 204.
func (o ops) unbindAgent(ctx context.Context, in *machineRef) (*cloud.Unit, error) {
	c, org, err := scope(ctx)
	if err != nil {
		return nil, err
	}
	id := strings.TrimSpace(in.ID)
	if id == "" {
		return nil, zip.ErrBadRequest("machine id required")
	}
	if err := o.State.cl.op(c, http.MethodDelete, machine(org, id)+"/agent", "", nil, nil); err != nil {
		return nil, err
	}
	return nil, nil
}

// bindingList is every agent↔machine binding in the org.
//
// It is the shape vm's list op ANSWERS with, not a re-wrapping of it: the same
// object with the same key, decoded once and handed on, so vm stays the single
// source of truth for the binding shape and this route adds no second one.
type bindingList struct {
	// AgentBindings is one row per bound machine, emitted verbatim as vm reports
	// it.
	AgentBindings []agentBinding `json:"agentBindings"`
}

// listAgents returns every agent↔machine binding in the caller's org — which
// machines are running which cloud Agent, with vm's own reconciled status.
//
// Response: {"agentBindings":[{"machineId":"drop-a","agentName":"bot-a","status":"running","publicIp":"1.2.3.4"}]}
func (o ops) listAgents(ctx context.Context, _ *cloud.Unit) (*bindingList, error) {
	c, org, err := scope(ctx)
	if err != nil {
		return nil, err
	}
	var out bindingList
	if err := o.State.cl.op(c, http.MethodGet, "/v1/machines/agents", q("owner", org), nil, &out); err != nil {
		return nil, err
	}
	if out.AgentBindings == nil {
		out.AgentBindings = []agentBinding{}
	}
	return &out, nil
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

// agentsBase is the base of the /v1/agent surface. In the unified binary the
// agents subsystem is mounted on THIS process's app listener, so the default is
// self (CLOUD_LISTEN :8000); CLOUD_AGENTS_URL overrides it (a split deploy, or a
// test's fake agents server).
func agentsBase() string {
	if v := environ.Or("CLOUD_AGENTS_URL", ""); v != "" {
		return strings.TrimRight(v, "/")
	}
	return "http://127.0.0.1:8000"
}
