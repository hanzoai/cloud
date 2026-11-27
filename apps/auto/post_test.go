package auto

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk"
	"github.com/zap-proto/zip"
)

// platforms stands in for the provider app's posts, keeping each post it made.
type platforms struct {
	mu     sync.Mutex
	posted map[string][]string
}

func (p *platforms) count(name string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.posted[name])
}

// standIn answers the provider app's post op for a test: the named accounts are
// connected for acme, and any other is not; the person posting is an admin.
func standIn(t *testing.T, connected ...string) *platforms {
	t.Helper()
	p := &platforms{posted: map[string][]string{}}
	links := map[string]string{"x": "https://x.com/i/status/190001", "linkedin": "https://www.linkedin.com/feed/update/urn:li:share:42", "facebook": "https://www.facebook.com/pg1_9"}
	os, oa := sendPost, adminOf
	adminOf = func(_ context.Context, org, sub string) (bool, error) {
		return org == "acme" && (strings.HasPrefix(sub, "u-") || strings.HasPrefix(sub, "admin")), nil
	}
	sendPost = func(_ context.Context, org, to, text string) (*client.SocialPostOut, error) {
		for _, c := range connected {
			if c == to && org == "acme" {
				p.mu.Lock()
				p.posted[to] = append(p.posted[to], text)
				p.mu.Unlock()
				return &client.SocialPostOut{URL: links[to]}, nil
			}
		}
		return &client.SocialPostOut{Error: targets[to].name + " is not connected: connect it in Settings → Integrations"}, nil
	}
	t.Cleanup(func() { sendPost, adminOf = os, oa })
	return p
}

// A post longer than its platform takes is not sent, and only x, linkedin and
// facebook take a text post.
func TestATargetTakesWhatItCan(t *testing.T) {
	p := standIn(t, "x")
	if got := publish(context.Background(), "acme", "x", strings.Repeat("a", 281)); !strings.Contains(got.Error, "281 characters and X takes 280") || p.count("x") != 0 {
		t.Fatalf("a long post to X = %+v", got)
	}
	if _, err := postTargets([]string{"instagram"}); err == nil {
		t.Fatal("instagram was taken as a text post target")
	}
	if got, err := postTargets([]string{"X", "linkedin", "x"}); err != nil || strings.Join(got, ",") != "linkedin,x" {
		t.Fatalf("targets = %v %v", got, err)
	}
}

// An automation that acts on its own posts what its agent wrote to each of its
// accounts, once, and its run keeps the post and where it went.
func TestAnAutomationPostsWhatItsAgentWrote(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		app := newApp(t)
		engineFor(ctx, t, "acme")
		p := standIn(t, "x", "facebook")
		dev := devPlane(t, client.TurnEvents{Next: 2, Outcome: client.TurnCompleted, Events: []client.TurnEvent{{Seq: 2, Kind: client.TurnAnswer, Text: "Shipped three things this week."}}})
		r := askAs(t, app, http.MethodPost, "/v1/auto/automations", "acme", "admin-ann", map[string]any{
			"name": "Weekly post", "instructions": "Write this week's post.", "permissions": "auto", "postTo": []string{"x", "facebook", "linkedin"},
		})
		if r.Code != http.StatusCreated {
			t.Fatalf("create = %d %s", r.Code, r.Body)
		}
		a := decode[Automation](t, r)
		if strings.Join(a.PostTo, ",") != "facebook,linkedin,x" {
			t.Fatalf("postTo = %v", a.PostTo)
		}
		run := decode[runStarted](t, askAs(t, app, http.MethodPost, "/v1/auto/automations/"+a.ID+"/run", "acme", "admin-ann", nil)).Run.ID
		got := until(t, "acme", run)
		if got.Status != RunSucceeded || !strings.Contains(got.Summary, "Posted to Facebook, X; not posted: LinkedIn is not connected") {
			t.Fatalf("run = %+v", got)
		}
		if p.count("x") != 1 || p.count("facebook") != 1 || p.posted["x"][0] != "Shipped three things this week." {
			t.Fatalf("posted = %+v", p.posted)
		}
		dev.mu.Lock()
		prompt := dev.starts[0].Prompt
		dev.mu.Unlock()
		if !strings.Contains(prompt, "social post, published as you write it to Facebook, LinkedIn, X") || !strings.Contains(prompt, "at most 280 characters") {
			t.Fatalf("prompt = %q", prompt)
		}
		runs := decode[automationRunPage](t, askAs(t, app, http.MethodGet, "/v1/auto/automations/"+a.ID+"/runs", "acme", "admin-ann", nil)).Data
		if len(runs) != 1 || runs[0].Draft != "Shipped three things this week." || len(runs[0].Posts) != 3 {
			t.Fatalf("runs = %+v", runs)
		}
		for _, x := range runs[0].Posts {
			if (x.To == "linkedin") == (x.URL != "") {
				t.Fatalf("post = %+v", x)
			}
		}
	})
}

