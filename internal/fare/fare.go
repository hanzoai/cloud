// Copyright (c) 2026 Hanzo AI Inc.

// Package fare is the preamble a metered, org-scoped surface opens every
// operation with: admit the caller, resolve the org they act for, take the fee,
// run the operation, and record the debit only once it has succeeded.
//
// It is ONE function because it is ONE decision. apps/s3 wrote it first and
// apps/space needs exactly the same one, and a second copy would be two answers
// to the pair of questions that decide whether one org can reach another's
// bytes: may this caller spend here, and whose namespace do they address. So it
// is generic over the subsystem's own State — [Paid] is the same code for every
// surface, and [Surface] is the two facts it has to ask each of them about
// itself.
//
// It is NOT the fleet's default price. cloud.Toll asks what an operation costs
// from the address, once, at op.invoke, for every operation the binary serves.
// This is what ONE surface charges for one of its own operations, and it carries
// the org down beside the charge, which the fleet-wide rule has no place to put.
//
// # Paid wraps the HANDLER, and that is the whole point
//
// zip records a typed op and its route's fiber handler as two fields of one
// entry and wraps only the second, so a gate handed to Group or composed through
// With runs for REST and for nothing else — while MCP, the call plane, the graph
// and the CLI invoke the op directly and the depth-0 identity middleware has
// already authenticated whoever is calling. The handler is the one value every
// way in dispatches to, so an operation that costs money asks for it there.
//
// The org travels DOWN in the context, put there by the same call that took the
// money, which is what makes [Org] the only way an operation learns one: an
// operation registered WITHOUT [Paid] can name no org and so can touch nothing.
package fare

import (
	"context"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/hanzoai/cloud/metering"
	"github.com/hanzoai/namespace"
	"github.com/zap-proto/zip"
)

// Surface is what [Paid] has to ask a subsystem about ITSELF, and it is exactly
// two facts because those are the two a type parameter cannot otherwise reach.
//
// Everything else the preamble needs — the meter, the logger, the principal — is
// either on cloud.Base, which every Service carries, or on the request. These
// two are the subsystem's own: whether its backing store is there at all, and
// what it charges. A constraint rather than an argument, so the call site stays
// `fare.Paid(s, o.listFiles)` and the compiler, not the author, is what makes a
// new surface answer both.
type Surface interface {
	// Ready is nil when the subsystem can serve anybody, and the honest refusal
	// every operation answers with when it cannot. It is asked FIRST: a surface
	// that can serve nobody says so before it says who it serves.
	Ready() error
	// Fee is the meter's kind and what ONE operation on this surface costs, in
	// cents. It is read per call rather than once at mount, so an operator's knob
	// takes effect without a restart, and 0 makes the surface free and therefore
	// un-gated.
	Fee() (kind string, cents int64)
}

// Paid composes [admit] and [settle] onto an operation. Every operation on a
// metered surface but its probe is registered through it, so there is one
// decision about money and org scope and no second place to keep in step with it.
func Paid[S Surface, In, Out any](s *cloud.Service[S], core zip.TypedHandler[In, Out]) zip.TypedHandler[In, Out] {
	return func(ctx context.Context, in *In) (*Out, error) {
		c, ok := cloud.Request(ctx)
		if !ok {
			return nil, principal.RefusedFrom(ctx) // no request: no credential, no org
		}
		org, err := admit(s, c)
		if err != nil {
			return nil, err
		}
		out, err := core(context.WithValue(ctx, orgKey{}, org), in)
		if err != nil {
			return nil, err
		}
		settle(s, c)
		return out, nil
	}
}

// admit is the sentence every operation on a metered surface opens with, and it
// answers the caller's org so the operation can address storage inside it.
//
// Four refusals in the order they have to be asked. A subsystem that cannot serve
// anyone says so before it says who it serves, so the readiness check comes first.
// Then the org boundary — cloud.Member (a validated principal, HIP-0519's one
// predicate set) and an org to act for — because billing an unauthenticated caller
// would read an empty ledger and answer a money question about nobody. Then the
// anti-forgery token, immediately before the money, because that is what it is
// about: these surfaces spend the caller's balance on a READ, so a page the caller
// never visited must not be able to spend it for them by sending them here with a
// cookie they already hold. Then the balance itself: an unfunded org is 402 and,
// in the fail-closed posture, an unreachable commerce is 503, both with NOTHING
// touched.
//
// THE TOKEN IS ASKED HERE RATHER THAN ON THE ROUTE, and it is free to ask here.
// account's control refuses a caller with no request, and these surfaces have none
// to serve: every operation resolves its org from the request, so a caller without
// one is already refused above. Asking in the preamble therefore costs no CLI, no
// agent and no service caller, and it covers the seams a route cannot — the same
// reason the money is here. account's gate is a no-op the moment a caller
// PRESENTS a credential (Bearer, gateway, API key), which is every client that
// reaches these surfaces; only the ambient-cookie path is asked for the echoed token.
//
// The denial travels as an ERROR (cloud.Denied), which is the one refusal channel
// every shape here shares — serve.go installs DenyEnvelope app-wide, so a REST
// caller reads the money wire's own nested {"error":{"code","message"}} bytes, and
// off the HTTP path deniedErr.Unwrap keeps the same status and sentence.
func admit[S Surface](s *cloud.Service[S], c *zip.Ctx) (string, error) {
	if err := s.State.Ready(); err != nil {
		return "", err
	}
	if !cloud.Member.Admits(cloud.AuthorityOf(c)) {
		return "", cloud.Member.Refusal()
	}
	org, ok := orgOf(c)
	if !ok {
		return "", principal.Refused(c)
	}
	if err := cloud.CSRF(c.Context()); err != nil {
		return "", err
	}
	kind, cents := s.State.Fee()
	project, projectValidated := principal.ValidatedProject(c)
	if err := s.Bill.Authorize(c.Context(), principal.Payer(c), project, projectValidated, kind, cents); err != nil {
		return "", cloud.Denied(err)
	}
	return org, nil
}

