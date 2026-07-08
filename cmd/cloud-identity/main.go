// cloud-identity is the IDENTITY-ROLE entrypoint of the unified Hanzo Cloud
// binary family (HIP-0106 one-artifact/two-role split).
//
// It is the SAME cloud.Serve body and the SAME clients/iam subsystem source as
// cmd/cloud — but it blank-imports ONLY the iam subsystem, so the link set is
// minimal. Two things fall out of that, both required for the identity role:
//
//  1. ENCRYPTION. The embedded IAM keeps a per-org SQLCipher-ENCRYPTED SQLite
//     store (orgIsolation=sqlite), which needs a CGO=1 build linked against
//     libsqlcipher. The FULL cmd/cloud binary cannot be built CGO=1: several
//     app-role subsystems (hanzoai/base/core, commerce/db, o11y, orm/db) import
//     modernc.org/sqlite DIRECTLY, which double-registers the "sqlite" driver
//     against hanzoai/sqlite(mattn) and panics at init (the Dockerfile's SQLITE
//     gate proves this). clients/iam + the cloud root pull ZERO modernc, so THIS
//     entrypoint builds CGO=1+SQLCipher clean. Encrypted identity store, real.
//
//  2. BLAST RADIUS. Only iam is LINKED, so no sibling subsystem can crash the
//     single-writer identity authority — build-time isolation, stronger than a
//     runtime CLOUD_ENABLE=iam allowlist on the full binary (RED gate #4).
//
// cloud.Serve([]string{"iam"}) forces the iam-only mount independent of env; the
// canonical middleware pipeline (identity/audit/rate-limit/billing) no-ops the
// unconfigured legs (no COMMERCE_SERVICE_TOKEN ⇒ BillingGate/ScopeRateLimit are
// pass-through), so the reduced dependency set is safe. Validate() still enforces
// replicas=1 whenever iam is enabled (process-local session store).
//
// The multi-replica APP-ROLE front door remains cmd/cloud (CGO=0, all subsystems).
package main

import (
	"fmt"
	"os"

	"github.com/hanzoai/cloud"

	// Identity role: ONLY the IAM identity plane is linked. Its init() registers
	// "iam" into cloud.Registry; nothing else is in the build graph, which is
	// exactly what keeps this entrypoint modernc-free (CGO=1/SQLCipher-buildable)
	// and its blast radius a single subsystem.
	_ "github.com/hanzoai/cloud/clients/iam"
)

func main() {
	// Force the iam-only mount regardless of CLOUD_ENABLE — this binary IS the
	// identity role, not a general cloud honoring an arbitrary enable list.
	if err := cloud.Serve([]string{"iam"}); err != nil {
		fmt.Fprintf(os.Stderr, "cloud-identity: %v\n", err)
		os.Exit(1)
	}
}
