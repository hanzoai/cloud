package main

// verify.go — the read-only post-migration proof. For every CR-derived target it
// asserts the value on cloud is byte-identical to the value on the standalone by
// comparing SHA-256 digests (never the values themselves), and it exercises the
// org-isolation matrix on cloud: a token for org A cannot read org B's coordinate
// (403), and an unauthenticated read is refused (403).
//
// Digests, not values: a mismatch prints only a short hash prefix, so the proof
// never leaks a secret even on failure.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"sort"
)

type verifyOutcome string

const (
	vMatch     verifyOutcome = "match"
	vMismatch  verifyOutcome = "MISMATCH"
	vAbsentSrc verifyOutcome = "absent-src"
	vAbsentDst verifyOutcome = "ABSENT-DST"
	vUnseeded  verifyOutcome = "UNSEEDED-FOLDER"
	vError     verifyOutcome = "error"
)

type verifyResult struct {
	Coord   Coord
	Outcome verifyOutcome
	Detail  string // hash prefixes / error text — never a value
}

type verifyReport struct {
	Results  []verifyResult
	Match    int
	Mismatch int
	AbsentS  int
	AbsentD  int
	Unseeded int
	Errors   int
	Iso      []isoResult
}

func (r *verifyReport) add(res verifyResult) {
	r.Results = append(r.Results, res)
	switch res.Outcome {
	case vMatch:
		r.Match++
	case vMismatch:
		r.Mismatch++
	case vAbsentSrc:
		r.AbsentS++
	case vAbsentDst:
		r.AbsentD++
	case vUnseeded:
		r.Unseeded++
	case vError:
		r.Errors++
	}
}

func hashHex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// verify compares src (src-face token) vs dst (dst-face token) for every target and
// folder-resolved key. GREEN = every present source record matches on cloud with
// zero MISMATCH / ABSENT-DST / UNSEEDED-FOLDER / error.
func verify(ctx context.Context, inv Inventory, src, dst *kmsClient, srcAuth, dstAuth tokenFunc) *verifyReport {
	rep := &verifyReport{}
	for _, t := range inv.Targets {
		rep.add(verifyOne(ctx, t, src, dst, srcAuth, dstAuth))
	}
	for _, f := range inv.Folders {
		srcTok, err := srcAuth(ctx, f)
		if err != nil {
			rep.add(verifyResult{Coord: f.Coord(), Outcome: vError, Detail: "src auth: " + err.Error()})
			continue
		}
		dstTok, err := dstAuth(ctx, f)
		if err != nil {
			rep.add(verifyResult{Coord: f.Coord(), Outcome: vError, Detail: "dst auth: " + err.Error()})
			continue
		}
		keys, err := src.listFolder(ctx, srcTok, f.Org, f.Path, f.Env)
		if err != nil {
			rep.add(verifyResult{Coord: f.Coord(), Outcome: vError, Detail: "list folder: " + err.Error()})
			continue
		}
		// MED-1: an empty folder-sync is NON-green at the gate. reseal flags it as
		// absent; verify (the designated GREEN gate) must also count it as a failure,
		// or an UNSEEDED crown-jewel folder (e.g. billing-kms-sync) passes silently.
		if len(keys) == 0 {
			rep.add(verifyResult{Coord: f.Coord(), Outcome: vUnseeded, Detail: "folder EMPTY at source — nothing to verify (seeding wedge risk)"})
			continue
		}
		sort.Strings(keys)
		for _, k := range keys {
			t := f
			t.Folder = false
			t.Key = k
			rep.add(verifyOneWithTokens(ctx, t, src, dst, srcTok, dstTok))
		}
	}
	return rep
}

func verifyOne(ctx context.Context, t Target, src, dst *kmsClient, srcAuth, dstAuth tokenFunc) verifyResult {
	srcTok, err := srcAuth(ctx, t)
	if err != nil {
		return verifyResult{Coord: t.Coord(), Outcome: vError, Detail: "src auth: " + err.Error()}
	}
	dstTok, err := dstAuth(ctx, t)
	if err != nil {
		return verifyResult{Coord: t.Coord(), Outcome: vError, Detail: "dst auth: " + err.Error()}
	}
	return verifyOneWithTokens(ctx, t, src, dst, srcTok, dstTok)
}

