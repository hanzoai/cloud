package reward

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/hanzoai/reward/catalog"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/sqltest"
)

// github is a fake GitHub: JSON answers by exact path, a status for the rest, and
// every path asked.
type github struct {
	answers map[string]string
	busy    bool
	asked   []string
}

func (g *github) api(_ context.Context, path, _ string) (int, []byte, error) {
	g.asked = append(g.asked, path)
	if g.busy {
		return 0, nil, fmt.Errorf("%w: rate limited", errBusy)
	}
	if a, ok := g.answers[path]; ok {
		return http.StatusOK, []byte(a), nil
	}
	return http.StatusNotFound, []byte(`{}`), nil
}

func (r *rig) github(g *github, linked map[int64]string) {
	stub(r.t, &apiFn, g.api)
	stub(r.t, &peopleFn, func(_ context.Context, ids []string) ([]client.GitHubPerson, error) {
		var out []client.GitHubPerson
		for _, id := range ids {
			for gid, sub := range linked {
				if fmt.Sprint(gid) == id {
					out = append(out, client.GitHubPerson{ID: id, Subject: sub, Org: "acme"})
				}
			}
		}
		return out, nil
	})
}

func mergedPR(author, mergedBy int64, labels ...string) []byte {
	var ls []map[string]string
	for _, l := range labels {
		ls = append(ls, map[string]string{"name": l})
	}
	b, _ := json.Marshal(map[string]any{
		"action": "closed", "sender": map[string]any{"id": mergedBy, "login": "maint"},
		"repository":   map[string]any{"full_name": "hanzoai/commerce", "private": false, "default_branch": "main"},
		"pull_request": map[string]any{"number": 7, "merged": true, "user": map[string]any{"id": author, "login": "contrib", "type": "User"}, "merged_by": map[string]any{"id": mergedBy, "login": "maint", "type": "User"}, "author_association": "NONE", "labels": ls, "base": map[string]any{"ref": "main"}},
	})
	return b
}

const prFromGitHub = `{"merged": true, "user": {"id": 42, "login": "contrib"}, "base": {"repo": {"private": false}}}`

func TestAMergedPullRequestIsStatedToItsAuthorWithTheCheckThatProvedIt(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		r := newRig(t)
		g := &github{answers: map[string]string{"/repos/hanzoai/commerce/pulls/7": prFromGitHub}}
		r.github(g, map[int64]string{42: "sub-contrib"})
		n, why, err := r.s.delivery(context.Background(), "pull_request", "d1", mergedPR(42, 9, "bug"))
		if err != nil || n != 1 {
			t.Fatalf("delivery = %d %q %v", n, why, err)
		}
		cs := r.claimsOf("sub-contrib", "github.pr.merged")
		if len(cs) != 1 || cs[0].Rule != "pr-fix" || cs[0].Fields["kind"] != "fix" {
			t.Fatalf("claims = %+v", cs)
		}
		ev := cs[0].Evidence
		if ev == nil || ev.Source != "github" || ev.URL != "https://github.com/hanzoai/commerce/pull/7" || ev.NeedsReview || len(ev.Checks) != 1 {
			t.Fatalf("evidence = %+v", ev)
		}
		if c := ev.Checks[0]; c.Status != 200 || !c.Public || c.AuthorMatch != catalog.Yes || c.FetchedAt.IsZero() || c.RefFound != nil {
			t.Errorf("check = %+v", c)
		}
		// it reads back through the staff queue, evidence and all.
		q := r.queue("sub-contrib")
		if len(q) != 1 || q[0]["evidence"] == nil {
			t.Fatalf("queue = %+v", q)
		}
		// the same delivery again, as GitHub redelivers, is one claim.
		if n, _, _ := r.s.delivery(context.Background(), "pull_request", "d1", mergedPR(42, 9, "bug")); n != 0 {
			t.Errorf("a redelivery claimed %d", n)
		}
		if len(r.claimsOf("sub-contrib", "github.pr.merged")) != 1 {
			t.Error("a redelivery made a second claim")
		}

	})
}

