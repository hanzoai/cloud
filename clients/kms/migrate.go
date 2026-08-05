package kms

// migrate.go is the ONE-TIME cutover from the legacy embedded ZapDB KMS store to
// the per-org SQLite store (store.go). The old store held EVERY org's sealed
// secrets in ONE ZapDB KV under {DataDir}/kms, whose exclusive OS lock pinned cloud
// to replicas=1. Deploying the per-org store over live data WITHOUT this migration
// would boot an EMPTY store and orphan every secret — a cluster-wide outage, since
// cloud's KMS is the authoritative source the kms-operator syncs OUT to every
// service's k8s Secret. So New() runs this on the WRITER, keyed, BEFORE serving,
// and treats any error as FATAL (refuse to boot rather than silently lose secrets).
//
// SAFETY. Secrets move SEALED: the legacy VALUE is the JSON-marshaled store.Secret
// carrying the AES-256-GCM ciphertext + ML-KEM wrapped DEK, and it is re-stored
// verbatim — NEVER unsealed here — so no plaintext is exposed and the per-secret
// Seal envelope (AAD-bound to the full /orgs/{org} path) is preserved byte-for-byte;
// a migrated record Opens exactly as before and still fails Open if relocated across
// orgs. On success the legacy dir is RENAMED to {DataDir}/kms.migrated (not deleted)
// so it is never reopened — releasing the ZapDB OS lock for good — while the old
// ciphertext stays recoverable. Idempotent: a prior .migrated marker or an absent
// legacy store is a no-op; dst.put upserts, so a partially-copied prior run resumes.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	kmsstore "github.com/luxfi/kms/pkg/store"
	luxlog "github.com/luxfi/log"
	zapdb "github.com/luxfi/zapdb"
)

// legacySecretPrefix mirrors the (unexported) prefix the legacy SecretStore wrote
// every record under — kms/secrets/{path}/{env}/{name}. The stored VALUE is the
// JSON store.Secret (full coordinate + sealed bytes), so migration decodes the
// VALUE and never has to parse the ambiguous key (path itself contains '/').
var legacySecretPrefix = []byte("kms/secrets/")

// migrateLegacyZapDB performs the one-time ZapDB -> per-org SQLite cutover. Called
// by New() for the WRITER, keyed, before serving. Returns nil (no-op) when there is
// nothing to migrate. Any real error is returned so New treats it as FATAL.
func migrateLegacyZapDB(dataDir string, masterKey []byte, dst *secretStore, log luxlog.Logger) error {
	legacyDir := filepath.Join(dataDir, "kms")
	migratedDir := legacyDir + ".migrated"

	// Already cut over (or a fresh deploy that never had a legacy store): no-op.
	if _, err := os.Stat(migratedDir); err == nil {
		return nil
	}
	// No legacy ZapDB present — its MANIFEST marks an initialized store: no-op.
	if _, err := os.Stat(filepath.Join(legacyDir, "MANIFEST")); err != nil {
		return nil
	}
	if len(masterKey) == 0 {
		// The legacy store is encrypted at rest; without the key it cannot be read.
		// Never destroy unreadable secrets — refuse so a keyed boot can migrate.
		return fmt.Errorf("legacy store present at %s but master key absent — refusing to skip", legacyDir)
	}

	opts := zapdb.DefaultOptions(legacyDir).WithLogger(nil).
		WithEncryptionKey(masterKey).WithIndexCacheSize(16 << 20)
	db, err := zapdb.Open(opts)
	if err != nil {
		return fmt.Errorf("open legacy store %s: %w", legacyDir, err)
	}
	n, copyErr := copyLegacySecrets(db, dst)
	// Close BEFORE renaming so the OS lock is released and the dir is movable.
	closeErr := db.Close()
	if copyErr != nil {
		return fmt.Errorf("copy legacy secrets: %w", copyErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close legacy store: %w", closeErr)
	}
	// Cutover marker — rename ONLY after every secret landed in SQLite. The old store
	// is now archived (never reopened, lock gone) yet the sealed ciphertext survives.
	if err := os.Rename(legacyDir, migratedDir); err != nil {
		return fmt.Errorf("mark migrated (rename %s): %w", legacyDir, err)
	}
	log.Info("kms: migrated legacy ZapDB store to per-org SQLite (replicas=1 lock released)",
		"secrets", n, "archived", migratedDir)
	return nil
}

// copyLegacySecrets streams every sealed secret under the kms/secrets/ prefix from
// the legacy ZapDB into its per-org SQLite file via dst.put. It decodes the VALUE
// (the JSON store.Secret: full coordinate + sealed ciphertext + wrapped DEK), so no
// key parsing and no unsealing occurs. Returns the count copied.
func copyLegacySecrets(db *zapdb.DB, dst *secretStore) (int, error) {
	n := 0
	err := db.View(func(txn *zapdb.Txn) error {
		iopts := zapdb.DefaultIteratorOptions
		iopts.Prefix = legacySecretPrefix
		it := txn.NewIterator(iopts)
		defer it.Close()
		for it.Rewind(); it.Valid(); it.Next() {
			var sec kmsstore.Secret
			if err := it.Item().Value(func(val []byte) error {
				return json.Unmarshal(val, &sec)
			}); err != nil {
				return fmt.Errorf("decode secret %q: %w", it.Item().Key(), err)
			}
			if err := dst.put(&sec); err != nil {
				return fmt.Errorf("write secret %s/%s/%s: %w", sec.Path, sec.Env, sec.Name, err)
			}
			n++
		}
		return nil
	})
	return n, err
}
