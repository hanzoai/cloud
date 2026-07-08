// Package writefence is the two-plane epoch write-fence: the primitive that
// makes a same-epoch double-write to a (plugin, shard) mirror physically
// impossible, regardless of what the CONTROL plane's writer-lease election
// believes or how stale a DATA-plane process's local view of that election is.
//
// # Where this sits (HIP-0107 / HIP-0116)
//
// The data plane (github.com/hanzoai/vfs/replica, wired in internal/org for
// per-org SQLite; the same shape governs any (plugin, shard) mirror) ships a
// writer's WAL/snapshot to an S3/vfs object-store mirror. Today the "am I
// allowed to push" decision is TWO comment-only, non-atomic things layered on
// top of an unconditional object overwrite:
//
//   - replica.IsOwner(orgID, selfID, members) — a PURE, LOCAL computation over
//     whatever membership view the caller happens to hold. Two processes with
//     different (stale) membership views can both compute themselves as owner.
//   - the K8s deployment shape (StatefulSet replicas:1 + Recreate — see
//     role.Role's doc comment) — a coarse, non-epoch-aware "there should only
//     be one" guarantee that does not hold under a partition/force-delete: k8s
//     can start a new writer pod while the old one's process is still running
//     and still able to open its RWO mount and still able to reach the object
//     store.
//   - replica.Store.Put / vfs's backend.Backend.Put — "Overwriting is allowed"
//     (verbatim doc comment upstream). No conditional write, no epoch, no
//     admission check of any kind at the storage layer.
//
// So the actual "canPush" predicate that exists today is: nothing. Any
// process that believes it is the owner can overwrite the mirror at any time.
// A deposed/partitioned old writer and a freshly-elected new writer can BOTH
// push — same-epoch (or no-epoch-at-all) double-write, silently corrupting
// the mirror / collapsing KMS share-isolation.
//
// This package closes that gap as an orthogonal, storage-agnostic primitive
// that sits IN FRONT of the existing Push call, without forking
// github.com/hanzoai/vfs or github.com/hanzoai/vfs/replica and without
// touching clients/controlplane (owned by a concurrent effort — Stage 1,
// shadow-only, not yet consulted on the live write path):
//
//   - EpochSource is the seam controlplane's granted lease epoch drops into
//     once it graduates from shadow. Nothing here imports controlplane.
//   - ConditionalStore models the one storage primitive every real
//     object-store CAS boils down to (S3 If-Match / GCS generation-match):
//     "write iff the object is still at the version I last observed."
//   - Fence.Push performs the STRICT epoch check and the CAS in one call, so
//     there is no window between "epoch admitted" and "data written" for a
//     second writer to land in.
//
// See store.go for the real, already-vendored S3-compatible implementation
// (minio-go v7's native SetMatchETag/SetMatchETagExcept — no new dependency)
// and the doc comment there for exactly how this plugs into internal/org's
// today-vfs-only binding.
package writefence

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
)

// ErrNotFound is returned by ConditionalStore.Get when key has never been
// written. Fence treats this as "recorded epoch 0" — any candidateEpoch >= 1
// is admissible against an empty mirror.
var ErrNotFound = errors.New("writefence: marker not found")

// ErrStaleEpoch is returned when candidateEpoch is not STRICTLY greater than
// the epoch already recorded in the store for this (plugin, shard). This is
// the invariant that closes the same-epoch double-write: a deposed or
// partitioned writer retrying at the epoch it was (or believes it was)
// granted is rejected even though it never raced anyone at the store level —
// the check runs before any write is attempted.
var ErrStaleEpoch = errors.New("writefence: candidate epoch is not strictly greater than the recorded epoch")

// ErrConflict is returned when the store's atomic conditional write rejected
// our attempt because the object changed between our Get and our
// PutIfVersion — we lost a race against a concurrent pusher. Fence.Push
// retries internally (re-reading the new authoritative state and re-running
// the strict-epoch check), so ErrConflict escaping Push means retries were
// exhausted, not that a caller should blindly retry with the same epoch.
var ErrConflict = errors.New("writefence: conditional write lost a concurrent race")

// maxCASAttempts bounds Push's internal read-check-CAS retry loop. Bounded
// (not unbounded) so a pathologically hot shard fails loudly rather than
// spinning forever; in practice at most one retry is ever needed per genuine
// race (the loser's retry either wins the now-uncontended CAS or discovers
// its epoch was never going to win and returns ErrStaleEpoch).
const maxCASAttempts = 8

