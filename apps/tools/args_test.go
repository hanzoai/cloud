package tools

import (
	"encoding/json"
	"testing"
)

// The arguments arrive through encoding/json, so the test decodes real JSON rather
// than building the map by hand — a hand-built map can hold an int, which JSON
// never delivers.
func decoded(t *testing.T, s string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestArgReaders(t *testing.T) {
	a := decoded(t, `{"name":"  backlog ","code":"  print(1)\n","n":42,"big":1e6,"s":"7","nul":null}`)

	if got := Str(a, "name"); got != "backlog" {
		t.Errorf("Str trims a name: got %q", got)
	}
	if got := Text(a, "code"); got != "  print(1)\n" {
		t.Errorf("Text keeps content verbatim: got %q", got)
	}
	if got := Int(a, "n"); got != 42 {
		t.Errorf("Int reads a JSON number: got %d", got)
	}
	if got := Int(a, "big"); got != 1000000 {
		t.Errorf("Int reads an exponent: got %d", got)
	}
	for _, k := range []string{"s", "nul", "absent"} {
		if got := Int(a, k); got != 0 {
			t.Errorf("Int(%q) is 0 for a non-number, got %d", k, got)
		}
	}
	for _, k := range []string{"n", "nul", "absent"} {
		if Str(a, k) != "" || Text(a, k) != "" {
			t.Errorf("Str/Text(%q) is empty for a non-string", k)
		}
	}
}
