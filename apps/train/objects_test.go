package train

// objects_test.go — the org's objects: rows it uploads for a job to read, sealed
// under its own key; deleting one; retention for what is unpublished; and the key
// minted once in KMS.

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk/audit"
	"github.com/hanzoai/cloud/sdk/locus"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/hanzoai/cloud/sdk/secret"
	"github.com/hanzoai/namespace"
)

func TestADatasetTheOrgUploadedIsReadBackBeforeAJobReadsIt(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		r := mount(t, "")
		sum := strings.Repeat("d1", 32)
		up := r.must(200, "POST", "/v1/train/artifacts", "acme", map[string]any{"kind": "dataset", "name": "tickets", "sha256": sum, "size": 2048})
		if up["stored"] != false || !strings.Contains(str(up, "upload", "url"), "acme/"+sum) {
			t.Fatalf("grant: %v", up)
		}
		if r.objs.granted["acme/"+sum] != keyID("acme") || r.objs.mimes["acme/"+sum] != jsonlType {
			t.Fatalf("the grant was not sealed under the org's key as JSON Lines: key %q mime %q", r.objs.granted["acme/"+sum], r.objs.mimes["acme/"+sum])
		}
		j := job("dgx")
		j["dataset"] = map[string]any{"uri": "artifact:" + sum}
		if code, m := r.do("POST", "/v1/train/jobs", "acme", j); code != 409 || str(m, "code") != "artifact_missing" {
			t.Fatalf("a job on rows not yet uploaded: %d %v", code, m)
		}
		// Bytes sealed under another key are not the org's, and are removed.
		r.objs.storeSealed("acme/"+sum, sum, keyID("evil"))
		if code, m := r.do("POST", "/v1/train/jobs", "acme", j); code != 409 || str(m, "code") != "artifact_mismatch" {
			t.Fatalf("rows not sealed under the org's key: %d %v", code, m)
		}
		if r.objs.has("acme/" + sum) {
			t.Fatal("an object sealed under another key was kept")
		}
		r.objs.store("acme/"+sum, sum)
		id := str(r.must(200, "POST", "/v1/train/jobs", "acme", j), "id")
		if code, m := r.do("POST", "/v1/train/jobs", "evil", j); code != 400 || str(m, "code") != "invalid_request" {
			t.Fatalf("another org named the rows: %d %v", code, m)
		}
		ob := r.must(200, "GET", "/v1/train/artifacts/"+sum, "acme", nil)
		if str(ob, "state") != "stored" || str(ob, "kind") != "dataset" || !strings.Contains(str(ob, "url"), "acme/"+sum) {
			t.Fatalf("the executor's read of the rows: %v", ob)
		}
		if code, _ := r.do("GET", "/v1/train/artifacts/"+sum, "evil", nil); code != 404 {
			t.Fatalf("another org read the rows: %d", code)
		}
		if code, m := r.do("DELETE", "/v1/train/artifacts/"+sum, "acme", nil); code != 409 || str(m, "code") != "in_use" {
			t.Fatalf("deleting rows a queued job reads: %d %v", code, m)
		}
		r.must(200, "POST", "/v1/train/jobs/"+id+"/cancel", "acme", nil)
		if code, _ := r.do("DELETE", "/v1/train/artifacts/"+sum, "evil", nil); code != 404 {
			t.Fatalf("another org deleted the rows: %d", code)
		}
		d := r.must(200, "DELETE", "/v1/train/artifacts/"+sum, "acme", nil)
		if d["deleted"] != true || r.objs.has("acme/"+sum) {
			t.Fatalf("delete: %v", d)
		}
		if code, _ := r.do("GET", "/v1/train/artifacts/"+sum, "acme", nil); code != 404 {
			t.Fatalf("a deleted object answered: %d", code)
		}
		if code, m := r.do("POST", "/v1/train/jobs", "acme", j); code != 400 {
			t.Fatalf("a job on deleted rows: %d %v", code, m)
		}
	})
}

