// Package idempotencykey is a reusable idempotency guard for money-moving HTTP
// requests (refunds, captures, payouts). It stores one record per
// (scope, key) whose STORAGE id is deterministic, so a duplicate submit
// collapses onto the same row via the backend ON CONFLICT(id, kind, namespace)
// upsert — independent of datastore.RunInTransaction, which is a no-op on this
// backend and provides no isolation.
//
// Lifecycle of a guarded operation:
//
//	rec, replay, err := idempotencykey.Begin(db, scope, key)
//	  replay == true  ⇒ this exact request already ran; return rec.Response,
//	                    do NOT move money again.
//	  replay == false ⇒ we recorded the in-flight marker first; perform the
//	                    side effect, then idempotencykey.Complete(rec, response).
//
// LIMITATION (documented, not hidden): Begin uses read-then-write, and this
// backend has no atomic compare-and-swap reachable from mixin.Model[T]
// (db.SQLiteDB.RunInTransaction is real but the datastore layer routes to the
// no-op datastore.RunInTransaction). So two callers racing on a FIRST-EVER key
// can both observe "not started" and both proceed. The deterministic id still
// guarantees a single STORED record (the second Create upserts), but the side
// effect could run twice in that narrow window. Callers whose side effect is
// itself non-idempotent (e.g. a raw gateway refund) MUST additionally pass the
// SAME key through to the gateway (Stripe/Square both honor an idempotency key)
// so the gateway de-dups the money move. This guard de-dups OUR ledger and
// replays OUR response; the gateway key closes the money-move window. See
// api/checkout refund for the wired example.
package idempotencykey

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/mixin"
	"github.com/hanzoai/orm"
)

func init() {
	orm.Register[IdempotencyKey]("idempotency-key", orm.WithStringKey[IdempotencyKey]())
}

// nowFn is the clock, a seam so tests can simulate elapsed time for the
// stale-guard recovery path without sleeping.
var nowFn = time.Now

// Status values.
const (
	StatusStarted   = "started"
	StatusCompleted = "completed"
)

// StartedTTL bounds how long a "started" guard is treated as a live in-flight
// operation. A money op (refund/capture) completes in seconds; a "started" guard
// older than this is presumed CRASHED (the process died between Begin and
// Complete) and is recoverable — Begin re-claims it and lets the caller retry.
// This is only safe because the guarded money move ALSO carries the same
// deterministic gateway idempotency key, so the gateway de-dupes the retry (no
// double charge). Without that gateway key a stale guard must stay fail-closed.
const StartedTTL = 5 * time.Minute

// IdempotencyKey records one guarded request. Scope namespaces the key to a
// resource kind + id (e.g. "refund:ord_123") so the same key under different
// scopes never collides. Response holds the JSON body returned on first success
// so a replay returns byte-identical output.
type IdempotencyKey struct {
	mixin.Model[IdempotencyKey]

	Scope string `json:"scope"`
	// IdemKey is the caller idempotency key. Named IdemKey (not Key) because a
	// field named Key would shadow the embedded Model[T].Key() method and break
	// the mixin.Entity interface. JSON stays "key" for the API.
	IdemKey  string `json:"key"`
	Status   string `json:"status" orm:"default:started"`
	Response string `json:"response,omitempty" datastore:",noindex"`

	// RecoveryPoint lets a caller record how far a multi-step side effect got,
	// so a retry can resume rather than restart. Optional.
	RecoveryPoint string `json:"recoveryPoint,omitempty"`
}

func (k *IdempotencyKey) Load(ps []datastore.Property) error {
	return datastore.LoadStruct(k, ps)
}

func (k *IdempotencyKey) Save() ([]datastore.Property, error) {
	return datastore.SaveStruct(k)
}

