package cloud

// The fail policy is the single most consequential function in the lifecycle
// defense: it decides what happens to every request when the judge is out. These
// tests are its specification.
//
// Each one is mutation-proven — invert the branch it guards and exactly this test
// goes red.

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// resetScorer restores the seam after a test, so one test's installed scorer can
// never leak into another's — the package-level seam is the only shared state
// here and it is reset explicitly rather than hoped about.
func resetScorer(t *testing.T) {
	t.Helper()
	t.Cleanup(func() { SetRiskScorer(nil) })
}

func TestDecide_FailsOpenOnTheOrdinaryPath(t *testing.T) {
	resetScorer(t)
	ordinary := RiskQuery{Stage: StageUsage, Subject: RiskSubject{Kind: "session", ID: "c1"}}

	cases := []struct {
		name    string
		install RiskScorer
		refusal string
	}{
		{"no scorer installed", nil, RefusalAbsent},
		{"scorer errors", func(context.Context, string, RiskQuery) (RiskVerdict, error) {
			return RiskVerdict{}, errors.New("model unavailable")
		}, RefusalError},
		{"scorer answers with no action", func(context.Context, string, RiskQuery) (RiskVerdict, error) {
			return RiskVerdict{Score: 0.9}, nil
		}, RefusalSilent},
		{"scorer answers outside the vocabulary", func(context.Context, string, RiskQuery) (RiskVerdict, error) {
			return RiskVerdict{Action: "quarantine"}, nil
		}, RefusalUnknown},
		{"scorer panics", func(context.Context, string, RiskQuery) (RiskVerdict, error) {
			panic("nil map")
		}, RefusalError},
		{"scorer exceeds the budget", func(ctx context.Context, _ string, _ RiskQuery) (RiskVerdict, error) {
			<-ctx.Done()
			return RiskVerdict{Action: ActionBlock}, nil
		}, RefusalTimeout},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			SetRiskScorer(tc.install)
			v := Decide(context.Background(), "acme", ordinary)
			if v.Action != ActionAllow {
				t.Fatalf("ordinary path must fail OPEN: action = %q, want %q", v.Action, ActionAllow)
			}
			if v.Refusal != tc.refusal {
				t.Fatalf("refusal = %q, want %q", v.Refusal, tc.refusal)
			}
			if v.Scored() {
				t.Fatal("an unscored allow must not report itself as scored — silence is not innocence")
			}
		})
	}
}

func TestDecide_FailsClosedOnAPrivilegedGrant(t *testing.T) {
	resetScorer(t)
	grant := RiskQuery{Stage: StageUsage, Subject: RiskSubject{Kind: "session", ID: "c1"}, Privileged: true}

	cases := []struct {
		name    string
		install RiskScorer
	}{
		{"no scorer installed", nil},
		{"scorer errors", func(context.Context, string, RiskQuery) (RiskVerdict, error) {
			return RiskVerdict{}, errors.New("model unavailable")
		}},
		{"scorer answers with no action", func(context.Context, string, RiskQuery) (RiskVerdict, error) {
			return RiskVerdict{}, nil
		}},
		{"scorer panics", func(context.Context, string, RiskQuery) (RiskVerdict, error) {
			panic("nil map")
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			SetRiskScorer(tc.install)
			v := Decide(context.Background(), "acme", grant)
			if v.Action != ActionBlock {
				t.Fatalf("a privileged grant must fail CLOSED: action = %q, want %q", v.Action, ActionBlock)
			}
			if v.Refusal == "" {
				t.Fatal("a fail-closed block must name why it could not be scored")
			}
		})
	}
}

func TestDecide_PassesAScoredVerdictThrough(t *testing.T) {
	resetScorer(t)
	SetRiskScorer(func(_ context.Context, org string, q RiskQuery) (RiskVerdict, error) {
		if org != "acme" {
			t.Fatalf("the scorer was asked about org %q, not the one Decide was called for", org)
		}
		return RiskVerdict{ID: "d-1", Action: ActionBlock, Score: 0.97, Agency: AgencyBot, Cause: "peers"}, nil
	})
	v := Decide(context.Background(), "acme", RiskQuery{Stage: StageUsage})
	if !v.Scored() {
		t.Fatalf("a scored verdict must carry no refusal, got %q", v.Refusal)
	}
	if v.Action != ActionBlock || v.ID != "d-1" || v.Agency != AgencyBot {
		t.Fatalf("verdict was reshaped in transit: %+v", v)
	}
}

