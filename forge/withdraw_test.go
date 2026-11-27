package forge

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// A pull request is closed, not merged, and a number that names none is refused
// before the forge is asked.
func TestWithdrawClosesAPullRequest(t *testing.T) {
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b map[string]string
		_ = json.NewDecoder(r.Body).Decode(&b)
		got = append(got, r.Method+" "+r.URL.Path+" "+b["state"])
		writeJSON(w, map[string]any{})
	}))
	t.Cleanup(srv.Close)
	c, err := New(srv.URL, "tok")
	if err != nil {
		t.Fatal(err)
	}
	c = c.Machine()
	if err := c.Withdraw(context.Background(), "hanzo-inc", "cloud", 4); err != nil {
		t.Fatalf("Withdraw: %v", err)
	}
	if len(got) != 1 || got[0] != "PATCH "+API+"/repos/hanzo-inc/cloud/pulls/4 closed" {
		t.Fatalf("the forge was asked %v", got)
	}
	if err := c.Withdraw(context.Background(), "hanzo-inc", "cloud", 0); err == nil {
		t.Fatalf("pull request 0: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("a number that names no pull request reached the forge: %v", got)
	}
}
