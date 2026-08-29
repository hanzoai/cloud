package help

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/openapi"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// mountHelpOnly mounts ONLY this subsystem, so the projection gate below reads this
// package's surface and not the framework engine's. The public plane owns no store,
// so it needs nothing else mounted to answer.
func mountHelpOnly(t *testing.T) *zip.App {
	t.Helper()
	t.Setenv("CLOUD_HELP_PUBLIC_ORG", "acme")
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	compose(app)
	if err := Use(app, cloud.Deps{DataDir: t.TempDir()}); err != nil {
		t.Fatalf("mount help: %v", err)
	}
	return app
}

// typed_wire_test.go MEASURES the wire the four /v1/help ops answer, rather than
// asserting it in prose. Every one of them is a typed op now, and typing moves two
// things a status-code test would not see on its own:
//
//   - the ORDER four different statuses are decided in. zip decodes a typed op's
//     body BEFORE the handler runs, so a naive conversion would have answered 400
//     for an oversized or unreadable body on a deployment whose help center does
//     not exist (404) or is not installed (503). helpTicketIntake records the size
//     and the parse instead of refusing, and fileTicket reads them back after those
//     two gates — which is what these tests pin.
//   - the ?limit grammar. The untyped handler ran strconv.Atoi over the raw query;
//     zip's binder leaves an unreadable integer at zero, which articleLimit reads as
//     "use the default". Same answer, and the test says so out loud.
//
// TestPublicIntake_RejectsOversizedBody (subsystem_test.go) already pins the 413
// itself; these pin the ORDER around it.

// TestIntakeGateOrder proves the four statuses stay in the order the route has
// always decided them in, using the input that has BOTH problems at once: an
// oversized body sent to a deployment with no help center is still a 404, and an
// unreadable one to an org whose Help model is not installed is still a 503.
func TestIntakeGateOrder(t *testing.T) {
	huge := strings.Repeat("x", maxIntakeBytes+1)

	t.Run("no help center wins over an oversized body", func(t *testing.T) {
		app := mountPublic(t, "") // fail-closed: no public org
		code, _ := anon(t, app, http.MethodPost, "/v1/help/tickets",
			map[string]any{"subject": "hi", "email": "a@b.c", "description": huge}, nil)
		if code != http.StatusNotFound {
			t.Fatalf("oversized body with no help center: want 404, got %d", code)
		}
	})

	t.Run("not configured wins over an oversized body", func(t *testing.T) {
		app := mountPublic(t, "acme") // org named, Help model never installed
		code, _ := anon(t, app, http.MethodPost, "/v1/help/tickets",
			map[string]any{"subject": "hi", "email": "a@b.c", "description": huge}, nil)
		if code != http.StatusServiceUnavailable {
			t.Fatalf("oversized body with no installed model: want 503, got %d", code)
		}
	})

	t.Run("not configured wins over a body that is valid JSON but not an object", func(t *testing.T) {
		app := mountPublic(t, "acme")
		code, _ := anonRaw(t, app, http.MethodPost, "/v1/help/tickets", []byte(`[1,2,3]`))
		if code != http.StatusServiceUnavailable {
			t.Fatalf("array body with no installed model: want 503, got %d", code)
		}
	})

	t.Run("an unreadable body is 400 once the center is live", func(t *testing.T) {
		const org = "acme"
		app := mountPublic(t, org)
		seedArticle(t, app, org, "x", "X", "Published", true) // installs the help module
		code, _ := anonRaw(t, app, http.MethodPost, "/v1/help/tickets", []byte("{not json"))
		if code != http.StatusBadRequest {
			t.Fatalf("malformed body on a live center: want 400, got %d", code)
		}
	})
}

