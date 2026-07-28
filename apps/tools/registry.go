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
	ErrChargerUnset = errors.New("tools: payment seam not configured")
)

// Charge is one settlement request for a monetized tool call. It is the whole
// contract the registry hands the x402 seam: who pays, who is paid, how much.
type Charge struct {
	Payer     string // paying org's billing ledger (principal Owner/Org).
	Project   string
	Tool      string
	Recipient string // seller payout wallet (Tool.Price.Recipient).
	Currency  string
	Cents     int64
	RequestID string
}

// Pricer resolves the marketplace price of a tool at dispatch time. It is the seam
// the marketplace fills so a monetized listing's price + recipient wallet reach the
// per-call enforcement path WITHOUT the tool plane importing the marketplace. A tool
// whose provider already set an intrinsic Price does not consult the Pricer.
type Pricer interface {
	PriceFor(ctx context.Context, scope Scope, tool string) *Price
}

// Charger settles a monetized tool call over the x402 payment rail (LP-3028,
// clients/commerce/payment/x402). A SEPARATE team owns the concrete implementation
// backed by x402.Facilitator.Settle (Payee = the recipient wallet address); the
// registry codes to THIS narrow interface and keeps the seam explicit so it never
// pulls the processor/MPC/chain graph into the tool plane. A nil Charger makes
// every priced tool fail closed (ErrChargerUnset) — a paid tool is never free.
//
// Charge returns nil on a settled payment, ErrPaymentRequired when the payer cannot
// pay, or another error (fail-closed) on an unavailable rail.
type Charger interface {
	Charge(ctx context.Context, ch Charge) error
}

// Registry is THE tool plane: the set of registered source Providers, the shared
// per-(org,project) activation store, and the x402 Charger seam. It composes
// providers and enforces the ONE dispatch policy (activation gate → price gate →
// dispatch); it knows nothing about how any source lists or runs a tool.
type Registry struct {
	mu         sync.RWMutex
	providers  []Provider
	activation *ActivationStore
	charger    Charger
	pricer     Pricer
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

// SetCharger installs the x402 payment seam on the process-wide registry. The x402
// wiring team calls this once; until it does, priced tools fail closed.
func SetCharger(c Charger) { std.SetCharger(c) }

// SetPricer installs the marketplace price seam on the process-wide registry.
func SetPricer(p Pricer) { std.SetPricer(p) }

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

// SetCharger installs the payment seam.
func (r *Registry) SetCharger(c Charger) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.charger = c
}

// SetPricer installs the marketplace price seam.
func (r *Registry) SetPricer(p Pricer) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pricer = p
}

// Activate turns a tool on for (org, project), recording its resolved source. This
// is the ONE activation write the marketplace "install" and the /v1/tools/activation
// API both drive, so activation is one store reached one way.
func (r *Registry) Activate(ctx context.Context, org, project, tool, byUser string) error {
	_, act, _, _ := r.snapshot()
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
	_, act, _, _ := r.snapshot()
	if act == nil {
		return errors.New("tools: activation store not configured")
	}
	return act.Deactivate(ctx, org, project, tool)
}

// Activated returns the activated tool names for (org, project).
func (r *Registry) Activated(ctx context.Context, org, project string) ([]string, error) {
	_, act, _, _ := r.snapshot()
	return act.List(ctx, org, project)
}

// Exists reports whether any source offers a tool by name to scope. Used by the
// marketplace to refuse a phantom listing/install.
func (r *Registry) Exists(ctx context.Context, scope Scope, name string) bool {
	_, _, ok := r.resolve(ctx, scope, name)
	return ok
}

// snapshot returns the current providers + activation + charger + pricer under one
// lock, so a List/Dispatch never races a concurrent Register/Set*.
func (r *Registry) snapshot() ([]Provider, *ActivationStore, Charger, Pricer) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	ps := make([]Provider, len(r.providers))
	copy(ps, r.providers)
	return ps, r.activation, r.charger, r.pricer
}

// List returns every tool offered to scope, deduped by name under source
// precedence (the lowest-rank source wins a collision), sorted by name. Each
// tool's Activated flag is filled from the activation store. A provider that errors
// is skipped (its tools are simply absent) — one failing source never blanks the
// whole plane.
func (r *Registry) List(ctx context.Context, scope Scope) []Tool {
	providers, act, _, _ := r.snapshot()
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
	providers, _, _, _ := r.snapshot()
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
//  3. PRICE gate — a priced tool settles through the x402 Charger seam or fails
//     closed (ErrChargerUnset / ErrPaymentRequired). A paid tool is never free.
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
	_, act, charger, pricer := r.snapshot()
	if !act.IsActivated(ctx, p.Org, p.Project, name) {
		return nil, ErrNotActivated
	}
	// Price: a provider-set intrinsic price wins; otherwise the marketplace Pricer
	// seam supplies a published listing's price + recipient wallet.
	price := tool.Price
	if price == nil && pricer != nil {
		price = pricer.PriceFor(ctx, scope, name)
	}
	if price != nil && price.AmountCents > 0 {
		if charger == nil {
			return nil, ErrChargerUnset
		}
		payer := p.Owner
		if payer == "" {
			payer = p.Org
		}
		cur := price.Currency
		if cur == "" {
			cur = "USD"
		}
		if err := charger.Charge(ctx, Charge{
			Payer: payer, Project: p.Project, Tool: name,
			Recipient: price.Recipient, Currency: cur, Cents: price.AmountCents,
		}); err != nil {
			if errors.Is(err, ErrPaymentRequired) {
				return nil, ErrPaymentRequired
			}
			return nil, err
		}
	}
	return provider.Dispatch(ctx, p, name, args)
}
