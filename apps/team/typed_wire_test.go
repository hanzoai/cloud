package team

import (
	"fmt"
	"github.com/hanzoai/cloud/internal/planetest"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/openapi"
)

// The routes this file pins were raw fiber handlers and are now typed ops. A
// typed op is a DESCRIPTION of a route, never a change to it, so what is tested
// here is the exact bytes: same status, same body, same headers, for every arm
// the handler has. Each assertion is the answer the untyped handler gave,
// measured before the conversion.

// TestCollabRPCShapesAreExact pins all three reply shapes of the collaborator
// RPC. They are three DIFFERENT bodies on one 200, which is why collabResult
// carries a POINTER to its content map: `{"content":{}}` (a real empty answer)
// and `{}` (updateContent, which answers nothing) must stay distinguishable, and
// a plain map with omitempty would render both as `{}`.
func TestCollabRPCShapesAreExact(t *testing.T) {
	app := mountTeam(t)
	const org, acct = "acme", "550e8400-e29b-41d4-a716-446655440000"
	ws, err := mounted.State.accounts.EnsureSpace(t.Context(), org, acct, "Ada")
	if err != nil {
		t.Fatal(err)
	}
	auth := bearerFor(t, acct, org)
	docID := collabDocID(ws.UUID, "tracker:class:Issue", "issue-shapes", "description")

	// getContent with NO source is a first-class case (source is optional in the
	// client contract) and it answers an empty content OBJECT, not an absent one.
	code, body := call(t, app, http.MethodPost, "/v1/team/collaborator/rpc/"+docID, auth,
		map[string]any{"method": "getContent", "payload": map[string]any{}})
	if code != http.StatusOK || string(body) != `{"content":{}}` {
		t.Fatalf("getContent without source = %d %s, want 200 {\"content\":{}}", code, body)
	}

	// createContent with an empty content map answers the same empty object.
	code, body = call(t, app, http.MethodPost, "/v1/team/collaborator/rpc/"+docID, auth,
		map[string]any{"method": "createContent", "payload": map[string]any{"content": map[string]string{}}})
	if code != http.StatusOK || string(body) != `{"content":{}}` {
		t.Fatalf("createContent with no fields = %d %s, want 200 {\"content\":{}}", code, body)
	}

	// updateContent answers the bare empty object — no content key at all.
	code, body = call(t, app, http.MethodPost, "/v1/team/collaborator/rpc/"+docID, auth,
		map[string]any{"method": "updateContent", "payload": map[string]any{"content": map[string]string{}}})
	if code != http.StatusOK || string(body) != `{}` {
		t.Fatalf("updateContent = %d %s, want 200 {}", code, body)
	}

	// An unknown verb is a SEMANTIC refusal, which this RPC reports under 200
	// because the client throws on result.error.
	code, body = call(t, app, http.MethodPost, "/v1/team/collaborator/rpc/"+docID, auth,
		map[string]any{"method": "nope", "payload": map[string]any{}})
	if code != http.StatusOK || string(body) != `{"error":"unknown method nope"}` {
		t.Fatalf("unknown verb = %d %s, want 200 {\"error\":\"unknown method nope\"}", code, body)
	}

	// A malformed documentId is still a 400, and a request with no token at all
	// is still a 401 — the gates the op kept.
	if code, _ := call(t, app, http.MethodPost, "/v1/team/collaborator/rpc/not-a-doc-id", auth,
		map[string]any{"method": "getContent", "payload": map[string]any{}}); code != http.StatusBadRequest {
		t.Fatalf("malformed documentId = %d, want 400", code)
	}
	if code, _ := call(t, app, http.MethodPost, "/v1/team/collaborator/rpc/"+docID, nil,
		map[string]any{"method": "getContent", "payload": map[string]any{}}); code != http.StatusUnauthorized {
		t.Fatalf("no token = %d, want 401", code)
	}
}

