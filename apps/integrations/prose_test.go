package integrations

import (
	"sort"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/openapi"
)

// TestEveryPublishedFieldIsDescribed closes the half of the surface an op-level
// gate cannot see. Typing a route documents its ADDRESS and its SHAPE; the
// shape's FIELDS come from a different place — a doc comment on each one, which
// zipdoc lifts one at a time.
//
// It matters here because this surface is mostly SOMEBODY ELSE'S vocabulary
// relayed through ours, and a relayed word means what the upstream meant by it,
// not what it looks like. A search hit's `stars` is GitHub's stargazer count when
// the index answered, so it lags the repository; `private` is false for every hit
// because the index queried is the public one; `count` is the length of the array
// returned and NOT GitHub's total_count, so it never says how many more matched.
// And a hit's `full_name` is not a forkable name: githubFork takes a repository
// the org's installation was granted, which a public search result usually is not.
//
// Presence is all a gate can check. A description restating the field's name is
// worse than none, and only a reader catches that.
// proseless is the CLOSED list of published properties that carry NO description
// because the CLIENT they arrived through cannot carry one — not because nobody
// wrote it. Every one of them HAS a doc comment in forge_webhook.go; reflection
// cannot see it.
//
// It is exact in BOTH directions. A bare property anywhere else goes red, and an
// entry here that starts publishing prose goes red too — that is the day the
// generator learns, and the ledger must shrink then rather than outlive the gap.
var proseless = map[string]bool{
	// REFLECTION CLIENT. POST /v1/integration/forge/webhook is declared with
	// openapi.Register (forge_webhook.go) and not as a typed op, because
	// AUTHENTICATION IS THE SIGNATURE: the HMAC covers the raw bytes and is verified
	// BEFORE the payload is parsed, and a typed op decodes first. Register derives
	// its schema by REFLECTION, and Go drops comments at compile time, so zipdoc —
	// which walks zip's TYPED registrations — can never reach a type that arrives
	// this way.
	"forgeJob.action":                    true,
	"forgeJob.workflow_job":              true,
	"forgeJob.workflow_job.id":           true,
	"forgeJob.workflow_job.run_id":       true,
	"forgeJob.workflow_job.name":         true,
	"forgeJob.workflow_job.labels":       true,
	"forgeJob.repository":                true,
	"forgeJob.repository.name":           true,
	"forgeJob.repository.owner":          true,
	"forgeJob.repository.owner.login":    true,
	"forgeJob.repository.owner.username": true,
	"forgeLaunched.org":                  true,
	"forgeLaunched.repo":                 true,
	"forgeLaunched.job":                  true,
	"forgeLaunched.runner":               true,
}

func TestEveryPublishedFieldIsDescribed(t *testing.T) {
	doc, err := openapi.Spec(newApp(t, newKMS(t)), openapi.Info{Title: "integrations", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	if doc.Components == nil || len(doc.Components.Schemas) == 0 {
		t.Fatal("integrations publishes no schemas at all — the gate would pass vacuously")
	}
	bare, err := openapi.Bare(doc)
	if err != nil {
		t.Fatalf("bare: %v", err)
	}
	var stale, missing []string
	seen := map[string]bool{}
	for _, path := range bare {
		seen[path] = true
		if !proseless[path] {
			missing = append(missing, path)
		}
	}
	for path := range proseless {
		if !seen[path] {
			stale = append(stale, path)
		}
	}
	if len(missing) > 0 {
		t.Errorf("%d published schema propert(ies) with no description: %s\n"+
			"Write the field's OWN doc comment — a header above a group of fields is lifted onto "+
			"the first of them alone — then run: make -C apps/integrations describe",
			len(missing), strings.Join(missing, ", "))
	}
	if len(stale) > 0 {
		sort.Strings(stale)
		t.Errorf("proseless names propert(ies) that are gone or now described: %s\n"+
			"An exemption that outlives its cause is how a generator gap becomes permanent — "+
			"delete the entr(ies).", strings.Join(stale, ", "))
	}
}
