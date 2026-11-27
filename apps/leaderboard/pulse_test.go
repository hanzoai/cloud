package leaderboard

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/sqltest"
)

// noon is the moment these tests are held at: 2026-11-08 13:30 UTC. The thirty days are
// 2026-10-10..2026-11-08 and the twenty-four hours 2026-11-07T14:00Z..2026-11-08T13:00Z.
var noon = time.Date(2026, 11, 8, 13, 30, 0, 0, time.UTC)

func at(t *testing.T) {
	t.Helper()
	was := nowFn
	nowFn = func() time.Time { return noon }
	t.Cleanup(func() { nowFn = was })
}

// pulseRows answers the pulse's five warehouse reads.
func pulseRows(sql string, _ []any) []map[string]any {
	switch {
	case strings.Contains(sql, "GROUP BY hour, model"):
		h13, h12 := time.Date(2026, 11, 8, 13, 0, 0, 0, time.UTC), time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
		return []map[string]any{
			{"hour": h13, "model": "zen4"},
			{"hour": h13, "model": "ZEN4 "},         // the same model, spelled otherwise
			{"hour": h13, "model": "openai/gpt-4o"}, // a vendor's path: the model served, not one of ours
			{"hour": h13, "model": ""},              // names none
			{"hour": h12, "model": "zen4"},
			{"hour": h12, "model": " Enso-Auto"},
			{"hour": h12, "model": "enso-auto"},
			{"hour": "2026-11-07 14:00:00", "model": "enso-auto"},
			{"hour": time.Date(2026, 11, 7, 13, 0, 0, 0, time.UTC), "model": "before-the-window"},
		}
	case strings.Contains(sql, "GROUP BY day, model"):
		d8, d7 := time.Date(2026, 11, 8, 0, 0, 0, 0, time.UTC), "2026-11-07"
		return []map[string]any{
			{"day": d8, "model": "enso-auto"},
			{"day": d8, "model": "Enso-Auto"},
			{"day": d8, "model": " enso-auto "},
			{"day": d8, "model": "zen4"},
			{"day": d8, "model": "anthropic/claude-sonnet-4"},
			{"day": d8, "model": ""},
			{"day": d7, "model": "enso-auto"},
			{"day": d7, "model": "enso-pro"},
			{"day": d7, "model": "zen4"},
			{"day": "2025-01-01", "model": "before-the-window"},
		}
	case strings.Contains(sql, "toStartOfHour"):
		return []map[string]any{
			{"hour": time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC), "total_tokens": uint64(7_000), "orgs": uint64(3)},
			{"hour": time.Date(2026, 11, 8, 13, 0, 0, 0, time.UTC), "total_tokens": uint64(900), "orgs": uint64(2)},
			{"hour": "2026-11-07 14:00:00", "total_tokens": uint64(50), "orgs": uint64(1)},
		}
	case strings.Contains(sql, "first_day"):
		return []map[string]any{
			{"day": time.Date(2026, 11, 8, 0, 0, 0, 0, time.UTC), "model": "zen4", "tries": uint64(3)},
			{"day": time.Date(2026, 11, 7, 0, 0, 0, 0, time.UTC), "model": "zen4", "tries": uint64(2)},
			{"day": time.Date(2026, 11, 7, 0, 0, 0, 0, time.UTC), "model": "enso-pro", "tries": uint64(4)},
			{"day": time.Date(2026, 11, 7, 0, 0, 0, 0, time.UTC), "model": "qwen3-72b", "tries": uint64(9)}, // a base model: never named
			{"day": time.Date(2026, 11, 7, 0, 0, 0, 0, time.UTC), "model": "", "tries": uint64(9)},
		}
	case strings.Contains(sql, "GROUP BY day"):
		return []map[string]any{
			{"day": time.Date(2026, 11, 8, 0, 0, 0, 0, time.UTC), "total_tokens": uint64(12_000), "orgs": uint64(5)},
			{"day": "2026-11-07", "total_tokens": uint64(30_000), "orgs": uint64(8)},
			{"day": "2025-01-01", "total_tokens": uint64(99), "orgs": uint64(1)}, // outside the window
		}
	}
	return nil
}

func pulseWorld(t *testing.T) *fakeDS {
	t.Helper()
	at(t)
	world(t,
		[]iamOrg{{"early", "2026-10-01T00:00:00Z"}, {"newer", "2026-11-07T20:15:00Z"}, {"newest", "2026-11-08T13:05:00Z"}, {"also", "2026-11-08T09:00:00Z"}},
		nil, nil, nil)
	ds := installFakeDS(t, pulseRows)
	referralSeriesFn = func(context.Context, *client.ReferralSeriesIn) (*client.ReferralSignups, error) {
		return &client.ReferralSignups{At: []int64{
			time.Date(2026, 11, 8, 13, 10, 0, 0, time.UTC).Unix(),
			time.Date(2026, 11, 8, 9, 0, 0, 0, time.UTC).Unix(),
			time.Date(2026, 11, 7, 8, 0, 0, 0, time.UTC).Unix(),
		}}, nil
	}
	t.Cleanup(func() { referralSeriesFn = defaultReferralSeries })
	return ds
}

