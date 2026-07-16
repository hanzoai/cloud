// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
// Licensed under the Apache License, Version 2.0.

package featuregate

import (
	"context"
	"path/filepath"
	"testing"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	st, err := openStore(filepath.Join(t.TempDir(), "featuregate.db"))
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestSeed_Idempotent_NeverClobbersLiveToggle(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	seed := []SeedService{
		{Service: "chat", DisplayName: "Chat", Hosts: []string{"hanzo.chat", "chat.hanzo.ai"}, WaitlistMode: true},
		{Service: "api", DisplayName: "API", Hosts: []string{"api.hanzo.ai"}, WaitlistMode: true},
	}
	created, err := st.Seed(ctx, seed, 100)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	if created != 2 {
		t.Fatalf("first seed created = %d, want 2", created)
	}

	// Admin opens chat (mode OFF).
	if _, err := st.SetMode(ctx, "chat", false, "z@hanzo.ai", 200); err != nil {
		t.Fatalf("SetMode: %v", err)
	}

	// A re-seed (restart) must NOT re-gate chat.
	created, err = st.Seed(ctx, seed, 300)
	if err != nil {
		t.Fatalf("re-seed: %v", err)
	}
	if created != 0 {
		t.Fatalf("re-seed created = %d, want 0 (idempotent)", created)
	}
	got, err := st.Get(ctx, "chat")
	if err != nil {
		t.Fatalf("Get chat: %v", err)
	}
	if got.WaitlistMode {
		t.Fatalf("re-seed clobbered the live toggle: chat re-gated")
	}
}

func TestModeForHost_NormalizationAndUnknown(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	if _, err := st.Seed(ctx, []SeedService{
		{Service: "chat", Hosts: []string{"hanzo.chat"}, WaitlistMode: true},
	}, 100); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Case-insensitive + port-stripped resolution to the same service.
	for _, h := range []string{"hanzo.chat", "Hanzo.Chat", "hanzo.chat:443", " HANZO.CHAT "} {
		mode, svc, known, err := st.ModeForHost(ctx, h)
		if err != nil {
			t.Fatalf("ModeForHost(%q): %v", h, err)
		}
		if !known || svc != "chat" || !mode {
			t.Fatalf("ModeForHost(%q) = mode=%v svc=%q known=%v, want true/chat/true", h, mode, svc, known)
		}
	}

	// An un-governed host is honestly unknown.
	_, _, known, err := st.ModeForHost(ctx, "example.com")
	if err != nil {
		t.Fatalf("ModeForHost(unknown): %v", err)
	}
	if known {
		t.Fatalf("example.com reported known; want unknown")
	}
}

func TestSetMode_TogglesAndStamps(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	if _, err := st.Seed(ctx, []SeedService{{Service: "api", Hosts: []string{"api.hanzo.ai"}, WaitlistMode: true}}, 100); err != nil {
		t.Fatalf("seed: %v", err)
	}
	updated, err := st.SetMode(ctx, "api", false, "z@hanzo.ai", 500)
	if err != nil {
		t.Fatalf("SetMode: %v", err)
	}
	if updated.WaitlistMode || updated.UpdatedBy != "z@hanzo.ai" || updated.UpdatedAt != 500 {
		t.Fatalf("SetMode result = %+v, want mode off, by z@hanzo.ai, at 500", updated)
	}
	// The host now reads mode OFF.
	mode, _, known, _ := st.ModeForHost(ctx, "api.hanzo.ai")
	if !known || mode {
		t.Fatalf("after open: mode=%v known=%v, want false/true", mode, known)
	}
	// Unknown service → errNotFound.
	if _, err := st.SetMode(ctx, "nope", true, "z", 600); err != errNotFound {
		t.Fatalf("SetMode(nope) err = %v, want errNotFound", err)
	}
}

func TestUpsert_OnboardsAndPreservesLiveMode(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	// Onboard a brand-new service at mode ON, no redeploy.
	created, err := st.Upsert(ctx, Service{
		Service: "search", DisplayName: "Search", Hosts: []string{"search.hanzo.ai"}, WaitlistMode: true,
	}, "z@hanzo.ai", 100)
	if err != nil {
		t.Fatalf("Upsert new: %v", err)
	}
	if !created.WaitlistMode || len(created.Hosts) != 1 || created.Hosts[0] != "search.hanzo.ai" {
		t.Fatalf("Upsert new = %+v", created)
	}

	// Admin opens it.
	if _, err := st.SetMode(ctx, "search", false, "z@hanzo.ai", 150); err != nil {
		t.Fatalf("SetMode: %v", err)
	}

	// A metadata edit (add a host) must PRESERVE the opened mode, even though the
	// request body carries waitlistMode=true (a re-register never re-gates).
	edited, err := st.Upsert(ctx, Service{
		Service: "search", DisplayName: "Search v2", Hosts: []string{"search.hanzo.ai", "find.hanzo.ai"}, WaitlistMode: true,
	}, "z@hanzo.ai", 200)
	if err != nil {
		t.Fatalf("Upsert edit: %v", err)
	}
	if edited.WaitlistMode {
		t.Fatalf("Upsert edit re-gated an opened service")
	}
	if edited.DisplayName != "Search v2" || len(edited.Hosts) != 2 {
		t.Fatalf("Upsert edit = %+v, want display Search v2 + 2 hosts", edited)
	}
}

func TestList_SortedWithHosts(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	if _, err := st.Seed(ctx, []SeedService{
		{Service: "chat", Hosts: []string{"chat.hanzo.ai", "hanzo.chat"}, WaitlistMode: true},
		{Service: "api", Hosts: []string{"api.hanzo.ai"}, WaitlistMode: false},
	}, 100); err != nil {
		t.Fatalf("seed: %v", err)
	}
	list, err := st.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 2 || list[0].Service != "api" || list[1].Service != "chat" {
		t.Fatalf("List order = %v, want [api chat]", []string{list[0].Service, list[1].Service})
	}
	if len(list[1].Hosts) != 2 || list[1].Hosts[0] != "chat.hanzo.ai" {
		t.Fatalf("chat hosts = %v, want sorted [chat.hanzo.ai hanzo.chat]", list[1].Hosts)
	}
}
