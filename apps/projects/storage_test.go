package projects

// storage_test.go — a publish is free, and a site is billed for what its org
// stores: the gate past the free tier, the hourly meter and its quantity, and the
// republish that replaces bytes rather than adding to them.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/meter"
	"github.com/zap-proto/zip"
)


// TestStorageArithmetic reads the two prices in one direction against numbers
// worked by hand: an hour of the excess for the meter, rounded half up, and a
// month of it for the gate, rounded up.
func TestStorageArithmetic(t *testing.T) {
	for _, tc := range []struct {
		name         string
		excess, rate int64
		hour, monthC int64
	}{
		{"36 GB is exactly 1000 µ$ an hour", 36 * gb, 20_000, 1_000, 72},
		{"one GB rounds its 27.78 µ$ hour up", gb, 20_000, 28, 2},
		{"one byte is no hour and one cent of month", 1, 20_000, 0, 1},
		{"nothing past the tier is free", 0, 20_000, 0, 0},
		{"a published zero is free", 36 * gb, 0, 0, 0},
		{"a petabyte does not overflow", 1_000_000 * gb, 20_000, 27_777_778, 2_000_000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := hourMicros(tc.excess, tc.rate); got != tc.hour {
				t.Errorf("hourMicros(%d, %d) = %d, want %d", tc.excess, tc.rate, got, tc.hour)
			}
			if got := monthCents(tc.excess, tc.rate); got != tc.monthC {
				t.Errorf("monthCents(%d, %d) = %d, want %d", tc.excess, tc.rate, got, tc.monthC)
			}
		})
	}
	if billable(freeSiteBytes) != 0 || billable(freeSiteBytes+7) != 7 || billable(0) != 0 {
		t.Fatal("only what lies past the free tier is billable")
	}
}

// ledger is the storage meter's debit sink as a test reads it.
type ledger struct {
	mu   sync.Mutex
	rows []debited
}

type debited struct {
	org string
	u   meter.Usage
}

func (l *ledger) emit(org string, u meter.Usage) {
	l.mu.Lock()
	l.rows = append(l.rows, debited{org, u})
	l.mu.Unlock()
}

func (l *ledger) take() []debited {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := l.rows
	l.rows = nil
	return out
}

// lister is the sweep's view of the fake store: a real listing through the
// production footprint, so the tenant boundary under test is the one that ships.
func lister(t *testing.T, store *Store) func(context.Context, string) (int64, error) {
	t.Helper()
	cli := openBlobStore().store
	return func(ctx context.Context, org string) (int64, error) {
		return footprint(ctx, store, cli, testBucket, org)
	}
}

