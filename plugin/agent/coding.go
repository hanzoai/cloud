package main

// coding.go is where the coding engine gets its endpoint. One typed op:
//
//	POST /v1/agent/coding
//
// Every surface arrives through it. An HTTP client posts to it; a model calls it
// as `create_agent_coding` because the fleet's MCP server projects the same
// registration into the MCP tool list. There is no second adapter to keep in
// step, so a run cannot differ by how it was asked for.
//
// It used to have a sibling — a plane op the Slack bridge called when a message
// began with `code:`. That existed because the brain could not reach the sandbox,
// so a human had to type a magic word to route around it. The op was always a
// registered, dispatchable product op; the bridge just never offered it. Deleting
// the prefix deleted the second engine with it: two endpoints that agreed only
// by coincidence became one that cannot disagree with itself.
//
// It lives at the composition root because the engine cannot live anywhere else.
// apps/coding imports apps/agents (the routed workflow's types), so apps/agents
// can never import apps/coding, and neither can hold both halves. A main is a
// leaf: it is the one place that can. That is the same reason the six client ops
// in clients.go are declared here rather than in either app.
//
// # Why the tenant is read and never accepted
//
// Both handlers take the org from cloud.Who(ctx) — the caller's own identity, as
// the gateway or the plane established it — and CodingStartIn has no org field
// for anyone to set. A run spends the org's balance, reads the org's repos and
// pushes with the org's credential; an org that arrived as an argument would let
// any authenticated caller spend and reach any tenant it could name. This is the
// same rule commerce states at balance_rpc.go:36, kept here for the same reason.
//
// A caller with no org is refused with the principal rule's own two answers
// (principal.RefusedFrom): 401 when no credential was verified — the identity
// boundary writes an org only beside a principal it validated, so an absent,
// expired or signed-out bearer arrives here org-less whatever X-Org-Id it sent —
// and 403 when the caller is known and names no org to act in.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/hanzoai/cloud/apps/agents"
	"github.com/hanzoai/cloud/apps/coding"
	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/audit"
	"github.com/hanzoai/cloud/sdk/coord"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/zap-proto/zip"
)

// trailOf is the audit trail a parked run's tries are written to: the request path's recorder,
// which is nil where the deployment keeps none (CLOUD_AUDIT_DISABLED). A nil *audit.Recorder put
// in the interface is a trail that is not nil and panics when it is written, so no recorder is
// passed as no trail, and a parked run is not gone on with unrecorded.
func trailOf(a *audit.Recorder) coding.Trail {
	if a == nil {
		return nil
	}
	return a
}

// zipdoc lifts the doc comment off each typed op and each In/Out field into
// zipdoc_gen.go, which is the ONLY way that prose reaches the published document
// and the MCP tool list — Go drops comments at compile time.
//
// The composition root needs it as much as any app, and had it least: the ops
// declared HERE are the coding run itself, and they reached the tool list with a
// bare schema — twelve properties and not one word. An agent was told `tool` is
// a string and not that the strings are dev, claude, codex, python and node;
// told `repo` and `project` both exist with nothing to say which one names the
// code. This is the surface a chat turn uses to start a run, so a guess here is
// a run spent on the wrong thing.
//
//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

