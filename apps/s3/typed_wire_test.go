package s3_test

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/account"
	"github.com/hanzoai/cloud/apps/s3"
	"github.com/hanzoai/cloud/fleet"
	"github.com/hanzoai/cloud/internal/planetest"
	"github.com/hanzoai/cloud/openapi"
	luxlog "github.com/luxfi/log"
	"github.com/valyala/fasthttp"
	"github.com/zap-proto/zip"
)

// untypedByDesign is the CLOSED list of object-storage operations that are NOT
// typed ops, each with the wire fact that keeps it raw. The address is written
// the way the DOCUMENT writes it, which is the identity every projection keys on.
//
// It is EMPTY, and that is the whole surface's state rather than a gap: all eight
// operations are typed. It stays because the sum below is what a ninth route added
// untyped would violate, and because a genuine refusal needs somewhere to be
// written down with its reason.
//
// FOUR refusals were recorded here over time and all four expired — each named a
// capability zip did not have and now has. A refusal is kept as an entry rather
// than as prose for exactly that reason: a sentence cannot notice that its reason
// stopped being true, and three of the four were found by re-reading rather than
// by anything going red. The fourth was, which is what the entries are for.
var untypedByDesign = map[string]string{}

// TestEveryRouteIsTypedOrNamed reads BOTH projections of the live router at their
// one shared address form — what the document says is served, and which of those
// carry a typed registry entry — and requires the two ledgers to SUM to the
// served surface. A route added untyped goes red without anyone remembering this
// file, and a name that stops being served goes red too.
func TestEveryRouteIsTypedOrNamed(t *testing.T) {
	app := newApp(t, true)
	doc, err := openapi.Spec(app, openapi.Info{Title: "s3", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	reg, err := openapi.Typed(app)
	if err != nil {
		t.Fatalf("typed: %v", err)
	}

	served, typed := map[string]bool{}, map[string]bool{}
	for path, item := range doc.Paths {
		if !strings.HasPrefix(path, "/v1/s3") {
			continue
		}
		for method := range item {
			served[strings.ToUpper(method)+" "+path] = true
		}
	}
	// reg.Ops is already keyed "METHOD /path" in the document's own spelling —
	// Fold refuses outright when the two readings disagree, which is what makes
	// this comparison sound rather than approximate.
	for key := range reg.Ops {
		if _, path, ok := strings.Cut(key, " "); ok && strings.HasPrefix(path, "/v1/s3") {
			typed[key] = true
		}
	}

	var untyped []string
	for key := range served {
		if typed[key] {
			continue
		}
		if _, named := untypedByDesign[key]; !named {
			untyped = append(untyped, key)
		}
	}
	if len(untyped) > 0 {
		sort.Strings(untyped)
		t.Errorf("served but neither typed nor named: %s\n"+
			"An untyped route publishes no schema, no MCP tool, no CLI command and no typed SDK "+
			"method. Convert it (zip.Get/Post/... on the group in Mount), or name it in "+
			"untypedByDesign with the WIRE FACT that keeps it raw — re-read against the pinned "+
			"zip, never inherited from an older pass.", strings.Join(untyped, ", "))
	}
	for key := range untypedByDesign {
		if !served[key] {
			t.Errorf("untypedByDesign names %q, which this surface no longer serves", key)
		}
		if typed[key] {
			t.Errorf("untypedByDesign names %q, which IS a typed op — delete the entry", key)
		}
	}
	if got, want := len(typed)+len(untypedByDesign), len(served); got != want {
		t.Errorf("the two ledgers must sum to the served surface: typed %d + named %d = %d, served %d",
			len(typed), len(untypedByDesign), got, want)
	}
}

// TestEveryTypedOpIsDescribed: prose is the product surface. A typed op with no
// description reaches the document, every generated SDK and the MCP tool list as
// a name and a shape with nothing saying what it does.
func TestEveryTypedOpIsDescribed(t *testing.T) {
	app := newApp(t, true)
	doc, err := openapi.Spec(app, openapi.Info{Title: "s3", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	for path, item := range doc.Paths {
		if !strings.HasPrefix(path, "/v1/s3") {
			continue
		}
		for method, op := range item {
			key := strings.ToUpper(method) + " " + path
			if _, named := untypedByDesign[key]; named {
				continue
			}
			if strings.TrimSpace(op.Description) == "" && strings.TrimSpace(op.Summary) == "" {
				t.Errorf("%s has no description — run: go generate -run zipdoc ./apps/s3/...", key)
			}
		}
	}
}

// TestEveryPublishedFieldIsDescribed gates the half neither gate above can see.
// Typing a route documents its ADDRESS and its SHAPE; it says nothing about what
// the shape's FIELDS mean, and those come from a different comment — one per
// field, which zipdoc lifts one at a time.
//
// It matters on this surface because two of the fields are easy to read wrongly:
// an ETag looks like a checksum and is not one, and a presigned URL's key is what
// the STORE will use rather than the string the caller sent. A property reaches
// openapi.yaml, every generated SDK and every MCP inputSchema, and its description
// is the only place a fact like that can travel with it.
//
// Presence is all a gate can check. A description that restates the field's name
// is worse than none, and only a reader catches that.
func TestEveryPublishedFieldIsDescribed(t *testing.T) {
	doc, err := openapi.Spec(alone(t), openapi.Info{Title: "s3", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	if doc.Components == nil || len(doc.Components.Schemas) == 0 {
		t.Fatal("s3 publishes no schemas at all — the gate would pass vacuously")
	}
	bare, err := openapi.Bare(doc)
	if err != nil {
		t.Fatalf("bare: %v", err)
	}
	if len(bare) > 0 {
		t.Errorf("%d published propert(ies) carry no description: %s\n"+
			"Write the FIELD's own doc comment — a header above a group of fields is lifted "+
			"onto the first of them alone — then run: make -C apps/s3 describe",
			len(bare), strings.Join(bare, ", "))
	}
}

// deep is a key with separators in it, which is the ONLY reason the two object
// addresses end in a greedy capture rather than a `:key` segment. A router
// parameter matches one segment; this is three.
const deep = "2019/summer/a.jpg"

// live mounts the real surface with credentials and presigning, against a store
// that records what it was asked. Presigning is arithmetic on a URL and reaches
// no network, so the download answers for real; the delete has to travel, and
// what arrives at the store is the object it addressed.
func live(t *testing.T) (*zip.App, *string) {
	t.Helper()
	var asked string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = r.URL.Path
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)
	t.Setenv(account.KeyEnv, "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	t.Setenv("CLOUD_S3_FEE_CENTS", "0")
	t.Setenv("S3_ADMIN_ACCESS_KEY", "AKIATEST")
	t.Setenv("S3_ADMIN_SECRET_KEY", "secrettest")
	t.Setenv("S3_ADMIN_ENDPOINT", srv.Listener.Addr().String())
	// ABSENT, not empty: set-and-empty is how an operator says "no public host",
	// which turns presigning off — and the download would then answer 503 for a
	// reason that has nothing to do with the key.
	t.Setenv("S3_PUBLIC_ENDPOINT", "")
	if err := os.Unsetenv("S3_PUBLIC_ENDPOINT"); err != nil {
		t.Fatalf("unset S3_PUBLIC_ENDPOINT: %v", err)
	}
	app := zip.New(zip.Config{Logger: luxlog.New("s3-live"), DisableStartupMessage: true})
	app.Use(cloud.Bridge())
	if err := s3.Use(app, cloud.Deps{}); err != nil {
		t.Fatalf("Use:  %v", err)
	}
	return app, &asked
}

// ask drives one request as a validated principal of org "acme". body is sent
// only when non-empty, so a bodyless method is driven exactly as a client sends it.
func ask(t *testing.T, app *zip.App, method, path, body string) (int, string) {
	t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, rdr)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer t")
	req.Header.Set("X-Org-Id", "acme")
	req.Header.Set("X-User-Id", "u-acme")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// answered reports whether an MCP frame carries the operation's answer rather
// than a refusal. tools/call always answers 200; the error lives in the envelope.
func answered(body string) bool {
	var f struct {
		Result struct {
			IsError bool `json:"isError"`
		} `json:"result"`
		Error json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal([]byte(body), &f); err != nil {
		return false
	}
	return len(f.Error) == 0 && !f.Result.IsError
}

// signedKey drives the download and answers the key the URL was signed for.
func signedKey(t *testing.T, app *zip.App, at string) string {
	t.Helper()
	st, body := ask(t, app, http.MethodGet, at, "")
	if st != http.StatusOK {
		t.Fatalf("GET %s = %d %s, want 200", at, st, body)
	}
	var signed struct{ URL, Key string }
	if err := json.Unmarshal([]byte(body), &signed); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	if !strings.Contains(signed.URL, signed.Key) {
		t.Errorf("the signed URL does not address the key it reports (%q): %s", signed.Key, signed.URL)
	}
	return signed.Key
}

// TestTheCaptureIsTheWholeKey. The trailing capture is the object key, and a key
// with separators in it reaches the store intact — which is the whole reason
// these two addresses end in `*` rather than a `:key` segment. Nothing else here
// drives a key with a separator in it, so without this the surface could stop
// carrying one and every other check would stay green.
//
// Both tags on objectRef.Key are exercised, because each can be lost without the
// other noticing. `url:"*1"` is what the ROUTE binds through; `json:"key"` is what
// a caller addressing the operation BY NAME supplies, and MCP hands its arguments
// across as one JSON object with no path to read.
//
// It also pins the leading-separator trim: fiber hands back "/2019/…" for a URL
// with a doubled separator, and the key is read past it.
func TestTheCaptureIsTheWholeKey(t *testing.T) {
	app, asked := live(t)

	for _, at := range []string{
		"/v1/s3/buckets/photos/objects/" + deep,
		"/v1/s3/buckets/photos/objects//" + deep,
	} {
		if got := signedKey(t, app, at); got != deep {
			t.Errorf("GET %s signed key %q, want %q — the capture is the WHOLE trailing path",
				at, got, deep)
		}
	}

	if st, body := ask(t, app, http.MethodDelete, "/v1/s3/buckets/photos/objects/"+deep, ""); st != http.StatusNoContent {
		t.Fatalf("DELETE a key with separators = %d %s, want 204", st, body)
	}
	if !strings.HasSuffix(*asked, "/"+deep) {
		t.Errorf("the store was asked to remove %q, which does not end in %q", *asked, deep)
	}

	// By name, whose whole input is a JSON object.
	*asked = ""
	frame := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{` +
		`"name":"delete_s3_buckets_by_bucket_objects_by_wildcard1",` +
		`"arguments":{"bucket":"photos","key":"` + deep + `"}}}`
	if _, body := ask(t, app, http.MethodPost, "/mcp", frame); !answered(body) {
		t.Fatalf("delete by name was refused: %s", body)
	}
	if !strings.HasSuffix(*asked, "/"+deep) {
		t.Errorf("by name the store was asked to remove %q, which does not end in %q — a caller "+
			"with no URL to carry the key named an object and a different one was addressed",
			*asked, deep)
	}
}

// TestTheAddressBeatsADecoy. zip binds body, then query, then path, so the URL is
// the addressing authority: whatever else a request carries, the operation acts on
// what the address named — which is also what admit checked and whose ledger is
// debited. A request that could redirect itself past that would be a caller
// spending one tenant's balance on another tenant's object.
//
// The decoy rides the QUERY rather than the body, because that is what these two
// methods can actually carry: zip reads no body for a GET or a DELETE, so a decoy
// there is refused by the transport rather than by the binding order, which would
// prove nothing about the order.
func TestTheAddressBeatsADecoy(t *testing.T) {
	app, asked := live(t)
	const decoy = "victim/secret.pdf"

	at := "/v1/s3/buckets/photos/objects/" + deep + "?bucket=victim&key=" + decoy + "&%2A1=" + decoy
	if got := signedKey(t, app, at); got != deep {
		t.Errorf("a query decoy redirected the download to %q, want %q — the URL is the "+
			"addressing authority and the path binds last", got, deep)
	}

	if st, body := ask(t, app, http.MethodDelete, at, ""); st != http.StatusNoContent {
		t.Fatalf("DELETE with a query decoy = %d %s, want 204", st, body)
	}
	if !strings.HasSuffix(*asked, "/"+deep) {
		t.Errorf("a query decoy redirected the delete: the store was asked to remove %q, want a "+
			"key ending %q", *asked, deep)
	}
	if strings.Contains(*asked, "victim") {
		t.Errorf("the delete reached a bucket or key the address never named: %q", *asked)
	}
}

// TestTheGraphCanAimTheDelete drives the fleet's GraphQL field end to end — the
// document, the field built from it, the transport the host dials, the operation,
// and the object store — and requires the key to ARRIVE.
//
// It exists because this is where typing a greedy address goes wrong, and where it
// went wrong before. A GraphQL field is built out of an operation's declared
// PARAMETERS (openapi.Fields), and the address a request is sent to is built by
// substituting them (fleet's address). A greedy segment that is templated in the
// path and declared under no name yields a field with no argument for the object,
// and the substitution then sends the template through as written: the delete
// answers 204 having removed an object literally named "{wildcard1}". Nothing in
// that path errors — a caller reads success — so it is invisible to every check
// that reads a status code.
//
// The whole chain is here on purpose. Asserting on the field's argument list alone
// would pass while the substitution or the transport dropped the key, and asserting
// on the operation alone says nothing about what the graph can aim at it. What the
// STORE was asked is the only fact that covers all of it.
func TestTheGraphCanAimTheDelete(t *testing.T) {
	app, asked := live(t)

	// The child speaks the transport the fleet dials, on its own socket, exactly as
	// the host starts one.
	sock := filepath.Join(planetest.Dir(t), "s3.sock")
	go func() { _ = app.Listen(sock) }()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if c, err := net.Dial("unix", sock); err == nil {
			_ = c.Close()
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	doc, err := openapi.Spec(app, openapi.Info{Title: "s3", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	// Whose app answers is stamped by the compose, which is the only reader that
	// knows; this app describing itself does not. Standing in for it is what lets
	// one app's document be driven without composing the fleet around it.
	for _, item := range doc.Paths {
		for _, op := range item {
			op.App = "s3"
		}
	}

	// The identity the host established, forwarded — this endpoint establishes none
	// of its own, so without it the child refuses before it reads an address.
	from := fasthttp.AcquireRequest()
	defer fasthttp.ReleaseRequest(from)
	from.Header.Set("Authorization", "Bearer t")
	from.Header.Set("X-Org-Id", "acme")
	from.Header.Set("X-User-Id", "u-acme")

	graph := fleet.NewGraph(doc, func(string) (string, string, error) { return sock, "/", nil })
	answer := graph.Run(fleet.Request{Query: `{ ` + graphDelete + `(bucket: "photos", wildcard1: "` + deep + `") }`}, from)
	if len(answer.Errors) > 0 {
		t.Fatalf("the graph could not run the delete: %+v", answer.Errors)
	}
	if _, ran := answer.Data[graphDelete]; !ran {
		t.Fatalf("the graph answered without the field: %+v", answer.Data)
	}
	if !strings.HasSuffix(*asked, "/"+deep) {
		t.Errorf("the graph asked the store to remove %q, which does not end in %q — the field "+
			"reported success about an object the caller never named", *asked, deep)
	}
	if strings.Contains(*asked, "wildcard") {
		t.Errorf("the graph sent its own path template through as the key: %q", *asked)
	}
}

// graphDelete is the field name the delete is published under, which is its
// operationId — one token across the document, the tool list and the graph.
const graphDelete = "delete_s3_buckets_by_bucket_objects_by_wildcard1"

// alone mounts s3 and nothing else, which is the document this app PUBLISHES:
// `make -C apps/s3 describe` runs the app's own binary over its own router. The
// harness the ledger gates read mounts a sibling beside it, which is right for
// asking who wins an address and wrong for asking whose schemas these are — the
// components of a two-app document belong to two apps.
func alone(t *testing.T) *zip.App {
	t.Helper()
	// admit asks account's anti-forgery control, and account refuses to mount a
	// verifier holding a key nobody else does — so Mount needs one to return.
	t.Setenv(account.KeyEnv, "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	app := zip.New(zip.Config{Logger: luxlog.New("s3-doc"), DisableStartupMessage: true})
	if err := s3.Use(app, cloud.Deps{}); err != nil {
		t.Fatalf("Use:  %v", err)
	}
	return app
}

// TestTheCollectionIsNotSwallowedByTheObjectRoute drives the bare collection and
// requires it to reach listObjects.
//
// It did not. A greedy `*` matches the EMPTY remainder and beats an exact sibling
// whichever order the two register in, so every spelling of
// GET /v1/s3/buckets/{b}/objects — bare, with ?prefix=, with a trailing separator
// — arrived at presignDownload as an object request carrying no key and was
// refused 400 before the store was asked. listObjects was registered, published,
// dispatchable by name, and unreachable over HTTP.
//
// The route captures with `+` now, which requires a character after /objects/.
// This asserts on WHAT THE STORE WAS ASKED rather than on the status, because the
// status alone cannot tell "listed nothing" from "never ran": the 400 came back
// without the store hearing anything at all.
func TestTheCollectionIsNotSwallowedByTheObjectRoute(t *testing.T) {
	app, asked := live(t)
	for _, path := range []string{
		"/v1/s3/buckets/photos/objects",
		"/v1/s3/buckets/photos/objects?prefix=2019/",
		"/v1/s3/buckets/photos/objects/",
	} {
		*asked = ""
		code, body := ask(t, app, "GET", path, "")
		if *asked == "" {
			t.Errorf("GET %s answered %d without asking the store — the collection is being "+
				"served by the object route again: %s", path, code, body)
			continue
		}
		if strings.Contains(*asked, "objects") {
			t.Errorf("GET %s asked the store %q — the address leaked into the key", path, *asked)
		}
	}
}

// TestTheObjectRouteStillTakesADeepKey is the other half, and the reason the fix
// is `+` rather than dropping the greedy capture: an object key IS a path, so the
// segment must still swallow slashes whole.
func TestTheObjectRouteStillTakesADeepKey(t *testing.T) {
	app, asked := live(t)
	*asked = ""
	if code, body := ask(t, app, "GET", "/v1/s3/buckets/photos/objects/"+deep, ""); code != 200 {
		t.Fatalf("the deep key answered %d: %s", code, body)
	}
	*asked = ""
	if code, _ := ask(t, app, "DELETE", "/v1/s3/buckets/photos/objects/"+deep, ""); code != 204 {
		t.Fatalf("the deep delete answered %d", code)
	}
	if !strings.HasSuffix(*asked, "/"+deep) {
		t.Fatalf("the store was asked %q, want a key ending %q", *asked, deep)
	}
}

// TestAnEncodedKeyAndAPlainOneAreTheSameObject is the property every generated
// client depends on and none of them had.
//
// The router hands a captured segment over exactly as it arrived — it decodes
// nothing, not %2F and not %20 — while every client cut from this API
// percent-encodes a path parameter: Go's url.PathEscape, Python's quote(safe=""),
// and JS's encodeURIComponent all render "2019/summer/a.jpg" as
// "2019%2Fsummer%2Fa.jpg". Undecoded, the two spellings addressed two DIFFERENT
// keys and BOTH answered success, so a delete sent by any SDK removed a key
// nobody had stored, reported 204, and left the object it named in place.
//
// It asserts on WHAT THE STORE WAS ASKED, because both spellings answer 204
// either way: the status cannot tell the two apart, which is precisely why this
// went unseen.
func TestAnEncodedKeyAndAPlainOneAreTheSameObject(t *testing.T) {
	app, asked := live(t)

	*asked = ""
	if code, body := ask(t, app, "DELETE", "/v1/s3/buckets/photos/objects/"+deep, ""); code != 204 {
		t.Fatalf("the plain spelling answered %d: %s", code, body)
	}
	plain := *asked

	*asked = ""
	encoded := strings.ReplaceAll(deep, "/", "%2F")
	if code, body := ask(t, app, "DELETE", "/v1/s3/buckets/photos/objects/"+encoded, ""); code != 204 {
		t.Fatalf("the encoded spelling answered %d: %s", code, body)
	}
	if *asked != plain {
		t.Fatalf("the two spellings addressed different objects:\n  plain   %q\n  encoded %q\n"+
			"every generated SDK sends the encoded form, so it was deleting a key nobody stored "+
			"and reporting success", plain, *asked)
	}
}

// TestAnEscapeIsDecodedOnceAndOnlyOnce pins the two cases a decode gets wrong in
// opposite directions: a literal percent must survive as one character, and a
// malformed escape must be refused rather than smuggled through as a key.
func TestAnEscapeIsDecodedOnceAndOnlyOnce(t *testing.T) {
	app, asked := live(t)

	for _, c := range []struct{ sent, want string }{
		{"a%20b.txt", "a b.txt"},     // a space, encoded
		{"a%25b.txt", "a%b.txt"},     // a LITERAL percent: decoded once, not twice
		{"a%2520b.txt", "a%20b.txt"}, // and once only — this is "a%20b.txt" as a key
	} {
		*asked = ""
		if code, body := ask(t, app, "DELETE", "/v1/s3/buckets/photos/objects/"+c.sent, ""); code != 204 {
			t.Fatalf("%s answered %d: %s", c.sent, code, body)
		}
		if !strings.HasSuffix(*asked, "/"+c.want) {
			t.Errorf("%s addressed %q, want a key ending %q", c.sent, *asked, c.want)
		}
	}

	// The MALFORMED case ("a%zz.txt") is not driven here and cannot be: Go's own
	// httptest.NewRequest refuses to build that request ("invalid URL escape"), so
	// no Go client can send one and this harness cannot either. remainder still
	// refuses it — a 400 rather than a key with a stray percent — for a raw client
	// that can put those bytes on the wire.
}

// TestTheUploadTakesItsBucketFromTheAddress drives POST /buckets/:bucket/objects
// at the address the published document declares, with the bucket ONLY in the path.
//
// It exists because uploadIn.Bucket carried `url:"-"`, and bindURL skips such a
// field for every URL source — the path included, not just the query. The document
// declares bucket a REQUIRED PATH PARAMETER, so every generated client builds this
// exact address, and every one of them was answered `400 invalid bucket name`. The
// operation was reachable only by repeating the bucket in the body, which no
// generated client does.
//
// The decoy half is the same property TestTheAddressBeatsADecoy pins for download
// and delete: path outranks body, so a body cannot name a target the URL did not.
// With the field skipped, the body was the ONLY source, which inverted it.
func TestTheUploadTakesItsBucketFromTheAddress(t *testing.T) {
	app, _ := live(t)

	at := "/v1/s3/buckets/photos/objects"
	st, body := ask(t, app, http.MethodPost, at, `{"key":"a.txt"}`)
	if st != http.StatusOK {
		t.Fatalf("POST %s with the bucket only in the path = %d %s, want 200 — the document "+
			"declares bucket a required path parameter, so this is the address every "+
			"generated client builds", at, st, body)
	}
	var signed struct{ URL, Key string }
	if err := json.Unmarshal([]byte(body), &signed); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	if !strings.Contains(signed.URL, "photos") {
		t.Errorf("the signed upload URL %q does not name the bucket the address did", signed.URL)
	}

	// The body must not be able to aim it somewhere the URL never named.
	st, body = ask(t, app, http.MethodPost, at, `{"bucket":"victim","key":"a.txt"}`)
	if st != http.StatusOK {
		t.Fatalf("POST %s with a body decoy = %d %s, want 200", at, st, body)
	}
	if err := json.Unmarshal([]byte(body), &signed); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	if strings.Contains(signed.URL, "victim") {
		t.Errorf("a body decoy redirected the upload to %q — path binds last precisely so "+
			"the target signed is the target the URL named", signed.URL)
	}
}
