package patrol

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/hanzoai/cloud/apps/framework"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/hanzoai/cloud/sdk/text"
	"github.com/zap-proto/zip"
)

// incident.go is the incident state machine: one table of acts, one function
// that applies them.
//
// The nine states run forward only. Every act stamps its own timestamp on the
// incident, writes the new state, and appends one patrol.step. An act from a
// state it does not leave is 409 naming the state the incident is in; a caller
// without one of the act's roles gets the refusal principal returns.

// How long one act holds an incident, and how long another waits for it. The
// lease is only the crash net: an act is a handful of writes, so a holder that
// has not finished within actLife is gone, and a caller that has waited actWait
// is told the incident is busy.
const (
	actLife = 15 * time.Second
	actWait = 5 * time.Second
)

// The nine states of patrol.incident.state.
const (
	Received   = "received"
	Verified   = "verified"
	Dispatched = "dispatched"
	Enroute    = "enroute"
	Onsite     = "onsite"
	Checked    = "checked"
	Secured    = "secured"
	Filed      = "filed"
	Closed     = "closed"
)

// The nine acts a caller may perform.
const (
	Verify   = "VERIFY"
	Dispatch = "DISPATCH"
	Roll     = "ROLL"
	Arrive   = "ARRIVE"
	Check    = "CHECK"
	Secure   = "SECURE"
	File     = "FILE"
	Close    = "CLOSE"
	Escalate = "ESCALATE"
)

// move is one row of the transition table: the states the act leaves, the state
// it lands in, the incident field it stamps, and the roles admitted to it.
type move struct {
	from   []string
	to     string // empty leaves the state where it is
	stamp  string
	admits []string
}

// acts is the whole state machine. Nothing else decides what an incident may do
// next.
var acts = map[string]move{
	Verify:   {from: []string{Received}, to: Verified, stamp: "verified", admits: control},
	Dispatch: {from: []string{Verified}, to: Dispatched, stamp: "dispatched", admits: control},
	Roll:     {from: []string{Dispatched}, to: Enroute, stamp: "enroute", admits: crew},
	Arrive:   {from: []string{Enroute}, to: Onsite, stamp: "onsite", admits: crew},
	Check:    {from: []string{Onsite}, to: Checked, stamp: "checked", admits: crew},
	Secure:   {from: []string{Checked}, to: Secured, stamp: "secured", admits: crew},
	File:     {from: []string{Secured}, to: Filed, stamp: "filed", admits: control},
	Close:    {from: []string{Secured, Filed}, to: Closed, stamp: "closed", admits: shift},
	Escalate: {from: []string{Dispatched, Enroute, Onsite, Checked, Secured}, stamp: "escalated", admits: control},
}

// lands is the state the act leaves the incident in: the one it moves to, or the
// one it was already in for an act that stamps without moving.
func (m move) lands(state string) string {
	if m.to == "" {
		return state
	}
	return m.to
}

// leaves reports whether the act may be performed from state.
func (m move) leaves(state string) bool {
	return slices.Contains(m.from, state)
}

// open records one incident against a site, in state received, carrying the
// site's tier and SLA. It is the one creator: alarm intake calls it and so does
// POST /v1/patrol/incident.
//
// The incident and the first step land together: an incident whose trail does
// not say it was opened is not a record, so a failed step takes the incident
// with it.
func (o ops) open(ctx context.Context, org string, st framework.Document, zone, ref, trigger string) (framework.Document, error) {
	at := now()
	made, err := framework.Ingest(ctx, org, DTIncident, map[string]any{
		"site":    st.Name,
		"zone":    zone,
		"ref":     ref,
		"trigger": trigger,
		"tier":    strField(st, "tier"),
		"sla":     float64(intField(st, "sla")),
		"state":   Received,
		"opened":  at,
	}, "")
	if err != nil {
		return framework.Document{}, err
	}
	if _, err := o.record(ctx, org, made.Name, "OPEN", "", Received, ""); err != nil {
		if err := framework.Delete(ctx, org, DTIncident, made.Name); err != nil {
			o.s.Log.Warn("patrol: incident opened with no trail", "org", org, "incident", made.Name, "err", err)
		}
		return framework.Document{}, err
	}
	inc, err := framework.Get(ctx, org, DTIncident, made.Name)
	if err != nil {
		return framework.Document{}, err
	}
	return inc, nil
}

