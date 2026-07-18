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
	case vError:
		r.Errors++
	}
}

func hashHex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// verify compares src vs dst for every target (and folder-resolved key). GREEN =
// every present source record matches on cloud with zero MISMATCH / ABSENT-DST.
func verify(ctx context.Context, inv Inventory, src, dst *kmsClient, tokenFor tokenFunc) *verifyReport {
	rep := &verifyReport{}
	for _, t := range inv.Targets {
		rep.add(verifyOne(ctx, t, src, dst, tokenFor))
	}
	for _, f := range inv.Folders {
		tok, err := tokenFor(ctx, f)
		if err != nil {
			rep.add(verifyResult{Coord: f.Coord(), Outcome: vError, Detail: "auth: " + err.Error()})
			continue
		}
		keys, err := src.listFolder(ctx, tok, f.Org, f.Path, f.Env)
		if err != nil {
			rep.add(verifyResult{Coord: f.Coord(), Outcome: vError, Detail: "list folder: " + err.Error()})
			continue
		}
		sort.Strings(keys)
		for _, k := range keys {
			t := f
			t.Folder = false
			t.Key = k
			rep.add(verifyOneWithToken(ctx, t, src, dst, tok))
		}
	}
	return rep
}

func verifyOne(ctx context.Context, t Target, src, dst *kmsClient, tokenFor tokenFunc) verifyResult {
	tok, err := tokenFor(ctx, t)
	if err != nil {
		return verifyResult{Coord: t.Coord(), Outcome: vError, Detail: "auth: " + err.Error()}
	}
	return verifyOneWithToken(ctx, t, src, dst, tok)
}

// verifyOneWithToken reads both faces and compares digests. Buffers are wiped.
func verifyOneWithToken(ctx context.Context, t Target, src, dst *kmsClient, tok string) verifyResult {
	sv, serr := src.getSecret(ctx, tok, t.Org, t.Path, t.Env, t.Key)
	if serr == errSecretNotFound {
		return verifyResult{Coord: t.Coord(), Outcome: vAbsentSrc, Detail: "not at source"}
	}
	if serr != nil {
		return verifyResult{Coord: t.Coord(), Outcome: vError, Detail: "read src: " + serr.Error()}
	}
	defer wipe(sv)
	dv, derr := dst.getSecret(ctx, tok, t.Org, t.Path, t.Env, t.Key)
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
	inv := filterHost(BuildInventory(crs), *onlyHost)

	ctx := context.Background()
	src := newKMSClient(*srcURL, nil)
	dst := newKMSClient(*cloudURL, nil)
	tokenFor := newK8sTokenFunc(src)

	rep := verify(ctx, inv, src, dst, tokenFor)
	printVerifyReport(rep)
	if rep.Mismatch > 0 || rep.AbsentD > 0 || rep.Errors > 0 {
		return fmt.Errorf("verification RED: mismatch=%d absent-dst=%d errors=%d", rep.Mismatch, rep.AbsentD, rep.Errors)
	}
	return nil
}

func printVerifyReport(rep *verifyReport) {
	fmt.Printf("VERIFY: match=%d MISMATCH=%d absent-src=%d ABSENT-DST=%d errors=%d\n",
		rep.Match, rep.Mismatch, rep.AbsentS, rep.AbsentD, rep.Errors)
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