// mountAgents is the process's Use:  the agents subsystem, then the coding
// endpoint that rides in the same process. Composition, not a second plugin —
// the engine has to be here (the session store, the durable engine and the
// routed mailbox are all in this process) and giving it its own binary would put
// a socket between a run and the mailbox it is handed through.
func mountAgents(app cloud.Router, deps cloud.Deps) error {
	if err := agentsMount(app, deps); err != nil {
		return err
	}
	exposeCoding()
	codingEndpoint(app)
	agents.ResumeCoding = resumeCoding
	// A parked run that is stopped is not tried again: its park goes with it.
	agents.Stopped = coding.Released
	// The requests a run is asked for once, and what is said to a run no pod holds,
	// are kept in the database every pod shares: a retry on another pod, or after a
	// restart, is the same request (coding.Keep). A mount that cannot keep them fails,
	// since a request id honoured on some pods and not others is a double start.
	keep, err := coding.Keep(coord.Default(), routeLog, trailOf(deps.Audit))
	if err != nil {
		return err
	}
	stopKeeping = keep
	// The runs this pod flies answer the other pods' words to them (a redirect, more
	// time): a person's request lands on any pod.
	stopServing = coding.Serve(coord.Default(), routeLog)

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
			codingLog.Warn("could not sweep abandoned run keys", "err", err)
		} else if n > 0 {
			codingLog.Info("withdrew abandoned run keys", "count", n)
		}
		// AFTER the sweep, because the sweep is what resolves the credential and
		// the pin is read alongside it. Asked first, this reported the zero value
		// on every boot — "not configured", with an empty reason, whether or not
		// the secret was there — which is worse than not asking: it is the one
		// line a deploy checks to confirm the pin took effect.
		if ok, why := coding.Pinned(); ok {
			codingLog.Info("forge host key is configured")
		} else {
			codingLog.Warn("forge host key is not configured; the pin will be learned on first use", "why", why)
		}
	}()
	return nil
}

// stopServing stops answering the other pods about the runs this process flies, and
// stopKeeping closes the request ledger.
var (
	stopServing = func() {}
	stopKeeping = func() {}
)

// forgeOrg is the tenant whose forge namespace this deployment's runs live in.
//
// It is the estate's own org rather than a per-tenant loop: forge.Owner is a
// closed table with one entry, so this is the whole set, and a sweep that walked
// every IAM org would be walking orgs that have no forge at all.
const forgeOrg = "hanzo"

// shutdownAgents hands off the runs in flight, then gives back every run credential this
// process is still holding before it goes away. A rolling deploy is the ordinary way keys are abandoned,
// and this is the ordinary way they are not; coding.Sweep at the next start is
// the backstop for whatever a SIGKILL takes with it.
func shutdownAgents(ctx context.Context) error {
	// The runs this pod drives are stopped with their work kept and parked, to go on at
	// once on the next pod; what does not land in time is found by the orphan sweep.
	if n := coding.Handoff(ctx); n > 0 {
		codingLog.Info("handed off the runs in flight", "count", n)
	}
	stopServing()
	// Its OWN budget, not the caller's: a shutdown context is often already
	// cancelled by the time it reaches here, and a withdrawal that does not
	// happen leaves a live push credential rather than an untidy log line.
	give, cancel := context.WithTimeout(cloud.For(context.WithoutCancel(ctx), forgeOrg), 20*time.Second)
	defer cancel()
	coding.Drain(give)
	err := agents.Shutdown(ctx)
	stopKeeping()
	return err
}

// resumeCoding adopts an orphaned coding session whose sandbox is still alive
// after a platform rollout and continues its run in place.
func resumeCoding(ctx context.Context, sto *agents.Store, x agents.Session) bool {
	if sto == nil || x.Box == "" || x.Target != "" {
		return false
	}
	in, ok := resumeAsk(func(since int64, limit int) ([]agents.Event, error) {
		return sto.ListEvents(ctx, x.Org, x.ID, since, limit)
	}, x)
	return ok && coding.Resume(ctx, x, in, routeLog)
}

