package benchmark

// Presets: "design your own router blend." A preset is a named arm-set + rank — the
// same shape the enso tiers take in the family catalog, but user-authored and
// named under enso-<name>. A caller composes one from the arena's measured
// leaderboard (pick the arms that win YOUR tasks). What a Hanzo-served tier is
// composed of stays ours; a preset publishes only what its author put in it.
//
// This surface STORES preset definitions, per org and by name: a blend posted
// here is its org's until the org posts the same name again, which replaces it.
// It does not serve them. The model layer serves Hanzo's own enso tiers and
// resolves no stored preset, so served_as is the name a blend would be resolved
// under, not one that answers today.
//
// THE NAME IS ALWAYS PREFIXED, AND A BARE NAME IS NOT EXPRESSIBLE. A preset's name
// is the part after enso-, and that prefix is the namespace that keeps a blend
// from shadowing a model: a bare `gpt-5.5` or `zen5-pro` would read as the model
// it names to every router that resolves it. For the same reason a name is never
// one of the tiers Hanzo itself serves under the prefix (enso-auto, enso-flash,
// enso-pro, enso-max, enso-ultra, enso-embed), nor free, since no model is named
// for a price, and never begins with enso- — that would be served as enso-enso-….

import (
	"context"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/zap-proto/zip"
)

// Preset is one org's router blend. Arms are catalog model ids (or BYO); rank is
// the escalation order (probe = rank[0], panel = rank[:panel]); panel bounds
// fan-out.
type Preset struct {
	Name  string   `json:"name"`           // the part after enso-: lowercase letters, digits and inner hyphens, at most 64
	Owner string   `json:"owner"`          // the org the blend belongs to: the writer's verified org, never a value sent
	Arms  []string `json:"arms"`           // the blend — model ids from the arena
	Rank  []string `json:"rank"`           // escalation order over arms
	Panel int      `json:"panel"`          // fan-out width (>=1)
	Note  string   `json:"note,omitempty"` // why this blend (audit)
	// At is when this version was stored. Set by the server; a value sent is not
	// read. Zero on the reference blend.
	At time.Time `json:"at,omitempty"`
	// By is the verified user who stored it. Set by the server; a value sent is
	// not read.
	By string `json:"by,omitempty"`
}

// The preset bounds: an org holds at most maxPresets names and stores at most
// maxPresetWrites versions per UTC day, because every version lands in the one
// file every preset read scans; a blend holds at most maxArms arms of at most
// maxClaimName characters, and a note at most maxNote.
const (
	maxPresets      = 100
	maxPresetWrites = 500
	maxArms         = 32
	maxNote         = 1024
)

