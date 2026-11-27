package coding

// start_ledger_test.go — the ops that can be retried, driven end to end: a start that
// would spend real money, more time that would be added twice, a pull request, a merge.
// Each is asked twice with one request id, on a pod, across a restart, or on two pods.

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud/apps/agents"
	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk/coord"
	"github.com/hanzoai/cloud/sdk/coord/coordtest"
	"github.com/zap-proto/zip"
)

// keeping makes l the ledger this process's starts, pulls and merges are kept in.
func keeping(t *testing.T, l *ledger) {
	t.Helper()
	flights.keep(l)
	t.Cleanup(func() { flights.keep(nil) })
}

// A start asked twice with one request id is one run: the second is answered the first's
// handle, and no second session is opened and no second sandbox leased. The pod that
// answers the second may be a new process, as it is after a deploy, and a different
// request id is a different run.
//
// MUTATION: dedupe in a map of the process and the retry after the restart runs again.
func TestAStartRetriedAfterARestartIsOneRun(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		s := &depot{t: t, open: open}
		pod := coordtest.Memory(t)
		r := gitHubRun(t, map[string]string{})
		keeping(t, s.replica(pod, "cloud-0", periods{}))

		in := client.CodingStartIn{Repo: "github.com/hanzo-apps/site", Prompt: "fix the header", Pull: true, RequestID: "req-start-1"}
		first, err := r.startAs(t, asCaller("sub-zoe"), "sub-zoe", in)
		if err != nil || first.SessionID == "" {
			t.Fatalf("start: (%+v, %v)", first, err)
		}

		// The process is replaced: a new pod, a new ledger, the same store.
		keeping(t, s.replica(pod, "cloud-1", periods{}))
		again, err := r.startAs(t, asCaller("sub-zoe"), "sub-zoe", in)
		if err != nil || again != first {
			t.Fatalf("the retry after a restart answered (%+v, %v), want the first's %+v", again, err, first)
		}
		counts := func() (opened, leased int) {
			r.sessions.mu.Lock()
			defer r.sessions.mu.Unlock()
			r.pod.mu.Lock()
			defer r.pod.mu.Unlock()
			return len(r.sessions.opened), len(r.pod.leased)
		}
		if opened, leased := counts(); opened != 1 || leased != 1 {
			t.Fatalf("one request id opened %d sessions and leased %d sandboxes, want one run", opened, leased)
		}

		in.RequestID = "req-start-2"
		if _, err := r.startAs(t, asCaller("sub-zoe"), "sub-zoe", in); err != nil {
			t.Fatal(err)
		}
		if opened, leased := counts(); opened != 2 || leased != 2 {
			t.Fatalf("another request id made %d sessions and %d leases, want a run of its own", opened, leased)
		}
	})
}

// A start whose claim was retaken before it committed spawns nothing: its session is
// closed with the reason, its credential withdrawn, no sandbox leased, and the caller is
// told another attempt owns the request. This is what keeps a slow claimer, judged
// abandoned by a duplicate, from being the second run.
//
// MUTATION: spawn the run whether or not the outcome could be recorded and the slow
// attempt is a second run.
func TestAStartThatLostItsClaimSpawnsNothing(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		s := &depot{t: t, open: open}
		pod := coordtest.Memory(t)
		r := gitHubRun(t, map[string]string{})
		l := s.replica(pod, "cloud-0", periods{})
		keeping(t, l)

		// Between the start telling its session what it runs and spawning it, another
		// attempt takes the request over.
		r.sessions.onTag = func() {
			if _, err := s.db.Exec(`UPDATE requests SET owner = 'cloud-9#thief' WHERE id = 'req-lost'`); err != nil {
				t.Errorf("take over: %v", err)
			}
		}
		in := client.CodingStartIn{Repo: "github.com/hanzo-apps/site", Prompt: "fix the header", RequestID: "req-lost"}
		_, err := r.startAs(t, asCaller("sub-zoe"), "sub-zoe", in)
		if code := codeOf(t, err); code != http.StatusConflict {
			t.Fatalf("a start that lost its claim answered %v, want 409", err)
		}
		r.sessions.mu.Lock()
		closes := len(r.sessions.closes)
		r.sessions.mu.Unlock()
		r.pod.mu.Lock()
		leased := len(r.pod.leased)
		r.pod.mu.Unlock()
		r.mu.Lock()
		revoked := len(r.revoked)
		r.mu.Unlock()
		if leased != 0 || closes != 1 || revoked != 2 {
			t.Fatalf("a start that lost its claim leased %d sandboxes, closed %d sessions and revoked %d credentials, want 0, 1 and 2 (to push, and to read)", leased, closes, revoked)
		}
	})
}

