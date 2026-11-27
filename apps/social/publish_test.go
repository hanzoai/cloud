package social

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	luxlog "github.com/luxfi/log"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk"
)

// fakePublisher is an injected Publisher that records every call and returns a
// configurable outcome — it proves the publish MACHINE (claim → fanout → record) end to
// end without any network, exactly the client a real provider edge slots into.
type fakePublisher struct {
	mu    sync.Mutex
	calls []fakeCall
	extID string
	err   error
	// refuse names the accounts that answer with err while the rest succeed.
	refuse map[string]error
}

type fakeCall struct{ org, acctID, postID string }

func (f *fakePublisher) Publish(_ context.Context, org string, acct socialAccount, post socialPost) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, fakeCall{org, acct.ID, post.ID})
	if err := f.refuse[acct.ID]; err != nil {
		return "", err
	}
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
	b := cloud.NewBase(cloud.Deps{}, "social")
	return &cloud.Service[state]{Base: b, State: state{store: store, pub: pub}}
}

// seed creates one connected account + one post for org in a single step.
func seed(t *testing.T, s *cloud.Service[state], org, provider, postID, status string, scheduleAt int64) {
	t.Helper()
	ctx := context.Background()
	if _, err := s.State.store.CreateAccount(ctx, socialAccount{
		ID: "acct_" + org, Org: org, Provider: provider, Handle: "@" + org, Status: "connected", Token: "tok", CreatedAt: 1, UpdatedAt: 1,
	}); err != nil {
		t.Fatalf("seed account: %v", err)
	}
	if _, err := s.State.store.CreatePost(ctx, socialPost{
		ID: postID, Org: org, Content: "hello", Channel: provider, Status: status, ScheduleAt: scheduleAt, CreatedAt: 1, UpdatedAt: 1,
	}); err != nil {
		t.Fatalf("seed post: %v", err)
	}
}

// TestPublishNow_Success proves a draft post publishes through a connected account and
// records the provider's external id + the account it went through.
func TestPublishNow_Success(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
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

	})
}

// TestPublish_NotConfigured proves the fail-closed default records an honest failure that
// names the exact missing credentials AND surfaces errProviderNotConfigured (→ 503) —
// never a fake success.
func TestPublish_NotConfigured(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		t.Setenv("X_CLIENT_ID", "")
		t.Setenv("X_CLIENT_SECRET", "")
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

	})
}

// TestPublish_NoConnectedAccount proves an org with no connected account for the channel
// fails honestly (recorded), not a 503.
func TestPublish_NoConnectedAccount(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		ctx := context.Background()
		fake := &fakePublisher{extID: "ext"}
		s := testService(t, fake)
		// A post with no matching account (nothing seeded).
		if _, err := s.State.store.CreatePost(ctx, socialPost{ID: "post_1", Org: "hanzo", Content: "hi", Channel: "x", Status: statusDraft, CreatedAt: 1, UpdatedAt: 1}); err != nil {
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

	})
}

// TestPublish_Idempotent proves re-publishing an already-published post is a no-op: the
// publisher is called exactly once and the external id is unchanged.
func TestPublish_Idempotent(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
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

	})
}

// TestPublish_PerOrgIsolation proves publishing is org-scoped: org A's post publishes
// only through org A's account, and B cannot publish A's post.
func TestPublish_PerOrgIsolation(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
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

	})
}

// TestScheduler_Sweep proves the sweep publishes due scheduled posts and leaves
// future-scheduled + draft posts untouched — the scheduled → published transition.
func TestScheduler_Sweep(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		ctx := context.Background()
		fake := &fakePublisher{extID: "sched"}
		s := testService(t, fake)
		now := time.Now().Unix()

		// account for org hanzo (channel x)
		if _, err := s.State.store.CreateAccount(ctx, socialAccount{ID: "acct_hanzo", Org: "hanzo", Provider: "x", Handle: "@h", Status: "connected", Token: "t", CreatedAt: 1, UpdatedAt: 1}); err != nil {
			t.Fatalf("account: %v", err)
		}
		mk := func(id, status string, at int64) {
			if _, err := s.State.store.CreatePost(ctx, socialPost{ID: id, Org: "hanzo", Content: "c", Channel: "x", Status: status, ScheduleAt: at, CreatedAt: 1, UpdatedAt: 1}); err != nil {
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

	})
}

