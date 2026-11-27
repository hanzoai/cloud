package reward

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/hanzoai/orm"
	"github.com/hanzoai/reward/ledger"

	"github.com/hanzoai/cloud/apps/crm"
	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/sqltest"
)

func (r *rig) legacySources() {
	r.directory(
		cloudUser{ID: "sub-ann", Owner: "acme", Name: "ann", Admin: true, Created: "2026-01-01T00:00:00Z"},
		cloudUser{ID: "sub-bob", Owner: "acme", Name: "bob", Created: "2026-01-02T00:00:00Z"},
		cloudUser{ID: "sub-svc", Owner: "acme", Name: "svc", Type: "service-account"},
	)
	r.warehouse(map[string][]map[string]any{"AS total FROM": {
		{"user_id": "acme/ann", "total": uint64(250_000)},
		{"user_id": "acme/bob", "total": uint64(99_999)},
		{"user_id": "acme/svc", "total": uint64(50_000_000)},
		{"user_id": "ghost/nobody", "total": uint64(50_000_000)},
	}})
	stub(r.t, &tallyFn, func(_ context.Context, in *client.ReferralTallyIn) (*client.ReferralTally, error) {
		out := &client.ReferralTally{}
		for _, o := range in.Orgs {
			if o == "acme" {
				out.Orgs = append(out.Orgs, client.ReferralOrgTally{Org: "acme", Referrals: 3, Qualified: 1})
			}
		}
		return out, nil
	})
	stub(r.t, &bountyFn, func(context.Context) (*client.BountyTotals, error) {
		return &client.BountyTotals{Orgs: client.OrgTotals(map[string]int{"acme": 30, "ghostorg": 99})}, nil
	})
	stub(r.t, &installFn, func(context.Context, string) error { return nil })
	stub(r.t, &waitingFn, func(context.Context, string) ([]crm.Wait, error) {
		rec := `{"user":"sub-ann","actions":[{"action":"star","target":"hanzoai/a"},{"action":"star","target":"hanzoai/b"},{"action":"star","target":"hanzoai/b"},{"action":"follow","target":"zeekay"}]}`
		stranger := `{"user":"sub-stranger","actions":[{"action":"star","target":"hanzoai/a"}]}`
		return []crm.Wait{{Data: json.RawMessage(rec)}, {Data: json.RawMessage(stranger)}}, nil
	})
}

func TestTheLegacyBackfillAgreesWithTheSharedScoreAndRecordsWhoRanIt(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		r := newRig(t)
		r.legacySources()
		ctx := context.Background()
		out, err := r.s.legacy(ctx, "staff@hanzo.ai")
		if err != nil {
			t.Fatal(err)
		}
		// ann: 250K tokens, 3 referred signups and 1 that paid (org acme's), 2 distinct
		// stars and a follow on the waitlist, 30 bounty points. The one formula says:
		want := int64(client.Points(client.Sources{Tokens: 250_000, Referrals: 3, Qualified: 1, Stars: 2, Follows: 1, Bounty: 30}))
		if want != 2+3+20+2+1+30 {
			t.Fatalf("the shared score changed: %d", want)
		}
		acct, err := r.s.eng.Standing(ctx, program, "sub-ann")
		if err != nil || acct.Lifetime != want || acct.Redeemable != 0 {
			t.Fatalf("ann = %+v %v, want %d lifetime and nothing redeemable", acct, err, want)
		}
		// bob holds 99,999 tokens: under a point, so nothing is written for him. The
		// service account and a person IAM does not list earn nothing.
		for _, sub := range []string{"sub-bob", "sub-svc", "sub-stranger"} {
			if a, _ := r.s.eng.Standing(ctx, program, sub); a.Lifetime != 0 {
				t.Errorf("%s holds %d", sub, a.Lifetime)
			}
		}
		if out.Subjects != 1 || out.Entries != 6 || out.Points != want || out.By != "staff@hanzo.ai" {
			t.Errorf("out = %+v", out)
		}
		for source, pts := range map[string]int64{"usage.tokens": 2, "referral.signup": 3, "referral.paying": 20, "github.star": 2, "github.follow": 1, "bounty": 30} {
			if out.Sources[source].Points != pts || out.Sources[source].People != 1 {
				t.Errorf("%s = %+v, want 1 person and %d points", source, out.Sources[source], pts)
			}
		}
		// level n needs 10*n*n points: 58 is level 2 of the Photon band, and a real level is read.
		if acct.Level.Name != "Photon 2" || acct.Level.Band != "Photon" {
			t.Errorf("level = %+v", acct.Level)
		}
		// the trail names who ran it.
		trail, err := orm.TypedQuery[ledger.Trail](r.s.db).Filter("Verb=", "ledger.backfilled").GetAll(ctx)
		if err != nil || len(trail) != 1 || trail[0].Actor != "staff@hanzo.ai" {
			t.Errorf("trail = %+v %v", trail, err)
		}
		// run again: idempotent, the first count stands.
		again, err := r.s.legacy(ctx, "staff@hanzo.ai")
		if err != nil || again.Entries != 0 || again.Points != 0 {
			t.Errorf("a second run wrote %+v %v", again, err)
		}
		if a, _ := r.s.eng.Standing(ctx, program, "sub-ann"); a.Lifetime != want {
			t.Errorf("a second run moved ann to %d", a.Lifetime)
		}

	})
}

func TestTheBackfillRouteIsTheSuperAdminsAndNamesThem(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		r := newRig(t)
		r.legacySources()
		if code, _ := r.do(http.MethodPost, "/v1/admin/reward/legacy", memberHeaders("sub-ann", "acme"), map[string]any{}); code != http.StatusForbidden {
			t.Errorf("a member ran the backfill: %d", code)
		}
		code, body := r.do(http.MethodPost, "/v1/admin/reward/legacy", platformHeaders, map[string]any{})
		var out legacyOut
		if err := json.Unmarshal(body, &out); code != http.StatusOK || err != nil || out.By != "staff@hanzo.ai" || out.Subjects != 1 {
			t.Fatalf("backfill = %d %s", code, body)
		}
		// a signed-in member now reads a real level and their points.
		code, body = r.do(http.MethodGet, "/v1/reward/me", memberHeaders("sub-ann", "acme"), nil)
		if code != http.StatusOK || !json.Valid(body) {
			t.Fatalf("me = %d %s", code, body)
		}
		var me struct {
			Programs []struct {
				Lifetime int64 `json:"lifetime"`
				Level    struct {
					Name string `json:"name"`
				} `json:"level"`
			} `json:"programs"`
		}
		_ = json.Unmarshal(body, &me)
		if len(me.Programs) != 1 || me.Programs[0].Lifetime != 58 || me.Programs[0].Level.Name != "Photon 2" {
			t.Errorf("me = %+v (%s)", me, body)
		}

	})
}
