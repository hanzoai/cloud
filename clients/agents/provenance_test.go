package agents

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// ---- the binding: git states it, we only parse it ----

// TestParseLinksReadsTrailersAndNotes proves ONE parser covers both ways a
// commit can carry the binding — a trailer written into a new commit, and a note
// attached to a commit that already existed — and that a partial record asserts
// nothing. This is the whole reason there is no commit⇄turn table.
func TestParseLinksReadsTrailersAndNotes(t *testing.T) {
	// Exactly what `git log --format=ProvenanceLogFormat` emits: NUL-separated
	// records, sha on the first line, message body (and note) after it.
	log := strings.Join([]string{
		// (1) a new commit carrying trailers in its own message
		"a1b2c3d4e5f60718293a4b5c6d7e8f9012345678\n" +
			"projects: make the build readable\n\nWhy: a visitor should follow the session.\n\n" +
			"Hanzo-Session: sess_abc\nHanzo-Turn: 7\n",
		// (2) a pre-existing commit whose binding arrived later as a NOTE
		"deadbeefcafe1234567890abcdefabcdefabcdef\n" +
			"initial import\n\n" + "Hanzo-Session: sess_abc\nHanzo-Turn: 1\n",
		// (3) ordinary work — no binding, must be skipped, not invented
		"0123456789abcdef0123456789abcdef01234567\nchore: bump dep\n",
		// (4) half-written trailer — session without a turn asserts nothing
		"fedcba9876543210fedcba9876543210fedcba98\nwip\n\nHanzo-Session: sess_abc\n",
		// (5) not a sha at all — never parsed as one
		"not-a-sha\nHanzo-Session: sess_abc\nHanzo-Turn: 3\n",
	}, "\x00")

	got := ParseLinks(log)
	if len(got) != 2 {
		t.Fatalf("want 2 links (trailer + note), got %d: %+v", len(got), got)
	}
	if got[0].Commit != "a1b2c3d4e5f60718293a4b5c6d7e8f9012345678" || got[0].Turn != 7 || got[0].Session != "sess_abc" {
		t.Fatalf("trailer link wrong: %+v", got[0])
	}
	if got[1].Commit != "deadbeefcafe1234567890abcdefabcdefabcdef" || got[1].Turn != 1 {
		t.Fatalf("note link wrong: %+v", got[1])
	}
}

// TestParseLinksLastTrailerWins pins git's own trailer semantics: an amended
// commit APPENDS the corrected trailer, so the last occurrence is the truth.
func TestParseLinksLastTrailerWins(t *testing.T) {
	log := "abcdef1234567\nfix\n\nHanzo-Session: sess_old\nHanzo-Turn: 2\n" +
		"Hanzo-Session: sess_new\nHanzo-Turn: 9\n"
	got := ParseLinks(log)
	if len(got) != 1 || got[0].Session != "sess_new" || got[0].Turn != 9 {
		t.Fatalf("want the amended trailer to win, got %+v", got)
	}
}

// ---- the guard gate: refuse loudly, never store, never echo ----

// TestGuardRefusesSecretInTranscript is the load-bearing security test. A
// transcript turn carrying a live-shaped credential must be REFUSED with 422,
// the response must name the rule but NEVER contain the secret, and the store
// must be left with zero events — a redaction-based design would silently store
// a "clean" turn and leave the real secret live and unrotated.
func TestGuardRefusesSecretInTranscript(t *testing.T) {
	app := mountApp(t, &fakeAI{content: "x"})
	s := register(t, app, "acme", map[string]any{"agent": "dev", "project": "shop"})

	secret := "AKIA" + strings.Repeat("Q", 16) // AWS access key ID shape
	body, _ := json.Marshal(map[string]any{
		"text": "then I exported AWS_ACCESS_KEY_ID=" + secret + " and reran the deploy",
	})
	code, resp := do(t, app, http.MethodPost, "/v1/agents/sessions/"+s.ID+"/events", "acme",
		map[string]any{"kind": KindMessage, "payload": json.RawMessage(body)})

	if code != http.StatusUnprocessableEntity {
		t.Fatalf("secret in transcript want 422, got %d (%s)", code, resp)
	}
	if strings.Contains(string(resp), secret) {
		t.Fatalf("refusal ECHOED the secret back — the one thing it must never do: %s", resp)
	}
	var out struct {
		Code     string        `json:"code"`
		Error    string        `json:"error"`
		Findings []leakFinding `json:"findings"`
	}
	mustJSON(t, resp, &out)
	if out.Code != "secret_in_transcript" || len(out.Findings) == 0 {
		t.Fatalf("refusal must be machine-readable and name findings: %+v", out)
	}
	if out.Findings[0].Rule != "aws-access-key-id" {
		t.Fatalf("want the aws rule named, got %q", out.Findings[0].Rule)
	}
	// The preview locates the secret without being it: first/last chars kept, the
	// middle starred, and the SHA-256 fingerprint is what proves a rotation later.
	if p := out.Findings[0].Preview; p == secret || !strings.Contains(p, "*") {
		t.Fatalf("preview must be masked, got %q", p)
	}
	if out.Findings[0].Fingerprint == "" {
		t.Fatalf("finding must carry a fingerprint so the author can confirm the rotation: %+v", out.Findings[0])
	}

	// Nothing was stored: the refusal is the whole outcome.
	code, resp = do(t, app, http.MethodGet, "/v1/agents/sessions/"+s.ID, "acme", nil)
	if code != http.StatusOK {
		t.Fatalf("detail want 200, got %d (%s)", code, resp)
	}
	var det sessionDetail
	mustJSON(t, resp, &det)
	if det.Events != 0 {
		t.Fatalf("refused turn must not be stored; session has %d events", det.Events)
	}
}