// ConditionalStore is the atomic-CAS object-store surface the fence needs.
// It models the S3 If-Match / GCS generation-match primitive directly:
// PutIfVersion succeeds only if the store's CURRENT version for key still
// equals expectVersion at the moment of the write. expectVersion == ""
// means "key must not exist yet" (a create-only put, i.e. S3
// If-None-Match: *).
//
// Implementations MUST perform this check atomically, server-side. A
// client-side "Get, compare in Go, then Put" is NOT a valid implementation —
// that non-atomicity is exactly the race this package exists to close. See
// store.go for a real implementation over minio-go's native
// SetMatchETag/SetMatchETagExcept, and fake_test.go for the in-memory model
// used by the concurrency tests.
type ConditionalStore interface {
	// Get returns the marker bytes and the store's authoritative version
	// token (e.g. ETag / generation) for key. Returns ErrNotFound (wrapped)
	// when key has never been written.
	Get(ctx context.Context, key string) (data []byte, version string, err error)

	// PutIfVersion writes data at key IFF the store's current version for
	// key still equals expectVersion. Returns the new version on success, or
	// a wrapped ErrConflict when the precondition failed.
	PutIfVersion(ctx context.Context, key string, data []byte, expectVersion string) (newVersion string, err error)
}

// EpochSource resolves the CANDIDATE epoch a writer should push at for a
// given (plugin, shard). It is the seam the control-plane epoch-fenced
// writer lease (clients/controlplane — Stage 1, shadow-only today) drops
// into once it graduates from shadow: a live implementation there reads the
// currently-granted lease epoch for (plugin, shard) from the Quasar PQ-BFT
// RSM. This package imports nothing from clients/controlplane and has no
// opinion on how the epoch is decided — only on what happens once a push
// arrives claiming one.
type EpochSource interface {
	Epoch(ctx context.Context, plugin, shard string) (uint64, error)
}

// StaticEpochSource is the trivial EpochSource used before the control plane
// graduates from shadow: every (plugin, shard) reports a fixed epoch chosen
// by the caller (e.g. 1, matching today's "there is only one writer, ever"
// assumption). It exists so callers can adopt the Fence/EpochSource seam now
// without waiting on controlplane, and swap in the real lease-backed source
// later with no change to call sites.
type StaticEpochSource uint64

// Epoch implements EpochSource by returning the fixed epoch unconditionally.
func (s StaticEpochSource) Epoch(context.Context, string, string) (uint64, error) {
	return uint64(s), nil
}

// Fence enforces strict-epoch, atomically-CAS'd admission for a shard's
// single mirror object: one shard, one object, one conditional write. The
// epoch advance and the payload append happen in the SAME PutIfVersion call,
// so there is no window between "epoch admitted" and "data written" in which
// a second writer can land a conflicting append.
type Fence struct {
	Store ConditionalStore
}

// New builds a Fence over store.
func New(store ConditionalStore) *Fence {
	return &Fence{Store: store}
}

// mirrorKey is the canonical fenced object for (plugin, shard). One key per
// shard: the header (epoch + writer) and the payload live in the same
// object, so admission and append are a single atomic write.
//
// The (plugin, shard) pair is LENGTH-PREFIXED, not slash-joined: a bare
// "writefence/"+plugin+"/"+shard is not injective — mirrorKey("kms/a","s")
// would collide with mirrorKey("kms","a/s"), letting a push framed as one
// shard silently overwrite another's epoch/writer/payload record. Real shard
// scopes carry '/' (vfs replica.DBPath yields "projects/site", "users/dave"),
// so this is reachable, not theoretical. The `%d:` byte-length prefix makes
// the boundary unambiguous regardless of any '/' inside either field.
func mirrorKey(plugin, shard string) string {
	return fmt.Sprintf("writefence/%d:%s/%d:%s", len(plugin), plugin, len(shard), shard)
}

// header is the small, fixed-layout prefix carried in front of the payload:
// magic (so a foreign object at the same key is detected rather than
// silently misparsed) + 8-byte big-endian epoch + length-prefixed writer id.
type header struct {
	Epoch  uint64
	Writer string
}

const headerMagic = "wfnc1\x00"

func encodeRecord(h header, payload []byte) []byte {
	writerBytes := []byte(h.Writer)
	buf := make([]byte, 0, len(headerMagic)+8+4+len(writerBytes)+len(payload))
	buf = append(buf, headerMagic...)
	var epochBE [8]byte
	binary.BigEndian.PutUint64(epochBE[:], h.Epoch)
	buf = append(buf, epochBE[:]...)
	var wlen [4]byte
	binary.BigEndian.PutUint32(wlen[:], uint32(len(writerBytes)))
	buf = append(buf, wlen[:]...)
	buf = append(buf, writerBytes...)
	buf = append(buf, payload...)
	return buf
}

