package spec

// THE AUDIENCE IS DERIVED, and it decides which of the two documents an operation
// lands in.
//
// api.hanzo.ai answers ~1,800 paths across ~185 products. The split between the
// two is AUDIENCE, not secrecy: what the published SDKs, the CLI, the MCP server
// and docs.hanzo.ai present to a customer, against what an operator reaches
// through the same origin.
//
//	PUBLIC    the customer surface — openapi.yaml, and GET /v1/openapi.json.
//	INTERNAL  everything, unchanged — private.yaml, the compose the routing gates measure.
//
// THE PUBLIC ONE HOLDS THE NAME, because the name is what gets read. openapi.yaml
// is what a developer types, what an SDK generator defaults to and what a
// spec-reading tool finds first, so a generator that has never heard of this rule
// still obeys it. The two were the other way round, and the cost was exactly
// that: hanzoai/openapi's publish.py projects cloud's openapi.yaml into every
// published SDK and filters by path, never by stage, so every alpha capability
// reached every generated client while this file said it could not.
//
// # One rule, read off facts the operation already carries
//
// An operation is public when all of these hold, and nothing else decides it:
//
//   - its address is under /v1/ — the contract's one namespace (HIP-0128). The
//     document's furniture outside it (/.well-known/openapi.json, /_/…, /zap,
//     /ws/…) is served and is not a product surface;
//   - its product is not [Operator] — the first segment after /v1/ is the product
//     (Product), the axis every tag is already read off, and /v1/admin/* is the
//     one family the fleet reserves for the operator whichever app serves a leaf
//     of it (admin itself, pricing's /v1/admin/pricing, affiliates' operator view);
//   - it is not a relay — an UNTYPED `{wildcardN}` address publishes whatever
//     grows behind it and names nothing a client can call. A TYPED op on a
//     greedy tail ([Operation.Tool]) is not one: its tail is a parameter the op
//     binds (a secret's sub-path, an object key, a file path), so it names
//     exactly what a client calls, and a client builds the tail with its
//     slashes raw. Kept out, `DELETE /v1/kms/secrets/{wildcard1}` reached no
//     CLI command, no SDK method and no published page while the router served
//     it;
//   - it is not tagged [Compat] — a legacy spelling is served so a pinned caller
//     keeps working and is, by its own declaration, not the contract;
//   - its capability is `ga` (HIP-0139 §8) — a beta or alpha capability is
//     reached by flag and by nobody else, so it is in no generated client, no
//     tool list, no command group and no public page. It is still in the
//     internal document, which is where an operator and a flagged-in customer
//     read it;
//   - it is not the operator's — an operation only a SuperAdmin may call, at an
//     address outside /v1/admin, is named in [operated] and stamped by the
//     compose, and stays in the internal document beside the admin family.
//
// It used to be a whitelist: eighteen operations, each declared by hand in the
// app that served it, everything else internal by default. That held the
// inference surface public and the other hundred-odd products — commerce, git,
// observability, accounting, the cap table — out of every generated client,
// while those same clients quietly read the internal document instead. A
// whitelist nothing reads is a statement of intent. The rule above says what the
// customer surface IS, from the address, so a product launched next month is
// public the day it answers — unless it is the operator's, which its address
// already says.
//
// # Why this is not the prefix list the whitelist refused
//
// The whitelist refused a prefix list in the EMITTER because a second copy of the
// routing table drifts from the first. Nothing here is a copy of anything: the
// product is read from the operation's own address at the moment it is stamped,
// the relay from its own parameters and its own typing, the legacy spelling from
// its own tag.
// There is no list to keep current and no address a reader could spell
// differently from the router — translate has already rendered every path in the
// one form the document publishes before [Stamp] runs.
//
// # What holds it honest
//
// Both documents are COMMITTED and regenerated from source by the drift gate
// (mk/fleet.mk check), by one run of one compose. An operation entering or leaving
// the contract is a diff in openapi.yaml, reviewed next to the route that caused
// it, and openapi/compose_test.go asserts the property the two names promise: no
// alpha capability and no operator path reaches the public one, and both reach
// the private one.

import (
	"slices"
	"strings"
)

// RootPath is the API root — the capability index a client starts from. House
// law: /v1 only, and never a v2. It is here and not beside the index that
// serves it because the audience rule names it: the root is the contract's own
// namespace, so the rule that stamps every operation has to know its address.
const RootPath = "/v1"

// Operator is the product reserved for the operator: /v1/admin/*. Whichever app
// serves a leaf of it, the address says who it is for, and the public projection
// reads that rather than a list of apps.
const Operator = "admin"

// audience is the rule, asked of one operation at its published address.
func audience(path string, op *Operation) bool {
	// The root IS the contract's namespace, not a path outside it: /v1 is where a
	// client that knows nothing else starts, so a public projection that dropped it
	// would generate clients unable to reach the index of their own API.
	if path != RootPath && !strings.HasPrefix(path, "/v1/") {
		return false
	}
	if Product(path) == Operator {
		return false
	}
	if strings.Contains(path, "{wildcard") && !op.Tool {
		return false
	}
	if op.Stage != "" || op.Operator {
		return false
	}
	return !slices.Contains(op.Tags, Compat)
}

// Stamp writes the audience on every operation.
//
// It runs at the end of [Of], and AGAIN at the end of [Compose]. That is not two
// rules — it is one rule asked at the two points where the facts it reads are
// complete, and the second reading subsumes the first.
//
// Within an app, earlier than the end of [Of] is too early: [Fold] replaces a
// structural operation wholesale with the typed one, and [Project] replaces a
// relay with the registry behind it, so a mark written before either would be
// discarded by it.
//
// Across the fleet, the end of [Of] is too EARLY for one term of the rule. A
// subset is written by the app's own binary, which does not read the fleet's
// manifest and so cannot know its own stage (HIP-0139 §8); the stage arrives with
// [Part], and Compose is where it is stamped. So Compose asks the whole rule again on
// the finished composition, where every term — address, product, relay, compat
// and stage — is finally in hand. It is still a pure function of the parts.
func Stamp(d *Document) {
	for path, item := range d.Paths {
		for _, op := range item {
			op.Public = audience(path, op)
		}
	}
}
