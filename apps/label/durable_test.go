package label

// durable_test.go is the ship-before-ack proof, and it is the one property this
// plane cannot be honest without.
//
// cloud deploys strategy Recreate at one replica: every rollout is a hard
// teardown, and the pod that comes up hydrates the org's DURABLE snapshot over
// whatever is on local disk. A write that was acknowledged and never shipped is
// therefore not merely at risk of being lost — it is overwritten by an older copy
// of the same tenant's history, silently, on the ordinary deploy path.
//
// The suite's other restart test closes every handle first, which is the GRACEFUL
// case and the one that always worked. This one never closes the owner: a
// successor pod with its own empty data directory takes over and reads what the
// durable object holds, which is exactly what a Recreate rollout does and exactly
// what an unshipped write does not survive.
//
// The successor opens what it restored under the key cek DERIVES from the same
// process master and the same namespace, so nothing has to travel beside the
// file. The in-process CAS below is the same double apps/research uses for the
// same proof. It is duplicated rather than shared because it is a test fixture in a
// package-private test file; the CONTRACT it stands in for — org.Durability over
// a conditional store — is the shared thing, and both planes call it identically.

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/internal/org"
	sqlitedrv "github.com/hanzoai/sqlite"
	"github.com/hanzoai/vfs/replica"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// memCAS is an in-process replica.ConditionalStore: one atomic (data, generation)
// slot per key, a single mutex making PutIfVersion's compare-and-set indivisible —
// the server-side CAS a real SeaweedFS S3 gateway provides. Two "pods" share ONE.
type memCAS struct {
	mu   sync.Mutex
	objs map[string]memObj
}

type memObj struct {
	data []byte
	ver  int
}

func newMemCAS() *memCAS { return &memCAS{objs: map[string]memObj{}} }

func (m *memCAS) Get(_ context.Context, key string) ([]byte, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	o, ok := m.objs[key]
	if !ok {
		return nil, "", replica.ErrNotFound
	}
	return append([]byte(nil), o.data...), strconv.Itoa(o.ver), nil
}

func (m *memCAS) PutIfVersion(_ context.Context, key string, data []byte, expect string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	o, ok := m.objs[key]
	cur := ""
	if ok {
		cur = strconv.Itoa(o.ver)
	}
	if cur != expect {
		return "", fmt.Errorf("%w: have %q want %q", replica.ErrConflict, cur, expect)
	}
	m.objs[key] = memObj{data: append([]byte(nil), data...), ver: o.ver + 1}
	return strconv.Itoa(o.ver + 1), nil
}

// shipCheckpoint mirrors what the composition root wires (build.go: NewDurability
// with WithCheckpoint). Without it the ship reads the real path on a backend that
// has not written it yet: the pure-Go envelope keeps the plaintext on tmpfs and
// only re-encrypts on Checkpoint or Close, so a snapshot taken without this ships
// stale ciphertext — or, on a store never closed, no file at all.
func shipCheckpoint() org.DurabilityOption {
	return org.WithCheckpoint(func(_ context.Context, db *sql.DB) error {
		return sqlitedrv.Checkpoint(db)
	})
}

// soleMembership names id as the only writer-eligible member, so id is the owner
// of every org — a pod that believes it is the sole replica, which is what cloud
// at one replica is.
func soleMembership(t *testing.T, id string) *org.Membership {
	t.Helper()
	m := org.NewMembership(id, org.StaticSource(org.Member{ID: id, Addr: id}), time.Second)
	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("membership start: %v", err)
	}
	t.Cleanup(m.Stop)
	return m
}

// wireDurable mounts the surface over a DURABLE org store, as the deployment
// does. The columnar plane is stubbed so the test is about durability and not
// about a warehouse.
func wireDurable(t *testing.T, dur *org.Durability, w *recorder) (*zip.App, *cloud.Service[*state]) {
	t.Helper()
	s, err := build(cloud.Deps{Logger: luxlog.New("labeldur"), DataDir: t.TempDir(), Brand: "hanzo", Durable: dur})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	s.State.derived = w.plane()
	app := zip.New(zip.Config{Logger: luxlog.New("labeldur"), DisableStartupMessage: true})
	routes(app, s)
	t.Cleanup(func() { _ = s.State.stores.CloseAll() })
	return app, s
}

