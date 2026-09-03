package integrations

// github_repository.go turns "a repository became visible" into an OFFER to import
// it, and nothing more.
//
// WHY AN OFFER AND NOT AN IMPORT. A repo the App can now see is not a repo the org
// wants mirrored — importing on sight would mirror every repo anyone creates, which
// is a policy decision the org owns. GET /v1/integration/github/repos already
// lists every granted repo with imported=false, so the ABILITY to import arrives
// with the grant; what was missing is anyone being TOLD. This closes that gap and
// leaves the decision where it was.
//
// The offer is one todo per repo, idempotent by ExtRef, so GitHub's redelivery (and
// a transfer that fires twice) reconciles to one row rather than a pile. Accepting
// it means calling githubImport with that repo — the same path the console's
// import button already takes.

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/zap-proto/zip"
)

// githubRepositoryEvent is the slice of GitHub's `repository` payload this reads.
// Tenancy comes from Installation.ID and nothing else — see githubWebhook.
type githubRepositoryEvent struct {
	Action     string `json:"action"`
	Repository struct {
		Name     string `json:"name"`
		FullName string `json:"full_name"`
		Private  bool   `json:"private"`
		HTMLURL  string `json:"html_url"`
	} `json:"repository"`
	Installation struct {
		ID int64 `json:"id"`
	} `json:"installation"`
}

// handleGitHubRepositoryEvent offers a newly-reachable repo for import.
//
// Only ARRIVALS are offered. `deleted`/`archived`/`privatized` are the repo leaving
// the granted set, and a stale offer for a repo that is gone is worse than none —
// but this does NOT retract the todo either: a human may have started acting on it,
// and the import path already refuses a repo the installation no longer grants.
func handleGitHubRepositoryEvent(c *zip.Ctx, body []byte) error {
	var ev githubRepositoryEvent
	if err := json.Unmarshal(body, &ev); err != nil {
		return zip.ErrBadRequest("invalid repository payload")
	}
	if !offersImport(ev.Action) {
		return c.JSON(http.StatusOK, map[string]any{"ignored": "action"})
	}
	if ev.Installation.ID == 0 {
		return c.JSON(http.StatusOK, map[string]any{"ignored": "no installation"})
	}
	full := strings.TrimSpace(ev.Repository.FullName)
	if full == "" {
		return c.JSON(http.StatusOK, map[string]any{"ignored": "no repository"})
	}
	org, ok := OrgForExternalID("github", strconv.FormatInt(ev.Installation.ID, 10))
	if !ok {
		return c.JSON(http.StatusOK, map[string]any{"ignored": "unknown installation"})
	}
	// Kind stays inside the documented "issue"|"pr" vocabulary; what this IS lives
	// in the title, not in a third kind no reader knows how to render.
	res, err := cloud.UpsertIssue(c.Context(), cloud.IssueUpsert{
		Org:         org,
		ProjectKey:  githubTodoProjectKey,
		ProjectName: githubTodoProjectName,
		Repo:        ev.Repository.Name,
		ExtRef:      "github:" + full + "#import",
		Kind:        "issue",
		Source:      "git",
		Title:       "Import " + full,
		Description: importOffer(full, ev.Repository.HTMLURL, ev.Repository.Private, ev.Action),
		State:       "open",
	})
	if err != nil {
		return zip.Errorf(http.StatusBadGateway, "offer import: %v", err)
	}
	return c.JSON(http.StatusOK, map[string]any{
		"offered": true, "created": res.Created, "repo": full, "action": ev.Action,
	})
}

// importOffer is what a human reads when deciding. It names the repo, says how it
// arrived, and points at the one call that accepts.
func importOffer(fullName, htmlURL string, private bool, action string) string {
	visibility := "public"
	if private {
		visibility = "private"
	}
	var b strings.Builder
	b.WriteString(fullName)
	b.WriteString(" became reachable (")
	b.WriteString(action)
	b.WriteString(", ")
	b.WriteString(visibility)
	b.WriteString(").")
	if htmlURL != "" {
		b.WriteString("\n\n")
		b.WriteString(htmlURL)
	}
	b.WriteString("\n\nImport it into git.hanzo.ai:\n")
	b.WriteString(`POST /v1/integration/github/import {"repos":["`)
	b.WriteString(fullName)
	b.WriteString(`"]}`)
	return b.String()
}

// offersImport reports whether an action put a repo INTO the granted set. The
// departures (deleted, archived, privatized, renamed) are deliberately not offers:
// renamed already has a todo under its old name, and the rest are the repo leaving.
func offersImport(action string) bool {
	switch action {
	case "created", "transferred", "unarchived":
		return true
	}
	return false
}
