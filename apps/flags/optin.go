// Copyright © 2026 Hanzo AI. MIT License.

package flags

// optin.go — the opt-in to public betas.
//
//	GET /v1/flag/optin       the caller's own opt-in, whether their org blocked it, and the public betas
//	PUT /v1/flag/optin       set the caller's OWN opt-in (self only)
//	PUT /v1/flag/optin/org   block or unblock the opt-in for the org the caller acts in (org admin)
//
// "Automatically opt into new experimental features, unless disabled by your
// team or organization": a person who opts in has every PUBLIC beta, the ones
// there are and the ones promoted later, unless the org they act in blocked it
// (access.go reads it as the "optin" property). Nothing here touches a private
// beta, and nothing here grants: a block only withholds, and the person's own
// row is keyed by their validated subject, whatever the request says.
//
// Both rows live in the reserved platform store beside the switches they open,
// so a tenant cannot name them. A shared org (a brand's sign-up org) names no
// customer: its members are strangers to each other and no admin speaks for them,
// so it has no opt-in to block.

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/audit"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/zap-proto/zip"
)

// optinRule is the one value the opt-in group's rule may hold: the "optin"
// property is "true" for a person who opted in and whose org did not block it.
const optinRule = "^(?:true)$"

// staffed reports whether key is a beta a SuperAdmin has by being staff: the
// `beta` switch and each beta.<id>.
func staffed(key string) bool { return key == Beta || strings.HasPrefix(key, Beta+".") }

// opted reports whether user's public betas are on in org: they opted in, and org
// is a shared one or did not block it. A row that cannot be read is no opt-in.
func opted(st *Store, user, org string) bool {
	if user == "" {
		return false
	}
	on, err := st.optedIn(user)
	if err != nil || !on {
		return false
	}
	if shared(org) {
		return true
	}
	blocked, err := st.blockedIn(org)
	return err == nil && !blocked
}

// public reports whether a stored principal switch is a public beta: active, with
// the opt-in group taking anyone.
func public(stored json.RawMessage) bool {
	var t targets
	if json.Unmarshal(stored, &t) != nil || !t.Active {
		return false
	}
	for _, g := range t.Filters.Groups {
		if g.Rollout > 0 && len(g.Properties) == 1 && g.Properties[0].Key == "optin" && g.Properties[0].Value == optinRule {
			return true
		}
	}
	return false
}

// publicBetas is the id of every beta.<id> that is public, in the Betas list's order.
func publicBetas(st *Store) ([]string, error) {
	out := []string{}
	for _, d := range Defs() {
		id, ok := strings.CutPrefix(d.Key, Beta+".")
		if !ok || !d.Principal {
			continue
		}
		row, found, err := st.Get(d.Key)
		if err != nil {
			return nil, err
		}
		if found && public(row.Definition) {
			out = append(out, id)
		}
	}
	return out, nil
}

// ── store ───────────────────────────────────────────────────────────────────

func (s *Store) optedIn(subject string) (bool, error) {
	var v int
	err := s.db.QueryRow(`SELECT opted FROM flag_optin WHERE subject = $1`, subject).Scan(&v)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return v == 1, err
}

func (s *Store) setOpted(subject string, on bool) error {
	_, err := s.db.Exec(`INSERT INTO flag_optin (subject, opted, at) VALUES ($1, $2, $3)
ON CONFLICT(subject) DO UPDATE SET opted = excluded.opted, at = excluded.at`,
		subject, bit(on), time.Now().UTC().Format(time.RFC3339))
	return err
}

func (s *Store) blockedIn(org string) (bool, error) {
	var v int
	err := s.db.QueryRow(`SELECT blocked FROM flag_optin_org WHERE org = $1`, org).Scan(&v)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return v == 1, err
}

func (s *Store) setBlocked(org string, blocked bool, by string) error {
	_, err := s.db.Exec(`INSERT INTO flag_optin_org (org, blocked, at, by) VALUES ($1, $2, $3, $4)
ON CONFLICT(org) DO UPDATE SET blocked = excluded.blocked, at = excluded.at, by = excluded.by`,
		org, bit(blocked), time.Now().UTC().Format(time.RFC3339), by)
	return err
}

func bit(b bool) int {
	if b {
		return 1
	}
	return 0
}

// ── wire ────────────────────────────────────────────────────────────────────

// optinOut is the caller's opt-in to public betas as it stands.
type optinOut struct {
	// On is true when the caller opted in: every public beta, and each one
	// promoted later, is on for them unless Blocked.
	On bool `json:"on"`
	// Blocked is true when the org the caller acts in turned the opt-in off for
	// its members. Their own choice is kept and applies again once unblocked.
	Blocked bool `json:"blocked"`
	// Public is the id of every beta anyone may opt in to (beta.<id>). The rest are
	// private: a SuperAdmin grants them, person by person.
	Public []string `json:"public"`
}

// optinIn sets the caller's own opt-in.
type optinIn struct {
	// On opts the caller in to every public beta, current and future, when true,
	// and out when false.
	On bool `json:"on"`
}

// orgOptinIn blocks or unblocks the opt-in for the org the caller acts in.
type orgOptinIn struct {
	// Blocked turns the opt-in off for every member of the org when true, and back
	// on when false. It never turns a beta on for anyone.
	Blocked bool `json:"blocked"`
}

// orgOptinOut is the org's block as it stands.
type orgOptinOut struct {
	// Blocked is true when the org's members do not get public betas by opting in.
	Blocked bool `json:"blocked"`
}

