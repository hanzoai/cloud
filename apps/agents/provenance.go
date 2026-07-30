package agents

// provenance.go makes a build READABLE: the agent session that produced a
// project, published so a visitor can follow how template X became product Y —
// the prompt, the reasoning, the diffs, the deploy — and fork from any turn.
//
// THE ONE STORE. A build story is not a new subsystem: a session already IS an
// ordered log of turns (sessions_store.go), so "the build of project P" is just
// the sessions tagged Project=P. Two columns carry the whole feature —
// `project` (which product this session built) and `published` (its author's
// decision to show the world) — and nothing else about a session changes.
//
// THE BINDING LIVES IN GIT. A turn is tied to the commit it produced by a
// `Hanzo-Session:`/`Hanzo-Turn:` trailer ON THAT COMMIT (new work), or by a note
// under refs/notes/hanzo-provenance carrying the same two lines (history that
// already exists and must not be rewritten). Git is the authority; this package
// only ever PARSES it (ParseLinks). There is deliberately no commit⇄turn table:
// a table is a second copy of a fact git already holds, and a second copy is a
// thing that can disagree with the commits it claims to describe. Anyone can
// re-derive every link this API returns with one `git log` — see ProvenanceLogFormat.
//
// SECRETS NEVER ENTER. Every event body is scanned by the same engine the code
// -security surface uses (clients/security/detect — the in-binary port of
// hanzoai/guard's redaction concept) BEFORE it is stored, and a hit is REFUSED
// with 422 naming the rule, the line and a masked preview. Loudly, not silently:
// a redacted transcript hides that a secret was pasted at all, and the secret is
// still live in the log the agent read from. Secrets live in KMS and are
// referenced BY NAME, so a truthful transcript has nothing to redact.

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/projects"
	"github.com/hanzoai/cloud/apps/security/detect"
	"github.com/zap-proto/zip"
)

// Trailer keys. The SAME two keys are read from a commit message trailer and
// from a refs/notes/hanzo-provenance note body — a note is just trailer lines
// attached out-of-band — so one parser covers both and there is exactly one
// spelling of the binding anywhere in the system.
const (
	TrailerSession = "Hanzo-Session"
	TrailerTurn    = "Hanzo-Turn"

	// NotesRef is where a link is recorded for a commit that already exists.
	// Rewriting history to add a trailer would change every downstream sha and
	// break every URL already pointing at it; a note attaches the same fact
	// without touching the commit.
	NotesRef = "refs/notes/hanzo-provenance"

	// ProvenanceLogFormat is the EXACT `git log --format=` a client uses to emit
	// what ParseLinks reads. Published as a constant so the producer (the CLI
	// that ingests a transcript) and the verifier (anyone auditing our claims)
	// run the same command, and so the parser can never quietly drift from the
	// format it parses. Records are NUL-separated; within a record the first line
	// is the sha and the rest is the message body plus any note.
	ProvenanceLogFormat = "%x00%H%n%B%n%N"
)

// Link is one commit⇄turn binding as GIT states it. It is derived, never
// stored: ParseLinks is a pure function of `git log` output.
type Link struct {
	Commit  string `json:"commit"`
	Session string `json:"session"`
	Turn    int64  `json:"turn"`
}

// ParseLinks reads `git log --format=ProvenanceLogFormat` output and returns one
// Link per commit that carries BOTH keys. A commit with neither (ordinary work),
// with only one (a half-written trailer), or with an unparseable turn is skipped
// — a link asserts a specific turn produced a specific commit, and a partial
// record cannot assert that. Pure: no exec, no I/O, safe to call concurrently.
func ParseLinks(gitLog string) []Link {
	var out []Link
	for _, rec := range strings.Split(gitLog, "\x00") {
		rec = strings.TrimLeft(rec, "\r\n")
		if rec == "" {
			continue
		}
		nl := strings.IndexByte(rec, '\n')
		sha := rec
		body := ""
		if nl >= 0 {
			sha, body = rec[:nl], rec[nl+1:]
		}
		sha = strings.TrimSpace(sha)
		if !isHex(sha) {
			continue
		}
		sess, turn, ok := scanTrailers(body)
		if !ok {
			continue
		}
		out = append(out, Link{Commit: sha, Session: sess, Turn: turn})
	}
	return out
}

