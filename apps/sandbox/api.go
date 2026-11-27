package sandbox

// api.go — the sandbox as a VALUE you can call, not an address you can only POST to.
//
// Every operation here used to exist ONLY as `func X(s *Service, c *zip.Ctx) error`.
// That braids two different things into one function: what a sandbox IS (lease it,
// run in it, read from it) and how a request reaches it (bind a body, read a param,
// pick a status). The consequence is not stylistic — it is that NOTHING ELSE IN THE
// PROCESS CAN USE A SANDBOX. apps/exec needs exactly these five verbs and could not
// have them, so it proxied to a workload nobody deployed instead.
//
// So the domain moves here and the handlers become adapters over it: bind, call,
// JSON. The same functions back the internal plane (peer.go), which is how a peer
// app composes over sandboxes without a router, a request or a socket in the way.
//
// It is functions-taking-*Service rather than methods for a reason the compiler
// enforces: `Service = cloud.Service[state]` is an ALIAS for a generic type declared
// in package cloud, and Go cannot define a method on a non-local type. apps/git's
// core/adapter split (files.go) is the same shape for the same reason, so this is
// the house form rather than a second one.
//
// ERRORS ARE zip ERRORS, in the core. Both adapters return them unchanged — the HTTP
// one to a client, the plane one to a peer — so "this sandbox is not yours" is 404
// once, decided where the fact is known, rather than twice in two vocabularies that
// have to be kept in step.

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/activation"
	"github.com/hanzoai/cloud/sdk/audit"
	"github.com/hanzoai/cloud/sdk/meter"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/hanzoai/account"

	"github.com/zap-proto/zip"
)

// Spec is what a lease asks for.
//
// ID is the RESUME: name a sandbox you already hold and Lease returns it instead of
// minting one. That is what makes "a session" expressible without a session table —
// the caller's session id IS the sandbox id, and the only store either needs is the
// one this package already keeps.
type Spec struct {
	ID      string
	Class   string
	Project string
	Image   string
	// Runtime is the isolation boundary the caller ASKS FOR, per sandbox. It was
	// deployment-wide (one env var read at startup), which made it impossible to
	// run the same task on two runtimes and compare — and impossible for a caller
	// to choose. Empty means the deployment's default.
	//
	// Asking is not getting. The server decides, in runtimeFor, and it answers a
	// request it cannot honour with a refusal rather than with a different
	// runtime — so what the sandbox got comes back on Sandbox.Runtime and the two
	// can be compared. `RuntimeClass` is the Kubernetes spelling and it stays in
	// runtime.go, where Kubernetes is spoken.
	Runtime string
	TTLSec  int
	// Cluster names one of the org's ATTACHED clusters to run the lease on,
	// empty for the home cluster. Resolved through the fleet registry before
	// anything is built, so an unknown name refuses with nothing behind it.
	Cluster string
	// Disk gives a lease with no project a disk of its own (diskName), so its
	// files survive a park. It is small, billed as storage like any disk, and
	// deleted with the lease (settle). A project lease has its project's disk.
	Disk bool
}

// Cmd is one command to run inside a sandbox. Argv is the honest form; Command is
// the convenience for a caller holding a shell line. Exactly one is required.
type Cmd struct {
	Argv       []string
	Command    string
	Stdin      string
	Dir        string
	TimeoutSec int
	// Session is where this command NARRATES. Its output is appended to that
	// session's live log as the program produces it, so a surface watching the run
	// sees the work happen instead of a blank pause with a verdict at the end.
	// Empty means nothing is watching, and then nothing is sent — see work.go.
	Session string
	// Blind are the caller's secrets, hidden from BOTH paths this command's bytes
	// leave by — the narration above and the result returned. Redaction has to
	// happen here rather than at the caller because the narration never passes
	// through the caller: see blind.go.
	Blind []string
	// Events says the command's stdout is JSON lines, each one event: narrated as
	// an `event` apiece rather than as output (work.go jsonl).
	Events bool
}

// Entry is what a path IS: a file's bytes, or a directory's entries. One read
// answers both because one shell command answers both, and a caller that had to
// stat first would pay two round trips through the apiserver to learn something
// the same command already knew.
type Entry struct {
	Path    string   `json:"path"`
	Dir     bool     `json:"dir,omitempty"`
	Data    []byte   `json:"data,omitempty"`
	Entries []string `json:"entries,omitempty"`
}

// dirExit is how the read below reports "that path is a directory" without putting
// a single byte of its own in front of the payload. stdout carries the artifact —
// a PNG, a parquet file — so a sentinel PREFIX would corrupt every binary read;
// the exit code is a channel the stream already carries and nothing else uses it,
// `sh` reserving only 0, 1, 2 and 126-255 for its own meanings.
const dirExit = 10

// Lease returns the org's sandbox named by spec.ID, or mints one.
//
// Resuming is checked against the STORE and against the row's status, so a session
// whose pod the reaper already ended comes back as a fresh sandbox rather than as a
// 502 from the first command sent into a pod that is gone.
//
// `super` is the caller's platform sudo, and it is a PARAMETER rather than a field
// on Spec or a read off the context. Both of the alternatives were worse in the
// same way. On Spec it would sit beside Class and Project — things the caller asks
// for — one refactor away from being bound off a request body, which is the whole
// hazard trust_test.go exists to catch. Read from the context it would make this
// core read identity, and the reason every function in this file takes `org` as an
// argument is that none of them may. So it arrives the way org does: named by the
// adapter that knows it, from the one predicate that answers it.
//
// `bearer` is the credential the caller authenticated WITH, and it arrives the same
// way and for the same reason. It is the subject of the token exchange that gives
// the pod its owner's identity — see cred.go — so it is a fact about who is calling,
// which is exactly what this core must be told rather than go looking for.
// ResourceFee is what one lease of `class` costs, in cents.
//
//	SANDBOX_FEE_CENTS_EXEC / _DEV / _DESKTOP / _BROWSER   per class
//	SANDBOX_FEE_CENTS                                     the fallback for all of them
//
// DEFAULTS TO FREE, and deliberately not to cloud.ResourceFeeCents, whose default
// is $1.00. Every class has been free since sandboxes existed; shipping the meter
// must not also ship a price, or the first anybody hears of it is a 402 on a
// button that worked yesterday. Turning it on is a values change.
//
// A negative or unparseable value falls through to free rather than to the
// fallback, so a typo cannot price a class by accident in either direction.
//
// The RULE is cloud.FeeCents and only the DEFAULT is local. This used to restate
// the three-line lookup so it could change that one value, which is the copy
// FeeCents was extracted to remove — the resolution order is fleet-wide, the
// number is this surface's.
func ResourceFee(class string) int64 { return cloud.FeeCents(feeEnv, class, 0) }

