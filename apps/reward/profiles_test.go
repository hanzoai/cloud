package reward

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/authz"
	"github.com/hanzoai/reward/catalog"
	"github.com/hanzoai/reward/profile"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk"
)

// person makes sub a member with a profile, public or not.
func (r *rig) person(sub, handle string, public bool) {
	r.t.Helper()
	if _, err := r.s.eng.SaveProfile(context.Background(), sub, profile.Profile{Handle: handle, Public: public}); err != nil {
		r.t.Fatalf("save profile %s: %v", sub, err)
	}
}

// awarded states ev and has staff approve every claim it made.
func (r *rig) awarded(ev catalog.Event) {
	r.t.Helper()
	ctx := context.Background()
	if ev.At.IsZero() {
		ev.At = time.Now().UTC()
	}
	out, err := r.s.eng.Evaluate(ctx, ev)
	if err != nil {
		r.t.Fatalf("evaluate %s: %v", ev.ID, err)
	}
	for _, d := range out.Decisions {
		if d.Claim == nil {
			continue
		}
		if _, err := r.s.eng.Approve(ctx, d.Claim.Id(), "staff"); err != nil {
			r.t.Fatalf("approve %s: %v", d.Claim.Id(), err)
		}
	}
}

func seenOn(url string) *catalog.Evidence {
	return &catalog.Evidence{Source: "github", URL: url, Checks: []catalog.Check{{FetchedAt: time.Now().UTC(), Status: 200, Public: true, AuthorMatch: catalog.Yes}}}
}

func platformCtx() context.Context { return cloud.For(context.Background(), authz.AdminOrg) }

// A public profile is read with the records beside it; a private one, one never
// made and one unknown are the same absence.
func TestProfilesAnswerOnlyForAPublicProfile(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		r := newRig(t)
		r.directory(cloudUser{ID: "sub-ann", Owner: "acme", Name: "ann"}, cloudUser{ID: "sub-bob", Owner: "acme", Name: "bob"})
		r.person("sub-ann", "ann", true)
		r.person("sub-bob", "bob", false)
		// ann: 1.2M tokens (two badges), two merged PRs in base and one in commerce, a
		// completed bounty in base, a star that is not merged work.
		r.awarded(catalog.Event{ID: "usage.tokens:sub-ann:1", Type: "usage.tokens", Subject: "sub-ann", Fields: map[string]any{"tokens": int64(1_200_000), "model": ""}})
		for i, repo := range []string{"hanzoai/base", "hanzoai/base", "hanzoai/commerce"} {
			r.awarded(catalog.Event{ID: "pr:" + repo + string(rune('a'+i)), Type: "github.pr.merged", Subject: "sub-ann",
				Fields: map[string]any{"repo": repo, "labels": []string{}, "kind": "feature"}, Evidence: seenOn("https://github.com/" + repo + "/pull/1")})
		}
		r.awarded(catalog.Event{ID: "bounty:1", Type: "bounty.completed", Subject: "sub-ann",
			Fields: map[string]any{"repo": "hanzoai/base", "issue": "9", "tier": "starter"}, Evidence: seenOn("https://github.com/hanzoai/base/pull/9")})
		r.awarded(catalog.Event{ID: "star:1", Type: "github.star", Subject: "sub-ann", Fields: map[string]any{"repo": "hanzoai/base"}, Evidence: seenOn("https://github.com/hanzoai/base")})
		// bob has the same work, behind a private profile.
		r.awarded(catalog.Event{ID: "pr:bob", Type: "github.pr.merged", Subject: "sub-bob",
			Fields: map[string]any{"repo": "hanzoai/base", "labels": []string{}, "kind": "feature"}, Evidence: seenOn("https://github.com/hanzoai/base/pull/2")})
		// A claim that was reversed is not merged work.
		r.awarded(catalog.Event{ID: "pr:undone", Type: "github.pr.merged", Subject: "sub-ann",
			Fields: map[string]any{"repo": "hanzoai/bot", "labels": []string{}, "kind": "feature"}, Evidence: seenOn("https://github.com/hanzoai/bot/pull/3")})
		for _, c := range r.claimsOf("sub-ann", "github.pr.merged") {
			if repo, _ := c.Fields["repo"].(string); repo == "hanzoai/bot" {
				if _, err := r.s.eng.Reverse(context.Background(), c.Id(), "staff", "undone"); err != nil {
					t.Fatal(err)
				}
			}
		}
		wh := r.warehouse(map[string][]map[string]any{"extract(lower(model)": {
			{"user_id": "acme/ann", "family": "zen", "requests": uint64(12), "tokens": uint64(480_000)},
			{"user_id": "acme/ann", "family": "kai", "requests": uint64(3), "tokens": uint64(9_000)},
			{"user_id": "acme/bob", "family": "zen", "requests": uint64(1), "tokens": uint64(1)},
		}})

		out, err := r.s.profiles(platformCtx(), &client.RewardPeopleIn{
			Subjects: []string{"sub-ann", "sub-bob", "sub-ghost", "sub-ann", " "}, Families: []string{"kai", "enso", "zen"}})
		if err != nil {
			t.Fatal(err)
		}
		if len(out.People) != 1 || out.People[0].Subject != "sub-ann" {
			t.Fatalf("people = %+v, want only the public profile", out.People)
		}
		p := out.People[0]
		// 10 + 20 for the milestones, 50 for each of three PRs, 75 for the bounty, 1 for the star.
		if p.Points != 256 || p.LevelName != "Photon 5" {
			t.Fatalf("standing = level %d %q points %d, want Photon 5 with 256 (level n needs 10*n*n points)", p.Level, p.LevelName, p.Points)
		}
		for _, id := range []string{"first-100k", "million-token-club", "first-merge", "bounty-hunter"} {
			if !slices.Contains(p.Held, id) {
				t.Errorf("held %v lacks %s", p.Held, id)
			}
		}
		if len(p.Badges) != 3 {
			t.Errorf("top badges = %+v, want three", p.Badges)
		}
		if len(p.Pillars) != 5 {
			t.Errorf("pillars = %v, want the five", p.Pillars)
		}
		want := []client.RewardContribution{
			{Repo: "hanzoai/base", Event: "bounty.completed", Count: 1},
			{Repo: "hanzoai/base", Event: "github.pr.merged", Count: 2},
			{Repo: "hanzoai/commerce", Event: "github.pr.merged", Count: 1},
		}
		if !slices.Equal(p.Contributions, want) {
			t.Errorf("contributions = %+v, want %+v (a star and a reversed claim are not merged work)", p.Contributions, want)
		}
		if len(p.Usage) != 2 {
			t.Fatalf("usage = %+v, want zen and kai for ann alone", p.Usage)
		}
		byFamily := map[string]client.RewardUsage{}
		for _, u := range p.Usage {
			byFamily[u.Family] = u
		}
		if byFamily["zen"].Tokens != 480_000 || byFamily["zen"].Requests != 12 || byFamily["kai"].Tokens != 9_000 {
			t.Errorf("usage = %+v", p.Usage)
		}
		// Nothing of the private profile, the subject's email or org leaves.
		b, _ := json.Marshal(out)
		for _, leak := range []string{"sub-bob", "bob", "acme", "@"} {
			if strings.Contains(string(b), leak) {
				t.Errorf("the answer carries %q: %s", leak, b)
			}
		}
		// The usage ask is one query for every family, bound and never interpolated.
		asked := wh.asked
		if len(asked) != 1 || strings.Contains(asked[0], "sub-ann") || strings.Contains(asked[0], "acme/ann") {
			t.Errorf("warehouse asked %v", asked)
		}

	})
}

