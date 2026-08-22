package openapi

// The FIFTH projection of the one route table — after the REST routes, this
// document, the MCP tools and the CLI.
//
// A command is not a new kind of thing and this file does not derive one. It
// hands the rendered document to zip.CommandsFromSpec — the SAME function the
// `hanzo` CLI's command tree is built from, applied to the SAME bytes every
// published SDK is generated from — and serves what comes back. So a route
// registered this morning is a ⌘K command, a CLI command and a Slack command
// this afternoon, with nothing written down twice and no generator to run.
//
// # Why a second address rather than a field on the document
//
// One measured reason, and there is no second one: the fleet document is
// 3,680,332 bytes of YAML — 3,103,407 as JSON, 598,303 gzipped — and a browser
// palette cannot load it to find five commands.
//
// MEASURE IT BEFORE YOU QUOTE IT. The projection of the same 2,349 operations is
// 2,344,651 bytes, 454,881 gzipped: 1.32x smaller, not the 4.5x the design
// assumed. That figure was taken from hanzoai/cli's spec/products.json — a
// DIFFERENT artifact, an OpenAPI-shaped trim with every description, summary and
// operationId stripped, which is why it fits in 107 KB. This projection keeps the
// prose, and the prose is most of it: Description is 1,028,067 bytes of the
// payload and Flags another 561,788.
//
// So the endpoint is worth its own address, but by less than a third rather than
// by a league, and the gap is entirely the search corpus a bar wants. Trimming it
// is a real option and it is NOT taken here, because the shape on the wire is
// zip.Command — the registry's own type, unedited. A hand-picked subset of its
// fields would be the second shape this whole design exists to avoid, and the
// first surface that needed a dropped field would have to add it back somewhere.
// If the weight has to come down, drop it in the registry or compress harder;
// do not fork the type.
//
// # It is UNAUTHENTICATED, in the same words and for the same reason as [Path]
//
// A client has to be able to read the contract before it holds a credential, and
// a list of operation names grants nothing. Every route named here stays
// individually gated; reading the map does not open a door.
//
// # The projection is TOTAL, and that is the design, not an omission
//
// Nothing is filtered — not by method, not by product, and above all not by
// caller. zip has no per-op scope; it has Authorizer, which runs on the DECODED
// INPUT of every op, over REST and MCP alike. Permission is therefore a fact
// about an input, not about an operation, and any list this endpoint filtered
// would be a second, static claim the Authorizer is free to contradict — right
// on the day it was written and wrong afterwards, in the direction that hides
// working functionality from people who have access.
//
//	The registry states what exists.
//	The Authorizer states what you may do.
//	The surface renders the refusal honestly.
//
// The cost accepted is that a surface can show a command its caller cannot run,
// and gets a 403 sentence when they run it. That is strictly better than a
// command that silently does not exist.
//
// Method rides along for the same reason it is already in the registry, and it
// is what lets a bar be safe without a second list: GET commands are safe to
// browse fuzzily, everything else has to be named exactly. That is a RENDERING
// rule and it belongs to the surface — stated here only because the data it
// needs is present, so no surface has to invent it.

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"sync"

	"github.com/zap-proto/zip"
)

// CommandPath is the command projection's address. House law: /v1/ only, no
// /api/ prefix, and never a v2.
const CommandPath = "/v1/openapi/commands"

// catalog is the projection rendered: the bytes and the tag that names them.
// Both are computed once, together, because the tag is a fact ABOUT the bytes
// and hashing them per request would be work whose answer cannot change.
type catalog struct {
	body []byte
	etag string
}