// published runs a job on the org's machine to a stored, accepted capability and
// publishes it under name with route.
func (r *rig) published(org, name, sum string, spec map[string]any, route map[string]any) (string, int, map[string]any) {
	r.t.Helper()
	id := str(r.must(200, "POST", "/v1/train/jobs", org, spec), "id")
	tk := r.must(200, "POST", "/v1/train/jobs/claim", org, claimBody(spec["resources"].(map[string]any)["machines"].([]string)[0]))["task"].(map[string]any)
	path := "/v1/train/jobs/" + id
	r.must(200, "POST", path+"/artifacts", org, map[string]any{"task": str(tk, "id"), "lease": str(tk, "lease"), "kind": "capability", "sha256": sum, "size": 4096})
	r.objs.store(org+"/"+sum, sum)
	res := map[string]any{"type": "result", "verdict": "accepted", "base": map[string]any{"sha256": baseSHA}, "outputs": []any{map[string]any{"kind": "capability", "sha256": sum}}}
	r.must(200, "POST", path+"/events", org, map[string]any{"task": str(tk, "id"), "lease": str(tk, "lease"), "events": []any{map[string]any{"type": "artifact", "sha256": sum}, res}})
	code, m := r.as("POST", path+"/publish", org, false, map[string]any{"name": name, "route": route})
	return id, code, m
}

func TestAPublishedArtifactIsKeptAndAnUnpublishedOneOutlivesRetentionOnlyInUse(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		r := mount(t, "")
		c := pin(t)
		o := ops{s: r.svc}
		ctx := context.Background()
		pub := strings.Repeat("e1", 32)
		if _, code, m := r.published("acme", "triage", pub, job("dgx"), route("topic")); code != 200 {
			t.Fatalf("publish: %d %v", code, m)
		}
		if code, m := r.do("DELETE", "/v1/train/artifacts/"+pub, "acme", nil); code != 409 || str(m, "code") != "published" {
			t.Fatalf("deleting a published artifact: %d %v", code, m)
		}
		stale := strings.Repeat("e2", 32)
		r.must(200, "POST", "/v1/train/artifacts", "acme", map[string]any{"kind": "dataset", "sha256": stale, "size": 64})
		r.objs.store("acme/"+stale, stale)
		read := strings.Repeat("e3", 32)
		r.must(200, "POST", "/v1/train/artifacts", "acme", map[string]any{"kind": "dataset", "sha256": read, "size": 64})
		r.objs.store("acme/"+read, read)
		j := job("dgx")
		j["dataset"] = map[string]any{"uri": "artifact:" + stale}
		id := str(r.must(200, "POST", "/v1/train/jobs", "acme", j), "id")
		r.must(200, "POST", "/v1/train/jobs/"+id+"/cancel", "acme", nil)
		j["dataset"] = map[string]any{"uri": "artifact:" + read}
		r.must(200, "POST", "/v1/train/jobs", "acme", j)
		l := r.must(200, "GET", "/v1/train/artifacts", "acme", nil)["data"].([]any)
		for _, x := range l {
			ob := x.(map[string]any)
			want := c.now + r.st.retain
			if ob["sha256"] == pub {
				want = 0
			}
			if got, _ := ob["expires"].(float64); int64(got) != want {
				t.Fatalf("%s expires %v, want %d", ob["sha256"], ob["expires"], want)
			}
		}
		c.tick(r.st.retain - 1)
		o.sweep(ctx, c.now)
		if !r.objs.has("acme/" + stale) {
			t.Fatal("retention deleted an object before its time")
		}
		c.tick(2)
		o.sweep(ctx, c.now)
		if r.objs.has("acme/"+stale) || !r.objs.has("acme/"+pub) || !r.objs.has("acme/"+read) {
			t.Fatalf("retention: stale %v published %v read by a queued job %v", r.objs.has("acme/"+stale), r.objs.has("acme/"+pub), r.objs.has("acme/"+read))
		}
		if code, _ := r.do("GET", "/v1/train/artifacts/"+stale, "acme", nil); code != 404 {
			t.Fatalf("a retained-out object answered: %d", code)
		}
		rows, _, err := r.trail.Query(ctx, audit.Filter{Action: "train.artifact.retain"})
		if err != nil || len(rows) != 1 || rows[0].Resource.ID != "acme/"+stale {
			t.Fatalf("retention audit: %v %v", rows, err)
		}
	})
}

