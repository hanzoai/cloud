// Copyright © 2026 Hanzo AI. MIT License.

package planetest

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/hanzoai/cloud/client"
	"github.com/zap-proto/zip"
)

// KMS is the kms app's half of the plane, holding secrets by ref. It admits what
// the real store admits for a tenant ref (apps/kms attest.go admit): a call
// acting for the org the ref names, and no other. A secret it does not hold is
// 404, which the caller's cloud.KMSPeer reads back as cloud.ErrNoSecret.
type KMS struct {
	mu     sync.Mutex
	values map[string][]byte
	asked  []string
}

// ServeKMS stands the kms peer up on this test's runtime directory.
func ServeKMS(t *testing.T) *KMS {
	t.Helper()
	runtimeDir(t)
	k := &KMS{values: map[string][]byte{}}
	app := zip.New(zip.Config{AppName: "kms"})
	app.Post("/kms/get", func(ctx context.Context, in *client.SecretIn) (*client.Secret, error) {
		if err := k.admit(ctx, in.Ref); err != nil {
			return nil, err
		}
		k.mu.Lock()
		defer k.mu.Unlock()
		v, ok := k.values[in.Ref]
		if !ok {
			return nil, zip.ErrNotFound("kms.get: no such secret")
		}
		return &client.Secret{Value: append([]byte(nil), v...)}, nil
	}, zip.WithOperationID(client.KMSGet))
	app.Post("/kms/put", func(ctx context.Context, in *client.SecretIn) (*client.Secret, error) {
		if err := k.admit(ctx, in.Ref); err != nil {
			return nil, err
		}
		k.Put(in.Ref, string(in.Value))
		return &client.Secret{}, nil
	}, zip.WithOperationID(client.KMSPut))
	app.Post("/kms/delete", func(ctx context.Context, in *client.SecretIn) (*client.Secret, error) {
		if err := k.admit(ctx, in.Ref); err != nil {
			return nil, err
		}
		k.mu.Lock()
		defer k.mu.Unlock()
		if _, ok := k.values[in.Ref]; !ok {
			return nil, zip.ErrNotFound("kms.delete: no such secret")
		}
		delete(k.values, in.Ref)
		return &client.Secret{}, nil
	}, zip.WithOperationID(client.KMSDel))
	listen(t, app, "kms")
	return k
}

// admit records the call and refuses a tenant ref asked for another org.
func (k *KMS) admit(ctx context.Context, ref string) error {
	org := zip.CallerOf(ctx).Org
	k.mu.Lock()
	k.asked = append(k.asked, org+" "+ref)
	k.mu.Unlock()
	if strings.HasPrefix(ref, "orgs/") && !strings.HasPrefix(ref, "orgs/"+org+"/") {
		return zip.ErrForbidden("kms: ref " + ref + " belongs to another org than " + org)
	}
	return nil
}

// Put holds value at ref; "" removes it.
func (k *KMS) Put(ref, value string) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if value == "" {
		delete(k.values, ref)
		return
	}
	k.values[ref] = []byte(value)
}

// Get is the value held at ref, "" when none.
func (k *KMS) Get(ref string) string {
	k.mu.Lock()
	defer k.mu.Unlock()
	return string(k.values[ref])
}

// Asked is every call as "<org the call acted for> <ref>".
func (k *KMS) Asked() []string {
	k.mu.Lock()
	defer k.mu.Unlock()
	return append([]string(nil), k.asked...)
}
