package link

// select.go parses the per-request account SELECTION — which of the caller's
// linked accounts a request should route through — from the non-secret selectors a
// client may set. It is pure and total: identifiers in, a Selection out, no I/O.
//
// TENANCY IS NOT SELECTABLE. A Selection carries only (provider, profile). It has
// no org and no subject field, by construction — those come solely from the
// validated principal at the router boundary. So no selector a client controls can
// name another tenant's account; the worst a hostile selector achieves is naming
// an account the caller does not have, which resolves to "unavailable".

import "strings"

// Selection is the per-request account choice. A zero Account means "route across
// ALL of my linked accounts" (auto-failover); a set Account with Pinned=true means
// "use THIS account (and, for cycling, its provider's other profiles)".
type Selection struct {
	Account Account
	Pinned  bool
}

// auto is the empty selection: no account named, route across everything.
func auto() Selection { return Selection{} }

// ParseModelRef splits an openclaw-style model reference "model@provider:profile"
// into the bare model and the account it pins. It mirrors openclaw's
// `Opus@anthropic:work`:
//
//	"gpt-4o"              ⟹ model "gpt-4o",  no pin
//	"Opus@anthropic:work" ⟹ model "Opus",   pin anthropic:work
//	"Opus@anthropic"      ⟹ model "Opus",   pin anthropic (default profile)
//	"Opus@"               ⟹ model "Opus",   no pin (empty account is not a pin)
//
// Only the FIRST '@' splits, so a model id that itself contains '@' keeps
// everything after the provider:profile intact is not a concern here — provider
// and profile are single path-ish labels. Pure + total.
func ParseModelRef(ref string) (model string, sel Selection) {
	ref = trim(ref)
	at := strings.IndexByte(ref, '@')
	if at < 0 {
		return ref, auto()
	}
	model = trim(ref[:at])
	a, ok := ParseAccountRef(ref[at+1:])
	if !ok {
		return model, auto()
	}
	return model, Selection{Account: a, Pinned: true}
}

// ParseAccountRef parses a bare account selector "provider:profile" or "provider".
// The profile after the first ':' is optional; a leading/trailing-blank provider
// yields ok=false (nothing to pin). Only the first ':' splits, so a profile may
// contain ':' if a provider ever needs it. Pure + total.
func ParseAccountRef(s string) (Account, bool) {
	s = trim(s)
	if s == "" {
		return Account{}, false
	}
	provider, profile := s, ""
	if i := strings.IndexByte(s, ':'); i >= 0 {
		provider, profile = trim(s[:i]), trim(s[i+1:])
	}
	if provider == "" {
		return Account{}, false
	}
	return Account{Provider: provider, Profile: profile}, true
}

// SelectionFrom resolves a request's account selection from its non-secret
// selectors, in precedence order:
//
//  1. an explicit X-Provider-Account header ("provider:profile") — the per-request
//     override,
//  2. the model reference's "@provider:profile" suffix — the openclaw inline form,
//  3. a session pin ("provider:profile") the caller set earlier — the sticky
//     default that mirrors openclaw's `Opus@anthropic:work` staying selected.
//
// It returns the BARE model (the "@…" stripped) and the Selection. None of the
// three inputs is org/subject; the router still binds tenancy from the principal.
func SelectionFrom(header, modelRef, sessionPin string) (model string, sel Selection) {
	model, fromModel := ParseModelRef(modelRef)
	if a, ok := ParseAccountRef(header); ok {
		return model, Selection{Account: a, Pinned: true}
	}
	if fromModel.Pinned {
		return model, fromModel
	}
	if a, ok := ParseAccountRef(sessionPin); ok {
		return model, Selection{Account: a, Pinned: true}
	}
	return model, auto()
}

// matches reports whether a linked account satisfies this selection. An auto
// selection matches everything; a pinned selection matches when the provider
// agrees AND (no profile was named, so any of that provider's accounts qualifies —
// the cycling set — OR the named profile equals the account's label). The account
// label on a Link is its profile in this model.
func (s Selection) matches(l Link) bool {
	if !s.Pinned || s.Account.isZero() {
		return true
	}
	if !strings.EqualFold(trim(s.Account.Provider), trim(l.Provider)) {
		return false
	}
	prof := trim(s.Account.Profile)
	if prof == "" {
		return true // provider named, profile open → route across that provider's accounts
	}
	return strings.EqualFold(prof, trim(l.Account)) || strings.EqualFold(prof, defaultProfile) && trim(l.Account) == ""
}
