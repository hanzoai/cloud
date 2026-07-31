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

// secretOps binds the store client so each op can be a NAMED method rather than a
// closure. It carries the client and no logic — the same shape apps/marketplace's
// registry uses for its plane op, and for the same reason: zipdoc lifts prose from
// a named handler and has nothing to read off a func literal.
type secretOps struct{ c cloud.KMSClient }

// exposeSecrets publishes the store's reads and writes. Mount calls it.
func exposeSecrets(c cloud.KMSClient) {
	if c == nil {
		return
	}
	p := cloud.Plane()
	o := secretOps{c: c}

	zip.Post[plane.SecretIn, plane.Secret](p, "/kms/get", o.get,
		zip.WithOperationID(plane.KMSGet),
		zip.WithSummary("Read one secret"))

	zip.Post[plane.SecretIn, plane.Secret](p, "/kms/put", o.put,
		zip.WithOperationID(plane.KMSPut),
		zip.WithSummary("Write one secret"))

	zip.Post[plane.SecretIn, plane.Secret](p, "/kms/sign", o.sign,
		zip.WithOperationID(plane.KMSSign),
		zip.WithSummary("Sign a payload with a key that never leaves this process"))
}

// Get opens one sealed secret and returns its value to the calling process. This
// op exists because exactly one process holds the store, so every other app has to
// ask it for material it needs; the value travels back over the internal socket
// only, and appears in no log line and in no error.
//
// The ref decides the authority. A ref naming a tenant is served only to a call
// acting for that same tenant, so an app holding one org's context cannot read
// another's. A ref naming no tenant is the deployment's own material and is served
// to any peer, because the socket has already decided who may ask — it is
// mode-0600 and peer-credential authenticated, so the caller is one of our own
// processes.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func (o secretOps) get(ctx context.Context, in *plane.SecretIn) (*plane.Secret, error) {
	if err := authorize(ctx, in.Ref); err != nil {
		return nil, err
	}
	v, err := o.c.GetSecret(ctx, in.Ref)
	if err != nil {
		return nil, fmt.Errorf("kms.get: %w", err)
	}
	return &plane.Secret{Value: v}, nil
}

// Put seals one secret into the store under the given ref, replacing whatever was
// there. The reply is EMPTY on purpose — a write confirms by not failing, and
// echoing the value back would put it on the wire a second time for no reader.
//
// The same ref rule as the read governs the write: a ref naming a tenant is
// accepted only from a call acting for that tenant, so one org's context can never
// plant material in another's namespace.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func (o secretOps) put(ctx context.Context, in *plane.SecretIn) (*plane.Secret, error) {
	if err := authorize(ctx, in.Ref); err != nil {
		return nil, err
	}
	if err := o.c.PutSecret(ctx, in.Ref, in.Value); err != nil {
		return nil, fmt.Errorf("kms.put: %w", err)
	}
	return &plane.Secret{}, nil
}

// Sign returns a signature over the submitted payload, produced by the key the ref
// names. The KEY ITSELF NEVER LEAVES this process — that is the whole point of the
// op: a caller that needs something signed sends the payload rather than fetching
// the key, so signing material has one custodian and no copies.
//
// The value field carries the payload on the way in and the signature on the way
// out; it is never a key. The same ref rule as the read applies, so a tenant's key
// signs only for a call acting for that tenant.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func (o secretOps) sign(ctx context.Context, in *plane.SecretIn) (*plane.Secret, error) {
	if err := authorize(ctx, in.Ref); err != nil {
		return nil, err
	}
	sig, err := o.c.Sign(ctx, in.Ref, in.Value)
	if err != nil {
		return nil, fmt.Errorf("kms.sign: %w", err)
	}
	return &plane.Secret{Value: sig}, nil
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