// resumeAsk is what a lost run is asked again, read off its session: the task it was
// started with and, in order, what was said to it since that no turn took (a message
// recorded as queued: the instructions a resume carried in its task). False for a run
// that ended, that was resumed twice already, or that left no task. page reads the
// session's events after since.
func resumeAsk(page func(since int64, limit int) ([]agents.Event, error), x agents.Session) (client.CodingStartIn, bool) {
	var (
		prompt  string
		said    []string
		tool    string
		model   string
		effort  string
		desktop bool
		issue   client.Issue
		resumes int
	)
	// Every event, a page at a time: the instructions said to the run while no pod held
	// it were recorded when a resume took them, which is long after its first message.
	for since := int64(0); ; {
		evs, err := page(since, 1000)
		if err != nil {
			return client.CodingStartIn{}, false
		}
		for _, e := range evs {
			since = e.Seq
			switch e.Kind {
			case agents.KindMessage:
				var m struct {
					Role   string `json:"role"`
					Text   string `json:"text"`
					Queued bool   `json:"queued"`
				}
				if json.Unmarshal([]byte(e.Payload), &m) != nil || m.Role != "user" {
					continue
				}
				switch {
				case prompt == "":
					prompt = strings.TrimSpace(m.Text)
				case m.Queued:
					said = append(said, m.Text)
				}
			case agents.KindStatus:
				var st struct {
					Status  string       `json:"status"`
					Tool    string       `json:"tool"`
					Model   string       `json:"model"`
					Effort  string       `json:"effort"`
					Desktop bool         `json:"desktop"`
					Issue   client.Issue `json:"issue"`
				}
				if json.Unmarshal([]byte(e.Payload), &st) == nil {
					switch st.Status {
					case "started":
						tool, model, effort, desktop, issue = st.Tool, st.Model, st.Effort, st.Desktop, st.Issue
					case "resumed":
						resumes++
					case "completed", "error", "stopped", "paused":
						return client.CodingStartIn{}, false
					}
				}
			}
		}
		if len(evs) < 1000 {
			break
		}
	}
	if prompt == "" || resumes >= 2 {
		return client.CodingStartIn{}, false
	}
	// What was said to the run while no pod held it rides in its task, in order: a run
	// resumed twice is given it twice, because the session recorded it.
	return client.CodingStartIn{
		Repo:     x.Repo,
		Base:     x.Base,
		Branch:   x.Branch,
		Mode:     x.Mode,
		Project:  x.Project,
		Prompt:   coding.Said(prompt, said),
		Tool:     tool,
		Model:    model,
		Effort:   effort,
		Desktop:  desktop,
		Issue:    issue,
		AgentRef: x.Agent,
	}, true
}

// codingEndpoint is the one registration of the coding op in this program. It is its
// own function so a test can stand the real endpoint up on a socket and be
// DOWNSTREAM of the registration rather than beside it — a harness that rebuilt
// the route by hand would stay green through exactly the mutation that matters,
// and this op's reachability is now the only way a chat turn gets to a sandbox.
func codingEndpoint(app *zip.App) {
	g := app.Group("/v1/agent")
	g.Post("/coding", startCoding,
		zip.WithStatus(http.StatusAccepted),
		zip.WithSummary("Start one autonomous coding run against a repo in the caller's org"))
	g.Post("/coding/:session/steer", steerOwnCoding, zip.WithStatus(http.StatusAccepted))
	// What a run did, read from the forge it pushed to: its change, and its files.
	g.Get("/coding/:session/changes", codingChanges)
	g.Get("/coding/:session/tree", codingTree)
	g.Get("/coding/:session/blob", codingBlob)
	// Merge what a run proposed into its base, on the forge it pushed to.
	g.Post("/coding/:session/merge", codingMerge)
	// Open the pull request for the branch a run pushed, with no agent run.
	g.Post("/coding/:session/pull", codingPull)
	// More time for a run that is working, so it is not ended for time at the
	// moment it was asked to be.
	g.Post("/coding/:session/extend", extendOwnCoding)
	// What a run left, kept after its sandbox is gone (apps/coding artifact.go).
	// Raw for a file: it answers the file's own bytes. Its name is `+`, a wildcard
	// that needs at least one character: `*` also matches nothing, so it took the
	// list's own address and answered it "no artifact by that name".
	g.Get("/coding/:session/artifacts", codingArtifacts)
	g.Raw(http.MethodGet, "/coding/:session/artifacts/+", codingArtifact)
	// The ask server a run's harness is handed (ask.go). Raw: it speaks MCP, and a
	// waiting call answers as an event stream.
	g.Raw(http.MethodPost, "/ask", askServer)
	// The run's MCP address for each of its org's servers (relay.go). Raw for the
	// same reason: one address answering JSON-RPC, its body decided by the method.
	g.Raw(http.MethodPost, "/mcp/:server", relayServer)
	cloud.Plane().Post("/agents/coding/steer", steerCoding,
		zip.WithOperationID(client.AgentsCodingSteer),
		zip.WithSummary("Give a coding run a new instruction, or continue a paused one, as its person"))
	// The SAME handler on the plane, for a surface that is not a request: an issue
	// handed to the agent from the board, or labelled for it on GitHub. One
	// handler, so a run cannot differ by the door it came through.
	cloud.Plane().Post("/agents/coding/start", startCoding,
		zip.WithOperationID(client.AgentsCodingStart),
		zip.WithSummary("Start one coding run as the caller"))
	// A project's site built from its repository in a sandbox, for the project
	// app (a template just taken) and the platform (the builder's Publish). The
	// sandbox runner is in this process.
	cloud.Plane().Post("/agents/coding/publish", publishCoding,
		zip.WithOperationID(client.AgentsCodingPublish),
		zip.WithSummary("Build one of the caller's projects from its repository in a sandbox and publish it"))
}