// scanTrailers pulls the two keys out of a message body (or note body). The LAST
// occurrence wins, matching git's own trailer semantics: an amended commit
// appends the corrected trailer rather than editing the earlier line.
func scanTrailers(body string) (session string, turn int64, ok bool) {
	haveTurn := false
	for _, line := range strings.Split(body, "\n") {
		k, v, found := strings.Cut(strings.TrimSpace(line), ":")
		if !found {
			continue
		}
		v = strings.TrimSpace(v)
		switch strings.TrimSpace(k) {
		case TrailerSession:
			if v != "" && len(v) <= maxSessionID {
				session = v
			}
		case TrailerTurn:
			n, err := strconv.ParseInt(v, 10, 64)
			if err == nil && n > 0 {
				turn, haveTurn = n, true
			}
		}
	}
	return session, turn, session != "" && haveTurn
}

func isHex(s string) bool {
	if len(s) < 7 || len(s) > 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') && (c < 'A' || c > 'F') {
			return false
		}
	}
	return true
}

// ---- the guard gate ----

// maxProject bounds the project tag; it holds an org-scoped project slug, which
// clients/projects caps well under this.
const maxProject = 128

// leakFinding is one refused secret, reported back to whoever tried to store it.
// It carries WHERE and WHICH RULE and a masked preview — never the secret. The
// fingerprint is the same SHA-256 the security findings store uses, so a caller
// can confirm they rotated the right value without ever echoing it.
type leakFinding struct {
	Rule        string `json:"rule"`
	Severity    string `json:"severity"`
	Line        int    `json:"line"`
	Preview     string `json:"preview"`
	Fingerprint string `json:"fingerprint"`
}

// guardEvent scans an event body for credentials and returns what it found —
// nil when the body is clean. Callers REFUSE on a non-nil result (422); nothing
// here rewrites the payload.
//
// FAIL LOUDLY, DO NOT REDACT. Silent redaction stores a transcript that reads as
// clean while the underlying secret is still live and still in whatever log the
// agent copied it from; the author never learns to rotate it. A refusal is the
// only outcome that reaches the human. Refusing also keeps the invariant that
// makes publishing safe at all: a stored transcript has never held a secret, so
// making one public later cannot leak one.
func guardEvent(payload string) []leakFinding {
	f := detect.ScanContent("transcript", payload)
	if len(f) == 0 {
		return nil
	}
	out := make([]leakFinding, 0, len(f))
	for _, x := range f {
		out = append(out, leakFinding{
			Rule: x.RuleID, Severity: x.Severity, Line: x.Line,
			Preview: x.Preview, Fingerprint: x.Fingerprint,
		})
	}
	return out
}

// refuseLeak is the 422 a guarded write answers with: the count, the first rule
// that fired, and every finding — rule, severity, line, MASKED preview, and the
// SHA-256 fingerprint that lets the author confirm they rotated the right value.
// The secret itself is never in this body, because it was never stored.
func refuseLeak(c *zip.Ctx, f []leakFinding) error {
	return c.JSON(http.StatusUnprocessableEntity, map[string]any{
		"status": http.StatusUnprocessableEntity,
		"code":   "secret_in_transcript",
		"error": "transcript rejected: " + strconv.Itoa(len(f)) + " secret(s) detected (" +
			f[0].Rule + "). Secrets belong in KMS and are referenced by name; rotate the exposed value.",
		"findings": f,
	})
}

// ---- the public read ----

// buildTurn is one readable turn: what was said or done, and the commit that
// turn produced (empty when the turn changed nothing). Commit is echoed from the
// event body, which the ingesting client derived from git via ParseLinks; the
// commit itself carries the trailer/note, so the claim is checkable at source.
type buildTurn struct {
	Seq     int64  `json:"turn"`
	Kind    string `json:"kind"`
	Actor   string `json:"actor,omitempty"`
	Body    string `json:"body"`
	Commit  string `json:"commit,omitempty"`
	Subject string `json:"subject,omitempty"`
	At      string `json:"at"`
}

