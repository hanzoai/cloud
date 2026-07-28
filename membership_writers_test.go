package cloud

import (
	"context"
	"testing"

	"github.com/hanzoai/cloud/internal/org"
)


// TestMembershipSourceStaticFallback: with no selector (dev / native-Go) the source IS
// the static set — the exact wiring runs everywhere, no cluster required.
func TestMembershipSourceStaticFallback(t *testing.T) {
	peers := []org.Member{{ID: "cloud-0", Addr: "cloud-0:8080"}, {ID: "cloud-1", Addr: "cloud-1:8080"}}
	src := membershipSource(peers, "", "8080", nil)
	got, err := src(context.Background())
	if err != nil {
		t.Fatalf("static source: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("static fallback want 2 members, got %d", len(got))
	}
}

// TestMembershipSourceSelectorOutOfClusterFallsBack: a selector is set but the process
// is NOT in a cluster (the test env) — the source must fall back to static rather than
// fail, so a misconfigured selector never strands the deployment.
func TestMembershipSourceSelectorOutOfClusterFallsBack(t *testing.T) {
	peers := []org.Member{{ID: "solo", Addr: "solo:8080"}}
	src := membershipSource(peers, "app.kubernetes.io/name=cloud", "8080", nil)
	got, err := src(context.Background())
	if err != nil {
		t.Fatalf("out-of-cluster source must not error: %v", err)
	}
	if len(got) != 1 || got[0].ID != "solo" {
		t.Fatalf("out-of-cluster selector must fall back to static self: %+v", got)
	}
}

func TestHTTPPortOf(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{":8080", "8080"},
		{"0.0.0.0:8080", "8080"},
		{"8080", "8080"},
		{"", ""},
	} {
		if got := httpPortOf(tc.in); got != tc.want {
			t.Fatalf("httpPortOf(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
