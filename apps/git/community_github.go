package git

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// community_github.go — the GitHub replica of a community project.
//
// git.hanzo.ai is canonical; GitHub is a mirror. But a mirror nobody can find is
// not marketing, so a public project gets a real repo at
// github.com/<owner>/<org>-<slug> — a link its author can hand out, star,
// and be found through — and its visibility is kept in step on BOTH hosts. One
// switch in the console, two hosts follow.
//
// We hold admin on the community org, so this creates the far-side repo rather
// than assuming somebody provisioned it. That matters: mirror_out force-pushes
// to a target that must already exist, so without this the mirror would fail on
// every project forever.
//
// PRIVATE IS HONOURED, not approximated. Flipping a project private flips the
// GitHub repo private in the same call. The alternative — deleting the replica —
// would destroy stars, forks and issue history for what the author intended as a
// temporary change, so the repo persists and only its visibility moves.
//
// Credentials are the SAME KMS-injected GIT_MIRROR_TOKEN mirror_out pushes with.
// No token ⇒ every call here is a no-op, so a dev or test deployment runs the
// whole publish path without reaching for the network.

// api is the GitHub API root. A package var, not a const, for the same
// reason clients/platform does it: tests point it at an httptest server so the
// create/patch decisions are proven without a network or a live org.
var api = "https://api.github.com"

// envOwner overrides the GitHub org community projects are replicated
// into. It exists for staging (point it at a scratch org), not as an on/off
// switch — the default is the real one, because appearing in the community is
// the opt-OUT default the platform wants.
const envOwner = "GIT_COMMUNITY_ORG"

// defaultOwner is where public projects land on GitHub.
const defaultOwner = "hanzo-community"

// owner resolves the GitHub org, trimmed of anything path-like so it can
// only ever name an org.
func owner() string {
	if v := strings.Trim(strings.TrimSpace(os.Getenv(envOwner)), "/"); v != "" {
		return v
	}
	return defaultOwner
}

// name is the far-side repo name for one project, and false for a pair that
// cannot have one.
//
// A single GitHub org is a flat namespace and tenant slugs collide across orgs,
// so the tenant org is part of the name. It is an identifier, derived
// identically every time — never a display name.
//
// The separator is `_` because NEITHER HALF MAY CONTAIN ONE, which is the whole
// of what makes the flattening injective — and injective is not cosmetic here:
// with `-`, org `a-b` publishing project `c` and org `a` publishing project
// `b-c` spell ONE repo, so the first tenant's create would decide who may read
// the second tenant's source. A pair that cannot be spelled this way gets NO
// replica, which is the safe half: nothing is published, so nothing is exposed.
// It is the same name, minted by the same rule, that the canonical forge uses.
func name(org, slug string) (string, bool) {
	if org == "" || slug == "" || strings.ContainsAny(org+slug, "_/") {
		return "", false
	}
	return org + "_" + slug, true
}

// visibility is GitHub's name for what we call listed. The CREATE endpoint
// takes a `private` boolean and the PATCH endpoint takes this string; they are
// not interchangeable (see ensure), so the mapping lives here once.
func visibility(listed bool) string {
	if listed {
		return "public"
	}
	return "private"
}

// secret is our GitHub credential, resolved by the ONE resolver every other git
// credential goes through. This reader only ever talks to api.github.com, so the host
// is named here rather than configured.
func secret() string { return mirrorCredential("github.com") }

// call performs one authenticated GitHub API call and returns the status code.
// Bodies are read and discarded except on the decode path, so a caller never
// leaks a connection. The token rides the Authorization header only — never a
// URL, never argv, the same discipline as gitexec.
func call(ctx context.Context, method, endpoint string, body any) (int, error) {
	var rdr *bytes.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		rdr = bytes.NewReader(b)
	} else {
		rdr = bytes.NewReader(nil)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, rdr)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+secret())
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode, nil
}