// TestCollabRPCBridgedUnderABareApp is the regression bar for the defect that
// typing this route surfaced, and it is deliberately mounted with NO COMPOSER:
// zip.New, Mount, and nothing else.
//
// A typed op receives only a context, so it reaches its caller's token ONLY
// through the request cloud.Bridge parks. Serve installs that bridge binary-wide
// in production, so production was never broken — but no package's harness runs
// Serve, and team installed none of its own, so every team test measured a
// posture Mount alone did not have and an embedder mounting team without Serve
// got 403 on all nine typed ops. This asks the question the name always claimed
// to: does MOUNT carry it. Composing the bridge here (as this test used to) makes
// it pass whatever Mount does.
//
// The collaborator plane is the sharpest case because it is a BRANCH of
// /v1/team, registered from a different file's group — so a 200 also proves the
// install reaches every group team builds and not merely the one it sits beside.
func TestCollabRPCBridgedUnderABareApp(t *testing.T) {
	planetest.ServeIdentity(t)
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	if err := useWith(app, cloud.Deps{DataDir: t.TempDir(), KMS: teamKMS(t, testSecret)}, newMemVFS()); err != nil {
		t.Fatalf("Use:  %v", err)
	}
	t.Cleanup(func() { _ = Shutdown() })

	const org, acct = "acme", "550e8400-e29b-41d4-a716-446655440000"
	ws, err := mounted.State.accounts.EnsureSpace(t.Context(), org, acct, "Ada")
	if err != nil {
		t.Fatal(err)
	}
	docID := collabDocID(ws.UUID, "tracker:class:Issue", "issue-bridge", "description")
	code, body := call(t, app, http.MethodPost, "/v1/team/collaborator/rpc/"+docID, bearerFor(t, acct, org),
		map[string]any{"method": "getContent", "payload": map[string]any{}})
	if code != http.StatusOK {
		t.Fatalf("collab RPC under a BARE mount = %d (%s), want 200 — team.Use does not install its own "+
			"cloud.Bridge, so a typed op resolves no caller without a composer", code, body)
	}
}

// TestClearCookieIsExact pins the account-cookie DELETE: 200, {"result":true},
// and a Set-Cookie that EXPIRES the account token (max-age 0) — the sign-out the
// SPA depends on. It takes no body, which is what makes it a typed DELETE.
func TestClearCookieIsExact(t *testing.T) {
	app := mountTeam(t)
	req := httptest.NewRequest(http.MethodDelete, "/v1/team/account/cookie", nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("DELETE /cookie = %d, want 200", resp.StatusCode)
	}
	buf := make([]byte, 64)
	n, _ := resp.Body.Read(buf)
	if got := string(buf[:n]); got != `{"result":true}` {
		t.Fatalf("DELETE /cookie body = %s, want {\"result\":true}", got)
	}
	sc := resp.Header.Get("Set-Cookie")
	if !strings.HasPrefix(sc, authCookie+"=;") || !strings.Contains(sc, "max-age=0") {
		t.Fatalf("DELETE /cookie Set-Cookie = %q, want an expiring %s", sc, authCookie)
	}
	if !strings.Contains(sc, "HttpOnly") || !strings.Contains(sc, "secure") {
		t.Fatalf("DELETE /cookie Set-Cookie = %q, want HttpOnly+Secure", sc)
	}
}

// TestClearCookieDegraded proves the typed DELETE carries the SAME fail-closed
// refusal Mount's guard gave it: a subsystem with no signing secret answers 503,
// because a typed op is not a zip.Handler and cannot be wrapped by that guard.
func TestClearCookieDegraded(t *testing.T) {
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	compose(app)
	if err := useWith(app, cloud.Deps{DataDir: t.TempDir(), KMS: teamKMS(t, "")}, newMemVFS()); err != nil {
		t.Fatalf("Use:  %v", err)
	}
	t.Cleanup(func() { _ = Shutdown() })
	if code, body := call(t, app, http.MethodDelete, "/v1/team/account/cookie", nil, nil); code != http.StatusServiceUnavailable {
		t.Fatalf("degraded DELETE /cookie = %d (%s), want 503", code, body)
	}
}

