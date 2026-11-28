// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package ai

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"github.com/hanzoai/authz"

	"github.com/hanzoai/cloud/apps/ai/limits"
	"github.com/hanzoai/cloud/apps/flags"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/audit"
	"github.com/hanzoai/cloud/sdk/meter"
	"github.com/hanzoai/cloud/sdk/principal"
	aiobject "github.com/hanzoai/ai/object"
	"github.com/zap-proto/zip"
)

// mountLimits publishes the caller's plan usage and its organization's requests a
// minute. Registered before ai's /v1/* wildcard, which the more specific path
// outranks either way.
func mountLimits(app *zip.App, l *limits.Limiter, trail *audit.Recorder) {
	o := limitOps{l: l, trail: trail}
	z := app.Group("/v1/ai")
	z.Get("/limits", o.read, zip.WithOperationID("aiLimits"))
	z.Post("/limits/reset", o.reset, zip.WithOperationID("aiResetLimits"))
	z.Get("/limits/rate", o.rate, zip.WithOperationID("aiRateLimit"))
	z.Put("/limits/rate", o.setRate, zip.WithOperationID("aiSetRateLimit"))
}

type limitOps struct {
	l     *limits.Limiter
	trail *audit.Recorder // where an organization's change to its requests a minute is recorded
}

// aiLimitsQuery takes nothing: the payer and its plan come from the validated
// principal.
type aiLimitsQuery struct{}

// aiWindow is one window as a share used, never an amount.
type aiWindow struct {
	// Percent is the share used, 0 to 100, rounded up to the next five so any use
	// shows.
	Percent int `json:"percent"`
	// State is ok, near (four fifths used) or limited (used up).
	State string `json:"state,omitempty"`
	// ResetsAt is when the window starts again (RFC3339), null for a session that is
	// not running: it starts at the next request.
	ResetsAt *string `json:"resets_at"`
}

// aiClass is one class of model the plan includes: the share of the billing period's
// included usage used, its short window when the plan sets one, and who pays for the
// next request.
type aiClass struct {
	Percent int `json:"percent"`
	// State is ok, near, or limited when the class is used up and nothing else pays.
	State string `json:"state"`
	// Paying is plan, prepaid, credits, or none.
	Paying   string    `json:"paying"`
	ResetsAt *string   `json:"resets_at"`
	Window   *aiWindow `json:"window,omitempty"`
	// Unbounded says the plan includes all of this class's use: there is no share
	// to use up, and Percent is 0.
	Unbounded bool `json:"unbounded,omitempty"`
}

// aiLimited says the caller is in limited mode, why, and in which classes: chat goes
// on in the free model, and priced requests of those classes are refused.
type aiLimited struct {
	// Reason is plan_allowance_used.
	Reason  string   `json:"reason"`
	Classes []string `json:"classes"`
	Message string   `json:"message"`
}

// aiAction is one way past a limit: upgrade the plan or add prepaid credit.
type aiAction struct {
	Kind  string `json:"kind"`
	Label string `json:"label"`
	URL   string `json:"url"`
	Plan  string `json:"plan,omitempty"`
}

