package train

// money.go — what a job's compute costs, and how a run is kept inside what its
// payer can pay (HIP-1333 §10).
//
// WHO PAYS. A job that names none of the org's machines is run by Hanzo's
// executor and its compute is charged by the device-second to the job's payer,
// the wallet that created it. A job on the org's own machines is the org's
// hardware: its seconds are counted and its caps hold, and it is charged nothing.
//
// HOLD A WINDOW, CHARGE WHAT RAN. A task may use only the device-seconds held for
// it. A claim holds the first window, windowSeconds of wall time on each of its
// devices. The executor reports at least every reportEvery seconds, and each
// report meters the task by the clock: its devices times the seconds since it was
// claimed, no more than is held, each report charged what that adds. Once less
// than extendBelow is left the next window is held. A window is held only while
// the job's own caps (resources.budget, resources.max_seconds) and the payer's
// balance and spend caps allow it, weighed with everything the payer has
// committed that the ledger does not know yet: its other holds and its charges
// not yet sent, in every org this process holds. While the next window cannot be
// held the task runs what is held, asking again at every report, and when less
// than stopBelow is left its job is stopped: it ends stopped with the reason, and
// the report's answer says cancel, on which the executor kills its trainer. So
// nothing is charged past what is held, and an executor that reports on time and
// obeys cancel runs nothing past it. A task that goes silent is charged through
// its last report, and what it held beyond that is released when its lease
// lapses.
//
// ONE PAYER, ONE GATE AT A TIME. Weighing a window and holding it are two steps,
// so a payer's are serialized across every replica (gate): two claims or reports for
// one wallet cannot both weigh the same balance before either holds.
//
// CHARGES GO THROUGH AN OUTBOX. A report's seconds and the charge they owe are
// written in one transaction (store.use); the outbox is handed to the meter only
// once the org's file has shipped (settle), and emptied after. A report whose
// file does not ship keeps its charge, and the next ship sends it; the ledger
// knows each charge by its ref, so one sent twice moves money once.
//
// THE HOLDS ARE THE GATE'S, NOT THE LEDGER'S. finance.Holder sets money aside in
// the ledger, but only a payment (finance.Draw) is paid from it, and a payment is
// not metered usage: it fires no usage hook and counts toward no spend cap. A
// job's compute is metered usage, so its windows are held the way the prepaid
// gate holds an in-flight call (cloud.Meter.Reserve): committed, then weighed
// with Authorize, and kept durably in the org's train.db because a job outlives
// any one process.
//
// The price of a device-hour is a row in commerce's meter authority, per base
// model and device class (kai-gpu-hour, kai-cpu-hour), read through
// cloud.RateMicros with the compiled floor below when absent.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"

	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/dialect"
	"github.com/hanzoai/cloud/sdk/meter"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/hanzoai/account"
	"github.com/hanzoai/namespace"
	"github.com/zap-proto/zip"
)

const (
	// reportEvery is the most seconds an executor may let pass between reports:
	// train serve's heartbeat.
	reportEvery int64 = 20
	// windowSeconds is the wall time one hold covers on each of a task's devices.
	windowSeconds int64 = 300
	// extendBelow: a task with less wall time than this held on each device asks
	// for its next window, again at every report until one is held.
	extendBelow int64 = 180
	// stopBelow: a task whose next window cannot be held runs what is held until
	// less than this is left on each device, then its job is stopped. Two
	// intervals, so the report that says cancel arrives before the hold runs out.
	stopBelow = 2 * reportEvery
)

// gpuHourFloor is $3.48 a GPU-hour: one H100, the published price of the
// "GPU Basic" row (@hanzo/plans gpu.json). TestTheComputeFloorIsTheCatalogs pins it.
const gpuHourFloor int64 = 3_480_000

// cpuHourFloor is a CPU device-hour at the platform's one price for an hour of a
// default compute envelope.
const cpuHourFloor = cloud.RuntimeHourMicros

// class is a device kind's price class: cpu, or gpu for every accelerator.
func class(device string) string {
	if device == "cpu" {
		return "cpu"
	}
	return "gpu"
}

// meterName names a base's device-hour in the meter authority and on the
// invoice: kai-gpu-hour.
func meterName(base, class string) string { return base + "-" + class + "-hour" }

// rate is what one device-hour of base on a class costs, in micro-USD.
func rate(ctx context.Context, base, class string) int64 {
	floor := gpuHourFloor
	if class == "cpu" {
		floor = cpuHourFloor
	}
	return cloud.RateMicros(ctx, "train", meterName(base, class), floor)
}

// hanzo reports whether Hanzo's executor runs a job: it names none of the org's
// machines.
func hanzo(s Spec) bool { return len(s.Resources.Machines) == 0 }

// opening is the class a job's first window is priced at before any executor
// claimed it: cpu only when cpu is all it may run on.
func opening(s Spec) string {
	if len(s.Resources.Devices) == 1 && s.Resources.Devices[0] == "cpu" {
		return "cpu"
	}
	return "gpu"
}