// publishCoding builds one of the caller's projects from its repository in a
// sandbox and puts it on the project's site. The org is the caller's plane
// identity, never an argument; the project, its repository and the org's funds
// are checked by the project app before anything is leased.
func publishCoding(ctx context.Context, in *client.PublishIn) (*client.Publishing, error) {
	return coding.Publish(ctx, strings.TrimSpace(cloud.Who(ctx).Org), *in, routeLog)
}

type codingInstruction struct {
	// Session identifies the running or paused coding session from the path.
	Session string `json:"session"`
	// Text is the new instruction to carry into the running turn or follow-up run.
	Text string `json:"text"`
	// RequestID makes a retry of this instruction the same instruction: said once to
	// the run, whichever pod takes the retry and whatever restarts between. The
	// Idempotency-Key header is the same value, and wins when both are sent.
	// Optional.
	RequestID string `json:"requestId,omitempty" header:"Idempotency-Key"`
}

func (in *codingInstruction) UnmarshalJSON(b []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	pick := func(keys ...string) string {
		for _, k := range keys {
			if v, ok := raw[k]; ok {
				var s string
				if json.Unmarshal(v, &s) == nil && strings.TrimSpace(s) != "" {
					return s
				}
			}
		}
		return ""
	}
	if s := pick("session", "sessionId", "session_id", "id"); s != "" {
		in.Session = s
	}
	if t := pick("text", "message", "prompt", "instruction", "input", "content"); t != "" {
		in.Text = t
	}
	if r := pick("requestId", "request_id"); r != "" {
		in.RequestID = r
	}
	return nil
}

// Redirect your running or paused coding session. On an active dev turn the
// instruction is queued into the running turn in place; otherwise the current
// run keeps its work before starting a follow-up. Ownership comes from the
// authenticated credential, never the request body.
//
// A run that no pod holds at that moment, between the halves of a rolling restart
// or while it is resumed, keeps the instruction in its inbox and takes it, once and
// in order, when its owner is up; GET /v1/agent/coding/{session}/changes lists these
// as `steers`, queued or delivered, and the answer says `queued` while the run has
// not taken the instruction. A run that has ended refuses it with 409. `requestId`,
// or the Idempotency-Key header, makes a retry the same instruction: said once,
// whichever pod takes the retry and whatever restarts between. The same id with
// another instruction is 422.
func steerOwnCoding(ctx context.Context, in *codingInstruction) (*client.CodingAck, error) {
	org := strings.TrimSpace(cloud.Who(ctx).Org)
	if org == "" {
		return nil, principal.RefusedFrom(ctx)
	}
	subject, keyed := principal.SubjectFrom(ctx)
	if !keyed {
		return nil, zip.ErrForbidden("coding: this credential names no subject to steer a run")
	}
	if strings.TrimSpace(in.Session) == "" || strings.TrimSpace(in.Text) == "" {
		return nil, zip.ErrBadRequest("coding: session and text are required")
	}
	return steerCoding(ctx, &client.CodingSteerIn{SessionID: in.Session, Subject: subject, Text: in.Text, RequestID: in.RequestID})
}