// buildView is the whole readable build of one project.
type buildView struct {
	Org       string      `json:"org"`
	Project   string      `json:"project"`
	Session   string      `json:"session"`
	Title     string      `json:"title,omitempty"`
	Agent     string      `json:"agent"`
	Status    string      `json:"status"`
	Repo      string      `json:"repo,omitempty"`
	Model     string      `json:"model,omitempty"`
	StartedAt string      `json:"startedAt"`
	EndedAt   string      `json:"endedAt,omitempty"`
	Turns     []buildTurn `json:"turns"`
	// Verify is the exact command that re-derives every commit binding below
	// straight from git, so nothing here has to be taken on trust.
	Verify string `json:"verify"`
}

// buildEventBody is the shape a transcript turn is stored in. Body is the human
// -readable text; Commit/Subject are the git facts the ingesting client read out
// of the trailer or note. Unknown fields are ignored, so a richer producer is
// forward-compatible with this reader.
type buildEventBody struct {
	Text    string `json:"text"`
	Commit  string `json:"commit,omitempty"`
	Subject string `json:"subject,omitempty"`
	Model   string `json:"model,omitempty"`
}

// buildsCap bounds one build read. A published build is a story, not an archive.
const buildsCap = 1000

// buildRef addresses one published build by the (org, project) pair in the URL.
//
// THIS IS NOT A TENANT KEY, and it is the one route in this package where an org
// arrives as an In field. The route is PUBLIC by construction: the only rows it
// can reach are ones an author explicitly published, so the org here is part of
// the build's public ADDRESS (the same pair a visitor reads in the URL bar), not
// an assertion of who is asking. Every org-scoped read in this package takes its
// tenant from principal.OrgFrom instead, because a tenant key read off the wire
// is a cross-tenant read the caller asserted for itself.
type buildRef struct {
	// Org is the org that published the build, from the path.
	Org string `json:"org"`
	// Project is the product's slug, from the path.
	Project string `json:"project"`
}

// ReadBuild returns the readable build of one product: the agent session that
// produced it, turn by turn — the prompts, the reasoning, the commits each turn
// produced — plus the exact `git log` that re-derives every commit binding from
// git itself, so nothing here has to be taken on trust.
//
// PUBLIC, no tenancy: it answers only for a session its author explicitly
// published, which is what makes it safe to be anonymous. An unpublished session
// is invisible here no matter who asks; its owner reads it through the org-scoped
// /v1/agents/sessions routes, which need a validated principal.
//
// Example: {"org": "hanzo", "project": "landing"}
func (o sessionOps) build(ctx context.Context, in *buildRef) (*buildView, error) {
	s := o.s
	org := strings.TrimSpace(in.Org)
	project := strings.TrimSpace(in.Project)
	if org == "" || project == "" || len(project) > maxProject {
		return nil, zip.ErrNotFound("build not found")
	}
	rows, err := s.State.store.ListSessions(ctx, org,
		SessionFilter{Project: project, Published: true, Limit: 1})
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "builds: %v", err)
	}
	if len(rows) == 0 {
		return nil, zip.ErrNotFound("no published build for " + org + "/" + project)
	}
	x := rows[0]
	evs, err := s.State.store.ListEvents(ctx, org, x.ID, 0, buildsCap)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "turns: %v", err)
	}
	v := buildView{
		Org: org, Project: project, Session: x.ID, Title: x.Title, Agent: x.Agent,
		Status: x.Status, Repo: x.Repo,
		StartedAt: rfc3339(x.StartedAt), EndedAt: rfc3339(x.EndedAt),
		Turns:  make([]buildTurn, 0, len(evs)),
		Verify: "git log --format='" + ProvenanceLogFormat + "' --notes=" + NotesRef,
	}
	for _, e := range evs {
		var b buildEventBody
		if e.Payload != "" {
			_ = json.Unmarshal([]byte(e.Payload), &b)
		}
		if v.Model == "" {
			v.Model = b.Model
		}
		v.Turns = append(v.Turns, buildTurn{
			Seq: e.Seq, Kind: e.Kind, Actor: e.Actor, Body: b.Text,
			Commit: b.Commit, Subject: b.Subject, At: rfc3339(e.CreatedAt),
		})
	}
	return &v, nil
}

