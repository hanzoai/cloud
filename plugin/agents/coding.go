package main

// coding.go is where the coding engine gets its two doors, and it is one file
// because they are two doors and not two engines.
//
//	POST /v1/coding      the app's door   (hanzo.app, and anything else with a token)
//	coding_start         the plane's door (the chat bridge, in another process)
//
// Both run the SAME coding.Start: same validation, same pool, same credential
// custody, same detached tenant-stated run context. Nothing about a run differs
// by how it was asked for, which is the property "one engine, two adapters" has
// to mean if it is going to mean anything. The alternative — each surface
// assembling its own Dispatcher — is what the code did before, and it produced
// two engines that agreed only by coincidence: a run started in Slack had no
// address the app could name, and a pool that bounded one door bounded nothing
// at the other.
//
// It lives at the composition root because the engine cannot live anywhere else.
// apps/coding imports apps/agents (the routed workflow's types), so apps/agents
// can never import apps/coding, and neither can hold both halves. A main is a
// leaf: it is the one place that can. That is the same reason the six seam ops
// next door are declared here rather than in either app.
//
// # Why the tenant is read and never accepted
//
// Both handlers take the org from cloud.Who(ctx) — the caller's own identity, as
// the gateway or the plane established it — and CodingStartIn has no org field
// for anyone to set. A run spends the org's balance, reads the org's repos and
// pushes with the org's credential; an org that arrived as an argument would let
// any authenticated caller spend and reach any tenant it could name. This is the
// same rule commerce states at balance_rpc.go:36, kept here for the same reason.

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/agents"
	"github.com/hanzoai/cloud/apps/coding"
	"github.com/hanzoai/cloud/plane"
	"github.com/zap-proto/zip"
)

// zipdoc lifts the doc comment off each typed op and each In/Out field into
// zipdoc_gen.go, which is the ONLY way that prose reaches the published document
// and the MCP tool list — Go drops comments at compile time.
//
// The composition root needs it as much as any app, and had it least: the ops
// declared HERE are the coding run itself, and they reached the door with a bare
// schema — twelve properties and not one word. An agent was told `tool` is a
// string and not that the strings are dev, claude, codex, python and node; told
// `repo` and `project` both exist with nothing to say which one names the code.
// This is the surface a chat turn uses to start a run, so a guess here is a run
// spent on the wrong thing.
//
//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

// mountAgents is the process's Mount: the agents subsystem, then the coding
// door that rides in the same process. Composition, not a second plugin — the
// engine has to be here (the session store, the durable engine and the routed
// mailbox are all in this process) and giving it its own binary would put a
// socket between a run and the mailbox it is handed through.
func mountAgents(app cloud.Router, deps cloud.Deps) error {
	if err := agentsMount(app, deps); err != nil {
		return err
	}
	zip.Post[plane.CodingStartIn, plane.CodingStarted](cloud.ZipApp(app), "/v1/coding", httpCodingStart,
		zip.WithOperationID("post_v1_coding"),
		zip.WithStatus(http.StatusAccepted),
		zip.WithSummary("Start one autonomous coding run against a repo in the caller's org"))

	// SAY WHETHER THE FORGE HOST KEY IS CONFIGURED, once, here.
	//
	// Without it the degradation is silent: a wrong ref or a secret nobody
	// created reads exactly like a healthy deployment until somebody intercepts
	// the first handshake a pod makes. The forge package has no logger and should
	// not grow one, so the composition root asks and says.
	//
	// AND TAKE BACK WHAT AN EARLIER PROCESS ABANDONED. A restart is what produces
	// orphaned run keys — the run in flight when the old process went away holds
	// one nobody will withdraw — so a restart is the honest moment to clear them.
	// Detached and best-effort: a forge that will not answer must not stop this
	// process from serving.
	go func() {
		ctx, cancel := context.WithTimeout(cloud.For(context.Background(), forgeOrg), 5*time.Minute)
		defer cancel()
		if n, err := coding.Sweep(ctx, forgeOrg); err != nil {
			deps.Logger.Warn("could not sweep abandoned run keys", "err", err)
		} else if n > 0 {
			deps.Logger.Info("withdrew abandoned run keys", "count", n)
		}
		// AFTER the sweep, because the sweep is what resolves the credential and
		// the pin is read alongside it. Asked first, this reported the zero value
		// on every boot — "not configured", with an empty reason, whether or not
		// the secret was there — which is worse than not asking: it is the one
		// line a deploy checks to confirm the pin took effect.
		if ok, why := coding.Pinned(); ok {
			deps.Logger.Info("forge host key is configured")
		} else {
			deps.Logger.Warn("forge host key is not configured; the pin will be learned on first use", "why", why)
		}
	}()
	return nil
}