// steerCoding is how a chat surface redirects a coding run: what the run's person
// says in its thread while it works, and Continue on a paused run's message.
//
// The org is the caller's plane identity and never an argument. The subject is
// the person the surface resolved from the platform's signed identity, and the run
// has to be theirs (coding.Follow). A run is one process that cannot be told
// anything mid-flight, so it is stopped with its work kept on its branch and a
// follow-up run starts from there.
func steerCoding(ctx context.Context, in *client.CodingSteerIn) (*client.CodingAck, error) {
	org := strings.TrimSpace(cloud.Who(ctx).Org)
	if org == "" {
		return nil, principal.RefusedFrom(ctx)
	}
	subject := strings.TrimSpace(in.Subject)
	if subject == "" {
		return nil, zip.ErrForbidden("coding: steering a run needs the person it is for")
	}
	ack, err := coding.Follow(ctx, org, subject, *in, routeLog)
	if err != nil {
		return nil, err
	}
	return &ack, nil
}

// Runs a coding task on a repository: clones it into a sandbox, lets a model read
// and edit the code, run the tests, and push the work to a branch. Say the thing
// you want done — "fix the failing auth test in hanzoai/cloud" — and the run
// infers the repo, the branch and the plan. No prefix, no ceremony.
//
// Name no repo and the run works in an empty workspace of its own: nothing is
// cloned and nothing is pushed, and what it makes is kept in its sandbox and as
// its artifacts. Name a repository the caller can read and not push to — a public
// one — and it is cloned read-only, the same way.
//
// It answers 202 with the run's handle the moment the run is ADMITTED — not when
// it finishes. A coding run takes minutes; holding a request open for one would
// tie a connection to a model loop and give the caller nothing it cannot get
// better from the session stream.
//
// The handle is a session id, and that is deliberate: the session is already the
// run's durable record and its live stream (/v1/agent/sessions/stream?root=<id>), so
// this op does not grow a progress endpoint, a status endpoint or a cancel
// endpoint of its own. One way to watch a run, whoever started it.
//
// It is also how work CONTINUES. Pass an earlier run's session as `after` and
// this one starts from where that one stopped, so "now add tests for it" builds
// on the branch already pushed instead of a fresh clone. The follow-up has its
// own session and writes the same branch, so one piece of work is one branch and
// one pull request however many instructions it takes.
//
// Pass `branch` to name the branch the run writes (`agent/<name>`), and `pull`
// as false to push it without opening a pull request.
//
// Pass `requestId`, or the Idempotency-Key header, to make a retry the same
// request: one run, whichever pod takes the retry and whatever restarts between,
// and the retry answers the first's handle. The same id with another request is
// 422, and an id is at most 256 bytes.
func startCoding(ctx context.Context, in *client.CodingStartIn) (*client.CodingStarted, error) {
	org := strings.TrimSpace(cloud.Who(ctx).Org)
	if org == "" {
		// Anonymous is refused, never defaulted. A run with no tenant has no
		// balance to spend, no repos to reach and nobody to attribute — it must
		// fail here rather than land on somebody's account.
		return nil, principal.RefusedFrom(ctx)
	}
	// The actor is read HERE, from the validated principal — never from the input.
	// The thing filling in a tool's arguments is a model, and this value is the
	// session actor and the PR assignee; a model must not be able to state it.
	// It is read in the handler rather than in middleware because zip dispatches
	// an MCP tools/call and a call-plane op straight into the op with no route
	// middleware, so a middleware guard would cover one entry point in three.
	//
	// It is the subject the identity boundary attested no two people share
	// (principal.SubjectFrom), because the run's sandbox is held by it: a person
	// whose credential names none — a token with no `sub`, whose user id is a
	// username or a display name somebody else can present — starts no run.
	subject, keyed := principal.SubjectFrom(ctx)
	if !keyed {
		return nil, zip.ErrForbidden("coding: a run is held by the person it is for, and this credential names no subject to hold it by")
	}
	acc, err := coding.Start(ctx, org, subject, *in, routeLog)
	if err != nil {
		return nil, codingRefusal(err)
	}
	return started(acc), nil
}

