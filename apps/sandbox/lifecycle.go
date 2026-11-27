package sandbox

// lifecycle.go — WHEN a sandbox ends, and the one function that decides it.
//
// THE BUG THIS REPLACES. There was one number, `idleAfter = 1h`, and `LastUsedAt`
// was stamped in exactly one place: an fs or exec call. So the reaper could not
// tell these two people apart, and got both of them wrong:
//
//	somebody reading a diff for twenty minutes  — looks idle. It is not idleness.
//	                                              Reading is what the tool is FOR.
//	a tab closed twenty minutes ago             — looks busy for another forty,
//	                                              holding a pod for nobody.
//
// One flat number cannot be right for both, because they are not one question.
// "How long may a sandbox go untouched" has a different answer depending on
// whether anyone is looking at it, and until now nothing in this process could
// answer whether anyone was.
//
// SO THERE ARE THREE CLOCKS, and a sandbox ends at whichever runs out first:
//
//	connected     someone has this project open. Generous, because the sandbox is
//	              doing its job while its owner reads — a lease that ends under
//	              somebody's cursor costs them a warm pod for no saving at all.
//	disconnected  nobody is watching. SHORT, because the pod is the expensive half
//	              and the volume — the part that holds the work — survives the
//	              reap. Fifteen minutes is generous for a page refresh or a flaky
//	              tunnel, and cheap because coming back costs a pod and not a
//	              checkout.
//	absolute      the ceiling, regardless of either. It is what makes the other two
//	              safe to trust: a presence stamp that somehow never went stale, a
//	              stream wedged open by a proxy, a caller asking for a day-long
//	              lease — none of them can hold a pod past this.
//
// ALL THREE ARE CONFIGURABLE and every default is written down below with what it
// is measured against. They are read ONCE, at construction, so a sweep cannot see
// two different policies halfway through.
//
// WHY THE REAP IS SAFE TO DO AGGRESSIVELY. `end` stops the POD and keeps the
// VOLUME (reap.go). The volume is per-(org, project), it outlives every pod that
// ever mounted it, and the next lease for that project attaches the same disk —
// so a reaped session resumes with its checkout, its node_modules and its
// half-finished edit exactly where they were. That is the property the short
// clock spends, and it is proven live in live_reap_test.go rather than asserted
// here.

import (
	"fmt"
	"time"

	"github.com/hanzoai/cloud/sdk/env"

	"github.com/hanzoai/cloud/client"
)

// The defaults, each in the unit it is argued in.
const (
	// connectedIdle is how long a WATCHED sandbox may go untouched. An hour,
	// which is also the longest single command the runtime allows: a sandbox
	// quiet for longer than its own maximum command is not being read, it is
	// abandoned by someone who left the tab open.
	connectedIdleDefault = time.Hour

	// disconnectedIdle is the same allowance once the stream has dropped. Fifteen
	// minutes: long enough for a refresh, a redeploy of the page, or a train
	// tunnel; short enough that a closed tab costs one quarter-hour of a node
	// instead of a full hour of one.
	disconnectedIdleDefault = 15 * time.Minute

	// absoluteDefault is the ceiling on a sandbox's whole life, measured from
	// CREATION and regardless of any activity — so no amount of work turns a
	// sandbox into a permanent resident. A day.
	//
	// It is ONE fact with three readers, which is the whole reason it lives here:
	// the create clamps a requested lease to it (capTTL) so the row tells the
	// truth about how long it will live, the busy-lease extension in reap.go
	// bounds its push by it, and the sweep ends anything past it. Those were
	// `maxTTL` in runtime.go and `maxLifetime` in reap.go — the same day written
	// twice, in two units, either of which could be changed alone.
	absoluteDefault = 24 * time.Hour

	// parkHoldDefault is how long a temporary parked scratch sandbox is kept
	// before the reaper retires it, measured from the park. Configurable via
	// SANDBOX_PARK_HOLD_SEC.
	parkHoldDefault = 24 * time.Hour
)

// parkHold is the default hold window for temporary parked scratch sandboxes.
const parkHold = parkHoldDefault

