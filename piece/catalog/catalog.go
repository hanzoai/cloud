package catalog

import (
	"embed"
	"path"
	"strings"
	"sync"

	"github.com/hanzoai/cloud/piece"
)

//go:embed catalog.json
var catalogJSON []byte

//go:embed logo program
var files embed.FS

var (
	once   sync.Once
	loaded piece.Catalog
	bad    error
	logos  map[string]string
)

// Load is the catalog, parsed once. An error is a build fault — a generated
// file that does not read — and a caller fails its mount on it.
func Load() (piece.Catalog, error) {
	once.Do(func() {
		loaded, bad = piece.Parse(catalogJSON)
		logos = make(map[string]string, len(loaded.Pieces))
		for _, p := range loaded.Pieces {
			if p.Logo != "" {
				logos[p.ID] = p.Logo
			}
		}
	})
	return loaded, bad
}

// Logo is a piece's logo and its content type.
func Logo(id string) ([]byte, string, bool) {
	if _, err := Load(); err != nil {
		return nil, "", false
	}
	name, ok := logos[id]
	if !ok {
		return nil, "", false
	}
	b, err := files.ReadFile("logo/" + name)
	if err != nil {
		return nil, "", false
	}
	if path.Ext(name) == ".svg" {
		return b, "image/svg+xml", true
	}
	return b, "image/png", true
}

// Program is the vendored program of a piece that runs in-process: the
// generated <id>.provider.js, or a hand-written <id>.js that serves the same
// id and takes precedence.
func Program(id string) ([]byte, bool) {
	if strings.ContainsAny(id, "/\\.") {
		return nil, false
	}
	for _, name := range []string{id + ".js", id + ".provider.js"} {
		if b, err := files.ReadFile("program/" + name); err == nil {
			return b, true
		}
	}
	return nil, false
}

// Programs is every id with a vendored program.
func Programs() []string {
	entries, _ := files.ReadDir("program")
	seen := map[string]bool{}
	var out []string
	for _, e := range entries {
		id := strings.TrimSuffix(strings.TrimSuffix(e.Name(), ".js"), ".provider")
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}
