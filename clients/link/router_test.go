package link

// router_test.go proves the security invariants that are the point of this
// increment: routing through a linked account works; cycling on a live 429 stays
// strictly in-org; org A can NEVER route through org B's account (the critical
// isolation test, run against the REAL store); a credential never reaches a log, an
// error, or the audit line; and the router fails secure — it never substitutes a
// platform key when a caller's own accounts are exhausted or unresolvable.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	luxlog "github.com/luxfi/log"
)

// ── fakes (the seams the router composes) ─────────────────────────────────────────

// fakeLinks is a Links seam returning preset accounts per (org, subject), recording
// every scope it was asked for so a test can assert the router never queried a
// foreign scope.
type fakeLinks struct {
	mu    sync.Mutex
	byKey map[string][]Link
	asked []string
}

func (f *fakeLinks) put(org, subject string, ls ...Link) {
	if f.byKey == nil {
		f.byKey = map[string][]Link{}
	}
	f.byKey[org+"/"+subject] = ls
}

func (f *fakeLinks) ListLinked(_ context.Context, org, subject string) ([]Link, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asked = append(f.asked, org+"/"+subject)
	return f.byKey[org+"/"+subject], nil
}

// resolveKey is the (org, subject, account) a Resolve was asked for — the exact
// tuple the isolation test asserts never crosses a tenant.
type resolveKey struct {
	org, subject, account string
}

// fakeResolver returns preset credentials keyed by (org, account.String()), records
// every Resolve it was asked for, and can be told to error for specific accounts (to
// exercise fail-secure). It NEVER invents a credential for a scope it wasn't seeded
// with — so a router that asked for a foreign account gets an error, and the test
// also sees the forbidden ask in `asked`.
type fakeResolver struct {
	mu    sync.Mutex
	creds map[string]Credential // key: org + "|" + account.String()
	fail  map[string]error      // same key → error to return
	asked []resolveKey
}

func (f *fakeResolver) seed(org string, a Account, c Credential) {
	if f.creds == nil {
		f.creds = map[string]Credential{}
	}
	f.creds[org+"|"+a.String()] = c
}

func (f *fakeResolver) failOn(org string, a Account, err error) {
	if f.fail == nil {
		f.fail = map[string]error{}
	}
	f.fail[org+"|"+a.String()] = err
}

func (f *fakeResolver) Resolve(_ context.Context, org, subject string, a Account) (Credential, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asked = append(f.asked, resolveKey{org, subject, a.String()})
	key := org + "|" + a.String()
	if err, ok := f.fail[key]; ok {
		return Credential{}, err
	}
	if c, ok := f.creds[key]; ok {
		return c, nil
	}
	return Credential{}, ErrNoCredential
}

// askedOrgs returns the distinct orgs a Resolve was requested for.
func (f *fakeResolver) askedOrgs() map[string]bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]bool{}
	for _, k := range f.asked {
		out[k.org] = true
	}
	return out
}

// fakeUpstream returns a preset outcome per credential token, and records every
// token it was handed — so the isolation test can prove one org's token never
// reaches the upstream while another org routes.
type fakeUpstream struct {
	mu       sync.Mutex
	outcome  map[string]func() (Result, error) // key: cred.Token
	gotToken []string
	gotAcct  []string
}

func (f *fakeUpstream) on(token string, fn func() (Result, error)) {
	if f.outcome == nil {
		f.outcome = map[string]func() (Result, error){}
	}
	f.outcome[token] = fn
}

func (f *fakeUpstream) Call(_ context.Context, cred Credential, a Account, _ Request) (Result, error) {
	f.mu.Lock()
	f.gotToken = append(f.gotToken, cred.Token)
	f.gotAcct = append(f.gotAcct, a.String())
	fn := f.outcome[cred.Token]
	f.mu.Unlock()
	if fn == nil {
		return Result{}, fmt.Errorf("unexpected upstream call for %s", a)
	}
	return fn()
}