const feeEnv = "SANDBOX_FEE_CENTS"

// Lease hands the caller a running sandbox: the one it named, if that is still
// its own, else a new one. How long that took, and which of the three ways it
// went (activation.go), is recorded as the session's activation.
func Lease(s *Service, ctx context.Context, org string, payer account.Account, super bool, bearer string, spec Spec) (Sandbox, error) {
	clock := activation.Start()
	m, err := acquire(s, ctx, org, payer, super, bearer, spec, clock)
	if err != nil {
		activated(ctx, clock, org, Sandbox{ID: strings.TrimSpace(spec.ID), Class: strings.ToLower(strings.TrimSpace(spec.Class))}, err)
		return Sandbox{}, err
	}
	activated(ctx, clock, org, m, nil)
	return m, nil
}

// acquire is Lease's work. It tells clock what the activation is at the point that
// is decided, and leaves it undecided on every refusal ahead of that.
func acquire(s *Service, ctx context.Context, org string, payer account.Account, super bool, bearer string, spec Spec, clock *activation.Clock) (Sandbox, error) {
	if strings.TrimSpace(org) == "" {
		return Sandbox{}, zip.ErrForbidden("org required")
	}
	store, err := storeFor(s, org)
	if err != nil {
		return Sandbox{}, zip.Errorf(http.StatusInternalServerError, "open store: %v", err)
	}
	// WHO IS LEASING, read once: the new row records them, and a resume is theirs
	// to ask only of a sandbox they hold. One whose credential names no subject
	// to hold it by leases nothing — the row would be keyed on a name somebody
	// else can present.
	h := holderOf(ctx)
	if h.unkeyed {
		return Sandbox{}, zip.ErrForbidden("a sandbox is held by the person who leases it, and this credential names no subject to hold it by: sign in with a token that carries one")
	}
	if id := strings.TrimSpace(spec.ID); id != "" {
		m, err := store.Get(ctx, org, id)
		// Somebody else's sandbox is not resumed: it is ABSENT to this caller, the
		// same answer find gives, so naming a colleague's id mints a sandbox of the
		// caller's own rather than handing over theirs — or starting a pod on their
		// disk carrying this caller's session.
		if err == nil && !h.holds(m) {
			err = errNotFound
		}
		// An admin taking back a sandbox somebody else leased reaches their session
		// and whatever the run armed into it, so the resume is on the trail before
		// it is handed over, exactly as every other act of theirs on it is (find).
		if err == nil && !h.leased(m) && (m.Status == statusRunning || parked(m)) {
			if err := crossed(s, ctx, h, m, "resume"); err != nil {
				return Sandbox{}, err
			}
		}
		// RUNNING IN THE ROW IS NOT RUNNING IN THE CLUSTER. A pod the cluster ended
		// leaves its row as it was, and handing that back would make the caller's
		// first command the one that finds out (fault). It gets a new pod under the
		// same id, carrying what this caller would bring to a resume (unpark).
		if err == nil && m.Status == statusRunning {
			if _, gone := s.State.rt.lost(ctx, m); !gone {
				clock.Is(activation.Warm, true)
				refreshSession(s, ctx, h, m, bearer)
				return m, nil
			}
			clock.Is(activation.Relocated, true)
			cr := cred{}
			if h.leased(m) {
				cr = fleetCred(ctx, s, org, m.Class, super)
				sess, serr := sessionFor(ctx, bearer, time.Until(time.Unix(m.ExpiresAt, 0)))
				if serr != nil {
					s.Log.Warn("no owner session to hand the restarted sandbox", "sandbox", m.ID, "err", serr)
				}
				cr.session = sess
			}
			back, rerr := restart(s, ctx, store, m, cr)
			if rerr != nil {
				// Another call moved the row first: the relocation is that call's,
				// and this one activated nothing.
				if errors.Is(rerr, errMoved) {
					clock.Is("", false)
				}
				return Sandbox{}, zip.Errorf(http.StatusServiceUnavailable,
					"sandbox %s lost its pod and no new one started (%v); ask again", m.ID, rerr)
			}
			return back, nil
		}
		// A PARKED one comes back as itself, with its disk as it was left: the
		// caller parked it to come back to it, and a fresh sandbox would hand it an
		// empty tree under a new id.
		if err == nil && parked(m) {
			clock.Is(activation.Cold, true)
			return unpark(s, ctx, store, m, payer, super, bearer, spec.TTLSec)
		}
		// One that is PENDING is being given a pod by somebody else's resume right
		// now. It is not gone, so it is not replaced; the caller asks again.
		if err == nil && m.Status == statusPending {
			return Sandbox{}, zip.Errorf(http.StatusConflict, "sandbox %s is starting; ask again in a moment", m.ID)
		}
		if err != nil && err != errNotFound {
			return Sandbox{}, zip.Errorf(http.StatusInternalServerError, "get: %v", err)
		}
		// A named-but-gone session is a NEW sandbox, not an error. The caller is
		// resuming a conversation whose lease expired while they were reading, and
		// the honest answer to that is a working sandbox with a new id — which the
		// contract already carries back on every reply.
	}

	class := strings.ToLower(strings.TrimSpace(spec.Class))
	if class == "" {
		class = "exec"
	}
	// The refusal ENUMERATES from the table rather than repeating it. Written out,
	// the message named three classes for as long as the table held three, and
	// then went on naming three.
	if _, ok := classes[class]; !ok {
		return Sandbox{}, zip.ErrBadRequest("class must be one of " + strings.Join(classNames(), ", "))
	}
	// A caller-supplied image is spent against OUR pull secret, so the namespace
	// is checked before it reaches a pod spec. See image.go — unchecked, this
	// field let one org's request fetch another org's private image.
	if err := checkImage(org, spec.Image); err != nil {
		return Sandbox{}, zip.ErrBadRequest(err.Error())
	}
	// A sandbox with no project is SCRATCH: its disk is an emptyDir that dies with
	// the pod — or, when it asked for one (Spec.Disk), a small disk of its own that
	// survives a park and dies with the lease — and nothing about it outlives the
	// lease. Any class the size of an exec box may be scratch — a coding run asks
	// for exactly that (a dev box whose work leaves by push,
	// apps/coding/sandboxrunner.go) — and every scratch sandbox is bounded by the
	// per-class ceiling in room, since the single-attach rule cannot reach one. A
	// class that asks for more than that box (a device, a larger request) must
	// name the project it is spent on.
	project := slug(spec.Project)
	if project == "" && !classes[class].scratch() {
		return Sandbox{}, zip.ErrBadRequest("project required for class " + class)
	}

	// THE CLUSTER, before anything is built or billed. A lease may name one of
	// the org's ATTACHED clusters and run there; empty is the home cluster,
	// exactly as every lease has always run. Resolving here is what makes an
	// unknown name a clean 404 and a cluster without the gvisor floor a clean
	// refusal — with no row, no PVC and no charge behind either.
	cluster := strings.ToLower(strings.TrimSpace(spec.Cluster))
	rt, err := s.State.rt.at(ctx, org, cluster)
	if err != nil {
		return Sandbox{}, err
	}

	// A project in the signup org is a person's (projectOwner), so a lease that
	// names no person has none there to take: with no owner it would land on the
	// org-wide disk every stranger's project once shared.
	if project != "" && org == account.SignupOrg && h.actor == "" {
		return Sandbox{}, zip.Errorf(http.StatusForbidden, "a project in %s belongs to a person, and this caller names none", org)
	}
	if err := room(ctx, store, org, projectOwner(org, h.actor), project, ""); err != nil {
		return Sandbox{}, err
	}

	// The id is minted before the money is asked, because a disk of the lease's
	// own is named by it and the gate below prices the disk.
	id, err := genID()
	if err != nil {
		return Sandbox{}, zip.Errorf(http.StatusInternalServerError, "id: %v", err)
	}
	volume := ""
	switch {
	case project != "":
		volume = volumeName(org, projectOwner(org, h.actor), project)
	case spec.Disk:
		volume = diskName(id)
	}

	// THE MONEY, before the pod. A sandbox is a microVM on somebody's node and it
	// was leased free: nothing gated it and nothing recorded it, so the reaper was
	// the only thing standing between a button and unbounded compute. It stopped a
	// runaway; it never charged for the ones that ran.
	//
	// The gate goes HERE for the same reason the credential does — a lease that
	// cannot be honoured must leave nothing behind. Refusing after store.Put would
	// bill nobody and strand a row and a PVC.
	//
	// THE PAYER IS NOT THE NAMESPACE. org says whose DATA this is; payer says whose
	// BOOKS pay (payer.Org()) and which wallet in them (payer.Subject()). The two orgs
	// differ in exactly one case — a platform SuperAdmin acting in somebody else's org
	// spends its own, which is what platform sudo means. Keying the gate on org would
	// have charged the tenant being inspected for the operator inspecting it. Both
	// arrive as arguments, resolved by the adapter (cloud.PayerOf, which reads the
	// request where there is one and the stated caller on the plane), because this
	// core reads no identity of its own.
	//
	// THE WALLET IS AN ACCOUNT, NOT AN ORG. In the signup org the org's own account is
	// the platform's pool, and every member there pays from their own wallet; a lease
	// gated and charged on the org was a stranger's computer paid for out of Hanzo's
	// balance. The account the adapter resolved names the person, and it is handed to
	// the gate and the debit unchanged.
	//
	// ("", false) for the project scope is the documented no-principal answer: this
	// core takes identity as arguments and reads none, so it cannot state whether a
	// project claim was validated — and claiming it was would hard-enforce a cap on
	// a value it cannot vouch for.
	// PREPAID, BEFORE THE POD. afford prices the hour this lease is about to bill
	// (fee, runtime and disk) and refuses on the wallet the debit will land in, and
	// budgeted asks the org's ledger for that same hour, so a lease the org's
	// budget has no room for is refused here too. See prepaid.go.
	disk := diskSize(Sandbox{Project: project, Volume: volume})
	admitted, err := afford(ctx, payer, class, project, disk)
	if err != nil {
		return Sandbox{}, err
	}
	if err := within(s, ctx, store, org, admitted.payer); err != nil {
		return Sandbox{}, err
	}
	if err := budgeted(s, ctx, admitted, class, disk); err != nil {
		return Sandbox{}, err
	}

	// The lease. Unbounded is not an option for a sandbox running submitted code on
	// our nodes, so an unset ttl takes the class default rather than forever. It is
	// settled HERE, ahead of the credential, because the credential is asked to live
	// exactly this long and no longer.
	ttl := s.State.clk.capTTL(lifetime(spec.TTLSec, class, admitted))

	// THE CREDENTIALS, both resolved HERE — before the row and before the pod, for
	// the reason runtimeFor is asked here: a lease that cannot be honoured must
	// leave nothing behind, and an outage upstream should not strand a row and a
	// PVC.
	//
	// The SESSION is every lease's: the caller's own identity, exchanged for a token
	// that expires with this lease. The cluster credentials are one branch's, read
	// from the org's fleet, and it is the same predicate that reached the image —
	// see cred.go, where both live so they cannot drift apart.
	cr := fleetCred(ctx, s, org, class, super)
	// AN IDENTITY THAT CANNOT BE MINTED DOES NOT COST THE SANDBOX. The exchange
	// reaches IAM, and a sandbox that starts without a session is exactly the
	// sandbox everybody got before this existed — useful, and holding nothing.
	// Refusing the lease instead would turn an identity outage into a total sandbox
	// outage, and it would deny nothing that is not already denied: the failure
	// leaves the pod with no credential either way. So it is LOUD and it continues.
	sess, err := sessionFor(ctx, bearer, time.Duration(ttl)*time.Second)
	if err != nil {
		s.Log.Warn("no owner session to hand the sandbox", "org", org, "class", class, "err", err)
	}
	cr.session = sess

	now := time.Now().Unix()
	m := Sandbox{
		ID: id, Org: org, Kind: KindSandbox, Actor: h.actor,
		Class: class, Project: project, Volume: volume, Cluster: cluster,
		Image: cmp.Or(strings.TrimSpace(spec.Image), rt.imageFor(class, super)),
		Pod:   podName(id), Status: statusPending,
		// The runtime meter's two facts, both settled HERE because both are facts
		// about the act that took the lease. Payer is the wallet this lease was
		// gated against, so the recurring charge lands where the fee did; the
		// watermark starts NOW rather than at zero, because a lease is held from
		// the moment it is taken and a zero would make the first tick read this as
		// a row it had never seen and charge nothing at all — which for an exec
		// sandbox, whose whole life is shorter than one tick, is every exec
		// sandbox running free. The wallet is stored as its subject, which reads back
		// losslessly through account.PayerOf (sandbox.go debit).
		Payer:     admitted.payer.Subject(),
		MeteredAt: now,
		CreatedAt: now, LastUsedAt: now,
	}
	// THE ISOLATION BOUNDARY, derived from the volume decided above.
	// Asked here rather than at start() so that a request which cannot be honoured
	// leaves nothing behind — no row, no PVC, no pod.
	//
	// The ANSWER is recorded, not the question. A caller that asked for one
	// runtime and can only have another must be able to see which it got, or the
	// two are indistinguishable from the outside and a comparison between them
	// measures nothing.
	m.Runtime, err = rt.runtimeFor(m, spec.Runtime, rt.preference(ctx))
	if err != nil {
		return Sandbox{}, zip.ErrBadRequest(err.Error())
	}
	m.ExpiresAt = now + int64(ttl)

	// Every gate has admitted it: from here a pod is started for a session that had
	// none, which is a cold start whether or not the caller named an earlier one.
	clock.Is(activation.Cold, false)
	if err := store.Put(ctx, m); err != nil {
		return Sandbox{}, zip.Errorf(http.StatusInternalServerError, "put: %v", err)
	}
	// A failure to start is RECORDED on the row and answered 503 — the row stays so
	// an operator can see what was asked for and why it did not happen, rather than
	// the request vanishing with the evidence.
	//
	// AND THE POD GOES, because `start` gives up WAITING and does not stop what it
	// started. A create makes the pod first and then polls it for
	// SANDBOX_START_TIMEOUT_SEC; past that the caller is answered 503 and the pod is
	// left in the cluster, still pulling. When the pull finishes it RUNS — the
	// caller's own image, the caller's own code — and nothing is left that will ever
	// stop it: the meter skips it (`holding` reads pending|running), the per-class
	// ceiling skips it for the same reason, and the orphan sweep PROTECTS it, since
	// a row still claims its id. Three defences that each read "not running" as "not
	// there", and the row this line writes is what switches all three off.
	//
	// So the two halves of the failure are separated: the ROW is the evidence and it
	// stays, the POD is the cost and it is released here. A stop that itself fails is
	// logged and not retried — the reaper reaches the row now (clocks.over refuses an
	// error row at the first clause) and takes the pod with it on the next pass.
	if err := s.State.rt.start(ctx, m, cr); err != nil {
		if serr := s.State.rt.stop(ctx, m); serr != nil {
			s.Log.Warn("the sandbox could not be started and its pod could not be stopped",
				"sandbox", m.ID, "org", org, "start", err, "stop", serr)
		}
		m.Status, m.Error = statusError, err.Error()
		_ = store.Put(ctx, m)
		return Sandbox{}, zip.Errorf(http.StatusServiceUnavailable, "start sandbox: %v", err)
	}
	// THE POD IS UP; SAY SO BEFORE ANYTHING TALKS TO IT. exec refuses a sandbox that
	// is not running, and the row this holds still reads pending.
	m.Status = statusRunning
	// THE OWNER'S SESSION, into a pod that is now running — and its failure does not
	// take the sandbox with it, for the same reason the exchange's does not: the pod
	// holds nothing either way, and a shell without an identity is the shell every
	// lease got before this existed. Both halves of "the owner could not be signed
	// in" are therefore ONE policy, stated here where the lease is decided.
	if err := s.State.rt.signIn(ctx, m, cr.session); err != nil {
		s.Log.Warn("the sandbox could not take the owner session", "sandbox", m.ID, "org", org, "err", err)
	}

	// RUNNING only if it is still the pending row this call wrote: a park or an end
	// that landed while the pod came up has the last word, and the pod goes.
	if ok, err := store.Shift(ctx, m, statusPending); err != nil || !ok {
		if serr := s.State.rt.stop(ctx, m); serr != nil {
			s.Log.Warn("the sandbox changed while it started and its pod could not be stopped",
				"sandbox", m.ID, "org", org, "err", serr)
		}
		if err != nil {
			return Sandbox{}, zip.Errorf(http.StatusInternalServerError, "put: %v", err)
		}
		return Sandbox{}, zip.Errorf(http.StatusConflict, "sandbox %s was ended while it started", m.ID)
	}
	// Recorded only once it RUNS and only for paid leases: a free lease
	// (admitted.payer.Zero()) bills nobody — neither runtime nor startup fee.
	if !admitted.payer.Zero() {
		s.Bill.Record(admitted.payer, "sandbox", meter.Usage{
			Model:       class + "/" + m.Runtime,
			AmountCents: ResourceFee(class),
		})
	}
	return m, nil
}

