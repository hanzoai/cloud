package coding

// hour_test.go — a run is bounded by what it runs on: the ceiling a session may live (a day), the lease
// for a sandbox that is not carried, the sandbox's own ceiling from its creation, and, for a harness that
// cannot renew its model credential, the four hours IAM grants it. A GitHub token is renewed when it is
// used and is no bound. Each bound ends the run with its work kept; none of them cuts it off.

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/client"
	"github.com/zap-proto/zip"
)

// longest is the longest any command was given, in seconds.
func longest(p *pod) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	most := 0
	for _, s := range p.timeouts {
		most = max(most, s)
	}
	return most
}

// runFor runs one request against a pod whose lease answers as it is told to and
// answers the longest any command was given.
func runFor(t *testing.T, p *pod, asked time.Duration) int {
	t.Helper()
	servePod(t, p)
	req := aRun()
	req.RunTimeoutSeconds = int(asked.Seconds())
	if _, err := (sandboxRunner{}).Run(context.Background(), "acme", "u_1", req, nil); err != nil {
		t.Fatalf("run: %v", err)
	}
	return longest(p)
}

// A run on a lease that says nothing of its end, or one that is carried, is given
// what it asked for, up to the most a run may take.
func TestARunOnACarriedLeaseIsGivenWhatItAsksFor(t *testing.T) {
	if got := runFor(t, &pod{}, ceiling()); got != int(ceiling().Seconds()) {
		t.Fatalf("a run that asked for %s was given %ds", ceiling(), got)
	}
	soon := &pod{expires: time.Now().Add(time.Hour).Unix(), carried: true}
	if got := runFor(t, soon, ceiling()); got != int(ceiling().Seconds()) {
		t.Fatalf("a carried lease an hour from its end bounded the run to %ds", got)
	}
}

// A lease that is not carried ends at its expiry whatever the run is doing, so the
// run ends a little short of it with its work kept — a fresh free lease's hour, and
// a resumed one's few minutes alike. It is the lease that says so: a guess about
// the wallet is wrong for the signup org, for a SuperAdmin, and for a resume.
//
// MUTATION: drop the `!leased.Carried` bound in Run and the free run is given its
// whole budget, to be cut off by the lease with nothing pushed.
func TestALeaseThatIsNotCarriedBoundsTheRunToItsOwnExpiry(t *testing.T) {
	for name, left := range map[string]time.Duration{
		"a fresh free lease":               time.Hour,
		"a resumed one with ten minutes":   10 * time.Minute,
		"a resumed one with eight minutes": 8 * time.Minute,
	} {
		t.Run(name, func(t *testing.T) {
			p := &pod{expires: time.Now().Add(left).Unix()}
			got := runFor(t, p, ceiling())
			want := int((left - leaseMargin).Seconds())
			if got > want || got < want-10 {
				t.Fatalf("a run on a lease with %s left was given %ds, want about %ds", left, got, want)
			}
		})
	}
}

// A sandbox is ended at its own ceiling — a day from when it was CREATED, however it is paid — and the run
// that works in it past that loses the work with the pod. So a run in an older sandbox ends a little short of
// the instant the lease names, with its work kept, though its lease is carried and its own budget is the day.
// A lease that names none (a desktop) holds the run to nothing.
//
// MUTATION: leave the clamp out of Run and a follow-up in a ten-hour-old sandbox is cut by the sweep with
// nothing pushed.
func TestARunInASandboxThatEndsBeforeItDoesEndsAMarginShortOfIt(t *testing.T) {
	p := &pod{expires: time.Now().Add(6 * time.Hour).Unix(), carried: true, endsAt: time.Now().Add(40 * time.Minute).Unix()}
	got := runFor(t, p, ceiling())
	want := int((40*time.Minute - leaseMargin).Seconds())
	if got > want || got < want-10 {
		t.Fatalf("a run in a sandbox with 40 minutes left was given %ds, want about %ds", got, want)
	}
	if got := runFor(t, &pod{expires: time.Now().Add(6 * time.Hour).Unix(), carried: true}, ceiling()); got != int(ceiling().Seconds()) {
		t.Fatalf("a lease that names no ceiling bounded the run to %ds", got)
	}
}