// advance applies one act to one incident and then runs what the act sets in
// motion.
//
// The two are separate because they are bounded differently. Deciding and
// writing is a handful of store calls under a lease; the side effects are mail,
// which is somebody else's network and has no bound this code can promise. A
// lease held across them can expire under its own holder, and the next caller is
// then let in while the first is still writing.
func (o ops) advance(ctx context.Context, name string, in *patrolIncidentAct) (framework.Document, error) {
	org, a, inc, unit, err := o.apply(ctx, name, in)
	if err != nil {
		return framework.Document{}, err
	}
	o.after(ctx, org, a, inc, unit)
	// The frame carries the incident's own row and not its trail: one frame goes
	// to every screen watching, including a customer's, and who worked an act and
	// what they wrote on it are the operation's.
	row := incidentRow(inc)
	o.publish(ctx, org, frame{Kind: "incident", Incident: &row})
	return inc, nil
}

// apply is the act itself: it admits the caller for that act, refuses a
// transition the state does not allow, stamps the act's timestamp, writes the
// new state and appends the step. It answers the org, the act, the incident as
// it now stands, and the unit the act bound.
//
// One act at a time, per incident. Reading the state, deciding, and writing the
// new one is three steps, and two callers racing the same act would both read
// `received`, both pass the table and both land — one transition, twice, with
// two rows on the trail. The engine's lease is what the whole fleet contends on,
// so the answer is the same behind one replica or four; a caller that waits out
// the window is told the incident is busy rather than let through.
func (o ops) apply(ctx context.Context, name string, in *patrolIncidentAct) (string, string, framework.Document, string, error) {
	a := strings.ToUpper(strings.TrimSpace(in.Act))
	m, ok := acts[a]
	if !ok {
		return "", a, framework.Document{}, "", zip.ErrBadRequest("unknown act " + a)
	}
	org, err := o.admit(ctx, m.admits...)
	if err != nil {
		return "", a, framework.Document{}, "", err
	}
	lease, held, err := framework.AcquireLease(ctx, org, "incident/"+name, actLife, actWait)
	if err != nil {
		return "", a, framework.Document{}, "", zip.Errorf(http.StatusInternalServerError, "advance incident")
	}
	if !held {
		return "", a, framework.Document{}, "", zip.Errorf(http.StatusConflict, "incident %s is being worked", name)
	}
	defer func() { _ = lease.Release(ctx) }()
	inc, err := framework.Get(ctx, org, DTIncident, name)
	if err != nil {
		if errors.Is(err, framework.ErrNotFound) {
			return "", a, framework.Document{}, "", zip.ErrNotFound("incident not found")
		}
		return "", a, framework.Document{}, "", zip.Errorf(http.StatusInternalServerError, "read incident")
	}
	state := strField(inc, "state")
	if !m.leaves(state) {
		return "", a, framework.Document{}, "", zip.Errorf(http.StatusConflict,
			"incident %s is %s; %s runs from %s", name, state, a, strings.Join(m.from, " or "))
	}
	// An act with no landing state leaves the incident in a state its own `from`
	// set names, so the table alone would let it run again and again — each pass
	// restamping, appending a step and mailing the controllers. Its stamp is
	// where it landed, so the stamp is what says it has already been applied.
	if m.to == "" && strField(inc, m.stamp) != "" {
		return "", a, framework.Document{}, "", zip.Errorf(http.StatusConflict,
			"incident %s was %s at %s", name, m.stamp, strField(inc, m.stamp))
	}

	change := map[string]any{m.stamp: now()}
	if m.to != "" {
		change["state"] = m.to
	}
	unit := strings.TrimSpace(in.Unit)
	switch a {
	case Dispatch:
		if unit == "" {
			unit = strField(inc, "unit")
		}
		if unit == "" {
			return "", a, framework.Document{}, "", zip.ErrBadRequest("dispatch needs a unit")
		}
		change["unit"] = unit
	case Roll, Arrive, Check, Secure:
		if err := o.ownUnit(ctx, org, strField(inc, "unit")); err != nil {
			return "", a, framework.Document{}, "", err
		}
	case Close:
		if in.Outcome != "" {
			change["outcome"] = text.Trim(in.Outcome, maxDetail)
		}
		change["false"] = in.False
		// Closing from secured writes the report the incident never had, so the
		// filed stamp lands with it.
		if strField(inc, "filed") == "" {
			change["filed"] = change[m.stamp]
		}
	}
	// The step and the state land together. The step is written first because it
	// is the record of the act, and a state that moved with nothing on the trail
	// behind it cannot be told from one that was never earned; a state write that
	// then fails takes the step back with it.
	step, err := o.record(ctx, org, name, a, state, m.lands(state), in.Detail)
	if err != nil {
		return "", a, framework.Document{}, "", o.fail(err, "record act")
	}
	if err := framework.UpdateData(ctx, org, DTIncident, name, merge(inc.Data, change)); err != nil {
		if err := framework.Delete(ctx, org, DTStep, step); err != nil {
			o.s.Log.Warn("patrol: step left by a transition that did not land",
				"org", org, "incident", name, "step", step, "err", err)
		}
		return "", a, framework.Document{}, "", o.fail(err, "advance incident")
	}
	inc, err = framework.Get(ctx, org, DTIncident, name)
	if err != nil {
		return "", a, framework.Document{}, "", zip.Errorf(http.StatusInternalServerError, "read incident")
	}
	return org, a, inc, unit, nil
}

