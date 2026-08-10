// Copyright © 2026 Hanzo AI. MIT License.

package cloud

// master.go — the data-plane key, resolved once, before the first store opens.
//
// cek derives every database's key from ONE master and refuses to open a file
// without it, so this runs before anything in this process touches the disk.
//
// THERE ARE TWO ANSWERS AND NO THIRD. The deployment supplies the key in the
// environment, from KMS, exactly the way every other secret in the estate
// arrives — so a child process inherits it like any other configuration and has
// nothing to ask anyone for. Or nothing supplies one, and a process over an
// EMPTY data directory mints a random master that dies with it, which is what
// lets a laptop run with no configuration at all.
//
// THE REFUSAL IS THE LOAD-BEARING HALF. Minting a master over a directory that
// already holds databases SUCCEEDS, and every one of those files then reads as
// "file is not a database" while the data sits intact and unreadable. So a data
// directory with databases in it and no key is a fault: the stores fail closed
// rather than opening under a key nothing was written with.
//
// This replaced a 1,237-line broker that held the key in one process and served
// per-app credential bundles to the others over a unix socket, identified by an
// HMAC stamp the launcher minted. It bought a boundary against ACCIDENT — its
// own documentation said so, and said it was not a boundary against a peer that
// reads its neighbours, since the stamp sat in the child's environment where any
// same-uid process could read it. The pod is the data-plane boundary either way:
// every process in it opens the same encrypted files under the same master. So
// the broker's cost was a socket protocol, a bespoke token scheme and a class of
// boot-ordering failures, for a property the pod already had.

import (
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/hanzoai/cek"
)

// MasterEnv carries the base64 32-byte data-plane master. It is NOT scrubbed:
// inheritance is how a child process is given the key.
const MasterEnv = "CLOUD_KMS_MASTER_KEY_REF"

var (
	masterOnce sync.Once
	master     []byte
	masterErr  error
	masterFrom string
)

// BootMaster installs the data-plane master. Idempotent, and called from both
// Serve and BuildDeps because a caller may skip Serve — the first one wins and
// the rest are free.
func BootMaster(dataDir string) { masterOnce.Do(func() { masterErr = resolveMaster(dataDir) }) }

// Master is the key this process holds, or nil.
func Master() []byte { return master }

// MasterB64 is Master in the base64 form Config and the embedded KMS client carry.
func MasterB64() string {
	if len(master) == 0 {
		return ""
	}
	return base64.StdEncoding.EncodeToString(master)
}

// MasterErr reports why no key is held, or nil. A process with no key stays UP —
// its stores refuse — so this is the only place the reason survives.
func MasterErr() error { return masterErr }

// MasterFrom names where the key came from, for the boot line.
func MasterFrom() string { return masterFrom }

func resolveMaster(dataDir string) error {
	if k, ok := decodeMaster(os.Getenv(MasterEnv)); ok {
		if err := cek.SetMaster(k); err != nil {
			return fmt.Errorf("master: %w", err)
		}
		master, masterFrom = k, MasterEnv
		return nil
	}

	// No key. Minting one is honest only over a directory nothing preceded, and
	// this is the one question that tells a laptop from a deployment that has
	// lost its KMS — a question about the disk, needing no notion of "production".
	had, err := hasDatabases(dataDir)
	if err != nil {
		return fmt.Errorf("master: no key configured, and %s could not be read to tell whether one is needed: %w", dataDir, err)
	}
	if had {
		return fmt.Errorf("master: no key configured, but %s already holds databases — minting a new one would make every one of them unreadable; restore the key this deployment was given", dataDir)
	}

	k, err := cek.SetDevMaster()
	if err != nil {
		return err
	}
	master, masterFrom = k, "dev key"
	return nil
}

// hasDatabases reports whether dataDir already holds at least one database. The
// first `.db` under the tree answers it, so a large directory costs a partial
// walk and an empty one costs a stat. A missing directory is the clearest
// possible "nothing preceded this process"; an UNREADABLE subtree is not proof
// of absence and is reported, because the point here is to refuse unless we are
// sure the directory is empty.
func hasDatabases(dataDir string) (bool, error) {
	if dataDir == "" {
		return false, nil
	}
	found := false
	err := filepath.WalkDir(dataDir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(d.Name(), ".db") {
			found = true
			return fs.SkipAll
		}
		return nil
	})
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	return found, err
}

func decodeMaster(s string) ([]byte, bool) {
	k, err := base64.StdEncoding.DecodeString(strings.TrimSpace(s))
	if err != nil || len(k) != 32 {
		return nil, false
	}
	return k, true
}
