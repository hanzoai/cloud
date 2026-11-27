// Copyright © 2026 Hanzo AI. MIT License.

package kms

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/secret"
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
type secretOps struct {
	c secret.Store
	a *attest
}

// exposeSecrets publishes the store's reads and writes. Mount calls it.
func exposeSecrets(c secret.Store) {
	if c == nil {
		return
	}
	p := cloud.Plane()
	o := secretOps{c: c, a: newAttest(context.Background())}

	p.Post("/kms/get", o.get,
		zip.WithOperationID(client.KMSGet),
		zip.WithSummary("Read one secret"))

	p.Post("/kms/put", o.put,
		zip.WithOperationID(client.KMSPut),
		zip.WithSummary("Write one secret"))

	p.Post("/kms/sign", o.sign,
		zip.WithOperationID(client.KMSSign),
		zip.WithSummary("Sign a payload with a key that never leaves this process"))

	p.Post("/kms/delete", o.del,
		zip.WithOperationID(client.KMSDel),
		zip.WithSummary("Forget one secret"))
}

// Delete forgets one secret. It is here for the same reason put is: exactly one
// process holds the store, so an app that custodies a credential on a customer's
// behalf must be able to REMOVE it when that customer disconnects — otherwise
// disconnecting leaves the material behind and the connection row is the only
// thing that goes.
//
// It widens no boundary. The surface is deliberately narrow because material
// LEAVING is the risk, and delete moves nothing outward; a caller that can put can
// already overwrite a secret into uselessness, so this adds no destructive power
// either. The same ref rule as every other op applies, so a tenant's material is
// removable only by a call acting for that tenant.
func (o secretOps) del(ctx context.Context, in *client.SecretIn) (*client.Secret, error) {
	if err := o.authorize(ctx, in.Ref, Write); err != nil {
		return nil, err
	}
	switch err := o.c.DeleteSecret(ctx, in.Ref); {
	case errors.Is(err, secret.ErrNone):
		// A 404 on the wire, which the caller's KMSPeer reads back as ErrNoSecret.
		return nil, zip.ErrNotFound("kms.delete: no such secret")
	case err != nil:
		return nil, fmt.Errorf("kms.delete: %w", err)
	}
	return &client.Secret{}, nil
}

// Get opens one sealed secret and returns its value to the calling process. This
// op exists because exactly one process holds the store, so every other app has to
// ask it for material it needs; the value travels back over the internal socket
// only, and appears in no log line and in no error.
//
// The ref decides the authority. A ref naming a tenant is served only to a call
// acting for that same tenant, so an app holding one org's context cannot read
// another's. A ref naming no tenant is the deployment's own material and is served
// only to the custodian — this process and the plugins it hosts, which the
// kernel names in this PID namespace — because every other pod on the node, and
// every process outside this one's namespace, has no standing on this plane.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func (o secretOps) get(ctx context.Context, in *client.SecretIn) (*client.Secret, error) {
	if err := o.authorize(ctx, in.Ref, Read); err != nil {
		return nil, err
	}
	v, err := o.c.GetSecret(ctx, in.Ref)
	switch {
	case errors.Is(err, secret.ErrNone):
		// A 404 on the wire, which the caller's KMSPeer reads back as ErrNoSecret.
		return nil, zip.ErrNotFound("kms.get: no such secret")
	case err != nil:
		return nil, fmt.Errorf("kms.get: %w", err)
	}
	return &client.Secret{Value: v}, nil
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
func (o secretOps) put(ctx context.Context, in *client.SecretIn) (*client.Secret, error) {
	if err := o.authorize(ctx, in.Ref, Write); err != nil {
		return nil, err
	}
	if err := o.c.PutSecret(ctx, in.Ref, in.Value); err != nil {
		return nil, fmt.Errorf("kms.put: %w", err)
	}
	return &client.Secret{}, nil
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
func (o secretOps) sign(ctx context.Context, in *client.SecretIn) (*client.Secret, error) {
	if err := o.authorize(ctx, in.Ref, Write); err != nil {
		return nil, err
	}
	sig, err := o.c.Sign(ctx, in.Ref, in.Value)
	if err != nil {
		return nil, fmt.Errorf("kms.sign: %w", err)
	}
	return &client.Secret{Value: sig}, nil
}

// authorize decides one call: who the kernel and the kubelet say is asking,
// what the call states it acts for, and which tenant the ref names, all in
// [admit]. A call with no transport behind it is this process asking itself,
// which is the custodian.
//
// The grant rule (policy.go) does not apply here. Every caller this admits is
// the custodian — the process holding the master key, or one it hosts — or a
// tenant confined to its own org, and a grant on the process that can open the
// store itself would restrict nothing.
func (o secretOps) authorize(ctx context.Context, ref string, v Verb) error {
	who := custodian()
	if !zip.Local(ctx) {
		var err error
		if who, err = o.a.of(ctx); err != nil {
			return err
		}
	}
	return admit(ref, cloud.Who(ctx).Org, who, v)
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
