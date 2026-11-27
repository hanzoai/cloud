package cloud_test

import (
	"context"
	"testing"

	"github.com/hanzoai/cloud/client"
	cloud "github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/kit"
	"github.com/zap-proto/zip"
)

// KMSPeer states the org its CALLER acts for, never one read out of the ref.
//
// The store serves a tenant's ref only to a call acting for that tenant (apps/kms
// attest.go admit). If the peer stated the org FROM the ref, that check would
// compare the ref with itself and always pass, so a plugin that let a request
// shape a ref's org segment would read whichever tenant the request named. This
// drives the real transport against a stand-in store that answers with the org
// the call arrived acting for.
func TestKMSPeerStatesTheCallersOrgNeverTheRefs(t *testing.T) {
	t.Setenv("ZIP_RUNTIME_DIR", kit.Sockets(t))
	app := zip.New(zip.Config{AppName: "kms"})
	app.Post("/kms/get", func(ctx context.Context, in *client.SecretIn) (*client.Secret, error) {
		return &client.Secret{Value: []byte("acting-for:" + cloud.Who(ctx).Org)}, nil
	}, zip.WithOperationID(client.KMSGet))
	go func() { _ = app.Listen(zip.SocketPath("kms")) }()
	t.Cleanup(func() { _ = app.Shutdown() })
	waitFor(t, "kms")

	for _, tc := range []struct {
		what string
		ctx  context.Context
		ref  string
		want string
	}{
		{"a job acting for acme, asking for zeta's ref", cloud.For(context.Background(), "acme"), "orgs/zeta/db/URL@prod", "acting-for:acme"},
		{"a job acting for acme, asking for its own", cloud.For(context.Background(), "acme"), "orgs/acme/db/URL@prod", "acting-for:acme"},
		{"a job that states nothing, asking for a tenant's ref", context.Background(), "orgs/zeta/db/URL@prod", "acting-for:"},
	} {
		got, err := cloud.KMSPeer{}.GetSecret(tc.ctx, tc.ref)
		if err != nil {
			t.Fatalf("%s: %v", tc.what, err)
		}
		if string(got) != tc.want {
			t.Errorf("%s: the store saw %q, want %q — the org must be the caller's, never the ref's", tc.what, got, tc.want)
		}
	}
}
