// Package authors is a royalty for open-source work: your repo runs, you get paid.
//
// An author links GitHub, proves they own a repo, and earns a royalty on the
// metered spend of every org that deploys a project built from it — accrued per
// period, with a full audit trail behind the number. Accrual records what is OWED;
// paying it is a human act.
//
// It is the CREATOR member of the three programs built on the same shape;
// apps/referrals is the one-time bonus and apps/affiliates the partner commission.
// All three share the commerce ledger path (a credits payout is a grant, tag
// grant:author).
//
// The loop, end to end:
//
//  1. An author CONNECTS GitHub (POST /v1/author/connect): we link the caller's org
//     to a GitHub login — from IAM's linked GitHub account when available (identity
//     verified), else a login the caller supplies (verified per-repo below). We mint
//     a stable per-author VERIFY CODE for the file method.
//  2. The author VERIFIES a repo (POST /v1/author/repos/verify): ownership is proven
//     either by an IAM-linked GitHub token showing ADMIN/PUSH permission on the repo
//     (OAuth method), or by a hanzo.json on the repo's default branch carrying the
//     author's verify code (file method — proves default-branch control). A verified
//     repo can now earn.
//  3. When a published project whose sourceRepo matches a VERIFIED author repo is
//     DEPLOYED by ANY org, the deploy path records it (POST /v1/author/deploys/
//     record): deploying_org↔repo↔project, idempotent per (repo, project, org).
//     hanzo.app persists sourceRepo on the published project so the deploy is
//     attributable.
//  4. The ACCRUAL SWEEP (POST /v1/admin/author/sweep, SuperAdmin) folds over each
//     approved author's DISTINCT deploying orgs (excluding the author's own): royalty =
//     that org's metered spend THIS PERIOD × the author's share (20%), accrued
//     at-most-once per (author, deploying_org, period). Accrual is TRACKING — it records
//     what we owe and issues nothing.
//  5. A PAYOUT (POST /v1/admin/author/:id/payout, SuperAdmin) RECORDS a disbursement
//     against pending royalty, for every method. It moves no money: a human settles the
//     recorded payout out of band. A payout can never exceed pending (accrued − paid),
//     guarded atomically.
//
// HANZO FORKS. A repo whose owner is a brand org (owner ∈ {hanzoai, hanzo-*}) is
// auto-attributed on first deploy to the treasury SYSTEM author (org = the brand slug),
// so Hanzo earns 20% on its OWN templates when other orgs deploy them — recorded, not
// settled.
//
// Surface:
//
//	GET  /v1/author                       (org)          my status, login, verified, repos, deploys, accrued/pending/paid, payouts
//	GET  /v1/author/basis                 (org)          why my number is my number: share, cost model, immutable rows, reconciliation
//	POST /v1/author/connect               (org)          link GitHub (IAM-linked account or supplied login) + mint verify code
//	POST /v1/author/repos/verify          (org)          verify repo ownership (oauth admin-check OR hanzo.json file)
//	POST /v1/author/deploys/record        (org=deployer) record a deploy of a verified author repo (provenance → royalty)
//	GET  /v1/admin/author                  (SuperAdmin) every author + a summary
//	POST /v1/admin/author/sweep            (SuperAdmin) accrue royalty for every deploying org this period
//	POST /v1/admin/author/:id/approve      (SuperAdmin) admit to earning (+ optional share override)
//	POST /v1/admin/author/:id/suspend      (SuperAdmin) suspend
//	POST /v1/admin/author/:id/payout       (SuperAdmin) RECORD a payout (record-only; a human settles it)
//	GET  /v1/admin/author/:id/basis        (SuperAdmin) the SAME basis payload the author reads (support mirror)
//
// serve.go auto-registers GET /v1/author/health.
package author