// failing makes every warehouse read whose SQL holds marker fail, and answers the rest
// as it did. installFakeDS puts the datastore back when the test ends.
func failing(marker string) {
	inner := queryDatastore
	queryDatastore = func(ctx context.Context, sql string, args ...any) ([]map[string]any, error) {
		if strings.Contains(sql, marker) {
			return nil, errors.New("warehouse down")
		}
		return inner(ctx, sql, args...)
	}
}

func readPulseBody(t *testing.T, headers map[string]string) (PulseView, string) {
	t.Helper()
	app := mountApp(t)
	code, body := doGet(t, app, "/v1/leaderboard/pulse", headers)
	if code != http.StatusOK {
		t.Fatalf("pulse: %d %s", code, body)
	}
	var v PulseView
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatalf("pulse body: %v: %s", err, body)
	}
	return v, string(body)
}

func TestPulseSeriesFromWhereTheyAreRecorded(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		pulseWorld(t)
		v, body := readPulseBody(t, nil) // with no principal
		if !v.Available || len(v.Days) != 30 || v.Days[0] != "2026-10-10" || v.Days[29] != "2026-11-08" {
			t.Fatalf("days: %v", v.Days)
		}
		if len(v.Hours) != 24 || v.Hours[0] != "2026-11-07T14:00:00Z" || v.Hours[23] != "2026-11-08T13:00:00Z" {
			t.Fatalf("hours: %v", v.Hours)
		}
		// Tokens and active orgs: from the rollup by day, and from the ledger by hour.
		if v.Tokens == nil || v.Tokens.Days[29] != 12_000 || v.Tokens.Days[28] != 30_000 || v.Tokens.Hours[22] != 7_000 || v.Tokens.Hours[23] != 900 || v.Tokens.Hours[0] != 50 {
			t.Fatalf("tokens: %+v", v.Tokens)
		}
		if v.Orgs == nil || v.Orgs.Days[29] != 5 || v.Orgs.Hours[22] != 3 {
			t.Fatalf("orgs: %+v", v.Orgs)
		}
		// Signups: IAM's org created times; "early" is before the window.
		if v.Signups == nil || v.Signups.Days[29] != 2 || v.Signups.Days[28] != 1 || v.Signups.Hours[23] != 1 || v.Signups.Hours[19] != 1 || v.Signups.Hours[6] != 1 {
			t.Fatalf("signups: %+v", v.Signups)
		}
		// Referred signups: the referral ledger's moments.
		if v.Referred == nil || v.Referred.Days[29] != 2 || v.Referred.Days[28] != 1 || v.Referred.Hours[23] != 1 || v.Referred.Hours[19] != 1 {
			t.Fatalf("referred: %+v", v.Referred)
		}
		// First tries of a model: by day only, and a base model is never named.
		if v.Models == nil || v.Models.Days[29] != 3 || v.Models.Days[28] != 6 || v.Models.Hours != nil {
			t.Fatalf("models: %+v", v.Models)
		}
		if len(v.Tries) != 2 || v.Tries[0].Name != "zen4" || v.Tries[0].Count != 5 || v.Tries[1].Name != "enso-pro" {
			t.Fatalf("tries order: %+v", v.Tries)
		}
		if strings.Contains(body, "qwen") {
			t.Fatalf("a base model is named: %s", body)
		}
		// Two series nothing records are named, never drawn.
		if strings.Join(v.Missing, ",") != "products,shares" {
			t.Fatalf("missing: %v", v.Missing)
		}
		for _, leak := range []string{"early", "newer", "newest", "also", "@"} {
			if strings.Contains(body, `"`+leak+`"`) {
				t.Errorf("the pulse names %q: %s", leak, body)
			}
		}
	})
}

func TestPulseLeavesOutASeriesItCannotRead(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		pulseWorld(t)
		referralSeriesFn = func(context.Context, *client.ReferralSeriesIn) (*client.ReferralSignups, error) {
			return nil, errors.New("referral peer down")
		}
		v, body := readPulseBody(t, nil)
		if v.Referred != nil || strings.Contains(body, `"referred":{`) {
			t.Fatalf("a series that was not read is not drawn as zeroes: %s", body)
		}
		if strings.Join(v.Missing, ",") != "products,referred,shares" || v.Tokens == nil || !v.Available {
			t.Fatalf("missing: %v", v.Missing)
		}
	})
}

