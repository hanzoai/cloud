package payout_test

// devmaster keys this test binary: cek opens nothing without a master, and a test
// process has no KMS to resolve one from. The ledger these tests read is a real
// encrypted per-org store, not a stand-in, so it needs a real key.
import _ "github.com/hanzoai/cloud/internal/devmaster"