// TestSyntacticallyInvalidJSONIs400Early is the ONE delta typing this route took,
// MEASURED rather than glossed. It is not fixable in cloud.
//
// encoding/json validates the WHOLE document before it invokes any custom
// Unmarshaler (json.Unmarshal runs checkValid first), so a body that is not valid
// JSON at all never reaches helpTicketIntake.UnmarshalJSON — zip's op.invoke
// (typed.go, `if err := dec(rawIn, &in)`) refuses it with 400 before the handler
// runs. The size-and-parse record below therefore preserves the gate order for an
// oversized body and for one that parses to the wrong SHAPE, and cannot preserve it
// for a syntax error.
//
// What moves: a caller who sends syntactically invalid bytes to a deployment with
// no help center, or to one whose Help model is not installed, now sees 400 where
// it saw 404 or 503. Nothing else — a well-formed body is answered exactly as
// before, which every other case in TestIntakeGateOrder proves. The same delta is
// live on apps/captable's typed writes (writes.go's note names it) and is the same
// shape wherever a route decides a status before it reads its body.
func TestSyntacticallyInvalidJSONIs400Early(t *testing.T) {
	app := mountPublic(t, "") // no help center at all: the untyped route answered 404
	code, _ := anonRaw(t, app, http.MethodPost, "/v1/help/tickets", []byte("{not json"))
	if code != http.StatusBadRequest {
		t.Fatalf("syntactically invalid body: want the measured 400, got %d — "+
			"if this is 404 again, zip learned to defer the decode and the prose above is stale", code)
	}
}

// TestArticleLimitGrammar pins what ?limit means, including the value zip cannot
// read as an integer — which takes the default, exactly as strconv.Atoi's error did.
func TestArticleLimitGrammar(t *testing.T) {
	for _, c := range []struct {
		in   int
		want int
	}{
		{0, defaultArticleLimit},  // absent, or a value zip could not read
		{-5, defaultArticleLimit}, // negative
		{7, 7},                    // honoured
		{10000, maxArticleLimit},  // clamped
	} {
		if got := articleLimit(c.in); got != c.want {
			t.Errorf("articleLimit(%d) = %d, want %d", c.in, got, c.want)
		}
	}
}

// TestIntakeCarriesTheSizeAndTheParse proves the input records both facts rather
// than refusing, which is what lets fileTicket keep its gate order. A decoder that
// returned an error here would move the 400 ahead of the 404 and the 503.
func TestIntakeCarriesTheSizeAndTheParse(t *testing.T) {
	var ok helpTicketIntake
	if err := ok.UnmarshalJSON([]byte(`{"subject":"s","email":"e"}`)); err != nil {
		t.Fatalf("a good body must not error: %v", err)
	}
	if ok.malformed || ok.oversize || ok.Subject != "s" {
		t.Fatalf("good body: malformed=%v oversize=%v subject=%q", ok.malformed, ok.oversize, ok.Subject)
	}

	var bad helpTicketIntake
	if err := bad.UnmarshalJSON([]byte("{not json")); err != nil {
		t.Fatalf("a malformed body must not error either: %v", err)
	}
	if !bad.malformed {
		t.Fatal("a malformed body must be RECORDED, so fileTicket can answer 400 in its own order")
	}

	var big helpTicketIntake
	if err := big.UnmarshalJSON([]byte(`{"description":"` + strings.Repeat("x", maxIntakeBytes) + `"}`)); err != nil {
		t.Fatalf("an oversized body must not error: %v", err)
	}
	if !big.oversize {
		t.Fatal("an oversized body must be RECORDED, so fileTicket can answer 413 after its two gates")
	}
}

// ── the projection gate ─────────────────────────────────────────────────────
//
// Both halves of this package's surface are MEASURED here rather than asserted in
// prose, because prose cannot go red: a route added untyped goes red without anyone
// remembering to name it, a reason naming a route this package no longer serves goes
// red too, and the two ledgers must sum to what the live router actually serves.

