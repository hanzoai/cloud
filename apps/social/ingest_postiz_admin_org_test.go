package social

import (
	"context"
	"testing"

	"github.com/hanzoai/cloud/internal/kmstest"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/authz"
)

// The Postiz secrets live under orgs/admin/…. Social is a plugin, so deps.KMS is
// KMSPeer and apps/kms admit serves a tenant ref only to a call acting for that
// tenant. Background alone acts as the brand and is refused — the live 401 with
// a correct sealed token. cloud.For(…, admin) is the same pattern websearch uses
// for orgs/admin/websearch keys.
func TestPostizSecretRefRequiresAdminOrg(t *testing.T) {
	kc := kmstest.New()
	ref := postizSecretRef(postizIngestSecretName)
	want := []byte("probe-token-not-for-prod")
	if err := kc.PutSecret(cloud.For(context.Background(), authz.AdminOrg), ref, want); err != nil {
		t.Fatalf("seed as admin: %v", err)
	}
	if _, err := kc.GetSecret(context.Background(), ref); err == nil {
		t.Fatal("GetSecret(Background) must be refused for orgs/admin/…")
	}
	got, err := kc.GetSecret(cloud.For(context.Background(), authz.AdminOrg), ref)
	if err != nil {
		t.Fatalf("GetSecret(For admin): %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("got len=%d, want len=%d", len(got), len(want))
	}
}
