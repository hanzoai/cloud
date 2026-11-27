package sites

import (
	"bytes"
	"errors"
	"iter"
	"net/http"
	"strings"
	"testing"
)

// keysOf drains a candidate sequence: every key, and the error that ended it.
func keysOf(seq iter.Seq2[string, error]) ([]string, error) {
	var out []string
	for key, err := range seq {
		if err != nil {
			return out, err
		}
		out = append(out, key)
	}
	return out, nil
}

// The declared page is the LAST candidate of a page-shaped path, by the longest
// prefix it sits under and by whole segment; a file is never answered by a page,
// and the table is not read for one.
func TestCandidatesEndWithTheDeclaredPage(t *testing.T) {
	s := testServer()
	table := routes{"chat": "chat.html", "dev": "dev.html", "dev/runs": "dev/runs.html"}
	reads := 0
	declared := func() (routes, error) { reads++; return table, nil }
	for in, want := range map[string]string{
		"chat/abc":       "chat/abc|chat/abc.html|chat/abc/index.html|chat.html",
		"chat":           "chat|chat.html|chat/index.html", // chat.html is not asked twice
		"dev/runs/r_1":   "dev/runs/r_1|dev/runs/r_1.html|dev/runs/r_1/index.html|dev/runs.html",
		"dev/projects":   "dev/projects|dev/projects.html|dev/projects/index.html|dev.html",
		"chatter":        "chatter|chatter.html|chatter/index.html",
		"settings/mcp":   "settings/mcp|settings/mcp.html|settings/mcp/index.html",
		"":               "index.html",
		"chat/app.js":    "chat/app.js",
		"chat/data.json": "chat/data.json",
	} {
		got, err := keysOf(s.candidates(in, declared))
		if err != nil || strings.Join(got, "|") != want {
			t.Errorf("candidates(%q) = %v %v, want %s", in, got, err, want)
		}
	}
	if reads != 7 {
		t.Errorf("the table was read %d times, want once per page-shaped path (7)", reads)
	}

	// A caller that stops at the first hit never reads the table.
	reads = 0
	for range s.candidates("chat/abc", declared) {
		break
	}
	if reads != 0 {
		t.Fatal("the table was read for a path a file answered")
	}

	// "/" declares the whole site one app.
	all := func() (routes, error) { return routes{"": "index.html"}, nil }
	if got, _ := keysOf(s.candidates("any/deep/path", all)); got[len(got)-1] != "index.html" {
		t.Fatalf("a root prefix did not answer a deep path: %v", got)
	}

	// A store that could not read the table ends the sequence with its error.
	down := errors.New("store down")
	if _, err := keysOf(s.candidates("chat/abc", func() (routes, error) { return nil, down })); !errors.Is(err, down) {
		t.Fatalf("the table's read error was not yielded: %v", err)
	}
}

// A table is prefixes to .html pages, or it is nothing: anything else declares
// no page at all, and what it names is cleaned like a request path so it cannot
// leave the site.
func TestParseRoutes(t *testing.T) {
	got := parseRoutes([]byte(`{"/chat/": "/chat.html", "/": "index.html", "dev": "dev/index.htm", "/a%20b": "../../../other/x.html"}`))
	want := routes{"chat": "chat.html", "": "index.html", "dev": "dev/index.htm", "a b": "other/x.html"}
	if len(got) != len(want) {
		t.Fatalf("parseRoutes = %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("parseRoutes[%q] = %q, want %q (all: %v)", k, got[k], v, got)
		}
	}
	for _, bad := range []string{
		``, `null`, `[]`, `{}`, `"chat.html"`, `{"/chat": 1}`,
		`{"/chat": "chat.js"}`, `{"/chat": "chat"}`, `{"/chat": "/"}`,
		`{"/chat": "chat.html", "chat/": "other.html"}`,
		`{"/chat": "chat.html"`,
	} {
		if r := parseRoutes([]byte(bad)); r != nil {
			t.Errorf("parseRoutes(%s) = %v, want nothing", bad, r)
		}
	}
	var many strings.Builder
	many.WriteString("{")
	for i := range maxFallbackRoutes + 1 {
		if i > 0 {
			many.WriteString(",")
		}
		many.WriteString(`"/p` + strings.Repeat("x", i) + `":"p.html"`)
	}
	many.WriteString("}")
	if r := parseRoutes([]byte(many.String())); r != nil {
		t.Fatalf("a table past %d rows declared %d", maxFallbackRoutes, len(r))
	}
}

