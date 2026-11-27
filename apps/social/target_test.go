package social

import (
	"context"
	"testing"

	"github.com/hanzoai/cloud/internal/sqltest"
)

// Connecting the same platform id again updates the row the org already
// manages. A different id is another account, so a company page does not
// replace a personal profile.
func TestTwoTargetsStayDistinct(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		ctx := context.Background()
		s := testStore(t)
		first, err := s.UpsertTarget(ctx, "acme", "linkedin", "mem", "Ada", 1)
		if err != nil {
			t.Fatal(err)
		}
		page, err := s.UpsertTarget(ctx, "acme", "linkedin", "99", "Acme", 2)
		if err != nil {
			t.Fatal(err)
		}
		if first.ID == page.ID || first.ExternalID == page.ExternalID {
			t.Fatalf("one row for two accounts: %+v %+v", first, page)
		}
		again, err := s.UpsertTarget(ctx, "acme", "linkedin", "mem", "Ada M", 3)
		if err != nil {
			t.Fatal(err)
		}
		if again.ID != first.ID || again.Handle != "Ada M" {
			t.Fatalf("reconnect = %+v, first %s", again, first.ID)
		}
		list, err := s.ListAccounts(ctx, "acme", "linkedin", 10)
		if err != nil || len(list) != 2 {
			t.Fatalf("list = %+v err=%v", list, err)
		}

	})
}
