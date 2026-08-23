package openapi

// THE CREDENTIAL, stated once, because the router cannot state it at all.
//
// Everything else this package publishes is READ off the live route table. Auth
// is the one fact that reading cannot reach, and [Document] already says why: a
// requirement is a middleware in a handler chain (iammiddleware.IAMTokenRequired,
// commercemid.TokenRequired, guard(...)) — a func value, invisible as data. So it
// is DECLARED here, in the one place, exactly as bodies are declared by [Register]
// and prose by [Describe].
//
// Until it was, the document declared no scheme at all, and a document with no
// scheme generates a client with no auth: openapi-generator emits credential
// plumbing only for the schemes a document names, so every SDK cut from this file
// carried an `accessToken` field that reached no header. One declaration repairs
// all five at once, which is the whole reason it belongs here rather than in each
// generator's templates.
//
// # ONE scheme, because there is ONE credential
//
// cloud's identity boundary resolves exactly one token per request (callerToken,
// middleware_identity.go) and mints every downstream authority header from it. The
// canonical spelling is `Authorization: Bearer <token>`. X-Authorization and HTTP
// Basic-with-the-token-as-password are proxy idioms for the SAME token, and a
// session cookie is what a browser holds instead of a header — spellings, not
// second credentials, so declaring them would put three knobs in every generated
// client where the wire has one.
//
// X-Api-Key is deliberately absent for the opposite reason: it is not a spelling
// of the credential at all. The identity boundary never reads it — agency.go reads
// it only to tell two anonymous callers apart for the abuse sensor — so declaring
// it fleet-wide would publish a header that authenticates nothing.
//
// bearerFormat is omitted rather than set to JWT. The token is EITHER an
// IAM-minted access token OR an opaque API key (pk-/sk-, APIKeyPrefixes), and
// naming one format would misdescribe the other half of the traffic.
//
// # Default-REQUIRE, and [Open] is the exception
//
// The document-level requirement is the default every operation inherits, and it
// is the safe default: an SDK that sends a credential to a route not asking for
// one is ignored, while an SDK that withholds one from a route that needs it gets
// a 401 the caller cannot explain. [Open] is the one declaration that overrides
// it, and it is per-operation for the reason [Public] is — a prefix list here
// would be a second copy of the routing table, free to drift from the routes.

import (
	"fmt"
	"strings"
	"sync"
)

// Bearer names the scheme in the document. Plain, and the same word the header
// uses, so a reader meets one name for one thing.
const Bearer = "bearer"

// Requirement is one OpenAPI security requirement: scheme name → the scopes it
// needs. The scope list is empty for a bearer credential — cloud authorizes on
// the token's claims and its org membership, never on OAuth scopes, so an
// operation asking for a scope would be asking for something nothing mints.
type Requirement map[string][]string

// SecurityScheme is the sliver of the Security Scheme Object this generator can
// honestly assert: an HTTP scheme, named. The fields OpenAPI defines for the
// other kinds (apiKey's name/in, oauth2's flows, openIdConnect's discovery URL)
// are absent because no operation here uses those kinds, and a field emitted for
// a kind nothing declares is a field nothing keeps true.
type SecurityScheme struct {
	Type        string `json:"type"`
	Scheme      string `json:"scheme,omitempty"`
	Description string `json:"description,omitempty"`
}

// bearer is the whole declaration.
var bearer = SecurityScheme{
	Type:   "http",
	Scheme: "bearer",
	Description: "The caller's credential, sent as `Authorization: Bearer <token>`. It is either an " +
		"access token minted by Hanzo IAM or an API key (pk-/sk-). X-Authorization, and HTTP Basic " +
		"carrying the token as the password, are accepted spellings of the same header for clients " +
		"that cannot set Authorization; a browser presents the session cookie instead. Every " +
		"operation requires it unless it says `security: []`.",
}

var (
	openMu  sync.Mutex
	openOps = map[opKey]bool{}
)

// Open declares that one operation is reachable with NO credential, keyed by its
// address in the document and its method exactly as [Public] is — and normalized
// through [translate] for the same reason, so `/v1/videos/:id` and
// `/v1/videos/{id}` are one key and neither spelling can be wrong.
//
// It is the override on a document-level requirement, and it renders as
// `security: []`, which is OpenAPI's own way of saying "this one needs nothing".
// Generated clients read it and stop demanding a token for a call that never
// wanted one: GET /v1/models is the model catalog, it answers 200 to anybody, and
// a client that could not read it before holding a credential could not show a
// model picker.
//
// ONE operation per call, never a path and never a prefix — the same rule
// [Public] states, for the same reason. A method is not a detail to be defaulted:
// a GET that reads a public catalog and a POST at that address are different
// questions, and a declaration covering "every method here" would open tomorrow's
// write endpoint on the strength of today's read.
//
// Called from the owning subsystem's init, next to the routes. A duplicate is a
// programming error at init time and panics, as [Register], [Describe] and
// [Public] do.
func Open(path, method string) {
	if strings.ContainsAny(path, "*+") {
		panic(fmt.Sprintf("openapi: Open(%q) — a credential exemption names operations, never a wildcard; "+
			"a pattern here would unauthenticate whatever grows behind it", path))
	}
	m := strings.ToUpper(method)
	if !methods[m] {
		panic(fmt.Sprintf("openapi: Open(%q, %q) — this generator publishes %v and nothing else, "+
			"so a declaration for any other method marks an operation no document carries", path, method, Methods()))
	}
	at, _ := translate(path)
	key := opKey{method: m, path: at}
	openMu.Lock()
	defer openMu.Unlock()
	if openOps[key] {
		panic(fmt.Sprintf("openapi: duplicate Open for %s %s", key.method, key.path))
	}
	openOps[key] = true
}

// secure states the credential on a finished document: the scheme, the default
// requirement every operation inherits, and `security: []` on the operations that
// declared they need none.
//
// Every producer of a published document calls it — [Spec] for an app's own
// surface and its committed subset, [Compose] for the fleet, [Publish] for the
// public projection — so the credential cannot be a fact one document carries and
// another forgets. It is idempotent, which is what lets the composed documents
// call it over operations their parts already stamped.
//
// It runs LAST in each, for the reason [stamp] does: [Fold] replaces a structural
// operation with the typed one and [Project] replaces a relay with the registry
// behind it, so a mark written before either would be thrown away by it.
func secure(d *Document) {
	if d.Components == nil {
		d.Components = &Components{}
	}
	d.Components.SecuritySchemes = map[string]SecurityScheme{Bearer: bearer}
	d.Security = []Requirement{{Bearer: []string{}}}

	openMu.Lock()
	defer openMu.Unlock()
	for path, item := range d.Paths {
		for method, op := range item {
			if openOps[opKey{method: strings.ToUpper(method), path: path}] {
				op.Security = &[]Requirement{}
			}
		}
	}
}
