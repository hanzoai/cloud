package s3_test

import (
	"context"
	"net/http"
	"sort"
	"strings"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/account"
	"github.com/hanzoai/cloud/apps/s3"
	"github.com/hanzoai/cloud/openapi"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// untypedByDesign is the CLOSED list of object-storage operations that are NOT
// typed ops, each with the wire fact that keeps it raw. The address is written
// the way the DOCUMENT writes it, which is the identity every projection keys on.
//
// This package recorded THREE blockers and two of them have expired, which is
// why the gate is a test rather than a comment: a refusal in prose cannot notice
// that its reason stopped being true.
//
//   - THE MONEY WIRE said a balance denial must be written IN BAND, because a
//     typed op refuses only by RETURNING an error and zip renders that flat.
//     cloud.Denied carries the fleet's nested {"error":{"code","message"}} off a
//     returned error and serve.go installs DenyEnvelope app-wide. It never even
//     applied here: the gate is in guard, a MIDDLEWARE, so a denial is written
//     before an op is entered.
//   - TWO STATUSES, ONE OBJECT said /health cannot declare both. zip v1.31.0 made
//     WithStatus variadic and added StatusCoder, so the op declares the set and
//     the ANSWER says which one it is.
//
// What is left is the wildcard, and it is the same refusal apps/pricing and
// apps/kms hold: fiber binds `*` as a greedy capture, the typed registry
// publishes op.Path VERBATIM while the router reading renders {wildcardN}, and
// openapi.Fold then refuses the whole document because the two spellings of one
// route do not match. It is not a description that would be wrong — the app
// publishes NOTHING until it is resolved.
//
// That one is RUN rather than believed: TestTheWildcardCannotBeATypedOp below
// registers a typed op at the same `*` and requires the refusal, so the reason
// that survived the other two goes red the day IT stops being true.
var untypedByDesign = map[string]string{
	"GET /v1/s3/buckets/{bucket}/objects/{wildcard1}": "the object key is a fiber greedy wildcard: the typed " +
		"registry publishes the path verbatim (`*`) while the router reading renders {wildcard1}, so " +
		"openapi.Fold refuses with \"typed op has no live route\" and the app publishes nothing at all.",
	"DELETE /v1/s3/buckets/{bucket}/objects/{wildcard1}": "the object key is a fiber greedy wildcard; same " +
		"two spellings of one route, same refusal from openapi.Fold.",
}

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

// TestTheWildcardCannotBeATypedOp is untypedByDesign's reason RUN rather than
// believed. It registers a typed op at the same greedy `*` the two object routes
// use and requires openapi.Spec to refuse a document for it — which is the whole
// of why those two are not typed ops.
//
// It is the half a ledger cannot hold: an entry names an address and a sentence,
// and a sentence cannot notice that its reason stopped being true. This package
// has been burned by exactly that twice (the money wire and two-statuses-one-object
// both outlived their causes and were found by re-reading, not by a red test), so
// the surviving reason is the one that fails when it expires. The day zip's
// Template names a wildcard the way cloud's router reading does, this goes green
// and says what to do about it.
func TestTheWildcardCannotBeATypedOp(t *testing.T) {
	app := zip.New(zip.Config{Logger: luxlog.New("probe"), DisableStartupMessage: true})
	g := app.Group("/v1/probe")
	zip.Get(g, "/buckets/:bucket/objects/*", func(context.Context, *probeIn) (*probeOut, error) {
		return &probeOut{}, nil
	})
	_, err := openapi.Spec(app, openapi.Info{Title: "probe", Version: "v1"})
	if err == nil {
		t.Fatal("openapi.Spec accepted a typed op on a `*` wildcard — zip's registry and cloud's " +
			"router reading now agree about how that segment is named, so untypedByDesign has " +
			"stopped being true. Type GET and DELETE /v1/s3/buckets/:bucket/objects/* and delete " +
			"both entries.")
	}
	if !strings.Contains(err.Error(), "no live route") {
		t.Fatalf("the document was refused for a different reason than the one recorded: %v", err)
	}
}

// probeIn and probeOut are the shapes the refusal above needs and nothing else
// does. They never reach a document — Spec refuses before one exists, which is
// the assertion.
type probeIn struct{}

type probeOut struct {
	OK bool `json:"ok"`
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

// TestTheUntypedRoutesDeclareWhatTheyCan measures what staying raw costs, and
// what it does NOT cost. A refusal buys three things nothing else can supply —
// prose lifted from a doc comment, an MCP tool, a CLI command — and it must not
// additionally cost a SHAPE: openapi.Register declares the download's answer, so
// an SDK generated off this document has a return type for it.
//
// The delete is the opposite assertion and just as deliberate. It reads no body
// and answers 204 with none, so it declares neither half; a declaration there
// would be a shape this route has never carried.
func TestTheUntypedRoutesDeclareWhatTheyCan(t *testing.T) {
	doc, err := openapi.Spec(alone(t), openapi.Info{Title: "s3", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	const wildcard = "/v1/s3/buckets/{bucket}/objects/{wildcard1}"
	download := doc.Paths[wildcard][strings.ToLower(http.MethodGet)]
	if download == nil {
		t.Fatalf("GET %s is not in the document", wildcard)
	}
	if download.Responses == nil {
		t.Errorf("GET %s declares no response — the URL it answers with is invisible to every "+
			"generated SDK, which is a cost the wildcard refusal does not have to carry", wildcard)
	}
	if download.RequestBody != nil {
		t.Errorf("GET %s declares a request body it has never read", wildcard)
	}
	remove := doc.Paths[wildcard][strings.ToLower(http.MethodDelete)]
	if remove == nil {
		t.Fatalf("DELETE %s is not in the document", wildcard)
	}
	if remove.RequestBody != nil || remove.Responses != nil {
		t.Errorf("DELETE %s declares a body: it reads none and answers 204 with none, so a "+
			"declaration states something the route does not do", wildcard)
	}
}

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
	if err := s3.Mount(app, cloud.Deps{}); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	return app
}
