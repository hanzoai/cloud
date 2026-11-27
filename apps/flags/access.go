// Copyright © 2026 Hanzo AI. MIT License.

package flags

// access.go — a PLATFORM switch decided per caller.
//
// Most platform switches are facts about the deployment: the waitlist is open or
// it is not, for everyone. A PRINCIPAL switch is a fact about who is asking. Its
// definition lives in the reserved platform store beside every other switch and
// is written the one way they all are — a SuperAdmin, through the admin cockpit
// (SetPlatformSwitch) — and it is evaluated for each caller against that caller's
// validated org and user: a rule on "org" or "user", or a rollout.
//
// `beta` is the ONE predicate for beta access: the pricing catalog shows a beta
// item to a caller it is on for, and the console lists beta products for them.
// Beside it, each app the Hanzo app does not offer yet has one of its own,
// beta.<id> (betas.go), granted person by person (grant.go). Nothing else
// decides them.
//
// THE CALLER HAS NO SAY OVER A PRIVATE BETA. The org is the gateway-validated
// principal's (principal.Acting), never a raw X-Org-Id and never a field; the
// user is the validated subject. A customer or an admin of an org cannot write
// the platform store — no tenant can name it (orgns.Reserved), and its one write
// path refuses everyone but a SuperAdmin (board_rpc.go) — so no one turns a
// private beta on for themselves.
//
// A beta has two stages, and the stored definition says which:
//
//	private  every beta until a SuperAdmin says otherwise: on for the people and
//	         orgs its rules name (grant.go), and for no one else
//	public   its definition also holds the opt-in group, one rule on the property
//	         "optin" matching ^(?:true)$ (optinRule); it is on as well for every
//	         person who opted in to public betas (optin.go), unless the org they
//	         act in blocked that
//
// Promotion is a write of that group through the ONE definition write path, set
// or grant, checked by targetable and on the audit trail like every other rule:
// a second route to the same row would be a second answer to who may promote.
// "optin" is a property this package computes from its own store, never one a
// request states, exactly like "org" and "user". Opting in grants nothing
// private: a private beta's definition has no group the property can match.
//
// A SuperAdmin (principal.SuperFrom, owner == "admin") is staff, and has every
// beta, the `beta` switch and each beta.<id>, whatever is stored.
//
// NO FALLBACK, AND IT FAILS CLOSED. Nothing stored, an inactive definition, or one
// the engine cannot compute is off. A store that cannot be read is an error, and
// every reader takes an error as off.
//
// ONE SHAPE IS STORED. A principal switch's definition is the cockpit's and
// nothing wider (targetable): rules naming orgs or people exactly, and a rollout.
// The engine reads more — an active definition with no groups is on for
// everyone, `exact` folds case, a variant is on — and none of that is a way in.
//
// A ROLLOUT IS SAMPLING, NOT ACCESS CONTROL. It hashes the org's name, which a
// customer chooses, so a customer can choose its way into a small rollout. Who
// must have beta is named by a rule; a rollout only widens a beta for the many.
//
// A tenant's own store may hold a flag named `beta` for its own product, and
// nothing here reads it: the platform's answer comes from the reserved store
// alone, through [On].

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/hanzoai/authz"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/audit"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/zap-proto/zip"
)

// Beta is the key of the beta switch: whether a caller sees what is not yet
// generally available. Spelled once, in the contract every reader shares.
const Beta = client.Beta

// On evaluates the principal switch key for the caller ctx carries.
//
// The rollout buckets the subject [subjectOf] names; the conditions read the
// properties "org" and "user". An unknown key, or one that is not a principal
// switch, is an error rather than an off: asking the wrong question must not
// read as an answer.
func On(ctx context.Context, key string) (bool, error) {
	d, ok := lookupDef(key)
	if !ok || !d.Principal {
		return false, zip.ErrBadRequest(fmt.Sprintf("flags: %q is not a principal switch", key))
	}
	// A validated caller, however the call arrived: an org parked on the context
	// with nobody signed in behind it — an anonymous read naming an org — is no
	// principal, co-resident or across a socket alike.
	if !principal.ValidatedFrom(ctx) {
		return false, principal.Unauthenticated()
	}
	org, err := principal.Acting(ctx)
	if err != nil {
		return false, err
	}
	if principal.SuperFrom(ctx) && staffed(key) {
		return true, nil
	}
	user, _ := principal.SubjectFrom(ctx)
	subject := subjectOf(org, user)
	if subject == "" {
		return false, nil
	}
	if mounted == nil {
		return false, fmt.Errorf("flags: engine not configured")
	}
	st, err := mounted.platform()
	if err != nil {
		return false, err
	}
	// A shared org names no customer, so no rule may name it: its members are
	// asked about as the people they are.
	props := map[string]string{"user": user}
	if !shared(org) {
		props["org"] = org
	}
	if opted(st, user, org) {
		props["optin"] = "true"
	}
	bag, err := json.Marshal(props)
	if err != nil {
		return false, err
	}
	a, err := assign(st, key, subject, bag)
	if err != nil {
		return false, err
	}
	return a.On, nil
}

