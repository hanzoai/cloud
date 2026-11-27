package agents

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk/audit"
)

// A grant covers its folder and every folder under it, and nothing else: not a
// sibling that shares its prefix, not a path that climbs out with `..`, not a
// relative path, and not after it is revoked.
func TestFolderGrantContainment(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		s := testSessionStore(t)
		ctx := context.Background()
		now := time.Now().Unix()
		if _, created, err := s.Grant(ctx, "acme", "t1", "/a", "u-ann", now); err != nil || !created {
			t.Fatalf("grant /a: created=%v %v", created, err)
		}
		if _, _, err := s.Grant(ctx, "acme", "t1", "/c/d", "u-ann", now); err != nil {
			t.Fatal(err)
		}
		if _, created, err := s.Grant(ctx, "acme", "t1", "/a", "u-ann", now); err != nil || created {
			t.Fatalf("a second grant of /a made another row: created=%v %v", created, err)
		}
		for dir, want := range map[string]bool{
			"/a": true, "/a/": true, "/a/b": true, "/a/./b/c": true, "/c/d/e": true,
			"/ab": false, "/c": false, "/a/../etc": false, "/a/b/../../etc": false,
			"a/b": false, "": false, "/": false, "/a/b\x00": false,
		} {
			got, err := s.Granted(ctx, "acme", "t1", dir)
			if err != nil || got != want {
				t.Errorf("Granted(%q) = %v, %v; want %v", dir, got, err, want)
			}
		}
		if got, _ := s.Granted(ctx, "acme", "t2", "/a/b"); got {
			t.Fatal("a grant on one machine covered another")
		}
		if got, _ := s.Granted(ctx, "evil", "t1", "/a/b"); got {
			t.Fatal("a grant in one org covered another")
		}
		if ok, err := s.Revoke(ctx, "acme", "t1", "/a", now+1); err != nil || !ok {
			t.Fatalf("revoke /a: %v %v", ok, err)
		}
		if got, _ := s.Granted(ctx, "acme", "t1", "/a/b"); got {
			t.Fatal("a revoked grant still counts")
		}
		if got, _ := s.Granted(ctx, "acme", "t1", "/c/d/e"); !got {
			t.Fatal("revoking one folder revoked another")
		}
		if ok, _ := s.Revoke(ctx, "acme", "t1", "/a", now+2); ok {
			t.Fatal("revoking a revoked folder reported a revoke")
		}
		if _, _, err := s.Grant(ctx, "acme", "t1", "/", "u-ann", now); err != nil {
			t.Fatal(err)
		}
		if got, _ := s.Granted(ctx, "acme", "t1", "/anything/at/all"); !got {
			t.Fatal("a grant of / does not cover the machine")
		}
	})
}

func TestCleanFolder(t *testing.T) {
	for raw, want := range map[string]string{"/a/./b/": "/a/b", "/a//b": "/a/b", "/": "/"} {
		if got, err := cleanFolder(raw); err != nil || got != want {
			t.Errorf("cleanFolder(%q) = %q, %v; want %q", raw, got, err, want)
		}
	}
	for _, raw := range []string{"", "a", "./a", "/a/..", "/a/../b", "/a\nb", strings.Repeat("/a", maxFolder)} {
		if got, err := cleanFolder(raw); err == nil {
			t.Errorf("cleanFolder(%q) = %q; want refused", raw, got)
		}
	}
}

