package channels

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// policy_test.go is the pure-store proof of the gate engine and pairing state
// machine: no HTTP, no network, explicit clocks. The policy key is
// (org, channel) — no account dimension anywhere (C2-2).

// t0 is the fixed test epoch every explicit clock counts from.
const t0 int64 = 1_700_000_000

func newStore(t *testing.T) *store {
	t.Helper()
	st, err := openStore(filepath.Join(t.TempDir(), "channels.db"))
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func mustPair(t *testing.T, st *store, org, channel, sender string, now int64) string {
	t.Helper()
	code, created, err := upsertPairing(context.Background(), st, org, channel, sender, now)
	if err != nil || !created {
		t.Fatalf("upsertPairing(%s): created=%v err=%v", sender, created, err)
	}
	return code
}

func TestPairingCodeShape(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()

	code := mustPair(t, st, "acme", "telegram", "42", t0)
	if len(code) != pairCodeLen {
		t.Fatalf("code %q length %d, want %d", code, len(code), pairCodeLen)
	}
	for _, r := range code {
		if !strings.ContainsRune(pairAlphabet, r) {
			t.Fatalf("code %q contains %q outside the pairing alphabet", code, r)
		}
	}
	if code != strings.ToUpper(code) {
		t.Fatalf("code %q must be uppercase", code)
	}
	var stored string
	if err := st.db.QueryRowContext(ctx, `SELECT code FROM channel_pairing
  WHERE org='acme' AND channel='telegram' AND sender='42'`).Scan(&stored); err != nil {
		t.Fatalf("stored code: %v", err)
	}
	if stored != code {
		t.Fatalf("stored %q != returned %q", stored, code)
	}
}

func TestPairingTTLStrictAfter(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()

	code42 := mustPair(t, st, "acme", "telegram", "42", t0)
	code43 := mustPair(t, st, "acme", "telegram", "43", t0)

	// Exactly TTL later the request is STILL valid — expiry is strictly after
	// one hour (the pairing-store.ts `>` boundary).
	rows, err := listPairing(ctx, st, "acme", t0+3600)
	if err != nil || len(rows) != 2 {
		t.Fatalf("listPairing at +3600 = %d rows, err %v; want both", len(rows), err)
	}
	sender, _, ok, err := approvePairing(ctx, st, "acme", "telegram", code42, t0+3600)
	if err != nil || !ok || sender != "42" {
		t.Fatalf("approve at exactly TTL: ok=%v sender=%q err=%v", ok, sender, err)
	}

	// One second past TTL the request is gone.
	if rows, _ := listPairing(ctx, st, "acme", t0+3601); len(rows) != 0 {
		t.Fatalf("listPairing at +3601 = %d rows, want 0", len(rows))
	}
	if _, _, ok, err := approvePairing(ctx, st, "acme", "telegram", code43, t0+3601); err != nil || ok {
		t.Fatalf("approve past TTL: ok=%v err=%v, want a miss", ok, err)
	}
}

func TestPairingRefreshNotRemint(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()

	code := mustPair(t, st, "acme", "telegram", "42", t0)
	code2, created, err := upsertPairing(ctx, st, "acme", "telegram", "42", t0+10)
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if created || code2 != code {
		t.Fatalf("refresh minted (created=%v code=%q), want the same pending code %q", created, code2, code)
	}
	var lastSeen int64
	if err := st.db.QueryRowContext(ctx, `SELECT last_seen FROM channel_pairing
  WHERE org='acme' AND channel='telegram' AND sender='42'`).Scan(&lastSeen); err != nil {
		t.Fatalf("last_seen: %v", err)
	}
	if lastSeen != t0+10 {
		t.Fatalf("last_seen = %d, want bumped to %d", lastSeen, t0+10)
	}
}

func TestPairingMaxPending(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()

	codes := make([]string, 0, pairMaxPending)
	for _, sender := range []string{"a", "b", "c"} {
		codes = append(codes, mustPair(t, st, "acme", "telegram", sender, t0))
	}
	// The 4th request finds the cap: no code, no error (the exact OpenClaw
	// port — no eviction; TTL or approval clears slots).
	code, created, err := upsertPairing(ctx, st, "acme", "telegram", "d", t0)
	if err != nil || created || code != "" {
		t.Fatalf("at cap: code=%q created=%v err=%v, want empty no-op", code, created, err)
	}
	// The first three stay approvable.
	for i, c := range codes {
		if _, _, ok, err := approvePairing(ctx, st, "acme", "telegram", c, t0+1); err != nil || !ok {
			t.Fatalf("approve %d: ok=%v err=%v", i, ok, err)
		}
	}
}

func TestPairingCodesDistinct(t *testing.T) {
	st := newStore(t)

	seen := map[string]bool{}
	for _, sender := range []string{"a", "b", "c"} {
		code := strings.ToUpper(mustPair(t, st, "acme", "telegram", sender, t0))
		if seen[code] {
			t.Fatalf("code %q minted twice for one (org, channel)", code)
		}
		seen[code] = true
	}
}

func TestApproveGrantsDMOnly(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()

	code := mustPair(t, st, "acme", "telegram", "42", t0)
	sender, boot, ok, err := approvePairing(ctx, st, "acme", "telegram", code, t0+1)
	if err != nil || !ok || sender != "42" || !boot {
		t.Fatalf("approve: sender=%q boot=%v ok=%v err=%v", sender, boot, ok, err)
	}
	// The grant is a pairing-source DM allow entry.
	_, paired, err := allowEntries(ctx, st, "acme", "telegram", "dm")
	if err != nil || len(paired) != 1 || paired[0] != "42" {
		t.Fatalf("paired entries = %v err=%v, want [42]", paired, err)
	}
	v, err := dmGate(ctx, st, "acme", "telegram", "42", true)
	if err != nil || !v.Allow || v.Reason != dmPaired {
		t.Fatalf("dmGate = %+v err=%v, want allow dmPaired", v, err)
	}
	// DM approval NEVER admits the sender to a group allowlist.
	if err := setPolicy(ctx, st, "acme", "telegram", policyRow{DM: DMPairing, Group: GroupAllowlist}, t0+2); err != nil {
		t.Fatalf("setPolicy: %v", err)
	}
	gv, err := groupGate(ctx, st, "acme", "telegram", "42")
	if err != nil || gv.Allow || gv.Reason != groupEmptyAllowlist {
		t.Fatalf("groupGate = %+v err=%v, want groupEmptyAllowlist block", gv, err)
	}
}

func TestPairedRowsOnlyUnderPairingPolicy(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()

	code := mustPair(t, st, "acme", "telegram", "42", t0)
	if _, _, ok, err := approvePairing(ctx, st, "acme", "telegram", code, t0+1); err != nil || !ok {
		t.Fatalf("approve: ok=%v err=%v", ok, err)
	}
	// The pairing-source row is suspended, not deleted, under other policies.
	for _, dm := range []DMPolicy{DMAllowlist, DMOpen} {
		if err := setPolicy(ctx, st, "acme", "telegram", policyRow{DM: dm, Group: GroupOpen}, t0+2); err != nil {
			t.Fatalf("setPolicy(%s): %v", dm, err)
		}
		v, err := dmGate(ctx, st, "acme", "telegram", "42", true)
		if err != nil || v.Allow || v.Pair || v.Reason != dmNotAllowlisted {
			t.Fatalf("dmGate under %s = %+v err=%v, want plain block", dm, v, err)
		}
	}
	// Switching back re-validates the grant.
	if err := setPolicy(ctx, st, "acme", "telegram", policyRow{DM: DMPairing, Group: GroupOpen}, t0+3); err != nil {
		t.Fatalf("setPolicy: %v", err)
	}
	if v, err := dmGate(ctx, st, "acme", "telegram", "42", true); err != nil || !v.Allow || v.Reason != dmPaired {
		t.Fatalf("dmGate back under pairing = %+v err=%v", v, err)
	}
}

func TestDMOpenSemantics(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()

	if err := setPolicy(ctx, st, "acme", "telegram", policyRow{DM: DMOpen, Group: GroupOpen}, t0); err != nil {
		t.Fatalf("setPolicy: %v", err)
	}
	// Open is not unconditional: no entries ⇒ block, and never a pairing mint.
	v, err := dmGate(ctx, st, "acme", "telegram", "55", true)
	if err != nil || v.Allow || v.Pair || v.Reason != dmNotAllowlisted {
		t.Fatalf("open+empty = %+v err=%v, want block", v, err)
	}
	if err := putAllow(ctx, st, "acme", "telegram", "dm", []string{"*"}, t0+1); err != nil {
		t.Fatalf("putAllow: %v", err)
	}
	if v, err := dmGate(ctx, st, "acme", "telegram", "55", true); err != nil || !v.Allow || v.Reason != dmOpenWildcard {
		t.Fatalf("open+wildcard = %+v err=%v", v, err)
	}
	// Explicit config match, and only that match.
	if err := putAllow(ctx, st, "acme", "telegram", "dm", []string{"55"}, t0+2); err != nil {
		t.Fatalf("putAllow: %v", err)
	}
	if v, err := dmGate(ctx, st, "acme", "telegram", "55", true); err != nil || !v.Allow || v.Reason != dmAllowlisted {
		t.Fatalf("open+explicit = %+v err=%v", v, err)
	}
	if v, err := dmGate(ctx, st, "acme", "telegram", "56", true); err != nil || v.Allow {
		t.Fatalf("open must not admit an unlisted sender: %+v err=%v", v, err)
	}
}

func TestOwnerBootstrapOnce(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()

	codeA := mustPair(t, st, "acme", "telegram", "a1", t0)
	if _, boot, ok, err := approvePairing(ctx, st, "acme", "telegram", codeA, t0+1); err != nil || !ok || !boot {
		t.Fatalf("first approve: boot=%v ok=%v err=%v", boot, ok, err)
	}
	var entry string
	if err := st.db.QueryRowContext(ctx, `SELECT entry FROM channel_owner WHERE org='acme'`).Scan(&entry); err != nil {
		t.Fatalf("owner: %v", err)
	}
	if entry != "telegram:a1" {
		t.Fatalf("owner entry = %q, want telegram:a1", entry)
	}

	codeB := mustPair(t, st, "acme", "telegram", "b2", t0+2)
	if _, boot, ok, err := approvePairing(ctx, st, "acme", "telegram", codeB, t0+3); err != nil || !ok || boot {
		t.Fatalf("second approve: boot=%v ok=%v err=%v, want no re-bootstrap", boot, ok, err)
	}
	if err := st.db.QueryRowContext(ctx, `SELECT entry FROM channel_owner WHERE org='acme'`).Scan(&entry); err != nil || entry != "telegram:a1" {
		t.Fatalf("owner after second approve = %q err=%v, want unchanged", entry, err)
	}
}

func TestApproveInputHygiene(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()

	// Case-insensitive input: codes are stored uppercase, approvals fold.
	code := mustPair(t, st, "acme", "telegram", "42", t0)
	if sender, _, ok, err := approvePairing(ctx, st, "acme", "telegram", strings.ToLower(code), t0+1); err != nil || !ok || sender != "42" {
		t.Fatalf("lowercase approve: sender=%q ok=%v err=%v", sender, ok, err)
	}
	// Blank input is a miss, not an error.
	if _, _, ok, err := approvePairing(ctx, st, "acme", "telegram", "   ", t0+1); err != nil || ok {
		t.Fatalf("blank code: ok=%v err=%v", ok, err)
	}
	// `*` and "" are policy syntax, never identities — a poisoned pending row
	// must not mint a grant.
	for _, bad := range []struct{ sender, code string }{{"*", "WWWWWWWW"}, {"", "EEEEEEEE"}} {
		if _, err := st.db.ExecContext(ctx, `INSERT INTO channel_pairing (org, channel, sender, code, created_at, last_seen)
  VALUES ('acme','telegram',?,?,?,?)`, bad.sender, bad.code, t0, t0); err != nil {
			t.Fatalf("seed %q: %v", bad.sender, err)
		}
		if _, _, ok, err := approvePairing(ctx, st, "acme", "telegram", bad.code, t0+1); err != nil || ok {
			t.Fatalf("approve of sender %q: ok=%v err=%v, want refusal", bad.sender, ok, err)
		}
	}
	_, paired, err := allowEntries(ctx, st, "acme", "telegram", "dm")
	if err != nil {
		t.Fatalf("allowEntries: %v", err)
	}
	for _, p := range paired {
		if p == "*" || p == "" {
			t.Fatalf("policy syntax %q minted as a grant", p)
		}
	}
}

func TestAccessGroups(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()

	err := putAccessGroups(ctx, st, "acme", map[string]map[string][]string{
		"eng": {"telegram": {"7"}},
		"ops": {"*": {"9"}}, // '*' = shared across channels
	})
	if err != nil {
		t.Fatalf("putAccessGroups: %v", err)
	}
	if err := setPolicy(ctx, st, "acme", "telegram", policyRow{DM: DMAllowlist, Group: GroupOpen}, t0); err != nil {
		t.Fatalf("setPolicy: %v", err)
	}
	if err := putAllow(ctx, st, "acme", "telegram", "dm", []string{"accessGroup:eng"}, t0); err != nil {
		t.Fatalf("putAllow: %v", err)
	}
	if v, err := dmGate(ctx, st, "acme", "telegram", "7", true); err != nil || !v.Allow || v.Reason != dmAllowlisted {
		t.Fatalf("group member = %+v err=%v", v, err)
	}
	if v, err := dmGate(ctx, st, "acme", "telegram", "8", true); err != nil || v.Allow {
		t.Fatalf("non-member = %+v err=%v, want block", v, err)
	}
	// A '*'-channel group row matches from any channel.
	if err := setPolicy(ctx, st, "acme", "slack", policyRow{DM: DMAllowlist, Group: GroupOpen}, t0); err != nil {
		t.Fatalf("setPolicy: %v", err)
	}
	if err := putAllow(ctx, st, "acme", "slack", "dm", []string{"accessGroup:ops"}, t0); err != nil {
		t.Fatalf("putAllow: %v", err)
	}
	if v, err := dmGate(ctx, st, "acme", "slack", "9", true); err != nil || !v.Allow {
		t.Fatalf("shared-group member = %+v err=%v", v, err)
	}
	// An unknown group name grants nobody.
	if err := putAllow(ctx, st, "acme", "slack", "dm", []string{"accessGroup:ghost"}, t0); err != nil {
		t.Fatalf("putAllow: %v", err)
	}
	if v, err := dmGate(ctx, st, "acme", "slack", "9", true); err != nil || v.Allow {
		t.Fatalf("ghost group = %+v err=%v, want block", v, err)
	}
}

func TestGroupGate(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()

	// Absent policy row ⇒ the group default is open.
	if v, err := groupGate(ctx, st, "acme", "slack", "u1"); err != nil || !v.Allow || v.Reason != groupOpen {
		t.Fatalf("default = %+v err=%v", v, err)
	}
	if err := setPolicy(ctx, st, "acme", "slack", policyRow{DM: DMPairing, Group: GroupDisabled}, t0); err != nil {
		t.Fatalf("setPolicy: %v", err)
	}
	if v, err := groupGate(ctx, st, "acme", "slack", "u1"); err != nil || v.Allow || v.Reason != groupDisabled {
		t.Fatalf("disabled = %+v err=%v", v, err)
	}
	if err := setPolicy(ctx, st, "acme", "slack", policyRow{DM: DMPairing, Group: GroupAllowlist}, t0); err != nil {
		t.Fatalf("setPolicy: %v", err)
	}
	if v, err := groupGate(ctx, st, "acme", "slack", "u1"); err != nil || v.Allow || v.Reason != groupEmptyAllowlist {
		t.Fatalf("empty allowlist = %+v err=%v", v, err)
	}
	if err := putAllow(ctx, st, "acme", "slack", "group", []string{"u1"}, t0); err != nil {
		t.Fatalf("putAllow: %v", err)
	}
	if v, err := groupGate(ctx, st, "acme", "slack", "u1"); err != nil || !v.Allow || v.Reason != groupAllowlisted {
		t.Fatalf("allowlisted = %+v err=%v", v, err)
	}
	if v, err := groupGate(ctx, st, "acme", "slack", "u2"); err != nil || v.Allow || v.Reason != groupNotAllowlisted {
		t.Fatalf("unlisted = %+v err=%v", v, err)
	}
	if err := putAllow(ctx, st, "acme", "slack", "group", []string{"*"}, t0); err != nil {
		t.Fatalf("putAllow: %v", err)
	}
	if v, err := groupGate(ctx, st, "acme", "slack", "u2"); err != nil || !v.Allow {
		t.Fatalf("wildcard = %+v err=%v", v, err)
	}
}

func TestOrgIsolationStore(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()

	// Org A opens telegram wide; org B still gets the strict default.
	if err := setPolicy(ctx, st, "org-a", "telegram", policyRow{DM: DMOpen, Group: GroupOpen}, t0); err != nil {
		t.Fatalf("setPolicy: %v", err)
	}
	if err := putAllow(ctx, st, "org-a", "telegram", "dm", []string{"*"}, t0); err != nil {
		t.Fatalf("putAllow: %v", err)
	}
	if v, err := dmGate(ctx, st, "org-a", "telegram", "42", true); err != nil || !v.Allow {
		t.Fatalf("org-a gate = %+v err=%v", v, err)
	}
	if v, err := dmGate(ctx, st, "org-b", "telegram", "42", true); err != nil || v.Allow || !v.Pair {
		t.Fatalf("org-b gate = %+v err=%v, want the pairing default", v, err)
	}
	// Pairing rows are org-scoped: invisible and unapprovable across orgs.
	code := mustPair(t, st, "org-a", "slack", "s1", t0)
	if rows, err := listPairing(ctx, st, "org-b", t0+1); err != nil || len(rows) != 0 {
		t.Fatalf("org-b pending = %d rows err=%v, want none", len(rows), err)
	}
	if _, _, ok, err := approvePairing(ctx, st, "org-b", "slack", code, t0+1); err != nil || ok {
		t.Fatalf("cross-org approve: ok=%v err=%v, want a miss", ok, err)
	}
	if rows, err := listPairing(ctx, st, "org-a", t0+1); err != nil || len(rows) != 1 {
		t.Fatalf("org-a pending = %d rows err=%v, want the request intact", len(rows), err)
	}
}

func TestStoreRetention(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()

	longText := strings.Repeat("x", inboxTextMax+100)
	old := inboxRow{Org: "acme", Channel: "telegram", RoomID: "1", RoomKind: RoomDM, Sender: "s", Text: "old", EventKey: "e-old", CreatedAt: t0 - inboxKeepSec - 1}
	young := inboxRow{Org: "acme", Channel: "telegram", RoomID: "1", RoomKind: RoomDM, Sender: "s", Text: longText, EventKey: "e-young", CreatedAt: t0 - 10}
	for _, r := range []inboxRow{old, young} {
		if err := st.insertInbox(ctx, r); err != nil {
			t.Fatalf("insertInbox(%s): %v", r.EventKey, err)
		}
	}
	if _, _, err := st.markSend(ctx, "acme", "telegram", "k-old", t0-sendKeepSec-1); err != nil {
		t.Fatalf("markSend old: %v", err)
	}
	if _, _, err := st.markSend(ctx, "acme", "telegram", "k-young", t0-10); err != nil {
		t.Fatalf("markSend young: %v", err)
	}

	if err := st.gc(ctx, t0); err != nil {
		t.Fatalf("gc: %v", err)
	}

	rows, err := st.listInbox(ctx, "acme", 0, 0)
	if err != nil || len(rows) != 1 || rows[0].EventKey != "e-young" {
		t.Fatalf("inbox after gc = %+v err=%v, want only the young row", rows, err)
	}
	// Text is truncated at insert — flood damage is bounded (C1-F3).
	if len(rows[0].Text) != inboxTextMax {
		t.Fatalf("stored text length = %d, want the %d cap", len(rows[0].Text), inboxTextMax)
	}
	var n int
	var key string
	if err := st.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM channel_send WHERE org='acme'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("send rows after gc = %d err=%v, want 1", n, err)
	}
	if err := st.db.QueryRowContext(ctx, `SELECT idempotency FROM channel_send WHERE org='acme'`).Scan(&key); err != nil || key != "k-young" {
		t.Fatalf("surviving send key = %q err=%v, want k-young (48 h replay window)", key, err)
	}
}