// TestGuardAdmitsKMSReference proves the gate does not punish the CORRECT
// pattern: a transcript that names a secret instead of pasting it is clean, so
// truthful sessions publish without friction.
func TestGuardAdmitsKMSReference(t *testing.T) {
	app := mountApp(t, &fakeAI{content: "x"})
	s := register(t, app, "acme", map[string]any{"agent": "dev", "project": "shop"})
	body, _ := json.Marshal(map[string]any{
		"text": "read the deploy key from KMS by name: hanzo/projects/shop/deploy-key",
	})
	code, resp := do(t, app, http.MethodPost, "/v1/agents/sessions/"+s.ID+"/events", "acme",
		map[string]any{"kind": KindMessage, "payload": json.RawMessage(body)})
	if code != http.StatusCreated {
		t.Fatalf("a KMS reference is not a secret; want 201, got %d (%s)", code, resp)
	}
}

// ---- publishing: the author's act is the whole access rule ----

// TestPublishedBuildIsPubliclyReadable walks the visitor's path end to end: an
// unpublished build is invisible to an anonymous reader, publishing it makes the
// SAME session readable with its turns and per-turn commits, and the response
// carries the exact git command that re-derives every binding it claims.
func TestPublishedBuildIsPubliclyReadable(t *testing.T) {
	app := mountApp(t, &fakeAI{content: "x"})
	s := register(t, app, "acme", map[string]any{
		"agent": "dev", "project": "shop", "title": "storefront from the vite template",
		"repo": "https://git.hanzo.ai/acme/shop",
	})

	// Two real turns: the prompt, and the change it produced (bound to a commit).
	for _, turn := range []map[string]any{
		{"text": "take the vite template and make it a storefront with a cart"},
		{"text": "added CartProvider and the checkout route", "commit": "a1b2c3d4e5f6071829", "subject": "shop: cart + checkout"},
	} {
		p, _ := json.Marshal(turn)
		code, b := do(t, app, http.MethodPost, "/v1/agents/sessions/"+s.ID+"/events", "acme",
			map[string]any{"kind": KindMessage, "payload": json.RawMessage(p)})
		if code != http.StatusCreated {
			t.Fatalf("append turn want 201, got %d (%s)", code, b)
		}
	}

	// Anonymous, before publishing: nothing to see. Note doNoUser sends no
	// X-User-Id — a true stranger, exactly who this route is for.
	code, b := doNoUser(t, app, http.MethodGet, "/v1/agents/builds/acme/shop", "", nil)
	if code != http.StatusNotFound {
		t.Fatalf("unpublished build must be invisible to a stranger; got %d (%s)", code, b)
	}

	// The author publishes.
	code, b = do(t, app, http.MethodPatch, "/v1/agents/sessions/"+s.ID, "acme",
		map[string]any{"published": true})
	if code != http.StatusOK {
		t.Fatalf("publish want 200, got %d (%s)", code, b)
	}

	// The same stranger can now read the build.
	code, b = doNoUser(t, app, http.MethodGet, "/v1/agents/builds/acme/shop", "", nil)
	if code != http.StatusOK {
		t.Fatalf("published build want 200 for a stranger, got %d (%s)", code, b)
	}
	var v buildView
	mustJSON(t, b, &v)
	if v.Session != s.ID || v.Project != "shop" || v.Repo != "https://git.hanzo.ai/acme/shop" {
		t.Fatalf("build identity wrong: %+v", v)
	}
	if len(v.Turns) != 2 {
		t.Fatalf("want 2 turns, got %d: %+v", len(v.Turns), v.Turns)
	}
	if !strings.Contains(v.Turns[0].Body, "storefront with a cart") {
		t.Fatalf("turn 1 must be the real prompt, got %q", v.Turns[0].Body)
	}
	if v.Turns[1].Commit != "a1b2c3d4e5f6071829" {
		t.Fatalf("turn 2 must carry the commit it produced, got %q", v.Turns[1].Commit)
	}
	// Forkable from any point requires BOTH halves to be present per turn.
	if v.Turns[1].Subject == "" {
		t.Fatalf("a forkable turn needs its commit subject, got %+v", v.Turns[1])
	}
	if !strings.Contains(v.Verify, NotesRef) || !strings.Contains(v.Verify, "git log") {
		t.Fatalf("build must publish the command that re-derives it: %q", v.Verify)
	}

	// And it shows up in the public index the gallery links from.
	code, b = doNoUser(t, app, http.MethodGet, "/v1/agents/builds", "", nil)
	if code != http.StatusOK || !strings.Contains(string(b), "shop") {
		t.Fatalf("published build must appear in the public index: %d %s", code, b)
	}
}

