package tools

import (
	"context"
	"errors"
	"sort"
	"sync"
)

// Sentinel dispatch outcomes. The HTTP layer maps these to status codes: a
// not-activated tool is 403 (the tool plane never serves an unactivated tool), an
// unknown tool is 404-in-JSON-RPC, payment-required is 402, not-dispatchable is 422.
var (
	// ErrNotActivated — the tool exists for the scope but is not activated for this
	// (org,project). The activation API (task 5) toggles it on.
	ErrNotActivated = errors.New("tools: not activated for this org/project")
	// ErrUnknownTool — no source offers a tool by this name for the scope.
	ErrUnknownTool = errors.New("tools: unknown tool")
	// ErrNotDispatchable — the tool is discovery/activation-only (e.g. a skill).
	ErrNotDispatchable = errors.New("tools: not dispatchable")
	// ErrPaymentRequired — a priced tool could not be settled through the Charger.
	ErrPaymentRequired = errors.New("tools: payment required")
	// ErrChargerUnset — a priced tool was called but no x402 Charger is wired; the
	// call fails CLOSED (a paid tool is never served free).
	ErrChargerUnset = errors.New("tools: payment client not configured")
)

// Charger settles one tool call over the x402 payment rail. The tool plane hands it
// the TOOL NAME and the request context and NOTHING else, because nothing else is
// the tool plane's to know: who pays is the attested principal already on the
// context, and what a call costs and who is paid live in the payment layer's own
// price table — the marketplace listing the x402 Registry is published from. A
// Charge value carrying cents and a payout wallet would be the commerce graph
// smuggled into the tool plane, and a payer passed down would be a second answer to
// a question principal.Ledger already answers.
//
// Every dispatch is offered to the Charger, including free ones: "is this priced"
// is one lookup in that same table, and asking twice is how a gate and a settlement
// come to disagree. A FREE tool settles for nothing and returns nil.
//
// Charge returns nil once the call is paid for, ErrPaymentRequired when it is not
// (the x402 challenge is on the response headers), or another error on an
// unavailable rail. A nil Charger falls back to the internal plane
// (charge_peer.go), which is how the shipped fleet — one binary per app — reaches a
// rail that is never in this process; only a deployment with neither a rail nor a
// price table in it answers ErrChargerUnset, and there a tool with a DECLARED price
// fails closed. A paid tool is never served free on any of those paths.
type Charger interface {
	Charge(ctx context.Context, tool string) error
}

// Registry is THE tool plane: the set of registered source Providers, the shared
// per-(org,project) activation store, and the x402 Charger client. It composes
// providers and enforces the ONE dispatch policy (activation gate → price gate →
// dispatch); it knows nothing about how any source lists or runs a tool.
type Registry struct {
	mu         sync.RWMutex
	providers  []Provider
	activation *ActivationStore
	charger    Charger
}

// NewRegistry builds an empty registry. The process-wide one is std (see Default);
// tests build their own with fake providers.
func NewRegistry() *Registry { return &Registry{} }

// std is the process-wide registry. Sources call the package-level Register from
// their Mount; the tools subsystem's Mount installs the activation store and the
// HTTP surface reads std. Marketplace resolves it via Default.
var std = NewRegistry()

// Register adds a source Provider to the process-wide registry. Each source calls
// this ONCE from its Mount (only enabled subsystems mount, so a disabled source is
// simply absent). Order does not matter: List/Dispatch run at request time.
func Register(p Provider) { std.Register(p) }

// Default returns the process-wide registry (for marketplace + the HTTP surface).
func Default() *Registry { return std }

// SetCharger installs the x402 payment client on the process-wide registry. The
// subsystem that owns the price table calls this once from its Mount
// (apps/marketplace); until it does, a tool with a declared price fails closed.
func SetCharger(c Charger) { std.SetCharger(c) }

// Register adds a provider. Duplicate sources are allowed (each lists its own
// tools); precedence resolves any name collision across sources.
func (r *Registry) Register(p Provider) {
	if p == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.providers = append(r.providers, p)
}

// SetActivation installs the activation store (called by tools.Mount once DataDir
// is known). A nil store fails every dispatch closed (nothing is activated).
func (r *Registry) SetActivation(a *ActivationStore) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.activation = a
}

// SetCharger installs the payment client.
func (r *Registry) SetCharger(c Charger) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.charger = c
}

// Activate turns a tool on for (org, project), recording its resolved source. This
// is the ONE activation write the marketplace "install" and the /v1/tools/activation
// API both drive, so activation is one store reached one way.
func (r *Registry) Activate(ctx context.Context, org, project, tool, byUser string) error {
	_, act, _ := r.snapshot()
	if act == nil {
		return errors.New("tools: activation store not configured")
	}
	src := Source("")
	if t, _, ok := r.resolve(ctx, Scope{Org: org, Project: project}, tool); ok {
		src = t.Source
	}
	return act.Activate(ctx, org, project, tool, src, byUser)
}

// Deactivate turns a tool off for (org, project).
func (r *Registry) Deactivate(ctx context.Context, org, project, tool string) error {
	_, act, _ := r.snapshot()
	if act == nil {
		return errors.New("tools: activation store not configured")
	}
	return act.Deactivate(ctx, org, project, tool)
}