// A GitHub token lives an hour from its admission, and is renewed before remote
// operations when it nears expiry (github.go ensureToken), so a build on a
// GitHub token gets the full budget it asks for rather than being cut off at
// 50 minutes.
func TestStart_ARunOnAGitHubTokenIsHeldToTheHourItLasts(t *testing.T) {
	for name, c := range map[string]struct {
		in   client.CodingStartIn
		most int
	}{
		"a build": {client.CodingStartIn{Repo: "github.com/hanzo-apps/site", Prompt: "fix the header", Pull: true,
			TimeoutSeconds: int(ceiling().Seconds())}, int(ceiling().Seconds())},
		"a plan": {client.CodingStartIn{Repo: "github.com/hanzo-apps/site", Prompt: "fix the header", Mode: "plan",
			TimeoutSeconds: int(ceiling().Seconds())}, int(ceiling().Seconds())},
	} {
		t.Run(name, func(t *testing.T) {
			r := gitHubRun(t, map[string]string{})
			r.startMode(t, c.in)
			r.mu.Lock()
			defer r.mu.Unlock()
			if got := longest(r.pod); got != c.most {
				t.Fatalf("%s on a GitHub token was given %ds, want %ds", name, got, c.most)
			}
		})
	}
}

// A run that names no timeout may take a day (ceiling), and its credential is never asked for
// that long. A harness that takes another credential while it runs (dev) is given a short life
// and renewed (key.go); one that reads it once (claude) is given what IAM will grant, four hours,
// and held to it. Either way the run is not refused at delegation after its sandbox was leased and
// its checkout cloned, which is what a credential asked for longer than IAM grants did.
//
// MUTATION: ask IAM for the run's whole deadline for a dev run, and the delegation is for a day.
func TestAPaidRunWithNoTimeoutIsDelegatedACredentialIAMWillGrant(t *testing.T) {
	for tool, want := range map[string]int{"": int(credLife.Seconds()), "claude": 0} {
		t.Run("tool "+tool, func(t *testing.T) {
			iam := &fakeIAM{token: func(n int) string { return jwtWith("ai:inference", "run-"+strconv.Itoa(n)) }, life: 4 * 3600}
			iam.serve(t)
			p := headPod()
			servePod(t, p)

			ctx, cancel, _ := runContext("acme", 0, ceiling())
			defer cancel()
			run := aRun()
			run.Tool = tool
			run.RunTimeoutSeconds = timeoutOr(0)
			if _, err := (sandboxRunner{}).Run(ctx, "acme", "9b1d0a2e-alice", run, nil); err != nil {
				t.Fatalf("a run that named no timeout: %v", err)
			}
			if tool == "" {
				if _, _, ran := agentRan(p); !ran {
					t.Fatalf("the harness did not run:\n%s", strings.Join(p.lines(), "\n"))
				}
			}
			iam.mu.Lock()
			defer iam.mu.Unlock()
			asked, _ := strconv.Atoi(iam.asked[0].Get("lifetime"))
			switch {
			case want > 0 && (asked > want || asked < want-5):
				t.Fatalf("a dev run's delegation was asked for %ds, want about %ds", asked, want)
			case want == 0 && asked <= int((4*time.Hour).Seconds()):
				t.Fatalf("a run whose harness reads its credential once was asked for %ds, want the run's whole deadline", asked)
			}
		})
	}
}