// aiLimits is the caller's plan, its request windows and each class it includes, as
// shares only — never a dollar, a count or a cap.
type aiLimits struct {
	// Plan is the plan the caller is served as ("dev", "max-5x", "max-20x", "team",
	// "agency", "advisory", "dedicated", ...), "free" when none counts.
	Plan string `json:"plan"`
	// PeriodStart and PeriodEnd bound the month the plan's usage renews over
	// (RFC3339): the billing period, or one month of a yearly one.
	PeriodStart string `json:"period_start,omitempty"`
	PeriodEnd   string `json:"period_end,omitempty"`
	// State is the worst class's: ok, near or limited.
	State string `json:"state"`
	// Classes are premium (third-party frontier models) and ours (Hanzo's), each
	// present when the plan includes it.
	Classes map[string]*aiClass `json:"classes"`
	// Session and Day are the request windows over every request the plan covers.
	Session *aiWindow `json:"session,omitempty"`
	Day     *aiWindow `json:"day,omitempty"`
	// Limited is present in limited mode.
	Limited *aiLimited `json:"limited,omitempty"`
	// Actions are the ways on: upgrade, add prepaid credit.
	Actions []aiAction `json:"actions,omitempty"`
	// Upgrade is the plan that raises these limits, absent at the top.
	Upgrade string `json:"upgrade,omitempty"`
	// Paused are the models whose share of the plan is used for now: each is
	// answered by its fallback in chat until its share resets.
	Paused []aiPaused `json:"paused,omitempty"`
	// Pays is who pays for the next priced request of each class, on any plan or none:
	// plan (its included usage), prepaid (funds the caller added), credits (granted
	// credit, where it pays for the class) or none (nothing does: a request of the class
	// is refused 402 insufficient_balance until funds are added, the `topup` action).
	// It is the usage policy's own answer, so a picker shows Top up on `none` without
	// knowing which credit pays for which class. A class is absent when the wallet could
	// not be read.
	Pays map[string]string `json:"pays,omitempty"`
	// Entitlements are what a model or mode needs the plan to include by name, beyond
	// its usage: fable (Claude Fable 5.2) and fast (fast mode). A request that needs
	// one the plan does not include is refused 402 entitlement_required whatever the
	// caller holds, so a picker hides or marks it here instead.
	Entitlements map[string]*aiEntitlement `json:"entitlements,omitempty"`
	// Reset describes the caller's self-service weekly free usage reset.
	Reset *aiReset `json:"reset,omitempty"`
}

// aiReset describes the caller's 1x/week self-service free usage reset status.
type aiReset struct {
	// Available is true when the caller can reset their usage counters now.
	Available bool `json:"available"`
	// LastAt is when the caller last used their free reset (RFC3339), null if never.
	LastAt *string `json:"last_at"`
	// NextAt is when the next free reset unlocks (RFC3339), null when Available is true.
	NextAt *string `json:"next_at"`
}

// aiLimitsResetIn takes no fields: the payer comes from the validated principal.
type aiLimitsResetIn struct{}

// aiEntitlement is one plan entitlement: whether the plan includes it, and the plan
// that does when it does not.
type aiEntitlement struct {
	// Included says the caller's plan includes it. No plan includes none.
	Included bool `json:"included"`
	// Upgrade is the cheapest plan on sale that includes it, the one the refusal
	// names; present only when the plan does not include it and one on sale does.
	Upgrade string `json:"upgrade,omitempty"`
}

// aiPaused is one paused model: no figure.
type aiPaused struct {
	// Model is the model id, or a pattern ending in `*` naming a family of them.
	Model string `json:"model"`
	// Fallback is the Hanzo model that answers a conversation in its place.
	Fallback string `json:"fallback"`
	// ResetsAt is when its share resets (RFC3339).
	ResetsAt *string `json:"resets_at"`
}

// payerOf resolves the payer from the validated principal.
func payerOf(ctx context.Context) (subject, org string, err error) {
	if !principal.ValidatedFrom(ctx) {
		return "", "", zip.ErrUnauthorized("sign in to read your plan usage")
	}
	w := meter.PayerFrom(ctx)
	if w.Zero() {
		return "", "", zip.ErrForbidden("no billing account for this principal")
	}
	return w.Subject(), w.Org(), nil
}

// Reads the caller's plan usage: for each class of model the plan includes, the
// share used of the billing period and of its short window, who pays for the next
// request, and whether the caller is in limited mode and why; the share used of the
// session and the day; and the ways on. Shares only, never amounts.
func (o limitOps) read(ctx context.Context, _ *aiLimitsQuery) (*aiLimits, error) {
	subject, org, err := payerOf(ctx)
	if err != nil {
		return nil, err
	}
	s, err := o.l.Read(ctx, subject, org)
	if err != nil {
		return nil, zip.Errorf(503, "plan usage is unavailable right now")
	}
	host := ""
	if c, ok := cloud.Request(ctx); ok {
		host = c.Host()
	}
	return view(s, aiobject.PayURL(host, org)), nil
}

