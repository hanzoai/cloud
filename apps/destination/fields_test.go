package destination

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk/locus"
	"github.com/hanzoai/cloud/sdk/tenure"
)

func fullTouch() Touch {
	return Touch{
		Gclid: "gclid-EAIaIQobChMI-secret", Gbraid: "gbraid-1", Wbraid: "wbraid-1", FBC: "fb.1.1790000000000.fbclid-Abc",
		Lifat: "li-fat-id-9", Twclid: "twclid-9", Ttclid: "ttclid-9", Rdtcid: "rdt-cid-9", Oppref: "oppref-9",
		FBP: "fb.1.1790000000000.123456789", CID: "1234567890.1790000000", UA: "Mozilla/5.0 (X11; Linux) Firefox/130.0",
		URL:     "https://shop.example/landing?utm_source=ads&email=ada%40acme.example",
		Em:      "5d41402abc4b2a76b9719d911017c592ae0f3a3e6d9fcf3f8f2c28bd4d6e5d7a",
		Clicked: time.Unix(1_790_000_000, 0),
	}
}

// rawTouch reads a touch row as the database holds it, past the store.
func rawTouch(t *testing.T, s *Store, org, person string) map[string]string {
	t.Helper()
	got := map[string]string{}
	vals := make([]any, len(touchSealed))
	dest := make([]string, len(touchSealed))
	for i := range vals {
		vals[i] = &dest[i]
	}
	if err := s.db.QueryRow(`SELECT `+strings.Join(touchSealed, ",")+` FROM touches WHERE org = $1 AND person = $2`, org, person).Scan(vals...); err != nil {
		t.Fatalf("raw touch: %v", err)
	}
	for i, c := range touchSealed {
		got[c] = dest[i]
	}
	return got
}

func TestTheSealedColumnsAreTheTouchColumns(t *testing.T) {
	if want := strings.Join(touchSealed, ",") + ",clicked"; touchCols != want {
		t.Fatalf("touchCols = %q, but the sealed columns and clicked make %q", touchCols, want)
	}
	var tt Touch
	if len(tt.fields()) != len(touchSealed) {
		t.Fatalf("Touch.fields has %d values for %d sealed columns", len(tt.fields()), len(touchSealed))
	}
}

// A visitor's click ids, browser ids, agent, page and hashed email are ciphertext in the
// database on both engines, and the store gives them back whole.
func TestATouchIsSealedAtRest(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		s := testStore(t)
		ctx := context.Background()
		want := fullTouch()
		if err := s.keep(ctx, "acme", "sub-1", want); err != nil {
			t.Fatalf("keep: %v", err)
		}

		raw := rawTouch(t, s, "acme", "sub-1")
		wantFields := want.fields()
		for i, c := range touchSealed {
			plain := *wantFields[i]
			if !strings.HasPrefix(raw[c], "seal:") || strings.Contains(raw[c], plain) || strings.Contains(raw[c], plain[:5]) {
				t.Errorf("touches.%s is stored as %.30q: not sealed", c, raw[c])
			}
		}

		got, found, err := s.touch(ctx, "acme", "sub-1")
		if err != nil || !found {
			t.Fatalf("touch: %v, found=%v", err, found)
		}
		if got != want {
			t.Fatalf("touch came back as %+v, want %+v", got, want)
		}

		// A rewrite seals again under a new nonce and still reads back, and a touch with
		// empty fields stores them empty.
		if err := s.keep(ctx, "acme", "sub-1", want); err != nil {
			t.Fatal(err)
		}
		if again := rawTouch(t, s, "acme", "sub-1"); again["gclid"] == raw["gclid"] {
			t.Error("two writes of one value sealed to the same bytes")
		}
		if err := s.keep(ctx, "acme", "sub-2", Touch{CID: "only-a-cid"}); err != nil {
			t.Fatal(err)
		}
		if bare := rawTouch(t, s, "acme", "sub-2"); bare["gclid"] != "" || bare["em"] != "" || !strings.HasPrefix(bare["cid"], "seal:") {
			t.Errorf("a touch with one field: %+v", bare)
		}
		if got, _, err := s.touch(ctx, "acme", "sub-2"); err != nil || got.CID != "only-a-cid" || got.Gclid != "" {
			t.Fatalf("a touch with one field: %+v, %v", got, err)
		}
	})
}