// presetName is a name as the model layer would carry it inside enso-<name>.
var presetName = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,62}[a-z0-9])?$`)

// tiers are the names Hanzo serves under enso- itself; no preset takes one.
var tiers = map[string]bool{"auto": true, "flash": true, "pro": true, "max": true, "ultra": true, "free": true, "embed": true}

// presetList is the answer to a preset read.
type presetList struct {
	// Data is the reference blend, then the caller's org's stored blends by name.
	Data []Preset `json:"data"`
}

// presetStored is the answer to a composed blend: what was stored, and the name
// it would be resolved under.
type presetStored struct {
	// ServedAs is enso-<name>: the model id a router would resolve this blend
	// under.
	ServedAs string `json:"served_as"`
	// Preset is the blend as stored: defaults filled in, Owner the caller's
	// verified org, At and By the server's.
	Preset Preset `json:"preset"`
	// Note says what storing does and does not promise.
	Note string `json:"note"`
}

// presets are the router blends available to compose from — a named set of model
// arms, the rank they escalate through and the panel width that bounds fan-out —
// each named for the model layer as enso-<name>.
//
// The first row is the reference blend: a worked example written in models we
// name, published as an example of the FORM. It is deliberately not the
// composition of a Hanzo-served tier — the tier name exists to abstract that — so
// fork it and swap arms by what the leaderboard measures on your own tasks rather
// than reading it as a disclosure.
//
// The rows after it are the caller's org's stored blends, by name, the latest
// version of each. Anyone reads the reference; only a member of the org reads the
// org's own, and a SuperAdmin acting in an org it does not belong to reads what a
// stranger reads.
func (o ops) presets(ctx context.Context, _ *noIn) (*presetList, error) {
	out := []Preset{referenceBlend()}
	if org, member := memberOf(ctx); member && o.s.State.store != nil {
		versions, err := o.s.State.store.Versions(ctx, org)
		if err != nil {
			return nil, o.gap(err)
		}
		out = append(out, latest(versions)...)
	}
	return &presetList{Data: out}, nil
}

// compose stores a router blend for the caller's org — its name, its arms, the
// rank they escalate through and the panel fan-out width — and answers with the
// stored blend and the enso-<name> it would be resolved under.
//
// Storing is not serving: the model layer resolves no stored preset, and the
// answer's note says so.
//
// Any signed-in caller may compose; a caller with no verified principal is refused
// 401. Owner, At and By are the server's, whatever the body says, so a blend is
// never stored as another org's. Posting a name the org already holds replaces
// that blend; an org holds at most 100 names and stores at most 500 versions per
// UTC day (429 past that).
//
// Defaults fill the shape rather than refusing it: an omitted rank becomes the arms
// in declared order and a panel below 1 becomes 1. A blend with no name or no arms
// is a 400. A name is the part after enso-: 1 to 64 lowercase letters, digits and
// inner hyphens, never one of Hanzo's own tiers (auto, flash, pro, ultra, free,
// embed) and never beginning with enso-, or it is a 422. So is a blend of more
// than 32 arms, an arm named twice or past 128 characters, a rank naming an arm
// the blend does not declare, a panel wider than the rank, or a note past 1024
// characters.
//
// Example: {"name": "research", "arms": ["enso-ultra", "zen5-pro"], "panel": 2}
func (o ops) compose(ctx context.Context, in *Preset) (*presetStored, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	p := *in
	p.Owner, p.By, p.At = org, strings.TrimSpace(cloud.Who(ctx).User), time.Now().UTC()
	p.Name = strings.TrimSpace(p.Name)
	if p.Name == "" || len(p.Arms) == 0 {
		return nil, zip.ErrBadRequest("preset needs a name and at least one arm")
	}
	if p.Panel < 1 {
		p.Panel = 1
	}
	if len(p.Rank) == 0 {
		p.Rank = p.Arms
	}
	if err := checkPreset(p); err != nil {
		return nil, err
	}
	store := o.s.State.store
	if store == nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "benchmark: preset store unavailable")
	}
	// The day's count, the name bound and the version are one transaction under the
	// store's write lock.
	err = store.write(ctx, func(b book) error {
		versions, err := b.Versions(ctx, org)
		if err != nil {
			return err
		}
		day, today := p.At.Truncate(24*time.Hour), 0
		for _, v := range versions {
			if !v.At.Before(day) {
				today++
			}
		}
		if today >= maxPresetWrites {
			return zip.Errorf(http.StatusTooManyRequests, "benchmark: org %s has stored %d of its %d preset versions today (UTC)", org, today, maxPresetWrites)
		}
		held := latest(versions)
		if len(held) >= maxPresets && !holds(held, p.Name) {
			return zip.Errorf(http.StatusUnprocessableEntity, "org %s holds %d presets, the most an org may; replace one by posting its name", org, len(held))
		}
		return b.PutPreset(ctx, p)
	})
	if isRefusal(err) {
		return nil, err
	}
	if err != nil {
		o.s.Log.Error("benchmark: preset not stored", "org", org, "name", p.Name, "err", err)
		return nil, zip.Errorf(http.StatusInternalServerError, "benchmark: the preset was not stored")
	}
	return &presetStored{
		ServedAs: "enso-" + p.Name, Preset: p,
		Note: "stored for your org; the model layer serves Hanzo's enso tiers and resolves no stored preset, so served_as is the name this blend would be resolved under.",
	}, nil
}

// checkPreset is compose's 422s, in the order the doc states them.
func checkPreset(p Preset) error {
	switch {
	case strings.HasPrefix(p.Name, "enso-"):
		return zip.Errorf(http.StatusUnprocessableEntity, "a name is the part after enso-: %q would be served as enso-%s", p.Name, p.Name)
	case !presetName.MatchString(p.Name):
		return zip.Errorf(http.StatusUnprocessableEntity, "a name is 1 to 64 lowercase letters, digits and inner hyphens")
	case tiers[p.Name]:
		return zip.Errorf(http.StatusUnprocessableEntity, "enso-%s is a tier Hanzo serves; name the blend something else", p.Name)
	case len(p.Arms) > maxArms:
		return zip.Errorf(http.StatusUnprocessableEntity, "a blend holds at most %d arms", maxArms)
	case utf8.RuneCountInString(p.Note) > maxNote:
		return zip.Errorf(http.StatusUnprocessableEntity, "a note is at most %d characters", maxNote)
	}
	arms := map[string]bool{}
	for _, a := range p.Arms {
		switch {
		case strings.TrimSpace(a) == "" || overlong(a):
			return zip.Errorf(http.StatusUnprocessableEntity, "an arm is a model id of 1 to %d characters", maxClaimName)
		case arms[a]:
			return zip.Errorf(http.StatusUnprocessableEntity, "arm %q is named twice", a)
		}
		arms[a] = true
	}
	// Rank may only name declared arms — the same invariant the model catalog
	// enforces.
	var bad []string
	for _, r := range p.Rank {
		if !arms[r] {
			bad = append(bad, r)
		}
	}
	if len(bad) > 0 {
		return zip.Errorf(http.StatusUnprocessableEntity, "rank references undeclared arms: %s", strings.Join(bad, ", "))
	}
	if p.Panel > len(p.Rank) {
		return zip.Errorf(http.StatusUnprocessableEntity, "panel %d is wider than the rank's %d arms", p.Panel, len(p.Rank))
	}
	return nil
}

// holds reports whether ps holds a preset named name.
func holds(ps []Preset, name string) bool {
	for _, p := range ps {
		if p.Name == name {
			return true
		}
	}
	return false
}

// referenceBlend is the worked example a user forks — a blend written in models
// we name: Hanzo's own, plus the frontier models resold under their own brand.
//
// It is deliberately NOT the composition of enso-ultra. Publishing which bases
// serve an enso tier is the Zen mapping, and the enso name exists precisely to
// abstract it; a shipped preset is an example of the FORM, not a disclosure.
func referenceBlend() Preset {
	return Preset{
		Name:  "ultra",
		Owner: "hanzo",
		Arms:  []string{"enso-ultra", "zen5-pro", "x-ai/grok-4.5", "anthropic-claude-opus-4.8", "google/gemini-3.1-pro", "openai-gpt-5.6-sol"},
		Rank:  []string{"enso-ultra", "x-ai/grok-4.5", "anthropic-claude-opus-4.8"},
		Panel: 3,
		Note:  "example blend; fork it and swap arms by your measured leaderboard.",
	}
}

// latest is the newest version of each name in versions, by name.
func latest(versions []Preset) []Preset {
	newest := map[string]Preset{}
	for _, p := range versions {
		if prev, ok := newest[p.Name]; !ok || !p.At.Before(prev.At) {
			newest[p.Name] = p
		}
	}
	out := make([]Preset, 0, len(newest))
	for _, p := range newest {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