func (f *fakeUpstream) sawToken(tok string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, t := range f.gotToken {
		if t == tok {
			return true
		}
	}
	return false
}

// spyMeter records the metering calls so a test can assert what was metered.
type spyMeter struct {
	mu   sync.Mutex
	recs []struct {
		org, subject, account, kind string
		res                         Result
	}
}

func (m *spyMeter) RecordRouted(_ context.Context, org, subject string, a Account, kind string, res Result) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.recs = append(m.recs, struct {
		org, subject, account, kind string
		res                         Result
	}{org, subject, a.String(), kind, res})
}

// ok is an Upstream outcome that succeeds with a fixed token count.
func ok(model string) func() (Result, error) {
	return func() (Result, error) {
		return Result{Model: model, PromptTokens: 10, CompletionTokens: 20, TotalTokens: 30}, nil
	}
}

// quota is an Upstream outcome that returns a live 429 the router must cycle on.
func quota(provider string) func() (Result, error) {
	return func() (Result, error) {
		return Result{}, &QuotaError{Provider: provider, Status: 429, Err: errors.New("rate limit reached")}
	}
}

// linkedAPIKey is a helper to build a linked api-key account row.
func linkedAPIKey(org, subject, provider, account string) Link {
	return Link{Org: org, User: subject, Provider: provider, Account: account, Kind: KindAPIKey, Status: StatusLinked}
}

func newTestRouter(t *testing.T, links Links, res Resolver, up Upstream, meter Meter, pol Policy) *Router {
	t.Helper()
	r, err := NewRouter(Config{Links: links, Resolver: res, Upstream: up, Meter: meter, Policy: pol, Now: func() time.Time { return time.Unix(1_700_000_000, 0) }})
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	return r
}

// ── routing works ─────────────────────────────────────────────────────────────────

func TestRouteThroughLinkedAccount(t *testing.T) {
	links := &fakeLinks{}
	links.put("acme", "alice", linkedAPIKey("acme", "alice", "openai", "work"))
	res := &fakeResolver{}
	res.seed("acme", Account{"openai", "work"}, Credential{Token: "tok-acme-work"})
	up := &fakeUpstream{}
	up.on("tok-acme-work", ok("gpt-4o"))
	meter := &spyMeter{}
	r := newTestRouter(t, links, res, up, meter, PolicyPlan)

	out, a, err := r.Route(context.Background(), "acme", "alice", auto(), Request{Model: "gpt-4o"})
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if a.Provider != "openai" || a.Profile != "work" {
		t.Fatalf("served through wrong account: %+v", a)
	}
	if out.TotalTokens != 30 {
		t.Fatalf("wrong result: %+v", out)
	}
	if !up.sawToken("tok-acme-work") {
		t.Fatalf("upstream never received the resolved credential")
	}
	if len(meter.recs) != 1 || meter.recs[0].account != "openai:work" {
		t.Fatalf("expected one metered record for openai:work, got %+v", meter.recs)
	}
}

// ── cycling on a live 429 stays in-org ────────────────────────────────────────────