// forgeOrg is the tenant whose forge namespace this deployment's runs live in.
//
// It is the estate's own org rather than a per-tenant loop: forge.Owner is a
// closed table with one entry, so this is the whole set, and a sweep that walked
// every IAM org would be walking orgs that have no forge at all.
const forgeOrg = "hanzo"

// shutdownAgents gives back every run credential this process is still holding
// before it goes away. A rolling deploy is the ordinary way keys are abandoned,
// and this is the ordinary way they are not; coding.Sweep at the next start is
// the backstop for whatever a SIGKILL takes with it.
func shutdownAgents(ctx context.Context) error {
	// Its OWN budget, not the caller's: a shutdown context is often already
	// cancelled by the time it reaches here, and a withdrawal that does not
	// happen leaves a live push credential rather than an untidy log line.
	give, cancel := context.WithTimeout(cloud.For(context.WithoutCancel(ctx), forgeOrg), 20*time.Second)
	defer cancel()
	coding.Drain(give)
	return agents.Shutdown(ctx)
}

// httpCodingStart is the app's door. It answers 202 with the run's handle the
// moment the run is ADMITTED — not when it finishes. A coding run takes minutes;
// holding a request open for one would tie a connection to a model loop and give
// the caller nothing it cannot get better from the session stream.
//
// The handle is a session id, and that is deliberate: the session is already the
// run's durable record and its live stream (/v1/agents/sessions/{id}/stream), so
// this door does not grow a progress endpoint, a status endpoint or a cancel
// endpoint of its own. One way to watch a run, whoever started it.
func httpCodingStart(ctx context.Context, in *plane.CodingStartIn) (*plane.CodingStarted, error) {
	return startCoding(ctx, in)
}

// planeCodingStart is the plane's door — the chat bridge's way in, from the
// process that holds the Slack adapter.
//
// The org arrives ON THE WIRE, stated by the bridge on a detached context before
// the hop. Re-stating it here would be a no-op (this op is reached over a
// request, and zip reads a stated caller only where there is none), which is the
// mistake that shipped once already and is written down at
// apps/agents/onbehalf_rpc.go:61.
func planeCodingStart(ctx context.Context, in *plane.CodingStartIn) (*plane.CodingStarted, error) {
	return startCoding(ctx, in)
}

// startCoding is the shared body. Having it here — rather than duplicated in two
// handlers that "obviously do the same thing" — is what makes the two doors
// provably identical instead of conventionally similar.
func startCoding(ctx context.Context, in *plane.CodingStartIn) (*plane.CodingStarted, error) {
	org := strings.TrimSpace(cloud.Who(ctx).Org)
	if org == "" {
		// Anonymous is refused, never defaulted. A run with no tenant has no
		// balance to spend, no repos to reach and nobody to attribute — it must
		// fail here rather than land on somebody's account.
		return nil, zip.ErrForbidden("coding: org required")
	}
	acc, err := coding.Start(ctx, org, *in, routeLog)
	if err != nil {
		return nil, codingRefusal(err)
	}
	return &plane.CodingStarted{
		SessionID: acc.SessionID, Branch: acc.Branch, Repo: acc.Repo,
		Routed: acc.Routed, TargetID: acc.TargetID,
	}, nil
}

// codingRefusal gives a refusal the status its cause deserves, so a caller can
// tell "try again in a minute" from "you asked for something impossible".
// Capacity is the only retriable one, which is why it is the only one lifted out.
func codingRefusal(err error) error {
	if err == coding.ErrBusy {
		return zip.Errorf(http.StatusTooManyRequests, "%s", err.Error())
	}
	return zip.ErrBadRequest(err.Error())
}

func init() {
	zip.Post[plane.CodingStartIn, plane.CodingStarted](cloud.Plane(), "/coding/start", planeCodingStart,
		zip.WithOperationID(plane.CodingStart),
		zip.WithSummary("Start one coding run, for a chat bridge in another process"))
}
