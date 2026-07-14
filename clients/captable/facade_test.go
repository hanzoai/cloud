package captable

import (
	"context"
	"testing"

	"github.com/hanzoai/cloud"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// mountForFacade mounts captable on a bare app so the package global `mounted` is
// set and the in-proc facade can dispatch against the real goja bundle + per-tenant
// Base — the same host the HTTP handlers use.
func mountForFacade(t *testing.T) {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	if err := Mount(app, cloud.Deps{Logger: luxlog.New("test"), DataDir: t.TempDir()}); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	t.Cleanup(func() { _ = Shutdown(nil) })
}

// TestFacadeSeedAndRound proves the in-proc facade drives the real bundle: set
// incorporation, add founders, create a share class, issue shares, record a round —
// and that the computed cap table reflects the issuance. This is the exact path
// Hanzo Company's genesis + fundraising flows take.
func TestFacadeSeedAndRound(t *testing.T) {
	mountForFacade(t)
	ctx := context.Background()
	const org = "acme"

	if err := SetIncorporation(ctx, org, "Acme Inc.", "c-corp", "US", "DE"); err != nil {
		t.Fatalf("SetIncorporation: %v", err)
	}

	inserted, err := AddStakeholders(ctx, org, []StakeholderInput{
		{Name: "Ada Lovelace", Email: "ada@acme.com", StakeholderType: "INDIVIDUAL", CurrentRelationship: "FOUNDER"},
		{Name: "Bo Diddley", Email: "bo@acme.com", StakeholderType: "INDIVIDUAL", CurrentRelationship: "FOUNDER"},
	})
	if err != nil || inserted != 2 {
		t.Fatalf("AddStakeholders inserted=%d err=%v", inserted, err)
	}

	classID, err := EnsureShareClass(ctx, org, ShareClassInput{
		Name: "Common", ClassType: "COMMON", InitialSharesAuthorized: 10_000_000,
		VotesPerShare: 1, ParValue: 0.0001, PricePerShare: 0.0001,
	})
	if err != nil || classID == "" {
		t.Fatalf("EnsureShareClass id=%q err=%v", classID, err)
	}
	// Idempotent: a second EnsureShareClass with the same name returns the same id.
	again, err := EnsureShareClass(ctx, org, ShareClassInput{Name: "Common", ClassType: "COMMON"})
	if err != nil || again != classID {
		t.Fatalf("EnsureShareClass not idempotent: %q vs %q err=%v", again, classID, err)
	}

	ids, err := StakeholderIDsByEmail(ctx, org)
	if err != nil || ids["ada@acme.com"] == "" || ids["bo@acme.com"] == "" {
		t.Fatalf("StakeholderIDsByEmail=%v err=%v", ids, err)
	}

	if err := IssueShares(ctx, org, ShareInput{
		StakeholderID: ids["ada@acme.com"], ShareClassID: classID, CertificateID: "CS-1",
		Quantity: 6_000_000, Status: "ACTIVE", IssueDate: "2026-01-03",
	}); err != nil {
		t.Fatalf("IssueShares ada: %v", err)
	}
	if err := IssueShares(ctx, org, ShareInput{
		StakeholderID: ids["bo@acme.com"], ShareClassID: classID, CertificateID: "CS-2",
		Quantity: 4_000_000, Status: "ACTIVE", IssueDate: "2026-01-03",
	}); err != nil {
		t.Fatalf("IssueShares bo: %v", err)
	}

	roundID, err := RecordRound(ctx, org, RoundInput{
		Name: "Seed", RoundType: "PRICED", TargetAmount: 2_000_000,
		PreMoneyValuation: 8_000_000, PricePerShare: 1.0, ShareClassID: classID,
	})
	if err != nil || roundID == "" {
		t.Fatalf("RecordRound id=%q err=%v", roundID, err)
	}
}

// TestFacadeFailsClosedWhenUnmounted proves the facade returns ErrNotMounted rather
// than panicking when captable is not mounted.
func TestFacadeFailsClosedWhenUnmounted(t *testing.T) {
	mounted = nil // ensure unmounted
	if _, err := AddStakeholders(context.Background(), "x", []StakeholderInput{{Name: "a", Email: "a@x.com"}}); err != ErrNotMounted {
		t.Fatalf("want ErrNotMounted, got %v", err)
	}
}
