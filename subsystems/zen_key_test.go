// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package subsystems

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
func (stubKMS) Sign(context.Context, string, []byte) ([]byte, error) { return nil, nil }

// TestZenKeyResolver_EnvFallback pins the production wiring: the upstream provider
// key is provisioned as env (from the cloud-api-llm-keys secret), NOT sealed in the
// co-resident KMS store, so a KMS miss must fall back to the env value rather than
// returning "" (which would send an empty bearer upstream → provider 401).
func TestZenKeyResolver_EnvFallback(t *testing.T) {
	const env = "DO_AI_API_KEY"
	t.Setenv(env, "env-provisioned-key")

	// KMS store has no upstream keys (the real deployment state) — fall back to env.
	if got := zenKeyResolver(stubKMS{})(context.Background(), env); got != "env-provisioned-key" {
		t.Fatalf("KMS-miss: got %q, want env fallback", got)
	}

	// A nil KMS client (KMS disabled) — still resolve from env.
	if got := zenKeyResolver(nil)(context.Background(), env); got != "env-provisioned-key" {
		t.Fatalf("nil-KMS: got %q, want env fallback", got)
	}
}

// TestZenKeyResolver_SealedTakesPrecedence keeps the sealed-at-rest path first: a
// value explicitly sealed in the co-resident KMS store wins over env, so completing
// the sealed-store provisioning later needs no code change.
func TestZenKeyResolver_SealedTakesPrecedence(t *testing.T) {
	const env = "ANTHROPIC_API_KEY"
	t.Setenv(env, "env-key")
	got := zenKeyResolver(stubKMS{sealed: map[string]string{env: "sealed-key"}})(context.Background(), env)
	if got != "sealed-key" {
		t.Fatalf("got %q, want sealed-key (KMS precedence)", got)
	}
}

// TestZenKeyResolver_AbsentEverywhere keeps the fail-fast contract: absent from both
// surfaces resolves to "" so zen refuses rather than serving for free.
func TestZenKeyResolver_AbsentEverywhere(t *testing.T) {
	if got := zenKeyResolver(stubKMS{})(context.Background(), "MISSING_KEY_XYZ"); got != "" {
		t.Fatalf("got %q, want empty", got)
	}
}
