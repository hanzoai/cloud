package pref

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/internal/sqltest"
)

// The plane op reads the document of the person on the call — the same
// `<owner>/<name>` the HTTP surface keys on — and nobody else's. A call that
// names no person is refused rather than answered for the org.
func TestOwnReadsTheCallersDocument(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		s := &service{store: testStore(t)}
		ctx := context.Background()
		if _, err := s.store.Merge(ctx, "hanzo/z", map[string]any{"instructions": "Be brief."}, 100); err != nil {
			t.Fatal(err)
		}
		as := func(org, user string) context.Context {
			return zip.WithCaller(ctx, zip.Caller{Org: org, User: user})
		}

		got, err := s.own(as("hanzo", "z"), nil)
		if err != nil || got.Doc != `{"instructions":"Be brief."}` {
			t.Fatalf("own = %+v, %v", got, err)
		}
		for _, c := range [][2]string{{"admin", "z"}, {"hanzo", "zoe"}} {
			got, err := s.own(as(c[0], c[1]), nil)
			if err != nil || got.Doc != "{}" {
				t.Fatalf("%s/%s read %+v, %v", c[0], c[1], got, err)
			}
		}
		var he *zip.HTTPError
		if _, err := s.own(as("hanzo", ""), nil); !errors.As(err, &he) || he.Status != http.StatusForbidden {
			t.Fatalf("a call naming no person was answered: %v", err)
		}
	})
}
