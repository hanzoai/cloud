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

package cloud

import (
	"testing"

	"github.com/hanzoai/cloud/audit"
)

// TestAuditChainIsPerProcess pins the one-writer rule for the audit chain.
//
// audit_log.seq is a gapless chain position and every row's prev_hash seals the
// one before it, so the chain only means anything if a single process appends to
// it. When cloud was one binary that was automatic. The moment subsystems became
// plugin CHILD PROCESSES sharing a DataDir, every one of them opened the same
// audit.db, recovered its own in-memory nextSeq, and raced for the same PRIMARY
// KEY — v1.801.313 produced "UNIQUE constraint failed: audit_log.seq" ~94 times a
// minute and, because the audit gate fails closed, refused every POST in the
// fleet.
//
// Two writers cannot share a hash chain; they can only fork it. Each process
// therefore gets its own file. If someone collapses these back onto one name,
// this test fails before the fleet does.
func TestAuditChainIsPerProcess(t *testing.T) {
	for _, tc := range []struct {
		proc string
		want string
	}{
		{"cloud", "audit"}, // host keeps the canonical name (and its history)
		{"", "audit"},      // unknown proc must not invent a second host chain
		{"tasks", "audit-tasks"},
		{"integrations", "audit-integrations"},
		{"visor", "audit-visor"},
	} {
		t.Run(tc.proc, func(t *testing.T) {
			if got := audit.Name(tc.proc); got != tc.want {
				t.Fatalf("audit.Name(%q) = %q, want %q", tc.proc, got, tc.want)
			}
		})
	}

	// The property that actually matters: distinct processes never collide.
	seen := map[string]string{}
	for _, p := range []string{"cloud", "tasks", "integrations", "visor", "commerce", "iam"} {
		n := audit.Name(p)
		if prev, dup := seen[n]; dup {
			t.Fatalf("processes %q and %q share audit chain %q — two writers on one hash chain", prev, p, n)
		}
		seen[n] = p
	}
}