// STORAGE IS METERED PER ORG, BY WHAT THE ORG HOLDS PAST THE TIER. acme keeps 41
// GB across a live site, a retained release and a build output; its neighbour
// acme-corp keeps 50 GB under a prefix that shares acme's name; globex keeps 3 GB.
// Two hours on, acme is billed 36 GB for two hours at $0.02/GB-month — 2000 µ$ —
// and not a byte of acme-corp's, and globex, inside the tier, is billed nothing.
func TestStorageIsMeteredPerOrgByWhatItHolds(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		f := startFakeS3(t)
		store, err := openStore(t.TempDir(), testMaster, testKMS)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = store.Close() })
		for _, p := range []Project{mkProject("acme", "shop", "Shop"), mkProject("acme-corp", "home", "Home"), mkProject("globex", "site", "Site")} {
			if err := store.CreateProject(t.Context(), p); err != nil {
				t.Fatal(err)
			}
		}
		f.seedSparse(testBucket, "acme/shop/video.mp4", 20*gb)
		f.seedSparse(testBucket, "acme/.releases/shop/rel_0123456789abcdef0123456789abcdef/video.mp4", 16*gb)
		f.seedSparse(testBucket, "acme/builds/v1/video.mp4", 5*gb)
		f.seedSparse(testBucket, "acme-corp/home/archive.tar", 50*gb)
		f.seedSparse(testBucket, "globex/site/photos.zip", 3*gb)

		var l ledger
		const t0 int64 = 1_000_000
		list := lister(t, store)

		// First sight starts every clock and bills nothing.
		if n, err := sweepStorage(t.Context(), store, t0, storageGBMonthMicros, list, meterHeld, l.emit); err != nil || n != 0 {
			t.Fatalf("first sweep debited %d (%v), want 0: storage held before the meter is never billed backwards", n, err)
		}
		if got := l.take(); len(got) != 0 {
			t.Fatalf("first sight emitted %+v", got)
		}

		n, err := sweepStorage(t.Context(), store, t0+7200, storageGBMonthMicros, list, meterHeld, l.emit)
		if err != nil || n != 2 {
			t.Fatalf("second sweep debited %d orgs (%v), want 2 (acme and acme-corp)", n, err)
		}
		got := map[string]meter.Usage{}
		for _, d := range l.take() {
			got[d.org] = d.u
		}
		acme, ok := got["acme"]
		if !ok {
			t.Fatalf("acme was not billed: %+v", got)
		}
		// 41 GB held, 36 GB past the tier, 36 × 20000 / 720 = 1000 µ$ an hour, two hours.
		if acme.AmountMicros != 2000 {
			t.Fatalf("acme billed %d µ$, want 2000 (36 GB past the tier for two hours at $0.02/GB-month)", acme.AmountMicros)
		}
		if acme.Model != storageModel || acme.Service != "" {
			t.Fatalf("acme's debit reads model=%q service=%q, want %q and the hosting service the meter stamps", acme.Model, acme.Service, storageModel)
		}
		if want := "rt_acme#sites_" + strconv.FormatInt(t0, 10) + "_" + strconv.FormatInt(t0+7200, 10); acme.Ref != want {
			t.Fatalf("acme's debit is named %q, want %q: the span, so a retried debit is one movement of money", acme.Ref, want)
		}
		// 50 GB, 45 past the tier: 1250 µ$ an hour. Summed into acme, acme would read 3000+.
		if c := got["acme-corp"]; c.AmountMicros != 2500 {
			t.Fatalf("acme-corp billed %d µ$, want 2500 — and none of it on acme", c.AmountMicros)
		}
		if _, ok := got["globex"]; ok {
			t.Fatalf("globex holds 3 GB, inside the tier, and was billed %+v", got["globex"])
		}
		if h, _ := store.Holding(t.Context(), "acme"); h.Bytes != 41*gb || h.MeteredAt != t0+7200 {
			t.Fatalf("acme's row reads %+v, want 41 GB billed through %d", h, t0+7200)
		}
		if h, _ := store.Holding(t.Context(), "globex"); h.MeteredAt != t0+7200 {
			t.Fatalf("a free span still moves the clock: globex billed through %d, want %d", h.MeteredAt, t0+7200)
		}

		// The same instant again is the same span: nothing more is owed.
		if n, err := sweepStorage(t.Context(), store, t0+7200, storageGBMonthMicros, list, meterHeld, l.emit); err != nil || n != 0 {
			t.Fatalf("a repeated sweep debited %d (%v), want 0", n, err)
		}
		if got := l.take(); len(got) != 0 {
			t.Fatalf("a repeated sweep emitted %+v", got)
		}
	})
}

// AN UNREADABLE STORE BILLS NOTHING AND LOSES NOTHING: a sweep that cannot list an
// org leaves its clock where it was, and the next sweep that can bills the span
// whole.
func TestAnUnlistableOrgIsBilledWholeLater(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		f := startFakeS3(t)
		store, err := openStore(t.TempDir(), testMaster, testKMS)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = store.Close() })
		if err := store.CreateProject(t.Context(), mkProject("acme", "shop", "Shop")); err != nil {
			t.Fatal(err)
		}
		f.seedSparse(testBucket, "acme/shop/video.mp4", 41*gb)
		var l ledger
		list := lister(t, store)
		down := func(context.Context, string) (int64, error) { return 0, errors.New("store unwell") }

		if _, err := sweepStorage(t.Context(), store, 100, storageGBMonthMicros, list, meterHeld, l.emit); err != nil {
			t.Fatal(err)
		}
		if _, err := sweepStorage(t.Context(), store, 3700, storageGBMonthMicros, down, meterHeld, l.emit); err != nil {
			t.Fatal(err)
		}
		if got := l.take(); len(got) != 0 {
			t.Fatalf("a sweep that could not list billed %+v", got)
		}
		if _, err := sweepStorage(t.Context(), store, 7300, storageGBMonthMicros, list, meterHeld, l.emit); err != nil {
			t.Fatal(err)
		}
		got := l.take()
		if len(got) != 1 || got[0].u.AmountMicros != 2000 {
			t.Fatalf("the span across the outage billed %+v, want one debit of 2000 µ$ (two hours)", got)
		}
	})
}