// untypedByDesign is the CLOSED list of team operations that are NOT typed ops,
// each naming the WIRE MECHANISM that forbids one. A typed op is a route PLUS a
// registry entry — the one value the OpenAPI operation, the MCP tool, the CLI
// command and the SDK method all come from — so an operation missing from that
// registry is invisible to all four. These ten are missing on purpose. Addresses
// are written the way the DOCUMENT writes them, which is the identity every
// projection keys on.
//
// EVERY REASON HERE CITES THE CODE THAT MAKES IT TRUE, because "cannot be typed"
// is a claim about a DEPENDENCY and a dependency moves. Two of these were
// re-derived after their stated reason EXPIRED: zip.WithStatus is variadic and
// accepts any code in 100..599, so "a typed op answers a 2xx" — which is what both
// OAuth legs used to say — has been false for some time. Neither route converted;
// each was blocked all along on something the old reason never named. A dead
// clause is worse than a missing one, because a reader who checks it, finds it
// false and converts the route is wrong for a reason nothing warned them about.
//
// A CITATION HAS TWO SPELLINGS AND THE DIFFERENCE IS LOAD-BEARING.
//
//   - This package's own code is cited as `verbatim snippet` (file.go:NNN), and
//     TestLedgerCitationsResolve OPENS that line and requires the snippet to be on
//     it. That is not decoration: writing a reason MOVES the file it is about, so
//     every app citation in this ledger was off by the exact number of comment
//     lines its own author had just added — pointing, in the account.go cases, at
//     unrelated live code that reads perfectly well and says nothing.
//   - A pinned DEPENDENCY is cited as `zip typed.go:NNN` / `fiber redirect.go:NNN`,
//     qualified because apps/team HAS ITS OWN typed.go: a bare "typed.go:567" names
//     two different files and the reader cannot tell which. Those are not opened —
//     they are a module cache away and the version, not this tree, decides them.
var untypedByDesign = map[string]string{
	"GET /v1/team/collaborator": "BYTE-STREAM/UPGRADE REPLY: the handler ends in " +
		"`wsx.Upgrade(` (collabws.go:703) and hands the socket to a frame loop that outlives it, " +
		"while a typed op's only response path is c.JSON(out) (zip typed.go:567).",
	"GET /v1/team/transactor/{token}": "BYTE-STREAM/UPGRADE REPLY: the handler ends in " +
		"`wsx.Upgrade(` (transactor.go:147) and hands the socket to a frame loop that outlives it, " +
		"while a typed op's only response path is c.JSON(out) (zip typed.go:567).",

	"POST /v1/team/account": "RAW-BODY TOLERANCE, and two more: (1) ORDER — an unparseable body " +
		"answers HTTP 200 carrying a Status, `g.fail(c, statusError(\"bad request\"))` " +
		"(account.go:717), where op.invoke decodes into In BEFORE the handler is entered and " +
		"answers ErrBadRequest (zip typed.go:485-491), so error precedence would move; (2) SHAPE — " +
		"`result` is a different type per verb (LoginInfo, a space list, a bool, RegionInfo), " +
		"so one Out could only say `any`; (3) A SECOND KEY — the entitlement arm answers 402 with " +
		"`\"upgradeUrl\": upgradeURL` (account.go:830) beside its error.",
	"PUT /v1/team/account/cookie": "RAW-BODY TOLERANCE: `_ = c.Bind(&body)` (account.go:662) " +
		"DISCARDS the decode error and the token falls back to the Authorization bearer, so an " +
		"unparseable body SUCCEEDS — where op.invoke answers ErrBadRequest before the handler runs " +
		"(zip typed.go:485-491).",
	"GET /v1/team/account/auth/{provider}": "HEADERS WITHOUT A BODY. The status is NOT the blocker — " +
		"WithStatus takes 100..599 (zip typed.go:154-161), so a declared 302 is legal. A typed op " +
		"cannot emit a header without also emitting a body: a nil Out returns at zip " +
		"typed.go:543-551, BEFORE responseHeadersOf runs at 553, so no Location and no Set-Cookie; " +
		"a non-nil Out reaches c.JSON(out) at zip typed.go:567, putting a JSON body and a " +
		"Content-Type on a redirect that carries neither (fiber redirect.go:328-335 writes exactly " +
		"Location and the status). Its no-nonce arm also answers text/plain, " +
		"`c.String(http.StatusInternalServerError, \"state\")` (account.go:412).",
	"GET /v1/team/account/auth/{provider}/callback": "TWO Set-Cookie HEADERS, which is a harder and " +
		"WHOLLY DIFFERENT blocker from its sibling's: the success path clears the flow cookie, " +
		"`g.setSessionCookie(c, stateCookie, \"\", -1)` (account.go:469), and sets the IAM token " +
		"cookie, `g.setIAMTokenCookie(c, access)` (account.go:495). zip's HeaderCoder is " +
		"map[string]string (zip typed.go:258) written with a REPLACING c.Set (zip " +
		"typed.go:557-558), so one key carries one value, and RFC 6265 §3 forbids folding two " +
		"cookies into one header. This needs a header MULTIMAP, not a body capability. Its bounce " +
		"also has a text/plain 500 arm, " +
		"`c.String(http.StatusInternalServerError, \"bad front url\")` (account.go:1350).",

	"GET /v1/team/billing/ui": "BYTE REPLY: the embedded wallet page's bytes under a per-asset " +
		"Content-Type, `c.Bytes(http.StatusOK, body)` (billing.go:204), while a typed op's only " +
		"response path is c.JSON(out) (zip typed.go:567).",
	"GET /v1/team/billing/ui/{wildcard1}": "GREEDY FIBER WILDCARD, plus the byte reply above. zip's " +
		"Template rewrites only `:name` segments and publishes `*` VERBATIM while cloud's router " +
		"reading renders the same segment `{wildcard1}`, so Fold looks the op up at a key the " +
		"router does not carry and refuses the WHOLE document — team would publish nothing at all " +
		"rather than one bad path. The open path space is structural to the shell: ui reads " +
		"`c.Param(\"*\")` (billing.go:185) and falls back to index.html, which is what makes a deep " +
		"link survive a hard refresh.",

	"POST /v1/team/files/{space}": "MULTIPART BODY, and a text/plain reply. The form is read " +
		"straight off fiber, `c.Fiber().FormFile(\"file\")` (files.go:164) — its part filename IS " +
		"the blob id — against an op.invoke that json-decodes every non-empty body before the " +
		"handler is entered (zip typed.go:485-491); and the success body is a bare uuid, " +
		"`c.String(http.StatusOK, blobID)` (files.go:198), not JSON.",
	"GET /v1/team/files/{space}/{filename}": "BYTE REPLY: the blob's raw bytes under a " +
		"Content-Type derived from those bytes, `c.Bytes(http.StatusOK, data)` (files.go:244), " +
		"while a typed op's only response path is c.JSON(out) (zip typed.go:567).",
}

