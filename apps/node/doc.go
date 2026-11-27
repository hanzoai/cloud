// Package nodes is your machines: the ones you own, connected and ready to take
// a command.
//
// A node is a machine. It dials in over a socket, holds it open, and is asked to
// run commands it declared it can run. That is the whole capability, and it is
// its own: a bot is an agent bound to a machine, a bot RUN is a task the
// executor drives on a surface (apps/bots), and a bot MACHINE is one you RENT
// from visor at /v1/compute/bots. Three values, three names, three addresses.
//
// The machine half used to answer under the bot's name. The address moved to
// /v1/node first and openapi/misfiled.txt recorded the gap; the name has
// followed, so HIP-0139 §1 holds again — one capability, one package, one
// plugin, one name.
//
// Route families:
//
//	GET  /v1/node/connect            the socket a node dials and holds open
//	GET  /v1/node                    this org's connected nodes
//	POST /v1/node/{id}/invoke        ask one of them to run a command
//	POST /v1/node/peer/invoke        replica-to-replica forward (machine hop)
//
// node.go is the plane and its Mount; registry.go holds who is connected where;
// ws.go is the socket; policy.go decides what a command is allowed to be.
//
// # The org is the gateway's verdict, never the caller's
//
// Every route here takes its org from X-Org-Id, which the gateway injects after
// validating IAM and after stripping any client copy. It is never read from a
// body, a query, or a path — a caller that could name an org could name someone
// else's. principal.Org returns nothing without a validated principal, so the
// direct-to-pod path (where a forged X-Org-Id is restored but no user is) is
// refused rather than defaulted.
//
// The one exception is the peer hop, whose org DOES arrive in a body: it is a
// machine call from another replica of this binary, which derived that org from
// a validated header, and it authenticates with a shared token instead of a
// user. See PeerInvoke.
//
// # Where authorization happens
//
// Once, at the socket. The registry runs the gate on the replica that holds the
// node — the only one that knows what that node declared it can do — so a
// locally-held node and one reached through a forward are authorized by the same
// code with the same session in hand. This file supplies that gate (policy.Check
// over the deployment's Mode); it does not re-check before invoking, because a
// second check in a place that sometimes has the session and sometimes does not
// is how the two answers drift apart.
package node
