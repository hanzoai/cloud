package reward

// tenure.go — who has been here, who came every day, and who was here first.
//
// The three read what the platform already holds and state a milestone for every
// threshold a person has reached, history included: a person who signed up three
// years ago has been here three years, and that is proved by the account's own
// sign-up time and by the metering ledger, so none of them is forward only. Each
// event is idempotent by its id, and each pass runs once a day.

import (
	"context"
	"encoding/json"
	"slices"
	"strconv"
	"time"

	"github.com/hanzoai/reward/catalog"

	"github.com/hanzoai/cloud/sdk"
)

var (
	// tenureMonths are the months since first sign-up that earn.
	tenureMonths = []int{1, 6, 12, 24, 36}
	// streakDays are the consecutive active days that earn.
	streakDays = []int{7, 30, 100}
)

// monthsBetween is the whole calendar months from from to to.
func monthsBetween(from, to time.Time) int {
	if to.Before(from) {
		return 0
	}
	m := (to.Year()-from.Year())*12 + int(to.Month()) - int(from.Month())
	if to.Day() < from.Day() || (to.Day() == from.Day() && to.Before(from.AddDate(0, m, 0))) {
		m--
	}
	return max(m, 0)
}

// longestRun is the most consecutive days in days, which are YYYY-MM-DD, sorted
// ascending, without repeats.
func longestRun(days []string) int {
	best, run := 0, 0
	var prev time.Time
	for _, d := range days {
		t, err := time.Parse(time.DateOnly, d)
		if err != nil {
			continue
		}
		if !prev.IsZero() && t.Sub(prev) == 24*time.Hour {
			run++
		} else {
			run = 1
		}
		prev, best = t, max(best, run)
	}
	return best
}

// daily runs pass once a day: the cursor named name is the last day it ran.
func (s *state) daily(name string, pass func(context.Context) error) func(context.Context) error {
	return func(ctx context.Context) error {
		day, err := s.cursorOf(ctx, name)
		if err != nil || day == today() {
			return err
		}
		if err := pass(ctx); err != nil {
			return err
		}
		return s.moveCursor(ctx, name, today())
	}
}

// sweepTenure states the tenure each person has reached.
func (s *state) sweepTenure(ctx context.Context) error {
	who, err := roster(ctx)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	var stated int
	for _, sub := range sorted(who.created) {
		months := monthsBetween(who.created[sub], now)
		for _, m := range tenureMonths {
			if m > months {
				break
			}
			ok, err := s.once(ctx, catalog.Event{ID: "tenure.reached:" + sub + ":" + strconv.Itoa(m), Type: "tenure.reached", Subject: sub, At: who.created[sub].AddDate(0, m, 0),
				Fields: map[string]any{"months": m}})
			if err != nil {
				return err
			}
			if ok {
				stated++
			}
		}
	}
	if stated > 0 {
		s.base.Log.Info("reward: tenure stated", "events", stated)
	}
	return nil
}

// sweepStreaks states the streak each person has reached: the longest run of days
// with metered usage they ever had, from the usage rollup, for everyone active
// since the last pass (everyone, on the first).
func (s *state) sweepStreaks(ctx context.Context) error {
	if !readyFn() {
		return nil
	}
	since, err := s.cursorOf(ctx, "streak")
	if err != nil {
		return err
	}
	if since == "" {
		since = "1970-01-01"
	}
	rows, err := queryFn(ctx, `SELECT user_id, toString(day) AS d FROM `+rollup+`
		WHERE total_tokens > 0 AND user_id IN (SELECT DISTINCT user_id FROM `+rollup+` WHERE day >= toDate(?) AND total_tokens > 0)
		GROUP BY user_id, day ORDER BY user_id, day`, since)
	if err != nil {
		return err
	}
	who, err := roster(ctx)
	if err != nil {
		return err
	}
	days := map[string][]string{}
	for _, r := range rows {
		if sub, ok := who.bySlug[txt(r["user_id"])]; ok {
			days[sub] = append(days[sub], txt(r["d"]))
		}
	}
	var stated int
	for _, sub := range sorted(days) {
		best := longestRun(days[sub])
		for _, d := range streakDays {
			if d > best {
				break
			}
			ok, err := s.once(ctx, catalog.Event{ID: "streak.reached:" + sub + ":" + strconv.Itoa(d), Type: "streak.reached", Subject: sub, At: time.Now().UTC(),
				Fields: map[string]any{"days": d}})
			if err != nil {
				return err
			}
			if ok {
				stated++
			}
		}
	}
	if stated > 0 {
		s.base.Log.Info("reward: streaks stated", "events", stated)
	}
	return nil
}

// waitRecord is what a waitlist record says of an application.
type waitRecord struct {
	User    string   `json:"user"`
	Program string   `json:"program"`
	Applied string   `json:"applied"`
	Betas   []string `json:"betas"`
}

// reasons are why a person is eligible to be a Founding Developer, strongest first.
var reasons = []string{"alpha", "beta", "early"}

// sweepFounders states the Founding Developer eligibility of everyone who applied
// for the beta or the alpha, from the brand's waitlist records, and of everyone whose
// account was made before the program's founding cutoff, when staff have set one. A
// person is eligible once whatever the reasons, under the strongest.
func (s *state) sweepFounders(ctx context.Context) error {
	who, err := roster(ctx)
	if err != nil {
		return err
	}
	brand := cloud.For(context.WithoutCancel(ctx), cloud.Brand())
	if err := installFn(brand, cloud.Brand()); err != nil {
		return err
	}
	recs, err := waitingFn(brand, cloud.Brand())
	if err != nil {
		return err
	}
	why := map[string]string{}
	strongest := func(sub, reason string) {
		if have, ok := why[sub]; !ok || slices.Index(reasons, reason) < slices.Index(reasons, have) {
			why[sub] = reason
		}
	}
	for _, r := range recs {
		var d waitRecord
		if json.Unmarshal(r.Data, &d) != nil || !who.member[d.User] {
			continue
		}
		if slices.Contains(d.Betas, "alpha") {
			strongest(d.User, "alpha")
		}
		if d.Program != "" || d.Applied != "" {
			strongest(d.User, "beta")
		}
	}
	if cutoff, err := s.cutoff(ctx); err != nil {
		return err
	} else if !cutoff.IsZero() {
		for sub, at := range who.created {
			if at.Before(cutoff) {
				strongest(sub, "early")
			}
		}
	}
	var stated int
	for _, sub := range sorted(why) {
		ok, err := s.once(ctx, catalog.Event{ID: "founder.eligible:" + sub, Type: "founder.eligible", Subject: sub, At: time.Now().UTC(),
			Fields: map[string]any{"reason": why[sub]}})
		if err != nil {
			return err
		}
		if ok {
			stated++
		}
	}
	if stated > 0 {
		s.base.Log.Info("reward: founding developers stated", "events", stated)
	}
	return nil
}

// cutoff is the founding cutoff staff set in the program: the instant before which a
// sign-up is early. It is zero while staff have set none, which is the state the
// program starts in; the owner sets the date.
func (s *state) cutoff(ctx context.Context) (time.Time, error) {
	p, err := s.eng.Program(ctx, program)
	if err != nil {
		return time.Time{}, err
	}
	return p.Cutoff()
}
