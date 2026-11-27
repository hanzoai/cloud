package reward

import (
	"sort"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk/spec"
)

// surface reads the router this app mounted two ways at their one shared address
// form: what the document says is SERVED, and which of those carry a TYPED registry
// entry. Reading the router rather than the source is what makes this a gate.
func surface(t *testing.T) (served map[string]bool, typed map[string]string) {
	t.Helper()
	r := newRig(t)
	doc, err := spec.Of(r.app, spec.Info{Title: "reward", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	reg, err := spec.Typed(r.app)
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

// moduleOwed is the CLOSED list of operations the embedded hanzoai/reward module
// serves, each a raw `func(*zip.Ctx) error` registered by the module's own route
// table (http.Route, native zip Raw handlers). Typing one is module work: the
// handler, its request and its response shape live in the module behind unexported
// types, and a typed twin declared here against a private shape would be a second
// implementation of the same decision, with two sets of bounds to drift. Their
// prose is stated by openapi.Describe (describe.go). The module owes the typing;
// the number can only fall.
const moduleReason = "registered raw by hanzoai/reward's http.Route; typing it is module work, and its prose is describe.go's"

var moduleOwed = map[string]string{
	"GET /v1/reward/health":                                   moduleReason,
	"GET /v1/reward/catalog":                                  moduleReason,
	"POST /v1/reward/events":                                  moduleReason,
	"GET /v1/reward/me":                                       moduleReason,
	"GET /v1/reward/me/claims":                                moduleReason,
	"GET /v1/reward/me/explore":                               moduleReason,
	"GET /v1/reward/me/profile":                               moduleReason,
	"PUT /v1/reward/me/profile":                               moduleReason,
	"GET /v1/reward/profile/{handle}":                         moduleReason,
	"GET /v1/reward/badge/{handle}":                           moduleReason,
	"GET /v1/admin/reward/programs":                           moduleReason,
	"POST /v1/admin/reward/programs":                          moduleReason,
	"GET /v1/admin/reward/programs/{program}":                 moduleReason,
	"PUT /v1/admin/reward/programs/{program}":                 moduleReason,
	"DELETE /v1/admin/reward/programs/{program}":              moduleReason,
	"PUT /v1/admin/reward/programs/{program}/rules/{rule}":    moduleReason,
	"DELETE /v1/admin/reward/programs/{program}/rules/{rule}": moduleReason,
	"POST /v1/admin/reward/programs/{program}/backfill":       moduleReason,
	"GET /v1/admin/reward/claims":                             moduleReason,
	"POST /v1/admin/reward/claims/{claim}/approve":            moduleReason,
	"POST /v1/admin/reward/claims/{claim}/reject":             moduleReason,
	"POST /v1/admin/reward/claims/{claim}/reverse":            moduleReason,
	"POST /v1/admin/reward/simulate":                          moduleReason,
}

// TestEveryRouteIsTypedOrNamed fails three ways: a served operation that is neither
// typed nor named (a route added here, or upstream in the module, is typed by
// default); an entry naming an address the router no longer serves; and an entry
// naming an address that IS a typed op.
func TestEveryRouteIsTypedOrNamed(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		served, typed := surface(t)
		var unclassified []string
		for key := range served {
			if _, ok := typed[key]; ok {
				continue
			}
			if _, named := moduleOwed[key]; !named {
				unclassified = append(unclassified, key)
			}
		}
		if len(unclassified) > 0 {
			sort.Strings(unclassified)
			t.Errorf("operation(s) with no registry entry and no reason:\n\t%s\nConvert it to a typed op in this package, or name it in moduleOwed with its blocker.", strings.Join(unclassified, "\n\t"))
		}
		for key := range moduleOwed {
			if !served[key] {
				t.Errorf("moduleOwed names %q, which this app no longer serves", key)
			}
			if _, ok := typed[key]; ok {
				t.Errorf("moduleOwed names %q, which IS a typed op: delete the entry in the change that converted it", key)
			}
		}

	})
}

// TestTheOwedRatchetOnlyFalls: typed ops may only rise and the module's owed routes
// only fall.
func TestTheOwedRatchetOnlyFalls(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		served, typed := surface(t)
		if len(typed)+len(moduleOwed) != len(served) {
			t.Errorf("typed %d + owed %d != served %d", len(typed), len(moduleOwed), len(served))
		}
		const (
			typedFloor = 9
			owedCeil   = 23
		)
		if len(typed) < typedFloor {
			t.Errorf("typed ops fell to %d, below %d", len(typed), typedFloor)
		}
		if len(moduleOwed) > owedCeil {
			t.Errorf("moduleOwed grew to %d, above %d: a raw route was added rather than converted", len(moduleOwed), owedCeil)
		}

	})
}

// TestEveryTypedOpIsDescribed holds the prose to the bar the schema is held to.
func TestEveryTypedOpIsDescribed(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		_, typed := surface(t)
		for key, desc := range typed {
			if strings.TrimSpace(desc) == "" {
				t.Errorf("%s is a typed op that says nothing about itself", key)
			}
		}

	})
}