// A scored verdict must not be produced by the fail policy just because the
// caller marked the request privileged: the flag selects a BRANCH of the fail
// policy, it does not deny on its own.
func TestDecide_PrivilegedIsNotItselfADenial(t *testing.T) {
	resetScorer(t)
	SetRiskScorer(func(context.Context, string, RiskQuery) (RiskVerdict, error) {
		return RiskVerdict{ID: "d-2", Action: ActionAllow}, nil
	})
	v := Decide(context.Background(), "acme", RiskQuery{Stage: StageUsage, Privileged: true})
	if v.Action != ActionAllow {
		t.Fatalf("a scored allow on a privileged grant must be honoured, got %q", v.Action)
	}
}

// The budget is the gate's, not the scorer's: a scorer that ignores its context
// must not be able to hold the request path open.
func TestDecide_ReturnsInsideTheBudget(t *testing.T) {
	resetScorer(t)
	SetRiskScorer(func(context.Context, string, RiskQuery) (RiskVerdict, error) {
		time.Sleep(2 * time.Second) // deliberately ignores ctx
		return RiskVerdict{Action: ActionAllow}, nil
	})
	start := time.Now()
	v := Decide(context.Background(), "acme", RiskQuery{Stage: StageUsage})
	if elapsed := time.Since(start); elapsed > 10*RiskBudget {
		t.Fatalf("Decide took %s; the budget is %s", elapsed, RiskBudget)
	}
	if v.Refusal != RefusalTimeout {
		t.Fatalf("refusal = %q, want %q", v.Refusal, RefusalTimeout)
	}
}

func TestRiskScorerInstalled(t *testing.T) {
	resetScorer(t)
	SetRiskScorer(nil)
	if RiskScorerInstalled() {
		t.Fatal("no scorer is installed, but the seam says there is one")
	}
	SetRiskScorer(func(context.Context, string, RiskQuery) (RiskVerdict, error) {
		return RiskVerdict{Action: ActionAllow}, nil
	})
	if !RiskScorerInstalled() {
		t.Fatal("a scorer is installed, but the seam says there is not")
	}
	SetRiskScorer(nil) // a degraded scorer withdraws rather than answering badly.
	if RiskScorerInstalled() {
		t.Fatal("uninstalling must be possible — a scorer that cannot withdraw cannot degrade safely")
	}
}

func TestPrivileged(t *testing.T) {
	cases := []struct {
		method, path string
		want         bool
		why          string
	}{
		{"POST", "/v1/iam/mint-user-keys", true, "minting a credential is a grant"},
		{"POST", "/v1/iam/issue-user-token", true, "issuing a token is a grant"},
		{"POST", "/v1/iam/revoke-user-keys", true, "revocation changes who can act"},
		{"POST", "/v1/iam/signup", true, "creating an account is a grant"},
		{"POST", "/v1/iam/onboard", true, "minting a tenant is a grant"},
		{"GET", "/v1/kms/orgs/acme/secrets/db", true, "reading a secret with a stolen key IS the attack"},
		{"DELETE", "/v1/admin/orgs/acme", true, "an admin mutation changes who can do what"},
		{"GET", "/v1/admin/orgs", false, "an admin read is audited and access-controlled, not a grant"},
		{"POST", "/v1/orgs/acme/members", true, "adding a member grants standing authority"},
		{"GET", "/v1/models", false, "an ordinary read is not a grant"},
		{"POST", "/v1/ai/chat/completions", false, "inference is not a grant"},
		{"GET", "/v1/iam/whoami", false, "reading your own identity grants nothing"},

		// REGRESSION — the spelling must not decide. fiber routes case-insensitively
		// and ignores a trailing slash, so each of these reaches the SAME handler the
		// canonical spelling does; a prefix test over the raw path matched none of
		// them, and the scorer's silence then ALLOWED what it must refuse.
		{"GET", "/V1/KMS/orgs/acme/secrets/db", true, "one capital letter is not a different route"},
		{"GET", "/v1/Kms/orgs/acme/secrets/db", true, "nor is one capital letter in the middle"},
		{"POST", "/V1/IAM/MINT-USER-KEYS", true, "a shouted grant is still a grant"},
		{"GET", "/v1/kms/", true, "the subtree root is in the subtree"},
		{"GET", "/v1/kms", true, "and so is the root without its slash — one route, per StrictRouting"},
		{"DELETE", "/V1/Admin/orgs/acme", true, "an admin mutation, whatever its case"},
		{"POST", "/v1/orgs/", true, "the org subtree root"},

		// And normalization must not WIDEN the list either: a neighbouring name that
		// merely shares a prefix is a different route and must stay ordinary.
		{"GET", "/v1/kmsx/keys", false, "a prefix of a name is not the subtree"},
		{"GET", "/v1/iam/signup-preflight", false, "a longer name is a different route"},
		{"POST", "/v1/organizations/x", false, "/v1/orgs is not /v1/organizations"},
	}
	for _, tc := range cases {
		if got := Privileged(tc.method, tc.path); got != tc.want {
			t.Errorf("Privileged(%s %s) = %v, want %v — %s", tc.method, tc.path, got, tc.want, tc.why)
		}
	}
}

