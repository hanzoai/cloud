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

// schema_test.go — the pins that keep ONE schema one.
//
// Every test here protects a property that has no other guard: a rule about naming, a
// rule about tenancy, or a rule about the door and the store agreeing. They are cheap
// because they are all pure — no bus, no store, no HTTP.

package analytics

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"
)

// TestNoVersionSuffixAnywhere is the rule that cannot be argued with at review time.
//
// A `_v2` is a CONFESSION: two tables are the same thing and nobody deleted one. The
// namespace this package writes into inherited nine of them (session_replay_events_v2,
// person_on_events_v2, raw_sessions_v3 …) from a fork, and the way that stops recurring
// is not a convention — it is a build failure. If a name needs distinguishing, the
// namespace is wrong, not the name.
func TestNoVersionSuffixAnywhere(t *testing.T) {
	versioned := regexp.MustCompile(`\b[a-z_]+_v[0-9]+\b`)
	for _, path := range packageSources(t) {
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		for i, line := range strings.Split(string(src), "\n") {
			// The prose of THIS test names the very thing it forbids, and a couple of
			// comments elsewhere name the fork's tables to explain what was retired.
			// A rule that cannot be described in the file that enforces it is a rule
			// nobody can read, so only real identifiers count: comments are exempt.
			if strings.HasPrefix(strings.TrimSpace(line), "//") {
				continue
			}
			if m := versioned.FindString(line); m != "" {
				t.Errorf("%s:%d names %q — a version suffix means two spellings of one thing. "+
					"Qualify by namespace, never by number.", filepath.Base(path), i+1, m)
			}
		}
	}
}

// TestPlaneHasExactlyTwoTables. Two grains, two tables, and the writers are the only
// place that mapping is written down.
//
// The number is the point. Five signals landing in five tables was CONSISTENT and not
// UNIFIED, and the way it grew was one table at a time, each one obviously reasonable.
// A test that counts is what makes the sixth table a conversation instead of a commit.
func TestPlaneHasExactlyTwoTables(t *testing.T) {
	if factTable != "event.fact" || sampleTable != "event.sample" {
		t.Fatalf("the plane's two tables are event.fact and event.sample, got %q and %q",
			factTable, sampleTable)
	}
	for _, w := range writers {
		if w.table.name != factTable && w.table.name != sampleTable {
			t.Errorf("%s writes %q — the plane has two tables, named for the two grains. "+
				"A third is a new name in the namespace, which is what this schema exists "+
				"to stop.", w.signal, w.table.name)
		}
	}
}

// TestEverySignalIsRoutableAndSpelledOnce. The signal vocabulary is CLOSED: five
// occurrences and one measurement. It is closed because the DDL's per-signal TTL
// enumerates it — a signal with no clause never expires — and because the anonymous
// lane's allowlist is keyed on it.
func TestEverySignalIsRoutableAndSpelledOnce(t *testing.T) {
	want := map[signal]bool{
		signalAct: true, signalClip: true, signalError: true,
		signalLog: true, signalSpan: true, signalSample: true,
	}
	if len(want) != 6 {
		t.Fatal("the vocabulary is six")
	}
	seen := map[string]signal{}
	for s := range want {
		if prior, dup := seen[string(s)]; dup {
			t.Fatalf("%q spells both %v and %v", s, prior, s)
		}
		seen[string(s)] = s
		if got := s.subject(); got != plane+"."+string(s) {
			t.Errorf("%s subject = %q, want the plane-rooted name", s, got)
		}
	}
	// `event` must NOT be a signal value. A namespace cannot also be a member of
	// itself: `event.fact WHERE signal='event'` is the stutter the whole rename
	// exists to remove, and the wire word `event` maps onto `act` instead.
	if seen["event"] != "" {
		t.Error("`event` is the NAMESPACE — it cannot also be a signal value")
	}
	if got := routeOf(CaptureEvent{Type: "event", Event: "x"}); got.signal != signalAct {
		t.Errorf("the wire word `event` must route to act, got %v", got.signal)
	}
}