func TestPulseWithoutAWarehouseDrawsWhatIsElsewhere(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		pulseWorld(t)
		datastoreEnabled = func() bool { return false }
		v, _ := readPulseBody(t, nil)
		if v.Tokens != nil || v.Orgs != nil || v.Models != nil || v.Variety != nil || v.Signups == nil || v.Referred == nil || !v.Available {
			t.Fatalf("pulse: %+v", v)
		}
		if strings.Join(v.Missing, ",") != "models,orgs,products,shares,tokens,variety" || v.Signups.Days[29] != 2 {
			t.Fatalf("missing: %v", v.Missing)
		}
	})
}

func TestPulseIsPublicAndCached(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		pulseWorld(t)
		var asked int
		referralSeriesFn = func(context.Context, *client.ReferralSeriesIn) (*client.ReferralSignups, error) {
			asked++
			return &client.ReferralSignups{}, nil
		}
		app := mountApp(t)
		for range 3 {
			if code, body := doGet(t, app, "/v1/leaderboard/pulse", nil); code != http.StatusOK {
				t.Fatalf("pulse with no principal: %d %s", code, body)
			}
		}
		if asked != 1 {
			t.Fatalf("the referral ledger was read %d times for three readers, want 1", asked)
		}
	})
}

// bucketed is a series' counts at the indexes named, and zero everywhere else.
func bucketed(counts []int64, want map[int]int64) bool {
	for i, n := range counts {
		if n != want[i] {
			return false
		}
	}
	return true
}

func TestPulseVarietyCountsAModelOncePerBucket(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		ds := pulseWorld(t)
		v, body := readPulseBody(t, nil)
		// 2026-11-08 holds enso-auto three ways, zen4, a vendor path and an empty id: two
		// models. 2026-11-07 holds enso-auto, enso-pro and zen4: three. The row before the
		// window falls on no day.
		if v.Variety == nil || !bucketed(v.Variety.Days, map[int]int64{29: 2, 28: 3}) {
			t.Fatalf("variety by day: %+v", v.Variety)
		}
		// 13:00 holds zen4 twice spelled, a vendor path and an empty id: one. 12:00 holds zen4
		// and enso-auto twice spelled: two. The first hour of the window, which a string
		// stamp names, holds enso-auto: one. The hour before the window falls on none.
		if !bucketed(v.Variety.Hours, map[int]int64{23: 1, 22: 2, 0: 1}) {
			t.Fatalf("variety by hour: %+v", v.Variety.Hours)
		}
		for _, id := range []string{"gpt-4o", "claude", "before-the-window", "openai/", "anthropic/"} {
			if strings.Contains(body, id) {
				t.Errorf("the pulse names a model it only counts (%q): %s", id, body)
			}
		}
		if strings.Join(v.Missing, ",") != "products,shares" {
			t.Fatalf("missing: %v", v.Missing)
		}
		// The window bounds are bound parameters, never part of the SQL.
		var days, hours *dsCall
		for _, c := range ds.allCalls() {
			switch {
			case strings.Contains(c.sql, "GROUP BY day, model"):
				days = &c
			case strings.Contains(c.sql, "GROUP BY hour, model"):
				hours = &c
			}
		}
		if days == nil || len(days.args) != 2 || days.args[0] != "2026-10-10" || days.args[1] != "2026-11-09" || strings.Contains(days.sql, "2026") {
			t.Fatalf("variety by day asked: %+v", days)
		}
		if hours == nil || len(hours.args) != 1 || hours.args[0] != "2026-11-07 14:00:00" || strings.Contains(hours.sql, "2026") {
			t.Fatalf("variety by hour asked: %+v", hours)
		}
	})
}

func TestPulseLeavesOutVarietyItCannotRead(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		pulseWorld(t)
		failing("GROUP BY day, model")
		v, body := readPulseBody(t, nil)
		if v.Variety != nil || strings.Contains(body, `"variety":{`) {
			t.Fatalf("a series that was not read is not drawn as zeroes: %s", body)
		}
		if strings.Join(v.Missing, ",") != "products,shares,variety" || !v.Available {
			t.Fatalf("missing: %v", v.Missing)
		}
		// Every other series still answers.
		if v.Tokens == nil || v.Orgs == nil || v.Models == nil || v.Signups == nil || v.Referred == nil || v.Tokens.Days[29] != 12_000 || v.Models.Days[29] != 3 {
			t.Fatalf("the others: %+v", v)
		}
	})
}

func TestPulseVarietyKeepsItsDaysWhenOnlyTheHoursFail(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		pulseWorld(t)
		failing("GROUP BY hour, model")
		v, _ := readPulseBody(t, nil)
		if v.Variety == nil || v.Variety.Hours != nil {
			t.Fatalf("variety: %+v", v.Variety)
		}
		if !bucketed(v.Variety.Days, map[int]int64{29: 2, 28: 3}) {
			t.Fatalf("variety by day: %+v", v.Variety.Days)
		}
		if strings.Join(v.Missing, ",") != "products,shares" {
			t.Fatalf("missing: %v", v.Missing)
		}
	})
}
