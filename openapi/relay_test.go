package openapi_test

// What a door owes, tested as refusals.
//
// A relay is the one seam in this package that can ADD an operation, so it is the
// one that has to be hardest to lie with. Everything below is a case where the
// honest answer is "no": a registry that answered with nothing, a registry that
// answered about somebody else's prefix, two registries that mean different things
// by one name. The silent version of each is a smaller or wronger published
// contract, which reads exactly like a smaller or different API.

import (
	"errors"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/openapi"
)

// door is a document whose whole /v1/thing surface is one wildcard — what the
// router honestly reports for `app.All("/v1/thing/*")`.
func door(prefix string) *openapi.Document {
	return &openapi.Document{
		Paths: map[string]openapi.PathItem{
			prefix + "/{wildcard1}": {
				"get":  {OperationID: "get_door"},
				"post": {OperationID: "post_door"},
			},
		},
	}
}

func behind(paths ...string) func() (*openapi.Document, error) {
	return func() (*openapi.Document, error) {
		d := &openapi.Document{Paths: map[string]openapi.PathItem{}}
		for _, p := range paths {
			d.Paths[p] = openapi.PathItem{"get": {OperationID: "get" + strings.ReplaceAll(p, "/", "_"), Tags: []string{"thing"}}}
		}
		return d, nil
	}
}

