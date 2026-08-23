package zzmeasure

import (
	"sort"
	"testing"

	_ "github.com/hanzoai/cloud/apps/admin"
	_ "github.com/hanzoai/cloud/apps/affiliate"
	_ "github.com/hanzoai/cloud/apps/ai"
	_ "github.com/hanzoai/cloud/apps/allowance"
	_ "github.com/hanzoai/cloud/apps/entitlement"
	"github.com/hanzoai/cloud/apps/flags"
)

func TestDefsUnion(t *testing.T) {
	var keys []string
	for _, d := range flags.Defs() {
		keys = append(keys, d.Key)
	}
	sort.Strings(keys)
	t.Logf("union of defs when admin+entitlement+allowance+affiliate+ai are ALL linked: %d", len(keys))
	for _, k := range keys {
		t.Log("   ", k)
	}
}
