package referral

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/zap-proto/zip"
)

// told catches what the claim tells marketing, answering with err.
func told(t *testing.T, err error) *[]*client.InviteJoin {
	t.Helper()
	var got []*client.InviteJoin
	was := joinFn
	joinFn = func(_ context.Context, in *client.InviteJoin) (*client.InviteJoined, error) {
		got = append(got, in)
		if err != nil {
			return nil, err
		}
		return &client.InviteJoined{Joined: 1}, nil
	}
	t.Cleanup(func() { joinFn = was })
	return &got
}

// claimWith claims code as org, with the invite named and the address given.
func claimWith(t *testing.T, app *zip.App, org, code, invite, email string) int {
	t.Helper()
	body := `{"code":"` + code + `"`
	if invite != "" {
		body += `,"invite":"` + invite + `"`
	}
	hr := httptest.NewRequest(http.MethodPost, "/v1/referral/claim", strings.NewReader(body+"}"))
	hr.Header.Set("Content-Type", "application/json")
	hr.Header.Set("X-Org-Id", org)
	hr.Header.Set("X-User-Id", "u_"+org)
	if email != "" {
		hr.Header.Set("X-User-Email", email)
	}
	resp, err := app.Test(hr, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

// TestAClaimTellsMarketingTheInviteItAnswered: a claim naming an invite, or made
// by a person with a verified address, tells marketing the referral AS RECORDED —
// so a replay with another code still names the first referrer — and a claim with
// neither tells it nothing.
func TestAClaimTellsMarketingTheInviteItAnswered(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, s, _ := mount(t)
		ctx := context.Background()
		aCode, _ := s.State.store.EnsureCode(ctx, "orgA")
		cCode, _ := s.State.store.EnsureCode(ctx, "orgC")
		got := told(t, nil)

		if code := claimWith(t, app, "orgB", aCode, "", ""); code != http.StatusCreated {
			t.Fatalf("plain claim: %d", code)
		}
		if len(*got) != 0 {
			t.Fatalf("a claim with no invite and no address told marketing: %+v", *got)
		}

		if code := claimWith(t, app, "orgD", aCode, "inv_4f1a9c2e7b3d5a6c8e0f1a2b", "Grace@Example.com "); code != http.StatusCreated {
			t.Fatalf("invited claim: %d", code)
		}
		if len(*got) != 1 {
			t.Fatalf("the invited claim told marketing %d times", len(*got))
		}
		if in := (*got)[0]; in.Referrer != "orgA" || in.Referee != "orgD" || in.Invite != "inv_4f1a9c2e7b3d5a6c8e0f1a2b" || in.Email != "grace@example.com" {
			t.Fatalf("told: %+v", in)
		}

		// A replay with orgC's code is the referral on file: orgA's.
		if code := claimWith(t, app, "orgD", cCode, "inv_0d9e8c7b6a5f4e3d2c1b0a9f", ""); code != http.StatusOK {
			t.Fatalf("replay: %d", code)
		}
		if in := (*got)[1]; in.Referrer != "orgA" || in.Invite != "inv_0d9e8c7b6a5f4e3d2c1b0a9f" {
			t.Fatalf("a replay named another referrer: %+v", in)
		}

	})
}

// TestMarketingDownNeverFailsAClaim: the referral is the record; an invite left
// unmarked is a reading that is late, never a claim refused.
func TestMarketingDownNeverFailsAClaim(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, s, _ := mount(t)
		aCode, _ := s.State.store.EnsureCode(context.Background(), "orgA")
		got := told(t, errors.New("marketing is not answering"))
		if code := claimWith(t, app, "orgB", aCode, "inv_4f1a9c2e7b3d5a6c8e0f1a2b", ""); code != http.StatusCreated {
			t.Fatalf("a claim failed with marketing down: %d", code)
		}
		if len(*got) != 1 {
			t.Fatalf("marketing was not asked: %d", len(*got))
		}

	})
}

// TestCodeOfIsTheOrgsOwnCode: the plane answers the same stable code GET
// /v1/referral answers that org, and refuses an org it is not given.
func TestCodeOfIsTheOrgsOwnCode(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		_, s, _ := mount(t)
		ctx := context.Background()
		want, _ := s.State.store.EnsureCode(ctx, "orgA")
		got, err := codeOf(ctx, s, &client.ReferralOrg{Org: " orgA "})
		if err != nil || got.Code != want || got.Link != "https://hanzo.ai/?ref="+want {
			t.Fatalf("code of orgA: %+v %v (want %s)", got, err, want)
		}
		if _, err := codeOf(ctx, s, &client.ReferralOrg{}); err == nil {
			t.Fatal("an empty org was answered a code")
		}

	})
}
