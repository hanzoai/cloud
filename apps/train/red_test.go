package train

// red_test.go — adversarial review of 7947ec9ce. Every test here fails on that
// commit and names the finding it demonstrates.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/meter"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/hanzoai/account"
	"github.com/zap-proto/zip"
)

// asIn is as with a project and a home org on the request, as the identity
// boundary mints them.
func (r *rig) asIn(method, path, org, project, owner string, super bool, body any) (int, map[string]any) {
	r.t.Helper()
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(method, path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Org-Id", org)
	req.Header.Set("X-User-Id", "u-"+org)
	if project != "" {
		req.Header.Set("X-Project-Id", project)
	}
	if owner != "" {
		req.Header.Set("X-User-Owner", owner)
	}
	if super {
		req.Header.Set("X-User-IsAdmin", "true")
	}
	resp, err := r.app.Test(req, zip.TestConfig{Timeout: 60 * time.Second})
	if err != nil {
		r.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	m := map[string]any{}
	_ = json.Unmarshal(raw, &m)
	return resp.StatusCode, m
}

// R1. A job that has not ended keeps its outputs unbilled, and a job on the org's
// own machine never has to end: its executor's heartbeat keeps it running.
func TestRedARunningJobsOutputIsStoredFree(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		r := mount(t, "")
		c := pin(t)
		o := ops{s: r.svc}
		ctx := context.Background()
		id := str(r.must(200, "POST", "/v1/train/jobs", "acme", job("dgx")), "id")
		tk := r.must(200, "POST", "/v1/train/jobs/claim", "acme", claimBody("dgx"))["task"].(map[string]any)
		path := "/v1/train/jobs/" + id
		beat := func(events ...any) {
			if events == nil {
				events = []any{}
			}
			r.must(200, "POST", path+"/events", "acme", map[string]any{"task": str(tk, "id"), "lease": str(tk, "lease"), "events": events})
		}
		const size = int64(60e9)
		sum := strings.Repeat("9a", 32)
		r.must(200, "POST", path+"/artifacts", "acme", map[string]any{"task": str(tk, "id"), "lease": str(tk, "lease"), "kind": "checkpoint", "sha256": sum, "size": size})
		r.objs.store("acme/"+sum, sum)
		beat(map[string]any{"type": "artifact", "sha256": sum})
		for h := 0; h < 30*24; h++ {
			for range 9 {
				c.tick(400)
				beat()
			}
			o.sweep(ctx, c.now)
		}
		g := r.must(200, "GET", path, "acme", nil)
		month := cost(hourMicros(size, storageFloorMicros), 30*24*3600)
		if got := r.books.spent("storage"); got < month/2 {
			t.Fatalf("%d bytes kept 30 days by a job that is %s: charged %d µ$, want ≈ %d; downloadable all along", size, str(g, "status"), got, month)
		}
	})
}

// R2. A rejected job's output is never billed, and a queued job that merely names
// its sha256 in any spec field — here as an evaluation suite — keeps retention
// from ever deleting it.
func TestRedARejectedOutputPinnedByAQueuedJobIsNeitherBilledNorRetained(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		r := mount(t, "")
		c := pin(t)
		o := ops{s: r.svc}
		ctx := context.Background()
		x := strings.Repeat("5c", 32)
		id := str(r.must(200, "POST", "/v1/train/jobs", "acme", job("dgx")), "id")
		tk := r.must(200, "POST", "/v1/train/jobs/claim", "acme", claimBody("dgx"))["task"].(map[string]any)
		path := "/v1/train/jobs/" + id
		r.must(200, "POST", path+"/artifacts", "acme", map[string]any{"task": str(tk, "id"), "lease": str(tk, "lease"), "kind": "capability", "sha256": x, "size": int64(60e9)})
		r.objs.store("acme/"+x, x)
		res := map[string]any{"type": "result", "verdict": "rejected", "base": map[string]any{"sha256": baseSHA}, "outputs": []any{map[string]any{"kind": "capability", "sha256": x}}}
		r.must(200, "POST", path+"/events", "acme", map[string]any{"task": str(tk, "id"), "lease": str(tk, "lease"), "events": []any{map[string]any{"type": "artifact", "sha256": x}, res}})
		pinning := job("nowhere")
		pinning["evaluation"] = map[string]any{"suites": []string{x}}
		r.must(200, "POST", "/v1/train/jobs", "acme", pinning)
		if code, m := r.do("DELETE", "/v1/train/artifacts/"+x, "acme", nil); code == 409 {
			t.Logf("DELETE answers %d %s: a job that names the sha256 as a suite \"reads\" it", code, str(m, "code"))
		}
		c.tick(61 * 24 * 3600)
		o.sweep(ctx, c.now)
		if r.objs.has("acme/"+x) && r.books.spent("storage") == 0 {
			t.Fatalf("60 GB held 61 days past a 30-day retention, charged %d µ$: rejected, and pinned by a queued job that only names it", r.books.spent("storage"))
		}
	})
}

