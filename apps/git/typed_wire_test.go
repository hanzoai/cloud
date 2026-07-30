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