// room refuses a pod whose project disk another sandbox holds, and it is asked before one starts
// by both doors to one: a lease, and a resume (unpark) of self. A door that
// skipped it would be the way around it.
// projectOwner is who a project belongs to inside org, and "" when it is the
// org's own.
//
// In an org of colleagues a project is the org's: its disk is the team's, and one
// sandbox at a time attaches it, whoever leased it. The signup org is not an org
// of colleagues. Every person who signs up acts in it (account.SignupOrg) and is
// a customer of their own, paying from their own wallet (quota.go counted), so a
// project there is the person's. Keyed on the org alone, two strangers naming
// the same project shared one disk: the second was refused while the first held
// it, and handed the first's files once they let it go. Workspace clients name
// their first machine the same way for everybody (`cloud-1`), so that was every
// sign-up but one.
func projectOwner(org, actor string) string {
	if org == account.SignupOrg {
		return actor
	}
	return ""
}

func room(ctx context.Context, st *Store, org, owner, project, self string) error {
	// One sandbox per (org, project), and the refusal is deliberate: the project
	// volume is single-attach, so a second concurrent sandbox would either fail to
	// attach or silently get a cold empty disk. "Silently cold" is the worse of the
	// two — the user sees a sandbox that works and reinstalls everything on every
	// call — so it is refused in the open, naming the sandbox that already holds
	// the volume to a caller who holds it (holds). A PARKED one holds it too: its
	// disk is kept, and billed, for it. A colleague's is refused without its id or
	// its state: a sandbox is its lessee's, and so is what it is doing.
	if project != "" {
		held, err := st.Holding(ctx, org, owner, project, self)
		switch {
		case err != nil || held.ID == "":
			return nil
		case holderOf(ctx).holds(held):
			return zip.Errorf(http.StatusConflict,
				"project %q already has a %s sandbox (%s); resume or delete it first", project, held.Status, held.ID)
		}
		return zip.Errorf(http.StatusConflict,
			"project %q already has a sandbox another member of the org leased; they or an admin of the org can end it", project)
	}

	return nil
}