// A touch re-kept under a wallet key (a sale that names both person and payer) is sealed
// for that key, and each key opens only its own.
func TestATouchCopiedToAnotherKeyIsResealed(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		s := testStore(t)
		ctx := context.Background()
		if err := s.keep(ctx, "acme", "sub-1", fullTouch()); err != nil {
			t.Fatal(err)
		}
		t1, _, _ := s.touch(ctx, "acme", "sub-1")
		if err := s.keep(ctx, "acme", wallet+"0xabc", t1); err != nil {
			t.Fatal(err)
		}
		if got, found, err := s.touch(ctx, "acme", wallet+"0xabc"); err != nil || !found || got != t1 {
			t.Fatalf("the copy: %+v, %v", got, err)
		}
		if rawTouch(t, s, "acme", "sub-1")["gclid"] == rawTouch(t, s, "acme", wallet+"0xabc")["gclid"] {
			t.Error("two keys hold the same sealed bytes")
		}
	})
}

// A database writer who carries a visitor's touch to another person, another tenant or a
// planted plaintext gets a refusal and not a conversion credited to the wrong click.
func TestATouchIsBoundToItsKey(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		s := testStore(t)
		ctx := context.Background()
		for _, p := range []string{"sub-1", "sub-2"} {
			if err := s.keep(ctx, "acme", p, fullTouch()); err != nil {
				t.Fatal(err)
			}
		}
		move := func(q string, args ...any) {
			t.Helper()
			if _, err := s.db.ExecContext(ctx, q, args...); err != nil {
				t.Fatal(err)
			}
		}
		// sub-2's gclid replaced by sub-1's sealed bytes.
		move(`UPDATE touches SET gclid = $1 WHERE org = $2 AND person = $3`, rawTouch(t, s, "acme", "sub-1")["gclid"], "acme", "sub-2")
		if _, _, err := s.touch(ctx, "acme", "sub-2"); !errors.Is(err, tenure.ErrFieldAuth) {
			t.Fatalf("a ciphertext carried to another person opened: %v", err)
		}
		// A planted plaintext click id.
		move(`UPDATE touches SET gclid = $1 WHERE org = $2 AND person = $3`, "planted-gclid", "acme", "sub-2")
		if _, _, err := s.touch(ctx, "acme", "sub-2"); !errors.Is(err, tenure.ErrFieldPlain) {
			t.Fatalf("a planted plaintext read as the store's: %v", err)
		}
		// The whole row carried to another tenant.
		move(`UPDATE touches SET org = $1 WHERE org = $2 AND person = $3`, "evil", "acme", "sub-1")
		if _, _, err := s.touch(ctx, "evil", "sub-1"); !errors.Is(err, tenure.ErrFieldAuth) {
			t.Fatalf("a touch carried to another tenant opened: %v", err)
		}
	})
}

func TestTheStoreDoesNotOpenWithoutAMaster(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		for _, master := range [][]byte{nil, testMaster[:31]} {
			if s, err := openStore(t.TempDir(), master); err == nil {
				_ = s.Close()
				t.Fatalf("opened with a %d-byte master", len(master))
			}
		}
	})
}

// A SQLite file written before touches were sealed holds them in plaintext. It is built
// by hand (the rows are plaintext because the code that wrote them was), so this stays on
// SQLite: hanzo-sql has no such file. The upgrade runs at open, and a sale credited from
// the old touch still carries its click.
func TestALegacyFileIsSealedInPlaceAndStillReads(t *testing.T) {
	t.Setenv(locus.Env, "")
	dir := t.TempDir()
	s, err := openStore(dir, testMaster)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	old := fullTouch()
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO touches (org,person,`+touchCols+`,seen) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18)`,
		"acme", "sub-old", old.Gclid, old.Gbraid, old.Wbraid, old.FBC, old.Lifat, old.Twclid, old.Ttclid, old.Rdtcid, old.Oppref, old.FBP, old.CID, old.UA, old.URL, old.Em,
		old.Clicked.Unix(), 1); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s, err = openStore(dir, testMaster)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	got, found, err := s.touch(ctx, "acme", "sub-old")
	if err != nil || !found || got != old {
		t.Fatalf("a legacy touch: %+v, found=%v, %v; want %+v", got, found, err, old)
	}
	for c, v := range rawTouch(t, s, "acme", "sub-old") {
		if !strings.HasPrefix(v, "seal:") {
			t.Errorf("touches.%s is still %.20q after the upgrade", c, v)
		}
	}
	// Opening again changes nothing: a sealed value is never sealed twice.
	before := rawTouch(t, s, "acme", "sub-old")
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if s, err = openStore(dir, testMaster); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	after := rawTouch(t, s, "acme", "sub-old")
	for c := range before {
		if before[c] != after[c] {
			t.Errorf("touches.%s was rewritten by a second open", c)
		}
	}
}