// TestPublishRequiresAProject refuses a build with no product: /v1/agents/builds
// is keyed on (org, project), so publishing without one creates a story nobody
// can open — better refused at the write than silently unreachable.
func TestPublishRequiresAProject(t *testing.T) {
	app := mountApp(t, &fakeAI{content: "x"})
	code, b := do(t, app, http.MethodPost, "/v1/agents/sessions", "acme",
		map[string]any{"agent": "dev", "published": true})
	if code != http.StatusBadRequest {
		t.Fatalf("publish without a project want 400, got %d (%s)", code, b)
	}
	s := register(t, app, "acme", map[string]any{"agent": "dev"})
	code, b = do(t, app, http.MethodPatch, "/v1/agents/sessions/"+s.ID, "acme",
		map[string]any{"published": true})
	if code != http.StatusBadRequest {
		t.Fatalf("patch-publish without a project want 400, got %d (%s)", code, b)
	}
}

// TestBuildsAreOrgKeyed proves two orgs can hold the same project slug and each
// build stays its own: the public route is keyed on (org, project), not slug.
func TestBuildsAreOrgKeyed(t *testing.T) {
	app := mountApp(t, &fakeAI{content: "x"})
	for _, org := range []string{"acme", "globex"} {
		s := register(t, app, org, map[string]any{"agent": "dev", "project": "shop", "title": org + " build"})
		if code, b := do(t, app, http.MethodPatch, "/v1/agents/sessions/"+s.ID, org,
			map[string]any{"published": true}); code != http.StatusOK {
			t.Fatalf("publish %s want 200, got %d (%s)", org, code, b)
		}
	}
	for _, org := range []string{"acme", "globex"} {
		code, b := doNoUser(t, app, http.MethodGet, "/v1/agents/builds/"+org+"/shop", "", nil)
		if code != http.StatusOK {
			t.Fatalf("%s build want 200, got %d (%s)", org, code, b)
		}
		var v buildView
		mustJSON(t, b, &v)
		if v.Title != org+" build" {
			t.Fatalf("(org,project) keying broken: %s/shop returned %q", org, v.Title)
		}
	}
}

// ---- the deploy closes the story ----

// TestDeployBecomesTheLastTurn proves the seam clients/projects left open is now
// filled: a site going live appends the final turn to the session that built it,
// so the published story ends at the live URL.
func TestDeployBecomesTheLastTurn(t *testing.T) {
	app := mountApp(t, &fakeAI{content: "x"})
	s := register(t, app, "acme", map[string]any{"agent": "dev", "project": "shop"})
	if code, b := do(t, app, http.MethodPatch, "/v1/agents/sessions/"+s.ID, "acme",
		map[string]any{"published": true}); code != http.StatusOK {
		t.Fatalf("publish: %d %s", code, b)
	}

	// Exactly what projects.notifyDeploy calls.
	deployWriter{s: mounted}.OnDeploy(context.Background(), "acme", "shop",
		"https://shop.hanzo.app", "dep_123")

	code, b := doNoUser(t, app, http.MethodGet, "/v1/agents/builds/acme/shop", "", nil)
	if code != http.StatusOK {
		t.Fatalf("read build: %d %s", code, b)
	}
	var v buildView
	mustJSON(t, b, &v)
	if len(v.Turns) != 1 {
		t.Fatalf("want the deploy turn, got %d turns", len(v.Turns))
	}
	last := v.Turns[len(v.Turns)-1]
	if last.Kind != KindStatus || !strings.Contains(last.Body, "https://shop.hanzo.app") {
		t.Fatalf("last turn must be the live deploy: %+v", last)
	}
}

