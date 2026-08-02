package risk

// devmaster keys this test binary: cek opens nothing without a master and a test
// process has no KMS. Risk needs it because every decision is DURABLE FIRST —
// the record lands in the tenant's own encrypted file before the analytics copy
// is emitted — so a suite that cannot open a store cannot exercise the decision
// path at all.
import _ "github.com/hanzoai/cloud/internal/devmaster"
