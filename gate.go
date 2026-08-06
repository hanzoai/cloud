package cloud

// gate.go is the ONE authorization gate of the data plane.
//
// HIP-0519 §"The one predicate set": every authorization check reads the SAME
// three predicates — principal.Validated (a principal is present at all),
// principal.IsSuperAdmin (platform sudo, owner == the admin org), and
// principal.IsOrgAdmin (admin of one's OWN org). A subsystem that spells its own
// gate is a second rule, and two rules for one fact drift apart; seven of them
// drift seven ways.
//
// TWO VALUES, ONE RULE. Authority is what a caller HAS, reduced to exactly those
// three facts. Scope is what a route REQUIRES. Admits is the whole rule, in one
// expression, over the two — and where it admits either admin scope it writes
// `Super || OrgAdmin` explicitly, so the superset is visible AT the gate rather
// than hidden inside a predicate. Guard is that rule's standard application to a
// handler.
//
// WHAT IS NOT HERE. Authentication: identity is verified exactly once, at the
// edge, against IAM (middleware_identity.go); this reads the assertion that
// minted and answers an authorization question about it. Nor anything a
// subsystem does BESIDES authorization — store readiness, key syntax, per-op
// billing, the shape of a refusal, the tenant confinement applied after the door
// opens. Those compose AROUND the gate, so each app keeps its own business and
// none of them keeps a copy of the rule.

import (
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/zap-proto/zip"
)

// Authority is a caller reduced to the three facts an authorization check may
// read. The two admin bits stay APART — conflating them is a privilege
// escalation: Super is cross-tenant platform sudo, OrgAdmin administers only its
// own org and is never platform-privileged.
type Authority struct {
	Validated bool // a credential was verified — the identity middleware MINTED X-User-Id
	Super     bool // platform sudo: a member of the reserved admin org
	OrgAdmin  bool // admin OF ITS OWN org — org-scoped, self-service
}

// AuthorityOf reads one off an HTTP request: the three predicates, and nothing
// else. It is the ONE extraction — a transport that carries identity some other
// way (a delegated capability on the internal plane) fills the same value from
// its own fields and reaches the identical verdict, because the verdict is
// Scope.Admits and not the extraction.
func AuthorityOf(c *zip.Ctx) Authority {
	return Authority{
		Validated: principal.Validated(c),
		Super:     principal.IsSuperAdmin(c),
		OrgAdmin:  principal.IsOrgAdmin(c),
	}
}

// Scope is how much authority a route REQUIRES. It is a closed set: there are
// three kinds of door in the platform, and a fourth would be a new rule, not a
// new constant.
type Scope int

const (
	// Member admits any validated principal. It is the door on a TENANT surface,
	// where the caller's own org is the whole of what it may reach — the gate
	// establishes that a principal exists, and the handler's own org resolution
	// confines it.
	Member Scope = iota
	// Admin admits a SuperAdmin OR an admin of its own org. It is the door on an
	// administrative READ, where a tenant admin sees its own and platform sudo
	// sees everything — again, the confinement is the handler's.
	Admin
	// Super admits platform sudo alone. It is the door on an act against SHARED
	// platform state, which no customer-org admin may take however much authority
	// they hold inside their own org.
	Super
)

// Admits is the whole authorization rule. Nothing else in the platform decides
// this question.
func (s Scope) Admits(a Authority) bool {
	if !a.Validated {
		return false // fail closed: no principal, no authority, at every scope
	}
	switch s {
	case Member:
		return true
	case Admin:
		return a.Super || a.OrgAdmin
	case Super:
		return a.Super
	}
	return false
}

// Guard applies the rule to a handler in the standard way: an inadmissible
// caller is refused 403 before the handler runs, so nothing behind the gate ever
// observes an unauthorized request.
//
// A subsystem whose REFUSAL has a different shape (the deploy console sends a
// browser navigation to sign-in) calls Admits directly instead. That is a
// difference in the answer, not in the rule — the rule is still read from here.
func Guard(s Scope, h zip.Handler) zip.Handler {
	return func(c *zip.Ctx) error {
		if !s.Admits(AuthorityOf(c)) {
			return s.Refusal()
		}
		return h(c)
	}
}

// Refusal is the 403 this scope answers an inadmissible caller with, and Guard's
// own answer. It exists because a TYPED op cannot be guarded by wrapping:
// zip.Get[In, Out] takes a handler that receives a context and its decoded In, so
// there is no zip.Handler for Guard to compose around and the gate becomes the
// first line INSIDE the op (apps/platform/ops.go). The rule is still Admits and
// the sentence is still refusal, so the two forms of the one gate cannot answer
// differently.
//
// It is a METHOD on the scope rather than a function taking one, because the
// sentence is derived from the scope — a caller is told what it lacks in the
// vocabulary of the door it failed — and because Refuse is already the name of the
// 402 every SPEND gate renders (middleware_spend.go). Two refusals that mean
// different things do not share a name.
func (s Scope) Refusal() error { return zip.ErrForbidden(s.refusal()) }

// refusal says what the caller lacks, in the vocabulary of the scope it failed.
func (s Scope) refusal() string {
	switch s {
	case Admin:
		return "admin required"
	case Super:
		return "SuperAdmin required"
	default:
		return "authentication required"
	}
}