// errCorruptRecord is returned when a stored record does not decode as one
// this package wrote — fail closed rather than guess an epoch out of
// arbitrary bytes.
var errCorruptRecord = errors.New("writefence: stored record is not a valid writefence marker")

func decodeRecord(b []byte) (header, []byte, error) {
	m := len(headerMagic)
	if len(b) < m+8+4 || string(b[:m]) != headerMagic {
		return header{}, nil, errCorruptRecord
	}
	epoch := binary.BigEndian.Uint64(b[m : m+8])
	wlen := int(binary.BigEndian.Uint32(b[m+8 : m+12]))
	rest := b[m+12:]
	if wlen < 0 || wlen > len(rest) {
		return header{}, nil, errCorruptRecord
	}
	writer := string(rest[:wlen])
	payload := rest[wlen:]
	return header{Epoch: epoch, Writer: writer}, payload, nil
}

// Push admits and (atomically with admission) writes payload for (plugin,
// shard) at epoch, attributed to writer. It enforces, in order:
//
//  1. STRICT monotonicity: epoch must be > the epoch currently recorded in
//     the store for this shard (0 if the shard has never been pushed). A
//     push at an epoch equal to or below the recorded one is rejected with
//     ErrStaleEpoch BEFORE any write is attempted — this is what closes the
//     same-epoch double-write: a deposed/partitioned writer retrying at the
//     epoch it was granted can never pass this check once ANY push at that
//     epoch (its own original one, or a racing peer's) has already landed.
//  2. Atomicity: the header (epoch, writer) advance and the payload append
//     are written in a single PutIfVersion, conditioned on the store's
//     CURRENT version for the shard — read fresh on every attempt, never
//     cached. If a concurrent pusher's write lands first, our PutIfVersion
//     fails with ErrConflict and we retry: re-read (now sees the winner's
//     epoch), re-run the strict check. A same-epoch racer loses this second
//     check (ErrStaleEpoch); a legitimately higher epoch retries the CAS
//     against the new version and, absent further contention, wins.
//
// The store — never an in-memory cache — is the sole arbiter of "what epoch
// currently owns this shard's mirror."
func (f *Fence) Push(ctx context.Context, plugin, shard, writer string, epoch uint64, payload []byte) error {
	if f == nil || f.Store == nil {
		return fmt.Errorf("writefence: nil fence or store")
	}
	if plugin == "" || shard == "" {
		return fmt.Errorf("writefence: plugin and shard are required")
	}
	key := mirrorKey(plugin, shard)

	var lastErr error
	for attempt := 0; attempt < maxCASAttempts; attempt++ {
		curEpoch, version, err := f.readCurrent(ctx, key)
		if err != nil {
			return err
		}
		if epoch <= curEpoch {
			return fmt.Errorf("%w: shard=%s/%s candidate=%d recorded=%d", ErrStaleEpoch, plugin, shard, epoch, curEpoch)
		}
		rec := encodeRecord(header{Epoch: epoch, Writer: writer}, payload)
		if _, err := f.Store.PutIfVersion(ctx, key, rec, version); err != nil {
			if errors.Is(err, ErrConflict) {
				lastErr = err
				continue // re-read the new authoritative state and retry.
			}
			return fmt.Errorf("writefence: push CAS: %w", err)
		}
		return nil
	}
	if lastErr == nil {
		lastErr = ErrConflict
	}
	return fmt.Errorf("%w: exhausted %d attempts pushing %s/%s at epoch %d", lastErr, maxCASAttempts, plugin, shard, epoch)
}

// readCurrent returns the currently-recorded epoch (0 if absent) and the
// store's version token to condition the next write on.
func (f *Fence) readCurrent(ctx context.Context, key string) (epoch uint64, version string, err error) {
	data, version, err := f.Store.Get(ctx, key)
	switch {
	case errors.Is(err, ErrNotFound):
		return 0, "", nil
	case err != nil:
		return 0, "", fmt.Errorf("writefence: read marker: %w", err)
	}
	h, _, err := decodeRecord(data)
	if err != nil {
		return 0, "", err
	}
	return h.Epoch, version, nil
}

// Latest returns the currently-recorded (epoch, writer, payload) for
// (plugin, shard), for readers / diagnostics / tests. ErrNotFound if the
// shard has never been pushed.
func (f *Fence) Latest(ctx context.Context, plugin, shard string) (epoch uint64, writer string, payload []byte, err error) {
	data, _, err := f.Store.Get(ctx, mirrorKey(plugin, shard))
	if err != nil {
		return 0, "", nil, err
	}
	h, p, err := decodeRecord(data)
	if err != nil {
		return 0, "", nil, err
	}
	return h.Epoch, h.Writer, p, nil
}