// settle debits the caller's ledger for one operation, and runs only after the
// work succeeded — a failed operation is surfaced and not billed, which is the
// edge gate's rule ("do not bill failed work"). The debit is async best-effort, so
// it never blocks the answer; a zero fee or unconfigured billing makes it a no-op.
func settle[S Surface](s *cloud.Service[S], c *zip.Ctx) {
	kind, cents := s.State.Fee()
	s.Bill.Record(principal.Payer(c), kind, metering.Usage{
		Model:       kind,
		AmountCents: cents,
		Project:     principal.Project(c),
		RequestID:   c.RequestID(),
		ClientIP:    cloud.ClientIP(c),
	})
}

// orgKey names the request-scoped slot the admitted org travels in. Unexported
// zero-size type: unforgeable from another package.
type orgKey struct{}

// Org is the caller's org as [admit] resolved it, and the ONLY way a typed
// operation on a metered surface learns one. It is not a second resolution — it
// reads the value [Paid] carried down, so the org an operation addresses storage
// under is by construction the org whose balance was checked and whose ledger is
// debited.
//
// FAILS CLOSED. An operation reached with no admission — off the HTTP path, or
// registered without [Paid] — has no org and refuses rather than defaulting to one.
func Org(ctx context.Context) (string, error) {
	org, _ := ctx.Value(orgKey{}).(string)
	if org == "" {
		return "", principal.RefusedFrom(ctx)
	}
	return org, nil
}

// orgOf resolves the caller's org exactly as apps/provisioning does — the SAME
// sanitized slug the control plane keys on, so a bucket allocated there and one
// operated on here share the one org tag.
//
// A VALIDATED PRINCIPAL IS ALREADY ESTABLISHED (RED HIGH): admit asks
// cloud.Member before this, so the forgeable data path is closed before the org
// is read. SanitizeIdentity sets X-User-Id ONLY when it validated a bearer/cookie;
// on the no-principal "Phase-1 data" path it RESTORES the client's raw X-Org-Id
// but leaves X-User-Id empty. A pure data plane that trusted X-Org-Id alone would
// let an in-cluster caller (a co-namespace pod within the cloud-api NetworkPolicy)
// forge `X-Org-Id: victim` with NO bearer and get cross-org object CRUD. Every
// legitimate caller reaches this through the console BFF /cloud proxy, which mints
// a user-bound bearer, so the gate refuses ONLY the anonymous-forge path and
// breaks no real client. These are data planes; they never serve an
// unauthenticated principal.
//
// Empty org falls back to the literal "admin" bucket for a SuperAdmin, and only
// for one: SanitizeIdentity mints X-User-IsAdmin solely for a JWT-verified
// SuperAdmin (HIP-0026), and that fallback reaches the admin bucket, never a real
// customer's.
//
// NORMALIZATION — this uses namespace.Sanitize (case-folds to a DNS slug),
// NOT KMS's exact-match, ON PURPOSE: the physical bucket name is derived through
// provisioning's SAME sanitized slug (s3admin.BucketName), so a bucket provisioned
// via POST /v1/provisioning is findable here — exact-match would break that
// lockstep. A real IAM owner claim is already a lowercase DNS label, so the fold is
// a no-op on validated input (and, post the gate, only a validated principal
// reaches it). The divergence from KMS is intentional per-subsystem, not drift.
func orgOf(c *zip.Ctx) (string, bool) {
	if org := namespace.Sanitize(c.Org()); org != "" {
		return org, true
	}
	if principal.IsSuperAdmin(c) {
		return "admin", true
	}
	return "", false
}

// Bytes composes the same preamble onto a handler that reads or writes a BODY.
//
// A typed operation answers a value; some answer bytes. An object's contents, a
// rendered file, an upload — none of them are a struct, and zip's typed pair
// cannot carry them, so those handlers take the request itself. They are the same
// decision about money and org scope as every other operation on the surface, and
// this is that decision, not a second one: [Paid] and this call admit, run, and
// settle in the same order, and both put the org where [Org] reads it.
//
// The org travels in the CONTEXT the handler reads off the request, so a bytes
// handler learns its org exactly the way a typed one does and cannot name another.
func Bytes[S Surface](s *cloud.Service[S], core func(*zip.Ctx) error) func(*zip.Ctx) error {
	return func(c *zip.Ctx) error {
		org, err := admit(s, c)
		if err != nil {
			return err
		}
		c.SetContext(context.WithValue(c.Context(), orgKey{}, org))
		if err := core(c); err != nil {
			return err
		}
		settle(s, c)
		return nil
	}
}
