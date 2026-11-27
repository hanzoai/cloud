package authztest

import (
	"fmt"
	"strings"
	"testing"
)

// rec records what check reports, so the contract's own rules are tested on
// tables instead of on an app.
type rec struct{ errs []string }

func (r *rec) Helper() {}
func (r *rec) Errorf(format string, args ...any) {
	r.errs = append(r.errs, fmt.Sprintf(format, args...))
}

// answers is one route's answers: a held gate by default, overridden per case.
func answers(over map[string]string) map[string]string {
	got := map[string]string{
		anon: "401", forged: "401",
		member: "403", memberForged: "403",
		orgadmin: "403", orgadminForged: "403",
		cross: "403", super: "200",
	}
	for k, v := range over {
		got[k] = v
	}
	return got
}

func TestCheck(t *testing.T) {
	for _, tc := range []struct {
		name  string
		floor Floor
		over  map[string]string
		want  string // a substring of the one report, or "" for none
	}{
		{"a held SuperAdmin gate", Super, nil, ""},
		{"forged headers let anonymous in", Signed, map[string]string{forged: "200"}, "let an anonymous caller in"},
		{"forged headers elevate a member", Signed, map[string]string{memberForged: "200"}, "elevated a member"},
		{"forged headers elevate an org admin", Signed, map[string]string{orgadminForged: "204"}, "elevated an org admin"},
		{"another org's name lets a member in", Signed, map[string]string{cross: "200"}, "naming another org"},
		{"a refusal of another kind is not an escalation", Signed, map[string]string{cross: "409"}, ""},
		{"a signed route admits anonymous", Signed, map[string]string{anon: "200", forged: "200"}, "anonymous caller got in"},
		{"a validator answer is not a way in", Signed, map[string]string{anon: "400", forged: "400"}, ""},
		{"an in-band refusal is not a way in", Signed, map[string]string{anon: "200-error", forged: "200-error"}, ""},
		{"an org admin route admits a member", OrgAdmin, map[string]string{member: "200", memberForged: "200", cross: "200", orgadmin: "200", orgadminForged: "200"}, "plain member got in"},
		{"a SuperAdmin route admits an org admin", Super, map[string]string{orgadmin: "200", orgadminForged: "200"}, "org admin got in"},
		{"a SuperAdmin route refuses the SuperAdmin", Super, map[string]string{super: "403"}, "SuperAdmin was refused"},
		{"an open route admits anyone", Open, map[string]string{anon: "200", forged: "200", member: "200", memberForged: "200", cross: "200"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var r rec
			check(&r, tc.floor, answers(tc.over))
			switch {
			case tc.want == "" && len(r.errs) > 0:
				t.Errorf("reported %q, want nothing", r.errs)
			case tc.want != "" && (len(r.errs) != 1 || !strings.Contains(r.errs[0], tc.want)):
				t.Errorf("reported %q, want one report containing %q", r.errs, tc.want)
			}
		})
	}
}

func TestFloorDerivation(t *testing.T) {
	c := Contract{Floors: map[string]Floor{"/v1/x/open": Open, "/v1/x": OrgAdmin}}
	for _, tc := range []struct {
		pattern string
		want    Floor
	}{
		{"/v1/admin/orgs", Super},
		{"/v1/admin", Super},
		{"/v1/administer", Signed}, // a segment boundary, not a string prefix
		{"/v1/x/open/:id", Open},   // the longest prefix wins
		{"/v1/x/y", OrgAdmin},
		{"/v1/other", Signed}, // the default is signed: a new route is never open by accident
	} {
		if got := c.floor("GET", tc.pattern); got != tc.want {
			t.Errorf("floor(%s) = %s, want %s", tc.pattern, got, tc.want)
		}
	}
}
