package destinations

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/analytics"
	"github.com/hanzoai/cloud/clients/kms"
	luxlog "github.com/luxfi/log"
)

// fakeDest records the batch + resolved secret it was handed, so a fan-out test can
// assert what actually reached the platform boundary. It declares a KMS secret so the
// credential-resolution path is exercised.
type fakeDest struct {
	mu      sync.Mutex
	secret  string
	batch   []Conversion
	fallbck string
}

func (f *fakeDest) ID() string       { return "fake" }
func (f *fakeDest) Name() string     { return "Fake" }
func (f *fakeDest) Category() string { return categoryAdvertising }
func (f *fakeDest) Spec() Spec {
	return Spec{Fields: []Field{{Key: "pixelId", Required: true}}, Secrets: []string{"access_token"}, Fallback: f.fallbck}
}
func (f *fakeDest) Send(_ context.Context, _ Config, secret string, batch []Conversion) (Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.secret = secret
	f.batch = batch
	return Result{Sent: len(batch)}, nil
}

func newKMS(t *testing.T) *kms.Client {
	t.Helper()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatalf("rand: %v", err)
	}
	kc, err := kms.New(kms.Config{DataDir: t.TempDir(), MasterKeyB64: base64.StdEncoding.EncodeToString(key)}, luxlog.New("test"))
	if err != nil {
		t.Fatalf("kms.New: %v", err)
	}
	t.Cleanup(func() { _ = kc.Close() })
	return kc
}

func testService(t *testing.T, kc *kms.Client, dests map[string]Destination) *cloud.Service[state] {
	t.Helper()
	return &cloud.Service[state]{
		Base:  cloud.Base{Log: luxlog.New("test"), Brand: "hanzo"},
		State: state{store: testStore(t), kms: kc, dests: dests, sem: make(chan struct{}, maxFanout)},
	}
}

// TestFanOutEndToEnd is the load-bearing fan-out test: an enabled destination with a
// KMS-sealed secret receives the TRANSLATED batch, scoped to its own org.
func TestFanOutEndToEnd(t *testing.T) {
	ctx := context.Background()
	kc := newKMS(t)
	fd := &fakeDest{}
	s := testService(t, kc, map[string]Destination{"fake": fd})

	// Seal the destination's secret + connect it enabled for org "acme".
	if err := kc.Put(kmsPath("acme", "fake"), "access_token", kmsEnv, []byte("s3cr3t")); err != nil {
		t.Fatalf("seal: %v", err)
	}
	if err := s.State.store.Upsert(ctx, Row{Org: "acme", Platform: "fake", Enabled: true, Config: Config{"pixelId": "px"}}); err != nil {
		t.Fatalf("connect: %v", err)
	}

	consume(s, "acme", []analytics.SinkEvent{
		{Name: "order_completed", DistinctID: "u1", Revenue: 49, Currency: "usd", MessageID: "m1", Time: time.Now()},
		{Name: "$pageview", DistinctID: "u1", MessageID: "m2", Time: time.Now()},
	})

	fd.mu.Lock()
	defer fd.mu.Unlock()
	if fd.secret != "s3cr3t" {
		t.Fatalf("destination got secret %q, want the sealed one", fd.secret)
	}
	if len(fd.batch) != 2 {
		t.Fatalf("destination got %d conversions, want 2", len(fd.batch))
	}
	if fd.batch[0].Standard != EventPurchase || fd.batch[0].Value != 49 || fd.batch[0].Currency != "USD" {
		t.Fatalf("first conversion not translated: %+v", fd.batch[0])
	}
}

// TestFanOutSkipsDisabledAndForeign verifies a disabled destination is never sent to,
// and one org's events never reach another org's destination.
func TestFanOutSkipsDisabledAndForeign(t *testing.T) {
	ctx := context.Background()
	kc := newKMS(t)
	fd := &fakeDest{}
	s := testService(t, kc, map[string]Destination{"fake": fd})
	_ = kc.Put(kmsPath("acme", "fake"), "access_token", kmsEnv, []byte("tok"))
	// Disabled for acme.
	_ = s.State.store.Upsert(ctx, Row{Org: "acme", Platform: "fake", Enabled: false, Config: Config{"pixelId": "px"}})

	consume(s, "acme", []analytics.SinkEvent{{Name: "order_completed", MessageID: "m", Time: time.Now()}})
	fd.mu.Lock()
	if fd.batch != nil {
		t.Fatal("disabled destination must not be sent to")
	}
	fd.mu.Unlock()

	// A DIFFERENT org with no connection: consume is a no-op for it.
	consume(s, "other", []analytics.SinkEvent{{Name: "order_completed", MessageID: "m", Time: time.Now()}})
	fd.mu.Lock()
	if fd.batch != nil {
		t.Fatal("foreign org must not reach acme's destination")
	}
	fd.mu.Unlock()
}

// TestResolveSecretKMSThenFallbackError verifies KMS resolution and the fail-closed
// error when neither KMS nor a fallback yields a credential.
func TestResolveSecret(t *testing.T) {
	kc := newKMS(t)
	fd := &fakeDest{}
	s := testService(t, kc, map[string]Destination{"fake": fd})

	// No secret sealed, no fallback → error (fail-closed).
	if _, err := resolveSecret(s, "acme", fd, Config{}); err == nil {
		t.Fatal("no credential must error")
	}
	// Seal → resolves.
	_ = kc.Put(kmsPath("acme", "fake"), "access_token", kmsEnv, []byte("tok"))
	got, err := resolveSecret(s, "acme", fd, Config{})
	if err != nil || got != "tok" {
		t.Fatalf("resolveSecret = %q, %v", got, err)
	}
}

// TestSeamConnect verifies the guide-facing in-process Connect seam provisions
// non-secret config, requires the platform's required fields, and reports live=false
// without a credential.
func TestSeamConnect(t *testing.T) {
	kc := newKMS(t)
	fd := &fakeDest{}
	s := testService(t, kc, map[string]Destination{"fake": fd})
	mounted = s
	t.Cleanup(func() { mounted = nil })

	// Missing the required pixelId → honest error, nothing provisioned.
	if _, err := Connect(context.Background(), "acme", "fake", map[string]any{}); err == nil {
		t.Fatal("Connect without the required field must error")
	}
	// With the id → provisioned; live=false (no secret sealed).
	st, err := Connect(context.Background(), "acme", "fake", map[string]any{"pixelId": "px"})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if !st.Connected || st.Live {
		t.Fatalf("status: connected=%v live=%v (want connected, not live)", st.Connected, st.Live)
	}
	if st.Config["pixelId"] != "px" {
		t.Fatalf("config not stored: %+v", st.Config)
	}
	// Seal the secret → List reports live.
	_ = kc.Put(kmsPath("acme", "fake"), "access_token", kmsEnv, []byte("tok"))
	list, err := List(context.Background(), "acme")
	if err != nil || len(list) != 1 || !list[0].Live {
		t.Fatalf("List after sealing secret: %+v (err %v)", list, err)
	}
}