// TestAnAcknowledgedRecordSurvivesATakeover is the regression.
//
// The owner records a label over the real router and answers 200. Its process is
// then simply GONE — no CloseAll, no Shutdown, which is what an ungraceful
// Recreate is — and a successor pod with an empty data directory serves the same
// org. The record must be there.
//
// Before the fix nothing in this package ever called OrgStore.Sync: the only ship
// was CloseAll on a graceful shutdown, so the successor hydrated a snapshot that
// predated the write and answered zero. Every compliance record acknowledged
// since process start was gone, and the response had already said it was kept.
func TestAnAcknowledgedRecordSurvivesATakeover(t *testing.T) {
	cas := newMemCAS()
	at := time.Now().UTC().Add(-100 * 24 * time.Hour).Truncate(time.Second)

	owner, _ := wireDurable(t, org.NewDurability(cas, soleMembership(t, "pod-owner"), nil, shipCheckpoint()), &recorder{})
	out := post(t, owner, "acme", "u_acme", batch(
		assertion("transaction", "tx-durable", at, at.Add(24*time.Hour), Productive, Dispute, "dp-1", 1)))
	if out.Recorded != 1 {
		t.Fatalf("the owner recorded %d, want 1: %+v", out.Recorded, out.Results)
	}
	// NOTHING IS CLOSED HERE. That is the point.

	succ, _ := wireDurable(t, org.NewDurability(cas, soleMembership(t, "pod-successor"), nil, shipCheckpoint()), &recorder{})
	code, raw := req(t, succ, http.MethodGet, "/v1/risk/labels?subject=tx-durable", "acme", "u_acme", "")
	if code != http.StatusOK {
		t.Fatalf("the successor's read = %d %s", code, raw)
	}
	var got riskLabelsOut
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.Count != 1 {
		t.Fatal("the takeover LOST an acknowledged compliance record: the write was answered 200 and never shipped, so the successor hydrated a snapshot that predates it")
	}
	if got.Labels[0].Evidence != "dp-1" || got.Labels[0].By == "" {
		t.Fatalf("the record survived without its provenance: %+v", got.Labels[0])
	}
}

// TestAWriteOnANonOwnerFailsClosed. On a pod that is NOT the org's elected
// writer, the ship cannot be acked — and a write acknowledged there is a second,
// divergent copy of one tenant's compliance record. It must refuse, and the
// refusal must be one the caller retries on (every write here is idempotent on
// the assertion's content digest, so a retry against the new owner costs
// nothing).
func TestAWriteOnANonOwnerFailsClosed(t *testing.T) {
	cas := newMemCAS()
	const orgID = "acme"
	set := []org.Member{{ID: "pod-a"}, {ID: "pod-b"}}
	elected, _ := org.Owner(orgID, set)
	self := "pod-a"
	if self == elected.ID {
		self = "pod-b"
	}
	m := org.NewMembership(self, org.StaticSource(set...), time.Second)
	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("membership start: %v", err)
	}
	t.Cleanup(m.Stop)

	app, _ := wireDurable(t, org.NewDurability(cas, m, nil, shipCheckpoint()), &recorder{})
	at := time.Now().UTC().Add(-100 * 24 * time.Hour).Truncate(time.Second)
	code, raw := req(t, app, http.MethodPost, "/v1/risk/labels", orgID, "u_acme", batch(
		assertion("transaction", "tx-nonowner", at, at.Add(24*time.Hour), Productive, Dispute, "dp-1", 1)))
	if code != http.StatusServiceUnavailable {
		t.Fatalf("a write on a non-owner = %d %s, want 503: acknowledging it would create a second divergent copy of the tenant's record", code, raw)
	}
}

// TestADeposedWriterDoesNotAcknowledge covers the OTHER unacked ship, and it is
// a different fact from the one above.
//
// Sync answers (false, ErrNotOwner) on a replica that never held the lease — the
// case above — and (false, nil) on one that held it and was DEPOSED between the
// write and the ship: the fenced Put is refused at a stale round, and there is no
// error, only a no. Two shapes, and only the second reaches the `!acked` branch.
// A ship that checked the error alone would acknowledge a record written on a pod
// whose file the next reader never opens.
//
// The deposition is real rather than simulated: the owner writes and ships, a
// second pod opens the same org over the same conditional store and takes the
// lease at a higher round, and the first pod — which has not been told anything —
// writes again. Its ship is fenced out.
func TestADeposedWriterDoesNotAcknowledge(t *testing.T) {
	cas := newMemCAS()
	at := time.Now().UTC().Add(-100 * 24 * time.Hour).Truncate(time.Second)

	owner, _ := wireDurable(t, org.NewDurability(cas, soleMembership(t, "pod-owner"), nil, shipCheckpoint()), &recorder{})
	if out := post(t, owner, "acme", "u_acme", batch(
		assertion("transaction", "tx-before", at, at.Add(24*time.Hour), Productive, Dispute, "dp-before", 1))); out.Recorded != 1 {
		t.Fatalf("the owner recorded %d, want 1", out.Recorded)
	}

	// The successor takes the lease at a higher round. The owner is told nothing:
	// it still believes it is the writer, which is exactly the split it must not
	// acknowledge a record through.
	succ, _ := wireDurable(t, org.NewDurability(cas, soleMembership(t, "pod-successor"), nil, shipCheckpoint()), &recorder{})
	if code, raw := req(t, succ, http.MethodGet, "/v1/risk/labels", "acme", "u_acme", ""); code != http.StatusOK {
		t.Fatalf("the successor's read = %d %s", code, raw)
	}

	code, raw := req(t, owner, http.MethodPost, "/v1/risk/labels", "acme", "u_acme", batch(
		assertion("transaction", "tx-deposed", at, at.Add(24*time.Hour), Productive, Dispute, "dp-deposed", 1)))
	if code != http.StatusServiceUnavailable {
		t.Fatalf("a write on a DEPOSED writer = %d %s, want 503: the ship was refused at a stale round with no error, and answering 200 there acknowledges a record only this pod will ever see", code, raw)
	}

	// And the refusal is honest: the record really is not in the durable copy the
	// next reader opens.
	code, raw = req(t, succ, http.MethodGet, "/v1/risk/labels?subject=tx-deposed", "acme", "u_acme", "")
	if code != http.StatusOK {
		t.Fatalf("the successor's read = %d %s", code, raw)
	}
	var got riskLabelsOut
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.Count != 0 {
		t.Fatalf("the refused write is visible to the successor (%d records), so the 503 described a state that did not happen", got.Count)
	}
}

