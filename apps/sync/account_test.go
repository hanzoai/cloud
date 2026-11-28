package sync

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// account_test.go proves the account sync: an address naming only a GitHub
// account links every repository that account holds, an event from any of its
// repositories reaches it, and reconciling it declares one repository sync per
// repository — once.

// TestAnAccountAddressIsAnAccountSync: the account's own address is the source,
// folded to one spelling; the target is every repository under its own name; a
// GitLab group is refused because the flat forge cannot spell what nests under
// one; a repository address is still a repository.
func TestAnAccountAddressIsAnAccountSync(t *testing.T) {
	for _, raw := range []string{"https://github.com/Webby-AI", "https://github.com/webby-ai/"} {
		src, err := validateGitSource(endpointReq{Provider: "github", Locator: raw})
		if err != nil {
			t.Fatalf("%s: %v", raw, err)
		}
		if src.Locator != "https://github.com/webby-ai" {
			t.Errorf("%s: locator = %q, want https://github.com/webby-ai", raw, src.Locator)
		}
		if tgt := deriveGitTarget(endpointReq{Locator: "ignored"}, src); tgt.Locator != accountTarget || tgt.Provider != provNative {
			t.Errorf("%s: target = %+v, want every repository on the native side", raw, tgt)
		}
	}
	if _, err := validateGitSource(endpointReq{Provider: "gitlab", Locator: "https://gitlab.com/acme"}); err == nil {
		t.Error("a GitLab group was accepted as an account source")
	}
	repo, err := validateGitSource(endpointReq{Provider: "github", Locator: widgetsURL})
	if err != nil || accountLocator(repo.Locator) != "" {
		t.Errorf("a repository address read as an account: %+v, %v", repo, err)
	}
}

// TestAnAccountSyncHearsItsAccount: any event from a repository of the account
// reaches the account sync, with or without a ref, because it may name a
// repository the account sync does not cover yet. Another account's does not,
// and an account sync that is off hears nothing.
func TestAnAccountSyncHearsItsAccount(t *testing.T) {
	sy := Sync{Kind: "git", Direction: dirPull,
		Source: Endpoint{Provider: provGitHub, Locator: "https://github.com/webby-ai"},
		Target: Endpoint{Provider: provNative, Locator: accountTarget}}
	created := Event{Provider: provGitHub, Locator: "https://github.com/webby-ai/intel-hub.git", Repo: "intel-hub"}
	if a := resolve(sy, created); !a.do || a.inbound {
		t.Fatalf("a repository of the account = %+v, want a reconcile", a)
	}
	other := created
	other.Locator = "https://github.com/acme/intel-hub.git"
	if a := resolve(sy, other); a.do {
		t.Fatalf("another account's repository reached the account sync: %+v", a)
	}
	off := sy
	off.Direction = dirOff
	if a := resolve(off, created); a.do {
		t.Fatalf("an account sync that is off acted: %+v", a)
	}
	// A repository sync is not woken by an event with no ref: there is nothing
	// to advance.
	if a := resolve(gitSync(dirPull), Event{Provider: provGitHub, Repo: "widgets"}); a.do {
		t.Fatalf("a repository sync acted on an event with no ref: %+v", a)
	}
}

// TestDeclareWritesOneSyncPerRepository: each repository the account holds gets
// a sync of its own carrying the account sync's policy, under the spelling a
// hand-made sync of it would have; declaring again writes nothing.
func TestDeclareWritesOneSyncPerRepository(t *testing.T) {
	mountForge(t)
	st, err := storeFor(mounted.Load(), "webby-ai")
	if err != nil {
		t.Fatal(err)
	}
	acct := Sync{ID: "sync_acct", Org: "webby-ai", Kind: "git", Direction: dirPull, Trigger: trigPoll, Actor: "hanzo-sync",
		Source: Endpoint{Provider: provGitHub, Locator: "https://github.com/webby-ai"},
		Target: Endpoint{Provider: provNative, Locator: accountTarget}}
	sources := []string{
		"https://github.com/webby-ai/intel-hub.git",
		"https://github.com/webby-ai/Site.git",
		"https://github.com/webby-ai",              // the account itself is not a repository
		"https://github.com/webby-ai/bad name.git", // a name the forge cannot hold
	}
	made, err := declare(context.Background(), st, acct, sources)
	if err != nil {
		t.Fatalf("declare: %v", err)
	}
	if len(made) != 2 {
		t.Fatalf("declared %d syncs, want 2: %+v", len(made), made)
	}
	byTarget := map[string]Sync{}
	for _, c := range made {
		byTarget[c.Target.Locator] = c
		if c.Direction != dirPull || c.Trigger != trigPoll || c.Actor != "hanzo-sync" || c.Org != "webby-ai" {
			t.Errorf("%s did not inherit the account sync's policy: %+v", c.Target.Locator, c)
		}
		if c.account() != "" {
			t.Errorf("%s is itself an account sync", c.Target.Locator)
		}
	}
	if c, ok := byTarget["intel-hub"]; !ok || c.Source.Locator != "https://github.com/webby-ai/intel-hub.git" {
		t.Errorf("intel-hub = %+v", c)
	}
	if _, ok := byTarget["site"]; !ok {
		t.Errorf("Site was not declared under its folded name: %v", byTarget)
	}
	again, err := declare(context.Background(), st, acct, sources)
	if err != nil || len(again) != 0 {
		t.Fatalf("declaring again wrote %d syncs (%v); it must write none", len(again), err)
	}
}

