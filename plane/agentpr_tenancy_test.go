package plane

import (
	"reflect"
	"strings"
	"testing"
)

// The two ops that file work items share ONE socket, and they must not disagree
// about where tenancy comes from.
//
// AgentPRIn used to carry an Org field which plugin/todo/seams.go read off
// the wire and passed straight into the per-tenant store selector
// (apps/todo/agentpr.go storeFor), so a caller on the plane could file a work
// item onto ANOTHER tenant's board simply by naming it. Its sibling IssueIn has
// never had one, and says why in its own doc comment.
//
// This test is the gate on that: re-adding an org-shaped field to either input
// re-opens the hole, and it must break a named test rather than pass review as a
// convenience.
func TestWorkItemInputsCarryNoTenancy(t *testing.T) {
	for _, tc := range []struct {
		name string
		typ  reflect.Type
	}{
		{"AgentPRIn", reflect.TypeFor[AgentPRIn]()},
		{"IssueIn", reflect.TypeFor[IssueIn]()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for f := range tc.typ.Fields() {
				if isTenancy(f.Name) {
					t.Fatalf("%s has a %s field: the org is the CALLER's plane identity "+
						"(cloud.Who), never an argument — a caller able to state the tenant "+
						"can write into another tenant's todo",
						tc.name, f.Name)
				}
				if tag := f.Tag.Get("json"); isTenancy(strings.Split(tag, ",")[0]) {
					t.Fatalf("%s field %s serialises as %q, which is a tenant key on the wire",
						tc.name, f.Name, tag)
				}
			}
		})
	}
}

// isTenancy names the field spellings that would put a tenant key on the wire.
// Project is NOT one of them: it is the IAM sub-scope WITHIN the caller's org,
// and it cannot cross an org boundary on its own.
func isTenancy(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "org", "orgid", "owner", "tenant", "tenantid", "account", "accountid":
		return true
	}
	return false
}