// AN ORG THAT LEAVES IS NOT BILLED FOR THE TIME IT WAS GONE. acme deletes its
// site and everything it held; its row is forgotten once it holds nothing, and
// when it comes back a month later with a new site that is a first sight — not a
// month of the new site's size charged backwards.
func TestAnOrgThatLeavesIsNotBilledForTheGap(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		f := startFakeS3(t)
		store, err := openStore(t.TempDir(), testMaster, testKMS)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = store.Close() })
		ctx := t.Context()
		if err := store.CreateProject(ctx, mkProject("acme", "shop", "Shop")); err != nil {
			t.Fatal(err)
		}
		f.seedSparse(testBucket, "acme/shop/video.mp4", 41*gb)
		var l ledger
		list := lister(t, store)
		const t0 int64 = 1_000_000
		for _, at := range []int64{t0, t0 + 3600} {
			if _, err := sweepStorage(ctx, store, at, storageGBMonthMicros, list, meterHeld, l.emit); err != nil {
				t.Fatal(err)
			}
		}
		if got := l.take(); len(got) != 1 || got[0].u.AmountMicros != 1000 {
			t.Fatalf("an hour at 36 GB past the tier billed %+v, want 1000 µ$", got)
		}

		// acme leaves: the project and every byte it held.
		if _, _, err := store.DeleteProject(ctx, "acme", "shop"); err != nil {
			t.Fatal(err)
		}
		f.remove(testBucket, "acme/shop/video.mp4")
		if _, err := sweepStorage(ctx, store, t0+7200, storageGBMonthMicros, list, meterHeld, l.emit); err != nil {
			t.Fatal(err)
		}
		if orgs, _ := store.Holders(ctx); len(orgs) != 0 {
			t.Fatalf("an org that holds nothing and has no project is still swept: %v", orgs)
		}

		// A month later it returns with the same size of site.
		month := t0 + 30*24*3600
		if err := store.CreateProject(ctx, mkProject("acme", "shop", "Shop")); err != nil {
			t.Fatal(err)
		}
		f.seedSparse(testBucket, "acme/shop/video.mp4", 41*gb)
		if _, err := sweepStorage(ctx, store, month, storageGBMonthMicros, list, meterHeld, l.emit); err != nil {
			t.Fatal(err)
		}
		if got := l.take(); len(got) != 0 {
			t.Fatalf("the month acme was gone was billed on its return: %+v", got)
		}
		if _, err := sweepStorage(ctx, store, month+3600, storageGBMonthMicros, list, meterHeld, l.emit); err != nil {
			t.Fatal(err)
		}
		if got := l.take(); len(got) != 1 || got[0].u.AmountMicros != 1000 {
			t.Fatalf("the first hour back billed %+v, want 1000 µ$", got)
		}
	})
}

// A REPUBLISH REPLACES THE SITE, IT DOES NOT ADD A SECOND ONE. acme's site carries
// a 6 GB asset, so acme is billed past the tier; the next publish of the same site
// drops it, and from then on acme holds the new files alone and is billed nothing.
func TestARepublishIsNotBilledTwice(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		f := startFakeS3(t)
		bs := &billServer{available: 100000}
		app := mountSites(t, &fakeAI{}, bs.start(t))
		manifest := func(asset string) map[string]any {
			return map[string]any{"slug": "shop", "files": []map[string]string{
				{"path": "index.html", "content": "<!doctype html><title>shop</title>"},
				{"path": asset, "content": "console.log(1)"},
			}}
		}
		if code, body := doSite(t, app, http.MethodPost, "/v1/project/sites/deploy", "acme", manifest("v1.js")); code != http.StatusOK {
			t.Fatalf("first publish want 200, got %d (%s)", code, body)
		}
		// The first build shipped a large asset alongside its files.
		f.seedSparse(testBucket, "acme/shop/video.mp4", 6*gb)
		var l ledger
		list := lister(t, mounted.State.store)
		const t0 int64 = 1_000_000
		if _, err := sweepStorage(t.Context(), mounted.State.store, t0, storageGBMonthMicros, list, meterHeld, l.emit); err != nil {
			t.Fatal(err)
		}
		if _, err := sweepStorage(t.Context(), mounted.State.store, t0+3600, storageGBMonthMicros, list, meterHeld, l.emit); err != nil {
			t.Fatal(err)
		}
		if got := l.take(); len(got) != 1 || got[0].u.AmountMicros != 28 {
			t.Fatalf("an hour of a site one GB past the tier billed %+v, want one debit of 28 µ$", got)
		}

		if code, body := doSite(t, app, http.MethodPost, "/v1/project/sites/deploy", "acme", manifest("v2.js")); code != http.StatusOK {
			t.Fatalf("republish want 200, got %d (%s)", code, body)
		}
		held, err := measure(mounted, t.Context(), "acme")
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := f.body(testBucket, "acme/shop/video.mp4"); ok || f.count(testBucket+"/acme/shop/v1.js") != 0 {
			t.Fatal("the republish left the first build's files in the site")
		}
		if want := f.bytesUnder(testBucket + "/acme/shop/"); held != want || held > gb {
			t.Fatalf("after the republish acme holds %d bytes, want the new site's %d alone", held, want)
		}
		if _, err := sweepStorage(t.Context(), mounted.State.store, t0+7200, storageGBMonthMicros, list, meterHeld, l.emit); err != nil {
			t.Fatal(err)
		}
		if got := l.take(); len(got) != 0 {
			t.Fatalf("the replaced site was billed again: %+v", got)
		}
		if waitForDebit(func() bool { return bs.debits() > 0 }) {
			t.Fatalf("neither publish may be charged, got %d debits", bs.debits())
		}
	})
}

