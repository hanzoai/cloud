package coding

// The credential the agent inside a sandbox answers the gateway with.
//
// Without one the whole product stops one step short of working: the box leases,
// the repo clones, `dev` starts, hanzo-mcp answers — and then the model call
// returns `Missing environment variable: HANZO_API_KEY`, or, with a key that
// names nobody, the gateway's 402 `a billable tenant is required (no anonymous
// usage)`. Every part was built and nothing could finish a task.
//
// IT IS THE PERSON'S, DELEGATED, MINTED PER RUN. IAM issues it to this deployment
// for the run's person, in the org the run bills, for model calls only, naming this
// deployment as the actor (cloud.Delegate; iam LLM.md "Delegation"). The gateway
// prices, meters and lifts the run as that person in that org, and serves the token
// nothing but model calls (confined.go).
//
// IT IS SHORT-LIVED AND RENEWED BY THIS PROCESS, NOT BY THE BOX. A token minted for
// a whole run is a long-lived credential in a box that executes a model's output,
// and one that dies at IAM's four hours caps every run there. So a run that
// outlives one life holds a token that lasts credLife, and a timer per run asks IAM
// for the next credMargin before it ends (bearer). IAM has no refresh and needs
// none: a fresh delegation is a fresh check of the person, so a person removed from
// the org is refused at the next renewal and the run is ended; a copy of the token
// lifted from the box dies within an hour.
//
// HOW THE NEXT ONE REACHES A RUNNING HARNESS. An environment variable is fixed when
// a process starts, so the first token rides it as it always did (it is what the
// harness's children, such as hanzo-mcp, inherit). dev asks for its credential
// again on a timer and after a 401, by running a command (hanzo-config's
// `[model_providers.hanzo.auth]`), and a run points that command at a file of its own in
// the box, which this process rewrites on each renewal (refreshing, planted, plantAt). The
// channel into the box is the one every secret uses: the stdin of an exec, into a
// file only the run's user reads (arm, prepare). A harness that reads its credential
// once (claude, codex) cannot take another: its token is the run's whole credential,
// and the run is held to its life (bearer.dies).
//
// IT USED TO BE OUR OWN MACHINE IDENTITY, and that was wrong three ways. Every
// customer's run was metered to the admin org; the enso lift weighed the admin
// org's funding, putting every run on paid models at our cost; and code in the box
// could lift the token and spend on our account until it expired. The machine
// token is still minted here — it is the actor_token IAM checks is ours — and it
// never leaves this process.
//
// A STATIC KEY WAS THE OTHER OPTION AND IS WORSE IN EVERY DIRECTION. A long-lived
// sk- key would have to be stored, rotated, and handed to a box that is about to
// execute a model's output; this token expires on its own, is scoped to one
// person in one org, and nothing has to remember to revoke it.
//
// The token URL is not resolved here. IAMBaseURL is the fleet's split-horizon
// policy — the public issuer is Cloudflare-fronted and 403s an in-cluster
// server-side POST (edge 1006), so an in-cluster address must win — and that
// policy having exactly one home is why the agent runner works at all.

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	mrand "math/rand/v2"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/cloud/apps/agents"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/audit"
	"github.com/hanzoai/cloud/sdk/iam"
	"golang.org/x/oauth2"
)

// source is this deployment's own identity, built once and reused: the oauth2
// client_credentials source caches a token until it expires, so a burst of runs
// presents one actor token rather than minting one each. Nil when the deployment
// carries no machine identity, which is the honest answer for a dev box and reads
// as "no credential" at the call site rather than as an empty string that looks
// like one.
var source = sync.OnceValue(iam.Machine)

const (
	// credLife is how long a run's model credential lives when the harness can take
	// another: an hour, as this deployment's own IAM token and a GitHub installation
	// token do. IAM's four hours (delegationTTL) is the most it would grant, not a
	// figure chosen here.
	credLife = time.Hour
	// credMargin is how long before it ends a credential is renewed: the twenty minutes
	// hubRefresh leaves a GitHub token. It is more than the ten the harness waits between
	// asks (askEvery), so a renewal is read before the credential it replaces ends.
	credMargin = credLife - hubRefresh
)

// The errors a run is ended with when its credential cannot go on. Each is a stop
// (agents.ErrStopped): the run keeps its work on its branch, as a stopped one does.
var (
	errRevoked = fmt.Errorf("%w: its person may no longer act in this organization", agents.ErrStopped)
	errLapsed  = fmt.Errorf("%w: its model credential ran out", agents.ErrStopped)
	// errUnrenewable is how a run is ended a little short of the life of the one credential its
	// harness can hold: it reads the credential once (claude, codex), or is a dev too old to be
	// pointed at a file (see renews). Not "its time ran out": a follow-up (`after`) carries it on.
	errUnrenewable = fmt.Errorf("%w: its harness cannot renew its model credential", agents.ErrStopped)
)