func TestAGitHubAccountNobodyLinkedBotsAndSelfMergesEarnNothing(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		r := newRig(t)
		g := &github{answers: map[string]string{"/repos/hanzoai/commerce/pulls/7": prFromGitHub}}
		r.github(g, map[int64]string{42: "sub-contrib"})
		ctx := context.Background()
		for name, body := range map[string][]byte{
			"unlinked author": mergedPR(77, 9),
			"self merge":      mergedPR(42, 42),
		} {
			if n, why, err := r.s.delivery(ctx, "pull_request", "d", body); err != nil || n != 0 || why == "" {
				t.Errorf("%s: %d %q %v", name, n, why, err)
			}
		}
		bot := strings.Replace(string(mergedPR(42, 9)), `"type":"User"`, `"type":"Bot"`, -1)
		if n, why, _ := r.s.delivery(ctx, "pull_request", "d", []byte(bot)); n != 0 || why != "a bot's pull request" {
			t.Errorf("bot: %d %q", n, why)
		}
		staff := strings.Replace(string(mergedPR(42, 9)), `"NONE"`, `"MEMBER"`, 1)
		if n, why, _ := r.s.delivery(ctx, "pull_request", "d", []byte(staff)); n != 0 || why != "a member's own work" {
			t.Errorf("staff: %d %q", n, why)
		}
		other := strings.Replace(string(mergedPR(42, 9)), "hanzoai/commerce", "someone/else", 1)
		if n, why, _ := r.s.delivery(ctx, "pull_request", "d", []byte(other)); n != 0 || why != "not a repository of ours" {
			t.Errorf("other org: %d %q", n, why)
		}
		// GitHub contradicts the delivery: the PR is by another account. Nothing is stated.
		g.answers["/repos/hanzoai/commerce/pulls/7"] = `{"merged": true, "user": {"id": 99}, "base": {"repo": {"private": false}}}`
		if n, _, _ := r.s.delivery(ctx, "pull_request", "d", mergedPR(42, 9)); n != 0 {
			t.Errorf("a delivery GitHub contradicts claimed %d", n)
		}
		if got := r.claims("sub-contrib"); len(got) != 0 {
			t.Errorf("claims = %d, want 0", len(got))
		}

	})
}

func TestABountyLabelStatesTheBountyAndNotAlsoTheOrdinaryMerge(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		r := newRig(t)
		g := &github{answers: map[string]string{
			"/repos/hanzoai/commerce/pulls/7":   prFromGitHub,
			"/repos/hanzoai/commerce/pulls/8":   `{"merged": true, "title": "Fix bug", "body": "Fixes #12", "user": {"id": 42, "login": "contrib"}, "base": {"repo": {"private": false}}}`,
			"/repos/hanzoai/commerce/issues/12": `{"user": {"id": 88, "login": "reporter", "type": "User"}, "author_association": "NONE", "labels": [{"name": "bounty"}, {"name": "bounty:P0"}]}`,
		}}
		r.github(g, map[int64]string{42: "sub-contrib", 88: "sub-reporter"})
		if n, _, err := r.s.delivery(context.Background(), "pull_request", "d", mergedPR(42, 9, "bounty-standard")); err != nil || n != 1 {
			t.Fatalf("delivery = %d %v", n, err)
		}
		if len(r.claimsOf("sub-contrib", "bounty.completed")) != 1 || len(r.claimsOf("sub-contrib", "github.pr.merged")) != 0 {
			t.Errorf("claims = %+v", r.claims("sub-contrib"))
		}

		// Merging a PR that references a bounty-tagged issue ("Fixes #12") automatically awards both
		// the PR author (bounty.completed, tier=advanced from bounty:P0) and the issue filer (github.issue.accepted).
		prFixesIssue := strings.Replace(string(mergedPR(42, 9)), `"number":7`, `"number":8,"body":"Fixes #12"`, 1)
		if n, why, err := r.s.delivery(context.Background(), "pull_request", "d2", []byte(prFixesIssue)); err != nil || n != 2 {
			t.Fatalf("merged PR fixing bounty issue = %d %q %v", n, why, err)
		}
		if len(r.claimsOf("sub-contrib", "bounty.completed")) != 2 {
			t.Errorf("sub-contrib bounty claims = %+v", r.claimsOf("sub-contrib", "bounty.completed"))
		}
		if len(r.claimsOf("sub-reporter", "github.issue.accepted")) != 1 {
			t.Errorf("sub-reporter issue claims = %+v", r.claimsOf("sub-reporter", "github.issue.accepted"))
		}
	})
}

