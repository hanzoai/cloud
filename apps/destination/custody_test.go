package destination

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"testing"

	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/secret"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// plane is the secret plane as a process that does not hold the store meets it:
// the kms app over the internal plane (cloud.KMSPeer), here a map. It records the
// org each call acted for, because the store serves orgs/<org>/… to that org alone.
type plane struct {
	mu     sync.Mutex
	held   map[string][]byte
	actFor []string
}

func (p *plane) note(ctx context.Context) {
	p.actFor = append(p.actFor, cloud.Who(ctx).Org)
}

func (p *plane) GetSecret(ctx context.Context, ref string) ([]byte, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.note(ctx)
	v, ok := p.held[ref]
	if !ok {
		return nil, fmt.Errorf("kms get: %w", secret.ErrNone)
	}
	return v, nil
}

func (p *plane) PutSecret(ctx context.Context, ref string, value []byte) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.note(ctx)
	p.held[ref] = value
	return nil
}

func (p *plane) DeleteSecret(ctx context.Context, ref string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.note(ctx)
	if _, ok := p.held[ref]; !ok {
		return fmt.Errorf("kms del: %w", secret.ErrNone)
	}
	delete(p.held, ref)
	return nil
}

func (p *plane) Sign(context.Context, string, []byte) ([]byte, error) {
	return nil, fmt.Errorf("not a signer")
}

// TestConnectSealsThroughThePlane connects Meta the way production runs it, with the
// store in another process: the token is sealed at orgs/<org>/destinations/meta,
// acting for the org, the card reads live, and a disconnect forgets it. Before, the
// subsystem took only the in-process store, and every connect in production answered
// 503 "master key not configured".
func TestConnectSealsThroughThePlane(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		kms := &plane{held: map[string][]byte{}}
		app := zip.New(zip.Config{Logger: luxlog.New("test")})
		compose(app)
		t.Setenv(publicFanoutEnv, "0")
		t.Setenv("CLOUD_DATA_DIR", t.TempDir())
		if err := Use(app, cloud.Deps{KMS: kms, MasterKey: testMaster}); err != nil {
			t.Fatalf("Use: %v", err)
		}
		t.Cleanup(func() { _ = Shutdown() })

		// A required field missing is refused before anything is sealed or stored.
		if code, _ := do(t, app, http.MethodPost, "/v1/destination/meta", "hanzo", true, map[string]any{"accessToken": "EAAB"}); code != http.StatusBadRequest {
			t.Fatalf("connect with no pixel id: %d, want 400", code)
		}
		if len(kms.held) != 0 {
			t.Fatalf("a refused connect sealed %v", kms.held)
		}
		// A member who is not an admin of the org connects nothing.
		if code, _ := do(t, app, http.MethodPost, "/v1/destination/meta", "hanzo", false, map[string]any{"pixelId": "1790308611893001", "accessToken": "EAAB"}); code != http.StatusForbidden {
			t.Fatalf("a non-admin connect: %d, want 403", code)
		}

		code, b := do(t, app, http.MethodPost, "/v1/destination/meta", "hanzo", true, map[string]any{"pixelId": "1790308611893001", "testEventCode": "TEST1", "accessToken": "EAAB"})
		if code != http.StatusOK {
			t.Fatalf("connect: %d %s", code, b)
		}
		var card DestinationStatus
		if err := json.Unmarshal(b, &card); err != nil || !card.Connected || !card.Live || card.Config["pixelId"] != "1790308611893001" {
			t.Fatalf("card = %+v, %v", card, err)
		}
		const ref = "orgs/hanzo/destinations/meta/access_token@default"
		if string(kms.held[ref]) != "EAAB" || len(kms.held) != 1 {
			t.Fatalf("sealed %v, want the token at %s alone", kms.held, ref)
		}
		if bytes.Contains(b, []byte("EAAB")) {
			t.Fatal("the card carries the token")
		}
		for _, org := range kms.actFor {
			if org != "hanzo" {
				t.Fatalf("a secret-plane call acted for %q, want hanzo: %v", org, kms.actFor)
			}
		}

		// Another org reads its own namespace, never hanzo's.
		code, b = do(t, app, http.MethodGet, "/v1/destination/meta", "acme", false, nil)
		if err := json.Unmarshal(b, &card); code != http.StatusOK || err != nil || card.Live || card.Connected {
			t.Fatalf("acme's card = %d %+v", code, card)
		}

		if code, _ := do(t, app, http.MethodDelete, "/v1/destination/meta", "hanzo", true, nil); code != http.StatusOK {
			t.Fatalf("disconnect: %d", code)
		}
		if len(kms.held) != 0 {
			t.Fatalf("disconnect left %v", kms.held)
		}
	})
}

// TestConnectFailsClosedWithNoPlane: a process with no secret plane stores nothing.
func TestConnectFailsClosedWithNoPlane(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountApp(t)
		if code, _ := do(t, app, http.MethodPost, "/v1/destination/meta", "hanzo", true, map[string]any{"pixelId": "1", "accessToken": "EAAB"}); code != http.StatusServiceUnavailable {
			t.Fatalf("connect with no plane: %d, want 503", code)
		}
		if code, b := do(t, app, http.MethodGet, "/v1/destination/meta", "hanzo", false, nil); code != http.StatusOK || bytes.Contains(b, []byte(`"connected":true`)) {
			t.Fatalf("read: %d %s", code, b)
		}
	})
}
