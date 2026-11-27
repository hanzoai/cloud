package forge

// notes_test.go — an issue's conversation and a repository's branches, read as
// whoever the client acts for.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCommentsAreReadAsTheActor(t *testing.T) {
	var asked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = append(asked, r.URL.Path+" as "+r.Header.Get("Sudo"))
		if r.URL.Path != API+"/repos/acme/site/issues/7/comments" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"id": 1, "body": "first", "user": map[string]any{"login": "josh"}, "created_at": "2026-10-04T10:00:00Z"},
			{"id": 2, "body": "second", "user": map[string]any{"login": "ann"}, "created_at": "2026-10-04T11:00:00Z"},
		})
	}))
	defer srv.Close()
	c, _ := New(srv.URL, "tok")
	notes, err := c.As("josh").Comments(context.Background(), "acme", "site", 7)
	if err != nil {
		t.Fatal(err)
	}
	if len(notes) != 2 || notes[0].Body != "first" || notes[1].User == nil || notes[1].User.Login != "ann" {
		t.Fatalf("notes = %+v", notes)
	}
	if asked[0] != API+"/repos/acme/site/issues/7/comments as josh" {
		t.Fatalf("asked %v", asked)
	}
	if _, err := c.As("josh").Comments(context.Background(), "acme", "site", 0); err == nil {
		t.Fatal("a bad number was read")
	}
	if _, err := c.As("josh").Comments(context.Background(), "acme/x", "site", 1); err == nil {
		t.Fatal("a bad owner was read")
	}
}

func TestBranchesAreEveryPage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/repos/acme/site/branches") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var out []map[string]any
		switch r.URL.Query().Get("page") {
		case "1":
			for i := 0; i < page; i++ {
				out = append(out, map[string]any{"name": "b" + string(rune('a'+i%26)) + strings.Repeat("x", i/26)})
			}
		case "2":
			out = []map[string]any{{"name": "main"}}
		}
		_ = json.NewEncoder(w).Encode(out)
	}))
	defer srv.Close()
	c, _ := New(srv.URL, "tok")
	got, err := c.Machine().Branches(context.Background(), "acme", "site")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != page+1 || got[len(got)-1] != "main" {
		t.Fatalf("branches = %d, last %q", len(got), got[len(got)-1])
	}
}

func TestARepositoryCarriesItsDescriptionAndCloneAddress(t *testing.T) {
	var r Repo
	if err := json.Unmarshal([]byte(`{"name":"site","description":"the site","clone_url":"https://git.test/acme/site.git"}`), &r); err != nil {
		t.Fatal(err)
	}
	if r.Description != "the site" || r.Clone != "https://git.test/acme/site.git" {
		t.Fatalf("repo = %+v", r)
	}
}