// someone is the person an opt-in belongs to and the org they act in, from the
// validated principal alone. A call with no person behind it holds no opt-in.
func someone(ctx context.Context) (user, org string, err error) {
	if !principal.ValidatedFrom(ctx) {
		return "", "", principal.Unauthenticated()
	}
	if org, err = principal.Acting(ctx); err != nil {
		return "", "", err
	}
	user, _ = principal.SubjectFrom(ctx)
	if user = strings.TrimSpace(user); user == "" {
		return "", "", zip.ErrForbidden("an opt-in is a person's, and this call has no person behind it")
	}
	return user, org, nil
}

// standing reads the caller's opt-in, their org's block and the public betas.
func (o ops) standing(user, org string) (*optinOut, error) {
	st, err := o.s.State.client.platform()
	if err != nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "flags: the opt-in could not be read")
	}
	on, err := st.optedIn(user)
	if err != nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "flags: the opt-in could not be read")
	}
	blocked := false
	if !shared(org) {
		if blocked, err = st.blockedIn(org); err != nil {
			return nil, zip.Errorf(http.StatusServiceUnavailable, "flags: the opt-in could not be read")
		}
	}
	betas, err := publicBetas(st)
	if err != nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "flags: the public betas could not be read")
	}
	return &optinOut{On: on, Blocked: blocked, Public: betas}, nil
}

// GetOptin answers the signed-in caller's opt-in to public betas: whether they
// opted in, whether the org they act in blocked it, and which betas are public. A
// beta not listed is private, granted person by person.
//
// Response: {"on": true, "blocked": false, "public": ["koma", "iro"]}
func (o ops) getOptin(ctx context.Context, _ *cloud.Unit) (*optinOut, error) {
	user, org, err := someone(ctx)
	if err != nil {
		return nil, err
	}
	return o.standing(user, org)
}

// PutOptin sets the signed-in caller's OWN opt-in to public betas: on, every
// public beta and each one promoted later is on for them; off, none is. Self only:
// the row written is the caller's validated subject, whatever the request says.
// Opting in while the org they act in blocked it is refused; opting out never is.
//
// Example: {"on": true}
// Response: {"on": true, "blocked": false, "public": ["koma", "iro"]}
func (o ops) putOptin(ctx context.Context, in *optinIn) (*optinOut, error) {
	user, org, err := someone(ctx)
	if err != nil {
		return nil, err
	}
	st, err := o.s.State.client.platform()
	if err != nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "flags: the opt-in could not be saved")
	}
	if in.On && !shared(org) {
		blocked, err := st.blockedIn(org)
		if err != nil {
			return nil, zip.Errorf(http.StatusServiceUnavailable, "flags: the opt-in could not be saved")
		}
		if blocked {
			return nil, zip.ErrForbidden("experimental features are disabled by your organization")
		}
	}
	if err := st.setOpted(user, in.On); err != nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "flags: the opt-in could not be saved")
	}
	return o.standing(user, org)
}

// PutOrgOptin blocks or unblocks the opt-in to public betas for every member of
// the org the caller acts in. An owner or admin of that org, or a SuperAdmin, may;
// a member may not. It only withholds: blocked, no member gets a public beta by
// opting in, and their own choices are kept for when it is lifted; it never turns
// a beta on. A shared sign-up org has no admin to speak for its members and is
// refused. The change is on the audit trail before it lands.
//
// Example: {"blocked": true}
// Response: {"blocked": true}
func (o ops) putOrgOptin(ctx context.Context, in *orgOptinIn) (*orgOptinOut, error) {
	if !principal.ValidatedFrom(ctx) {
		return nil, principal.Unauthenticated()
	}
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	if !cloud.Admin.Admits(cloud.AuthorityIn(ctx)) {
		return nil, zip.ErrForbidden("whether " + org + "'s members may opt in to experimental features is an owner's or admin's of the org to set")
	}
	if shared(org) {
		return nil, zip.ErrBadRequest(org + " is a shared sign-up org: it names no customer, so it has no opt-in to block")
	}
	st, err := o.s.State.client.platform()
	if err != nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "flags: the org's opt-in could not be saved")
	}
	before, err := st.blockedIn(org)
	if err != nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "flags: the org's opt-in could not be read")
	}
	who := cloud.Who(ctx)
	if o.s.State.audit != nil {
		was, _ := json.Marshal(orgOptinOut{Blocked: before})
		now, _ := json.Marshal(orgOptinOut{Blocked: in.Blocked})
		if _, err := o.s.State.audit.Append(ctx, audit.Record{
			Actor:     audit.Actor{Org: org, Sub: who.User, Email: who.Email},
			Action:    "flag.optin.org",
			Resource:  audit.Resource{Type: "org", ID: org},
			Auth:      audit.AuthContext{Method: "attested", IsAdmin: principal.SuperFrom(ctx)},
			Outcome:   audit.Outcome{Result: "success", Status: http.StatusOK, Reason: "whether the org's members get public betas by opting in"},
			SourceIP:  who.IP,
			RequestID: who.RequestID,
			Before:    was,
			After:     now,
		}); err != nil {
			o.s.Log.Error("flags: an org's opt-in change could not be audited", "org", org, "err", err)
			return nil, zip.Errorf(http.StatusServiceUnavailable, "flags: this change could not be audited")
		}
	}
	if err := st.setBlocked(org, in.Blocked, cmp.Or(who.Email, who.User)); err != nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "flags: the org's opt-in could not be saved")
	}
	return &orgOptinOut{Blocked: in.Blocked}, nil
}
