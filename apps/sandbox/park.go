package sandbox

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/activation"
	"github.com/hanzoai/cloud/sdk/meter"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/hanzoai/account"
	"github.com/zap-proto/zip"
)

// park.go is how a sandbox stops costing compute without ending.
//
// A lease had two outcomes before this: it ran, or it was retired and gone. So
// somebody who wanted to keep a workspace overnight had one choice that kept it
// — keep the pod — and a pod is the whole cost. The volume already outlives the
// pod (store.go: "a dev sandbox keeps its work across leases through it"), which
// means the expensive half and the durable half were already separable and only
// the states were missing.
//
// WHAT PARKING PRESERVES, EXACTLY. The disk, through the volume. Not the memory:
// a pod cannot be suspended, so parking stops it and resuming starts a new one.
// A process the caller left running does not survive, and this is the one place
// that difference is written down — an agent that parks mid-build resumes to a
// tree, not to a build.
//
// Parking is EXPLICIT on both sides. A parked sandbox refuses exec and file
// operations rather than waking on them, because a caller who parked one meant
// to stop paying and an operation that silently relet it would be a charge they
// did not ask for. Auto-waking on the next operation is a separate state and is
// not this: it has to be decided at every entry point, including the two
// WebSocket surfaces, and doing it here would leave half of them silently awake.
//
// THE MONEY FALLS OUT. holding() (meter.go) reads pending|running, so a parked
// row is not billed for compute by construction — no second rule, and nothing to
// keep equal. The tail owed up to the moment of parking is settled here, in the
// same one settlement retire() uses, because a watermark that is not made durable
// with the state change is a window where the next pass bills the parked seconds;
// and a resume moves that watermark over the parked span, for the same reason.
//
// HOW LONG. A parked sandbox is kept for parkHold (lifecycle.go) and then retired
// like any lease whose time is up — with the disk, when the disk was the lease's
// own. An abandoned park costs its disk for at most that window, and nothing after.

// The states a sandbox is in. One vocabulary, because these were bare strings in
// nine places and a misspelling in any of them reads as "not running", which
// three separate defences treat as "not there" (api.go).
const (
	// statusPending is the pod coming up. It holds capacity and is not yet usable.
	statusPending = "pending"
	// statusRunning is the pod up and able to take work. The only state that
	// answers an exec or mints an interactive ticket.
	statusRunning = "running"
	// statusParked is no pod, volume kept, nothing billed for compute. Only a
	// resume brings it back — POST /:id/resume, or a lease that names it; every
	// other operation is refused.
	statusParked = "parked"
	// statusError is a pod that could not come up. The row is the evidence and
	// Error is the reason; the reaper takes it.
	statusError = "error"
)

// parked reports whether this row is holding a volume and no pod.
func parked(m Sandbox) bool { return m.Status == statusParked }

// park stops the pod, keeps the row and the volume, and settles the tail: POST
// /v1/sandbox/:id/pause.
//
// The claim, the stop and the ship are ONE settlement for the same reason a
// retirement is — the snapshot that carries the advanced watermark has to carry
// the new state, or a successor hydrates a running row for a pod that is gone
// and bills for it until the reaper notices.
func (o ops) park(ctx context.Context, in *sandboxRef) (*Sandbox, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	m, err := Park(o.s, ctx, org, in.ID)
	if err != nil {
		return nil, err
	}
	return &m, nil
}

// Park is the park itself, for the org the adapter validated: the resource
// surface's pause above and the plane's sandbox_park both land here.
func Park(s *Service, ctx context.Context, org, id string) (Sandbox, error) {
	m, store, err := find(s, ctx, org, strings.TrimSpace(id), "park")
	if err != nil {
		return Sandbox{}, err
	}
	return park(s, ctx, store, m, "")
}

