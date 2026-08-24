// typed_wire_test.go — the projection gate, over the WHOLE capability. Three of
// this package's operations are TYPED ops; the rest are not, and each of those has
// a WIRE FACT that keeps it out. Both halves are MEASURED here rather than asserted
// in prose, because prose cannot go red: a route added untyped goes red without
// anyone remembering to name it, a reason naming a route this package no longer
// serves goes red too, and the two ledgers must sum to the surface the live router
// actually serves.
//
// ONE gate over the WHOLE capability, which is what makes it a gate: the run
// plane and the relay each measuring half a surface is how a route lands in the
// gap between two ledgers and is counted by neither. The MACHINE plane is not
// half of this one — it is a different capability with a gate of its own
// (apps/nodes), which is the same argument, applied to the right boundary.
package bot

import (
	"encoding/json"
	"sort"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/openapi"
)

// untypedByDesign is the CLOSED list of bot operations that are NOT typed ops, each
// with the wire fact that keeps it out. A typed op is a route PLUS a registry entry
// — the one value the OpenAPI operation, the MCP tool, the CLI command and the
// generated SDK method all come from — so an operation missing from that registry is
// invisible to all four. These are missing on purpose: every one would answer a
// DIFFERENT wire as a typed op, and typing is a description task. Addresses are
// written the way the DOCUMENT writes them, which is the identity every projection
// keys on.
//
// THE LAUNCH IS NO LONGER HERE, and the reason it left is the reason this list is
// a test. It was refused on two facts and ONE OF THEM HAD EXPIRED: "a typed op
// publishes a SUCCESS response it can never send" was true at zip v1.18.x and is
// false at the pin — WithStatus takes any code in 100–599 (typed.go:154-176) and
// the document keys its responses on the declared set (openapi.go:222-252), so
// an operation whose only answer is 501 declares exactly that. The surviving fact
// (body tolerance) was a self-imposed tolerance rather than one of the wire
// mechanisms this ledger exists for, and the delta it cost is measured in
// run_wire_test.go rather than glossed.
var untypedByDesign = map[string]string{
	// The relay face. All five ARE one registration —
	// app.All("/v1/bot/runtime/*", s.proxy) in relay.go — so they share one reason.
	//
	// FIVE, not seven: the document publishes what was DECLARED, and OPTIONS and
	// TRACE were never declared — they were methods the router happened to bind
	// under All(). They left the document in ceff43ac, which is the change that
	// drew that line, so they leave the ledger with it.
	"DELETE /v1/bot/runtime/{wildcard1}": reasonProxy,
	"GET /v1/bot/runtime/{wildcard1}":    reasonProxy,
	"PATCH /v1/bot/runtime/{wildcard1}":  reasonProxy,
	"POST /v1/bot/runtime/{wildcard1}":   reasonProxy,
	"PUT /v1/bot/runtime/{wildcard1}":    reasonProxy,
}

// botOps reads BOTH projections of the live router at their one shared address form:
// what the document says is served, and which of those carry a typed registry entry.
// Reading the router — the REAL routes(), not a reconstruction of it — is what makes
// this a gate rather than prose.
func botOps(t *testing.T) (served map[string]bool, typed map[string]string) {
	t.Helper()
	app := mountAll(t)
	doc, err := openapi.Spec(app, openapi.Info{Title: "bot", Version: "v1"})
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

// TestEveryRouteIsTypedOrNamed fails when a bot operation is neither a typed op nor
// one named above — so the next route added here is typed by default, and dropping
// one out of the registry takes a deliberate edit with a reason.
func TestEveryRouteIsTypedOrNamed(t *testing.T) {
	served, typed := botOps(t)

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
			t.Errorf("untypedByDesign names %q, which bot no longer serves", key)
		}
	}
	// The two ledgers must SUM to the served surface: neither may quietly shrink.
	if got, want := len(typed)+len(untypedByDesign), len(served); got != want {
		t.Errorf("typed(%d) + named(%d) = %d, served = %d — the ledgers must partition the surface",
			len(typed), len(untypedByDesign), got, want)
	}
}

// TestEveryTypedOpIsDescribed holds the prose to the same bar as the schema, because
// that prose IS the product surface: it becomes the OpenAPI description AND the MCP
// tool description a model reads to pick the tool. zipdoc_gen.go is what carries it
// into the binary, so an op added without regenerating shows up here as a nameless
// tool.
func TestEveryTypedOpIsDescribed(t *testing.T) {
	_, typed := botOps(t)
	if len(typed) == 0 {
		t.Fatal("no typed bot ops in the registry at all")
	}
	for key, desc := range typed {
		if strings.TrimSpace(desc) == "" {
			t.Errorf("%s has no description — run: go generate -run zipdoc ./apps/bot/...", key)
		}
	}
}

// published is the MARSHALLED document — the artifact, not the Go value. Walking
// the value would silently skip whichever half of an open-typed field it did not
// expect, and what a reader of openapi.yaml, a generated SDK or an MCP inputSchema
// actually sees is these bytes.
type published struct {
	Paths map[string]map[string]struct {
		Parameters []struct {
			Name        string `json:"name"`
			In          string `json:"in"`
			Description string `json:"description"`
		} `json:"parameters"`
	} `json:"paths"`
	Components struct {
		Schemas map[string]struct {
			Properties map[string]struct {
				Description string `json:"description"`
			} `json:"properties"`
		} `json:"schemas"`
	} `json:"components"`
}

