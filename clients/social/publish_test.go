package social

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	luxlog "github.com/luxfi/log"

	"github.com/hanzoai/cloud"
)

// fakePublisher is an injected Publisher that records every call and returns a
// configurable outcome — it proves the publish MACHINE (claim → fanout → record) end to
// end without any network, exactly the seam a real provider edge slots into.
type fakePublisher struct {
	mu    sync.Mutex
	calls []fakeCall
	extID string
	err   error
}

type fakeCall struct{ org, acctID, postID string }

func (f *fakePublisher) Publish(_ context.Context, org string, acct Account, post Post) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, fakeCall{org, acct.ID, post.ID})
	if f.err != nil {
		return "", f.err
	}
	return f.extID, nil
}

func (f *fakePublisher) count() int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.calls) }

// testService builds a mounted-shaped service (real store, injected publisher, no-op
// logger) WITHOUT starting the scheduler — the publish path + sweep are driven directly.
func testService(t *testing.T, pub Publisher) *cloud.Service[state] {
	t.Helper()
	store := testStore(t)
	b := cloud.NewBase(cloud.Deps{Logger: luxlog.NewNoOpLogger()}, "social")
	return &cloud.Service[state]{Base: b, State: state{store: store, pub: pub}}
}

// seed creates one connected account + one post for org in a single step.
func seed(t *testing.T, s *cloud.Service[state], org, provider, postID, status string, scheduleAt int64) {
	t.Helper()
	ctx := context.Background()
	if _, err := s.State.store.CreateAccount(ctx, Account{
		ID: "acct_" + org, Org: org, Provider: provider, Handle: "@" + org, Status: "connected", Token: "tok", CreatedAt: 1, UpdatedAt: 1,
	}); err != nil {
		t.Fatalf("seed account: %v", err)
	}
	if _, err := s.State.store.CreatePost(ctx, Post{
		ID: postID, Org: org, Content: "hello", Channel: provider, Status: status, ScheduleAt: scheduleAt, CreatedAt: 1, UpdatedAt: 1,
	}); err != nil {
		t.Fatalf("seed post: %v", err)
	}
}

// TestPublishNow_Success proves a draft post publishes through a connected account and
// records the provider's external id + the account it went through.
func TestPublishNow_Success(t *testing.T) {
	ctx := context.Background()
	fake := &fakePublisher{extID: "ext_123"}
	s := testService(t, fake)
	seed(t, s, "hanzo", "x", "post_1", statusDraft, 0)

	post, err := publishPost(ctx, s, "hanzo", "post_1")
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	if post.Status != statusPublished || post.ExternalID != "ext_123" || post.AccountID != "acct_hanzo" {
		t.Fatalf("want published/ext_123/acct_hanzo, got %s/%s/%s", post.Status, post.ExternalID, post.AccountID)
	}
	if post.Error != "" {
		t.Fatalf("published post must carry no error, got %q", post.Error)
	}
	if fake.count() != 1 {
		t.Fatalf("publisher must be called once, got %d", fake.count())
	}
}

// TestPublish_NotConfigured proves the fail-closed default records an honest failure that
// names the exact missing credentials AND surfaces errProviderNotConfigured (→ 503) —
// never a fake success.
func TestPublish_NotConfigured(t *testing.T) {
	t.Setenv("X_API_KEY", "")
	t.Setenv("X_API_SECRET", "")
	ctx := context.Background()
	s := testService(t, notConfiguredPublisher{})
	seed(t, s, "hanzo", "x", "post_1", statusDraft, 0)

	post, err := publishPost(ctx, s, "hanzo", "post_1")
	if !errors.Is(err, errProviderNotConfigured) {
		t.Fatalf("want errProviderNotConfigured, got %v", err)
	}
	if post.Status != statusFailed {
		t.Fatalf("want failed, got %s", post.Status)
	}
	if !strings.Contains(post.Error, "X_API_KEY") {
		t.Fatalf("failure must name the missing credential, got %q", post.Error)
	}
}

