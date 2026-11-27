// Copyright © 2026 Hanzo AI. MIT License.

package commerce

import (
	"context"
	"os"
	"strings"

	commercesecrets "github.com/hanzoai/commerce/secrets"

	"github.com/hanzoai/cloud/sdk/network"
	"github.com/hanzoai/cloud/sdk/secret"
)

// envSecrets is the secret plane commerce reads through, with this network's KMS environment put
// on the absolute paths commerce names.
//
// Commerce names a processor's secret by the KMS path it lives at and reads it through the host:
// "/orgs/hanzo/commerce-secrets/STRIPE_SECRET_KEY" on mainnet, "/orgs/hanzo/cloud/STRIPE_TEST_SECRET_KEY"
// on the testnet. The host's store keys a secret on (path, name, environment), and a ref that names no
// environment is read in "default", where this deployment's secrets are not: mainnet seals them under
// "prod", the test networks under their own name. So an absolute path that names no environment is
// read in the environment of the network this deployment is.
//
// Refs that already name one ("…@prod") and the host's own addresses ("orgs/<org>/…", bare names)
// pass through untouched.
type envSecrets struct {
	inner interface {
		GetSecret(ctx context.Context, ref string) ([]byte, error)
	}
	env string
}

// kmsEnv is the KMS environment slug of a network.
func kmsEnv(n network.Network) string {
	switch n {
	case network.Testnet:
		return "testnet"
	case network.Devnet:
		return "devnet"
	default:
		return "prod"
	}
}

// GetSecret reads ref in the deployment's environment when ref is an absolute path naming none.
func (e envSecrets) GetSecret(ctx context.Context, ref string) ([]byte, error) {
	if strings.HasPrefix(ref, "/") && !strings.Contains(ref, "@") {
		ref += "@" + e.env
	}
	b, err := e.inner.GetSecret(ctx, ref)
	if (err != nil || len(b) == 0) && strings.HasPrefix(ref, "orgs/hanzo/wire/") {
		name := strings.TrimPrefix(ref, "orgs/hanzo/wire/")
		if b2, err2 := e.inner.GetSecret(ctx, "/orgs/hanzo/commerce-secrets/"+name+"@"+e.env); err2 == nil && len(b2) > 0 {
			return b2, nil
		}
		if b2, err2 := e.inner.GetSecret(ctx, "/commerce-secrets/"+name+"@"+e.env); err2 == nil && len(b2) > 0 {
			return b2, nil
		}
		if v := strings.TrimSpace(os.Getenv(name)); v != "" {
			return []byte(v), nil
		}
	}
	return b, err
}

// hostSecrets is the host's secret plane as commerce reads it, in this network's KMS environment.
// nil stays nil: a build with no KMS reads nothing.
func hostSecrets(k secret.Store) commercesecrets.Reader {
	if k == nil {
		return nil
	}
	return envSecrets{inner: k, env: kmsEnv(network.NetworkOf())}
}
