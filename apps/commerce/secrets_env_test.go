// Copyright © 2026 Hanzo AI. MIT License.

package commerce

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"testing"

	luxlog "github.com/luxfi/log"

	"github.com/hanzoai/cloud/apps/kms"
	"github.com/hanzoai/cloud/sdk/network"
	"github.com/hanzoai/cloud/sdk/secret"
)

type refsAsked struct{ refs []string }

func (a *refsAsked) GetSecret(_ context.Context, ref string) ([]byte, error) {
	a.refs = append(a.refs, ref)
	return []byte("v"), nil
}

// The paths commerce names for a processor's secrets are absolute and name no environment; the
// host's store would read them in "default", where this deployment's secrets are not. They are
// read in the environment of the network the deployment is, and nothing else is rewritten.
func TestAnAbsoluteSecretPathIsReadInTheNetworksEnvironment(t *testing.T) {
	for net, env := range map[network.Network]string{network.Mainnet: "prod", network.Testnet: "testnet", network.Devnet: "devnet"} {
		if got := kmsEnv(net); got != env {
			t.Fatalf("%s reads KMS environment %q, want %q", net, got, env)
		}
	}
	in := &refsAsked{}
	r := envSecrets{inner: in, env: "prod"}
	for _, ref := range []string{"/commerce-secrets/STRIPE_SECRET_KEY", "/cloud/SQUARE_SANDBOX_ACCESS_TOKEN", "/commerce-secrets/NAME@staging", "orgs/hanzo/key", "BARE_NAME"} {
		if _, err := r.GetSecret(context.Background(), ref); err != nil {
			t.Fatal(err)
		}
	}
	want := []string{"/commerce-secrets/STRIPE_SECRET_KEY@prod", "/cloud/SQUARE_SANDBOX_ACCESS_TOKEN@prod", "/commerce-secrets/NAME@staging", "orgs/hanzo/key", "BARE_NAME"}
	for i := range want {
		if in.refs[i] != want[i] {
			t.Fatalf("ref %d refsAsked as %q, want %q", i, in.refs[i], want[i])
		}
	}
}

// No KMS in the build stays no reader: commerce reads nothing and says "not configured".
func TestNoKMSIsNoSecretPlane(t *testing.T) {
	if hostSecrets(nil) != nil {
		t.Fatal("a build with no KMS handed commerce a reader")
	}
}

// The reader commerce gets, in front of the REAL store's reader rather than a fake one: the path a
// ref names crosses the store's own parser and lands on the record sealed at (path, name,
// environment). The mainnet keys sit at the org root, in "prod"; a network whose store holds
// nothing there reads ErrNoSecret.
func TestAnOrgRootRefReachesTheSealedRecordInTheNetworksEnvironment(t *testing.T) {
	master := make([]byte, 32)
	_, _ = rand.Read(master)
	c, err := kms.New(kms.Config{DataDir: t.TempDir(), MasterKeyB64: base64.StdEncoding.EncodeToString(master)}, luxlog.New("test"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	if err := c.Put("/orgs/hanzo/commerce-secrets", "STRIPE_SECRET_KEY", "prod", []byte("sk-live")); err != nil {
		t.Fatal(err)
	}
	const ref = "/orgs/hanzo/commerce-secrets/STRIPE_SECRET_KEY"
	got, err := envSecrets{inner: c, env: "prod"}.GetSecret(context.Background(), ref)
	if err != nil || string(got) != "sk-live" {
		t.Fatalf("mainnet read = %q, %v; want the sealed value", got, err)
	}
	if _, err := c.GetSecret(context.Background(), ref); !errors.Is(err, secret.ErrNone) {
		t.Fatalf("the bare ref read %v; the default environment holds nothing, which is why the wrapper exists", err)
	}
	if _, err := (envSecrets{inner: c, env: "testnet"}).GetSecret(context.Background(), ref); !errors.Is(err, secret.ErrNone) {
		t.Fatalf("a network whose store lacks the secret read %v, want ErrNoSecret", err)
	}
}
