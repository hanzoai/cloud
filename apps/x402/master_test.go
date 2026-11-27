package x402

import "bytes"

// testMaster is the data-plane master a test deployment is given (cloud.Deps.MasterKey).
var testMaster = bytes.Repeat([]byte{0x5a}, 32)
