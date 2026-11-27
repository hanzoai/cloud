package team

// The operator's directory reads every tenant's spaces, which is exactly what no
// other read in this store may do — so the gate is asserted first, and then the
// three things a console relies on: pages that walk the whole set once, filters
// that narrow it, and a roster whose roles are IAM's.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"
	"time"

	fiber "github.com/zap-proto/fiber/v3"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/internal/planetest"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk"
)

var superHdr = map[string]string{"X-User-IsAdmin": "true"}

func mountAdmin(t *testing.T) (*accountStore, func(path string, hdr map[string]string) (int, []byte)) {
	t.Helper()
	s := newAccountStore(t)
	app := zip.New(zip.Config{})
	app.Use(cloud.Bridge())
	registerAdmin(app, s)
	fa := app.Fiber()
	return s, func(path string, hdr map[string]string) (int, []byte) {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		resp, err := fa.Test(req, fiber.TestConfig{Timeout: 30 * time.Second})
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, b
	}
}

// TestAdminTeamsIsSuperAdminOnly: an org admin, a member and an anonymous caller
// are all refused, on both addresses, before any row is read.
func TestAdminTeamsIsSuperAdminOnly(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		s, get := mountAdmin(t)
		w, err := s.EnsureSpace(context.Background(), "acme", accountID("ada@acme.test"), "Acme")
		if err != nil {
			t.Fatal(err)
		}
		callers := map[string]map[string]string{
			"anon":     nil,
			"member":   {"X-Org-Id": "acme", "X-User-Id": "u_ada"},
			"orgadmin": {"X-Org-Id": "acme", "X-User-Id": "u_ada", "X-User-IsOrgAdmin": "true"},
		}
		for name, hdr := range callers {
			for _, path := range []string{"/v1/admin/team", "/v1/admin/team/acme/" + w.UUID} {
				if code, body := get(path, hdr); code != http.StatusForbidden {
					t.Errorf("%s GET %s = %d (%s), want 403", name, path, code, body)
				}
			}
		}
		if code, body := get("/v1/admin/team", superHdr); code != http.StatusOK {
			t.Fatalf("super = %d (%s), want 200", code, body)
		}
	})
}

// TestAdminTeamsPagesWalkEverySpaceOnce: every tenant's spaces come back across
// the pages, none twice, in the order asked, with the total the filters match.
func TestAdminTeamsPagesWalkEverySpaceOnce(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		s, get := mountAdmin(t)
		ctx := context.Background()
		want := map[string]bool{}
		for i, org := range []string{"acme", "acme", "globex", "initech", "initech"} {
			w, err := s.EnsureSpace(ctx, org, accountID("p"+strconv.Itoa(i)+"@x.test"), "Space "+strconv.Itoa(i))
			if err != nil {
				t.Fatal(err)
			}
			want[w.UUID] = true
		}

		for _, sort := range []string{"name", "name:desc", "org", "created:desc"} {
			seen := map[string]bool{}
			var last string
			cursor := ""
			for page := 0; ; page++ {
				if page > 5 {
					t.Fatalf("%s: paging did not end", sort)
				}
				q := url.Values{"limit": {"2"}, "sort": {sort}}
				if cursor != "" {
					q.Set("cursor", cursor)
				}
				out := list(t, get, q)
				if out.Total == nil || *out.Total != len(want) {
					t.Fatalf("%s: total = %v, want %d", sort, out.Total, len(want))
				}
				for _, r := range out.Data {
					if seen[r.ID] {
						t.Fatalf("%s: %s returned twice", sort, r.ID)
					}
					seen[r.ID] = true
					key := map[string]string{"name": r.Name, "name:desc": r.Name, "org": r.Org}[sort]
					if key != "" && last != "" {
						if (sort == "name:desc" && key > last) || (sort != "name:desc" && key < last) {
							t.Fatalf("%s: %q after %q is out of order", sort, key, last)
						}
					}
					last = key
					if r.Members == nil || *r.Members != 1 {
						t.Errorf("%s: members = %v, want the owner's 1", r.ID, r.Members)
					}
				}
				if out.Next == "" {
					break
				}
				cursor = out.Next
			}
			if len(seen) != len(want) {
				t.Fatalf("%s: walked %d spaces, want %d", sort, len(seen), len(want))
			}
		}
	})
}

