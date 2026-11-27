package auto

// when.go — a schedule said in words, read into the Schedule the engine fires on.
//
// "every 5 minutes", "weekdays at 8:30", "every monday at 9am", "tomorrow at
// 7pm", "in 20 minutes": the phrases people use are read here, by rule, in the
// person's own time zone, so the common case costs no model call and answers the
// same way every time. A phrase no rule reads is put to one model — whenModel —
// which answers a Schedule as JSON; what it answers is held to normalize exactly
// as a person's own schedule is, so a model can propose a schedule and never arm
// one the engine would refuse.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/cloud/client"
	aipeer "github.com/hanzoai/cloud/client/ai"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/zap-proto/zip"
)

// whenModel reads a phrase no rule does. One constant, so moving it is one edit.
const whenModel = "zen5"

// maxWhen bounds a phrase.
const maxWhen = 200

// whenIn is a schedule said in words.
type whenIn struct {
	// Text is when it runs, as a person says it: "every monday at 9am".
	Text string `json:"text"`
	// TZ is the IANA zone the words are read in. Empty is UTC.
	TZ string `json:"tz"`
}

// whenOut is the schedule the words say.
type whenOut struct {
	// Schedule is the schedule, ready to send as an automation's `schedule`.
	Schedule Schedule `json:"schedule"`
	// Says is the schedule said back in plain words.
	Says string `json:"says"`
	// Next is when it would first run, RFC 3339 UTC; null when it runs only on
	// demand.
	Next *string `json:"next"`
}

// Cron reads a schedule said in words — "every 5 minutes", "weekdays at 8:30",
// "every monday at 9am", "tomorrow at 7pm", "in 20 minutes", or a five-field
// cron expression — in your time zone, and answers the schedule an automation
// takes, said back in plain words, with when it would first run. Nothing is
// created: send the schedule to POST /v1/auto/automations. A phrase that no rule
// reads is read by a model; a phrase that names no schedule answers 422.
//
// Example: {"text": "every monday at 9am", "tz": "America/Los_Angeles"}
func (o ops) cron(ctx context.Context, in *whenIn) (*whenOut, error) {
	if _, err := principal.Acting(ctx); err != nil {
		return nil, err
	}
	text := strings.TrimSpace(in.Text)
	if text == "" || len(text) > maxWhen {
		return nil, invalid("text is when it runs, in at most 200 characters")
	}
	tz := strings.TrimSpace(in.TZ)
	if tz == "" {
		tz = "UTC"
	}
	loc, err := time.LoadLocation(tz)
	if err != nil || tz == "Local" {
		return nil, &zip.HTTPError{Status: http.StatusUnprocessableEntity, Code: "invalid_schedule", Msg: "tz is not an IANA time zone"}
	}
	now := time.Now()
	s, ok := read(text, now.In(loc))
	if !ok {
		if s, err = askModel(ctx, text, tz, now.In(loc)); err != nil {
			return nil, err
		}
	}
	s.TZ = tz
	if s, err = normalize(s); err != nil {
		return nil, &zip.HTTPError{Status: http.StatusUnprocessableEntity, Code: "invalid_schedule", Msg: err.Error()}
	}
	if err := ahead(s, now); err != nil {
		return nil, err
	}
	out := &whenOut{Schedule: s, Says: says(s)}
	if t := upcoming(s, now); !t.IsZero() {
		at := t.Format(time.RFC3339)
		out.Next = &at
	}
	return out, nil
}

