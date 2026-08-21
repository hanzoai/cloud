package platform

// prose_test.go gates the FIELD half of this surface. Typing a route documents its
// ADDRESS and its SHAPE; the shape's FIELDS come from a different place — a doc
// comment on each one, which zipdoc lifts one at a time — so a fully typed control
// plane can still publish a document nobody can act on.
//
// It matters here because this is the surface the fleet is DRIVEN from, and its
// fields carry distinctions the names do not. `declaredTag`, `runningTag` and
// `latestTag` are three different facts about one service — what the CR says, what
// is actually running, what has been released — and the drift verdict between them
// is a closed vocabulary (ok|yellow|red over six named kinds), not a mood; today
// `latestTag` is always empty because the release reader has not landed, so `stale`
// cannot fire at all, and only prose can say that. `health` has a FOURTH value, "",
// meaning the cluster reported no replica counts — unknown, which a reader who
// assumes three colours will render as a failure. An app's `status` is what this
// store recorded and its `phase` is what the operator says, which is why both
// exist; a deployment's `deploying` is the TERMINAL success state, not an
// in-flight one. And an env var's empty `value` is not an empty value: a sealed
// secret reads back masked, so posting "" keeps what is in KMS rather than wiping
// it — the difference between a round trip and data loss.
//
// The gate checks presence, not meaning. A description restating the field's name is
// worse than none, and only a reader catches that.

import (
	"sort"
	"strings"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/openapi"
	"github.com/zap-proto/zip"
)

// mountPublished is the WHOLE surface this app publishes, which mountApp is not:
// routes() registers the typed ops, and Mount registers one more door beside them
// — the forge's push receiver, a RAW handler because the HMAC covers the bytes and
// has to run before the decode. It publishes two shapes all the same, so a gate
// that read only the typed half would pass while they went unread.
func mountPublished(t *testing.T) *zip.App {
	t.Helper()
	app, s := mountSvcK8s(t, &k8sClient{initErr: "no cluster (test)", limits: testLimits()})
	app.Post(hookPath, cloud.Terminal(cloud.Handle(s, hook)))
	return app
}

// proseless is the CLOSED list of published properties that carry NO description
// because the SEAM they arrived through cannot carry one — not because nobody wrote
// it. Every one of them HAS a doc comment in hook.go; reflection cannot see it.
//
// It is exact in BOTH directions. A bare property anywhere else goes red, and an
// entry here that starts publishing prose goes red too — that is the day the
// generator learns, and the ledger must shrink then rather than outlive the gap.
var proseless = map[string]bool{
	// REFLECTION SEAM. POST /v1/platform/hook is declared with openapi.Register
	// (hook.go) and not as a typed op, because AUTHENTICATION IS THE SIGNATURE: the
	// HMAC covers the raw bytes and is verified BEFORE the payload is parsed, and a
	// typed op decodes first. Register derives its schema by REFLECTION, and Go
	// drops comments at compile time, so zipdoc — which walks zip's TYPED
	// registrations — can never reach a type that arrives this way.
	"push.ref":                       true,
	"push.before":                    true,
	"push.after":                     true,
	"push.repository":                true,
	"push.repository.name":           true,
	"push.repository.owner":          true,
	"push.repository.owner.login":    true,
	"push.repository.owner.username": true,
	"push.pusher":                    true,
	"push.pusher.login":              true,
	"push.pusher.username":           true,
	"verdict.org":                    true,
	"verdict.repo":                   true,
	"verdict.ref":                    true,
	"verdict.commit":                 true,
	"verdict.fired":                  true,
	"verdict.builds":                 true,
	"verdict.reason":                 true,
}

// TestEveryPublishedFieldIsDescribed fails on any property of any published schema
// that carries no description and is not named above.
func TestEveryPublishedFieldIsDescribed(t *testing.T) {
	doc, err := openapi.Spec(mountPublished(t), openapi.Info{Title: "platform", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	if doc.Components == nil || len(doc.Components.Schemas) == 0 {
		t.Fatal("platform publishes no schemas at all — the gate would pass vacuously")
	}
	if doc.Paths[hookPath]["post"] == nil {
		t.Fatalf("%s is not in the document this gate reads, so the shapes it publishes are "+
			"unchecked — mountPublished has drifted from Mount", hookPath)
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
			"the first of them alone — then run: make -C apps/platform describe",
			len(bare), strings.Join(bare, ", "))
	}
	if len(stale) > 0 {
		sort.Strings(stale)
		t.Errorf("proseless names propert(ies) that are gone or now described: %s\n"+
			"An exemption that outlives its cause is how a generator gap becomes permanent — "+
			"delete the entr(ies).", strings.Join(stale, ", "))
	}
}
