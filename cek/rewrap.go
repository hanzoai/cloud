// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// rewrap.go — the one-time migration "a store's key names its owner" needed and
// did not ship with.
//
// That change (cek: a store's key names its owner) started deriving a store's KEK
// from (principal, fileID) instead of fileID alone. Every sidecar written before it
// was wrapped under Global, so every pre-existing ORG store stopped opening the
// moment the new derivation landed — reported as
//
//	cek: unwrap DEK (wrong master key or corrupt sidecar)
//
// which is true and misleading: the master key is right and the sidecar is intact.
// Only the identity the KEK derives from moved.
//
// Rewrap moves ONE sidecar forward: unwrap the DEK under the legacy principal,
// re-wrap the SAME DEK under the store's real owner, write it atomically. The DEK
// never changes, so the database's pages are untouched — this rewrites the wrapper
// and nothing else.
//
// It is NOT a compatibility shim. There is exactly one derivation — the owner-bound
// one — and this walks the old world into it once. A store that already opens under
// its owner is left alone; a store that opens under NEITHER identity is reported and
// skipped, never "repaired" by minting a fresh DEK, because a new DEK would answer
// every future open with plausible garbage instead of an error.
package cek

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// RewrapResult is the honest per-store outcome.
type RewrapResult struct {
	Path    string // the database path whose sidecar was examined
	Owner   string // the principal it now derives from
	Rewrapped bool // true when this call moved it forward
	Already bool   // true when it already opened under its owner
	Err     error  // non-nil when it opened under neither identity
}

// Rewrap migrates one sidecar from the legacy (Global) derivation to owner-bound.
//
// Order matters and is deliberate: try the OWNER first. A store already migrated
// must not be touched, and trying the legacy identity first on an already-correct
// store would fail and look like corruption.
func Rewrap(owner Principal, dbPath string) RewrapResult {
	res := RewrapResult{Path: dbPath, Owner: owner.String()}
	master := Master()
	if len(master) == 0 {
		res.Err = errors.New("cek: no master key configured")
		return res
	}
	sidecarPath := dbPath + dekSuffix
	sidecar, err := os.ReadFile(sidecarPath)
	if err != nil {
		res.Err = fmt.Errorf("cek: read sidecar: %w", err)
		return res
	}

	if dek, err := unwrapSidecar(owner, master, sidecar); err == nil {
		zero(dek)
		res.Already = true
		return res
	}

	dek, err := unwrapSidecar(Global, master, sidecar)
	if err != nil {
		// Neither identity opens it. That is a real failure and it stays one.
		res.Err = fmt.Errorf("cek: sidecar opens under neither %s nor %s: %w", owner, Global, err)
		return res
	}
	defer zero(dek)

	// Re-wrap the SAME DEK under the owner. A fresh fileID comes with the new
	// sidecar (mintSidecar's contract is id+wrap together), so the wrap is
	// self-consistent; the DEK — the only thing the database's pages care about —
	// is carried across unchanged.
	fileID, wrapped, err := wrapExisting(owner, master, dek)
	if err != nil {
		res.Err = err
		return res
	}
	next := append(append(make([]byte, 0, fileIDLen+len(wrapped)), fileID...), wrapped...)

	// Atomic replace: write beside, fsync, rename. A crash mid-migration leaves the
	// ORIGINAL sidecar in place — the store stays exactly as broken as it was, which
	// is recoverable, rather than half-written, which is not.
	tmp := sidecarPath + ".rewrap"
	if err := os.WriteFile(tmp, next, 0o600); err != nil {
		res.Err = fmt.Errorf("cek: write new sidecar: %w", err)
		return res
	}
	if f, err := os.Open(tmp); err == nil {
		_ = f.Sync()
		_ = f.Close()
	}
	if err := os.Rename(tmp, sidecarPath); err != nil {
		_ = os.Remove(tmp)
		res.Err = fmt.Errorf("cek: replace sidecar: %w", err)
		return res
	}
	res.Rewrapped = true
	return res
}

// RewrapOrgs walks every per-org store under root and migrates each to its owner.
// The subdirectory NAME is the slug OrgDB folded through on the way in, so the owner
// is read from the layout rather than guessed.
func RewrapOrgs(root string) ([]RewrapResult, error) {
	return walkOrgs(root, func(owner Principal, db string) RewrapResult { return Rewrap(owner, db) })
}

// InspectOrgs is RewrapOrgs' read-only twin: it reports what each store would do
// without writing a byte. The dry run and the real run therefore share one walk and
// one decision — a dry run that used different logic would be a different program
// telling you about this one.
func InspectOrgs(root string) ([]RewrapResult, error) {
	return walkOrgs(root, func(owner Principal, db string) RewrapResult {
		res := RewrapResult{Path: db, Owner: owner.String()}
		master := Master()
		sidecar, err := os.ReadFile(db + dekSuffix)
		if err != nil {
			res.Err = fmt.Errorf("cek: read sidecar: %w", err)
			return res
		}
		if dek, err := unwrapSidecar(owner, master, sidecar); err == nil {
			zero(dek)
			res.Already = true
			return res
		}
		if dek, err := unwrapSidecar(Global, master, sidecar); err == nil {
			zero(dek)
			return res // needs migration
		}
		res.Err = errors.New("opens under neither identity")
		return res
	})
}

// walkOrgs is the ONE traversal both the inspection and the migration use.
func walkOrgs(root string, fn func(Principal, string) RewrapResult) ([]RewrapResult, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("cek: read org root: %w", err)
	}
	var out []RewrapResult
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		slug := e.Name()
		dir := filepath.Join(root, slug)
		files, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, f := range files {
			name := f.Name()
			if f.IsDir() || filepath.Ext(name) != dekSuffix {
				continue
			}
			out = append(out, fn(Org(slug), filepath.Join(dir, name[:len(name)-len(dekSuffix)])))
		}
	}
	return out, nil
}
