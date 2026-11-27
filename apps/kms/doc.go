// Package kms is secret custody: your org's secrets sealed at rest, plus threshold
// signing.
//
// Secrets are read and written over /v1/kms; signing is done by the MPC ring.
//
// It embeds luxfi/kms in-process inside the unified Hanzo Cloud binary per
// HIP-0106 ("all Go embeds in cloud"), replacing the legacy Infisical fork.
//
// It has two faces, both backed by the SAME embedded luxfi/kms library:
//
//	KMSClient  — the in-process cloud.Store (GetSecret/PutSecret/Sign) other
//	             subsystems call via deps.KMS. No RPC, no external DB. Built once by
//	             the factory this package registers (init, mount.go), filled into
//	             deps.KMS by build.go's BuildDeps before UseAll, and reused by Mount.
//	/v1/kms/*  — the secrets-manager REST surface the KMS console (kms.hanzo.ai)
//	             calls, mounted onto cloud's Fiber app: JWT-gated, org-scoped
//	             secrets CRUD + a real health probe + the SPA admin config (mount.go).
//
// STORAGE — sealed secrets persist to PER-ORG SQLite (store.go): each org's
// secrets live in ITS OWN encrypted file {CLOUD_DATA_DIR}/orgs/{org}/kms.db via
// the canonical cloud.OrgDB → cek client, mirroring clients/finance. This REPLACES
// the single embedded ZapDB KV, whose exclusive OS lock pinned cloud to
// replicas=1: a per-org SQLite file has no single-opener lock, so different pods
// can serve different tenants and cloud scales horizontally (consistent-hash
// org→pod; see the package report). Two layers of at-rest protection, both rooted
// in the SAME env-only master key: (1) each secret is sealed with an AES-256-GCM
// envelope (store.Seal: a fresh per-secret DEK wrapped by the master key) BEFORE
// it reaches SQLite — plaintext never touches disk; (2) cek opens each file
// SQLCipher-encrypted under a per-db DEK wrapped by the master key (defense in
// depth). No PostgreSQL, no external DB, no ZapDB.
//
// BOOTSTRAP — cloud hosting the secret store is a chicken-and-egg: cloud cannot
// fetch its OWN master key from the KMS it hosts. The 32-byte master key is
// injected by the operator via a K8s Secret env (CLOUD_KMS_MASTER_KEY_REF,
// base64 of 32 bytes) and read ONLY from env — never from the store, never
// logged, never persisted in plaintext. When the master key is absent the
// subsystem mounts in a fail-closed HEALTH-ONLY mode: /v1/kms/health reports 503,
// every secret op returns a clear "master key not configured" error, and the
// store is backed by an EPHEMERAL in-memory KV (never an unencrypted on-disk one),
// so a later keyed boot opens a clean encrypted store rather than a bricked one.
// Never a silent insecure default.
//
// SIGN — luxfi/kms's Sign is MPC-backed (threshold signing via the MPC daemon).
// cloud does not co-host the MPC cluster; Sign therefore fails CLOSED with a
// clear error whenever the MPC backend is not configured (CLOUD_KMS_MPC_ADDR /
// CLOUD_KMS_MPC_VAULT_ID unset). A signature is NEVER fabricated.
//
// SECURITY — the REST surface (mount.go) reuses cloud's ONE auth boundary
// (SanitizeIdentity in serve.go establishes the validated principal; each op
// reads it through principal.OrgFrom and cloud.AuthorityOf) rather than a
// parallel JWT stack. The admission is inside the operation, not around the
// route, because an operation is also an MCP tool and a call-plane op and those
// reach it without one.
//
// LAYERING — the library face (Client/New here in kms.go) is the cloud-free CLIENT
// core: the types.Store impl + sealed store access, importing only cloud/types.
// The REST face (mount.go) imports cloud to mount /v1/kms/* and register the
// subsystem. build.go does NOT import this package; it receives the embedded-client
// constructor via cloud.RegisterKMSClientFactory (init, mount.go), so deps.KMS is
// built by BuildDeps before UseAll with no cloud⇄kms import cycle — the same
// inversion cloud already uses to mount every subsystem it never imports.
package kms