// first is what a job's first window costs on one device, sized as a claim sizes
// it: windowSeconds clipped to max_seconds, at most the budget.
func first(ctx context.Context, s Spec) int64 {
	secs := windowSeconds
	if m := s.Resources.MaxSeconds; m > 0 {
		secs = min(secs, m)
	}
	c := cost(rate(ctx, s.BaseModel, opening(s)), secs)
	if b := s.Resources.Budget; b > 0 {
		c = min(c, b*10_000)
	}
	return c
}

// fit is the device a claim runs a job on, and how many of them: the first of
// the claimant's devices the job may run on, counted as often as the claim names
// it. "" when none fits.
func fit(claimed []string, s Spec) (string, int64) {
	allowed := s.Resources.Devices
	if len(allowed) == 0 {
		allowed = family(s.BaseModel).Devices
	}
	for _, d := range claimed {
		if slices.Contains(allowed, d) {
			var n int64
			for _, e := range claimed {
				if e == d {
					n++
				}
			}
			return d, n
		}
	}
	return "", 0
}

// payerOf is the wallet a row names, or the org's own for a row written before
// rows named one.
func payerOf(org, stored string) account.Account {
	if a := account.Parse(stored); !a.Zero() {
		return a
	}
	return account.Org(org)
}

// centsUp renders micro-USD in whole cents, rounded up: a gate never weighs less
// than it is about to admit.
func centsUp(m int64) int64 {
	if m <= 0 {
		return 0
	}
	return (m + 9_999) / 10_000
}

// affordable is the most device-seconds a task already holding held can add at
// rate without their cost passing left micro-USD.
func affordable(rate, held, left int64) int64 {
	if left <= 0 {
		return 0
	}
	e := left * 3600 / rate
	for e > 0 && cost(rate, held+e)-cost(rate, held) > left {
		e--
	}
	return e
}

// verdict is an extension's answer: the device-seconds held, or, when none, why
// — whether that was an answer at all or the payer's balance could not be read,
// and whether the payer's money is held by its other work.
type verdict struct {
	seconds int64
	reason  string
	unknown bool
	busy    bool
}

// Why a job stops, as its stop and its error say.
const (
	stopBudget  = "budget"
	stopSeconds = "max_seconds"
	stopFunds   = "insufficient_balance"
	stopCap     = "spend_cap"
	stopUnread  = "balance_unavailable"
	stopNoPayer = "no_payer"
)

// refusal names what refused a window, and whether it was the balance that could
// not be read rather than an answer.
func refusal(err error) (string, bool) {
	switch {
	case errors.Is(err, meter.ErrInsufficientBalance):
		return stopFunds, false
	case errors.Is(err, meter.ErrSpendCapExceeded):
		return stopCap, false
	case errors.Is(err, cloud.ErrNoLedger):
		return stopNoPayer, false
	}
	return stopUnread, true
}

// extend asks for task t's next window: windowSeconds on each of its devices,
// clipped to what the job's max_seconds and budget leave, then — when the task is
// priced — weighed against the payer's balance and spend caps together with
// everything the payer has committed that the ledger does not know (holding).
// The caller holds the payer's lock.
func (o ops) extend(ctx context.Context, st *store, org string, r row, spec Spec, t task) (verdict, error) {
	want := windowSeconds * max(t.Devices, 1)
	secs, micros, err := st.committed(ctx, r.ID)
	if err != nil {
		return verdict{}, err
	}
	if m := spec.Resources.MaxSeconds; m > 0 {
		if want = min(want, m-secs); want <= 0 {
			return verdict{reason: stopSeconds}, nil
		}
	}
	if b := spec.Resources.Budget; b > 0 && t.Rate > 0 {
		e := affordable(t.Rate, t.Held, b*10_000-micros)
		if e <= 0 {
			return verdict{reason: stopBudget}, nil
		}
		want = min(want, e)
	}
	if t.Rate > 0 {
		payer := payerOf(org, r.Payer)
		out, err := o.holding(ctx, payer.String())
		if err != nil {
			o.s.Log.Warn("train: the payer's holds cannot be read", "org", org, "job", r.ID, "err", err)
			return verdict{reason: stopUnread, unknown: true}, nil
		}
		need := out + cost(t.Rate, t.Held+want) - cost(t.Rate, t.Held)
		if err := o.s.State.afford(ctx, payer, r.Project, r.Validated, need); err != nil {
			reason, unknown := refusal(err)
			return verdict{reason: reason, unknown: unknown, busy: out > 0}, nil
		}
	}
	return verdict{seconds: want}, nil
}

