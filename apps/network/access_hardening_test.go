package network

import (
	"context"
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"

	api "github.com/hanzoai/cloud/client"
	basepeer "github.com/hanzoai/cloud/client/base"
	"github.com/hanzoai/cloud/internal/planetest"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/spec"
	"github.com/zap-proto/zip"
)

// TestAFailingBaseIsAskedAtTheCallersRateAndNoMore: while the brand's Base refuses the
// import that grows it, a caller's requests are limited before the import is tried, so
// one caller causes the ten the limit allows and not one for every request it sends.
func TestAFailingBaseIsAskedAtTheCallersRateAndNoMore(t *testing.T) {
	var imports atomic.Int64
	in := mountOn(t, func(app *zip.App, s *cloud.Service[state]) { routes(app, s) }, func(t *testing.T) {
		peer := zip.New(zip.Config{AppName: basepeer.App})
		peer.Post("/base/import", func(context.Context, *api.BaseImportIn) (*api.BaseGrowth, error) {
			imports.Add(1)
			return nil, zip.Errorf(http.StatusServiceUnavailable, "base: down")
		}, zip.WithOperationID(api.BaseImport))
		planetest.Bind(t, basepeer.App, peer)
	})
	in.key(t)
	for i := range 25 {
		if a := in.verify(t, miss(i), from(1)); a.code != http.StatusServiceUnavailable && a.code != http.StatusTooManyRequests {
			t.Fatalf("request %d: %d %s, want 503 or 429", i+1, a.code, a.body)
		}
	}
	if n := imports.Load(); n < 1 || n > 10 {
		t.Fatalf("25 requests from one caller made %d import attempts, want at most the 10 its rate allows", n)
	}
}

// TestTheDocsPromiseOnlyWhatIsTrue: the verify and waitlist contracts say the limits are
// per network address and shared by clients behind one, and never that one caller's
// misses cannot touch another's.
func TestTheDocsPromiseOnlyWhatIsTrue(t *testing.T) {
	app := mountApp(t, &fakeZT{})
	doc, err := spec.Of(app, spec.Info{Title: "network", Version: "v1"})
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/v1/network/invite/verify", "/v1/network/waitlist"} {
		d := doc.Paths[path]["post"].Description
		for _, never := range []string{"never refuse another", "never refuses another", "another caller's"} {
			if strings.Contains(d, never) {
				t.Errorf("%s still says %q:\n%s", path, never, d)
			}
		}
		for _, want := range []string{"behind one address share", "address is unknown"} {
			if !strings.Contains(d, want) {
				t.Errorf("%s does not say %q:\n%s", path, want, d)
			}
		}
	}
	if d := doc.Paths["/v1/network/invite/verify"]["post"].Description; !strings.Contains(d, "per network address") {
		t.Errorf("verify does not say its limits are per network address:\n%s", d)
	}
}

var (
	codeInDoc = regexp.MustCompile(`HNZ-[0-9A-Za-z]+(?:-[0-9A-Za-z]+)*`)
	idInDoc   = regexp.MustCompile(`"id":"([^"]*)"`)
)

