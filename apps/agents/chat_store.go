package agents

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/locus"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/hanzoai/cek"
	"github.com/hanzoai/namespace"
	"github.com/hanzoai/orm"
	luxlog "github.com/luxfi/log"
)

// The conversation surface (hanzoai/agent) keeps each org's conversations, and one
// index of the share links, in databases this host opens and hands over, so they are
// schemas of hanzo-sql like every other store and not files the module writes under
// the data directory. Subsystem "agent" is the per-org conversations and "agent-shares"
// the index; "agents" is the run registry beside them and a different store.
const (
	chatSubsystem   = "agent"
	sharesSubsystem = "agent-shares"
)

// chatDB is a database the module borrows: the store owns the close.
type chatDB struct{ *sql.DB }

// openChat is the store's own open: it makes the orm's table in the database, so a
// move finds the same tables on both sides of it. The module adapts the handle again
// for its own use; the orm borrows the handle and closes nothing.
func openChat(db *sql.DB) (chatDB, error) {
	if _, err := orm.AdaptSQL(db); err != nil {
		_ = db.Close()
		return chatDB{}, fmt.Errorf("agents: conversation store: %w", err)
	}
	return chatDB{db}, nil
}

// chatStores holds the per-org conversation databases and the share index.
type chatStores struct {
	orgs *cloud.OrgStore[chatDB]

	mu     sync.Mutex
	shares *chatDB
}

func newChatStores(deps cloud.Deps) *chatStores {
	convertChats(cloud.DataDir())
	b := cloud.NewBase(deps, chatSubsystem)
	return &chatStores{
		orgs: cloud.NewOrgStore(b, chatSubsystem, openChat),
	}
}

// open is the module's Deps.Open: the org's conversation database.
func (c *chatStores) open(org string) (*sql.DB, error) {
	ns, err := principal.OrgNamespace(org, "")
	if err != nil {
		return nil, err
	}
	// The module wrote these files in the clear. A file still on the volume is made
	// the encrypted store the seam opens (a no-op for one that already is, and for
	// none), and the seam then moves it to hanzo-sql when the policy says to.
	if path, err := namespace.Path(cloud.DataDir(), ns, chatSubsystem); err == nil {
		if err := cek.Convert(ns, chatSubsystem, path); err != nil {
			return nil, fmt.Errorf("agents: encrypt %s's conversations: %w", org, err)
		}
	}
	s, err := c.orgs.For(ns)
	if err != nil {
		return nil, err
	}
	return s.DB, nil
}

// index is the module's Deps.Shares: the share index, one for the deployment.
func (c *chatStores) index() (*sql.DB, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.shares != nil {
		return c.shares.DB, nil
	}
	s, err := locus.Use(context.Background(), nil, namespace.System(), sharesSubsystem, cloud.DataDir(),
		openChat)
	if err != nil {
		return nil, fmt.Errorf("agents: open the share index: %w", err)
	}
	c.shares = &s
	return s.DB, nil
}

// Close closes every database this holds.
func (c *chatStores) Close() error {
	err := c.orgs.CloseAll()
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.shares != nil {
		if e := c.shares.Close(); err == nil {
			err = e
		}
		c.shares = nil
	}
	return err
}

// convertChats makes every conversation file the module wrote in the clear the
// encrypted store the seam opens, once, before the store is opened and before the
// sweep walks it. open does the same for one org on its first request, but the sweep
// moves files nobody has asked for, and a plaintext file opened as an encrypted store
// reads "file is not a database": the move recorded that as failed and the org's
// conversations stayed on the volume. A directory whose name is not an org's own
// namespace is left alone, as is a file that is already encrypted (Convert is a no-op).
func convertChats(dir string) {
	if dir == "" {
		return
	}
	entries, err := os.ReadDir(filepath.Join(dir, "orgs"))
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		ns, err := principal.OrgNamespace(e.Name(), "")
		if err != nil {
			continue
		}
		path, err := namespace.Path(dir, ns, chatSubsystem)
		if err != nil || filepath.Dir(path) != filepath.Join(dir, "orgs", e.Name()) {
			continue
		}
		if _, err := os.Stat(path); err != nil {
			continue
		}
		if err := cek.Convert(ns, chatSubsystem, path); err != nil {
			luxlog.Default().Warn("agents: could not encrypt an org's conversations", "path", path, "err", err)
		}
	}
}
