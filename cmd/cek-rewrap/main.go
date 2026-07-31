// cek-rewrap — the one-time migration for "a store's key names its owner".
//
// Walks every per-org store and moves its sidecar from the legacy Global
// derivation to the owner-bound one, carrying the SAME DEK across. Read-only
// unless a store actually needs migrating; --dry-run reports without writing.
package main

import (
	"encoding/base64"
	"flag"
	"fmt"
	"os"

	"github.com/hanzoai/cloud/cek"
)

func main() {
	root := flag.String("root", "/var/lib/cloud/orgs", "per-org store root")
	dry := flag.Bool("dry-run", false, "report without writing")
	flag.Parse()

	// The SAME variable cloud reads. This asked for CLOUD_KMS_MASTER_KEY while every
	// deployment sets CLOUD_KMS_MASTER_KEY_REF, so the tool exited 2 in the only
	// environment it was meant to run in — which is part of why the migration it
	// carries never ran, and two stores stayed unopenable until the outage surfaced
	// them. A tool that cannot read the deployment's own configuration is a tool
	// that was never going to be run.
	//
	// Kept as a fallback rather than a rename, because a one-off already invoked
	// with the old name must keep working.
	mk := firstSet("CLOUD_KMS_MASTER_KEY_REF", "CLOUD_KMS_MASTER_KEY")
	if mk == "" {
		fmt.Fprintln(os.Stderr, "CLOUD_KMS_MASTER_KEY_REF (base64) is required — the same variable cloud reads")
		os.Exit(2)
	}
	master, err := base64.StdEncoding.DecodeString(mk)
	if err != nil || len(master) != 32 {
		fmt.Fprintf(os.Stderr, "master key must be 32 bytes base64 (got %d, err %v)\n", len(master), err)
		os.Exit(2)
	}
	cek.SetMasterKey(master)

	if *dry {
		res, err := cek.InspectOrgs(*root)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		var need, ok, bad int
		for _, r := range res {
			switch {
			case r.Err != nil:
				bad++
				fmt.Printf("UNOPENABLE %s: %v\n", r.Path, r.Err)
			case r.Already:
				ok++
			default:
				need++
				fmt.Printf("NEEDS-MIGRATION %s (owner %s)\n", r.Path, r.Owner)
			}
		}
		fmt.Printf("\ndry-run: %d need migration, %d already correct, %d unopenable\n", need, ok, bad)
		return
	}

	res, err := cek.RewrapOrgs(*root)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	var moved, already, bad int
	for _, r := range res {
		switch {
		case r.Err != nil:
			bad++
			fmt.Printf("FAILED %s: %v\n", r.Path, r.Err)
		case r.Rewrapped:
			moved++
			fmt.Printf("migrated %s -> %s\n", r.Path, r.Owner)
		case r.Already:
			already++
		}
	}
	fmt.Printf("\nmigrated %d, already correct %d, failed %d\n", moved, already, bad)
	if bad > 0 {
		os.Exit(1)
	}
}

// firstSet returns the first of names that is set and non-empty.
func firstSet(names ...string) string {
	for _, n := range names {
		if v := os.Getenv(n); v != "" {
			return v
		}
	}
	return ""
}