func TestAGitHubThatCannotBeReadSendsTheClaimToReviewWithNoChecks(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		r := newRig(t)
		g := &github{busy: true}
		r.github(g, map[int64]string{42: "sub-contrib"})
		if n, _, err := r.s.delivery(context.Background(), "pull_request", "d", mergedPR(42, 9)); err != nil || n != 1 {
			t.Fatalf("delivery = %d %v", n, err)
		}
		cs := r.claimsOf("sub-contrib", "github.pr.merged")
		if len(cs) != 1 || cs[0].Evidence == nil || !cs[0].Evidence.NeedsReview || len(cs[0].Evidence.Checks) != 0 || cs[0].Evidence.Recheck != nil {
			t.Fatalf("claim = %+v", cs)
		}

	})
}

func TestAStarAndAForkAreStatedOnlyWhenGitHubShowsThem(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		r := newRig(t)
		g := &github{answers: map[string]string{
			"/users/fan/starred?per_page=100": `[{"repo": {"full_name": "hanzoai/Commerce"}}]`,
			"/repos/fan/commerce":             `{"fork": true, "owner": {"id": 5}, "parent": {"full_name": "hanzoai/commerce"}}`,
		}}
		r.github(g, map[int64]string{5: "sub-fan"})
		star := []byte(`{"action":"created","sender":{"id":5,"login":"fan"},"repository":{"full_name":"hanzoai/commerce","private":false}}`)
		fork := []byte(`{"forkee":{"full_name":"fan/commerce"},"sender":{"id":5,"login":"fan"},"repository":{"full_name":"hanzoai/commerce","private":false}}`)
		ctx := context.Background()
		if n, why, err := r.s.delivery(ctx, "star", "d", star); err != nil || n != 1 {
			t.Fatalf("star = %d %q %v", n, why, err)
		}
		if n, why, err := r.s.delivery(ctx, "fork", "d", fork); err != nil || n != 1 {
			t.Fatalf("fork = %d %q %v", n, why, err)
		}
		if len(r.claimsOf("sub-fan", "github.star")) != 1 || len(r.claimsOf("sub-fan", "github.fork")) != 1 {
			t.Errorf("claims = %+v", r.claims("sub-fan"))
		}
		// a star GitHub does not list is not stated.
		delete(g.answers, "/users/fan/starred?per_page=100")
		g.answers["/users/fan/starred?per_page=100"] = `[{"repo": {"full_name": "hanzoai/other"}}]`
		other := []byte(strings.Replace(string(star), "hanzoai/commerce", "hanzoai/gone", 1))
		if n, _, _ := r.s.delivery(ctx, "star", "d", other); n != 0 {
			t.Errorf("a star GitHub does not list claimed %d", n)
		}
		// an unstar is not a star.
		unstar := []byte(strings.Replace(string(star), "created", "deleted", 1))
		if n, why, _ := r.s.delivery(ctx, "star", "d", unstar); n != 0 || why != "not a new star" {
			t.Errorf("unstar = %d %q", n, why)
		}

	})
}

func page(s string) string {
	b, _ := json.Marshal(map[string]string{"encoding": "base64", "content": base64.StdEncoding.EncodeToString([]byte(s))})
	return string(b)
}