// ensure makes github.com/<owner>/<org>-<slug> exist and match
// the project's visibility. Idempotent, and cheapest in the steady state: it
// PATCHes first (the common case is a project that already has a replica) and
// only creates on a 404.
//
// Returns the clone URL so the caller can register the mirror against exactly
// what it just ensured, and "" when the token is unconfigured — which is the
// signal to skip the mirror too, rather than register a push that cannot land.
func ensure(ctx context.Context, org, slug, description string, listed bool) (string, error) {
	name, ok := name(org, slug)
	if !ok || secret() == "" {
		return "", nil
	}
	ghOrg := owner()
	repoURL := fmt.Sprintf("https://github.com/%s/%s.git", ghOrg, name)
	api := fmt.Sprintf("%s/repos/%s/%s", api,
		url.PathEscape(ghOrg), url.PathEscape(name))

	// Visibility is the ONE field this owns. Description is sent only at create
	// (below) so an author who edits it on GitHub keeps their edit.
	//
	// The field is `visibility`, NOT `private`. They look interchangeable and are
	// not: PATCHing {"private":true} on an org repo is rejected 422 with an empty
	// error list, while {"visibility":"private"} succeeds. Verified against the
	// live hanzo-community org — with `private` here, creates would have worked
	// and every RETRACTION would have silently failed, which is precisely the
	// direction that cannot be allowed to fail.
	code, err := call(ctx, http.MethodPatch, api, map[string]any{"visibility": visibility(listed)})
	if err != nil {
		return "", fmt.Errorf("github: patch %s/%s: %w", ghOrg, name, err)
	}
	if code == http.StatusOK {
		return repoURL, nil
	}
	if code != http.StatusNotFound {
		return "", fmt.Errorf("github: patch %s/%s: status %d", ghOrg, name, code)
	}

	// Not there yet: create it, born with the right visibility so a private
	// project is never briefly public. auto_init stays false — the first mirror
	// push carries the real history, and an initial commit would collide with it.
	create := fmt.Sprintf("%s/orgs/%s/repos", api, url.PathEscape(ghOrg))
	code, err = call(ctx, http.MethodPost, create, map[string]any{
		"name": name, "description": description,
		"private": !listed, "auto_init": false, "has_wiki": false,
	})
	if err != nil {
		return "", fmt.Errorf("github: create %s/%s: %w", ghOrg, name, err)
	}
	// 422 is GitHub's "name already exists" — a concurrent publish won the race,
	// which is exactly the state we wanted.
	if code != http.StatusCreated && code != http.StatusUnprocessableEntity {
		return "", fmt.Errorf("github: create %s/%s: status %d", ghOrg, name, code)
	}
	return repoURL, nil
}

// remove takes a deleted project's replica off GitHub: CLOSED first, then
// deleted, and the deletion CONFIRMED by failing to find it.
//
// Deleting rather than closing is what makes a reclaimed slug safe. The replica
// is found by NAME, so one left behind is one the next project of that name
// adopts — mirrored history and all — and the first thing that project does is
// publish. The trade goes the other way for a project that merely goes private
// (see ensure): there the project still exists, its stars, forks and issues are
// its own, and the visibility is the only thing that has changed.
//
// Closed first because the two cover different failures: a token that may not
// delete has still been told the readable half, and open is the half that leaks.
//
// An absent repo is SUCCESS, on both halves. Absence is the state asked for, and
// a retirement that has to be retried must be able to say so by trying again.
func remove(ctx context.Context, org, slug string) error {
	name, ok := name(org, slug)
	if !ok || secret() == "" {
		return nil
	}
	ghOrg := owner()
	endpoint := fmt.Sprintf("%s/repos/%s/%s", api, url.PathEscape(ghOrg), url.PathEscape(name))

	code, err := call(ctx, http.MethodPatch, endpoint, map[string]any{"visibility": visibility(false)})
	if err != nil {
		return fmt.Errorf("github: patch %s/%s: %w", ghOrg, name, err)
	}
	if code == http.StatusNotFound {
		return nil // not there at all, which is the state asked for
	}
	if code != http.StatusOK {
		return fmt.Errorf("github: patch %s/%s: status %d", ghOrg, name, code)
	}
	if code, err = call(ctx, http.MethodDelete, endpoint, nil); err != nil {
		return fmt.Errorf("github: delete %s/%s: %w", ghOrg, name, err)
	}
	if code != http.StatusNoContent && code != http.StatusNotFound {
		return fmt.Errorf("github: delete %s/%s: status %d", ghOrg, name, code)
	}
	// The verdict comes from a READ, the same way the canonical forge's delete
	// reaches one: a 2xx says GitHub accepted the request, and the only statement
	// that the repo is gone is GitHub failing to find it afterwards.
	if code, err = call(ctx, http.MethodGet, endpoint, nil); err != nil {
		return fmt.Errorf("github: confirm delete %s/%s: %w", ghOrg, name, err)
	}
	if code != http.StatusNotFound {
		return fmt.Errorf("github: %s/%s is still there after a delete: status %d", ghOrg, name, code)
	}
	return nil
}
