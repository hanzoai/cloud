package content

// red_final_test.go — RED final-pass adversarial suite over Blue's round-3 fixes: the
// per-item store lease (publish.go / framework lock), the server-owned-field gate
// (enforceServerOwned), and the trusted-write marker. Every test re-derives a guarantee
// from the OUTSIDE (raw framework PUT, direct op calls, concurrency) rather than trusting
// Blue's own suite. Vectors, in order: 5 (key collision), 2 (omit/wipe/forge), 3
// (published_at format), 4 (trusted-marker smuggle), 6 (flood: no-5xx/exactly-once/no-leak),
// 7 (two concurrent transitions → one fan-out), 1 (skip-set empty mid-fan-out — the
// double-post window mechanism).

import (
	"context"
	"net/http"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hanzoai/cloud/clients/framework"
)

// ── helpers ──────────────────────────────────────────────────────────────────────

func getDoc(t *testing.T, org, name string) map[string]any {
	t.Helper()
	doc, err := framework.Get(context.Background(), org, "SocialPost", name)
	if err != nil {
		t.Fatalf("framework.Get(%s): %v", name, err)
	}
	return doc.Data
}

// ════════════════════════════════════════════════════════════════════════════════
// Vector 5 — lease-key namespacing: distinct items never collide onto one lease
// ════════════════════════════════════════════════════════════════════════════════

// TestRedFinal_PublishLeaseKey_Injective proves publishLeaseKey is injective across hostile
// (doctype,name) inputs — embedded NUL delimiters, names that spell another doctype+delim,
// empty, unicode, very long. A collision would let one item's publish starve a DIFFERENT
// item (block its lease). The org is a SEPARATE lease column, so cross-org is out of scope
// here (proven at the store level).
func TestRedFinal_PublishLeaseKey_Injective(t *testing.T) {
	type pair struct{ dt, name string }
	pairs := []pair{
		{"SocialPost", "A"},
		{"SocialPost", "B"},
		{"Campaign", "A"},
		{"Asset", "A"},
		// name that embeds the delimiter, trying to shift the boundary to impersonate
		// (Campaign, A):
		{"SocialPost", "\x00Campaign\x00A"},
		{"SocialPost", "A\x00Campaign\x00A"},
		// name equal to another doctype (no delimiter tricks) — must still be distinct from
		// (that-doctype, ""):
		{"SocialPost", "Campaign"},
		{"Campaign", ""},
		{"SocialPost", ""},
		{"Asset", ""},
		// unicode + very long
		{"SocialPost", "café—launch—🚀"},
		{"Asset", string(make([]byte, 4096))},
	}
	seen := map[string]pair{}
	for _, p := range pairs {
		k := publishLeaseKey(p.dt, p.name)
		if prev, dup := seen[k]; dup {
			t.Fatalf("KEY COLLISION: (%q,%q) and (%q,%q) map to the same lease key %q — one item "+
				"can starve the other", prev.dt, prev.name, p.dt, p.name, k)
		}
		seen[k] = p
	}
}

// ════════════════════════════════════════════════════════════════════════════════
// Vector 2 — external_ids: omit preserves, empty/null do NOT wipe, forge rejected
// ════════════════════════════════════════════════════════════════════════════════

