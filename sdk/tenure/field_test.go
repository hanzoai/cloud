package tenure

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cek"
	"github.com/hanzoai/namespace"
	"golang.org/x/crypto/chacha20poly1305"
)

func rootKey(b byte) []byte { return bytes.Repeat([]byte{b}, keyLen) }

func orgNS(t *testing.T, slug string) namespace.Namespace {
	t.Helper()
	ns, err := namespace.Org(slug)
	if err != nil {
		t.Fatal(err)
	}
	return ns
}

func newFields(t *testing.T, ns namespace.Namespace, sub string, c *Cipher) *Fields {
	t.Helper()
	f, err := c.Fields(ns, sub)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func cipherOf(t *testing.T, keys ...byte) *Cipher {
	t.Helper()
	c, err := NewCipher(rootKey(keys[0]))
	if err != nil {
		t.Fatal(err)
	}
	for i, k := range keys[1:] {
		if c, err = c.WithRoot(uint8(i+2), rootKey(k)); err != nil {
			t.Fatal(err)
		}
	}
	return c
}

func one(t *testing.T) *Fields {
	return newFields(t, namespace.System(), "compliance", cipherOf(t, 1))
}

// sealField and openField are one operation on one column, with its error.
func sealField(f *Fields, table, col string, key []string, plain string) (string, error) {
	r := f.Row(table, key...)
	out := r.Seal(col, plain)
	return out, r.Err()
}

func openField(f *Fields, table, col string, key []string, stored string) (string, error) {
	r := f.Row(table, key...)
	out := r.Open(col, stored)
	return out, r.Err()
}

var subjectKey = []string{"acme", "sub_1"}

func sealEmail(t *testing.T, f *Fields, plain string) string {
	t.Helper()
	enc, err := sealField(f, "compliance_subject", "email", subjectKey, plain)
	if err != nil {
		t.Fatal(err)
	}
	return enc
}

func openEmail(f *Fields, stored string) (string, error) {
	return openField(f, "compliance_subject", "email", subjectKey, stored)
}

func TestFieldRoundTrip(t *testing.T) {
	f := one(t)
	for _, plain := range []string{
		"a@b.example",
		"Zoë Kelling-Wörring 王",
		"with\x00nul and \xff invalid utf-8",
		strings.Repeat("x", 1<<20),
		"seal:looks-like-a-sealed-value",
	} {
		enc := sealEmail(t, f, plain)
		if !strings.HasPrefix(enc, fieldPrefix) {
			t.Fatalf("sealed value does not lead with %q", fieldPrefix)
		}
		// Valid TEXT on a Postgres column whatever the plaintext was.
		if strings.ContainsRune(enc, 0) || strings.ContainsAny(enc, " \n\r\t'\\") {
			t.Fatalf("sealed value is not plain ASCII text: %q", enc[:40])
		}
		if len(plain) > 8 && strings.Contains(enc, plain[:8]) {
			t.Fatalf("sealed value contains the plaintext")
		}
		got, err := openEmail(f, enc)
		if err != nil || got != plain {
			t.Fatalf("open: %v; got a different value (equal=%v)", err, got == plain)
		}
	}
}

func TestFieldEmptyIsEmpty(t *testing.T) {
	f := one(t)
	if enc := sealEmail(t, f, ""); enc != "" {
		t.Fatalf("empty seals to %q; want empty", enc)
	}
	if got, err := openEmail(f, ""); err != nil || got != "" {
		t.Fatalf("empty opens to %q, %v", got, err)
	}
	if f.Blind("compliance_subject", "email", "") != "" {
		t.Fatal("the empty value has an index")
	}
}

func TestTwoSealsOfOneFieldDiffer(t *testing.T) {
	f := one(t)
	if sealEmail(t, f, "same@value.example") == sealEmail(t, f, "same@value.example") {
		t.Fatal("two seals of one value are equal: the ciphertexts are an equality oracle")
	}
}

func TestPlaintextIsNotPassedThrough(t *testing.T) {
	f := one(t)
	if _, err := openEmail(f, "a@b.example"); !errors.Is(err, ErrFieldPlain) {
		t.Fatalf("a plaintext value opened: %v", err)
	}
	// A value that only begins like a sealed one is malformed, not plaintext.
	if _, err := openEmail(f, "seal:not base64 at all"); !errors.Is(err, ErrFieldMalformed) {
		t.Fatalf("garbage after the prefix: %v", err)
	}
}

// Every single-bit flip of every byte of a sealed value, the version byte, the nonce and the
// tag included, is refused; so is every truncation and an appended byte.
func TestFieldTamperIsRefused(t *testing.T) {
	f := one(t)
	enc := sealEmail(t, f, "tamper@me.example")
	raw, _ := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(enc, fieldPrefix))
	reopen := func(r []byte) error {
		_, err := openEmail(f, fieldPrefix+base64.RawURLEncoding.EncodeToString(r))
		return err
	}
	for i := range raw {
		for bit := 0; bit < 8; bit++ {
			m := bytes.Clone(raw)
			m[i] ^= 1 << bit
			if err := reopen(m); err == nil {
				t.Fatalf("flipping bit %d of byte %d (of %d) still opened", bit, i, len(raw))
			}
		}
	}
	for n := 0; n < len(raw); n++ {
		if err := reopen(raw[:n]); err == nil {
			t.Fatalf("a value truncated to %d bytes opened", n)
		}
	}
	if err := reopen(append(bytes.Clone(raw), 0)); err == nil {
		t.Fatal("a value with a byte appended opened")
	}
	// The same bytes in a spelling the encoder does not write.
	if _, err := openEmail(f, enc+"\n"); err == nil {
		t.Fatal("a trailing line break opened")
	}
}