// A run that ends for time keeps its work, pushes it, and says the time was why,
// so the dispatcher can tell its person to continue with `after`.
func TestARunEndedForTimeSaysSoAndKeepsItsWork(t *testing.T) {
	ctx, cancel := context.WithCancelCause(context.Background())
	tip := theSHA
	p := &pod{}
	p.answer = func(argv []string) client.Ran {
		switch {
		case slices.Contains(argv, "--skip-git-repo-check"): // the task, out of time while it works
			cancel(errTimeUp)
			return client.Ran{ExitCode: 130}
		case slices.Contains(argv, "commit"):
			tip = newSHA
		case slices.Contains(argv, "rev-parse"):
			return client.Ran{Stdout: tip + "\n"}
		case slices.Contains(argv, "diff"):
			return client.Ran{Stdout: " 1 file changed, 3 insertions(+)\n"}
		}
		return client.Ran{}
	}
	servePod(t, p)

	res, err := sandboxRunner{}.Run(ctx, "acme", "u_1", aRun(), nil)
	if err != nil {
		t.Fatalf("a run out of time keeps its work rather than failing: %v", err)
	}
	if !res.TimeUp || !res.Changed || res.CommitSha != newSHA || !p.gitVerb("push") {
		t.Fatalf("the work was not kept and reported as out of time: %+v", res)
	}
}

// A GitHub-homed run that executes past hubRefresh (40m) renews its GitHub App
// installation token before pushing and verifying, blinds the renewed token,
// and revokes both tokens cleanly.
func TestGitHubTokenRenewsBeforeRemoteOperationsPastRefreshWindow(t *testing.T) {
	t0 := time.Now()
	now := t0
	wasNow := hubNow
	hubNow = func() time.Time { return now }
	t.Cleanup(func() { hubNow = wasNow })

	const freshToken = "ghs_fresh9876543210secret"
	r := newRig()
	calls := 0
	r.admit = func(_ zip.Caller, owner, repo string) (*client.GitHubRun, error) {
		calls++
		tok := ghToken
		if calls > 1 {
			tok = freshToken
		}
		return &client.GitHubRun{Token: tok, Owner: "hanzo-apps", Repo: "site", Base: "main", Login: "zoe-gh"}, nil
	}
	r.refs = map[string]string{}
	tip := theSHA
	r.pod.answer = func(argv []string) client.Ran {
		switch {
		case slices.Contains(argv, "--skip-git-repo-check"):
			// Advance clock past hubRefresh (40m) while the agent works (e.g. 65 minutes).
			now = t0.Add(65 * time.Minute)
			return client.Ran{Stdout: "worked with " + freshToken + "\n"}
		case slices.Contains(argv, "commit"):
			tip = newSHA
		case slices.Contains(argv, "push"):
			ref := argv[len(argv)-1]
			r.mu.Lock()
			r.refs[strings.TrimPrefix(ref, "HEAD:refs/heads/")] = newSHA
			r.mu.Unlock()
		case slices.Contains(argv, "rev-parse"):
			return client.Ran{Stdout: tip + "\n"}
		case slices.Contains(argv, "diff"):
			return client.Ran{Stdout: " 1 file changed\n"}
		}
		return client.Ran{}
	}
	r.serve(t)

	if _, err := r.startAs(t, asCaller("sub-zoe"), "sub-zoe", client.CodingStartIn{
		Repo: "github.com/hanzo-apps/site", Prompt: "long run", Pull: true, TimeoutSeconds: 7200,
	}); err != nil {
		t.Fatalf("start: %v", err)
	}
	if calls < 2 {
		t.Fatalf("admit was called %d times, want at least 2 (initial + mid-run renewal)", calls)
	}
	r.pod.mu.Lock()
	fed := slices.Clone(r.pod.fed)
	r.pod.mu.Unlock()
	if !slices.ContainsFunc(fed, func(s string) bool { return strings.Contains(s, freshToken) }) {
		t.Fatalf("git push was not fed the renewed token; fed=%q", fed)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, e := range r.sessions.events {
		if strings.Contains(e.payload, freshToken) || strings.Contains(e.payload, ghToken) {
			t.Fatalf("a token leaked into session event: %s", e.payload)
		}
	}
}
