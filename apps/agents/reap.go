package agents

// reap.go — WHEN a session ends on its own, and the one function that says so.
//
// THE LEAK. A session is OPENED by a surface and CLOSED by one of four writers of
// ended_at, every one of which is somebody else's process choosing to say so: the
// machine running the run, a browser's PATCH, the in-process runner, a logout
// revoke. So a client that stops existing — a closed laptop, a killed container, a
// tab that never came back — leaves a row that says `running` with nobody left who
// can say otherwise. Before the runtime meter (meter.go) that was untidy. Now it is
// $0.08 an hour, forever: an abandoned row bills about $57.60 a month for work
// nobody is doing, and there is no event that will ever stop it.
//
// apps/sandbox already answers this for the lease it sells, and this is that answer
// with the clocks a SESSION can feed: a policy read once, a pure function that says
// whether a row's time is up and which clock ran out, and a pass on the meter's own
// loop that acts on it. WHEN a session ends is decided here and nowhere else — the
// store holds the stamps, this holds the rule that reads them, which is why there
// is one list query and not one per reason.
//
// TWO CLOCKS, where the sandbox has three, and the missing one is missing on
// purpose. A sandbox knows whether anybody is WATCHING it: the session stream
// restamps a per-project presence beat, so it can hold two idle allowances and pick
// between them by that fact. A session has no such fact about ITSELF — the beat
// names a project and a project holds many sessions — so a presence clock here
// would be inventing the input as well as the rule.
//
//	idle      a session that claims to be RUNNING and has said nothing since.
//	max-life  the ceiling over any live session, measured from when it started.
//
// IDLE READS ACTIVITY, NEVER LIVENESS, and they are different questions. `status`
// is a CLAIM by whoever last wrote the row; updated_at is what the session has
// actually DONE — every message, tool call, spawn, log line, status change and
// control command appends an event and bumps it in the SAME transaction
// (Store.AppendEvent), from the server's clock and never a caller's. So the rule is
// over the second, and a run that is working is never reaped however long it has
// been working. The runtime meter deliberately does not touch that column
// (AdvanceSession writes metered_at alone), which is what keeps this a clock rather
// than something a tick rewinds every fifteen minutes.
//
// PAUSED IS NOT IDLE. `paused` is a state a person chose and the row says so, so
// the short allowance does not reach it: ending somebody's paused run an hour into
// their lunch is the mistake this file would otherwise ship, and it cannot be taken
// back — a terminal session stays terminal. Nor does it COST anything: a pause
// writes its run's end (Store.Pause, Store.UpdateSession, register), as Rest and
// Park already did, so the meter bills to the pause and the row leaves the live set
// until a resume opens it again. This pass closes a paused row that has no end — one
// paused before that rule, or by a writer that forgot it — at its last activity.
// #768 was the cost of the old rule: hanzo dev records a clean exit as paused, and
// each one billed for a day with nothing running in it.
//
// QUEUED IS A CLAIM ABOUT WORK TO COME, not a state a person chose, and the meter
// bills it as it bills running; so the idle clock reads it too.
//
// WHAT A REAP IS, and why it is the same act as a stop. It goes through stop,
// exactly as the login-manager revoke does: a control event the surface consumes
// and stops on, a terminal status, and an ended_at that closes the meter's span. It
// destroys nothing a tenant made — the events, the tree and the provenance all stay
// — so the price of reaping early is a run somebody starts again, against a leak
// that otherwise never stops.

import (
	"context"
	"encoding/json"
	"strconv"
	"time"

	"github.com/hanzoai/cloud/sdk/env"
	"github.com/hanzoai/cloud/sdk/mint"

	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/meter"
	"github.com/hanzoai/account"
	"github.com/hanzoai/namespace"
)