// More time asked twice with one request id is given once, on whichever pod flies the
// run and whichever pod is asked: a retry that adds it again would end a run later than
// its person said. A request with no id adds each time it is asked.
//
// MUTATION: skip the ledger in extend and the retry adds the time again.
func TestMoreTimeIsGivenOncePerRequest(t *testing.T) {
	eachSite(t, func(t *testing.T, s *depot, pod func(string) *coord.Coord) {
		flying, la := podOf(t, s, pod, "cloud-0")
		asked, _ := podOf(t, s, pod, "cloud-1")
		_ = la
		f, _ := flyOn(t, flying, "sess_t", "u-acme", client.Reply{})
		f.clock = newClock(time.Now(), time.Minute, time.Hour, f.cancel)
		t.Cleanup(f.clock.end)
		ctx := t.Context()

		in := &client.CodingExtendIn{Session: "sess_t", Seconds: 300, RequestID: "x1"}
		first, err := asked.extend(ctx, "acme", "u-acme", in)
		if err != nil {
			t.Fatalf("extend: %v", err)
		}
		again, err := flying.extend(ctx, "acme", "u-acme", in) // the retry lands on the pod that flies it
		if err != nil || *again != *first {
			t.Fatalf("the retry answered (%+v, %v), want the first's %+v", again, err, first)
		}
		if left := f.clock.left(time.Now()); left < 6*time.Minute-5*time.Second || left > 6*time.Minute+5*time.Second {
			t.Fatalf("the run has %v left, want its minute and the 300 seconds asked for, once", left)
		}

		in.RequestID = ""
		if _, err := flying.extend(ctx, "acme", "u-acme", in); err != nil {
			t.Fatal(err)
		}
		if left := f.clock.left(time.Now()); left < 11*time.Minute-5*time.Second {
			t.Fatalf("a request with no id added nothing: %v left", left)
		}
	})
}

// A pull request asked for twice with one request id is opened once and answered the
// same; the retry does not touch the host.
func TestAPullRequestIsOpenedOncePerRequest(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		s := &depot{t: t, open: open}
		pod := coordtest.Memory(t)
		r := aRunToPull(t, map[string]string{pullBranch: theSHA}, pushedRun())
		keeping(t, s.replica(pod, "cloud-0", periods{}))

		in := &client.CodingPullIn{Session: pullSession, RequestID: "pr-1"}
		first, err := Pull(asCaller("sub-zoe"), in)
		if err != nil {
			t.Fatalf("pull: %v", err)
		}
		keeping(t, s.replica(pod, "cloud-1", periods{})) // a restart between
		again, err := Pull(asCaller("sub-zoe"), in)
		if err != nil || *again != *first {
			t.Fatalf("the retry answered (%+v, %v), want %+v", again, err, first)
		}
		r.mu.Lock()
		defer r.mu.Unlock()
		if len(r.pulls) != 1 {
			t.Fatalf("the host was asked to open %d pull requests, want one", len(r.pulls))
		}
	})
}

