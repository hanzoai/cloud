package provisioning

import (
	"context"
	"github.com/hanzoai/cloud/types"
	"errors"

	luxlog "github.com/luxfi/log"
)

// secrets wraps cloud's MPC-sealing client (clients/mpc) for
// storing provisioned-resource passwords. Encryption is client-side: the CEK
// is derived from CLOUD_KMS_PASSPHRASE and never leaves this process; the MPC
// nodes only ever see ciphertext.
//
// SAFE DEGRADE: if KMS is not configured (CLOUD_KMS_NODES or
// CLOUD_KMS_PASSPHRASE empty) or cannot be unlocked, Enabled() is false. In
// that mode we NEVER write a plaintext password anywhere persistent — the
// create handler returns the generated password exactly once in the HTTP
// response and stores only metadata (secret_ref left empty). This honors the
// hard rule: never store a password in plaintext.
type secrets struct {
	client  types.KMSClient
	enabled bool
	log     luxlog.Logger
}

// newSecrets takes the deployment's KMS rather than opening one. build.go
// constructs it once, from the one bootstrap env, and hands it to every subsystem
// that custodies a credential — so a second opener cannot disagree with it about
// which org's namespace, which quorum, or what an absent passphrase means.
//
// A nil client is the degraded mode this package has always had: Enabled() is
// false, nothing plaintext is ever written, and the create handler returns the
// generated password once in its response while the row stores only metadata.
func newSecrets(k types.KMSClient, log luxlog.Logger) *secrets {
	return &secrets{client: k, enabled: k != nil, log: log}
}

// Enabled reports whether secrets can be persisted to KMS.
func (s *secrets) Enabled() bool { return s != nil && s.enabled }

// Put seals value under ref. Only call when Enabled() is true.
func (s *secrets) Put(ctx context.Context, ref string, value []byte) error {
	if !s.Enabled() {
		return errors.New("provisioning: KMS disabled")
	}
	return s.client.PutSecret(ctx, ref, value)
}

// Get returns the sealed value for ref.
func (s *secrets) Get(ctx context.Context, ref string) ([]byte, error) {
	if !s.Enabled() {
		return nil, errors.New("provisioning: KMS disabled")
	}
	return s.client.GetSecret(ctx, ref)
}

// Delete removes the sealed secret. Best-effort; no-op when degraded.
func (s *secrets) Delete(ctx context.Context, ref string) error {
	if !s.Enabled() {
		return nil
	}
	return s.client.DeleteSecret(ctx, ref)
}