// TestEveryExampleIsAcceptedByItsOwnValidator: each example the ops publish (their request
// bodies, their path parameters and the codes and ids named in their prose) is a value the
// op's own validator takes, so an example cannot drift from the shape it shows.
func TestEveryExampleIsAcceptedByItsOwnValidator(t *testing.T) {
	app := mountApp(t, &fakeZT{})
	doc, err := spec.Of(app, spec.Info{Title: "network", Version: "v1"})
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for path, item := range doc.Paths {
		if !strings.HasPrefix(path, "/v1/network/invite") && !strings.HasPrefix(path, "/v1/network/waitlist") && !strings.HasPrefix(path, "/v1/admin/network/invites") {
			continue
		}
		for method, op := range item {
			raw, _ := json.Marshal(op)
			for _, c := range codeInDoc.FindAllString(string(raw), -1) {
				if got, ok := normalise(c); !ok || got != c {
					t.Errorf("%s %s names the code %q, which is not a code of the shape HNZ-XXXXX-XXXXX", method, path, c)
				}
				checked++
			}
			for _, m := range idInDoc.FindAllStringSubmatch(string(raw), -1) {
				if !idShape.MatchString(m[1]) {
					t.Errorf("%s %s shows the id %q, which is not %s", method, path, m[1], idShape)
				}
				checked++
			}
			for _, p := range op.Parameters {
				if p.In == "path" && p.Name == "id" {
					var id string
					if err := json.Unmarshal(p.Example, &id); err != nil || !idShape.MatchString(id) {
						t.Errorf("%s %s: the path parameter id has the example %s, which is not %s", method, path, p.Example, idShape)
					}
					checked++
				}
			}
			var body struct {
				Content map[string]struct {
					Example json.RawMessage `json:"example"`
				} `json:"content"`
			}
			rb, _ := json.Marshal(op.RequestBody)
			_ = json.Unmarshal(rb, &body)
			ex := body.Content["application/json"].Example
			if len(ex) == 0 {
				continue
			}
			checked++
			switch path {
			case "/v1/network/invite/verify":
				var in InviteCheckIn
				_ = json.Unmarshal(ex, &in)
				if _, ok := normalise(in.Code); !ok {
					t.Errorf("verify's example code %q is not a code", in.Code)
				}
			case "/v1/network/waitlist":
				var in WaitlistIn
				_ = json.Unmarshal(ex, &in)
				if _, _, err := identifier(&in); err != nil || !roles[strings.ToLower(in.Role)] {
					t.Errorf("the waitlist example %s is refused: %v", ex, err)
				}
			case "/v1/admin/network/invites":
				if method == "post" {
					var in InviteIssueIn
					_ = json.Unmarshal(ex, &in)
					if in.Count < 1 || in.Count > maxIssue || in.ExpiresDays < 1 || in.ExpiresDays > maxLife {
						t.Errorf("the issue example %s is out of bounds", ex)
					}
				}
			}
		}
	}
	if checked < 5 {
		t.Fatalf("only %d examples were found to check: the walk reads nothing", checked)
	}
}

// TestEveryRevokeAttemptIsAudited: an id that names no invite (404), one that is not even
// shaped like an id (404) and one already revoked (204) each leave a record of the
// attempt and its outcome, as the revoke that changes something does.
func TestEveryRevokeAttemptIsAudited(t *testing.T) {
	in := mountIntake(t)
	out := in.issue(t, `{"count":1}`)
	id := out.Invites[0].ID
	count := func(result string, status int) int {
		n := 0
		for _, r := range in.trailOf(t, "network.invites.revoke", result) {
			if r.Outcome.Status == status {
				n++
			}
		}
		return n
	}
	for _, none := range []string{"0123456789abcde", "zz"} {
		if a := in.send(t, http.MethodDelete, invitesPath+"/"+none, "", operator, ""); !problem(a, http.StatusNotFound) {
			t.Fatalf("revoke %q: %d %s", none, a.code, a.body)
		}
	}
	if n := count("error", http.StatusNotFound); n != 2 {
		t.Fatalf("two unknown ids left %d audit records of a 404, want 2", n)
	}
	if a := in.send(t, http.MethodDelete, invitesPath+"/"+id, "", operator, ""); a.code != http.StatusNoContent {
		t.Fatalf("revoke: %d %s", a.code, a.body)
	}
	before := count("success", http.StatusNoContent)
	if a := in.send(t, http.MethodDelete, invitesPath+"/"+id, "", operator, ""); a.code != http.StatusNoContent {
		t.Fatalf("revoke again: %d %s", a.code, a.body)
	}
	if after := count("success", http.StatusNoContent); after != before+1 {
		t.Fatalf("the repeat revoke left %d records of 204 against %d, want one more", after, before)
	}
	last := in.trailOf(t, "network.invites.revoke", "success")
	if r := last[len(last)-1]; !strings.Contains(r.Outcome.Reason, "already") || r.Resource.ID != id || r.Actor.Sub != "admin/z" {
		t.Fatalf("the repeat's record is %+v", r)
	}
}