// Begin looks up (or records) the guard for (scope, key).
//
// Returns replay=true with the stored record when the key was already seen
// (whether still in-flight or completed) — the caller must NOT repeat the side
// effect and should return rec.Response if Status==completed. Returns
// replay=false with a freshly-created "started" marker when this is the first
// sighting; the caller performs the side effect then calls Complete.
func Begin(db *datastore.Datastore, scope, key string) (rec *IdempotencyKey, replay bool, err error) {
	id := DeterministicID(scope, key)

	// Replay: the guard for this (scope,key) already exists. Its STORAGE id is
	// deterministic, so concurrent first-time Begins collapse onto ONE row via
	// the backend ON CONFLICT(id,kind,namespace) upsert — no ledger fork.
	//
	// Read it by its EXACT storage key (kind + deterministic id + namespace). Do
	// NOT route this read through GetById: GetById decodes the id as a hashid,
	// and a deterministic NON-hashid string ("idem_<hex>") decodes to a KIND-LESS
	// key. The production Postgres backend's Get requires an exact kind match
	// (db/postgres.go), so a kind-less lookup never finds the row this guard just
	// wrote under kind "idempotency-key" — every retry then looks brand-new and
	// the money move runs AGAIN (double charge). SQLite's Get has a kind-less
	// fallback (db/sqlite.go), which is the ONLY reason this passed in tests and
	// bit solely in production. A kind-qualified Get round-trips on both backends.
	existing := New(db)
	guardKey := db.NewKey(existing.Kind(), id, 0, nil)
	if e := existing.Get(guardKey); e == nil {
		// Completed → always a replay (return the stored response).
		if existing.Status == StatusCompleted {
			return existing, true, nil
		}
		// Started + FRESH → a genuine concurrent in-flight op. Replay (caller
		// 409s) — do not run a second money move alongside it.
		if !existing.Recoverable() {
			return existing, true, nil
		}
		// Started + STALE → the original crashed between Begin and Complete.
		// Re-claim (Put bumps UpdatedAt) and let the caller RETRY: the money
		// move carries the same deterministic gateway key, so the gateway
		// de-dupes if the original had in fact reached it. Fail-safe recovery
		// of an otherwise-stuck guard.
		existing.SetId(id)
		existing.Status = StatusStarted
		if e := existing.Put(); e != nil {
			return nil, false, e
		}
		return existing, false, nil
	} else if !errors.Is(e, datastore.ErrNoSuchEntity) {
		return nil, false, e
	}

	rec = New(db)
	rec.SetId(id)
	rec.Scope = scope
	rec.IdemKey = key
	rec.Status = StatusStarted
	if e := rec.Create(); e != nil {
		return nil, false, e
	}
	return rec, false, nil
}

// DeterministicID derives the stable storage id for a (scope, key) pair. Same
// inputs → same id → the second concurrent Create upserts onto the first's row.
func DeterministicID(scope, key string) string {
	sum := sha256.Sum256([]byte(scope + "\x00" + key))
	return "idem_" + hex.EncodeToString(sum[:16])
}

// Complete records the successful response so future replays return it, and
// flips Status to completed. Called after the guarded side effect succeeds.
//
// Re-pins the deterministic id and Puts so the write lands on the exact
// "started" row (the ON CONFLICT upsert transitions it in place). SetId+Put is
// the reliable in-place write for a string-key model.
func Complete(rec *IdempotencyKey, response string) error {
	rec.SetId(DeterministicID(rec.Scope, rec.IdemKey))
	rec.Status = StatusCompleted
	rec.Response = response
	return rec.Put()
}

// New returns an initialized IdempotencyKey bound to db.
func New(db *datastore.Datastore) *IdempotencyKey {
	k := new(IdempotencyKey)
	k.Init(db)
	return k
}

// Query returns a query for this kind, scoped to db's namespace.
func Query(db *datastore.Datastore) datastore.Query {
	return db.Query("idempotency-key")
}

// Recoverable reports whether a "started" guard is stale enough to presume its
// originator crashed — i.e. safe to re-claim and retry. A completed guard is
// never recoverable (it's a replay); a fresh started guard is a live in-flight
// op (fail-closed, caller 409s).
func (k *IdempotencyKey) Recoverable() bool {
	return k.Status == StatusStarted && nowFn().Sub(k.UpdatedAt) >= StartedTTL
}
