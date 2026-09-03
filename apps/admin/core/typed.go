package core

// The GATE, at the typed-op client.
//
// Every /v1/admin/* route is a zip typed op — `zip.Get[In, Out]` and friends — because
// a typed op is what lands in zip's registry, and the registry is what the OpenAPI
// document, the MCP tool list and the CLI are all projections of. A raw
// `func(*zip.Ctx) error` serves the same bytes and is invisible to every one of them.
//
// A typed handler receives only a context and its decoded In, so the two things this
// surface gates on arrive by the two canonical routes and no third:
//
//	the REQUEST — cloud.Bridge parks it, cloud.Request takes it back off. admin does
//	              not merely READ the caller's identity, it REPLAYS the caller's own
//	              credential to IAM (CallerCreds), which is the case cloud.Request
//	              exists for.
//	the KERNEL  — a receiver. A TypedHandler has no parameter for it, so an op that
//	              needs the upstream clients is a method on a value that holds them.
//
// THE GATE MOVED, IT DID NOT CHANGE. It used to wrap the handler; it is now the first
// line INSIDE it. Same two predicates, same fail-closed answers, same 403 — but visible
// where the handler is read, and applied on the MCP and CLI projections too, which never
// pass through the router a wrapper would have lived on.
//
// FAIL CLOSED OFF THE HTTP PATH. The CLI projection's LocalInvoke runs an op with no
// request at all, so Admit finds none and refuses. There is no second gate to keep in
// sync: an anonymous POST /mcp is refused by the same line that refuses an anonymous GET.

import (
	"context"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/zap-proto/zip"
)

// The two states of the /v1 envelope every admin op answers with
// ({ status, msg, data, total } — the operator transport's get<T>/getList<T> shape).
// The transport surfaces anything that is not OK as an error, never a value, so a
// failed read is a 200 carrying Err — NOT an HTTP error status.
const (
	OK  = "ok"
	Err = "error"
)

// None is the input of an op that takes none: no body, no query, no path param. It is
// shared rather than redeclared per op because an empty struct carries no contract to
// document — the ops that DO take input each declare their own named In.
type None struct{}

// Total is the row count of a LIST read, as the pointer the envelope's optional total
// field takes. Present — even at zero — on a success; left nil on a failure, because a
// failed read has no count and adding the key would change the wire.
func Total(n int) *int { return &n }

// Admit is the SuperAdmin gate, called once at the top of every PLATFORM op. It is the
// same fail-closed predicate the old Guard wrapper applied: a request whose validated
// identity is not a SuperAdmin (principal.IsSuperAdmin — X-User-IsAdmin, which
// SanitizeIdentity sets only for owner == AdminOrg) is refused 403 before any upstream
// is touched.
//
// It gates on that ONE fact, not the platform's cloud.Super scope, which also requires
// principal.Validated: the cockpit's second tier (AdmitScoped) and its org-scoped reads
// resolve a SuperAdmin off the admin bit alone, so requiring the conjunct here would
// give one surface two admin rules.
//
// It returns the request because an admitted op almost always needs it — to replay the
// caller's credential to IAM, or to read the body a passthrough forwards verbatim.
func Admit(ctx context.Context) (*zip.Ctx, error) {
	c, ok := cloud.Request(ctx)
	if !ok {
		return nil, zip.ErrForbidden("SuperAdmin required")
	}
	if !principal.IsSuperAdmin(c) {
		return nil, zip.ErrForbidden("SuperAdmin required")
	}
	return c, nil
}

// Change is [Admit] for an operation that CHANGES something, and every platform write
// calls it where a read calls Admit.
//
// The extra question is the estate's anti-forgery control (apps/account). Admit
// establishes WHO is calling; this also asks whether the CALL was meant. A browser
// authenticates this board from an httpOnly session cookie, which is ambient — any
// origin's request to us carries it — so an operator merely reading a page elsewhere
// is enough for that page to raise a spend ceiling, flip a platform switch or open a
// gated service as them. Admit cannot see that: the caller really is the SuperAdmin,
// and it is the request they did not make.
//
// It runs BEFORE Admit. The refusal then says which control answered whoever is
// calling, rather than hiding behind an authorization refusal an unattested caller
// would have got anyway — which is also what makes it measurable (limits_test.go).
//
// A caller who PRESENTED a credential — Bearer, the service token, the gateway —
// passes it untouched: they cannot be forged into, so an API client pays nothing.
//
// READ vs CHANGE is the distinction, and each has ONE gate. An operation reaching for
// Admit when it changes something is then a visible choice rather than an omission.
func Change(ctx context.Context) (*zip.Ctx, error) {
	if err := cloud.CSRF(ctx); err != nil {
		return nil, err
	}
	return Admit(ctx)
}

// AdmitScoped is the gate for the ORG-SCOPED panels, called once at the top of each.
// It admits a SuperAdmin (principal.IsSuperAdmin) OR an admin of an ENABLED
// WHITE-LABEL TENANT org — three facts, ALL required for that second tier:
//
//   - X-User-IsOrgAdmin (principal.IsOrgAdmin — "admin of my own org", unforgeable
//     because SanitizeIdentity strips it on ingress and re-mints it only from a
//     validated isAdmin claim), AND
//   - a validated principal pinned to its own org (principal.Org: validated
//     X-User-Id + non-empty in-bounds X-Org-Id, never client-chosen), AND
//   - that org is an ENABLED WL tenant (State.IsWhiteLabelTenant — the fail-closed
//     allowlist; empty/unset ⇒ SuperAdmins only).
//
// Passing this gate is not the end of the scoping: the handler then folds every read
// through ResolveScope/ScopedOrgs, which hard-limits a non-super caller to their own
// org subtree whatever the request says.
func AdmitScoped(ctx context.Context, s *cloud.Service[State]) (*zip.Ctx, error) {
	c, ok := cloud.Request(ctx)
	if !ok {
		return nil, zip.ErrForbidden("admin required")
	}
	if principal.IsSuperAdmin(c) {
		return c, nil // SuperAdmin: cross-tenant, admitted regardless of org pin.
	}
	if org, ok := principal.Org(c); ok && principal.IsOrgAdmin(c) && s.State.IsWhiteLabelTenant(org) {
		return c, nil
	}
	return nil, zip.ErrForbidden("admin required")
}

// ChangeScoped is [AdmitScoped] for an operation that CHANGES something. Same pairing,
// same order, same reasons as [Change] — and the org-scoped tier needs it MORE, because
// the caller it admits is an ordinary customer's own org admin rather than a SuperAdmin,
// so the ambient session it rides is a customer's browser on the open web.
func ChangeScoped(ctx context.Context, s *cloud.Service[State]) (*zip.Ctx, error) {
	if err := cloud.CSRF(ctx); err != nil {
		return nil, err
	}
	return AdmitScoped(ctx, s)
}