// The defaults, each in the unit it is argued in.
const (
	// idleDefault is how long a session that says it is running may say nothing.
	//
	// An hour, and both ends of the argument meet there. From below: the longest
	// single step this process bounds is a scheduled run at ten minutes
	// (scheduler.go runTimeout) and a tool dispatch is ninety seconds
	// (tools.go toolRunBudget), so six scheduled runs of silence is not a slow step,
	// it is an absent client. From above: an hour is what apps/sandbox already
	// allows a WATCHED sandbox, so a coding session and the sandbox it runs in do
	// not disagree about how long silence is allowed.
	idleDefault = time.Hour

	// maxLifeDefault is the ceiling over a live session, from its start, whatever it
	// is doing — the same day apps/sandbox puts over a lease and for the same
	// reason: no amount of activity turns a session into a permanent resident.
	//
	// A thing MEANT to run forever is not a session. That is a resident bot, which
	// has no open session at all (the scheduler invokes it and each invocation is
	// born and dies in one call) and accrues on its MODE instead — so this ceiling
	// can be a day without shortening anything the platform sells as long-running.
	maxLifeDefault = 24 * time.Hour
)

// sweepEvery is how often the loop below runs. It carries two passes and the
// cadence answers to both: for the meter it decides FRESHNESS and never the amount,
// since the charge is (now − watermark), and for the reaper it is the resolution of
// the clocks above — a quarter of an hour is four times finer than the shortest of
// them. Fifteen minutes keeps a resident bot's ledger within a quarter-hour of the
// truth without writing a row per minute per bot.
const sweepEvery = 15 * time.Minute

// sweepFirst is how long after mount the FIRST pass runs, and it is short for a
// reason measured rather than assumed: this app is LAZY, so its process lives from
// the first request to its prefix until the host goes down, and a fleet whose pods
// turn over faster than sweepEvery would never reach a tick at all — every debit
// deferred indefinitely while the watermarks sit still, and every leaked session
// left standing. No money is lost when that happens (the watermark is durable, so
// the next process bills the whole gap at once) but "eventually, if a pod lives long
// enough" is neither a billing cadence nor a lifetime policy.
//
// It is not ZERO either: a crashlooping pod would then sweep on every boot, and a
// span of seconds is a real debit at micro-USD precision. A minute is longer than a
// crashloop's cycle and far shorter than a healthy pod's life.
const sweepFirst = time.Minute

// clocks is the whole lifetime policy, read once.
//
// A value rather than two package constants because it is read ONCE, at mount, and
// carried into the pass: read per row, a knob changed under a running process would
// be visible to half a sweep and not to the other half.
type clocks struct {
	idle    time.Duration
	maxLife time.Duration
}

// newClocks reads the policy from the environment, in SECONDS — the spelling
// apps/sandbox already uses for the same questions, so nobody has to remember which
// knobs take `15m` and which take `900`. A non-positive or unparseable value is the
// default, so a typo can neither shorten a life nor remove a ceiling.
func newClocks() clocks {
	return clocks{
		idle:    envSecs("AGENTS_IDLE_SEC", idleDefault),
		maxLife: envSecs("AGENTS_MAX_LIFE_SEC", maxLifeDefault),
	}
}

// MaxLife is the ceiling over a live session (AGENTS_MAX_LIFE_SEC, a day by default):
// the one figure for how long a session may stand. A coding run parked for a refusal to
// clear goes on by itself for that long and no longer (apps/coding park.go): past it the
// run waits for a person to press Continue.
func MaxLife() time.Duration { return newClocks().maxLife }

func envSecs(key string, def time.Duration) time.Duration {
	if n, err := strconv.Atoi(env.Or(key, "")); err == nil && n > 0 {
		return time.Duration(n) * time.Second
	}
	return def
}