// TestDoorAcceptsExactlyWhatTheSinkCanLand is the property that keeps "accepted"
// honest, restated as a test rather than as a comment.
//
// landableSignals is DERIVED from writers, so the two cannot be edited apart. The
// failure it prevents is a 200 that means "discarded": publish to a subject no writer
// drains and the fact sits on the stream until MaxAge takes it, with the caller told it
// landed.
func TestDoorAcceptsExactlyWhatTheSinkCanLand(t *testing.T) {
	if len(landableSignals) != len(writers) {
		t.Fatalf("landable=%d writers=%d — the door's answer must be derived from the "+
			"sink's, never kept beside it", len(landableSignals), len(writers))
	}
	for _, w := range writers {
		if !landableSignals[w.signal] {
			t.Errorf("%s has a writer but the door refuses it", w.signal)
		}
	}
	// signalSample is the one signal deliberately NOT landable: event.sample acquires
	// its name by RENAME (migration 0003), so a writer shipped ahead of that migration
	// would accept metrics and fail every insert. The refusal is at the door, where a
	// caller can still be told.
	if landableSignals[signalSample] {
		t.Error("the door accepts samples — add the sixth writer only alongside the " +
			"migration that gives event.sample its name")
	}
}

// TestEverySignalWritesTheIdenticalRow. This is what "one table" MEANS at the write
// path: five writers, one column list, one statement, one args builder. Five copies of
// an identical envelope is exactly the shape that drifts.
func TestEverySignalWritesTheIdenticalRow(t *testing.T) {
	var stmt string
	for _, w := range writers {
		got := w.table.statement()
		if stmt == "" {
			stmt = got
			continue
		}
		if got != stmt {
			t.Fatalf("%s renders a different INSERT:\n%s\nvs\n%s", w.signal, got, stmt)
		}
	}
	if !strings.HasPrefix(stmt, "INSERT INTO event.fact (") {
		t.Fatalf("occurrences land in event.fact: %s", stmt)
	}
	// The tenant is the first bound value of every insert, because it is the first
	// column of the sort key.
	if factColumns[0] != "org" {
		t.Fatalf("org must lead the column list, got %q", factColumns[0])
	}
	if factColumns[1] != "signal" {
		t.Fatalf("signal must follow org — it leads the PARTITION key, got %q", factColumns[1])
	}
	if got := factArgs(message{Org: "acme", Signal: "log"}); got[0] != "acme" || got[1] != "log" {
		t.Fatalf("args must open (org, signal), got %v", got[:2])
	}
}

// TestClipCostsTwoColumnsAndNoName is the worked example of the whole design: a product
// that would have been three tables under the old shape is a signal VALUE plus two
// metadata columns, and the blob it indexes never enters the plane at all.
func TestClipCostsTwoColumnsAndNoName(t *testing.T) {
	f, ok := normalize("acme", time.Now().UTC(), CaptureEvent{
		Type:      "clip",
		SessionID: "s-1",
		URL:       "https://acme.test/checkout",
		Clip:      &ClipBody{Object: "s3://replay/acme/s-1/0001.zst", Bytes: 4 << 20, Duration: 9e9},
	})
	if !ok {
		t.Fatal("a clip must normalize")
	}
	if f.signal != signalClip {
		t.Fatalf("signal = %q, want clip", f.signal)
	}
	if f.object != "s3://replay/acme/s-1/0001.zst" || f.bytes != 4<<20 || f.duration != 9e9 {
		t.Fatalf("the clip index did not land: %+v", f)
	}
	// It reuses the envelope rather than restating it — that reuse is the saving.
	if f.session != "s-1" || f.url != "https://acme.test/checkout" || f.org != "acme" {
		t.Fatalf("a clip must carry the same envelope as every other signal: %+v", f)
	}
	// And it lands in the same table as everything else.
	var landed bool
	for _, w := range writers {
		if w.signal == signalClip {
			landed = w.table.name == factTable
		}
	}
	if !landed {
		t.Error("a clip must land in event.fact — a new product that mints a table is the " +
			"growth this schema exists to stop")
	}
}