// Get answers one sandbox THIS org owns and the caller holds. An id belonging to
// another org, or to another member, is 404 and not 403, because a 403 would
// confirm the id exists and whether a given sandbox exists is itself a fact about
// somebody else.
func Get(s *Service, ctx context.Context, org, id string) (Sandbox, error) {
	m, _, err := find(s, ctx, org, id, "")
	return m, err
}

// List answers the org's sandboxes the caller holds, newest first: their own, or
// every one for an admin of the org (holder.holds).
func List(s *Service, ctx context.Context, org, project, status string) ([]Sandbox, error) {
	if strings.TrimSpace(org) == "" {
		return nil, zip.ErrForbidden("org required")
	}
	store, err := storeFor(s, org)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "open store: %v", err)
	}
	ms, err := store.List(ctx, org, slug(project), strings.TrimSpace(status), holderOf(ctx))
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list: %v", err)
	}
	return ms, nil
}

// Run runs a command inside the sandbox and answers its exit code and output.
//
// A non-zero exit is a SUCCESSFUL call carrying a failed program: no error is
// returned, because "the tests failed" and "the sandbox is broken" are different
// facts and a caller has to be able to tell them apart.
func Run(s *Service, ctx context.Context, org, id string, cmd Cmd) (ExecResult, error) {
	m, store, err := find(s, ctx, org, id, "exec")
	if err != nil {
		return ExecResult{}, err
	}
	argv, err := argvOf(cmd.Argv, cmd.Command)
	if err != nil {
		return ExecResult{}, err
	}
	if cmd.Dir != "" {
		argv = append([]string{"sh", "-c", "cd " + shellQuote(cmd.Dir) + " && exec \"$@\"", "sh"}, argv...)
	}
	touched(ctx, store, m) // a call is in flight — see touched

	// THE COMMAND BECOMES ADDRESSABLE the moment it starts, in the two ways a
	// caller needs it to be: its cancel is held under the sandbox so Stop can
	// reach it, and its output is narrated into the session that asked to watch.
	// Both end when the command does. See work.go.
	ctx, stop := context.WithCancel(ctx)
	defer stop()
	forget := s.State.work.start(m.ID, stop)
	defer forget()
	// The secrets this command must never publish, applied at BOTH paths it
	// leaves by: the narration below as it is produced, and the result returned
	// to the caller. See blind.go.
	blind := newBlinder(cmd.Blind)
	say := newTell(org, cmd.Session, blind, cmd.Events)
	go pulse(ctx, store, m, s.State.pulse)

	r, err := s.State.rt.exec(ctx, m, argv, strings.NewReader(cmd.Stdin), s.State.clk.capTTL(cmd.TimeoutSec), say)
	r = blind.result(r)
	// The last word is said on EVERY path, including the one a stop took. A run
	// that vanishes mid-sentence leaves a watcher reading "working…" forever.
	say.done(r.ExitCode, err)
	if err != nil {
		return ExecResult{}, fault(s, ctx, store, m, "exec", err)
	}
	touched(ctx, store, m)
	return r, nil
}

