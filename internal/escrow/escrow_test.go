package escrow

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/apps/kms"
	"github.com/hanzoai/cloud/internal/kmstest"
)

// A secret is kept at the org's own path and the column gets that path behind the mark;
// a read gives it back, a drop forgets it, and the value is never in what the column holds.
func TestAValueLivesInKMSAndTheColumnHoldsItsPath(t *testing.T) {
	k := kmstest.New()
	e := New(k, "webhooks")
	ctx := context.Background()
	stored, err := e.Put(ctx, "acme", "wh_1", "secret", "whsec_hunter2")
	if err != nil {
		t.Fatal(err)
	}
	if stored != "kms:orgs/acme/webhooks/wh_1/secret@default" || strings.Contains(stored, "hunter2") || !Held(stored) {
		t.Fatalf("Put answered %q", stored)
	}
	if held := k.Held(); len(held) != 1 || held["orgs/acme/webhooks/wh_1/secret@default"] != "whsec_hunter2" {
		t.Fatalf("KMS holds %v", held)
	}
	if v, err := e.Get(ctx, "acme", stored); err != nil || v != "whsec_hunter2" {
		t.Fatalf("Get = %q, %v", v, err)
	}
	if err := e.Drop(ctx, "acme", stored); err != nil {
		t.Fatal(err)
	}
	if refs := k.Refs(); len(refs) != 0 {
		t.Fatalf("a dropped value left %v", refs)
	}
	// Forgetting what is forgotten, or was never kept, is not an error.
	if err := e.Drop(ctx, "acme", stored); err != nil {
		t.Fatalf("a second drop: %v", err)
	}
	if _, err := e.Get(ctx, "acme", stored); !errors.Is(err, ErrGone) {
		t.Fatalf("a read of a forgotten value = %v, want ErrGone", err)
	}
}

// An empty value is kept nowhere and stored as the empty string, which reads as empty.
func TestAnEmptyValueIsKeptNowhere(t *testing.T) {
	k := kmstest.New()
	e := New(k, "social")
	stored, err := e.Put(context.Background(), "acme", "acct_1", "token", "")
	if err != nil || stored != "" || len(k.Refs()) != 0 {
		t.Fatalf("Put of nothing = %q, %v, KMS %v", stored, err, k.Refs())
	}
	if v, err := e.Get(context.Background(), "acme", ""); err != nil || v != "" {
		t.Fatalf("Get of nothing = %q, %v", v, err)
	}
	if err := e.Drop(context.Background(), "acme", ""); err != nil {
		t.Fatalf("Drop of nothing: %v", err)
	}
}

// A value that is not a path is what a file held before: the escrow does not read it as a
// secret, so a store that has not moved it fails rather than hands it on.
func TestAValueIsNotAPath(t *testing.T) {
	e := New(kmstest.New(), "social")
	if _, err := e.Get(context.Background(), "acme", "EAAB-live-token"); err == nil {
		t.Fatal("a plaintext column was read as a reference")
	}
	if Held("EAAB-live-token") || Held("seal:abc") || Held("") {
		t.Fatal("Held took a value for a path")
	}
}

// Every call acts for the record's own org: a path carried to another org is refused by
// KMS, whoever asks.
func TestAPathOfAnotherOrgIsRefused(t *testing.T) {
	k := kmstest.New()
	e := New(k, "social")
	ctx := context.Background()
	stored, err := e.Put(ctx, "acme", "acct_1", "token", "tok")
	if err != nil {
		t.Fatal(err)
	}
	if v, err := e.Get(ctx, "globex", stored); err == nil || v != "" {
		t.Fatalf("globex read acme's token: %q, %v", v, err)
	}
	if err := e.Drop(ctx, "globex", stored); err == nil {
		t.Fatal("globex forgot acme's token")
	}
	if len(k.Refs()) != 1 {
		t.Fatal("a refused drop forgot the value")
	}
}

// The path is made of segments a ref can be cut on: a record or a field that could name
// another path, and an org that cannot key a path, are refused.
func TestThePathRefusesWhatCouldNameAnother(t *testing.T) {
	e := New(kmstest.New(), "social")
	for _, c := range []struct{ org, record, field string }{
		{"acme", "../globex", "token"},
		{"acme", "acct_1/../x", "token"},
		{"acme", "acct_1", "token@prod"},
		{"acme", "", "token"},
		{"acme", "acct_1", ""},
		{"", "acct_1", "token"},
	} {
		if ref, err := e.Ref(c.org, c.record, c.field); err == nil {
			t.Errorf("Ref(%q, %q, %q) = %q, want a refusal", c.org, c.record, c.field, ref)
		}
	}
	want := strings.TrimPrefix(kms.OrgPath("Acme", "social", "wh_1"), "/") + "/secret@default"
	if ref, err := e.Ref("Acme", "wh_1", "secret"); err != nil || ref != want || strings.HasPrefix(ref, "orgs/Acme/") {
		t.Errorf("an org folds as every app's does: %q, %v, want %q", ref, err, want)
	}
}

// With no KMS a value is neither kept nor read, and a path cannot be forgotten.
func TestNoKMSKeepsAndReadsNothing(t *testing.T) {
	e := New(nil, "social")
	ctx := context.Background()
	if _, err := e.Put(ctx, "acme", "acct_1", "token", "tok"); !errors.Is(err, ErrNoKMS) {
		t.Fatalf("Put = %v, want ErrNoKMS", err)
	}
	if _, err := e.Get(ctx, "acme", "kms:orgs/acme/social/acct_1/token@default"); !errors.Is(err, ErrNoKMS) {
		t.Fatalf("Get = %v, want ErrNoKMS", err)
	}
	if err := e.Drop(ctx, "acme", "kms:orgs/acme/social/acct_1/token@default"); !errors.Is(err, ErrNoKMS) {
		t.Fatalf("Drop = %v, want ErrNoKMS", err)
	}
	var none *Escrow
	if _, err := none.Put(ctx, "acme", "acct_1", "token", "tok"); !errors.Is(err, ErrNoKMS) {
		t.Fatalf("a nil escrow Put = %v, want ErrNoKMS", err)
	}
}
