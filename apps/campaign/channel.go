package campaign

// channel.go is the orthogonality boundary of the GTM stack: a Campaign is a
// VALUE (store.go), a Channel is an EXECUTOR. The campaign orchestrator knows
// only this interface; it never imports ads / publish / marketing and never
// touches a credential. Each concrete channel CONSUMES its own connectors
// (paid → the ad connectors, organic → the social connectors, email → the email
// connectors) through the integrations.TokenFor custody seam, and is registered
// here at the composition root — the SAME injected-function pattern the coding
// dispatcher uses (coding.NewDispatcher(git.CloneURL, …)). One constructor,
// three registrations: the campaign package stays free of every executor's type.

import (
	"context"
	"sync"
)

// Kinds — the three orthogonal go-to-market channels. A campaign fans out to a
// subset of these; each is also usable standalone at its own /v1 surface
// (paid=/v1/ads, organic=/v1/publish, email=/v1/marketing).
const (
	KindPaid    = "paid"    // ad campaigns across Meta/Google/TikTok/… (the ad connectors)
	KindOrganic = "organic" // content syndication / social posts (the social connectors)
	KindEmail   = "email"   // drip / broadcast through the org's email provider (the email connectors)
)

// Plan is the org-scoped, channel-specific slice a campaign hands ONE executor at
// launch. It carries no credential — the executor resolves the org's connector
// token itself (integrations.TokenFor), so credential custody never crosses this
// seam. Variant is the A/B creative assigned for this fan-out (the utm_content
// the executor tags its events with); "" when the campaign runs a single creative.
type Plan struct {
	CampaignID  string
	Name        string
	Objective   string
	Platform    string   // paid→meta|google|tiktok|…; organic→x|instagram|…; email→(provider)
	Account     string   // provider account ref (ad-account id, page id, list id)
	Content     []string // creative(s); Content[0] is the active creative for this launch
	Variant     string   // A/B creative id (utm_content) assigned by the experiment seam, or ""
	BudgetCents int64
	ScheduleAt  int64 // unix; 0 = launch now
}

// Ref is an executor's durable handle on a launched execution — the provider-side
// id plus the platform/account needed to read spend or pause it later. The
// orchestrator records it onto the campaign's channel row and hands it back
// verbatim for Spend / Pause.
type Ref struct {
	Platform   string
	Account    string
	ExternalID string
	Status     string
	Detail     string
}

// The three executor operations, as primitive-typed function seams so a concrete
// channel package never has to import campaign to be registered.
type (
	LaunchFunc func(ctx context.Context, org string, p Plan) (Ref, error)
	SpendFunc  func(ctx context.Context, org string, ref Ref) (int64, error) // provider-reported spend, minor units (cents)
	PauseFunc  func(ctx context.Context, org string, ref Ref) error
)

// Channel is a go-to-market executor. Launch runs the campaign's slice on the
// channel's provider (via the org's connector token); Spend reads the
// provider-reported spend for a launched Ref; Pause stops it. Every method is
// org-scoped and fails closed when the org has not connected the channel's
// connector — the orchestrator records that honestly, never fabricates a launch.
type Channel interface {
	Kind() string
	Launch(ctx context.Context, org string, p Plan) (Ref, error)
	Spend(ctx context.Context, org string, ref Ref) (int64, error)
	Pause(ctx context.Context, org string, ref Ref) error
}

// channel is the ONE Channel implementation: a kind plus the three injected
// executor funcs. DRY — paid/organic/email differ only by kind and which funcs
// the composition root supplies. A nil func is treated as "unsupported"
// (errChannelUnsupported), never a nil-panic.
type channel struct {
	kind   string
	launch LaunchFunc
	spend  SpendFunc
	pause  PauseFunc
}

func (c channel) Kind() string { return c.kind }

func (c channel) Launch(ctx context.Context, org string, p Plan) (Ref, error) {
	if c.launch == nil {
		return Ref{}, errChannelUnsupported
	}
	return c.launch(ctx, org, p)
}

func (c channel) Spend(ctx context.Context, org string, ref Ref) (int64, error) {
	if c.spend == nil {
		return 0, errChannelUnsupported
	}
	return c.spend(ctx, org, ref)
}

func (c channel) Pause(ctx context.Context, org string, ref Ref) error {
	if c.pause == nil {
		return errChannelUnsupported
	}
	return c.pause(ctx, org, ref)
}

// NewChannel builds a Channel from a kind and its injected executor funcs. The
// composition root calls it once per channel (apps/wire_seams.go) with the
// concrete ads/publish/marketing execution funcs, then RegisterChannel-s it.
func NewChannel(kind string, launch LaunchFunc, spend SpendFunc, pause PauseFunc) Channel {
	return channel{kind: kind, launch: launch, spend: spend, pause: pause}
}

// ── registry ────────────────────────────────────────────────────────────────
//
// Open registry keyed by Kind (database/sql-driver idiom, same as flags.Register).
// The composition root registers the concrete executors at init; the orchestrator
// resolves them at launch. A kind with no registered executor is not an error —
// the fan-out records that channel "unavailable" and moves on (fail-soft), so a
// deployment that has not wired a channel degrades honestly instead of failing.

var (
	regMu sync.RWMutex
	reg   = map[string]Channel{}
)

// RegisterChannel installs a channel executor. Last registration for a kind wins
// (idempotent re-wire). Called from the composition root, never from the
// orchestrator.
func RegisterChannel(ch Channel) {
	if ch == nil {
		return
	}
	regMu.Lock()
	defer regMu.Unlock()
	reg[ch.Kind()] = ch
}

// resolveChannel returns the executor for a kind, if one is registered.
func resolveChannel(kind string) (Channel, bool) {
	regMu.RLock()
	defer regMu.RUnlock()
	ch, ok := reg[kind]
	return ch, ok
}

// registeredKinds reports which channel kinds have a wired executor — the
// summary surface shows it so an operator sees what this deployment can run.
func registeredKinds() []string {
	regMu.RLock()
	defer regMu.RUnlock()
	out := make([]string, 0, len(reg))
	for _, k := range []string{KindPaid, KindOrganic, KindEmail} {
		if _, ok := reg[k]; ok {
			out = append(out, k)
		}
	}
	return out
}