// Stop interrupts whatever the caller's sandbox is running right now, and leaves
// the sandbox leased.
//
// It is a different act from End, deliberately: STOP ENDS THE WORK, END ENDS THE
// RESOURCE. A run that has gone wrong is usually one somebody still wants to look
// at — the checkout, the logs, the half-written file are all in there — and a
// stop that also deleted the pod would take the evidence with it. Two verbs, no
// overlap, and nothing that has to be undone.
//
// It answers HOW MANY commands it interrupted, and zero is a true answer rather
// than a failure: a command that finished a moment ago is one there is nothing
// left to stop. What a caller must be able to tell apart is "already over" from
// "not yours", and the second is a 404 out of find below — the same org gate
// every other operation here walks through, applied before the in-flight set is
// consulted at all.
func Stop(s *Service, ctx context.Context, org, id string) (int, error) {
	m, _, err := find(s, ctx, org, id, "stop")
	if err != nil {
		return 0, err
	}
	return s.State.work.stop(m.ID), nil
}

// Read reads one file, or lists a directory when the path names one. Both go through
// the same exec channel — there is no second protocol and no daemon in the pod to
// speak one.
func Read(s *Service, ctx context.Context, org, id, path string) (Entry, error) {
	m, store, err := find(s, ctx, org, id, "read")
	if err != nil {
		return Entry{}, err
	}
	p, err := confine(m.Class, path)
	if err != nil {
		return Entry{}, err
	}
	q := shellQuote(p)
	r, err := s.State.rt.exec(ctx, m, []string{"sh", "-c",
		"if [ -d " + q + " ]; then ls -1A -- " + q + "; exit " + strconv.Itoa(dirExit) + "; fi; cat -- " + q}, nil, 0, nil)
	if err != nil {
		return Entry{}, fault(s, ctx, store, m, "fs read", err)
	}
	touched(ctx, store, m)
	switch r.ExitCode {
	case 0:
		return Entry{Path: p, Data: []byte(r.Stdout)}, nil
	case dirExit:
		return Entry{Path: p, Dir: true, Entries: lines(r.Stdout)}, nil
	}
	return Entry{}, zip.ErrNotFound(cmp.Or(strings.TrimSpace(r.Stderr), "no such path"))
}

