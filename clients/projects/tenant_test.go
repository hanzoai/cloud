package projects

import (
	"encoding/json"
	"net/http"
	"testing"
)

// TestOrgKeyInjectiveNoFold is the guard for the cross-tenant S3-prefix collision:
// the org key MUST be the VERBATIM validated owner (principal.Org), so DISTINCT
// owners always get DISTINCT namespaces + S3 prefixes. The old sanitizeOrg FOLD
// (lowercase + non-alnum→'-' + truncate-32) collapsed "acme"/"Acme" and
// "acme-corp"/"acme.corp" onto ONE key — one org could then overwrite/read the
// other's deployed site. This test FAILS on that fold (both twins share a
// namespace) and PASSES on the verbatim key. It also proves a path-hostile owner
// (an S3-key traversal) is refused 403 rather than allowed to escape its prefix.
func TestOrgKeyInjectiveNoFold(t *testing.T) {
	app := mountApp(t)

	// Owner pairs a case/punct/length FOLD would collapse onto one key. Each org
	// creates a same-named project; verbatim keying keeps them fully isolated.
	pairs := [][2]string{
		{"acme", "Acme"},           // case
		{"acme-corp", "acme.corp"}, // punctuation ('.'→'-' under the old fold)
	}
	for _, p := range pairs {
		a, b := p[0], p[1]
		if code, body := do(t, app, http.MethodPost, "/v1/projects", a, map[string]any{"name": "own"}); code != http.StatusCreated {
			t.Fatalf("org %q create: %d %s", a, code, body)
		}
		if code, body := do(t, app, http.MethodPost, "/v1/projects", b, map[string]any{"name": "own"}); code != http.StatusCreated {
			t.Fatalf("org %q create (fold twin): %d %s", b, code, body)
		}
		// b must see ONLY its own project — never a's (no shared namespace).
		code, body := do(t, app, http.MethodGet, "/v1/projects", b, nil)
		if code != http.StatusOK {
			t.Fatalf("org %q list: %d %s", b, code, body)
		}
		var list []projectView
		if err := json.Unmarshal(body, &list); err != nil {
			t.Fatalf("org %q list decode: %v (%s)", b, err, body)
		}
		if len(list) != 1 {
			t.Fatalf("fold leak: org %q sees %d projects (want its own 1): %+v", b, len(list), list)
		}
		// a's slug is org-scoped: deleting "own" under b must NOT touch a's "own".
		if code, body := do(t, app, http.MethodDelete, "/v1/projects/own", b, nil); code != http.StatusNoContent {
			t.Fatalf("org %q delete own: %d %s", b, code, body)
		}
		if code, _ := do(t, app, http.MethodGet, "/v1/projects/own", a, nil); code != http.StatusOK {
			t.Fatalf("org %q project was deleted by its fold-twin %q — NOT isolated", a, b)
		}
	}

	// A path-hostile org (S3-key traversal) IS refused: the verbatim key must not
	// let an owner escape its sitePrefix segment. `do` sets a validated principal
	// (X-User-Id), so this exercises the orgPathSafe refusal, not the anonymous path.
	for _, bad := range []string{"a/b", "..", "../x", "a\\b"} {
		if code, body := do(t, app, http.MethodGet, "/v1/projects", bad, nil); code != http.StatusForbidden {
			t.Fatalf("path-hostile org %q must be 403, got %d %s", bad, code, body)
		}
	}
}

// TestPlainProjectGitDeployStampsBucket is the regression guard for the
// git-CI go-live invariant: a project that goes live via the git path
// (link repo → git deploy → CI complete) MUST carry a NON-EMPTY bucket, else
// siteResolver serves <slug>.hanzo.app as a 404. In current main createProject
// stamps p.Bucket at creation, so the invariant already holds end-to-end; this
// test locks it in (a future refactor that drops the create-time stamp — as an
// upstream branch once did — would trip this guard).
func TestPlainProjectGitDeployStampsBucket(t *testing.T) {
	app := mountApp(t)

	// Create a plain project, then link a repo (makes it a git-buildable site).
	if code, b := do(t, app, http.MethodPost, "/v1/projects", "acme", map[string]any{"name": "myblog"}); code != http.StatusCreated {
		t.Fatalf("create: %d %s", code, b)
	}
	if code, b := do(t, app, http.MethodPatch, "/v1/projects/myblog", "acme", map[string]any{"repo": map[string]any{"url": "https://github.com/acme/myblog"}}); code != http.StatusOK {
		t.Fatalf("link repo: %d %s", code, b)
	}

	// Git deploy (application/json body → deployGit) → 202 queued deployment.
	code, b := do(t, app, http.MethodPost, "/v1/projects/myblog/deploy", "acme", map[string]any{"source": "git"})
	if code != http.StatusAccepted {
		t.Fatalf("git deploy must be 202, got %d %s", code, b)
	}
	var dep deploymentView
	if err := json.Unmarshal(b, &dep); err != nil || dep.ID == "" {
		t.Fatalf("decode deployment: %v (%s)", err, b)
	}
	if dep.Bucket == "" {
		t.Fatalf("git deployment must carry a bucket at create, got empty")
	}

	// CI completion flips it live.
	if code, b := do(t, app, http.MethodPost, "/v1/projects/myblog/deployments/"+dep.ID+"/complete", "acme", map[string]any{"status": "live"}); code != http.StatusOK {
		t.Fatalf("complete must be 200, got %d %s", code, b)
	}

	// The project is now live AND carries a non-empty bucket. Without it,
	// siteResolver.Resolve returns Site.Bucket="" and the live site 404s.
	code, b = do(t, app, http.MethodGet, "/v1/projects/myblog", "acme", nil)
	if code != http.StatusOK {
		t.Fatalf("get after deploy: %d %s", code, b)
	}
	var p projectView
	if err := json.Unmarshal(b, &p); err != nil {
		t.Fatalf("decode project: %v (%s)", err, b)
	}
	if p.Status != "live" {
		t.Fatalf("status=%q want live", p.Status)
	}
	if p.Bucket == "" {
		t.Fatalf("live project MUST have a non-empty bucket or its site 404s; got empty")
	}
}
