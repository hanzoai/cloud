// Command kmsreseal is the CR-driven re-seal migration tool for embedding the
// fleet KMS into cloud (#79). It moves the ~125 KMSSecret-referenced secrets from
// the legacy standalone KMS (which stores them UNSEALED at rest) into cloud's
// embedded /v1/kms (which seals each per-secret with an AES-256-GCM envelope) — a
// security UPGRADE performed as an authenticated GET→POST re-seal, never a raw
// store copy.
//
// The migration is driven by the KMSSecret CRs, the authoritative (org,path,env,key)
// manifest: the standalone ZapDB is unreadable by cloud and carries no org
// attribution. Secret plaintext transits tool memory only — never disk, never a log.
//
// Subcommands:
//
//	inventory   parse + validate the CRs, print the manifest + dry-run stats  (read-only, offline)
//	preflight   probe cloud /v1/kms reachability + JWT validation + G1 readiness (read-only)
//	reseal      the RUN: per-CR org-bound auth, GET standalone → POST cloud (seals) (WRITES to cloud)
//	verify      hash-compare every target standalone-vs-cloud + auth/isolation matrix (read-only)
//	runbook     print the ordered cutover runbook
//
// BUILD/TEST/DRY-RUN. `reseal` WRITES into cloud (idempotent upserts) and is the
// only mutating subcommand; the live cutover (operator/ingress repoint, scale-down)
// is out of this tool and CTO-gated.
package main
