package agents

// prose_test.go gates the FIELD half of this surface. Typing a route documents its
// ADDRESS and its SHAPE; the shape's FIELDS come from a different place — a doc
// comment on each one, which zipdoc lifts one at a time — so a fully typed control
// plane can still publish a document that has to be guessed at.
//
// It matters here more than almost anywhere, because this surface is measurements
// that look alike and are not. A session `status` is a closed four (running, paused,
// done, error) whose last two are TERMINAL and monotonic, and no control command
// writes it — only the surface running the agent reports it. A `seq` on a turn is a
// POSITION in one session's log; `turns` on a build is a COUNT of them; `promptTokens`
// and `completionTokens` are neither, and cover only the run's FINAL completion, so
// reading them as a tool loop's spend undercounts it. A target's `status` is the
// EFFECTIVE liveness rather than the stored one — a heartbeat older than 90 seconds
// says offline whatever the row claims — while `load1` is a Unix load average, not a
// percentage, and means nothing until it is read against `cpus`. Half of these
// (`id`, `org`, `rootSessionId`, `metricsAt`, `seq`) are server-minted and refused
// from a caller outright.
//
// The gate checks presence, not meaning. A description restating the field's name is
// worse than none, and only a reader catches that.

import (
	"sort"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/openapi"
)

// proseless is the CLOSED list of published properties that carry NO description
// because the CLIENT they arrived through cannot carry one — not because nobody wrote
// it. Every one of them has a doc comment in the Go source; it is filed under
// another schema name.
//
// EMBEDDED STRUCT. sessionDetail embeds sessionView and agentDetail embeds agentView,
// because the detail read IS the list projection plus two fields and a second copy of
// the other twenty-six is a second thing to forget to update. zip's schema walk
// flattens an embedding, so the document publishes those fields on the detail shape
// too — but zipdoc files a field's prose under the type that DECLARES it, and an
// embedded field of an unexported type is not itself exported, so the lift never
// reaches them. The prose exists and is published, on sessionView and agentView.
//
// The two ways to make this list shorter are both worse. Unrolling the embedding into
// copies replaces one true statement with two that can drift, and the wire is what
// would drift. Hand-writing a Fields map beside the struct does the same thing to the
// prose. So the comment goes on the embedded struct's own fields, and this records
// what the generator cannot reach.
//
// It is exact in BOTH directions. A bare property anywhere else goes red, and an
// entry here that starts publishing prose goes red too — that is the day zipdoc
// learns to follow an embedding, and this ledger must shrink then rather than outlive
// the gap.
var proseless = map[string]bool{
	// sessionView, promoted into sessionDetail.
	"sessionDetail.account":         true,
	"sessionDetail.actor":           true,
	"sessionDetail.agent":           true,
	"sessionDetail.children":        true,
	"sessionDetail.createdAt":       true,
	"sessionDetail.cwd":             true,
	"sessionDetail.endedAt":         true,
	"sessionDetail.events":          true,
	"sessionDetail.host":            true,
	"sessionDetail.id":              true,
	"sessionDetail.lastEvent":       true,
	"sessionDetail.org":             true,
	"sessionDetail.parentSessionId": true,
	"sessionDetail.progress":        true,
	"sessionDetail.project":         true,
	"sessionDetail.provider":        true,
	"sessionDetail.published":       true,
	"sessionDetail.repo":            true,
	"sessionDetail.room":            true,
	"sessionDetail.rootSessionId":   true,
	"sessionDetail.startedAt":       true,
	"sessionDetail.status":          true,
	"sessionDetail.target":          true,
	"sessionDetail.taskRunId":       true,
	"sessionDetail.taskWorkflowId":  true,
	"sessionDetail.terminal":        true,
	"sessionDetail.title":           true,
	"sessionDetail.updatedAt":       true,

	// agentView, promoted into agentDetail.
	"agentDetail.computeRef":       true,
	"agentDetail.createdAt":        true,
	"agentDetail.description":      true,
	"agentDetail.executionMode":    true,
	"agentDetail.id":               true,
	"agentDetail.model":            true,
	"agentDetail.name":             true,
	"agentDetail.runs":             true,
	"agentDetail.schedule":         true,
	"agentDetail.serviceAccountId": true,
	"agentDetail.status":           true,
	"agentDetail.tools":            true,
	"agentDetail.updatedAt":        true,
}

// TestEveryPublishedFieldIsDescribed fails on any property of any published schema
// that carries no description and is not named above.
func TestEveryPublishedFieldIsDescribed(t *testing.T) {
	doc, err := openapi.Spec(mountApp(t, &fakeAI{content: "x"}), openapi.Info{Title: "agents", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	if doc.Components == nil || len(doc.Components.Schemas) == 0 {
		t.Fatal("agents publishes no schemas at all — the gate would pass vacuously")
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
			"the first of them alone — then run: make -C apps/agents describe",
			len(bare), strings.Join(bare, ", "))
	}
	if len(stale) > 0 {
		sort.Strings(stale)
		t.Errorf("proseless names propert(ies) that are gone or now described: %s\n"+
			"An exemption that outlives its cause is how a generator gap becomes permanent — "+
			"delete the entr(ies).", strings.Join(stale, ", "))
	}
}