// Resets the caller's active AI usage counters (session, day, rolling window, and
// period spend) once every 7 days for free. Protected by CSRF verification and
// recorded on the audit trail when configured. Returns 429 when the caller's
// weekly free reset has already been used within the last 7 days.
func (o limitOps) reset(ctx context.Context, _ *aiLimitsResetIn) (*aiLimits, error) {
	subject, org, err := payerOf(ctx)
	if err != nil {
		return nil, err
	}
	if err := cloud.CSRF(ctx); err != nil {
		return nil, err
	}
	st, err := o.l.FreeReset(ctx, subject, org)
	if err != nil {
		var cool *limits.ResetCooldownError
		if errors.As(err, &cool) {
			return nil, zip.Errorf(http.StatusTooManyRequests, "%v", cool)
		}
		return nil, zip.Errorf(http.StatusServiceUnavailable, "usage reset is unavailable right now")
	}
	if o.trail != nil {
		who := cloud.Who(ctx)
		after, _ := json.Marshal(map[string]any{"last_at": st.LastAt.UTC().Format(time.RFC3339), "next_at": st.NextAt.UTC().Format(time.RFC3339)})
		if _, aerr := o.trail.Append(ctx, audit.Record{
			Actor:     audit.Actor{Org: org, Sub: who.User, Email: who.Email},
			Action:    "ai.limits.reset",
			Resource:  audit.Resource{Type: "limits", ID: org + "/" + subject},
			Auth:      audit.AuthContext{Method: "attested", IsAdmin: principal.SuperFrom(ctx)},
			Outcome:   audit.Outcome{Result: "success", Status: http.StatusOK},
			SourceIP:  who.IP,
			RequestID: who.RequestID,
			After:     after,
		}); aerr != nil {
			slog.Warn("ai limits reset: audit append failed", "org", org, "subject", subject, "err", aerr)
		}
	}
	s, err := o.l.Read(ctx, subject, org)
	if err != nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "plan usage is unavailable right now")
	}
	host := ""
	if c, ok := cloud.Request(ctx); ok {
		host = c.Host()
	}
	return view(s, aiobject.PayURL(host, org)), nil
}

// view projects a standing onto the published shares.
func view(s limits.Standing, pay string) *aiLimits {
	out := &aiLimits{Plan: s.Plan, State: "ok", Classes: map[string]*aiClass{}, Upgrade: s.Upgrade.Plan}
	if s.Plan == "" {
		out.Plan = limits.FreePlan
	} else {
		out.PeriodStart = s.Start.UTC().Format(time.RFC3339)
		out.PeriodEnd = s.End.UTC().Format(time.RFC3339)
		out.Session, out.Day = window(s.Session), window(s.Day)
	}
	for _, name := range flags.PlanClasses {
		if who, ok := s.Pays[name]; ok {
			if out.Pays == nil {
				out.Pays = map[string]string{}
			}
			out.Pays[name] = cmp.Or(who, "none")
		}
	}
	var limited []string
	for _, name := range flags.PlanClasses {
		c, ok := s.Classes[name]
		if !ok || (!c.Unbounded && c.Period.Limit <= 0) {
			continue
		}
		v := &aiClass{Percent: c.Period.Percent(), Paying: cmp.Or(c.Paying, "none"), ResetsAt: stamp(c.Period.ResetsAt)}
		if c.Unbounded {
			v.Percent, v.Unbounded = 0, true
		}
		v.State = classState(c)
		if c.Window != nil {
			v.Window = window(*c.Window)
		}
		if v.State == "limited" {
			limited = append(limited, name)
		}
		out.Classes[name] = v
		out.State = worse(out.State, v.State)
	}
	for _, m := range s.Paused {
		out.Paused = append(out.Paused, aiPaused{Model: m.Model, Fallback: m.Fallback, ResetsAt: stamp(m.ResetsAt)})
	}
	for _, name := range flags.Entitlements {
		if e, ok := s.Entitlements[name]; ok {
			if out.Entitlements == nil {
				out.Entitlements = map[string]*aiEntitlement{}
			}
			out.Entitlements[name] = &aiEntitlement{Included: e.Included, Upgrade: e.Upgrade.Plan}
		}
	}
	if len(limited) > 0 {
		out.Limited = &aiLimited{Reason: limits.CodePlanAllowance, Classes: limited,
			Message: "Your plan's included usage is used for now. Wait until it reopens, or buy usage credits to keep using these models now."}
	}
	if up := s.Upgrade; up.Plan != "" {
		label := "Upgrade your plan"
		if up.Name != "" {
			label = "Upgrade to " + up.Name
		}
		out.Actions = append(out.Actions, aiAction{Kind: "upgrade", Label: label, Plan: up.Plan, URL: pay + "/cart?plan=" + url.QueryEscape(up.Plan)})
	}
	if s.Reset != nil {
		out.Reset = &aiReset{
			Available: s.Reset.Available,
			LastAt:    stamp(s.Reset.LastAt),
			NextAt:    stamp(s.Reset.NextAt),
		}
	}
	out.Actions = append(out.Actions, aiAction{Kind: "topup", Label: "Buy usage credits", URL: pay})
	return out
}

