package iam

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

// IAM answers a collection as a TYPED object named for its entity, with the size
// beside it. The bytes below are what it returns for each (hanzoai/iam
// internal/organizations, internal/users). There is no envelope: the operation's
// outcome is the HTTP status and the body is the value.
//
// The two spell the size differently — organizations `count`, users `total` — and
// users' is a REAL unpaged count rather than the page length, which is why the
// fixture pairs two rows with a total of 222: it is the number the cockpit pages
// against, and reading the rows instead would silently report every directory as
// one page long.
const (
	orgsBody = `{"organizations":[
		{"owner":"admin","name":"hanzo","displayName":"Hanzo","createdTime":"2020-01-01T00:00:00Z"},
		{"owner":"admin","name":"acme","displayName":"Acme","createdTime":"2021-02-02T00:00:00Z"}
	],"count":2}`

	usersBody = `{"users":[
		{"owner":"acme","name":"ada","email":"ada@acme.com"},
		{"owner":"acme","name":"bob","email":"bob@acme.com"}
	],"total":222}`

	// What the deleted verb surface used to answer. Kept as a fixture because a
	// proxy or a rolled-back IAM can still produce it, and reading it as an empty
	// directory is the failure this client must not have.
	legacyBody = `{"status":"ok","msg":"","data":[
		{"owner":"admin","name":"hanzo","displayName":"Hanzo"}
	],"data2":222}`
)

// TestOrgs_ReadsTheOrganizationCollection pins the path this client must call, the
// key it reads its rows out of, and that the caller's own credential is replayed
// rather than replaced by a service credential.
func TestOrgs_ReadsTheOrganizationCollection(t *testing.T) {
	var gotPath, gotAuth, gotCookie, gotOwner string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotOwner = r.URL.Path, r.URL.Query().Get("owner")
		gotAuth, gotCookie = r.Header.Get("Authorization"), r.Header.Get("Cookie")
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/v1/iam/organizations" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(orgsBody))
	}))
	defer srv.Close()

	cr := Creds{Cookie: "session=abc", Auth: "Bearer caller-token"}
	res, err := New(srv.URL).Orgs(context.Background(), cr, url.Values{"owner": {"admin"}})
	if err != nil {
		t.Fatalf("Orgs: %v", err)
	}
	if gotPath != "/v1/iam/organizations" {
		t.Fatalf("path = %q, want the organization collection", gotPath)
	}
	if gotAuth != "Bearer caller-token" || gotCookie != "session=abc" {
		t.Fatalf("caller credential not replayed: auth=%q cookie=%q", gotAuth, gotCookie)
	}
	if gotOwner != "admin" {
		t.Fatalf("owner = %q, want the scope the caller asked for", gotOwner)
	}
	if res.Total != 2 {
		t.Fatalf("total = %d, want 2", res.Total)
	}
	var orgs []Org
	if err := json.Unmarshal(res.Rows, &orgs); err != nil {
		t.Fatalf("decode rows: %v", err)
	}
	if len(orgs) != 2 || orgs[0].Name != "hanzo" {
		t.Fatalf("rows = %+v, want the org directory", orgs)
	}
}

// TestUsers_TotalIsTheDirectorysNotThePages proves the size is read from the
// answer's own field rather than counted off the rows. The cockpit pages on it, so
// a client that counted would report a 222-person directory as two people.
func TestUsers_TotalIsTheDirectorysNotThePages(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/iam/users" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(usersBody))
	}))
	defer srv.Close()

	res, err := New(srv.URL).Users(context.Background(), Creds{}, url.Values{"owner": {"acme"}})
	if err != nil {
		t.Fatalf("Users: %v", err)
	}
	if res.Total != 222 {
		t.Fatalf("total = %d, want the directory total 222", res.Total)
	}
	var users []User
	if err := json.Unmarshal(res.Rows, &users); err != nil {
		t.Fatalf("decode rows: %v", err)
	}
	if len(users) != 2 {
		t.Fatalf("rows = %d, want the page of 2", len(users))
	}
}

// TestAnAnswerWithoutTheRowsIsAnError is what this file exists for. A body that
// does not carry the collection asked for is a DIFFERENT shape, and the one thing
// this client must never do with it is report an empty page: the directory would
// render blank, with a 200 and no error anywhere to say why. The legacy envelope
// is the reachable instance — a rolled-back IAM or a proxy still answering it.
func TestAnAnswerWithoutTheRowsIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(legacyBody))
	}))
	defer srv.Close()

	_, err := New(srv.URL).List(context.Background(), Creds{}, "/v1/iam/organizations", "organizations", nil)
	if err == nil {
		t.Fatal("an answer carrying no organizations must not read as an empty directory")
	}
	if err.Error() != `iam organizations: answer carries no "organizations"` {
		t.Fatalf("err = %q, want the shape refusal", err)
	}
}

// TestEmptyCollectionIsAListNotANull keeps the operator's own envelope honest: an
// org with no rows answers `null` from a nil slice, and a console that renders a
// list must receive one.
func TestEmptyCollectionIsAListNotANull(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"organizations":null,"count":0}`))
	}))
	defer srv.Close()

	res, err := New(srv.URL).Orgs(context.Background(), Creds{}, nil)
	if err != nil {
		t.Fatalf("Orgs: %v", err)
	}
	if string(res.Rows) != "[]" {
		t.Fatalf("rows = %s, want an empty list", res.Rows)
	}
	if res.Total != 0 {
		t.Fatalf("total = %d, want 0", res.Total)
	}
}

// TestRefusalCarriesIAMsOwnWords proves a non-2xx is reported with the reason IAM
// gave rather than a bare status, which is what makes a missing selector
// diagnosable from the operator's screen.
func TestRefusalCarriesIAMsOwnWords(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"status":400,"error":"owner and name are required"}`))
	}))
	defer srv.Close()

	_, err := New(srv.URL).Org(context.Background(), Creds{}, "admin", "")
	if err == nil || err.Error() != "iam: owner and name are required" {
		t.Fatalf("err = %v, want IAM's own refusal", err)
	}
}

// TestSetUserAddressesTheRowByItsOwnKey pins the write shape: the row travels
// nested under `user`, and there is no id parameter — the object read is the
// object written, so the two cannot name different people.
func TestSetUserAddressesTheRowByItsOwnKey(t *testing.T) {
	var gotPath, gotQuery string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery = r.URL.Path, r.URL.RawQuery
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"owner":"acme","name":"ada"}`))
	}))
	defer srv.Close()

	err := New(srv.URL).SetUser(context.Background(), Creds{},
		map[string]any{"owner": "acme", "name": "ada", "isForbidden": true})
	if err != nil {
		t.Fatalf("SetUser: %v", err)
	}
	if gotPath != "/v1/iam/users/update" {
		t.Fatalf("path = %q, want the user update", gotPath)
	}
	if gotQuery != "" {
		t.Fatalf("query = %q, want the row addressed by its own key alone", gotQuery)
	}
	row, ok := gotBody["user"].(map[string]any)
	if !ok {
		t.Fatalf("body = %v, want the row nested under user", gotBody)
	}
	if row["owner"] != "acme" || row["name"] != "ada" || row["isForbidden"] != true {
		t.Fatalf("row = %v, want the whole row carried through", row)
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