var (
	reEvery   = regexp.MustCompile(`^every\s+(\d{1,3})\s*(m|min|mins|minute|minutes|h|hr|hrs|hour|hours)$`)
	reIn      = regexp.MustCompile(`^in\s+(\d{1,3})\s*(m|min|mins|minute|minutes|h|hr|hrs|hour|hours|d|day|days)$`)
	reClock   = regexp.MustCompile(`(?:\bat\s+)?\b(\d{1,2})(?::(\d{2}))?\s*(am|pm)?\b`)
	reMonthly = regexp.MustCompile(`^(?:every month|monthly)(?:\s+on)?(?:\s+the)?\s+(\d{1,2})(?:st|nd|rd|th)?\b`)
	dayWords  = map[string]string{
		"monday": "mon", "mondays": "mon", "mon": "mon", "tuesday": "tue", "tuesdays": "tue", "tue": "tue", "tues": "tue",
		"wednesday": "wed", "wednesdays": "wed", "wed": "wed", "thursday": "thu", "thursdays": "thu", "thu": "thu", "thurs": "thu",
		"friday": "fri", "fridays": "fri", "fri": "fri", "saturday": "sat", "saturdays": "sat", "sat": "sat",
		"sunday": "sun", "sundays": "sun", "sun": "sun",
	}
	// Parts of a day said instead of a time.
	parts = map[string]string{"morning": "08:00", "noon": "12:00", "afternoon": "14:00", "evening": "18:00", "night": "21:00", "midnight": "00:00"}
)

// read applies the rules. It answers false for a phrase none of them reads.
func read(text string, now time.Time) (Schedule, bool) {
	t := strings.ToLower(strings.Join(strings.Fields(strings.Trim(text, ".!? ")), " "))
	if _, err := userCron.Parse(t); err == nil && len(strings.Fields(t)) == 5 {
		return Schedule{Kind: KindCron, Cron: t}, true
	}
	switch t {
	case "now", "right now", "once", "run once", "one time":
		return Schedule{Kind: KindOnce}, true
	case "manually", "manual", "on demand", "when i run it", "never":
		return Schedule{Kind: KindManual}, true
	case "every minute":
		return Schedule{Kind: KindCron, Cron: "* * * * *"}, true
	case "hourly", "every hour":
		return Schedule{Kind: KindHourly}, true
	}
	if m := reEvery.FindStringSubmatch(t); m != nil {
		n, _ := strconv.Atoi(m[1])
		if strings.HasPrefix(m[2], "h") {
			if n < 1 || n > 23 {
				return Schedule{}, false
			}
			return Schedule{Kind: KindCron, Cron: fmt.Sprintf("0 */%d * * *", n)}, true
		}
		if n < 1 || n > 59 {
			return Schedule{}, false
		}
		if n == 1 {
			return Schedule{Kind: KindCron, Cron: "* * * * *"}, true
		}
		return Schedule{Kind: KindCron, Cron: fmt.Sprintf("*/%d * * * *", n)}, true
	}
	if m := reIn.FindStringSubmatch(t); m != nil {
		n, _ := strconv.Atoi(m[1])
		d := time.Duration(n) * time.Minute
		switch {
		case strings.HasPrefix(m[2], "h"):
			d = time.Duration(n) * time.Hour
		case strings.HasPrefix(m[2], "d"):
			d = time.Duration(n) * 24 * time.Hour
		}
		// A minute's grain: the engine fires on the minute, so the moment rounds up.
		at := now.Add(d).Add(time.Minute - time.Nanosecond).Truncate(time.Minute)
		return Schedule{Kind: KindOnce, At: at.Format(onceLayout)}, true
	}
	at, timed := clockOf(t)
	if m := reMonthly.FindStringSubmatch(t); m != nil {
		day, _ := strconv.Atoi(m[1])
		if day < 1 || day > 31 {
			return Schedule{}, false
		}
		h, mi := split(cmpStr(at, "09:00"))
		return Schedule{Kind: KindCron, Cron: fmt.Sprintf("%d %d %d * *", mi, h, day)}, true
	}
	days := daysOf(t)
	switch {
	case strings.Contains(t, "weekday"):
		return Schedule{Kind: KindWeekdays, At: cmpStr(at, "09:00")}, true
	case strings.Contains(t, "weekend"):
		h, mi := split(cmpStr(at, "09:00"))
		return Schedule{Kind: KindCron, Cron: fmt.Sprintf("%d %d * * 0,6", mi, h)}, true
	case len(days) == 1:
		return Schedule{Kind: KindWeekly, Day: days[0], At: cmpStr(at, "09:00")}, true
	case len(days) > 1:
		h, mi := split(cmpStr(at, "09:00"))
		nums := make([]string, 0, len(days))
		for _, d := range days {
			nums = append(nums, strconv.Itoa(weekdays[d]))
		}
		return Schedule{Kind: KindCron, Cron: fmt.Sprintf("%d %d * * %s", mi, h, strings.Join(nums, ","))}, true
	case strings.HasPrefix(t, "every day") || strings.HasPrefix(t, "daily") || strings.HasPrefix(t, "each day") ||
		strings.HasPrefix(t, "every morning") || strings.HasPrefix(t, "every evening") ||
		strings.HasPrefix(t, "every night") || strings.HasPrefix(t, "every afternoon"):
		return Schedule{Kind: KindDaily, At: cmpStr(at, "09:00")}, true
	case strings.HasPrefix(t, "tomorrow") || strings.HasPrefix(t, "today") || strings.HasPrefix(t, "tonight") ||
		((strings.HasPrefix(t, "at ") || strings.HasPrefix(t, "this ")) && timed):
		day := now
		if strings.HasPrefix(t, "tomorrow") {
			day = now.AddDate(0, 0, 1)
		}
		clock := cmpStr(at, "09:00")
		if strings.HasPrefix(t, "tonight") && !timed {
			clock = "21:00"
		}
		h, mi := split(clock)
		when := time.Date(day.Year(), day.Month(), day.Day(), h, mi, 0, 0, now.Location())
		// "at 7pm" said after seven means tomorrow's.
		if !strings.HasPrefix(t, "today") && !when.After(now) {
			when = when.AddDate(0, 0, 1)
		}
		return Schedule{Kind: KindOnce, At: when.Format(onceLayout)}, true
	}
	return Schedule{}, false
}