// Only the machine's owner grants or revokes a folder; another member of the
// org — an org admin included — is refused 403, reads the grants all the same,
// and another org finds no machine. The grants ride on the target a heartbeat
// answers, and every grant and revoke is audited.
func TestFolderGrantIsTheOwnersAlone(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountApp(t, &fakeAI{content: "x"})
		rec := audited(t)
		id := registerAs(t, app, "acme", "u-ann", "box")
		folders := "/v1/agent/targets/" + id + "/folders"
		site := map[string]any{"folder": "/home/ann/site"}

		if code, body := reqAs(t, app, "POST", folders, "acme", "u-ann", false, "", map[string]any{"folder": "site"}); code != http.StatusBadRequest {
			t.Fatalf("a relative folder: %d %s", code, body)
		}
		if code, body := reqAs(t, app, "POST", folders, "acme", "u-ann", false, "", map[string]any{"folder": "/home/ann/../bob"}); code != http.StatusBadRequest {
			t.Fatalf("a folder with ..: %d %s", code, body)
		}
		code, body := reqAs(t, app, "POST", folders, "acme", "u-ann", false, "", site)
		var v folderView
		if code != http.StatusCreated || json.Unmarshal(body, &v) != nil || v.Folder != "/home/ann/site" || v.GrantedBy != "u-ann" || v.GrantedAt == "" {
			t.Fatalf("grant: %d %s", code, body)
		}
		if code, body := reqAs(t, app, "POST", folders, "acme", "u-ann", false, "", site); code != http.StatusOK {
			t.Fatalf("a repeated grant: %d %s", code, body)
		}
		if code, body := reqAs(t, app, "POST", folders, "acme", "u-bob", false, "", map[string]any{"folder": "/home/bob"}); code != http.StatusForbidden {
			t.Fatalf("another member grants: %d %s", code, body)
		}
		if code, body := reqAs(t, app, "POST", folders, "acme", "u-bob", true, "", map[string]any{"folder": "/home/bob"}); code != http.StatusForbidden {
			t.Fatalf("an org admin grants: %d %s", code, body)
		}
		if code, body := reqAs(t, app, "POST", folders, "evil", "u-ann", false, "", site); code != http.StatusNotFound {
			t.Fatalf("another org grants: %d %s", code, body)
		}

		code, body = reqAs(t, app, "GET", folders, "acme", "u-bob", false, "", nil)
		var list folderList
		if code != http.StatusOK || json.Unmarshal(body, &list) != nil || len(list.Folders) != 1 || list.Folders[0].Folder != "/home/ann/site" {
			t.Fatalf("a member lists the grants: %d %s", code, body)
		}
		if code, _ := reqAs(t, app, "GET", folders, "evil", "u-mal", false, "", nil); code != http.StatusNotFound {
			t.Fatalf("another org lists the grants: %d", code)
		}

		// The heartbeat answers the machine its folders.
		code, body = reqAs(t, app, "PATCH", "/v1/agent/targets/"+id, "acme", "u-ann", false, "", map[string]any{"metrics": map[string]any{"load1": 0.5}})
		var tv targetView
		if code != http.StatusOK || json.Unmarshal(body, &tv) != nil || len(tv.Folders) != 1 || tv.Folders[0] != "/home/ann/site" {
			t.Fatalf("heartbeat: %d %s", code, body)
		}

		if code, body := reqAs(t, app, "DELETE", folders+"?folder=/home/ann/site", "acme", "u-bob", true, "", nil); code != http.StatusForbidden {
			t.Fatalf("an org admin revokes: %d %s", code, body)
		}
		if code, body := reqAs(t, app, "DELETE", folders+"?folder=/home/ann/site", "acme", "u-ann", false, "", nil); code != http.StatusOK {
			t.Fatalf("revoke: %d %s", code, body)
		}
		if code, body := reqAs(t, app, "DELETE", folders+"?folder=/home/ann/site", "acme", "u-ann", false, "", nil); code != http.StatusNotFound {
			t.Fatalf("revoking again: %d %s", code, body)
		}
		code, body = reqAs(t, app, "GET", "/v1/agent/targets/"+id, "acme", "u-ann", false, "", nil)
		tv = targetView{}
		if code != http.StatusOK || json.Unmarshal(body, &tv) != nil || tv.Folders == nil || len(tv.Folders) != 0 {
			t.Fatalf("the machine still reads a revoked folder: %d %s", code, body)
		}

		rows, _, err := rec.Query(context.Background(), audit.Filter{Org: "acme"})
		if err != nil {
			t.Fatal(err)
		}
		seen := map[string]int{}
		for _, r := range rows {
			if !strings.HasPrefix(r.Action, "machine.folder.") {
				continue
			}
			var after map[string]string
			_ = json.Unmarshal(r.After, &after)
			if r.Resource != (audit.Resource{Type: "machine", ID: id}) || after["folder"] != "/home/ann/site" || r.Actor.Sub != "u-ann" {
				t.Fatalf("%s recorded as %+v %s", r.Action, r, r.After)
			}
			seen[r.Action]++
		}
		if seen[folderGrant] != 1 || seen[folderRevoke] != 1 || len(seen) != 2 {
			t.Fatalf("audited %v, want one grant and one revoke", seen)
		}
	})
}

