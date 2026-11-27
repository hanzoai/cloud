package forge

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// initStub answers the three routes Init touches: the create, the repository
// PATCH that sets its units, and the branch-protection read of monotone.
type initStub struct {
	*httptest.Server
	mu      sync.Mutex
	exists  bool // the repository is already there: the create answers 409
	refuse  bool // the PATCH answers 403
	calls   []string
	patches []map[string]any
}

func newInitStub(t *testing.T, exists, refuse bool) *initStub {
	t.Helper()
	s := &initStub{exists: exists, refuse: refuse}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		defer s.mu.Unlock()
		s.calls = append(s.calls, r.Method+" "+r.URL.Path)
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/orgs/acme/repos"):
			if s.exists {
				w.WriteHeader(http.StatusConflict)
				return
			}
			w.WriteHeader(http.StatusCreated)
		case r.Method == http.MethodPatch && strings.HasSuffix(r.URL.Path, "/repos/acme/api"):
			if s.refuse {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			var in map[string]any
			_ = json.Unmarshal(body, &in)
			s.patches = append(s.patches, in)
			_ = json.NewEncoder(w).Encode(in)
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/branch_protections"):
			_ = json.NewEncoder(w).Encode([]map[string]any{{"rule_name": "**", "enable_push": true, "enable_force_push": false}})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *initStub) init(t *testing.T) error {
	t.Helper()
	c, err := New(s.URL, "machine-token-value")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c.Init(context.Background(), "acme", "api", "synced from github.com")
}

// A synced repository holds an upstream's workflows, which GitHub already runs.
// Init turns the Actions unit off right after the create, before the first push
// can land and start a run.
func TestInit_TurnsActionsOff(t *testing.T) {
	s := newInitStub(t, false, false)
	if err := s.init(t); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if len(s.patches) != 1 || s.patches[0]["has_actions"] != false {
		t.Fatalf("patches=%v; want one PATCH with has_actions false", s.patches)
	}
	if s.calls[0] != "POST /v1/orgs/acme/repos" || s.calls[1] != "PATCH /v1/repos/acme/api" {
		t.Fatalf("calls=%v; want the create, then the PATCH", s.calls)
	}
}

// A repository created before Init learned this is brought into line on its
// next import: the 409 is the state wanted, and the unit is still turned off.
func TestInit_TurnsActionsOffOnARepositoryAlreadyThere(t *testing.T) {
	s := newInitStub(t, true, false)
	if err := s.init(t); err != nil {
		t.Fatalf("Init on an existing repository: %v", err)
	}
	if len(s.patches) != 1 || s.patches[0]["has_actions"] != false {
		t.Fatalf("patches=%v; want one PATCH with has_actions false", s.patches)
	}
}

// A repository whose unit could not be turned off would run the upstream's CI
// on the first push, so Init fails rather than hand it to the sync.
func TestInit_FailsWhenActionsCannotBeTurnedOff(t *testing.T) {
	s := newInitStub(t, false, true)
	err := s.init(t)
	if err == nil || !strings.Contains(err.Error(), "quiet") {
		t.Fatalf("Init = %v; want a refusal naming the unit", err)
	}
}
