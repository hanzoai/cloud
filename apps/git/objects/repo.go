package objects

import (
	"bytes"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/format/index"
	"github.com/go-git/go-git/v5/plumbing/storer"
	"github.com/go-git/go-git/v5/storage"
	"github.com/hanzoai/cloud/sdk/dialect"
)

// ---- references ----

const symbolic = "ref: "

func refValue(ref *plumbing.Reference) string {
	if ref.Type() == plumbing.SymbolicReference {
		return symbolic + ref.Target().String()
	}
	return ref.Hash().String()
}

func parseRef(name, value string) *plumbing.Reference {
	if target, ok := strings.CutPrefix(value, symbolic); ok {
		return plumbing.NewSymbolicReference(plumbing.ReferenceName(name), plumbing.ReferenceName(target))
	}
	return plumbing.NewHashReference(plumbing.ReferenceName(name), plumbing.NewHash(value))
}

func checkRef(ref *plumbing.Reference) error {
	if err := ref.Name().Validate(); err != nil {
		return err
	}
	if err := dialect.Text(ref.Name().String()); err != nil {
		return err
	}
	if ref.Type() == plumbing.SymbolicReference {
		if err := ref.Target().Validate(); err != nil {
			return err
		}
		return dialect.Text(ref.Target().String())
	}
	return nil
}

// Reference reads one ref.
func (r *Repo) Reference(name plumbing.ReferenceName) (*plumbing.Reference, error) {
	var v string
	err := r.q.QueryRowContext(r.ctx, `SELECT value FROM refs WHERE repo = $1 AND name = $2`, r.id, name.String()).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, plumbing.ErrReferenceNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("objects: ref %s: %w", name, err)
	}
	return parseRef(name.String(), v), nil
}

// SetReference sets a ref to ref, whatever it was.
func (r *Repo) SetReference(ref *plumbing.Reference) error {
	if err := checkRef(ref); err != nil {
		return err
	}
	_, err := r.q.ExecContext(r.ctx,
		`INSERT INTO refs (repo, name, value) VALUES ($1, $2, $3)
		 ON CONFLICT (repo, name) DO UPDATE SET value = excluded.value`,
		r.id, ref.Name().String(), refValue(ref))
	if err != nil {
		return fmt.Errorf("objects: set ref %s: %w", ref.Name(), err)
	}
	return nil
}

