package knowledge

import (
	"reflect"
	"testing"
)

// TestExtractWikilinks is the table-driven proof of the "[[…]]" parser: alias,
// heading and block-ref suffixes are stripped, embeds count, media attachments are
// dropped, duplicates collapse (case-insensitively), and order is preserved.
func TestExtractWikilinks(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"none", "plain text, no links", nil},
		{"simple", "see [[Alpha]] and [[Beta]]", []string{"Alpha", "Beta"}},
		{"alias", "see [[Target Page|shown text]]", []string{"Target Page"}},
		{"heading anchor", "[[Runbook#Restart]]", []string{"Runbook"}},
		{"block ref", "[[Notes#^abc123]]", []string{"Notes"}},
		{"embed", "![[Transcluded Note]]", []string{"Transcluded Note"}},
		{"media dropped", "![[diagram.png]] but [[Real Page]]", []string{"Real Page"}},
		{"dedup case-insensitive", "[[Foo]] [[foo]] [[FOO]]", []string{"Foo"}},
		{"whitespace trimmed", "[[  Spaced Out  ]]", []string{"Spaced Out"}},
		{"empty ignored", "[[]] [[ ]] [[Ok]]", []string{"Ok"}},
		{"order preserved", "[[C]] [[A]] [[B]] [[A]]", []string{"C", "A", "B"}},
		{"not a link", "single [brackets] and [[unclosed", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := extractWikilinks(tc.in)
			if len(got) == 0 && len(tc.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("extractWikilinks(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

// TestExtractWikilinks_Cap proves the per-page cap bounds a pathological body.
func TestExtractWikilinks_Cap(t *testing.T) {
	var b []byte
	for i := 0; i < maxLinksPerPage+50; i++ {
		b = append(b, []byte("[[p")...)
		b = append(b, []byte{byte('a' + i%26), byte('a' + i/26)}...)
		b = append(b, []byte("]] ")...)
	}
	if got := len(extractWikilinks(string(b))); got > maxLinksPerPage {
		t.Fatalf("extracted %d links, exceeds cap %d", got, maxLinksPerPage)
	}
}