// failedParkHold is how long a park the REAPER took is kept. The reaper writes
// why into the row's error, and that mark is this clock: a failed or abandoned
// run is retired on the next sweep, ahead of a pause someone asked for, which
// leaves the error empty and keeps parkHold.
const failedParkHold = 2 * time.Minute

// presenceGrace is how stale a presence stamp may be before the sandbox counts as
// disconnected.
//
// DERIVED, not chosen: a watcher restamps on every beat of its own heartbeat
// (client.AttachEvery), so the only question is how many beats may be lost to a
// slow hop before we stop believing it. Three — one beat late is ordinary, two is
// a hiccup, three in a row means the stream is gone. There is no separate knob,
// because a grace that could disagree with the cadence that feeds it is a knob
// whose only settings are "wrong" and "the same as this".
const presenceGrace = 3 * client.AttachEvery

// clocks is the whole lifetime policy, read once.
//
// A struct rather than three package constants because it is READ once at
// construction and passed to the sweep: constants would be read per row, which is
// how a `SANDBOX_IDLE_*` changed by a config reload becomes visible to half a
// pass and not the other half.
type clocks struct {
	connected    time.Duration
	disconnected time.Duration
	absolute     time.Duration
	parkHold     time.Duration
	// maxSpend caps what one paid lease may cost its payer, in cents
	// (SANDBOX_MAX_SPEND_CENTS). Zero is no cap: the wallet is the only ceiling.
	maxSpend int64
}

// newClocks reads the policy from the environment, in seconds — the same spelling
// every other duration in this package already uses (SANDBOX_START_TIMEOUT_SEC,
// SANDBOX_EXEC_TIMEOUT_SEC). One form, so nobody has to remember which knobs take
// `15m` and which take `900`.
func newClocks() clocks {
	return clocks{
		connected:    envDur("SANDBOX_IDLE_CONNECTED_SEC", connectedIdleDefault),
		disconnected: envDur("SANDBOX_IDLE_DISCONNECTED_SEC", disconnectedIdleDefault),
		absolute:     envDur("SANDBOX_MAX_LIFE_SEC", absoluteDefault),
		parkHold:     envDur("SANDBOX_PARK_HOLD_SEC", parkHoldDefault),
		maxSpend:     int64(atoiOr(env.Or("SANDBOX_MAX_SPEND_CENTS", ""), 0)),
	}
}

func envDur(key string, def time.Duration) time.Duration {
	return time.Duration(atoiOr(env.Or(key, ""), int(def/time.Second))) * time.Second
}

// watched reports whether somebody currently has this sandbox's project open.
//
// It is a fact with an EXPIRY rather than a flag with a matching clear, and that
// is the whole reason a wedged stream cannot pin a pod. A flag needs someone to
// turn it off, and the cases that matter — the agents process restarting, a
// half-open TCP connection, a plane call lost on the way back — are exactly the
// cases where nobody is left to do it. A stamp that must be renewed fails the
// right way on its own.
func (c clocks) watched(m Sandbox, now time.Time) bool {
	return m.ConnectedAt > 0 && now.Sub(time.Unix(m.ConnectedAt, 0)) <= presenceGrace
}

