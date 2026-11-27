# LLM.md — apps/deploy

Guidance for apps/deploy. Moved from the root LLM.md; cross-cutting rules stay in the root.

## Two CR kinds, and the documented endpoint had the empty one

`hanzo.ai/v1 App` is the TENANT plane (per-tenant namespace, org-labelled).
`apps.hanzo.ai/v1alpha1 Application` is the PLATFORM plane, what Hanzo CD
reconciles. Production holds **328** of the latter and **zero** of the former, yet
`/v1/deploy/applications` projected only App CRs — so it answered `items: []` for
the whole estate while `/v1/deploy/gitops` held the real list.

They are not interchangeable, so the fix is not to pick a kind: it is to answer for
the CALLER, which `scope.namespaces()` already branches on. A tenant scope is
untouched; a platform SuperAdmin additionally gets the CD plane folded in, from the
same source and the same gate as `/v1/deploy/gitops`. This widens the ENDPOINT,
never the audience — `TestApplicationsDoesNotWidenForATenant` is the half that keeps
it so. `projectCDApp` goes through `observeGitOpsApp`, so the list and the gitops
view can never disagree about sync state, health, or applied revision.

## Deploy plane (`apps/deploy`, `/v1/deploy`)

Native ArgoCD-grade GitOps console over the operator-managed fleet, parallel to
`/v1/git`: each `hanzo.ai/v1` App CR IS the Application, and the plane OBSERVES the
operator's reconcile — `GET /v1/deploy/applications` (fleet list), `/{name}/tree`
(ownerRef resource tree + per-node health/sync), `/{name}/resource/{ref}` (live
manifest + desired-vs-live diff), `/{name}/logs`; `POST /{name}/sync` syncs, and with a
`revision` from the application's own history rolls it back (below). Writes are
SuperAdmin-only, fail-closed, save a site rollback by its own org; Secret nodes are never
surfaced. `engine.go` embeds the argo
`gitops-engine` (`hanzoai/deploy/gitops-engine` v0.7.2, no replace) in-process for the
reconcile half behind `DEPLOY_ENGINE_ENABLED` (default off), with a prune-safety fuse.

### What is live, and what ran for a ref

- `GET /v1/ci/runs/{owner}/{repo}?ref=` (`apps/ci/ref.go`): the ref (branch, tag, sha;
  empty = default branch) is resolved to a commit, then every Actions run on that commit
  is read live from the forge (`forge.Client.Runs`, `Jobs`, in `forge/actions.go`) with
  jobs and steps. Forge tags are not on GitHub, so this is the only place a tag's CI is
  visible. The answer's `state` is the one word for "is this ref green?": `failed` when
  any job or run in the latest attempt of any workflow (grouped by workflow and ref;
  highest run number, then attempt, wins) has a conclusion other than success or skipped;
  else `running` for any `in_progress`, else `queued` for any `queued`/`waiting`, else
  `passed`; `none` when nothing ran. A completed run with no conclusion is `failed`:
  green has to be proven. Each job carries `runner`, the forge's `runner_name`, empty
  until a runner takes it. Scope is `forge.Org(owner)` == the caller's org (SuperAdmin
  reads any), refused 403 before the forge is asked; the read is the machine client, so the gate is the closed
  namespace table, not the forge ACL. `/v1/ci/runs` itself is still the hanzo.ai/ci board
  snapshot (no jobs, no ref filter).

## A rollback is a sync to an earlier revision (#432)

There is no rollback address. `POST /v1/deploy/applications/{name}/sync` takes an
optional body `{"revision": "..."}`; empty syncs to what the source declares, anything
else must come from the application's `status.history` (only a one-application read
answers it, oldest first as argocd orders it) and is the rollback. Every kind resolves
through `resolve.go`:

- CD Application: `k8s.RequestSyncTo` with that commit. Refused (409) for a commit CD never
  deployed, the one it runs, or an Application with automated sync (CD would put git's
  head back on its next poll: revert in git instead). SuperAdmin only.
- App CR: no history, so a revision is 409.
- Site, `site-<slug>` (sites.go): the caller's org's sites, read for the caller from the
  projects app over the plane (`projects_site_apps`, `projects_site_rollback`). A site
  only syncs to a revision: one of the releases its live deployments were kept as
  (apps/projects record.go), which it then serves again. A validated member of the site's
  org may roll it back (the projects app already lets them publish it and activate its
  releases); a SuperAdmin acting in the org may too. The `site-` prefix exists because site
  slugs and CD Application names collide (`hanzo-studio` is both).

A key limit's `deploy` kind names the writes (sdk/grant.go): `deploy:rollback` for a sync
with a revision, `deploy:sync` for one without. `product:deploy,deploy:rollback` is the
least a key can hold and still undo a bad site deploy: this console's reads and the
rollback, no other capability (the boundary refuses `/v1/project`, so no new deploy).

