// Copyright © 2026 Hanzo AI. MIT License.

package kms

import (
	"context"
	"fmt"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/plane"
	"github.com/zap-proto/zip"
)

// KMS on the internal plane.
//
// Exactly one process holds the sealed store, so every other app has to ask it.
// The ops are the KMSClient interface and nothing more: a secret store's call
// surface is the one place where "while I'm here, expose the rest" is how a
// tenant's material leaves the boundary that protects it.
//
// They are declared on cloud.Plane(), which listens only on this app's
// canonical socket, so there is no route from the edge to any of them.

// exposeSecrets publishes the store's reads and writes. Mount calls it.
func exposeSecrets(c cloud.KMSClient) {
	if c == nil {
		return
	}
	p := cloud.Plane()

	zip.Post[plane.SecretIn, plane.Secret](p, "/kms/get",
		func(ctx context.Context, in *plane.SecretIn) (*plane.Secret, error) {
			if err := authorize(ctx, in.Ref); err != nil {
				return nil, err
			}
			v, err := c.GetSecret(ctx, in.Ref)
			if err != nil {
				return nil, fmt.Errorf("kms.get: %w", err)
			}
			return &plane.Secret{Value: v}, nil
		},
		zip.WithOperationID(plane.KMSGet),
		zip.WithSummary("Read one secret"))

	zip.Post[plane.SecretIn, plane.Secret](p, "/kms/put",
		func(ctx context.Context, in *plane.SecretIn) (*plane.Secret, error) {
			if err := authorize(ctx, in.Ref); err != nil {
				return nil, err
			}
			if err := c.PutSecret(ctx, in.Ref, in.Value); err != nil {
				return nil, fmt.Errorf("kms.put: %w", err)
			}
			return &plane.Secret{}, nil
		},
		zip.WithOperationID(plane.KMSPut),
		zip.WithSummary("Write one secret"))

	zip.Post[plane.SecretIn, plane.Secret](p, "/kms/sign",
		func(ctx context.Context, in *plane.SecretIn) (*plane.Secret, error) {
			if err := authorize(ctx, in.Ref); err != nil {
				return nil, err
			}
			sig, err := c.Sign(ctx, in.Ref, in.Value)
			if err != nil {
				return nil, fmt.Errorf("kms.sign: %w", err)
			}
			return &plane.Secret{Value: sig}, nil
		},
		zip.WithOperationID(plane.KMSSign),
		zip.WithSummary("Sign a payload with a key that never leaves this process"))
}

// authorize checks the ref against the tenant the call is acting for.
//
// A ref that names a tenant must match it. That catches an app asking for one
// org's material while acting for another, which is a bug worth failing on.
//
// A ref that names none is the DEPLOYMENT's own material and is served to any
// peer, because the socket already decided who may ask: it is 0600 and
// SO_PEERCRED-authenticated, so a caller here is one of our own processes. A
// second gate derived from config would not add a boundary, only a spelling of
// one that already exists — and the platform-admin bit is minted from a
// validated token at the edge and cannot be asserted by a background call.
func authorize(ctx context.Context, ref string) error {
	if ref == "" {
		return zip.ErrBadRequest("kms: empty ref")
	}
	org, ok := tenantOf(ref)
	if !ok {
		return nil
	}
	if who := cloud.Who(ctx).Org; who != org {
		return zip.ErrForbidden(fmt.Sprintf("kms: ref %q belongs to %q but the call acts for %q", ref, org, who))
	}
	return nil
}

// tenantOf reads the org out of a ref, and reports whether the ref names one at
// all. "orgs/<org>/…" is a tenant's; anything else is the deployment's.
func tenantOf(ref string) (string, bool) {
	const p = "orgs/"
	if !strings.HasPrefix(ref, p) {
		return "", false
	}
	rest := ref[len(p):]
	i := strings.IndexByte(rest, '/')
	if i <= 0 {
		return "", false
	}
	return rest[:i], true
}
