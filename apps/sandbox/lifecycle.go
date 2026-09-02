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
	"github.com/hanzoai/cloud/internal/environ"
	"time"

	"github.com/hanzoai/cloud/plane"
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
)

// presenceGrace is how stale a presence stamp may be before the sandbox counts as
// disconnected.
//
// DERIVED, not chosen: a watcher restamps on every beat of its own heartbeat
// (plane.AttachEvery), so the only question is how many beats may be lost to a
// slow hop before we stop believing it. Three — one beat late is ordinary, two is
// a hiccup, three in a row means the stream is gone. There is no separate knob,
// because a grace that could disagree with the cadence that feeds it is a knob
// whose only settings are "wrong" and "the same as this".
const presenceGrace = 3 * plane.AttachEvery

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
	}
}

func envDur(key string, def time.Duration) time.Duration {
	return time.Duration(atoiOr(environ.Or(key, ""), int(def/time.Second))) * time.Second
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
	if m.Status == "error" {
		return true, "start-failed"
	}
	if c.absolute > 0 && m.CreatedAt > 0 && now.Sub(time.Unix(m.CreatedAt, 0)) > c.absolute {
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
