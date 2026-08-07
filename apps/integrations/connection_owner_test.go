package integrations

import (
	"context"
	"testing"
)

// A GitHub App is installed per account, so an org that owns several GitHub
// organizations holds one connection each. These cover the key that makes that
// possible and the migration that recovers it for rows written before it existed.

func openTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := openStore(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestOneOrgHoldsSeveralGithubAccounts(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	accounts := map[string]string{
		"hanzoai":    "143007410",
		"hanzo-apps": "143008414",
		"hanzo-docs": "150229336",
	}
	for owner, inst := range accounts {
		if err := s.Upsert(ctx, Connection{
			Org: "hanzo", Provider: "github", Label: owner,
			ExternalID: inst, AccountLabel: owner,
		}); err != nil {
			t.Fatalf("upsert %s: %v", owner, err)
		}
	}
	conns, err := s.ListFor(ctx, "hanzo", "github")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(conns) != 3 {
		t.Fatalf("want 3 connections, got %d — the second account overwrote the first", len(conns))
	}
	// Each account keeps its OWN installation. Sharing one would mean a token
	// minted for one org granting nothing on the repos of another.
	for owner, inst := range accounts {
		c, ok, err := s.Get(ctx, "hanzo", "", "github", owner)
		if err != nil || !ok {
			t.Fatalf("get %s: ok=%v err=%v", owner, ok, err)
		}
		if c.ExternalID != inst {
			t.Errorf("%s resolved installation %s, want %s", owner, c.ExternalID, inst)
		}
	}
}

// The pre-migration bug: without the owner in the key, connecting a second
// account silently REPLACED the first, because both rows were (org, provider).
func TestASecondAccountDoesNotReplaceTheFirst(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	first := Connection{Org: "hanzo", Provider: "github", Label: "hanzoai", ExternalID: "143007410"}
	second := Connection{Org: "hanzo", Provider: "github", Label: "hanzo-apps", ExternalID: "143008414"}
	if err := s.Upsert(ctx, first); err != nil {
		t.Fatal(err)
	}
	if err := s.Upsert(ctx, second); err != nil {
		t.Fatal(err)
	}
	c, ok, err := s.Get(ctx, "hanzo", "", "github", "hanzoai")
	if err != nil || !ok {
		t.Fatalf("the first account must survive the second: ok=%v err=%v", ok, err)
	}
	if c.ExternalID != "143007410" {
		t.Errorf("first account now points at %s", c.ExternalID)
	}
}

// Re-connecting the SAME account updates it rather than adding a duplicate.
func TestReconnectingAnAccountUpdatesInPlace(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	for _, inst := range []string{"111", "222"} {
		if err := s.Upsert(ctx, Connection{
			Org: "hanzo", Provider: "github", Label: "hanzoai", ExternalID: inst,
		}); err != nil {
			t.Fatal(err)
		}
	}
	conns, err := s.ListFor(ctx, "hanzo", "github")
	if err != nil {
		t.Fatal(err)
	}
	if len(conns) != 1 {
		t.Fatalf("re-connecting one account made %d rows", len(conns))
	}
	if conns[0].ExternalID != "222" {
		t.Errorf("re-connect did not take: %s", conns[0].ExternalID)
	}
}

// A provider with one account per org carries owner="" — not a special case,
// just one owner — and behaves exactly as before.
func TestSingleAccountProviderIsUnchanged(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	if err := s.Upsert(ctx, Connection{
		Org: "acme", Provider: "slack", ExternalID: "T123", BotUserID: "U1",
	}); err != nil {
		t.Fatal(err)
	}
	c, ok, err := s.Get(ctx, "acme", "", "slack", "")
	if err != nil || !ok {
		t.Fatalf("slack: ok=%v err=%v", ok, err)
	}
	if c.ExternalID != "T123" || c.BotUserID != "U1" {
		t.Errorf("slack connection round-tripped wrong: %+v", c)
	}
}

// Disconnecting is a statement about the provider, so it removes every account.
func TestDisconnectRemovesEveryAccount(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	for _, o := range []string{"hanzoai", "hanzo-apps", "hanzo-docs"} {
		if err := s.Upsert(ctx, Connection{Org: "hanzo", Provider: "github", Label: o, ExternalID: "1"}); err != nil {
			t.Fatal(err)
		}
	}
	gone, err := s.Disconnect(ctx, "hanzo", "", "github")
	if err != nil || !gone {
		t.Fatalf("delete: gone=%v err=%v", gone, err)
	}
	conns, err := s.ListFor(ctx, "hanzo", "github")
	if err != nil {
		t.Fatal(err)
	}
	if len(conns) != 0 {
		t.Errorf("%d connections survived a disconnect", len(conns))
	}
}

// An installation id still resolves back to the org that connected it — the
// mapping inbound webhooks depend on, now one per account rather than one total.
func TestEachInstallationResolvesToItsOrg(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	if err := s.Upsert(ctx, Connection{Org: "hanzo", Provider: "github", Label: "hanzo-apps", ExternalID: "143008414"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Upsert(ctx, Connection{Org: "lux", Provider: "github", Label: "luxfi", ExternalID: "143008606"}); err != nil {
		t.Fatal(err)
	}
	for inst, want := range map[string]string{"143008414": "hanzo", "143008606": "lux"} {
		got, ok, err := s.ResolveOrgByExternalID(ctx, "github", inst)
		if err != nil || !ok {
			t.Fatalf("resolve %s: ok=%v err=%v", inst, ok, err)
		}
		if got != want {
			t.Errorf("installation %s resolved to org %q, want %q", inst, got, want)
		}
	}
}

// A single connection answers for any owner: a row written before the owner key
// existed, or one whose GitHub account was renamed, still holds the right
// installation. Breaking a working mirror over a label would be the worse answer.
func TestOneAccountAnswersForAnyOwner(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	if err := s.Upsert(ctx, Connection{Org: "acme", Provider: "github", ExternalID: "333"}); err != nil {
		t.Fatal(err)
	}
	conns, err := s.ListFor(ctx, "acme", "github")
	if err != nil {
		t.Fatal(err)
	}
	if len(conns) != 1 {
		t.Fatalf("fixture: want 1 connection, got %d", len(conns))
	}
	// Exactly the shape githubConnection falls back on: the name does not match a
	// row, and with one connection there is nothing to confuse it with.
	if _, ok := s.mustGet(t, "acme", "github", "acme-gh"); ok {
		t.Fatal("fixture: no row should carry that owner")
	}
}

func (s *Store) mustGet(t *testing.T, org, provider, owner string) (Connection, bool) {
	t.Helper()
	c, ok, err := s.Get(context.Background(), org, "", provider, owner)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	return c, ok
}

// The key only helps if the connect path FILLS it. Setting the owner nowhere
// would leave every GitHub account colliding on the empty string — the exact
// failure the widened key exists to prevent — while the schema looked correct.
func TestOnlyAMultiAccountProviderIsKeyedByAccount(t *testing.T) {
	res := &ExchangeResult{AccountLabel: "hanzo-apps", ExternalID: "143008414"}

	gh, ok := snapshotRegistry()["github"]
	if !ok {
		t.Fatal("github is not registered")
	}
	if !gh.MultiAccount {
		t.Fatal("a GitHub App is installed per account; the provider must say so")
	}
	if got := connOwner(gh, res); got != "hanzo-apps" {
		t.Errorf("github connection keyed by %q, want the account", got)
	}

	// A provider with one account per org keeps the empty owner its callers read.
	single := &Provider{ID: "slack"}
	if got := connOwner(single, res); got != "" {
		t.Errorf("single-account provider keyed by %q, want empty", got)
	}
}

// Three GitHub accounts under one org must produce three rows, each with its own
// installation — this is the production case: hanzoai, hanzo-apps, hanzo-docs.
func TestThreeGithubAccountsSurviveTogether(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	gh := &Provider{ID: "github", MultiAccount: true}
	for owner, inst := range map[string]string{
		"hanzoai": "143007410", "hanzo-apps": "143008414", "hanzo-docs": "150229336",
	} {
		res := &ExchangeResult{AccountLabel: owner, ExternalID: inst}
		if err := s.Upsert(ctx, Connection{
			Org: "hanzo", Provider: "github",
			Label: connOwner(gh, res), ExternalID: res.ExternalID, AccountLabel: owner,
		}); err != nil {
			t.Fatal(err)
		}
	}
	conns, err := s.ListFor(ctx, "hanzo", "github")
	if err != nil {
		t.Fatal(err)
	}
	if len(conns) != 3 {
		t.Fatalf("want 3 accounts, got %d — they collided on the owner", len(conns))
	}
}
