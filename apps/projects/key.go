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

// key.go — the project's publishable ingest key: minted with the project,
// resolved back to it, and the ONE thing that attributes a site's beacons.
//
// A project IS a site here (apps/sites' Site is a read-time projection of a
// Project row), so one key covers both words.
//
// WHY THE KEY IS ISSUED HERE AND NOT BY IAM. IAM owns publishable keys for ORGS,
// and this is the org-level credential a caller mints at POST /v1/keys. It cannot
// be this one: mint-user-keys is keyed by USER id and (re)generates that user's
// single key of a type, so minting one per project would rotate the org's key on
// every create and hand every project the same credential besides. IAM has no
// project to scope to — projects are this app's resource — so the issuer is the
// app that owns the row. That is not the second pk- family publishable.go
// retired: the retired one restated an ORG key IAM already issued, under a
// different prefix and a different secret. This names a project, which nothing
// else can name, and it wears the ONE publishable spelling.
package projects

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/analytics"
)

// mintKey returns a fresh publishable ingest key.
//
// 32 random bytes: the key is a bearer credential for a write scope, guessing one
// writes into a stranger's project, and it is public by design so an attacker can
// study its shape. cloud.PublishablePrefix is the ONE publishable spelling, which
// is what lets a project key ride every carrier a pk- already rides (Bearer,
// x-hanzo-ingest-key, ?ingest_key= for sendBeacon) without widening anything.
func mintKey() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return cloud.PublishablePrefix + base64.RawURLEncoding.EncodeToString(b), nil
}

// keyResolver answers "which project holds this key" for the analytics ingest
// path, in-process. It is the same seam shape sites.SetResolver uses, for the same
// reason: the fact lives in this store and the reader is another app.
type keyResolver struct{ store *Store }

// Resolve maps a publishable key to the write scope it names. A key no project
// holds is (…, false) — not an error, not a fallback, and never an org on its own,
// because a key without a project is exactly the case that must stop recording.
func (r keyResolver) Resolve(ctx context.Context, key string) (analytics.Attribution, bool, error) {
	p, err := r.store.ResolveKey(ctx, key)
	if errors.Is(err, errNotFound) {
		return analytics.Attribution{}, false, nil
	}
	if err != nil {
		return analytics.Attribution{}, false, err
	}
	// Analytics off is a project that holds a valid key and has asked not to be
	// recorded. Reporting it as unresolvable is the honest answer: the caller is
	// told the write did not land, rather than being told it did while the row is
	// dropped somewhere downstream.
	if !p.Analytics {
		return analytics.Attribution{}, false, nil
	}
	return analytics.Attribution{Org: p.Org, Project: p.Slug}, true, nil
}