// TestRedFinal_ExternalIDs_OmitPreserves_NoWipe_NoForge is the reincarnation check of the
// RED-1 forge. Once a real fan-out has recorded external_ids, a client raw-PUT must never
// (a) wipe it via {} or null (which would reset the skip-set → re-post), nor (b) forge a
// different value. Only omission is allowed, and it PRESERVES. After every attack the
// recorded skip-set must be intact AND a re-publish must post NOTHING.
func TestRedFinal_ExternalIDs_OmitPreserves_NoWipe_NoForge(t *testing.T) {
	const org = "acme"
	app := mountContent(t)
	installMarketing(t, app, org)
	stub := newSocialStub(t, []socialIntegration{{ID: "int_x", Identifier: "x"}})
	useStub(t, stub)

	name := createSocialPost(t, app, org, map[string]any{"caption": "launch", "channels": "x"})
	for _, to := range []string{StatusInReview, StatusApproved, StatusPublished} {
		if code, b := req(t, app, http.MethodPost, "/v1/content/SocialPost/"+name+"/transition", org,
			map[string]any{"to": to}); code != http.StatusOK {
			t.Fatalf("transition →%s: %d %s", to, code, b)
		}
	}
	recorded := docExternalIDs(t, org, name)
	if len(recorded) != 1 || recorded["int_x"] == "" || recorded["int_x"] == "FORGED" {
		t.Fatalf("setup: expected one real recorded external id, got %v", recorded)
	}
	realID := recorded["int_x"].(string)
	postsAfterSetup := stubPosts(stub)

	base := map[string]any{"title": "Post", "caption": "launch", "channels": "x", "status": StatusPublished}
	withField := func(k string, v any) map[string]any {
		m := map[string]any{}
		for k, v := range base {
			m[k] = v
		}
		m[k] = v
		return m
	}

	// (a) WIPE via empty map — must be REJECTED (4xx), skip-set intact.
	if code, b := req(t, app, http.MethodPut, "/v1/framework/SocialPost/"+name, org,
		withField("external_ids", map[string]any{})); code < 400 || code >= 500 {
		t.Fatalf("external_ids:{} against a non-empty recorded set must 4xx (no wipe), got %d %s", code, b)
	}
	assertExternalID(t, org, name, realID, "after {} wipe attempt")

	// (b) WIPE via JSON null — must be REJECTED.
	if code, b := req(t, app, http.MethodPut, "/v1/framework/SocialPost/"+name, org,
		withField("external_ids", nil)); code < 400 || code >= 500 {
		t.Fatalf("external_ids:null against a non-empty recorded set must 4xx (no wipe), got %d %s", code, b)
	}
	assertExternalID(t, org, name, realID, "after null wipe attempt")

	// (c) FORGE a different value — must be REJECTED.
	if code, b := req(t, app, http.MethodPut, "/v1/framework/SocialPost/"+name, org,
		withField("external_ids", map[string]any{"int_x": "HACKED", "int_evil": "E"})); code < 400 || code >= 500 {
		t.Fatalf("forged external_ids must 4xx, got %d %s", code, b)
	}
	assertExternalID(t, org, name, realID, "after forge attempt")

	// (d) OMIT external_ids on a legit partial edit — must SUCCEED and PRESERVE (full-replace
	// would otherwise drop the reconciliation state).
	if code, b := req(t, app, http.MethodPut, "/v1/framework/SocialPost/"+name, org, base); code != http.StatusOK {
		t.Fatalf("omitting external_ids on a legit edit must succeed (preserve), got %d %s", code, b)
	}
	assertExternalID(t, org, name, realID, "after omit-preserve edit")

	// (e) The skip-set survived every attack: a re-publish posts NOTHING (idempotent).
	if _, err := Publish(context.Background(), org, PublishInput{DocType: DocTypeSocialPost, Name: name}); err != nil {
		t.Fatalf("re-publish: %v", err)
	}
	if got := stubPosts(stub); got != postsAfterSetup {
		t.Fatalf("re-publish after wipe attempts posted %d new times — the skip-set was reset "+
			"(external_ids wipe leaked through)", got-postsAfterSetup)
	}
}

// ════════════════════════════════════════════════════════════════════════════════
// Vector 3 — published_at: forge rejected, same-instant format variants are no-ops
// ════════════════════════════════════════════════════════════════════════════════

