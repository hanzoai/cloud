package o11y

import (
	"encoding/json"
	"strings"
	"testing"

	v3 "github.com/hanzoai/o11y/pkg/query-service/model/v3"
	qbtypes "github.com/hanzoai/o11y/pkg/types/querybuildertypes/querybuildertypesv5"
)

// consoleListPayload is, verbatim, the body hanzoai/console POSTs to the flat
// /v1/o11y/query_range — its listQueryPayload() (src/lib/api/apm.ts). It is the
// composite list query behind every trace/log explorer in the console, so it is
// the request this package's builderQueryHandler exists to route correctly.
const consoleListPayload = `{
  "start": 1700000000000, "end": 1700003600000, "step": 60,
  "compositeQuery": {
    "queryType": "builder",
    "panelType": "list",
    "builderQueries": {
      "A": { "queryName":"A","dataSource":"traces","aggregateOperator":"noop",
             "aggregateAttribute":{},"expression":"A","disabled":false,
             "stepInterval":60,"filters":{"items":[],"op":"AND"},
             "selectColumns":[],"groupBy":[],"having":[],
             "orderBy":[{"columnName":"timestamp","order":"desc"}],
             "limit":null,"offset":0,"pageSize":50 }
    }
  }
}`

// The flat public builder path resolves to the v3 engine route INTERNALLY, and
// that pin is the only reason the console's explorers work. This makes the
// reason executable rather than a comment: the SAME payload the console sends is
// valid v3 and is refused by v5, so "resolve to the highest engine version" —
// which is what the module's own version-less alias does — is not a
// simplification available here.
//
// Nothing else in cloud can catch this. builderQueryHandler forwards by
// rewriting r.URL.Path in-handler, so a route-table gate (typed_wire_test.go)
// cannot see where it points, and a wrong pin fails as a 400 from a live engine
// rather than a 404 — a broken console over a green build.
func TestConsoleCompositeIsV3ShapedNotV5(t *testing.T) {
	var v3params v3.QueryRangeParamsV3
	if err := json.Unmarshal([]byte(consoleListPayload), &v3params); err != nil {
		t.Fatalf("console payload must be valid v3: %v", err)
	}
	if v3params.CompositeQuery == nil || len(v3params.CompositeQuery.BuilderQueries) != 1 {
		t.Fatalf("console payload lost its builderQueries under v3 decode: %+v", v3params.CompositeQuery)
	}

	var v5req qbtypes.QueryRangeRequest
	err := json.Unmarshal([]byte(consoleListPayload), &v5req)
	if err == nil {
		t.Fatal("v5 accepted the console's v3 composite — if the engines have converged, " +
			"repoint builderQueryHandler at the flat route and delete the v3 pin")
	}
	// v5 names ONE offending key and the v3 composite carries several it does not
	// know (queryType, panelType, builderQueries), so which one it reports is map
	// order — assert the refusal, never the field.
	if !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("v5 rejected the console payload for an unexpected reason: %v", err)
	}
}

// The pin itself: builderQueryHandler must not forward to the flat public path.
// /v1/o11y/query_range is served by the v5 querier (o11yapiserver/querier.go), and
// TestConsoleCompositeIsV3ShapedNotV5 proves v5 refuses what the console sends —
// so forwarding there would 400 every explorer. It is also a self-forward: the
// flat path is what this handler is registered on (scope.go).
func TestBuilderQueryDoesNotForwardToTheFlatPath(t *testing.T) {
	for _, resource := range []string{"query", "query_range"} {
		internal := builderInternalPath(resource)
		if internal == o11yPrefix+"/"+resource {
			t.Fatalf("builderQueryHandler(%q) forwards to its own public path %q — "+
				"that is the v5 route, which refuses the console's composite", resource, internal)
		}
		if !strings.HasSuffix(internal, "/"+resource) {
			t.Fatalf("builderQueryHandler(%q) forwards to %q, which does not name the resource", resource, internal)
		}
	}
}
