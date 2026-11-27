package team

// A space's documents are written by every replica of the deployment. These tests
// open the store twice, as two replicas do: on hanzo-sql two pools on one space's
// schema, on SQLite the same store, because a file belongs to one process.

import (
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk/dialect"
)

const (
	testOrg   = "acme"
	testSpace = "ws-acme"
)

// replicas opens a space's store twice.
func replicas(t *testing.T) (a, b *docStore) {
	t.Helper()
	dir := t.TempDir()
	a = newStore(dir)
	t.Cleanup(func() { _ = a.Close() })
	db, err := a.db(testOrg, testSpace)
	if err != nil {
		t.Fatal(err)
	}
	if !dialect.Of(db).Postgres() {
		return a, a
	}
	b = newStore(dir)
	t.Cleanup(func() { _ = b.Close() })
	return a, b
}

// A space's documents come back as they went in: by id, by class, counted, replaced
// by the same id and removed.
func TestADocumentRoundTripsOnBothEngines(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		s, _ := replicas(t)
		for _, d := range []map[string]any{
			{"_id": "a", "_class": "x:class:One", "space": "core:space:S", "n": 1.0},
			{"_id": "b", "_class": "x:class:One", "n": 2.0},
			{"_id": "c", "_class": "x:class:Two"},
		} {
			if err := s.put(testOrg, testSpace, d); err != nil {
				t.Fatal(err)
			}
		}
		if err := s.put(testOrg, testSpace, map[string]any{"_id": "a", "_class": "x:class:One", "n": 9.0}); err != nil {
			t.Fatal(err)
		}
		got, err := s.get(testOrg, testSpace, "a")
		if err != nil || got == nil || got["n"] != 9.0 {
			t.Fatalf("get a = %v, %v; want the replacement", got, err)
		}
		if missing, err := s.get(testOrg, testSpace, "nope"); err != nil || missing != nil {
			t.Fatalf("get of an absent id = %v, %v", missing, err)
		}
		ones, err := s.byClasses(testOrg, testSpace, []string{"x:class:One"})
		if err != nil || len(ones) != 2 {
			t.Fatalf("byClasses One = %d, %v; want 2", len(ones), err)
		}
		both, _ := s.byClasses(testOrg, testSpace, []string{"x:class:One", "x:class:Two"})
		if n, _ := s.count(testOrg, testSpace); n != 3 || len(both) != 3 {
			t.Fatalf("count %d, byClasses of both %d; want 3", n, len(both))
		}
		if err := s.del(testOrg, testSpace, "b"); err != nil {
			t.Fatal(err)
		}
		if n, _ := s.count(testOrg, testSpace); n != 2 {
			t.Fatalf("count after a delete = %d, want 2", n)
		}
		// Text PostgreSQL would refuse is refused on both engines.
		if err := s.put(testOrg, testSpace, map[string]any{"_id": "bad\x00id", "_class": "x:class:One"}); err == nil {
			t.Fatal("a NUL in an id was stored")
		}
	})
}

