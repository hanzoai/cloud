package blueprint

import (
	"encoding/json"
	"sort"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/openapi"
)

// untypedByDesign is the CLOSED list of blueprint operations that are NOT typed
// ops, each with the wire fact that keeps it out. A typed op is a route PLUS a
// registry entry — the one value the OpenAPI operation, the MCP tool, the CLI
// command and the SDK method all come from — so an operation missing from that
// registry is invisible to all four. This one is missing on purpose. Addresses
// are written the way the DOCUMENT writes them, which is the identity every
// projection keys on.
var untypedByDesign = map[string]string{
	"GET /v1/blueprint/sbom": "ONE address, TWO 200 shapes: `?template=<id>` returns a bare Estimate, " +
		"no template returns the {data:[Estimate]} batch (blueprint.go sbomRead). An op declares exactly " +
		"one Out, so either shape would publish the other as a lie. Convertible when zip can declare a " +
		"polymorphic response.",
}

// blueprintOpsUnderTest reads BOTH projections of the live router at their one
// shared address form: what the document says is served, and which of those carry
// a typed registry entry. Reading the router (not the source) is what makes this a
// gate rather than prose — a route added anywhere in routes() shows up here.
func blueprintOpsUnderTest(t *testing.T) (served map[string]bool, typed map[string]string, schemas map[string]any) {
	t.Helper()
	app := mountApp(t)
	doc, err := openapi.Spec(app, openapi.Info{Title: "blueprint", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	reg, err := openapi.Typed(app)
	if err != nil {
		t.Fatalf("typed registry: %v", err)
	}
	ours := func(p string) bool { return strings.HasPrefix(p, "/v1/blueprint") }
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
	return served, typed, reg.Schemas
}

// TestEveryRouteIsTypedOrNamed fails when a blueprint operation is neither a typed
// op nor the one named above — so the next route added here is typed by default,
// and dropping one out of the registry takes a deliberate edit with a reason.
func TestEveryRouteIsTypedOrNamed(t *testing.T) {
	served, typed, _ := blueprintOpsUnderTest(t)

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
	for key := range untypedByDesign {
		if !served[key] {
			t.Errorf("untypedByDesign names %q, which blueprint no longer serves", key)
		}
	}
}

// TestEveryTypedOpIsDescribed holds the prose to the same bar as the schema,
// because that prose IS the product surface: it becomes the OpenAPI description
// AND the MCP tool description a model reads to pick the tool. zipdoc_gen.go is
// what carries it into the binary, so an op added without regenerating shows up
// here as a nameless tool.
func TestEveryTypedOpIsDescribed(t *testing.T) {
	_, typed, _ := blueprintOpsUnderTest(t)
	if len(typed) == 0 {
		t.Fatal("no typed blueprint ops in the registry at all")
	}
	for key, desc := range typed {
		if strings.TrimSpace(desc) == "" {
			t.Errorf("%s has no description — run: go generate -run zipdoc ./apps/blueprint/...", key)
		}
	}
}

// TestEveryPublishedFieldIsDescribed closes the half of the surface the op-level
// gate cannot see: typing a route documents its ADDRESS and its SHAPE, never the
// shape's FIELDS, which come from doc comments zipdoc lifts per field.
func TestEveryPublishedFieldIsDescribed(t *testing.T) {
	_, _, schemas := blueprintOpsUnderTest(t)
	if len(schemas) == 0 {
		t.Fatal("no blueprint schemas in the typed registry at all")
	}
	var bare []string
	for name, raw := range schemas {
		sch, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		props, ok := sch["properties"].(map[string]any)
		if !ok {
			continue
		}
		for field, praw := range props {
			p, ok := praw.(map[string]any)
			if !ok {
				continue
			}
			if desc, _ := p["description"].(string); strings.TrimSpace(desc) == "" {
				bare = append(bare, name+"."+field)
			}
		}
	}
	if len(bare) > 0 {
		sort.Strings(bare)
		t.Errorf("published propert(ies) with no description: %s\n"+
			"Write a doc comment on the struct field and run: go generate -run zipdoc ./apps/blueprint/...",
			strings.Join(bare, ", "))
	}
}

// TestTypedOpsKeepTheirBytes pins the two converted reads at the BYTE level. Both
// used to marshal a map[string]any, whose keys Go emits in sorted order; a struct
// emits them in declaration order. Declaring the fields in that same sorted order
// is what kept the wire byte-identical, and this is the assertion that keeps it so
// — a field inserted in the "natural" place goes red here rather than silently
// reordering a shipped body.
func TestTypedOpsKeepTheirBytes(t *testing.T) {
	app := mountApp(t)

	code, body := get(t, app, "/v1/blueprint/health")
	if code != 200 {
		t.Fatalf("health: %d", code)
	}
	if err := sameKeyOrder(body, []string{"blueprints", "rateCard", "service", "status"}); err != nil {
		t.Errorf("health: %v", err)
	}

	code, body = get(t, app, "/v1/blueprint")
	if code != 200 {
		t.Fatalf("index: %d", code)
	}
	if err := sameKeyOrder(body, []string{"data"}); err != nil {
		t.Errorf("index: %v", err)
	}
	var index struct {
		Data []json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &index); err != nil {
		t.Fatalf("index decode: %v", err)
	}
	if len(index.Data) == 0 {
		t.Fatal("index is empty")
	}
	if err := sameKeyOrder(index.Data[0], []string{"templateId", "services", "estCentsPerMonth"}); err != nil {
		t.Errorf("index row: %v", err)
	}
}

// sameKeyOrder reports whether obj's top-level keys appear in exactly want's
// order. json.Decoder.Token walks the object in WIRE order, which is the fact
// under test — json.Unmarshal into a map would sort it away.
func sameKeyOrder(obj []byte, want []string) error {
	dec := json.NewDecoder(strings.NewReader(string(obj)))
	if _, err := dec.Token(); err != nil { // opening {
		return err
	}
	var got []string
	depth := 0
	for dec.More() || depth > 0 {
		tok, err := dec.Token()
		if err != nil {
			return err
		}
		switch v := tok.(type) {
		case json.Delim:
			if v == '{' || v == '[' {
				depth++
			} else {
				depth--
			}
			continue
		case string:
			if depth == 0 {
				got = append(got, v)
				// Skip the value, whatever it is.
				var skip json.RawMessage
				if err := dec.Decode(&skip); err != nil {
					return err
				}
			}
		}
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		return errKeyOrder(got, want)
	}
	return nil
}

type keyOrderErr struct{ got, want []string }

func (e keyOrderErr) Error() string {
	return "key order " + strings.Join(e.got, ",") + ", want " + strings.Join(e.want, ",")
}

func errKeyOrder(got, want []string) error { return keyOrderErr{got, want} }