// over reports whether this sandbox's time is up, and WHICH clock ran out.
//
// The reason is returned, not logged here, because it is the same string the
// reaper writes on the row's last line and the only thing an operator has when
// asked "why did my sandbox go away". A boolean would have made every reap look
// identical in the log, which is how "idle" and "your lease was over" become one
// unexplainable disappearance.
//
// Order is deliberate: the ABSOLUTE cap is asked first, so a bug in the presence
// path can delay a reap by at most the ceiling and never past it.
func (c clocks) over(m Sandbox, now time.Time) (bool, string) {
	// A FAILED START IS OVER, AND IT IS OVER FIRST, because it is the one state
	// with no future: nothing retries it, no route resumes it (Lease mints a fresh
	// sandbox rather than returning a row in error), so no clock below can ever
	// make it live again.
	//
	// It is a leak until something says so. `start` gives up WAITING for the pod;
	// it does not stop the pod. So a container image that pulls for longer than
	// SANDBOX_START_TIMEOUT_SEC leaves a pod that comes up afterwards and runs the
	// caller's code — unbilled, because `holding` reads pending|running and this
	// row is neither; uncounted, because the per-class ceiling reads the same
	// predicate; and, worst, PROTECTED from the orphan sweep, which skips any pod
	// whose id a row still claims. Three defences that each read "not running" as
	// "not there". Ending the row here is what puts the pod back in reach of the
	// one thing that can stop it.
	//
	// There is no grace. A grace would be time for a state to change, and this one
	// does not change; the pod is stopped and the row is deleted at the next sweep,
	// which is at most a minute after the start gave up. What the OPERATOR needs —
	// which image was asked for and what the cluster said about it — is not lost
	// with the row: `start` records the reason and answers 503 with it in the same
	// breath, so the caller has it, and the failure is a log line either way.
	if m.Status == statusError {
		return true, "start-failed"
	}
	// A PARKED SANDBOX HAS ONE CLOCK, and it is asked before the three below
	// because each of them is about a pod and a parked sandbox has none: its idle
	// allowance would end a park fifteen minutes after it was taken, and its lease
	// and its ceiling are renewed by the resume that gives it a pod again (unpark).
	// Park stamps LastUsedAt, and nothing stamps a parked row after that.
	//
	// TWO HOLDS. A pause someone asked for leaves Error empty and keeps parkHold,
	// the "back after lunch" day. A park the reaper took records why in Error, and
	// that mark is the short clock: a failed or abandoned run is retired on the
	// next sweep, ahead of every pause.
	if parked(m) {
		// A desktop sandbox is long-running suspend/resume: it keeps its disk and
		// stays parked indefinitely without compute billing until the user resumes it.
		if m.Class == "desktop" && m.Error == "" {
			return false, ""
		}
		hold := c.parkHold
		if hold <= 0 {
			hold = parkHold
		}
		if m.Error != "" {
			hold = failedParkHold
		}
		if now.Sub(time.Unix(m.LastUsedAt, 0)) > hold {
			return true, "parked"
		}
		return false, ""
	}
	// A desktop sandbox can run indefinitely while covered by its wallet.
	if end := c.ends(m); end > 0 && now.After(time.Unix(end, 0)) {
		return true, "max-life"
	}
	if m.ExpiresAt > 0 && now.Unix() > m.ExpiresAt {
		return true, "lease-expired"
	}
	// WHAT THE CLOCK RUNS FROM is not the same question in both branches, and
	// collapsing them is how one of the two clocks stops existing.
	//
	// WATCHED: from the last COMMAND. Presence chooses the generous allowance and
	// that is all it does — an hour is already longer than any single command the
	// runtime allows, so somebody reading is inside it and somebody who left a tab
	// open on a sandbox they stopped using is not. Running it from the beat
	// instead would reset it every 25 seconds, and a clock a heartbeat rewinds is
	// not a clock: `connected` would be unreachable and the knob for it inert.
	//
	// NOT WATCHED: from the LATER of the command and the last beat, because the
	// short allowance is measured from the moment they LEFT. A refresh or a train
	// tunnel gets its fifteen minutes from the disconnect; taking it from the last
	// command would reap someone who read for twenty minutes and then blinked.
	since := m.LastUsedAt
	if !c.watched(m, now) {
		if m.ConnectedAt > since {
			since = m.ConnectedAt
		}
		if since > 0 && now.Sub(time.Unix(since, 0)) > c.disconnected {
			return true, "idle-disconnected"
		}
		return false, ""
	}
	if since > 0 && now.Sub(time.Unix(since, 0)) > c.connected {
		return true, "idle-connected"
	}
	return false, ""
}

// ends is when the sweep ends the sandbox whatever it is doing, in Unix seconds: the absolute
// ceiling from its creation. A desktop sandbox has none, and neither has a row that records no
// creation, and both answer 0. It is the one reading of the ceiling by the sweep (over) and by
// the caller the lease tells it to (Leased.EndsAt).
func (c clocks) ends(m Sandbox) int64 {
	if m.Class == "desktop" || c.absolute <= 0 || m.CreatedAt <= 0 {
		return 0
	}
	return m.CreatedAt + int64(c.absolute/time.Second)
}