// A space written before the work facet was renamed is carried over when it is
// opened, once, and the facet is where the code now reads it.
func TestALegacyFacetIsCarriedOverOnOpen(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		dir := t.TempDir()
		old := newStore(dir)
		db, err := old.db(testOrg, testSpace)
		if err != nil {
			t.Fatal(err)
		}
		legacy := fmt.Sprintf(`{"_id":"ch","_class":%q,"big":12345678901234567,%q:{"kind":"work"}}`, clChannel, mixinRenamed)
		message := fmt.Sprintf(`{"_id":"m","_class":"chunter:class:ChatMessage",%q:{"kept":true}}`, mixinRenamed)
		for id, raw := range map[string]string{"ch": legacy, "m": message} {
			class := clChannel
			if id == "m" {
				class = "chunter:class:ChatMessage"
			}
			if _, err := db.Exec(`INSERT INTO docs (id, class, space, json) VALUES ($1, $2, '', $3)`, id, class, raw); err != nil {
				t.Fatal(err)
			}
		}
		_ = old.Close()

		s := newStore(dir)
		t.Cleanup(func() { _ = s.Close() })
		ch, err := s.get(testOrg, testSpace, "ch")
		if err != nil || ch == nil {
			t.Fatalf("get ch: %v, %v", ch, err)
		}
		if _, still := ch[mixinRenamed]; still {
			t.Fatal("the old facet key is still on the channel")
		}
		if facet, _ := ch[mixinChannel].(map[string]any); facet["kind"] != "work" {
			t.Fatalf("the facet was not carried to %s: %v", mixinChannel, ch)
		}
		db, _ = s.db(testOrg, testSpace)
		var raw string
		if err := db.QueryRow(`SELECT json FROM docs WHERE id = 'ch'`).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var back struct {
			Big json.Number `json:"big"`
		}
		if err := json.Unmarshal([]byte(raw), &back); err != nil || back.Big.String() != "12345678901234567" {
			t.Fatalf("a number the channel held was changed by the carry-over: %s, %v", raw, err)
		}
		// Only the channel classes: a message keeps whatever it holds.
		m, _ := s.get(testOrg, testSpace, "m")
		if _, kept := m[mixinRenamed]; !kept {
			t.Fatalf("a message was rewritten: %v", m)
		}
	})
}

// Writers on two replicas, each reading a document and writing it back plus one
// under the space's lock, lose none: without the lock a read is overtaken and an
// increment is written over.
func TestReplicasWriteASpaceInTurn(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		a, b := replicas(t)
		if err := a.put(testOrg, testSpace, map[string]any{"_id": "n", "_class": "x:class:Counter", "n": 0.0}); err != nil {
			t.Fatal(err)
		}
		const per = 25
		var wg sync.WaitGroup
		for _, s := range []*docStore{a, b} {
			for range 4 {
				wg.Go(func() {
					for range per {
						held, release, err := s.lock(testOrg, testSpace)
						if err != nil {
							t.Errorf("lock: %v", err)
							return
						}
						doc, err := held.get(testOrg, testSpace, "n")
						if err != nil || doc == nil {
							t.Errorf("get: %v, %v", doc, err)
							_ = release()
							return
						}
						doc["n"] = doc["n"].(float64) + 1
						if err := held.put(testOrg, testSpace, doc); err != nil {
							t.Errorf("put: %v", err)
						}
						if err := release(); err != nil {
							t.Errorf("release: %v", err)
						}
					}
				})
			}
		}
		wg.Wait()
		got, err := a.get(testOrg, testSpace, "n")
		if want := float64(2 * 4 * per); err != nil || got["n"] != want {
			t.Fatalf("the counter reads %v, %v; want %v (an increment was written over)", got["n"], err, want)
		}
	})
}

// The lock is held across replicas: while one holds a space another asks and waits,
// a different space is nobody's, and what the holder wrote is what the next reads.
func TestAReplicaWaitsForAnothersSpace(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		a, b := replicas(t)
		held, release, err := a.lock(testOrg, testSpace)
		if err != nil {
			t.Fatal(err)
		}
		if err := held.put(testOrg, testSpace, map[string]any{"_id": "w", "_class": "x:class:One", "by": "a"}); err != nil {
			t.Fatal(err)
		}
		db, err := a.db(testOrg, testSpace)
		if err != nil {
			t.Fatal(err)
		}
		if dialect.Of(db).Postgres() {
			// A held space is read and written through the holder's transaction: until it
			// commits, the shared store does not see the write.
			if seen, _ := a.get(testOrg, testSpace, "w"); seen != nil {
				t.Fatal("a write under the lock was visible before it committed")
			}
		}

		type answer struct {
			doc map[string]any
			err error
		}
		next := make(chan answer, 1)
		go func() {
			h, rel, err := b.lock(testOrg, testSpace)
			if err != nil {
				next <- answer{nil, err}
				return
			}
			doc, err := h.get(testOrg, testSpace, "w")
			_ = rel()
			next <- answer{doc, err}
		}()
		select {
		case got := <-next:
			t.Fatalf("a second replica took the space while the first held it: %v", got)
		case <-time.After(300 * time.Millisecond):
		}

		// Another space is nobody's.
		otherHeld, otherRelease, err := b.lock(testOrg, "ws-other")
		if err != nil {
			t.Fatalf("another space waited: %v", err)
		}
		_ = otherHeld
		if err := otherRelease(); err != nil {
			t.Fatal(err)
		}

		if err := release(); err != nil {
			t.Fatal(err)
		}
		select {
		case got := <-next:
			if got.err != nil || got.doc == nil || got.doc["by"] != "a" {
				t.Fatalf("the waiting replica read %v, %v; want the first's write", got.doc, got.err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("the waiting replica never took the space after it was released")
		}
	})
}