// subjectOf is who a principal switch's rollout buckets: the org, because the
// org is the customer — every member of it answers alike. A SHARED org is the
// exception: a brand's sign-up org or a platform-run one (authz.IsHeldOrg, the
// list org creation reads) holds people who are strangers to each other, so
// bucketing it would put everyone who ever signed up on one side of the rollout.
// There the subject is the person, and a caller with no person behind it has none.
func subjectOf(org, user string) string {
	if shared(org) {
		return strings.TrimSpace(user)
	}
	return org
}

// shared reports whether org is one no customer owns: a brand's sign-up org or a
// platform-run one, other than the admin org.
func shared(org string) bool { return org != authz.AdminOrg && authz.IsHeldOrg(org) }

// targetable checks a definition of a principal switch against the cockpit's
// shape and answers it re-encoded, which is what is stored: `active`, stated, and
// groups of at most one rule each — on "org" or "user", a "person" property,
// compared by "regex" against an anchored alternation of escaped literals,
// ^(?:a|b)$, matched case-sensitively, or the opt-in rule that makes a beta
// public, "optin" against optinRule and nothing else — with an explicit
// rollout_percentage from 0 to 100. Anything else the engine reads is refused by
// name: variants,
// payloads, group aggregation, cohorts, other operators, an implicit rollout. An
// absent `active` is refused because the engine reads it as true, and an active
// definition with no groups because it is on for everyone: "everyone" is written
// as a rollout of 100 or not at all. A rule may not name a shared org.
//
// Storing the re-encoding rather than the bytes sent means the engine evaluates
// exactly what was checked: a key repeated in another case, which this decode and
// the engine's would read differently, cannot reach the store.
func targetable(definition json.RawMessage) (json.RawMessage, error) {
	type rule struct {
		Key      string `json:"key"`
		Type     string `json:"type"`
		Operator string `json:"operator"`
		Value    string `json:"value"`
	}
	var in struct {
		Key     string `json:"key"`
		Active  *bool  `json:"active"`
		Filters struct {
			Groups []struct {
				Properties []rule   `json:"properties"`
				Rollout    *float64 `json:"rollout_percentage"`
			} `json:"groups"`
		} `json:"filters"`
	}
	dec := json.NewDecoder(bytes.NewReader(definition))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		return nil, fmt.Errorf("not a targeting definition: %w", err)
	}
	if in.Active == nil {
		return nil, fmt.Errorf("active must be stated: the engine reads an absent one as true")
	}
	if *in.Active && len(in.Filters.Groups) == 0 {
		return nil, fmt.Errorf("an active definition with no groups is on for everyone; write a rollout of 100 to mean that")
	}
	type group struct {
		Properties []rule  `json:"properties"`
		Rollout    float64 `json:"rollout_percentage"`
	}
	type filters struct {
		Groups []group `json:"groups"`
	}
	out := struct {
		Active  bool    `json:"active"`
		Filters filters `json:"filters"`
	}{Active: *in.Active, Filters: filters{Groups: []group{}}}
	for i, g := range in.Filters.Groups {
		if g.Rollout == nil || *g.Rollout < 0 || *g.Rollout > 100 {
			return nil, fmt.Errorf("group %d: rollout_percentage must be stated, from 0 to 100", i)
		}
		if len(g.Properties) > 1 {
			return nil, fmt.Errorf("group %d: one rule a group", i)
		}
		for _, p := range g.Properties {
			if p.Key != "org" && p.Key != "user" && p.Key != "optin" {
				return nil, fmt.Errorf("group %d: a rule reads \"org\", \"user\" or \"optin\", not %q", i, p.Key)
			}
			if p.Type != "person" || p.Operator != "regex" {
				return nil, fmt.Errorf("group %d: a rule is a person property compared by regex", i)
			}
			if p.Key == "optin" && p.Value != optinRule {
				return nil, fmt.Errorf("group %d: the opt-in rule matches %s and nothing else", i, optinRule)
			}
			names, ok := literals(p.Value)
			if !ok {
				return nil, fmt.Errorf("group %d: %q is not ^(?:a|b)$ over escaped names", i, p.Value)
			}
			for _, n := range names {
				if p.Key == "org" && shared(n) {
					return nil, fmt.Errorf("group %d: %s is a shared org, whose members are strangers to each other; name them", i, n)
				}
			}
		}
		props := append([]rule{}, g.Properties...)
		out.Filters.Groups = append(out.Filters.Groups, group{Properties: props, Rollout: *g.Rollout})
	}
	return json.Marshal(out)
}

