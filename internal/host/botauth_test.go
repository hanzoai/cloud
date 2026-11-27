package host

import (
	"context"
	"sync"
	"testing"

	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/botauth"
	"github.com/hanzoai/cloud/sdk/secret"
)

// keyVault is a secret.Store that records which ref each call named and which org
// it acted for.
type keyVault struct {
	mu    sync.Mutex
	held  map[string][]byte
	refs  []string
	actAs []string
}

func (v *keyVault) note(ctx context.Context, ref string) {
	v.refs = append(v.refs, ref)
	v.actAs = append(v.actAs, cloud.Who(ctx).Org)
}

func (v *keyVault) GetSecret(ctx context.Context, ref string) ([]byte, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.note(ctx, ref)
	b, ok := v.held[ref]
	if !ok {
		return nil, secret.ErrNone
	}
	return b, nil
}

func (v *keyVault) PutSecret(ctx context.Context, ref string, b []byte) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.note(ctx, ref)
	v.held[ref] = b
	return nil
}

func (v *keyVault) DeleteSecret(context.Context, string) error { return nil }
func (v *keyVault) Sign(context.Context, string, []byte) ([]byte, error) {
	return nil, secret.ErrDisabled
}

// TestTheBotKeyIsTheBrandsAndLivesInKMS: the process's signer names the brand's
// API host as its directory, and keeps its key set in KMS under the brand's org,
// acting for that org whatever request the fetch was made for, in the custodied
// botauth subtree.
func TestTheBotKeyIsTheBrandsAndLivesInKMS(t *testing.T) {
	t.Setenv("CLOUD_BRAND", "hanzo")
	vault := &keyVault{held: map[string][]byte{}}
	useBotAuth(vault)
	t.Cleanup(func() { botauth.Use(nil) })

	s := botauth.Default()
	if s == nil || s.Agent != "https://api.hanzo.ai/.well-known/http-message-signatures-directory" {
		t.Fatalf("signer %+v", s)
	}
	// A fetch made for another org's request: the key is still read as hanzo's.
	ctx := cloud.For(context.Background(), "acme")
	if _, err := s.Keys(ctx); err == nil {
		t.Fatal("a fetch found keys in an empty vault")
	}
	if len(vault.held) != 0 {
		t.Fatal("a fetch minted the key; only the directory does")
	}
	keys, err := s.Provision(ctx)
	if err != nil || len(keys) != 1 {
		t.Fatalf("Provision: %d keys, %v", len(keys), err)
	}
	const ref = "orgs/hanzo/botauth/WEB_BOT_AUTH_KEYS@prod"
	if _, ok := vault.held[ref]; !ok || len(vault.held) != 1 {
		t.Fatalf("the key set is not stored at %s: %v", ref, vault.refs)
	}
	for i, r := range vault.refs {
		if r != ref || vault.actAs[i] != "hanzo" {
			t.Errorf("call %d named %s acting for %q", i, r, vault.actAs[i])
		}
	}

	useBotAuth(nil)
	if botauth.Default() != nil {
		t.Fatal("a process with no KMS kept a signer")
	}
}
