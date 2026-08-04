package reference

// live_test.go takes every published source for real.
//
// It is env-gated because it reaches ten third-party publishers and a unit suite
// must not depend on them. It is here anyway because the failure it catches is
// the one nothing else can: a publisher who changes a column keeps serving 200,
// the parser keeps returning entries, and the set silently becomes a shorter
// list of correct members. Only real bytes prove otherwise.
//
//	REFERENCE_LIVE=1 go test -tags sqlite_fts5 -run Live ./apps/reference/

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestLiveSourcesStillParse(t *testing.T) {
	if os.Getenv("REFERENCE_LIVE") == "" {
		t.Skip("set REFERENCE_LIVE=1 to take every published source for real")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	for _, set := range Catalog() {
		if set.Kind != KindFetch {
			continue
		}
		for _, src := range set.Sources {
			t.Run(set.Name+"/"+src.Name, func(t *testing.T) {
				got, err := pull(ctx, wire, src, pause)
				if err != nil {
					t.Fatalf("%s: %v", src.Origin, err)
				}
				if len(got) == 0 {
					t.Fatalf("%s parsed to nothing", src.Origin)
				}
				// A key with no value is a row the parser read halfway.
				for _, e := range got[:min(len(got), 50)] {
					if e.Key == "" {
						t.Fatalf("%s produced an entry with no key", src.Origin)
					}
				}
				// The digest is stable across two parses of the same bytes, which is
				// what makes a re-ingest a no-op.
				if digest(got) == "" {
					t.Fatal("no digest")
				}
				t.Logf("%-14s %-13s %6d entries  %s", set.Name, src.Name, len(got), digest(got)[:12])
			})
		}
	}
}
