package openapi_test

// The stage, on the document (HIP-0139 §8).
//
// Two claims, and only the second one is about publishing. The first is that
// x-stage reaches every operation the app serves, because the internal document
// is what a flagged-in customer and every operator tool read, and a console that
// wants to mark a surface "beta" has nowhere else to get the word. The second is
// that a beta operation is not in the public contract, which is what keeps it out
// of the eight generated SDKs, the CLI's command tree and the agent MCP server's tool
// list — three projections that never ask about stages and do not have to,
// because each reads x-public.
//
// They are asserted through Compose over parts rather than over the fleet's real
// subsets, for the reason fleet_test.go states: a test on a document that is
// already correct cannot show what happens when an input changes.

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/manifest"
	"github.com/hanzoai/cloud/openapi"
)

// part is one app's contribution at a stage: a single typed-looking operation at
// the app's own address, which is all the audience rule reads.
func part(app, stage string) openapi.Part {
	return openapi.Part{App: app, Stage: stage, Doc: &openapi.Document{
		OpenAPI: "3.1.0",
		Paths: map[string]openapi.PathItem{
			"/v1/" + app: {"get": {OperationID: "get_v1_" + app}},
		},
	}}
}

func composed(t *testing.T, parts ...openapi.Part) *openapi.Document {
	t.Helper()
	d, err := openapi.Compose(parts)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// The stage reaches the operation, from the part, and ga says nothing at all.
func TestComposeStampsTheStage(t *testing.T) {
	d := composed(t, part("ad", manifest.Beta), part("iam", ""))

	if got := d.Paths["/v1/ad"]["get"].Stage; got != manifest.Beta {
		t.Errorf("x-stage = %q, want %q — the compose is where an operation learns whose app it is", got, manifest.Beta)
	}
	if got := d.Paths["/v1/iam"]["get"].Stage; got != "" {
		t.Errorf("x-stage = %q on a ga operation, want absent — ga is the absence of the word", got)
	}

	// Absent, not empty: an `x-stage: ""` on 2,000 ga operations is noise every
	// consumer has to know to ignore, and a second spelling of the default.
	raw, err := json.Marshal(d.Paths["/v1/iam"]["get"])
	if err != nil {
		t.Fatal(err)
	}
	if bytes := string(raw); strings.Contains(bytes, "x-stage") {
		t.Errorf("a ga operation serialised x-stage: %s", bytes)
	}
}

// A beta capability is in the internal document and in no client.
func TestBetaIsNotPublic(t *testing.T) {
	d := composed(t, part("ad", manifest.Beta), part("iam", ""))

	if !d.Paths["/v1/iam"]["get"].Public {
		t.Fatal("a ga operation under /v1 is not public — the rule broke on something other than the stage")
	}
	if d.Paths["/v1/ad"]["get"].Public {
		t.Error("a beta operation is public: it would be in every generated SDK, the CLI and the tool list")
	}

	pub, err := openapi.Publish(d)
	if err != nil {
		t.Fatal(err)
	}
	if _, published := pub.Paths["/v1/ad"]; published {
		t.Error("the public contract carries a beta address")
	}
	if _, published := pub.Paths["/v1/iam"]; !published {
		t.Error("the public contract dropped a ga address")
	}
	// And the tag goes with it. A product named in the tag list with no operation
	// under it is a section every documentation site renders empty.
	for _, tag := range pub.Tags {
		if tag.Name == "ad" {
			t.Error("the public contract names the beta capability in its tag list")
		}
	}
}

// alpha is the same answer as beta. It exists because a capability that is not
// yet ready for the customers who asked for it is a different product fact from
// one that is, and both are refused the same way.
func TestAlphaIsNotPublicEither(t *testing.T) {
	d := composed(t, part("world", manifest.Alpha), part("iam", ""))
	if d.Paths["/v1/world"]["get"].Public {
		t.Error("an alpha operation is public")
	}
}

// Promotion is one edit to the row and nothing else: the same parts at ga
// publish.
func TestPromotionIsTheOnlyDifference(t *testing.T) {
	d := composed(t, part("ad", ""))
	if !d.Paths["/v1/ad"]["get"].Public {
		t.Error("the same operation at ga is not public — something besides the stage is deciding")
	}
}
