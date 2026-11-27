package postgres

import (
	"testing"
	"time"
)

func TestConvertTakesWhatTheTargetHoldsAndRefusesTheRest(t *testing.T) {
	at := time.Date(2026, 11, 9, 10, 11, 12, 123456000, time.UTC)
	for _, c := range []struct {
		name string
		k    kind
		in   any
		want any
		ok   bool
	}{
		{"int", kInt, int64(7), int64(7), true},
		{"whole float in a bigint", kInt, 3.0, int64(3), true},
		{"fraction in a bigint", kInt, 1.5, nil, false},
		{"text in a bigint", kInt, "seven", nil, false},
		{"int in a float", kFloat, int64(2), 2.0, true},
		{"text bytes", kText, []byte("é"), "é", true},
		{"NUL", kText, "a\x00b", nil, false},
		{"not UTF-8", kText, "\xff", nil, false},
		{"string as blob", kBlob, "ab", []byte("ab"), true},
		{"number as blob", kBlob, int64(1), nil, false},
		{"json", kJSON, `{"a": 1}`, `{"a": 1}`, true},
		{"not json", kJSON, `{a`, nil, false},
		{"flag", kBool, int64(1), true, true},
		{"flag of 2", kBool, int64(2), nil, false},
		{"NULL", kInt, nil, nil, true},
		{"time", kTime, at.Add(789 * time.Nanosecond), at, true}, // to the microsecond PostgreSQL holds
		{"time as CURRENT_TIMESTAMP writes it", kTime, "2026-11-09 10:11:12", time.Date(2026, 11, 9, 10, 11, 12, 0, time.UTC), true},
		{"time as a driver writes it", kTime, "2026-11-09 02:11:12.123456789-08:00", at.Add(0), true},
		{"time in RFC 3339", kTime, "2026-11-09T10:11:12.123456Z", at, true},
		{"time nobody writes", kTime, "next tuesday", nil, false},
		{"integer as a time", kTime, int64(1), nil, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := c.k.convert(c.in)
			switch {
			case c.ok && err != nil:
				t.Fatalf("refused: %v", err)
			case !c.ok && err == nil:
				t.Fatalf("took %v", got)
			case c.ok && !same(got, c.want):
				t.Fatalf("got %#v, want %#v", got, c.want)
			}
		})
	}
}

func same(a, b any) bool {
	if x, ok := a.([]byte); ok {
		y, ok := b.([]byte)
		return ok && string(x) == string(y)
	}
	if x, ok := a.(time.Time); ok {
		y, ok := b.(time.Time)
		return ok && x.Equal(y)
	}
	return a == b
}

// A document is its meaning: jsonb keeps the key order, spacing and number form it likes.
func TestJSONIsComparedByMeaning(t *testing.T) {
	a, err := canonJSON([]byte(`{"b": [1, 2.50, 1e2], "a": {"y": null, "x": true}}`))
	if err != nil {
		t.Fatal(err)
	}
	b, err := canonJSON([]byte(`{"a":{"x":true,"y":null},"b":[1,2.5,100]}`))
	if err != nil {
		t.Fatal(err)
	}
	if string(a) != string(b) {
		t.Fatalf("one document in two spellings:\n%s\n%s", a, b)
	}
	c, _ := canonJSON([]byte(`{"a":{"x":true,"y":null},"b":[1,2.5,101]}`))
	if string(a) == string(c) {
		t.Fatal("two different documents compared equal")
	}
}
