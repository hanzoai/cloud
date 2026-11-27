package auto

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
	"github.com/zap-proto/zip"
)

// schedule.go — when an automation runs, said the way a person says it.
//
// A Schedule is the person's words: daily at 08:00, weekdays, Fridays at 16:00,
// or a cron expression of their own, always in their own time zone. compile turns
// those words into the one expression the durable engine fires on, prefixed
// CRON_TZ=<zone> so the ENGINE keeps the wall-clock time across a daylight-saving
// change: 08:00 in Los Angeles is 15:00 UTC in summer and 16:00 in winter, and a
// bare UTC cron would drift an hour twice a year.
//
// next reads that same expression with the same parser the engine fires it with
// (hanzoai/tasks engine.go cronParser), so the time a page shows is the time the
// run starts, not a second opinion about it.

// Schedule kinds.
const (
	KindManual   = "manual"
	KindHourly   = "hourly"
	KindDaily    = "daily"
	KindWeekdays = "weekdays"
	KindWeekly   = "weekly"
	KindCron     = "cron"
	// KindOnce runs one time, at At — a local date and time — or, with none, as
	// it is made; then it switches itself off.
	KindOnce = "once"
)

// onceLayout is how a once schedule says its moment: a local date and time.
const onceLayout = "2006-01-02T15:04"

// Schedule is when an automation runs.
type Schedule struct {
	// Kind is manual, hourly, daily, weekdays, weekly, cron or once. Manual runs
	// only when asked; once runs one time and then switches itself off.
	Kind string `json:"kind"`
	// At is the local time, HH:MM on a 24-hour clock, for daily, weekdays and
	// weekly. Hourly takes its minute, and runs on the hour without one. Once
	// takes a local date and time, YYYY-MM-DDTHH:MM, and runs now without one.
	At string `json:"at,omitempty"`
	// Day is the weekday a weekly schedule runs on: mon, tue, wed, thu, fri, sat
	// or sun.
	Day string `json:"day,omitempty"`
	// Cron is a five-field cron expression (minute hour day-of-month month
	// day-of-week), for kind cron only, read in TZ.
	Cron string `json:"cron,omitempty"`
	// TZ is the IANA time zone every time here is read in. Empty is UTC.
	TZ string `json:"tz,omitempty"`
}

// weekdays maps a schedule's day to cron's day-of-week number.
var weekdays = map[string]int{"sun": 0, "mon": 1, "tue": 2, "wed": 3, "thu": 4, "fri": 5, "sat": 6}

// userCron parses what a person may write as a cron expression: exactly five
// fields and nothing else. No seconds, no @descriptors and no TZ prefix — the
// zone is TZ's to say, and a prefix inside the expression would be a second one.
var userCron = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)

// engineCron is the durable engine's own parser, option for option, so next
// answers what the engine will do.
var engineCron = cron.NewParser(cron.SecondOptional | cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)

// errSchedule is an invalid schedule; the handlers answer it 422.
type errSchedule struct{ why string }

func (e errSchedule) Error() string { return "invalid schedule: " + e.why }

func badSchedule(format string, a ...any) error { return errSchedule{why: fmt.Sprintf(format, a...)} }

