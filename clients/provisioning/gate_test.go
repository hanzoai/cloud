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

// TestUnavailableKinds_EmptyAfterDedicated pins the topology: the FOUR on-demand
// data add-ons — kv, sql, docdb, datastore — are the dedicated set (isolation by
// instance); vector, search and s3 stay shared-logical; and unavailableKinds is
// EMPTY (no kind is honest-gated). A regression that re-gates a working kind,
// moves an add-on off the dedicated path, or promotes a shared kind, fails here.
func TestUnavailableKinds_EmptyAfterDedicated(t *testing.T) {
	if len(unavailableKinds) != 0 {
		t.Fatalf("unavailableKinds must be empty, got %v", unavailableKinds)
	}
	for _, k := range []string{"vector", "search", "s3"} {
		if _, gated := unavailableKinds[k]; gated {
			t.Fatalf("shared kind %q must not be gated", k)
		}
		if _, dedicated := dedicatedEngines[k]; dedicated {
			t.Fatalf("shared kind %q must NOT be a dedicated engine", k)
		}
	}
	for _, k := range []string{"kv", "sql", "docdb", "datastore"} {
		if _, dedicated := dedicatedEngines[k]; !dedicated {
			t.Fatalf("add-on %q must be a dedicated engine (true isolation by instance)", k)
		}
	}
	if len(dedicatedEngines) != 4 {
		t.Fatalf("dedicatedEngines should hold exactly {kv,sql,docdb,datastore}, got %d: %v", len(dedicatedEngines), dedicatedEngines)
	}
}

// TestCreate_DedicatedFailsClosedWithoutCluster locks the safety bar for the
// dedicated path: with no cluster client (orch nil), every add-on create is
// REFUSED with an honest 503 — never a silent/fabricated success — and NOTHING
// is persisted. (The former "not yet available" gate is replaced by a real
// dedicated launch; when the cluster is unreachable we still fail closed.)
func TestCreate_DedicatedFailsClosedWithoutCluster(t *testing.T) {
	for _, kind := range []string{"datastore", "docdb", "sql", "kv"} {
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
