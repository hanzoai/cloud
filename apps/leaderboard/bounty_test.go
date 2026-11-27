package leaderboard

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/hanzoai/cloud/internal/sqltest"
)

func TestBountyBoard_SeededAndAwardFlow(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		installFakeDS(t, nil)
		app := mountApp(t)

		// 1. Seeded board returns valerypetrov (zlatan mioslav) with 3 bug reports, 150 bounty points and the credits recorded with them, and no token volume the warehouse did not report.
		code, body := doGet(t, app, "/v1/leaderboard/bounty", principalHeaders("valerypetrov", "valerypetrov"))
		if code != http.StatusOK {
			t.Fatalf("GET /v1/leaderboard/bounty code=%d body=%s", code, body)
		}
		var board BountyBoard
		if err := json.Unmarshal(body, &board); err != nil {
			t.Fatalf("unmarshal BountyBoard: %v", err)
		}
		// The board promises nothing: the rules are the rewards program's (GET /v1/reward/rules).
		var keys map[string]json.RawMessage
		if err := json.Unmarshal(body, &keys); err != nil {
			t.Fatalf("unmarshal keys: %v", err)
		}
		if _, ok := keys["rules"]; ok {
			t.Fatalf("the bounty board carries rules: %s", body)
		}
		if len(board.Hunters) == 0 {
			t.Fatal("expected seeded hunters on bounty board")
		}
		top := board.Hunters[0]
		if top.Org != "valerypetrov" || top.BugsReported != 3 || top.BountyPoints != 150 || top.CreditsEarnedCents != 2000 {
			t.Fatalf("unexpected top hunter: %+v", top)
		}
		if top.GlobalTokenRank != 0 || top.TokensUsed != 0 || top.TotalScore != 150 {
			t.Fatalf("an empty warehouse must leave tokens and global rank at zero, got rank=%d tokens=%d score=%d",
				top.GlobalTokenRank, top.TokensUsed, top.TotalScore)
		}
		if board.Self == nil || board.Self.Org != "valerypetrov" {
			t.Fatalf("expected Self standing for valerypetrov, got %+v", board.Self)
		}

		// 2. Non-SuperAdmin cannot POST /v1/admin/leaderboard/bounty.
		code, _ = doJSON(t, app, http.MethodPost, "/v1/admin/leaderboard/bounty", principalHeaders("acme", "alice"), BountyAwardIn{
			Org:     "acme",
			Handle:  "AliceZ",
			Kind:    "bug_report",
			Summary: "Reported race condition",
		})
		if code != http.StatusForbidden {
			t.Fatalf("non-admin award must be 403, got %d", code)
		}

		// 3. SuperAdmin awards a merged bounty PR and verifies rank update.
		superH := withHeader(principalHeaders("hanzo", "admin"), "X-User-IsAdmin", "true")
		code, body = doJSON(t, app, http.MethodPost, "/v1/admin/leaderboard/bounty", superH, BountyAwardIn{
			Org:          "acme",
			User:         "alice",
			Handle:       "AliceZ",
			Kind:         "bounty",
			Points:       250,
			CreditsCents: 2000,
			Summary:      "Merged starter bounty PR",
			Target:       "https://github.com/hanzoai/ui/pull/258",
		})
		if code != http.StatusOK {
			t.Fatalf("SuperAdmin award failed: code=%d body=%s", code, body)
		}
		var out BountyAwardOut
		if err := json.Unmarshal(body, &out); err != nil {
			t.Fatalf("unmarshal BountyAwardOut: %v", err)
		}
		if out.Award.Points != 250 || out.Hunter.BountiesMerged != 1 {
			t.Fatalf("unexpected award out: %+v", out)
		}
	})
}