// TestRedFinal_PublishedAt_ForgeAndFormat covers the format-differential: a DIFFERENT
// instant must be rejected (no forge of "went live earlier/later"), while the SAME instant
// spelled as canonical OR RFC3339 must be an accepted no-op (no false-reject that would
// break a legit full-doc round-trip), and omission preserves.
func TestRedFinal_PublishedAt_ForgeAndFormat(t *testing.T) {
	const org = "acme"
	app := mountContent(t)
	installMarketing(t, app, org)
	stub := newSocialStub(t, []socialIntegration{{ID: "int_x", Identifier: "x"}})
	useStub(t, stub)

	name := createSocialPost(t, app, org, map[string]any{"caption": "launch", "channels": "x"})
	for _, to := range []string{StatusInReview, StatusApproved, StatusPublished} {
		if code, b := req(t, app, http.MethodPost, "/v1/content/SocialPost/"+name+"/transition", org,
			map[string]any{"to": to}); code != http.StatusOK {
			t.Fatalf("transition →%s: %d %s", to, code, b)
		}
	}
	stored, _ := getDoc(t, org, name)["published_at"].(string)
	if stored == "" {
		t.Fatal("setup: published_at must be stamped after →published")
	}
	// stored is canonical "2006-01-02 15:04:05" (validate canonicalizes). Derive the RFC3339
	// spelling of the SAME instant to prove format convergence.
	ts, err := time.Parse("2006-01-02 15:04:05", stored)
	if err != nil {
		t.Fatalf("stored published_at not canonical: %q (%v)", stored, err)
	}
	rfc := ts.UTC().Format(time.RFC3339)

	base := map[string]any{"title": "Post", "caption": "launch", "channels": "x", "status": StatusPublished}
	put := func(pa any) (int, []byte) {
		m := map[string]any{}
		for k, v := range base {
			m[k] = v
		}
		if pa != nil {
			m["published_at"] = pa
		}
		return req(t, app, http.MethodPut, "/v1/framework/SocialPost/"+name, org, m)
	}

	// FORGE a different instant — must 4xx, stored value unchanged.
	if code, b := put("2999-01-01 00:00:00"); code < 400 || code >= 500 {
		t.Fatalf("forged published_at (different instant) must 4xx, got %d %s", code, b)
	}
	if now, _ := getDoc(t, org, name)["published_at"].(string); now != stored {
		t.Fatalf("forge changed published_at: %q → %q", stored, now)
	}

	// SAME instant, canonical spelling — accepted no-op.
	if code, b := put(stored); code != http.StatusOK {
		t.Fatalf("round-trip of the exact stored published_at must be an accepted no-op, got %d %s", code, b)
	}
	// SAME instant, RFC3339 spelling — validate canonicalizes to the stored value → no-op,
	// NOT a false-reject (a full-doc console PUT must not break).
	if code, b := put(rfc); code != http.StatusOK {
		t.Fatalf("same-instant RFC3339 published_at must canonicalize to a no-op, got %d %s (stored=%q rfc=%q)",
			code, b, stored, rfc)
	}
	// OMIT — preserved.
	if code, b := put(nil); code != http.StatusOK {
		t.Fatalf("omitting published_at must preserve (succeed), got %d %s", code, b)
	}
	if now, _ := getDoc(t, org, name)["published_at"].(string); now != stored {
		t.Fatalf("published_at not preserved across format/omit round-trips: %q → %q", stored, now)
	}
}

// ════════════════════════════════════════════════════════════════════════════════
// Vector 4 — the trusted-write marker cannot be smuggled through a transition body
// ════════════════════════════════════════════════════════════════════════════════

// TestRedFinal_TransitionCannotSmuggleServerOwned proves the ONE trusted write reachable
// from a client (POST .../transition) carries only SERVER truth: the handler binds just
// {to, scheduleAt}, so extra body fields (external_ids/published_at) are dropped before the
// trusted UpdateData. A client cannot ride the trusted marker to plant server-owned fields.
func TestRedFinal_TransitionCannotSmuggleServerOwned(t *testing.T) {
	const org = "acme"
	app := mountContent(t)
	installMarketing(t, app, org)
	stub := newSocialStub(t, []socialIntegration{{ID: "int_x", Identifier: "x"}})
	useStub(t, stub)

	name := createSocialPost(t, app, org, map[string]any{"caption": "launch", "channels": "x"})
	for _, to := range []string{StatusInReview, StatusApproved} {
		if code, b := req(t, app, http.MethodPost, "/v1/content/SocialPost/"+name+"/transition", org,
			map[string]any{"to": to}); code != http.StatusOK {
			t.Fatalf("transition →%s: %d %s", to, code, b)
		}
	}
	// Transition to published while trying to SMUGGLE external_ids + a forged published_at in
	// the same body. The smuggle must be ignored; only the real fan-out id lands.
	code, b := req(t, app, http.MethodPost, "/v1/content/SocialPost/"+name+"/transition", org, map[string]any{
		"to":           StatusPublished,
		"external_ids": map[string]any{"int_x": "SMUGGLE", "int_evil": "E"},
		"published_at": "2999-01-01 00:00:00",
	})
	if code != http.StatusOK {
		t.Fatalf("transition →published: %d %s", code, b)
	}
	ext := docExternalIDs(t, org, name)
	if len(ext) != 1 || ext["int_x"] == "SMUGGLE" || ext["int_x"] == "" || ext["int_evil"] != nil {
		t.Fatalf("SMUGGLE: transition body planted server-owned external_ids: %v", ext)
	}
	if pa, _ := getDoc(t, org, name)["published_at"].(string); pa == "" || pa == "2999-01-01 00:00:00" {
		t.Fatalf("SMUGGLE: transition body planted a forged published_at: %q", pa)
	}
}