// A merge asked for twice with one request id is merged once and answered the same; the
// retry does not touch the forge.
func TestAMergeIsAskedOncePerRequest(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		s := &depot{t: t, open: open}
		pod := coordtest.Memory(t)
		f, _ := servesPull(t, "hanzo", "hanzoai", "zoe")
		keeping(t, s.replica(pod, "cloud-0", periods{}))

		in := &client.CodingMergeIn{Session: runSession, RequestID: "merge-1"}
		first, err := Merge(asCaller("sub-zoe"), in)
		if err != nil || first.State != "merged" {
			t.Fatalf("merge: (%+v, %v)", first, err)
		}
		f.mu.Lock()
		asked := len(f.as)
		f.mu.Unlock()
		keeping(t, s.replica(pod, "cloud-1", periods{}))
		again, err := Merge(asCaller("sub-zoe"), in)
		if err != nil || *again != *first {
			t.Fatalf("the retry answered (%+v, %v), want %+v", again, err, first)
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		if len(f.as) != asked {
			t.Fatalf("the retry made %d requests of the forge, want none", len(f.as)-asked)
		}
	})
}

// A run resumed after a rollout begins with what was said to it while no pod held it: in
// its task, in the order it was said, and taken, so the flight does not hand it over a
// second time and stop the run it just resumed.
//
// MUTATION: resume without reading the inbox and the task is the original alone.
func TestAResumedRunIsGivenWhatWasSaid(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		s := &depot{t: t, open: open}
		pod := coordtest.Memory(t)
		r := gitHubRun(t, map[string]string{})
		l := s.replica(pod, "cloud-0", periods{})
		keeping(t, l)

		const session, org = "sess_abc123def456", "hanzo"
		ctx := t.Context()
		for i, text := range []string{"use the other endpoint", "and add a test"} {
			if err := l.enqueue(ctx, org, session, "sub-zoe", "r"+string(rune('1'+i)), text); err != nil {
				t.Fatal(err)
			}
		}
		x := agents.Session{ID: session, Org: org, Actor: org + "/sub-zoe", Kind: kindCoding, Box: "sbx_old",
			Repo: "hanzo-apps/site", Base: "main", Branch: "agent/abc", Status: agents.StatusRunning}
		engineOnce = sync.Once{}
		t.Cleanup(func() { engineOnce = sync.Once{} })
		if !Resume(asCaller("sub-zoe"), x, client.CodingStartIn{Prompt: "fix the header"}, nil) {
			t.Fatal("the run was not resumed")
		}
		flights.Lock()
		f := flights.m[session]
		flights.Unlock()
		if f != nil {
			select {
			case <-f.done:
			case <-time.After(20 * time.Second):
				t.Fatal("the resumed run never ended")
			}
		}

		r.pod.mu.Lock()
		fed := strings.Join(r.pod.fed, "\n---\n")
		r.pod.mu.Unlock()
		want := "fix the header\n\nThen the person who asked said:\n\nuse the other endpoint\n\nand add a test"
		if !strings.Contains(fed, want) {
			t.Fatalf("the resumed run's task does not carry what was said, in order:\n%s", fed)
		}
		rows, err := l.inbox(ctx, org, session)
		if err != nil || len(rows) != 2 || rows[0].state != steerDelivered || rows[1].state != steerDelivered {
			t.Fatalf("the inbox reads %+v (%v), want both taken by the resumed run", rows, err)
		}
	})
}

