package auto

import (
	"bufio"
	"bytes"
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/zap-proto/zip"
)

// keeper stands in for the Library, keeping what each run filed.
type keeper struct {
	mu      sync.Mutex
	kept    []client.LibraryPutIn
	callers []zip.Caller
}

func library(t *testing.T) *keeper {
	t.Helper()
	k := &keeper{}
	orig := libraryPut
	libraryPut = func(ctx context.Context, in *client.LibraryPutIn) (*client.LibraryItem, error) {
		k.mu.Lock()
		defer k.mu.Unlock()
		k.kept = append(k.kept, *in)
		k.callers = append(k.callers, zip.CallerOf(ctx))
		return &client.LibraryItem{ID: "lib_" + in.Key, Kind: in.Kind, Source: in.Source}, nil
	}
	t.Cleanup(func() { libraryPut = orig })
	return k
}

// A scheduled Chat prompt's run files its answer in its person's Library, as a
// Chat document, and the run names the item it made — in its own record and as
// a job.
func TestARunsAnswerIsKeptInItsPersonsLibrary(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		app := newApp(t)
		engineFor(ctx, t, "acme")
		devPlane(t, client.TurnEvents{Next: 4, Outcome: client.TurnCompleted, Events: []client.TurnEvent{
			{Seq: 4, Kind: client.TurnAnswer, Text: "## Monday\n\nThree things moved."},
		}})
		k := library(t)
		a := decode[Automation](t, askAs(t, app, http.MethodPost, "/v1/auto/automations", "acme", "alice", map[string]any{
			"name": "Weekly summary", "instructions": "Summarize my week.", "source": "chat",
			"schedule": map[string]any{"kind": "cron", "cron": "*/5 * * * *"},
		}))
		if a.Source != "chat" {
			t.Fatalf("source = %q", a.Source)
		}
		r := askAs(t, app, http.MethodPost, "/v1/auto/automations/"+a.ID+"/run", "acme", "alice", nil)
		run := until(t, "acme", decode[runStarted](t, r).Run.ID)
		k.mu.Lock()
		if len(k.kept) != 1 {
			k.mu.Unlock()
			t.Fatalf("kept %d items", len(k.kept))
		}
		in, who := k.kept[0], k.callers[0]
		k.mu.Unlock()
		if in.Source != "chat" || in.Kind != "document" || in.Mime != "text/markdown" || string(in.Data) != "## Monday\n\nThree things moved." ||
			in.Key != run.ID || in.Ref != run.ID || in.Origin != "/dev/-/automations/"+a.ID || !strings.HasPrefix(in.Name, "Weekly summary ") {
			t.Fatalf("kept %+v", in)
		}
		if who.Org != "acme" || who.User != "alice" {
			t.Fatalf("kept as %+v, want alice of acme", who)
		}
		runs := decode[automationRunPage](t, askAs(t, app, http.MethodGet, "/v1/auto/automations/"+a.ID+"/runs", "acme", "alice", nil))
		if len(runs.Data) != 1 || len(runs.Data[0].Outputs) != 1 || runs.Data[0].Outputs[0] != "lib_"+run.ID {
			t.Fatalf("runs = %+v", runs.Data)
		}
		job := decode[client.Job](t, askAs(t, app, http.MethodGet, "/v1/auto/jobs/"+run.ID, "acme", "alice", nil))
		if job.State != StateSucceeded || job.Product != "chat" || job.Kind != "automation" || len(job.Outputs) != 1 ||
			job.Automation == nil || *job.Automation != a.ID || job.Session == nil || job.Finished == nil {
			t.Fatalf("job = %+v", job)
		}
		if list := decode[automationPage](t, askAs(t, app, http.MethodGet, "/v1/auto/automations?source=dev", "acme", "alice", nil)); len(list.Data) != 0 {
			t.Fatalf("source=dev lists a chat automation: %+v", list.Data)
		}
	})
}