// Creates of children of one parent, made through sessions on two replicas, move the
// parent's counter once each: the write path's read-modify-write of the counter is the
// one the lock orders.
func TestChildrenCreatedOnTwoReplicasAreAllCounted(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		a, b := replicas(t)
		hier := buildHierarchy(modelJSON)
		on := func(s *docStore) *session {
			return &session{server: &transServer{hub: newHub(nil)}, store: s, hier: hier, org: testOrg, space: testSpace, account: acctSystem}
		}
		write := func(s *session, tx map[string]any) {
			raw, err := json.Marshal(tx)
			if err != nil {
				t.Error(err)
				return
			}
			s.applyTx(raw)
		}
		write(on(a), createTx("ch", clChannel, spaceChannels, acctSystem, map[string]any{"name": "general"}))

		const per = 15
		var wg sync.WaitGroup
		for r, s := range []*docStore{a, b} {
			for w := range 3 {
				wg.Go(func() {
					sess := on(s)
					for i := range per {
						id := fmt.Sprintf("m-%d-%d-%d", r, w, i)
						write(sess, attachedCreateTx(id, clChatMessage, "ch", "ch", clChannel, "messages", acctSystem, map[string]any{"message": "<p>hi</p>"}))
					}
				})
			}
		}
		wg.Wait()
		ch, err := a.get(testOrg, testSpace, "ch")
		if err != nil || ch == nil {
			t.Fatalf("get ch: %v, %v", ch, err)
		}
		if n, _ := asFloat(ch["messages"]); n != 2*3*per {
			t.Fatalf("the channel counts %v messages, want %d", ch["messages"], 2*3*per)
		}
		if n, _ := a.count(testOrg, testSpace); n != 1+2*3*per {
			t.Fatalf("the space holds %d documents, want %d", n, 1+2*3*per)
		}
	})
}

// The socket's check of a tx and its application are one step under the lock, on
// whichever replica it runs: two sessions on two replicas that each check and apply
// an increment of one document, held, lose none.
func TestHeldChecksAndWritesOnTwoReplicasAreOneStep(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		a, b := replicas(t)
		if err := a.put(testOrg, testSpace, map[string]any{"_id": "n", "_class": "x:class:Counter", "n": 0.0}); err != nil {
			t.Fatal(err)
		}
		const per = 20
		var wg sync.WaitGroup
		for _, s := range []*docStore{a, b} {
			for range 3 {
				wg.Go(func() {
					sess := &session{server: &transServer{hub: newHub(nil)}, store: s, hier: buildHierarchy(modelJSON), org: testOrg, space: testSpace, account: acctSystem}
					for range per {
						release, ok := sess.hold()
						if !ok {
							t.Error("hold failed")
							return
						}
						// The check reads the document; the application writes it, through the
						// session's store as the held view.
						doc, _ := sess.store.get(testOrg, testSpace, "n")
						doc["n"] = doc["n"].(float64) + 1
						if err := sess.store.put(testOrg, testSpace, doc); err != nil {
							t.Errorf("put: %v", err)
						}
						if !sess.released(release) {
							t.Error("the held write did not commit")
						}
					}
				})
			}
		}
		wg.Wait()
		got, _ := a.get(testOrg, testSpace, "n")
		if want := float64(2 * 3 * per); got["n"] != want {
			t.Fatalf("the counter reads %v, want %v", got["n"], want)
		}
	})
}
