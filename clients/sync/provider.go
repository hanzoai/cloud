package sync

import "context"

// provider.go defines the engine↔provider contract. The engine (engine.go) is
// generic: it resolves Syncs, guards loops, dedupes by cursor, and propagates
// chains — it knows NOTHING about git, storage, or db. A PROVIDER per kind supplies
// the semantics through ONE method: Reconcile drives the two endpoints toward
// agreement for an event. Reconcile is idempotent by construction (already in
// agreement ⇒ no change), so there is no plan/apply split — you converge, and
// report whether the target moved. Git is the first provider (git_provider.go);
// adding "storage"/"db" later is a new Provider at its own kind, nothing in the
// engine changes.

// Event is the provider-agnostic trigger the engine hands a provider. Branch/After
// are the generic cursor position (git: branch + tip SHA); other kinds map them to
// their own partition + version token.
type Event struct {
	Provider string // endpoint the event came from: "github" | "gitlab" | "hanzo-git"
	Org      string
	Locator  string // source repo locator (clone URL or "<org>/<repo>")
	Repo     string // short repo name (git)
	Branch   string // cursor position key
	Before   string
	After    string // cursor position value (git: the tip SHA)
	Actor    string // who made the upstream change (loop guard compares to Sync.Actor)
	Token    string // OPTIONAL pre-minted credential (never logged)
	Manual   bool   // manual /run or initial reconcile (no specific position)
	Hop      int
}

// Provider reconciles one Sync's endpoints toward agreement for a kind. ONE method
// — Reconcile converges (drives source→target, or ensures the mirror, per the
// Sync's direction) and returns whether it CHANGED the target, so the engine
// advances the cursor and fires the chain. Stateless: every call carries the full
// Sync + event, so one registered value serves every org.
type Provider interface {
	Kind() string
	Reconcile(ctx context.Context, sy Sync, ev Event) (changed bool, err error)
}

// providers is the kind→Provider registry, written once at Mount (before serving),
// read on the hot path — a plain map, like the git plane's registries.
var providers = map[string]Provider{}

// registerProvider installs a provider for its kind. One provider per kind.
func registerProvider(p Provider) { providers[p.Kind()] = p }

// providerFor returns the provider for kind, or nil when none is registered (the
// engine then skips the sync fail-closed rather than guess).
func providerFor(kind string) Provider { return providers[kind] }
