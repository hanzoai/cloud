package featuregate

// Registry coverage for the host→service store. It drives the store over a raw sqlite
// handle (the same driver OrgDB uses), so it exercises the registry WITHOUT the cek
// at-rest layer — runnable under CGO=0. The MODE is out of scope here by design (it is
// the waitlist.<svc> switch, evaluated by the flag engine, covered separately).

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	_ "github.com/hanzoai/sqlite" // registers "sqlite" under both build tags
)

func newWaitlistStore(t *testing.T) *waitlistStore {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "waitlist.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	st, err := openWaitlistStore(db)
	if err != nil {
		t.Fatalf("openWaitlistStore: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestWaitlistStore_SeedIdempotentAndServiceForHost(t *testing.T) {
	st := newWaitlistStore(t)
	ctx := context.Background()
	seed := []SeedService{
		{Service: "chat", DisplayName: "Chat", Hosts: []string{"hanzo.chat", "chat.hanzo.ai"}},
		{Service: "api", DisplayName: "API", Hosts: []string{"api.hanzo.ai"}},
	}
	created, err := st.Seed(ctx, seed, 100)
	if err != nil || created != 2 {
		t.Fatalf("seed = %d, %v; want 2, nil", created, err)
	}
	if created, _ := st.Seed(ctx, seed, 200); created != 0 {
		t.Fatalf("re-seed created = %d, want 0 (idempotent)", created)
	}
	// Host resolution is case-insensitive + port-stripped → the same service.
	for _, h := range []string{"hanzo.chat", "Hanzo.Chat", "hanzo.chat:443", " HANZO.CHAT "} {
		svc, known, err := st.ServiceForHost(ctx, h)
		if err != nil || !known || svc != "chat" {
			t.Fatalf("ServiceForHost(%q) = %q,%v,%v; want chat,true,nil", h, svc, known, err)
		}
	}
	// An un-governed host is honestly unknown (the decide fail-opens on it).
	if _, known, _ := st.ServiceForHost(ctx, "example.com"); known {
		t.Fatal("example.com reported known; want unknown")
	}
}

func TestWaitlistStore_ListSortedWithHosts(t *testing.T) {
	st := newWaitlistStore(t)
	ctx := context.Background()
	if _, err := st.Seed(ctx, []SeedService{
		{Service: "chat", Hosts: []string{"chat.hanzo.ai", "hanzo.chat"}},
		{Service: "api", Hosts: []string{"api.hanzo.ai"}},
	}, 100); err != nil {
		t.Fatalf("seed: %v", err)
	}
	list, err := st.List(ctx)
	if err != nil || len(list) != 2 {
		t.Fatalf("List = %d, %v; want 2", len(list), err)
	}
	if list[0].Service != "api" || list[1].Service != "chat" {
		t.Fatalf("List order = [%s %s], want [api chat]", list[0].Service, list[1].Service)
	}
	if len(list[1].Hosts) != 2 || list[1].Hosts[0] != "chat.hanzo.ai" {
		t.Fatalf("chat hosts = %v, want sorted [chat.hanzo.ai hanzo.chat]", list[1].Hosts)
	}
}

func TestWaitlistStore_UpsertOnboardsAndReplacesHosts(t *testing.T) {
	st := newWaitlistStore(t)
	ctx := context.Background()
	row, err := st.Upsert(ctx, ServiceRow{Service: "search", DisplayName: "Search", Hosts: []string{"search.hanzo.ai"}}, "z@hanzo.ai", 100)
	if err != nil || len(row.Hosts) != 1 || row.Hosts[0] != "search.hanzo.ai" {
		t.Fatalf("Upsert new = %+v, %v", row, err)
	}
	// A metadata edit REPLACES the host set and updates the display name.
	row, err = st.Upsert(ctx, ServiceRow{Service: "search", DisplayName: "Search v2", Hosts: []string{"search.hanzo.ai", "find.hanzo.ai"}}, "z@hanzo.ai", 200)
	if err != nil || row.DisplayName != "Search v2" || len(row.Hosts) != 2 {
		t.Fatalf("Upsert edit = %+v, %v", row, err)
	}
	svc, known, _ := st.ServiceForHost(ctx, "find.hanzo.ai")
	if !known || svc != "search" {
		t.Fatalf("onboarded host find.hanzo.ai = %q,%v; want search,true", svc, known)
	}
	// An unknown slug is ErrServiceNotFound (the admin lens maps it to 404).
	if _, err := st.Get(ctx, "nope"); !errors.Is(err, ErrServiceNotFound) {
		t.Fatalf("Get(nope) err = %v, want ErrServiceNotFound", err)
	}
}
