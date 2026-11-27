// Package admission is launch control for Hanzo's hosted services — the waitlist
// DECIDE, COMPOSING the ONE flag engine (apps/flags) one-way. It owns:
//
//   - the host→service registry (registry.go) + the brand seed (waitlist.go),
//   - the per-service MODE decide WaitlistModeForHost — a service's mode IS the switch
//     waitlist.<svc>, evaluated through the flag engine (flags.Bool),
//   - the admin control funcs (List/Set/Upsert) the /v1/admin/services board calls,
//   - the public mode read /v1/admission/waitlist, Mount,
//   - the per-user approval predicate (Approvals, reused from IAM — approval.go).
//
// flags NEVER imports admission; admission imports flags. The engine is the pure
// (Principal, context) -> verdict primitive; this package is its first composed tenant.
// The decide is two orthogonal axes:
//
//   - PER-SERVICE  waitlist mode on|off  — the switch waitlist.<svc>, resolved for a
//     request host via WaitlistModeForHost (the decide, waitlist.go).
//   - PER-USER     approvalStatus pending|approved — owned by IAM (approval.go), REUSED.
//
// THE RULE:
//
//	if waitlistMode[host] AND NOT user.approved → the waitlist
//	if approved OR mode=off                     → allow
//	unauthenticated                             → login first
//
// IT DECIDES AND DOES NOT ENFORCE, and that is where the fleet's shape puts the two.
// Enforcement has to sit in front of EVERY governed host, and the fleet is one process
// per app behind the ingress — so the enforcement point is the ingress guard, which
// reads THIS registry through /v1/admission/waitlist and applies the rule above for
// every host at once. One decide, one enforcement, each in the layer that can see the
// whole of what it answers for.
//
// A native in-binary middleware used to live here for the fused single binary, which
// no longer exists (cmd/cloud is a router that mounts each app as its own process). In
// this shape there is no in-binary place a middleware would cover more than one app's
// own routes, so it enforced nothing and read as though it enforced everything. It is
// gone; the address above is what an enforcer reads.
package admission
