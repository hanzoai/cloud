// Copyright © 2026 Hanzo AI. MIT License.

package commerce

// account.go — ORGS ON ACCOUNT: billed in arrears, never refused for money.
//
// An org on account settles out of band. Every call it makes is metered, limited and
// logged as anyone's; what it costs accrues on the org's own account as what it owes
// — the receivable GET /v1/admin/credit lists, with how old it is and what the month
// used — until a recorded wire (POST /v1/admin/customers/:org/credit, source
// prepaid) pays it down. Which orgs are on account is a platform switch only a
// SuperAdmin sets (client.OnAccount), and the switch store audits each change.
// finance asks [arrears.on] (finance.SetAccount) and gives an org on account a line
// with no limit, so no gate refuses one of its calls for money.
//
// The SuperAdmins are told when one owes past client.OnAccountAlert ([arrears.owing]).
//
// Every gate in the process reads the org's own account through finance, so the
// switches are never read on a gate's time but the first: once the process has an
// answer, a stale one is read again in the background, at most once every
// [switchTTL], and the last answer stands meanwhile and while the flag plane cannot
// be read. A flag plane that is down neither stops an org on account nor puts one
// on, and never holds up a call.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/cloud/client"
	flagplane "github.com/hanzoai/cloud/client/flag"
	financeclient "github.com/hanzoai/cloud/finance"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/audit"
	"github.com/hanzoai/cloud/sdk/bus"
	"github.com/hanzoai/cloud/sdk/coord"
	"github.com/hanzoai/cloud/sdk/principal"
	luxlog "github.com/luxfi/log"
)

// switchTTL is how long one read of the switches is used before it is read again.
const switchTTL = 30 * time.Second

// switches is what the two switches say: the orgs on account, and the debt in cents
// past which the SuperAdmins are told (0 tells nobody).
type switches struct {
	orgs  map[string]bool
	alert int64
}

// arrears keeps the switches as last read and the day each org was last alerted on.
// The alert is once a UTC day per org for the whole deployment: told is only what this
// process already knows to be done, and coord.Once is the deployment's word on it.
type arrears struct {
	pods    *coord.Coord // nil is this process's default
	mu      sync.Mutex
	read    func(context.Context) (switches, error)
	spawn   func(func()) // runs a refresh off the caller's time
	held    switches
	at      time.Time         // when the switches were last read, answered or not
	reading bool              // a refresh is in flight
	told    map[string]string // org → the UTC day it was last alerted on, by any replica
	trail   *audit.Recorder
	now     func() time.Time
}

// onAccount is the process's one reader, installed at Use.
var onAccount = &arrears{read: readSwitches, spawn: func(f func()) { go f() }, told: map[string]string{}, now: time.Now}

// on reports whether org is on account. It is finance's reader.
func (a *arrears) on(ctx context.Context, org string) bool {
	org = strings.ToLower(strings.TrimSpace(org))
	return org != "" && a.switches(ctx).orgs[org]
}

// switches answers the switches as last read. The process's first ask reads them,
// and every caller waits for that one answer; after it, an answer older than
// [switchTTL] is read again in the background and the caller is not held.
func (a *arrears) switches(ctx context.Context) switches {
	a.mu.Lock()
	if a.at.IsZero() {
		defer a.mu.Unlock()
		got, err := a.read(ctx)
		a.keep(got, err)
		return a.held
	}
	stale := !a.reading && a.now().Sub(a.at) >= switchTTL
	a.reading = a.reading || stale
	held := a.held
	a.mu.Unlock()
	if stale {
		a.spawn(func() {
			got, err := a.read(context.Background())
			a.mu.Lock()
			defer a.mu.Unlock()
			a.reading = false
			a.keep(got, err)
		})
	}
	return held
}

// keep records one read of the switches, under a.mu. A read that failed keeps the
// last answer, and is tried again a TTL later.
func (a *arrears) keep(got switches, err error) {
	a.at = a.now()
	if err != nil {
		luxlog.Default().Warn("the on account switches did not answer; the last answer stands", "err", err)
		return
	}
	a.held = got
}