// unrenewed is what a run is ended with when IAM could not be asked for the next
// credential before the last one ran out. The person lost nothing, so a follow-up
// (`after`) carries the task on from the work kept on its branch.
const unrenewed = "its model credential could not be renewed: IAM did not answer before the last one ran out"

// stopper is a timer's Stop.
type stopper interface{ Stop() bool }

// credNow and credAfter are the clock and the timer a credential is renewed by, as
// values so a test can hold them.
var (
	credNow   = time.Now
	credAfter = func(d time.Duration, f func()) stopper { return time.AfterFunc(d, f) }
)

// loan is what a run asks of key: whose credential, for which run, and how it reaches the box.
type loan struct {
	org, person, run string
	// scopes lists optional confined sub-scopes ("research", "workflow:gpu") for autonomous science runs.
	scopes []string
	// ttl is the run's seconds, for a run whose context names no deadline.
	ttl int
	// plant puts a renewed credential where the harness reads it, and is nil for a
	// harness that reads its credential once; file is where, inside the box.
	plant func(ctx context.Context, token string) error
	file  string
	// forget removes the credential file from the box when the run is over, and is nil where
	// none was written.
	forget func(ctx context.Context) error
	// halt ends the run for a reason its card tells, with the cause its keep reads.
	halt func(reason string, cause error)
}

func delegateForLoan(ctx context.Context, actor oauth2.TokenSource, d cloud.Delegation) (cloud.Delegated, error) {
	first := d
	if len(d.Scopes) > 0 {
		first.Scopes = slices.Clone(d.Scopes)
	}
	got, err := cloud.Delegate(ctx, actor, first)
	if err != nil && len(d.Scopes) > 0 {
		var refused *cloud.Refused
		if errors.As(err, &refused) && refused.Code == "invalid_scope" {
			fallback := d
			fallback.Scopes = nil
			return cloud.Delegate(ctx, actor, fallback)
		}
	}
	return got, err
}

