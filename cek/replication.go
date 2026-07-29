package cek

// replication.go — where SQLite→S3 replication belongs, and why it is not a
// sidecar.
//
// DESIGN ONLY. Nothing here is wired yet; this file marks the seam and records
// the decision so the next change lands in the right place instead of adding a
// sixth object to every stateful pod.
//
// # What the sidecar costs
//
// Today each replicated service carries four objects and a key: a `replicate`
// container, a generated ConfigMap, a restore initContainer, and its own age
// keypair. All of it exists for one reason — `replicate` is a separate binary
// watching a file it does not own, so the file has to be described to it.
//
// On 2026-07-29 that arrangement produced four independent outages in one day:
// a misindented age stanza replicate refused outright, an age/plaintext
// mismatch between config and bucket, a service whose data directory was not
// mounted at all, and a restore path that had never once run successfully. The
// last is the instructive one: restore only executes `-if-db-not-exists`, so
// while the local file happened to exist it was never exercised. The backups
// were configured, not current, and not restorable — and nothing said so until
// a volume was lost.
//
// # Why it belongs here
//
// Replication is a property of the STORE, not of a process watching it. Open is
// already "the single way a cloud store opens its file" and Exists already
// answers "is there a store here" — which is the entire question the
// initContainer was shelling out to ask.
//
// Native, the lifecycle collapses into Open:
//
//	if !Exists(path) && replica.Has(path) { replica.Hydrate(path) }
//	db := open(path)
//	replica.Follow(db)   // every commit streams out
//
// Restore stops being a lifecycle stage and becomes what Open does. The
// ConfigMap, the initContainer, the second container and the ordering between
// them all disappear — there is nothing left to describe to a peer, because
// there is no peer.
//
// # One key, not two
//
// This is the sharpest argument and the reason the age keypair should not be
// migrated to KMS but DELETED. cek already holds a master key
// (CLOUD_KMS_MASTER_KEY_REF) and resolveMaster is explicit that there is no
// plaintext-at-rest mode: a store is keyed or it does not open. The age
// identity is a SECOND key system encrypting the SAME data — with its own
// per-service secret, its own failure modes, and no rotation story at all,
// because an age identity cannot be rotated after the fact. Lose it and every
// replica under it is unreadable.
//
// A replica written by this layer is encrypted under the key the process is
// already holding. One store, one key, one encryption path.
//
// # Transport
//
// S3 over ZAP on the internal plane, like every other cross-app call — see
// cloud/rpc.go. That also retires ghcr.io/hanzoai/replicate as a shipped image.
//
// # The interface this wants
//
// Small on purpose: three verbs, all about bytes at a path, none about
// containers or lifecycle.
type Replica interface {
	// Has reports whether a replica exists for this store. It is what makes
	// hydrate-on-open safe to call unconditionally: a first-ever boot finds
	// nothing and proceeds to create an empty store, which is correct and is
	// what `-if-replica-exists` was approximating from outside.
	Has(path string) (bool, error)

	// Hydrate materializes the store at path from its replica. It runs only
	// when Exists(path) is false, so it can never overwrite live data — the
	// property the initContainer got right and which must not be lost.
	Hydrate(path string) error

	// Follow streams subsequent commits out. It returns once following has
	// started, not when the replica is caught up: a store that refuses to open
	// until its backup is current is a store that will not open during an S3
	// incident, which trades a durability risk for an availability one.
	Follow(path string) (stop func() error, err error)
}