// ---- the deploy, as the build's last turn ----

// deployWriter fills the seam clients/projects left open (projects/observer.go):
// when a site goes live, the build session that produced it gets ONE more turn
// saying so, with the URL. That is what closes the story a visitor reads —
// prompt, reasoning, diffs, DEPLOY — and it is why the seam exists: projects
// still knows nothing about sessions, it only calls an interface.
//
// The write is best-effort and silent on failure. A deploy is the user's
// outcome; it must never fail because its narration could not be recorded.
type deployWriter struct{ s *cloud.Service[state] }

// mountProvenance registers the deploy seam. Called once from Mount; the wiring
// lives HERE, next to the writer it installs, so the provenance lane is one file
// you can read end to end and agents.go stays a router.
func mountProvenance(s *cloud.Service[state]) { projects.SetDeployObserver(deployWriter{s: s}) }

// OnDeploy appends the "site is live" turn to the newest session tagged with
// this project. No tagged session ⇒ nothing to narrate ⇒ no-op (a script-deployed
// site has no build story, and inventing one would be a lie).
func (w deployWriter) OnDeploy(ctx context.Context, org, slug, url, deploymentID string) {
	if w.s == nil || org == "" || slug == "" {
		return
	}
	rows, err := w.s.State.store.ListSessions(ctx, org, SessionFilter{Project: slug, Limit: 1})
	if err != nil || len(rows) == 0 {
		return
	}
	body, err := json.Marshal(buildEventBody{
		Text: "Deployed. " + slug + " is live at " + url + " (deployment " + deploymentID + ").",
	})
	if err != nil {
		return
	}
	id, err := genID("evt")
	if err != nil {
		return
	}
	e, err := w.s.State.store.AppendEvent(ctx, Event{
		ID: id, SessionID: rows[0].ID, Org: org, Kind: KindStatus, Actor: "deploy",
		Payload: string(body), CreatedAt: time.Now().Unix(),
	})
	if err != nil {
		return
	}
	publishEvent(w.s, org, rows[0].RootID, e)
}

// buildsQuery pages the public build index.
type buildsQuery struct {
	// Limit caps the page. Absent, zero or over 500 reads as 100.
	Limit int `json:"limit"`
}

// buildSummary is one published build as the index lists it: enough to render a
// gallery card and link to the full story. Every field is always present —
// including the empty ones — because that is what this route has always sent.
type buildSummary struct {
	Org       string `json:"org"`
	Project   string `json:"project"`
	Session   string `json:"session"`
	Title     string `json:"title"`
	Agent     string `json:"agent"`
	Status    string `json:"status"`
	Repo      string `json:"repo"`
	Turns     int    `json:"turns"`
	StartedAt string `json:"startedAt"`
	EndedAt   string `json:"endedAt"`
}

// buildList is the public index of published builds.
type buildList struct {
	// Builds is every published build, most recently updated first.
	Builds []buildSummary `json:"builds"`
}

// ListBuilds returns the public index of every published build, most recently
// updated first, so a gallery can link straight to the story behind each product.
// PUBLIC, no tenancy: publishing is the author's act, and only published root
// sessions appear here.
func (o sessionOps) builds(ctx context.Context, in *buildsQuery) (*buildList, error) {
	s := o.s
	rows, err := s.State.store.ListPublishedBuilds(ctx, in.Limit)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "builds: %v", err)
	}
	out := make([]buildSummary, 0, len(rows))
	for _, x := range rows {
		n, _ := s.State.store.CountEvents(ctx, x.Org, x.ID)
		out = append(out, buildSummary{
			Org: x.Org, Project: x.Project, Session: x.ID, Title: x.Title,
			Agent: x.Agent, Status: x.Status, Repo: x.Repo, Turns: n,
			StartedAt: rfc3339(x.StartedAt), EndedAt: rfc3339(x.EndedAt),
		})
	}
	return &buildList{Builds: out}, nil
}
