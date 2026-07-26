package tracker

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/principal"
)

// github_sink.go implements cloud.IssueSink: it mirrors an external work item (today
// a GitHub issue, via the App webhook + backfill in clients/integrations) into THIS
// native tracker, idempotently by ExtRef. It is the tracker's side of the
// tracker_seam inversion — integrations never imports tracker; it calls
// cloud.UpsertIssue, which lands here.
//
// One team, repo as the discriminator: every mirrored GitHub issue for an org files
// under ONE tracker team (ProjectKey, e.g. "GH") in the org's DEFAULT project store,
// with Repo set to the source repo — so a repo's Issues tab is the EXISTING
// ListIssues(Filter{Source:"git", Repo}) view, never a second store. The upstream
// open/closed State maps to a board column HERE (the tracker owns its status
// vocabulary); the feeder stays provider-shaped, this stays tracker-shaped.

// registerIssueSink wires the sink. Called from Mount once `mounted` is set, so the
// sink never runs before its store cache exists.
func registerIssueSink() { cloud.RegisterIssueSink(upsertIssueSeam) }

// stateToStatus maps an upstream open/closed state to a tracker board column: open
// work is actionable (todo), closed work is done. Anything else (defensive) → todo.
func stateToStatus(state string) string {
	if strings.EqualFold(strings.TrimSpace(state), "closed") {
		return "done"
	}
	return "todo"
}

// upsertIssueSeam is the registered cloud.IssueSink: ensure the target team, find any
// existing row for the ExtRef, and create or update it — the ONE mirror upsert,
// shared by the webhook (one issue) and the backfill (many). Idempotent by ExtRef.
func upsertIssueSeam(ctx context.Context, in cloud.IssueUpsert) (cloud.IssueUpsertResult, error) {
	s := mounted
	if s == nil {
		return cloud.IssueUpsertResult{}, cloud.ErrIssueSinkUnavailable
	}
	org := strings.TrimSpace(in.Org)
	extRef := strings.TrimSpace(in.ExtRef)
	if org == "" || extRef == "" {
		return cloud.IssueUpsertResult{}, errors.New("tracker upsert: org and extRef are required")
	}
	proj := strings.TrimSpace(in.Project)
	if proj == "" {
		proj = principal.DefaultProject // GitHub issues are org-level → the default project store
	}
	store, err := s.State.stores.For(org, proj)
	if err != nil {
		return cloud.IssueUpsertResult{}, err
	}
	p, err := ensureProject(ctx, store, org, in.ProjectKey, in.ProjectName)
	if err != nil {
		return cloud.IssueUpsertResult{}, err
	}

	status := stateToStatus(in.State)
	labels := strings.Join(cleanLabels(in.Labels), ",")
	title := clampStr(strings.TrimSpace(in.Title), maxTitle)
	if title == "" {
		title = clampStr(extRef, maxTitle) // an issue MUST carry a title; the anchor is the fallback
	}
	desc := clampStr(in.Description, maxDesc)
	assignee := clampStr(strings.TrimSpace(in.Assignee), maxField)
	now := time.Now().Unix()

	existing, err := store.GetIssueByExtRef(ctx, org, p.ID, extRef)
	if err == nil {
		// Update the mutable board state; identity (kind/source/repo/extRef/number) is
		// immutable, so a re-sync never migrates the row between surfaces.
		existing.Title, existing.Description, existing.Status = title, desc, status
		existing.Assignee, existing.Labels, existing.UpdatedAt = assignee, labels, now
		if uerr := store.UpdateIssue(ctx, existing); uerr != nil {
			return cloud.IssueUpsertResult{}, uerr
		}
		return cloud.IssueUpsertResult{Number: existing.Number, Identifier: fmt.Sprintf("%s-%d", p.Key, existing.Number)}, nil
	}
	if !errors.Is(err, errNotFound) {
		return cloud.IssueUpsertResult{}, err
	}
	id, err := genID("issue")
	if err != nil {
		return cloud.IssueUpsertResult{}, err
	}
	created, err := store.CreateIssue(ctx, Issue{
		ID: id, ProjectID: p.ID, Org: org,
		Kind: normKindDefault(in.Kind), Source: "git",
		Repo: clampStr(strings.TrimSpace(in.Repo), maxField), ExtRef: extRef,
		Title: title, Description: desc, Status: status, Priority: "none",
		Assignee: assignee, Labels: labels, CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		return cloud.IssueUpsertResult{}, err
	}
	return cloud.IssueUpsertResult{Created: true, Number: created.Number, Identifier: fmt.Sprintf("%s-%d", p.Key, created.Number)}, nil
}

// ensureProject resolves the mirror's target team by key, creating it once on first
// use. The key is normalized to the tracker's ^[A-Z][A-Z0-9]{1,7}$ shape (defaulting
// to "GH"); a concurrent create is absorbed (read the row back on conflict).
func ensureProject(ctx context.Context, store *Store, org, key, name string) (Project, error) {
	key = strings.ToUpper(strings.TrimSpace(key))
	if !keyRE.MatchString(key) {
		key = "GH"
	}
	if p, err := store.GetProject(ctx, org, key); err == nil {
		return p, nil
	} else if !errors.Is(err, errNotFound) {
		return Project{}, err
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = "GitHub"
	}
	id, err := genID("prj")
	if err != nil {
		return Project{}, err
	}
	now := time.Now().Unix()
	p := Project{ID: id, Org: org, Key: key, Name: clampStr(name, maxField),
		Description: "Mirrored external issues.", CreatedAt: now, UpdatedAt: now}
	if err := store.CreateProject(ctx, p); err != nil {
		if errors.Is(err, errConflict) {
			return store.GetProject(ctx, org, key) // raced with another mirror op — read it back
		}
		return Project{}, err
	}
	return p, nil
}

// cleanLabels is the lenient sink-side label normalizer (vs the strict HTTP
// normLabels): it trims, drops empties, folds the storage-separator comma to a space,
// and caps length — so an odd upstream label degrades gracefully instead of failing
// the whole mirror.
func cleanLabels(in []string) []string {
	out := make([]string, 0, len(in))
	for _, l := range in {
		l = strings.TrimSpace(strings.ReplaceAll(l, ",", " "))
		if l == "" {
			continue
		}
		out = append(out, clampStr(l, maxLabel))
	}
	return out
}

// clampStr caps s to n bytes, dropping any partial trailing rune so the result is
// always valid UTF-8 (the DB stores TEXT).
func clampStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return strings.ToValidUTF8(s[:n], "")
}

// normKindDefault clamps an external kind to the tracker's closed set, defaulting to
// "issue" (a GitHub issue is "issue"; a mirrored PR is "pr").
func normKindDefault(k string) string {
	k = strings.ToLower(strings.TrimSpace(k))
	if kinds[k] {
		return k
	}
	return "issue"
}