func TestTheBadgeVerifierWantsAPublicRepoOfTheLinkedAccountWithABadgeImage(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		r := newRig(t)
		stub(t, &linkedFn, func(context.Context) (string, error) { return "5", nil })
		owner := `"owner": {"id": 5, "login": "Fan", "type": "User"}`
		all3 := "# proj\n[![Hanzo](https://api.hanzo.ai/v1/reward/badge/fan.svg?theme=dark)](https://hanzo.ai)\n" +
			`<img alt="rank" width="120" src='https://api.hanzo.ai/v1/leaderboard/badge/acme.svg'>` + "\n" +
			"![Built with Hanzo](https://hanzo.ai/badges/built-with-hanzo-dark.svg?v=2)\n"
		g := &github{answers: map[string]string{
			"/repos/fan/proj":          `{"id": 11, "full_name": "fan/proj", "private": false, ` + owner + `}`,
			"/repos/fan/proj/readme":   page(all3),
			"/repos/fan/fan":           `{"id": 12, "full_name": "fan/fan", "private": false, ` + owner + `}`,
			"/repos/fan/fan/readme":    page("![x](https://hanzo.ai/badges/built-with-hanzo.svg)\n![again](https://hanzo.ai/badges/built-with-hanzo.svg)"),
			"/repos/fan/link":          `{"id": 13, "full_name": "fan/link", "private": false, ` + owner + `}`,
			"/repos/fan/link/readme":   page("[Built with Hanzo](https://hanzo.ai/badges/built-with-hanzo.svg) and https://api.hanzo.ai/v1/reward/badge/fan.svg and [Deploy to Hanzo](https://platform.hanzo.ai/deploy?repo=a/b)"),
			"/repos/fan/bare":          `{"id": 14, "full_name": "fan/bare", "private": false, ` + owner + `}`,
			"/repos/fan/bare/readme":   page("nothing here"),
			"/repos/org/proj":          `{"id": 15, "full_name": "org/proj", "private": false, "owner": {"id": 77, "login": "org", "type": "Organization"}}`,
			"/repos/other/proj":        `{"id": 16, "full_name": "other/proj", "private": false, "owner": {"id": 6, "login": "other", "type": "User"}}`,
			"/repos/fan/secret":        `{"id": 17, "full_name": "fan/secret", "private": true, ` + owner + `}`,
			"/repos/fan/forked":        `{"id": 18, "full_name": "fan/forked", "private": false, "fork": true, ` + owner + `}`,
			"/repos/fan/forked/readme": page("![x](https://hanzo.ai/badges/built-with-hanzo.svg)"),
		}}
		r.github(g, nil)
		ctx := context.Background()

		// three kinds in one README: three claims, each its own, with the check that proved them.
		out, err := r.s.badge(ctx, "sub-fan", "Fan/Proj")
		if err != nil || !slices.Equal(out.Badges, []string{"profile", "rank", "built"}) || out.Claimed != 3 {
			t.Fatalf("badge = %+v %v", out, err)
		}
		cs := r.claimsOf("sub-fan", "github.badge")
		kinds := map[string]bool{}
		for _, c := range cs {
			kinds[c.Slot] = true
			if c.Rule != "github-badge" || c.Evidence == nil || len(c.Evidence.Checks) != 1 || c.Evidence.Checks[0].AuthorMatch != catalog.Yes {
				t.Errorf("claim %+v", c)
			}
		}
		if len(cs) != 3 || !kinds["fan/proj+profile"] || !kinds["fan/proj+rank"] || !kinds["fan/proj+built"] {
			t.Fatalf("claims by repo+badge = %v", kinds)
		}
		// asking again claims nothing twice.
		if out, err = r.s.badge(ctx, "sub-fan", "fan/proj"); err != nil || out.Claimed != 0 {
			t.Errorf("the same project again: %+v %v", out, err)
		}
		// the same kind twice in a README is one claim; the member's own profile repository is a repository.
		if out, err = r.s.badge(ctx, "sub-fan", "fan/fan"); err != nil || !slices.Equal(out.Badges, []string{"built"}) || out.Claimed != 1 {
			t.Errorf("fan/fan = %+v %v", out, err)
		}
		// a link or a bare address that is not an image is no badge, and Deploy to Hanzo is not built yet.
		if _, err = r.s.badge(ctx, "sub-fan", "fan/link"); !errors.Is(err, errNoBadge) && err != errNoBadge {
			t.Errorf("fan/link: %v, want errNoBadge", err)
		}
		if n := len(r.claimsOf("sub-fan", "github.badge")); n != 4 {
			t.Errorf("claims = %d, want 3 + 1", n)
		}
		for repo, want := range map[string]error{
			"fan/bare": errNoBadge, "fan/forked": errNotOwner, "org/proj": errNotOwner, "other/proj": errNotOwner, "fan/secret": errNotPublic, "fan/missing": errNotPublic, "not a repo": errBadRepo,
		} {
			if _, err := r.s.badge(ctx, "sub-fan", repo); !errors.Is(err, want) && err != want {
				t.Errorf("%s: err = %v, want %v", repo, err, want)
			}
		}
		// no linked GitHub account: refused before GitHub is asked.
		stub(t, &linkedFn, func(context.Context) (string, error) { return "", nil })
		asked := len(g.asked)
		if _, err := r.s.badge(ctx, "sub-fan", "fan/proj"); err != errNoGitHub || len(g.asked) != asked {
			t.Errorf("unlinked: %v, %d new GitHub calls", err, len(g.asked)-asked)
		}

	})
}

