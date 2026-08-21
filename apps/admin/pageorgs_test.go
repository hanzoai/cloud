package admin

import (
	"testing"

	"github.com/hanzoai/cloud/apps/admin/iam"
)

// The directory pages BEFORE it fans out, which is the whole point: the rows
// returned decide how many per-org reads happen. This fleet grew from the
// eighty-one tenants the fan-out was written for to six hundred and eighty-four,
// and an unpaged directory costs one round trip per tenant on every load.
func TestPageOrgsBoundsTheFanOut(t *testing.T) {
	all := make([]iam.Org, 684)
	for i := range all {
		all[i].Name = string(rune('a' + i%26))
	}

	if got := len(pageOrgs(all, nil)); got != 200 {
		t.Errorf("no input = %d rows, want the 200 default — an unpaged default is the bug", got)
	}
	if got := len(pageOrgs(all, &orgsIn{PageSize: "50"})); got != 50 {
		t.Errorf("pageSize=50 = %d rows", got)
	}
	// The last page is short, not padded.
	if got := len(pageOrgs(all, &orgsIn{Page: "4", PageSize: "200"})); got != 84 {
		t.Errorf("page 4 of 684@200 = %d rows, want 84", got)
	}
	// Walking past the end stops cleanly rather than erroring or wrapping.
	if got := pageOrgs(all, &orgsIn{Page: "99", PageSize: "200"}); got != nil {
		t.Errorf("page 99 = %d rows, want none", len(got))
	}
	// Garbage falls back to the defaults instead of returning zero rows, because
	// a directory that renders empty on a bad query looks like an empty fleet.
	if got := len(pageOrgs(all, &orgsIn{Page: "abc", PageSize: "-3"})); got != 200 {
		t.Errorf("garbage input = %d rows, want the 200 default", got)
	}
}

// A fleet smaller than one page is returned whole.
func TestPageOrgsSmallFleet(t *testing.T) {
	all := make([]iam.Org, 7)
	if got := len(pageOrgs(all, nil)); got != 7 {
		t.Errorf("7 orgs = %d rows, want all 7", got)
	}
}
