// Package git is Git hosting for your org: create repos, clone, push, and see what
// they cost.
//
// It mounts the Hanzo Cloud /v1/git surface: S3-backed Git hosting native in the
// unified cloud binary — Hanzo Git, the internal git host foundation agents push
// code into.
//
// A repo is the Git LAYER (source code, buildable/deployable) that lives UNDER
// an IAM project. It is NOT the IAM project itself: `project` is org-scoping
// CONTEXT (org → project → env); a repo is scoped BY that context. Every repo
// belongs to exactly one org (the gateway-minted X-Org-Id, HIP-0026) and an
// optional project sub-scope (X-Project-Id), enforced on every query, so one
// org can never read, clone, push to, or delete another's repos.
//
// Surface:
//
//	POST   /v1/git/repos            create a bare repo            -> repoView (201)
//	GET    /v1/git/repos            list the org's repos       -> {data:[repoView]}
//	GET    /v1/git/repos/:name      repo detail (branches, HEAD)  -> repoView
//	DELETE /v1/git/repos/:name      delete + purge storage        -> 204
//	GET    /v1/git/usage            per-repo + total bytes        -> usageView
//
// Smart-HTTP git protocol (so `git clone` / `git push` work natively):
//
//	GET  /v1/git/:org/:repo/info/refs?service=git-upload-pack|git-receive-pack
//	POST /v1/git/:org/:repo/git-upload-pack     (clone/fetch)
//	POST /v1/git/:org/:repo/git-receive-pack    (push)
//
// A project-scoped repo names its project as a middle segment
// (/v1/git/:org/:project/:repo/…, git@host:org/project/repo.git). The scope
// otherwise rides X-Project-Id, and a git client sends no headers, so the path
// is the only channel that reaches a remote. Two names are only unique within
// one project — hanzo/hanzo-apps/ai and hanzo/hanzo-docs/ai are distinct repos.
//
// Storage: a repository whose history is here is rows of the org's objects store
// (apps/git/objects), a schema of hanzo-sql, so a replica holds no repository on its
// disk and any replica serves any repository (vault.go, serve.go). The bare directory
// under {DataDir}/git is the cache of a linked repository, which the `git` CLI fills and
// reads (gitexec.go), and the legacy copy of a repository made before the store
// existed, imported on first use and left read-only.
//
// Billing: every repo tracks sizeBytes, re-measured on create and after each
// push. /v1/git/usage exposes per-repo + total bytes per org, and each
// measurement emits a "git.usage" log line a metering consumer can bill on.
package git