// TestDeclaredMediaTypesAreTheServedOnes reads each byte reply's DECLARED media
// type out of the document and compares it to the one the live route sets.
//
// A route that cannot be typed still owes its bodies, and openapi.Bytes is how it
// states one — but a media type is a LITERAL a human wrote next to a handler that
// derives its own, so the two can disagree the moment either moves, and a document
// that says text/html over JavaScript is a document that lies to whoever generates
// against it. Declaring the type and checking it are one change or neither.
//
// Only the routes that NAME a type are checked. The wildcard asset read and the
// blob download declare opaque bytes deliberately — their type is derived per
// response, so "varies" is the true statement and there is no single value to
// hold them to.
func TestDeclaredMediaTypesAreTheServedOnes(t *testing.T) {
	app := mountTeam(t)
	doc, err := openapi.Spec(app, openapi.Info{Title: "team", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	// declared answers the ONE media type an operation's success response names.
	declared := func(t *testing.T, method, path string) string {
		t.Helper()
		op := doc.Paths[path][strings.ToLower(method)]
		if op == nil {
			t.Fatalf("%s %s is not in the document at all", method, path)
		}
		responses, ok := op.Responses.(map[string]*openapi.Response)
		if !ok {
			t.Fatalf("%s %s declares no response — openapi.Register is what states one", method, path)
		}
		var types []string
		for _, resp := range responses {
			for media := range resp.Content {
				types = append(types, media)
			}
		}
		if len(types) != 1 {
			t.Fatalf("%s %s declares %d media types (%v), want exactly one", method, path, len(types), types)
		}
		return types[0]
	}

	const org, acct = "acme", "550e8400-e29b-41d4-a716-446655440000"
	auth := bearerFor(t, acct, org)
	ws, err := mounted.State.accounts.EnsureSpace(t.Context(), org, acct, "Ada")
	if err != nil {
		t.Fatal(err)
	}

	// The wallet page: an empty capture resolves to index.html unconditionally, so
	// one type is the whole truth about this route.
	page := getRaw(t, app, "/v1/team/billing/ui", auth)
	defer func() { _ = page.Body.Close() }()
	if page.StatusCode != http.StatusOK {
		t.Fatalf("GET /v1/team/billing/ui = %d, want 200", page.StatusCode)
	}
	if got, want := page.Header.Get("Content-Type"), declared(t, http.MethodGet, "/v1/team/billing/ui"); got != want {
		t.Errorf("the wallet page serves %q and the document declares %q", got, want)
	}

	// The upload receipt: a bare blob id as text, which is the second reason this
	// route cannot be a typed op and is therefore worth holding.
	receipt := uploadRaw(t, app, ws.UUID, "9f1c2d3e-4a5b-6c7d-8e9f-0a1b2c3d4e5f", auth, []byte("bytes"))
	defer func() { _ = receipt.Body.Close() }()
	if receipt.StatusCode != http.StatusOK {
		t.Fatalf("upload = %d, want 200", receipt.StatusCode)
	}
	if got, want := receipt.Header.Get("Content-Type"), declared(t, http.MethodPost, "/v1/team/files/{space}"); got != want {
		t.Errorf("the upload receipt serves %q and the document declares %q", got, want)
	}
}

// citation is `verbatim snippet` (file.go:NNN) — this package's own code, opened
// and checked. dependency is a qualified module citation, which is not.
var (
	citation = regexp.MustCompile("`([^`]+)` \\((\\w+\\.go):(\\d+)\\)")
	// The qualifier is OPTIONAL in the pattern so that its ABSENCE is a match
	// rather than a miss. Requiring `\w+\s+` instead made this arm unreachable:
	// the ambiguous form it exists to catch is parenthesised — "(typed.go:567)" —
	// so the mandatory prefix never matched and the check silently measured
	// nothing while passing. Mutation-checked by unqualifying one citation.
	dependency = regexp.MustCompile(`(?:(\w+)\s+)?(\w+\.go):(\d+)`)
)

// TestLedgerCitationsResolve OPENS every line this ledger cites and requires the
// quoted code to be on it.
//
// A file:line nobody can land on is how a refusal stops being re-checkable, and
// the failure mode here is not carelessness — it is MECHANICAL. Writing the reason
// adds comment lines to the very file the reason cites, so a citation measured
// before the edit is wrong after it, by exactly the number of lines just written.
// Every app citation in this ledger was wrong that way, and the account.go ones
// landed on unrelated live code that reads perfectly and answers nothing.
//
// It also refuses an UNQUALIFIED foreign citation, which is the ambiguity half:
// apps/team has its own typed.go, so a bare "typed.go:567" names two files.
func TestLedgerCitationsResolve(t *testing.T) {
	lines := map[string][]string{}
	read := func(t *testing.T, file string) []string {
		t.Helper()
		if got, ok := lines[file]; ok {
			return got
		}
		body, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("a citation names %s, which this package does not have: %v", file, err)
		}
		lines[file] = strings.Split(string(body), "\n")
		return lines[file]
	}
	checked := 0
	for route, reason := range untypedByDesign {
		for _, m := range citation.FindAllStringSubmatch(reason, -1) {
			snippet, file, num := m[1], m[2], m[3]
			n, err := strconv.Atoi(num)
			if err != nil || n < 1 {
				t.Errorf("%s cites %s:%s, which is not a line", route, file, num)
				continue
			}
			src := read(t, file)
			if n > len(src) {
				t.Errorf("%s cites %s:%d, past the end of a %d-line file", route, file, n, len(src))
				continue
			}
			if !strings.Contains(src[n-1], snippet) {
				t.Errorf("%s cites %s:%d for %q, and that line reads %q — re-measure it:"+
					" writing a reason MOVES the file the reason is about",
					route, file, n, snippet, strings.TrimSpace(src[n-1]))
			}
			checked++
		}
		// Whatever is left after the checked citations are removed may only cite a
		// pinned module, and must SAY WHICH — the qualifier is what tells a reader
		// (and this test) that the file is not one of ours.
		for _, m := range dependency.FindAllStringSubmatch(citation.ReplaceAllString(reason, ""), -1) {
			if qualifier := m[1]; qualifier != "zip" && qualifier != "fiber" {
				t.Errorf("%s cites %s:%s qualified by %q — a foreign file must be named for its"+
					" module (this package has its own typed.go), and one of ours must be quoted",
					route, m[2], m[3], qualifier)
			}
		}
	}
	if checked < len(untypedByDesign) {
		t.Errorf("only %d checked citations across %d refusals — a reason that cites nothing"+
			" cannot be re-derived, which is the whole job of this ledger", checked, len(untypedByDesign))
	}
}

// teamOps reads BOTH projections of the live router at their one shared address
// form: what the document says is served, and which of those carry a typed
// registry entry. It filters on the prefix the capability answers under, which
// since the collaborator fold is the single /v1/team — the live lane and the
// snapshot RPC are branches of it, not a second address.
func teamOps(t *testing.T) (served map[string]bool, typed map[string]string, schemas map[string]any) {
	t.Helper()
	app := mountTeam(t)
	doc, err := openapi.Spec(app, openapi.Info{Title: "team", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	reg, err := openapi.Typed(app)
	if err != nil {
		t.Fatalf("typed registry: %v", err)
	}
	ours := func(p string) bool { return strings.HasPrefix(p, teamPrefix) }
	served, typed = map[string]bool{}, map[string]string{}
	for path, item := range doc.Paths {
		if !ours(path) {
			continue
		}
		for method := range item {
			served[strings.ToUpper(method)+" "+path] = true
		}
	}
	for key, op := range reg.Ops {
		if i := strings.Index(key, " "); i > 0 && ours(key[i+1:]) {
			typed[key] = op.Description
		}
	}
	// The app under test mounts team and nothing else, so every schema in the
	// registry is one a team op publishes.
	return served, typed, reg.Schemas
}

// TestEveryRouteIsTypedOrNamed holds TWO LEDGERS THAT SUM TO THE SERVED SURFACE,
// read off the live router. It fails three ways, and the third is what makes the
// word "sum" true rather than approximate:
//
//   - a served operation that is neither typed nor named — so the next route
//     added here is typed by default;
//   - a named operation team no longer serves — so a reason cannot rot into prose
//     about a route that is gone;
//   - a named operation that IS a typed op — so a refusal that stopped being true
//     cannot sit in the list being satisfied by the very conversion that retired
//     it. Without this arm the two sets could overlap and the counts would still
//     "add up" while describing a surface smaller than the one served.
func TestEveryRouteIsTypedOrNamed(t *testing.T) {
	served, typed, _ := teamOps(t)

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
			"method. Convert it (zip.Get/Post/... on the group), or add it to untypedByDesign with the reason "+
			"typing it would move the wire.", strings.Join(untyped, ", "))
	}
	// The reasons must describe operations that exist, or the list is stale prose.
	for key := range untypedByDesign {
		if !served[key] {
			t.Errorf("untypedByDesign names %q, which team no longer serves", key)
		}
		if _, ok := typed[key]; ok {
			t.Errorf("untypedByDesign names %q, which IS a typed op — the refusal expired; delete it", key)
		}
	}
	// Stated as the arithmetic the two arms above imply, so a future edit that
	// weakens either one is caught by the count it can no longer satisfy.
	if got := len(typed) + len(untypedByDesign); got != len(served) {
		t.Errorf("typed %d + named %d = %d, but team serves %d — the two ledgers must partition the "+
			"served surface exactly", len(typed), len(untypedByDesign), got, len(served))
	}
}

// Every typed op must carry lifted prose, because that prose IS the product
// surface: it becomes the OpenAPI description AND the MCP tool description a
// model reads to pick the tool. zipdoc_gen.go is what carries it into the
// binary, so an op added without regenerating shows up here as a nameless tool.
func TestEveryTypedOpIsDescribed(t *testing.T) {
	_, typed, _ := teamOps(t)
	if len(typed) == 0 {
		t.Fatal("no typed team ops in the registry at all")
	}
	for key, desc := range typed {
		if strings.TrimSpace(desc) == "" {
			t.Errorf("%s has no description — run: go generate -run zipdoc ./apps/team/...", key)
		}
	}
}

// TestEveryPublishedFieldIsDescribed closes the half of the surface the op-level
// gate above cannot see. Typing a route documents its ADDRESS and its SHAPE; it
// does NOT document the shape's FIELDS, which come from a different place — doc
// comments on the In/Out struct fields, which zipdoc lifts per field. So a package
// can be at 100% of its typable routes and still publish bare properties: team
// shipped three (ProviderInfo.name, ProviderInfo.displayName, botMember.active),
// each reaching openapi.yaml, all four generated SDKs and the MCP inputSchema with
// no description, because the two facts are counted in different places and only
// the op-level one was counted.
//
// Every property of every schema a team op publishes must say what it is. Add a
// field to a published type without prose and this fails.
//
// The walk RECURSES — nested objects, array items, additionalProperties and every
// alternative of allOf/anyOf/oneOf — because a top-level-only walk reads clean
// while an inline object inside a property ships bare. Team has none at any depth
// today; the recursion is what keeps that true when the first one is written,
// rather than the day someone thinks to look.
func TestEveryPublishedFieldIsDescribed(t *testing.T) {
	_, _, schemas := teamOps(t)
	if len(schemas) == 0 {
		t.Fatal("no team schemas in the typed registry at all")
	}
	var bare []string
	for name, raw := range schemas {
		bare = append(bare, bareUnder(name, raw)...)
	}
	if len(bare) > 0 {
		sort.Strings(bare)
		t.Errorf("published propert(ies) with no description: %s\n"+
			"Every field of a published schema is read by SDK users and by a model choosing a tool. Write a "+
			"doc comment on the struct field and run: go generate -run zipdoc ./apps/team/...",
			strings.Join(bare, ", "))
	}
}

// TestBareUnderSeesEveryNestingForm is the gate ON the gate. team publishes no
// bare property at any depth today, so the recursion above is satisfied by a
// walker that does not recurse at all — an assertion that passes because nothing
// was reached is not an assertion. This drives one bare field through each shape
// a schema can nest through, so the day team grows an inline object the gate is
// already known to see into it.
//
// Each case is a SHAPE PARAMETERISED BY ITS LEAF, so the positive and the
// negative half are the same literal with one value swapped. Driving both off one
// shape is what makes the negative half evidence: a walker that reported POSITION
// rather than absence would pass the first assertion and fail the second.
//
// Every CONTAINER carries prose, because a property that happens to be an object
// is still a published field and bareUnder is right to name it. Leaving one bare
// made each case report two paths, so the test measured the container instead of
// the nesting — which is how the first draft of it failed.
func TestBareUnderSeesEveryNestingForm(t *testing.T) {
	obj := func(props map[string]any) map[string]any {
		return map[string]any{"type": "object", "description": "said.", "properties": props}
	}
	for _, tc := range []struct {
		form  string
		shape func(leaf map[string]any) map[string]any
		want  string
	}{
		{"top level", func(leaf map[string]any) map[string]any {
			return obj(map[string]any{"a": leaf})
		}, "S.a"},
		{"nested object", func(leaf map[string]any) map[string]any {
			return obj(map[string]any{"a": obj(map[string]any{"b": leaf})})
		}, "S.a.b"},
		{"array items", func(leaf map[string]any) map[string]any {
			return obj(map[string]any{"a": map[string]any{
				"type": "array", "description": "said.", "items": obj(map[string]any{"b": leaf}),
			}})
		}, "S.a[items].b"},
		{"open map value", func(leaf map[string]any) map[string]any {
			return obj(map[string]any{"a": map[string]any{
				"type": "object", "description": "said.", "additionalProperties": obj(map[string]any{"b": leaf}),
			}})
		}, "S.a[additionalProperties].b"},
		{"oneOf alternative", func(leaf map[string]any) map[string]any {
			return map[string]any{"oneOf": []any{obj(map[string]any{"b": leaf})}}
		}, "S.oneOf[0].b"},
	} {
		t.Run(tc.form, func(t *testing.T) {
			bare := map[string]any{"type": "string"}
			if got := bareUnder("S", tc.shape(bare)); len(got) != 1 || got[0] != tc.want {
				t.Fatalf("bareUnder over a %s = %v, want exactly [%s]", tc.form, got, tc.want)
			}
			said := map[string]any{"type": "string", "description": "said."}
			if got := bareUnder("S", tc.shape(said)); len(got) != 0 {
				t.Fatalf("bareUnder over a described %s = %v, want none", tc.form, got)
			}
		})
	}
}

// bareUnder names every property at or beneath one schema node that carries no
// description, keyed by its dotted path so the failure says WHICH field.
func bareUnder(at string, raw any) []string {
	node, ok := raw.(map[string]any)
	if !ok {
		return nil
	}
	var bare []string
	if props, ok := node["properties"].(map[string]any); ok {
		for field, praw := range props {
			p, ok := praw.(map[string]any)
			if !ok {
				continue
			}
			if desc, _ := p["description"].(string); strings.TrimSpace(desc) == "" {
				bare = append(bare, at+"."+field)
			}
			bare = append(bare, bareUnder(at+"."+field, p)...)
		}
	}
	// A list's element and an open map's value are shapes too. additionalProperties
	// is often a bool (`true` = any JSON), which carries nothing to describe.
	for _, key := range []string{"items", "additionalProperties"} {
		bare = append(bare, bareUnder(at+"["+key+"]", node[key])...)
	}
	for _, key := range []string{"allOf", "anyOf", "oneOf"} {
		alts, ok := node[key].([]any)
		if !ok {
			continue
		}
		for i, alt := range alts {
			bare = append(bare, bareUnder(fmt.Sprintf("%s.%s[%d]", at, key, i), alt)...)
		}
	}
	return bare
}
