// Copyright © 2026 Hanzo AI. MIT License.

// describe.go is the prose for iam routes this repo serves but does not write.
//
// Almost all of them explain themselves already: hanzoai/iam runs zipdoc over
// its own source and ships the table, so its handlers' doc comments arrive here
// with the module. The two below arrived without prose because they were added
// as bare registrations onto an existing handler — the same handler the verb
// spelling already used — and a registration carries no comment of its own.
//
// openapi.Describe is the client for exactly that: additive metadata on a route
// the router already carries, so this file cannot invent an operation, only
// explain one that exists. The key is the fiber pattern VERBATIM.
package iam

import (
	"net/http"

	"github.com/hanzoai/cloud/openapi"
)

func init() {
	describeKeyDoors()
}

// ---- /v1/iam/keys — the two key doors, at their nouns ----

// A key resolver sits on the request-authentication path of cloud, ai and base,
// so its address cannot move in a single release: those are separate
// deployments and cannot cut over in the same instant. Both spellings answer —
// the verb (resolve-key, get-user?accessKey=) and the noun below — off the SAME
// handlers, so there is no second implementation to keep in agreement. The verbs
// are deleted later, when nothing asks for them.
//
// Which door a key belongs to is the distinction worth keeping straight, and it
// is why these are two routes and not one with a mode: a publishable key names
// an organization and a secret key names a principal. One address answering both
// would be an address whose answer type depends on its input, and the caller
// that ships a key in a browser would be one parameter away from a person.
func describeKeyDoors() {
	openapi.Describe("/v1/iam/keys/org", http.MethodGet,
		"Resolve a PUBLISHABLE key to the organization that owns it",
		"Answers which organization a publishable key belongs to — what a service calls to "+
			"attribute a request that arrived carrying a key shipped in a browser. This is the "+
			"noun spelling of `/v1/iam/resolve-key`, the same handler at the address that "+
			"replaces it; both answer while callers migrate.\n\n"+
			"It names an ORGANIZATION and never a person. No path through it loads or returns a "+
			"user, so a key placed in client code cannot become a way to learn who anyone is — "+
			"which is the whole reason this is a separate door from the one below.\n\n"+
			"A key that is expired, secret rather than publishable, or simply unknown all answer "+
			"with the same sentence and a `code` saying which it was. Only a confidential service "+
			"that has already proved it may resolve keys reads that code — there is no anonymous "+
			"caller here to probe which keys exist — and telling those apart is what lets a holder "+
			"be told to re-mint an expired key instead of hunting a configuration error.")

	openapi.Describe("/v1/iam/keys/principal", http.MethodGet,
		"Resolve a SECRET key to the principal it authenticates",
		"Answers who a secret key belongs to — the owner and name a gateway needs to attribute "+
			"and bill a request that arrived carrying an `sk-`. This is the noun spelling of "+
			"`/v1/iam/get-user?accessKey=`, the same handler at the address that replaces it; "+
			"both answer while callers migrate.\n\n"+
			"It resolves a KEY and nothing else. The verb it replaces also reads a user by `?id=`, "+
			"and carrying that here would make this a second address for the user read — the exact "+
			"thing being retired. Ask for a person by name at the user read; ask here only what a "+
			"credential resolves to.\n\n"+
			"Requires a confidential caller: the resolver authenticates as an app, so a request "+
			"without that credential resolves nothing rather than falling back to an anonymous "+
			"lookup. An unresolvable key answers with a `code` distinguishing expired from wrong-"+
			"door from unknown, so the holder can be told which one cure applies.")
}