func TestProjectReplacesTheDoorWithWhatIsBehindIt(t *testing.T) {
	doc := door("/v1/thing")
	err := openapi.Project(doc, []openapi.Relay{{
		Source: "github.com/hanzoai/thing", Prefix: "/v1/thing",
		Behind: behind("/v1/thing/a", "/v1/thing/b"),
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, still := doc.Paths["/v1/thing/{wildcard1}"]; still {
		t.Error("the door survived its own registry — one address published twice, and {wildcard1} is what a spec-derived CLI turns into a phantom command")
	}
	if len(doc.Paths) != 2 {
		t.Fatalf("paths = %d, want 2", len(doc.Paths))
	}
	if got := doc.Paths["/v1/thing/a"]["get"].App; got != "github.com/hanzoai/thing" {
		t.Errorf("x-app = %q, want the module behind the door — an operation nobody can trace to a repo is one nobody can file against", got)
	}
	if len(doc.Tags) != 1 || doc.Tags[0].Name != "thing" {
		t.Errorf("tags = %+v, want exactly the products the operations carry", doc.Tags)
	}
}

// THE SHRINK. A registry that could not answer must not degrade to the bare
// wildcard: that publishes one path where a whole API is, and nothing downstream
// can tell it from an API with one path.
func TestProjectRefusesARelayThatPublishesNothing(t *testing.T) {
	empty := func() (*openapi.Document, error) {
		return &openapi.Document{Paths: map[string]openapi.PathItem{}}, nil
	}
	err := openapi.Project(door("/v1/thing"), []openapi.Relay{{
		Source: "github.com/hanzoai/thing", Prefix: "/v1/thing", Behind: empty,
	}})
	if err == nil {
		t.Fatal("Project accepted a door with nothing behind it — a silently smaller document is the failure this exists to prevent")
	}
	if !strings.Contains(err.Error(), "github.com/hanzoai/thing") {
		t.Errorf("error does not name the source: %v", err)
	}
}

// UNREACHABLE. The registry itself failed; say so, loudly, instead of publishing
// what the router could see on its own.
func TestProjectRefusesARelayThatCouldNotDescribeItself(t *testing.T) {
	boom := errors.New("dial: connection refused")
	err := openapi.Project(door("/v1/thing"), []openapi.Relay{{
		Source: "github.com/hanzoai/thing", Prefix: "/v1/thing",
		Behind: func() (*openapi.Document, error) { return nil, boom },
	}})
	if !errors.Is(err, boom) {
		t.Fatalf("Project = %v, want the registry's own error — a door that cannot say what is behind it is not a door with nothing behind it", err)
	}
}

// PLACEMENT, which is why x-app exists at all: an operation published through the
// wrong door is unreachable there, so it is a routing bug in the registry that
// registered it — and the message has to name that registry.
func TestProjectRefusesAnOperationOutsideItsOwnDoor(t *testing.T) {
	err := openapi.Project(door("/v1/thing"), []openapi.Relay{{
		Source: "github.com/hanzoai/thing", Prefix: "/v1/thing",
		Behind: behind("/v1/thing/a", "/v1/billing/charge"),
	}})
	if err == nil {
		t.Fatal("Project published /v1/billing/charge through the /v1/thing door")
	}
	for _, want := range []string{"github.com/hanzoai/thing", "/v1/billing/charge", "/v1/thing"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not name %q: %v", want, err)
		}
	}
}

// THE SAME noun gate the compose uses, on the same terms: one schema name, one
// shape, whether the two claimants are two apps or an app and the registry behind
// its door.
func TestProjectRefusesOneSchemaNameWithTwoShapes(t *testing.T) {
	doc := door("/v1/thing")
	doc.Components = &openapi.Components{Schemas: map[string]any{"None": map[string]any{"type": "object"}}}
	err := openapi.Project(doc, []openapi.Relay{{
		Source: "github.com/hanzoai/thing", Prefix: "/v1/thing",
		Behind: func() (*openapi.Document, error) {
			d, _ := behind("/v1/thing/a")()
			d.Components = &openapi.Components{Schemas: map[string]any{"None": map[string]any{"type": "string"}}}
			return d, nil
		},
	}})
	var c *openapi.Conflict
	if !errors.As(err, &c) {
		t.Fatalf("Project = %v, want a *Conflict — every generated SDK would bind whichever shape the merge read last", err)
	}
	if c.Kind != "schema" || c.Name != "None" {
		t.Errorf("Conflict = %+v, want schema None", c)
	}
}

// The router keeps its authority. A specific path registered in front of the door
// is the one the matcher picks, so the relay's operation there would name a
// handler no request reaches.
func TestProjectLeavesTheHostsOwnRouteAlone(t *testing.T) {
	doc := door("/v1/thing")
	doc.Paths["/v1/thing/a"] = openapi.PathItem{"get": {OperationID: "hostsOwn"}}
	if err := openapi.Project(doc, []openapi.Relay{{
		Source: "github.com/hanzoai/thing", Prefix: "/v1/thing",
		Behind: behind("/v1/thing/a", "/v1/thing/b"),
	}}); err != nil {
		t.Fatal(err)
	}
	if got := doc.Paths["/v1/thing/a"]["get"].OperationID; got != "hostsOwn" {
		t.Errorf("operationId = %q, want the host's own — the relay overwrote a route it does not answer", got)
	}
	if doc.Paths["/v1/thing/a"]["get"].App != "" {
		t.Error("the host's own operation was stamped with the relay's source")
	}
}

// A relay with no door does not apply — the same law Register and Describe obey,
// and what makes one binary per app work: the relay registry is process-wide and a
// describe run mounts one subsystem.
func TestRelayWithNoDoorDoesNotApply(t *testing.T) {
	doc := &openapi.Document{Paths: map[string]openapi.PathItem{"/v1/other": {"get": {OperationID: "x"}}}}
	if err := openapi.Project(doc, []openapi.Relay{{
		Source: "github.com/hanzoai/thing", Prefix: "/v1/thing",
		Behind: func() (*openapi.Document, error) {
			t.Fatal("asked a registry about a door this process does not have")
			return nil, nil
		},
	}}); err != nil {
		t.Fatal(err)
	}
	if len(doc.Paths) != 1 {
		t.Errorf("paths = %d, want the document untouched", len(doc.Paths))
	}
}

// A route table's "*" means the registry dispatches every method at that address,
// which is the same expansion the door itself gets — so the two halves of one
// wildcard cannot disagree about which verbs exist.
func TestTableExpandsTheAnyMethodToTheOnesThisGeneratorPublishes(t *testing.T) {
	r := openapi.Table("github.com/hanzoai/ai", "/v1", func() map[string][]string {
		return map[string][]string{"/v1/router/policy": {"*"}, "/v1/models/:model": {"GET", "CONNECT"}}
	}, func() map[string]openapi.Said {
		// One handler answers every verb at a "*" address, so one sentence is what
		// the registry has to say about all of them and the key stays the star.
		return map[string]openapi.Said{
			"* /v1/router/policy":    {Summary: "Read or write the org's routing policy"},
			"GET /v1/models/{model}": {Summary: "Retrieve one model"},
		}
	})
	doc, err := r.Behind()
	if err != nil {
		t.Fatal(err)
	}
	if got, want := len(doc.Paths["/v1/router/policy"]), len(openapi.Methods()); got != want {
		t.Errorf("* expanded to %d methods, want %d", got, want)
	}
	for method, op := range doc.Paths["/v1/router/policy"] {
		if op.Summary != "Read or write the org's routing policy" {
			t.Errorf("%s carries %q — every verb of a star address is the same handler and says the same thing",
				method, op.Summary)
		}
	}
	// CONNECT has no OpenAPI Path Item field; the projection drops it rather than
	// inventing a name for it.
	if got := len(doc.Paths["/v1/models/{model}"]); got != 1 {
		t.Errorf("methods = %d, want 1 (CONNECT is not representable)", got)
	}
	if doc.Paths["/v1/models/{model}"]["get"].Tags[0] != "models" {
		t.Error("a relayed operation must carry the product its path names")
	}
}

// A REGISTRY THAT SAYS NOTHING ABOUT A ROUTE IS REFUSED, exactly as an app's own
// undescribed operation is (openapi/prose.go). A door is where another repo's
// surface enters this document, so it is where that repo's silence has to be
// caught — downstream every projection has an address and no sentence, and none of
// them can write one.
func TestTableRefusesARouteTheRegistrySaysNothingAbout(t *testing.T) {
	r := openapi.Table("github.com/hanzoai/ai", "/v1", func() map[string][]string {
		return map[string][]string{"/v1/chat/completions": {"POST"}}
	}, func() map[string]openapi.Said { return nil })
	_, err := r.Behind()
	if err == nil {
		t.Fatal("Behind published an operation with nothing to say")
	}
	if !strings.Contains(err.Error(), "POST /v1/chat/completions") {
		t.Errorf("the refusal must name the operation: %v", err)
	}
}

// Two relays at one door is two answers to one question, and it is a programming
// error at wire time — the same shape Register and Describe refuse.
func TestFrontRefusesTwoRelaysAtOneDoor(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("Front accepted a second relay for one prefix")
		}
	}()
	r := openapi.Relay{Source: "a", Prefix: "/v1/one-door-test", Behind: behind("/v1/one-door-test/x")}
	openapi.Front(r)
	openapi.Front(openapi.Relay{Source: "b", Prefix: "/v1/one-door-test", Behind: r.Behind})
}
