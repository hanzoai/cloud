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
	}
	for _, tc := range cases {
		if got := Privileged(tc.method, tc.path); got != tc.want {
			t.Errorf("Privileged(%s %s) = %v, want %v — %s", tc.method, tc.path, got, tc.want, tc.why)
		}
	}
}

// The seam must be safe to read from every request goroutine while a mount
// writes it. Run under -race, this is the whole assertion.
func TestSetRiskScorer_IsRaceFree(t *testing.T) {
	resetScorer(t)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 200; i++ {
			SetRiskScorer(func(context.Context, string, RiskQuery) (RiskVerdict, error) {
				return RiskVerdict{Action: ActionAllow}, nil
			})
			SetRiskScorer(nil)
		}
	}()
	for i := 0; i < 200; i++ {
		_ = Decide(context.Background(), "acme", RiskQuery{Stage: StageUsage})
	}
	<-done
}

// A probe is never a grant and is never screened. The kubelet is not a customer,
// and a health endpoint a risk decision can fail is not a health endpoint.
func TestProbe(t *testing.T) {
	cases := []struct {
		method, path string
		want         bool
	}{
		{"GET", "/health", true},
		{"GET", "/healthz", true},
		{"GET", "/readyz", true},
		{"GET", "/metrics", true},
		{"HEAD", "/health", true},
		{"GET", "/v1/kms/health", true},
		{"GET", "/v1/gateway/health", true},
		// A mutation is never a probe, so a route cannot be named into the
		// exemption to dodge the gate.
		{"POST", "/health", false},
		{"POST", "/v1/kms/health", false},
		{"GET", "/v1/kms/orgs/acme/secrets/health-check", false},
		{"GET", "/v1/models", false},
	}
	for _, tc := range cases {
		if got := Probe(tc.method, tc.path); got != tc.want {
			t.Errorf("Probe(%s %s) = %v, want %v", tc.method, tc.path, got, tc.want)
		}
	}
	// The KMS subtree is a grant for every method EXCEPT its probe — otherwise a
	// fail-closed deployment would answer 403 to its own kubelet.
	if Privileged("GET", "/v1/kms/health") {
		t.Fatal("a health probe must never be treated as a grant")
	}
	if !Privileged("GET", "/v1/kms/orgs/acme/secrets/db") {
		t.Fatal("reading a secret is still a grant")
	}
}
