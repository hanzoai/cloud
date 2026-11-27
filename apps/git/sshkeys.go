package git

// sshkeys.go is the SSH public-key registry: the ONE place a presented key is
// resolved to a person or a repository.
//
// The SSH handshake knows only the key the client presents, before any org is
// named, so the registry is a single table in the system namespace keyed by the
// key's fingerprint — a schema on hanzo-sql, shared by every replica — and the row
// says whose it is. A fingerprint belongs to exactly one row: the same key cannot
// be registered twice, in one org or across two.
//
// Two kinds share the table. A USER key authenticates the person who registered
// it into their org. A DEPLOY key is bound to one repository and may be read-only;
// it is what a coding run is handed (the forge minted these per run and deleted
// them at the end, forge/grant.go). A public key is not a secret, and no private
// material ever reaches this store.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/dialect"
	"github.com/hanzoai/cloud/sdk/locus"
	"github.com/hanzoai/cloud/sdk/mint"
	"github.com/hanzoai/cloud/sdk/text"
	"github.com/hanzoai/namespace"
	"github.com/zap-proto/zip"
	"golang.org/x/crypto/ssh"
)

// keySubsystem names the store in the system namespace; manifest.SQLStores lists it.
const keySubsystem = "ssh_keys"

const (
	kindUser   = "user"
	kindDeploy = "deploy"
)

var (
	errKeyTaken    = errors.New("git: ssh key already registered")
	errKeyNotFound = errors.New("git: ssh key not found")
)

// sshKey is one registered key.
type sshKey struct {
	ID          string
	Fingerprint string
	Org         string
	Project     string
	Repo        string // empty for a user key
	Creator     string
	Title       string
	Kind        string
	ReadOnly    bool
	PublicKey   string
	CreatedAt   int64
}

type keyIndex struct{ db *sql.DB }

// Close releases the handle.
func (k *keyIndex) Close() error { return k.db.Close() }

const keyDDL = `
CREATE TABLE IF NOT EXISTS ssh_keys (
  fingerprint TEXT PRIMARY KEY,
  id          TEXT NOT NULL,
  org         TEXT NOT NULL,
  project     TEXT NOT NULL DEFAULT '',
  repo        TEXT NOT NULL DEFAULT '',
  creator     TEXT NOT NULL DEFAULT '',
  title       TEXT NOT NULL DEFAULT '',
  kind        TEXT NOT NULL,
  read_only   BIGINT NOT NULL DEFAULT 0,
  public_key  TEXT NOT NULL,
  created_at  BIGINT NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS ux_ssh_keys_id ON ssh_keys(id);
CREATE INDEX IF NOT EXISTS ix_ssh_keys_org ON ssh_keys(org, created_at);
`

func openKeyIndex(dataDir string) (*keyIndex, error) {
	return locus.Use(context.Background(), nil, namespace.System(), keySubsystem, dataDir, func(db *sql.DB) (*keyIndex, error) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if err := dialect.Of(db).Migrate(ctx, db, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, keyDDL)
			return err
		}); err != nil {
			return nil, fmt.Errorf("git: init ssh key index: %w", err)
		}
		return &keyIndex{db: db}, nil
	})
}

// keyIndexFor opens the registry once per service.
func keyIndexFor(s *cloud.Service[state]) (*keyIndex, error) {
	return s.State.keys.get(s.State.dataDir)
}

const keyCols = `fingerprint,id,org,project,repo,creator,title,kind,read_only,public_key,created_at`

func scanKey(sc interface{ Scan(...any) error }) (sshKey, error) {
	var k sshKey
	var ro int64
	err := sc.Scan(&k.Fingerprint, &k.ID, &k.Org, &k.Project, &k.Repo, &k.Creator, &k.Title, &k.Kind, &ro, &k.PublicKey, &k.CreatedAt)
	k.ReadOnly = ro != 0
	return k, err
}

