package platform

// review_test.go pins what a member's deploy may cost the org: a rate and a cap on the
// review branches they hold — and that those branches are deleted once they are merged,
// stale, or were never written, while nobody else's branch is touched.

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/internal/sqltest"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// memberDeploy deploys acme-gh/web as the member user of acme.
func (h *deployHarness) memberDeploy(t *testing.T, user string, body map[string]any) (int, []byte) {
	t.Helper()
	if body == nil {
		body = map[string]any{"repo": "acme-gh/web"}
	}
	return doAs(t, h.app, http.MethodPost, "/v1/platform/apps", "acme", user, body)
}

// finish marks every build Job the cluster holds as done.
func (h *deployHarness) finish(t *testing.T) {
	t.Helper()
	list, err := h.k.dyn.Resource(jobsGVR).Namespace(h.k.buildNS).List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for i := range list.Items {
		jobSucceeded(t, h.k, list.Items[i].GetName())
	}
}

// branches is every review branch the bare universe holds.
func branches(t *testing.T, bare string) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(mustGit(t, bare, "for-each-ref", "--format=%(refname:short)", "refs/heads/"+declareBranchPrefix)), "\n") {
		if line != "" {
			out[line] = true
		}
	}
	return out
}

// A MEMBER'S BUILD IS NEVER REFUSED FOR THE BUILDS RUNNING. Three members and one of them
// twice are all accepted, and each member's Job says so, which is what lets the line start an
// org's admins' builds before them; an admin's Job carries no such mark.
func TestAMembersBuildsAreNotRefusedForConcurrency(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		h := newDeployHarness(t)
		for i, user := range []string{"u-ann", "u-ben", "u-cat", "u-ann"} {
			body := map[string]any{"repo": "acme-gh/web", "name": "web-" + itoa(i)}
			if code, out := h.memberDeploy(t, user, body); code != http.StatusAccepted {
				t.Fatalf("%s: want 202, got %d %s", user, code, out)
			}
		}
		h.deploy(t, true, map[string]any{"repo": "acme-gh/web", "name": "web-adm", "mode": "branch"})
		lines, err := h.k.buildLines(context.Background(), "acme")
		if err != nil {
			t.Fatal(err)
		}
		members := 0
		for _, l := range lines {
			if l.member {
				members++
			}
		}
		if len(lines) != 5 || members != 4 {
			t.Fatalf("%d builds with %d members', want 5 with 4", len(lines), members)
		}
	})
}

// A MEMBER OPENS AT MOST memberDeploys DEPLOYS AN HOUR. Settled reviews still
// count: the bound is on how fast they deploy, not on what is still open. Other
// members and the org's admins are not bound by it.
func TestAMembersDeploysAreRateLimited(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		h := newDeployHarness(t)
		now := time.Now()
		for i := range memberDeploys {
			r := review{Branch: "deploy/acme/old/" + genID("bld"), Org: "acme", User: "u-ann", App: "old", Tag: "t"}
			if err := h.s.State.store.reserveReview(context.Background(), r, now); err != nil {
				t.Fatalf("seed %d: %v", i, err)
			}
			if err := h.s.State.store.closeReviews(context.Background(), []string{r.Branch}, now); err != nil {
				t.Fatal(err)
			}
		}
		code, body := h.memberDeploy(t, "u-ann", nil)
		if code != http.StatusTooManyRequests || !strings.Contains(string(body), "an hour") {
			t.Fatalf("the %dth deploy in an hour must be refused: %d %s", memberDeploys+1, code, body)
		}
		if n := h.jobs(t); n != 0 {
			t.Fatalf("a rate-limited deploy launched %d builds", n)
		}
		if code, body := h.memberDeploy(t, "u-ben", nil); code != http.StatusAccepted {
			t.Fatalf("another member is not bound by ann's rate: %d %s", code, body)
		}
		h.finish(t)
		h.deploy(t, true, map[string]any{"repo": "acme-gh/web", "name": "adm", "mode": "branch"})
		// The window is an hour: rows older than it do not count.
		if err := h.s.State.store.reserveReview(context.Background(),
			review{Branch: "deploy/acme/later/x", Org: "acme", User: "u-ann", App: "later", Tag: "x"}, now.Add(memberWindow+time.Minute)); err != nil {
			t.Fatalf("an hour later ann may deploy again: %v", err)
		}
	})
}

