package principals

import (
	"context"
	"net/http"
	"sync"
	"testing"

	"github.com/hanzoai/cloud/apps/tax"
	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/sqltest"
)

// claims is an Issuer that remembers what it put on chain.
type claims struct {
	mu      sync.Mutex
	issued  map[string]string // claim ref → statement id
	revoked []string
}

func (c *claims) Issue(_ context.Context, s client.Statement) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	ref := "claim:" + s.ID
	c.issued[ref] = s.ID
	return ref, nil
}

func (c *claims) Revoke(_ context.Context, ref string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.revoked = append(c.revoked, ref)
	return nil
}

// With an issuer bound, every credential signed is claimed on chain once, and
// every one withdrawn has its claim revoked — the binding's whole contract, held
// against a stand-in until the contract is deployed.
func TestTaxCredentialClaimsOnChain(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		e := newEnv(t)
		c := &claims{issued: map[string]string{}}
		mounted.State.claims = c
		e.must(201, http.MethodPost, "/v1/principal/keys", reviewer("admin"), nil, nil)
		e.agent("acme", "scout", "")
		e.w9("acme", tax.CCorp, tax.EIN, acmeEIN)
		e.certify("acme")
		if len(c.issued) != 1 {
			t.Fatalf("claimed %d credentials on chain, want 1", len(c.issued))
		}
		// Reconciling again changes nothing: a claimed credential is not claimed twice.
		ops{s: mounted}.sweepOnce(context.Background())
		if len(c.issued) != 1 || len(c.revoked) != 0 {
			t.Fatalf("a quiet sweep claimed %d and revoked %d", len(c.issued), len(c.revoked))
		}
		e.w9("acme", tax.CCorp, tax.EIN, "98-7654321")
		if len(c.revoked) != 1 {
			t.Fatalf("revoked %d claims when the form changed, want 1", len(c.revoked))
		}
	})
}