// park parks m: the one park, for a caller who holds it (Park) and for the
// reaper, which parks a sandbox whose own disk would otherwise go with its pod
// (reap.go). why is why the reaper took the park. An explicit pause passes ""
// and the row keeps parkHold. A reaper park records why on the row, and that
// mark is the short clock (lifecycle.go): a failed or abandoned run is the
// first thing the next sweep retires, ahead of a pause someone asked for.
func park(s *Service, ctx context.Context, store *Store, m Sandbox, why string) (Sandbox, error) {
	if parked(m) {
		return m, nil // already parked: asking twice is the same answer
	}
	if m.Status == statusError {
		return Sandbox{}, zip.Errorf(http.StatusConflict, "sandbox %s never came up, so there is nothing to park", m.ID)
	}
	ns, err := principal.OrgNamespace(m.Org, "")
	if err != nil {
		return Sandbox{}, zip.Errorf(http.StatusInternalServerError, "the lease names no namespace: %v", err)
	}

	was := m
	m.Status = statusParked
	m.LastUsedAt = time.Now().Unix()
	// For desktop sandboxes, suspend/park is ordinary suspend-to-disk so Error stays clean.
	// For other sandboxes, why marks the short clock if reaper-taken.
	if m.Class != "desktop" && why != "" {
		m.Error = why
	} else {
		m.Error = ""
	}

	// BOTH meters, and this is the whole point of the state: the runtime span ends
	// here and the DISK span does not. A park that settled only the runtime would
	// hand out a volume for nothing, which is what the first version of this did.
	committed := false
	commit := func() (bool, error) {
		committed = true
		if !s.State.stores.Owned(ns) {
			return false, zip.Errorf(http.StatusConflict, "sandbox: %s is no longer this replica's to park", ns)
		}
		// A POD THAT DID NOT STOP IS NOT PARKED. A parked row answers to one clock
		// and bills no compute, so writing it over a pod still running would hand
		// out that pod for a day, unbilled — the row stays as it was, billed and
		// reaped as it was, and the caller is told.
		if serr := s.State.rt.stop(ctx, was); serr != nil {
			return false, zip.Errorf(http.StatusBadGateway, "park sandbox %s: its pod did not stop: %v", m.ID, serr)
		}
		// From running or pending and nothing else: a lease that ended while this
		// stopped its pod is not brought back as a parked row.
		ok, perr := store.Shift(ctx, m, statusRunning, statusPending)
		if perr != nil {
			return false, perr
		}
		if !ok {
			return false, zip.Errorf(http.StatusConflict, "sandbox %s changed while it was being parked", m.ID)
		}
		return s.State.stores.Sync(ns)
	}
	if _, serr := cloud.RuntimeSweep(ctx, spans(ctx, was), time.Now().Unix(),
		advance(store, m.Org),
		commit, s.State.debit,
	); serr != nil {
		return Sandbox{}, refused(serr, "park sandbox")
	}
	// A row that owed nothing is still parked — RuntimeSweep ships no pass that
	// claimed nothing, rightly, so the state change is taken directly whenever the
	// sweep did not take it (retire says when that is).
	if !committed {
		if _, cerr := commit(); cerr != nil {
			return Sandbox{}, refused(cerr, "park sandbox")
		}
	}
	return m, nil
}

// refused is err as the caller is answered: a refusal already spoken in zip's
// words keeps its status, and anything else is this server's fault.
func refused(err error, what string) error {
	if he, ok := errors.AsType[*zip.HTTPError](err); ok {
		return he
	}
	return zip.Errorf(http.StatusInternalServerError, "%s: %v", what, err)
}

// resume gives a parked sandbox a pod again: POST /v1/sandbox/:id/resume.
//
// The pod is NEW and so is its lease: a fresh pod name, because a pod name is
// never reused (store.go), the volume the row already names, a lease of the
// class's own length from now, and the RESUMING caller's credential rather than
// the one that took the lease — a session is short-lived and the one that parked
// it is gone.
func (o ops) resume(ctx context.Context, in *sandboxRef) (*Sandbox, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	clock := activation.Start()
	m, store, err := find(o.s, ctx, org, strings.TrimSpace(in.ID), "resume")
	if err != nil {
		return nil, err
	}
	if m.Status == statusRunning {
		clock.Is(activation.Warm, true)
		activated(ctx, clock, org, m, nil)
		return &m, nil // already running: asking twice is the same answer
	}
	if !parked(m) {
		return nil, zip.Errorf(http.StatusConflict,
			"sandbox %s is %s, and only a parked sandbox resumes", m.ID, m.Status)
	}
	clock.Is(activation.Cold, true)
	c, _ := cloud.Request(ctx)
	back, err := unpark(o.s, ctx, store, m, cloud.PayerOf(ctx).Wallet, principal.Super(c), cloud.CallerBearer(c), 0)
	if err != nil {
		activated(ctx, clock, org, m, err)
		return nil, err
	}
	activated(ctx, clock, org, back, nil)
	return &back, nil
}