// A MEMBER HOLDS AT MOST memberReviews OPEN REVIEW BRANCHES. At the cap their
// reviews are settled before they are refused: an open one keeps them refused,
// and one merged into main gives its place back — and its branch is deleted.
func TestAMemberHoldsFiveOpenReviews(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		h := newDeployHarness(t)
		var refs []string
		for i := range memberReviews {
			code, body := h.memberDeploy(t, "u-ann", map[string]any{"repo": "acme-gh/web", "name": "web" + string(rune('a'+i))})
			out := deployed(t, code, body)
			refs = append(refs, out.Declaration.Ref)
			h.finish(t)
		}
		if got := branches(t, h.bare); len(got) != memberReviews {
			t.Fatalf("want %d review branches, got %v", memberReviews, got)
		}
		code, body := h.memberDeploy(t, "u-ann", map[string]any{"repo": "acme-gh/web", "name": "webx"})
		if code != http.StatusTooManyRequests || !strings.Contains(string(body), "open review branches") {
			t.Fatalf("a sixth open review must be refused: %d %s", code, body)
		}
		if got := branches(t, h.bare); len(got) != memberReviews {
			t.Fatalf("a refusal must delete nothing that is still open: %v", got)
		}

		// An admin merges the first review: main now declares its tag.
		first := refs[0]
		parts := strings.Split(first, "/")
		spec := declareSpec{Name: parts[2], Org: "acme", PartOf: parts[2], Repository: declareRepository(h.s, "acme", parts[2]),
			Tag: parts[3], Hosts: []string{defaultHost(h.s, parts[2])}, Port: declarePort, Replicas: 1, Automated: true, Building: true}
		if _, err := declare(h.s, context.Background(), spec, modeCommit); err != nil {
			t.Fatalf("merge: %v", err)
		}
		code, body = h.memberDeploy(t, "u-ann", map[string]any{"repo": "acme-gh/web", "name": "webx"})
		if code != http.StatusAccepted {
			t.Fatalf("a merged review gives its place back: %d %s", code, body)
		}
		got := branches(t, h.bare)
		if got[first] {
			t.Fatalf("the merged review's branch was not deleted: %v", got)
		}
		if len(got) != memberReviews {
			t.Fatalf("want the four still open plus the new one, got %v", got)
		}
	})
}