// A start asked again under the same Idempotency-Key with another body is a different
// request, and is refused naming the key; the retry with the same body is the first's run.
//
// MUTATION: key on the id alone and the second start silently answers the first's session.
func TestAStartUnderAReusedKeyWithAnotherBodyIsRefused(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		s := &depot{t: t, open: open}
		pod := coordtest.Memory(t)
		r := gitHubRun(t, map[string]string{})
		keeping(t, s.replica(pod, "cloud-0", periods{}))

		in := client.CodingStartIn{Repo: "github.com/hanzo-apps/site", Prompt: "fix the header", RequestID: "key-7"}
		first, err := r.startAs(t, asCaller("sub-zoe"), "sub-zoe", in)
		if err != nil {
			t.Fatal(err)
		}
		in.Prompt = "delete the site"
		_, err = r.startAs(t, asCaller("sub-zoe"), "sub-zoe", in)
		if codeOf(t, err) != http.StatusUnprocessableEntity || !strings.Contains(err.Error(), "key-7") {
			t.Fatalf("a start under a reused key with another body answered %v, want 422 naming the key", err)
		}
		in.Prompt = "fix the header"
		if again, err := r.startAs(t, asCaller("sub-zoe"), "sub-zoe", in); err != nil || again != first {
			t.Fatalf("the retry with the same body answered (%+v, %v), want %+v", again, err, first)
		}
		r.sessions.mu.Lock()
		defer r.sessions.mu.Unlock()
		if len(r.sessions.opened) != 1 {
			t.Fatalf("one key opened %d sessions, want one run", len(r.sessions.opened))
		}
	})
}

// More time whose claimer died after the run was given it is not given again by the retry
// that retakes the claim: the pod that flies the run recorded the effect under a claim of
// its own, and answers the retry what it answered the first.
//
// MUTATION: apply the extension without that record and the run gets the time twice.
func TestMoreTimeReplayedAfterItsClaimerDiesIsGivenOnce(t *testing.T) {
	eachSite(t, func(t *testing.T, s *depot, pod func(string) *coord.Coord) {
		flying, _ := podOf(t, s, pod, "cloud-0")
		claimer, lc := podOf(t, s, pod, "cloud-1")
		retry, lr := podOf(t, s, pod, "cloud-2")
		lr.p = periods{Bound: 100 * time.Millisecond}.set()
		f, _ := flyOn(t, flying, "sess_t", "u-acme", client.Reply{})
		f.clock = newClock(time.Now(), time.Minute, time.Hour, f.cancel)
		t.Cleanup(f.clock.end)
		ctx := t.Context()
		in := &client.CodingExtendIn{Session: "sess_t", Seconds: 300, RequestID: "x1"}

		k := request{org: "acme", subject: "u-acme", scope: scoped("extend", "sess_t"), id: "x1"}
		if mine, _, _, err := lc.take(ctx, k); err != nil || !mine {
			t.Fatalf("claim: %v %v", mine, err)
		}
		first, err := claimer.extendElsewhere("acme", "u-acme", in)
		if err != nil {
			t.Fatal(err)
		}
		lc.attach(nil) // the claimer's pod is gone before it settles
		again, err := retry.extend(ctx, "acme", "u-acme", in)
		if err != nil || *again != *first {
			t.Fatalf("the retry answered (%+v, %v), want what the run was told: %+v", again, err, first)
		}
		if left := f.clock.left(time.Now()); left < 6*time.Minute-5*time.Second || left > 6*time.Minute+5*time.Second {
			t.Fatalf("the run has %v left, want its minute and the 300 seconds, once", left)
		}
	})
}

