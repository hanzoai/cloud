package git

import (
	"sort"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/openapi"
)

// typed_wire_test.go turns git's typed/untyped PARTITION from prose into a GATE.
//
// apps/git/LLM.md said "COMPLETE at 24 typed / 24 refused" and named four
// refusal families. Prose cannot fail, so it decays two ways: a new raw route
// lands and the count is silently wrong, or a refusal is retired and the reason
// outlives the route it described. Both are invisible until somebody re-reads
// the file. The list below is the same partition as a VALUE the suite checks in
// both directions — every served operation is typed or named here, and every
// name still describes an operation git serves. Copied in shape from
// apps/team/typed_wire_test.go, which is the one form of this gate.

// untypedByDesign is the CLOSED list of git operations that are NOT typed ops,
// each with the wire fact that keeps it raw. A typed op is a route PLUS a
// registry entry — the one value the OpenAPI operation, the MCP tool, the CLI
// command and the SDK method all come from — so an operation missing from that
// registry is invisible to all four. These 24 are missing on purpose. Addresses
// are written the way the DOCUMENT writes them, which is the identity every
// projection keys on.
var untypedByDesign = map[string]string{
	// 1. The canonical-forge webhook. Auth IS the HMAC over the RAW received
	// bytes, verified before any parse (webhook.go), and zip's invoke unmarshals
	// BEFORE the handler — so a typed In destroys the exact bytes the signature
	// covers. It also answers a benign 204 to deliveries it ignores (non-push,
	// ref delete, bot author) where a typed op's decode failure would 400 and
	// retry-storm the forge.
	"POST /v1/git/webhook": "auth is an HMAC over the RAW request bytes, verified before parse; a typed " +
		"In decodes first and cannot re-derive them. Ignored deliveries answer 204, which a decode " +
		"failure would turn into a 400 the forge retries.",

	// 2. The smart-HTTP git pack protocol, on both hosts. Neither direction is
	// JSON: requests are application/x-git-*-request pack streams, responses are
	// x-git-*-advertisement / -result, and upload-pack STREAMS a multi-GB pack
	// (smart_http.go SendStream) rather than answering a value.
	"GET /v1/git/{org}/{repo}/info/refs": "answers the pkt-line ref advertisement as " +
		"application/x-git-*-advertisement bytes, not a JSON value.",
	"POST /v1/git/{org}/{repo}/git-upload-pack": "the request body is an x-git-upload-pack-request pack " +
		"stream and the response STREAMS the packfile; a typed op answers one JSON value.",
	"POST /v1/git/{org}/{repo}/git-receive-pack": "the request body is an x-git-receive-pack-request pack " +
		"stream and the response is the pkt-line report-status, not JSON.",
	"GET /{org}/{repo}/info/refs": "the git-host root form of the ref advertisement; pkt-line bytes, and " +
		"it falls THROUGH with c.Next() on a non-git Host (onGitHost), which a typed dispatch cannot do.",
	"POST /{org}/{repo}/git-upload-pack": "the git-host root form: a pack stream in, a streamed packfile " +
		"out, and a c.Next() fall-through on a non-git Host.",
	"POST /{org}/{repo}/git-receive-pack": "the git-host root form: a pack stream in, pkt-line " +
		"report-status out, and a c.Next() fall-through on a non-git Host.",

	// 3. The browser UI — Hanzo Git's server-rendered web surface (ui.go),
	// text/html from html/template. A typed dispatch ends in c.JSON(out). The six
	// root-level pages carry the same onGitHost c.Next() fall-through as family 2.
	// The JSON twin of every one of these IS a typed op (/v1/git/repos/{name}/…
	// refs|tree|blob|commits|readme), so the schema is not missing — it is at the
	// address that answers JSON.
	"GET /git":                   "server-rendered text/html (the repo list page); a typed Out answers JSON.",
	"GET /git/explore":           "server-rendered text/html (the public explore page); a typed Out answers JSON.",
	"GET /git/{org}/{repo}":      "server-rendered text/html (the repo page); a typed Out answers JSON.",
	"GET /git/{org}/{repo}/tree/{wildcard1}": "server-rendered text/html (the tree browser); a typed Out " +
		"answers JSON.",
	"GET /git/{org}/{repo}/blob/{wildcard1}": "server-rendered text/html (the blob view); a typed Out " +
		"answers JSON.",
	"GET /git/{org}/{repo}/commits": "server-rendered text/html (the commit log); a typed Out answers JSON.",
	"GET /": "the git-host root form of the repo list page: text/html, plus a c.Next() fall-through on a " +
		"non-git Host.",
	"GET /explore": "the git-host root form of the explore page: text/html, plus a c.Next() fall-through " +
		"on a non-git Host.",
	"GET /{org}/{repo}": "the git-host root form of the repo page: text/html, plus a c.Next() " +
		"fall-through on a non-git Host.",
	"GET /{org}/{repo}/tree/{wildcard1}": "the git-host root form of the tree browser: text/html, plus a " +
		"c.Next() fall-through on a non-git Host.",
	"GET /{org}/{repo}/blob/{wildcard1}": "the git-host root form of the blob view: text/html, plus a " +
		"c.Next() fall-through on a non-git Host.",
	"GET /{org}/{repo}/commits": "the git-host root form of the commit log: text/html, plus a c.Next() " +
		"fall-through on a non-git Host.",

	// 4. The ZAP procedure adapters (zap.go). Their PUBLISHED envelope is the
	// contract the bridge's clients parse: success is cloud.OK, failure is a
	// non-2xx {status:"error", msg}. A typed op reports failure by RETURNING an
	// error, which zip renders as its own {status:<int>, code, error:<msg>}
	// (zip/ctx.go HTTPError) — a different field name and a different type for
	// `status`. cloud.Bridge only applies a handler-set status on SUCCESS, and the
	// only exported setters are Created/Accepted, so nothing lets a typed op
	// answer a 4xx with this body. Typing these renames the error field on a live
	// wire. They shrink by client migration, not by typing: the shared /zap plane
	// already replays the typed /v1 ops frame-for-frame.
	"POST /v1/git/zap/createRepo": "the ZAP envelope: a failure is a non-2xx {status:\"error\", msg}, " +
		"where a typed op's returned error renders zip's {status:<int>, code, error}.",
	"POST /v1/git/zap/listRepos": "the ZAP envelope: a failure is a non-2xx {status:\"error\", msg}, " +
		"where a typed op's returned error renders zip's {status:<int>, code, error}.",
	"POST /v1/git/zap/getRepo": "the ZAP envelope: a failure is a non-2xx {status:\"error\", msg}, " +
		"where a typed op's returned error renders zip's {status:<int>, code, error}.",
	"POST /v1/git/zap/deleteRepo": "the ZAP envelope: a failure is a non-2xx {status:\"error\", msg}, " +
		"where a typed op's returned error renders zip's {status:<int>, code, error}.",
	"POST /v1/git/zap/usage": "the ZAP envelope: a failure is a non-2xx {status:\"error\", msg}, " +
		"where a typed op's returned error renders zip's {status:<int>, code, error}.",
}