// over reports whether this session's time is up, and says WHY in the words the
// caller publishes.
//
// The reason is a SENTENCE rather than a label because it has two readers who must
// not be told different things: the operator's log line, and the control event the
// session's own surface renders to the person whose run just ended. One string,
// composed from the clock that actually ran out, so "why did my agent stop" has one
// answer wherever it is asked.
//
// The ceiling is asked FIRST, so a bug in the activity stamp can delay a reap by at
// most max-life and never past it.
func (c clocks) over(x Session, now time.Time) (bool, string) {
	// A session that has already ended is over in the only sense that matters and
	// there is nothing left to do to it. Store.Live does not return one; asked about
	// one anyway, the answer is no — never a second end written over the first, and
	// never a reap reported for a row nothing touched.
	if x.EndedAt > 0 || isTerminalStatus(x.Status) {
		return false, ""
	}
	if c.maxLife > 0 && x.StartedAt > 0 && now.Sub(time.Unix(x.StartedAt, 0)) > c.maxLife {
		return true, "ended by the platform: past the " + c.maxLife.String() + " a session may run"
	}
	// Only a session CLAIMING to run, or to be about to (queued), is idle-reaped.
	// Paused is a state somebody chose and the ceiling above is what bounds it —
	// see the file comment.
	if x.Status != StatusRunning && x.Status != StatusQueued {
		return false, ""
	}
	// The LATER of the two stamps. updated_at is written at create and only ever
	// moves forward, so the max is normally updated_at; taking it anyway is what
	// keeps the rule right for a row whose stamps an older release wrote.
	since := max(x.StartedAt, x.UpdatedAt)
	if c.idle > 0 && since > 0 && now.Sub(time.Unix(since, 0)) > c.idle {
		return true, "ended by the platform: no activity for " + c.idle.String()
	}
	return false, ""
}

// reap ends every session whose time is up, in every org THIS REPLICA OWNS.
//
// Errors are per-org and never abort the pass: one org with an unreadable file must
// not keep every other org's leaked sessions alive. A store that could not be
// opened or listed is REPORTED and skipped, never read as "this org has nothing
// running" — the rule the sandbox orphan sweep states, for the same reason: silence
// is not an answer.
//
// OWNERSHIP IS A GATE ON THE ACT, NOT ON THE WRITE. A reap is terminal — a control
// event the surface stops on, a terminal status, an ended_at — and none of that can
// be taken back. Every pod holds every org's file its volume carries, so an ungated
// pass is every pod ending every org's sessions: the loser's write is discarded on
// the next hydrate, but the person whose agent stopped mid-run does not get it back,
// and the control event has already been consumed. So the org's elected writer is
// the one that decides when its sessions end, and the others leave them alone.
func reap(ctx context.Context, st *state, log logger, clk clocks) {
	now := time.Now()
	// What the pods are running is asked once per pass, and only if a session looks
	// lost: a run executing on another pod is not an orphan of this one.
	var held map[string]bool
	var heldErr error
	asked := false
	heldNow := func() (map[string]bool, error) {
		if !asked {
			asked = true
			held, heldErr = running.held(ctx)
		}
		return held, heldErr
	}
	_ = st.eachStore(func(ns namespace.Namespace, sto *Store, openErr error) {
		if ctx.Err() != nil {
			return
		}
		if openErr != nil {
			log.Warn("reap: open store", "namespace", ns, "err", openErr)
			return
		}
		if !st.stores.Owned(ns) {
			return // another replica ends this org's sessions
		}
		org := ns.ID()
		since := time.Now()
		live, err := sto.Live(ctx, org)
		if err != nil {
			log.Warn("reap: list live sessions", "org", org, "err", err)
			return
		}
		for _, x := range live {
			if sto.gone(ctx, x, now) {
				h, err := heldNow()
				switch {
				case err != nil:
					// A pod that could not be asked may be running it: leave it for the next pass.
					log.Warn("reap: cannot tell whether another pod runs a session; leaving it", "org", org, "session", x.ID, "err", err)
				case !h[x.ID]:
					if ended, err := endOrphan(ctx, sto, x, now.Unix()); err != nil {
						log.Warn("reap: end orphaned turn", "org", org, "session", x.ID, "err", err)
					} else if ended {
						log.Info("ended orphaned turn", "org", org, "session", x.ID)
					}
					continue
				}
			}
			done, why := clk.over(x, now)
			if !done {
				// A paused session with no end is one nobody closed: nothing runs in
				// it, so its span ends at its last activity and the meter, which runs
				// next on this loop, bills it to there. It stays paused.
				if x.Status == StatusPaused {
					if ok, err := sto.EndPause(ctx, org, x.ID); err != nil {
						log.Warn("reap: end a pause", "org", org, "session", x.ID, "err", err)
					} else if ok {
						log.Info("closed a paused session's runtime", "org", org, "session", x.ID)
					}
				}
				continue
			}
			if _, _, err := stop(ctx, sto, x, why); err != nil {
				log.Warn("reap: end session", "org", org, "session", x.ID, "err", err)
				continue
			}
			log.Info("reaped session", "org", org, "session", x.ID, "why", why)
		}
		if quiet, err := sto.Quiet(ctx, org); err == nil && quiet {
			st.stores.Settle(ns, since)
		}
	})
}

