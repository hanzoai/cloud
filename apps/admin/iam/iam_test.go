package iam

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

// IAM serves two contracts on /v1/iam. The management surface this client speaks
// is uniform — {status,msg,data,data2} with the real total in data2. The
// per-entity REST routes return their own bare shape with no status and no
// uniform total. The bytes below are what IAM actually returns for each (see
// hanzoai/iam internal/compat/aliases.go and internal/organizations).
const (
	managementOrgs = `{"status":"ok","msg":"","data":[
		{"owner":"admin","name":"hanzo","displayName":"Hanzo","createdTime":"2020-01-01T00:00:00Z"},
		{"owner":"admin","name":"acme","displayName":"Acme","createdTime":"2021-02-02T00:00:00Z"}
	],"data2":222}`

	restOrgs = `{"organizations":[
		{"owner":"admin","name":"hanzo","displayName":"Hanzo","createdTime":"2020-01-01T00:00:00Z"}
	],"count":1}`
)

// TestOrgs_ReadsManagementSurface pins the ONE surface this client speaks, on the
// exact path it must call, and proves the caller's credential is replayed rather
// than replaced by a service credential.
func TestOrgs_ReadsManagementSurface(t *testing.T) {
	var gotPath, gotAuth, gotCookie, gotOwner string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotOwner = r.URL.Path, r.URL.Query().Get("owner")
		gotAuth, gotCookie = r.Header.Get("Authorization"), r.Header.Get("Cookie")
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/v1/iam/get-organizations" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(managementOrgs))
	}))
	defer srv.Close()

	cr := Creds{Cookie: "session=abc", Auth: "Bearer caller-token"}
	res, err := New(srv.URL).Orgs(context.Background(), cr, url.Values{"owner": {"admin"}})
	if err != nil {
		t.Fatalf("Orgs: %v", err)
	}
	if gotPath != "/v1/iam/get-organizations" {
		t.Fatalf("path = %q, want the management surface", gotPath)
	}
	if gotAuth != "Bearer caller-token" || gotCookie != "session=abc" {
		t.Fatalf("caller credential not replayed: auth=%q cookie=%q", gotAuth, gotCookie)
	}
	if gotOwner != "admin" {
		t.Fatalf("owner = %q, want the scope the caller asked for", gotOwner)
	}
	// data2 is the REAL directory total, not the page length — the cockpit pages on it.
	if res.Total != 222 {
		t.Fatalf("total = %d, want 222", res.Total)
	}
	var orgs []Org
	if err := json.Unmarshal(res.Rows, &orgs); err != nil {
		t.Fatalf("decode rows: %v", err)
	}
	if len(orgs) != 2 || orgs[0].Name != "hanzo" {
		t.Fatalf("rows = %+v, want the org directory", orgs)
	}
}

// TestRESTShapeIsNotDecodable is the regression this file exists for. Pointing
// this client at IAM's per-entity REST route returns a perfectly healthy 200
// whose body carries no status field — which this envelope reads as failure and
// reports as "iam status 200", the error that took admin.hanzo.ai's Organizations
// panel down. The surfaces are not interchangeable; the client speaks one.
func TestRESTShapeIsNotDecodable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(restOrgs))
	}))
	defer srv.Close()

	_, err := New(srv.URL).List(context.Background(), Creds{}, "/v1/iam/organizations", nil)
	if err == nil {
		t.Fatal("a REST-shaped 200 must not decode as a management envelope")
	}
	if err.Error() != "iam: iam status 200" {
		t.Fatalf("err = %q, want the production symptom", err)
	}
}

// TestNotConfigured keeps an unwired IAM honest rather than silently empty.
func TestNotConfigured(t *testing.T) {
	c := New("")
	if c.Ready() {
		t.Fatal("an empty base must not report Ready")
	}
	if _, err := c.Orgs(context.Background(), Creds{}, nil); err == nil {
		t.Fatal("an unwired IAM must report the not-configured error")
	}
}