func publishedDoc(t *testing.T) published {
	t.Helper()
	doc, err := openapi.Spec(mountAll(t), openapi.Info{Title: "bot", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal doc: %v", err)
	}
	var out published
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal doc: %v", err)
	}
	return out
}

// TestEveryPublishedFieldIsDescribed covers the RESPONSE side the op-level gate
// cannot see. A typed op publishes its Out's whole schema, and a property that
// reaches openapi.yaml with no description reaches every generated SDK and every MCP
// inputSchema without one too — `startedAt` as a bare string nowhere documented as
// RFC 3339 stamped by the runtime rather than by cloud.
func TestEveryPublishedFieldIsDescribed(t *testing.T) {
	doc := publishedDoc(t)
	if len(doc.Components.Schemas) == 0 {
		t.Fatal("no published schemas at all — a typed op must publish its In/Out")
	}
	var bare []string
	for name, schema := range doc.Components.Schemas {
		for field, prop := range schema.Properties {
			if strings.TrimSpace(prop.Description) == "" {
				bare = append(bare, name+"."+field)
			}
		}
	}
	if len(bare) > 0 {
		sort.Strings(bare)
		t.Errorf("%d published schema propert(ies) with no description: %s\n"+
			"Write the field's doc comment and run: go generate -run zipdoc ./apps/bot/...",
			len(bare), strings.Join(bare, ", "))
	}
}

// proselessParams names every published PARAMETER that structurally cannot carry
// prose, with the reason. It may only SHRINK: a parameter that starts publishing a
// description goes red here, so the day the generator learns this ledger empties
// instead of outliving the gap.
//
// A parameter is the half of the published surface a components.schemas walk cannot
// see, which is why it needs its own list: a schema property and a path parameter
// are both "a field a caller has to fill in", and only one of them was gated. All
// five entries are one address — the relay's greedy wildcard — and the reason is
// the wildcard itself: fiber captures the whole remaining sub-path positionally and
// cloud's router reading names the segment `{wildcard1}`, so there is no Go field
// declaring it and therefore nothing a doc comment can be written on.
var proselessParams = map[string]string{
	"DELETE /v1/bot/runtime/{wildcard1} :: path/wildcard1": reasonWildcardParam,
	"GET /v1/bot/runtime/{wildcard1} :: path/wildcard1":    reasonWildcardParam,
	"PATCH /v1/bot/runtime/{wildcard1} :: path/wildcard1":  reasonWildcardParam,
	"POST /v1/bot/runtime/{wildcard1} :: path/wildcard1":   reasonWildcardParam,
	"PUT /v1/bot/runtime/{wildcard1} :: path/wildcard1":    reasonWildcardParam,
}

const reasonWildcardParam = "a greedy wildcard is a POSITIONAL capture, not a declared field: fiber binds " +
	"it as `*1` and cloud's router reading publishes it as {wildcard1}, so no Go struct field declares it " +
	"and there is no doc comment for zipdoc to lift. It goes away when the address does, not before."

// TestEveryPublishedParameterIsDescribed is the gate that closes the other half of
// the published surface.
//
// The one entry that LEFT this list is the reason it is written both ways. `runId`
// on the stop op published bare because the field carried `json:"-" url:"runId"`,
// and zipdoc skips a field whose json name is "-" (internal/zipdoc/extract.go:669),
// so the description zip looks up as `stopBotIn.runId` (openapi.go:162-167) was
// never emitted. The same tag made the op UNCALLABLE as an MCP tool — a tools/call
// reaches op.invoke with a nil path map (mcp.go:654) and zip's schema builder skips
// the field (openapi.go:719-722), so the tool published an empty input schema and
// could not name the run to stop. Both were one tag.
func TestEveryPublishedParameterIsDescribed(t *testing.T) {
	doc := publishedDoc(t)
	seen := map[string]bool{}
	var bare []string
	for path, item := range doc.Paths {
		for method, op := range item {
			for _, p := range op.Parameters {
				key := strings.ToUpper(method) + " " + path + " :: " + p.In + "/" + p.Name
				described := strings.TrimSpace(p.Description) != ""
				if _, named := proselessParams[key]; named {
					seen[key] = true
					if described {
						t.Errorf("proselessParams names %q, which now publishes prose — delete the entry", key)
					}
					continue
				}
				if !described {
					bare = append(bare, key)
				}
			}
		}
	}
	if len(bare) > 0 {
		sort.Strings(bare)
		t.Errorf("%d published parameter(s) with no description: %s\n"+
			"A parameter's prose is the doc comment on the In field it binds to, lifted by zipdoc under "+
			"<Type>.<jsonName> — so the field needs a `json:` name zipdoc can key on AND a comment. "+
			"Write it and run: go generate -run zipdoc ./apps/bot/...", len(bare), strings.Join(bare, ", "))
	}
	for key := range proselessParams {
		if !seen[key] {
			t.Errorf("proselessParams names %q, which bot no longer publishes", key)
		}
	}
}
