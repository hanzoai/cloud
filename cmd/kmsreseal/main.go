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

import (
	"fmt"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	cmd := os.Args[1]
	args := os.Args[2:]
	var err error
	switch cmd {
	case "inventory":
		err = runInventory(args)
	case "preflight":
		err = runPreflight(args)
	case "reseal":
		err = runReseal(args)
	case "verify":
		err = runVerify(args)
	case "runbook":
		printRunbook()
	case "-h", "--help", "help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "kmsreseal: unknown subcommand %q\n\n", cmd)
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "kmsreseal %s: %v\n", cmd, err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `kmsreseal — CR-driven KMS re-seal migration (#79)

usage:
  kmsreseal inventory --crs <file|-> [--kubectl] [--only-host <url>]
  kmsreseal preflight --cloud <url> [--src <url>]
  kmsreseal reseal    --crs <file> --src <url> --cloud <url> [--only-host <url>] [--plan]
  kmsreseal verify    --crs <file> --src <url> --cloud <url> [--only-host <url>]
  kmsreseal runbook

Secret plaintext transits memory only — never written to disk, never logged.
`)
}

// readCRSource loads CR JSON from a file path, "-" (stdin), or the live cluster
// via kubectl when kubectlMode is set. It never fetches secret VALUES — a
// KMSSecret CR carries only coordinates (org/path/env/key names) + references.
func readCRSource(path string, kubectlMode bool) ([]cr, error) {
	if kubectlMode {
		return loadCRsFromKubectl()
	}
	if path == "" {
		return nil, fmt.Errorf("--crs <file|-> is required (or --kubectl)")
	}
	var data []byte
	var err error
	if path == "-" {
		data, err = os.ReadFile("/dev/stdin")
	} else {
		data, err = os.ReadFile(path)
	}
	if err != nil {
		return nil, fmt.Errorf("read CR source: %w", err)
	}
	return parseCRs(data)
}
