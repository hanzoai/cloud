package coding

// park.go — a run the model API refused waits, and goes on by itself.
//
// A turn refused for a usage limit (429, a quota, allowance_spent) or by the payer (402,
// insufficient balance, after the run asked the free model once) used to end the run
// failed. A run that took an hour was a dead end, and its person sent the task again by
// hand once the day's free calls came back or after adding credits. Now the run is
// PARKED: its work is kept on its branch as a stop keeps it, its sandbox is given back
// (a parked run holds no box and no lease), and its session waits, paused with its end
// written, saying why and when it goes on.
//
//	park     the durable step (parkRun): the park row, then the session's ONE update
//	         (agents.ParkSession: paused, ended, no sandbox, the reason and the time). The row
//	         is written first, so a crash between the two leaves a row for a session still
//	         running, which no try accepts and the next one removes; a session is never
//	         parked without its row.
//	wake     a timer for the EARLIEST time any parked run may be tried (fire). It is armed
//	         from the rows when the process starts, when a run parks (told over the coord
//	         bus), and after each pass, and on no other occasion: it is not a ticker. The
//	         pod that leads it makes the tries, and a pod that was not the lead when one
//	         fell due asks once more after the time a dead lead's lease takes to pass, and
//	         then no more. The process that holds the timer starts with the host and is
//	         never stopped for idling (manifest: agent is Eager).
//	funds    credits added, a plan opened or a cap changed (internal/funds) try the parked
//	         runs of the org, or of the plan, the event names; an event that names nobody
//	         tries nobody. The event is told where the change is made, never found.
//	try      the run goes on as a resumed run does: the same session, its branch carried on
//	         from the remote in a fresh sandbox, the task it was asked with and one line
//	         saying it is continuing after a limit. Admission is the start's own: nothing is
//	         weakened or bypassed. A try is a request in the ledger, named by the park and
//	         its attempt, so of any number of pods that make the same try, one launches the
//	         run, and an attempt that finished, however it ended, is not made again.
//	whom     a run goes on days after a person's request ended, so nothing of that request
//	         is trusted: IAM is asked whether the person still exists, is not disabled and
//	         is a member of the org; the payer is resolved from the person as a credential
//	         that carries no billing claim is; the try is audited before it is made; and a
//	         person who may not act any more has the run ended, "access removed". That
//	         question at each try, and Stop, are what protect a parked run from a sign-out:
//	         a link revoke does not select a coding session (agents.StopSessions narrows by
//	         the link's host, provider and account, which a coding session does not carry).
//	first    a try the clock or funds make asks the authority that refused the run whether
//	         the refusal stands, before a sandbox is leased or a turn spent: still refused,
//	         it costs one read and the run waits for the time the authority names. A person
//	         pressing Continue is not asked: they go on.
//	again    a try that is refused parks the run again with the reason and the time the
//	         refusal names; a start that cannot be made leaves it parked and is tried again
//	         after a delay that doubles with each failure, counted on the row. Nothing
//	         retries on a fixed clock, so a refusal that does not clear is not a loop.
//	age      a run goes on by itself for as long as a session may live (agents.MaxLife, a
//	         day) after its chain of parks began, and no longer: after that it waits for a
//	         person to press Continue, and the parked message says so. The chain outlives
//	         the park a relaunch drops (chains), and the run that flies carries its start.
//
// The time comes from the authority that set the limit (clearsAt): the allowance app's
// own reset for the person (a session's end, the UTC hour or day) and the free pool's
// refill. Where neither names one, no time is made up.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/hanzoai/cloud/apps/agents"
	"github.com/hanzoai/cloud/client"
	aipeer "github.com/hanzoai/cloud/client/ai"
	allowancepeer "github.com/hanzoai/cloud/client/allowance"
	commercepeer "github.com/hanzoai/cloud/client/commerce"
	iampeer "github.com/hanzoai/cloud/client/iam"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/dialect"
	"github.com/hanzoai/cloud/sdk/meter"
	"github.com/zap-proto/zip"
)