// key returns the run's credential — the person's delegated token for org — or nil
// when this deployment holds no machine identity to delegate with. An error is
// returned when an identity IS configured and IAM did not delegate, because that is
// a broken deployment or a person who may not run here, and the run must stop rather
// than spend as anyone else; or when no audit trail takes the mint, because a
// credential that renews as a person is never unrecorded.
func key(ctx context.Context, l loan) (*bearer, error) {
	actor := source()
	if actor == nil {
		return nil, nil
	}
	renews := l.plant != nil
	until, ok := ctx.Deadline()
	if !ok {
		until = time.Now().Add(time.Duration(l.ttl) * time.Second)
	}
	deadline := time.Time{}
	if ok {
		deadline = until
	}
	if renews {
		until = minTime(until, time.Now().Add(credLife))
	}
	b := &bearer{loan: l, actor: actor, renews: renews, deadline: deadline, now: credNow, after: credAfter}
	b.ctx, b.cancel = context.WithCancel(context.WithoutCancel(ctx))
	asked := b.now()
	d, err := delegateForLoan(ctx, actor, cloud.Delegation{Subject: l.person, Org: l.org, Until: until, Run: l.run, Scopes: l.scopes})
	if err != nil {
		b.cancel()
		// The reason is IAM's own words — a service account it will not act for,
		// an organization key that was revoked, a deadline already past — and it
		// is logged here because the session a caller opens does not always carry
		// the wrapped error this returns.
		slog.Warn("coding: the run's model calls were not delegated",
			"org", l.org, "subject", l.person, "run", l.run, "err", err)
		return nil, fmt.Errorf("the run's model calls were not delegated for %s: %w", l.person, err)
	}
	b.token, b.until, b.capped = d.Token, asked.Add(d.Life), d.Capped
	// The mint is on the trail before the credential reaches the box. A credential
	// the trail would not take is revoked unseen: it exists only in this process.
	if err := b.record("coding.run.mint", audit.Outcome{Result: "success", Status: 200},
		lent{Life: int(d.Life / time.Second), Renews: renews}); err != nil {
		b.cancel()
		b.revoke(b.token)
		return nil, fmt.Errorf("the run's credential was not recorded: %w", err)
	}
	b.mu.Lock()
	b.arm()
	b.mu.Unlock()
	return b, nil
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

// lent, spent and lost are what the trail says of a run's credential: its first mint, how
// the run ended with it, and why a run was ended for it.
type (
	lent struct {
		Life   int  `json:"life"`
		Renews bool `json:"renews"`
	}
	spent struct {
		Renewals int `json:"renewals"`
	}
	lost struct {
		Why      string `json:"why"`
		Renewals int    `json:"renewals"`
	}
)

// bearer is a run's model credential: the token in use, the one before it while that
// still lives, and the one timer that renews it. Each run has its own — no lock is
// held across a call to IAM and no call waits on another run's — so a renewal that
// hangs or fails delays that run's alone.
type bearer struct {
	loan
	actor    oauth2.TokenSource
	renews   bool
	deadline time.Time // when the run ends, if it names one
	now      func() time.Time
	after    func(time.Duration, func()) stopper
	// ctx is what a renewal's calls run on: the run's tenant, ended with close.
	ctx    context.Context
	cancel context.CancelFunc

	mu       sync.Mutex
	token    string    // the one in use
	older    string    // the one before it, while it lives
	capped   bool      // IAM granted the first token less than was asked: it ends before the run does
	unsure   []string  // tokens the box may hold though it never said so (fresh), not yet revoked
	laid     bool      // a turn has been started with the file written
	until    time.Time // when token ends
	renewals int
	tries    int64 // failed tries since the last renewal
	timer    stopper
	done     bool // the run is over, or ended for its credential: nothing renews
	gone     bool // its person may no longer act
	cut      bool // the tokens have been revoked
	erased   bool // the credential file has been taken out of the box
}

// current is the token a command starts with; "" when the run has none.
func (b *bearer) current() string {
	if b == nil {
		return ""
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.token
}

// known is every token this run has held that may still be spent, which is what its
// output is blinded against.
func (b *bearer) known() []string {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.heldLocked()
}

// heldLocked is known with b.mu held.
func (b *bearer) heldLocked() []string {
	out := make([]string, 0, 2+len(b.unsure))
	for _, t := range slices.Concat([]string{b.token, b.older}, b.unsure) {
		if strings.TrimSpace(t) != "" {
			out = append(out, t)
		}
	}
	return out
}

// snapshot is the token a command starts with and every token that is blinded, read together: a
// renewal adopted between two reads would feed a token the other read does not name.
func (b *bearer) snapshot() (token string, known []string) {
	if b == nil {
		return "", nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.token, b.heldLocked()
}

// isLaid and lay say whether a turn of this flight has been started with the file written, so
// that the first writes it whatever is there and a later one only if it is gone (planted, lodged).
func (b *bearer) isLaid() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.laid
}

func (b *bearer) lay() {
	b.mu.Lock()
	b.laid = true
	b.mu.Unlock()
}

// dies is when the run's credential ends and nothing will replace it, for a run that holds the one
// token it was started with (a harness that reads its credential once, or a dev that cannot be
// pointed at a file) and whose token IAM clipped to less than the run: its end. A token that covers
// the run it was asked for is no limit on it.
func (b *bearer) dies() (time.Time, bool) {
	if b == nil || b.renews {
		return time.Time{}, false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.until, b.capped
}

// refreshes reports a credential the harness asks for again, so that a turn is started
// with the file it reads (planted, refreshing).
func (b *bearer) refreshes() bool { return b != nil && b.renews }

// removed reports that the run was ended because its person may no longer act.
func (b *bearer) removed() bool {
	if b == nil {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.gone
}

// arm sets the one timer for the next renewal, jittered earlier by up to a quarter of the
// margin so runs started together do not ask IAM together. b.mu is held. A token that
// outlives the run is never renewed, so a caller whose deadline is inside one life has no timer
// (a run in production carries the ceiling, which no token outlasts, so it always arms one).
func (b *bearer) arm() {
	if b.timer != nil {
		b.timer.Stop()
		b.timer = nil
	}
	if !b.renews || b.done {
		return
	}
	if !b.deadline.IsZero() && !b.deadline.After(b.until) {
		return
	}
	b.timer = b.after(max(b.until.Sub(b.now())-credMargin-spread(credMargin/4), 0), b.renew)
}

// spread is a random duration in [0, d).
func spread(d time.Duration) time.Duration {
	if d <= 0 {
		return 0
	}
	return time.Duration(mrand.Int64N(int64(d)))
}

// renew is the timer's firing. The person is asked of IAM as a parked run's is (mayAct),
// a fresh token is delegated, and it is put where the harness reads it before it is
// adopted: so a token the box never received is never the one a command starts with.
func (b *bearer) renew() {
	b.mu.Lock()
	if b.done {
		b.mu.Unlock()
		return
	}
	b.timer = nil
	until := b.until
	b.mu.Unlock()
	if !b.now().Before(until) {
		b.lapsed()
		return
	}
	fresh, life, asked, err := b.fresh()
	var refused *cloud.Refused
	switch {
	case err == nil:
		b.adopt(fresh, life, asked)
	case errors.Is(err, errBarred), errors.As(err, &refused) && refused.Denied():
		b.denied(err)
	default:
		b.retry(err)
	}
}

// errBarred is mayAct saying the person may not act in the org any more.
var errBarred = errors.New("coding: IAM says the run's person may no longer act in this organization")

// fresh delegates the next token and puts it in the box.
func (b *bearer) fresh() (token string, life time.Duration, asked time.Time, err error) {
	call := func() (context.Context, context.CancelFunc) { return context.WithTimeout(b.ctx, clientTimeout) }
	ctx, cancel := call()
	ok, err := mayAct(ctx, b.org, b.person)
	cancel()
	switch {
	case err != nil:
		return "", 0, time.Time{}, fmt.Errorf("IAM could not say whether the run's person may act in %s: %w", b.org, err)
	case !ok:
		return "", 0, time.Time{}, errBarred
	}
	until := time.Now().Add(credLife)
	if !b.renews {
		if !b.deadline.IsZero() {
			until = b.deadline
		} else {
			until = time.Now().Add(time.Duration(b.ttl) * time.Second)
		}
	} else if !b.deadline.IsZero() {
		until = minTime(until, b.deadline)
	}
	asked = b.now()
	ctx, cancel = call()
	d, err := delegateForLoan(ctx, b.actor, cloud.Delegation{Subject: b.person, Org: b.org, Until: until, Run: b.run, Scopes: b.scopes})
	cancel()
	if err != nil {
		return "", 0, time.Time{}, err
	}
	if b.plant == nil {
		return d.Token, d.Life, asked, nil
	}
	ctx, cancel = call()
	err = b.plant(ctx, d.Token)
	cancel()
	switch {
	case err == nil:
		return d.Token, d.Life, asked, nil
	case errors.Is(err, errNotLanded):
		b.revoke(d.Token)
		return "", 0, time.Time{}, fmt.Errorf("the renewed credential did not reach the box: %w", err)
	default:
		// The exec may have run and its answer been lost, in which case the file holds this
		// token and the harness will read it: revoking it would leave the harness a dead one
		// until the next renewal. So it is kept, blinded and revoked with the run or once a
		// later renewal has replaced it, and the token in use stays the one the box is known to
		// hold; the retry puts another there.
		//
		// A run that ended while this was in flight has already revoked what it knew: nothing will
		// replace this token or revoke it later, so it is revoked here.
		b.mu.Lock()
		over := b.cut
		if !over {
			b.unsure = append(b.unsure, d.Token)
		}
		b.mu.Unlock()
		if over {
			b.revoke(d.Token)
		}
		return "", 0, time.Time{}, fmt.Errorf("the renewed credential may not have reached the box: %w", err)
	}
}

// adopt makes the renewed token the one in use, and arms for the next.
func (b *bearer) adopt(token string, life time.Duration, asked time.Time) {
	b.adoptMode(token, life, asked, false)
}

func (b *bearer) adoptMode(token string, life time.Duration, asked time.Time, forcePlant bool) {
	b.mu.Lock()
	if b.done {
		b.mu.Unlock()
		b.revoke(token) // the run ended while IAM was asked; nothing is left to hold it
		return
	}
	b.older, b.token, b.until = b.token, token, asked.Add(life)
	if forcePlant {
		b.laid = false
	}
	b.renewals++
	b.tries = 0
	stale := b.unsure // the file holds this token now, whatever it held before
	b.unsure = nil
	b.arm()
	b.mu.Unlock()
	for _, t := range stale {
		b.revoke(t)
	}
}

// reissue mints a fresh delegated token on demand when the run's current token was
// rejected mid-run (401 revoked or expired) or is nearing expiry before a new turn.
// Like a scheduled renewal, it checks mayAct at IAM first and ends the run with
// accessRemoved if the person may no longer act in the organization.
func (b *bearer) reissue(ctx context.Context) error {
	if b == nil {
		return errors.New("coding: no delegated credential to reissue")
	}
	b.mu.Lock()
	if b.done {
		b.mu.Unlock()
		return errors.New("coding: the run's credential is already closed")
	}
	b.mu.Unlock()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	fresh, life, asked, err := b.fresh()
	var refused *cloud.Refused
	switch {
	case err == nil:
		b.adoptMode(fresh, life, asked, true)
		return nil
	case errors.Is(err, errBarred), errors.As(err, &refused) && refused.Denied():
		b.denied(err)
		return err
	default:
		return err
	}
}

// ensureFresh proactively refreshes the run's delegated token before a subsequent
// turn (settle/check/retry) when the current token is within credMargin of expiry
// and the run still has time beyond it, so multi-turn loops and harness child
// processes never start a turn on a stale credential.
func (b *bearer) ensureFresh(ctx context.Context) {
	if b == nil {
		return
	}
	b.mu.Lock()
	need := !b.done && b.laid && !b.until.IsZero() && b.until.Sub(b.now()) <= credMargin &&
		(b.deadline.IsZero() || b.deadline.After(b.until))
	b.mu.Unlock()
	if !need {
		return
	}
	if err := b.reissue(ctx); err != nil {
		slog.Warn("coding: proactive pre-turn credential refresh failed",
			"org", b.org, "subject", b.person, "run", b.run, "err", err)
	}
}

// retry asks again after the delay a claim that failed is asked after, doubling to its
// ceiling (retryAfter) and spread, while the token in use still lives. The last try is
// at its end: a token that ends with no renewal ends the run (lapsed).
func (b *bearer) retry(why error) {
	b.mu.Lock()
	if b.done {
		b.mu.Unlock()
		return
	}
	b.tries++
	wait := min(retryAfter(b.tries)+spread(retryAfter(b.tries)/4), max(b.until.Sub(b.now()), 0))
	slog.Warn("coding: a run's model credential was not renewed; it is asked again",
		"org", b.org, "subject", b.person, "run", b.run, "try", b.tries, "in", wait, "err", why)
	b.timer = b.after(wait, b.renew)
	b.mu.Unlock()
}

// denied ends a run whose person may no longer act in its org: "access removed", as a
// parked run of theirs is ended (revoked). The tokens are revoked now, not when the run
// has kept its work, and the act is on the trail whether or not the run is ended by it.
func (b *bearer) denied(why error) {
	b.mu.Lock()
	if b.done {
		b.mu.Unlock()
		return
	}
	b.done, b.gone = true, true
	renewals := b.renewals
	b.mu.Unlock()
	slog.Warn("coding: a run's person may no longer act; the run is ended",
		"org", b.org, "subject", b.person, "run", b.run, "err", why)
	b.revokeAll()
	if err := b.record("coding.run.revoke", audit.Outcome{Result: "deny", Status: 403, Reason: "access removed"}, lost{Why: firstLine(why.Error()), Renewals: renewals}); err != nil {
		slog.Warn("coding: the end of a run for its person's access was not recorded", "run", b.run, "err", err)
	}
	b.halt(accessRemoved, errRevoked)
}

// lapsed ends a run whose credential ran out with IAM unable to renew it: its work is
// kept, and it is never run on as anyone else.
func (b *bearer) lapsed() {
	b.mu.Lock()
	if b.done {
		b.mu.Unlock()
		return
	}
	b.done = true
	renewals := b.renewals
	b.mu.Unlock()
	slog.Warn("coding: a run's model credential ran out unrenewed; the run is ended",
		"org", b.org, "subject", b.person, "run", b.run)
	if err := b.record("coding.run.lapse", audit.Outcome{Result: "error", Status: 503, Reason: "not renewed"}, lost{Why: unrenewed, Renewals: renewals}); err != nil {
		slog.Warn("coding: the end of a run for its credential was not recorded", "run", b.run, "err", err)
	}
	b.halt(unrenewed, errLapsed)
}

// close ends the credential with the run, whichever way the run ends: no renewal is
// made after it, the timer is let go, the tokens still live are revoked at IAM, and the
// trail is told how many renewals the run made. It is safe to call twice.
func (b *bearer) close() {
	if b == nil {
		return
	}
	b.mu.Lock()
	was := b.done
	b.done = true
	if b.timer != nil {
		b.timer.Stop()
		b.timer = nil
	}
	renewals := b.renewals
	b.mu.Unlock()
	b.cancel()
	b.revokeAll()
	b.erase()
	if !was {
		if err := b.record("coding.run.end", audit.Outcome{Result: "success", Status: 200}, spent{Renewals: renewals}); err != nil {
			slog.Warn("coding: how a run's credential ended was not recorded", "run", b.run, "renewals", renewals, "err", err)
		}
	}
}

// erase takes the credential file out of the box once the run is over, so a sandbox kept for a
// follow-up does not hold a token (revoked, but a file nobody reads is a file somebody finds). On a
// context of its own and short, because the run's is done; a failure is said and not fatal.
func (b *bearer) erase() {
	b.mu.Lock()
	again := b.erased
	b.erased = true
	b.mu.Unlock()
	if b.forget == nil || again {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(b.ctx), 10*time.Second)
	defer cancel()
	if err := b.forget(ctx); err != nil {
		slog.Warn("coding: the run's credential file was not removed from its sandbox", "run", b.run, "err", err)
	}
}

// record writes the act to the audit trail as the run's person.
func (b *bearer) record(action string, out audit.Outcome, after any) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(b.ctx), writeWait)
	defer cancel()
	return flights.record(ctx, b.org, b.person, b.run, "delegated", action, out, after)
}

// revokeAll revokes the tokens that are still spendable, once.
func (b *bearer) revokeAll() {
	b.mu.Lock()
	if b.cut {
		b.mu.Unlock()
		return
	}
	b.cut = true
	live := slices.Concat([]string{b.token, b.older}, b.unsure)
	b.mu.Unlock()
	for _, t := range live {
		b.revoke(t)
	}
}

// revoke ends a delegated token at IAM so a copy of it lifted from the box dies with
// the run instead of at its deadline: the gateway asks IAM whether a delegated token's
// grant still stands (cloud liveness), so a revocation is refused within half a minute
// everywhere.
//
// On a cancel-immune context, because the run that most needs its credential ended is
// the one that hit its deadline. A failure is said and not fatal: the token still dies
// at the deadline it was minted for.
func (b *bearer) revoke(tok string) {
	if strings.TrimSpace(tok) == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(b.ctx), 10*time.Second)
	defer cancel()
	if err := cloud.Revoke(ctx, tok); err != nil {
		slog.Warn("coding: the run's delegated token was not revoked; it ends at its deadline", "err", err)
	}
}

// ── the ticket ──────────────────────────────────────────────────────────────
//
// The run's harness can ask its person a question (ask_user), through an MCP
// server cloud serves at askPath. That call needs a credential, and none of the
// ones above is it: the machine identity is the DEPLOYMENT's and the push key is
// the repository's, and a box that executes a model's output is the last place
// either should be spent on anything else.
//
// So a run gets a TICKET of its own: 32 random bytes, minted when the run is
// admitted, held by the run's flight (steer.go) and by nothing durable. It opens
// exactly one door — the ask server, for exactly this run, asking in the thread
// this run narrates into, answered only by the person it runs for — and the
// server names the run from the ticket, so the harness names nothing. It dies
// with the flight: a finished run's ticket finds nothing, and a restart forgets
// them all, which is right, since the run it named went with the process.
//
// It is not an IAM credential and nothing but the ask server reads it. Carried in
// its own header rather than as a bearer, it is not a token any other route could
// mistake for one: everywhere else the request is simply anonymous.

// TicketHeader is the header a run's ticket rides to the ask server in.
const TicketHeader = "X-Hanzo-Run"

// askPath is where the ask server answers, under the agents address.
const askPath = "/v1/agent/ask"

// askURL is the ask server as a run's harness reaches it: this deployment's own
// API host, which is where the harness already reaches the gateway.
func askURL() string { return "https://" + cloud.Domain() + askPath }

// relayPath is where the run's MCP address for each of its org's servers
// answers, under the agents address: <relayPath>/<server id> (steer.go Relay).
const relayPath = "/v1/agent/mcp"

// relayURL is the run's MCP address for the org's server id, on the same host as
// the ask server.
func relayURL(id string) string { return "https://" + cloud.Domain() + relayPath + "/" + id }

// ticket mints one run's ticket.
func ticket() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b) // crypto/rand.Read never returns an error
	return "run_" + hex.EncodeToString(b)
}

