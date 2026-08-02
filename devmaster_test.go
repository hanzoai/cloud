package cloud

// The root package's store-backed tests (orgdb_test.go's per-org isolation and
// OrgStore.Each proofs, the audit middleware chain) open real databases, and cek
// opens nothing without a master. This test binary has no KMS, so it mints its
// own — the one line that says so, for every test in the package.
import _ "github.com/hanzoai/cloud/internal/devmaster"
