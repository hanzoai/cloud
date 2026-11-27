package patrol

// This package's tests run the money paths against the production ledger, the way
// every plugin binary does: sdk/meter bills nothing until a driver registers.
import _ "github.com/hanzoai/cloud/internal/driver"