// gitOps reads BOTH projections of the live router at their one shared address
// form: what the document says is served, and which of those carry a typed
// registry entry. EVERY served operation counts — git's surface is not confined
// to /v1/git (the browser UI is at /git/*, and the git-host forms are at the
// root), and a route being outside the prefix does not make it less of a product
// surface. That is also what makes the gate total: a new route at an address
// nobody expected is caught, not filtered out.
func gitOps(t *testing.T) (served map[string]bool, typed map[string]string) {
	t.Helper()
	app := mountApp(t)
	doc, err := openapi.Spec(app, openapi.Info{Title: "git", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	reg, err := openapi.Typed(app)
	if err != nil {
		t.Fatalf("typed registry: %v", err)
	}
	served, typed = map[string]bool{}, map[string]string{}
	for path, item := range doc.Paths {
		for method := range item {
			served[strings.ToUpper(method)+" "+path] = true
		}
	}
	for key, op := range reg.Ops {
		typed[key] = op.Description
	}
	return served, typed
}

// TestEveryRouteIsTypedOrNamed fails when a git operation is neither a typed op
// nor one of the 24 above — so the next route added here is typed by default,
// and dropping one out of the registry takes a deliberate edit with a reason.
func TestEveryRouteIsTypedOrNamed(t *testing.T) {
	served, typed := gitOps(t)

	var untyped []string
	for key := range served {
		if _, ok := typed[key]; ok {
			continue
		}
		if _, named := untypedByDesign[key]; named {
			continue
		}
		untyped = append(untyped, key)
	}
	if len(untyped) > 0 {
		sort.Strings(untyped)
		t.Errorf("operation(s) with no registry entry and no reason: %s\n"+
			"A route that is not a typed op has no schema, no prose, no MCP tool, no CLI command and no SDK "+
			"method. Convert it (zip.Get/Post/... on the /v1/git group), or add it to untypedByDesign with "+
			"the reason typing it would move the wire.", strings.Join(untyped, ", "))
	}
	// The reasons must describe operations that exist, or the list is stale prose.
	for key := range untypedByDesign {
		if !served[key] {
			t.Errorf("untypedByDesign names %q, which git no longer serves", key)
		}
	}
	// A typed op named as a refusal is a contradiction — one of the two is wrong.
	for key := range untypedByDesign {
		if _, ok := typed[key]; ok {
			t.Errorf("untypedByDesign names %q, which IS a typed op — delete the entry", key)
		}
	}
}

// TestEveryTypedOpIsDescribed proves the lifted prose reached the binary. That
// prose IS the product surface: it becomes the OpenAPI description AND the MCP
// tool description a model reads to pick the tool. zipdoc_gen.go is what carries
// it in, so an op added without regenerating shows up here as a nameless tool.
func TestEveryTypedOpIsDescribed(t *testing.T) {
	_, typed := gitOps(t)
	if len(typed) == 0 {
		t.Fatal("no typed git ops in the registry at all")
	}
	for key, desc := range typed {
		if strings.TrimSpace(desc) == "" {
			t.Errorf("%s has no description — run: go generate -run zipdoc ./apps/git/...", key)
		}
	}
}

// declaredBodies is the CLOSED list of untypedByDesign routes that DECLARE the
// request they read (openapi.Register, webhook.go). "Cannot be a typed op" is not
// "must be undocumented": a route publishing an operationId and nothing else is
// indistinguishable, to every SDK generator, from a route that takes no body — so
// the forge webhook shipped with nowhere to put the delivery and three ZAP
// procedures with nowhere to put the repo name.
//
// The value is the media type the declaration renders under, which is the fact
// worth pinning: JSON for a body that is a document, octet-stream for one that is
// opaque bytes. Every OTHER route in untypedByDesign must publish NO requestBody,
// because it reads none — the two ZAP procedures that ignore the body (listRepos,
// usage), the ref advertisement, and the twelve HTML pages. Declaring a body for
// one of those would replace an honest silence with a fresh falsehood.
var declaredBodies = map[string]string{
	"POST /v1/git/webhook":                       "application/json",
	"POST /v1/git/zap/createRepo":                "application/json",
	"POST /v1/git/zap/getRepo":                   "application/json",
	"POST /v1/git/zap/deleteRepo":                "application/json",
	"POST /v1/git/{org}/{repo}/git-upload-pack":  "application/octet-stream",
	"POST /v1/git/{org}/{repo}/git-receive-pack": "application/octet-stream",
	"POST /{org}/{repo}/git-upload-pack":         "application/octet-stream",
	"POST /{org}/{repo}/git-receive-pack":        "application/octet-stream",
}

// TestRefusedRoutesDeclareTheBodyTheyRead holds the description of the 24 refusals
// to the list above, in both directions.
func TestRefusedRoutesDeclareTheBodyTheyRead(t *testing.T) {
	doc, err := openapi.Spec(mountApp(t), openapi.Info{Title: "git", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	for key := range untypedByDesign {
		method, path, ok := strings.Cut(key, " ")
		if !ok {
			t.Fatalf("malformed key %q", key)
		}
		op := doc.Paths[path][strings.ToLower(method)]
		if op == nil {
			continue // TestEveryRouteIsTypedOrNamed owns the "still served" half
		}
		want, declared := declaredBodies[key]
		switch {
		case declared && op.RequestBody == nil:
			t.Errorf("%s reads a %s body and declares none — every generated SDK offers "+
				"this call with no payload parameter. Restore its openapi.Register (webhook.go).", key, want)
		case declared:
			// Operation.RequestBody is `any` because two seams write it; a REFUSED
			// route's can only have come from openapi.Register.
			body, ok := op.RequestBody.(*openapi.RequestBody)
			if !ok {
				t.Errorf("%s: request body is %T, not the *openapi.RequestBody Register writes", key, op.RequestBody)
				continue
			}
			if _, ok := body.Content[want]; !ok {
				got := make([]string, 0, len(body.Content))
				for media := range body.Content {
					got = append(got, media)
				}
				sort.Strings(got)
				t.Errorf("%s declares %v, want %s", key, got, want)
			}
		case op.RequestBody != nil:
			t.Errorf("%s declares a request body but reads none — an SDK would demand a "+
				"payload the handler never looks at. Either it now reads one (add it to "+
				"declaredBodies) or the declaration is false.", key)
		}
	}
	for key := range declaredBodies {
		if _, named := untypedByDesign[key]; !named {
			t.Errorf("declaredBodies names %q, which is not a refused route — a TYPED op states "+
				"its own body from its In type, so a declaration beside it is a second source", key)
		}
	}
}

// cliNameCollisions is the CLOSED list of CLI command names that MORE THAN ONE of
// git's typed ops derives — the fourth projection's version of the partition
// above, and a live defect rather than a design choice.
//
// A typed op is one value with four projections, and three of them key on the
// op's own identity: the OpenAPI path, the MCP tool name, the SDK method. The CLI
// keys on a name zip SPELLS from the route (commandName, zip/cli.go), and that
// spelling keeps the segments before the first path parameter and after the last
// one while dropping everything between — so `mirrors` and `subscriptions`, the
// words that say WHICH thing a DELETE removes, never reach the name. All three
// DELETEs below land on `repos-delete`, which means two of git's 24 typed ops
// have no command a caller can reach: the runner has one name and three routes.
//
// Neither the wire nor the document is involved — every one of these is served,
// published and described under its own operationId. It is a projection defect
// only TYPING can surface, because an untyped route has no command to collide.
//
// The fix belongs in commandName (carry the interior static segments), NOT in
// zip.WithOperationID here, which would make git's operation ids a special case
// of a general bug (root LLM.md, failure mode 6). Until it lands this pins the
// damage in BOTH directions: a new collision fails, and a collision that is
// GONE fails too, so the zip fix retires this list instead of outliving it.
var cliNameCollisions = map[string][]string{
	"repos-delete": {
		"DELETE /v1/git/repos/:name",
		"DELETE /v1/git/repos/:name/mirrors/:id",
		"DELETE /v1/git/repos/:name/subscriptions/:id",
	},
}

// TestCLINamesCollideExactlyWhereKnown holds the CLI projection to the list above.
func TestCLINamesCollideExactlyWhereKnown(t *testing.T) {
	byName := map[string][]string{}
	for _, c := range mountApp(t).Commands() {
		byName[c.Name] = append(byName[c.Name], c.Method+" "+c.Path)
	}
	for name, routes := range byName {
		if len(routes) < 2 {
			continue
		}
		sort.Strings(routes)
		want, known := cliNameCollisions[name]
		if !known {
			t.Errorf("NEW CLI name collision: %d typed ops derive the command %q (%s).\n"+
				"All but one are unreachable from the CLI. Either the route spelling changed or a "+
				"route was added under an existing parameter; do not paper over it with "+
				"zip.WithOperationID.", len(routes), name, strings.Join(routes, ", "))
			continue
		}
		if strings.Join(routes, ", ") != strings.Join(want, ", ") {
			t.Errorf("CLI name %q now collides over %v, the list says %v", name, routes, want)
		}
	}
	for name := range cliNameCollisions {
		if len(byName[name]) < 2 {
			t.Errorf("cliNameCollisions still names %q, which no longer collides — the zip fix "+
				"landed: delete the entry (and the class note in apps/git/LLM.md)", name)
		}
	}
}