// normalize validates s and returns it in canonical form: lowercase kind and
// day, the zone defaulted to UTC, and only the fields its kind reads.
func normalize(s Schedule) (Schedule, error) {
	out := Schedule{Kind: strings.ToLower(strings.TrimSpace(s.Kind)), TZ: strings.TrimSpace(s.TZ)}
	if out.Kind == "" {
		out.Kind = KindManual
	}
	if out.TZ == "" {
		out.TZ = "UTC"
	}
	// "Local" is the server's zone, which is nobody's.
	if out.TZ == "Local" || len(out.TZ) > 64 {
		return Schedule{}, badSchedule("tz %q is not an IANA time zone", s.TZ)
	}
	if _, err := time.LoadLocation(out.TZ); err != nil {
		return Schedule{}, badSchedule("tz %q is not an IANA time zone", s.TZ)
	}
	at := strings.TrimSpace(s.At)
	switch out.Kind {
	case KindManual:
	case KindHourly:
		if at != "" {
			_, m, err := clock(at)
			if err != nil {
				return Schedule{}, err
			}
			out.At = fmt.Sprintf("00:%02d", m)
		}
	case KindDaily, KindWeekdays, KindWeekly:
		h, m, err := clock(at)
		if err != nil {
			return Schedule{}, err
		}
		out.At = fmt.Sprintf("%02d:%02d", h, m)
		if out.Kind == KindWeekly {
			out.Day = strings.ToLower(strings.TrimSpace(s.Day))
			if _, ok := weekdays[out.Day]; !ok {
				return Schedule{}, badSchedule("day %q is not one of mon, tue, wed, thu, fri, sat, sun", s.Day)
			}
		}
	case KindOnce:
		if at != "" {
			loc, _ := time.LoadLocation(out.TZ)
			t, err := time.ParseInLocation(onceLayout, at, loc)
			if err != nil {
				return Schedule{}, badSchedule("at %q is not YYYY-MM-DDTHH:MM", s.At)
			}
			out.At = t.Format(onceLayout)
		}
	case KindCron:
		out.Cron = strings.Join(strings.Fields(s.Cron), " ")
		if out.Cron == "" {
			return Schedule{}, badSchedule("cron is required for kind cron")
		}
		if strings.Contains(out.Cron, "TZ=") {
			return Schedule{}, badSchedule("cron %q names a zone; the zone is tz", s.Cron)
		}
		if _, err := userCron.Parse(out.Cron); err != nil {
			return Schedule{}, badSchedule("cron %q: %v", s.Cron, err)
		}
	default:
		return Schedule{}, badSchedule("kind %q is not one of manual, hourly, daily, weekdays, weekly, cron, once", s.Kind)
	}
	return out, nil
}

// checkCron refuses an armed expression the engine should not fire: anything but
// five fields — so nothing more often than once a minute, no seconds field and no
// @every — optionally led by one CRON_TZ=<IANA zone>. Every flow's schedule
// passes it, an automation's and a builder's alike.
func checkCron(expr string) error {
	expr = strings.Join(strings.Fields(expr), " ")
	if zone, rest, ok := strings.Cut(expr, " "); ok && strings.HasPrefix(zone, "CRON_TZ=") {
		tz := strings.TrimPrefix(zone, "CRON_TZ=")
		if _, err := time.LoadLocation(tz); err != nil || tz == "" || tz == "Local" {
			return badSchedule("cron %q names %q, which is not an IANA time zone", expr, tz)
		}
		expr = rest
	}
	if strings.Contains(expr, "TZ=") {
		return badSchedule("cron %q names a zone somewhere other than its start", expr)
	}
	if _, err := userCron.Parse(expr); err != nil {
		return badSchedule("cron %q is not five fields, once a minute at most: %v", expr, err)
	}
	return nil
}

// clock reads HH:MM on a 24-hour clock.
func clock(at string) (hour, minute int, err error) {
	h, m, ok := strings.Cut(at, ":")
	if !ok || len(m) != 2 || h == "" || len(h) > 2 {
		return 0, 0, badSchedule("at %q is not HH:MM", at)
	}
	if hour, err = strconv.Atoi(h); err != nil || hour < 0 || hour > 23 {
		return 0, 0, badSchedule("at %q is not HH:MM", at)
	}
	if minute, err = strconv.Atoi(m); err != nil || minute < 0 || minute > 59 {
		return 0, 0, badSchedule("at %q is not HH:MM", at)
	}
	return hour, minute, nil
}

