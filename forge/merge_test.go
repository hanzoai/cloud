package forge

// merge_test.go — a merge lands only at the commit it was read at and is
// confirmed by reading the pull request back; and a workspace answered across
// processes is checked by the rule Find applies.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

const (
	readTip  = "1111111111111111111111111111111111111111"
	movedTip = "2222222222222222222222222222222222222222"
)

// mergeForge holds pull request #7 of acme/app at readTip.
type mergeForge struct {
	mu     sync.Mutex
	merged bool
	block  bool // a rule on the base keeps it from merging
	fake   bool // the forge answers 200 and merges nothing
	asked  []map[string]any
}

func newMergeForge(t *testing.T) (*mergeForge, *Client) {
	t.Helper()
	f := &mergeForge{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/repos/acme/app/pulls/7/merge":
			var in map[string]any
			_ = json.NewDecoder(r.Body).Decode(&in)
			f.asked = append(f.asked, in)
			switch {
			case f.block:
				w.WriteHeader(http.StatusMethodNotAllowed)
				writeJSON(w, map[string]string{"message": "Not allowed to merge: required approvals"})
			case in["head_commit_id"] != readTip:
				w.WriteHeader(http.StatusConflict)
			case !f.fake:
				f.merged = true
			}
		case r.Method == http.MethodGet && r.URL.Path == "/v1/repos/acme/app/pulls/7":
			writeJSON(w, map[string]any{"number": 7, "state": map[bool]string{true: "closed", false: "open"}[f.merged],
				"merged": f.merged, "head": map[string]any{"ref": "agent/x", "sha": readTip, "repo": map[string]any{"full_name": "acme/app"}},
				"base": map[string]any{"ref": "main"}})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	c, err := New(srv.URL, "machine")
	if err != nil {
		t.Fatal(err)
	}
	return f, c.Machine()
}

// A MERGE CARRIES THE COMMIT IT WAS READ AT, never forces, and is confirmed by
// the pull request reading merged afterwards.
func TestMergeLandsTheCommitItRead(t *testing.T) {
	f, c := newMergeForge(t)
	got, err := c.Merge(context.Background(), "acme", "app", 7, readTip, "Add a footer")
	if err != nil || got.State != "merged" || got.Base != "main" || got.Tip != readTip {
		t.Fatalf("Merge = %+v, %v", got, err)
	}
	if len(f.asked) != 1 || f.asked[0]["Do"] != "merge" || f.asked[0]["head_commit_id"] != readTip ||
		f.asked[0]["force_merge"] != nil || f.asked[0]["MergeTitleField"] != "Add a footer" {
		t.Fatalf("asked %v", f.asked)
	}
}

// A BRANCH THAT MOVED, A RULE THAT REFUSES, AND A FORGE THAT SAYS YES AND DOES
// NOTHING are each a failure, told apart.
func TestMergeRefusesWhatDidNotLand(t *testing.T) {
	f, c := newMergeForge(t)
	if _, err := c.Merge(context.Background(), "acme", "app", 7, movedTip, "x"); !errors.Is(err, ErrExists) {
		t.Fatalf("a moved branch = %v", err)
	}
	f.block = true
	_, err := c.Merge(context.Background(), "acme", "app", 7, readTip, "x")
	if !errors.Is(err, ErrNotNow) || !strings.Contains(err.Error(), "required approvals") {
		t.Fatalf("a refusing rule = %v", err)
	}
	f.block, f.fake = false, true
	if _, err := c.Merge(context.Background(), "acme", "app", 7, readTip, "x"); err == nil || !strings.Contains(err.Error(), "reads open") {
		t.Fatalf("a merge that did not happen = %v", err)
	}
	if _, err := c.Merge(context.Background(), "acme", "app", 7, "main", "x"); err == nil {
		t.Fatal("a merge at a branch name, not a commit, was sent")
	}
	if _, err := c.Merge(context.Background(), "acme/..", "app", 7, readTip, "x"); !errors.Is(err, ErrBadName) {
		t.Fatalf("a bad owner = %v", err)
	}
}

// AN ANSWER FROM ANOTHER PROCESS IS HELD TO FIND'S RULE: made, a plain name, and
// never one the estate keeps.
func TestAnAnsweredWorkspaceIsChecked(t *testing.T) {
	if ws, err := Answered("acme-2", true); err != nil || ws != (Workspace{Owner: "acme-2", Made: true}) {
		t.Fatalf("a made workspace = %+v, %v", ws, err)
	}
	if ws, err := Answered("", false); err != nil || ws != (Workspace{}) {
		t.Fatalf("none = %+v, %v", ws, err)
	}
	for owner, made := range map[string]bool{
		"acme": false, "hanzoai": true, "hanzo-apps": true, "lux": true,
		"a/b": true, "../x": true, "-acme": true, strings.Repeat("a", 41): true, "a..b": true,
	} {
		if _, err := Answered(owner, made); err == nil {
			t.Errorf("Answered(%q, %v) was believed", owner, made)
		}
	}
}
