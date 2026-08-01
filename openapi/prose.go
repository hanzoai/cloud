package openapi

import (
	"fmt"
	"sort"
	"strings"
)

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
// An orphan is judged only inside the products THIS document publishes. The prose
// registry is process-wide and every app binary links cloud's core, so a subset is
// built with declarations belonging to subsystems it does not mount; those are not
// this app's business and are skipped. A declaration that lands in a product this
// app does publish, on a route it does not serve, is this app's defect.
func Complete(doc *Document) error {
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
		if p := Product(path); p != "" {
			published[p] = true
		}
	}
	var orphan []string
	for _, key := range describedRoutes() {
		if !published[Product(key.path)] {
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
			"Each is keyed to a route in a product this app publishes, and this app serves no such "+
			"route — so the prose renders nowhere while reading, in the source, as though it had "+
			"landed. Key it to the route as the router registers it, or delete it with the route",
			len(orphan), strings.Join(orphan, "\n  "))
	}
	return nil
}
