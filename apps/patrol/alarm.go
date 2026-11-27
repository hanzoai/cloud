package patrol

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/mail"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/cloud/apps/framework"
	"github.com/hanzoai/cloud/client"
	notifypeer "github.com/hanzoai/cloud/client/notify"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/text"
	"github.com/zap-proto/zip"
)

// alarm.go is where an activation becomes an incident.
//
// One intake: the receiving centre's machine credential posts here, and so does
// a controller keying an activation by hand. A DC-09 receiver, when there is
// one, normalizes into this same body and calls this same path.
//
// An activation is never dropped. A body that matches a site opens an incident
// and answers 201; a body that matches none is still recorded, raises an amber
// event, and answers 202.

// Intake caps. An activation is a reference, a zone, a trigger phrase and the
// panel's own payload; anything longer is noise.
const (
	maxRef     = 64
	maxTrigger = 300
	maxRaw     = 16 * 1024
	maxDetail  = 4 * 1024
)

// match resolves the site an activation belongs to: the document the caller
// named, else the site whose account is the panel account that reported. It
// returns an empty document when neither resolves.
func (o ops) match(ctx context.Context, org, site, account string) (framework.Document, bool) {
	if site != "" {
		if d, err := framework.Get(ctx, org, DTSite, site); err == nil {
			return d, true
		}
	}
	if account == "" {
		return framework.Document{}, false
	}
	name, err := framework.FindByField(ctx, org, DTSite, "account", account)
	if err != nil || name == "" {
		return framework.Document{}, false
	}
	d, err := framework.Get(ctx, org, DTSite, name)
	if err != nil {
		return framework.Document{}, false
	}
	return d, true
}

// take records an activation and, when it matches a site, opens the incident
// that answers it. Everything after the two document writes is best effort: an
// activation that is on record and an incident that is open are the facts an
// operation runs on.
func (o ops) take(ctx context.Context, org string, in *patrolAlarmIn) (*patrolAlarmOut, error) {
	// The lease covers the two document writes and is given back before them: what
	// follows is mail and a fan-out, which are somebody else's network and have no
	// bound this code can promise, and a lease held across that lapses under its
	// own holder and lets the next caller in while this one is still writing. It
	// is the same split apply and after are, for the same reason.
	var lease framework.Lease
	release := func() {
		if lease != nil {
			_ = lease.Release(ctx)
			lease = nil
		}
	}
	defer release()

	ref := text.Trim(in.Ref, maxRef)
	if ref != "" {
		// The receiving centre's own reference identifies the activation, so a
		// retry — a timeout on its side, a redelivery, a panel repeating itself —
		// is the same activation and not a second one. Two incidents for one
		// break-in is two units dispatched and two accounts filed.
		taken, held, err := framework.AcquireLease(ctx, org, "alarm/"+ref, actLife, actWait)
		if err != nil {
			return nil, zip.Errorf(http.StatusInternalServerError, "record activation")
		}
		if !held {
			return nil, zip.Errorf(http.StatusConflict, "activation %s is being taken", ref)
		}
		lease = taken
		if again, ok := o.already(ctx, org, ref); ok {
			return again, nil
		}
	}
	account := text.Trim(in.Account, maxRef)
	zone := text.Trim(in.Zone, maxRef)
	trigger := text.Trim(in.Trigger, maxTrigger)
	source := text.Trim(in.Source, maxRef)
	at := now()
	raw := ""
	if len(in.Raw) > 0 && len(in.Raw) <= maxRaw {
		raw = string(in.Raw)
	}

	st, matched := o.match(ctx, org, strings.TrimSpace(in.Site), account)
	row := map[string]any{
		"ref": ref, "account": account, "zone": zone,
		"trigger": trigger, "source": source, "at": at,
	}
	if raw != "" {
		row["raw"] = json.RawMessage(raw)
	}
	if !matched {
		// The activation's own document name, which is what `already` answers a
		// redelivery of this same reference with: one field, one value, whichever
		// of the two answered.
		made, err := framework.Ingest(ctx, org, DTAlarm, row, "")
		if err != nil {
			return nil, o.fail(err, "record activation")
		}
		release()
		o.raise(ctx, org, LevelAmber, "Activation with no matching site", "", "",
			fmt.Sprintf("account %s zone %s: %s", account, zone, trigger))
		return &patrolAlarmOut{Data: patrolActivation{Alarm: made.Name, Matched: false}}, nil
	}

	row["site"] = st.Name
	inc, err := o.open(ctx, org, st, zone, ref, trigger)
	if err != nil {
		return nil, o.fail(err, "open incident")
	}
	row["incident"] = inc.Name
	made, err := framework.Ingest(ctx, org, DTAlarm, row, "")
	if err != nil {
		return nil, o.fail(err, "record activation")
	}
	release()
	o.setSite(ctx, org, st.Name, SiteAlarm)
	o.raise(ctx, org, LevelRed, "Activation at "+st.Name, st.Name, "", trigger)
	opened := incidentRow(inc)
	o.publish(ctx, org, frame{Kind: "incident", Incident: &opened})
	o.tellControllers(ctx, org, "Activation at "+st.Name,
		fmt.Sprintf("Incident %s opened at %s (zone %s): %s", inc.Name, st.Name, zone, trigger))
	return &patrolAlarmOut{Data: patrolActivation{
		Alarm: made.Name, Matched: true, Site: st.Name,
		Incident: o.viewIncident(ctx, org, inc, o.reachOf(ctx, org)),
	}}, nil
}