// clockOf finds the time of day a phrase names, as HH:MM, and whether it named one.
func clockOf(t string) (string, bool) {
	for _, m := range reClock.FindAllStringSubmatch(t, -1) {
		h, _ := strconv.Atoi(m[1])
		mi := 0
		if m[2] != "" {
			mi, _ = strconv.Atoi(m[2])
		}
		switch m[3] {
		case "pm":
			if h < 12 {
				h += 12
			}
		case "am":
			if h == 12 {
				h = 0
			}
		default:
			// A bare number is a time only after "at" or with minutes: "every 5" is not.
			if m[2] == "" && !strings.Contains(m[0], "at") {
				continue
			}
		}
		if h > 23 || mi > 59 {
			continue
		}
		return fmt.Sprintf("%02d:%02d", h, mi), true
	}
	// A part of the day stands in for a time only when none is said.
	for _, word := range []string{"midnight", "morning", "noon", "afternoon", "evening", "night"} {
		if strings.Contains(t, word) {
			return parts[word], true
		}
	}
	return "", false
}

// daysOf is the weekdays a phrase names, in week order, each once.
func daysOf(t string) []string {
	seen := map[string]bool{}
	for _, w := range strings.FieldsFunc(t, func(r rune) bool { return r == ' ' || r == ',' || r == '/' }) {
		if d, ok := dayWords[w]; ok {
			seen[d] = true
		}
	}
	var out []string
	for _, d := range []string{"mon", "tue", "wed", "thu", "fri", "sat", "sun"} {
		if seen[d] {
			out = append(out, d)
		}
	}
	return out
}

func split(at string) (int, int) {
	h, m, _ := clock(at)
	return h, m
}