// The slot is part of what a ciphertext says. Moving it anywhere else is refused: another row,
// another tenant, another column, another table, a row key of another shape.
func TestFieldWrongSlotIsRefused(t *testing.T) {
	f := one(t)
	enc := sealEmail(t, f, "bound@here.example")
	for name, at := range map[string]struct {
		table, col string
		key        []string
	}{
		"another row":    {"compliance_subject", "email", []string{"acme", "sub_2"}},
		"another tenant": {"compliance_subject", "email", []string{"evil", "sub_1"}},
		"another column": {"compliance_subject", "name", subjectKey},
		"another table":  {"compliance_check", "email", subjectKey},
		"key reordered":  {"compliance_subject", "email", []string{"sub_1", "acme"}},
		"key shorter":    {"compliance_subject", "email", []string{"sub_1"}},
		"key longer":     {"compliance_subject", "email", []string{"acme", "sub_1", ""}},
	} {
		if _, err := openField(f, at.table, at.col, at.key, enc); !errors.Is(err, ErrFieldAuth) {
			t.Errorf("%s: opened or wrong error: %v", name, err)
		}
	}
	if got, err := openEmail(f, enc); err != nil || got != "bound@here.example" {
		t.Fatalf("the right slot stopped opening: %v", err)
	}
}

// The AAD frames what it binds. Every way of cutting the same characters into a table, a column
// and a row key is a different slot: a framing that merely concatenated would let ("ab","c")
// stand for ("a","bc"). Enumerate every split of a string over the three parts and the key's own
// arity.
func TestFieldSlotFramingIsInjective(t *testing.T) {
	const text = "abcdefg"
	f := one(t)
	type cut struct {
		table, column string
		key           []string
	}
	var cuts []cut
	for i := 1; i <= len(text); i++ {
		for j := i + 1; j <= len(text); j++ {
			rest := text[j:]
			cuts = append(cuts, cut{text[:i], text[i:j], []string{rest}})
			for k := 0; k <= len(rest); k++ {
				cuts = append(cuts, cut{text[:i], text[i:j], []string{rest[:k], rest[k:]}})
			}
		}
	}
	if len(cuts) < 50 {
		t.Fatalf("only %d slots enumerated", len(cuts))
	}
	for i, a := range cuts {
		enc, err := sealField(f, a.table, a.column, a.key, "v")
		if err != nil {
			t.Fatal(err)
		}
		for j, b := range cuts {
			if i == j {
				continue
			}
			if _, err := openField(f, b.table, b.column, b.key, enc); err == nil {
				t.Fatalf("slot %+v opened a value sealed for %+v", b, a)
			}
		}
	}
}