// RoutePath is the router's own rule, and a security comparison may use no other.
func TestRoutePath(t *testing.T) {
	cases := map[string]string{
		"/v1/kms/x":  "/v1/kms/x",
		"/V1/KMS/X":  "/v1/kms/x",
		"/v1/kms/":   "/v1/kms",
		"/v1/kms///": "/v1/kms",
		"/":          "/",
		"":           "",
		// Percent-encoding is NOT decoded, deliberately: fiber does not decode it
		// either, so this spelling routes nowhere and normalizing it here would
		// make the predicate match a route that does not exist.
		"/v1/%6bms/x": "/v1/%6bms/x",
	}
	for in, want := range cases {
		if got := RoutePath(in); got != want {
			t.Errorf("RoutePath(%q) = %q, want %q", in, got, want)
		}
	}
}

// The scorer is bounded. Every ask costs a goroutine that lives until the scorer
// returns, so a scorer that has stopped returning must stop being asked — past
// the ceiling the fail policy answers, which is the same answer a timeout gives
// and reaches it without allocating anything.
//
// The ceiling is passed in so the property is asserted deterministically rather
// than by launching MaxScorerCalls goroutines and hoping none of them belongs to
// another test. It is the SAME function the production path runs; only the size
// of the ceiling differs.
func TestDecide_IsBoundedWhenTheScorerStops(t *testing.T) {
	resetScorer(t)
	sem := slots(2)

	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	SetRiskScorer(func(context.Context, string, RiskQuery) (RiskVerdict, error) {
		<-release // never answers within the test
		return RiskVerdict{Action: ActionAllow}, nil
	})

	// Fill every slot. Each call returns at the budget; the goroutine behind it
	// stays parked, which is precisely the cost being bounded.
	var wg sync.WaitGroup
	refusals := make([]string, 2)
	for i := range refusals {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			refusals[i] = decide(sem, context.Background(), "acme", RiskQuery{Stage: StageUsage}).Refusal
		}(i)
	}
	wg.Wait()
	for i, r := range refusals {
		if r != RefusalTimeout {
			t.Fatalf("call %d refusal = %q, want %q", i, r, RefusalTimeout)
		}
	}

	// The next one is not asked at all — and the fail policy still holds both ways.
	if v := decide(sem, context.Background(), "acme", RiskQuery{Stage: StageUsage}); v.Refusal != RefusalBusy || v.Action != ActionAllow {
		t.Fatalf("past the ceiling: %+v, want allow/%s", v, RefusalBusy)
	}
	if v := decide(sem, context.Background(), "acme", RiskQuery{Stage: StageUsage, Privileged: true}); v.Refusal != RefusalBusy || v.Action != ActionBlock {
		t.Fatalf("past the ceiling on a grant: %+v, want block/%s", v, RefusalBusy)
	}
}

// The slot is released by the goroutine that HELD it, when the scorer actually
// returns — not when the budget expires. Releasing it at the timeout would let a
// stalled scorer exceed the ceiling without bound, which is the thing the ceiling
// exists to prevent.
func TestDecide_ReleasesItsSlotWhenTheScorerAnswers(t *testing.T) {
	resetScorer(t)
	sem := slots(1)

	SetRiskScorer(func(context.Context, string, RiskQuery) (RiskVerdict, error) {
		return RiskVerdict{Action: ActionAllow}, nil
	})
	for i := 0; i < 50; i++ {
		if v := decide(sem, context.Background(), "acme", RiskQuery{Stage: StageUsage}); v.Refusal != "" {
			t.Fatalf("call %d was refused %q — a returned scorer must give its slot back", i, v.Refusal)
		}
	}
}