// TestSeverityHasOneSpelling. `level`, `severity_text` and `severity_number` were three
// columns for one fact, and a row could carry a number and a word that disagreed. The
// number is stored; the word is a function of it.
func TestSeverityHasOneSpelling(t *testing.T) {
	for _, tc := range []struct {
		text string
		num  uint8
		want uint8
		word string
	}{
		{"", 0, 0, ""},
		{"trace", 0, severityTrace, "trace"},
		{"DEBUG", 0, severityDebug, "debug"},
		{"Info", 0, severityInfo, "info"},
		{"warning", 0, severityWarn, "warn"},
		{"warn", 0, severityWarn, "warn"},
		{"error", 0, severityError, "error"},
		{"critical", 0, severityFatal, "fatal"},
		// An explicit number is the precise form and wins over the word.
		{"info", 18, 18, "error"},
		// A word nothing recognizes falls back rather than becoming 0.
		{"spicy", 0, 0, ""},
	} {
		if got := severityOf(tc.text, tc.num, 0); got != tc.want {
			t.Errorf("severityOf(%q,%d) = %d, want %d", tc.text, tc.num, got, tc.want)
		}
		if got := severityText(tc.want); got != tc.word {
			t.Errorf("severityText(%d) = %q, want %q", tc.want, got, tc.word)
		}
	}
	// An error with no stated level IS an error. Stored as 0 it would sort below every
	// warning in the one list that exists to surface it.
	f, ok := normalize("acme", time.Now().UTC(), CaptureEvent{
		Type:  "error",
		Error: &Exception{Type: "TypeError", Message: "boom"},
	})
	if !ok || f.severity != severityError {
		t.Fatalf("an unqualified error must store severity %d, got %d", severityError, f.severity)
	}
}

// TestGroupsIsAMapAndNotFiveSlots. group0..group4 is a sixth slot waiting to become a
// version suffix; a Map makes the second grouping a data change.
func TestGroupsIsAMapAndNotFiveSlots(t *testing.T) {
	for _, c := range factColumns {
		if regexp.MustCompile(`group[0-9]`).MatchString(c) {
			t.Fatalf("positional group slot %q — group membership is a Map", c)
		}
	}
	f, _ := normalize("acme", time.Now().UTC(), CaptureEvent{Event: "x", GroupID: "org_7"})
	if got := f.groups["organization"]; got != "org_7" {
		t.Fatalf("groups = %v, want the default grouping keyed `organization`", f.groups)
	}
	f, _ = normalize("acme", time.Now().UTC(), CaptureEvent{Event: "x", GroupID: "w1", GroupType: "workspace"})
	if got := f.groups["workspace"]; got != "w1" {
		t.Fatalf("groups = %v, want the caller's own grouping key", f.groups)
	}
	// No grouping is an absent map, not one holding a blank.
	f, _ = normalize("acme", time.Now().UTC(), CaptureEvent{Event: "x"})
	if len(f.groups) != 0 {
		t.Fatalf("groups = %v, want empty", f.groups)
	}
}

// TestScopeBindsTenantThenSignal is the tenancy pin for the READ side, and it is the
// one this whole schema was measured against: the fork's translation table mapped three
// tenants into one bucket, and the fix is that there is no translation — org is the
// value the row carries and the value a read binds, leading.
func TestScopeBindsTenantThenSignal(t *testing.T) {
	const hostile = "acme'; DROP TABLE event.fact; --"
	where, args := scope(hostile, signalError)
	if strings.Contains(where, hostile) {
		t.Fatalf("the tenant reached the SQL text: %q", where)
	}
	if !strings.HasPrefix(where, "org = ?") {
		t.Fatalf("org must LEAD the predicate (it leads the sort key): %q", where)
	}
	if !strings.Contains(where, "signal = ?") {
		t.Fatalf("the signal must be bound, not baked: %q", where)
	}
	if len(args) != 2 || args[0] != hostile || args[1] != string(signalError) {
		t.Fatalf("args = %v, want [org signal]", args)
	}
	// An empty tenant still binds — it matches no org, so the read is empty rather
	// than unscoped. Fail closed.
	where, args = scope("", signalAct)
	if !strings.HasPrefix(where, "org = ?") || len(args) != 2 || args[0] != "" {
		t.Fatalf("an empty tenant must still bind a predicate that matches nothing: %q %v", where, args)
	}
}

