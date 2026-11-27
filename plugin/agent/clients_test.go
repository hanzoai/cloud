package main

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk"
	"github.com/zap-proto/zip"
)

// A coding run opened through the session plane names its linked SUBJECT, not a
// composite actor. The session must still be keyed by billingActor(org, sub), or
// the actor that opened it reads its own events as "session not found" (the
// private-session rule in agents.sought). The subject is prefixed even when it
// contains a slash: a home-org key's subject hanzo/z owns sessions as
// hanzo/hanzo/z, the same value agents.readerOf forms.
func TestASessionOpensUnderTheActorItsCreatorReadsBy(t *testing.T) {
	for _, c := range []struct {
		name, org, actor, want string
	}{
		{"a linked subject", "acme", "u-1", "acme/u-1"},
		{"a home-org key's subject", "hanzo", "hanzo/z", "hanzo/hanzo/z"},
		{"a member's key from another org", "acme", "globex/z", "acme/globex/z"},
		{"an org's own work", "acme", "", ""},
	} {
		if got := sessionActor(c.org, c.actor); got != c.want {
			t.Errorf("%s: sessionActor(%q, %q) = %q, want %q", c.name, c.org, c.actor, got, c.want)
		}
	}
}

// A plane op on a session, a machine or the route queue acts in the CALLER's org.
// The argument may repeat it; one that names another org is refused before
// anything is read or written, and so is a call that states no org at all —
// whatever org the argument names.
func TestAPlaneSessionOpActsInTheCallersOrg(t *testing.T) {
	acme := cloud.For(context.Background(), "acme")
	none := context.Background()
	for _, c := range []struct {
		name string
		call func(context.Context, string) error
	}{
		{"open", func(ctx context.Context, org string) error {
			_, err := planeSessionOpen(ctx, &client.SessionOpenIn{Org: org, Agent: "hanzo"})
			return err
		}},
		{"event", func(ctx context.Context, org string) error {
			_, err := planeSessionEvent(ctx, &client.SessionEventIn{Org: org, SessionID: "sess_x", Kind: "log"})
			return err
		}},
		{"close", func(ctx context.Context, org string) error {
			_, err := planeSessionClose(ctx, &client.SessionCloseIn{Org: org, SessionID: "sess_x", Status: "done"})
			return err
		}},
		{"tag", func(ctx context.Context, org string) error {
			_, err := planeSessionTag(ctx, &client.SessionTagIn{Org: org, SessionID: "sess_x", Sandbox: "m_theirs"})
			return err
		}},
		{"resolve-target", func(ctx context.Context, org string) error {
			_, err := planeResolveTarget(ctx, &client.TargetRefIn{Org: org, Ref: "box"})
			return err
		}},
		{"target-gate", func(ctx context.Context, org string) error {
			_, err := planeTargetGate(ctx, &client.TargetGateIn{Org: org, TargetID: "t_1"})
			return err
		}},
		{"route-run", func(ctx context.Context, org string) error {
			_, err := planeRouteRun(ctx, &client.RouteRunIn{Org: org, TargetID: "t_1", SessionID: "sess_x"})
			return err
		}},
	} {
		if err := c.call(acme, "evil"); !forbidden(err) {
			t.Errorf("%s naming another org than the caller's = %v, want 403", c.name, err)
		}
		if err := c.call(none, "acme"); !forbidden(err) {
			t.Errorf("%s with no org on the call = %v, want 403", c.name, err)
		}
		// The control: the caller's own org, named or not, gets past the check to
		// whatever the op itself answers.
		for _, named := range []string{"acme", ""} {
			if err := c.call(acme, named); forbidden(err) {
				t.Errorf("%s in the caller's own org (named %q) was refused as another's: %v", c.name, named, err)
			}
		}
	}
	if org, err := callerOrg(acme, ""); err != nil || org != "acme" {
		t.Fatalf("callerOrg = %q, %v; want the caller's", org, err)
	}
}

func forbidden(err error) bool {
	var he *zip.HTTPError
	return errors.As(err, &he) && he.Status == http.StatusForbidden
}

// A routed run is attributed to the person the call VALIDATED, never to whom the
// request body names: the delivery activity states that person as its caller, so a
// forged actor would have the run's grant and completion act as someone else.
func TestARoutedRunIsRefusedAnActorTheCallerIsNot(t *testing.T) {
	alice := zip.WithCaller(context.Background(), zip.Caller{Org: "acme", User: "u-alice"})
	nobody := zip.WithCaller(context.Background(), zip.Caller{Org: "acme"})
	for _, c := range []struct {
		name  string
		ctx   context.Context
		actor string
	}{
		{"another person named in the body", alice, "u-bob"},
		{"a person named by a call that states none", nobody, "u-bob"},
	} {
		_, err := planeRouteRun(c.ctx, &client.RouteRunIn{TargetID: "t_1", SessionID: "sess_x", Actor: c.actor})
		if !forbidden(err) {
			t.Errorf("%s = %v, want 403", c.name, err)
		}
	}
	// A run on a forge repository takes its login from the forge, so one whose caller
	// the forge cannot resolve is not queued under the login its body names.
	if _, err := routedAs(alice, &client.RouteRunIn{Repo: "hanzo/app", TargetID: "t_1", SessionID: "sess_x", ForgeActor: "bob"}); err == nil {
		t.Error("a forge run queued under the login its body names")
	}
}

// The control, and what is handed to the queue: the run carries the caller's org and
// person whether the body repeats them or leaves them out, and a call that states
// nobody has no actor to carry.
func TestARoutedRunCarriesTheCallersOrgAndActor(t *testing.T) {
	alice := zip.WithCaller(context.Background(), zip.Caller{Org: "acme", User: "u-alice"})
	nobody := zip.WithCaller(context.Background(), zip.Caller{Org: "acme"})
	for _, c := range []struct {
		name, named, want string
		ctx               context.Context
	}{
		{"named and the caller's", "u-alice", "u-alice", alice},
		{"left out", "", "u-alice", alice},
		{"no one stated", "", "", nobody},
	} {
		run, err := routedAs(c.ctx, &client.RouteRunIn{Org: "", Made: true, TargetID: "t_1", SessionID: "sess_x", Actor: c.named})
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if run.Actor != c.want || run.Org != "acme" {
			t.Errorf("%s: queued for %q in %q, want %q in acme", c.name, run.Actor, run.Org, c.want)
		}
	}
}
