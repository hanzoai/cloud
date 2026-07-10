// Command commerce-encrypt-dbs converts commerce's PLAINTEXT per-tenant SQLite
// stores (users/<id>/data.db, orgs/<id>/data.db) to the ENVELOPED, SQLCipher-
// encrypted layout the daemon opens under COMMERCE_KMS_MASTER_KEY.
//
// Run it as a one-shot Job BEFORE flipping COMMERCE_KMS_MASTER_KEY onto the
// deployment: once the key is set the daemon refuses any tenant file that has no
// DEK sidecar (fail-closed), so pre-existing plaintext files must be migrated
// first. It is idempotent (already-encrypted tenants are skipped) and never
// deletes the plaintext — each converted file is kept as <data.db>.plaintext.bak
// for operator verification.
//
// Usage:
//
//	COMMERCE_KMS_MASTER_KEY=<64-hex> commerce-encrypt-dbs -data /data
//	COMMERCE_KMS_MASTER_KEY=<64-hex> commerce-encrypt-dbs -data /data -dry-run
//
// Build (production, links libsqlcipher):
//
//	CGO_ENABLED=1 go build -tags "libsqlite3 sqlite_fts5" ./cmd/commerce-encrypt-dbs
package main

import (
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/hanzoai/cloud/clients/commerce/db"
)

func main() {
	// Default to COMMERCE_DIR so the migration targets the SAME tree the daemon
	// serves (commerce.go: DataDir = getEnv("COMMERCE_DIR", "./commerce_data")).
	defaultDir := os.Getenv("COMMERCE_DIR")
	if defaultDir == "" {
		defaultDir = "/app/data"
	}
	dataDir := flag.String("data", defaultDir, "commerce data directory (contains users/ and orgs/); defaults to $COMMERCE_DIR")
	dryRun := flag.Bool("dry-run", false, "report what would be migrated without writing")
	flag.Parse()

	if _, err := os.Stat(*dataDir); err != nil {
		log.Fatalf("data dir %q: %v", *dataDir, err)
	}

	masterKey, err := db.ResolveMasterKey()
	if err != nil {
		log.Fatalf("master key: %v", err)
	}
	if masterKey == nil {
		log.Fatal("COMMERCE_KMS_MASTER_KEY is not set — nothing to encrypt to")
	}

	rep, err := db.EncryptDataDir(*dataDir, masterKey, *dryRun)
	if err != nil {
		log.Fatalf("migrate: %v", err)
	}

	verb := "encrypted"
	if *dryRun {
		verb = "would encrypt"
	}
	fmt.Printf("%s %d tenant db(s), %d row(s); skipped %d already-encrypted/empty\n",
		verb, len(rep.Encrypted), rep.Rows, len(rep.Skipped))
	for _, p := range rep.Encrypted {
		fmt.Printf("  + %s\n", p)
	}
}