// ticketKey is how a ticket is held: its digest, so the registry holds no
// credential and a lookup compares no secret.
func ticketKey(t string) string {
	d := sha256.Sum256([]byte(t))
	return hex.EncodeToString(d[:])
}

// keyed wraps an agent's argv so the credential arrives on STDIN and becomes an
// environment variable inside the box, and is never an argument.
//
// ARGV IS PUBLIC AND STDIN IS NOT. Every process in the pod can read another's
// command line out of /proc, the argv is what a run echoes into its own audit
// line and its session narration, and the process we are handing it to is about
// to execute a language model's output. Stdin reaches this one process and
// nothing else, the exec stream already carries it, and the value never touches
// a file, a layer, or the pod spec.
//
// TWO LINES, TWO CREDENTIALS: the gateway's (HANZO_API_KEY) and the run's ticket
// (HANZO_RUN), which the harness's MCP config names as the header it sends
// (argvFor). Either may be empty — a dev box has no machine identity, a harness
// that asks nothing has no ticket — and an empty one is unset rather than
// exported blank, so the harness reports it missing rather than wrong. The
// ticket is also the whole of what opens the org's MCP servers to the harness
// (relayURL): no server's own credential is ever handed to it.
//
// `IFS= read -r` takes exactly one line each and leaves the rest of stdin for
// the agent, so this does not consume input a tool might want. `exec "$@"`
// replaces the shell, so the agent keeps PID-of-the-command and signals and exit
// codes travel unchanged — the wrapper is gone by the time the agent runs.
func keyed(argv []string) []string { return carry(argv, "HANZO_API_KEY", "HANZO_RUN") }