// A product records its background run as the person whose call it is; Scheduled
// lists it to them alone, and nobody else can move it.
func TestATrackedJobIsItsPersons(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := newApp(t)
		o := ops{s: mounted}
		alice := zip.WithCaller(context.Background(), zip.Caller{Org: "acme", User: "alice"})
		half := 0.5
		cost := int64(42)
		j, err := o.trackJob(alice, &client.JobTrackIn{Product: "studio", Kind: "render", Title: "Poster", Ref: "run_s1",
			Origin: "/studio?run=run_s1", State: StateRunning, Progress: &half})
		if err != nil || !strings.HasPrefix(j.ID, "job_") || j.State != StateRunning || j.Finished != nil {
			t.Fatalf("track: %v %+v", err, j)
		}
		again, err := o.trackJob(alice, &client.JobTrackIn{Product: "studio", Kind: "render", Title: "Poster", Ref: "run_s1",
			State: StateSucceeded, Outputs: []string{"lib_a"}, Cost: &cost})
		if err != nil || again.ID != j.ID || again.Finished == nil || again.Cost == nil || *again.Cost != 42 || len(again.Outputs) != 1 {
			t.Fatalf("update: %v %+v", err, again)
		}
		bob := zip.WithCaller(context.Background(), zip.Caller{Org: "acme", User: "bob"})
		if _, err := o.trackJob(bob, &client.JobTrackIn{Product: "studio", Ref: "run_s1", State: StateCancelled}); err == nil {
			t.Fatal("bob moved alice's job")
		}
		other := zip.WithCaller(context.Background(), zip.Caller{Org: "globex", User: "alice"})
		if g, err := o.trackJob(other, &client.JobTrackIn{Product: "studio", Ref: "run_s1", State: StateScheduled}); err != nil || g.ID == j.ID {
			t.Fatalf("another org's same ref is its own job: %v %+v", err, g)
		}
		for _, bad := range []client.JobTrackIn{
			{Product: "elsewhere", Ref: "r", State: StateRunning},
			{Product: "studio", Ref: "r", State: "done"},
			{Product: "studio", Ref: "r", State: StateRunning, Origin: "https://evil.test"},
			{Product: "studio", Ref: "r", State: StateRunning, Outputs: []string{"not-a-lib-id"}},
		} {
			if _, err := o.trackJob(alice, &bad); err == nil {
				t.Errorf("tracked %+v", bad)
			}
		}
		if _, err := o.trackJob(context.Background(), &client.JobTrackIn{Product: "studio", Ref: "r2", State: StateRunning}); err == nil {
			t.Fatal("a call naming nobody tracked a job")
		}

		mine := decode[jobPage](t, askAs(t, app, http.MethodGet, "/v1/auto/jobs?product=studio", "acme", "alice", nil))
		if len(mine.Data) != 1 || mine.Data[0].ID != j.ID || mine.Data[0].State != StateSucceeded {
			t.Fatalf("alice's jobs = %+v", mine.Data)
		}
		if theirs := decode[jobPage](t, askAs(t, app, http.MethodGet, "/v1/auto/jobs", "acme", "bob", nil)); len(theirs.Data) != 0 {
			t.Fatalf("bob lists alice's job: %+v", theirs.Data)
		}
		if r := askAs(t, app, http.MethodGet, "/v1/auto/jobs/"+j.ID, "acme", "bob", nil); r.Code != http.StatusNotFound {
			t.Fatalf("bob reads alice's job = %d", r.Code)
		}
		if r := askAs(t, app, http.MethodGet, "/v1/auto/jobs/"+j.ID, "globex", "alice", nil); r.Code != http.StatusNotFound {
			t.Fatalf("another org reads the job = %d", r.Code)
		}
	})
}