func TestCycleOnQuotaStaysInOrg(t *testing.T) {
	links := &fakeLinks{}
	// acme has TWO accounts; a subscription (tried first under PolicyPlan) that 429s,
	// then an api-key that serves. A DIFFERENT org's account is also present in the
	// resolver + upstream, and must never be touched.
	sub := Link{Org: "acme", User: "alice", Provider: "anthropic", Account: "max", Kind: KindSubscription, Status: StatusLinked}
	key := linkedAPIKey("acme", "alice", "anthropic", "backup")
	links.put("acme", "alice", sub, key)

	res := &fakeResolver{}
	res.seed("acme", Account{"anthropic", "max"}, Credential{Token: "tok-acme-max"})
	res.seed("acme", Account{"anthropic", "backup"}, Credential{Token: "tok-acme-backup"})
	res.seed("evilco", Account{"anthropic", "victim"}, Credential{Token: "tok-evilco-victim"}) // must never be asked

	up := &fakeUpstream{}
	up.on("tok-acme-max", quota("anthropic"))     // first candidate 429s
	up.on("tok-acme-backup", ok("claude-opus"))   // cycle lands here
	up.on("tok-evilco-victim", ok("claude-opus")) // a trap: if ever called, isolation broke

	r := newTestRouter(t, links, res, up, &spyMeter{}, PolicyPlan)
	out, a, err := r.Route(context.Background(), "acme", "alice", auto(), Request{Model: "claude-opus"})
	if err != nil {
		t.Fatalf("Route should have cycled to the backup account, got: %v", err)
	}
	if a.String() != "anthropic:backup" {
		t.Fatalf("cycled to wrong account: %s", a.String())
	}
	if out.Model != "claude-opus" {
		t.Fatalf("wrong result after cycle: %+v", out)
	}
	// The cycle used ONLY acme's accounts, in order, and NEVER reached evilco's.
	if orgs := res.askedOrgs(); orgs["evilco"] {
		t.Fatalf("cycling crossed the org boundary: resolver asked for evilco")
	}
	if up.sawToken("tok-evilco-victim") {
		t.Fatalf("cycling routed through another org's credential")
	}
}

// ── the CRITICAL isolation test, against the REAL store ───────────────────────────

func TestOrgCannotRouteThroughAnotherOrgsAccount(t *testing.T) {
	// A real SQLite store is the tenant boundary: ListLinked(org, subject) can only
	// ever return that scope's rows, so the router — whose ONLY candidate source is
	// ListLinked — cannot construct a cross-org candidate. We prove it end to end.
	store, err := openStore(filepath.Join(t.TempDir(), "link.db"))
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	defer func() { _ = store.Close() }()
	ctx := context.Background()
	now := time.Now().Unix()
	mustUpsert := func(l Link) {
		l.CreatedAt, l.UpdatedAt, l.LastSeen = now, now, now
		if _, e := store.Upsert(ctx, l); e != nil {
			t.Fatalf("seed upsert: %v", e)
		}
	}
	// acme/alice linked "openai:work"; victimco/carol linked "openai:paid". Distinct
	// tenants, coincidentally the same provider.
	mustUpsert(Link{ID: "l1", Org: "acme", User: "alice", Machine: "m1", Provider: "openai", Account: "work", Kind: KindAPIKey, Status: StatusLinked})
	mustUpsert(Link{ID: "l2", Org: "victimco", User: "carol", Machine: "m2", Provider: "openai", Account: "paid", Kind: KindAPIKey, Status: StatusLinked})

	res := &fakeResolver{}
	res.seed("acme", Account{"openai", "work"}, Credential{Token: "tok-acme"})
	res.seed("victimco", Account{"openai", "paid"}, Credential{Token: "tok-VICTIM-secret"})
	up := &fakeUpstream{}
	up.on("tok-acme", ok("gpt-4o"))
	up.on("tok-VICTIM-secret", ok("gpt-4o"))
	r := newTestRouter(t, store, res, up, &spyMeter{}, PolicyPlan)

	// 1) acme routes: it reaches ONLY its own account; the victim's is never resolved
	//    and its token never reaches the upstream — no selector can change that.
	for _, sel := range []Selection{
		auto(),
		{Account: Account{"openai", "paid"}, Pinned: true}, // acme naming the victim's PROFILE
		{Account: Account{"openai", ""}, Pinned: true},     // acme naming the provider open
	} {
		if _, _, err := r.Route(ctx, "acme", "alice", sel, Request{Model: "gpt-4o"}); err != nil && sel.Account.Profile != "paid" {
			t.Fatalf("acme legitimate route failed for %+v: %v", sel, err)
		}
	}
	if res.askedOrgs()["victimco"] {
		t.Fatalf("ISOLATION BREACH: acme's routing resolved a victimco account")
	}
	if up.sawToken("tok-VICTIM-secret") {
		t.Fatalf("ISOLATION BREACH: acme routed through victimco's credential")
	}

	// 2) acme pinning the victim's exact account resolves to "unavailable" — acme has
	//    no such linked row, so there is no candidate at all.
	_, _, err = r.Route(ctx, "acme", "alice", Selection{Account: Account{"openai", "paid"}, Pinned: true}, Request{Model: "gpt-4o"})
	if !errors.Is(err, ErrAccountUnavailable) && !errors.Is(err, ErrNoLinkedAccount) {
		t.Fatalf("acme pinning victim account should be unavailable, got: %v", err)
	}

	// 3) victimco routes through ITS OWN account normally — isolation is symmetric,
	//    not a lockout.
	_, a, err := r.Route(ctx, "victimco", "carol", auto(), Request{Model: "gpt-4o"})
	if err != nil || a.String() != "openai:paid" {
		t.Fatalf("victimco own-account route failed: a=%s err=%v", a.String(), err)
	}
}