// costCeiling is the reason a paid lease ends for MONEY: its payer's wallet would
// not carry it another hour, its org's budget had no room for one, or it reached
// the per-lease cap (maxSpend).
const costCeiling = "cost-ceiling"

// purse is what the sweep can find out about a lease's money. Each answer is
// asked lazily, because each may be a round trip to the finance plane and most
// rows in a sweep never need any of them.
type purse struct {
	// hour is what one more hour of the lease costs, in cents (hourly).
	hour func() int64
	// short reports that the payer's balance was READ and does not cover that hour.
	// A balance that could not be read is not short: an outage of the finance plane
	// is not a verdict on the wallet.
	short func() bool
	// budget reports that the org's ledger ANSWERED and refused that hour at the
	// org's budget (its enforced spend cap). A ledger that did not answer is not a
	// verdict on the budget.
	budget func() bool
}

// closes is over with the money asked: whether the sweep ends this lease, the reason
// it carries, and a line of detail for whoever asks why afterwards.
//
// A PAID LEASE THAT RUNS OUT HAS RUN OUT OF MONEY. The sweep carries a paid lease
// into its next hour whenever the wallet covers it and its org's budget has room
// (reap.go), so one that reaches its own expiry is one that one of the two stopped
// carrying. Reading that as `lease-expired` told the run it lost its sandbox to a
// clock, when it was a ceiling; it is `cost-ceiling` when the balance was read and
// was short, or the ledger answered that the budget is spent, and the detail says
// which, so the run is told to top up a wallet or raise a budget. It stays
// `lease-expired` when neither could be read.
//
// THE CAP IS ASKED LAST, so a lease that is over for any other reason keeps that
// reason. A parked lease bills only its disk and has its own hold (over), and a
// free lease bills nobody, so neither is held to it.
func (c clocks) closes(m Sandbox, now time.Time, p purse) (bool, string, string) {
	if done, why := c.over(m, now); done {
		if why == "lease-expired" && m.Payer != "" {
			if p.short != nil && p.short() {
				return true, costCeiling, "its payer's balance does not cover another hour"
			}
			if p.budget != nil && p.budget() {
				return true, costCeiling, "its org's budget does not cover another hour"
			}
		}
		return true, why, ""
	}
	if c.maxSpend > 0 && m.Payer != "" && !parked(m) && p.hour != nil {
		if got := spent(m, p.hour(), now); got >= c.maxSpend {
			return true, costCeiling, fmt.Sprintf("it cost %d cents, at or past its cap of %d", got, c.maxSpend)
		}
	}
	return false, "", ""
}

// spent is what a lease has cost its payer since it was taken, in cents: the
// one-shot fee, and its hourly price for the time it has held a pod, rounded UP so
// the cap is never a cent late. CreatedAt is renewed by a resume, which charges the
// fee again (unpark), so this is the spend of THIS lease and a cap is per lease.
func spent(m Sandbox, hour int64, now time.Time) int64 {
	cents := ResourceFee(m.Class)
	if m.CreatedAt <= 0 || hour <= 0 {
		return cents
	}
	held := now.Unix() - m.CreatedAt
	if held <= 0 {
		return cents
	}
	return cents + (hour*held+3599)/3600
}

// capTTL bounds a requested lease by the absolute ceiling, so the row TELLS THE
// TRUTH about how long it will live.
//
// Clamping here as well as checking at the sweep is not two rules. It is one rule
// applied at the two moments it can be observed: `expiresAt` in a create response
// is a promise, and a promise longer than the ceiling is a lie the client plans
// around. The sweep still checks, because lowering the ceiling has to reach the
// sandboxes that are already running.
//
// This REPLACES a separate `maxTTL` constant. "The longest a sandbox may live"
// was written twice — once as a cap on the request, once as the reaper's
// business — and two homes for one fact is how they end up disagreeing.
func (c clocks) capTTL(sec int) int {
	if max := int(c.absolute / time.Second); c.absolute > 0 && sec > max {
		return max
	}
	return sec
}