// Write writes data to one file, creating parent directories. It answers the
// RESOLVED path, because the caller's path is relative far more often than not and
// echoing back what it asked for would tell it nothing it did not already know.
func Write(s *Service, ctx context.Context, org, id, path string, data []byte) (string, int, error) {
	m, store, err := find(s, ctx, org, id, "write")
	if err != nil {
		return "", 0, err
	}
	p, err := confine(m.Class, path)
	if err != nil {
		return "", 0, err
	}
	r, err := s.State.rt.put(ctx, m, p, data)
	if err != nil {
		return "", 0, fault(s, ctx, store, m, "fs write", err)
	}
	touched(ctx, store, m)
	if r.ExitCode != 0 {
		return "", 0, zip.Errorf(http.StatusBadRequest, "write %s: %s", p, strings.TrimSpace(r.Stderr))
	}
	return p, len(data), nil
}

// End ends the lease: the pod goes, the volume stays unless purge.
//
// purge drops the VOLUME as well, and it is opt-in because the volume holds the only
// copy of the checkout and the caches. Ending a lease is cheap and reversible;
// deleting someone's uncommitted work is neither.
func End(s *Service, ctx context.Context, org, id string, purge bool) error {
	m, store, err := find(s, ctx, org, id, "end")
	if err != nil {
		return err
	}
	// The tail, the stop, the delete and the ship are ONE settlement (meter.go), in
	// that order: the snapshot that carries the advanced watermark has to carry the
	// deletion too, or a successor hydrates a lease this caller ended and bills them
	// for it until it notices.
	retire(s, ctx, store, m, purge)
	return nil
}

// holder is who asks for a sandbox, reduced to the facts that decide whether they
// may act on one: the subject asking, whether it is one to key a sandbox on, and
// whether they administer the org the call acts in.
type holder struct {
	actor string // the verified subject; empty for the org's own background work
	// unkeyed is a person whose credential names no subject a sandbox can be held
	// by (principal.SubjectFrom): they lease none, and hold only as an admin.
	unkeyed  bool
	super    bool // a SuperAdmin (owner == "admin")
	orgAdmin bool // an admin of the acting org
}

// adminOf is whether the caller administers org's sandboxes: a SuperAdmin, or an
// admin of that org unless it is the signup org. Its members are strangers, each a
// customer of their own (projectOwner), so an admin there holds no more of
// another's sandbox than any member does; acting across customers is the
// platform's alone, as billing's administers has it.
func (h holder) adminOf(org string) bool {
	return h.super || (h.orgAdmin && org != account.SignupOrg)
}

// holderOf reads the caller off ctx, on every way in. The subject is the one the
// identity boundary attested no two people share (principal.SubjectFrom): off a
// request, the token's `sub` or an API key's IAM user, never X-User-Id's fallback
// to a username or a display name; on the plane, the caller a sibling stated.
// The admin bits are the headers the boundary minted from a verified token
// (X-User-IsAdmin, X-User-IsOrgAdmin — what principal.Super and
// principal.IsOrgAdmin read) off a request, and the stated caller's on the
// plane, which is what planeLease already reads its SuperAdmin from. Every
// adapter here hands its ctx through untouched, so there is no second answer to
// diverge.
//
// A ctx with no caller is nobody: no subject and no admin, so it holds only what
// no person leased — which fails closed on every row a person did.
func holderOf(ctx context.Context) holder {
	who := cloud.Who(ctx)
	actor, keyed := principal.SubjectFrom(ctx)
	return holder{actor: actor, unkeyed: !keyed, super: who.Admin, orgAdmin: who.OrgAdmin}
}

// holds is the one rule: a sandbox is its lessee's, and an admin of its org's.
// A member of the org who did not lease it holds nothing — not its shell, not its
// files, not a ticket to its screen and not its end — because what it holds is the
// lessee's own: their session, and for a coding run a write key to a repository,
// the codebase's secrets and the deployment's gateway key.
func (h holder) holds(m Sandbox) bool { return h.adminOf(m.Org) || h.leased(m) }

// leased is whether the caller is the sandbox's lessee. A person with no subject
// to key on is nobody's, so another person who shares their name is not them.
func (h holder) leased(m Sandbox) bool { return !h.unkeyed && m.Actor == h.actor }

// find resolves an id to a sandbox THIS org owns and the CALLER holds, beside the
// store it came from. The query is scoped by the validated org, so a caller cannot
// address another org's sandbox by guessing an id; and a sandbox of this org the
// caller does not hold is the same 404, so a member cannot address a colleague's.
//
// act names what the caller is about to do with it, for the trail: an admin acting
// on a sandbox somebody else leased is recorded before the act (crossed). An empty
// act is a read of the row itself, which records nothing.
func find(s *Service, ctx context.Context, org, id, act string) (Sandbox, *Store, error) {
	m, store, err := lookup(s, ctx, org, id)
	h := holderOf(ctx)
	if err != nil {
		if gone := buried(s, ctx, org, id, h, err); gone != nil {
			return Sandbox{}, nil, gone
		}
		return Sandbox{}, nil, err
	}
	if !h.holds(m) {
		return Sandbox{}, nil, zip.ErrNotFound("sandbox not found")
	}
	if act != "" && !h.leased(m) {
		if err := crossed(s, ctx, h, m, act); err != nil {
			return Sandbox{}, nil, err
		}
	}
	return m, store, nil
}

