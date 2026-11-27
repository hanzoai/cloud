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

// protectStub answers the branch-protection routes the way the forge does,
// including its 403 for a create of a rule that already exists.
type protectStub struct {
	*httptest.Server
	mu      sync.Mutex
	rules   []map[string]any
	creates int
	patches []map[string]any
}

func newProtectStub(t *testing.T, rules ...map[string]any) *protectStub {
	t.Helper()
	s := &protectStub{rules: rules}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		defer s.mu.Unlock()
		switch {
		case strings.HasSuffix(r.URL.Path, "/branch_protections") && r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode(s.rules)
		case strings.HasSuffix(r.URL.Path, "/branch_protections") && r.Method == http.MethodPost:
			var in map[string]any
			_ = json.Unmarshal(body, &in)
			for _, ru := range s.rules {
				if ru["rule_name"] == in["rule_name"] {
					w.WriteHeader(http.StatusForbidden)
					_, _ = w.Write([]byte(`{"message":"Branch protection already exist"}`))
					return
				}
			}
			s.creates++
			s.rules = append(s.rules, in)
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(in)
		case strings.HasSuffix(r.URL.Path, "/branch_protections/**") && r.Method == http.MethodPatch:
			var in map[string]any
			_ = json.Unmarshal(body, &in)
			s.patches = append(s.patches, in)
			_ = json.NewEncoder(w).Encode(in)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *protectStub) machine(t *testing.T) *Client {
	t.Helper()
	c, err := New(s.URL, "machine-token-value")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

func TestMonotone_CreatesTheRuleWhenAbsent(t *testing.T) {
	s := newProtectStub(t)
	if err := s.machine(t).monotone(context.Background(), "acme", "api"); err != nil {
		t.Fatalf("monotone: %v", err)
	}
	if s.creates != 1 || len(s.patches) != 0 {
		t.Fatalf("creates=%d patches=%d; want one create and no patch", s.creates, len(s.patches))
	}
	if push, force := s.rules[0]["enable_push"], s.rules[0]["enable_force_push"]; push != true || force != false {
		t.Fatalf("created rule push=%v force=%v; want push open and force closed", push, force)
	}
}

// The case every existing synced repository is in: the forge would refuse a
// second create with 403, so the rule must be recognised rather than recreated.
func TestMonotone_AcceptsTheRuleAlreadyThere(t *testing.T) {
	s := newProtectStub(t, map[string]any{"rule_name": "**", "enable_push": true, "enable_force_push": false})
	if err := s.machine(t).monotone(context.Background(), "acme", "api"); err != nil {
		t.Fatalf("monotone on a repository that already holds the rule: %v", err)
	}
	if s.creates != 0 || len(s.patches) != 0 {
		t.Fatalf("creates=%d patches=%d; the rule was already right, nothing should be written", s.creates, len(s.patches))
	}
}

func TestMonotone_ClosesForcePushOnAWeakerRule(t *testing.T) {
	s := newProtectStub(t, map[string]any{"rule_name": "**", "enable_push": true, "enable_force_push": true})
	if err := s.machine(t).monotone(context.Background(), "acme", "api"); err != nil {
		t.Fatalf("monotone: %v", err)
	}
	if s.creates != 0 || len(s.patches) != 1 {
		t.Fatalf("creates=%d patches=%d; want the existing rule corrected in place", s.creates, len(s.patches))
	}
	if force := s.patches[0]["enable_force_push"]; force != false {
		t.Fatalf("patched force=%v; want force push closed", force)
	}
}