// A pull request and a merge are made once: a claimer that was retaken between reading the
// run and making the effect (it was slow, not dead) makes nothing, and says another attempt
// owns the request.
//
// MUTATION: drop the fence from pull or merge and the retaken claimer opens or merges too.
func TestARetakenClaimerOpensAndMergesNothing(t *testing.T) {
	steal := func(db *sql.DB) {
		if _, err := db.Exec(`UPDATE requests SET owner = 'cloud-9#thief'`); err != nil {
			t.Errorf("take over: %v", err)
		}
	}
	t.Run("pull", func(t *testing.T) {
		r := aRunToPull(t, map[string]string{pullBranch: theSHA}, pushedRun())
		l := ledgerOf(t)
		keeping(t, l)
		was := visibleRun
		visibleRun = func(ctx context.Context, id string) (agents.Session, error) {
			steal(l.db) // between reading the run and opening its pull request
			return was(ctx, id)
		}
		t.Cleanup(func() { visibleRun = was })
		_, err := Pull(asCaller("sub-zoe"), &client.CodingPullIn{Session: pullSession, RequestID: "p1"})
		if codeOf(t, err) != http.StatusConflict {
			t.Fatalf("a retaken claimer's pull answered %v, want 409", err)
		}
		r.mu.Lock()
		defer r.mu.Unlock()
		if len(r.pulls) != 0 {
			t.Fatalf("a retaken claimer opened %v", r.pulls)
		}
	})
	t.Run("merge", func(t *testing.T) {
		f, _ := servesPull(t, "hanzo", "hanzoai", "zoe")
		l := ledgerOf(t)
		keeping(t, l)
		was := visibleRun
		visibleRun = func(ctx context.Context, id string) (agents.Session, error) {
			steal(l.db)
			return was(ctx, id)
		}
		t.Cleanup(func() { visibleRun = was })
		_, err := Merge(asCaller("sub-zoe"), &client.CodingMergeIn{Session: runSession, RequestID: "m1"})
		if codeOf(t, err) != http.StatusConflict {
			t.Fatalf("a retaken claimer's merge answered %v, want 409", err)
		}
		if asked := f.asked(); len(asked) != 0 {
			t.Fatalf("a retaken claimer merged: %v", asked)
		}
	})
}

// A pull request or a merge asked with a request id by a credential that names no person
// is refused 400, not run without the protection the id asked for.
func TestAPullOrAMergeWithAnIDAndNoPersonIsRefused(t *testing.T) {
	f, _ := servesPull(t, "hanzo", "hanzoai", "zoe")
	keeping(t, ledgerOf(t))
	nobody := zip.WithCaller(context.Background(), zip.Caller{Org: "hanzo"})
	if _, err := Merge(nobody, &client.CodingMergeIn{Session: runSession, RequestID: "m1"}); codeOf(t, err) != http.StatusBadRequest {
		t.Fatalf("a merge with an id and no person: %v, want 400", err)
	}
	if _, err := Pull(nobody, &client.CodingPullIn{Session: runSession, RequestID: "p1"}); codeOf(t, err) != http.StatusBadRequest {
		t.Fatalf("a pull with an id and no person: %v, want 400", err)
	}
	if len(f.asked()) != 0 {
		t.Fatalf("the forge was asked to merge: %v", f.asked())
	}
}

// What a resumed run is owed is recorded on its session before it is marked delivered, so
// the next resume, which rebuilds the task from the session, has it. One the session
// cannot record stays queued, with every one after it, for the flight to hand over.
//
// MUTATION: mark before recording (the old order) and a failed record loses the instruction.
func TestWhatAResumedRunIsOwedIsRecordedBeforeItIsDelivered(t *testing.T) {
	r := newRig()
	r.serve(t)
	engineOnce = sync.Once{}
	t.Cleanup(func() { engineOnce = sync.Once{} })
	l := ledgerOf(t)
	keeping(t, l)
	ctx := t.Context()
	for _, in := range []struct{ id, text string }{{"r1", "use the other endpoint"}, {"r2", "and add a test"}, {"r3", "then stop"}} {
		if err := l.enqueue(ctx, "hanzo", "sess_x", "sub-zoe", in.id, in.text); err != nil {
			t.Fatal(err)
		}
	}
	// The session records the first two and refuses the third.
	r.sessions.refuse = func(kind, payload string) error {
		if kind == kindMessage && strings.Contains(payload, "then stop") {
			return errors.New("the session store blinked")
		}
		return nil
	}
	took := backlog(ctx, &agents.Session{ID: "sess_x"}, "hanzo", "sess_x", "sub-zoe", "fix the header", nil)
	if len(took) != 2 || took[0].body != "use the other endpoint" || took[1].body != "and add a test" {
		t.Fatalf("the resumed run was given %+v, want the two the session recorded, in order", took)
	}
	r.sessions.mu.Lock()
	events := slices.Clone(r.sessions.events)
	r.sessions.mu.Unlock()
	if len(events) != 2 || events[0].kind != kindMessage || !strings.Contains(events[0].payload, `"queued":true`) ||
		!strings.Contains(events[0].payload, "use the other endpoint") || !strings.Contains(events[1].payload, "and add a test") {
		t.Fatalf("the session recorded %+v, want the two instructions as queued messages, in order", events)
	}
	rows, err := l.inbox(ctx, "hanzo", "sess_x")
	if err != nil || len(rows) != 3 || rows[0].state != steerDelivered || rows[1].state != steerDelivered || rows[2].state != steerQueued {
		t.Fatalf("the inbox reads %+v (%v), want the recorded two delivered and the third queued", rows, err)
	}
}