// TestClaimAndRecover proves the claim guard (at-most-once) + crash recovery.
func TestClaimAndRecover(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		ctx := context.Background()
		s := testStore(t)
		now := time.Now().Unix()

		if _, err := s.CreatePost(ctx, socialPost{ID: "p", Org: "o", Content: "c", Channel: "x", Status: statusScheduled, CreatedAt: 1, UpdatedAt: 1}); err != nil {
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
		// Recovery flips the stuck 'publishing' post back to failed (retryable) once its
		// claim is older than the longest attempt.
		stale := int64(publishStale.Seconds())
		n, err := s.RecoverStuckPublishing(ctx, now+stale, stale)
		if err != nil || n != 1 {
			t.Fatalf("recover want 1, got %d err=%v", n, err)
		}
		if p, _ := s.GetPost(ctx, "o", "p"); p.Status != statusFailed {
			t.Fatalf("recovered post must be failed, got %s", p.Status)
		}

	})
}

// TestProviderCapabilities proves the capabilities read is live + honest: unset creds
// list the exact missing vars; set creds mark the provider configured.
func TestProviderCapabilities(t *testing.T) {
	t.Setenv("X_CLIENT_ID", "")
	t.Setenv("X_CLIENT_SECRET", "")
	t.Setenv("X_API_KEY", "")
	t.Setenv("X_API_SECRET", "")
	caps := providerCapabilities()
	if len(caps) != len(providerOrder) {
		t.Fatalf("want %d providers, got %d", len(providerOrder), len(caps))
	}
	x := caps[0]
	if x.Provider != "x" || x.CredentialsConfigured || len(x.MissingCredentials) != 4 {
		t.Fatalf("x must be unconfigured with the four consulted names, got %+v", x)
	}

	t.Setenv("X_API_KEY", "k")
	t.Setenv("X_API_SECRET", "s")
	if got := providerCapabilities()[0]; !got.CredentialsConfigured || len(got.MissingCredentials) != 0 {
		t.Fatalf("x must be configured once creds are set, got %+v", got)
	}
}