// THE THREE CASES, through the edge: a declared deep path answers its page 200,
// an undeclared one stays 404, and a bundle without the file is served as it was.
func TestFallbackThroughTheEdge(t *testing.T) {
	chat := []byte("<!doctype html><title>chat</title>")
	notFound := []byte("<!doctype html><title>custom 404</title>")

	t.Run("declared deep path is its page", func(t *testing.T) {
		st := startStore(t)
		st.put("sites/o/mysite/chat.html", chat)
		st.put("sites/o/mysite/_fallback.json", []byte(`{"/chat": "chat.html"}`))
		for _, p := range []string{"/chat/abc123", "/chat/abc123/", "/chat/a/b/c", "/chat"} {
			if code, body := serveSite(t, p); code != http.StatusOK || !bytes.Equal(body, chat) {
				t.Fatalf("GET %s = %d %q, want 200 with chat.html", p, code, body)
			}
		}
	})

	t.Run("undeclared path stays 404", func(t *testing.T) {
		st := startStore(t)
		st.put("sites/o/mysite/chat.html", chat)
		st.put("sites/o/mysite/404.html", notFound)
		st.put("sites/o/mysite/_fallback.json", []byte(`{"/chat": "chat.html"}`))
		for _, p := range []string{"/settings/mcp", "/chatter", "/chat/app.js", "/chat/data.json"} {
			if code, body := serveSite(t, p); code != http.StatusNotFound || bytes.Equal(body, chat) {
				t.Fatalf("GET %s = %d %q, want a 404 that is not chat.html", p, code, body)
			}
		}
		if code, body := serveSite(t, "/settings/mcp"); !bytes.Equal(body, notFound) {
			t.Fatalf("GET /settings/mcp = %d %q, want the site's own 404.html", code, body)
		}
	})

	t.Run("no table is today", func(t *testing.T) {
		st := startStore(t)
		st.put("sites/o/mysite/chat.html", chat)
		st.put("sites/o/mysite/404.html", notFound)
		if code, body := serveSite(t, "/chat"); code != http.StatusOK || !bytes.Equal(body, chat) {
			t.Fatalf("GET /chat = %d %q, want 200 with chat.html", code, body)
		}
		if code, body := serveSite(t, "/chat/abc123"); code != http.StatusNotFound || !bytes.Equal(body, notFound) {
			t.Fatalf("GET /chat/abc123 = %d %q, want the site's own 404", code, body)
		}
	})

	t.Run("a table that is not one declares nothing", func(t *testing.T) {
		st := startStore(t)
		st.put("sites/o/mysite/chat.html", chat)
		st.put("sites/o/mysite/_fallback.json", []byte(`{"/chat": "chat.html",`))
		if code, _ := serveSite(t, "/chat/abc123"); code != http.StatusNotFound {
			t.Fatalf("GET /chat/abc123 under a malformed table = %d, want 404", code)
		}
	})

	t.Run("a declared page the bundle lacks is 404", func(t *testing.T) {
		st := startStore(t)
		st.put("sites/o/mysite/_fallback.json", []byte(`{"/chat": "chat.html"}`))
		if code, _ := serveSite(t, "/chat/abc123"); code != http.StatusNotFound {
			t.Fatalf("GET /chat/abc123 with no chat.html = %d, want 404", code)
		}
	})

	t.Run("a table the store cannot read is 503", func(t *testing.T) {
		st := startStore(t)
		st.put("sites/o/mysite/chat.html", chat)
		st.put("sites/o/mysite/_fallback.json", []byte(`{"/chat": "chat.html"}`))
		st.failGets = 1 << 20
		if code, _ := serveSite(t, "/chat/abc123"); code != http.StatusServiceUnavailable {
			t.Fatalf("GET /chat/abc123 with the table unreadable = %d, want 503", code)
		}
	})
}
