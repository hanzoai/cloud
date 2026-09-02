package kms

// This file makes the KMS broker's typed partition a MEASUREMENT rather than a
// paragraph. Every operation this subsystem serves is now a typed op, so the
// partition is one-sided — and it is still asked, because the next route added
// here must be typed by default rather than by anyone remembering.
//
// The two that arrived last are the ones addressed by a greedy fiber segment.
// They were held out while the registry and the router spelled that segment
// differently, because the fold matches an op to its route by that spelling and
// refused the whole document when the two disagreed. TestTheWildcardAddress
// measures what is true of that address now, one fact at a time.
//
// THE ONE RESIDUAL, recorded here because a reader of the document will not see
// it and could reasonably assume the opposite. The tail IS declared as a path
// parameter now, and the document is self-consistent — but an OpenAPI path
// parameter matches ONE segment, so a client generated from that document fills
// `{wildcard1}` with the slashes percent-encoded, and no greedy segment matches
// that: the route answers 404 for a secret that is there. Measured, and pinned as
// fact FOUR. So a secret under a subpath is reachable by a caller that builds the
// path itself — the console, the operator, curl — and not by a generated SDK
// method. Declaring the parameter did not change that and cannot: it is a
// property of the address, not of the declaration. Closing it means addressing a
// secret by something OpenAPI can carry in one segment, which is a decision about
// the API rather than a gap in the generator.

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/openapi"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// untypedByDesign is the CLOSED list of KMS operations that are NOT typed ops,
// each with the WIRE FACT that typing it would move. Keyed the way the DOCUMENT
// writes an address, which is the identity every projection reads.
//
// It is EMPTY, and that is the whole of the subsystem's current answer: every
// operation the broker serves carries an In and an Out. It stays declared
// because TestEveryRouteIsTypedOrNamed reads it, so the next route added here is
// typed by default and an exception has to be written down with its reason.
var untypedByDesign = map[string]string{}

// proseless is the CLOSED list of published properties that carry no
// description, and it may only SHRINK.
//
// It is EMPTY. Its six entries were the fields of kmsSecret and kmsRemoved,
// which reached the document through openapi.Register — a client that derives a
// schema by REFLECTION, after Go has dropped the comments. Both are an op's Out
// now, so zipdoc lifts each field's prose from the struct itself and the gap
// closed by removing the client rather than by working around it. Do NOT re-open
// it by hand-writing schemas beside the structs.
var proseless = map[string]bool{}

// proselessParams is the same closed list for published PARAMETERS, and it is a
// SECOND ledger rather than more keys in the first because a parameter is
// declared on the OPERATION and never enters components.schemas, so the
// properties walk cannot see one.
//
// It is EMPTY. It held the greedy tail that names a secret, on both value
// routes, through two separate gaps: the tail was not declared as a parameter at
// all, and then it was declared but its prose was looked up under a router key
// fiber does not use for a REQUIRED greedy segment. Both are closed, so ONE
// field — kmsRef.Secret, tagged with the key the router actually matched on —
// carries the binding, the schema and the description together.
//
// It may only SHRINK, in both directions: an entry that starts publishing prose
// goes red too, so a gap that closes empties the ledger instead of outliving it.
var proselessParams = map[string]bool{}