// Returns what a coding run changed, read from the forge it pushed its branch
// to: the commits on its branch that the base does not have, newest first; the
// net change of the branch against its base, one entry per file with that file's
// patch; and its pull request with the reviews it has had, or null while it has
// none. `steers` lists the instructions said to the run while no pod held it, each
// queued until the run took it and delivered after.
//
// A run whose branch is not on the forge yet — still working, or finished with
// nothing to change — answers with no commits, no files and no pull request.
// Every read is made as the caller, so a repository they cannot open on the
// forge is not found here either, whoever can see the run.
//
// One answer is bounded, and says where it was cut rather than failing: the
// newest 250 commits (`moreCommits` when there are more), the change up to 8 MiB
// of diff or 3000 files (`moreFiles`, the last file marked truncated), and the
// first 50 reviews (`moreReviews`), each body up to 16 KiB and 256 KiB across
// them (`truncated` on a cut one). A caller has at most two of these reads in
// flight and is answered 429 past that; two asking for the same change at once
// share one read.
//
// Example: {"session": "sess_0123456789abcdef0123456789abcdef"}
//
//	Response: {"repo": "hanzoai/cloud", "base": "main", "head": "agent/456789abcdef",
//		"commits": [{"sha": "2f1c9e0a7b3d4c5e6f708192a3b4c5d6e7f80912",
//		"message": "Fix the port", "author": "hanzo-agent", "date": "2026-09-27T08:00:00Z"}],
//		"files": [{"path": "api/server.go", "status": "modified", "additions": 1,
//		"deletions": 1, "patch": "@@ -1 +1 @@\n-var port = 80\n+var port = 8080\n"}],
//		"pull": null}
func codingChanges(ctx context.Context, in *client.CodingRunIn) (*client.CodingChanges, error) {
	return coding.Changes(ctx, in.Session)
}

// Lists one directory of a coding run's repository, one level down with
// directories first: at the run's own branch once the forge holds it, and at the
// branch it started from until then — `ref` says which. Walk down a level at a
// time; an empty path is the root.
//
// Example: {"session": "sess_0123456789abcdef0123456789abcdef", "path": "api"}
//
//	Response: {"ref": "agent/456789abcdef", "entries": [{"name": "server.go",
//		"path": "api/server.go", "type": "blob", "size": 1042}]}
func codingTree(ctx context.Context, in *client.CodingPathIn) (*client.CodingTree, error) {
	return coding.Tree(ctx, in.Session, in.Path)
}

// Returns one file of a coding run's repository, read where the tree is read,
// in the shape GET /v1/git/repos/{name}/blob answers: text verbatim, anything
// else base64, and a file past the 1 MiB view cap marked truncated with no
// content.
//
// Example: {"session": "sess_0123456789abcdef0123456789abcdef", "path": "api/server.go"}
//
//	Response: {"path": "api/server.go", "size": 29, "encoding": "utf8",
//		"content": "package api\n\nvar port = 8080\n", "binary": false, "truncated": false}
func codingBlob(ctx context.Context, in *client.CodingPathIn) (*client.CodingBlob, error) {
	return coding.Blob(ctx, in.Session, in.Path)
}

