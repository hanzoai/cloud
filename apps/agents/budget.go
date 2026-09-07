package agents

// A budget is what an agent may spend, stated on the agent, and this file is the
// ONE place a call is priced before it is made.
//
// Every model call and every priced tool call an agent makes passes through
// [state.afford] first. It computes a quote — an upper bound on what the call
// could cost — and checks it, in this order, against the four things that can
// refuse it:
//
//	1. the organization's balance      (can it be paid for at all)
//	2. the agent's period cap           cap_micro_usd, less what this period spent
//	3. the task ceiling                 max_task_micro_usd, less what this run spent
//	4. the session's budget             budget_micro_usd, less what the session spent
//
// A breach REFUSES the call and emits `budget.exceeded` — the agent is told in
// band, so it can wrap up rather than crash, and a session paused at its cap
// resumes when the cap is raised or removed. Nothing here can be switched off:
// there is no flag, and a run reaches the model only through [budgeted], which
// asks first.
//
// Money is integer micro-USD on the wire and in the store (1 USD = 1,000,000),
// in fields suffixed `_micro_usd`. That is the unit the metering ledger already
// speaks ([metering.Usage.AmountMicros]) and the one a per-call price needs: a
// call costs fractions of a cent, and a cap kept in cents could not see it.
//
// The org-balance step reuses the meter's own Authorize, in cents rounded UP, so
// the two gates that guard money — the org's and the agent's — are one call
// chain rather than two opinions.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	cloud "github.com/hanzoai/cloud"
	"github.com/hanzoai/account"
	"github.com/hanzoai/cloud/internal/mint"
	"github.com/hanzoai/cloud/types"
	"github.com/zap-proto/zip"
)

// The reset windows a cap may carry. Calendar-aligned in UTC, so "month" means
// the month, not thirty days from the first call.
const (
	PeriodDay   = "day"
	PeriodWeek  = "week"
	PeriodMonth = "month"
)

// The components spend is attributed to. Model is every completion an agent
// buys; computer is the runtime it was resident for; tool is a priced tool call.
// A component with no spend is absent from a read, never zero.
const (
	ComponentModel    = "model"
	ComponentComputer = "computer"
	ComponentTool     = "tool"
)

// defaultCompletionCeiling is the output ceiling a quote assumes when the request
// names none. A quote is an UPPER bound, so it errs large: the settle records what
// the gateway actually reported, and the difference comes back to the cap.
const defaultCompletionCeiling = 4096

// codeBudgetExceeded is the machine code on a refusal. It rides a 402 like the
// org-level spend cap does — the two are the same kind of answer at two scopes.
const codeBudgetExceeded = "budget_exceeded"

// eventBudgetExceeded is what the session stream carries when a call is refused.
const eventBudgetExceeded = "budget.exceeded"

// validPeriod says whether p names a reset window.
func validPeriod(p string) bool {
	switch p {
	case PeriodDay, PeriodWeek, PeriodMonth:
		return true
	}
	return false
}

// validateBudget is the ONE rule for what an agent's budget must be. Every field
// is required: a cap of zero would mean no limit and no record, which is the
// state a budget exists to remove. The refusal names the field.
func validateBudget(cap, task int64, period string) error {
	if cap <= 0 {
		return zip.ErrBadRequest("cap_micro_usd is required and must be a positive integer of micro-USD")
	}
	if task <= 0 {
		return zip.ErrBadRequest("max_task_micro_usd is required and must be a positive integer of micro-USD")
	}
	if task > cap {
		return zip.ErrBadRequest("max_task_micro_usd cannot exceed cap_micro_usd")
	}
	if !validPeriod(period) {
		return zip.ErrBadRequest("period is required and must be one of day, week, month")
	}
	return nil
}

// periodStart is the UTC instant the window holding `now` began.
func periodStart(now time.Time, period string) int64 {
	t := now.UTC()
	switch period {
	case PeriodWeek:
		// ISO weeks start on Monday.
		back := (int(t.Weekday()) + 6) % 7
		d := t.AddDate(0, 0, -back)
		return time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, time.UTC).Unix()
	case PeriodMonth:
		return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC).Unix()
	default:
		return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC).Unix()
	}
}