// buried answers a sandbox that is not found with WHY it is gone, when the reaper
// ended it within endedKeep and the caller holds it: still a 404, the answer every
// caller already reads as gone, but saying which clock or ceiling ended it and
// carrying it as `reason` beside the message. A coding run that lost its sandbox
// mid-turn reports the message it was refused with, so this is what it says.
//
// nil leaves the caller's answer as it was: an error that is not "not found", no
// tombstone, or one held by somebody else, to whom the id stays as absent as any
// other they do not hold.
func buried(s *Service, ctx context.Context, org, id string, h holder, err error) error {
	var he *zip.HTTPError
	if !errors.As(err, &he) || he.Status != http.StatusNotFound {
		return nil
	}
	store, serr := storeFor(s, org)
	if serr != nil {
		return nil
	}
	e, eerr := store.Ended(ctx, org, strings.TrimSpace(id), time.Now().Unix())
	if eerr != nil || !h.holds(Sandbox{Org: e.Org, Actor: e.Actor}) {
		return nil
	}
	return zip.ErrNotFound(endedLine(e)).With(map[string]any{
		"reason": e.Reason, "endedAt": e.EndedAt,
	})
}

// endedLine is the sentence a tombstone answers with.
func endedLine(e tombstone) string {
	line := "sandbox " + e.ID + " was ended by the reaper (" + e.Reason + ")"
	if why := endedWhy[e.Reason]; why != "" {
		line += ": " + why
	}
	if e.Detail != "" {
		line += " — " + e.Detail
	}
	return line
}

// endedWhy is each reason in plain words.
var endedWhy = map[string]string{
	"start-failed":      "it never came up",
	"max-life":          "it reached the longest a sandbox may live",
	"lease-expired":     "its lease ran out",
	"idle-connected":    "nothing ran in it for the idle allowance, though it was open",
	"idle-disconnected": "nothing ran in it and nobody had it open for the idle allowance",
	"parked":            "it stayed parked past its hold",
	costCeiling:         "it reached its cost ceiling",
}

// crossed writes one admin act on a sandbox the admin did not lease to the trail,
// BEFORE the act. What an admin reaches there is somebody else's credentials, so an
// act the trail could not record is refused rather than taken unrecorded. A
// deployment with no trail logs it, which is every such deployment's answer.
//
// The admin is named by the subject they hold sandboxes by, and — for one whose
// credential names none — by the user id their token did carry: an act on the
// trail with no actor is one nobody can be asked about.
func crossed(s *Service, ctx context.Context, h holder, m Sandbox, act string) error {
	who := cloud.Who(ctx)
	admin := cmp.Or(h.actor, strings.TrimSpace(who.User))
	s.Log.Info("sandbox acted on by an admin who did not lease it",
		"org", m.Org, "sandbox", m.ID, "act", act, "admin", admin, "lessee", m.Actor)
	if s.State.audit == nil {
		return nil
	}
	after, _ := json.Marshal(map[string]string{"lessee": m.Actor, "class": m.Class})
	// The caller as the edge attested it — a verified token off a request, or the
	// principal a sibling forwarded on the plane — which is the one thing this
	// core knows about how they authenticated.
	_, err := s.State.audit.Append(ctx, audit.Record{
		Actor:     audit.Actor{Org: m.Org, Sub: admin, Email: who.Email},
		Action:    "sandbox." + act,
		Resource:  audit.Resource{Type: "sandbox", ID: m.ID},
		Auth:      audit.AuthContext{Method: "attested", IsAdmin: who.Admin},
		Outcome:   audit.Outcome{Result: "success", Status: http.StatusOK, Reason: "admin acting on a sandbox another member leased"},
		SourceIP:  who.IP,
		RequestID: who.RequestID,
		After:     audit.Redact(after),
	})
	if err != nil {
		s.Log.Error("sandbox: an admin act on another member's sandbox could not be audited",
			"org", m.Org, "sandbox", m.ID, "act", act, "err", err)
		return zip.Errorf(http.StatusServiceUnavailable, "sandbox: this act on another member's sandbox could not be audited")
	}
	return nil
}

// lookup resolves an id to a sandbox THIS org owns, with no question about who is
// asking. It is find's first half, and the whole of what a spent terminal ticket
// needs: the ticket was minted by find, for one sandbox, thirty seconds ago, and
// it is the only credential a WebSocket carries.
func lookup(s *Service, ctx context.Context, org, id string) (Sandbox, *Store, error) {
	if strings.TrimSpace(org) == "" {
		return Sandbox{}, nil, zip.ErrForbidden("org required")
	}
	store, err := storeFor(s, org)
	if err != nil {
		return Sandbox{}, nil, zip.Errorf(http.StatusInternalServerError, "open store: %v", err)
	}
	m, err := store.Get(ctx, org, strings.TrimSpace(id))
	if err == errNotFound {
		return Sandbox{}, nil, zip.ErrNotFound("sandbox not found")
	}
	if err != nil {
		return Sandbox{}, nil, zip.Errorf(http.StatusInternalServerError, "get: %v", err)
	}
	return m, store, nil
}

// touched records use, so the idle reaper can tell an actively-worked sandbox from
// one whose owner walked away.
//
// CALL IT BEFORE THE WORK, not only after. Stamped only on completion it means
// "when a call last FINISHED", which reads a sandbox in the middle of a
// forty-minute run as forty minutes idle — and any idle rule shorter than the
// longest legitimate call then reaps the work it was waiting for. Stamped on
// entry it means "a call is in flight or recently was", which is the question
// the reaper is actually asking.
//
// A single call LONGER than the idle window keeps saying so itself: pulse stamps
// the sandbox for as long as the call is in flight.
//
// It writes the stamp and NOTHING ELSE (Store.Touch). It used to Put the whole
// row as the call had read it, so a call that read a running sandbox and returned
// after a park wrote `running` back over the park — a row billing for a pod that
// was gone.
func touched(ctx context.Context, store *Store, m Sandbox) {
	_ = store.Touch(ctx, m.Org, m.ID, time.Now().Unix())
}

