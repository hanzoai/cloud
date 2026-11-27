package forge

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

// THE INVARIANT, stated as the forge's own predicate over the rule this writes.
//
// A run's key must be refused on the default branch and the humans who own the
// repository must not be. The first half is what [Client.Protected] asks before
// a grant; the second is the thing enable_push:false took away, and the reason
// every pre-existing repository was either unusable by an agent or unusable by
// its team.
func TestProtectRefusesKeysAndKeepsHumans(t *testing.T) {
	var wrote []rule
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/teams") && r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode([]map[string]string{
				{"name": "owners", "permission": "admin"},
				{"name": "readers", "permission": "read"},
			})
		// Two people granted access directly: one who can write and one who can
		// only read. A rule naming only teams would take the branch away from
		// the first.
		case strings.HasSuffix(r.URL.Path, "/collaborators") && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`[{"login":"ada"},{"login":"eve"}]`))
		case strings.HasSuffix(r.URL.Path, "/collaborators/ada/permission"):
			_, _ = w.Write([]byte(`{"permission":"write"}`))
		case strings.HasSuffix(r.URL.Path, "/collaborators/eve/permission"):
			_, _ = w.Write([]byte(`{"permission":"read"}`))
		case strings.HasSuffix(r.URL.Path, "/branch_protections") && r.Method == http.MethodPost:
			// Decoded as the VALUE the caller composed, not as a bag of keys.
			// If a tag is renamed this stops matching, which is the point.
			var got rule
			_ = json.NewDecoder(r.Body).Decode(&got)
			wrote = append(wrote, got)
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{}`))
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	defer srv.Close()

	c, err := New(srv.URL, "tok")
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if err := c.Protect(context.Background(), "hanzoai", "cloud", "main"); err != nil {
		t.Fatalf("protect: %v", err)
	}
	if len(wrote) == 0 {
		t.Fatal("no rule was written")
	}
	for _, r := range wrote {
		// The forge's own predicate, over the value the forge was handed.
		if !r.deployKeyRefused() {
			t.Errorf("rule %q does not refuse a run's key", r.Name)
		}
		// And the half enable_push:false gave away: a human push is still a push
		// the branch takes, governed by a team rather than forbidden outright.
		if !r.Push {
			t.Errorf("rule %q forbids every direct push, including the owners'", r.Name)
		}
		if len(r.PushTeams) == 0 {
			t.Errorf("rule %q whitelists nobody, so it refuses the owners too", r.Name)
		}
		// Only teams that may WRITE are named; a read-only team on a push
		// whitelist is a principal the forge would refuse anyway.
		if slices.Contains(r.PushTeams, "readers") {
			t.Errorf("rule %q whitelists a read-only team", r.Name)
		}
		// A person with write keeps it; a person with read gains nothing.
		if !slices.Contains(r.PushUsers, "ada") {
			t.Errorf("rule %q takes the branch from a collaborator who could write it", r.Name)
		}
		if slices.Contains(r.PushUsers, "eve") {
			t.Errorf("rule %q gives a read-only collaborator push", r.Name)
		}
		// Force-push survives for the same people and no others: the key is
		// refused both ways, and nobody who could rewrite the branch before the
		// rule is refused after it.
		if !r.Force || !r.ForceList || r.ForceKeys {
			t.Errorf("rule %q does not keep force-push for writers alone: %+v", r.Name, r)
		}
		if !slices.Equal(r.ForceTeams, r.PushTeams) || !slices.Equal(r.ForceUsers, r.PushUsers) {
			t.Errorf("rule %q names different people for push and force-push", r.Name)
		}
	}
}

// No resolvable team is a REFUSAL. An empty whitelist does not mean "everyone" —
// it means nobody pushes main, which locks the repository's owners out of their
// own default branch to let a run proceed. Better to refuse the run.
func TestProtectRefusesWhenNobodyCanBeResolved(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/teams") {
			_, _ = w.Write([]byte(`[{"name":"readers","permission":"read"}]`))
			return
		}
		if strings.HasSuffix(r.URL.Path, "/collaborators") {
			_, _ = w.Write([]byte(`[]`))
			return
		}
		t.Errorf("a rule was written with nobody on the whitelist: %s", r.URL.Path)
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	c, nerr := New(srv.URL, "tok")
	if nerr != nil {
		t.Fatalf("new: %v", nerr)
	}
	err := c.Protect(context.Background(), "hanzoai", "cloud", "main")
	if !errors.Is(err, ErrNoPusher) {
		t.Fatalf("want ErrNoPusher, got %v", err)
	}
}