func TestRetentionOfZeroKeepsEverything(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		r := mount(t, "")
		c := pin(t)
		r.st.retain = 0
		sum := strings.Repeat("e4", 32)
		r.must(200, "POST", "/v1/train/artifacts", "acme", map[string]any{"kind": "dataset", "sha256": sum, "size": 64})
		r.objs.store("acme/"+sum, sum)
		j := job("dgx")
		j["dataset"] = map[string]any{"uri": "artifact:" + sum}
		r.must(200, "POST", "/v1/train/jobs", "acme", j)
		c.tick(10 * 365 * 24 * 3600)
		ops{s: r.svc}.sweep(context.Background(), c.now)
		if !r.objs.has("acme/" + sum) {
			t.Fatal("retention 0 deleted an object")
		}
	})
}

// fakeKMS is a secret store that counts its writes.
type fakeKMS struct {
	mu   sync.Mutex
	held map[string][]byte
	puts int
}

func (k *fakeKMS) GetSecret(_ context.Context, ref string) ([]byte, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	v, ok := k.held[ref]
	if !ok {
		return nil, secret.ErrNone
	}
	return append([]byte(nil), v...), nil
}

func (k *fakeKMS) PutSecret(_ context.Context, ref string, v []byte) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.held[ref] = append([]byte(nil), v...)
	k.puts++
	return nil
}

func (k *fakeKMS) DeleteSecret(context.Context, string) error { return nil }

func (k *fakeKMS) Sign(context.Context, string, []byte) ([]byte, error) { return nil, nil }

// keyStore is the store the keys are minted under: the org's train database on this leg's
// engine. A second one, on hanzo-sql, is another replica's handle on the same database.
func keyStore(t *testing.T, open sqltest.Open) *store {
	t.Helper()
	st, err := openStore(testMaster)(namespace.System(), open(namespace.System(), "train"))
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	return st
}

func TestAnOrgsKeyIsMintedOnceInKMS(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		kms := &fakeKMS{held: map[string][]byte{}}
		ctx := context.Background()
		st := keyStore(t, open)
		k := newKeys(kms)
		var wg sync.WaitGroup
		for range 8 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if id, err := k.seal(ctx, st, "acme"); err != nil || id != "train-acme" {
					t.Errorf("seal: %q %v", id, err)
				}
			}()
		}
		wg.Wait()
		v := kms.held["s3/train-acme@prod"]
		if len(v) != 64 || kms.puts != 1 {
			t.Fatalf("the key at s3/train-acme@prod: %d hex bytes, %d writes, want one", len(v), kms.puts)
		}
		before := kms.puts
		if _, err := newKeys(kms).seal(ctx, st, "acme"); err != nil || kms.puts != before {
			t.Fatalf("a key KMS holds was minted again: %d writes, %v", kms.puts, err)
		}
		if _, err := newKeys(kms).seal(ctx, st, "other"); err != nil || string(kms.held["s3/train-other@prod"]) == string(v) {
			t.Fatalf("a second org shares the first's key: %v", err)
		}
		kms.held["s3/train-bad@prod"] = []byte("short")
		if _, err := newKeys(kms).seal(ctx, st, "bad"); err == nil {
			t.Fatal("a key that is not 32 bytes was used")
		}
		if _, err := newKeys(nil).seal(ctx, st, "acme"); err == nil {
			t.Fatal("a deployment with no KMS sealed an object")
		}
	})
}

// slowKMS is a KMS that takes its time to say a key is not there, as a store does over a
// network: long enough for a second replica to ask before the first has written.
type slowKMS struct {
	*fakeKMS
	gap time.Duration
}

func (k slowKMS) GetSecret(ctx context.Context, ref string) ([]byte, error) {
	v, err := k.fakeKMS.GetSecret(ctx, ref)
	if errors.Is(err, secret.ErrNone) {
		time.Sleep(k.gap)
	}
	return v, err
}