func cmpStr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// askModel puts a phrase no rule reads to whenModel, for the caller, and reads the
// Schedule it answers.
func askModel(ctx context.Context, text, tz string, now time.Time) (Schedule, error) {
	prompt := "Turn a schedule said in words into JSON with exactly these keys: " +
		`"kind" (one of manual, hourly, daily, weekdays, weekly, cron, once), ` +
		`"at" (HH:MM for daily, weekdays and weekly; YYYY-MM-DDTHH:MM for once), ` +
		`"day" (mon..sun, for weekly) and "cron" (five fields, for cron). ` +
		"Use cron only when no other kind says it. Answer the JSON object and nothing else. " +
		"Now is " + now.Format("Monday 2006-01-02 15:04") + " in " + tz + ".\n\nSchedule: " + text
	req, _ := json.Marshal(map[string]any{
		"model":           whenModel,
		"messages":        []map[string]string{{"role": "user", "content": prompt}},
		"temperature":     0,
		"max_tokens":      200,
		"response_format": map[string]string{"type": "json_object"},
	})
	got, err := aipeer.AIChat(ctx, &client.ChatIn{Request: req})
	if err != nil || got == nil || got.Status != http.StatusOK {
		return Schedule{}, &zip.HTTPError{Status: http.StatusUnprocessableEntity, Code: "invalid_schedule",
			Msg: "invalid schedule: no rule reads " + strconvQuote(text) + ", and it could not be read for you; say it as \"every monday at 9am\" or a cron expression"}
	}
	var chat struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if json.Unmarshal(got.Body, &chat) != nil || len(chat.Choices) == 0 {
		return Schedule{}, &zip.HTTPError{Status: http.StatusUnprocessableEntity, Code: "invalid_schedule", Msg: "invalid schedule: " + strconvQuote(text) + " names no schedule"}
	}
	body := chat.Choices[0].Message.Content
	if i, j := strings.Index(body, "{"), strings.LastIndex(body, "}"); i >= 0 && j > i {
		body = body[i : j+1]
	}
	var s Schedule
	if json.Unmarshal([]byte(body), &s) != nil || s.Kind == "" {
		return Schedule{}, &zip.HTTPError{Status: http.StatusUnprocessableEntity, Code: "invalid_schedule", Msg: "invalid schedule: " + strconvQuote(text) + " names no schedule"}
	}
	return Schedule{Kind: s.Kind, At: s.At, Day: s.Day, Cron: s.Cron}, nil
}

func strconvQuote(s string) string { return strconv.Quote(s) }

// says is a schedule in plain words.
func says(s Schedule) string {
	day := map[string]string{"mon": "Mondays", "tue": "Tuesdays", "wed": "Wednesdays", "thu": "Thursdays", "fri": "Fridays", "sat": "Saturdays", "sun": "Sundays"}
	switch s.Kind {
	case KindManual:
		return "When you run it"
	case KindHourly:
		if s.At == "" || s.At == "00:00" {
			return "Every hour"
		}
		return "Every hour at :" + s.At[3:]
	case KindDaily:
		return "Every day at " + s.At
	case KindWeekdays:
		return "Weekdays at " + s.At
	case KindWeekly:
		return day[s.Day] + " at " + s.At
	case KindOnce:
		if t, ok := moment(s); ok {
			return "Once, " + t.Format("Mon Jan 2 at 15:04")
		}
		return "Once, now"
	}
	f := strings.Fields(s.Cron)
	if len(f) == 5 && f[2] == "*" && f[3] == "*" && f[4] == "*" {
		if f[1] == "*" {
			if f[0] == "*" {
				return "Every minute"
			}
			if n, ok := strings.CutPrefix(f[0], "*/"); ok {
				return "Every " + n + " minutes"
			}
		}
		if n, ok := strings.CutPrefix(f[1], "*/"); ok && f[0] == "0" {
			return "Every " + n + " hours"
		}
	}
	return "On the cron schedule " + s.Cron
}