// TestNativesReadTheForge: a repo link's native copy reports where it lives, its
// default branch and that it is in step; a diverged upstream reads conflict; a
// repository the forge does not hold reads pending; an off link reads paused;
// an account link has no copy of its own.
func TestNativesReadTheForge(t *testing.T) {
	_, fg, src, cloneURL := imported(t)
	s := mounted.Load()
	st, err := storeFor(s, forgeOrg)
	if err != nil {
		t.Fatal(err)
	}
	link := func(id, locator, target, dir string) Sync {
		return Sync{ID: id, Org: forgeOrg, Kind: "git", Direction: dir,
			Source: Endpoint{Provider: provGitHub, Locator: locator},
			Target: Endpoint{Provider: provNative, Locator: target}}
	}
	rows := []Sync{
		link("held", cloneURL, repoName, dirPull),
		link("absent", strings.Replace(cloneURL, repoName, "gadgets", 1), "gadgets", dirPull),
		link("off", cloneURL, repoName, dirOff),
		link("acct", "https://github.com/"+upOwner, accountTarget, dirPull),
	}
	got := natives(context.Background(), s, forgeOrg, st, rows)

	held := got["held"]
	if held == nil {
		t.Fatalf("no copy for an imported repository: %v", got)
	}
	if held.Status != "synced" || held.Branch != "main" || held.AdvancedAt == "" {
		t.Errorf("held = %+v, want synced on main with an advance", held)
	}
	wantURL := "/" + upOwner + "/" + repoName
	if !strings.HasSuffix(held.URL, wantURL) || held.Clone != held.URL+".git" {
		t.Errorf("held addresses = %q / %q, want …%s and its .git", held.URL, held.Clone, wantURL)
	}
	if a := got["absent"]; a == nil || a.Status != "pending" || a.Branch != "" {
		t.Errorf("absent = %+v, want pending", a)
	}
	if o := got["off"]; o == nil || o.Status != "paused" {
		t.Errorf("off = %+v, want paused", o)
	}
	if _, ok := got["acct"]; ok {
		t.Error("an account link reported a copy of its own")
	}

	diverge(t, fg.URL+"/"+upOwner+"/"+repoName+".git", "somebody's work")
	src.commit("upstream's work")
	if res := inbound(t, cloneURL); !res.Conflict {
		t.Fatalf("want conflict, got %+v", res)
	}
	if c := natives(context.Background(), s, forgeOrg, st, rows[:1])["held"]; c == nil || c.Status != "conflict" {
		t.Errorf("after a divergence = %+v, want conflict", c)
	}
}

// TestTheAPIDeclaresAnAccountSync: POST /v1/sync with an account address answers
// an account link, and listing carries each link's scope.
func TestTheAPIDeclaresAnAccountSync(t *testing.T) {
	app := mountSync(t)
	code, body := do(t, app, http.MethodPost, "/v1/sync", "acme", map[string]any{
		"source": map[string]any{"provider": "github", "locator": "https://github.com/acme-gh"},
	})
	if code != http.StatusOK {
		t.Fatalf("create = %d (%s)", code, body)
	}
	var v syncView
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatal(err)
	}
	if v.Scope != "account" || v.Target.Locator != accountTarget || v.Native != nil {
		t.Fatalf("account link = %+v", v)
	}
	if code, body = do(t, app, http.MethodPost, "/v1/sync", "acme", map[string]any{
		"source": map[string]any{"provider": "github", "locator": widgetsURL},
	}); code != http.StatusOK {
		t.Fatalf("create repo = %d (%s)", code, body)
	}
	code, body = do(t, app, http.MethodGet, "/v1/sync", "acme", nil)
	if code != http.StatusOK {
		t.Fatalf("list = %d (%s)", code, body)
	}
	var list syncList
	if err := json.Unmarshal(body, &list); err != nil {
		t.Fatal(err)
	}
	scopes := map[string]string{}
	for _, l := range list.Data {
		scopes[l.Source.Locator] = l.Scope
	}
	if scopes["https://github.com/acme-gh"] != "account" || scopes[widgetsURL] != "repo" {
		t.Fatalf("scopes = %v", scopes)
	}
}