func TestAnUnboundFieldIsRefused(t *testing.T) {
	f := one(t)
	for name, r := range map[string]*Row{"no table": f.Row("", "k"), "no key": f.Row("t")} {
		if r.Seal("c", "v"); !errors.Is(r.Err(), ErrFieldRow) {
			t.Errorf("%s: sealed: %v", name, r.Err())
		}
	}
	if r := f.Row("t", "k"); r.Seal("", "v") != "" || !errors.Is(r.Err(), ErrFieldRow) {
		t.Error("sealed into no column")
	}
	if r := f.Row("t", "k"); r.Open("", "v") != "" || !errors.Is(r.Err(), ErrFieldRow) {
		t.Error("opened from no column")
	}
	// The first failure stops the rest.
	r := f.Row("t", "k")
	good := r.Seal("c", "v")
	r.Open("c", "not sealed")
	if r.Err() == nil || r.Seal("c", "w") != "" || r.Open("c", good) != "" {
		t.Error("a Row went on after its first failure")
	}
}

func TestFieldWrongNamespaceIsRefused(t *testing.T) {
	c := cipherOf(t, 1)
	mine := newFields(t, orgNS(t, "acme"), "compliance", c)
	enc := sealEmail(t, mine, "ns@bound.example")
	for name, other := range map[string]*Fields{
		"another org":       newFields(t, orgNS(t, "evil"), "compliance", c),
		"the system":        newFields(t, namespace.System(), "compliance", c),
		"another subsystem": newFields(t, orgNS(t, "acme"), "destinations", c),
		"another root":      newFields(t, orgNS(t, "acme"), "compliance", cipherOf(t, 2)),
	} {
		if _, err := openEmail(other, enc); !errors.Is(err, ErrFieldAuth) {
			t.Errorf("%s opened it, or failed another way: %v", name, err)
		}
	}
	if got, err := openEmail(newFields(t, orgNS(t, "acme"), "compliance", c), enc); err != nil || got != "ns@bound.example" {
		t.Fatalf("a second Fields for the same namespace and root did not open it: %v", err)
	}
}

// A new root seals, every held root opens, and the version is authenticated: relabelling a value
// with the other root's version does not turn one key's ciphertext into the other's.
func TestFieldRotation(t *testing.T) {
	ns := orgNS(t, "acme")
	before := newFields(t, ns, "compliance", cipherOf(t, 1))
	after := newFields(t, ns, "compliance", cipherOf(t, 1, 2))

	old := sealEmail(t, before, "sealed@v1.example")
	cur := sealEmail(t, after, "sealed@v2.example")

	if got, err := openEmail(after, old); err != nil || got != "sealed@v1.example" {
		t.Fatalf("a version 1 value stopped opening after rotation: %q, %v", got, err)
	}
	if got, err := openEmail(after, cur); err != nil || got != "sealed@v2.example" {
		t.Fatalf("a version 2 value did not open: %q, %v", got, err)
	}
	if raw, _ := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(cur, fieldPrefix)); raw[0] != 2 {
		t.Fatalf("the new root did not seal: version byte %d", raw[0])
	}
	if raw, _ := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(old, fieldPrefix)); raw[0] != 1 {
		t.Fatalf("version byte %d, want 1", raw[0])
	}
	// A process that never held root 2 refuses a value under it, by name.
	if _, err := openEmail(before, cur); !errors.Is(err, ErrFieldVersion) {
		t.Fatalf("an unheld version: %v", err)
	}
	// Relabel: version 1's bytes claiming version 2 (and the reverse) fail the tag.
	for _, enc := range []string{old, cur} {
		raw, _ := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(enc, fieldPrefix))
		raw[0] = 3 - raw[0]
		if _, err := openEmail(after, fieldPrefix+base64.RawURLEncoding.EncodeToString(raw)); !errors.Is(err, ErrFieldAuth) {
			t.Fatalf("a relabelled version opened: %v", err)
		}
	}
	// What was on disk before the field format is untouched by a root: Seal and Open stay on version 1.
	c1, c2 := cipherOf(t, 1), cipherOf(t, 1, 2)
	blob, _ := c1.Seal(testNS("acme"), "research", []byte("snapshot"))
	if got, err := c2.Open(testNS("acme"), "research", blob); err != nil || string(got) != "snapshot" {
		t.Fatalf("a snapshot sealed before a rotation: %q, %v", got, err)
	}
}