// A sealed org app is publish-readiness just as deployment env is. The org half
// only flips the network whose app is complete, never leaks the secret, keeps the
// env names when neither source is complete, and never weakens the env half.
func TestProviderCapabilitiesForOrgSealedApp(t *testing.T) {
	for _, k := range []string{
		"FACEBOOK_CLIENT_ID", "FACEBOOK_CLIENT_SECRET", "FACEBOOK_APP_ID", "FACEBOOK_APP_SECRET",
		"INSTAGRAM_CLIENT_ID", "INSTAGRAM_CLIENT_SECRET",
	} {
		t.Setenv(k, "")
	}
	const org = "acme"
	const secretValue = "ig-secret-never-returned"
	byName := func(caps []socialProvider) map[string]socialProvider {
		m := make(map[string]socialProvider, len(caps))
		for _, c := range caps {
			m[c.Provider] = c
		}
		return m
	}

	// Neither source: deployment-env names survive verbatim; no store is needed.
	base := byName(providerCapabilitiesFor(nil, org))
	if base["facebook"].CredentialsConfigured || len(base["facebook"].MissingCredentials) != 4 {
		t.Fatalf("facebook with neither source = %+v", base["facebook"])
	}
	if base["instagram"].CredentialsConfigured || len(base["instagram"].MissingCredentials) != 6 {
		t.Fatalf("instagram with neither source = %+v", base["instagram"])
	}

	kc := newSocialKMS(t)
	ctx := context.Background()
	if err := kc.PutSecret(ctx, appRef(appPath(org, "instagram"), "client_id"), []byte("ig-id")); err != nil {
		t.Fatalf("seal client id: %v", err)
	}
	if err := kc.PutSecret(ctx, appRef(appPath(org, "instagram"), "client_secret"), []byte(secretValue)); err != nil {
		t.Fatalf("seal client secret: %v", err)
	}

	got := byName(providerCapabilitiesFor(kc, org))
	if !got["instagram"].CredentialsConfigured || len(got["instagram"].MissingCredentials) != 0 {
		t.Fatalf("sealed instagram must be configured, got %+v", got["instagram"])
	}
	if got["facebook"].CredentialsConfigured || len(got["facebook"].MissingCredentials) != 4 {
		t.Fatalf("unsealed facebook must still list its env names, got %+v", got["facebook"])
	}
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), secretValue) || strings.Contains(string(raw), "ig-id") {
		t.Fatalf("a credential value leaked: %s", raw)
	}
	// A different org sees none of acme's app.
	other := byName(providerCapabilitiesFor(kc, "other"))
	if other["instagram"].CredentialsConfigured || len(other["instagram"].MissingCredentials) != 6 {
		t.Fatalf("another org must not inherit the app, got %+v", other["instagram"])
	}

	// Deployment env is still enough with no KMS at all.
	t.Setenv("FACEBOOK_CLIENT_ID", "fid")
	t.Setenv("FACEBOOK_CLIENT_SECRET", "fsec")
	env := byName(providerCapabilitiesFor(nil, org))
	if !env["facebook"].CredentialsConfigured || len(env["facebook"].MissingCredentials) != 0 {
		t.Fatalf("env-only facebook must stay configured, got %+v", env["facebook"])
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

// twoAccounts connects a personal profile and a company page for one org, the
// company updated later, so it is the one listed first.
func twoAccounts(t *testing.T, s *cloud.Service[state]) {
	t.Helper()
	ctx := context.Background()
	for i, a := range []socialAccount{
		{ID: "acct_me", Org: "hanzo", Provider: "linkedin", Handle: "Ada", Status: "connected", ExternalID: "mem", CreatedAt: 1, UpdatedAt: 1},
		{ID: "acct_co", Org: "hanzo", Provider: "linkedin", Handle: "Acme", Status: "connected", ExternalID: "99", CreatedAt: 2, UpdatedAt: 2},
	} {
		if _, err := s.State.store.CreateAccount(ctx, a); err != nil {
			t.Fatalf("account %d: %v", i, err)
		}
	}
	if _, err := s.State.store.CreatePost(ctx, socialPost{ID: "p", Org: "hanzo", Content: "Both", Channel: "linkedin", Status: statusDraft, CreatedAt: 1, UpdatedAt: 1}); err != nil {
		t.Fatalf("post: %v", err)
	}
}

// A person who connected a personal profile and a company page means both: the
// composer counts two accounts, and a publish reaches each of them.
func TestPublish_ReachesEveryConnectedAccount(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		fake := &fakePublisher{extID: "ext"}
		s := testService(t, fake)
		twoAccounts(t, s)

		post, err := publishPost(context.Background(), s, "hanzo", "p")
		if err != nil {
			t.Fatal(err)
		}
		if fake.count() != 2 {
			t.Fatalf("the publisher was asked %d times, want once per account", fake.count())
		}
		if post.Status != statusPublished || post.Error != "" || post.AccountID != "acct_co" {
			t.Fatalf("post = %+v", post)
		}

	})
}

// One account that refuses does not stop the others, and the post says which.
func TestPublish_APartialPublishNamesTheAccountThatRefused(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		fake := &fakePublisher{extID: "ext", refuse: map[string]error{"acct_co": errors.New("LinkedIn refused the post (403): not an admin")}}
		s := testService(t, fake)
		twoAccounts(t, s)

		post, err := publishPost(context.Background(), s, "hanzo", "p")
		if err != nil {
			t.Fatal(err)
		}
		if fake.count() != 2 {
			t.Fatalf("asked %d times", fake.count())
		}
		if post.Status != statusPublished || post.AccountID != "acct_me" {
			t.Fatalf("post = %+v", post)
		}
		if !strings.Contains(post.Error, "published to 1 of 2 accounts") || !strings.Contains(post.Error, "Acme: LinkedIn refused the post (403)") {
			t.Fatalf("the note = %q", post.Error)
		}
		// Published is final: a retry does not post the same words to the account that took it.
		if again, err := publishPost(context.Background(), s, "hanzo", "p"); err != nil || fake.count() != 2 || again.Status != statusPublished {
			t.Fatalf("a retry asked again: %d %v", fake.count(), err)
		}

	})
}

func TestPublish_EveryAccountRefusingFailsThePostWithEachReason(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		fake := &fakePublisher{err: errors.New("the network said no")}
		s := testService(t, fake)
		twoAccounts(t, s)

		post, err := publishPost(context.Background(), s, "hanzo", "p")
		if err != nil {
			t.Fatal(err)
		}
		if post.Status != statusFailed || !strings.Contains(post.Error, "Acme: the network said no") || !strings.Contains(post.Error, "Ada: the network said no") {
			t.Fatalf("post = %+v", post)
		}

	})
}

