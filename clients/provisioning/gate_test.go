// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0

package provisioning

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

// TestCreate_UnavailableKindHonest503 locks the safety bar: a kind whose backend
// cannot mint a per-tenant-SAFE credential (datastore, docdb) is REFUSED with an
// honest 503 BEFORE any backend write — never handed a cross-tenant capability nor
// a half-provisioned resource. The provisioner must not run.
func TestCreate_UnavailableKindHonest503(t *testing.T) {
	for _, kind := range []string{"datastore", "docdb"} {
		t.Run(kind, func(t *testing.T) {
			s, mp := newTestSvc(t, kind)
			resp := postCreate(t, s, kind, "acme", "warehouse")
			if resp.StatusCode != http.StatusServiceUnavailable {
				body, _ := io.ReadAll(resp.Body)
				t.Fatalf("%s status = %d body=%s, want 503", kind, resp.StatusCode, body)
			}
			body, _ := io.ReadAll(resp.Body)
			if !strings.Contains(string(body), "not yet available") {
				t.Fatalf("%s body %s missing honest 'not yet available'", kind, body)
			}
			if mp.created != 0 {
				t.Fatalf("%s provisioner ran %d times for a gated kind, want 0 (gate must precede the backend)", kind, mp.created)
			}
		})
	}
}

// TestUnavailableKinds_OnlyDatastoreAndDocdb pins the gate set: the 5 kinds the
// product guarantees (sql/vector/kv/search/s3) MUST NOT be gated, and exactly
// datastore+docdb are. A regression that gates a working kind — or un-gates a
// cross-tenant-unsafe one — fails here.
func TestUnavailableKinds_OnlyDatastoreAndDocdb(t *testing.T) {
	for _, k := range []string{"sql", "vector", "kv", "search", "s3"} {
		if _, gated := unavailableKinds[k]; gated {
			t.Fatalf("kind %q must NOT be gated — it is a guaranteed-working data kind", k)
		}
	}
	for _, k := range []string{"datastore", "docdb"} {
		if _, gated := unavailableKinds[k]; !gated {
			t.Fatalf("kind %q must be gated until its backend can grant a per-tenant-safe credential", k)
		}
	}
	if len(unavailableKinds) != 2 {
		t.Fatalf("unavailableKinds should hold exactly {datastore,docdb}, got %v", unavailableKinds)
	}
}