// holding is what payer has committed that the ledger does not know yet, in
// micro-USD: what its live priced tasks hold beyond what they used, and its
// charges not yet sent, summed over every org this process holds — one wallet
// pays in several orgs (a SuperAdmin's own books), and each org's file holds
// only its own. An org whose file cannot be read makes the sum unknown.
func (o ops) holding(ctx context.Context, payer string) (int64, error) {
	var total int64
	var failed error
	_ = o.s.State.stores.Each(func(_ namespace.Namespace, st *store, err error) {
		if failed != nil {
			return
		}
		if err != nil {
			failed = err
			return
		}
		n, err := st.outstanding(ctx, payer)
		if err != nil {
			failed = err
			return
		}
		total += n
	})
	return total, failed
}

// charge is one debit, counted with the seconds it bills and sent once the
// store has shipped them.
type charge struct {
	payer account.Account
	usage meter.Usage
}

// use meters task t at now: its devices times the seconds since it was claimed,
// no more than is held, written with the charge for what that adds. The count is
// the clock's, so a report sent twice adds nothing the second time.
func (o ops) use(ctx context.Context, st *store, org string, r row, t task, now int64) (task, error) {
	n := min(max(now-t.Claimed, 0)*max(t.Devices, 1), t.Held)
	if n <= t.Used {
		return t, nil
	}
	var c *charge
	if micros := cost(t.Rate, n) - cost(t.Rate, t.Used); micros > 0 {
		c = &charge{payer: payerOf(org, r.Payer), usage: meter.Usage{
			AmountMicros: micros,
			Model:        meterName(r.Base, t.Class),
			Project:      r.Project,
			RequestID:    "train-" + t.ID,
			Ref:          fmt.Sprintf("trc_%s_%d_%d", t.ID, t.Used, n),
		}}
	}
	won, err := st.use(ctx, t.ID, t.Used, n, c, now)
	if err != nil || !won {
		return t, err
	}
	t.Used = n
	return t, nil
}

// window keeps a running task held: with less than extendBelow of wall time held
// on each device it asks for the next window. When none can be held the task
// runs what is held, and once less than stopBelow is left its job ends stopped
// with the reason, which the report's answer turns into cancel.
func (o ops) window(ctx context.Context, st *store, org string, r row, spec Spec, t task, now int64) (task, error) {
	per := max(t.Devices, 1)
	left := t.Held - t.Used
	if left >= extendBelow*per {
		return t, nil
	}
	unlock, err := o.gate(ctx, st, payerOf(org, r.Payer))
	if err != nil {
		return t, err
	}
	defer unlock()
	v, err := o.extend(ctx, st, org, r, spec, t)
	if err != nil {
		return t, err
	}
	if v.seconds > 0 {
		won, err := st.hold(ctx, t.ID, t.Held, t.Held+v.seconds)
		if won {
			t.Held += v.seconds
		}
		return t, err
	}
	if left >= stopBelow*per {
		return t, nil
	}
	o.s.Log.Info("train job stopped: its next window cannot be held", "org", org, "job", r.ID, "reason", v.reason)
	if err := st.end(ctx, r.ID, stopped, v.reason, now); err != nil && !errors.Is(err, errTerminal) {
		return t, err
	}
	return t, nil
}

// settle ships the org's file, then hands the meter every charge in its outbox
// and empties it. A file that does not ship sends nothing: the charges wait in
// the outbox for the next ship.
func (o ops) settle(ctx context.Context, st *store, org string) error {
	if err := o.ship(org); err != nil {
		return err
	}
	cs, err := st.owed(ctx)
	if err != nil || len(cs) == 0 {
		if err != nil {
			o.s.Log.Error("train: the charges owed cannot be read", "org", org, "err", err)
		}
		return nil
	}
	refs := make([]string, 0, len(cs))
	for _, c := range cs {
		o.s.State.debit(c.payer, "compute", c.usage)
		refs = append(refs, c.usage.Ref)
	}
	if err := st.paid(ctx, refs); err != nil {
		o.s.Log.Error("train: charges sent and not cleared; the ledger takes each ref once", "org", org, "err", err)
	}
	return nil
}

// gate takes payer's money gate and answers its release: weighing a window and holding
// it are two steps, and the gate is what keeps a second payer-weighing from falling
// between them, in this process or on another replica.
//
// The gate is the payer's, so every org it pays in must reach the same lock: it is
// held on the store of the payer's own org, the one place a wallet that pays in several
// orgs (a SuperAdmin's own books) has. A file is one process, where the Keyed is the
// whole lock.
func (o ops) gate(ctx context.Context, st *store, payer account.Account) (release func(), err error) {
	db := st.db
	if dialect.Of(db).Postgres() && payer.Org() != "" {
		ns, err := principal.OrgNamespace(payer.Org(), "")
		if err != nil {
			return nil, err
		}
		home, err := o.s.State.stores.For(ns)
		if err != nil {
			return nil, err
		}
		db = home.db
	}
	release, err = o.s.State.payers.Across(ctx, db, payer.String())
	if err != nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "train: another replica holds this payer's money; retry")
	}
	return release, nil
}