// ════════════════════════════════════════════════════════════════════════════════
// Vector 6 — same-item flood: exactly-once, never 5xx, no goroutine/lease leak
// ════════════════════════════════════════════════════════════════════════════════

// TestRedFinal_SameItemFlood hammers ONE approved item with N concurrent publishers (direct
// ops) and, separately, concurrent HTTP publishes, asserting exactly one channel post, zero
// errors/5xx, and no goroutine leak (guards a future heartbeat regression, and proves the
// lease is always released).
func TestRedFinal_SameItemFlood(t *testing.T) {
	const org = "acme"
	app := mountContent(t)
	installMarketing(t, app, org)
	stub := newSocialStub(t, []socialIntegration{{ID: "int_x", Identifier: "x"}})
	useStub(t, stub)

	name := createSocialPost(t, app, org, map[string]any{"caption": "flood", "channels": "x"})
	for _, to := range []string{StatusInReview, StatusApproved} {
		if code, b := req(t, app, http.MethodPost, "/v1/content/SocialPost/"+name+"/transition", org,
			map[string]any{"to": to}); code != http.StatusOK {
			t.Fatalf("transition →%s: %d %s", to, code, b)
		}
	}

	settleGoroutines() // let harness setup goroutines settle before baseline
	base := runtime.NumGoroutine()

	const n = 32
	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, errs[i] = Publish(context.Background(), org, PublishInput{DocType: DocTypeSocialPost, Name: name})
		}(i)
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("flood publisher %d returned an error (must be clean/in_progress): %v", i, err)
		}
	}
	if got := stubPosts(stub); got != 1 {
		t.Fatalf("exactly one post expected under %d concurrent publishers, got %d", n, got)
	}

	// No goroutine leak: Publish holds no background goroutine and always releases its lease.
	settleGoroutines()
	if leaked := runtime.NumGoroutine() - base; leaked > 3 {
		t.Fatalf("goroutine leak after flood: base=%d now=%d (leaked=%d)", base, runtime.NumGoroutine(), leaked)
	}

	// Lease is free again — a fresh publisher wins immediately (no dead-lease wedge).
	if _, err := Publish(context.Background(), org, PublishInput{DocType: DocTypeSocialPost, Name: name}); err != nil {
		t.Fatalf("post-flood publish (lease must be free): %v", err)
	}

	// The HTTP path never emits a 5xx under contention either.
	var hg sync.WaitGroup
	go5 := make(chan struct{})
	codes := make([]int, 8)
	for i := 0; i < 8; i++ {
		hg.Add(1)
		go func(i int) {
			defer hg.Done()
			<-go5
			codes[i], _ = req(t, app, http.MethodPost, "/v1/content/publish", org,
				map[string]any{"doctype": DocTypeSocialPost, "name": name})
		}(i)
	}
	close(go5)
	hg.Wait()
	for i, code := range codes {
		if code >= 500 {
			t.Fatalf("HTTP publish %d returned a 5xx under contention: %d", i, code)
		}
	}
}

// ════════════════════════════════════════════════════════════════════════════════
// Vector 7 — two concurrent transitions → published: one fan-out (same lease)
// ════════════════════════════════════════════════════════════════════════════════

func TestRedFinal_TwoConcurrentTransitionsPublished_OneFanout(t *testing.T) {
	const org = "acme"
	app := mountContent(t)
	installMarketing(t, app, org)
	stub := newSocialStub(t, []socialIntegration{{ID: "int_x", Identifier: "x"}})
	useStub(t, stub)

	name := createSocialPost(t, app, org, map[string]any{"caption": "race", "channels": "x"})
	for _, to := range []string{StatusInReview, StatusApproved} {
		if code, b := req(t, app, http.MethodPost, "/v1/content/SocialPost/"+name+"/transition", org,
			map[string]any{"to": to}); code != http.StatusOK {
			t.Fatalf("transition →%s: %d %s", to, code, b)
		}
	}

	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make([]error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, errs[i] = Transition(context.Background(), org, DocTypeSocialPost, name, StatusPublished, "")
		}(i)
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("concurrent transition %d must not error: %v", i, err)
		}
	}
	if got := stubPosts(stub); got != 1 {
		t.Fatalf("two concurrent transitions must fan out exactly once (one lease), got %d posts", got)
	}
	if st, _ := getDoc(t, org, name)["status"].(string); st != StatusPublished {
		t.Fatalf("final status must be published, got %q", st)
	}
}