func TestBadgeKindsAreTheImagesAREADMECarriesNotThePagesItLinks(t *testing.T) {
	for name, tc := range map[string]struct {
		readme string
		want   []string
	}{
		"markdown profile":       {"![a](https://api.hanzo.ai/v1/reward/badge/fan.svg)", []string{"profile"}},
		"query ignored":          {"![a](https://api.hanzo.ai/v1/reward/badge/fan.svg?style=card&theme=dark \"title\")", []string{"profile"}},
		"html double quotes":     {`<img src="https://api.hanzo.ai/v1/leaderboard/badge/acme.svg" alt="x">`, []string{"rank"}},
		"html uppercase":         {`<IMG SRC="https://hanzo.ai/badges/built-with-hanzo.svg">`, []string{"built"}},
		"dark built":             {"![a](https://hanzo.ai/badges/built-with-hanzo-dark.svg)", []string{"built"}},
		"www built":              {"![a](https://www.hanzo.ai/badges/built-with-hanzo.svg)", []string{"built"}},
		"a link is no badge":     {"[a](https://api.hanzo.ai/v1/reward/badge/fan.svg)", nil},
		"a bare url is no badge": {"see https://hanzo.ai/badges/built-with-hanzo.svg", nil},
		"look-alike host":        {"![a](https://api.hanzo.ai.evil.com/v1/reward/badge/fan.svg)", nil},
		"look-alike path":        {"![a](https://hanzo.ai/badges/built-with-hanzo.svg.png)", nil},
		"another host's badge":   {"![a](https://evil.com/badges/built-with-hanzo.svg)", nil},
		"deploy is not yet":      {"[![Deploy](https://platform.hanzo.ai/button.svg)](https://platform.hanzo.ai/deploy?repo=a/b)", nil},
		"all three, any order":   {"![c](https://hanzo.ai/badges/built-with-hanzo.svg) ![b](https://api.hanzo.ai/v1/leaderboard/badge/a.svg) ![a](https://api.hanzo.ai/v1/reward/badge/f.svg)", []string{"profile", "rank", "built"}},
	} {
		if got := badgeKinds(tc.readme); !slices.Equal(got, tc.want) {
			t.Errorf("%s: %v, want %v", name, got, tc.want)
		}
	}
}

func TestAMemberCheckClaimsTheirStarsFollowsAndForksOnceEach(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		r := newRig(t)
		stub(t, &linkedFn, func(context.Context) (string, error) { return "5", nil })
		g := &github{answers: map[string]string{
			"/user/5":                                         `{"id": 5, "login": "Fan"}`,
			"/users/fan/starred?per_page=100&page=1":          `[{"repo":{"full_name":"hanzoai/commerce"}},{"repo":{"full_name":"someone/else"}}]`,
			"/users/fan/following?per_page=100&page=1":        `[{"login":"hanzoai"},{"login":"torvalds"}]`,
			"/users/fan/repos?type=owner&per_page=100&page=1": `[{"full_name":"fan/commerce","fork":true},{"full_name":"fan/own","fork":false}]`,
			"/repos/fan/commerce":                             `{"fork": true, "owner": {"id": 5}, "parent": {"full_name": "hanzoai/commerce"}}`,
		}}
		r.github(g, nil)
		out, err := r.s.mine(context.Background(), "sub-fan")
		if err != nil || out.Login != "fan" || out.Stars != 1 || out.Follows != 1 || out.Forks != 1 {
			t.Fatalf("check = %+v %v", out, err)
		}
		again, err := r.s.mine(context.Background(), "sub-fan")
		if err != nil || again.Stars+again.Follows+again.Forks != 0 {
			t.Errorf("a second check claimed %+v", again)
		}
		for _, ev := range []string{"github.star", "github.follow", "github.fork"} {
			cs := r.claimsOf("sub-fan", ev)
			if len(cs) != 1 || cs[0].Evidence == nil || len(cs[0].Evidence.Checks) != 1 || cs[0].Evidence.Checks[0].Status != 200 {
				t.Errorf("%s claims = %+v", ev, cs)
			}
		}

	})
}

func TestOnlyLegalPathsReachGitHub(t *testing.T) {
	for _, bad := range []string{"/repos/a/b/../../../user", "/repos/a b/c", "//evil.com/x", "/repos/a/b?x=1;y", "/repos/%2e%2e/x", "repos/a/b"} {
		if pathOK.MatchString(bad) {
			t.Errorf("%q passed", bad)
		}
	}
	for _, good := range []string{"/repos/hanzoai/commerce/pulls/7", "/users/fan/starred?per_page=100&page=2", "/user/5"} {
		if !pathOK.MatchString(good) {
			t.Errorf("%q refused", good)
		}
	}
}