// TestAdminTeamsFilters: q, org, and a person's own spaces.
func TestAdminTeamsFilters(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		s, get := mountAdmin(t)
		ctx := context.Background()
		const ada = "ada@acme.test"
		mine, err := s.EnsureSpace(ctx, "acme", accountID(ada), "Research")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.EnsureSpace(ctx, "acme", accountID("bob@acme.test"), "Sales"); err != nil {
			t.Fatal(err)
		}
		if _, err := s.EnsureSpace(ctx, "globex", accountID("cy@globex.test"), "Research"); err != nil {
			t.Fatal(err)
		}

		if out := list(t, get, url.Values{"q": {"reSEARCH"}}); *out.Total != 2 {
			t.Errorf("q=reSEARCH total = %d, want 2 (case-insensitive, across orgs)", *out.Total)
		}
		if out := list(t, get, url.Values{"org": {"acme"}}); *out.Total != 2 {
			t.Errorf("org=acme total = %d, want 2", *out.Total)
		}
		out := list(t, get, url.Values{"org": {"acme"}, "user": {ada}})
		if len(out.Data) != 1 || out.Data[0].ID != mine.UUID {
			t.Fatalf("ada's spaces = %+v, want only %s", out.Data, mine.UUID)
		}
		if code, _ := get("/v1/admin/team?user="+url.QueryEscape(ada), superHdr); code != http.StatusBadRequest {
			t.Errorf("user without org = %d, want 400", code)
		}
		for _, bad := range []string{"sort=owner", "sort=name:up", "limit=0", "limit=101", "cursor=nope"} {
			if code, _ := get("/v1/admin/team?"+bad, superHdr); code != http.StatusBadRequest {
				t.Errorf("%s = %d, want 400", bad, code)
			}
		}
	})
}

// TestAdminTeamShowsTheRosterWithRoles: one space, its people and their roles,
// owners first; another org's id for the space is a 404.
func TestAdminTeamShowsTheRosterWithRoles(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		id := planetest.ServeIdentity(t)
		s, get := mountAdmin(t)
		ctx := context.Background()
		owner, guest := accountID("ada@acme.test"), accountID("gus@acme.test")
		id.Named(owner, "Ada")
		id.Named(guest, "Gus")
		w, err := s.EnsureSpace(ctx, "acme", owner, "Acme")
		if err != nil {
			t.Fatal(err)
		}
		if err := s.AddMember(ctx, "acme", w.UUID, guest, "guest"); err != nil {
			t.Fatal(err)
		}

		code, body := get("/v1/admin/team/acme/"+w.UUID, superHdr)
		if code != http.StatusOK {
			t.Fatalf("GET one = %d (%s)", code, body)
		}
		var out teamOut
		if err := json.Unmarshal(body, &out); err != nil || out.Data == nil {
			t.Fatalf("decode: %v (%s)", err, body)
		}
		d := out.Data
		if d.Team.OwnerName != "Ada" || d.Team.Members == nil || *d.Team.Members != 2 {
			t.Errorf("row = %+v, want owner Ada and 2 members", d.Team)
		}
		if len(d.Roster) != 2 || d.Roster[0].Role != "owner" || d.Roster[1].Role != "guest" || d.Roster[1].Name != "Gus" {
			t.Errorf("roster = %+v, want Ada (owner) then Gus (guest)", d.Roster)
		}
		if code, _ := get("/v1/admin/team/globex/"+w.UUID, superHdr); code != http.StatusNotFound {
			t.Errorf("space under the wrong org = %d, want 404", code)
		}
	})
}

func list(t *testing.T, get func(string, map[string]string) (int, []byte), q url.Values) teamsOut {
	t.Helper()
	code, body := get("/v1/admin/team?"+q.Encode(), superHdr)
	if code != http.StatusOK {
		t.Fatalf("GET ?%s = %d (%s)", q.Encode(), code, body)
	}
	var out teamsOut
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode: %v (%s)", err, body)
	}
	if out.Status != "ok" || out.Total == nil {
		t.Fatalf("?%s: %s", q.Encode(), body)
	}
	return out
}