// after runs the act's side effect. Every one of them is best effort: the
// incident has already moved, and a failed notification or a failed event row
// must not unwind it.
func (o ops) after(ctx context.Context, org, a string, inc framework.Document, unit string) {
	site := strField(inc, "site")
	switch a {
	case Verify:
		o.raise(ctx, org, LevelGreen, "Activation verified", site, "", strField(inc, "trigger"))
	case Dispatch:
		o.setUnit(ctx, org, unit, UnitAssigned)
		o.tellOfficer(ctx, org, unit, inc)
	case Roll:
		o.setUnit(ctx, org, strField(inc, "unit"), UnitEnroute)
	case Arrive:
		o.setUnit(ctx, org, strField(inc, "unit"), UnitOnsite)
	case Secure:
		o.setSite(ctx, org, site, SiteOK)
		o.raise(ctx, org, LevelGreen, "Site secured", site, strField(inc, "unit"), "")
	case File:
		o.fileReport(ctx, org, inc)
	case Close:
		if !o.reported(ctx, org, inc.Name) {
			o.fileReport(ctx, org, inc)
		}
		o.setUnit(ctx, org, strField(inc, "unit"), UnitAvailable)
	case Escalate:
		o.raise(ctx, org, LevelRed, "Incident escalated", site, strField(inc, "unit"), strField(inc, "trigger"))
		o.tellControllers(ctx, org, "Incident "+inc.Name+" escalated",
			fmt.Sprintf("Incident %s at %s escalated.", inc.Name, site))
	}
}

// record appends one patrol.step and answers its name. The trail is the whole
// audit: nothing else writes it, patrol.step names no writer on the generic
// surface, and a caller whose step could not be written performed no act.
func (o ops) record(ctx context.Context, org, incident, a, from, to, detail string) (string, error) {
	made, err := framework.Ingest(ctx, org, DTStep, map[string]any{
		"incident": incident,
		"act":      a,
		"from":     from,
		"to":       to,
		"actor":    actor(ctx),
		"detail":   text.Trim(detail, maxDetail),
		"at":       now(),
	}, "")
	if err != nil {
		o.s.Log.Warn("patrol: step not recorded", "org", org, "incident", incident, "act", a, "err", err)
		return "", err
	}
	return made.Name, nil
}

// setUnit writes one unit's state.
func (o ops) setUnit(ctx context.Context, org, name, state string) {
	if name == "" {
		return
	}
	u, err := framework.Get(ctx, org, DTUnit, name)
	if err != nil {
		o.s.Log.Warn("patrol: unit not found", "org", org, "unit", name, "err", err)
		return
	}
	if err := framework.UpdateData(ctx, org, DTUnit, name, merge(u.Data, map[string]any{"state": state})); err != nil {
		o.s.Log.Warn("patrol: unit state not written", "org", org, "unit", name, "err", err)
	}
}

// setSite writes one site's status.
func (o ops) setSite(ctx context.Context, org, name, status string) {
	if name == "" {
		return
	}
	st, err := framework.Get(ctx, org, DTSite, name)
	if err != nil {
		o.s.Log.Warn("patrol: site not found", "org", org, "site", name, "err", err)
		return
	}
	if err := framework.UpdateData(ctx, org, DTSite, name, merge(st.Data, map[string]any{"status": status})); err != nil {
		o.s.Log.Warn("patrol: site status not written", "org", org, "site", name, "err", err)
	}
}

// ownUnit refuses an officer acting on a unit that is not theirs. A caller
// holding any control role passes: the control room works every unit.
func (o ops) ownUnit(ctx context.Context, org, unit string) error {
	if _, ok := requestOf(ctx); !ok {
		return principal.RefusedFrom(ctx)
	}
	held, err := o.roles(ctx, org)
	if err != nil {
		return err
	}
	if holdsAny(held, control...) || holdsAny(held, framework.RoleSystemManager) {
		return nil
	}
	if unit == "" {
		return principal.RefusedFrom(ctx)
	}
	u, err := framework.Get(ctx, org, DTUnit, unit)
	if err != nil {
		return zip.ErrNotFound("unit not found")
	}
	if !sameOfficer(strField(u, "officer"), who(ctx)) {
		return principal.RefusedFrom(ctx)
	}
	return nil
}

// sameOfficer matches a unit's officer against the caller's subject, ignoring
// case and surrounding space.
func sameOfficer(officer, user string) bool {
	officer, user = strings.TrimSpace(officer), strings.TrimSpace(user)
	return officer != "" && user != "" && strings.EqualFold(officer, user)
}