// ── credentials never logged / never in errors ────────────────────────────────────

func TestCredentialsNeverLogged(t *testing.T) {
	const secret = "sk-SUPER-SECRET-TOKEN-should-never-appear"
	var buf bytes.Buffer
	log := luxlog.New("test").Output(&buf)

	links := &fakeLinks{}
	links.put("acme", "alice",
		linkedAPIKey("acme", "alice", "openai", "a"),
		linkedAPIKey("acme", "alice", "openai", "b"),
	)
	res := &fakeResolver{}
	res.seed("acme", Account{"openai", "a"}, Credential{Token: secret})
	res.seed("acme", Account{"openai", "b"}, Credential{Token: secret})
	up := &fakeUpstream{}
	up.on(secret, quota("openai")) // both accounts 429 → the router cycles then exhausts

	r, err := NewRouter(Config{Links: links, Resolver: res, Upstream: up, Logger: log, Policy: PolicyPlan, Now: time.Now})
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	_, _, rerr := r.Route(context.Background(), "acme", "alice", auto(), Request{Model: "gpt-4o"})
	if !errors.Is(rerr, ErrAllExhausted) {
		t.Fatalf("expected ErrAllExhausted, got %v", rerr)
	}
	// Prove the capture works before asserting absence — a cycle logs the account id,
	// so an empty buffer would make the leak check vacuous.
	if !strings.Contains(buf.String(), "openai") {
		t.Fatalf("log capture is not wired (expected an account id in logs): %q", buf.String())
	}
	if strings.Contains(buf.String(), secret) {
		t.Fatalf("CREDENTIAL LEAK: the token appeared in logs:\n%s", buf.String())
	}
	if rerr != nil && strings.Contains(rerr.Error(), secret) {
		t.Fatalf("CREDENTIAL LEAK: the token appeared in the returned error: %v", rerr)
	}
	// Defence in depth: the Credential redacts under every fmt verb.
	c := Credential{Token: secret}
	for _, s := range []string{c.String(), fmt.Sprintf("%v", c), fmt.Sprintf("%+v", c), fmt.Sprintf("%#v", c), fmt.Sprintf("%s", c)} {
		if strings.Contains(s, secret) {
			t.Fatalf("CREDENTIAL LEAK: Credential rendered its token: %s", s)
		}
	}
}

// ── fail secure: no fallback to a platform key ────────────────────────────────────

func TestFailSecureNoFallbackWhenCredentialUnresolvable(t *testing.T) {
	links := &fakeLinks{}
	links.put("acme", "alice", linkedAPIKey("acme", "alice", "openai", "work"))
	res := &fakeResolver{}
	res.failOn("acme", Account{"openai", "work"}, errors.New("kms: secret not found"))
	up := &fakeUpstream{} // seeded with NOTHING: any Call is an unexpected fallback
	r := newTestRouter(t, links, res, up, &spyMeter{}, PolicyPlan)

	_, _, err := r.Route(context.Background(), "acme", "alice", auto(), Request{Model: "gpt-4o"})
	if !errors.Is(err, ErrAllExhausted) && !errors.Is(err, ErrAccountUnavailable) {
		t.Fatalf("unresolvable credential should exhaust, got: %v", err)
	}
	if len(up.gotToken) != 0 {
		t.Fatalf("FAIL-OPEN: upstream was dialed despite an unresolvable credential (would be a platform-key fallback)")
	}
}