// R3. Bytes PUT under a grant and never confirmed have no objects row: no sweep
// bills them, retention never sees them, and the multipart upload is never aborted.
// A wallet that cannot pay a month of the bytes is not granted them at all.
func TestRedAnUploadNeverConfirmedIsKeptForeverUnbilled(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		r := mount(t, "")
		c := pin(t)
		o := ops{s: r.svc}
		r.books.balance = 0
		sum := strings.Repeat("7b", 32)
		if code, _ := r.do("POST", "/v1/train/artifacts", "acme", map[string]any{"kind": "dataset", "sha256": sum, "size": int64(maxArtifact)}); code != 402 {
			t.Fatalf("a $0 wallet was granted 64 GiB: %d", code)
		}
		r.books.balance = -1
		r.must(200, "POST", "/v1/train/artifacts", "acme", map[string]any{"kind": "dataset", "sha256": sum, "size": int64(maxArtifact)})
		r.objs.store("acme/"+sum, sum)
		granted := 1
		for i := range 99 {
			s := strings.Repeat("7c", 31) + string("0123456789abcdef"[i%16]) + string("0123456789abcdef"[i/16])
			if code, _ := r.do("POST", "/v1/train/artifacts", "acme", map[string]any{"kind": "dataset", "sha256": s, "size": int64(maxArtifact)}); code == 200 {
				granted++
			}
		}
		c.tick(61 * 24 * 3600)
		o.sweep(context.Background(), c.now)
		if r.objs.has("acme/" + sum) {
			t.Fatalf("an upload never confirmed is still held 61 days on, charged %d µ$; the wallet was granted %d × 64 GiB", r.books.spent("storage"), granted)
		}
	})
}

// R4. Two platform claims for one payer each weigh the balance before either
// takes its task, so both windows are held against money for one.
func TestRedTwoClaimsForOnePayerHoldTwiceItsBalance(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		r := mount(t, "")
		pin(t)
		r.books.balance = gpu(windowSeconds) + 1
		r.must(200, "POST", "/v1/train/jobs", "acme", hanzoJob(map[string]any{"devices": []string{"cuda"}}))
		r.must(200, "POST", "/v1/train/jobs", "acme", hanzoJob(map[string]any{"devices": []string{"rocm"}}))
		var mu sync.Mutex
		arrived := 0
		both := make(chan struct{})
		r.st.afford = func(ctx context.Context, payer account.Account, project string, validated bool, micros int64) error {
			mu.Lock()
			if arrived++; arrived == 2 {
				close(both)
			}
			mu.Unlock()
			select {
			case <-both:
			case <-time.After(2 * time.Second):
			}
			return r.books.afford(ctx, payer, project, validated, micros)
		}
		codes := make(chan int, 2)
		for _, d := range []string{"cuda", "rocm"} {
			go func() {
				code, _ := r.as("POST", "/v1/train/jobs/claim", "hanzo", true, platformClaim(d))
				codes <- code
			}()
		}
		answered := 0
		for range 2 {
			if <-codes == 200 {
				answered++
			}
		}
		if held := r.owed("acme"); held > r.books.balance {
			t.Fatalf("%d claims answered: the payer holds %d µ$ against a balance of %d", answered, held, r.books.balance)
		}
	})
}