// classState is where a class stands: limited when it is used up and nothing else
// pays, near past four fifths or while something past the plan pays, ok otherwise.
func classState(c limits.Class) string {
	used := c.Period.Percent()
	if c.Window != nil {
		used = max(used, c.Window.Percent())
	}
	switch {
	case c.Paying == "":
		return "limited"
	case c.Paying != limits.PaysPlan || used >= 80:
		return "near"
	}
	return "ok"
}

// worse is the worse of two states.
func worse(a, b string) string {
	rank := map[string]int{"ok": 0, "near": 1, "limited": 2}
	if rank[b] > rank[a] {
		return b
	}
	return a
}

func window(w limits.Window) *aiWindow {
	if w.Limit <= 0 {
		return nil
	}
	v := &aiWindow{Percent: w.Percent(), ResetsAt: stamp(w.ResetsAt)}
	switch {
	case w.Used >= w.Limit:
		v.State = "limited"
	case w.Used*5 >= w.Limit*4:
		v.State = "near"
	default:
		v.State = "ok"
	}
	return v
}

// stamp is a time as RFC3339, nil for none.
func stamp(t time.Time) *string {
	if t.IsZero() {
		return nil
	}
	r := t.UTC().Format(time.RFC3339)
	return &r
}

// aiRateQuery takes nothing: the organization is the one the caller acts in.
type aiRateQuery struct{}

// aiRateIn sets the organization's own requests a minute.
type aiRateIn struct {
	// Rpm is the model requests one person may make in a minute acting in the
	// organization: 1 up to the platform's default, or 0 to hold its people to the
	// default.
	Rpm int `json:"rpm"`
}

// aiRate is the per-minute limit as it governs the organization the caller acts in.
type aiRate struct {
	// Org is the organization.
	Org string `json:"org"`
	// Rpm is the model requests a minute one person may make acting in it now: its
	// own setting where it set less than the default, else the default.
	Rpm int `json:"rpm"`
	// Default is the platform's requests a minute, the most the organization may set.
	Default int `json:"default"`
	// Own is the organization's own setting, 0 when it holds its people to the default.
	Own int `json:"own"`
}

// Reads the per-minute limit as it governs the organization the caller acts in: the
// model requests one person may make in a minute on every surface — the API, chat,
// coding runs, bots and MCP — the platform's default, which is the most the
// organization may set, and the organization's own setting. Any member may read it.
//
// Response: {"org":"acme","rpm":30,"default":40,"own":30}
func (o limitOps) rate(ctx context.Context, _ *aiRateQuery) (*aiRate, error) {
	if !principal.ValidatedFrom(ctx) {
		return nil, principal.Unauthenticated()
	}
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	return o.rateOf(ctx, org), nil
}

