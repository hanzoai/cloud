package main

// reseal.go — the RUN orchestration: for each CR-derived target, authenticate with
// the target's OWN org-bound credential, GET the plaintext from the standalone,
// POST it into cloud so CLOUD seals it. Idempotent (cloud upserts), re-runnable.
//
// Plaintext transits a []byte in memory only: it is read from src, written to dst,
// then wiped. It is never logged (results carry coordinates + status, never values)
// and never touches disk.
//
// The token is per-CREDENTIAL and cached: each org's targets authenticate under
// that org's machine identity (owner==org), enforced by both faces' guards, so a
// bug cannot cross tenants — the token itself is org-bound.

import (
	"context"
	"flag"
	"fmt"
	"sort"
	"strings"
)

// resealOutcome classifies one target's migration.
type resealOutcome string

const (
	outMigrated resealOutcome = "migrated" // read from src, written+sealed into cloud
	outAbsent   resealOutcome = "absent"   // 404 at source — nothing to migrate (already broken at source)
	outFailed   resealOutcome = "failed"   // transport/auth/write error
	outPlanned  resealOutcome = "planned"  // --plan: would migrate, no network
)

type resealResult struct {
	Coord   Coord
	Outcome resealOutcome
	Detail  string // status/error text — NEVER a secret value
}

type resealReport struct {
	Results  []resealResult
	Migrated int
	Absent   int
	Failed   int
	Planned  int
}

func (r *resealReport) add(res resealResult) {
	r.Results = append(r.Results, res)
	switch res.Outcome {
	case outMigrated:
		r.Migrated++
	case outAbsent:
		r.Absent++
	case outFailed:
		r.Failed++
	case outPlanned:
		r.Planned++
	}
}

// tokenFunc returns an org-bound bearer for a target's credential. Cached upstream
// so a credential logs in once. In tests this is a trivial closure.
type tokenFunc func(ctx context.Context, t Target) (string, error)

// reseal migrates every explicit target and resolves every folder target, reading
// from src and writing (sealing) into dst. It never stops on a single target's
// failure — every result is recorded so the report is complete and the run is
// re-runnable. plan=true performs NO network I/O (just enumerates the work).
func reseal(ctx context.Context, inv Inventory, src, dst *kmsClient, tokenFor tokenFunc, plan bool) *resealReport {
	rep := &resealReport{}

	// Explicit targets.
	for _, t := range inv.Targets {
		if plan {
			rep.add(resealResult{Coord: t.Coord(), Outcome: outPlanned})
			continue
		}
		rep.add(resealOne(ctx, t, src, dst, tokenFor))
	}

	// Folder targets: LIST the source folder to discover keys, then migrate each.
	for _, f := range inv.Folders {
		if plan {
			rep.add(resealResult{Coord: f.Coord(), Outcome: outPlanned, Detail: "folder (keys resolved at run)"})
			continue
		}
		tok, err := tokenFor(ctx, f)
		if err != nil {
			rep.add(resealResult{Coord: f.Coord(), Outcome: outFailed, Detail: "auth: " + err.Error()})
			continue
		}
		keys, err := src.listFolder(ctx, tok, f.Org, f.Path, f.Env)
		if err != nil {
			rep.add(resealResult{Coord: f.Coord(), Outcome: outFailed, Detail: "list folder: " + err.Error()})
			continue
		}
		if len(keys) == 0 {
			rep.add(resealResult{Coord: f.Coord(), Outcome: outAbsent, Detail: "folder empty at source"})
			continue
		}
		sort.Strings(keys)
		for _, k := range keys {
			t := f
			t.Folder = false
			t.Key = k
			rep.add(resealOneWithToken(ctx, t, src, dst, tok))
		}
	}
	return rep
}

// resealOne authenticates for the target then migrates it.
func resealOne(ctx context.Context, t Target, src, dst *kmsClient, tokenFor tokenFunc) resealResult {
	tok, err := tokenFor(ctx, t)
	if err != nil {
		return resealResult{Coord: t.Coord(), Outcome: outFailed, Detail: "auth: " + err.Error()}
	}
	return resealOneWithToken(ctx, t, src, dst, tok)
}