// The gate admits a run inside a granted folder and refuses one outside with
// folder_not_granted; a run that names no folder is admitted as before. A run
// queued in a folder revoked before the machine claims it is not handed out:
// the run ends refused. Each start and refusal is audited with its run.
func TestFolderGateAndClaim(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountApp(t, &fakeAI{content: "x"})
		rec := audited(t)
		old := claimLongPoll
		claimLongPoll = 150 * time.Millisecond
		defer func() { claimLongPoll = old }()
		id, key := registerAndMint(t, app, "acme", "box")
		claim := "/v1/agent/targets/" + id + "/claim"
		if code, _ := doKey(t, app, "POST", claim, "acme", key); code != http.StatusNoContent {
			t.Fatalf("first poll: %d", code)
		}
		if code, body := do(t, app, "POST", "/v1/agent/targets/"+id+"/folders", "acme", map[string]any{"folder": "/srv/site"}); code != http.StatusCreated {
			t.Fatalf("grant: %d %s", code, body)
		}
		ctx := context.Background()

		if err := TargetDispatchable(ctx, "acme", id, ""); err != nil {
			t.Fatalf("a run with no folder: %v", err)
		}
		if err := TargetDispatchable(ctx, "acme", id, "/srv/site/web"); err != nil {
			t.Fatalf("a run in a granted folder: %v", err)
		}
		for _, dir := range []string{"/srv/other", "/srv/site/../other", "srv/site"} {
			var refused *zip.HTTPError
			if err := TargetDispatchable(ctx, "acme", id, dir); !errors.As(err, &refused) || refused.Code != FolderNotGranted || refused.Status != http.StatusForbidden {
				t.Fatalf("a run in %s: %v", dir, err)
			}
		}
		if err := Admit(ctx, "acme", id, "/srv/site", "sess_ok"); err != nil {
			t.Fatalf("admit: %v", err)
		}

		OfferRoutedRun(RoutedRun{Org: "acme", TargetID: id, SessionID: "sess_ok", Dir: "/srv/site"})
		code, body := doKey(t, app, "POST", claim, "acme", key)
		var run routedRunOut
		if code != http.StatusOK || json.Unmarshal(body, &run) != nil || run.Dir != "/srv/site" {
			t.Fatalf("claim in a granted folder: %d %s", code, body)
		}

		off := OfferRoutedRun(RoutedRun{Org: "acme", TargetID: id, SessionID: "sess_late", Dir: "/srv/site"})
		got := make(chan RoutedResult, 1)
		go func() { res, _ := off.Await(ctx); got <- res }()
		if code, body := do(t, app, "DELETE", "/v1/agent/targets/"+id+"/folders?folder=/srv/site", "acme", nil); code != http.StatusOK {
			t.Fatalf("revoke: %d %s", code, body)
		}
		if code, body := doKey(t, app, "POST", claim, "acme", key); code != http.StatusNoContent {
			t.Fatalf("a run in a revoked folder was handed out: %d %s", code, body)
		}
		select {
		case res := <-got:
			if res.OK || !strings.Contains(res.Error, "not one the machine's owner has let the web use") {
				t.Fatalf("the refused run ended %+v", res)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("the refused run never ended")
		}

		rows, _, err := rec.Query(ctx, audit.Filter{Org: "acme"})
		if err != nil {
			t.Fatal(err)
		}
		var runs, denials []string
		for _, r := range rows {
			var after map[string]string
			_ = json.Unmarshal(r.After, &after)
			switch r.Action {
			case folderRun:
				runs = append(runs, after["run"])
			case folderDeny:
				if r.Outcome.Result != "deny" || r.Outcome.Reason != FolderNotGranted || after["machine"] != id {
					t.Fatalf("denial recorded as %+v %s", r, r.After)
				}
				denials = append(denials, after["folder"]+"@"+after["run"])
			}
		}
		if len(runs) != 1 || runs[0] != "sess_ok" {
			t.Fatalf("run starts audited: %v", runs)
		}
		if len(denials) != 4 || !slices.Contains(denials, "/srv/site@sess_late") {
			t.Fatalf("denials audited: %v", denials)
		}
	})
}