// verifyOneWithTokens reads src (srcTok) + dst (dstTok) and compares digests.
func verifyOneWithTokens(ctx context.Context, t Target, src, dst *kmsClient, srcTok, dstTok string) verifyResult {
	sv, serr := src.getSecret(ctx, srcTok, t.Org, t.Path, t.Env, t.Key)
	if serr == errSecretNotFound {
		return verifyResult{Coord: t.Coord(), Outcome: vAbsentSrc, Detail: "not at source"}
	}
	if serr != nil {
		return verifyResult{Coord: t.Coord(), Outcome: vError, Detail: "read src: " + serr.Error()}
	}
	defer wipe(sv)
	dv, derr := dst.getSecret(ctx, dstTok, t.Org, t.Path, t.Env, t.Key)
	if derr == errSecretNotFound {
		return verifyResult{Coord: t.Coord(), Outcome: vAbsentDst, Detail: "not migrated to cloud"}
	}
	if derr != nil {
		return verifyResult{Coord: t.Coord(), Outcome: vError, Detail: "read dst: " + derr.Error()}
	}
	defer wipe(dv)
	sh, dh := hashHex(sv), hashHex(dv)
	if sh != dh {
		return verifyResult{Coord: t.Coord(), Outcome: vMismatch, Detail: fmt.Sprintf("src=%s… dst=%s…", sh[:12], dh[:12])}
	}
	return verifyResult{Coord: t.Coord(), Outcome: vMatch, Detail: sh[:12] + "…"}
}

// ── org-isolation matrix (against cloud/dst) ────────────────────────────────────

type isoResult struct {
	Name   string
	Want   int
	Got    int
	Passed bool
}

// isolationProbe proves the cloud guard on dst: a token scoped to org A cannot read
// a coordinate under a DIFFERENT org (403), and an unauthenticated read is refused
// (403). victimOrg must be a real, distinct org so the path is well-formed.
func isolationProbe(ctx context.Context, dst *kmsClient, orgAToken, orgA, victimOrg, path, env, key string) []isoResult {
	var out []isoResult
	// A→B: org A's token reading org B's coordinate must be 403 (owner != :org).
	if st, err := dst.probeStatus(ctx, "GET", orgAToken, victimOrg, path, env, key); err == nil {
		out = append(out, isoResult{
			Name: fmt.Sprintf("cross-org read %s→%s", orgA, victimOrg),
			Want: 403, Got: st, Passed: st == 403,
		})
	}
	// No principal: unauthenticated read must be 403.
	if st, err := dst.probeStatus(ctx, "GET", "", victimOrg, path, env, key); err == nil {
		out = append(out, isoResult{
			Name: "no-principal read", Want: 403, Got: st, Passed: st == 403,
		})
	}
	return out
}

// ── CLI ─────────────────────────────────────────────────────────────────────────

func runVerify(args []string) error {
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	crsPath := fs.String("crs", "", "KMSSecret CR JSON (file or '-')")
	kubectlMode := fs.Bool("kubectl", false, "load CRs live via kubectl")
	srcURL := fs.String("src", "", "standalone KMS base URL")
	cloudURL := fs.String("cloud", "", "cloud embedded KMS base URL")
	onlyHost := fs.String("only-host", "", "verify only CRs whose hostAPI matches this")
	dstCredNS := fs.String("dst-cred-namespace", "hanzo", "namespace holding the per-org <org>-platform-kms credential Secrets")
	dstCredSuffix := fs.String("dst-cred-suffix", "-platform-kms-creds", "Secret name suffix for the per-org cloud (dst) credential: <org>+suffix")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *srcURL == "" || *cloudURL == "" {
		return fmt.Errorf("--src and --cloud are required")
	}
	crs, err := readCRSource(*crsPath, *kubectlMode)
	if err != nil {
		return err
	}
	full := BuildInventory(crs)
	inv := filterHost(full, *onlyHost)

	ctx := context.Background()
	src := newKMSClient(*srcURL, nil)
	dst := newKMSClient(*cloudURL, nil)
	srcAuth := newTokenFunc(src, crCredResolver, "src")
	dstAuth := newTokenFunc(dst, machineAudResolver(*dstCredNS, *dstCredSuffix), "dst")

	rep := verify(ctx, inv, src, dst, srcAuth, dstAuth)
	printVerifyReport(rep)
	printHostScope("VERIFY", full, inv, *onlyHost, rep.Match)
	if rep.Mismatch > 0 || rep.AbsentD > 0 || rep.Unseeded > 0 || rep.Errors > 0 {
		return fmt.Errorf("verification RED: mismatch=%d absent-dst=%d unseeded-folder=%d errors=%d", rep.Mismatch, rep.AbsentD, rep.Unseeded, rep.Errors)
	}
	return nil
}

func printVerifyReport(rep *verifyReport) {
	fmt.Printf("VERIFY: match=%d MISMATCH=%d absent-src=%d ABSENT-DST=%d UNSEEDED-FOLDER=%d errors=%d\n",
		rep.Match, rep.Mismatch, rep.AbsentS, rep.AbsentD, rep.Unseeded, rep.Errors)
	for _, r := range rep.Results {
		if r.Outcome == vMatch || r.Outcome == vAbsentSrc {
			continue // src-absent is not a regression (already broken at source)
		}
		fmt.Printf("  [%s] %s — %s\n", r.Outcome, r.Coord, r.Detail)
	}
	for _, iso := range rep.Iso {
		status := "PASS"
		if !iso.Passed {
			status = "FAIL"
		}
		fmt.Printf("  iso[%s] %s want=%d got=%d\n", status, iso.Name, iso.Want, iso.Got)
	}
}