// A start retried with the body spelled another way (`task` for `prompt`, a trailing
// newline) is the same run, not a 422.
func TestAStartRetriedInAnotherSpellingIsTheSameRun(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		s := &depot{t: t, open: open}
		pod := coordtest.Memory(t)
		r := gitHubRun(t, map[string]string{})
		keeping(t, s.replica(pod, "cloud-0", periods{}))
		first, err := r.startAs(t, asCaller("sub-zoe"), "sub-zoe",
			client.CodingStartIn{Repo: "github.com/hanzo-apps/site", Prompt: "fix the header", RequestID: "key-8"})
		if err != nil {
			t.Fatal(err)
		}
		again, err := r.startAs(t, asCaller("sub-zoe"), "sub-zoe",
			client.CodingStartIn{Repo: "github.com/hanzo-apps/site", Task: "fix the header\n", RequestID: "key-8"})
		if err != nil || again != first {
			t.Fatalf("the retry spelled another way answered (%+v, %v), want %+v", again, err, first)
		}
	})
}

// A pull request replayed after its claimer died between opening it and recording it files
// its work item under the same request, which the task app answers and files no second row
// for. The key is the request's, the same for the replay.
//
// MUTATION: send the work item with no request and every replay files a new row.
func TestAPullReplayFilesItsWorkItemUnderTheSameRequest(t *testing.T) {
	r := aRunToPull(t, map[string]string{pullBranch: theSHA}, pushedRun())
	l := ledgerOf(t)
	keeping(t, l)
	in := &client.CodingPullIn{Session: pullSession, RequestID: "p1"}
	if _, err := Pull(asCaller("sub-zoe"), in); err != nil {
		t.Fatal(err)
	}
	// The claimer died before it settled: the row is gone, and the retry runs the op again.
	if _, err := l.db.Exec(`DELETE FROM requests`); err != nil {
		t.Fatal(err)
	}
	if _, err := Pull(asCaller("sub-zoe"), in); err != nil {
		t.Fatal(err)
	}
	filed := func() []PRInput {
		r.todo.mu.Lock()
		defer r.todo.mu.Unlock()
		return slices.Clone(r.todo.inputs)
	}
	if got := filed(); len(got) != 2 || got[0].Request == "" || got[0].Request != got[1].Request {
		t.Fatalf("the work item was filed under %+v, want the same non-empty request both times", got)
	}
	// Another request id is another key; no id files with none.
	if _, err := Pull(asCaller("sub-zoe"), &client.CodingPullIn{Session: pullSession, RequestID: "p2"}); err != nil {
		t.Fatal(err)
	}
	if _, err := Pull(asCaller("sub-zoe"), &client.CodingPullIn{Session: pullSession}); err != nil {
		t.Fatal(err)
	}
	if got := filed(); len(got) != 4 || got[2].Request == "" || got[2].Request == got[0].Request || got[3].Request != "" {
		t.Fatalf("the requests were %+v", got)
	}
}