// ── non-quota errors are terminal (no cycling, no double-serve) ────────────────────

func TestNonQuotaErrorIsTerminal(t *testing.T) {
	links := &fakeLinks{}
	links.put("acme", "alice",
		linkedAPIKey("acme", "alice", "openai", "a"),
		linkedAPIKey("acme", "alice", "openai", "b"),
	)
	res := &fakeResolver{}
	res.seed("acme", Account{"openai", "a"}, Credential{Token: "tok-a"})
	res.seed("acme", Account{"openai", "b"}, Credential{Token: "tok-b"})
	up := &fakeUpstream{}
	up.on("tok-a", func() (Result, error) { return Result{}, errors.New("400 invalid request") })
	up.on("tok-b", ok("gpt-4o")) // must NOT be reached — a 400 is terminal
	r := newTestRouter(t, links, res, up, &spyMeter{}, PolicyPlan)

	_, _, err := r.Route(context.Background(), "acme", "alice", auto(), Request{Model: "gpt-4o"})
	if err == nil || strings.Contains(err.Error(), "should") {
		t.Fatalf("expected the terminal 400 to propagate, got: %v", err)
	}
	if up.sawToken("tok-b") {
		t.Fatalf("a non-quota error must NOT cycle to another account")
	}
}

// ── principal + selection edge cases ──────────────────────────────────────────────

func TestNoPrincipalFailsClosed(t *testing.T) {
	r := newTestRouter(t, &fakeLinks{}, &fakeResolver{}, &fakeUpstream{}, nil, PolicyPlan)
	for _, tc := range []struct{ org, subject string }{{"", "alice"}, {"acme", ""}, {"", ""}} {
		if _, _, err := r.Route(context.Background(), tc.org, tc.subject, auto(), Request{}); !errors.Is(err, ErrNoPrincipal) {
			t.Fatalf("blank principal (%q,%q) must fail closed, got: %v", tc.org, tc.subject, err)
		}
	}
}

func TestNoLinkedAccount(t *testing.T) {
	r := newTestRouter(t, &fakeLinks{}, &fakeResolver{}, &fakeUpstream{}, nil, PolicyPlan)
	if _, _, err := r.Route(context.Background(), "acme", "alice", auto(), Request{}); !errors.Is(err, ErrNoLinkedAccount) {
		t.Fatalf("a caller with no accounts should get ErrNoLinkedAccount, got: %v", err)
	}
}

// ── policies ──────────────────────────────────────────────────────────────────────

func withUsage(l Link, sessionPct float64) Link {
	l.Usage = fmt.Sprintf(`{"sessionPct":%g}`, sessionPct)
	return l
}

func TestPolicyMostRemainingTriesHighestHeadroomFirst(t *testing.T) {
	links := &fakeLinks{}
	// "low" has 80% used (20 headroom); "high" has 10% used (90 headroom).
	links.put("acme", "alice",
		withUsage(linkedAPIKey("acme", "alice", "openai", "low"), 80),
		withUsage(linkedAPIKey("acme", "alice", "openai", "high"), 10),
	)
	res := &fakeResolver{}
	res.seed("acme", Account{"openai", "low"}, Credential{Token: "tok-low"})
	res.seed("acme", Account{"openai", "high"}, Credential{Token: "tok-high"})
	up := &fakeUpstream{}
	up.on("tok-low", ok("m"))
	up.on("tok-high", ok("m"))
	r := newTestRouter(t, links, res, up, &spyMeter{}, PolicyMostRemaining)

	_, a, err := r.Route(context.Background(), "acme", "alice", auto(), Request{})
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if a.Profile != "high" || up.gotAcct[0] != "openai:high" {
		t.Fatalf("most-remaining must try highest headroom first, served %s first-tried %v", a.Profile, up.gotAcct)
	}
}

