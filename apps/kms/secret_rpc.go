// Copyright © 2026 Hanzo AI. MIT License.

package kms

import (
	"context"
	"fmt"
	"strings"

	"github.com/hanzoai/cloud"
)

// KMS on the internal plane.
//
// Exactly one process holds the sealed store, so every other app has to ask it —
// and until now there was nothing to ask. clients.KMSRPCAt returned a stub that
// errored on every method ("zapc-gen pending"), and pickKMSClient fell through to
// DisabledKMS, so an app in its own binary silently had no secrets at all. That is
// how a configured mail provider read back as "no email provider configured for
// org": the config was stored, by the process that owns the store, and read by one
// that could not reach it.
//
// The methods are the KMSClient interface and nothing more. A secret store's RPC
// surface is the one place where "while I'm here, expose the rest" is how a
// tenant's material leaves the boundary that protects it.
const (
	getMethod  = "kms.get"
	putMethod  = "kms.put"
	signMethod = "kms.sign"
)

// exposeSecrets publishes the store's reads and writes. Mount calls it.
func exposeSecrets(c cloud.KMSClient) {
	if c == nil {
		return
	}
	// A ref that names a tenant must match the tenant the call is acting for. That
	// catches an app asking for one org's material while acting for another, which
	// is a bug worth failing on.
	//
	// A ref that names none is the DEPLOYMENT's own material and is served to any
	// peer, because the socket already decided who may ask: it is 0600 and
	// SO_PEERCRED-authenticated, so a caller here is one of our own processes. A
	// second gate derived from config would not add a boundary, only a spelling of
	// one that already exists — and Ident.Admin, the real SuperAdmin predicate, is
	// minted from a validated token at the edge and cannot be asserted by a
	// background call at all.
	authorize := func(who cloud.Ident, ref string) error {
		org, ok := tenantOf(ref)
		if !ok {
			return nil
		}
		if who.Org != org {
			return fmt.Errorf("kms: ref %q belongs to %q but the call acts for %q", ref, org, who.Org)
		}
		return nil
	}
	decodeRef := func(who cloud.Ident, req []byte) (string, []byte, error) {
		ref, value, err := cloud.SecretMsg(req)
		if err != nil {
			return "", nil, err
		}
		if ref == "" {
			return "", nil, fmt.Errorf("kms: empty ref")
		}
		return ref, value, authorize(who, ref)
	}
	cloud.Expose(getMethod, func(ctx context.Context, who cloud.Ident, req []byte) ([]byte, error) {
		ref, _, err := decodeRef(who, req)
		if err != nil {
			return nil, err
		}
		v, err := c.GetSecret(ctx, ref)
		if err != nil {
			return nil, fmt.Errorf("kms.get: %w", err)
		}
		return cloud.PutSecretMsg("", v), nil
	})

	cloud.Expose(putMethod, func(ctx context.Context, who cloud.Ident, req []byte) ([]byte, error) {
		ref, raw, err := decodeRef(who, req)
		if err != nil {
			return nil, err
		}
		if err := c.PutSecret(ctx, ref, raw); err != nil {
			return nil, fmt.Errorf("kms.put: %w", err)
		}
		return cloud.PutSecretMsg("", nil), nil
	})

	cloud.Expose(signMethod, func(ctx context.Context, who cloud.Ident, req []byte) ([]byte, error) {
		ref, payload, err := decodeRef(who, req)
		if err != nil {
			return nil, err
		}
		sig, err := c.Sign(ctx, ref, payload)
		if err != nil {
			return nil, fmt.Errorf("kms.sign: %w", err)
		}
		return cloud.PutSecretMsg("", sig), nil
	})
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
