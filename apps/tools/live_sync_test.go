package tools

import (
	"context"
	"os"
	"testing"
)

// TestLiveRegistry syncs against the REAL public registry. It is SKIPPED unless
// CLOUD_TOOLS_LIVE=1, because a suite that needs the internet is a suite that
// goes red when a third party has a bad afternoon — but the shape this parses is
// theirs, so it has to be checked against theirs and not only against a fixture.
func TestLiveRegistry(t *testing.T) {
	if os.Getenv("CLOUD_TOOLS_LIVE") != "1" {
		t.Skip("set CLOUD_TOOLS_LIVE=1 to sync the real registry")
	}
	c, err := OpenCatalogStore(t.TempDir() + "/catalog.db")
	if err != nil {
		t.Fatalf("OpenCatalogStore: %v", err)
	}
	defer func() { _ = c.Close() }()
	ctx := context.Background()
	added, updated, err := c.Sync(ctx)
	if err != nil {
		t.Fatalf("live sync: %v", err)
	}
	t.Logf("live sync: added=%d updated=%d", added, updated)
	total, _ := c.Count(ctx)
	if total < 100 {
		t.Fatalf("the public registry has thousands of servers; got %d", total)
	}
	_, officials, err := c.List(ctx, Query{Official: true})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	n, page := 0, 0
	for {
		batch, _, err := c.List(ctx, Query{Limit: catalogMax, Offset: page})
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if len(batch) == 0 {
			break
		}
		for _, l := range batch {
			if l.Endpoint() != "" {
				n++
			}
		}
		page += len(batch)
	}
	t.Logf("catalog: %d listings, %d official, %d enable-able today", total, officials, n)
	official, _, err := c.List(ctx, Query{Official: true, Limit: 5})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for i, l := range official {
		if i == 5 {
			break
		}
		t.Logf("  official: %-40s %s", l.Name, l.Endpoint())
	}
	added2, updated2, err := c.Sync(ctx)
	if err != nil {
		t.Fatalf("second live sync: %v", err)
	}
	if added2 != 0 {
		t.Fatalf("a second sync over the same registry added %d rows", added2)
	}
	t.Logf("second live sync: added=%d updated=%d (idempotent against the real registry)", added2, updated2)
}
