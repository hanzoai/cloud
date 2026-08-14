package cloud

// grant.go is the second half of the authorization rule, and the axis gate.go
// deliberately does not have.
//
// gate.go answers HOW MUCH AUTHORITY A CALLER HAS — Validated, Super, OrgAdmin.
// That is a property of the person. This answers HOW MUCH OF THAT AUTHORITY A
// PARTICULAR CREDENTIAL CARRIES, which is a property of the key, and the two are
// orthogonal: the same human holds a session that may reach everything and a
// deploy key that may reach one model. Spelling the second as a fourth Scope
// would say the holder is less privileged, which is false and would leak into
// every check that reads authority.
//
// A GRANT ONLY EVER NARROWS. There is no entry that adds reach, so a key can
// never carry more than its holder — the composed rule is
//
//	Scope.Admits(AuthorityOf(c)) && GrantOf(c).Covers(kind, name)
//
// and a mistake in a grant can cost availability, never privilege.
//
// EMPTY MEANS UNRESTRICTED, and that is a decision rather than an oversight: all
// eleven keys in the estate carry no reach today, so any other default silently
// revokes production on the deploy that ships this. Restriction is opt-in and
// visible in the key's own row.
//
// WHERE IT IS ASKED. At the site that knows the target, because only that site
// does: the model is known at the AI edge, the project at the project resolver,
// the product at the mount. One call each, never a second copy of the rule.

import (
	"fmt"
	"slices"
	"strings"
)

// Grant is the set of things one credential may reach, read off the key's scope.
//
// Two shapes share the field and each reader ignores the other's:
//
//	publish       a CLASS — this key has no secret half (schema.KeyScopePublish)
//	model:zen5    a REACH — kind and name, the question Covers answers
//
// They coexist because a publishable key is also a key that may be narrowed, and
// splitting them into two fields would make "which one wins" a question somebody
// has to answer at every read.
type Grant []string

// GrantClassPublish is the one CLASS entry, IAM's storage value for a key with no
// secret half. It is not a reach and Covers never consults it.
const GrantClassPublish = "publish"

// ParseGrant reads a key's scope. Comma-separated, blanks dropped, order
// irrelevant — it is a set.
func ParseGrant(scope string) Grant {
	var g Grant
	for _, part := range strings.Split(scope, ",") {
		if p := strings.TrimSpace(part); p != "" {
			g = append(g, p)
		}
	}
	return g
}

// GrantKinds is every kind a limit may name, and it is the CLOSED set of
// questions the platform actually asks. A kind absent here is refused at the
// mint rather than stored, because a limit nothing consults is worse than no
// limit at all: it reads as a narrowing in the listing and is one nowhere.
//
// Adding a kind means adding the Covers call that asks it, in the same change.
var GrantKinds = []string{"model", "project", "product"}

// ParseLimit reads the `kind:name` entries an operator wrote and REFUSES what
// cannot be enforced. Duplicates collapse; order does not survive, because a
// grant is a set.
func ParseLimit(entries []string) (Grant, error) {
	seen := map[string]bool{}
	var g Grant
	for _, raw := range entries {
		e := strings.TrimSpace(raw)
		if e == "" {
			continue
		}
		kind, name, ok := strings.Cut(e, ":")
		if !ok || strings.TrimSpace(name) == "" {
			return nil, fmt.Errorf("limit %q must be kind:name, one of %s", raw, strings.Join(GrantKinds, ", "))
		}
		if !slices.Contains(GrantKinds, kind) {
			return nil, fmt.Errorf("limit %q names %q, which nothing enforces; use one of %s", raw, kind, strings.Join(GrantKinds, ", "))
		}
		if e = kind + ":" + strings.TrimSpace(name); !seen[e] {
			seen[e] = true
			g = append(g, e)
		}
	}
	return g, nil
}

// String renders a grant as the one field IAM stores.
func (g Grant) String() string { return strings.Join(g, ",") }

// Publishable reports whether the key carries the publish CLASS.
func (g Grant) Publishable() bool {
	for _, e := range g {
		if e == GrantClassPublish {
			return true
		}
	}
	return false
}

// Reach is the narrowing half of the grant — every `kind:name` entry, with the
// classes left out. It is what a holder is shown and what an operator edits; the
// class is not a reach and never appears in it.
func (g Grant) Reach() []string {
	var out []string
	for _, e := range g {
		if strings.Contains(e, ":") {
			out = append(out, e)
		}
	}
	return out
}

// Covers reports whether this credential may reach kind/name.
//
// A grant that names NOTHING OF THIS KIND does not restrict this kind — a key
// limited to one project is not thereby limited to zero models. Restriction is
// per kind, so adding a kind at one call site cannot silently revoke a key that
// was written before that site existed.
//
// `kind:*` is the whole kind. An empty name is refused whenever the kind is
// restricted at all: the caller could not say what it was reaching, and a
// credential that names a limit must not be satisfied by silence.
func (g Grant) Covers(kind, name string) bool {
	prefix := kind + ":"
	restricted := false
	for _, e := range g {
		if !strings.HasPrefix(e, prefix) {
			continue
		}
		restricted = true
		switch v := e[len(prefix):]; v {
		case "*":
			return true
		case name:
			if name != "" {
				return true
			}
		}
	}
	return !restricted
}
