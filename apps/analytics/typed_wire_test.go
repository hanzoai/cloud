package analytics

import (
	"sort"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/sdk/spec"
)

// untypedByDesign is the CLOSED list of analytics operations that are not typed
// ops. The stream is a long-lived SSE byte stream (text/event-stream) whose
// frames are coalesced across callers; a typed op's Out is a JSON value, so
// typing it would change the wire rather than describe it.
var untypedByDesign = map[string]string{
	"GET /v1/analytics/stream": "a byte stream: text/event-stream with coalesced, long-lived frames. " +
		"zip has no streaming response type, and a typed op's Out is one JSON value, so this route must stay raw.",
}

// TestEveryRouteIsTypedOrNamed pins that ledger against the live router: a new
// untyped route fails here, and a reason whose route or typed status changed
// fails here too. The two ledgers must sum to the served surface.
func TestEveryRouteIsTypedOrNamed(t *testing.T) {
	app, _ := newTestApp(t, Config{Backend: newFakeBackend(nil)})
	doc, err := spec.Of(app, spec.Info{Title: "analytics", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	reg, err := spec.Typed(app)
	if err != nil {
		t.Fatalf("typed registry: %v", err)
	}
	ours := func(p string) bool { return strings.HasPrefix(p, "/v1/analytics") }
	served := map[string]bool{}
	typed := map[string]bool{}
	for path, item := range doc.Paths {
		if !ours(path) {
			continue
		}
		for method := range item {
			served[strings.ToUpper(method)+" "+path] = true
		}
	}
	for key := range reg.Ops {
		i := strings.Index(key, " ")
		if i > 0 && ours(key[i+1:]) {
			typed[key] = true
		}
	}
	var untyped []string
	for key := range served {
		if !typed[key] && untypedByDesign[key] == "" {
			untyped = append(untyped, key)
		}
	}
	if len(untyped) > 0 {
		sort.Strings(untyped)
		t.Errorf("served but neither typed nor named: %s. Convert it (zip.Get/Post/... in routes()) "+
			"or name it in untypedByDesign with the wire fact that keeps it raw.", strings.Join(untyped, ", "))
	}
	for key := range untypedByDesign {
		if !served[key] {
			t.Errorf("untypedByDesign names %q, which analytics no longer serves", key)
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