// A BUILD'S COMPLETION RECONCILES THE PREFIX, so what it did not upload stops
// being stored and stops being counted: a CI or sandbox republish is the same one
// copy an archive republish is.
func TestACompletedBuildCountsOnlyWhatItUploaded(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		f := startFakeS3(t)
		bs := &billServer{available: 100000}
		mountSites(t, nil, bs.start(t))
		p := mkProject("acme", "shop", "Shop")
		if err := mounted.State.store.CreateProject(t.Context(), p); err != nil {
			t.Fatal(err)
		}
		now := time.Now().Unix()
		d := Deployment{ID: "dep_b", ProjectID: p.ID, Org: "acme", Version: 1, Status: "building", Source: sourceBuild,
			Bucket: testBucket, Prefix: sitePrefix("acme", "shop"), CreatedAt: now, UpdatedAt: now}
		if err := mounted.State.store.InsertDeployment(t.Context(), d); err != nil {
			t.Fatal(err)
		}
		f.seedSparse(testBucket, "acme/shop/old-video.mp4", 6*gb) // the previous build's
		f.seed(testBucket, "acme/shop/index.html", "<!doctype html><title>new</title>")
		if _, err := land(mounted, t.Context(), "acme", p, d, ending{live: true, keys: []string{"index.html"}}); err != nil {
			t.Fatal(err)
		}
		held, err := measure(mounted, t.Context(), "acme")
		if err != nil {
			t.Fatal(err)
		}
		if want := int64(len("<!doctype html><title>new</title>")); held != want {
			t.Fatalf("after the build acme holds %d bytes, want the %d it uploaded", held, want)
		}
	})
}

// PUBLISHING THE SAME BUILD AGAIN STORES NOTHING MORE: identical bytes are the
// same release, so the org holds one copy however many times it is published.
func TestRepublishingTheSameReleaseStoresOneCopy(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		f, app := releaseHarness(t)
		seedBuild(f, "acme", "builds/v1", "one")
		for i := range 3 {
			if code, body := publish(t, app, "acme", "shop", "builds/v1"); code != http.StatusOK {
				t.Fatalf("publish %d want 200, got %d (%s)", i, code, body)
			}
		}
		held, err := measure(mounted, t.Context(), "acme")
		if err != nil {
			t.Fatal(err)
		}
		// The build output, and one release copied from it.
		if want := 2 * f.bytesUnder(testBucket+"/acme/builds/v1/"); held != want {
			t.Fatalf("three publishes of one build hold %d bytes, want %d (the source and one release)", held, want)
		}
	})
}

// THE CUSTOMER LOOP PUBLISHES FREE: a template taken by an org with an empty
// wallet opens its sandbox build and goes live, and neither end is charged. Past
// the free tier, the same empty wallet is refused before a grant is minted.
//
// On the per-deploy fee the open was a 402 for this org, and a live close debited
// $1.00.
func TestASandboxBuildIsNotCharged(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, _, f := mountBuilding(t)
		bs := &billServer{available: 0}
		m, err := meter.New(meter.Config{BaseURL: bs.start(t), Timeout: time.Second})
		if err != nil {
			t.Fatal(err)
		}
		mounted.State.bill = cloud.NewMeter(cloud.Deps{Metering: m}, hostingProvider)
		forked(t, app, "acme", map[string]any{"slug": "synapse"})

		opened, err := planeSiteOpen(as("acme"), &client.SiteOpenIn{Slug: "synapse"})
		if err != nil {
			t.Fatalf("an unfunded org's sandbox build was refused: %v", err)
		}
		f.seed(testBucket, "acme/synapse/index.html", "<!doctype html><title>synapse</title>")
		got, err := planeSiteClose(as("acme"), &client.SiteCloseIn{Slug: "synapse", ID: opened.ID, Live: true,
			Commit: "abc", Files: 1, Bytes: 37, Keys: []string{"index.html"}})
		if err != nil || got.Status != "live" {
			t.Fatalf("close: %+v, %v", got, err)
		}
		if waitForDebit(func() bool { return bs.debits() > 0 }) {
			org, body := bs.lastDebit()
			t.Fatalf("a sandbox build was charged: %d debits, last to %q: %s", bs.debits(), org, body)
		}

		holdPastTheTier(t, f, "acme")
		_, err = planeSiteOpen(as("acme"), &client.SiteOpenIn{Slug: "synapse"})
		var he *zip.HTTPError
		if !errors.As(err, &he) || he.Status != http.StatusPaymentRequired {
			t.Fatalf("past the tier with an empty wallet, open = %v, want 402", err)
		}
	})
}