// Once runs one time: with no time it runs as it is made and is off; with one,
// it is armed for that minute and a passed moment is refused.
func TestOnceRunsOneTime(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		app := newApp(t)
		engineFor(ctx, t, "acme")
		devPlane(t, client.TurnEvents{Next: 1, Outcome: client.TurnCompleted, Events: []client.TurnEvent{{Seq: 1, Kind: client.TurnAnswer, Text: "Done."}}})
		library(t)
		now := decode[Automation](t, ask(t, app, http.MethodPost, "/v1/auto/automations", "acme", map[string]any{
			"name": "Background job", "instructions": "Refactor the parser.", "schedule": map[string]any{"kind": "once"},
		}))
		if now.Last == nil {
			t.Fatalf("once with no time did not run: %+v", now)
		}
		until(t, "acme", now.Last.ID)
		if got := decode[Automation](t, ask(t, app, http.MethodGet, "/v1/auto/automations/"+now.ID, "acme", nil)); got.Enabled || got.Next != nil {
			t.Fatalf("a once that ran is still on: %+v", got)
		}

		loc, _ := time.LoadLocation("America/Los_Angeles")
		at := time.Now().In(loc).Add(48 * time.Hour).Format(onceLayout)
		later := decode[Automation](t, ask(t, app, http.MethodPost, "/v1/auto/automations", "acme", map[string]any{
			"name": "Later", "instructions": "x", "schedule": map[string]any{"kind": "once", "at": at, "tz": "America/Los_Angeles"},
		}))
		if !later.Enabled || later.Next == nil || later.Last != nil {
			t.Fatalf("once later = %+v", later)
		}
		want, _ := time.ParseInLocation(onceLayout, at, loc)
		if *later.Next != want.UTC().Format(time.RFC3339) {
			t.Fatalf("next = %s, want %s", *later.Next, want.UTC().Format(time.RFC3339))
		}
		past := time.Now().In(loc).Add(-time.Hour).Format(onceLayout)
		if r := ask(t, app, http.MethodPost, "/v1/auto/automations", "acme", map[string]any{
			"name": "Past", "instructions": "x", "schedule": map[string]any{"kind": "once", "at": past, "tz": "America/Los_Angeles"},
		}); r.Code != http.StatusUnprocessableEntity {
			t.Fatalf("a passed moment = %d %s", r.Code, r.Body)
		}
	})
}

// The phrases people use read by rule, in the person's zone, and say themselves
// back.
func TestASchedulesWordsAreRead(t *testing.T) {
	loc, _ := time.LoadLocation("America/New_York")
	now := time.Date(2026, 10, 16, 15, 30, 0, 0, loc) // a Friday afternoon
	for text, want := range map[string]Schedule{
		"every 5 minutes":          {Kind: KindCron, Cron: "*/5 * * * *"},
		"Every minute":             {Kind: KindCron, Cron: "* * * * *"},
		"every 2 hours":            {Kind: KindCron, Cron: "0 */2 * * *"},
		"hourly":                   {Kind: KindHourly},
		"every day at 9am":         {Kind: KindDaily, At: "09:00"},
		"daily at 18:30":           {Kind: KindDaily, At: "18:30"},
		"every morning":            {Kind: KindDaily, At: "08:00"},
		"every night at 10pm":      {Kind: KindDaily, At: "22:00"},
		"weekdays at 8:30":         {Kind: KindWeekdays, At: "08:30"},
		"every Monday at 9am":      {Kind: KindWeekly, Day: "mon", At: "09:00"},
		"mondays":                  {Kind: KindWeekly, Day: "mon", At: "09:00"},
		"every mon and wed at 7pm": {Kind: KindCron, Cron: "0 19 * * 1,3"},
		"every weekend at 10am":    {Kind: KindCron, Cron: "0 10 * * 0,6"},
		"every month on the 1st":   {Kind: KindCron, Cron: "0 9 1 * *"},
		"tomorrow at 7pm":          {Kind: KindOnce, At: "2026-10-17T19:00"},
		"today at 5pm":             {Kind: KindOnce, At: "2026-10-16T17:00"},
		"at 9am":                   {Kind: KindOnce, At: "2026-10-17T09:00"},
		"in 20 minutes":            {Kind: KindOnce, At: "2026-10-16T15:50"},
		"now":                      {Kind: KindOnce},
		"manually":                 {Kind: KindManual},
		"*/15 9-17 * * 1-5":        {Kind: KindCron, Cron: "*/15 9-17 * * 1-5"},
	} {
		got, ok := read(text, now)
		if !ok || got != want {
			t.Errorf("read(%q) = %+v %v, want %+v", text, got, ok, want)
		}
	}
	if _, ok := read("whenever the moon is full", now); ok {
		t.Error("a phrase no rule reads was read")
	}
	for s, want := range map[Schedule]string{
		{Kind: KindCron, Cron: "*/5 * * * *"}:       "Every 5 minutes",
		{Kind: KindWeekly, Day: "mon", At: "09:00"}: "Mondays at 09:00",
		{Kind: KindWeekdays, At: "08:30"}:           "Weekdays at 08:30",
		{Kind: KindManual}:                          "When you run it",
		{Kind: KindOnce}:                            "Once, now",
	} {
		if got := says(s); got != want {
			t.Errorf("says(%+v) = %q, want %q", s, got, want)
		}
	}
}