// already answers what an activation already on record became, so a redelivery
// of one the operation is working is answered rather than run again.
//
// The reference is the receiving centre's own and centres recycle them, so it
// identifies an activation for as long as that activation is live and no longer.
// The response it opened is live while the incident is open; an activation that
// matched no site is live for as long as a redelivery takes. Past that, the same
// reference is the NEXT break-in at that site, and answering it with an incident
// that closed hours ago dispatches nobody and tells the centre somebody was sent.
func (o ops) already(ctx context.Context, org, ref string) (*patrolAlarmOut, bool) {
	docs, err := framework.Search(ctx, org, DTAlarm, map[string]string{"ref": ref}, 1)
	if err != nil || len(docs) == 0 {
		return nil, false
	}
	d := docs[0]
	site := strField(d, "site")
	out := patrolActivation{Alarm: d.Name, Matched: site != "", Site: site}
	name := strField(d, "incident")
	if name == "" {
		taken, ok := at(strField(d, "at"))
		if !ok || time.Now().UTC().Sub(taken) > retryLife {
			return nil, false
		}
		return &patrolAlarmOut{Data: out}, true
	}
	inc, err := framework.Get(ctx, org, DTIncident, name)
	if err != nil || strField(inc, "state") == Closed {
		return nil, false
	}
	out.Incident = o.viewIncident(ctx, org, inc, o.reachOf(ctx, org))
	return &patrolAlarmOut{Data: out}, true
}

// retryLife is how long an activation that matched no site answers its own
// reference again. It covers a redelivery — a timeout on the centre's side, a
// panel repeating itself — and nothing longer.
const retryLife = 15 * time.Minute

// raise writes one patrol.event and publishes it. The live feed row and the
// stored row are the same value written once.
func (o ops) raise(ctx context.Context, org, level, title, site, unit, detail string) {
	row := map[string]any{
		"level": level,
		"title": text.Trim(title, maxTrigger),
		"at":    now(),
	}
	if site != "" {
		row["site"] = site
	}
	if unit != "" {
		row["unit"] = unit
	}
	if detail != "" {
		row["detail"] = text.Trim(detail, maxDetail)
	}
	made, err := framework.Ingest(ctx, org, DTEvent, row, "")
	if err != nil {
		o.s.Log.Warn("patrol: event not written", "org", org, "title", title, "err", err)
		return
	}
	d, err := framework.Get(ctx, org, DTEvent, made.Name)
	if err != nil {
		return
	}
	v := viewEvent(d)
	o.publish(ctx, org, frame{Kind: "event", Event: &v})
}

// contacts reads one address list off the tenant document — contacts is a JSON
// object of named lists, and this returns the one asked for.
func (o ops) contacts(ctx context.Context, org, list string) []string {
	t, err := framework.Get(ctx, org, DTTenant, DTTenant.Name)
	if err != nil {
		if !errors.Is(err, framework.ErrNotFound) {
			o.s.Log.Warn("patrol: tenant not read", "org", org, "err", err)
		}
		return nil
	}
	var lists map[string][]string
	if err := json.Unmarshal(jsonField(t, "contacts"), &lists); err != nil {
		return nil
	}
	return lists[list]
}

// tellControllers emails the org's controller list. Best effort, and silent
// where no list is configured.
func (o ops) tellControllers(ctx context.Context, org, subject, body string) {
	for _, to := range o.contacts(ctx, org, "controllers") {
		o.mail(ctx, org, to, subject, body)
	}
}

// tellOfficer emails the officer a dispatch put on the incident, and the site's
// keyholder contact when the site holds keys.
func (o ops) tellOfficer(ctx context.Context, org, unit string, inc framework.Document) {
	subject := "Dispatch " + inc.Name
	body := fmt.Sprintf("Attend %s: %s.", strField(inc, "site"), strField(inc, "trigger"))
	if u, err := framework.Get(ctx, org, DTUnit, unit); err == nil {
		o.mail(ctx, org, strField(u, "officer"), subject, body)
	}
	st, err := framework.Get(ctx, org, DTSite, strField(inc, "site"))
	if err == nil && intField(st, "keys") == 1 {
		o.mail(ctx, org, strField(st, "contact"), subject, body)
	}
}