// microsToCentsUp renders micro-USD in whole cents, rounding AWAY from zero. Cents
// is the meter's unit for the org-balance step, and a gate must never understate
// what it is about to admit.
func microsToCentsUp(m int64) int64 {
	if m <= 0 {
		return 0
	}
	return (m + 9_999) / 10_000
}

// quote is what one call could cost, before it is made.
type quote struct {
	Micros    int64
	Component string
}

// quoteChat bounds a completion from above: the prompt as it will be sent, plus
// the largest completion the request allows, at the platform's inference rate.
// Tokens are estimated at four bytes each plus a per-message overhead, which is
// the conservative side for English and for code.
func quoteChat(req *types.ChatRequest) quote {
	var bytes int
	for _, m := range req.Messages {
		bytes += len(m.Content) + 32
	}
	prompt := bytes / 4
	out := req.MaxTokens
	if out <= 0 {
		out = defaultCompletionCeiling
	}
	return quote{Micros: cloud.InferenceMicros(prompt + out), Component: ComponentModel}
}

// runTally is what one run — one task — has spent so far. It lives for the run
// and is written onto the run's row when the run is recorded.
type runTally struct {
	ID     string
	Micros int64
}

// refusal is a budget breach as the caller sees it: a 402 carrying the scope that
// refused, the quote, and what remained. Detail is what the agent reads in band.
func refusal(scope string, q quote, remaining int64) *zip.HTTPError {
	return &zip.HTTPError{
		Status: http.StatusPaymentRequired,
		Code:   codeBudgetExceeded,
		Msg:    fmt.Sprintf("%s budget exceeded: this call would cost up to %d micro-USD and %d remain", scope, q.Micros, remaining),
		Detail: map[string]any{
			"event":               eventBudgetExceeded,
			"scope":               scope,
			"component":           q.Component,
			"quote_micro_usd":     q.Micros,
			"remaining_micro_usd": remaining,
		},
	}
}

// isBudgetRefusal says whether err is this file's refusal, so a caller can tell
// "out of budget" from "the model failed".
func isBudgetRefusal(err error) bool {
	var he *zip.HTTPError
	return errors.As(err, &he) && he.Code == codeBudgetExceeded
}

// afford is THE GATE. It is the only function that decides whether an agent may
// spend, and every path that spends calls it first.
//
// a is the agent, updated in place with the period reset and read for its caps;
// run is the task's tally; sess is the session the call runs under, or nil.
func (st *state) afford(ctx context.Context, sto *Store, a *Agent, run *runTally, sess *Session, q quote) error {
	// 1. The organization: can this be paid for at all. The meter's Authorize is
	// the org-level gate the whole platform uses; a quote of zero is a no-op there,
	// exactly as a zero fee is.
	if st.bill != nil {
		if err := st.bill.Authorize(ctx, account.PayerOf("", a.Org), "", false, meterKind, microsToCentsUp(q.Micros)); err != nil {
			return err
		}
	}
	// 2. The agent's period cap. An agent recorded before budgets existed carries
	// cap 0 and is not capped here; its spend is still tallied and readable, and
	// an update gives it a cap. Refusing every agent made before today would be a
	// deploy-day outage, not a policy.
	if a.CapMicroUSD > 0 {
		start := periodStart(time.Now(), a.Period)
		if a.PeriodStartedAt < start {
			if err := sto.ResetPeriod(ctx, a.Org, a.Name, start); err != nil {
				return zip.Errorf(http.StatusInternalServerError, "budget period: %v", err)
			}
			a.ConsumedMicroUSD, a.PeriodStartedAt = 0, start
		}
		if remaining := a.CapMicroUSD - a.ConsumedMicroUSD; q.Micros > remaining {
			return st.refuse(ctx, sto, sess, "agent", q, remaining)
		}
		// 3. The task ceiling: what this one run may spend.
		if run != nil {
			if remaining := a.MaxTaskMicroUSD - run.Micros; q.Micros > remaining {
				return st.refuse(ctx, sto, sess, "task", q, remaining)
			}
		}
	}
	// 4. The session's own budget, if it carries one that was never removed.
	if sess != nil && !sess.BudgetRemoved && sess.BudgetMicroUSD > 0 {
		if remaining := sess.BudgetMicroUSD - sess.ConsumedMicroUSD; q.Micros > remaining {
			if err := st.pauseAtCap(ctx, sto, sess); err != nil {
				return err
			}
			return st.refuse(ctx, sto, sess, "session", q, remaining)
		}
	}
	return nil
}

