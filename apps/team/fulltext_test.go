package team

import (
	"encoding/json"
	"testing"
)

// TestParseFulltextParams pins the two request shapes the SPA actually sends —
// a bare string and the {query} object — plus the options limit. A parser that
// silently returned "" for the object form would reinstate the old bug (a search
// box that always reports no matches) while looking wired up.
func TestParseFulltextParams(t *testing.T) {
	raw := func(vals ...string) []json.RawMessage {
		out := make([]json.RawMessage, 0, len(vals))
		for _, v := range vals {
			out = append(out, json.RawMessage(v))
		}
		return out
	}
	cases := []struct {
		name      string
		params    []json.RawMessage
		wantQ     string
		wantLimit int
	}{
		{"bare string", raw(`"incident runbook"`), "incident runbook", 10},
		{"query object", raw(`{"query":"incident runbook"}`), "incident runbook", 10},
		{"with limit", raw(`"x"`, `{"limit":25}`), "x", 25},
		{"limit over cap ignored", raw(`"x"`, `{"limit":9999}`), "x", 10},
		{"negative limit ignored", raw(`"x"`, `{"limit":-3}`), "x", 10},
		{"whitespace trimmed", raw(`"  padded  "`), "padded", 10},
		{"no params", nil, "", 10},
		{"unusable param", raw(`12345`), "", 10},
	}
	for _, tc := range cases {
		q, limit := parseFulltextParams(tc.params)
		if q != tc.wantQ || limit != tc.wantLimit {
			t.Errorf("%s: got (%q, %d), want (%q, %d)", tc.name, q, limit, tc.wantQ, tc.wantLimit)
		}
	}
}

// TestSearchFulltextEmptyQueryShape proves an unusable query still produces the
// exact wire shape the client's reviver expects, rather than a null the SPA would
// throw on.
func TestSearchFulltextEmptyQueryShape(t *testing.T) {
	s := &session{org: "acme"}
	out := s.searchFulltext(7, nil)

	var env struct {
		Result struct {
			Docs  []any `json:"docs"`
			Total int   `json:"total"`
		} `json:"result"`
	}
	if err := json.Unmarshal(out, &env); err != nil {
		t.Fatalf("reply is not decodable JSON: %v (%s)", err, out)
	}
	if env.Result.Docs == nil {
		t.Fatalf("docs must serialize as [] not null: %s", out)
	}
	if env.Result.Total != 0 {
		t.Fatalf("total = %d, want 0", env.Result.Total)
	}
}