// TestPublish_NoConnectedAccount proves an org with no connected account for the channel
// fails honestly (recorded), not a 503.
func TestPublish_NoConnectedAccount(t *testing.T) {
	ctx := context.Background()
	fake := &fakePublisher{extID: "ext"}
	s := testService(t, fake)
	// A post with no matching account (nothing seeded).
	if _, err := s.State.store.CreatePost(ctx, Post{ID: "post_1", Org: "hanzo", Content: "hi", Channel: "x", Status: statusDraft, CreatedAt: 1, UpdatedAt: 1}); err != nil {
		t.Fatalf("create: %v", err)
	}
	post, err := publishPost(ctx, s, "hanzo", "post_1")
	if err != nil {
		t.Fatalf("no-account must not be a control error, got %v", err)
	}
	if post.Status != statusFailed || !strings.Contains(post.Error, "no connected x account") {
		t.Fatalf("want failed/no-account, got %s/%q", post.Status, post.Error)
	}
	if fake.count() != 0 {
		t.Fatalf("publisher must not be called with no account, got %d", fake.count())
	}
}

// TestPublish_Idempotent proves re-publishing an already-published post is a no-op: the
// publisher is called exactly once and the external id is unchanged.
func TestPublish_Idempotent(t *testing.T) {
	ctx := context.Background()
	fake := &fakePublisher{extID: "ext_1"}
	s := testService(t, fake)
	seed(t, s, "hanzo", "x", "post_1", statusScheduled, 0)

	first, err := publishPost(ctx, s, "hanzo", "post_1")
	if err != nil || first.Status != statusPublished {
		t.Fatalf("first publish: status=%s err=%v", first.Status, err)
	}
	second, err := publishPost(ctx, s, "hanzo", "post_1")
	if err != nil {
		t.Fatalf("second publish: %v", err)
	}
	if second.Status != statusPublished || second.ExternalID != "ext_1" {
		t.Fatalf("second publish must return the unchanged published post, got %s/%s", second.Status, second.ExternalID)
	}
	if fake.count() != 1 {
		t.Fatalf("publisher must be called exactly once across two publishes, got %d", fake.count())
	}
}

// TestPublish_PerOrgIsolation proves publishing is org-scoped: org A's post publishes
// only through org A's account, and B cannot publish A's post.
func TestPublish_PerOrgIsolation(t *testing.T) {
	ctx := context.Background()
	fake := &fakePublisher{extID: "ext"}
	s := testService(t, fake)
	seed(t, s, "orga", "x", "post_a", statusDraft, 0)
	seed(t, s, "orgb", "x", "post_b", statusDraft, 0)

	// B cannot publish A's post.
	if _, err := publishPost(ctx, s, "orgb", "post_a"); !errors.Is(err, errNotFound) {
		t.Fatalf("B publishing A's post want errNotFound, got %v", err)
	}
	// A publishes A's post — only A's account is targeted.
	if _, err := publishPost(ctx, s, "orga", "post_a"); err != nil {
		t.Fatalf("A publish: %v", err)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.calls) != 1 || fake.calls[0].org != "orga" || fake.calls[0].acctID != "acct_orga" {
		t.Fatalf("A's publish must target only acct_orga, got %+v", fake.calls)
	}
}

// TestScheduler_Sweep proves the sweep publishes due scheduled posts and leaves
// future-scheduled + draft posts untouched — the scheduled → published transition.
func TestScheduler_Sweep(t *testing.T) {
	ctx := context.Background()
	fake := &fakePublisher{extID: "sched"}
	s := testService(t, fake)
	now := time.Now().Unix()

	// account for org hanzo (channel x)
	if _, err := s.State.store.CreateAccount(ctx, Account{ID: "acct_hanzo", Org: "hanzo", Provider: "x", Handle: "@h", Status: "connected", Token: "t", CreatedAt: 1, UpdatedAt: 1}); err != nil {
		t.Fatalf("account: %v", err)
	}
	mk := func(id, status string, at int64) {
		if _, err := s.State.store.CreatePost(ctx, Post{ID: id, Org: "hanzo", Content: "c", Channel: "x", Status: status, ScheduleAt: at, CreatedAt: 1, UpdatedAt: 1}); err != nil {
			t.Fatalf("post %s: %v", id, err)
		}
	}
	mk("due", statusScheduled, now-100)      // past → due
	mk("future", statusScheduled, now+10000) // future → not due
	mk("draft", statusDraft, 0)              // draft → never swept

	sweepDue(s)

	if p, _ := s.State.store.GetPost(ctx, "hanzo", "due"); p.Status != statusPublished || p.ExternalID != "sched" {
		t.Fatalf("due post must be published, got %s/%s", p.Status, p.ExternalID)
	}
	if p, _ := s.State.store.GetPost(ctx, "hanzo", "future"); p.Status != statusScheduled {
		t.Fatalf("future post must stay scheduled, got %s", p.Status)
	}
	if p, _ := s.State.store.GetPost(ctx, "hanzo", "draft"); p.Status != statusDraft {
		t.Fatalf("draft post must be untouched, got %s", p.Status)
	}
}

