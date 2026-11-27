// Copyright © 2026 Hanzo AI. MIT License.

package source

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/client"
	"github.com/zap-proto/zip"
)

// The relay asks this op from another binary: the owner of a live source is
// answered yes, and another person, another org and a revoked source no.
func TestOwnsAnswersForTheOwnerOnly(t *testing.T) {
	sqlite(t, func(t *testing.T) {
		r := mount(t)
		mounted = r.svc
		t.Cleanup(func() { mounted = nil })
		id := r.link("alice", "acme", newDevice(t))
		ask := func(org, user string) bool {
			t.Helper()
			ctx := zip.WithCaller(context.Background(), zip.Caller{Org: org, User: user})
			out, err := owns(ctx, &client.SourceOwnsIn{ID: id, Subject: user})
			if err != nil {
				t.Fatalf("owns %s/%s: %v", org, user, err)
			}
			return out.Owned
		}
		if !ask("acme", "alice") {
			t.Fatal("the owner was refused")
		}
		if ask("acme", "bob") || ask("other", "alice") {
			t.Fatal("someone other than the owner was answered yes")
		}
		if code, _ := r.as("DELETE", "/v1/sources/"+id, "alice", "acme", nil); code != 200 {
			t.Fatalf("revoke: %d", code)
		}
		if ask("acme", "alice") {
			t.Fatal("a revoked source is still owned")
		}
	})
}

func TestDispatchAndReleaseRPC(t *testing.T) {
	sqlite(t, func(t *testing.T) {
		x := newReady(t)
		mounted = x.r.svc
		t.Cleanup(func() { mounted = nil })

		ctx := zip.WithCaller(context.Background(), zip.Caller{Org: "acme", User: "alice"})
		// Explicit provider + instance grant.
		out, err := dispatchRPC(ctx, &client.SourceDispatchIn{
			ID: x.id, Subject: "alice", Provider: "claude", Instance: "claude-1",
		})
		if err != nil {
			t.Fatalf("dispatchRPC: %v", err)
		}
		if out.Zap != "https://mac.example/zap" || out.Provider != "claude" || out.Instance != "claude-1" || !strings.HasPrefix(out.Lease, "lse_") {
			t.Fatalf("unexpected route: %+v", out)
		}

		// Second dispatch while leased is refused 409 busy.
		if _, err := dispatchRPC(ctx, &client.SourceDispatchIn{
			ID: x.id, Subject: "alice", Provider: "claude", Instance: "claude-1",
		}); err == nil {
			t.Fatal("expected busy refusal")
		} else {
			var he *zip.HTTPError
			if !errors.As(err, &he) || he.Status != http.StatusConflict || he.Code != "busy" {
				t.Fatalf("want 409 busy, got %v", err)
			}
		}

		// Another person cannot release or dispatch.
		bobCtx := zip.WithCaller(context.Background(), zip.Caller{Org: "acme", User: "bob"})
		if _, err := releaseRPC(bobCtx, &client.SourceLeaseIn{ID: x.id, Subject: "bob", Lease: out.Lease}); err == nil {
			t.Fatal("bob released alice's lease")
		}
		if _, err := dispatchRPC(bobCtx, &client.SourceDispatchIn{ID: x.id, Subject: "bob", Provider: "claude", Instance: "claude-1"}); err == nil {
			t.Fatal("bob dispatched alice's desktop")
		}

		// Owner releases lease; then HIP-1350 instance-only shorthand (provider="claude-1", instance="") works.
		rel, err := releaseRPC(ctx, &client.SourceLeaseIn{ID: x.id, Subject: "alice", Lease: out.Lease})
		if err != nil || !rel.Released || rel.Lease != out.Lease {
			t.Fatalf("releaseRPC: %+v %v", rel, err)
		}
		out2, err := dispatchRPC(ctx, &client.SourceDispatchIn{
			ID: x.id, Subject: "alice", Provider: "claude-1",
		})
		if err != nil {
			t.Fatalf("shorthand dispatchRPC: %v", err)
		}
		if out2.Provider != "claude" || out2.Instance != "claude-1" || !strings.HasPrefix(out2.Lease, "lse_") {
			t.Fatalf("unexpected shorthand route: %+v", out2)
		}

		// Machine token is refused 403.
		appCtx := zip.WithCaller(context.Background(), zip.Caller{Org: "acme", User: "app:acme/ci"})
		if _, err := dispatchRPC(appCtx, &client.SourceDispatchIn{ID: x.id, Subject: "app:acme/ci", Provider: "claude", Instance: "claude-1"}); err == nil {
			t.Fatal("machine token allowed to dispatch")
		} else {
			var he *zip.HTTPError
			if !errors.As(err, &he) || he.Status != http.StatusForbidden {
				t.Fatalf("want 403, got %v", err)
			}
		}
	})
}
