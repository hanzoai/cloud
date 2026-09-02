package git

import (
	"github.com/hanzoai/cloud"
	"bytes"
	"context"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/openapi"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// typed_wire_test.go turns git's typed/untyped PARTITION from prose into a GATE.
//
// apps/git/LLM.md said "COMPLETE at 24 typed / 24 refused" while thirty routes
// were refused, and named four refusal families. Prose cannot fail, so it decays
// two ways: a new raw route lands and the count is silently wrong, or a refusal
// is retired and the reason outlives the route it described. Both are invisible
// until somebody re-reads the file. The list below is the same partition as a
// VALUE the suite checks in both directions — every served operation is typed or
// named here, and every name still describes an operation git serves. Copied in
// shape from apps/team/typed_wire_test.go, which is the one form of this gate.

// untypedByDesign is the CLOSED list of git operations that are NOT typed ops,
// each with the wire fact that keeps it raw. A typed op is a route PLUS a
// registry entry — the one value the OpenAPI operation, the MCP tool, the CLI
// command and the SDK method all come from — so an operation missing from that
// registry is invisible to all four. These 18 are missing on purpose. Addresses
// are written the way the DOCUMENT writes them, which is the identity every
// projection keys on.
var untypedByDesign = map[string]string{
	// 1. The RETIRED canonical-forge webhook (webhook.go), kept as a tombstone
	// that answers 410 naming platform.hanzo.ai.
	//
	// Its old reason — "a typed op needs an In or an Out; a tombstone has
	// neither" — was FALSE: `noInput` and `noContent` are this package's own
	// (ops.go) and six typed ops already use them. What holds is ORDER, and it is
	// measured rather than argued, below and in webhook_test.go.
	"POST /v1/git/webhook": "ANY bytes answer 410 — which is what retired MEANS, and what " +
		"TestWebhookIsGoneForEveryDelivery drives with a real push, an empty body and a malformed " +
		"one. op.invoke json-decodes a non-empty body BEFORE the handler is entered (zip " +
		"typed.go:485-490) and answers an undecodable one 400, so a typed op would tell `{not json` " +
		"its body is bad instead of where the delivery belongs. Run it: " +
		"TestATypedTombstoneWouldRefuseABodyItAnswersToday.",

	// 2. The smart-HTTP git pack protocol. Neither direction is JSON: requests
	// are application/x-git-*-request pack streams, responses are
	// x-git-*-advertisement / -result, and upload-pack STREAMS a multi-GB pack
	// (smart_http.go SendStream) rather than answering a value.
	"GET /v1/git/{org}/{repo}/info/refs": "answers the pkt-line ref advertisement as " +
		"application/x-git-*-advertisement bytes, not a JSON value.",
	"POST /v1/git/{org}/{repo}/git-upload-pack": "the request body is an x-git-upload-pack-request pack " +
		"stream and the response STREAMS the packfile; a typed op answers one JSON value.",
	"POST /v1/git/{org}/{repo}/git-receive-pack": "the request body is an x-git-receive-pack-request pack " +
		"stream and the response is the pkt-line report-status, not JSON.",

	// The same three one segment deeper, for a project-scoped repo: identical
	// handlers and identical non-JSON wire, addressed as :org/:project/:repo
	// because a git client sends no headers and the scope has nowhere else to
	// ride (git.go, cloneURL).
	"GET /v1/git/{org}/{project}/{repo}/info/refs": "the project-scoped ref advertisement; " +
		"application/x-git-*-advertisement bytes, not a JSON value.",
	"POST /v1/git/{org}/{project}/{repo}/git-upload-pack": "project-scoped: an x-git-upload-pack-request " +
		"pack stream in, a STREAMED packfile out.",
	"POST /v1/git/{org}/{project}/{repo}/git-receive-pack": "project-scoped: an x-git-receive-pack-request " +
		"pack stream in, pkt-line report-status out.",

	// 3. The browser UI — Hanzo Git's server-rendered web surface (ui.go),
	// text/html from html/template. A typed dispatch ends in c.JSON(out). The
	// JSON twin of every one of these IS a typed op (/v1/git/repos/{name}/…
	// refs|tree|blob|commits|readme), so the schema is not missing — it is at the
	// address that answers JSON.
	"GET /v1/git":                               "server-rendered text/html (the repo list page); a typed Out answers JSON.",
	"GET /v1/git/explore":                       "server-rendered text/html (the public explore page); a typed Out answers JSON.",
	"GET /v1/git/{org}/{repo}":                  "server-rendered text/html (the repo page); a typed Out answers JSON.",
	"GET /v1/git/{org}/{repo}/tree/{wildcard1}": "server-rendered text/html (the tree browser); a typed Out answers JSON.",
	"GET /v1/git/{org}/{repo}/blob/{wildcard1}": "server-rendered text/html (the blob view); a typed Out answers JSON.",
	"GET /v1/git/{org}/{repo}/commits":          "server-rendered text/html (the commit log); a typed Out answers JSON.",

	// 4. The ZAP procedure adapters (zap.go). One fact, five addresses — see
	// reasonZAP.
	"POST /v1/git/zap/createRepo": reasonZAP,
	"POST /v1/git/zap/listRepos":  reasonZAP,
	"POST /v1/git/zap/getRepo":    reasonZAP,
	"POST /v1/git/zap/deleteRepo": reasonZAP,
	"POST /v1/git/zap/usage":      reasonZAP,
}

// reasonZAP is the fact the five ZAP procedure adapters share, and the recorded
// version of it had EXPIRED twice over.
//
// It said a typed op's returned error renders `{status:<int>, code, error:<msg>}`
// and that nothing lets one answer a 4xx with a body of its own. Neither holds at
// the pinned zip: a refusal renders RFC 9457 problem-details — `{type, title,
// status, detail, code}`, no `error` key at all (zip problem.go:39-77) — and
// WithStatus is variadic over any status with StatusCoder picking one
// (zip typed.go:154, 189), so the success envelope and a 400/404/409/500 carrying
// {status:"error", msg} are both an ordinary typed Out today.
//
// What still holds is ORDER, and it is a fact no op can reach from inside itself.
// op.invoke decodes the request body BEFORE the handler is entered (zip
// typed.go:485-490) and answers an undecodable one with that problem document —
// whose `status` member is a NUMBER. The bridge unmarshals every non-2xx body
// into its own envelope, whose `status` is a STRING (zapface/dispatch.go:35-40),
// so the unmarshal FAILS and the ZAP client is told `INVALID_RESPONSE —
// non-envelope response (HTTP 400)` (dispatch.go:82-86) instead of the sentence
// the handler wrote. Today that same body answers {status:"error", msg:"invalid
// body"} and the bridge forwards the msg (dispatch.go:88-91).
//
// The 403 leg is NOT what holds them, and the old reason implied it did: dispatch
// short-circuits 401/403 before it parses anything (dispatch.go:76-80).
//
// They shrink by client migration, not by typing: the shared /zap plane already
// replays the typed /v1 ops frame-for-frame.
const reasonZAP = "the bridge's envelope, and the order it is written in: op.invoke decodes the " +
	"body before the handler (zip typed.go:485-490) and answers an undecodable one with a problem " +
	"document whose `status` is a NUMBER, which zapface cannot unmarshal into an envelope whose " +
	"`status` is a STRING — so the client is told INVALID_RESPONSE instead of the handler's " +
	"sentence (zapface/dispatch.go:35-40,82-91)."

// gitOps reads BOTH projections of the live router at their one shared address
// form: what the document says is served, and which of those carry a typed
// registry entry. EVERY served operation counts, at whatever address — git's
// surface is /v1/git now, pages included, but the gate reads the document rather
// than that prefix. That is what makes it total: a new route at an address
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
// nor one of the 18 above — so the next route added here is typed by default,
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

// voidOps is the CLOSED list of git's typed ops whose handler returns a nil *Out
// — the ONE fact about a typed op the document cannot reflect, because it lives
// in the handler's body and not in its types. zip writes a nil *Out as 204 with
// no body (typed.go), and keys the DOCUMENT on 204 only when the Out type has no
// NAME. `noContent` used to be a defined type here, so these four published "200
// with a `noContent` body" about a wire that has always answered 204 with none —
// git was the only app in the fleet that DEFINED the type rather than aliasing
// it, and the only publisher of a `noContent` schema. Every SDK generated from
// that document expected a status the service never sends.
var voidOps = map[string]bool{
	"DELETE /v1/git/repos/{name}":                    true,
	"DELETE /v1/git/keys/{id}":                       true,
	"DELETE /v1/git/repos/{name}/subscriptions/{id}": true,
	"DELETE /v1/git/repos/{name}/mirrors/{id}":       true,
}

// TestVoidOpsPublishTheStatusTheySend holds every typed op's DOCUMENTED success
// response to the one its handler can produce, in both directions: a void op must
// publish 204 and no content, and every other typed op must publish a 2xx that
// carries a schema. The wire half is asserted where the calls are made
// (git_test.go, ssh_test.go, lifecycle_test.go all require 204 from these four);
// this is the half that keeps the document from drifting away from it.
func TestVoidOpsPublishTheStatusTheySend(t *testing.T) {
	app := mountApp(t)
	reg, err := openapi.Typed(app)
	if err != nil {
		t.Fatalf("typed registry: %v", err)
	}
	for key, op := range reg.Ops {
		responses, ok := op.Responses.(map[string]any)
		if !ok {
			t.Errorf("%s: responses are %T, not the object a typed op projects", key, op.Responses)
			continue
		}
		for code, raw := range responses {
			body, _ := raw.(map[string]any)
			hasContent := body["content"] != nil
			switch {
			case voidOps[key] && (code != "204" || hasContent):
				t.Errorf("%s returns a nil *Out — the wire is 204 with no body — but the document says "+
					"%s, content=%t. Make its Out the UNNAMED empty struct (noContent, ops.go): zip keys "+
					"204 on the Out type having no name.", key, code, hasContent)
			case !voidOps[key] && !hasContent:
				t.Errorf("%s publishes %s with no schema, so every generated SDK drops the body it "+
					"returns. Either its Out type went void (add it to voidOps) or the projection "+
					"regressed.", key, code)
			}
		}
	}
	for key := range voidOps {
		if _, ok := reg.Ops[key]; !ok {
			t.Errorf("voidOps names %q, which is not a typed git op", key)
		}
	}
}

// declaredBodies is the CLOSED list of untypedByDesign routes that DECLARE the
// request they read. "Cannot be a typed op" is not "must be undocumented": a
// route publishing an operationId and nothing else is indistinguishable, to every
// SDK generator, from a route that takes no body — so three ZAP procedures
// shipped with nowhere to put the repo name and four pack POSTs with nowhere to
// put the pack.
//
// Each declaration lives BESIDE the family it describes — the ZAP three in
// zap.go's init, the pack four in smart_http.go's, both read off the same table
// their prose is. They used to sit together in webhook.go, one hand-written list
// beside a loop, which is how four of them came to name addresses nothing
// registers and render nothing at all.
//
// The value is the media type the declaration renders under, which is the fact
// worth pinning: JSON for a body that is a document, octet-stream for one that is
// opaque bytes. Every OTHER route in untypedByDesign must publish NO requestBody,
// because it reads none — the retired webhook, the two ZAP procedures that ignore
// the body (listRepos, usage), the ref advertisement, and the six HTML pages.
// Declaring a body for one of those would replace an honest silence with a fresh
// falsehood.
var declaredBodies = map[string]string{
	// No /v1/git/webhook entry: it was retired to a 410 and reads nothing, so
	// declaring a body would hand every SDK a payload parameter for a call that
	// ignores it — the "fresh falsehood" this list exists to prevent.
	"POST /v1/git/zap/createRepo":                          "application/json",
	"POST /v1/git/zap/getRepo":                             "application/json",
	"POST /v1/git/zap/deleteRepo":                          "application/json",
	"POST /v1/git/{org}/{repo}/git-upload-pack":            "application/octet-stream",
	"POST /v1/git/{org}/{repo}/git-receive-pack":           "application/octet-stream",
	"POST /v1/git/{org}/{project}/{repo}/git-upload-pack":  "application/octet-stream",
	"POST /v1/git/{org}/{project}/{repo}/git-receive-pack": "application/octet-stream",
}

// TestRefusedRoutesDeclareTheBodyTheyRead holds the description of the 18 refusals
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
				"this call with no payload parameter. Restore its openapi.Register, in the init "+
				"beside the family it belongs to (zap.go, smart_http.go).", key, want)
		case declared:
			// Operation.RequestBody is `any` because two clients write it; a REFUSED
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
// DELETEs below land on `repos-delete`, and reading ONE pull request lands on
// `repos-get` beside reading one repo, because `pulls` sits between two
// parameters and is dropped. Three of git's 28 typed ops therefore have no
// command a caller can reach.
//
// The two families differ in one way worth stating: the DELETEs collide with
// each other, so which one `repos-delete` reaches is arbitrary. `repos-get`
// collides a two-segment route with a four-segment one, and the shorter wins on
// specificity — so reading a repo works from the CLI and reading a pull request
// is the projection that is lost. The wire and the document are unaffected: both
// are served and published under their own operationId.
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
	"repos-get": {
		"GET /v1/git/repos/:name",
		"GET /v1/git/repos/:name/pulls/:number",
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

// proseless is the CLOSED list of published properties that carry NO description
// because the CLIENT they arrived through cannot carry one — not because nobody wrote
// it. All three DO have doc comments in zap.go; reflection cannot see them.
//
// It is exact in BOTH directions. A bare property anywhere else goes red, and an
// entry here that starts publishing prose goes red too — that is the day the
// generator learns, and this ledger must shrink then rather than outlive the gap.
var proseless = map[string]bool{
	// REFLECTION CLIENT. The three ZAP procedures that read a body are declared with
	// openapi.Register (webhook.go) rather than typed, because they are raw handlers
	// binding a shared envelope — the reason is in untypedByDesign above. Register
	// derives its schema by REFLECTION, and Go drops comments at compile time, so
	// zipdoc — which walks zip's TYPED registrations — can never reach a type that
	// arrives this way.
	"zapProcReq.name":        true,
	"zapProcReq.project":     true,
	"zapProcReq.description": true,
}

// TestEveryPublishedFieldIsDescribed closes the half of the surface the gates above
// cannot see. They prove every route's ADDRESS is typed or named and that a refused
// route declares the BODY it reads; neither says whether that body's FIELDS mean
// anything to a reader, and those come from a different place — a doc comment on
// each one, which zipdoc lifts one at a time.
//
// It matters here because most of this surface is a value with a rule behind it. A
// repo `name` is not free text but ^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$ with a trailing
// ".git" stripped, and it is the last path segment of both clone URLs. `sizeBytes`
// is the on-disk measurement BILLING METERS, not a display figure. `public` grants
// anonymous READ and nothing else — push and the whole control plane stay org-authed
// — so reading it as "world-writable" is a security misreading a name alone invites.
// A tree entry's `mode` is git's octal ("100644", "040000", "120000"), not a number.
//
// The gate checks presence, not meaning. A description restating the field's name is
// worse than none, and only a reader catches that.
func TestEveryPublishedFieldIsDescribed(t *testing.T) {
	doc, err := openapi.Spec(mountApp(t), openapi.Info{Title: "git", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	if doc.Components == nil || len(doc.Components.Schemas) == 0 {
		t.Fatal("git publishes no schemas at all — the gate would pass vacuously")
	}
	published, err := openapi.Bare(doc)
	if err != nil {
		t.Fatalf("bare: %v", err)
	}

	var bare, stale []string
	seen := map[string]bool{}
	for _, path := range published {
		seen[path] = true
		if !proseless[path] {
			bare = append(bare, path)
		}
	}
	for path := range proseless {
		if !seen[path] {
			stale = append(stale, path)
		}
	}

	if len(bare) > 0 {
		t.Errorf("%d published schema propert(ies) with no description: %s\n"+
			"Write the field's OWN doc comment — a header above a group of fields is lifted onto "+
			"the first of them alone — then run: make -C apps/git describe",
			len(bare), strings.Join(bare, ", "))
	}
	if len(stale) > 0 {
		sort.Strings(stale)
		t.Errorf("proseless names propert(ies) that are gone or now described: %s\n"+
			"An exemption that outlives its cause is how a generator gap becomes permanent — "+
			"delete the entr(ies).", strings.Join(stale, ", "))
	}
}

// probeApp is a bare app the two mechanism tests below register a throwaway op
// on. It mounts NO subsystem: the question is what zip and cloud's projections do
// with a registration, and mounting git would only add noise it does not turn on.
func probeApp(t *testing.T) *zip.App {
	t.Helper()
	return zip.New(zip.Config{Logger: luxlog.New("test"), DisableStartupMessage: true})
}

// TestATypedTombstoneWouldRefuseABodyItAnswersToday RUNS the reason POST
// /v1/git/webhook is not a typed op, rather than asserting it.
//
// The route's contract is that ANY bytes answer 410 — TestWebhookIsGoneForEveryDelivery
// drives a real push, an empty body and `{not json` and requires 410 from all
// three, because a "retired" endpoint with an input that changes the outcome is
// not retired. A typed op cannot hold that, and this is the demonstration: the
// same route shape, typed, answers the two decodable bodies exactly as the raw
// one does and answers the malformed one 400, because op.invoke decodes before
// the handler is entered (zip typed.go:485-490).
//
// It is written to go RED the day that stops being true: if every case answers
// 410 here, zip has learned to decline the decode and the tombstone can be typed
// — delete the untypedByDesign entry and this test with it.
func TestATypedTombstoneWouldRefuseABodyItAnswersToday(t *testing.T) {
	app := probeApp(t)
	zip.Post(app.Group("/v1/probe"), "/webhook", func(context.Context, *cloud.Unit) (*cloud.Unit, error) {
		return nil, zip.Errorf(http.StatusGone, "gone")
	})

	for _, tc := range []struct {
		name string
		body string
		want int
	}{
		{"an empty body", "", http.StatusGone},
		{"a real JSON delivery", `{"ref":"refs/heads/main"}`, http.StatusGone},
		{"a malformed body", "{not json", http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var body io.Reader
			if tc.body != "" {
				body = bytes.NewReader([]byte(tc.body))
			}
			req := httptest.NewRequest(http.MethodPost, "/v1/probe/webhook", body)
			req.Header.Set("Content-Type", "application/json")
			resp, err := app.Test(req, testCfg)
			if err != nil {
				t.Fatalf("probe: %v", err)
			}
			out, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode != tc.want {
				t.Fatalf("a typed op answered %d, want %d — the decode order recorded in "+
					"untypedByDesign[\"POST /v1/git/webhook\"] has changed. If the malformed "+
					"body now answers 410, type the tombstone and delete both the entry and "+
					"this test. (%s)", resp.StatusCode, tc.want, out)
			}
		})
	}
}

// TestTheHTMLPagesCannotBeTypedOps RUNS the reason SIX entries share — every
// page this app renders for a browser — instead of asserting it six times.
//
// Whatever a typed op returns, the answer is application/json: its only response
// path is c.JSON(out), and fiber stamps that content type over anything set
// earlier. A route that renders html/template therefore cannot be one, and the
// reason is a property of the framework rather than a claim about this package.
// The day a typed op can answer under a content type of its own, this goes red
// and all six entries want re-reading.
//
// It replaced a test that ran a DIFFERENT mechanism the tree and blob pages once
// carried on top of this one: their captured segment is a greedy wildcard, and
// zip's registry used to publish it verbatim while cloud's router reading named
// it {wildcard1}, so Fold found no live route and refused the whole document.
// zip's document builder asks Template for every path now, and Template names a
// wildcard {wildcardN}, so the two agree and that mechanism is gone. The two
// entries read exactly like their four siblings, which is what they always were.
func TestTheHTMLPagesCannotBeTypedOps(t *testing.T) {
	app := probeApp(t)
	type page struct {
		HTML string `json:"html"`
	}
	zip.Get(app.Group("/v1/probe"), "/:org/:repo/tree/*", func(context.Context, *cloud.Unit) (*page, error) {
		return &page{HTML: "<!doctype html><title>tree</title>"}, nil
	})

	res, err := app.Fiber().Test(httptest.NewRequest(http.MethodGet, "/v1/probe/acme/repo/tree/src/main.go", nil))
	if err != nil {
		t.Fatalf("drive the typed op: %v", err)
	}
	defer res.Body.Close()
	// Status first: a refusal answers application/problem+json, which is also not
	// text/html, so a request that never reached the op must report as that rather
	// than as a discovery about what an op can answer.
	if res.StatusCode != http.StatusOK {
		t.Fatalf("the request did not reach the typed op: status %d", res.StatusCode)
	}
	if got := res.Header.Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
		t.Fatalf("a typed op answered %q — it can carry a content type of its own now, so the "+
			"six server-rendered entries in untypedByDesign want re-reading.", got)
	}
}

// TestTheWildcardNoLongerRefusesTheDocument records what CHANGED, so the
// mechanism the previous test ran cannot quietly come back. A typed op on a
// greedy wildcard produces a document, and it is published at the name the router
// reading gives that segment; if this ever refuses again, every wildcard-addressed
// op in the fleet is unpublishable and this app publishes nothing at all.
func TestTheWildcardNoLongerRefusesTheDocument(t *testing.T) {
	app := probeApp(t)
	zip.Get(app.Group("/v1/probe"), "/:org/:repo/tree/*", func(context.Context, *cloud.Unit) (*cloud.Unit, error) {
		return nil, nil
	})
	doc, err := openapi.Spec(app, openapi.Info{Title: "probe", Version: "v1"})
	if err != nil {
		t.Fatalf("a typed op on a greedy wildcard refused the document again: %v", err)
	}
	if _, ok := doc.Paths["/v1/probe/{org}/{repo}/tree/{wildcard1}"]; !ok {
		t.Fatalf("the document does not carry the wildcard at its published name; it carries %v",
			slices.Sorted(maps.Keys(doc.Paths)))
	}
}