// R5. A create weighs a claim-bound project's hard cap; every later window is
// weighed with validated=false, where a project cap is soft.
func TestRedAProjectsHardSpendCapGoesSoftOnceTheJobRuns(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		r := mount(t, "")
		c := pin(t)
		capMicros := gpu(windowSeconds) + 1
		r.st.afford = func(ctx context.Context, payer account.Account, project string, validated bool, micros int64) error {
			// Meter.Authorize: a project cap is hard only for a claim-bound project.
			if project == "lab" && validated && r.books.spent("")+micros > capMicros {
				return meter.ErrSpendCapExceeded
			}
			return r.books.afford(ctx, payer, project, validated, micros)
		}
		if code, m := r.asIn("POST", "/v1/train/jobs", "acme", "lab", "", false, hanzoJob(map[string]any{})); code != 200 {
			t.Fatalf("create under the project: %d %v", code, m)
		}
		x := r.claimPlatform("cuda")
		for s := reportEvery; s <= 900; s += reportEvery {
			c.tick(reportEvery)
			if out := x.report(); out["cancel"] == true {
				if g := r.must(200, "GET", "/v1/train/jobs/"+x.job, "acme", nil); str(g, "error") != stopCap {
					t.Fatalf("stopped for %q, want spend_cap", str(g, "error"))
				}
				return
			}
		}
		for _, d := range r.books.of("compute") {
			if d.usage.Project != "lab" {
				t.Fatalf("debit not under the project: %+v", d)
			}
		}
		t.Fatalf("ran 900 s against project lab's hard cap of one window (%d µ$): charged %d µ$, held %d s", capMicros, r.books.spent("compute"), x.held)
	})
}

// R7. The last report meters its seconds and carries the result. Its ship fails, so
// the seconds are to be given back for the retry — but the result closed the
// task, the give-back's CAS needs it claimed, and the retry meets a terminal job.
// A ship is a SQLite file reaching S3; a store on hanzo-sql has no file to ship, so
// this runs on SQLite alone.
func TestRedAFinalReportWhoseShipFailsIsNeverCharged(t *testing.T) {
	r := mount(t, "")
	c := pin(t)
	r.must(200, "POST", "/v1/train/jobs", "acme", hanzoJob(map[string]any{}))
	x := r.claimPlatform("cuda")
	sum := strings.Repeat("ab", 32)
	c.tick(reportEvery)
	x.store("capability", sum, 1024)
	// The same files, reopened by a writer that cannot make them durable: every
	// ship is refused (OrgStore.Sync on a peered deployment with no fence).
	base := r.svc.Base
	_ = r.st.stores.CloseAll()
	peered := base
	peered.Peers = true
	r.st.stores = cloud.NewOrgStoreAt(peered, subsystem, openStore(testMaster))
	c.tick(reportEvery)
	res := map[string]any{"type": "result", "verdict": "accepted", "base": map[string]any{"sha256": baseSHA}, "outputs": []any{map[string]any{"kind": "capability", "sha256": sum}}}
	body := map[string]any{"task": x.task, "lease": x.lease, "events": []any{res}}
	if code, m := r.as("POST", "/v1/train/jobs/"+x.job+"/events", x.org, true, body); code != 503 {
		t.Fatalf("a report whose file does not ship: %d %v", code, m)
	}
	_ = r.st.stores.CloseAll()
	r.st.stores = cloud.NewOrgStoreAt(base, subsystem, openStore(testMaster))
	t.Cleanup(func() { _ = r.st.stores.CloseAll() })
	if code, m := r.as("POST", "/v1/train/jobs/"+x.job+"/events", x.org, true, body); code != 200 {
		t.Fatalf("the retry: %d %v", code, m)
	}
	if got := r.books.spent("compute"); got != gpu(2*reportEvery) {
		t.Fatalf("charged %d µ$ for %d metered seconds, want %d: the last %d s ran on Hanzo's devices and are billed to nobody", got, 2*reportEvery, gpu(2*reportEvery), reportEvery)
	}
}