// POST /v1/auto/cron answers the schedule an automation takes, said back, with
// when it first runs; it creates nothing.
func TestCronAnswersASchedule(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := newApp(t)
		out := decode[whenOut](t, ask(t, app, http.MethodPost, "/v1/auto/cron", "acme", map[string]any{"text": "every 5 minutes", "tz": "Europe/London"}))
		if out.Schedule != (Schedule{Kind: KindCron, Cron: "*/5 * * * *", TZ: "Europe/London"}) || out.Says != "Every 5 minutes" || out.Next == nil {
			t.Fatalf("cron = %+v", out)
		}
		if r := ask(t, app, http.MethodPost, "/v1/auto/cron", "acme", map[string]any{"text": "daily at 9", "tz": "Mars/Olympus"}); r.Code != http.StatusUnprocessableEntity {
			t.Fatalf("a zone that is not one = %d", r.Code)
		}
		if r := req(t, app, http.MethodPost, "/v1/auto/cron", "", map[string]any{"text": "daily"}); r.Code != http.StatusUnauthorized && r.Code != http.StatusForbidden {
			t.Fatalf("anonymous = %d", r.Code)
		}
	})
}

// The live stream carries the caller's own rows and nobody else's.
func TestEventsStreamOnlyYourRows(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		newApp(t)
		o := ops{s: mounted}
		was := eventsPoll
		eventsPoll = 20 * time.Millisecond
		t.Cleanup(func() { eventsPoll = was })
		alice := zip.WithCaller(context.Background(), zip.Caller{Org: "acme", User: "alice"})
		bob := zip.WithCaller(context.Background(), zip.Caller{Org: "acme", User: "bob"})
		var buf bytes.Buffer
		w := bufio.NewWriter(&buf)
		done := make(chan struct{})
		go func() {
			o.pump(alice, w, "acme", "alice", time.Now().Add(400*time.Millisecond))
			close(done)
		}()
		time.Sleep(60 * time.Millisecond)
		if _, err := o.trackJob(alice, &client.JobTrackIn{Product: "studio", Ref: "mine", Title: "Mine", State: StateRunning}); err != nil {
			t.Fatal(err)
		}
		if _, err := o.trackJob(bob, &client.JobTrackIn{Product: "studio", Ref: "theirs", Title: "Theirs", State: StateRunning}); err != nil {
			t.Fatal(err)
		}
		<-done
		out := buf.String()
		if !strings.Contains(out, "event: job") || !strings.Contains(out, `"title":"Mine"`) {
			t.Fatalf("alice's stream missed her job: %s", out)
		}
		if strings.Contains(out, "Theirs") {
			t.Fatalf("alice's stream carried bob's job: %s", out)
		}
		if strings.Count(out, `"title":"Mine"`) != 1 {
			t.Fatalf("one change was sent more than once: %s", out)
		}
	})
}