// rateOf is org's standing against the per-minute limit.
func (o limitOps) rateOf(ctx context.Context, org string) *aiRate {
	rpm, def, own := o.l.Rates(ctx, org)
	return &aiRate{Org: org, Rpm: rpm, Default: def, Own: own}
}

// Sets the model requests one person may make in a minute acting in the
// organization the caller acts in, on every surface: 1 up to the platform's
// default, never more, or 0 to hold its people to the default. An owner or admin of
// the organization, or a SuperAdmin, may; a member may not, and a shared sign-up
// org, which has no admin to speak for its people, is refused. A change a page made
// with the caller's cookie, not the caller (cloud.CSRF), is refused (403). Live at
// once for every model request. On the audit trail before the write (ai.limits.rate.attempt)
// and with its outcome (ai.limits.rate), the setting before and after; a trail that
// does not take the attempt refuses the change (503), and nothing moves.
//
// Example: {"rpm":30}
// Response: {"org":"acme","rpm":30,"default":40,"own":30}
func (o limitOps) setRate(ctx context.Context, in *aiRateIn) (*aiRate, error) {
	if !principal.ValidatedFrom(ctx) {
		return nil, principal.Unauthenticated()
	}
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	if !cloud.Admin.Admits(cloud.AuthorityIn(ctx)) {
		return nil, zip.ErrForbidden("the requests a minute of " + org + "'s people are an owner's or admin's of the org to set")
	}
	if err := cloud.CSRF(ctx); err != nil {
		return nil, err
	}
	if org != authz.AdminOrg && authz.IsHeldOrg(org) {
		return nil, zip.ErrBadRequest(org + " is a shared sign-up org: it names no customer, so it sets no requests a minute of its own")
	}
	before := o.rateOf(ctx, org)
	if in.Rpm < 0 || in.Rpm > before.Default {
		return nil, zip.ErrBadRequest("rpm is 1 up to the platform's requests a minute, or 0 for the platform's")
	}
	who := cloud.Who(ctx)
	want := &aiRate{Org: org, Rpm: cmp.Or(in.Rpm, before.Default), Default: before.Default, Own: in.Rpm}
	if err := o.record(ctx, "ai.limits.rate.attempt", org, before, want, audit.Outcome{Result: "success", Status: http.StatusAccepted}); err != nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "the audit trail did not take the change, so it was not made")
	}
	if err := o.l.SetRate(ctx, org, in.Rpm, cmp.Or(who.Email, who.User)); err != nil {
		status := http.StatusServiceUnavailable
		if errors.Is(err, limits.ErrRate) {
			status = http.StatusBadRequest
		}
		_ = o.record(ctx, "ai.limits.rate", org, before, before, audit.Outcome{Result: "error", Status: status, Reason: err.Error()})
		return nil, zip.Errorf(status, "%v", err)
	}
	after := o.rateOf(ctx, org)
	if err := o.record(ctx, "ai.limits.rate", org, before, after, audit.Outcome{Result: "success", Status: http.StatusOK}); err != nil {
		slog.Error("ai rate: the outcome of a change could not be audited", "org", org, "err", err)
	}
	return after, nil
}

// record puts one act on org's requests a minute on the audit trail; a process with
// no trail is an error.
func (o limitOps) record(ctx context.Context, action, org string, before, after *aiRate, outcome audit.Outcome) error {
	if o.trail == nil {
		return errors.New("ai: no audit trail on this process")
	}
	who := cloud.Who(ctx)
	was, _ := json.Marshal(before)
	now, _ := json.Marshal(after)
	_, err := o.trail.Append(ctx, audit.Record{
		Actor:     audit.Actor{Org: org, Sub: who.User, Email: who.Email},
		Action:    action,
		Resource:  audit.Resource{Type: "org", ID: org},
		Auth:      audit.AuthContext{Method: "attested", IsAdmin: principal.SuperFrom(ctx)},
		Outcome:   outcome,
		SourceIP:  who.IP,
		RequestID: who.RequestID,
		Before:    was,
		After:     now,
	})
	return err
}