// logger is what these passes report through — the two levels they use, so a test
// drives them with a recorder rather than a whole Service.
type logger interface {
	Warn(string, ...any)
	Info(string, ...any)
}

// startSweep runs the periodic pass until the returned stop is called. It is
// started by Mount and stopped by Shutdown, ahead of CloseAll.
//
// ONE LOOP, TWO PASSES, in this order. Reaping first means a session ended here has
// its final span charged by the meter in the SAME tick — Unbilled's second shape
// takes a row whose ended_at is past its watermark — rather than accruing for
// another quarter of an hour against a run that is already over. Neither pass
// depends on the other having succeeded: an unreaped session is billed for what it
// really was, and an unbilled reap is picked up by the next process, because the
// watermark is on the row.
//
// A TIMER RESET IN THE LOOP, not a ticker, because the first interval and every one
// after it are different questions — see [sweepFirst]. Nothing waits behind either:
// the loop is a goroutine, so Mount returns before the first pass starts however
// soon it is scheduled.
func startSweep(s *cloud.Service[state]) func() {
	ctx, stop := context.WithCancel(context.Background())
	// The policy is read ONCE here rather than per pass, so every row in every
	// sweep is judged by the same numbers.
	clk := newClocks()
	cloud.SayDurability(s.Log, s.Base, "agent runtime")
	go func() {
		t := time.NewTimer(sweepFirst)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				reap(ctx, &s.State, s.Log, clk)
				meterRuntime(ctx, &s.State, s.Log, time.Now().Unix(),
					func(payer string, u meter.Usage) { s.Bill.Record(account.PayerOf("", payer), meterKind, u) })
				t.Reset(sweepEvery)
			}
		}
	}()
	return stop
}

// orphaned is the orphan end for ONE turn, asked by the surface rendering it
// (turn.go waitTurn), in an org this replica owns.
//
// The sweep finds an orphan within a quarter of an hour, and until then the turn
// read as working: its process had gone away mid-run — a rollout ends every turn
// in flight — and nothing else was ever going to say otherwise. The surface
// rendering it asks every few seconds, so asking here too means a turn whose run
// is gone reads as over the next time anyone looks.
func (st *state) orphaned(ctx context.Context, sto *Store, org, id string) {
	x, err := sto.GetSession(ctx, org, id)
	if err != nil || !sto.gone(ctx, x, time.Now()) || !st.owns(org) {
		return
	}
	if held, err := running.held(ctx); err != nil || held[id] {
		return
	}
	if _, err := endOrphan(ctx, sto, x, time.Now().Unix()); err != nil && st.svc != nil {
		st.svc.Log.Warn("agents: end orphaned turn", "org", org, "session", id, "err", err)
	}
}

// ResumeCoding, when set by the composition root (plugin/agent), adopts an
// orphaned coding session whose sandbox is still alive and continues its run in
// place after a platform rollout instead of ending the session with an error.
var ResumeCoding func(ctx context.Context, sto *Store, x Session) bool