// refuse records the breach where the agent will read it and answers the caller.
// Under a session the event lands on the session's stream; a bare run carries it
// in the refusal itself, which is the only channel a bare run has.
func (st *state) refuse(ctx context.Context, sto *Store, sess *Session, scope string, q quote, remaining int64) error {
	err := refusal(scope, q, remaining)
	if sess != nil && sto != nil {
		payload, _ := json.Marshal(err.Detail)
		e, aerr := sto.AppendEvent(ctx, Event{
			ID: mint.ID("evt"), SessionID: sess.ID, Org: sess.Org, Kind: KindStatus,
			Actor: "budget", Payload: string(payload), CreatedAt: time.Now().Unix(),
		})
		if aerr == nil {
			publishEvent(st.svc, sess.Org, sess.RootID, e)
		}
	}
	return err
}

// pauseAtCap parks a session that has reached its budget. Work resumes when the
// cap is raised or removed — see [sessionOps.budget].
func (st *state) pauseAtCap(ctx context.Context, sto *Store, sess *Session) error {
	if sess.Status != StatusRunning {
		return nil
	}
	sess.Status = StatusPaused
	sess.UpdatedAt = time.Now().Unix()
	if err := sto.UpdateSession(ctx, *sess); err != nil {
		return zip.Errorf(http.StatusInternalServerError, "pause at cap: %v", err)
	}
	publishSession(st.svc, *sess, 0, 0)
	return nil
}

// settle records what a call ACTUALLY cost, once it has happened, against every
// tally the quote was checked against. A failed call never reaches here, so it is
// never billed and its headroom comes back.
func (st *state) settle(ctx context.Context, sto *Store, a *Agent, run *runTally, sess *Session, component string, micros int64) {
	if micros <= 0 {
		return
	}
	a.ConsumedMicroUSD += micros
	if run != nil {
		run.Micros += micros
	}
	sessionID := ""
	if sess != nil {
		sess.ConsumedMicroUSD += micros
		sessionID = sess.ID
	}
	runID := ""
	if run != nil {
		runID = run.ID
	}
	if err := sto.Consume(ctx, a.Org, a.Name, runID, sessionID, component, micros); err != nil && st.svc != nil {
		st.svc.Log.Warn("budget: spend not recorded", "org", a.Org, "agent", a.Name, "component", component, "err", err)
	}
}

// budgeted is the AI client a run is handed: the inner client, asked first.
//
// It is how the gate reaches every completion without every call site knowing
// it exists — a run with tools buys one completion per round, and each round
// passes through here on its way to the model.
type budgeted struct {
	inner types.AIClient
	st    *state
	sto   *Store
	a     *Agent
	run   *runTally
	sess  *Session
	// refused is the first refusal this run met, kept so the run can answer
	// with it rather than with the error string a completion loop keeps.
	refused error
}

func (b *budgeted) ChatCompletion(ctx context.Context, req *types.ChatRequest) (*types.ChatResponse, error) {
	if err := b.st.afford(ctx, b.sto, b.a, b.run, b.sess, quoteChat(req)); err != nil {
		if b.refused == nil {
			b.refused = err
		}
		return nil, err
	}
	resp, err := b.inner.ChatCompletion(ctx, req)
	if err != nil {
		return nil, err
	}
	if resp != nil {
		b.st.settle(ctx, b.sto, b.a, b.run, b.sess, ComponentModel, cloud.InferenceMicros(resp.PromptTokens+resp.CompletionTokens))
	}
	return resp, nil
}

// Embed and Rerank pass through: the gateway reports no usage for either, so
// there is nothing to quote or settle, and an agent's embeds are already inside
// the org-level meter. They are here so a budgeted client is a whole AIClient.
func (b *budgeted) Embed(ctx context.Context, req *types.EmbedRequest) ([][]float32, error) {
	return b.inner.Embed(ctx, req)
}

func (b *budgeted) Rerank(ctx context.Context, req *types.RerankRequest) ([]float64, error) {
	return b.inner.Rerank(ctx, req)
}

