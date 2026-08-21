package notify

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	ntypes "github.com/hanzoai/notify/pkg/types"
)

// This file is the DELIVERY BOUNDARY: what a provider must state about itself, and
// the registry of the providers that deliver. Nothing here names one.
//
// It used to name four, three times over. The same provider strings were switched at
// three separate sites — the per-channel default, the KMS key set, and a ~40-line
// constructor mirrored from notifyd's internal factory — so adding a provider meant
// finding three switches and getting the same four cases right in each, and a
// provider present in two of them and missing from the third failed silently at
// whichever site was forgotten. One provider is now ONE declaration, and each of the
// three questions is answered by reading a field of it.
//
// A new provider is a NEW FILE: a type's worth of declaration and a register() in its
// init(). Nothing in this file, in notify.go, or in the routes changes.

// provider is one delivery service Hanzo sends through.
type provider struct {
	// ID is the stable slug: the value a caller pins with `"provider": "twilio"`, the
	// KMS path segment its credentials live under, and the name the audit log records.
	ID string
	// Channels are the channels it delivers on. A send on a channel no provider
	// declares is refused rather than attempted.
	Channels []ntypes.Channel
	// Rank orders the providers of one channel when the caller pins none: the lowest
	// rank whose credentials are complete wins. It is a PREFERENCE and not a
	// capability — two providers of one channel both work, and this says which the
	// deployment reaches for first.
	Rank int
	// Keys is every credential read from KMS at orgs/<org>/notify/<id>/<key>. It is
	// the whole bag the provider may use, optional entries included.
	Keys []string
	// Needs is the subset of Keys without which delivery cannot happen. It is asked
	// TWICE and answered once: to pick a provider whose credentials are actually
	// present, and to refuse a send whose credentials are not.
	Needs []string
	// Open builds the delivering client for one send, targeting `to`. It is called
	// only after Needs is satisfied, so it validates nothing itself — the required
	// set is stated once, above, rather than restated in every constructor.
	Open func(c map[string]string, to []string) (notifier, error)
}

// serves reports whether p delivers on channel.
func (p *provider) serves(channel ntypes.Channel) bool {
	return slices.Contains(p.Channels, channel)
}

// providers is populated by each provider file's register() from its init(). Go
// initializes this map before any init() runs, so every provider is present by the
// time the first send resolves one.
var providers = map[string]*provider{}

// register adds a provider. A nil, unnamed, channel-less, constructor-less or
// duplicate provider is a programming error and panics at init.
func register(p *provider) {
	if p == nil || p.ID == "" || len(p.Channels) == 0 || p.Open == nil {
		panic("notify: register an incomplete provider")
	}
	if _, dup := providers[p.ID]; dup {
		panic("notify: duplicate provider id " + p.ID)
	}
	providers[p.ID] = p
}

// keysFor is the KMS key set for one provider — empty for a name nobody registered,
// so an unknown provider reads no credentials at all.
func keysFor(svc string) []string {
	if p, ok := providers[svc]; ok {
		return p.Keys
	}
	return nil
}

// forChannel is the providers that deliver on channel, in preference order: Rank,
// then id, so a deployment's choice does not depend on which file init() ran first.
func forChannel(channel ntypes.Channel) []*provider {
	var out []*provider
	for _, id := range slices.Sorted(maps.Keys(providers)) {
		if p := providers[id]; p.serves(channel) {
			out = append(out, p)
		}
	}
	slices.SortStableFunc(out, func(a, b *provider) int { return a.Rank - b.Rank })
	return out
}

// open builds the delivering client for one send. It fails CLOSED twice: a provider
// nobody registered, and one whose required credentials are not all present.
func open(svc string, c map[string]string, to []string) (notifier, error) {
	p, ok := providers[svc]
	if !ok {
		return nil, fmt.Errorf("notify: provider %q not wired in the cloud fold", svc)
	}
	if !hasKeys(c, p.Needs...) {
		return nil, fmt.Errorf("notify: %s requires %s", p.ID, strings.Join(p.Needs, ", "))
	}
	return p.Open(c, to)
}