func TestWithRootIsValidated(t *testing.T) {
	c := cipherOf(t, 1)
	for name, call := range map[string]func() (*Cipher, error){
		"version 0":        func() (*Cipher, error) { return c.WithRoot(0, rootKey(2)) },
		"version 1":        func() (*Cipher, error) { return c.WithRoot(1, rootKey(2)) },
		"short key":        func() (*Cipher, error) { return c.WithRoot(2, rootKey(2)[:31]) },
		"nil key":          func() (*Cipher, error) { return c.WithRoot(2, nil) },
		"repeated version": func() (*Cipher, error) { d, _ := c.WithRoot(2, rootKey(2)); return d.WithRoot(2, rootKey(3)) },
	} {
		if got, err := call(); err == nil || got != nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// WithRoot leaves its receiver alone.
	if _, err := c.WithRoot(2, rootKey(2)); err != nil {
		t.Fatal(err)
	}
	if _, err := c.WithRoot(2, rootKey(3)); err != nil {
		t.Fatalf("WithRoot changed its receiver: %v", err)
	}
	for name, call := range map[string]func() (*Fields, error){
		"zero namespace":  func() (*Fields, error) { return c.Fields(namespace.Namespace{}, "x") },
		"empty subsystem": func() (*Fields, error) { return c.Fields(namespace.System(), "") },
	} {
		if f, err := call(); err == nil || f != nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestBlindIndex(t *testing.T) {
	c := cipherOf(t, 1)
	a := newFields(t, orgNS(t, "acme"), "compliance", c)
	a2 := newFields(t, orgNS(t, "acme"), "compliance", c)
	other := newFields(t, orgNS(t, "evil"), "compliance", c)
	sub := newFields(t, orgNS(t, "acme"), "destinations", c)

	idx := a.Blind("touches", "em", "x@y.example")
	if len(idx) != 64 {
		t.Fatalf("index is %d hex characters, want 64", len(idx))
	}
	if _, err := hex.DecodeString(idx); err != nil {
		t.Fatal(err)
	}
	if idx != a.Blind("touches", "em", "x@y.example") || idx != a2.Blind("touches", "em", "x@y.example") {
		t.Fatal("the index is not stable for one namespace")
	}
	for name, got := range map[string]string{
		"another namespace": other.Blind("touches", "em", "x@y.example"),
		"another subsystem": sub.Blind("touches", "em", "x@y.example"),
		"another column":    a.Blind("touches", "ua", "x@y.example"),
		"another table":     a.Blind("people", "em", "x@y.example"),
		"another value":     a.Blind("touches", "em", "x@y.examplf"),
		"table/column cut":  a.Blind("touche", "sem", "x@y.example"),
		"another root":      newFields(t, orgNS(t, "acme"), "compliance", cipherOf(t, 2)).Blind("touches", "em", "x@y.example"),
	} {
		if got == idx {
			t.Errorf("%s gives the same index", name)
		}
	}
	// Keyed: not the bare hash of anything a reader without the key can compute.
	for _, bare := range []string{"x@y.example", "touchesemx@y.example"} {
		h := sha256.Sum256([]byte(bare))
		if idx == hex.EncodeToString(h[:]) {
			t.Fatalf("the index is the unkeyed hash of %q", bare)
		}
	}
	// The index is not the AEAD key's work: sealing a value does not reveal it.
	enc, _ := sealField(a, "touches", "em", []string{"acme", "p"}, "x@y.example")
	if strings.Contains(enc, idx[:16]) {
		t.Fatal("the sealed value carries the index")
	}
}

// An error says which column failed and never what it held.
func TestFieldErrorsCarryNoValue(t *testing.T) {
	f := one(t)
	secret := "correct-horse-battery-staple@example.test"
	enc := sealEmail(t, f, secret)
	for _, in := range []string{secret, enc[:len(enc)-2] + "AA", enc[:20]} {
		_, err := openField(f, "compliance_subject", "email", []string{"acme", "sub_9"}, in)
		if err == nil {
			t.Fatal("opened")
		}
		if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), enc[8:24]) {
			t.Fatalf("an error carries the value: %v", err)
		}
		if !strings.Contains(err.Error(), "compliance_subject.email") {
			t.Fatalf("an error does not say which column: %v", err)
		}
	}
}

// The column key is not the SQLCipher key, and Conceal's blob does not open as a field (nor the
// reverse): two formats under one cipher, kept apart.
func TestFieldKeyIsNotTheFileKey(t *testing.T) {
	c := cipherOf(t, 1)
	f := newFields(t, namespace.System(), "compliance", c)
	blob, err := c.Conceal(namespace.System(), "compliance", []byte("tin"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := openEmail(f, fieldPrefix+base64.RawURLEncoding.EncodeToString(blob)); err == nil {
		t.Fatal("a Conceal blob opened as a field")
	}
	enc := sealEmail(t, f, "tin")
	raw, _ := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(enc, fieldPrefix))
	if _, err := c.Open(namespace.System(), "compliance", raw); err == nil {
		t.Fatal("a field opened as a Conceal blob")
	}
	// The key SQLCipher pages are under, handed to the field's own AEAD with the right
	// associated data, opens nothing: the column key is another output of it.
	file, err := cek.DeriveKey(rootKey(1), namespace.System(), "compliance")
	if err != nil {
		t.Fatal(err)
	}
	a, err := chacha20poly1305.NewX(file)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Open(nil, raw[1:1+fieldNonce], raw[1+fieldNonce:], f.aad(raw[0], "compliance_subject", "email", subjectKey)); err == nil {
		t.Fatal("the SQLCipher key opened a field")
	}
}

// ---- storage: both engines ----

const peopleDDL = `CREATE TABLE IF NOT EXISTS people (
  org   TEXT NOT NULL,
  id    TEXT NOT NULL,
  email TEXT NOT NULL DEFAULT '',
  name  TEXT NOT NULL DEFAULT '',
  em_ix TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (org, id)
)`

func rawPeople(t *testing.T, db *sql.DB, org, id, col string) string {
	t.Helper()
	var v string
	if err := db.QueryRow(`SELECT `+col+` FROM people WHERE org = $1 AND id = $2`, org, id).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestStoredFields(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		db := open(namespace.System(), "sealtest")
		ctx := context.Background()
		if _, err := db.ExecContext(ctx, peopleDDL); err != nil {
			t.Fatal(err)
		}
		f := newFields(t, namespace.System(), "sealtest", cipherOf(t, 1))

		put := func(org, id, email, name string) {
			t.Helper()
			r := f.Row("people", org, id)
			e, n := r.Seal("email", email), r.Seal("name", name)
			if err := r.Err(); err != nil {
				t.Fatal(err)
			}
			if _, err := db.ExecContext(ctx, `INSERT INTO people (org, id, email, name, em_ix) VALUES ($1, $2, $3, $4, $5)`,
				org, id, e, n, f.Blind("people", "email", email)); err != nil {
				t.Fatal(err)
			}
		}
		put("acme", "1", "one@acme.example", "Ada Lovelace")
		put("acme", "2", "two@acme.example", "Grace Hopper")

		// What the database holds is ciphertext: no plaintext, and a column shape the engine took.
		for _, c := range []struct{ id, col, plain string }{{"1", "email", "one@acme.example"}, {"1", "name", "Ada Lovelace"}} {
			v := rawPeople(t, db, "acme", c.id, c.col)
			if !strings.HasPrefix(v, fieldPrefix) || strings.Contains(v, c.plain) {
				t.Fatalf("%s.%s is stored as %q", c.id, c.col, v)
			}
			got, err := openField(f, "people", c.col, []string{"acme", c.id}, v)
			if err != nil || got != c.plain {
				t.Fatalf("%s.%s: %q, %v", c.id, c.col, got, err)
			}
		}

		// A lookup by a sealed value goes through its blind column, and finds the one row.
		var id string
		if err := db.QueryRowContext(ctx, `SELECT id FROM people WHERE org = $1 AND em_ix = $2`, "acme",
			f.Blind("people", "email", "two@acme.example")).Scan(&id); err != nil || id != "2" {
			t.Fatalf("blind lookup: %q, %v", id, err)
		}

		// A database writer who swaps two rows' ciphertexts gets a refusal, not the other
		// person's email.
		e1, e2 := rawPeople(t, db, "acme", "1", "email"), rawPeople(t, db, "acme", "2", "email")
		if _, err := db.ExecContext(ctx, `UPDATE people SET email = $1 WHERE org = $2 AND id = $3`, e2, "acme", "1"); err != nil {
			t.Fatal(err)
		}
		if _, err := openField(f, "people", "email", []string{"acme", "1"}, rawPeople(t, db, "acme", "1", "email")); !errors.Is(err, ErrFieldAuth) {
			t.Fatalf("a swapped ciphertext opened: %v", err)
		}
		// Nor a column copied over another: name := email.
		if _, err := db.ExecContext(ctx, `UPDATE people SET email = $1, name = $1 WHERE org = $2 AND id = $3`, e1, "acme", "1"); err != nil {
			t.Fatal(err)
		}
		if _, err := openField(f, "people", "name", []string{"acme", "1"}, rawPeople(t, db, "acme", "1", "name")); !errors.Is(err, ErrFieldAuth) {
			t.Fatalf("a column copied into another opened: %v", err)
		}
		// Nor a row moved to another tenant.
		if _, err := db.ExecContext(ctx, `UPDATE people SET org = $1 WHERE org = $2 AND id = $3`, "evil", "acme", "2"); err != nil {
			t.Fatal(err)
		}
		if _, err := openField(f, "people", "email", []string{"evil", "2"}, rawPeople(t, db, "evil", "2", "email")); !errors.Is(err, ErrFieldAuth) {
			t.Fatalf("a row moved to another tenant opened: %v", err)
		}
	})
}

func TestUpgradeSealsPlaintextInPlace(t *testing.T) {
	db := legacyFile(t)
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, peopleDDL); err != nil {
		t.Fatal(err)
	}
	f := newFields(t, namespace.System(), "sealtest", cipherOf(t, 1))

	// Three rows as the code before sealing wrote them (plaintext), one empty, one already sealed.
	legacy := []struct{ org, id, email, name string }{
		{"acme", "1", "ada@acme.example", "Ada"},
		{"acme", "2", "", "Grace"},
		{"globex", "3", "hal@globex.example", ""},
	}
	for _, r := range legacy {
		if _, err := db.ExecContext(ctx, `INSERT INTO people (org, id, email, name) VALUES ($1, $2, $3, $4)`, r.org, r.id, r.email, r.name); err != nil {
			t.Fatal(err)
		}
	}
	done, _ := sealField(f, "people", "email", []string{"acme", "4"}, "new@acme.example")
	if _, err := db.ExecContext(ctx, `INSERT INTO people (org, id, email) VALUES ($1, $2, $3)`, "acme", "4", done); err != nil {
		t.Fatal(err)
	}

	// A strict reader refuses the legacy row until it is upgraded.
	if _, err := openField(f, "people", "email", []string{"acme", "1"}, rawPeople(t, db, "acme", "1", "email")); !errors.Is(err, ErrFieldPlain) {
		t.Fatalf("a legacy value opened: %v", err)
	}

	n, err := f.Upgrade(ctx, db, "people", []string{"org", "id"}, []string{"email", "name"})
	if err != nil {
		t.Fatal(err)
	}
	if n != 4 { // ada's email and name, grace's name, hal's email
		t.Fatalf("sealed %d values, want 4", n)
	}

	for _, r := range legacy {
		for col, want := range map[string]string{"email": r.email, "name": r.name} {
			v := rawPeople(t, db, r.org, r.id, col)
			if want != "" && (!strings.HasPrefix(v, fieldPrefix) || strings.Contains(v, want)) {
				t.Fatalf("%s.%s still reads %q", r.id, col, v)
			}
			got, err := openField(f, "people", col, []string{r.org, r.id}, v)
			if err != nil || got != want {
				t.Fatalf("%s.%s: %q, %v; want %q", r.id, col, got, err, want)
			}
		}
	}
	if rawPeople(t, db, "acme", "4", "email") != done {
		t.Fatal("a value that was already sealed was rewritten")
	}

	// Idempotent: nothing left to seal, and nothing rewritten.
	before := rawPeople(t, db, "acme", "1", "email")
	again, err := f.Upgrade(ctx, db, "people", []string{"org", "id"}, []string{"email", "name"})
	if err != nil || again != 0 {
		t.Fatalf("second pass sealed %d, %v", again, err)
	}
	if rawPeople(t, db, "acme", "1", "email") != before {
		t.Fatal("a second pass re-sealed a sealed value")
	}
}

