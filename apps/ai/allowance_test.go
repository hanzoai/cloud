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

package ai

import (
	"context"
	"os"
	"strings"
	"testing"

	aiobject "github.com/hanzoai/ai/object"
)

// A FREE CALL IS COUNTED WHERE A CALL IS RECORDED, AND A CALL IS RECORDED ONLY WHEN
// ONE ANSWERED.
//
// The plan allowance bounds spend, and spend is incurred when a model is reached. So
// admission and counting are two moments, asked as two ops: the gate READS the ceiling
// before a call, and the count rises on the record of a served one. A request that
// reaches no model — an unresolvable route, a vendor that never replied, a pod being
// rolled — costs the caller nothing.

// The recorder settles both halves of one served call, and it attempts the count for
// exactly the calls that spent no money.
func TestOneRecordSettlesMoneyOrACount(t *testing.T) {
	for _, c := range []struct {
		name  string
		event aiobject.UsageEvent
		count bool
	}{
		{
			name:  "a free call names the subject its count belongs to",
			event: aiobject.UsageEvent{Namespace: "acme", Subject: "acme", USD: "0", Allowance: "acme"},
			count: true,
		},
		{
			name:  "the public lane names the visitor it served",
			event: aiobject.UsageEvent{Namespace: "$public", Subject: "$public", USD: "0", Allowance: "visitor:abc"},
			count: true,
		},
		{
			name:  "a priced call names nobody, because money already bounds it",
			event: aiobject.UsageEvent{Namespace: "acme", Subject: "acme", USD: "0.00132"},
			count: false,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			paid := false
			settle := record(func(context.Context, aiobject.UsageEvent) error {
				paid = true
				return nil
			})

			// No allowance peer is mounted here, so an ATTEMPTED count is the error it
			// reports and a skipped one is silence. That is the whole question: whether
			// the event's subject decides it.
			err := settle(context.Background(), c.event)
			counted := err != nil && strings.Contains(err.Error(), "plane allowance count")

			if counted != c.count {
				t.Fatalf("counted=%v, want %v (err=%v)", counted, c.count, err)
			}
			if !paid {
				t.Fatal("the money path did not run — one record settles what the call spent, whichever it was")
			}
		})
	}
}

// THE POSITION IS STRUCTURAL, AND THIS IS WHAT HOLDS IT THERE. Counting is reachable
// only from the record of a served call: the gate asks the READ, and the one take in
// this file is inside the function the usage recorder calls.
//
// A future edit that moves the count back in front of the answer has to move
// plane.AllowanceTake to do it, and that is what fails here.
func TestOnlyTheRecordOfAServedCallCounts(t *testing.T) {
	src, err := os.ReadFile("ai.go")
	if err != nil {
		t.Fatalf("read ai.go: %v", err)
	}

	// Code only. The prose above these hooks names both ops on purpose, and a test
	// that could not tell an explanation from a call would forbid explaining.
	var code []string
	for _, line := range strings.Split(string(src), "\n") {
		if i := strings.Index(line, "//"); i >= 0 {
			line = line[:i]
		}
		code = append(code, line)
	}
	body := strings.Join(code, "\n")

	if n := strings.Count(body, "plane.AllowanceTake"); n != 1 {
		t.Fatalf("plane.AllowanceTake is called %d times; exactly one call site — the record of a served call — may count", n)
	}
	if !strings.Contains(body, "func countFree(") || !strings.Contains(
		body[strings.Index(body, "func countFree("):], "plane.AllowanceTake") {
		t.Error("the one take must live in countFree, which only the usage recorder reaches")
	}
	if !strings.Contains(body, "aiobject.SetSpent(func(") || !strings.Contains(
		body[strings.Index(body, "aiobject.SetSpent(func("):], "plane.AllowanceRead") {
		t.Error("the gate hook must ask plane.AllowanceRead — admission reads, it never counts")
	}
	if !strings.Contains(body, "aiobject.SetUsageRecorder(record(") {
		t.Error("the usage recorder must be the wrapped one, or a free call is never counted at all")
	}
}