func TestPolicyRoundRobinSpreads(t *testing.T) {
	links := &fakeLinks{}
	links.put("acme", "alice",
		linkedAPIKey("acme", "alice", "openai", "a"),
		linkedAPIKey("acme", "alice", "openai", "b"),
	)
	res := &fakeResolver{}
	res.seed("acme", Account{"openai", "a"}, Credential{Token: "tok-a"})
	res.seed("acme", Account{"openai", "b"}, Credential{Token: "tok-b"})
	up := &fakeUpstream{}
	up.on("tok-a", ok("m"))
	up.on("tok-b", ok("m"))
	r := newTestRouter(t, links, res, up, &spyMeter{}, PolicyRoundRobin)

	_, a1, _ := r.Route(context.Background(), "acme", "alice", auto(), Request{})
	_, a2, _ := r.Route(context.Background(), "acme", "alice", auto(), Request{})
	if a1.String() == a2.String() {
		t.Fatalf("round-robin should spread consecutive requests, both hit %s", a1.String())
	}
}

func TestCooldownSkipsRecently429dAccount(t *testing.T) {
	links := &fakeLinks{}
	// "a" always 429s; "b" always serves. PolicyPlan keeps input order (both api-key,
	// equal headroom), so "a" is tried first each time — unless it is cooled down.
	links.put("acme", "alice",
		linkedAPIKey("acme", "alice", "openai", "a"),
		linkedAPIKey("acme", "alice", "openai", "b"),
	)
	res := &fakeResolver{}
	res.seed("acme", Account{"openai", "a"}, Credential{Token: "tok-a"})
	res.seed("acme", Account{"openai", "b"}, Credential{Token: "tok-b"})
	up := &fakeUpstream{}
	up.on("tok-a", quota("openai"))
	up.on("tok-b", ok("m"))
	// A FIXED clock so the cooldown window never elapses between the two calls.
	r, err := NewRouter(Config{Links: links, Resolver: res, Upstream: up, Policy: PolicyPlan,
		Now: func() time.Time { return time.Unix(1_700_000_000, 0) }})
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	if _, a, _ := r.Route(context.Background(), "acme", "alice", auto(), Request{}); a.Profile != "b" {
		t.Fatalf("first call should cycle a→b, served %s", a.Profile)
	}
	if _, a, _ := r.Route(context.Background(), "acme", "alice", auto(), Request{}); a.Profile != "b" {
		t.Fatalf("second call should serve b, got %s", a.Profile)
	}
	// "a" was tried exactly ONCE (the first call); the cooldown skipped it the second.
	n := 0
	for _, acct := range up.gotAcct {
		if acct == "openai:a" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("a cooled-down account must be skipped on the next call; a was dialed %d times", n)
	}
}

func TestConcurrentRoutesAreRaceFree(t *testing.T) {
	links := &fakeLinks{}
	links.put("acme", "alice",
		linkedAPIKey("acme", "alice", "openai", "a"),
		linkedAPIKey("acme", "alice", "openai", "b"),
		linkedAPIKey("acme", "alice", "openai", "c"),
	)
	res := &fakeResolver{}
	for _, p := range []string{"a", "b", "c"} {
		res.seed("acme", Account{"openai", p}, Credential{Token: "tok-" + p})
		// half 429 to exercise cooldown + cycling under concurrency
	}
	up := &fakeUpstream{}
	up.on("tok-a", quota("openai"))
	up.on("tok-b", ok("m"))
	up.on("tok-c", ok("m"))
	r := newTestRouter(t, links, res, up, &spyMeter{}, PolicyRoundRobin)

	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, _, err := r.Route(context.Background(), "acme", "alice", auto(), Request{}); err != nil {
				t.Errorf("concurrent Route failed: %v", err)
			}
		}()
	}
	wg.Wait()
}