// carry is keyed for any secrets: one stdin line per name, each exported when
// it is not empty. A GitHub home's token reaches git this way (gitAs).
func carry(argv []string, names ...string) []string {
	var sh strings.Builder
	for _, n := range names {
		sh.WriteString(`IFS= read -r ` + n + `; `)
	}
	for _, n := range names {
		sh.WriteString(`if [ -n "$` + n + `" ]; then export ` + n + `; else unset ` + n + `; fi; `)
	}
	sh.WriteString(`exec "$@"`)
	return append([]string{"sh", "-c", sh.String(), "sh"}, argv...)
}

// fed is what keyed reads: the two credentials, a line each.
func fed(cred, ticket string) string { return cred + "\n" + ticket + "\n" }

// ── the credential file ─────────────────────────────────────────────────────
//
// dev asks for its credential by running a command, every askEvery and again after a
// 401, and reads what it prints (hanzo-config's `[model_providers.hanzo.auth]`; its
// default command, `dev token`, answers HANZO_API_KEY first, which is why that
// variable cannot be where a renewal lands). A run points the command at a file of its
// own under /tmp/hanzo-run, beside the key and the environment (arm, prepare), which is
// written whole and renamed into place, so what dev reads is the token before a renewal
// or the token after it and never half of one. The command falls back to `dev token`
// when there is no file, so a box with no machine identity behaves as it did, and a pod
// that was replaced between turns (its /tmp gone) starts the next turn on the
// environment alone. Measured against dev 0.7.14 on a local endpoint: with
// HANZO_API_KEY set and the override, the request after the file was replaced carried
// the new bearer.
//
// The first token of a flight is put there by the wrapper that already reads it from stdin
// (planted), whatever the file holds: a run adopted after a restart (Resume) starts in the sandbox
// the dead flight left, with the dead flight's file in it, and dev reads the file before the
// environment, so the new flight's token has to be the one that is there. A later turn of the
// flight (lodged) writes it only if it is gone, a pod replaced between turns, because it holds the
// token of the moment it began and a renewal may have replaced that since. A renewed token is put
// there by an exec of its own (plantAt). All write with umask 077, and none puts a token in argv.
//
// WHAT THIS WEAKENS. Before renewal the run's one token was in client.RunIn.Blind of every exec, so
// `env` printed [redacted] in every turn. Now every token this process knows is blinded at the start of
// each exec (turn), but a token renewed WHILE a turn runs is not in that exec's list (it is fixed when an
// exec starts, and a turn can last hours), so after the first renewal, about 40 minutes in,
// `cat /tmp/hanzo-run/model.<session>` mid-turn publishes the live token into the stream, the Slack
// thread and the session events; the path is also in dev's argv. That is a net weakening of redaction
// mid-turn, offset by a shorter life (an hour, where the token lasted up to four). What a token buys is
// bounded: model calls only (confined.go), on its person's own account, until it ends or the run does
// (revoked). A real fix is a blind list the sandbox plane can add to while an exec runs; that is the
// sandbox's to build, and until it is, this stays. The tokens a box holds at any moment are the one in
// use and the one before it.
//
// CHILDREN OF THE HARNESS GO STALE. A process the harness starts (hanzo-mcp and its llm, think, kai,
// compact and cloud tools, a script a command runs) inherits HANZO_API_KEY as it was when the turn
// began. A turn that began just before a renewal hands them a token with as little as 20 minutes left
// (less while IAM is failing to renew), and none has more than an hour; after that they answer 401.
// dev itself is not one of them: it asks its credential command again. Making a child read the file is
// the child's to do: hanzo-mcp would read the file named by HANZO_API_KEY_FILE when that is set, and
// this process would set it (modelPath).
//
// NO FENCE BETWEEN FLIGHTS OF ONE SESSION. A flight's timer writes the file, and nothing stops the
// timer of an older flight of the same session from writing over a newer one's. It is unreachable while
// one pod holds a session (agents.Hold, and the boot Resume that touches only runs no pod holds); a
// pod that is partitioned and alive while another resumes its run would break it, as it would already
// break a push.