// readSwitches reads both switches from the flag plane, as the platform.
func readSwitches(ctx context.Context) (switches, error) {
	ask, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	as := cloud.For(ask, principal.Reserved)
	orgs, err := flagplane.FlagsValue(as, &client.FlagValueIn{Key: client.OnAccount, Kind: "string"})
	if err == nil && orgs == nil {
		err = errors.New("the switch answered nothing")
	}
	if err != nil {
		return switches{}, fmt.Errorf("%s: %w", client.OnAccount, err)
	}
	alert, err := flagplane.FlagsValue(as, &client.FlagValueIn{Key: client.OnAccountAlert, Kind: "int", Default: "0"})
	if err == nil && alert == nil {
		err = errors.New("the switch answered nothing")
	}
	if err != nil {
		return switches{}, fmt.Errorf("%s: %w", client.OnAccountAlert, err)
	}
	return switches{orgs: names(orgs.Value), alert: max(int64(alert.N), 0)}, nil
}

// names is a switch's comma-separated org names, folded.
func names(v string) map[string]bool {
	out := map[string]bool{}
	for _, name := range strings.Split(v, ",") {
		if name = strings.ToLower(strings.TrimSpace(name)); name != "" {
			out[name] = true
		}
	}
	return out
}

// owing tells the SuperAdmins when an org on account owes as much as the alert
// switch or more: a row on the audit trail and a pulse on the live feed, once a UTC
// day per org while it does, whichever replica sees the debit. It runs after each of
// the org's debits (the finance usage hook); the sandbox books owe nobody, and are
// not read.
func (a *arrears) owing(org string, test bool) {
	org = strings.ToLower(strings.TrimSpace(org))
	if test || org == "" {
		return
	}
	ctx := context.Background()
	sw := a.switches(ctx)
	if !sw.orgs[org] || sw.alert <= 0 {
		return
	}
	day := a.now().UTC().Format(time.DateOnly)
	a.mu.Lock()
	told := a.told[org] == day
	a.mu.Unlock()
	if told {
		return
	}
	funder, ok := financeclient.Current().(financeclient.Funder)
	if !ok {
		return
	}
	f, err := funder.Funds(ctx, org, org, false)
	if err != nil {
		luxlog.Default().Warn("an org on account could not be read for its alert", "org", org, "err", err)
		return
	}
	owed := f.Owed.CentsUp()
	if owed < sw.alert {
		return
	}
	// The first replica to claim the org's day tells the SuperAdmins; every other one
	// learns it is done. A claim that cannot be proven (the KV is down) tells nobody and
	// is made again by the next debit, so the day is told late and not twice.
	pods := a.pods
	if pods == nil {
		pods = coord.Default()
	}
	first, err := pods.Once(ctx, "commerce:owing", org+"/"+day, 2*24*time.Hour)
	if err != nil {
		luxlog.Default().Warn("an org on account past its alert could not be claimed for the day", "org", org, "err", err)
		return
	}
	a.mu.Lock()
	a.told[org] = day
	a.mu.Unlock()
	if !first {
		return
	}
	if a.trail != nil {
		if _, err := a.trail.Append(ctx, owingRecord(org, owed, sw.alert)); err != nil {
			luxlog.Default().Warn("an org on account past its alert was not audited", "org", org, "err", err)
		}
	}
	if err := bus.Announce(owingPulse(org, owed, sw.alert, day)); err != nil {
		luxlog.Default().Warn("an org on account past its alert was not put on the pulse", "org", org, "err", err)
	}
}

// owingRecord is an org on account past its alert, as an audit row.
func owingRecord(org string, owed, alert int64) audit.Record {
	detail, _ := json.Marshal(map[string]int64{"owedCents": owed, "alertCents": alert})
	return audit.Record{
		Actor:    audit.Actor{Org: "admin", Sub: "commerce"},
		Action:   "billing.account.owing",
		Resource: audit.Resource{Type: "org_account", ID: org},
		Auth:     audit.AuthContext{Method: "service"},
		Outcome:  audit.Outcome{Result: "success", Status: http.StatusOK, Reason: "an org on account owes past its alert"},
		After:    audit.Redact(detail),
	}
}

// owingPulse is the pulse it reads as. Key folds an org's repeats within one day.
func owingPulse(org string, owed, alert int64, day string) bus.Pulse {
	return bus.Pulse{
		Kind:   "owing",
		Key:    org + "/" + day,
		Org:    org,
		Cents:  owed,
		Title:  org + " owes " + grouped(owed, "usd"),
		Detail: fmt.Sprintf("%s is on account and owes %s, past the %s alert. Record its wire in admin to pay it down.", org, grouped(owed, "usd"), grouped(alert, "usd")),
	}
}