// ════════════════════════════════════════════════════════════════════════════════
// Vector 1 — mechanism: external_ids (the skip-set) is EMPTY during the fan-out
// ════════════════════════════════════════════════════════════════════════════════

// blockDist is a Distributor that parks inside Publish's critical section so the test can
// observe that external_ids has NOT been recorded yet — i.e. a lease stolen at this instant
// (proven possible once TTL < fan-out by TestRedLease_ExpiredLiveHolderIsPreempted) would
// re-read an EMPTY skip-set and re-post the whole fan-out. Together the two tests are the
// full repro of the Vector-1 double-post window, without a 5-minute wait.
type blockDist struct {
	entered chan struct{}
	release chan struct{}
	posts   int64
}

func (b *blockDist) Channels(context.Context, string) ([]Channel, error) {
	return []Channel{{ID: "int_x", Provider: "x"}}, nil
}
func (b *blockDist) Publish(_ context.Context, _ string, _ DistributeRequest) (DistributeResult, error) {
	close(b.entered)
	<-b.release
	atomic.AddInt64(&b.posts, 1)
	return DistributeResult{
		ExternalIDs: map[string]string{"int_x": "sp_real_1"},
		Channels:    []ChannelResult{{Channel: "int_x", Status: "distributed", ExternalID: "sp_real_1"}},
	}, nil
}

func TestRedFinal_SkipSetEmptyDuringFanout(t *testing.T) {
	const org = "acme"
	app := mountContent(t)
	installMarketing(t, app, org)
	// Mount a blocking distributor directly onto the singleton.
	bd := &blockDist{entered: make(chan struct{}), release: make(chan struct{})}
	mounted.State.dist = bd

	name := createSocialPost(t, app, org, map[string]any{"caption": "slow", "channels": "x"})
	for _, to := range []string{StatusInReview, StatusApproved} {
		if code, b := req(t, app, http.MethodPost, "/v1/content/SocialPost/"+name+"/transition", org,
			map[string]any{"to": to}); code != http.StatusOK {
			t.Fatalf("transition →%s: %d %s", to, code, b)
		}
	}

	done := make(chan struct{})
	go func() {
		_, _ = Publish(context.Background(), org, PublishInput{DocType: DocTypeSocialPost, Name: name})
		close(done)
	}()

	<-bd.entered // publisher is now inside the critical section, mid fan-out

	// THE WINDOW: external_ids is not yet recorded. A contender that stole an expired lease
	// here would see this empty set and re-post everything.
	if ext := docExternalIDs(t, org, name); len(ext) != 0 {
		t.Fatalf("expected an EMPTY skip-set mid-fan-out (that is the window), got %v", ext)
	}

	close(bd.release)
	<-done

	// After the fan-out completes, the skip-set is recorded (records-at-end, not incrementally).
	if ext := docExternalIDs(t, org, name); len(ext) != 1 || ext["int_x"] != "sp_real_1" {
		t.Fatalf("post-fan-out external_ids must record the real id, got %v", ext)
	}
	if atomic.LoadInt64(&bd.posts) != 1 {
		t.Fatalf("blockDist must have posted exactly once, got %d", bd.posts)
	}
}

// ── small shared helpers ─────────────────────────────────────────────────────────

func stubPosts(s *socialStub) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.posts)
}

func assertExternalID(t *testing.T, org, name, want, when string) {
	t.Helper()
	ext := docExternalIDs(t, org, name)
	if len(ext) != 1 || ext["int_x"] != want {
		t.Fatalf("%s: recorded external_ids must be intact {int_x:%q}, got %v", when, want, ext)
	}
}

// settleGoroutines waits briefly for transient goroutines to exit so a leak assertion is
// stable (not a hard sleep race: it polls a short, bounded window).
func settleGoroutines() {
	for i := 0; i < 20; i++ {
		runtime.GC()
		time.Sleep(5 * time.Millisecond)
	}
}