// The words a parked run's events say.
const (
	statusParked    = "parked"
	statusContinued = "continued"
)

// phaseBlocked is the run's own word that it waits on something (agents progress.go).
const phaseBlocked = "blocked"

// The loops and channels of the wake.
const (
	wakeLoop  = "coding:wake"
	wakeTopic = "coding/park"
)

// What tries a parked run: the clock, a change of funds, a person pressing Continue.
const (
	byWake     = "wake"
	byFunds    = "funds"
	byContinue = "continue"
)

// Parking is a run that ended waiting for a refusal to clear instead of failing.
type Parking struct {
	// Reason is agents.ParkLimit, agents.ParkPayment or agents.ParkFault.
	Reason string
	// At is when the authority that refused the run says it may be asked again, and the
	// zero time when none names one.
	At time.Time
	// Retry says the park is for a start that could not be made (no sandbox capacity, a
	// timeout, an app rolling), not for a refusal: Reason is the one the run was parked for
	// before, At is the next attempt's, and Note says what failed.
	Retry bool
	Note  string
	// Print names the model-API fault a run parked for (faultOf), "" for any other park, and
	// Progressed says a turn of the run answered before it met it: the count of that fault begins again.
	Print      string
	Progressed bool
}

// Kept is what a run left on its branch when it parked, so the run that goes on is done
// when it adds nothing more.
type Kept struct {
	Commit   string `json:"commit,omitempty"`
	Diffstat string `json:"diffstat,omitempty"`
	Subject  string `json:"subject,omitempty"`
}

// Parked is a run waiting, as the durable step records it.
type Parked struct {
	Reason string
	At     int64
	Kept   Kept
	// Tries is how many starts in a row of this run could not be made, counting this park.
	Tries int64
	// Chain is when the chain of parks this run is in began (unix milliseconds), as the run that
	// went on was handed it; 0 when this park begins one.
	Chain int64
	// Fault is the model-API fault this park is for and how many turns in a row met it.
	Fault Fault
}

// admission is an error from before a run's harness began: the pre-flight or the lease
// refused it. Nothing was started, so a run that was parked and is tried again waits
// again.
type admission struct{ error }

func (a admission) Unwrap() error { return a.error }

// unpayable is the prepaid gate saying the org has spent its cap for the period: a refusal
// of money, with no time and no event that ends it but a person's.
type unpayable struct{ error }

func (u unpayable) Unwrap() error { return u.error }

// spec is what a parked run needs to go on, and nothing a credential is made of. Not the
// billing claim the request that parked it carried: that is a statement about a request
// that is over, and the run that goes on is paid as IAM and the ledger say today.
type spec struct {
	// In is the run as it was asked, with what was said to it since in its prompt.
	In client.CodingStartIn `json:"in"`
	// TS is the thread message its card narrates in, so the run that goes on edits it.
	TS string `json:"ts,omitempty"`
	// Name is the person's name as the credential that started the run gave it, which the
	// payer is resolved from (principal.PayerFrom).
	Name string `json:"name,omitempty"`
	Kept Kept   `json:"kept"`
	// Fault is the model-API fault the park is for and how many turns in a row met it, so the
	// run that goes on is handed the count (faultCap).
	Fault Fault `json:"fault,omitzero"`
}

// park is one parked run: the row the wake and the funds event read.
type park struct {
	org, session, subject, reason string
	// at is when it is tried again, unix seconds; 0 when nothing names a time.
	at int64
	// attempt names this park by when it was written (unix milliseconds, and always above
	// the one it replaces), so a later attempt at the same session is another request in
	// the ledger. It advances when an attempt finishes, and only then.
	attempt int64
	// tries is how many attempts in a row could not be made (a start that failed, an
	// authority that could not be read). It sets the delay before the next, and is
	// cleared when the run parks again.
	tries int64
	// created is when the run parked, unix milliseconds: from it the park is aged.
	created int64
	// trigger is what is trying it now (byWake, byFunds, byContinue). Not kept.
	trigger string
	spec    spec
}

