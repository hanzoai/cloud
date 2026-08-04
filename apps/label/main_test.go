package label

// devmaster keys this test binary: cek opens nothing without a master and a test
// process has no KMS. This plane needs it more than most — the record IS the
// product, so a suite that cannot open a store cannot exercise a single
// durability claim in it.
import _ "github.com/hanzoai/cloud/internal/devmaster"
