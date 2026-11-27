package kms

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"testing"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/sqltest"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// A secret the store does not hold crosses the plane as a 404, which KMSPeer
// reads back as cloud.ErrNoSecret — so a caller in another process tells
// "never written" from "could not be read" by type, as a caller in this one does.
func TestTheStoreAnswersAMissingSecretAs404(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		master := make([]byte, 32)
		_, _ = rand.Read(master)
		c, err := New(Config{DataDir: t.TempDir(), MasterKeyB64: base64.StdEncoding.EncodeToString(master)}, luxlog.New("test"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = c.Close() })
		_, err = secretOps{c: c}.get(context.Background(), &client.SecretIn{Ref: "principal/attestation"})
		var he *zip.HTTPError
		if !errors.As(err, &he) || he.Status != 404 {
			t.Fatalf("a missing secret over the plane = %v, want 404", err)
		}
	})
}
