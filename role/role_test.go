package role

import "testing"

func TestParse(t *testing.T) {
	cases := []struct {
		raw     string
		want    Role
		wantErr bool
	}{
		{"", Writer, false},
		{"writer", Writer, false},
		{"WRITER", Writer, false},
		{"  reader  ", Reader, false},
		{"Reader", Reader, false},
		{"leader", Writer, true},  // invalid → Writer + error (fail-secure default)
		{"primary", Writer, true}, // invalid → Writer + error
	}
	for _, c := range cases {
		got, err := parse(c.raw)
		if (err != nil) != c.wantErr {
			t.Errorf("parse(%q): err=%v wantErr=%v", c.raw, err, c.wantErr)
		}
		if got != c.want {
			t.Errorf("parse(%q)=%q want %q", c.raw, got, c.want)
		}
	}
}

func TestPredicates(t *testing.T) {
	if !Writer.IsWriter() || Writer.IsReader() {
		t.Error("Writer predicates wrong")
	}
	if !Reader.IsReader() || Reader.IsWriter() {
		t.Error("Reader predicates wrong")
	}
}

// TestInvalidDefaultsSafe asserts that an invalid role never silently yields a
// reader (which could demote the real writer) — it yields Writer + an error the
// caller is expected to fail closed on.
func TestInvalidDefaultsSafe(t *testing.T) {
	got, err := parse("garbage")
	if err == nil {
		t.Fatal("expected error for invalid role")
	}
	if got.IsReader() {
		t.Fatal("invalid role must not resolve to Reader")
	}
}