// endOrphan ends a turn whose run is not coming back, and says why on its
// session, so the surface rendering it concludes with a reason rather than none.
//
// When a coding run has a live sandbox (x.Box != ""), ResumeCoding first tries
// to adopt the session and continue its work in that sandbox seamlessly.
//
// Otherwise a coding run ends as its own runs end — a status event and the session's
// error — so whatever waits on it for done reads an end and a reason, and the
// reason says how to go on: its sandbox and branch outlive the process that
// drove it, and a follow-up continues in them.
func endOrphan(ctx context.Context, sto *Store, x Session, at int64) (bool, error) {
	if x.Kind == kindCoding && x.Box != "" && x.Target == "" && ResumeCoding != nil {
		// Every pod that finds the run lost would resume it. The deployment says yes
		// to one; the others leave it, because the one that said yes is carrying it
		// on, and ending it from under that one would end the run it just started.
		mine, err := running.claim(ctx, x)
		switch {
		case err != nil:
			return false, err
		case !mine:
			return false, nil
		case ResumeCoding(ctx, sto, x):
			return false, nil
		}
	}
	// The app's conversation outlives a turn whose process went away (talk.go): the
	// turn rests, with why on its record, and the person's next words go on.
	if talks(x) {
		rested, err := sto.Rest(ctx, x.Org, x.ID, x.Run, at)
		if err != nil || !rested {
			return rested, err
		}
		payload, _ := json.Marshal(map[string]any{"runId": x.Run, "status": "error", "error": orphanReason})
		if e, err := sto.AppendEvent(ctx, Event{
			ID: mint.ID("evt"), SessionID: x.ID, Org: x.Org, Kind: KindLog,
			Actor: x.Actor, Payload: string(payload), CreatedAt: at,
		}); err == nil {
			publishEvent(mounted, x, e)
		}
		x.Status, x.EndedAt, x.UpdatedAt = StatusPaused, at, at
		publishSession(mounted, x, 0, 0)
		return true, nil
	}
	why, kind := orphanReason, KindLog
	body := map[string]any{"runId": x.Run, "status": "error", "error": orphanReason}
	if x.Kind == kindCoding {
		why, kind = codingOrphanReason, KindStatus
		body = map[string]any{"status": "error", "error": codingOrphanReason, "branch": x.Branch}
	}
	ended, err := sto.End(ctx, x.Org, x.ID, StatusError, at, why)
	if err != nil || !ended {
		return ended, err
	}
	payload, _ := json.Marshal(body)
	if e, err := sto.AppendEvent(ctx, Event{
		ID: mint.ID("evt"), SessionID: x.ID, Org: x.Org, Kind: kind,
		Actor: x.Actor, Payload: string(payload), CreatedAt: at,
	}); err == nil {
		publishEvent(mounted, x, e)
	}
	return true, nil
}

// orphanReason is what an orphaned turn's session says about how it ended.
const orphanReason = "the process running this turn went away before it finished"

// codingOrphanReason is what an orphaned coding run's session says: what ended
// it, and what is left to go on from.
const codingOrphanReason = "the platform restarted while this run was working, so it ended unfinished; " +
	"its sandbox and branch are kept, and a follow-up (after: this session) continues in them"

// orphanGrace is how long a coding run's session may say nothing before a process
// that does not hold it reads the run as lost. A run is held a moment after its
// session is tagged (apps/coding start.go tags, then flies), and in a rollout the
// pod being replaced drives its runs until it is killed, writing as it goes;
// neither is an orphan to the pod answering the next read.
const orphanGrace = 2 * time.Minute

// gone is lost, except a run on a machine whose runner polled within servingTTL: it
// works in the runner, no pod holds it, and it reports once, when it ends. The sweep
// and a read of the session both ask it before ending a run as an orphan.
func (s *Store) gone(ctx context.Context, x Session, now time.Time) bool {
	if !lost(x, now) {
		return false
	}
	_, servingAt, err := s.ClaimKeyHash(ctx, x.Org, x.Environment)
	return err != nil || now.Sub(time.Unix(servingAt, 0)) > servingTTL
}

// lost reports a running session that no run is known to be executing: a turn's
// session, or a coding run's once it has said nothing past orphanGrace. Whether it
// IS an orphan is whether any pod of the deployment runs it (registry.held): the run
// lives only in the process that started it, and that is not always this one.
//
// A PAUSED one is not lost: it has no run anywhere by design, and waits for a
// resume. The ceiling in over is what bounds it.
//
// A coding run is held the same way (agents.Hold, from apps/coding), so a running
// coding session no pod holds, quiet past orphanGrace, is one whose process went
// away — a rollout or a crash — and nothing else will ever end it.
//
// Ending it ends no sandbox, because there is none here to end: a run's sandbox
// ends with the run's context (box.go), so an orphan's was given back by its run
// on the way out, or is left to apps/sandbox's reaper by a process that died.
func lost(x Session, now time.Time) bool {
	if x.Status != StatusRunning {
		return false
	}
	switch {
	case x.Run != "":
		return true
	case x.Kind == kindCoding && now.Sub(time.Unix(max(x.StartedAt, x.UpdatedAt), 0)) > orphanGrace:
		return true
	}
	return false
}