// An automation that asks first holds its post for review: nothing is posted
// until its person posts it, and a discarded post goes nowhere.
func TestAskFirstHoldsThePostForReview(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		app := newApp(t)
		engineFor(ctx, t, "acme")
		p := standIn(t, "x")
		devPlane(t, client.TurnEvents{Next: 2, Outcome: client.TurnCompleted, Events: []client.TurnEvent{{Seq: 2, Kind: client.TurnAnswer, Text: "A post to look at first."}}})
		a := decode[Automation](t, askAs(t, app, http.MethodPost, "/v1/auto/automations", "acme", "admin-ann", map[string]any{
			"name": "Daily post", "instructions": "Write today's post.", "postTo": []string{"x"},
		}))
		held := func() string {
			t.Helper()
			run := decode[runStarted](t, askAs(t, app, http.MethodPost, "/v1/auto/automations/"+a.ID+"/run", "acme", "admin-ann", nil)).Run.ID
			deadline := time.Now().Add(20 * time.Second)
			for time.Now().Before(deadline) {
				if r, _ := mounted.State.store.GetRun(context.Background(), "acme", run); r.Status == RunPaused {
					return run
				}
				time.Sleep(50 * time.Millisecond)
			}
			t.Fatalf("run %s never waited for review", run)
			return ""
		}
		run := held()
		// Reading the run where the builder reads runs leaves it in review.
		_ = req(t, app, http.MethodGet, "/v1/auto/runs/"+run, "acme", nil)
		runs := decode[automationRunPage](t, askAs(t, app, http.MethodGet, "/v1/auto/automations/"+a.ID+"/runs", "acme", "admin-ann", nil)).Data
		if runs[0].Status != "review" || runs[0].Draft != "A post to look at first." || p.count("x") != 0 {
			t.Fatalf("held = %+v, posted %d", runs[0], p.count("x"))
		}
		if r := askAs(t, app, http.MethodPost, "/v1/auto/automations/"+a.ID+"/runs/"+run+"/review", "acme", "mallory", map[string]any{"post": true}); r.Code != http.StatusNotFound {
			t.Fatalf("a member's review = %d %s", r.Code, r.Body)
		}
		if r := askAs(t, app, http.MethodPost, "/v1/auto/automations/"+a.ID+"/runs/"+run+"/review", "acme", "admin-ann", map[string]any{"post": true}); r.Code != http.StatusOK {
			t.Fatalf("post = %d %s", r.Code, r.Body)
		}
		if got := until(t, "acme", run); got.Status != RunSucceeded || got.Summary != "Posted to X." || p.count("x") != 1 {
			t.Fatalf("posted run = %+v, posted %d", got, p.count("x"))
		}
		if r := askAs(t, app, http.MethodPost, "/v1/auto/automations/"+a.ID+"/runs/"+run+"/review", "acme", "admin-ann", map[string]any{"post": true}); r.Code != http.StatusConflict {
			t.Fatalf("a second review = %d %s", r.Code, r.Body)
		}

		run = held()
		if r := askAs(t, app, http.MethodPost, "/v1/auto/automations/"+a.ID+"/runs/"+run+"/review", "acme", "admin-ann", map[string]any{"post": false}); r.Code != http.StatusOK {
			t.Fatalf("discard = %d %s", r.Code, r.Body)
		}
		if got := until(t, "acme", run); got.Status != RunSucceeded || !strings.Contains(got.Summary, "Discarded") || p.count("x") != 1 {
			t.Fatalf("discarded run = %+v, posted %d", got, p.count("x"))
		}
	})
}

// Only the automation routes write a post step: a builder flow cannot post as
// someone's connected accounts.
func TestOnlyAnAutomationPosts(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := newApp(t)
		r := req(t, app, http.MethodPost, "/v1/auto/flows", "acme", map[string]any{
			"displayName": "p",
			"trigger": map[string]any{"name": "trigger", "type": TriggerTypeProvider, "strategy": "MANUAL",
				"settings": map[string]any{"pieceName": "core", "triggerName": "manual"},
				"nextAction": map[string]any{"name": "post", "type": ActionTypeProvider,
					"settings": map[string]any{"pieceName": "core", "actionName": "post", "input": map[string]any{"to": []string{"x"}}}}},
		})
		if r.Code != http.StatusUnprocessableEntity {
			t.Fatalf("a builder flow with a post step = %d %s", r.Code, r.Body)
		}
		if r := askAs(t, app, http.MethodPost, "/v1/auto/automations", "acme", "admin-ann", map[string]any{"name": "a", "instructions": "x", "postTo": []string{"tiktok"}}); r.Code != http.StatusBadRequest {
			t.Fatalf("postTo tiktok = %d %s", r.Code, r.Body)
		}
	})
}