// stale reports a park older than a session may live: past it the run goes on only when
// a person says so.
func (p park) stale(now time.Time) bool {
	return p.created > 0 && now.Sub(time.UnixMilli(p.created)) > agents.MaxLife()
}

// attempted is how a try ended, as the ledger keeps it for the duplicates of that try: the
// run it launched; or that the authority still refuses it, or that the start could not be
// made (Failed says why, and the park was kept for another attempt).
type attempted struct {
	Accepted
	Held   bool   `json:"held,omitempty"`
	Failed string `json:"failed,omitempty"`
	// Denied is that the person may not act: Failed says why, and the run was ended.
	Denied bool `json:"denied,omitempty"`
}

// resolved is a try that ended without launching the run and recorded why on the park:
// the attempt is over, and nothing is left to retry. status is what a person who asked for it
// (Continue) is answered: 403 for a run whose person may not act, 409 for a start that failed.
type resolved struct {
	error
	status int
}

func (f resolved) Unwrap() error { return f.error }

// ---- the rows --------------------------------------------------------------

// park writes the park: one row per parked session, replaced when the run parks again. It is
// made in the chain the session is in (chain): created is when that chain began, whatever the
// number of parks and relaunches since, so the age a run goes on by itself for is the chain's.
func (l *ledger) park(ctx context.Context, p park) error {
	raw, err := json.Marshal(p.spec)
	if err != nil {
		return err
	}
	if err := dialect.Text(p.org, p.session, p.subject, p.reason, string(raw)); err != nil {
		return zip.ErrBadRequest("coding: a parked run must be text")
	}
	since, err := l.chain(ctx, p.org, p.session, p.created)
	if err != nil {
		return err
	}
	_, err = l.db.ExecContext(ctx, `INSERT INTO parks (org, session, subject, reason, resume_at, attempt, spec, created, tries)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
ON CONFLICT (org, session) DO UPDATE SET subject = $3, reason = $4, resume_at = $5,
  attempt = CASE WHEN parks.attempt >= $6 THEN parks.attempt + 1 ELSE $6 END, spec = $7, created = $8, tries = $9`,
		p.org, p.session, p.subject, p.reason, p.at, l.now().UnixMilli(), string(raw), since, p.tries)
	return err
}

// chain is when the chain of parks the session is in began (unix milliseconds). A session in
// none begins one: at the start the run being parked carries (a run that was gone on with
// carries its chain's start, so a chain the sweep dropped while the run flew is put back as it
// was), else now. A run refused again after it went on is the same chain; only a person's
// Continue begins another (unchain). The first park writes it and every later one reads it, so
// it is not lost when a relaunch drops the park.
func (l *ledger) chain(ctx context.Context, org, session string, carried int64) (int64, error) {
	begin := l.now().UnixMilli()
	if carried > 0 {
		begin = carried
	}
	// One statement writes it if there is none and reads it either way, so a chain taken between a
	// write and a read (a stop, a Continue, the sweep) cannot leave the park without one.
	var since int64
	err := l.db.QueryRowContext(ctx, `INSERT INTO chains (org, session, since) VALUES ($1, $2, $3)
ON CONFLICT (org, session) DO UPDATE SET since = chains.since RETURNING since`, org, session, begin).Scan(&since)
	return since, err
}

// unchain ends the chain of a session: its next park begins another.
func (l *ledger) unchain(ctx context.Context, org, session string) error {
	_, err := l.db.ExecContext(ctx, `DELETE FROM chains WHERE org = $1 AND session = $2`, org, session)
	return err
}