// TestTenantIsNeverOnTheWire. org is stamped from the SERVER-resolved principal, so no
// field a caller can set may reach it. The wire type is checked by NAME, because the
// failure is a future field called `org` that quietly starts winning.
func TestTenantIsNeverOnTheWire(t *testing.T) {
	rt := reflect.TypeFor[CaptureEvent]()
	for field := range rt.Fields() {
		name := strings.ToLower(field.Name)
		tag := strings.ToLower(field.Tag.Get("json"))
		for _, banned := range []string{"org", "tenant", "team", "orgid", "tenantid", "teamid"} {
			if name == banned || strings.HasPrefix(tag, banned+",") || tag == banned {
				t.Errorf("CaptureEvent.%s is a caller-settable tenant — the tenant is the "+
					"server's answer, never the caller's claim", field.Name)
			}
		}
	}
	// And the stamp is observable: two orgs normalizing the identical wire event
	// produce two facts that differ in exactly the tenant.
	e := CaptureEvent{Event: "signup", MessageID: "fixed"}
	now := time.Now().UTC()
	a, _ := normalize("acme", now, e)
	b, _ := normalize("zeta", now, e)
	if a.org != "acme" || b.org != "zeta" {
		t.Fatalf("the stamp did not take: %q %q", a.org, b.org)
	}
	a.org, b.org = "", ""
	if !reflect.DeepEqual(a.attributes, b.attributes) || a.id != b.id || a.name != b.name {
		t.Fatal("two tenants' facts differ in more than the tenant")
	}
}

// TestTenantIsNotAKey. The warehouse stores SLUGS. A UUID is a relational identity; a
// warehouse dimension is a stable legible value, and the moment one column holds both
// vocabularies, half the rows are unreadable by their owner — measured on the live
// event.error, where 88 of 638 rows carried a UUID in `org` and 13 carried one in
// `product`.
func TestTenantIsNotAKey(t *testing.T) {
	uuidish := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-`)
	f, ok := normalize("019f9b1e-5785-7359-ad0b-f75db8e58c99", time.Now().UTC(),
		CaptureEvent{Event: "x", Product: "019f9b1e-5785-7359-ad0b-f75db8e58c99"})
	if !ok {
		t.Fatal("normalize refused")
	}
	// This package cannot stop a CALLER from sending a UUID as `product` — that is the
	// caller's own field. What it can pin is that nothing HERE mints one, so the only
	// UUIDs in the warehouse come from a writer that must be fixed at its source.
	if uuidish.MatchString(f.org) && f.org != "019f9b1e-5785-7359-ad0b-f75db8e58c99" {
		t.Fatal("normalize rewrote the tenant")
	}
	for _, c := range factColumns {
		if strings.Contains(c, "_uuid") || c == "tenant_id" || c == "team_id" || c == "project_id" {
			t.Errorf("column %q — the plane has ONE tenant column and it is `org`. A second "+
				"way to say tenant is exactly how one customer's rows ended up in another's "+
				"project.", c)
		}
	}
}

// packageSources is every non-test .go file of this package — the same scan
// TestCloudDoesNotPartitionByTenant uses to prove cloud owns no DDL.
func packageSources(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}
	var out []string
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || !strings.HasSuffix(n, ".go") || strings.HasSuffix(n, "_test.go") {
			continue
		}
		out = append(out, n)
	}
	if len(out) == 0 {
		t.Fatal("no sources found")
	}
	return out
}