// CheckAndSetReference sets new when the ref is still at old, and answers
// storage.ErrReferenceHasChanged when it is not (or is not there). With no old it is
// SetReference. The compare and the set are one statement, so two writers that read the
// same value cannot both move the ref: the second finds it changed.
func (r *Repo) CheckAndSetReference(new, old *plumbing.Reference) error {
	if old == nil {
		return r.SetReference(new)
	}
	if err := checkRef(new); err != nil {
		return err
	}
	res, err := r.q.ExecContext(r.ctx,
		`UPDATE refs SET value = $1 WHERE repo = $2 AND name = $3 AND value = $4`,
		refValue(new), r.id, new.Name().String(), refValue(old))
	if err != nil {
		return fmt.Errorf("objects: set ref %s: %w", new.Name(), err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return storage.ErrReferenceHasChanged
	}
	return nil
}

// CreateReference makes a ref that is not there, and answers storage.ErrReferenceHasChanged
// when it is: a create is a compare against nothing.
func (r *Repo) CreateReference(ref *plumbing.Reference) error {
	if err := checkRef(ref); err != nil {
		return err
	}
	res, err := r.q.ExecContext(r.ctx,
		`INSERT INTO refs (repo, name, value) VALUES ($1, $2, $3) ON CONFLICT (repo, name) DO NOTHING`,
		r.id, ref.Name().String(), refValue(ref))
	if err != nil {
		return fmt.Errorf("objects: create ref %s: %w", ref.Name(), err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return storage.ErrReferenceHasChanged
	}
	return nil
}

// DeleteReference removes a ref that is still at old, and answers
// storage.ErrReferenceHasChanged when it is not.
func (r *Repo) DeleteReference(old *plumbing.Reference) error {
	res, err := r.q.ExecContext(r.ctx,
		`DELETE FROM refs WHERE repo = $1 AND name = $2 AND value = $3`,
		r.id, old.Name().String(), refValue(old))
	if err != nil {
		return fmt.Errorf("objects: delete ref %s: %w", old.Name(), err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return storage.ErrReferenceHasChanged
	}
	return nil
}

// RemoveReference removes a ref, whatever it was.
func (r *Repo) RemoveReference(name plumbing.ReferenceName) error {
	_, err := r.q.ExecContext(r.ctx, `DELETE FROM refs WHERE repo = $1 AND name = $2`, r.id, name.String())
	if err != nil {
		return fmt.Errorf("objects: remove ref %s: %w", name, err)
	}
	return nil
}

// References lists every ref, HEAD included, in name order.
func (r *Repo) References() ([]*plumbing.Reference, error) {
	rows, err := r.q.QueryContext(r.ctx, `SELECT name, value FROM refs WHERE repo = $1 ORDER BY name`, r.id)
	if err != nil {
		return nil, fmt.Errorf("objects: refs: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []*plumbing.Reference
	for rows.Next() {
		var n, v string
		if err := rows.Scan(&n, &v); err != nil {
			return nil, fmt.Errorf("objects: refs: %w", err)
		}
		out = append(out, parseRef(n, v))
	}
	return out, rows.Err()
}

func (r *Repo) IterReferences() (storer.ReferenceIter, error) {
	refs, err := r.References()
	if err != nil {
		return nil, err
	}
	return storer.NewReferenceSliceIter(refs), nil
}

func (r *Repo) CountLooseRefs() (int, error) {
	var n int
	if err := r.q.QueryRowContext(r.ctx, `SELECT COUNT(*) FROM refs WHERE repo = $1`, r.id).Scan(&n); err != nil {
		return 0, fmt.Errorf("objects: count refs: %w", err)
	}
	return n, nil
}

// PackRefs has nothing to do: refs are rows.
func (r *Repo) PackRefs() error { return nil }

// ---- shallow, config, index, modules ----

// SetShallow replaces the list of commits whose parents this repository lacks.
func (r *Repo) SetShallow(hashes []plumbing.Hash) error {
	if _, err := r.q.ExecContext(r.ctx, `DELETE FROM shallows WHERE repo = $1`, r.id); err != nil {
		return fmt.Errorf("objects: set shallow: %w", err)
	}
	for _, h := range hashes {
		if _, err := r.q.ExecContext(r.ctx,
			`INSERT INTO shallows (repo, oid) VALUES ($1, $2) ON CONFLICT (repo, oid) DO NOTHING`, r.id, h[:]); err != nil {
			return fmt.Errorf("objects: set shallow: %w", err)
		}
	}
	return nil
}

func (r *Repo) Shallow() ([]plumbing.Hash, error) {
	rows, err := r.q.QueryContext(r.ctx, `SELECT oid FROM shallows WHERE repo = $1 ORDER BY oid`, r.id)
	if err != nil {
		return nil, fmt.Errorf("objects: shallow: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []plumbing.Hash
	for rows.Next() {
		var b []byte
		if err := rows.Scan(&b); err != nil {
			return nil, fmt.Errorf("objects: shallow: %w", err)
		}
		out = append(out, hashOf(b))
	}
	return out, rows.Err()
}

func (r *Repo) file(name string) ([]byte, bool, error) {
	var b []byte
	err := r.q.QueryRowContext(r.ctx, `SELECT data FROM files WHERE repo = $1 AND name = $2`, r.id, name).Scan(&b)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("objects: read %s: %w", name, err)
	}
	return b, true, nil
}

func (r *Repo) setFile(name string, data []byte) error {
	_, err := r.q.ExecContext(r.ctx,
		`INSERT INTO files (repo, name, data) VALUES ($1, $2, $3)
		 ON CONFLICT (repo, name) DO UPDATE SET data = excluded.data`, r.id, name, data)
	if err != nil {
		return fmt.Errorf("objects: write %s: %w", name, err)
	}
	return nil
}

// Config reads the repository's config, a fresh one when it has none.
func (r *Repo) Config() (*config.Config, error) {
	b, ok, err := r.file("config")
	if err != nil {
		return nil, err
	}
	if !ok {
		return config.NewConfig(), nil
	}
	return config.ReadConfig(bytes.NewReader(b))
}

func (r *Repo) SetConfig(cfg *config.Config) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	b, err := cfg.Marshal()
	if err != nil {
		return err
	}
	return r.setFile("config", b)
}

// Index reads the staging index a non-bare working copy would keep, an empty one when
// there is none. A bare repository has none; the method is here because go-git's
// storage.Storer requires it.
func (r *Repo) Index() (*index.Index, error) {
	b, ok, err := r.file("index")
	if err != nil {
		return nil, err
	}
	if !ok {
		return &index.Index{Version: 2}, nil
	}
	idx := &index.Index{}
	if err := index.NewDecoder(bytes.NewReader(b)).Decode(idx); err != nil {
		return nil, err
	}
	return idx, nil
}

func (r *Repo) SetIndex(idx *index.Index) error {
	var b bytes.Buffer
	if err := index.NewEncoder(&b).Encode(idx); err != nil {
		return err
	}
	return r.setFile("index", b.Bytes())
}

// Module is the storer of a submodule: a repository of its own, keyed under this one.
// One that does not exist is empty, as go-git asks.
func (r *Repo) Module(name string) (storage.Storer, error) {
	return newRepo(r.ctx, r.q, r.s, r.id+"/modules/"+name), nil
}
