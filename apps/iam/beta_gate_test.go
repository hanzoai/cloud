// Copyright © 2026 Hanzo AI. MIT License.

package iam

import (
	"context"
	"testing"

	iamserver "github.com/hanzoai/iam/server"
)

// Mount binds the flag source; a host that never binds one leaves IAM failing
// the beta site gate closed. The empty-input case makes no plane call, so it
// pins the "never ask about nobody" rule without needing a running flags app.
func TestBetaGate_BoundAndFailClosedWithoutAPrincipal(t *testing.T) {
	bindBetaGate()
	t.Cleanup(func() { iamserver.BindBetaGate(nil) })
	if !iamserver.BetaGateBound() {
		t.Fatal("bindBetaGate did not install the flag source")
	}
	g := flagGate{}
	for _, tc := range []struct{ key, org, sub string }{
		{"beta.bot", "", "hanzo/alice"},
		{"beta.bot", "hanzo", ""},
		{"", "hanzo", "hanzo/alice"},
	} {
		on, err := g.On(context.Background(), tc.key, tc.org, tc.sub)
		if on || err != nil {
			t.Fatalf("flagGate.On(%q,%q,%q) = %v, %v; want false, nil", tc.key, tc.org, tc.sub, on, err)
		}
	}
}