// TestADisposalIsShippedBeforeItIsAcknowledged is the same contract in the other
// direction. A tenant told its records are gone, on a pod whose disposal was
// never shipped, gets every one of them back when the next pod hydrates.
func TestADisposalIsShippedBeforeItIsAcknowledged(t *testing.T) {
	cas := newMemCAS()
	w := &recorder{}
	owner, s := wireDurable(t, org.NewDurability(cas, soleMembership(t, "pod-owner"), nil, shipCheckpoint()), w)

	// A record old enough to dispose of, written eight years ago — retention
	// measures the SERVER clock at the write, so it has to be planted at that
	// instant rather than posted — AND SHIPPED, so the durable object really
	// holds it. Without that the successor would hydrate an empty snapshot and
	// see zero whether the disposal shipped or not, which is a test that cannot
	// fail.
	ancient := time.Now().UTC().Add(-8 * 365 * 24 * time.Hour).Truncate(time.Second)
	asserted(t, storeOf(t, s, "acme"), "tx-ancient", ancient, ancient, Productive, Dispute, "dp-1", 1)
	if err := s.State.ship(cloud.MustOrgNamespace("acme", "")); err != nil {
		t.Fatalf("the fixture could not be made durable: %v", err)
	}

	body := fmt.Sprintf(`{"before":%q}`, time.Now().UTC().Add(-minRetention-24*time.Hour).Format(time.RFC3339))
	code, raw := req(t, owner, http.MethodPost, "/v1/risk/labels/dispose", "acme", "u_acme", body)
	if code != http.StatusOK {
		t.Fatalf("dispose = %d %s", code, raw)
	}
	var out riskDisposeOut
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if out.Disposed != 1 {
		t.Fatalf("disposed %d, want 1", out.Disposed)
	}
	// The owner is gone, ungracefully.
	succ, _ := wireDurable(t, org.NewDurability(cas, soleMembership(t, "pod-successor"), nil, shipCheckpoint()), &recorder{})
	code, raw = req(t, succ, http.MethodGet, "/v1/risk/labels", "acme", "u_acme", "")
	if code != http.StatusOK {
		t.Fatalf("the successor's read = %d %s", code, raw)
	}
	var left riskLabelsOut
	if err := json.Unmarshal(raw, &left); err != nil {
		t.Fatal(err)
	}
	if left.Count != 0 {
		t.Fatalf("%d records the tenant was told were disposed of came back on the takeover", left.Count)
	}
}

// TestAHoldIsShippedBeforeItIsAcknowledged. A hold that a rollout forgets is a
// record disposed of while somebody believed it was preserved — the one failure
// of this control that cannot be undone.
func TestAHoldIsShippedBeforeItIsAcknowledged(t *testing.T) {
	cas := newMemCAS()
	owner, _ := wireDurable(t, org.NewDurability(cas, soleMembership(t, "pod-owner"), nil, shipCheckpoint()), &recorder{})
	at := time.Now().UTC().Add(-100 * 24 * time.Hour).Truncate(time.Second)
	out := post(t, owner, "acme", "u_acme", batch(
		assertion("transaction", "tx-hold", at, at.Add(24*time.Hour), Productive, Dispute, "dp-1", 1)))
	id := out.Results[0].ID
	if got := hold(t, owner, "acme", true, id); got.Changed != 1 {
		t.Fatalf("placing the hold: %+v", got)
	}

	succ, _ := wireDurable(t, org.NewDurability(cas, soleMembership(t, "pod-successor"), nil, shipCheckpoint()), &recorder{})
	if !heldFlag(t, succ, "acme", id) {
		t.Fatal("the takeover lost a litigation hold: the record is disposable again and nobody was told")
	}
}
