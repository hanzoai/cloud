// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0

package provisioning

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

// TestUnavailableKinds_EmptyAfterDedicated pins the new topology: datastore +
// docdb — the only kinds a shared backend could never scope a per-tenant role on
// — are no longer honest-gated; they moved to the DEDICATED-instance strategy
// (isolation by instance). So unavailableKinds is EMPTY, the 5 shared kinds stay
// shared-logical, and datastore/docdb are exactly the dedicated set. A
// regression that re-gates a working kind, or drops a dedicated one, fails here.
func TestUnavailableKinds_EmptyAfterDedicated(t *testing.T) {
	if len(unavailableKinds) != 0 {
		t.Fatalf("unavailableKinds must be empty now datastore/docdb are dedicated, got %v", unavailableKinds)
	}
	for _, k := range []string{"sql", "vector", "kv", "search", "s3"} {
		if _, gated := unavailableKinds[k]; gated {
			t.Fatalf("shared kind %q must not be gated", k)
		}
		if _, dedicated := dedicatedEngines[k]; dedicated {
			t.Fatalf("shared kind %q must NOT be a dedicated engine", k)
		}
	}
	for _, k := range []string{"datastore", "docdb"} {
		if _, dedicated := dedicatedEngines[k]; !dedicated {
			t.Fatalf("kind %q must be a dedicated engine (true isolation by instance)", k)
		}
	}
	if len(dedicatedEngines) != 2 {
		t.Fatalf("dedicatedEngines should hold exactly {datastore,docdb}, got %v", dedicatedEngines)
	}
}

// TestCreate_DedicatedFailsClosedWithoutCluster locks the safety bar for the
// dedicated path: with no cluster client (orch nil), a datastore/docdb create is
// REFUSED with an honest 503 — never a silent/fabricated success — and NOTHING
// is persisted. (The former "not yet available" gate is replaced by a real
// dedicated launch; when the cluster is unreachable we still fail closed.)
func TestCreate_DedicatedFailsClosedWithoutCluster(t *testing.T) {
	for _, kind := range []string{"datastore", "docdb"} {
		t.Run(kind, func(t *testing.T) {
			s, _ := newTestSvc(t) // no orch, no bill
			resp := postCreate(t, s, kind, "acme", "warehouse")
			if resp.StatusCode != http.StatusServiceUnavailable {
				body, _ := io.ReadAll(resp.Body)
				t.Fatalf("%s status = %d body=%s, want 503", kind, resp.StatusCode, body)
			}
			body, _ := io.ReadAll(resp.Body)
			if !strings.Contains(string(body), "no cluster client") {
				t.Fatalf("%s body %s missing honest cluster-unavailable reason", kind, body)
			}
			rows, err := s.store.List(context.Background(), "acme", kind)
			if err != nil {
				t.Fatalf("list: %v", err)
			}
			if len(rows) != 0 {
				t.Fatalf("%s persisted %d rows on a failed provision, want 0", kind, len(rows))
			}
		})
	}
}
