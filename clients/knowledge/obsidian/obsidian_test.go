package obsidian

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/clients/knowledge/vault"
)

// readVault loads a real fixture vault directory into []vault.File with
// vault-relative paths, exactly as the KB import handler's unzip would.
func readVault(t *testing.T, root string) []vault.File {
	t.Helper()
	var files []vault.File
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		files = append(files, vault.File{Path: filepath.ToSlash(rel), Data: b})
		return nil
	})
	if err != nil {
		t.Fatalf("read vault: %v", err)
	}
	return files
}

func pageByTitle(pages []vault.Page, title string) (vault.Page, bool) {
	for _, p := range pages {
		if p.Title == title {
			return p, true
		}
	}
	return vault.Page{}, false
}

func TestParse_Vault(t *testing.T) {
	pages, err := Parse(readVault(t, "testdata/vault"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(pages) != 3 {
		t.Fatalf("want 3 pages, got %d: %+v", len(pages), pages)
	}

	road, ok := pageByTitle(pages, "Q3 Roadmap")
	if !ok {
		t.Fatalf("missing Q3 Roadmap page")
	}
	if road.Slug != "q3-roadmap" {
		t.Errorf("slug = %q, want q3-roadmap", road.Slug)
	}
	// Frontmatter must be stripped; body must be valid Lexical.
	if strings.Contains(road.Body, "tags:") || strings.Contains(road.Body, "aliases:") {
		t.Errorf("frontmatter leaked into body: %s", road.Body)
	}
	if !strings.HasPrefix(road.Body, `{"root"`) {
		t.Errorf("body is not Lexical JSON: %s", road.Body[:40])
	}
	// Wikilinks (incl. alias form) must survive verbatim for the KB hook to extract.
	for _, want := range []string{"[[Incident Runbook]]", "[[Team Directory|the team]]", "[[Team Directory]]"} {
		if !strings.Contains(road.Body, want) {
			t.Errorf("body missing wikilink %q", want)
		}
	}

	// A note in a subfolder is still resolved by its filename (Obsidian semantics).
	if _, ok := pageByTitle(pages, "Team Directory"); !ok {
		t.Errorf("subfolder note not imported")
	}
	// Obsidian imports flat: no parent tree.
	for _, p := range pages {
		if p.Parent != "" {
			t.Errorf("page %q has parent %q, Obsidian import must be flat", p.Title, p.Parent)
		}
	}
}

func TestStripFrontmatter(t *testing.T) {
	cases := []struct{ name, in, wantPrefix string }{
		{"with fm", "---\na: 1\n---\n\n# Body", "# Body"},
		{"no fm", "# Just Body", "# Just Body"},
		{"unterminated", "---\nno close\n# still body", "---"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := stripFrontmatter(tc.in); !strings.HasPrefix(got, tc.wantPrefix) {
				t.Errorf("stripFrontmatter(%q) = %q, want prefix %q", tc.in, got, tc.wantPrefix)
			}
		})
	}
}
