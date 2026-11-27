package iam

import (
	"context"
	"errors"
	"strings"

	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/secret"
	iamserver "github.com/hanzoai/iam/server"
)

// vault.go: an organization's signing keys. IAM generates the keypair of an org's tenant cert
// and hands the private half here, sealed in KMS at orgs/<org>/iam/signing/<cert name> in the
// iam custody subtree (apps/kms custody.go), never on the cert's row or in the signing-key mount.

// vault is the host's iamserver.Vault: KMS, under the org.
type vault struct{ kms cloud.KMSPeer }

// signingRef is where one cert's key is sealed; IAM validates both parts.
func signingRef(org, name string) string { return "orgs/" + org + "/iam/signing/" + name }

// asOrg is a call acting for org and no one else: from a bare context, because the store reads the
// caller off a request when there is one.
func asOrg(_ context.Context, org string) context.Context {
	return cloud.For(context.Background(), org)
}

// Put seals pem, and refuses a key already held: a key is never replaced.
func (v vault) Put(ctx context.Context, org, name, pem string) error {
	ctx = asOrg(ctx, org)
	ref := signingRef(org, name)
	switch _, err := v.kms.GetSecret(ctx, ref); {
	case err == nil:
		return iamserver.ErrKeyExists
	case !errors.Is(err, secret.ErrNone):
		return err
	}
	return v.kms.PutSecret(ctx, ref, []byte(pem))
}

// Get reads the key sealed for (org, name).
func (v vault) Get(ctx context.Context, org, name string) (string, error) {
	b, err := v.kms.GetSecret(asOrg(ctx, org), signingRef(org, name))
	if errors.Is(err, secret.ErrNone) {
		return "", iamserver.ErrNoKey
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

// Delete forgets the key sealed for (org, name); a key that is not there is not an error.
func (v vault) Delete(ctx context.Context, org, name string) error {
	err := v.kms.DeleteSecret(asOrg(ctx, org), signingRef(org, name))
	if errors.Is(err, secret.ErrNone) {
		return nil
	}
	return err
}