// askEvery is how long dev waits between asking for its credential: its own default
// (refresh_interval_ms = 600000), named here so a renewal's margin is known to cover it.
const askEvery = 10 * time.Minute

// authTimeout is the timeout for dev's credential command: the 5s default is too short
// for the readKey command (file, else `dev token`).
const authTimeout = 30 * time.Second

// modelPath is the file a run's model credential is kept in inside the box: its own, so
// that what an earlier run of the same sandbox left there is never read as this run's.
func modelPath(session string) string {
	return "/tmp/hanzo-run/model." + strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-' {
			return r
		}
		return '_'
	}, session)
}

// put writes $k to the file $f, readable by its user alone, atomically.
const put = `( umask 077; mkdir -p "$(dirname "$f")" && printf '%s' "$k" > "$f.$$" ) && mv -f "$f.$$" "$f"`

// readKey is dev's credential command for a run, given the file as $1: the file, else dev's
// own answer.
const readKey = `if [ -s "$1" ]; then cat "$1"; else exec dev token; fi`

// planted is keyed for dev: the same two stdin lines, the credential also written to the run's
// file (put) whatever is there, before the wrapper executes dev. It is a flight's first turn's.
// A credential that cannot be placed ends the turn there, saying so, instead of starting a
// harness that fails at its first model call.
func planted(file string, argv []string) []string { return placing(file, argv, false) }