// untypedByDesign is the CLOSED list of operations here that are NOT typed ops, each
// with the wire fact that keeps it out. A typed op is a route PLUS a registry entry —
// the one value the OpenAPI operation, the MCP tool, the CLI command and the generated
// SDK method all come from — so an operation missing from that registry is invisible to
// all four. Addresses are written the way the DOCUMENT writes them.
var untypedByDesign = map[string]string{}

// helpOps reads BOTH projections of the live router at their one shared address form:
// what the document says is served, and which of those carry a typed registry entry.
// Reading the REAL mount, not a reconstruction of it, is what makes this a gate.
func helpOps(t *testing.T) (served map[string]bool, typed map[string]string) {
	t.Helper()
	app := mountHelpOnly(t)
	doc, err := openapi.Spec(app, openapi.Info{Title: "help", Version: "v1"})
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

// TestEveryRouteIsTypedOrNamed fails when an operation here is neither a typed op nor
// one named above — so the next route added is typed by default, and dropping one out
// of the registry takes a deliberate edit with a reason.
func TestEveryRouteIsTypedOrNamed(t *testing.T) {
	served, typed := helpOps(t)

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
			"A route that is not a typed op has no schema, no prose, no MCP tool, no CLI command and no "+
			"SDK method. Convert it (zip.Get/Post/... on the group), or add it to untypedByDesign with "+
			"the wire fact that typing it would move.", strings.Join(untyped, ", "))
	}
	for key := range untypedByDesign {
		if !served[key] {
			t.Errorf("untypedByDesign names %q, which help no longer serves", key)
		}
	}
	if got, want := len(typed)+len(untypedByDesign), len(served); got != want {
		t.Errorf("typed(%d) + named(%d) = %d, served = %d — the ledgers must partition the surface",
			len(typed), len(untypedByDesign), got, want)
	}
}

// TestEveryTypedOpIsDescribed holds the prose to the same bar as the schema, because
// that prose IS the product surface: it becomes the OpenAPI description AND the MCP tool
// description a model reads to pick the tool. zipdoc_gen.go carries it into the binary,
// so an op added without regenerating shows up here as a nameless tool.
func TestEveryTypedOpIsDescribed(t *testing.T) {
	_, typed := helpOps(t)
	if len(typed) == 0 {
		t.Fatal("no typed help ops in the registry at all")
	}
	for key, desc := range typed {
		if strings.TrimSpace(desc) == "" {
			t.Errorf("%s has no description — run: go generate -run zipdoc ./apps/help/...", key)
		}
	}
}

// TestEveryPublishedFieldIsDescribed covers the RESPONSE side the op-level gate cannot
// see. A typed op publishes its Out's whole schema, and a property that reaches
// openapi.yaml with no description reaches every generated SDK and every MCP inputSchema
// without one too.
func TestEveryPublishedFieldIsDescribed(t *testing.T) {
	app := mountHelpOnly(t)
	doc, err := openapi.Spec(app, openapi.Info{Title: "help", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	// Through JSON, because that is the artifact: only the marshalled form is what an
	// SDK generator actually reads.
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal doc: %v", err)
	}
	var published struct {
		Components struct {
			Schemas map[string]struct {
				Properties map[string]struct {
					Description string `json:"description"`
				} `json:"properties"`
			} `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal(raw, &published); err != nil {
		t.Fatalf("unmarshal doc: %v", err)
	}
	if len(published.Components.Schemas) == 0 {
		t.Fatal("no published schemas at all — a typed op must publish its In/Out")
	}
	var bare []string
	for name, schema := range published.Components.Schemas {
		for field, prop := range schema.Properties {
			if strings.TrimSpace(prop.Description) == "" {
				bare = append(bare, name+"."+field)
			}
		}
	}
	if len(bare) > 0 {
		sort.Strings(bare)
		t.Errorf("%d published schema propert(ies) with no description: %s\n"+
			"Write the field's doc comment and run: go generate -run zipdoc ./apps/help/...",
			len(bare), strings.Join(bare, ", "))
	}
}