// R8. The release before this one stored artifacts with no encryption. reshape
// turns every such row into an object, and admit then answers stored: true for
// those bytes without reading them back, so an unsealed object stands as sealed.
func TestRedALegacyUnsealedArtifactIsTrustedAsSealed(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		r := mount(t, "")
		st := r.store("acme")
		sum := strings.Repeat("1e", 32)
		if _, err := st.db.Exec(`INSERT INTO artifacts (job, sha256, kind, name, size, state, meta, created) VALUES ('job_legacy', $1, 'capability', 'old', 4096, 'stored', '{}', 1)`, sum); err != nil {
			t.Fatal(err)
		}
		r.objs.storeSealed("acme/"+sum, sum, "")
		if _, err := openStore(testMaster)(principal.MustOrgNamespace("acme", ""), st.db); err != nil {
			t.Fatal(err)
		}
		id := str(r.must(200, "POST", "/v1/train/jobs", "acme", job("dgx")), "id")
		tk := r.must(200, "POST", "/v1/train/jobs/claim", "acme", claimBody("dgx"))["task"].(map[string]any)
		reg := r.must(200, "POST", "/v1/train/jobs/"+id+"/artifacts", "acme", map[string]any{"task": str(tk, "id"), "lease": str(tk, "lease"), "kind": "capability", "sha256": sum, "size": 4096})
		if reg["stored"] == true {
			t.Fatalf("bytes the previous release stored unencrypted count as stored under the org's key, never read back: %v (sealed under %q)", reg, r.objs.sealed["acme/"+sum])
		}
	})
}

// R9. HIP-1333 §9 puts every platform claim on the audit trail before it is
// answered; with no recorder the claim is answered unrecorded. apps/plugin
// refuses the same act in the same regime.
func TestRedAPlatformClaimWithNoAuditTrailIsRefused(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		r := mount(t, "")
		pin(t)
		r.st.audit = nil
		r.must(200, "POST", "/v1/train/jobs", "acme", hanzoJob(map[string]any{}))
		if code, _ := r.as("POST", "/v1/train/jobs/claim", "hanzo", true, platformClaim()); code == 200 {
			t.Fatal("a claim that starts charging acme's payer was answered with no audit trail to record it")
		}
	})
}

// R10. Blue's vector 1. The one wallet that pays in two orgs is a SuperAdmin's own
// books: acting in another org it spends from home. Holds are summed per org file.
func TestRedOneWalletAcrossTwoOrgsHoldsTwiceItsBalance(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		r := mount(t, "")
		pin(t)
		r.books.balance = gpu(windowSeconds) + 1
		for _, org := range []string{"acme", "beta"} {
			if code, m := r.asIn("POST", "/v1/train/jobs", org, "", "admin", true, hanzoJob(map[string]any{})); code != 200 {
				t.Fatalf("create in %s: %d %v", org, code, m)
			}
			r.claimPlatform("cuda")
		}
		var held int64
		var payer string
		for _, org := range []string{"acme", "beta"} {
			rs, _ := r.store(org).jobs(context.Background(), "", 1)
			payer = rs[0].Payer
			n, err := r.store(org).outstanding(context.Background(), payer)
			if err != nil {
				t.Fatal(err)
			}
			held += n
		}
		if held > r.books.balance {
			t.Fatalf("wallet %s holds %d µ$ across two orgs against a balance of %d", payer, held, r.books.balance)
		}
	})
}

// held claims an own-machine job for acme on dgx and answers its id, task and lease.
func (r *rig) held() (id, task, lease string) {
	r.t.Helper()
	id = str(r.must(200, "POST", "/v1/train/jobs", "acme", job("dgx")), "id")
	tk := r.must(200, "POST", "/v1/train/jobs/claim", "acme", claimBody("dgx"))["task"].(map[string]any)
	return id, str(tk, "id"), str(tk, "lease")
}