// The accounts are the organization's: only an admin defines an automation that
// posts, a member cannot take one over by editing it, and a person who is no
// longer an admin posts nothing.
func TestOnlyAnAdminPostsAsTheOrganization(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		app := newApp(t)
		engineFor(ctx, t, "acme")
		p := standIn(t, "x")
		devPlane(t, client.TurnEvents{Next: 2, Outcome: client.TurnCompleted, Events: []client.TurnEvent{{Seq: 2, Kind: client.TurnAnswer, Text: "Mine now."}}})
		if r := askAs(t, app, http.MethodPost, "/v1/auto/automations", "acme", "mallory", map[string]any{"name": "a", "instructions": "x", "permissions": "auto", "postTo": []string{"x"}}); r.Code != http.StatusForbidden {
			t.Fatalf("a member's posting automation = %d %s", r.Code, r.Body)
		}
		own := decode[Automation](t, askAs(t, app, http.MethodPost, "/v1/auto/automations", "acme", "mallory", map[string]any{"name": "b", "instructions": "x"}))
		if r := askAs(t, app, http.MethodPatch, "/v1/auto/automations/"+own.ID, "acme", "mallory", map[string]any{"postTo": []string{"x"}}); r.Code != http.StatusForbidden {
			t.Fatalf("a member adding an account = %d %s", r.Code, r.Body)
		}
		a := decode[Automation](t, askAs(t, app, http.MethodPost, "/v1/auto/automations", "acme", "admin-ann", map[string]any{"name": "c", "instructions": "x", "permissions": "auto", "postTo": []string{"x"}}))
		adminOf = func(context.Context, string, string) (bool, error) { return false, nil }
		run := decode[runStarted](t, askAs(t, app, http.MethodPost, "/v1/auto/automations/"+a.ID+"/run", "acme", "admin-ann", nil)).Run.ID
		if got := until(t, "acme", run); got.Status != RunFailed || !strings.Contains(got.Summary, "not an admin") || p.count("x") != 0 {
			t.Fatalf("a demoted person's run = %+v, posted %d", got, p.count("x"))
		}
	})
}

// X counts most characters outside Latin, Greek and Cyrillic as two.
func TestXWeighsWhatItCounts(t *testing.T) {
	if weight("x", "日本語") != 6 || weight("linkedin", "日本語") != 3 || weight("x", "hello") != 5 {
		t.Fatalf("weights = %d, %d", weight("x", "日本語"), weight("linkedin", "日本語"))
	}
}

// A reviewed post is made after the review's request has gone, and still says
// whom it is for: IAM is asked about the automation's person in its org, and the
// provider app is asked to post for that org — never for the reviewer, and never
// for nobody.
func TestAReviewedPostSaysWhomItIsFor(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		app := newApp(t)
		engineFor(ctx, t, "acme")
		standIn(t, "x")
		devPlane(t, client.TurnEvents{Next: 2, Outcome: client.TurnCompleted, Events: []client.TurnEvent{{Seq: 2, Kind: client.TurnAnswer, Text: "Held."}}})
		var mu sync.Mutex
		var asked, posted []zip.Caller
		adminOf = func(ctx context.Context, org, sub string) (bool, error) {
			c := zip.CallerOf(zip.WithCaller(cloud.For(ctx, org), zip.Caller{Org: org, User: sub}))
			mu.Lock()
			asked = append(asked, c)
			mu.Unlock()
			return c.Org == "acme" && c.User == "admin-ann", nil
		}
		sendPost = func(ctx context.Context, org, to, text string) (*client.SocialPostOut, error) {
			c := zip.CallerOf(cloud.For(ctx, org))
			mu.Lock()
			posted = append(posted, c)
			mu.Unlock()
			return &client.SocialPostOut{URL: "https://x.com/i/status/7"}, nil
		}
		a := decode[Automation](t, askAs(t, app, http.MethodPost, "/v1/auto/automations", "acme", "admin-ann", map[string]any{"name": "d", "instructions": "x", "postTo": []string{"x"}}))
		run := decode[runStarted](t, askAs(t, app, http.MethodPost, "/v1/auto/automations/"+a.ID+"/run", "acme", "admin-ann", nil)).Run.ID
		deadline := time.Now().Add(20 * time.Second)
		for r, _ := mounted.State.store.GetRun(context.Background(), "acme", run); r.Status != RunPaused; r, _ = mounted.State.store.GetRun(context.Background(), "acme", run) {
			if time.Now().After(deadline) {
				t.Fatalf("never held: %+v", r)
			}
			time.Sleep(50 * time.Millisecond)
		}
		// Another admin cannot review it: the post is the person's.
		if r := askAs(t, app, http.MethodPost, "/v1/auto/automations/"+a.ID+"/runs/"+run+"/review", "acme", "admin-bob", map[string]any{"post": true}); r.Code != http.StatusNotFound {
			t.Fatalf("another admin's review = %d %s", r.Code, r.Body)
		}
		if r := askAs(t, app, http.MethodPost, "/v1/auto/automations/"+a.ID+"/runs/"+run+"/review", "acme", "admin-ann", map[string]any{"post": true}); r.Code != http.StatusOK {
			t.Fatalf("review = %d %s", r.Code, r.Body)
		}
		if got := until(t, "acme", run); got.Status != RunSucceeded || got.Summary != "Posted to X." {
			t.Fatalf("run = %+v", got)
		}
		mu.Lock()
		defer mu.Unlock()
		if len(asked) != 1 || asked[0].User != "admin-ann" || asked[0].Org != "acme" || len(posted) != 1 || posted[0].Org != "acme" {
			t.Fatalf("asked %+v, posted %+v", asked, posted)
		}
	})
}
