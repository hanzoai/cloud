// Copyright © 2026 Hanzo AI. MIT License.

package cloud

// master.go — the data-plane key, resolved once, before the first store opens.
//
// The deployment supplies it in the environment, from KMS, and every child
// inherits it — which is how they all open the same encrypted files. With none
// supplied, a process over an EMPTY data directory mints a random master that
// dies with it, so a laptop needs no configuration.
//
// THE REFUSAL IS THE LOAD-BEARING HALF. Minting over a directory that already
// holds databases SUCCEEDS, and every one of those files then reads as "file is
// not a database" while the data sits intact and unreadable. So that case is a
// fault: the stores fail closed rather than opening under a key nothing was
// written with.
//
// This replaced a 1,237-line broker that served per-app credential bundles over
// a socket. Its own docs said the boundary was against ACCIDENT, not against a
// peer that reads its neighbours — the stamp sat in the child's environment. The
// pod is the data-plane boundary either way.

import (
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"context"
	"github.com/hanzoai/cek"
	"github.com/luxfi/kms/pkg/store/mpcrek"
	"time"
)

// MasterEnv carries the base64 32-byte data-plane master. It is NOT scrubbed:
// inheritance is how a child process is given the key.
//
// It is also the weakest way to hold a root. A key in the environment is a key in
// a Secret, so whatever reads Secrets — a cloud API token, a shell in the pod, a
// snapshot of the volume — holds the one value that opens every store. Sealing
// under a root that sits beside the ciphertext protects nothing from the reader
// who has both.
const MasterEnv = "CLOUD_KMS_MASTER_KEY_REF"

// The ring. It holds shares: no single holder can produce the root, and the
// sealed form travels as ordinary configuration because ciphertext is safe to
// leave lying about. When these are set the ring is the only source — there is no
// falling back to the environment, because a root that can be reached the weak way
// is only ever as strong as the weak way.
const (
	RingEndpointEnv = "CLOUD_KMS_MPC_ENDPOINT"
	RingSealedEnv   = "CLOUD_KMS_MPC_SEALED_B64"
	RingKeyIDEnv    = "CLOUD_KMS_MPC_KEY_ID"

	// RingVaultEnv names the org the share set is filed under. The ring scopes
	// every answer by it and refuses a request that names only a key, so
	// without this the call cannot succeed — which is why this path, though
	// written and shipped, had never once opened anything.
	RingVaultEnv = "CLOUD_KMS_MPC_VAULT"
)

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
	if endpoint := strings.TrimSpace(os.Getenv(RingEndpointEnv)); endpoint != "" {
		k, err := ringMaster(endpoint)
		if err != nil {
			// Fail closed. A deployment that asked for the ring and did not get it
			// must not come up on a key the pod could read — that is the weakness
			// the ring was chosen to remove, and reaching for it here would make
			// the choice cosmetic.
			return err
		}
		if err := cek.SetMaster(k); err != nil {
			return fmt.Errorf("master: %w", err)
		}
		master, masterFrom = k, "ring"
		return nil
	}

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

// ringMaster opens the sealed root with a quorum of the ring. The endpoint and
// the ciphertext are ordinary configuration; the shares are not, and no share
// lives here.
func ringMaster(endpoint string) ([]byte, error) {
	if raw := strings.TrimSpace(os.Getenv(MasterEnv)); raw != "" {
		return nil, fmt.Errorf("master: %s and %s are both set — a root reachable from the environment is only as strong as the environment, so the ring is not a second opinion; unset %s",
			RingEndpointEnv, MasterEnv, MasterEnv)
	}
	sealedB64 := strings.TrimSpace(os.Getenv(RingSealedEnv))
	if sealedB64 == "" {
		return nil, fmt.Errorf("master: %s is set but %s is not — the ring holds shares, not ciphertexts", RingEndpointEnv, RingSealedEnv)
	}
	sealed, err := base64.StdEncoding.DecodeString(sealedB64)
	if err != nil {
		return nil, fmt.Errorf("master: %s is not base64: %w", RingSealedEnv, err)
	}
	keyID := strings.TrimSpace(os.Getenv(RingKeyIDEnv))
	if keyID == "" {
		keyID = "root"
	}
	vault := strings.TrimSpace(os.Getenv(RingVaultEnv))
	if vault == "" {
		return nil, fmt.Errorf("master: %s is set but %s is not — the ring files a share set under an owner and will not answer for a key alone", RingEndpointEnv, RingVaultEnv)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return mpcrek.Bootstrap(ctx, mpcrek.Config{
		Endpoint: endpoint,
		Vault:    vault,
		KeyID:    keyID,
		NodeID:   "cloud-rek-bootstrap",
		Timeout:  30 * time.Second,
		Sealed:   sealed,
	})
}