// Two replicas asked for an org's first key at once mint ONE: the second finds the
// first's key and uses it, where two writes would leave every object sealed under the
// loser's unreadable. The replicas are two key minters, each with its own single
// flight, over two handles on the org's database, as two pods are. A SQLite file
// belongs to one process, so there the replicas share the minter and the file.
//
// MUTATION: take no lock in keys.seal (the single flight alone) and each replica
// mints.
func TestTwoReplicasMintAnOrgsKeyOnce(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		fake := &fakeKMS{held: map[string][]byte{}}
		kms := slowKMS{fakeKMS: fake, gap: 200 * time.Millisecond}
		ctx := context.Background()
		a, b := keyStore(t, open), keyStore(t, open)
		pods := []struct {
			keys *keys
			st   *store
		}{{newKeys(kms), a}, {newKeys(kms), b}}
		if locus.DSN() == "" {
			pods[1].keys, pods[1].st = pods[0].keys, pods[0].st
		}
		var wg sync.WaitGroup
		ids := make([]string, len(pods))
		for i, pod := range pods {
			wg.Add(1)
			go func() {
				defer wg.Done()
				id, err := pod.keys.seal(ctx, pod.st, "acme")
				if err != nil {
					t.Errorf("replica %d: %v", i, err)
				}
				ids[i] = id
			}()
		}
		wg.Wait()
		if fake.puts != 1 {
			t.Fatalf("two replicas wrote the org's key %d times, want once", fake.puts)
		}
		if ids[0] != "train-acme" || ids[1] != "train-acme" {
			t.Fatalf("the replicas sealed under %q and %q", ids[0], ids[1])
		}
	})
}

func TestAnUploadUnconfirmedPastItsGrantIsDeletedAndPendingBytesAreBounded(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		r := mount(t, "")
		c := pin(t)
		o := ops{s: r.svc}
		ctx := context.Background()
		big := func(b byte) string {
			return strings.Repeat(string("0123456789abcdef"[b%16])+string("0123456789abcdef"[b/16%16]), 32)
		}
		a, b, x := big(1), big(2), big(3)
		r.must(200, "POST", "/v1/train/artifacts", "acme", map[string]any{"kind": "dataset", "sha256": a, "size": int64(maxArtifact)})
		r.must(200, "POST", "/v1/train/artifacts", "acme", map[string]any{"kind": "dataset", "sha256": b, "size": int64(maxArtifact)})
		if code, m := r.do("POST", "/v1/train/artifacts", "acme", map[string]any{"kind": "dataset", "sha256": x, "size": 1}); code != 409 || str(m, "code") != "pending_limit" {
			t.Fatalf("a grant past the pending ceiling: %d %v", code, m)
		}
		// Re-granting bytes already pending is not more pending bytes.
		r.must(200, "POST", "/v1/train/artifacts", "acme", map[string]any{"kind": "dataset", "sha256": b, "size": int64(maxArtifact)})
		r.objs.store("acme/"+a, a) // put, never confirmed
		c.tick(int64(grantTTL/time.Second) - 1)
		o.sweep(ctx, c.now)
		if !r.objs.has("acme/" + a) {
			t.Fatal("an upload was reaped while its grant lived")
		}
		c.tick(2)
		o.sweep(ctx, c.now)
		if r.objs.has("acme/" + a) {
			t.Fatal("bytes put under a lapsed grant and never confirmed were kept")
		}
		ob := r.must(200, "GET", "/v1/train/artifacts", "acme", nil)["data"].([]any)
		if len(ob) != 0 {
			t.Fatalf("lapsed uploads still listed: %v", ob)
		}
		rows, _, err := r.trail.Query(ctx, audit.Filter{Action: "train.artifact.lapse"})
		if err != nil || len(rows) != 2 {
			t.Fatalf("lapse audit: %d rows, %v", len(rows), err)
		}
		r.must(200, "POST", "/v1/train/artifacts", "acme", map[string]any{"kind": "dataset", "sha256": x, "size": 1})
	})
}