// An account typed in by hand has no consent behind it. Asking the provider for
// "the only connection" would post the same words again through whichever
// account the org did connect.
func TestPublish_AHandListedAccountIsNotPostedThroughAnotherOnesToken(t *testing.T) {
	_, err := nativePublisher{}.Publish(context.Background(), "hanzo", socialAccount{Provider: "bluesky", Handle: "@typed.in"}, socialPost{Content: "x"})
	if err == nil || !strings.Contains(err.Error(), "listed by hand") {
		t.Fatalf("err = %v", err)
	}
}

// A network that takes the org's own credential needs nothing from the
// deployment; one that needs an app says which names.
func TestProviderCapabilities_CredentialNetworksNeedNothingFromTheDeployment(t *testing.T) {
	for _, k := range []string{"PINTEREST_CLIENT_ID", "PINTEREST_CLIENT_SECRET", "REDDIT_CLIENT_ID", "REDDIT_CLIENT_SECRET"} {
		t.Setenv(k, "")
	}
	byName := map[string]socialProvider{}
	for _, c := range providerCapabilities() {
		byName[c.Provider] = c
	}
	for _, n := range []string{"bluesky", "mastodon", "telegram-bot", "discord-webhook"} {
		if c, ok := byName[n]; !ok || !c.CredentialsConfigured || len(c.MissingCredentials) != 0 {
			t.Errorf("%s = %+v", n, c)
		}
	}
	if c := byName["reddit"]; c.CredentialsConfigured || len(c.MissingCredentials) != 2 || c.MissingCredentials[0] != "REDDIT_CLIENT_ID" {
		t.Errorf("reddit = %+v", c)
	}
	if c := byName["pinterest"]; c.CredentialsConfigured || len(c.MissingCredentials) != 2 {
		t.Errorf("pinterest = %+v", c)
	}
	for _, n := range []string{"x", "facebook", "instagram", "linkedin", "tiktok", "youtube", "threads", "bluesky", "mastodon", "pinterest", "reddit", "telegram-bot", "discord-webhook"} {
		if _, ok := byName[n]; !ok {
			t.Errorf("%s is not a network", n)
		}
	}
	if err := providerNotConfiguredErr("myspace"); !errors.Is(err, errProviderNotConfigured) || !strings.Contains(err.Error(), "unknown provider") {
		t.Errorf("unknown = %v", err)
	}
}

// Disconnecting an account takes it off the list the org manages.
func TestForgetTarget_RemovesTheDisconnectedAccount(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		s := testService(t, &fakePublisher{})
		twoAccounts(t, s)
		old := mounted
		mounted = s
		t.Cleanup(func() { mounted = old })
		ctx := cloud.For(context.Background(), "hanzo")

		out, err := forgetTarget(ctx, &client.SocialTargetIn{Provider: "linkedin", ExternalID: "99"})
		if err != nil || out.ID == "" {
			t.Fatalf("out=%+v err=%v", out, err)
		}
		list, _ := s.State.store.ListAccounts(context.Background(), "hanzo", "linkedin", 10)
		if len(list) != 1 || list[0].ExternalID != "mem" {
			t.Fatalf("left = %+v", list)
		}
		// Forgetting what is not listed is not an error, and another org's is not touched.
		if _, err := forgetTarget(ctx, &client.SocialTargetIn{Provider: "linkedin", ExternalID: "99"}); err != nil {
			t.Fatalf("twice: %v", err)
		}
		if _, err := forgetTarget(cloud.For(context.Background(), "globex"), &client.SocialTargetIn{Provider: "linkedin", ExternalID: "mem"}); err != nil {
			t.Fatal(err)
		}
		if list, _ := s.State.store.ListAccounts(context.Background(), "hanzo", "linkedin", 10); len(list) != 1 {
			t.Fatalf("another org forgot this org's account: %+v", list)
		}
		for _, bad := range []*client.SocialTargetIn{{Provider: "myspace", ExternalID: "1"}, {Provider: "linkedin"}} {
			if _, err := forgetTarget(ctx, bad); err == nil {
				t.Errorf("%+v was taken", bad)
			}
		}

	})
}
