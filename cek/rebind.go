// Copyright © 2026 Hanzo AI. MIT License.

package cek

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	sqlitedrv "github.com/hanzoai/sqlite"
)

// Rebind moves one store from one owner's key to another's. It exists because the
// principal became part of the derivation AFTER stores were already on disk: a tenant
// store written when every file keyed under the platform tag cannot be opened by its
// owner until its sidecar is rewrapped.
//
// It is an OPERATION, deliberately not a fallback inside Open. A store has one key and
// Open derives it one way; a second derivation tried on failure would mean every open
// silently accepts two answers forever, which is the thing that made the old binding
// unenforceable in the first place. Running this once is a migration. Leaving it in the
// open path would be a permanent ambiguity.
//
// Only the sidecar changes. The DEK and the fileID are read out under `from` and
// written straight back under `to`, so not one database page is rewritten and the file
// itself is never opened — which is why this is safe on a store too large to copy and
// why a failure cannot corrupt data. The write is atomic (temp + rename), so an
// interrupted rebind leaves either the old sidecar or the new one, never a torn file.
//
// It is idempotent in the way that matters: a store already bound to `to` fails to
// unwrap under `from` and returns ErrNotBound, so re-running a completed migration
// reports "already done" rather than damaging anything.
func Rebind(from, to Principal, path string) error {
	master, err := resolveMaster()
	if err != nil {
		return err
	}
	unlock, err := flock(path)
	if err != nil {
		return err
	}
	defer unlock()

	dekPath := path + dekSuffix
	sidecar, err := os.ReadFile(dekPath)
	if err != nil {
		return fmt.Errorf("cek: read sidecar %q: %w", dekPath, err)
	}
	dek, err := unwrapSidecar(from, master, sidecar)
	if err != nil {
		// Either it is already bound to `to`, or neither principal owns it. Say which,
		// because "already migrated" and "wrong key" need opposite responses.
		if _, err2 := unwrapSidecar(to, master, sidecar); err2 == nil {
			return fmt.Errorf("%w: %s is already bound to %s", ErrNotBound, path, to)
		}
		return fmt.Errorf("cek: %q does not unwrap under %s: %w", path, from, err)
	}
	defer zero(dek)

	// Same fileID — the file's identity does not change, only who may derive its KEK.
	fileID := sidecar[:fileIDLen]
	kek, aad, err := deriveFor(to, master, fileID)
	if err != nil {
		return err
	}
	defer zero(kek)
	wrapped, err := sqlitedrv.WrapDEK(kek, dek, aad)
	if err != nil {
		return fmt.Errorf("cek: rewrap DEK for %s: %w", to, err)
	}
	next := append(append(make([]byte, 0, fileIDLen+len(wrapped)), fileID...), wrapped...)

	tmp := dekPath + ".rebind"
	if err := os.WriteFile(tmp, next, 0o600); err != nil {
		return fmt.Errorf("cek: stage sidecar: %w", err)
	}
	if err := os.Rename(tmp, dekPath); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("cek: commit sidecar: %w", err)
	}
	return nil
}

// ErrNotBound reports that a store did not unwrap under the principal it was asked to
// move FROM — most often because it is already bound to the target, which makes a
// re-run a no-op rather than a failure.
var ErrNotBound = fmt.Errorf("cek: store not bound to the source principal")

// RebindOrgs binds every per-org store under dataDir to the org that owns it, which is
// the one migration the principal change requires. It walks {dataDir}/orgs/<slug>/,
// including the nested projects/<slug>/ level, and rebinds each store from Global to
// Org(slug).
//
// The reserved platform partition is skipped: it is not a tenant and keys as Global
// both before and after, so there is nothing to move.
//
// It never stops on one store's failure. A run over a live volume will meet stores that
// are already bound (a re-run, or a store created after the change) and those are
// counted as skipped, not errors — the point of the walk is to leave every store bound,
// and that is a state to converge on rather than a transaction.
func RebindOrgs(dataDir, platformSlug string) (bound, skipped int, errs []error) {
	orgsDir := filepath.Join(dataDir, "orgs")
	entries, err := os.ReadDir(orgsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, 0, nil // nothing deployed yet
		}
		return 0, 0, []error{fmt.Errorf("cek: read %q: %w", orgsDir, err)}
	}

	for _, e := range entries {
		if !e.IsDir() || e.Name() == platformSlug {
			continue
		}
		slug := e.Name()
		owner := Org(slug)
		for _, store := range orgStores(filepath.Join(orgsDir, slug)) {
			switch err := Rebind(Global, owner, store); {
			case err == nil:
				bound++
			case errors.Is(err, ErrNotBound):
				skipped++
			default:
				errs = append(errs, fmt.Errorf("%s: %w", store, err))
			}
		}
	}
	return bound, skipped, errs
}

// orgStores lists every database beneath one org's directory — the org-scoped stores
// and the project-scoped ones under projects/<slug>/. A store is identified by its
// SIDECAR, not by a .db file: on a pure-Go build the codec envelope keys the file out
// of band and the sidecar can be the only thing on disk, so looking for *.db would
// silently skip exactly those deployments.
func orgStores(dir string) []string {
	var out []string
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil //nolint:nilerr // an unreadable subtree is skipped, never fatal
		}
		if strings.HasSuffix(p, dekSuffix) {
			out = append(out, strings.TrimSuffix(p, dekSuffix))
		}
		return nil
	})
	return out
}