// AN ARCHIVE UPLOAD IS FREE TOO: the untyped deploy route takes an unfunded org's
// site live and charges nothing.
func TestAnArchiveUploadIsNotCharged(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		startFakeS3(t)
		bs := &billServer{available: 0}
		app := mountSites(t, nil, bs.start(t))
		newSite(t, app, "acme", "shop")
		code, body := postRaw(t, app, "/v1/project/shop/deploy", "acme",
			buildZip(t, map[string]string{"index.html": "<!doctype html><title>shop</title>"}))
		if code != http.StatusOK {
			t.Fatalf("an unfunded org's upload want 200, got %d (%s)", code, body)
		}
		if !strings.Contains(string(body), `"status":"live"`) {
			t.Fatalf("the upload did not go live: %s", body)
		}
		if waitForDebit(func() bool { return bs.debits() > 0 }) {
			t.Fatalf("an upload was charged, got %d debits", bs.debits())
		}
	})
}

// THE STORAGE GATE ASKS THE ACCOUNT THE SWEEP DEBITS. An org's storage is billed
// to the org's own account (meterStorage), so past the tier the gate weighs that
// account and no other. alice lives in the signup org with an empty personal
// wallet while the org is funded: she publishes. bob's billing claim names his
// own funded wallet inside acme while acme is empty: he is refused. Every balance
// the gate reads is an org's.
func TestTheStorageGateAsksTheOrgItBills(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		f := startFakeS3(t)
		funds := map[string]int64{"hanzo": 100_000, "hanzo/alice": 0, "acme": 0, "acme/bob": 100_000}
		var mu sync.Mutex
		var asked []string
		mux := http.NewServeMux()
		mux.HandleFunc("/v1/billing/balance", func(w http.ResponseWriter, r *http.Request) {
			user := r.URL.Query().Get("user")
			mu.Lock()
			asked = append(asked, user)
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"available": funds[user]})
		})
		srv := httptest.NewServer(mux)
		t.Cleanup(srv.Close)
		app := mountSites(t, nil, srv.URL)
		holdPastTheTier(t, f, "hanzo")
		holdPastTheTier(t, f, "acme")

		publish := func(org, name, claim string) (int, []byte) {
			b, _ := json.Marshal(map[string]any{"slug": "shop", "files": []map[string]string{
				{"path": "index.html", "content": "<!doctype html><title>shop</title>"},
			}})
			req := httptest.NewRequest(http.MethodPost, "/v1/project/sites/deploy", bytes.NewReader(b))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-Org-Id", org)
			req.Header.Set("X-User-Id", "u-"+name)
			req.Header.Set("X-User-Name", name)
			if claim != "" {
				req.Header.Set("X-Billing-Account-Id", claim)
			}
			resp, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
			if err != nil {
				t.Fatalf("publish as %s/%s: %v", org, name, err)
			}
			defer func() { _ = resp.Body.Close() }()
			out, _ := io.ReadAll(resp.Body)
			return resp.StatusCode, out
		}

		if code, body := publish("hanzo", "alice", ""); code != http.StatusOK {
			t.Fatalf("a member of a funded org was refused for her own empty wallet: %d (%s)", code, body)
		}
		if code, body := publish("acme", "bob", "person:acme/bob"); code != http.StatusPaymentRequired {
			t.Fatalf("an unfunded org's storage was admitted on a member's wallet: %d (%s)", code, body)
		}
		mu.Lock()
		defer mu.Unlock()
		for _, who := range asked {
			if who != "hanzo" && who != "acme" {
				t.Fatalf("the storage gate read %q, a wallet the sweep never debits (asked %v)", who, asked)
			}
		}
		if len(asked) < 2 {
			t.Fatalf("the gate asked %v, want both orgs", asked)
		}
	})
}

// meterHeld is the fence of a meter this process holds.
func meterHeld(context.Context) error { return nil }