// Merges a coding run's pull request into the branch it proposes into, on the
// forge the run pushed to, and answers the pull request after.
//
// The caller must be able to read the run: its person, or an admin of its org.
// What lands is the run's branch at the commit the forge holds when this asks —
// a push after that is refused, not merged unseen — and nothing is forced: a
// pull request that conflicts, was closed, or that a rule on its base keeps
// from merging is 409 with the forge's reason. One already merged answers as
// it is. `requestId`, or the Idempotency-Key header, makes a retry the same
// request, answered as the first was; the same id with another request is 422.
//
// Example: {"session": "sess_0123456789abcdef0123456789abcdef"}
//
//	Response: {"number": 7, "url": "https://git.hanzo.ai/acme/site/pulls/7",
//		"state": "merged", "base": "main"}
func codingMerge(ctx context.Context, in *client.CodingMergeIn) (*client.CodingMerged, error) {
	return coding.Merge(ctx, in)
}

// Opens the pull request for the branch a coding run pushed, as the run's person
// and with no agent run, and answers its number and address.
//
// A run pushes its branch and stops; the pull request is the step after, taken
// when the work is ready. A run that already has an open pull request, or a
// branch that has one on its host, answers with that one, so asking twice is
// safe. Title, body, base and draft are optional: the title and body come from
// the run's task, the base is the branch the run started from, and draft opens it
// as one a reviewer cannot merge yet. The run's branch must be on its host: a run
// that pushed nothing, or has not pushed yet, is 409. Who may is who may read the
// run, and the pull request is read back at GET /v1/agent/coding/{session}/changes.
// `requestId`, or the Idempotency-Key header, makes a retry the same request,
// answered as the first was; the same id with another request is 422.
//
// Example: {"session": "sess_0123456789abcdef0123456789abcdef", "title": "Fix the footer"}
//
//	Response: {"number": 7, "url": "https://github.com/acme/site/pull/7", "base": "main"}
func codingPull(ctx context.Context, in *client.CodingPullIn) (*client.CodingPulled, error) {
	return coding.Pull(ctx, in)
}

// Gives your running coding run more time. A run is held to the time it was asked
// for (`timeoutSeconds`) and, when it is up, keeps its work on its branch and
// ends; this moves that time later while the run works, so a task expected to
// take five minutes that is nearly done is given two more rather than restarted.
// `seconds` is added to the time the run has left, as far as the longest a run
// may be given; the answer says when the run is up now, so asking for more than
// that reads what you got.
//
// The run must be yours and still working: a run that is paused or already out
// of time is a conflict (start a new one with `after` set to its session), and
// one that is not running is not found. A run sent to a machine of your own
// keeps that machine's time and cannot be extended.
//
// `requestId`, or the Idempotency-Key header, makes a retry the same request: the
// time is added once, and the retry answers what the first answered. The same id
// with another request is 422.
//
// Example: {"session": "sess_0123456789abcdef0123456789abcdef", "seconds": 300}
//
//	Response: {"until": "2026-11-02T12:35:00Z", "seconds": 480}
func extendOwnCoding(ctx context.Context, in *client.CodingExtendIn) (*client.CodingExtended, error) {
	org := strings.TrimSpace(cloud.Who(ctx).Org)
	if org == "" {
		return nil, principal.RefusedFrom(ctx)
	}
	subject, keyed := principal.SubjectFrom(ctx)
	if !keyed {
		return nil, zip.ErrForbidden("coding: this credential names no subject to extend a run for")
	}
	return coding.Extend(ctx, org, subject, in)
}