// retime sets when a park is tried next and how many attempts in a row have failed, and
// reports whether it did: only the attempt p names is changed, so a run that parked again
// while it was tried is not overwritten by the try that launched it. With next the attempt
// advances (above the one it replaces): the attempt finished, and the next is another
// request in the ledger. Without it the attempt stands: it was never made.
func (l *ledger) retime(ctx context.Context, p park, at, tries int64, next bool) (bool, error) {
	// The placeholders first appear in order, as the one dialect needs (SQLite binds by
	// appearance): the new values, then the park they are written to.
	query, args := `UPDATE parks SET resume_at = $1, tries = $2 WHERE org = $3 AND session = $4 AND attempt = $5`,
		[]any{at, tries, p.org, p.session, p.attempt}
	if next {
		query = `UPDATE parks SET resume_at = $1, tries = $2, attempt = CASE WHEN attempt >= $3 THEN attempt + 1 ELSE $3 END
 WHERE org = $4 AND session = $5 AND attempt = $6`
		args = []any{at, tries, l.now().UnixMilli(), p.org, p.session, p.attempt}
	}
	res, err := l.db.ExecContext(ctx, query, args...)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// drop removes a session's park, and says whether there was one. With an attempt it
// removes that park alone: a run that went on and was refused at once has parked again
// under another, and the try that launched it must not take that one with it.
func (l *ledger) drop(ctx context.Context, org, session string, attempt int64) (bool, error) {
	var res sql.Result
	var err error
	if attempt == 0 {
		res, err = l.db.ExecContext(ctx, `DELETE FROM parks WHERE org = $1 AND session = $2`, org, session)
	} else {
		res, err = l.db.ExecContext(ctx, `DELETE FROM parks WHERE org = $1 AND session = $2 AND attempt = $3`, org, session, attempt)
	}
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

const parkCols = `org, session, subject, reason, resume_at, attempt, tries, created, spec`

func scanPark(sc interface{ Scan(...any) error }) (park, error) {
	var p park
	var raw string
	if err := sc.Scan(&p.org, &p.session, &p.subject, &p.reason, &p.at, &p.attempt, &p.tries, &p.created, &raw); err != nil {
		return park{}, err
	}
	if err := json.Unmarshal([]byte(raw), &p.spec); err != nil {
		return park{}, fmt.Errorf("a parked run's record is unreadable: %w", err)
	}
	return p, nil
}

func (l *ledger) parks(ctx context.Context, where string, args ...any) ([]park, error) {
	rows, err := l.db.QueryContext(ctx, `SELECT `+parkCols+` FROM parks `+where+` ORDER BY resume_at, session`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []park
	for rows.Next() {
		p, err := scanPark(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// parked is the park of a session, if it has one.
func (l *ledger) parked(ctx context.Context, org, session string) (park, bool, error) {
	ps, err := l.parks(ctx, `WHERE org = $1 AND session = $2`, org, session)
	if err != nil || len(ps) == 0 {
		return park{}, false, err
	}
	return ps[0], true, nil
}

// parksOf is every parked run of an org, or of every org when org is empty.
func (l *ledger) parksOf(ctx context.Context, org string) ([]park, error) {
	if org == "" {
		return l.parks(ctx, ``)
	}
	return l.parks(ctx, `WHERE org = $1`, org)
}

// youngest is the oldest a park may be, in unix milliseconds, to be tried without a person
// saying so, at the instant now.
func youngest(now time.Time) int64 { return now.Add(-agents.MaxLife()).UnixMilli() }

// due is every park with a time at or before now (unix seconds) that a clock may still
// try: not one that has waited longer than a session may live.
func (l *ledger) due(ctx context.Context, now time.Time) ([]park, error) {
	return l.parks(ctx, `WHERE resume_at > 0 AND resume_at <= $1 AND created > $2`, now.Unix(), youngest(now))
}

// soonest is the earliest time any park a clock may still try is tried after the instant
// after (unix seconds), and 0 when none is.
func (l *ledger) soonest(ctx context.Context, after int64, now time.Time) (int64, error) {
	var at sql0
	if err := l.db.QueryRowContext(ctx, `SELECT MIN(resume_at) FROM parks WHERE resume_at > $1 AND created > $2`, after, youngest(now)).Scan(&at); err != nil {
		return 0, err
	}
	return int64(at), nil
}

// sql0 reads a nullable integer as 0.
type sql0 int64

func (n *sql0) Scan(v any) error {
	switch x := v.(type) {
	case nil:
		*n = 0
	case int64:
		*n = sql0(x)
	case []byte:
		i, err := strconv.ParseInt(string(x), 10, 64)
		if err != nil {
			return err
		}
		*n = sql0(i)
	default:
		return fmt.Errorf("coding: %T is not a time", v)
	}
	return nil
}

// ---- what clears the refusal -----------------------------------------------

// The authorities a park asks, as values so a test can stand in for them.
var (
	// allowed is the allowance app's read of a person's usage window.
	allowed = allowancepeer.AllowanceRead
	// pooled is the free pool's state.
	pooled = aipeer.AIPool
	// payable is the prepaid gate: whether a payer can pay at all.
	payable = func(ctx context.Context, org string, in *client.AuthorizeIn) (*client.Verdict, error) {
		return commercepeer.FinanceAuthorize(cloud.For(ctx, org), in)
	}
	// mayAct is IAM's own answer to whether a person may act in an org: the person exists,
	// is not disabled or deleted, and the org is in the membership set the gateway admits
	// an org by (client.Roles.Member), asked of the stored subject as automations ask it
	// before each of their runs.
	mayAct = func(ctx context.Context, org, subject string) (bool, error) {
		got, err := iampeer.IAMRoles(zip.WithCaller(cloud.For(ctx, org), zip.Caller{Org: org, User: subject}))
		if err != nil {
			return false, err
		}
		return got != nil && got.Member, nil
	}
	// onPlan is the plan a payer is on, "" with none.
	onPlan = func(ctx context.Context, org, payer string) (string, error) {
		got, err := commercepeer.BillingTier(cloud.For(ctx, org), &client.TierIn{Subject: payer})
		if err != nil || got == nil {
			return "", err
		}
		return got.Plan, nil
	}
	// endSession stops a session for a reason its stream and its card tell.
	endSession = agents.StopSession
)

// stance is whether the refusal that parked a run still stands, as the authority that
// made it says.
type stance struct {
	// holds is the authority refusing still.
	holds bool
	// at is when it says that ends, zero when it names no time.
	at time.Time
	// unread is no authority having said it holds, and one that could not be asked: not
	// known to have cleared.
	unread bool
}

// standsFor asks the authority of the refusal whether it stands. A usage limit is the
// allowance app's and the free pool's: refused until the later of the two reopens. A refusal
// by the payer is the prepaid gate's: refused while the payer has no funds or has spent a
// cap. An authority that names a time already past holds nothing.
func standsFor(ctx context.Context, reason string) stance {
	ctx, cancel := context.WithTimeout(ctx, clientTimeout)
	defer cancel()
	switch reason {
	case agents.ParkLimit:
		now := time.Now().Unix()
		var at int64
		unread := false
		if person := client.Person(zip.CallerOf(ctx)); person != "" {
			in := &client.AllowanceIn{Subject: person, Payer: meter.PayerFrom(ctx).Subject()}
			switch a, err := allowed(ctx, in); {
			case err != nil || a == nil:
				unread = true
			case a.Spent && a.Resets > now:
				at = a.Resets
			}
		}
		switch p, err := pooled(ctx, &client.PoolIn{}); {
		case err != nil || p == nil:
			unread = true
		case (p.State == "exhausted" || p.State == "busy") && p.Resets > now:
			at = max(at, p.Resets)
		}
		if at > 0 {
			return stance{holds: true, at: time.Unix(at, 0)}
		}
		return stance{unread: unread}
	case agents.ParkPayment:
		payer := meter.PayerFrom(ctx)
		if payer.Zero() {
			return stance{unread: true}
		}
		v, err := payable(ctx, payer.Org(), &client.AuthorizeIn{Subject: payer.Subject(), Amount: client.Money{Decimal: minFundedDecimal, Currency: "usd"}})
		switch {
		case err != nil || v == nil:
			return stance{unread: true}
		case v.OK:
			return stance{}
		case v.NoFunds || v.CapSpent:
			return stance{holds: true}
		}
		return stance{unread: true}
	}
	return stance{}
}

// clearsAt is when the authority that refused a run says it may be asked again, or the
// zero time. A refusal by the payer names no time: credits do. An authority that cannot
// be asked, or names an instant already past, names none, and none is made up; nor one
// beyond the age a run goes on by itself for, which a person's Continue answers instead.
func clearsAt(ctx context.Context, reason string) time.Time {
	if reason == agents.ParkRestart {
		return time.Now() // nothing refused it: the next pod goes on with it at once
	}
	at := standsFor(ctx, reason).at
	if at.After(time.Now().Add(agents.MaxLife())) {
		return time.Time{}
	}
	return at
}

// ---- the durable step ------------------------------------------------------

// holding is what a start hands the run it spawns for its park: who it is for, and the
// run as it was asked.
type holding struct {
	org, session, subject string
	in                    client.CodingStartIn
	// ts is the message the run narrates in, once it has one.
	ts func() string
	// name is the person's name as the credential that started the run gave it.
	name string
}

// errHalted is a park that found its session no longer running: somebody stopped it.
var errHalted = errors.New("coding: the run was stopped before it could park")

// parkRun parks a run. The row comes first, then the session's one update; a session that
// is no longer running (stopped, ended) is not parked, and its row goes. The other pods
// are told the earliest time to wake at.
func (fl *fleet) parkRun(ctx context.Context, h holding, p Parked) error {
	l := fl.ledger()
	if l == nil {
		return errors.New("coding: this process records no requests, so it cannot hold a parked run")
	}
	if unset(l.trail) {
		// A park is a promise that the run goes on, and a process with no audit trail refuses every
		// try (errNoTrail): the run fails as it did before parking, with its work on its branch.
		return errNoTrail
	}
	row := park{org: h.org, session: h.session, subject: h.subject, reason: p.Reason, at: p.At, tries: p.Tries, created: p.Chain,
		spec: spec{In: h.in, Name: h.name, Kept: p.Kept, Fault: p.Fault}}
	if h.ts != nil {
		row.spec.TS = h.ts()
	}
	row.spec.In.RequestID = ""
	if err := l.park(ctx, row); err != nil {
		return fmt.Errorf("coding: could not hold the parked run: %w", err)
	}
	ok, err := parkSession(ctx, h.org, h.session, p.Reason, p.At)
	if err != nil || !ok {
		_, _ = l.drop(context.WithoutCancel(ctx), h.org, h.session, 0)
		if err != nil {
			return fmt.Errorf("coding: could not park the session: %w", err)
		}
		return errHalted
	}
	fl.told(p.At)
	return nil
}

// retryAfter is how long a park waits after its tries-th failed attempt in a row. It
// doubles from the bound a claim stands before a duplicate asks after its claimer, to the
// ceiling a claim is waited on: the ledger's own two figures, so no other is invented. A
// test states its own.
var retryAfter = func(tries int64) time.Duration {
	if tries < 1 || tries > 20 {
		return claimCeiling
	}
	return min(claimBound<<(tries-1), claimCeiling)
}

// within is at, or 0 when it is later than a run goes on by itself for from the instant
// from: beyond it only a person's Continue goes on.
func within(at int64, from time.Time) int64 {
	if at > 0 && time.Unix(at, 0).After(from.Add(agents.MaxLife())) {
		return 0
	}
	return at
}

// remain ends an attempt that could not start the run, and keeps the run parked for
// another: the same reason, the attempt advanced, one failure more counted, and a time a
// delay after now that doubles with each failure. The refusal is left for the person to
// read. The attempt is finished, so duplicates of it that waited on it are answered that
// it failed and make no start of their own.
func (fl *fleet) remain(ctx context.Context, p park, why error) attempted {
	out := attempted{Failed: firstLine(why.Error())}
	l := fl.ledger()
	if l == nil {
		return out
	}
	ctx = context.WithoutCancel(ctx)
	tries := p.tries + 1
	at := within(time.Now().Add(retryAfter(tries)).Unix(), time.UnixMilli(p.created))
	if ok, err := l.retime(ctx, p, at, tries, true); err != nil || !ok {
		if err != nil {
			fl.say("coding: a parked run that could not start was not held again", "session", p.session, "err", err)
		}
		return out // parked again by another run, or gone: nothing of this attempt's is left to say
	}
	fl.told(at)
	if ok, err := parkSession(ctx, p.org, p.session, p.reason, at); err != nil || !ok {
		if err != nil {
			fl.say("coding: a parked run that could not start was not updated", "session", p.session, "err", err)
		}
		return out
	}
	_, log := fl.coord()
	Engine(log).mirror(ctx, p.org, p.session, p.subject, kindStatus, map[string]any{
		"status": statusParked, "reason": p.reason, "message": agents.ParkSaid(p.reason, at), "note": firstLine(why.Error()),
	})
	return out
}

// waits ends an attempt the authority that refused the run said still stands: the run is
// not started, no sandbox is leased and no turn spent. The attempt is finished and the
// park is kept for the time the authority names, or for funds when it names none.
func (fl *fleet) waits(ctx context.Context, p park, s stance) attempted {
	l := fl.ledger()
	ctx = context.WithoutCancel(ctx)
	at := within(s.at.Unix(), time.UnixMilli(p.created))
	if s.at.IsZero() {
		at = 0
	}
	if ok, err := l.retime(ctx, p, at, 0, true); err != nil || !ok {
		if err != nil {
			fl.say("coding: a parked run whose refusal stands was not held again", "session", p.session, "err", err)
		}
		return attempted{Held: true}
	}
	fl.told(at)
	if at != p.at {
		if ok, err := parkSession(ctx, p.org, p.session, p.reason, at); err == nil && ok {
			_, log := fl.coord()
			Engine(log).mirror(ctx, p.org, p.session, p.subject, kindStatus, map[string]any{
				"status": statusParked, "reason": p.reason, "message": agents.ParkSaid(p.reason, at),
			})
		}
	}
	return attempted{Held: true}
}

// later keeps a park for another try after one that could not be made at all: the pod could
// not read what it needed, or IAM, the audit trail or the ledger could not answer. The
// attempt was not made, so it stands; one failure more is counted and the next try is a
// delay away that doubles with each. What a person asked for with Continue is not retried:
// they were told.
func (fl *fleet) later(ctx context.Context, p park, why error) {
	l := fl.ledger()
	if l == nil || p.trigger == byContinue {
		return
	}
	tries := p.tries + 1
	at := within(time.Now().Add(retryAfter(tries)).Unix(), time.UnixMilli(p.created))
	if at == 0 {
		return // beyond the age a run goes on by itself for
	}
	ctx = context.WithoutCancel(ctx)
	if _, err := l.retime(ctx, p, at, tries, false); err != nil {
		fl.say("coding: a parked run that could not be tried was not held for later", "session", p.session, "err", err)
	}
	fl.told(at)
	fl.say("coding: a parked run could not be tried; it is tried again", "session", p.session, "after", time.Until(time.Unix(at, 0)).Round(time.Second).String(), "err", why)
}

// told sets this pod's wake, and tells the other pods, for a run parked to be tried at at.
func (fl *fleet) told(at int64) {
	if at <= 0 {
		return
	}
	fl.arm(at)
	if err := fl.waking().Publish(wakeTopic, []byte(strconv.FormatInt(at, 10))); err != nil {
		fl.say("coding: could not tell the other pods when a parked run is tried; they learn it when they next start", "err", err)
	}
}

// ---- the try ---------------------------------------------------------------

// launched starts the run that goes on, as a value so a test can stand in for the start.
// It is set at init, since the start reaches the park that reaches it.
