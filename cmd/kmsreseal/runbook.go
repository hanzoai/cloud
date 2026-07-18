package main

// runbook.go — the tool prints its OWN ordered cutover runbook. The migration is
// a security UPGRADE (the live standalone stores 125 fleet secrets UNSEALED at
// rest; cloud seals each per-secret with an AES-256-GCM envelope) executed as a
// CR-driven RE-SEAL, never a raw store copy. The standalone is READ-ONLY through
// the whole cutover and is the rollback: its ZapDB is never mutated.
//
// The actual RUN steps (4/5) are this tool; the cutover (7/8) and freeze/scale
// (1/10) are operator + ingress + kubectl actions the CTO gates. Kept as code
// (printed by `kmsreseal runbook`) rather than a stray doc, so the tool is the one
// home for its own operational contract.

const runbookText = `KMS EMBED — standalone (kms.hanzo.svc) → cloud embedded /v1/kms — ordered cutover
================================================================================
SCOPE: the 518 prod (org,path,env,key) rows across ~113 explicit-key + 12 folder
CRs whose hostAPI is the main standalone. The 12 devnet rows (kms.hanzo-devnet.svc,
org=hanzo-vector) are a SEPARATE KMS — NOT in this cutover.

INVARIANTS (hold at every step):
  * The standalone is READ-ONLY. Its ZapDB is never written. It is the rollback.
  * No secret plaintext is ever written to disk or logged — sealed-in-cloud is the
    only at-rest form. Plaintext transits tool memory only during re-seal.
  * cloud's embedded-KMS master key stays a DIRECT k8s Secret (cloud-kms-master-key),
    NEVER KMS-synced — so cloud can always boot its own KMS (no cold-start cycle).

PRE-REQ (already true / verify):
  P1. cloud embedded /v1/kms is live and READY (GET /v1/kms/health = 200), master
      key injected via CLOUD_KMS_MASTER_KEY_REF (direct Secret cloud-kms-master-key).
  P2. cloud + cloud-reader run the embedded KMS build.

  1. FREEZE. Announce the window. Pause net-new secret WRITES to the standalone
     (the kms-operator only READS + syncs; Orphan managed Secrets already persist).
     The standalone keeps SERVING reads — it is hot for rollback.

  2. SNAPSHOT. Back up the standalone kms pod's ZapDB PVC (immutable rollback
     artifact). From here the standalone is never mutated.

  3. APPLY G1 (audience parity). Patch the cloud App CR: add CLOUD_JWT_AUDIENCES =
     union(current GATEWAY_ALLOWED_AUDIENCES, the standalone's KMS_EXPECTED_AUDIENCE
     list). Roll cloud + cloud-reader. Verify: a real fleet machine token now
     validates on cloud /v1/kms (200 on its own org; 401 without a token). Issuer
     already covered by cloud's BrandIssuers set — no issuer change.

  4. RE-SEAL (this tool, re-runnable + idempotent):
       kmsreseal reseal --crs <live-crs.json> \
         --src http://kms.hanzo.svc --cloud http://cloud.hanzo.svc \
         --only-host http://kms.hanzo.svc
     Per CR: authenticate with the CR's OWN org-bound machine credential
     (owner==projectSlug), GET each (path,env,key) plaintext from the standalone,
     POST into cloud so CLOUD seals it. cloud upserts, so a re-run is safe.

  5. VERIFY (this tool, read-only):
       kmsreseal verify --crs <live-crs.json> \
         --src http://kms.hanzo.svc --cloud http://cloud.hanzo.svc
     Asserts every (org,path,env,key) resolves on cloud byte-identical to the
     standalone (SHA-256 compare, never values), the per-audience auth matrix
     (own-org 200 / cross-org 403 / no-principal 403), and org-isolation. GREEN gate.

  6. CUT cloud's own dependency (G5). Confirm cloud's boot-critical secrets do NOT
     require a live standalone: master key is a DIRECT Secret; cloud-api-secrets +
     commerce-secrets are Orphan (persist across KMS downtime). No boot cycle.

  7. REPOINT the operator. Set kms-operator KMS_API_BASE:
       http://kms.hanzo.svc  →  http://cloud.hanzo.svc   (cloud's /v1/kms)
     Roll kms-operator. It now brokers login + reads secrets from cloud.

  8. REPOINT ingress. Move kms.hanzo.ai (all 4 hostnames/paths) to the cloud
     Service. External KMS console + SDK now terminate at cloud.

  9. SOAK (standalone still HOT). Watch the operator reconcile all 125 CRs GREEN
     against cloud; watch cloud /v1/kms audit + health. Hold for a full resync
     interval (CR resyncInterval, ~60s) plus a safety margin.

 10. SCALE DOWN. kubectl scale deploy/kms --replicas=0 (do NOT delete: keep the
     PVC + the step-2 snapshot). Retire ghcr.io/luxfi/kms once soaked.

ROLLBACK (instant, lossless — the standalone ZapDB was never mutated):
  Flip kms-operator KMS_API_BASE + ingress back to kms.hanzo.svc; scale deploy/kms
  back to 1. The re-seal only WROTE into cloud; nothing on the standalone changed.
`

func printRunbook() { print(runbookText) }
