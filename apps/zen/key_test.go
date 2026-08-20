// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package zen

import (
	"context"
	"errors"
	"testing"
)

// stubKMS is a KMSClient whose GetSecret returns a sealed value when present or a
// not-found error otherwise — mirroring the co-resident store that, in production,
// holds no upstream provider keys (they are provisioned as KMS-injected env).
type stubKMS struct{ sealed map[string]string }

func (s stubKMS) GetSecret(_ context.Context, ref string) ([]byte, error) {
	if v, ok := s.sealed[ref]; ok {
		return []byte(v), nil
	}
	return nil, errors.New("secret not found")
}
func (stubKMS) PutSecret(context.Context, string, []byte) error      { return nil }
func (stubKMS) DeleteSecret(context.Context, string) error           { return nil }
func (stubKMS) Sign(context.Context, string, []byte) ([]byte, error) { return nil, nil }

// TestZenKeyResolver_EnvFallback keeps the environment as the FALLBACK. Provider
// keys are provisioned as env today and are not all sealed, so a store miss must
// resolve to the env value rather than "" — an empty bearer upstream is a 401 that
// reads to the caller as a failed chat. Asking the store first costs nothing here.
func TestZenKeyResolver_EnvFallback(t *testing.T) {
	const env = "DO_AI_API_KEY"
	t.Setenv(env, "env-provisioned-key")

	// KMS store has no upstream keys (the real deployment state) — resolve from env.
	if got := zenKeyResolver(stubKMS{})(context.Background(), env); got != "env-provisioned-key" {
		t.Fatalf("KMS-miss: got %q, want env value", got)
	}

	// A nil KMS client (KMS disabled) — still resolve from env.
	if got := zenKeyResolver(nil)(context.Background(), env); got != "env-provisioned-key" {
		t.Fatalf("nil-KMS: got %q, want env value", got)
	}
}

// TestZenKeyResolver_SealedTakesPrecedence pins the resolution ORDER: the store is
// read FIRST, the environment second — the same order ai uses (object/kms.go
// resolveSecretName). This is what makes sealing a key mean anything: with the
// environment first, a sealed key changes nothing while a stale entry sits beside
// it, so the entry can never be removed and the value stays readable to anything
// that can reach the process.
func TestZenKeyResolver_SealedTakesPrecedence(t *testing.T) {
	const env = "ANTHROPIC_API_KEY"
	t.Setenv(env, "env-key")
	got := zenKeyResolver(stubKMS{sealed: map[string]string{env: "sealed-key"}})(context.Background(), env)
	if got != "sealed-key" {
		t.Fatalf("got %q, want sealed-key — sealing is inert if the environment wins", got)
	}
}

// TestZenKeyResolver_AbsentEverywhere keeps the fail-fast contract: absent from both
// surfaces resolves to "" so zen refuses rather than serving for free.
func TestZenKeyResolver_AbsentEverywhere(t *testing.T) {
	if got := zenKeyResolver(stubKMS{})(context.Background(), "MISSING_KEY_XYZ"); got != "" {
		t.Fatalf("got %q, want empty", got)
	}
}