// lodged is planted for a later turn of the flight: the file is written only if it is not there. The
// credential it is fed is the one in use when the turn began, and a renewal may have put a newer
// one in the file since, which it must leave alone.
func lodged(file string, argv []string) []string { return placing(file, argv, true) }

func placing(file string, argv []string, ifAbsent bool) []string {
	absent := ""
	if ifAbsent {
		absent = `[ ! -s "$f" ] && `
	}
	sh := `IFS= read -r HANZO_API_KEY; IFS= read -r HANZO_RUN; f=$1; shift; ` +
		`if [ -n "$HANZO_API_KEY" ]; then ` +
		`if ` + absent + `true; then k=$HANZO_API_KEY; ` + put +
		` || { echo "could not place the model credential" >&2; exit 1; }; fi; export HANZO_API_KEY; HANZO_API_KEY_FILE="$f"; export HANZO_API_KEY_FILE; else unset HANZO_API_KEY HANZO_API_KEY_FILE; fi; ` +
		`if [ -n "$HANZO_RUN" ]; then export HANZO_RUN; else unset HANZO_RUN; fi; exec "$@"`
	return append([]string{"sh", "-c", sh, "sh", file}, argv...)
}

// refreshing is dev's argv with its credential command pointed at that file, asked
// every askEvery, placed where the other `-c` overrides go: ahead of the task.
func refreshing(file string, argv []string) []string {
	flags := []string{
		"-c", `model_providers.hanzo.auth.command="sh"`,
		"-c", `model_providers.hanzo.auth.args=["-c",` + quoted(readKey) + `,"sh",` + quoted(file) + `]`,
		"-c", "model_providers.hanzo.auth.refresh_interval_ms=" + strconv.Itoa(int(askEvery/time.Millisecond)),
		"-c", "model_providers.hanzo.auth.timeout_ms=" + strconv.Itoa(int(authTimeout/time.Millisecond)),
	}
	at := slices.Index(argv, "resume")
	if at < 0 {
		at = slices.Index(argv, "--")
	}
	if at < 0 {
		return append(slices.Clone(argv), flags...)
	}
	return slices.Concat(argv[:at], flags, argv[at:])
}