// affordTool asks the gate for a tool call. Tools do not yet declare a price to
// the agent plane, so the quote is zero: a call is refused only when the agent
// is already at or over a cap, which is the honest enforcement a free quote
// admits. When tools carry a price the quote is where it goes.
func (b *budgeted) affordTool(ctx context.Context, name string) error {
	err := b.st.afford(ctx, b.sto, b.a, b.run, b.sess, quote{Micros: 0, Component: ComponentTool})
	if err != nil && b.refused == nil {
		b.refused = err
	}
	return err
}

// The run's budgeted client travels on the context so a tool call, dispatched
// several frames below the completion loop, can find the same gate.
type budgetKey struct{}

func withBudget(ctx context.Context, b *budgeted) context.Context {
	return context.WithValue(ctx, budgetKey{}, b)
}

func budgetFrom(ctx context.Context) *budgeted {
	b, _ := ctx.Value(budgetKey{}).(*budgeted)
	return b
}

// The session a run executes under, when it has one. Set by whichever caller
// starts agent work inside a session; absent on a bare run.
type sessionKey struct{}

func withSession(ctx context.Context, s *Session) context.Context {
	return context.WithValue(ctx, sessionKey{}, s)
}

func sessionFrom(ctx context.Context) *Session {
	s, _ := ctx.Value(sessionKey{}).(*Session)
	return s
}

// ── The spend read ────────────────────────────────────────────────────────────

type spendQuery struct {
	// Ref is the agent's public id or its org-unique name.
	Ref string `json:"ref"`
	// By groups the answer: "component" is the only grouping today.
	By string `json:"by"`
}

// spendView is what an agent has spent, in integer micro-USD.
type spendView struct {
	// Ref names the agent this spend belongs to.
	Ref               string `json:"ref"`
	// Period is the window the cap resets on: day, week or month.
	Period            string `json:"period,omitempty"`
	// PeriodStartedAt is when the current period began, as a Unix second.
	PeriodStartedAt   int64  `json:"period_started_at,omitempty"`
	// CapMicroUSD is the total this agent may spend within one Period, as an
	// integer number of micro-USD (1,000,000 = $1). It is required at creation: a
	// cap of zero would mean no limit and no per-agent spend record at all.
	CapMicroUSD       int64  `json:"cap_micro_usd"`
	// MaxTaskMicroUSD is the ceiling for a single run, in micro-USD. A session
	// cannot exceed it even when the period cap still has room, so one runaway
	// task cannot consume a month.
	MaxTaskMicroUSD   int64  `json:"max_task_micro_usd"`
	// ConsumedMicroUSD is what has been spent in the current period, in micro-USD.
	// It is settled from what the gateway reported, not from the quote.
	ConsumedMicroUSD  int64  `json:"consumed_micro_usd"`
	// RemainingMicroUSD is what the cap still allows this period, in micro-USD.
	RemainingMicroUSD int64  `json:"remaining_micro_usd"`
	// ByComponent holds only the components with spend. Absent, not zero.
	ByComponent map[string]int64 `json:"by_component,omitempty"`
}

// spend answers what one of your org's agents has spent, in integer micro-USD.
//
// It answers the agent's budget — `cap_micro_usd` per `period`,
// `max_task_micro_usd` per run — with what the current period has consumed,
// what remains, and `by_component`: the spend attributed to `model` (every
// completion the agent bought), `computer` (the runtime it was resident for)
// and `tool`. A component with no spend is absent, not zero. Every amount is an
// integer number of micro-USD (1,000,000 = $1); 11902000 is $11.902. Pass
// `by=component` to ask for the breakdown by name — it is the one grouping, and
// the default.
func (o agentOps) spend(ctx context.Context, in *spendQuery) (*spendView, error) {
	sto, org, err := tenantStore(ctx, &o.s.State)
	if err != nil {
		return nil, err
	}
	a, err := sto.Resolve(ctx, org, strings.TrimSpace(in.Ref))
	if err == errNotFound {
		return nil, zip.ErrNotFound("agent not found")
	}
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "resolve: %v", err)
	}
	if a.CapMicroUSD > 0 && a.PeriodStartedAt < periodStart(time.Now(), a.Period) {
		a.ConsumedMicroUSD = 0 // the window rolled and nothing has spent in it yet
	}
	v := &spendView{
		Ref: a.ID, Period: a.Period, PeriodStartedAt: a.PeriodStartedAt,
		CapMicroUSD: a.CapMicroUSD, MaxTaskMicroUSD: a.MaxTaskMicroUSD,
		ConsumedMicroUSD: a.ConsumedMicroUSD,
	}
	if a.CapMicroUSD > 0 {
		v.RemainingMicroUSD = a.CapMicroUSD - a.ConsumedMicroUSD
	}
	if strings.TrimSpace(in.By) == "component" || in.By == "" {
		by, err := sto.SpendByComponent(ctx, org, a.Name)
		if err != nil {
			return nil, zip.Errorf(http.StatusInternalServerError, "spend: %v", err)
		}
		if len(by) > 0 {
			v.ByComponent = by
		}
	}
	return v, nil
}

