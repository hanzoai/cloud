package marketplace

import (
	"bytes"

	"github.com/hanzoai/cloud/internal/kmstest"
)

// testMaster is the data-plane master a test deployment is given (cloud.Deps.MasterKey).
var testMaster = bytes.Repeat([]byte{0x5a}, 32)

// testKMS is the KMS the stores most tests open keep their credentials in.
var testKMS = kmstest.New()