// resealOneWithToken performs the GET(src)→POST(dst) for one target with an already
// resolved token. The plaintext is wiped after the write.
func resealOneWithToken(ctx context.Context, t Target, src, dst *kmsClient, tok string) resealResult {
	val, err := src.getSecret(ctx, tok, t.Org, t.Path, t.Env, t.Key)
	if err == errSecretNotFound {
		return resealResult{Coord: t.Coord(), Outcome: outAbsent, Detail: "not found at source"}
	}
	if err != nil {
		return resealResult{Coord: t.Coord(), Outcome: outFailed, Detail: "read: " + err.Error()}
	}
	defer wipe(val)
	if err := dst.putSecret(ctx, tok, t.Org, t.Path, t.Env, t.Key, val); err != nil {
		return resealResult{Coord: t.Coord(), Outcome: outFailed, Detail: "write: " + err.Error()}
	}
	return resealResult{Coord: t.Coord(), Outcome: outMigrated}
}

// wipe zeroes a plaintext buffer once it is no longer needed (defense in depth;
// the value already never leaves memory).
func wipe(b []byte) {
	for i := range b {
		b[i] = 0
	}
}

// ── CLI ─────────────────────────────────────────────────────────────────────────

func runReseal(args []string) error {
	fs := flag.NewFlagSet("reseal", flag.ContinueOnError)
	crsPath := fs.String("crs", "", "KMSSecret CR JSON (file or '-')")
	kubectlMode := fs.Bool("kubectl", false, "load CRs live via kubectl instead of --crs")
	srcURL := fs.String("src", "", "standalone KMS base URL (read source), e.g. http://kms.hanzo.svc")
	cloudURL := fs.String("cloud", "", "cloud embedded KMS base URL (seal destination), e.g. http://cloud.hanzo.svc")
	onlyHost := fs.String("only-host", "", "migrate only CRs whose hostAPI matches this (excludes e.g. the devnet KMS)")
	plan := fs.Bool("plan", false, "enumerate the work without any network I/O or writes")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if !*plan {
		if *srcURL == "" || *cloudURL == "" {
			return fmt.Errorf("--src and --cloud are required (unless --plan)")
		}
	}
	crs, err := readCRSource(*crsPath, *kubectlMode)
	if err != nil {
		return err
	}
	inv := filterHost(BuildInventory(crs), *onlyHost)

	ctx := context.Background()
	src := newKMSClient(*srcURL, nil)
	dst := newKMSClient(*cloudURL, nil)
	tokenFor := newK8sTokenFunc(src) // logs in at the source with each CR's credential

	rep := reseal(ctx, inv, src, dst, tokenFor, *plan)
	printResealReport(inv, rep, *plan)
	if rep.Failed > 0 {
		return fmt.Errorf("%d target(s) failed — see report; re-run is safe (idempotent)", rep.Failed)
	}
	return nil
}

// filterHost drops targets/folders whose CR hostAPI does not match onlyHost (when
// set), so the main-standalone cutover never touches the separate devnet KMS.
func filterHost(inv Inventory, onlyHost string) Inventory {
	if strings.TrimSpace(onlyHost) == "" {
		return inv
	}
	keep := func(t Target) bool { return t.HostAPI == onlyHost }
	var ts, fs []Target
	for _, t := range inv.Targets {
		if keep(t) {
			ts = append(ts, t)
		}
	}
	for _, f := range inv.Folders {
		if keep(f) {
			fs = append(fs, f)
		}
	}
	inv.Targets = ts
	inv.Folders = fs
	inv.UniqueCount = len(ts)
	return inv
}

func printResealReport(inv Inventory, rep *resealReport, plan bool) {
	if plan {
		fmt.Printf("PLAN: %d explicit target(s) + %d folder(s) would migrate (no network I/O performed)\n",
			len(inv.Targets), len(inv.Folders))
	} else {
		fmt.Printf("RESEAL: migrated=%d absent=%d failed=%d (of %d explicit + %d folders)\n",
			rep.Migrated, rep.Absent, rep.Failed, len(inv.Targets), len(inv.Folders))
	}
	// Only non-migrated outcomes are itemized (a value-free coordinate + reason).
	for _, r := range rep.Results {
		if r.Outcome == outMigrated || r.Outcome == outPlanned {
			continue
		}
		fmt.Printf("  [%s] %s — %s\n", r.Outcome, r.Coord, r.Detail)
	}
}
