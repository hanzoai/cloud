// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.

package flags

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/hanzoai/cloud/sdk/audit"
	"github.com/hanzoai/cloud/sdk/principal"
)

// The one migration of the usage policy from compiled defaults to saved values.
//
// The binary used to ship each plan's included usage (and each costly model
// family's share of it) as defaults, read wherever the flag store held nothing. A
// plan now includes only what is saved for it, so before any reader is answered
// the figures the binary shipped are SAVED, once, for every switch that holds
// nothing: after this a plan that was served on its defaults is served on the
// same figures, saved, and a SuperAdmin's edit at admin.hanzo.ai is the only
// thing that changes one. A switch that holds a value, an admin's or an earlier
// run's, is never written.
//
// Nothing that decides a request reads this file's table. It is the record of
// what was shipped, kept for the stores that have not met this binary yet.

// Carried is one plan's included usage as the binary shipped it, per seat.
type Carried struct {
	Session int // requests in one session window
	Day     int // requests in one UTC day
	Premium int // US cents of premium models at list price per billing period
	Ours    int // US cents of Hanzo's models at list price per billing period
}

// CarriedPlans are the shipped figures, by plan slug: the last defaults, as of
// the release that stopped reading defaults.
var CarriedPlans = map[string]Carried{
	// Monthly plans cover Hanzo-hosted models (ours: Enso, Zen, Kai) ONLY.
	// Third-party models (premium) are prepaid only and never included (0).
	"go":        {Session: 50, Day: 150, Premium: 0, Ours: 500},
	"pro-3x":    {Session: 150, Day: 450, Premium: 0, Ours: 1500},
	"dev-5x":    {Session: 250, Day: 750, Premium: 0, Ours: 2500},
	"max-15x":   {Session: 750, Day: 2250, Premium: 0, Ours: 7500},
	"ultra":     {Session: 1250, Day: 3750, Premium: 0, Ours: 12500},
	"ultimate":  {Session: 3750, Day: 11250, Premium: 0, Ours: 37500},
	"unlimited": {Session: 0, Day: 0, Premium: 0, Ours: Unbounded},
	// Retired rungs keep their Hanzo-only figures for existing holders.
	"dev":          {Session: 50, Day: 150, Premium: 0, Ours: 500},
	"max-5x":       {Session: 250, Day: 750, Premium: 0, Ours: 2500},
	"max-20x":      {Session: 1000, Day: 3000, Premium: 0, Ours: 10000},
	"max-50x":      {Session: 2500, Day: 7500, Premium: 0, Ours: 25000},
	"team":         {Session: 50, Day: 150, Premium: 0, Ours: 500},
	"team-premium": {Session: 250, Day: 750, Premium: 0, Ours: 2500},
	"agency":       {Session: 1000, Day: 3000, Premium: 0, Ours: 5000},
	"advisory":     {Session: 1000, Day: 3000, Premium: 0, Ours: 5000},
	"dedicated":    {Session: 1000, Day: 3000, Premium: 0, Ours: 5000},
	"pod":          {Session: 1000, Day: 3000, Premium: 0, Ours: 5000},
	"white-label":  {Session: 100, Day: 300, Premium: 0, Ours: 500},
}

// carriedShare is the share of a plan's premium allowance one costly family took
// by default, in percent.
const carriedShare = 30

// Values are a plan's nine switches as a save of c writes them, by field
// (PlanFields): no short window, and prepaid paying past the plan.
func (c Carried) Values() map[string]string {
	return map[string]string{
		"session": strconv.Itoa(c.Session), "day": strconv.Itoa(c.Day),
		"premium": strconv.Itoa(c.Premium), "premium_window": "0", "premium_window_hours": "0",
		"ours": strconv.Itoa(c.Ours), "ours_window": "0", "ours_window_hours": "0",
		"overage": "true",
	}
}

// CarriedValues is every switch the migration saves where none is held, by key:
// each carried plan's nine figures and the model policy.
func CarriedValues() map[string]string {
	out := map[string]string{}
	caps := map[string]int{}
	for plan, c := range CarriedPlans {
		for field, v := range c.Values() {
			out[PlanKey(field, plan)] = v
		}
		if c.Premium <= 0 || c.Premium == Unbounded {
			continue // no premium allowance included in monthly plans
		}
		caps[plan] = (c.Premium*carriedShare/100 + 5) / 10 * 10
	}
	out[ModelPolicy] = modelPolicy(caps)
	return out
}

