package cloud_test

import (
	"context"
	"errors"
	"testing"

	"github.com/hanzoai/cloud/sdk/secret"

	"github.com/hanzoai/cloud/client"
	cloud "github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/kit"
	"github.com/zap-proto/zip"
)

// KMSPeer answers the store's 404 on a read as cloud.ErrNoSecret, and any other
// failure as itself: a caller keys absence on the type, never on the words.
func TestKMSPeerTypesAMissingSecret(t *testing.T) {
	t.Setenv("ZIP_RUNTIME_DIR", kit.Sockets(t))
	app := zip.New(zip.Config{AppName: "kms"})
	app.Post("/kms/get", func(_ context.Context, in *client.SecretIn) (*client.Secret, error) {
		if in.Ref == "absent" {
			return nil, zip.ErrNotFound("kms.get: no such secret")
		}
		return nil, zip.Errorf(500, "kms.get: secret not found in the words of an outage")
	}, zip.WithOperationID(client.KMSGet))
	app.Post("/kms/delete", func(_ context.Context, in *client.SecretIn) (*client.Secret, error) {
		if in.Ref == "absent" {
			return nil, zip.ErrNotFound("kms.delete: no such secret")
		}
		return nil, zip.Errorf(500, "kms.delete: store unavailable")
	}, zip.WithOperationID(client.KMSDel))
	go func() { _ = app.Listen(zip.SocketPath("kms")) }()
	t.Cleanup(func() { _ = app.Shutdown() })
	waitFor(t, "kms")

	if _, err := (cloud.KMSPeer{}).GetSecret(context.Background(), "absent"); !errors.Is(err, secret.ErrNone) {
		t.Fatalf("a missing secret = %v, want cloud.ErrNoSecret", err)
	}
	if _, err := (cloud.KMSPeer{}).GetSecret(context.Background(), "broken"); err == nil || errors.Is(err, secret.ErrNone) {
		t.Fatalf("an outage = %v, want an error that is not secret.ErrNone", err)
	}
	if err := (cloud.KMSPeer{}).DeleteSecret(context.Background(), "absent"); !errors.Is(err, secret.ErrNone) {
		t.Fatalf("deleting a missing secret = %v, want cloud.ErrNoSecret", err)
	}
	if err := (cloud.KMSPeer{}).DeleteSecret(context.Background(), "broken"); err == nil || errors.Is(err, secret.ErrNone) {
		t.Fatalf("a delete outage = %v, want an error that is not secret.ErrNone", err)
	}
}
