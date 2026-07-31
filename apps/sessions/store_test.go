package sessions

import (
	"bytes"
	"path/filepath"
	"testing"
	"time"

	"github.com/hanzoai/cloud/cek"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	// the data plane refuses to open unencrypted, so a test supplies its own key
	cek.SetMasterKey(bytes.Repeat([]byte{0x2a}, 32))
	s, err := openStore(filepath.Join(t.TempDir(), "sessions.db"))
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func beat(t *testing.T, s *Store, subject, id string, at time.Time) {
	t.Helper()
	if err := s.Beat(Session{
		Subject: subject, ID: id, Host: "dbc", Workspace: "/w",
		URL: "https://x.share.hanzo.ai", StartedAt: at.Unix(), BeatAt: at.Unix(),
	}); err != nil {
		t.Fatalf("Beat(%s/%s): %v", subject, id, err)
	}
}

// A session that beats twice is still one session. Without the upsert, every
// heartbeat would add a row and the console would show one terminal N times.
func TestBeatIsUpsertNotInsert(t *testing.T) {
	s := testStore(t)
	now := time.Now()
	beat(t, s, "hanzo", "a", now)
	beat(t, s, "hanzo", "a", now.Add(time.Second))

	got, err := s.List("hanzo", now.Add(time.Second), SessionTTL)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d sessions after two beats, want 1", len(got))
	}
}

// Liveness is a read-time predicate: a host that stops beating drops off the
// roster on its own, with nothing having to observe that it died.
func TestListHidesSessionsPastTTL(t *testing.T) {
	s := testStore(t)
	start := time.Now()
	beat(t, s, "hanzo", "stale", start)
	beat(t, s, "hanzo", "fresh", start.Add(SessionTTL))

	// read one second past the fresh beat, so the cutoff lands after the stale
	// one. A session exactly on the cutoff counts as live — the boundary is
	// inclusive, and a beat that arrives right on the TTL is not a dead host.
	got, err := s.List("hanzo", start.Add(SessionTTL+time.Second), SessionTTL)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 || got[0].ID != "fresh" {
		t.Fatalf("got %+v, want only the session that beat within the TTL", ids(got))
	}
}

// A session URL is a live shell on someone's machine. One org must never see
// another's, and the predicate belongs in the query, not the caller.
func TestListIsScopedToSubject(t *testing.T) {
	s := testStore(t)
	now := time.Now()
	beat(t, s, "hanzo", "mine", now)
	beat(t, s, "zoo", "theirs", now)

	got, err := s.List("hanzo", now, SessionTTL)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 || got[0].ID != "mine" {
		t.Fatalf("got %v, want only the caller's own session", ids(got))
	}
}

// Delete is likewise scoped: knowing another org's session id must not be enough
// to remove it.
func TestDeleteCannotReachAnotherSubject(t *testing.T) {
	s := testStore(t)
	now := time.Now()
	beat(t, s, "zoo", "theirs", now)

	if err := s.Delete("hanzo", "theirs"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	got, err := s.List("zoo", now, SessionTTL)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("another subject's delete removed the row")
	}
}

// Deleting a session that is not there is not an error: a host that exits twice
// should not have to care.
func TestDeleteMissingIsNotAnError(t *testing.T) {
	if err := testStore(t).Delete("hanzo", "nope"); err != nil {
		t.Fatalf("Delete(missing): %v", err)
	}
}

func TestPruneDropsLongDeadRows(t *testing.T) {
	s := testStore(t)
	start := time.Now()
	beat(t, s, "hanzo", "ancient", start)
	now := start.Add(pruneAfter + time.Minute)

	if err := s.Prune(now, pruneAfter); err != nil {
		t.Fatalf("Prune: %v", err)
	}
	// look with a TTL wide enough to have found it, so the assertion is about
	// pruning rather than about the liveness window
	got, err := s.List("hanzo", now, pruneAfter*2)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("got %v, want the long-dead row pruned", ids(got))
	}
}

func ids(v []Session) []string {
	out := make([]string, 0, len(v))
	for _, s := range v {
		out = append(out, s.ID)
	}
	return out
}
