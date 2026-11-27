package train

// keys.go — the org's key its artifacts are encrypted under at rest.
//
// Every object written for an org is server-side encrypted under that org's own
// key (SSE-KMS): the upload grant names the key, the signature covers that
// header, Hanzo S3 asks KMS for the key and seals each object's data key under
// it, and the read-back refuses an object that is not encrypted under it. The
// key is minted into KMS the first time the org stores anything and is read from
// there by S3 alone; cloud never holds it in config, never answers it, and keeps
// only the fact that it exists.
//
// It is minted once. Two writers of one org's key would leave every object sealed
// under the loser's unreadable, so a mint reads first, mints under a per-process
// single flight and a lock of the org's store that every replica takes, and reads
// back what it wrote. An org's requests are served by every replica, so two of them
// can ask for its first key at once: the lock is what makes the second find the
// first's key and not write over it.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/hanzoai/cloud/sdk/secret"
	"golang.org/x/sync/singleflight"
)

const (
	// keyPath and keyEnv are where Hanzo S3's KMS provider reads the key a request
	// names: the secret <keyPath>/<key id> in environment keyEnv, its provider's
	// defaults (kms.lux.path, kms.lux.env).
	keyPath = "s3"
	keyEnv  = "prod"
)

// keyID names an org's key: what an SSE-KMS request carries, and the secret's name.
func keyID(org string) string { return "train-" + org }

var errNoKMS = errors.New("train: no KMS to keep the org's key in")

// keys mints and confirms orgs' keys in KMS.
type keys struct {
	kms   secret.Store
	once  singleflight.Group
	known sync.Map // key id → struct{}: confirmed in KMS by this process
}

func newKeys(kms secret.Store) *keys { return &keys{kms: kms} }

// seal is the id of org's key, minted in KMS when the org has none. st is the org's
// store, where the replicas meet.
func (k *keys) seal(ctx context.Context, st *store, org string) (string, error) {
	id := keyID(org)
	if _, ok := k.known.Load(id); ok {
		return id, nil
	}
	if k.kms == nil {
		return "", errNoKMS
	}
	ref := keyPath + "/" + id + "@" + keyEnv
	_, err, _ := k.once.Do(ref, func() (any, error) {
		release, err := st.minting(ctx)
		if err != nil {
			return nil, err
		}
		defer release()
		err = k.check(ctx, ref)
		if !errors.Is(err, secret.ErrNone) {
			return nil, err
		}
		raw := make([]byte, 32)
		if _, err := rand.Read(raw); err != nil {
			return nil, err
		}
		v := []byte(hex.EncodeToString(raw))
		clear(raw)
		err = k.kms.PutSecret(ctx, ref, v)
		clear(v)
		if err != nil {
			return nil, err
		}
		return nil, k.check(ctx, ref)
	})
	if err != nil {
		return "", err
	}
	k.known.Store(id, struct{}{})
	return id, nil
}

// check reads a key back and confirms it is one S3 can use — 32 bytes, in hex —
// then forgets the bytes.
func (k *keys) check(ctx context.Context, ref string) error {
	v, err := k.kms.GetSecret(ctx, ref)
	if err != nil {
		return err
	}
	defer clear(v)
	raw, err := hex.DecodeString(strings.TrimSpace(string(v)))
	defer clear(raw)
	if err != nil || len(raw) != 32 {
		return fmt.Errorf("train: the key at %s is not 32 bytes of hex", ref)
	}
	return nil
}