func TestADeploymentWithNoAuditTrailDeletesNothingOnItsOwn(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		r := mount(t, "")
		c := pin(t)
		r.st.audit = nil
		sum := strings.Repeat("e5", 32)
		r.must(200, "POST", "/v1/train/artifacts", "acme", map[string]any{"kind": "dataset", "sha256": sum, "size": 64})
		r.objs.store("acme/"+sum, sum)
		j := job("dgx")
		j["dataset"] = map[string]any{"uri": "artifact:" + sum}
		r.must(200, "POST", "/v1/train/jobs", "acme", j)
		lapsed := strings.Repeat("e6", 32)
		r.must(200, "POST", "/v1/train/artifacts", "acme", map[string]any{"kind": "dataset", "sha256": lapsed, "size": 64})
		r.objs.store("acme/"+lapsed, lapsed)
		r.must(200, "POST", "/v1/train/jobs/"+str(r.must(200, "GET", "/v1/train/jobs", "acme", nil)["data"].([]any)[0].(map[string]any), "id")+"/cancel", "acme", nil)
		c.tick(r.st.retain + 1)
		ops{s: r.svc}.sweep(context.Background(), c.now)
		if !r.objs.has("acme/"+sum) || !r.objs.has("acme/"+lapsed) {
			t.Fatal("the sweep deleted an org's bytes with no audit trail to record it")
		}
	})
}

func TestBytesStoredUnsealedAreUploadedAgainSealed(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		r := mount(t, "")
		st := r.store("acme")
		sum := strings.Repeat("1f", 32)
		if _, err := st.db.Exec(`INSERT INTO artifacts (job, sha256, kind, name, size, state, meta, created) VALUES ('job_legacy', $1, 'capability', 'old', 4096, 'stored', '{}', 1)`, sum); err != nil {
			t.Fatal(err)
		}
		r.objs.storeSealed("acme/"+sum, sum, "")
		if _, err := openStore(testMaster)(principal.MustOrgNamespace("acme", ""), st.db); err != nil {
			t.Fatal(err)
		}
		register := func() map[string]any {
			id := str(r.must(200, "POST", "/v1/train/jobs", "acme", job("dgx")), "id")
			tk := r.must(200, "POST", "/v1/train/jobs/claim", "acme", claimBody("dgx"))["task"].(map[string]any)
			reg := r.must(200, "POST", "/v1/train/jobs/"+id+"/artifacts", "acme", map[string]any{"task": str(tk, "id"), "lease": str(tk, "lease"), "kind": "capability", "sha256": sum, "size": 4096})
			if reg["stored"] != true {
				r.objs.store("acme/"+sum, sum)
				r.must(200, "POST", "/v1/train/jobs/"+id+"/events", "acme", map[string]any{"task": str(tk, "id"), "lease": str(tk, "lease"), "events": []any{map[string]any{"type": "artifact", "sha256": sum}}})
			}
			r.must(200, "POST", "/v1/train/jobs/"+id+"/cancel", "acme", nil)
			return reg
		}
		if reg := register(); reg["stored"] != false || r.objs.granted["acme/"+sum] != keyID("acme") {
			t.Fatalf("unsealed bytes were not granted again under the org's key: %v", reg)
		}
		if reg := register(); reg["stored"] != true {
			t.Fatalf("bytes sealed on their second upload are not held: %v", reg)
		}
	})
}

// TestAnUploadIsWeighedAgainstAMonthOfItsBytes: every org may upload, and the
// uploader's wallet must pay a month of keeping the bytes before it is granted.
func TestAnUploadIsWeighedAgainstAMonthOfItsBytes(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		r := mount(t, "")
		size := int64(10 * gb)
		body := map[string]any{"kind": "dataset", "sha256": strings.Repeat("5e", 32), "size": size}
		r.books.balance = 0
		if code, m := r.do("POST", "/v1/train/artifacts", "acme", body); code != 402 || str(m, "error", "code") != "insufficient_balance" {
			t.Fatalf("an empty wallet: %d %v", code, m)
		}
		month := monthMicros(size, storageFloorMicros)
		if got := r.books.asked[len(r.books.asked)-1]; got != month || month != 200_000 {
			t.Fatalf("weighed %d µ$, want a month of 10 GB at $0.02 = %d", got, month)
		}
		r.books.balance = month
		if code, m := r.do("POST", "/v1/train/artifacts", "acme", body); code != 200 {
			t.Fatalf("a wallet holding the month: %d %v", code, m)
		}
	})
}
