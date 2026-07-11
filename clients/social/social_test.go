package social

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	s, err := openStore(filepath.Join(t.TempDir(), "social.db"))
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// TestPerOrgIsolation is the load-bearing tenant-isolation test: two orgs write
// accounts + posts; neither can list, get, update, or delete the other's rows.
func TestPerOrgIsolation(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)

	// Accounts.
	mpAcct, err := s.CreateAccount(ctx, Account{ID: "acct_mp", Org: "maxpower", Provider: "x", Handle: "@maxpower", Status: "connected", CreatedAt: 1, UpdatedAt: 1})
	if err != nil {
		t.Fatalf("create maxpower account: %v", err)
	}
	if _, err := s.CreateAccount(ctx, Account{ID: "acct_acme", Org: "acme", Provider: "instagram", Handle: "@acme", Status: "connected", CreatedAt: 1, UpdatedAt: 1}); err != nil {
		t.Fatalf("create acme account: %v", err)
	}
	if list, err := s.ListAccounts(ctx, "maxpower", "", 100); err != nil || len(list) != 1 || list[0].Handle != "@maxpower" {
		t.Fatalf("maxpower must see [@maxpower], got %+v err=%v", list, err)
	}
	if _, err := s.GetAccount(ctx, "acme", mpAcct.ID); !errors.Is(err, errNotFound) {
		t.Fatalf("acme GET maxpower account want errNotFound, got %v", err)
	}
	if _, err := s.UpdateAccount(ctx, Account{ID: mpAcct.ID, Org: "acme", Provider: "x", Handle: "HIJACK", Status: "error", UpdatedAt: 2}); !errors.Is(err, errNotFound) {
		t.Fatalf("acme UPDATE maxpower account want errNotFound, got %v", err)
	}
	if deleted, _ := s.DeleteAccount(ctx, "acme", mpAcct.ID); deleted {
		t.Fatalf("acme must not delete maxpower's account")
	}
	if got, _ := s.GetAccount(ctx, "maxpower", mpAcct.ID); got.Handle != "@maxpower" {
		t.Fatalf("maxpower account must be unchanged, got %q", got.Handle)
	}

	// Posts.
	mpPost, err := s.CreatePost(ctx, Post{ID: "post_mp", Org: "maxpower", Content: "hello world", Channel: "x", Status: "scheduled", ScheduleAt: 1000, CreatedAt: 1, UpdatedAt: 1})
	if err != nil {
		t.Fatalf("create maxpower post: %v", err)
	}
	if _, err := s.CreatePost(ctx, Post{ID: "post_acme", Org: "acme", Content: "acme blast", Channel: "email", Status: "draft", CreatedAt: 1, UpdatedAt: 1}); err != nil {
		t.Fatalf("create acme post: %v", err)
	}
	if list, err := s.ListPosts(ctx, "maxpower", "", 100); err != nil || len(list) != 1 || list[0].Content != "hello world" {
		t.Fatalf("maxpower must see [hello world], got %+v err=%v", list, err)
	}
	if _, err := s.GetPost(ctx, "acme", mpPost.ID); !errors.Is(err, errNotFound) {
		t.Fatalf("acme GET maxpower post want errNotFound, got %v", err)
	}
	if _, err := s.UpdatePost(ctx, Post{ID: mpPost.ID, Org: "acme", Content: "HIJACK", Channel: "x", Status: "failed", UpdatedAt: 2}); !errors.Is(err, errNotFound) {
		t.Fatalf("acme UPDATE maxpower post want errNotFound, got %v", err)
	}
	if deleted, _ := s.DeletePost(ctx, "acme", mpPost.ID); deleted {
		t.Fatalf("acme must not delete maxpower's post")
	}
	if got, _ := s.GetPost(ctx, "maxpower", mpPost.ID); got.Content != "hello world" {
		t.Fatalf("maxpower post must survive acme delete, got %q", got.Content)
	}
}