func TestProfilesAreForThePlatformAndBounded(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		r := newRig(t)
		if _, err := r.s.profiles(cloud.For(context.Background(), "acme"), &client.RewardPeopleIn{Subjects: []string{"x"}}); err == nil {
			t.Fatal("a tenant read people")
		}
		if _, err := r.s.profiles(context.Background(), &client.RewardPeopleIn{}); err == nil {
			t.Fatal("a caller with no org read people")
		}
		for name, in := range map[string]*client.RewardPeopleIn{
			"nil":          nil,
			"too many":     {Subjects: make([]string, maxAsked+1)},
			"many kinds":   {Families: []string{"a1", "b", "c", "d", "e", "f", "g", "h", "i"}},
			"a bad family": {Families: []string{"zen|.*"}},
			"a path":       {Families: []string{"moonshotai/kimi"}},
		} {
			if _, err := r.s.profiles(platformCtx(), in); err == nil {
				t.Errorf("%s: no refusal", name)
			}
		}

	})
}

// With the warehouse down the profile still answers, with no usage.
func TestProfilesAnswerWithoutUsageWhenTheWarehouseCannotBeRead(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		r := newRig(t)
		r.directory(cloudUser{ID: "sub-ann", Owner: "acme", Name: "ann"})
		r.person("sub-ann", "ann", true)
		stub(t, &readyFn, func() bool { return false })
		out, err := r.s.profiles(platformCtx(), &client.RewardPeopleIn{Subjects: []string{"sub-ann"}, Families: []string{"zen"}})
		if err != nil || len(out.People) != 1 || len(out.People[0].Usage) != 0 {
			t.Fatalf("people = %+v err %v", out, err)
		}
		stub(t, &readyFn, func() bool { return true })
		stub(t, &queryFn, func(context.Context, string, ...any) ([]map[string]any, error) { return nil, context.DeadlineExceeded })
		out, err = r.s.profiles(platformCtx(), &client.RewardPeopleIn{Subjects: []string{"sub-ann"}, Families: []string{"zen"}})
		if err != nil || len(out.People) != 1 || len(out.People[0].Usage) != 0 {
			t.Fatalf("people = %+v err %v", out, err)
		}

	})
}

// The directory is read once for many asks.
func TestProfilesReadTheDirectoryOnceInAWhile(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		r := newRig(t)
		reads := 0
		stub(t, &directoryFn, func(context.Context) (*clientDirectory, error) {
			reads++
			return &clientDirectory{Users: []client.DirectoryUser{{ID: "sub-ann", Owner: "acme", Name: "ann"}}}, nil
		})
		r.person("sub-ann", "ann", true)
		r.warehouse(nil)
		for range 3 {
			if _, err := r.s.profiles(platformCtx(), &client.RewardPeopleIn{Subjects: []string{"sub-ann"}, Families: []string{"zen"}}); err != nil {
				t.Fatal(err)
			}
		}
		if reads != 1 {
			t.Fatalf("the directory was read %d times, want 1", reads)
		}

	})
}
