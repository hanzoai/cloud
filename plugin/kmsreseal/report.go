package main

// report.go — the read-only, offline-first subcommands: `inventory` (parse +
// validate + print the dry-run manifest) and `preflight` (probe cloud /v1/kms
// reachability + JWT validation, and compute the exact G1 audience delta).

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"sort"
	"strings"
)

// runInventory validates the CRs and prints the dry-run manifest. No network.
func runInventory(args []string) error {
	fs := flag.NewFlagSet("inventory", flag.ContinueOnError)
	crsPath := fs.String("crs", "", "KMSSecret CR JSON (file or '-')")
	kubectlMode := fs.Bool("kubectl", false, "enumerate CRs live via kubectl")
	onlyHost := fs.String("only-host", "", "restrict to CRs whose hostAPI matches this")
	if err := fs.Parse(args); err != nil {
		return err
	}
	crs, err := readCRSource(*crsPath, *kubectlMode)
	if err != nil {
		return err
	}
	inv := BuildInventory(crs)
	if *onlyHost != "" {
		inv = filterHost(inv, *onlyHost)
	}
	printInventory(inv)
	if len(inv.Malformed) > 0 {
		return fmt.Errorf("%d malformed CR/key(s) — see report", len(inv.Malformed))
	}
	return nil
}

func printInventory(inv Inventory) {
	fmt.Printf("KMSSecret CR inventory\n")
	fmt.Printf("  CRs:               %d\n", inv.CRCount)
	fmt.Printf("  explicit-key rows: %d (before dedup)\n", inv.RowsTotal)
	fmt.Printf("  unique targets:    %d (deduped)\n", inv.UniqueCount)
	fmt.Printf("  folder-sync CRs:   %d (empty keys[]; resolved by LIST at run)\n", len(inv.Folders))
	fmt.Printf("  orgs:              %v\n", inv.Orgs)
	fmt.Printf("  envs:              %s\n", sortedCounts(inv.Envs))
	fmt.Printf("  hosts:             %s\n", sortedCounts(inv.Hosts))
	fmt.Printf("  duplicate targets: %d (idempotent upserts)\n", len(inv.Duplicates))
	if len(inv.Malformed) > 0 {
		fmt.Printf("  MALFORMED:         %d\n", len(inv.Malformed))
		for _, p := range inv.Malformed {
			fmt.Printf("    - %s/%s: %s %s\n", p.CRNamespace, p.CRName, p.Reason, p.Detail)
		}
	}
	if len(inv.Folders) > 0 {
		fmt.Printf("  folder CRs (need LIST):\n")
		for _, f := range inv.Folders {
			fmt.Printf("    - %s/%s  org=%s path=/%s env=%s → %s\n",
				f.CRNamespace, f.CRName, f.Org, f.Path, f.Env, f.ManagedName)
		}
	}
}

func sortedCounts(m map[string]int) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for i, k := range keys {
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(&b, "%s=%d", k, m[k])
	}
	return b.String()
}

// ── preflight ────────────────────────────────────────────────────────────────────

func runPreflight(args []string) error {
	fs := flag.NewFlagSet("preflight", flag.ContinueOnError)
	cloudURL := fs.String("cloud", "", "cloud embedded KMS base URL to probe")
	srcAud := fs.String("src-audiences", "", "standalone KMS_EXPECTED_AUDIENCE CSV (for the G1 delta)")
	cloudAud := fs.String("cloud-audiences", "", "cloud current audience allowlist CSV (for the G1 delta)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if *srcAud != "" && *cloudAud != "" {
		printG1Delta(*srcAud, *cloudAud)
	}

	if *cloudURL == "" {
		if *srcAud != "" && *cloudAud != "" {
			return nil // G1-only preflight
		}
		return fmt.Errorf("--cloud <url> is required (or supply --src-audiences + --cloud-audiences for a G1-only check)")
	}

	ctx := context.Background()
	c := newKMSClient(*cloudURL, nil)

	// 1. Reachability + readiness.
	hs, err := c.health(ctx)
	if err != nil {
		return fmt.Errorf("cloud /v1/kms unreachable: %w", err)
	}
	fmt.Printf("preflight: cloud /v1/kms/health = %d (%s)\n", hs, readyText(hs))

	// 2. JWT validation: an unauthenticated secrets read must be refused (403), and
	//    a garbage bearer must NOT be accepted — proof the guard validates rather
	//    than trusting the caller. A well-formed org path keeps the probe clean.
	noPrinc, _ := c.probeStatus(ctx, "GET", "", "hanzo", "preflight-probe", "prod", "NOPE")
	fmt.Printf("preflight: no-principal read = %d (want 403)\n", noPrinc)
	garbage, _ := c.probeStatus(ctx, "GET", "not-a-real-jwt", "hanzo", "preflight-probe", "prod", "NOPE")
	fmt.Printf("preflight: garbage-bearer read = %d (want 401/403, never 200)\n", garbage)

	if hs != http.StatusOK && hs != http.StatusServiceUnavailable {
		return fmt.Errorf("cloud /v1/kms health returned %d — expected 200 (ready) or 503 (health-only)", hs)
	}
	if noPrinc == http.StatusOK || garbage == http.StatusOK {
		return fmt.Errorf("SECURITY: cloud served a secrets read to an unauthenticated/garbage caller (200) — do NOT cut over")
	}
	return nil
}

func readyText(status int) string {
	switch status {
	case http.StatusOK:
		return "ready"
	case http.StatusServiceUnavailable:
		return "health-only (master key not configured)"
	default:
		return "unexpected"
	}
}

// printG1Delta computes CLOUD_JWT_AUDIENCES = union(cloud current, standalone) and
// prints the exact additions the fleet needs so it does not 401 on cloud. This is
// the same set the operator CR env change carries.
func printG1Delta(srcCSV, cloudCSV string) {
	src := splitCSV(srcCSV)
	cloud := splitCSV(cloudCSV)
	cloudSet := map[string]bool{}
	for _, a := range cloud {
		cloudSet[a] = true
	}
	var add []string
	for _, a := range src {
		if !cloudSet[a] {
			add = append(add, a)
		}
	}
	union := append(append([]string{}, cloud...), add...)
	fmt.Printf("G1 audience delta\n")
	fmt.Printf("  cloud current:      %d\n", len(cloud))
	fmt.Printf("  standalone expects: %d\n", len(src))
	fmt.Printf("  MUST ADD:           %d → %s\n", len(add), strings.Join(add, ","))
	fmt.Printf("  CLOUD_JWT_AUDIENCES (union, %d):\n    %s\n", len(union), strings.Join(union, ","))
}

func splitCSV(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if pp := strings.TrimSpace(p); pp != "" {
			out = append(out, pp)
		}
	}
	return out
}
