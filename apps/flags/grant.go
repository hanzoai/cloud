// Copyright © 2026 Hanzo AI. MIT License.

package flags

// grant.go — one person let into a principal switch, or out of it.
//
// A beta is given person by person: a SuperAdmin grants beta.crm to the one who
// asked, from their CRM record. Writing the whole definition for that is a read,
// an edit and a write in the cockpit, and two grants made together would lose
// one. So the edit is made here, under one lock, on the definition as stored:
// the person's IAM id is added to, or taken from, the switch's one "user" rule,
// and every other group (orgs, a rollout) is kept as it is. The result is
// checked and stored as any cockpit write is (targetable), on the audit trail
// first (record), so who had a beta and from when is a question the trail
// answers.
//
// The switch is on while any group is left: a grant turns it on, and taking the
// last person out of a switch with nothing else in it turns it off.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/zap-proto/zip"
)

// userShape is an IAM id as a principal names one: a person's `sub` (a UUID), a
// key holder's `<owner>/<name>`, an application's `app:<owner>/<name>`.
var userShape = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:@/-]{0,127}$`)

// rule is one targeting rule as targetable stores it.
type rule struct {
	Key      string `json:"key"`
	Type     string `json:"type"`
	Operator string `json:"operator"`
	Value    string `json:"value"`
}

// group is one targeting group as targetable stores it.
type group struct {
	Properties []rule  `json:"properties"`
	Rollout    float64 `json:"rollout_percentage"`
}

// targets is a stored principal switch definition.
type targets struct {
	Active  bool `json:"active"`
	Filters struct {
		Groups []group `json:"groups"`
	} `json:"filters"`
}

// exposeGrant publishes the grant on the internal plane. Use calls it.
func exposeGrant() {
	cloud.Plane().Post("/flags/grant",
		grantOp,
		zip.WithOperationID(client.FlagsGrant),
		zip.WithSummary("Let one person into a principal switch, or out of it"))
}

// grantOp lets in.User into the principal switch in.Key, or out of it. Only a
// SuperAdmin's call is answered, whichever process carries it.
func grantOp(ctx context.Context, in *client.FlagGrantIn) (*client.Unit, error) {
	if cloud.Who(ctx).Org == "" {
		return nil, zip.ErrUnauthorized("grant: no org on the call")
	}
	if !principal.SuperFrom(ctx) {
		return nil, zip.ErrForbidden("grant: a principal switch is a SuperAdmin's to grant")
	}
	if in == nil {
		return nil, zip.ErrBadRequest("grant: a flag key is required")
	}
	key, user := strings.TrimSpace(in.Key), strings.TrimSpace(in.User)
	if d, ok := lookupDef(key); !ok || !d.Principal {
		return nil, zip.ErrBadRequest(fmt.Sprintf("grant: %q is not a principal switch", key))
	}
	if !userShape.MatchString(user) {
		return nil, zip.ErrBadRequest("grant: user must be an IAM id")
	}
	if mounted == nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "grant: engine not configured")
	}
	if err := mounted.grant(ctx, key, user, in.On, in.Actor); err != nil {
		return nil, err
	}
	return &client.Unit{}, nil
}

// grant lets user into the principal switch key, or out of it, as the only edit of
// the switch across every replica (Store.Edit): each grant edits the definition the
// last one stored, whichever replica made it, and a grant made together with another
// is not lost. The stored definition is checked and put on the audit trail inside the
// lock, so the trail names what the grant really replaced.
func (c *Client) grant(ctx context.Context, key, user string, on bool, actor string) error {
	st, err := c.platform()
	if err != nil {
		return err
	}
	if err := st.Edit(ctx, key, actor, func(stored json.RawMessage) (json.RawMessage, error) {
		def, err := granted(stored, user, on)
		if err != nil {
			return nil, zip.ErrBadRequest("grant: " + key + ": " + err.Error())
		}
		canon, err := targetable(def)
		if err != nil {
			return nil, zip.ErrBadRequest("grant: " + key + ": " + err.Error())
		}
		if err := c.record(ctx, key, stored, canon); err != nil {
			return nil, zip.Errorf(http.StatusServiceUnavailable, "grant: this change could not be audited")
		}
		return canon, nil
	}); err != nil {
		return err
	}
	c.invalidate()
	return nil
}

// granted is the stored definition with user let in (on) or taken out: the one
// groups whose single rule names people are rewritten as one, created on the
// first grant and dropped with its last person, and every other group is kept. The switch is
// active while any group is left. Nothing stored reads as no groups.
func granted(stored json.RawMessage, user string, on bool) (json.RawMessage, error) {
	var t targets
	if len(stored) > 0 {
		if err := json.Unmarshal(stored, &t); err != nil {
			return nil, fmt.Errorf("the stored definition does not read: %w", err)
		}
	}
	var people []string
	kept := []group{}
	for _, g := range t.Filters.Groups {
		if len(g.Properties) == 1 && g.Properties[0].Key == "user" && g.Rollout == 100 {
			names, ok := literals(g.Properties[0].Value)
			if !ok {
				return nil, fmt.Errorf("the stored user rule is not ^(?:a|b)$")
			}
			people = append(people, names...)
			continue
		}
		kept = append(kept, g)
	}
	people = slices.DeleteFunc(people, func(n string) bool { return n == user })
	if on {
		people = append(people, user)
	}
	if len(people) > 0 {
		quoted := make([]string, len(people))
		for i, n := range people {
			quoted[i] = regexp.QuoteMeta(n)
		}
		kept = append(kept, group{
			Properties: []rule{{Key: "user", Type: "person", Operator: "regex", Value: "^(?:" + strings.Join(quoted, "|") + ")$"}},
			Rollout:    100,
		})
	}
	t.Filters.Groups = kept
	t.Active = len(kept) > 0
	return json.Marshal(t)
}

// named is who a stored principal switch's rules name: the people and the orgs.
// A rule that does not read as names names nobody.
func named(stored json.RawMessage) (users, orgs []string) {
	var t targets
	if json.Unmarshal(stored, &t) != nil || !t.Active {
		return nil, nil
	}
	for _, g := range t.Filters.Groups {
		for _, p := range g.Properties {
			names, ok := literals(p.Value)
			if !ok {
				continue
			}
			switch p.Key {
			case "user":
				users = append(users, names...)
			case "org":
				orgs = append(orgs, names...)
			}
		}
	}
	return users, orgs
}
