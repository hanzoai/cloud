package link

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// fakeKMS is a KMSClient over an in-memory map. It records every ref it was asked
// for, so a test can prove the resolver only ever reads within one org's namespace.
type fakeKMS struct {
	secrets map[string][]byte
	asked   []string
}

func (k *fakeKMS) GetSecret(_ context.Context, ref string) ([]byte, error) {
	k.asked = append(k.asked, ref)
	if v, ok := k.secrets[ref]; ok {
		return v, nil
	}
	return nil, fmt.Errorf("kms: secret not found: %s", ref)
}
func (k *fakeKMS) PutSecret(context.Context, string, []byte) error      { return nil }
func (k *fakeKMS) Sign(context.Context, string, []byte) ([]byte, error) { return nil, nil }

func TestKMSRefConvention(t *testing.T) {
	if got := KMSRef("acme", Account{"openai", "work"}); got != "orgs/acme/providers/openai/work" {
		t.Errorf("KMSRef = %q", got)
	}
	if got := KMSRef("acme", Account{"anthropic", ""}); got != "orgs/acme/providers/anthropic/default" {
		t.Errorf("default-profile KMSRef = %q", got)
	}
	// The org is the FIRST path segment, so no provider/profile can escape the prefix.
	if !strings.HasPrefix(KMSRef("acme", Account{"x", "y"}), "orgs/acme/") {
		t.Error("ref must be prefixed by the org namespace")
	}
}

func TestKMSResolverIsOrgScoped(t *testing.T) {
	kms := &fakeKMS{secrets: map[string][]byte{
		"orgs/acme/providers/openai/work":     []byte("tok-acme-work"),
		"orgs/victimco/providers/openai/paid": []byte("tok-VICTIM"),
	}}
	r := NewKMSResolver(kms)

	// acme resolves its own account.
	c, err := r.Resolve(context.Background(), "acme", "alice", Account{"openai", "work"})
	if err != nil || c.Token != "tok-acme-work" {
		t.Fatalf("acme resolve: token=%q err=%v", c.Token, err)
	}
	// Resolving for acme reads ONLY orgs/acme/... — never victimco's ref.
	for _, ref := range kms.asked {
		if !strings.HasPrefix(ref, "orgs/acme/") {
			t.Fatalf("resolver read outside acme's namespace: %s", ref)
		}
	}
	// An account acme does not have is a not-found error, never victimco's token.
	if _, err := r.Resolve(context.Background(), "acme", "alice", Account{"openai", "paid"}); err == nil {
		t.Fatal("acme must not resolve an account it has no sealed ref for")
	}
}

func TestKMSResolverFailsClosed(t *testing.T) {
	r := NewKMSResolver(&fakeKMS{secrets: map[string][]byte{}})
	if _, err := r.Resolve(context.Background(), "", "alice", Account{"openai", "work"}); !errors.Is(err, ErrNoPrincipal) {
		t.Fatalf("blank org must fail closed, got %v", err)
	}
	// A nil KMS resolves nothing (never a panic, never a fallback).
	if _, err := NewKMSResolver(nil).Resolve(context.Background(), "acme", "a", Account{"openai", "work"}); err == nil {
		t.Fatal("nil KMS must yield an error, not a credential")
	}
}

func TestDecodeCredential(t *testing.T) {
	// raw token
	c, err := decodeCredential([]byte("sk-raw-key"))
	if err != nil || c.Token != "sk-raw-key" || !c.Expiry.IsZero() {
		t.Fatalf("raw decode: %+v err=%v", c, err)
	}
	// JSON envelope (OAuth / multi-field)
	c, err = decodeCredential([]byte(`{"token":"at-123","header":"x-api-key","scheme":"","expiry":"2030-01-02T15:04:05Z","extra":{"region":"us-east-1"}}`))
	if err != nil || c.Token != "at-123" || c.Header != "x-api-key" || c.Extra["region"] != "us-east-1" {
		t.Fatalf("json decode: %+v err=%v", c, err)
	}
	if c.Expiry.IsZero() {
		t.Fatal("expiry should have parsed")
	}
	// empty → ErrNoCredential
	if _, err := decodeCredential([]byte("   ")); !errors.Is(err, ErrNoCredential) {
		t.Fatalf("empty decode should be ErrNoCredential, got %v", err)
	}
	if _, err := decodeCredential([]byte(`{"token":""}`)); !errors.Is(err, ErrNoCredential) {
		t.Fatalf("empty-token envelope should be ErrNoCredential, got %v", err)
	}
}

func TestCredentialExpiry(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	if (Credential{Token: "k"}).expired(now) {
		t.Error("a non-expiring credential (zero Expiry) must never be expired")
	}
	if !(Credential{Token: "k", Expiry: now.Add(-time.Second)}).expired(now) {
		t.Error("a past-expiry credential must be expired")
	}
	if (Credential{Token: "k", Expiry: now.Add(time.Hour)}).expired(now) {
		t.Error("a future-expiry credential must not be expired")
	}
}