// TestPostCRUD exercises the full post lifecycle round-trip + the summary roll-up.
func TestPostCRUD(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)

	created, err := s.CreatePost(ctx, Post{
		ID: "post_1", Org: "hanzo", Content: "launch day", Channel: "linkedin", Status: "draft",
		CreatedAt: 10, UpdatedAt: 10,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	got, err := s.GetPost(ctx, "hanzo", created.ID)
	if err != nil || got.Content != "launch day" || got.Channel != "linkedin" {
		t.Fatalf("get after create: %+v err=%v", got, err)
	}

	// Update: schedule it.
	if _, err := s.UpdatePost(ctx, Post{
		ID: created.ID, Org: "hanzo", Content: "launch day", Channel: "linkedin", Status: "scheduled",
		ScheduleAt: 2000, UpdatedAt: 20,
	}); err != nil {
		t.Fatalf("update: %v", err)
	}
	got, _ = s.GetPost(ctx, "hanzo", created.ID)
	if got.Status != "scheduled" || got.ScheduleAt != 2000 {
		t.Fatalf("update not applied: %+v", got)
	}

	// Status filter narrows the list.
	if rows, _ := s.ListPosts(ctx, "hanzo", "scheduled", 100); len(rows) != 1 {
		t.Fatalf("status=scheduled want 1, got %d", len(rows))
	}
	if rows, _ := s.ListPosts(ctx, "hanzo", "draft", 100); len(rows) != 0 {
		t.Fatalf("status=draft want 0, got %d", len(rows))
	}

	// A connected account + a published post make the summary non-trivial.
	if _, err := s.CreateAccount(ctx, Account{ID: "acct_1", Org: "hanzo", Provider: "linkedin", Handle: "@hanzo", Status: "connected", CreatedAt: 11, UpdatedAt: 11}); err != nil {
		t.Fatalf("create account: %v", err)
	}
	if _, err := s.CreatePost(ctx, Post{ID: "post_2", Org: "hanzo", Content: "shipped", Channel: "x", Status: "published", CreatedAt: 12, UpdatedAt: 12}); err != nil {
		t.Fatalf("create published post: %v", err)
	}

	posts, scheduled, published, accounts, err := s.Counts(ctx, "hanzo")
	if err != nil {
		t.Fatalf("counts: %v", err)
	}
	if posts != 2 || scheduled != 1 || published != 1 || accounts != 1 {
		t.Fatalf("summary want {posts:2,scheduled:1,published:1,accounts:1}, got {%d,%d,%d,%d}", posts, scheduled, published, accounts)
	}

	// Delete removes the post.
	deleted, err := s.DeletePost(ctx, "hanzo", created.ID)
	if err != nil || !deleted {
		t.Fatalf("delete: deleted=%v err=%v", deleted, err)
	}
	if _, err := s.GetPost(ctx, "hanzo", created.ID); !errors.Is(err, errNotFound) {
		t.Fatalf("get after delete want errNotFound, got %v", err)
	}
}

// TestAccountCRUD exercises the account lifecycle round-trip + provider filter.
func TestAccountCRUD(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)

	created, err := s.CreateAccount(ctx, Account{
		ID: "acct_1", Org: "hanzo", Provider: "x", Handle: "@hanzo", Status: "connected", CreatedAt: 10, UpdatedAt: 10,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// Update: flip to disconnected, change handle.
	if _, err := s.UpdateAccount(ctx, Account{
		ID: created.ID, Org: "hanzo", Provider: "x", Handle: "@hanzoai", Status: "disconnected", UpdatedAt: 20,
	}); err != nil {
		t.Fatalf("update: %v", err)
	}
	got, _ := s.GetAccount(ctx, "hanzo", created.ID)
	if got.Status != "disconnected" || got.Handle != "@hanzoai" {
		t.Fatalf("update not applied: %+v", got)
	}

	// Provider filter narrows the list.
	if _, err := s.CreateAccount(ctx, Account{ID: "acct_2", Org: "hanzo", Provider: "tiktok", Handle: "@h", Status: "connected", CreatedAt: 11, UpdatedAt: 11}); err != nil {
		t.Fatalf("create second: %v", err)
	}
	if rows, _ := s.ListAccounts(ctx, "hanzo", "tiktok", 100); len(rows) != 1 {
		t.Fatalf("provider=tiktok want 1, got %d", len(rows))
	}
	if rows, _ := s.ListAccounts(ctx, "hanzo", "", 100); len(rows) != 2 {
		t.Fatalf("all providers want 2, got %d", len(rows))
	}

	// Delete removes it.
	deleted, err := s.DeleteAccount(ctx, "hanzo", created.ID)
	if err != nil || !deleted {
		t.Fatalf("delete: deleted=%v err=%v", deleted, err)
	}
	if _, err := s.GetAccount(ctx, "hanzo", created.ID); !errors.Is(err, errNotFound) {
		t.Fatalf("get after delete want errNotFound, got %v", err)
	}
}