// TestDeployWithoutABuildSessionIsSilent pins the honest no-op: a site deployed
// by a script has no agent session, so there is no story to narrate and nothing
// is invented. It must also not panic or error the deploy.
func TestDeployWithoutABuildSessionIsSilent(t *testing.T) {
	app := mountApp(t, &fakeAI{content: "x"})
	_ = app
	deployWriter{s: mounted}.OnDeploy(context.Background(), "acme", "never-built",
		"https://never-built.hanzo.app", "dep_1")
	code, b := doNoUser(t, app, http.MethodGet, "/v1/agents/builds/acme/never-built", "", nil)
	if code != http.StatusNotFound {
		t.Fatalf("a script-deployed site has no build story; want 404, got %d (%s)", code, b)
	}
}

// ---- the format constant, proved against real git ----

// TestParseLinksAgainstRealGit is the test that keeps ProvenanceLogFormat honest.
// The fixture tests above encode MY belief about git's output; this one runs git
// itself, in a throwaway repo, both ways a binding can be recorded — a trailer in
// a fresh commit and a NOTE on a commit that already existed — and parses the
// bytes git actually produced. If the format constant ever drifts from what the
// parser expects, this fails and the fixtures would not.
func TestParseLinksAgainstRealGit(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	run := func(args ...string) string {
		t.Helper()
		cmd := exec.Command(git, args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@e", "GIT_COMMITTER_NAME=t",
			"GIT_COMMITTER_EMAIL=t@e", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return string(out)
	}
	run("init", "-q", "-b", "main")

	// (1) history that already exists — committed with NO trailer, exactly the
	// situation a retro-ingest faces. Rewriting it to add one would change the sha.
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "-A")
	run("commit", "-q", "-m", "initial import")
	old := strings.TrimSpace(run("rev-parse", "HEAD"))

	// (2) new work — the trailer goes in the commit message itself.
	if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte("two"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "-A")
	run("commit", "-q", "-m", "shop: cart + checkout\n\nHanzo-Session: sess_real\nHanzo-Turn: 12")
	fresh := strings.TrimSpace(run("rev-parse", "HEAD"))

	// The note binds the OLD commit without touching it.
	run("notes", "--ref="+NotesRef, "add", "-m", "Hanzo-Session: sess_real\nHanzo-Turn: 1", old)
	if now := strings.TrimSpace(run("rev-parse", "HEAD~1")); now != old {
		t.Fatalf("adding a note must not rewrite history: %s -> %s", old, now)
	}

	links := ParseLinks(run("log", "--format="+ProvenanceLogFormat, "--notes="+NotesRef))
	if len(links) != 2 {
		t.Fatalf("want both bindings from real git output, got %d: %+v", len(links), links)
	}
	byCommit := map[string]Link{}
	for _, l := range links {
		byCommit[l.Commit] = l
	}
	if got := byCommit[fresh]; got.Turn != 12 || got.Session != "sess_real" {
		t.Fatalf("trailer binding from real git wrong: %+v", got)
	}
	if got := byCommit[old]; got.Turn != 1 || got.Session != "sess_real" {
		t.Fatalf("note binding from real git wrong: %+v", got)
	}
}

// TestParseLinksSurvivesASplitTrailerBlock pins the reason ParseLinks scans the
// WHOLE message body line by line instead of calling git's trailer interpreter.
//
// Real repositories run commit-msg hooks. Ours appends a sign-off with a LEADING
// BLANK LINE, which starts a new paragraph — and git only treats the LAST
// paragraph as trailers, so `git log --format=%(trailers)` and
// `git interpret-trailers --parse` both report the appended line and nothing
// above it. A binding written by the agent would be invisible to git's own
// tooling through no fault of the agent.
//
// The fact is still in the commit, so the parser reads it. This test fails if
// anyone "simplifies" ParseLinks into a last-paragraph parser.
func TestParseLinksSurvivesASplitTrailerBlock(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	run := func(args ...string) string {
		t.Helper()
		cmd := exec.Command(git, args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@e",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@e",
			"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return string(out)
	}
	run("init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "-A")
	// Exactly the shape the hook produces: our trailers, then a blank line, then
	// the appended sign-off.
	run("commit", "-q", "-m",
		"agents: readable builds\n\nHanzo-Session: sess_split\nHanzo-Turn: 42\n\nCo-authored-by: Hanzo Dev <dev@hanzo.ai>")

	// Git's own interpreter sees only the last paragraph — this is the trap.
	if strings.Contains(run("log", "-1", "--format=%(trailers:key=Hanzo-Turn,valueonly)"), "42") {
		t.Log("note: this git treats a split block as trailers; the parser handles both")
	}
	links := ParseLinks(run("log", "--format="+ProvenanceLogFormat))
	if len(links) != 1 || links[0].Turn != 42 || links[0].Session != "sess_split" {
		t.Fatalf("a hook-split trailer block must still bind, got %+v", links)
	}
}