// mail sends one message over the plane. An address that is not there is not an
// error: a tenant that has configured no contact simply gets no mail.
//
// The addresses are free text off a site's contact field and a unit's officer
// field, so they are parsed rather than trusted. A carriage return or a newline
// inside one is how a recipient list becomes a header list — a second Bcc, a
// second Subject — and mail.ParseAddress refuses every one of them along with
// everything else that is not an address.
func (o ops) mail(ctx context.Context, org, to, subject, body string) {
	addr, err := mail.ParseAddress(strings.TrimSpace(to))
	if err != nil {
		return
	}
	to = addr.Address
	if _, err := notifypeer.NotifySend(cloud.For(ctx, org), &client.Send{
		Org: org, Channel: "email", To: to, Subject: subject, Body: body,
	}); err != nil {
		o.s.Log.Warn("patrol: notify", "org", org, "to", to, "err", err)
	}
}

// admitAlarm is the intake's admission: the receiving centre's own role, or the
// control room keying an activation in by hand.
//
// The centre holds `centre` and nothing else, because "may post an activation" is
// the whole of what its credential is for. The control room's role is the
// operation itself — the key register, a way into any camera at any premises, and
// every incident — and a third party that sends us activations is not the
// operation.
//
// The role is granted in IAM, as every other role is: a label the identity
// boundary puts on a machine credential is not a grant, and every app registered
// in the org carries one.
func (o ops) admitAlarm(ctx context.Context) (string, error) {
	return o.admit(ctx, intake...)
}

// takes is how many activations ONE CREDENTIAL may post in a minute, and ceiling
// is what the whole tenant may. A receiving centre sends a handful an hour; a
// panel stuck in a loop, or a credential somebody found, sends thousands, and
// each one is two document writes and an email.
//
// Two bounds because one is not enough and neither is the other. A bound on the
// org alone is paid by whoever draws second: the flood spends the allowance and
// the control room's own hand-keyed activation is the one refused, which turns a
// noisy credential into an outage of an alarm-response service. A bound on the
// credential alone leaves the store behind it open to as many credentials as an
// org has. So the credential's allowance is drawn first and the tenant's ceiling
// sits above it, an order of magnitude up, where it bounds the sum without
// standing in any one caller's way.
var (
	takes   = rate{per: time.Minute / 120, burst: 60}
	ceiling = rate{per: time.Minute / 1200, burst: 600}
)

// afford draws one activation's allowance: the caller's own, then the tenant's.
// The caller is asked first so a refusal costs the tenant nothing.
func (o ops) afford(ctx context.Context, org string) bool {
	return takes.take(actor(ctx)) && ceiling.take(org)
}

// rate is a token bucket per key, held in memory. Best effort by construction:
// it bounds what one process will take, which is what protects the store behind
// it, and a fleet of replicas bounds proportionally.
type rate struct {
	mu    sync.Mutex
	per   time.Duration
	burst int
	seen  map[string]bucket
}

// bucket is one key's allowance and the moment it was last drawn on.
type bucket struct {
	tokens float64
	at     time.Time
}

// take draws one token for a key, and reports whether there was one.
//
// A key whose bucket has refilled to the brim is a caller that has stopped, and
// its allowance is the one every fresh key starts with — so the row says nothing
// and is dropped. Without that the map is a row per credential and per org this
// process has ever answered, which is a caller-named set that only grows.
func (r *rate) take(key string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.seen == nil {
		r.seen = map[string]bucket{}
	}
	now := time.Now()
	r.forget(now)
	b, held := r.seen[key]
	if !held {
		b = bucket{tokens: float64(r.burst), at: now}
	}
	b.tokens += now.Sub(b.at).Seconds() / r.per.Seconds()
	if b.tokens > float64(r.burst) {
		b.tokens = float64(r.burst)
	}
	b.at = now
	if b.tokens < 1 {
		r.seen[key] = b
		return false
	}
	b.tokens--
	r.seen[key] = b
	return true
}

// forget drops every key whose allowance has gone back to full. Held by r.mu.
func (r *rate) forget(now time.Time) {
	full := time.Duration(float64(r.burst) * float64(r.per))
	for key, b := range r.seen {
		if now.Sub(b.at) >= full {
			delete(r.seen, key)
		}
	}
}

// unavailable is the answer when the framework model is not installed in the
// caller's org, which is the one thing an intake cannot work around.
func (o ops) installed(ctx context.Context, org string) error {
	if framework.Installed(ctx, org, DTAlarm) {
		return nil
	}
	return zip.Errorf(http.StatusServiceUnavailable, "patrol not installed for %s", org)
}
