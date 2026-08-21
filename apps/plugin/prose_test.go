package plugin

import (
	"sort"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/openapi"
	"github.com/zap-proto/zip"
)

// plane registers the control plane against a bare app, which is all a
// projection needs: the document is a function of the typed registry, not of a
// running fleet.
func plane(t *testing.T) *zip.App {
	t.Helper()
	z := zip.New(zip.Config{AppName: "plugins", DisableStartupMessage: true})
	Routes(z, &ops{z: z})
	return z
}

// proseless is the CLOSED list of published properties carrying NO description
// because they are declared in ANOTHER MODULE. Host.plugins is []zip.Status, so
// zip's own Status and Usage are published components of this surface, and their
// prose is written where they are declared — github.com/zap-proto/zip@v1.31.0,
// status.go — which is a read-only dependency here. Two of the three are the
// header defect zipdoc has upstream: a comment written above a GROUP of fields is
// lifted onto the first of them alone.
//
// It is exact in BOTH directions. A bare property anywhere else goes red, and an
// entry here that starts publishing prose goes red too — that is the day zip's
// own source gains the comment, and this ledger must shrink then rather than
// outlive the gap.
var proseless = map[string]bool{
	// zip status.go:17 — `Name string` carries no comment at all.
	"Status.name": true,
	// zip status.go:20-25 — the comment above Prefix speaks for Prefixes too, and
	// lands on Prefix alone.
	"Status.prefixes": true,
	// zip status.go:83-86 — the comment above Threads speaks for FDs too, and
	// lands on Threads alone.
	"Usage.fds": true,
}

// TestEveryPublishedFieldIsDescribed closes the half of the surface an op-level
// gate cannot see. Typing a route documents its ADDRESS and its SHAPE; the
// shape's FIELDS come from a different place — a doc comment on each one, which
// zipdoc lifts one at a time.
//
// It matters here because this surface is counters and digests, and neither reads
// as anything on its own. A version is never a release tag: it is the artifact's
// SHA-256, which is why re-pinning one is free and why `drifted` — more than one
// digest running at once — is the single bit the board exists to report. Drift's
// three counts are HOSTS and deliberately do not add up to the fleet, because a
// host that did not answer is counted nowhere: unknown is not down. `down` and
// `disabled` are split for the same reason, since both answer 503 and only one of
// them is an outage. And a mutation that halts answers 200 with status "error",
// because part of the fleet has already changed and `data` is the list of hosts
// that may have moved.
//
// Presence is all a gate can check. A description restating the field's name is
// worse than none, and only a reader catches that.
func TestEveryPublishedFieldIsDescribed(t *testing.T) {
	doc, err := openapi.Spec(plane(t), openapi.Info{Title: "plugins", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	if doc.Components == nil || len(doc.Components.Schemas) == 0 {
		t.Fatal("plugins publishes no schemas at all — the gate would pass vacuously")
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
			"the first of them alone — then run: make -C apps/plugin describe",
			len(bare), strings.Join(bare, ", "))
	}
	if len(stale) > 0 {
		sort.Strings(stale)
		t.Errorf("proseless names propert(ies) that are gone or now described: %s\n"+
			"An exemption that outlives its cause is how a generator gap becomes permanent — "+
			"delete the entr(ies).", strings.Join(stale, ", "))
	}
}