// TestClaimAndRecover proves the claim guard (at-most-once) + crash recovery.
func TestClaimAndRecover(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	now := time.Now().Unix()

	if _, err := s.CreatePost(ctx, Post{ID: "p", Org: "o", Content: "c", Channel: "x", Status: statusScheduled, CreatedAt: 1, UpdatedAt: 1}); err != nil {
		t.Fatalf("create: %v", err)
	}
	// First claim wins; second (now 'publishing') loses.
	if _, ok, err := s.ClaimForPublish(ctx, "o", "p", now); err != nil || !ok {
		t.Fatalf("first claim must win: ok=%v err=%v", ok, err)
	}
	if _, ok, err := s.ClaimForPublish(ctx, "o", "p", now); err != nil || ok {
		t.Fatalf("second claim must lose (already publishing): ok=%v err=%v", ok, err)
	}
	// An absent post → errNotFound.
	if _, _, err := s.ClaimForPublish(ctx, "o", "missing", now); !errors.Is(err, errNotFound) {
		t.Fatalf("claim absent want errNotFound, got %v", err)
	}
	// Recovery flips the stuck 'publishing' post back to failed (retryable).
	n, err := s.RecoverStuckPublishing(ctx, now)
	if err != nil || n != 1 {
		t.Fatalf("recover want 1, got %d err=%v", n, err)
	}
	if p, _ := s.GetPost(ctx, "o", "p"); p.Status != statusFailed {
		t.Fatalf("recovered post must be failed, got %s", p.Status)
	}
}

// TestProviderCapabilities proves the capabilities read is live + honest: unset creds
// list the exact missing vars; set creds mark the provider configured.
func TestProviderCapabilities(t *testing.T) {
	t.Setenv("X_API_KEY", "")
	t.Setenv("X_API_SECRET", "")
	caps := providerCapabilities()
	if len(caps) != len(providerOrder) {
		t.Fatalf("want %d providers, got %d", len(providerOrder), len(caps))
	}
	x := caps[0]
	if x.Provider != "x" || x.CredentialsConfigured || len(x.MissingCredentials) != 2 {
		t.Fatalf("x must be unconfigured with 2 missing, got %+v", x)
	}

	t.Setenv("X_API_KEY", "k")
	t.Setenv("X_API_SECRET", "s")
	if got := providerCapabilities()[0]; !got.CredentialsConfigured || len(got.MissingCredentials) != 0 {
		t.Fatalf("x must be configured once creds are set, got %+v", got)
	}
}

// TestSchedulerInterval proves the cadence parser: default, disable words, valid, and a
// fail-safe on garbage.
func TestSchedulerInterval(t *testing.T) {
	log := luxlog.NewNoOpLogger()
	cases := []struct {
		val  string
		want time.Duration
	}{
		{"", defaultSchedulerInterval},
		{"0", 0},
		{"off", 0},
		{"false", 0},
		{"45s", 45 * time.Second},
		{"garbage", 0},
	}
	for _, c := range cases {
		t.Setenv(schedulerIntervalEnv, c.val)
		if got := schedulerInterval(log); got != c.want {
			t.Fatalf("%q: want %v, got %v", c.val, c.want, got)
		}
	}
}