// THE SWEEP SETTLES WHAT IS DONE AND NOTHING ELSE. A merged review and a stale
// one lose their branches, a reservation whose branch never appeared is closed,
// a fresh open one and an admin's branch are left alone, and a settled review
// past the rate window is forgotten.
func TestTheSweepDeletesMergedStaleAndUnwrittenReviews(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		h := newDeployHarness(t)
		ctx := context.Background()
		deploy := func(name string) string {
			t.Helper()
			code, body := h.memberDeploy(t, "u-ann", map[string]any{"repo": "acme-gh/web", "name": name})
			out := deployed(t, code, body)
			h.finish(t)
			return out.Declaration.Ref
		}
		merge, stale, open := deploy("merged"), deploy("stale"), deploy("open")
		admin := h.deploy(t, true, map[string]any{"repo": "acme-gh/web", "name": "adminapp", "mode": "branch"}).Declaration.Ref
		h.finish(t)
		ghost := "deploy/acme/ghost/" + genID("bld")
		if err := h.s.State.store.reserveReview(ctx, review{Branch: ghost, Org: "acme", User: "u-ann", App: "ghost", Tag: "g"},
			time.Now().Add(-reviewGrace-time.Minute)); err != nil {
			t.Fatal(err)
		}

		tag := strings.Split(merge, "/")[3]
		spec := declareSpec{Name: "merged", Org: "acme", PartOf: "merged", Repository: declareRepository(h.s, "acme", "merged"),
			Tag: tag, Hosts: []string{defaultHost(h.s, "merged")}, Port: declarePort, Replicas: 1, Automated: true, Building: true}
		if _, err := declare(h.s, ctx, spec, modeCommit); err != nil {
			t.Fatalf("merge: %v", err)
		}
		if _, err := h.s.State.store.db.ExecContext(ctx, `UPDATE platform_reviews SET created_at=$1 WHERE branch=$2`,
			time.Now().Add(-reviewTTL-time.Hour).Unix(), stale); err != nil {
			t.Fatal(err)
		}

		sweepReviews(h.s, ctx, time.Now())

		got := branches(t, h.bare)
		if got[merge] || got[stale] {
			t.Fatalf("merged and stale review branches must be deleted: %v", got)
		}
		if !got[open] || !got[admin] {
			t.Fatalf("an open review and an admin's branch must stand: %v", got)
		}
		left, err := h.s.State.store.memberOpenReviews(ctx, "acme", "u-ann")
		if err != nil || len(left) != 1 || left[0].Branch != open {
			t.Fatalf("only the open review stays open: %+v %v", left, err)
		}
		// Settled rows are kept while the rate window still counts them; the stale
		// one was opened before the window and is forgotten.
		var n int
		if err := h.s.State.store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM platform_reviews`).Scan(&n); err != nil || n != 3 {
			t.Fatalf("want the merged, open and unwritten rows kept, got %d %v", n, err)
		}
		sweepReviews(h.s, ctx, time.Now().Add(reviewTTL+2*time.Hour))
		if err := h.s.State.store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM platform_reviews WHERE closed_at<>0`).Scan(&n); err != nil || n != 0 {
			t.Fatalf("settled rows past the rate window must be forgotten: %d %v", n, err)
		}
	})
}

// A MEMBER'S DEPLOY SAYS WHAT ITS BRANCH CARRIES: that it is a review, when it
// is removed, and that the public env values on it are cleartext in universe. An
// admin's deploy says nothing of the kind.
func TestAMembersDeploySaysWhatItsBranchCarries(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		h := newDeployHarness(t)
		code, body := h.memberDeploy(t, "u-ann", map[string]any{"repo": "acme-gh/web",
			"env": []map[string]any{{"name": "SITE_TITLE", "value": "Acme", "public": true}, {"name": "REGION", "value": "us", "public": true}}})
		out := deployed(t, code, body)
		for _, want := range []string{"review", "7 days", "SITE_TITLE, REGION", "plaintext", "does not unpublish"} {
			if !strings.Contains(out.Notice, want) {
				t.Fatalf("the member's notice must say %q: %q", want, out.Notice)
			}
		}
		file := mustGit(t, h.bare, "show", out.Declaration.Ref+":"+declarePath("acme", "web"))
		if !strings.Contains(file, "Acme") {
			t.Fatalf("the notice describes a value the branch does not carry:\n%s", file)
		}
		h.finish(t)
		admin := h.deploy(t, true, map[string]any{"repo": "acme-gh/web", "name": "adminapp"})
		if admin.Notice != "" {
			t.Fatalf("an admin's deploy carries no member notice: %q", admin.Notice)
		}
		var raw map[string]any
		_, b := h.post(t, true, map[string]any{"repo": "acme-gh/web", "name": "adminapp2"})
		_ = json.Unmarshal(b, &raw)
		if _, ok := raw["notice"]; ok {
			t.Fatalf("an admin's answer names a notice: %s", b)
		}
	})
}