// removeAt takes a run's credential file out of sandbox id.
func removeAt(id, file string) func(context.Context) error {
	return func(ctx context.Context) error {
		ran, err := runIn(ctx, id, []string{"rm", "-f", "--", file}, int(clientTimeout/time.Second), "", "")
		switch {
		case err != nil:
			return err
		case ran.ExitCode != 0:
			return fmt.Errorf("the exec exited %d", ran.ExitCode)
		}
		return nil
	}
}

// errNotLanded is a plant known not to have changed the file: the exec ran and said it failed. Any
// other error is not known, a transport that dropped or timed out after the exec may have run.
var errNotLanded = errors.New("the credential file was not written")

// plantAt puts a renewed credential in sandbox id's file where the running harness will ask
// for it: an exec whose stdin is the token. It names no session, so the exec narrates nothing,
// and blinds the token.
func plantAt(id, file string) func(context.Context, string) error {
	return func(ctx context.Context, token string) error {
		ran, err := runIn(ctx, id, []string{"sh", "-c", `IFS= read -r k; f=$1; ` + put, "sh", file},
			int(clientTimeout/time.Second), "", token+"\n", token)
		switch {
		case err != nil:
			return err
		case ran.ExitCode != 0:
			return fmt.Errorf("%w: the exec exited %d", errNotLanded, ran.ExitCode)
		}
		return nil
	}
}
