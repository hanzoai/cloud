package platform

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk"
	"github.com/zap-proto/zip"
)

// TestNamesAreOneNamespace holds the platform to the namespace's answer: a name
// held by another org, or by another app of the same org, is the 409 at create,
// a holder reclaiming its own name is not, and a released name is free to anyone.
func TestNamesAreOneNamespace(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		servesDesk(t)
		s := &cloud.Service[state]{State: state{sitesHost: "hanzo.app"}}
		ctx := context.Background()
		conflict := func(err error) bool {
			var he *zip.HTTPError
			return errors.As(err, &he) && he.Status == http.StatusConflict
		}
		if err := claim(s, ctx, "acme", "web", holder("app_1")); err != nil {
			t.Fatalf("a free name: %v", err)
		}
		if err := claim(s, ctx, "acme", "web", holder("app_1")); err != nil {
			t.Fatalf("its holder reclaiming it: %v", err)
		}
		if err := claim(s, ctx, "other", "web", holder("app_2")); !conflict(err) {
			t.Fatalf("another org asking for a held name answered %v, want 409", err)
		}
		if err := claim(s, ctx, "acme", "web", holder("app_3")); !conflict(err) {
			t.Fatalf("another app of the same org asking answered %v, want 409", err)
		}
		release(s, ctx, "other", "web", holder("app_2"))
		if err := claim(s, ctx, "other", "web", holder("app_2")); !conflict(err) {
			t.Fatalf("a release by a stranger freed the name: %v", err)
		}
		release(s, ctx, "acme", "web", holder("app_1"))
		if err := claim(s, ctx, "other", "web", holder("app_2")); err != nil {
			t.Fatalf("a released name is free to anyone: %v", err)
		}
	})
}