// More rows than one pass reads.
func TestUpgradeTakesMoreThanOneBatch(t *testing.T) {
	db := legacyFile(t)
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, peopleDDL); err != nil {
		t.Fatal(err)
	}
	f := newFields(t, namespace.System(), "sealtest", cipherOf(t, 1))
	const rows = upgradeRows*2 + 17
	tx, _ := db.BeginTx(ctx, nil)
	for i := 0; i < rows; i++ {
		if _, err := tx.ExecContext(ctx, `INSERT INTO people (org, id, email) VALUES ($1, $2, $3)`, "acme", string(rune('a'+i%26))+"-"+strings.Repeat("x", i), "p@acme.example"); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	n, err := f.Upgrade(ctx, db, "people", []string{"org", "id"}, []string{"email", "name"})
	if err != nil || n != rows {
		t.Fatalf("sealed %d of %d: %v", n, rows, err)
	}
	var left int
	if err := db.QueryRow(`SELECT COUNT(*) FROM people WHERE substr(email, 1, 5) <> 'seal:'`).Scan(&left); err != nil || left != 0 {
		t.Fatalf("%d rows left in plaintext: %v", left, err)
	}
}

func TestUpgradeRefusesAnythingButIdentifiers(t *testing.T) {
	f := one(t)
	for _, bad := range []string{"people; DROP TABLE people", "People", "a b", "", "x'y"} {
		if _, err := f.Upgrade(context.Background(), nil, bad, []string{"id"}, []string{"email"}); err == nil {
			t.Errorf("table %q accepted", bad)
		}
		if _, err := f.Upgrade(context.Background(), nil, "people", []string{"id"}, []string{bad}); err == nil {
			t.Errorf("column %q accepted", bad)
		}
	}
}