// Lists what a coding run left, kept after its sandbox is gone: every file it
// added or changed and its whole change as `changes.patch`, stored beside the run;
// the ports it served, each a preview while its sandbox is kept; its pull request
// and where its work was published. A run that is still working, or saved
// nothing, answers an empty list.
//
// Example: {"session": "sess_0123456789abcdef0123456789abcdef"}
//
//	Response: {"session": "sess_0123456789abcdef0123456789abcdef", "saved": "2026-09-29T18:00:00Z",
//		"artifacts": [{"name": "changes.patch", "kind": "patch", "size": 812, "sha256": "…"},
//		{"name": "src/app.ts", "kind": "file", "size": 1042, "sha256": "…"},
//		{"name": "port 5173", "kind": "preview", "port": 5173, "url": "https://sandbox-…-preview-5173.hanzo.app/",
//		"sandbox": "m_0123456789abcdef01234567"}]}
func codingArtifacts(ctx context.Context, in *client.CodingRunIn) (*client.CodingArtifacts, error) {
	return coding.Artifacts(ctx, in.Session)
}

// codingArtifact answers one stored artifact's bytes: text as text/plain, else
// application/octet-stream, never a type a browser runs, and as an attachment.
func codingArtifact(c *zip.Ctx) error {
	name := strings.TrimPrefix(c.Param("+"), "/")
	r, n, err := coding.Artifact(c.Forward(), c.Param("session"), name)
	if err != nil {
		return err
	}
	head := make([]byte, 512)
	k, _ := io.ReadFull(r, head)
	c.SetHeader("Content-Type", coding.ArtifactType(name, head[:k]))
	c.SetHeader("Content-Disposition", "attachment; filename="+coding.Disposition(name))
	c.SetHeader("X-Content-Type-Options", "nosniff")
	c.SetHeader("Content-Security-Policy", "sandbox; default-src 'none'")
	c.SetHeader("Cache-Control", "private, no-store")
	c.Fiber().Response().SetBodyStream(io.MultiReader(bytes.NewReader(head[:k]), r), int(n))
	return nil
}

// started is the wire's copy of an admitted run, every field of it.
func started(acc coding.Accepted) *client.CodingStarted {
	return &client.CodingStarted{
		SessionID: acc.SessionID, Branch: acc.Branch, Repo: acc.Repo,
		Routed: acc.Routed, TargetID: acc.TargetID,
		Pull:    "hanzo pull " + acc.SessionID,
		Carried: acc.Carried, Left: acc.Left, Dropped: acc.Dropped, Note: acc.Note,
	}
}

// codingRefusal gives a refusal the status its cause deserves, so a caller can
// tell "try again in a minute" from "you may not" from "you asked for something
// impossible". Capacity is retriable, and so are the caller's settings when they
// could not be read and the org's code workspace when it could not be read or
// made, each a 503 because nothing asked for was wrong; a
// repository GitHub would not admit the caller to is a 403, because the request
// was well formed and the answer is about who is asking.
func codingRefusal(err error) error {
	if said, ok := err.(*zip.HTTPError); ok {
		// A refusal that states its own status keeps it: who the person is could
		// not be read right now (503), or the credential names nobody (403).
		return said
	}
	var ne net.Error
	switch {
	case errors.Is(err, coding.ErrNotAdmitted):
		return zip.ErrForbidden(err.Error())
	case errors.Is(err, coding.ErrSettings), errors.Is(err, coding.ErrWorkspace), errors.Is(err, coding.ErrDraining),
		errors.Is(err, context.DeadlineExceeded), errors.As(err, &ne):
		return zip.Errorf(http.StatusServiceUnavailable, "%s", err.Error())
	}
	return zip.ErrBadRequest(err.Error())
}

// The plane op is gone with the bridge it served. Its only caller was the Slack
// `code:` prefix, which existed because the brain could not reach the sandbox —
// and the sandbox has been a typed product op, registered and dispatchable, the
// whole time. One entry point, chosen by the model, reached from every surface.