// compile is the engine expression for a normalized schedule, or "" for a
// manual one, which arms nothing.
func compile(s Schedule) string {
	var expr string
	switch s.Kind {
	case KindHourly:
		m := 0
		if s.At != "" {
			_, m, _ = clock(s.At)
		}
		expr = fmt.Sprintf("%d * * * *", m)
	case KindDaily, KindWeekdays, KindWeekly:
		h, m, _ := clock(s.At)
		dow := "*"
		switch s.Kind {
		case KindWeekdays:
			dow = "1-5"
		case KindWeekly:
			dow = strconv.Itoa(weekdays[s.Day])
		}
		expr = fmt.Sprintf("%d %d * * %s", m, h, dow)
	case KindCron:
		expr = s.Cron
	case KindOnce:
		t, ok := moment(s)
		if !ok {
			return ""
		}
		// The engine's clock has no year, so this names that minute of every
		// year; the run it starts switches the automation off (StartAgentActivity),
		// and nothing reaches the second.
		expr = fmt.Sprintf("%d %d %d %d *", t.Minute(), t.Hour(), t.Day(), int(t.Month()))
	default:
		return ""
	}
	return "CRON_TZ=" + s.TZ + " " + expr
}

// moment is when a once schedule with a time runs.
func moment(s Schedule) (time.Time, bool) {
	if s.Kind != KindOnce || s.At == "" {
		return time.Time{}, false
	}
	loc, err := time.LoadLocation(s.TZ)
	if err != nil {
		return time.Time{}, false
	}
	t, err := time.ParseInLocation(onceLayout, s.At, loc)
	return t, err == nil
}

// ahead refuses a once schedule whose moment has passed. It is asked when a
// schedule is written, never when one is read: a once that already ran is
// still a readable automation.
func ahead(s Schedule, now time.Time) error {
	if t, ok := moment(s); ok && !t.After(now) {
		return &zip.HTTPError{Status: http.StatusUnprocessableEntity, Code: "invalid_schedule",
			Msg: "invalid schedule: " + s.At + " in " + s.TZ + " has passed"}
	}
	return nil
}

// daily reports whether s runs at most once a local day, and the zone that day
// is read in. A daily, weekdays or weekly time that falls in the hour a zone
// repeats when its clocks go back is a time the engine's parser matches twice
// that night; a schedule like that fires once per local date, and the second
// match is not a tick (recordRunStart).
func daily(s Schedule) (bool, *time.Location) {
	switch s.Kind {
	case KindDaily, KindWeekdays, KindWeekly:
		loc, err := time.LoadLocation(s.TZ)
		if err != nil {
			return false, nil
		}
		return true, loc
	}
	return false, nil
}

// next is when a compiled expression fires after t, or the zero time for none.
func next(expr string, after time.Time) time.Time {
	if expr == "" {
		return time.Time{}
	}
	sched, err := engineCron.Parse(expr)
	if err != nil {
		return time.Time{}
	}
	return sched.Next(after).UTC()
}

// last is a compiled expression's last tick at or before at, looking back as far as
// a year for one that fires that rarely.
func last(expr string, at time.Time) (time.Time, bool) {
	if expr == "" {
		return time.Time{}, false
	}
	sched, err := engineCron.Parse(expr)
	if err != nil {
		return time.Time{}, false
	}
	for _, back := range []time.Duration{time.Hour, 48 * time.Hour, 400 * 24 * time.Hour} {
		var found time.Time
		for t, n := at.Add(-back), 0; n < 100_000; n++ {
			nx := sched.Next(t)
			if nx.IsZero() || nx.After(at) {
				break
			}
			found, t = nx, nx
		}
		if !found.IsZero() {
			return found, true
		}
	}
	return time.Time{}, false
}

// upcoming is when a schedule next runs after now: next, less the second match
// of a repeated local hour, which daily drops.
func upcoming(s Schedule, now time.Time) time.Time {
	if s.Kind == KindOnce {
		if t, ok := moment(s); ok && t.After(now) {
			return t.UTC()
		}
		return time.Time{}
	}
	expr := compile(s)
	t := next(expr, now)
	once, loc := daily(s)
	if !once || t.IsZero() {
		return t
	}
	for range 2 {
		day := t.In(loc)
		midnight := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, loc)
		if first := next(expr, midnight.Add(-time.Second)); !first.Before(t) {
			return t
		}
		t = next(expr, t)
	}
	return t
}
