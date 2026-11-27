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
