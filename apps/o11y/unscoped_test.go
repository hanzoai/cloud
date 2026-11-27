package o11y

import (
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/internal/sqltest"
)

// Logs and traces reads bind no org in the runtime, so gate admits only a
// SuperAdmin to them. These tests hold that rule against the route table the
// module actually declares.

var pathParam = regexp.MustCompile(`\{[^}]+\}|:[A-Za-z_]+`)

// concrete fills every path parameter of an address template.
func concrete(template string) string { return pathParam.ReplaceAllString(template, "p1") }

// caller builds the request SanitizeIdentity mints: a validated user, the org
// pinned from the principal's owner, and the admin bit only for a SuperAdmin.
func caller(method, target, org string, admin bool) *http.Request {
	r := httptest.NewRequest(method, "http://api.hanzo.ai"+target, nil)
	r.Header.Set("X-User-Id", "u1")
	if org != "" {
		r.Header.Set("X-Org-Id", org)
	}
	if admin {
		r.Header.Set("X-User-IsAdmin", "true")
	}
	return r
}

// through serves r through gate and reports the status and whether the runtime
// behind it was reached.
func through(r *http.Request) (int, string, bool) {
	reached := false
	rec := httptest.NewRecorder()
	gate(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		_, _ = io.WriteString(w, `{"status":"ok"}`)
	})).ServeHTTP(rec, r)
	return rec.Code, rec.Body.String(), reached
}

func TestUnscopedReadsAreSuperAdminOnly(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		served, _ := o11yOps(t)
		matched := map[string]bool{}
		n := 0
		for key := range served {
			method, template, _ := strings.Cut(key, " ")
			if !unscopedRead(template) {
				continue
			}
			for _, f := range unscopedReads {
				if covers(f, template) {
					matched[f] = true
				}
			}
			n++
			target := concrete(template)

			code, body, reached := through(caller(method, target, "acme", false))
			if code != http.StatusForbidden || reached || !strings.Contains(body, "limited to platform administrators") {
				t.Errorf("%s %s as an org member = %d reached=%v %s, want 403 before the runtime", method, target, code, reached, body)
			}
			for _, org := range []string{"hanzo", ""} {
				if code, _, reached := through(caller(method, target, org, true)); code != http.StatusOK || !reached {
					t.Errorf("%s %s as SuperAdmin (org %q) = %d reached=%v, want 200", method, target, org, code, reached)
				}
			}
		}
		if n == 0 {
			t.Fatal("no declared route is an unscoped read; the list names nothing the module serves")
		}
		for _, f := range unscopedReads {
			if !matched[f] {
				t.Errorf("%s matches no declared route", f)
			}
		}
		t.Logf("%d declared logs and traces reads refuse an org member", n)

	})
}

func TestUnscopedReadSpellingsAreRefused(t *testing.T) {
	for _, target := range []string{
		"/v1/o11y/query_range/",
		"/V1/O11Y/QUERY_RANGE",
		"/v1/o11y//logs",
		"/v1/o11y/./logs/aggregate",
		"/v1/o11y/Traces/abc/waterfall",
	} {
		if code, _, reached := through(caller(http.MethodPost, target, "acme", false)); code != http.StatusForbidden || reached {
			t.Errorf("POST %s as an org member = %d reached=%v, want 403", target, code, reached)
		}
	}
}

func TestTenantReadsStillAdmitAnOrgMember(t *testing.T) {
	for _, target := range []string{
		"/v1/o11y/sentinel/issues",
		"/v1/o11y/sentinel/logs",
		"/v1/o11y/sentinel/traces",
		"/v1/o11y/sentinel/traces/abc",
		"/v1/o11y/llm/traces",
		"/v1/o11y/llm/sessions",
		"/v1/o11y/llm/observations",
		"/v1/o11y/traces",
		"/v1/o11y/dashboards",
		"/v1/o11y/metrics",
	} {
		if code, _, reached := through(caller(http.MethodGet, target, "acme", false)); code != http.StatusOK || !reached {
			t.Errorf("GET %s as an org member = %d reached=%v, want 200", target, code, reached)
		}
	}
}