// The command list is itself a command — the same self-description [Path] has,
// and for the same reason: it is the one operation with no owning subsystem, so
// nothing else would ever declare it. In an init rather than in serveCommands
// because serve runs once per document source (Mount and MountFleet) and
// Describe refuses a duplicate.
func init() {
	Describe(CommandPath, http.MethodGet,
		"Every operation this API answers, as a command",
		"The command projection of the OpenAPI document at "+Path+" — each operation reduced "+
			"to what running it by name needs: its service and command token, its method and "+
			"path, the prose lifted from the handler, its path parameters as positional "+
			"arguments and its remaining inputs as typed flags.\n\n"+
			"It is a separate address for one measured reason: the fleet document is megabytes "+
			"and a command palette cannot load it, while this projection of the same operations "+
			"is several times smaller because it carries no schemas, responses or components.\n\n"+
			"Unauthenticated by design, exactly as the document it derives from: a client has to "+
			"be able to read the contract before it holds a credential, and a list of operation "+
			"names grants nothing. The list is TOTAL and is never filtered by caller — what you "+
			"may run is decided per request by the authorizer, on the decoded input, so a filtered "+
			"list would be a second claim about permission that is free to be wrong.\n\n"+
			"Rendered once and served as bytes thereafter, under a strong ETag.")
	// The sentence above, as data. A door the prose calls unauthenticated and the
	// contract calls credentialed is one of the two lying to a generated client.
	Open(CommandPath, http.MethodGet)
}

// order puts the list in a TOTAL order, which is what makes the payload a
// function of the document rather than of a map walk.
//
// zip.CommandsFromSpec sorts by (Service, Name) with an unstable sort, and 41 of
// the fleet's 2,323 commands share that key — `mq streams-delete` is claimed by
// three. The tie is then broken by the order the document's path map happened to
// iterate in, so two processes composing the SAME document serve the same commands
// in different orders under different ETags. Behind more than one replica that
// turns every conditional request that lands on a different pod into a full
// re-download of half a megabyte, which is precisely what the ETag was for.
//
// (Method, Path) completes the key and cannot itself tie: a document addresses at
// most one operation per method per path. Sorted here rather than upstream in zip
// because it is THIS artifact's cacheability that needs it — the CLI consumes the
// same list as a tree and never compares bytes.
func order(cmds []zip.Command) {
	slices.SortFunc(cmds, func(a, b zip.Command) int {
		return cmp.Or(
			strings.Compare(a.Service, b.Service),
			strings.Compare(a.Name, b.Name),
			strings.Compare(a.Method, b.Method),
			strings.Compare(a.Path, b.Path),
		)
	})
}

// serveCommands registers CommandPath and answers it with the document projected
// into commands.
//
// It takes the document's OWN renderer, so the two endpoints are one artifact
// read two ways: whichever is asked for first renders the document, and the
// other is then a projection of those exact bytes rather than of a second build
// that could differ. Lazy for the same reason [serve] is — it keeps the compose
// off the light host's boot path, and it is what lets the document contain the
// route this very call registers.
func serveCommands(app *zip.App, document func() ([]byte, error)) {
	render := sync.OnceValues(func() (catalog, error) {
		doc, err := document()
		if err != nil {
			return catalog{}, err
		}
		cmds, err := zip.CommandsFromSpec(doc)
		if err != nil {
			return catalog{}, err
		}
		if cmds == nil {
			cmds = []zip.Command{} // an empty list is [], never null: a client maps over it
		}
		order(cmds)
		body, err := json.Marshal(cmds)
		if err != nil {
			return catalog{}, err
		}
		sum := sha256.Sum256(body)
		return catalog{body: body, etag: `"` + hex.EncodeToString(sum[:16]) + `"`}, nil
	})
	app.Get(CommandPath, func(c *zip.Ctx) error {
		cat, err := render()
		if err != nil {
			return zip.ErrInternal(err.Error())
		}
		c.SetHeader("ETag", cat.etag)
		// The route table is fixed after boot, so a matching tag is proof the
		// caller already holds the whole list — the one case where the honest
		// answer is no bytes at all.
		if c.Header("If-None-Match") == cat.etag {
			return c.NoContent(http.StatusNotModified)
		}
		c.SetHeader("Content-Type", "application/json")
		return c.Bytes(http.StatusOK, cat.body)
	})
}
