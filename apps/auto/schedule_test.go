package auto

import (
	"errors"
	"testing"
	"time"
)

func at(t *testing.T, s string) time.Time {
	t.Helper()
	v, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// The next run is read in the automation's own zone, so it keeps its wall-clock
// time across a daylight-saving change and skips the days it does not run on.
func TestScheduleNext(t *testing.T) {
	for _, c := range []struct {
		name  string
		s     Schedule
		after string
		want  string
	}{
		{"weekdays skip the weekend", Schedule{Kind: KindWeekdays, At: "08:00", TZ: "America/Los_Angeles"},
			"2026-10-02T17:00:00Z", "2026-10-05T15:00:00Z"},
		{"weekdays from a Saturday", Schedule{Kind: KindWeekdays, At: "08:00", TZ: "America/Los_Angeles"},
			"2026-10-03T12:00:00Z", "2026-10-05T15:00:00Z"},
		{"weekdays later the same day", Schedule{Kind: KindWeekdays, At: "08:00", TZ: "America/Los_Angeles"},
			"2026-10-05T14:59:00Z", "2026-10-05T15:00:00Z"},
		{"daily across the end of daylight time", Schedule{Kind: KindDaily, At: "08:00", TZ: "America/Los_Angeles"},
			"2026-10-31T16:00:00Z", "2026-11-01T16:00:00Z"},
		{"daily across the start of daylight time", Schedule{Kind: KindDaily, At: "08:00", TZ: "America/Los_Angeles"},
			"2027-03-13T17:00:00Z", "2027-03-14T15:00:00Z"},
		{"weekly Friday across the end of summer time", Schedule{Kind: KindWeekly, Day: "fri", At: "16:00", TZ: "Europe/London"},
			"2026-10-23T16:00:00Z", "2026-10-30T16:00:00Z"},
		{"hourly in a half-hour zone", Schedule{Kind: KindHourly, TZ: "Asia/Kolkata"},
			"2026-10-01T00:10:00Z", "2026-10-01T00:30:00Z"},
		{"hourly at a minute", Schedule{Kind: KindHourly, At: "00:15", TZ: "UTC"},
			"2026-10-01T00:20:00Z", "2026-10-01T01:15:00Z"},
		{"cron in its zone", Schedule{Kind: KindCron, Cron: "0 8 * * 1-5", TZ: "Asia/Tokyo"},
			"2026-10-02T23:30:00Z", "2026-10-04T23:00:00Z"},
		{"no zone is UTC", Schedule{Kind: KindDaily, At: "09:00"},
			"2026-10-01T09:00:00Z", "2026-10-02T09:00:00Z"},
	} {
		t.Run(c.name, func(t *testing.T) {
			s, err := normalize(c.s)
			if err != nil {
				t.Fatalf("normalize: %v", err)
			}
			got := next(compile(s), at(t, c.after))
			if want := at(t, c.want); !got.Equal(want) {
				t.Fatalf("next after %s = %s, want %s (expr %q)", c.after, got.Format(time.RFC3339), c.want, compile(s))
			}
		})
	}
}

// A time the clock skips in spring does not happen that day, and a time it
// repeats in autumn runs once. That is the engine's parser answering, and next
// uses the same one, so this pins what an automation at such a time will do.
func TestScheduleDaylightEdges(t *testing.T) {
	spring, _ := normalize(Schedule{Kind: KindDaily, At: "02:30", TZ: "America/Los_Angeles"})
	// 02:30 on 2027-03-14 does not exist in Los Angeles.
	got := next(compile(spring), at(t, "2027-03-13T11:00:00Z"))
	if got.Before(at(t, "2027-03-14T00:00:00Z")) || got.Day() == 13 {
		t.Fatalf("spring gap: next = %s", got)
	}
	t.Logf("02:30 on the spring-forward day fires at %s", got.Format(time.RFC3339))

	autumn, _ := normalize(Schedule{Kind: KindDaily, At: "01:30", TZ: "America/Los_Angeles"})
	first := next(compile(autumn), at(t, "2026-11-01T08:00:00Z"))
	if want := at(t, "2026-11-01T08:30:00Z"); !first.Equal(want) {
		t.Fatalf("autumn first = %s, want %s", first, want)
	}
	// The parser matches 01:30 again an hour later, in standard time.
	if again := next(compile(autumn), first); !again.Equal(at(t, "2026-11-01T09:30:00Z")) {
		t.Fatalf("the parser no longer matches the repeated hour (%s); daily's guard can go", again)
	}
	// What the automation reports, and what runs, is once per local date.
	if got, want := upcoming(autumn, first), at(t, "2026-11-02T09:30:00Z"); !got.Equal(want) {
		t.Fatalf("after %s the next run is %s, want %s", first, got, want)
	}
	// An hourly schedule runs every real hour through the repeat.
	hourly, _ := normalize(Schedule{Kind: KindHourly, TZ: "America/Los_Angeles"})
	if got, want := upcoming(hourly, at(t, "2026-11-01T08:00:00Z")), at(t, "2026-11-01T09:00:00Z"); !got.Equal(want) {
		t.Fatalf("hourly after the first 01:00 = %s, want %s", got, want)
	}
}

func TestScheduleCompile(t *testing.T) {
	for _, c := range []struct {
		s    Schedule
		want string
	}{
		{Schedule{Kind: KindManual, TZ: "UTC"}, ""},
		{Schedule{Kind: KindHourly, TZ: "UTC"}, "CRON_TZ=UTC 0 * * * *"},
		{Schedule{Kind: KindDaily, At: "8:05", TZ: "Europe/Paris"}, "CRON_TZ=Europe/Paris 5 8 * * *"},
		{Schedule{Kind: KindWeekdays, At: "08:00", TZ: "America/Los_Angeles"}, "CRON_TZ=America/Los_Angeles 0 8 * * 1-5"},
		{Schedule{Kind: KindWeekly, Day: "SUN", At: "23:59", TZ: "UTC"}, "CRON_TZ=UTC 59 23 * * 0"},
		{Schedule{Kind: KindCron, Cron: " 0  8 * * 1-5 ", TZ: "Asia/Tokyo"}, "CRON_TZ=Asia/Tokyo 0 8 * * 1-5"},
	} {
		s, err := normalize(c.s)
		if err != nil {
			t.Fatalf("%+v: %v", c.s, err)
		}
		if got := compile(s); got != c.want {
			t.Errorf("%+v compiles to %q, want %q", c.s, got, c.want)
		}
	}
}

// Every schedule that cannot run is refused with the reason, before anything is
// stored or armed.
func TestScheduleRefusals(t *testing.T) {
	for _, s := range []Schedule{
		{Kind: "fortnightly", TZ: "UTC"},
		{Kind: KindDaily, TZ: "UTC"},
		{Kind: KindDaily, At: "24:00", TZ: "UTC"},
		{Kind: KindDaily, At: "08:60", TZ: "UTC"},
		{Kind: KindDaily, At: "8am", TZ: "UTC"},
		{Kind: KindWeekly, At: "08:00", TZ: "UTC"},
		{Kind: KindWeekly, At: "08:00", Day: "friday", TZ: "UTC"},
		{Kind: KindCron, TZ: "UTC"},
		{Kind: KindCron, Cron: "0 8 * *", TZ: "UTC"},
		{Kind: KindCron, Cron: "0 0 8 * * 1", TZ: "UTC"},
		{Kind: KindCron, Cron: "@daily", TZ: "UTC"},
		{Kind: KindCron, Cron: "CRON_TZ=UTC 0 8 * * *", TZ: "UTC"},
		{Kind: KindDaily, At: "08:00", TZ: "Mars/Olympus"},
		{Kind: KindDaily, At: "08:00", TZ: "Local"},
	} {
		if _, err := normalize(s); !errors.As(err, new(errSchedule)) {
			t.Errorf("%+v: want an invalid schedule, got %v", s, err)
		}
	}
	// A schedule keeps only the fields its kind reads.
	s, err := normalize(Schedule{Kind: "Daily", At: "07:30", Day: "fri", Cron: "* * * * *"})
	if err != nil || s != (Schedule{Kind: KindDaily, At: "07:30", TZ: "UTC"}) {
		t.Fatalf("normalize = %+v, %v", s, err)
	}
	if s, err := normalize(Schedule{}); err != nil || s.Kind != KindManual || s.TZ != "UTC" {
		t.Fatalf("an empty schedule is manual in UTC, got %+v, %v", s, err)
	}
}

// next reads what the engine fires: a manual schedule, or an expression the
// engine's parser refuses, has no next run.
func TestScheduleNextNone(t *testing.T) {
	if !next("", time.Now()).IsZero() || !next("not cron", time.Now()).IsZero() {
		t.Fatal("no expression has no next run")
	}
}
