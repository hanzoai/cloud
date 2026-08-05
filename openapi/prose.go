package openapi

import (
	"fmt"
	"sort"
	"strings"
)

// Owner reports which app the fleet routes path to, and "" when nothing does.
//
// It is a PARAMETER and not an import because the dependency runs one way only:
// manifest is the routing table, and manifest's own tests read this package to pin
// the spec door's address (manifest/openapi_test.go), so an openapi that imported
// manifest back would make manifest's test binary an import cycle — the compiler
// says so. The fleet passes manifest.OwnerOf, at the one producer (describe.go).
//
// nil is refused rather than defaulted. A caller with no routing table cannot tell
// a misfiled declaration from another app's, and the safe-looking default — judge
// nothing — is a gate that passes everything while still being called.
type Owner func(path string) string

// Complete refuses a document a reader cannot read.
//
// One law, stated as a bijection between operations and prose: EVERY OPERATION
// SAYS WHAT IT DOES, AND EVERYTHING SAID IS SAID ABOUT AN OPERATION. Both halves
// fail the same way — a consumer is handed an address and no sentence — so both
// are refused here rather than in two places under two names.
//
// # Why the producer refuses instead of a consumer coping
//
// A projection cannot repair its source, and every branch that handles "the
// source didn't say" hides the defect at the one place it could be fixed. That is
// not a design argument, it is a post-mortem: hanzoai/cli's generated tree used to
// fall back to the operation's own HTTP ROUTE when it had no sentence, so `hanzo
// platform health` shipped "GET /v1/platform/health" as its help. Nobody filed a
// bug, because a mechanical line reads exactly like a deliberate one — the
// fallback did not report the gap, it DISGUISED it, for as long as it existed.
// The CLI has since deleted the fallback and refuses to emit an undescribed
// command; this is the same refusal one step upstream, where the sentence can
// actually be written. Between them there is no place left for a route to stand
// in for prose.
//
// And there is no placeholder here either. A visible "TODO: describe this
// operation" is the same fallback with better manners: it would travel into eight
// generated SDKs, the MCP tool list and docs.hanzo.ai — every surface that cannot
// fix it — and would be no more visible in the one that can. A gap belongs in a
// build failure that names a file, not in a published artifact.
//
// # The remedy is always the same shape, and the message names it
//
// Prose reaches this document by exactly two seams, and both are already the
// operation's own source: a typed op carries the Go doc comment on its handler
// (zipdoc lifts it at build time), and a route the wire refuses to let become a
// typed op declares it with [Describe] beside that route. Neither is a table of
// strings anybody edits separately — which is why the second half of the law
// matters as much as the first. [Describe] renders nothing when its key is not a
// live route (that is what makes the registry unable to invent an operation), so a
// mis-keyed declaration is prose that was written, reviewed, and then silently
// dropped: POST /v1/store/storefront-token published an operationId and nothing
// else for as long as its description sat under the store's old /v1/store/token
// address. Silence in the safe direction, and total silence in the unsafe one.
//
// An orphan is judged only inside the surface THIS document publishes. The prose
// registry is process-wide and every app binary links cloud's core, so a subset is
// built with declarations belonging to subsystems it does not mount; those are not
// this app's business and are skipped. A declaration that lands on an address this
// document's own apps own, on a route it does not serve, is this app's defect.
//
// # The unit of that judgement is the APP, never the product segment
//
// A product is not an app, and reading one as the other is how this gate produced
// its own false positive. [Product] collapses /v1/s3 and /v1/s3/buckets to "s3",
// but the manifest gives them to different apps on purpose: /v1/s3 PROVISIONS an s3
// resource (provisioning) and /v1/s3/buckets is the DATA plane (storage), separated
// by longest prefix exactly as the router separates them. Keyed on the segment,
// storage was charged with provisioning's POST /v1/s3 — a declaration provisioning
// both serves and describes — so an app with nothing wrong with it could not
// project its own document, and `make surface-check` died there.
//
// That is not one awkward pair to special-case: FOURTEEN products are answered by
// more than one app (billing, catalog, finance, plans, platform, search, usage,
// vector among them), so the segment is simply the wrong value. owner is the right
// one — it is the routing rule itself (manifest.OwnerOf), asked rather than
// re-derived, which is what keeps this from drifting away from what the host
// actually does.
func Complete(doc *Document, owner Owner) error {
	if owner == nil {
		return fmt.Errorf("openapi: Complete needs the fleet's route ownership to tell a " +
			"misfiled declaration from another app's — pass manifest.OwnerOf. Judging " +
			"nothing would leave this gate green on every defect it exists to catch")
	}
	var bare []string
	for _, path := range sortedKeys(doc.Paths) {
		item := doc.Paths[path]
		for _, method := range sortedKeys(item) {
			op := item[method]
			if strings.TrimSpace(op.Summary) == "" && strings.TrimSpace(op.Description) == "" {
				bare = append(bare, strings.ToUpper(method)+" "+path)
			}
		}
	}
	if len(bare) > 0 {
		return fmt.Errorf("%d operation(s) say nothing about themselves:\n  %s\n\n"+
			"Every operation states what it does for the person calling it, and that sentence has one "+
			"home: the Go doc comment on the handler, which zipdoc lifts into this document, the MCP "+
			"tool list, every generated SDK and the CLI's help. A route the wire keeps untyped states "+
			"it with openapi.Describe beside the route instead. Nothing downstream can supply it — "+
			"write it where the handler lives",
			len(bare), strings.Join(bare, "\n  "))
	}

	published := map[string]bool{}
	for path := range doc.Paths {
		if a := owner(path); a != "" {
			published[a] = true
		}
	}
	var orphan []string
	for _, key := range describedRoutes() {
		// The host's doors belong to NO APP, and owner cannot say so.
		//
		// cmd/cloud registers GET /v1/openapi.json as a static route that outranks
		// ai's "/v1" remainder; manifest/openapi_test.go records the manifest's own
		// answer ("ai") as exactly the misroute the host's claim exists to correct.
		// The declaration is made in THIS package — see the inits beside [Path] and
		// [CommandPath] — because, in their words, they are the operations with no
		// owning subsystem.
		//
		// So every app whose paths fall to that remainder would be charged with a
		// door no app mounts. It is not going unjudged: they render in the fleet
		// document the host serves, which is where the operations actually are.
		// [Product] hid this by accident for the document, returning "" for any
		// segment holding a dot; /v1/commands has no dot to hide behind, so it
		// arrived as an orphan charged to ai the moment it was declared. owner has
		// to decline both on purpose, and [Door] is where serve says which.
		if Door(key.path) {
			continue
		}
		// The declaration is keyed by the ROUTER's pattern (/v1/kms/secrets/+) and
		// the document by the rendered template (/v1/kms/secrets/{wildcard1}). Both
		// forms answer owner identically — a manifest prefix is static leading
		// segments, so it matches before either form's first parameter.
		if !published[owner(key.path)] {
			continue
		}
		tmpl, _ := translate(key.path)
		if item := doc.Paths[tmpl]; item[strings.ToLower(key.method)] != nil {
			continue
		}
		orphan = append(orphan, key.method+" "+key.path)
	}
	if len(orphan) > 0 {
		sort.Strings(orphan)
		return fmt.Errorf("%d description(s) name no operation:\n  %s\n\n"+
			"Each is keyed to an address the fleet routes to THIS app, and this app serves no such "+
			"route — so the prose renders nowhere while reading, in the source, as though it had "+
			"landed. Key it to the route as the router registers it, or delete it with the route",
			len(orphan), strings.Join(orphan, "\n  "))
	}
	return nil
}