// literals reads ^(?:a|b)$ back into the names it matches exactly, and is false
// for anything else — an unanchored pattern, an empty name, a metacharacter left
// unescaped. It reads its own writing: the names, escaped again, must spell the
// pattern byte for byte.
func literals(pattern string) ([]string, bool) {
	inner, ok := strings.CutPrefix(pattern, "^(?:")
	if !ok {
		return nil, false
	}
	if inner, ok = strings.CutSuffix(inner, ")$"); !ok {
		return nil, false
	}
	var names []string
	var b strings.Builder
	escaped := false
	for _, r := range inner {
		switch {
		case escaped:
			b.WriteRune(r)
			escaped = false
		case r == '\\':
			escaped = true
		case r == '|':
			names = append(names, b.String())
			b.Reset()
		default:
			b.WriteRune(r)
		}
	}
	if escaped {
		return nil, false
	}
	names = append(names, b.String())
	quoted := make([]string, len(names))
	for i, n := range names {
		if n == "" {
			return nil, false
		}
		quoted[i] = regexp.QuoteMeta(n)
	}
	return names, "^(?:"+strings.Join(quoted, "|")+")$" == pattern
}

// record puts a principal switch's write on the platform's audit trail before it
// lands, with what it replaces (before; nil when nothing is stored): who had beta is
// a question someone will ask of a date. No trail to take it, no write.
func (c *Client) record(ctx context.Context, key string, before, definition json.RawMessage) error {
	if c.trail == nil {
		return nil
	}
	who := cloud.Who(ctx)
	_, err := c.trail.Append(ctx, audit.Record{
		Actor:     audit.Actor{Org: who.Org, Sub: who.User, Email: who.Email},
		Action:    "flag.platform.put",
		Resource:  audit.Resource{Type: "flag", ID: key},
		Auth:      audit.AuthContext{Method: "attested", IsAdmin: true},
		Outcome:   audit.Outcome{Result: "success", Status: http.StatusOK, Reason: "targets a platform switch decided per caller"},
		SourceIP:  who.IP,
		RequestID: who.RequestID,
		Before:    audit.Redact(before),
		After:     audit.Redact(definition),
	})
	return err
}

// stored is the definition a platform switch holds, nil when it holds none or the
// store cannot be read.
func (c *Client) stored(key string) json.RawMessage {
	st, err := c.platform()
	if err != nil {
		return nil
	}
	row, found, err := st.Get(key)
	if err != nil || !found {
		return nil
	}
	return row.Definition
}

// targeting is a principal switch as the board shows it: the stored definition,
// "true" when one is stored and active, and "flags" when one is stored at all.
// Nothing stored, or a store that cannot be read, shows off with no definition.
func (c *Client) targeting(key string) (definition json.RawMessage, value, source string) {
	st, err := c.platform()
	if err != nil {
		return nil, "false", "default"
	}
	row, found, err := st.Get(key)
	if err != nil || !found {
		return nil, "false", "default"
	}
	var head struct {
		Active bool `json:"active"`
	}
	_ = json.Unmarshal(row.Definition, &head)
	if head.Active {
		return row.Definition, "true", "flags"
	}
	return row.Definition, "false", "flags"
}

// exposeOn publishes [On] on the internal plane. Use calls it.
func exposeOn() {
	cloud.Plane().Post("/flags/on",
		answer,
		zip.WithOperationID(client.FlagsOn),
		zip.WithSummary("Whether a principal switch is on for the caller"))
}

// answer is [On] for a caller in another process — the pricing catalog. The
// principal is the one the plane carried, never a field.
func answer(ctx context.Context, in *client.FlagIn) (*client.Flag, error) {
	on, err := On(ctx, strings.TrimSpace(in.Key))
	if err != nil {
		return nil, err
	}
	return &client.Flag{On: on}, nil
}

// platformOut is every principal switch as it applies to the caller.
type platformOut struct {
	// Flags maps each principal switch's key to whether it is on for the caller:
	// `beta` is whether the caller sees products and catalog items that are not
	// yet generally available, and beta.<id> whether they see that one app in
	// beta in the Hanzo app.
	Flags map[string]bool `json:"flags"`
}

// Platform answers the platform switches that are decided per caller — `beta`,
// whether the caller sees what is not yet generally available — as they apply to
// the signed-in caller: its validated org and user, never anything the request
// states. A SuperAdmin targets them from the admin cockpit; nothing here changes
// them. A request without a signed-in principal is refused, and a switch that
// cannot be read is 503, which a caller takes as off.
//
// Response: {"flags": {"beta": false, "beta.crm": true, "beta.erp": false}}
func (o ops) platform(ctx context.Context, _ *cloud.Unit) (*platformOut, error) {
	out := &platformOut{Flags: map[string]bool{}}
	for _, d := range Defs() {
		if !d.Principal {
			continue
		}
		on, err := On(ctx, d.Key)
		if he, ok := errors.AsType[*zip.HTTPError](err); ok {
			return nil, he
		}
		if err != nil {
			o.s.Log.Warn("flags: a principal switch could not be read", "key", d.Key, "err", err)
			return nil, zip.Errorf(http.StatusServiceUnavailable, "flags: %s could not be read", d.Key)
		}
		out.Flags[d.Key] = on
	}
	return out, nil
}