// broker mounts the REAL Mount on a bare app with a live master key, so every
// gate below reads the router the binary serves.
func broker(t *testing.T) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("test"), DisableStartupMessage: true})
	c, err := New(Config{DataDir: t.TempDir(), MasterKeyB64: testMasterKey}, luxlog.New("test"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	if err := Use(app, cloud.Deps{KMS: c, Brand: "hanzo", IAMIssuer: "https://hanzo.id/"}); err != nil {
		t.Fatalf("Use:  %v", err)
	}
	return app
}

// testMasterKey is a fixed 32-byte key, base64. A fixed one is right here: these
// tests assert on shapes and admission, never on ciphertext.
const testMasterKey = "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY="

// ask drives one request through the live router. Identity is the pair
// SanitizeIdentity mints; admin adds the org-admin claim the write endpoint
// reads.
func ask(t *testing.T, app *zip.App, method, path, org string, admin bool, body string) (int, []byte) {
	t.Helper()
	var rdr *strings.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	var req *http.Request
	var err error
	if rdr != nil {
		req, err = http.NewRequest(method, "http://x"+path, rdr)
	} else {
		req, err = http.NewRequest(method, "http://x"+path, nil)
	}
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if org != "" {
		req.Header.Set("X-Org-Id", org)
		req.Header.Set("X-User-Id", "u@"+org)
	}
	if admin {
		req.Header.Set("X-User-IsOrgAdmin", "true")
	}
	res, err := app.Test(req)
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	defer func() { _ = res.Body.Close() }()
	out := make([]byte, 0, 1024)
	buf := make([]byte, 4096)
	for {
		n, rerr := res.Body.Read(buf)
		out = append(out, buf[:n]...)
		if rerr != nil {
			break
		}
	}
	return res.StatusCode, out
}

// ---- the partition ---------------------------------------------------------

// TestEveryRouteIsTypedOrNamed fails when a KMS operation is neither a typed op
// nor named above, so the next route added here is typed BY DEFAULT — and fails
// on a stale reason naming a route this subsystem no longer serves. The two
// ledgers must SUM to the served surface.
func TestEveryRouteIsTypedOrNamed(t *testing.T) {
	app := broker(t)
	doc, err := openapi.Spec(app, openapi.Info{Title: "kms", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	reg, err := openapi.Typed(app)
	if err != nil {
		t.Fatalf("typed registry: %v", err)
	}
	served := map[string]bool{}
	for path, item := range doc.Paths {
		if !strings.HasPrefix(path, "/v1/kms") {
			continue
		}
		for method := range item {
			served[strings.ToUpper(method)+" "+path] = true
		}
	}
	if len(served) == 0 {
		t.Fatal("the broker serves nothing at all — the router moved and this gate is now blind")
	}
	typed := map[string]bool{}
	for key := range reg.Ops {
		if i := strings.Index(key, " "); i > 0 && strings.HasPrefix(key[i+1:], "/v1/kms") {
			typed[key] = true
		}
	}
	var untyped []string
	for key := range served {
		if typed[key] || untypedByDesign[key] != "" {
			continue
		}
		untyped = append(untyped, key)
	}
	if len(untyped) > 0 {
		sort.Strings(untyped)
		t.Errorf("untyped and unnamed: %s\nAn untyped route projects to NOTHING — no prose, no MCP tool, "+
			"no CLI command, no typed SDK method. Convert it (zip.Get/Post/... on the group), or add it to "+
			"untypedByDesign with the WIRE FACT that typing it would move.", strings.Join(untyped, ", "))
	}
	for key := range untypedByDesign {
		if !served[key] {
			t.Errorf("untypedByDesign names %s, which kms does not serve — a stale reason nobody can re-check", key)
		}
		if typed[key] {
			t.Errorf("untypedByDesign names %s, which IS a typed op — remove the reason", key)
		}
	}
	if len(typed)+len(untypedByDesign) != len(served) {
		t.Errorf("%d typed + %d named != %d served", len(typed), len(untypedByDesign), len(served))
	}
	if len(typed) != 7 || len(untypedByDesign) != 0 {
		t.Errorf("the partition moved: %d typed, %d named (was 7 + 0)", len(typed), len(untypedByDesign))
	}
}

// TestTheWildcardAddress measures the facts a greedy address turns on, on a
// THROWAWAY app carrying the shape rather than on the broker's own router: each
// is a claim about a DEPENDENCY, and a dependency moves.
//
// ONE — the registry and the router spell the ADDRESS the same. While they did
// not, the fold refused the whole document ("no live route"), which is why these
// two routes stayed untyped for as long as they did. This goes red if that
// returns, and it is then every kms operation gone, not one.
//
// TWO — the tail is DECLARED as a path parameter. It comes from the pattern, not
// from any field, which is why the probe's In can take nothing and the parameter
// is there anyway.
//
// THREE — fiber's key for that capture is `+1`, and the marker is part of it: a
// REQUIRED greedy segment is `+N` and an OPTIONAL one is `*N`, counted on their
// own sequences. That key is what kmsRef.Secret carries, and a field tagged with
// the other marker binds NOTHING while still publishing a clean document — the
// worst shape available, because the op then reads an empty address and the
// document says it is fine. So the tag is measured here, not assumed.
//
// FOUR — that a secret has ONE address however it is spelled. An OpenAPI path
// parameter is one segment, so a generated client fills `{wildcard1}` with the
// slashes percent-encoded; the router decodes nothing, so that spelling used to
// reach no record and a subpath'd secret was addressable by a hand-built path and
// by no SDK. targetOf decodes the capture before it becomes a coordinate, which
// closes it — and safely, because the validators run AFTER the decode, so an
// encoded traversal is refused by the same rule as a plain one
// (TestAnEncodedTraversalIsStillRefused, kms_test.go).
func TestTheWildcardAddress(t *testing.T) {
	app := zip.New(zip.Config{Logger: luxlog.New("test"), DisableStartupMessage: true})
	g := app.Group("/v1/probe")
	zip.Get(g, "/secrets/+", func(context.Context, *cloud.Unit) (*kmsSecret, error) {
		return &kmsSecret{}, nil
	})
	var captured []string
	g.Get("/raw/+", func(c *zip.Ctx) error {
		captured = c.Fiber().Route().Params
		return c.JSON(http.StatusOK, map[string]string{"tail": c.Param("+")})
	})

	doc, err := openapi.Spec(app, openapi.Info{Title: "probe", Version: "v1"})
	if err != nil {
		t.Fatalf("openapi.Spec refused a typed op on a `+`: %v\nThe registry and the router have "+
			"gone back to spelling that segment differently, and Spec builds ONE document — so this "+
			"is every kms operation gone, not one.", err)
	}
	const at = "/v1/probe/secrets/{wildcard1}"
	op := doc.Paths[at]["get"]
	if op == nil {
		t.Fatalf("the typed op is not at %s — the two spellings moved", at)
	}

	// TWO. The probe's In takes nothing, so this parameter can only have come
	// from the pattern.
	if len(op.Parameters) != 1 || op.Parameters[0].In != "path" || op.Parameters[0].Name != "wildcard1" {
		t.Errorf("%s publishes %+v — the tail is the ONE argument these operations take, and a "+
			"document that does not declare it describes a call nobody can make", at, op.Parameters)
	}

	// THREE.
	if status, _ := ask(t, app, http.MethodGet, "/v1/probe/raw/ci/deploy/token", "", false, ""); status != http.StatusOK {
		t.Fatalf("the raw probe route did not match: %d", status)
	}
	if len(captured) != 1 || captured[0] != "+1" {
		t.Errorf("fiber keys the capture %v, not [+1] — that key is kmsRef.Secret's `url:` tag, and a "+
			"tag naming any other key binds nothing while the document stays clean, so the op would "+
			"read an EMPTY address and say so nowhere", captured)
	}

	// FOUR, on the broker's own router because it needs a real record to miss.
	live := broker(t)
	if s, b := ask(t, live, http.MethodPost, "/v1/kms/secrets", "acme", true,
		`{"path":"/ci/deploy","name":"token","env":"prod","value":"v"}`); s != http.StatusOK {
		t.Fatalf("seed: %d %s", s, b)
	}
	if s, _ := ask(t, live, http.MethodGet, "/v1/kms/secrets/ci/deploy/token?env=prod", "acme", false, ""); s != http.StatusOK {
		t.Fatalf("the raw multi-segment address stopped reaching the record: %d", s)
	}
	// FOUR's second half USED to assert a 404 here, and recorded it as the residual
	// a declared parameter does not fix: an SDK fills a path parameter
	// percent-encoded, and the encoded spelling reached no record. It is CLOSED —
	// targetOf decodes the capture before it becomes a coordinate, so one secret
	// has one address however it is spelled, and a generated client can address a
	// subpath'd secret. Asserted in the direction it now holds, so a regression
	// reads as one.
	if s, _ := ask(t, live, http.MethodGet, "/v1/kms/secrets/ci%2Fdeploy%2Ftoken?env=prod", "acme", false, ""); s != http.StatusOK {
		t.Errorf("the percent-encoded spelling answered %d — every generated client sends that "+
			"form, so a subpath'd secret is unreachable from every SDK again", s)
	}
}

// TestEveryTypedOpIsDescribed proves the prose reached the registry. zipdoc
// lifts it at build time, so a package whose //go:generate directive is missing
// publishes a fully typed surface that says nothing.
func TestEveryTypedOpIsDescribed(t *testing.T) {
	reg, err := openapi.Typed(broker(t))
	if err != nil {
		t.Fatalf("typed registry: %v", err)
	}
	seen := 0
	for key, op := range reg.Ops {
		if !strings.Contains(key, "/v1/kms") {
			continue
		}
		seen++
		if strings.TrimSpace(op.Description) == "" {
			t.Errorf("%s carries no description — its doc comment did not reach zipdoc_gen.go", key)
		}
		if strings.Contains(op.Summary, "\n") {
			t.Errorf("%s publishes a multi-line summary %q", key, op.Summary)
		}
	}
	if seen == 0 {
		t.Fatal("no typed ops found — the gate is blind")
	}
}

// TestEveryPublishedFieldIsDescribed gates the RESPONSE side, which the op-level
// gate cannot see: typing a route documents its ADDRESS and its SHAPE, never the
// shape's FIELDS. proseless is the closed exemption and may only shrink.
//
// It asks openapi.Bare, the ONE walker, and that is not a style preference — it
// is the fix for this gate having been BLIND to exactly the six properties its
// ledger names. It used to walk the Go value and cast each schema to
// map[string]any, which is the failure Bare's own doc comment describes:
// Components.Schemas is open-typed, the typed fold contributes zip's map and
// Register contributes a *Schema, so the cast skipped every Register-declared
// component — kmsSecret and kmsRemoved, i.e. the whole of proseless. Measured by
// emptying the ledger, which left the gate GREEN with six undescribed properties
// in the document. A check that skips passes for the wrong reason, and this one
// had been passing that way since it was written. Bare reads the MARSHALLED
// document, so it cannot skip a half it did not expect, and it descends into
// nested shapes, array items and each alternative of a oneOf as well.
func TestEveryPublishedFieldIsDescribed(t *testing.T) {
	doc, err := openapi.Spec(broker(t), openapi.Info{Title: "kms", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	if doc.Components == nil || len(doc.Components.Schemas) == 0 {
		t.Fatal("no components — the gate is blind")
	}
	bare, err := openapi.Bare(doc)
	if err != nil {
		t.Fatalf("bare: %v", err)
	}
	undescribed := map[string]bool{}
	var unnamed []string
	for _, key := range bare {
		undescribed[key] = true
		if !proseless[key] {
			unnamed = append(unnamed, key)
		}
	}
	for key := range proseless {
		if !undescribed[key] {
			t.Errorf("%s now publishes prose — remove it from proseless, which may only shrink", key)
		}
	}
	if len(unnamed) > 0 {
		sort.Strings(unnamed)
		t.Errorf("%d published properties carry no description: %s\nThey reach openapi.yaml, every "+
			"generated SDK and every MCP inputSchema bare. Describe the FIELD in the Go struct — "+
			"zipdoc lifts it for a typed op.", len(unnamed), strings.Join(unnamed, ", "))
	}
}

// TestEveryPublishedParameterIsDescribed gates the REQUEST side of the same
// fact, and it is a SEPARATE walk because a parameter is not a schema.
//
// A parameter is declared on the OPERATION and never enters components.schemas,
// so the properties walk above — the shape every field gate in this fleet takes
// — reports a clean surface while the arguments an operation ACTUALLY TAKES
// publish bare. Measured here before it was written: 6 of 6 said nothing, and
// four of those were the filters on the one typed read in this subsystem.
//
// proselessParams is the closed exemption and may only shrink.
func TestEveryPublishedParameterIsDescribed(t *testing.T) {
	doc, err := openapi.Spec(broker(t), openapi.Info{Title: "kms", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	var bare []string
	seen := 0
	for path, item := range doc.Paths {
		if !strings.HasPrefix(path, "/v1/kms") {
			continue
		}
		for method, op := range item {
			if op == nil {
				continue
			}
			for _, p := range op.Parameters {
				seen++
				key := strings.ToUpper(method) + " " + path + " " + p.In + ":" + p.Name
				switch {
				case strings.TrimSpace(p.Description) != "" && proselessParams[key]:
					t.Errorf("%s now publishes prose — remove it from proselessParams, which may only shrink", key)
				case strings.TrimSpace(p.Description) == "" && !proselessParams[key]:
					bare = append(bare, key)
				}
			}
		}
	}
	if seen == 0 {
		t.Fatal("kms publishes no parameters at all — the router moved and this gate is now blind")
	}
	if len(bare) > 0 {
		sort.Strings(bare)
		t.Errorf("%d published parameters carry no description: %s\nA parameter is the argument a "+
			"caller has to fill in, so it reaches openapi.yaml, every generated SDK and every MCP "+
			"inputSchema as a name and a type and nothing else. Describe the FIELD it binds to and "+
			"give that field a json name — zipdoc files prose under `<Type>.<json name>` and skips a "+
			"field named `-`, while zip reads a parameter's description at `<In>.<url name>`.",
			len(bare), strings.Join(bare, ", "))
	}
}

// TestTheListFiltersStayInTheURL is the wire half of giving kmsList's fields
// their json names, and it is the assertion that makes the change safe rather
// than merely tidy.
//
// A json name is how the prose reaches the published parameter (zipdoc files a
// field's description under `<Type>.<json name>` and skips `-`), and it is ALSO
// how zip's binder would fill a field from a request BODY. On this op there is
// no body to fill from — hasBody says a GET carries none, so the handler never
// reads one (zip/typed.go:517) and no requestBody is published — and this pins
// both halves, because the day either moves the filters would start taking a
// second source that the untyped handler this replaced never had.
func TestTheListFiltersStayInTheURL(t *testing.T) {
	app := broker(t)
	if s, b := ask(t, app, http.MethodPost, "/v1/kms/secrets", "acme", true,
		`{"path":"/ci","name":"a","env":"prod","value":"v"}`); s != http.StatusOK {
		t.Fatalf("seed: %d %s", s, b)
	}
	// A GET body naming another subtree is not read: the answer is the URL's.
	status, body := ask(t, app, http.MethodGet, "/v1/kms/secrets?path=/ci&env=prod", "acme", false,
		`{"path":"/nowhere","env":"nope"}`)
	if status != http.StatusOK {
		t.Fatalf("status %d: %s", status, body)
	}
	var got kmsSecrets
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Total != 1 {
		t.Errorf("total %d, want 1 — a GET body reached the filters (%s)", got.Total, body)
	}

	doc, err := openapi.Spec(app, openapi.Info{Title: "kms", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	op := doc.Paths["/v1/kms/secrets"]["get"]
	if op == nil {
		t.Fatal("the listing is not in the document")
	}
	if op.RequestBody != nil {
		t.Error("the listing publishes a request body it has never read — every generated client " +
			"gained an argument for a body a GET does not carry")
	}
	want := map[string]bool{"env": true, "environment": true, "path": true, "secretPath": true}
	for _, p := range op.Parameters {
		if p.In != "query" || !want[p.Name] {
			t.Errorf("the listing publishes %s parameter %q — the filter set moved", p.In, p.Name)
			continue
		}
		delete(want, p.Name)
	}
	if len(want) > 0 {
		t.Errorf("the listing stopped publishing %v — both spellings of both filters are the wire", want)
	}
}

// TestASecretIsAddressedBySubPath is the wire the greedy route exists for, and
// the assertion that makes typing it safe rather than merely richer.
//
// A secret is named by a SUB-PATH, so the address is a multi-segment tail. The
// untyped handler read that tail off the request and nothing else; the typed op
// must reach the same record from the same URL, and must still refuse to take
// the address from anywhere a caller could also name. Seeded twice on purpose:
// if the address ever came from the body or the query, the answer would be the
// OTHER secret rather than an error, which is a silent cross-read within the
// org and the sharpest thing a wire test here can catch.
func TestASecretIsAddressedBySubPath(t *testing.T) {
	app := broker(t)
	for _, seed := range []string{
		`{"path":"/ci/deploy","name":"token","env":"prod","value":"deep"}`,
		`{"name":"other","env":"prod","value":"shallow"}`,
	} {
		if s, b := ask(t, app, http.MethodPost, "/v1/kms/secrets", "acme", true, seed); s != http.StatusOK {
			t.Fatalf("seed: %d %s", s, b)
		}
	}

	read := func(t *testing.T, path, body string) kmsSecret {
		t.Helper()
		status, raw := ask(t, app, http.MethodGet, path, "acme", false, body)
		if status != http.StatusOK {
			t.Fatalf("GET %s: %d %s", path, status, raw)
		}
		var got kmsSecret
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return got
	}

	t.Run("multi-segment", func(t *testing.T) {
		got := read(t, "/v1/kms/secrets/ci/deploy/token?env=prod", "")
		if got.Name != "token" || got.Value != "deep" {
			t.Errorf("the tail no longer addresses the record it names: %+v", got)
		}
	})
	// THE DECOY. Every spelling a caller could reach for, against a REAL other
	// record, so a decoy that won would be a silent read of the wrong secret
	// rather than an error. zip binds body, then query, then path, and the path's
	// key is the one kmsRef.Secret carries — so the matched tail overwrites all of
	// them. Untag that field and the decoy wins.
	for _, decoy := range []struct{ name, query, body string }{
		{"query names the wire field", "&secret=other", ""},
		{"query names the router key", "&%2B1=other", ""},
		{"query names the document's parameter", "&wildcard1=other", ""},
		{"body names the wire field", "", `{"secret":"other"}`},
		{"both at once", "&secret=other&%2B1=other", `{"secret":"other"}`},
	} {
		t.Run("decoy: "+decoy.name, func(t *testing.T) {
			got := read(t, "/v1/kms/secrets/ci/deploy/token?env=prod"+decoy.query, decoy.body)
			if got.Name != "token" || got.Value != "deep" {
				t.Errorf("the decoy won: %+v\nThe address is the segment the ROUTER matched. zip binds "+
					"body, then query, then path, and kmsRef.Secret is tagged with the path's own key, "+
					"so nothing a caller sends can name a second address.", got)
			}
		})
	}
	t.Run("env defaults", func(t *testing.T) {
		if s, b := ask(t, app, http.MethodPost, "/v1/kms/secrets", "acme", true,
			`{"name":"plain","env":"default","value":"v"}`); s != http.StatusOK {
			t.Fatalf("seed: %d %s", s, b)
		}
		got := read(t, "/v1/kms/secrets/plain", "")
		if got.Env != defaultEnv || got.Value != "v" {
			t.Errorf("an omitted env stopped falling back to %q: %+v", defaultEnv, got)
		}
	})
	t.Run("delete reaches the same record", func(t *testing.T) {
		status, raw := ask(t, app, http.MethodDelete, "/v1/kms/secrets/ci/deploy/token?env=prod", "acme", true, "")
		if status != http.StatusOK {
			t.Fatalf("delete: %d %s", status, raw)
		}
		var gone kmsRemoved
		if err := json.Unmarshal(raw, &gone); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if !gone.Deleted || gone.Name != "token" || gone.Env != "prod" {
			t.Errorf("the receipt names another record: %+v", gone)
		}
		if s, b := ask(t, app, http.MethodGet, "/v1/kms/secrets/ci/deploy/token?env=prod", "acme", false, ""); s != http.StatusNotFound {
			t.Errorf("the record survived the delete: %d %s", s, b)
		}
		if s, _ := ask(t, app, http.MethodGet, "/v1/kms/secrets/other?env=prod", "acme", false, ""); s != http.StatusOK {
			t.Error("the delete took a neighbouring record with it")
		}
	})
	t.Run("the document offers no second address", func(t *testing.T) {
		doc, err := openapi.Spec(app, openapi.Info{Title: "kms", Version: "v1"})
		if err != nil {
			t.Fatalf("spec: %v", err)
		}
		for _, method := range []string{"get", "delete"} {
			op := doc.Paths["/v1/kms/secrets/{wildcard1}"][method]
			if op == nil {
				t.Fatalf("%s is not in the document", method)
			}
			want := map[string]string{"wildcard1": "path", "env": "query"}
			for _, p := range op.Parameters {
				at, ok := want[p.Name]
				if !ok || at != p.In {
					t.Errorf("%s publishes a %s parameter %q — these two take the address and the "+
						"environment, and a third URL-borne name is a second way to say one of them",
						method, p.In, p.Name)
					continue
				}
				delete(want, p.Name)
			}
			if len(want) > 0 {
				t.Errorf("%s stopped publishing %v", method, want)
			}
			if op.RequestBody != nil {
				t.Errorf("%s publishes a request body it never reads", method)
			}
		}
	})
}

// TestNoInputNamesATenant is the tenant boundary asserted where it actually
// lives: in the SHAPE of what a caller may send.
//
// The org is server-minted — principal.OrgFrom, off the claim cloud.Bridge
// parked — and an In field is caller-supplied, so a tenant read from one is a
// cross-tenant read the caller asserted for itself. A behaviour test can only
// catch the spelling it drives: a query name is refused by the route test below,
// but a field tagged `url:"-"` is invisible to every REST probe and still
// arrives as an MCP argument or a call-plane field, which is the same read
// through a quieter transport.
//
// So this walks the four inputs and fails on a field that names a tenant at all,
// under any tag. It is the check that cannot be satisfied by moving the field
// out of the URL.
func TestNoInputNamesATenant(t *testing.T) {
	tenant := map[string]bool{"org": true, "owner": true, "tenant": true, "account": true, "namespace": true}
	seen := 0
	for _, in := range []any{kmsRef{}, kmsPut{}, kmsList{}, kmsLogin{}} {
		rt := reflect.TypeOf(in)
		for i := range rt.NumField() {
			seen++
			f := rt.Field(i)
			for _, name := range []string{f.Name, f.Tag.Get("json"), f.Tag.Get("url")} {
				name, _, _ = strings.Cut(name, ",")
				if tenant[strings.ToLower(strings.TrimSpace(name))] {
					t.Errorf("%s.%s names a tenant — the org is minted from the validated claim and "+
						"never sent, so a caller that could name one would be reading somebody "+
						"else's secrets with its own credential", rt.Name(), f.Name)
				}
			}
		}
	}
	if seen == 0 {
		t.Fatal("no input fields were walked — the inputs moved and this check is now blind")
	}
}

// TestACallerCannotNameAnotherTenant is the same rule driven through the live
// router, which is what catches a tenant that arrives as a URL name rather than
// as a struct field.
func TestACallerCannotNameAnotherTenant(t *testing.T) {
	app := broker(t)
	if s, b := ask(t, app, http.MethodPost, "/v1/kms/secrets", "other", true,
		`{"name":"shared","env":"prod","value":"theirs"}`); s != http.StatusOK {
		t.Fatalf("seed: %d %s", s, b)
	}
	for _, q := range []string{"", "&org=other", "&owner=other", "&tenant=other", "&account=other"} {
		t.Run("query"+q, func(t *testing.T) {
			status, body := ask(t, app, http.MethodGet, "/v1/kms/secrets/shared?env=prod"+q, "acme", false, "")
			if status != http.StatusNotFound {
				t.Fatalf("status %d, want 404 — acme reached another tenant's record: %s", status, body)
			}
		})
	}
	status, body := ask(t, app, http.MethodDelete, "/v1/kms/secrets/shared?env=prod&org=other", "acme", true, "")
	if status != http.StatusNotFound {
		t.Fatalf("delete status %d, want 404 — acme reached another tenant's record: %s", status, body)
	}
	if s, _ := ask(t, app, http.MethodGet, "/v1/kms/secrets/shared?env=prod", "other", false, ""); s != http.StatusOK {
		t.Error("the other tenant's record went missing — the refusals above proved nothing")
	}
}

// ---- the wire --------------------------------------------------------------

// TestTypedAnswersAreByteIdenticalToTheMaps is the measurement. Each case is the
// map literal the untyped handler assembled beside the typed value that replaced
// it: encoding/json writes a map in SORTED KEY ORDER, so a model whose fields
// are not in alphabetical json-tag order produces different BYTES for the same
// value.
func TestTypedAnswersAreByteIdenticalToTheMaps(t *testing.T) {
	yes := true
	for _, tc := range []struct {
		name  string
		was   any
		typed any
	}{
		{"health ok", map[string]any{"service": "kms", "status": "ok", "signing": true, "ready": true},
			&kmsHealth{Ready: true, Service: "kms", Signing: &yes, Status: "ok"}},
		{"health no client", map[string]any{"service": "kms", "status": "degraded", "ready": false,
			"error": "no in-process KMS client (secrets served out-of-process or disabled)"},
			&kmsHealth{Error: "no in-process KMS client (secrets served out-of-process or disabled)",
				Ready: false, Service: "kms", Status: "degraded"}},
		{"config", map[string]any{"brand": "hanzo", "issuer": "https://hanzo.id",
			"apiBase": "/v1/kms", "loginPath": "/v1/kms/auth/login"},
			&kmsConfig{APIBase: "/v1/kms", Brand: "hanzo", Issuer: "https://hanzo.id",
				LoginPath: "/v1/kms/auth/login"}},
		{"list", map[string]any{"secrets": []SecretMeta{{Name: "a", Path: "/p", Env: "prod", Scheme: "aes"}},
			"total": 1, "names": []string{"a"}},
			&kmsSecrets{Names: []string{"a"}, Total: 1,
				Secrets: []SecretMeta{{Name: "a", Path: "/p", Env: "prod", Scheme: "aes"}}}},
		{"stored", map[string]any{"stored": true, "name": "a", "env": "prod"},
			&kmsStored{Env: "prod", Name: "a", Stored: true}},
		{"secret", map[string]any{"name": "a", "env": "prod", "value": "v"},
			kmsSecret{Env: "prod", Name: "a", Value: "v"}},
		{"removed", map[string]any{"deleted": true, "name": "a", "env": "prod"},
			kmsRemoved{Deleted: true, Env: "prod", Name: "a"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			was, err := json.Marshal(tc.was)
			if err != nil {
				t.Fatalf("marshal map: %v", err)
			}
			now, err := json.Marshal(tc.typed)
			if err != nil {
				t.Fatalf("marshal typed: %v", err)
			}
			if string(was) != string(now) {
				t.Errorf("the wire MOVED.\n was: %s\n now: %s\nDeclare the model's fields in "+
					"alphabetical json-tag order — encoding/json sorts a map's keys.", was, now)
			}
		})
	}
}

// TestTheEndpointIsOneRule drives the live router across both admission scopes. A
// member reads and cannot write; an admin does both; an anonymous caller does
// neither. The point is not that each answer is right — it is that the typed
// collection ops and the raw value routes reach the SAME answer, because they
// ask the same function.
func TestTheEndpointIsOneRule(t *testing.T) {
	app := broker(t)
	for _, tc := range []struct {
		name, method, path, org string
		admin                   bool
		body                    string
		want                    int
	}{
		{name: "anonymous list", method: http.MethodGet, path: "/v1/kms/secrets", want: http.StatusForbidden},
		{name: "anonymous read", method: http.MethodGet, path: "/v1/kms/secrets/a", want: http.StatusForbidden},
		{name: "member lists", method: http.MethodGet, path: "/v1/kms/secrets", org: "acme", want: http.StatusOK},
		{name: "member cannot write", method: http.MethodPost, path: "/v1/kms/secrets", org: "acme",
			body: `{"name":"a","env":"prod","value":"v"}`, want: http.StatusForbidden},
		{name: "member cannot delete", method: http.MethodDelete, path: "/v1/kms/secrets/a", org: "acme",
			want: http.StatusForbidden},
		{name: "admin writes", method: http.MethodPost, path: "/v1/kms/secrets", org: "acme", admin: true,
			body: `{"name":"a","env":"prod","value":"v"}`, want: http.StatusOK},
		{name: "admin reads back", method: http.MethodGet, path: "/v1/kms/secrets/a?env=prod", org: "acme",
			admin: true, want: http.StatusOK},
		{name: "bad org", method: http.MethodGet, path: "/v1/kms/secrets", org: "a/b", want: http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, body := ask(t, app, tc.method, tc.path, tc.org, tc.admin, tc.body)
			if status != tc.want {
				t.Fatalf("status %d, want %d (body %s)", status, tc.want, body)
			}
		})
	}
}

// TestTheWriteRefusesOffTheHTTPPath is the other half of that endpoint. An
// in-process CLI invoke runs the op with no request at all, so there is no
// principal to be an admin of anything and the write must refuse rather than
// act for whoever the context happens to name.
func TestTheWriteRefusesOffTheHTTPPath(t *testing.T) {
	o := ops{s: &cloud.Service[state]{Base: cloud.NewBase(cloud.Deps{}, "kms")}}
	if _, err := o.putSecret(context.Background(), &kmsPut{Name: "a", Env: "prod", Value: "v"}); err == nil {
		t.Fatal("a write with no request succeeded — the admin gate reads the request, and with no " +
			"request there is no admin")
	}
	if _, err := o.listSecrets(context.Background(), &kmsList{}); err == nil {
		t.Fatal("a read with no request succeeded")
	}
}

// TestTheQueryStringCannotRedirectAWrite is the wire-widening class every
// POST/PUT/PATCH conversion has to answer for. zip's binder fills an In field
// from the QUERY as well as the body, so a converted write silently starts
// accepting `?name=` — which here would put a secret under a name the body never
// asked for, and `?value=` would put the secret itself in the URL. The untyped
// handler read the body and nothing else; `url:"-"` restores that.
func TestTheQueryStringCannotRedirectAWrite(t *testing.T) {
	app := broker(t)
	if s, b := ask(t, app, http.MethodPost, "/v1/kms/secrets?name=hijacked&value=leaked&env=dev",
		"acme", true, `{"name":"honest","env":"prod","value":"v"}`); s != http.StatusOK {
		t.Fatalf("write: %d %s", s, b)
	}
	status, body := ask(t, app, http.MethodGet, "/v1/kms/secrets", "acme", false, "")
	if status != http.StatusOK {
		t.Fatalf("list: %d %s", status, body)
	}
	var got kmsSecrets
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Names) != 1 || got.Names[0] != "honest" {
		t.Errorf("the query string reached the body: %s", body)
	}
}

// TestListAcceptsBothSpellings pins the alias precedence the operator depends
// on. Two callers already spell these parameters two ways, and the primary wins
// when both are sent — the same rule firstQuery encoded before this was typed.
func TestListAcceptsBothSpellings(t *testing.T) {
	app := broker(t)
	if s, b := ask(t, app, http.MethodPost, "/v1/kms/secrets", "acme", true,
		`{"path":"/ci","name":"a","env":"prod","value":"v"}`); s != http.StatusOK {
		t.Fatalf("seed: %d %s", s, b)
	}
	for _, q := range []string{"?path=/ci&env=prod", "?secretPath=/ci&environment=prod",
		"?path=/ci&secretPath=/nowhere&env=prod&environment=nope"} {
		t.Run(q, func(t *testing.T) {
			status, body := ask(t, app, http.MethodGet, "/v1/kms/secrets"+q, "acme", false, "")
			if status != http.StatusOK {
				t.Fatalf("status %d: %s", status, body)
			}
			var got kmsSecrets
			if err := json.Unmarshal(body, &got); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if got.Total != 1 {
				t.Errorf("total %d, want 1 — the alias precedence moved (%s)", got.Total, body)
			}
		})
	}
}

// TestLoginKeepsItsBodyCap proves the cap survived the conversion. It is now a
// route middleware rather than the first line of a handler, because a typed op
// is handed a decoded value and cannot refuse a body it never sees — and this route
// is public and unauthenticated, which is exactly where an unbounded body
// matters.
func TestLoginKeepsItsBodyCap(t *testing.T) {
	app := broker(t)
	huge := `{"clientId":"a","clientSecret":"` + strings.Repeat("x", maxLoginBodyBytes) + `"}`
	status, body := ask(t, app, http.MethodPost, "/v1/kms/auth/login", "", false, huge)
	if status != http.StatusBadRequest {
		t.Fatalf("status %d, want 400 (body %s)", status, body)
	}
	if !strings.Contains(string(body), "login body too large") {
		t.Errorf("the cap answered something else: %s", body)
	}
}

// TestHealthFailsClosedWithBothStatuses proves the one op that answers two
// statuses over ONE shape still answers both, and that the 503 rides the same
// body — the whole reason it is a declared status rather than a returned error.
func TestHealthFailsClosedWithBothStatuses(t *testing.T) {
	app := broker(t)
	status, body := ask(t, app, http.MethodGet, "/v1/kms/health", "", false, "")
	if status != http.StatusOK {
		t.Fatalf("keyed broker: %d %s", status, body)
	}
	var ok kmsHealth
	if err := json.Unmarshal(body, &ok); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !ok.Ready || ok.Status != "ok" {
		t.Errorf("keyed broker reports %s", body)
	}

	// No in-process client at all: health-only mode, which must SAY so rather
	// than pretend to host secrets it cannot open.
	bare := zip.New(zip.Config{Logger: luxlog.New("test"), DisableStartupMessage: true})
	if err := Use(bare, cloud.Deps{Brand: "hanzo"}); err != nil {
		t.Fatalf("Use:  %v", err)
	}
	status, body = ask(t, bare, http.MethodGet, "/v1/kms/health", "", false, "")
	if status != http.StatusServiceUnavailable {
		t.Fatalf("health-only: %d %s", status, body)
	}
	var degraded kmsHealth
	if err := json.Unmarshal(body, &degraded); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if degraded.Ready || degraded.Error == "" || degraded.Signing != nil {
		t.Errorf("health-only reports %s — it must be not-ready, carry the reason, and claim nothing "+
			"about signing it cannot ask", body)
	}
}
