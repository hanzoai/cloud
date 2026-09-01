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
// JSON. The same functions back the internal plane (plane.go), which is how a peer
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
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/account"
	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/metering"

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
//	SANDBOX_FEE_CENTS_EXEC / _DEV / _DESKTOP   per class
//	SANDBOX_FEE_CENTS                          the fallback for all three
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

func Lease(s *Service, ctx context.Context, org, ledger string, super bool, bearer string, spec Spec) (Sandbox, error) {
	if strings.TrimSpace(org) == "" {
		return Sandbox{}, zip.ErrForbidden("org required")
	}
	store, err := storeFor(s, org)
	if err != nil {
		return Sandbox{}, zip.Errorf(http.StatusInternalServerError, "open store: %v", err)
	}
	if id := strings.TrimSpace(spec.ID); id != "" {
		m, err := store.Get(ctx, org, id)
		if err == nil && m.Status == "running" {
			return m, nil
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
	project := slug(spec.Project)
	if class != "exec" && project == "" {
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

	// One live sandbox per (org, project), and the refusal is deliberate: the
	// project volume is single-attach, so a second concurrent sandbox would either
	// fail to attach or silently get a cold empty disk. "Silently cold" is the worse
	// of the two — the user sees a sandbox that works and reinstalls everything on
	// every call — so it is refused in the open, naming the sandbox that already
	// holds the volume.
	if project != "" {
		if live, err := store.Live(ctx, org, project); err == nil && live.ID != "" {
			return Sandbox{}, zip.Errorf(http.StatusConflict,
				"project %q already has a live sandbox (%s); delete it first", project, live.ID)
		}
	}

	// THE CEILING. An `exec` sandbox carries no project, so the single-attach check
	// above never applies to one and nothing bounded how many an org could hold.
	// That is not a theoretical gap: the code tool sends no session_id, so every call
	// mints a fresh pod with a 15-minute lease, and a loop of 40 calls took 40 pods.
	//
	// The reaper is the FLOOR, not the ceiling. It ends leases that are over; it
	// cannot decline to start one, so on its own it bounds the steady state and not
	// the burst — and the burst is what fills a node. Each of these is a real pod at
	// 250m/512Mi/2Gi, so the cap is written in what a node can hold rather than as a
	// round number: maxLive x 512Mi is 8Gi of memory and 4 cores of requests, which
	// is one worker's worth for one tenant.
	//
	// It is refused with 429 and not 409, because the caller's correct response is to
	// wait rather than to change the request.
	if class == "exec" {
		n, err := store.LiveOfClass(ctx, org, class)
		if err != nil {
			return Sandbox{}, zip.Errorf(http.StatusInternalServerError, "count: %v", err)
		}
		if n >= maxLiveExec {
			return Sandbox{}, zip.Errorf(http.StatusTooManyRequests,
				"org already holds %d live exec sandboxes (max %d); end one or wait for a lease to expire",
				n, maxLiveExec)
		}
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
	// THE LEDGER IS NOT THE NAMESPACE. org says whose DATA this is; ledger says whose
	// BOOKS pay, and the two differ in exactly one case — a platform SuperAdmin acting
	// in somebody else's org spends its own, which is what platform sudo means. Keying
	// the gate on org would have charged the tenant being inspected for the operator
	// inspecting it. Both arrive as arguments, resolved by the adapter (principal.Ledger
	// on the request, principal.LedgerFrom on the plane), because this core reads no
	// identity of its own. Personal and org spend separate INSIDE the ledger, because
	// the payer carries IAM's signed billing_account claim.
	//
	// ("", false) for the project scope is the documented no-principal answer: this
	// core takes identity as arguments and reads none, so it cannot state whether a
	// project claim was validated — and claiming it was would hard-enforce a cap on
	// a value it cannot vouch for.
	//
	// ledger arrives as an argument — this core reads no identity of its own, so there
	// is no principal here to ask — and the address is parsed back out of it. PayerOf
	// answers a bare slug with that org's own account, which is what principal.Ledger
	// resolved and what this gate has always keyed on.
	fee := ResourceFee(class)
	if fee > 0 {
		if err := s.Bill.Authorize(ctx, account.PayerOf("", ledger), "", false, "sandbox", fee); err != nil {
			return Sandbox{}, err
		}
	}

	// The lease. Unbounded is not an option for a sandbox running submitted code on
	// our nodes, so an unset ttl takes the class default rather than forever. It is
	// settled HERE, ahead of the credential, because the credential is asked to live
	// exactly this long and no longer.
	ttl := spec.TTLSec
	if ttl <= 0 {
		ttl = classes[class].ttl
	}
	ttl = s.State.clk.capTTL(ttl)

	// THE CREDENTIALS, both resolved HERE — before the row and before the pod, for
	// the reason runtimeFor is asked here: a lease that cannot be honoured must
	// leave nothing behind, and an outage upstream should not strand a row and a
	// PVC.
	//
	// The SESSION is every lease's: the caller's own identity, exchanged for a token
	// that expires with this lease. The DigitalOcean credentials are one branch's,
	// and it is the same predicate that reached the image — see cred.go, where both
	// live so they cannot drift apart.
	var cr cred
	if admin(class, super) {
		if cr, err = credFor(ctx, s.Log); err != nil {
			return Sandbox{}, zip.Errorf(http.StatusServiceUnavailable, "admin credentials: %v", err)
		}
	}
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

	id, err := genID()
	if err != nil {
		return Sandbox{}, zip.Errorf(http.StatusInternalServerError, "id: %v", err)
	}
	now := time.Now().Unix()
	m := Sandbox{
		ID: id, Org: org, Kind: KindSandbox, Class: class, Project: project, Cluster: cluster,
		Image: cmp.Or(strings.TrimSpace(spec.Image), rt.imageFor(class, super)),
		Pod:   podName(id), Status: "pending",
		// The runtime meter's two facts, both settled HERE because both are facts
		// about the act that took the lease. Payer is the wallet this lease was
		// gated against, so the recurring charge lands where the fee did; the
		// watermark starts NOW rather than at zero, because a lease is held from
		// the moment it is taken and a zero would make the first tick read this as
		// a row it had never seen and charge nothing at all — which for an exec
		// sandbox, whose whole life is shorter than one tick, is every exec
		// sandbox running free.
		Payer:     ledger,
		MeteredAt: now,
		CreatedAt: now, LastUsedAt: now,
	}
	if project != "" {
		m.Volume = volumeName(org, project)
	}
	// THE ISOLATION BOUNDARY, derived from the volume the line above just decided.
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
		m.Status, m.Error = "error", err.Error()
		_ = store.Put(ctx, m)
		return Sandbox{}, zip.Errorf(http.StatusServiceUnavailable, "start sandbox: %v", err)
	}
	// THE POD IS UP; SAY SO BEFORE ANYTHING TALKS TO IT. exec refuses a sandbox that
	// is not running, and the row this holds still reads pending.
	m.Status = "running"
	// THE OWNER'S SESSION, into a pod that is now running — and its failure does not
	// take the sandbox with it, for the same reason the exchange's does not: the pod
	// holds nothing either way, and a shell without an identity is the shell every
	// lease got before this existed. Both halves of "the owner could not be signed
	// in" are therefore ONE policy, stated here where the lease is decided.
	if err := s.State.rt.signIn(ctx, m, cr.session); err != nil {
		s.Log.Warn("the sandbox could not take the owner session", "sandbox", m.ID, "org", org, "err", err)
	}

	// Recorded only once it RUNS. The gate above already refused a balance that
	// could not cover it, and metering a lease that failed to start would bill for
	// a pod nobody got. Class is the unit — an exec is not a desktop — so the meter
	// says which was leased rather than that one more thing happened.
	// Same string boundary as the gate above: ledger is the argument, parsed here so
	// the debit lands where the gate looked.
	s.Bill.Record(account.PayerOf("", ledger), "sandbox", metering.Usage{
		Model:       class + "/" + m.Runtime,
		AmountCents: fee,
	})
	if err := store.Put(ctx, m); err != nil {
		return Sandbox{}, zip.Errorf(http.StatusInternalServerError, "put: %v", err)
	}
	return m, nil
}

// Get answers one sandbox THIS org owns. An id belonging to another org is 404 and
// not 403, because a 403 would confirm the id exists and whether a given sandbox
// exists is itself a cross-tenant fact.
func Get(s *Service, ctx context.Context, org, id string) (Sandbox, error) {
	m, _, err := find(s, ctx, org, id)
	return m, err
}

// List answers the org's sandboxes, newest first.
func List(s *Service, ctx context.Context, org, project, status string) ([]Sandbox, error) {
	if strings.TrimSpace(org) == "" {
		return nil, zip.ErrForbidden("org required")
	}
	store, err := storeFor(s, org)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "open store: %v", err)
	}
	ms, err := store.List(ctx, org, slug(project), strings.TrimSpace(status))
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
	m, store, err := find(s, ctx, org, id)
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
	say := newTell(org, cmd.Session, blind)

	r, err := s.State.rt.exec(ctx, m, argv, strings.NewReader(cmd.Stdin), cmd.TimeoutSec, say)
	r = blind.result(r)
	// The last word is said on EVERY path, including the one a stop took. A run
	// that vanishes mid-sentence leaves a watcher reading "working…" forever.
	say.done(r.ExitCode, err)
	if err != nil {
		return ExecResult{}, zip.Errorf(http.StatusBadGateway, "exec: %v", err)
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
	m, _, err := find(s, ctx, org, id)
	if err != nil {
		return 0, err
	}
	return s.State.work.stop(m.ID), nil
}

// Read reads one file, or lists a directory when the path names one. Both go through
// the same exec channel — there is no second protocol and no daemon in the pod to
// speak one.
func Read(s *Service, ctx context.Context, org, id, path string) (Entry, error) {
	m, store, err := find(s, ctx, org, id)
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
		return Entry{}, zip.Errorf(http.StatusBadGateway, "fs read: %v", err)
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
	m, store, err := find(s, ctx, org, id)
	if err != nil {
		return "", 0, err
	}
	p, err := confine(m.Class, path)
	if err != nil {
		return "", 0, err
	}
	r, err := s.State.rt.put(ctx, m, p, data)
	if err != nil {
		return "", 0, zip.Errorf(http.StatusBadGateway, "fs write: %v", err)
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
	m, store, err := find(s, ctx, org, id)
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

// find resolves an id to a sandbox THIS org owns, beside the store it came from.
// The query is scoped by the validated org, so a caller cannot address another org's
// sandbox by guessing an id.
func find(s *Service, ctx context.Context, org, id string) (Sandbox, *Store, error) {
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
// It still does not cover a single call LONGER than the idle window on its own:
// for that the in-flight call has to keep saying so, which is why the window
// stays an hour until it does.
func touched(ctx context.Context, store *Store, m Sandbox) {
	m.LastUsedAt = time.Now().Unix()
	_ = store.Put(ctx, m)
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