// Activated returns the activated tool names for (org, project).
func (r *Registry) Activated(ctx context.Context, org, project string) ([]string, error) {
	_, act, _ := r.snapshot()
	return act.List(ctx, org, project)
}

// Exists reports whether any source offers a tool by name to scope. Used by the
// marketplace to refuse a phantom listing/install.
func (r *Registry) Exists(ctx context.Context, scope Scope, name string) bool {
	_, _, ok := r.resolve(ctx, scope, name)
	return ok
}

// snapshot returns the current providers + activation + charger under one lock, so
// a List/Dispatch never races a concurrent Register/Set*.
func (r *Registry) snapshot() ([]Provider, *ActivationStore, Charger) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	ps := make([]Provider, len(r.providers))
	copy(ps, r.providers)
	return ps, r.activation, r.charger
}

// List returns every tool offered to scope, deduped by name under source
// precedence (the lowest-rank source wins a collision), sorted by name. Each
// tool's Activated flag is filled from the activation store. A provider that errors
// is skipped (its tools are simply absent) — one failing source never blanks the
// whole plane.
func (r *Registry) List(ctx context.Context, scope Scope) []Tool {
	providers, act, _ := r.snapshot()
	winners := map[string]Tool{} // name -> winning tool
	for _, p := range providers {
		tools, err := p.List(ctx, scope)
		if err != nil {
			continue
		}
		for _, t := range tools {
			if t.Source == "" {
				t.Source = p.Source()
			}
			if cur, ok := winners[t.Name]; ok && rank(cur.Source) <= rank(t.Source) {
				continue // an equal-or-higher-precedence source already owns this name.
			}
			winners[t.Name] = t
		}
	}
	out := make([]Tool, 0, len(winners))
	for _, t := range winners {
		t.Activated = act.IsActivated(ctx, scope.Org, scope.Project, t.Name)
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// resolve returns the winning tool + its owning provider for a name in scope,
// honoring precedence. ok=false ⇒ ErrUnknownTool.
func (r *Registry) resolve(ctx context.Context, scope Scope, name string) (Tool, Provider, bool) {
	providers, _, _ := r.snapshot()
	var best Tool
	var bestP Provider
	found := false
	for _, p := range providers {
		tools, err := p.List(ctx, scope)
		if err != nil {
			continue
		}
		for _, t := range tools {
			if t.Name != name {
				continue
			}
			if t.Source == "" {
				t.Source = p.Source()
			}
			if !found || rank(t.Source) < rank(best.Source) {
				best, bestP, found = t, p, true
			}
		}
	}
	return best, bestP, found
}

// Dispatch is the ONE per-principal dispatch path, enforcing the ONE policy:
//
//  1. resolve the winning tool (precedence) — ErrUnknownTool if none.
//  2. ACTIVATION gate — the tool MUST be activated for (org,project) or ErrNotActivated (403).
//  3. PAYMENT gate — every call is offered to the x402 client, which owns the price
//     table: a free tool settles for nothing, a priced one settles or the call fails
//     closed (ErrPaymentRequired). With NO rail REACHABLE the client asks the table
//     instead (charge_peer.go), and only a deployment holding neither reaches
//     ErrChargerUnset, where a tool that DECLARES a price fails closed — a paid tool
//     is never served free.
//  4. dispatch to the winning source's provider, bound to the principal.
//
// The scope is the principal's own (org, project) — a caller can only ever dispatch
// its own tools. Metering + audit are the HTTP layer's job (one unit per call).
func (r *Registry) Dispatch(ctx context.Context, p Principal, name string, args map[string]any) (any, error) {
	scope := Scope{Org: p.Org, Project: p.Project}
	tool, provider, ok := r.resolve(ctx, scope, name)
	if !ok {
		return nil, ErrUnknownTool
	}
	_, act, charger := r.snapshot()
	if !act.IsActivated(ctx, p.Org, p.Project, name) {
		return nil, ErrNotActivated
	}
	switch err := charge(ctx, charger, name); {
	case err == nil:
	case errors.Is(err, ErrPaymentRequired):
		return nil, ErrPaymentRequired
	case errors.Is(err, ErrChargerUnset):
		// Neither a payment rail nor a price table anywhere this deployment can reach,
		// so nothing here is for sale and the row's own declaration is the last word.
		// A tool that declares a price is refused; one that declares none is free by
		// its own statement, not by our silence.
		if tool.Price != nil && tool.Price.Amount.Sign() > 0 {
			return nil, ErrChargerUnset
		}
	default:
		return nil, err
	}
	return provider.Dispatch(ctx, p, name, args)
}

// charge settles one call through the payment client: the installed Charger when the
// subsystem that owns the price table is in this process, the internal plane when it
// is not (charge_peer.go). ONE policy, two transports — which one answers is a
// deployment fact, never a difference in what is enforced.
//
// A registry with neither — the bare one a unit test builds — answers ErrChargerUnset
// directly, which is the same fact chargePeer reports for a deployment that holds no
// x402 and no price table, so Dispatch has ONE case for "nothing here sells anything"
// rather than one per reason.
func charge(ctx context.Context, c Charger, tool string) error {
	if c != nil {
		return c.Charge(ctx, tool)
	}
	return chargePeer(ctx, tool)
}