// unpark is the one door from a park back to compute. The resume above and a
// lease that names a parked sandbox (Lease) both come through it, so a pod is
// never started on a parked disk by any rule but this one.
//
// It asks what a lease asks before a pod starts, because a resume IS a lease of
// compute: whether the org has room for the pod (room), whether the wallet covers
// its first hour (afford), and the lease fee. Resuming without them would be the
// way around all three — take a lease while funded, park it, and resume it for as
// long as you like after the balance is gone. See prepaid.go. The wallet is the
// RESUMING caller's (payer), exactly as a lease's is, and it pays from here on.
//
// ONE RESUME WINS. The parked row is taken by a compare-and-set before anything
// starts, so resumes that race — a person and a run, or a burst of requests —
// start one pod between them and the rest are refused; each pod has a fresh name,
// so nothing else would have noticed the second. The row is let go the same way:
// to running only from the pending this call wrote, back to parked only from it.
//
// A NEW LEASE, from now: ttl as the lease asks for it, or the class's own, bounded
// by the same ceiling. CreatedAt moves with it, because the ceiling on a
// sandbox's life is a ceiling on a POD's, and this is a new pod — a resume late in
// the day would otherwise hand back a sandbox the reaper ends minutes later,
// taking the disk it was resumed for.
//
// THE PARKED SPAN IS NOT COMPUTE. Parking settled the runtime meter up to the
// moment it stopped the pod, and nothing has run since, so the runtime watermark
// is moved over the parked span here, billing nothing: the first tick after a
// resume bills from the resume, not from the park. The disk's watermark is left
// where it is — the disk existed the whole time, and it is billed for all of it.
//
// A RESUME BY SOMEBODY ELSE HANDS THE POD NOTHING OF THEIRS. An admin may give a
// member's parked sandbox a pod again, and that pod is the member's to exec in:
// whatever credential it starts with, the member can read. So the resumer's own
// session, and a SuperAdmin's fleet kubeconfig, go only into a sandbox they
// leased themselves; into anybody else's the pod starts holding nothing.
func unpark(s *Service, ctx context.Context, st *Store, m Sandbox, payer account.Account, super bool, bearer string, ttl int) (Sandbox, error) {
	if !holderOf(ctx).leased(m) {
		super, bearer = false, ""
	}
	org, was := m.Org, m
	if err := room(ctx, st, org, projectOwner(org, m.Actor), m.Project, m.ID); err != nil {
		return Sandbox{}, err
	}
	admitted, err := afford(ctx, payer, m.Class, m.Project, diskSize(m))
	if err != nil {
		return Sandbox{}, err
	}
	if err := within(s, ctx, st, org, admitted.payer); err != nil {
		return Sandbox{}, err
	}
	// A resume starts compute exactly as a lease does, so it asks the org's ledger
	// for the same first hour (prepaid.go): a parked sandbox stays parked, disk and
	// all, while its org is past its budget.
	if err := budgeted(s, ctx, admitted, m.Class, diskSize(m)); err != nil {
		return Sandbox{}, err
	}
	// THE CREDENTIALS, resolved the way a lease resolves them (api.go). The
	// cluster ones are one branch's and read on the SAME predicate the image is,
	// so a resume hands out exactly what a lease of this class would.
	cr := fleetCred(ctx, s, org, m.Class, super)
	ttl = s.State.clk.capTTL(lifetime(ttl, m.Class, admitted))
	// An identity that cannot be minted does not cost the resume, for the reason a
	// lease gives: the pod holds nothing either way.
	sess, serr := sessionFor(ctx, bearer, time.Duration(ttl)*time.Second)
	if serr != nil {
		s.Log.Warn("no owner session to hand the resumed sandbox", "sandbox", m.ID, "err", serr)
	}
	cr.session = sess

	pod, err := genID()
	if err != nil {
		return Sandbox{}, zip.Errorf(http.StatusInternalServerError, "id: %v", err)
	}
	now := time.Now().Unix()
	m.Pod, m.Status, m.Payer = podName(pod), statusPending, admitted.payer.Subject()
	m.CreatedAt, m.LastUsedAt, m.ExpiresAt = now, now, now+int64(ttl)
	if ok, err := st.Shift(ctx, m, statusParked); err != nil {
		return Sandbox{}, zip.Errorf(http.StatusInternalServerError, "resume sandbox: %v", err)
	} else if !ok {
		return Sandbox{}, zip.Errorf(http.StatusConflict, "sandbox %s is no longer parked; it is being resumed or was ended", m.ID)
	}
	if _, err := st.Advance(ctx, org, m.ID, was.MeteredAt, now); err != nil {
		s.Log.Warn("the resumed sandbox's runtime clock was not moved over its park", "sandbox", m.ID, "err", err)
	}
	m.MeteredAt = now
	if serr := s.State.rt.start(ctx, m, cr); serr != nil {
		// The pod is the cost and it is released; the row is the evidence and it
		// goes back to parked as it was, so the caller can try again rather than
		// losing the volume.
		if terr := s.State.rt.stop(ctx, m); terr != nil {
			s.Log.Warn("the sandbox could not resume and its pod could not be stopped",
				"sandbox", m.ID, "start", serr, "stop", terr)
		}
		if _, perr := st.Shift(ctx, was, statusPending); perr != nil {
			s.Log.Warn("the sandbox could not resume and was not put back", "sandbox", m.ID, "err", perr)
		}
		return Sandbox{}, zip.Errorf(http.StatusServiceUnavailable, "resume sandbox: %v", serr)
	}
	m.Status = statusRunning
	if ok, err := st.Shift(ctx, m, statusPending); err != nil || !ok {
		// Parked or ended while the pod came up: that has the last word.
		if terr := s.State.rt.stop(ctx, m); terr != nil {
			s.Log.Warn("the sandbox changed while it resumed and its pod could not be stopped",
				"sandbox", m.ID, "err", terr)
		}
		if err != nil {
			return Sandbox{}, zip.Errorf(http.StatusInternalServerError, "resume sandbox: %v", err)
		}
		return Sandbox{}, zip.Errorf(http.StatusConflict, "sandbox %s was parked or ended while it resumed", m.ID)
	}
	if !admitted.payer.Zero() {
		s.Bill.Record(admitted.payer, "sandbox", meter.Usage{
			Model:       m.Class + "/" + m.Runtime,
			AmountCents: ResourceFee(m.Class),
		})
	}
	if serr := s.State.rt.signIn(ctx, m, cr.session); serr != nil {
		s.Log.Warn("the resumed sandbox could not take the owner session", "sandbox", m.ID, "err", serr)
	}
	return m, nil
}