func (k *keyIndex) add(ctx context.Context, v sshKey) error {
	if err := dialect.Text(v.ID, v.Fingerprint, v.Org, v.Project, v.Repo, v.Creator, v.Title, v.Kind, v.PublicKey); err != nil {
		return fmt.Errorf("add ssh key: %w", err)
	}
	ro := int64(0)
	if v.ReadOnly {
		ro = 1
	}
	_, err := k.db.ExecContext(ctx, `INSERT INTO ssh_keys (`+keyCols+`) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
		v.Fingerprint, v.ID, v.Org, v.Project, v.Repo, v.Creator, v.Title, v.Kind, ro, v.PublicKey, v.CreatedAt)
	if err != nil {
		if dialect.Unique(err) {
			return errKeyTaken
		}
		return fmt.Errorf("add ssh key: %w", err)
	}
	return nil
}

// lookup resolves a presented key.
func (k *keyIndex) lookup(ctx context.Context, fingerprint string) (sshKey, error) {
	v, err := scanKey(k.db.QueryRowContext(ctx, `SELECT `+keyCols+` FROM ssh_keys WHERE fingerprint=$1`, fingerprint))
	if errors.Is(err, sql.ErrNoRows) {
		return sshKey{}, errKeyNotFound
	}
	return v, err
}

// list reads an org's keys, newest first; repo narrows to one repository's deploy
// keys, and creator (when set) to the keys one person registered.
func (k *keyIndex) list(ctx context.Context, org, creator, repo string) ([]sshKey, error) {
	q := `SELECT ` + keyCols + ` FROM ssh_keys WHERE org=$1`
	args := []any{org}
	if creator != "" {
		args = append(args, creator)
		q += fmt.Sprintf(` AND creator=$%d`, len(args))
	}
	if repo != "" {
		args = append(args, repo)
		q += fmt.Sprintf(` AND repo=$%d`, len(args))
	}
	rows, err := k.db.QueryContext(ctx, q+` ORDER BY created_at DESC, id ASC`, args...)
	if err != nil {
		return nil, fmt.Errorf("list ssh keys: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []sshKey
	for rows.Next() {
		v, err := scanKey(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (k *keyIndex) get(ctx context.Context, org, id string) (sshKey, error) {
	v, err := scanKey(k.db.QueryRowContext(ctx, `SELECT `+keyCols+` FROM ssh_keys WHERE org=$1 AND id=$2`, org, id))
	if errors.Is(err, sql.ErrNoRows) {
		return sshKey{}, errKeyNotFound
	}
	return v, err
}

func (k *keyIndex) remove(ctx context.Context, org, id string) (bool, error) {
	res, err := k.db.ExecContext(ctx, `DELETE FROM ssh_keys WHERE org=$1 AND id=$2`, org, id)
	if err != nil {
		return false, fmt.Errorf("delete ssh key: %w", err)
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// parseKey reads one authorized_keys line and refuses what a registry must not
// hold: options (a `command=` prefix would be honoured by OpenSSH and is never
// honoured here, so it is refused rather than half-meant), a second key, and a
// key type OpenSSH itself retired.
func parseKey(line string) (ssh.PublicKey, string, error) {
	line = strings.TrimSpace(line)
	if line == "" || strings.ContainsAny(line, "\r\n") {
		return nil, "", errors.New("key must be one authorized_keys line")
	}
	pub, _, opts, rest, err := ssh.ParseAuthorizedKey([]byte(line))
	if err != nil {
		return nil, "", fmt.Errorf("not an ssh public key: %w", err)
	}
	if len(opts) > 0 {
		return nil, "", errors.New("key options are not supported")
	}
	if len(strings.TrimSpace(string(rest))) > 0 {
		return nil, "", errors.New("key must be one authorized_keys line")
	}
	if pub.Type() == ssh.KeyAlgoDSA {
		return nil, "", errors.New("dsa keys are not accepted")
	}
	return pub, ssh.FingerprintSHA256(pub), nil
}

// ── wire shapes ──────────────────────────────────────────────────────────────

// keyIn registers a public key.
type keyIn struct {
	// Title is a label for the key ("laptop"). Required.
	Title string `json:"title"`
	// Key is the public key as one authorized_keys line ("ssh-ed25519 AAAA… comment"). Required.
	Key string `json:"key"`
	// Repo makes it a deploy key for that repository of the caller's project scope; absent registers a user key.
	Repo string `json:"repo"`
	// ReadOnly limits a deploy key to fetching. Ignored for a user key.
	ReadOnly bool `json:"readOnly"`
}

// keyListQuery filters the keys read.
type keyListQuery struct {
	// Repo narrows to one repository's deploy keys.
	Repo string `json:"repo" url:"repo"`
}

// keyRef addresses one key.
type keyRef struct {
	// ID is the key's identifier, from the :id path segment.
	ID string `json:"id"`
}

// keyView is one registered key; the key itself is public and is shown whole.
type keyView struct {
	// ID is the key's identifier ("key_…"), the handle to delete it by.
	ID string `json:"id"`
	// Title is the label given at registration.
	Title string `json:"title"`
	// Kind is "user" for a person's key or "deploy" for a repository's.
	Kind string `json:"kind"`
	// Repo is the repository a deploy key is bound to; empty for a user key.
	Repo string `json:"repo"`
	// ReadOnly is whether a deploy key may only fetch.
	ReadOnly bool `json:"readOnly"`
	// Fingerprint is the key's SHA256 fingerprint, the form `ssh-keygen -l` prints.
	Fingerprint string `json:"fingerprint"`
	// Creator is the subject that registered it.
	Creator string `json:"creator"`
	// CreatedAt is RFC 3339 UTC.
	CreatedAt string `json:"createdAt"`
}

// keyList is the collection envelope.
type keyList struct {
	// Data holds the keys.
	Data []keyView `json:"data"`
}

func toKeyView(k sshKey) keyView {
	return keyView{ID: k.ID, Title: k.Title, Kind: k.Kind, Repo: k.Repo, ReadOnly: k.ReadOnly,
		Fingerprint: k.Fingerprint, Creator: k.Creator, CreatedAt: text.Unix(k.CreatedAt)}
}

// addKey registers an SSH public key: the caller's own, or — with `repo` — a deploy
// key for one repository. Answers 201. A key already registered anywhere is a 409,
// and a repository outside the caller's scope is not found. Deploy keys are an
// admin's to register; a user key authenticates its owner into this org only.
//
// Example: {"title": "laptop", "key": "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAA… me@laptop"}
func (o ops) addKey(ctx context.Context, in *keyIn) (*keyView, error) {
	t, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	title := strings.TrimSpace(in.Title)
	if title == "" || len(title) > 200 {
		return nil, zip.ErrBadRequest("title is required and at most 200 characters")
	}
	_, fp, err := parseKey(in.Key)
	if err != nil {
		return nil, zip.ErrBadRequest(err.Error())
	}
	v := sshKey{ID: mint.ID("key"), Fingerprint: fp, Org: t.org, Creator: t.user, Title: title,
		Kind: kindUser, PublicKey: strings.TrimSpace(in.Key), CreatedAt: time.Now().Unix()}
	if repo := strings.TrimSpace(in.Repo); repo != "" {
		if !t.admin {
			return nil, zip.ErrForbidden("deploy keys are registered by an org admin")
		}
		_, name, serr := o.scoped(ctx, repo)
		if serr != nil {
			return nil, serr
		}
		v.Kind, v.Project, v.Repo, v.ReadOnly = kindDeploy, t.project, name, in.ReadOnly
	}
	idx, err := keyIndexFor(o.s)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "open key index: %v", err)
	}
	if err := idx.add(ctx, v); err != nil {
		if errors.Is(err, errKeyTaken) {
			return nil, zip.ErrConflict("this key is already registered")
		}
		return nil, internalErr(err)
	}
	view := toKeyView(v)
	return &view, nil
}

// listKeys reads the keys the caller may see: their own, or every key of the org
// for an org admin. `repo` narrows to one repository's deploy keys.
func (o ops) listKeys(ctx context.Context, in *keyListQuery) (*keyList, error) {
	t, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	creator := t.user
	if t.admin {
		creator = ""
	}
	repo := ""
	if in.Repo != "" {
		repo = normalizeName(in.Repo)
	}
	idx, err := keyIndexFor(o.s)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "open key index: %v", err)
	}
	rows, err := idx.list(ctx, t.org, creator, repo)
	if err != nil {
		return nil, internalErr(err)
	}
	out := &keyList{Data: make([]keyView, 0, len(rows))}
	for _, r := range rows {
		out.Data = append(out.Data, toKeyView(r))
	}
	return out, nil
}

// deleteKey removes a key; a push or fetch with it fails from the next connection.
// Answers 204. The person who registered it or an org admin may remove it; any
// other caller, and an id that is not this org's, is not found.
//
// Example: {"id": "key_7c2e"}
func (o ops) deleteKey(ctx context.Context, in *keyRef) (*cloud.Unit, error) {
	t, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	idx, err := keyIndexFor(o.s)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "open key index: %v", err)
	}
	id := strings.TrimSpace(in.ID)
	k, err := idx.get(ctx, t.org, id)
	if errors.Is(err, errKeyNotFound) || (err == nil && !t.admin && k.Creator != t.user) {
		return nil, zip.ErrNotFound("key not found")
	}
	if err != nil {
		return nil, internalErr(err)
	}
	if _, err := idx.remove(ctx, t.org, id); err != nil {
		return nil, internalErr(err)
	}
	return nil, nil
}