// pulseEvery is the gap between two stamps of a sandbox with a command in flight.
// Well inside the shortest idle allowance (disconnectedIdleDefault, a quarter of an
// hour), so a stamp is never missed by the clock that reads it.
const pulseEvery = time.Minute

// pulse stamps the sandbox as used until ctx ends. A command that prints nothing
// for longer than the idle allowance — a build, a model thinking, a sleep — is
// working, not abandoned, and without this the reaper could not tell the two apart.
func pulse(ctx context.Context, store *Store, m Sandbox, every time.Duration) {
	if every <= 0 {
		every = pulseEvery
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			touched(ctx, store, m)
		}
	}
}

// fault answers a call whose exec failed on the channel. A failure of the
// channel itself — the apiserver or the kubelet did not carry the call — is a bad
// gateway. A POD THE CLUSTER ENDED is not: an eviction, an OOM kill or a delete
// leaves the row reading running, and answering 502 there repeats on every call
// until the lease runs out, each call keeping it fresh.
//
// So a lost pod is replaced under the same id and lease (restart), and the call
// that found it is answered 503: its command did not run, the id answers again,
// and running it again works. What lived only in the pod is gone, and the answer
// says so — a retry that met an empty tree with no word of why would be the
// harder failure to read.
func fault(s *Service, ctx context.Context, st *Store, m Sandbox, what string, err error) error {
	why, gone := s.State.rt.settled(ctx, m)
	if !gone {
		return zip.Errorf(http.StatusBadGateway, "%s: %v", what, err)
	}
	// The session moved under a call that was using it: a relocation, timed from
	// the restart, because what came before it was the call failing, not the
	// session coming back. One that lost the race to another call is that call's.
	clock := activation.Start()
	back, rerr := restart(s, ctx, st, m, cred{})
	if !errors.Is(rerr, errMoved) {
		clock.Is(activation.Relocated, true)
		if rerr != nil {
			back = m
		}
		activated(ctx, clock, m.Org, back, rerr)
	}
	switch {
	case errors.Is(rerr, errMoved):
		return zip.Errorf(http.StatusServiceUnavailable,
			"sandbox %s lost its pod (%s) and is being given a new one; run the %s again in a moment", m.ID, why, what)
	case rerr != nil:
		return zip.Errorf(http.StatusServiceUnavailable,
			"sandbox %s lost its pod (%s) and no new one started (%v); lease a sandbox again", m.ID, why, rerr)
	}
	kept := "its working directory did not survive"
	if m.Volume != "" {
		kept = "its disk is as it was left"
	}
	return zip.Errorf(http.StatusServiceUnavailable,
		"sandbox %s lost its pod (%s) and has a new one under the same id; the %s did not run, so run it again — %s",
		m.ID, why, what, kept)
}

// errMoved is a restart that found the row already moved off the pod it was
// replacing: another call is giving it a new pod, or the lease ended.
var errMoved = errors.New("the sandbox moved while its pod was replaced")

// restart gives a running lease whose pod is gone a NEW pod, under the same id
// and the same lease: payer, clocks and volume unchanged, because nothing was
// bought — the pod the lease paid for went away under it. A fresh pod name, as
// a resume takes, because a pod name is never reused.
//
// ONE RESTART WINS: the row moves off the lost pod by compare-and-set on that pod
// (Store.Swap) before anything starts, and back to running only from the pending
// this call wrote, so an end or a park that lands meanwhile has the last word.
// It runs to the end whether or not the caller waits for it: a restart abandoned
// halfway would leave a pending row holding a pod.
//
// A pod that does not come up leaves the row in error, which the reaper retires
// and a lease naming it replaces with a new sandbox.
func restart(s *Service, ctx context.Context, st *Store, m Sandbox, cr cred) (Sandbox, error) {
	ctx = context.WithoutCancel(ctx)
	was := m.Pod
	if err := s.State.rt.stop(ctx, m); err != nil {
		return Sandbox{}, fmt.Errorf("remove the old pod: %w", err)
	}
	pod, err := genID()
	if err != nil {
		return Sandbox{}, err
	}
	m.Pod, m.Status = podName(pod), statusPending
	if ok, err := st.Swap(ctx, m, was); err != nil {
		return Sandbox{}, err
	} else if !ok {
		return Sandbox{}, errMoved
	}
	if err := s.State.rt.start(ctx, m, cr); err != nil {
		s.Log.Warn("the sandbox's new pod did not start", "sandbox", m.ID, "org", m.Org, "err", err)
		if serr := s.State.rt.stop(ctx, m); serr != nil {
			s.Log.Warn("the sandbox's new pod could not be stopped", "sandbox", m.ID, "org", m.Org, "err", serr)
		}
		m.Status = statusError
		_, _ = st.Shift(ctx, m, statusPending)
		return Sandbox{}, err
	}
	m.Status = statusRunning
	if ok, err := st.Shift(ctx, m, statusPending); err != nil || !ok {
		if serr := s.State.rt.stop(ctx, m); serr != nil {
			s.Log.Warn("the sandbox moved while its pod was replaced and the new pod could not be stopped",
				"sandbox", m.ID, "org", m.Org, "err", serr)
		}
		return Sandbox{}, cmp.Or(err, errMoved)
	}
	if err := s.State.rt.signIn(ctx, m, cr.session); err != nil {
		s.Log.Warn("the restarted sandbox could not take the owner session", "sandbox", m.ID, "org", m.Org, "err", err)
	}
	s.Log.Info("restarted sandbox on a new pod", "sandbox", m.ID, "org", m.Org, "pod", m.Pod, "was", was)
	return m, nil
}

func lines(s string) []string {
	out := []string{}
	for l := range strings.SplitSeq(s, "\n") {
		if l = strings.TrimRight(l, "\r"); l != "" {
			out = append(out, l)
		}
	}
	return out
}