// carrier is who the migration's writes are attributed to.
const carrier = "platform"

// errHeld says a switch already holds a value, so the migration left it.
var errHeld = errors.New("held")

// keep stores definition under key only where the store holds none, and reports
// whether it wrote. The check and the write are one step under the key's lock, so
// an admin's save landing beside it is never overwritten.
func (s *Store) keep(ctx context.Context, key string, definition json.RawMessage, actor string) (bool, error) {
	err := s.Edit(ctx, key, actor, func(stored json.RawMessage) (json.RawMessage, error) {
		if stored != nil {
			return nil, errHeld
		}
		return definition, nil
	})
	if errors.Is(err, errHeld) {
		return false, nil
	}
	return err == nil, err
}

// carry saves every carried switch the platform store holds nothing for and
// answers what it wrote, by key. Idempotent: a second run writes nothing.
func (c *Client) carry(ctx context.Context) (map[string]string, error) {
	st, err := c.platform()
	if err != nil {
		return nil, err
	}
	values := CarriedValues()
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	// A plan is named once all nine of its switches are saved, and its session
	// window is the last key of its nine in this order only by accident; what makes
	// a part-written plan safe is that a rerun writes the rest.
	slices.Sort(keys)
	// One read says what is held, so a store that met this binary before is asked
	// once and written never; keep still decides each write under the key's lock.
	rows, err := st.List()
	if err != nil {
		return nil, err
	}
	held := make(map[string]bool, len(rows))
	for _, r := range rows {
		held[r.Key] = true
	}
	wrote := map[string]string{}
	for _, key := range keys {
		if held[key] {
			continue
		}
		v := values[key]
		def, err := Valued(v, v == "true" || v == "false")
		if err != nil {
			return wrote, err
		}
		ok, err := st.keep(ctx, key, def, carrier)
		if err != nil {
			return wrote, err
		}
		if ok {
			wrote[key] = v
		}
	}
	if len(wrote) > 0 {
		c.invalidate()
	}
	return wrote, nil
}

// carried runs the migration before the flags app answers anything, so no reader
// meets a plan between its defaults and its saved figures. A store that cannot be
// written is retried until it can: the app still mounts, since every other switch
// is worth serving. What was written is put on the audit trail as the platform's
// act; the figures are what every reader was already served, so they stand while
// the trail is asked again.
func (c *Client) carried(trail *audit.Recorder) {
	run := func() bool {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		wrote, err := c.carry(ctx)
		if len(wrote) > 0 {
			slog.Info("flags: saved the usage policy's shipped figures where none was saved", "switches", len(wrote))
			go record(trail, wrote)
		}
		if err != nil {
			slog.Error("flags: the usage policy's shipped figures are not all saved; a plan with none saved includes nothing until they are", "err", err)
		}
		return err == nil
	}
	if run() {
		return
	}
	go func() {
		for !run() {
			time.Sleep(5 * time.Second)
		}
	}()
}

// record puts the migration's writes on the audit trail, asking until it takes
// them.
func record(trail *audit.Recorder, wrote map[string]string) {
	if trail == nil {
		return
	}
	after, err := json.Marshal(wrote)
	if err != nil {
		return
	}
	for wait := time.Second; ; wait = min(2*wait, time.Minute) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		_, err := trail.Append(ctx, audit.Record{
			Actor:    audit.Actor{Org: principal.Reserved, Sub: carrier},
			Action:   "admin.usage.carry",
			Resource: audit.Resource{Type: "usage_policy", ID: "shipped"},
			Auth:     audit.AuthContext{Method: "platform", IsAdmin: true},
			Outcome:  audit.Outcome{Result: "success", Status: http.StatusOK, Reason: "the figures the binary shipped as defaults, saved once where none was saved"},
			After:    audit.Redact(after),
		})
		cancel()
		if err == nil {
			return
		}
		slog.Warn("flags: the audit trail did not take the usage policy migration; asking again", "err", err)
		time.Sleep(wait)
	}
}