// R13. Every org trains once the stage is gone, so what a job on the org's own
// machines stores in Hanzo's bucket is weighed as an upload is: its payer pays a
// month of what it keeps in the org, what is pending, and these bytes.
func TestRedAnOwnMachineJobsOutputsAreWeighedLikeAnUpload(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		r := mount(t, "")
		id, tid, lease := r.held()
		r.books.balance = 0
		reg := map[string]any{"task": tid, "lease": lease, "kind": "checkpoint", "name": "ck", "sha256": strings.Repeat("ce", 32), "size": int64(maxArtifact)}
		if code, m := r.do("POST", "/v1/train/jobs/"+id+"/artifacts", "acme", reg); code != 402 {
			t.Fatalf("a $0 wallet stored 64 GiB from its own machine: %d %v", code, m)
		}
		r.books.balance = -1
		r.must(200, "POST", "/v1/train/jobs/"+id+"/artifacts", "acme", reg)
		reg["sha256"] = strings.Repeat("cf", 32)
		r.must(200, "POST", "/v1/train/jobs/"+id+"/artifacts", "acme", reg)
		pending := int64(maxArtifact)
		if got, want := r.books.asked[len(r.books.asked)-1], monthMicros(pending+int64(maxArtifact), storageFloorMicros); got != want {
			t.Fatalf("the second grant weighed %d µ$, want a month of both: %d", got, want)
		}
	})
}

// R14. An event is kept as its type's own fields, a result is bounded, and a
// job keeps at most maxNotes logs and metrics while its statuses still land.
func TestRedAJobsEventsAreBounded(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		r := mount(t, "")
		id, tid, lease := r.held()
		path := "/v1/train/jobs/" + id + "/events"
		junk := strings.Repeat("x", 1<<20)
		r.must(200, "POST", path, "acme", map[string]any{"task": tid, "lease": lease, "events": []any{
			map[string]any{"type": "log", "line": "ok", "junk": junk},
			map[string]any{"type": "status", "status": "running", "junk": junk}}})
		evs := r.must(200, "GET", path, "acme", nil)["data"].([]any)
		for _, e := range evs {
			if raw, _ := json.Marshal(e); strings.Contains(string(raw), "junk") {
				t.Fatalf("an event kept a field of no type: %.200s", raw)
			}
		}
		big := map[string]any{"type": "result", "verdict": "accepted", "research": junk}
		if code, m := r.do("POST", path, "acme", map[string]any{"task": tid, "lease": lease, "events": []any{big}}); code != 400 {
			t.Fatalf("a result over a MiB: %d %v", code, m)
		}
		st := r.store("acme")
		tx, err := st.db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		for i := range maxNotes {
			if _, err := tx.Exec(`INSERT INTO events (job, seq, at, task, body) VALUES ($1,$2,$3,$4,$5)`, id, 1000+i, 0, tid, `{"type":"log","line":"x"}`); err != nil {
				t.Fatal(err)
			}
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		r.must(200, "POST", path, "acme", map[string]any{"task": tid, "lease": lease, "events": []any{
			map[string]any{"type": "log", "line": "past the bound"},
			map[string]any{"type": "status", "status": "evaluating"}}})
		n, err := st.notes(context.Background(), id)
		if err != nil || n != maxNotes+1 {
			t.Fatalf("notes %d %v, want %d: the bound kept a log past it", n, err, maxNotes+1)
		}
		if j := r.must(200, "GET", "/v1/train/jobs/"+id, "acme", nil); str(j, "status") != "evaluating" {
			t.Fatalf("a status past the bound did not land: %v", j["status"])
		}
	})
}

// R15. An org has at most maxLive jobs that have not ended, whoever runs them.
func TestRedAnOrgHasBoundedLiveJobs(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		r := mount(t, "")
		for range maxLive {
			r.must(200, "POST", "/v1/train/jobs", "acme", job("dgx"))
		}
		if code, m := r.do("POST", "/v1/train/jobs", "acme", job("dgx")); code != 409 || str(m, "code") != "job_limit" {
			t.Fatalf("job %d: %d %v", maxLive+1, code, m)
		}
		if code, _ := r.do("POST", "/v1/train/jobs", "other", job("dgx")); code != 200 {
			t.Fatalf("another org was bounded by acme's jobs: %d", code)
		}
	})
}
