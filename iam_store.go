// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package cloud

import (
	"sync"

	"github.com/hanzoai/orm"
)

// The seam that lets this package read IAM's store without importing it.
//
// IAM is grafted into this binary (apps/iam mounts iamserver.NewApp on cloud's
// own router), so the code that resolves an API key and the code that asks for one
// resolved are the same process. They could not be the same CALL, though: apps/iam
// imports this package, so this package cannot import apps/iam, and the resolver
// here reached its own identity store over HTTP —
// http://iam.hanzo.svc/v1/iam/resolve-key — a network round trip a process made to
// itself, because of an import direction.
//
// Everything that grew around that hop was scaffolding for a call that should never
// have left the process: a confidential client credential to authenticate to
// ourselves, a 5-second HTTP timeout on the hot auth path, JSON envelopes to
// re-decode rows we already have, and an init() that panicked when iam.hanzo.svc was
// unreachable — taking api.hanzo.ai down over a dependency this binary contains.
//
// So the dependency is inverted rather than the cycle worked around: IAM hands its
// opened store to this package when it mounts, and the resolver reads it directly.
// The direction of the import is unchanged; only the direction of the CALL is.
//
// It is nil until apps/iam.Mount runs — IAM not enabled, or a boot that fail-closed
// the subsystem — so every reader nil-guards and degrades to "unresolved", never
// dereferences. An unresolved key is anonymous, which is exactly what an
// unconfigured resolver did before: a bad or unresolvable key has never granted
// trust, and does not now.
var (
	iamStoreMu sync.RWMutex
	iamStoreDB orm.DB
)

// SetIAMStore publishes the embedded IAM's store to this package. apps/iam.Mount
// calls it once, after the store opens and before any route is served. Passing nil
// is how a subsystem that failed closed says so.
func SetIAMStore(db orm.DB) {
	iamStoreMu.Lock()
	defer iamStoreMu.Unlock()
	iamStoreDB = db
}

// iamStore returns the embedded IAM's store, or nil when IAM has not mounted.
// Callers MUST nil-guard: nil means "cannot answer", never "no".
func iamStore() orm.DB {
	iamStoreMu.RLock()
	defer iamStoreMu.RUnlock()
	return iamStoreDB
}
