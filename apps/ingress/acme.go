package ingress

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/hanzoai/cloud/sdk/tenure"
	"github.com/hanzoai/namespace"
	"golang.org/x/crypto/acme/autocert"
)

// acmeTable is the table autocert keeps its accounts, certificates and challenge
// tokens in.
const acmeTable = "ingress_acme"

// acmeWithin bounds one cache statement. A certificate is read while a handshake
// waits for it, so a database that does not answer is a handshake that fails, not one
// that hangs.
const acmeWithin = 10 * time.Second

// acmeCache is autocert's cache over the ingress store. It is the deployment's: every
// replica of the edge serves every host, so a certificate one obtained is the
// certificate the others serve, an account key is one account and not one per
// replica, and a challenge token one wrote is there for the validation another
// began. A directory on a pod's disk is none of these and does not outlive the pod.
//
// What it keeps is a private key and the certificates made from it, so every value is
// sealed under the deployment's data-plane master before it is written, bound to its
// name: a value carried to another name does not open.
type acmeCache struct {
	db     *sql.DB
	fields *tenure.Fields
}

// acme returns the store's autocert cache. A process with no usable data-plane
// master refuses: a private key is never written in the clear because a key was
// missing.
func (s *Store) acme(master []byte) (autocert.Cache, error) {
	c, err := tenure.NewCipher(master)
	if err != nil {
		return nil, err
	}
	f, err := c.Fields(namespace.System(), "ingress")
	if err != nil {
		return nil, err
	}
	return &acmeCache{db: s.db, fields: f}, nil
}

// Get returns what is cached under name, or autocert.ErrCacheMiss.
func (c *acmeCache) Get(ctx context.Context, name string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, acmeWithin)
	defer cancel()
	var sealed string
	err := c.db.QueryRowContext(ctx, `SELECT data FROM `+acmeTable+` WHERE name=$1`, name).Scan(&sealed)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, autocert.ErrCacheMiss
	}
	if err != nil {
		return nil, fmt.Errorf("ingress: read acme cache: %w", err)
	}
	r := c.fields.Row(acmeTable, name)
	plain := r.Open("data", sealed)
	if err := r.Err(); err != nil {
		return nil, fmt.Errorf("ingress: open acme cache %q: %w", name, err)
	}
	return []byte(plain), nil
}

// Put caches data under name, replacing what was there.
func (c *acmeCache) Put(ctx context.Context, name string, data []byte) error {
	ctx, cancel := context.WithTimeout(ctx, acmeWithin)
	defer cancel()
	r := c.fields.Row(acmeTable, name)
	sealed := r.Seal("data", string(data))
	if err := r.Err(); err != nil {
		return fmt.Errorf("ingress: seal acme cache %q: %w", name, err)
	}
	if _, err := c.db.ExecContext(ctx,
		`INSERT INTO `+acmeTable+` (name, data, updated_at) VALUES ($1, $2, $3)
		 ON CONFLICT(name) DO UPDATE SET data=excluded.data, updated_at=excluded.updated_at`,
		name, sealed, time.Now().Unix()); err != nil {
		return fmt.Errorf("ingress: write acme cache: %w", err)
	}
	return nil
}

// Delete removes what is cached under name. A name that holds nothing is not an error.
func (c *acmeCache) Delete(ctx context.Context, name string) error {
	ctx, cancel := context.WithTimeout(ctx, acmeWithin)
	defer cancel()
	if _, err := c.db.ExecContext(ctx, `DELETE FROM `+acmeTable+` WHERE name=$1`, name); err != nil {
		return fmt.Errorf("ingress: delete acme cache: %w", err)
	}
	return nil
}