// ── The session budget ────────────────────────────────────────────────────────

type sessionBudgetIn struct {
	// ID is the session, from the path.
	ID string `json:"id"`
	// BudgetMicroUSD is the new cap, or null to remove the cap for good.
	BudgetMicroUSD *int64 `json:"budget_micro_usd"`
}

type sessionBudgetView struct {
	// ID names the session.
	ID               string `json:"id"`
	// Status is the session's state: running, paused at its cap, or done.
	Status           string `json:"status"`
	// BudgetMicroUSD is this session's own ceiling in micro-USD, beyond the agent's.
	// A replacement must strictly exceed what the session has already consumed, and
	// removing it is one-way.
	BudgetMicroUSD   int64  `json:"budget_micro_usd"`
	// BudgetRemoved says the session's own cap was taken off. It cannot be put back.
	BudgetRemoved    bool   `json:"budget_removed"`
	// ConsumedMicroUSD is what has been spent in the current period, in micro-USD.
	// It is settled from what the gateway reported, not from the quote.
	ConsumedMicroUSD int64  `json:"consumed_micro_usd"`
}

// budget sets, raises, or removes a session's cap.
//
//   - a replacement must be strictly greater than what the session has consumed
//   - removal is one-way: a session whose cap was removed cannot take one again,
//     and a session created without one cannot be given one
//   - raising or removing the cap resumes work that paused at it
func (o sessionOps) budget(ctx context.Context, in *sessionBudgetIn) (*sessionBudgetView, error) {
	sto, org, err := tenantStore(ctx, &o.s.State)
	if err != nil {
		return nil, err
	}
	x, err := sto.GetSession(ctx, org, in.ID)
	if err == errSessionNotFound {
		return nil, zip.ErrNotFound("session not found")
	}
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "get: %v", err)
	}
	if x.BudgetRemoved {
		return nil, zip.Errorf(http.StatusConflict, "this session's budget was removed, and removal is one-way")
	}
	if in.BudgetMicroUSD == nil {
		if x.BudgetMicroUSD <= 0 {
			return nil, zip.Errorf(http.StatusConflict, "this session was created without a budget, and one cannot be added later")
		}
		x.BudgetMicroUSD, x.BudgetRemoved = 0, true
	} else {
		if x.BudgetMicroUSD <= 0 {
			return nil, zip.Errorf(http.StatusConflict, "this session was created without a budget, and one cannot be added later")
		}
		if *in.BudgetMicroUSD <= x.ConsumedMicroUSD {
			return nil, zip.ErrBadRequest(fmt.Sprintf("budget_micro_usd must exceed what the session has consumed (%d)", x.ConsumedMicroUSD))
		}
		x.BudgetMicroUSD = *in.BudgetMicroUSD
	}
	if err := sto.SetSessionBudget(ctx, org, x.ID, x.BudgetMicroUSD, x.BudgetRemoved); err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "set budget: %v", err)
	}
	// Work that stopped at the cap goes again.
	if x.Status == StatusPaused {
		x.Status = StatusRunning
		x.UpdatedAt = time.Now().Unix()
		if err := sto.UpdateSession(ctx, x); err != nil {
			return nil, zip.Errorf(http.StatusInternalServerError, "resume: %v", err)
		}
		publishSession(o.s, x, 0, 0)
	}
	return &sessionBudgetView{
		ID: x.ID, Status: x.Status, BudgetMicroUSD: x.BudgetMicroUSD,
		BudgetRemoved: x.BudgetRemoved, ConsumedMicroUSD: x.ConsumedMicroUSD,
	}, nil
}
