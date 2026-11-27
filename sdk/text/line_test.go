package text_test

import (
	"strings"
	"testing"
	"unicode"

	"github.com/hanzoai/cloud/sdk/text"
)

// OneLine: a subject or an answer line never carries a line break of any script, nor
// a format character that reorders what follows.
func TestOneLineBreaksEveryBreak(t *testing.T) {
	for _, in := range []string{"a\r\nb", "a b", "a b", "a\u0085b", "a‮b", "a\tb", "a\x00b",
		"a   b", "a　b", "a    b"} {
		if got := text.OneLine(in); got != "a b" {
			t.Errorf("OneLine(%q) = %q, want %q", in, got, "a b")
		}
	}
}

func TestEmailIsOneAddress(t *testing.T) {
	for in, want := range map[string]bool{
		"ada@example.com": true, "a.b@mail.example.co": true,
		"": false, "ada": false, "@example.com": false, "ada@localhost": false,
		"a@example.com, b@example.com": false, "Ada <ada@example.com>": false,
		"ada@@example.com": false, "ada@example.com;b@example.com": false, "ada smith@example.com": false,
		// a control, a format or a bidirectional character, a line or paragraph separator
		// or a non-breaking space is not part of an address a mail rail may be handed
		"ada\x00@example.com": false, "ada@example.com\x00": false, "a\x1bda@example.com": false,
		"ada\u200b@example.com": false, "ada@exam\u202eple.com": false, "ada\u2028@example.com": false,
		"ada\u00a0@example.com": false, "\ufeffada@example.com": false, "ada@example.com\u2066": false,
	} {
		if got := text.Email(in); got != want {
			t.Errorf("Email(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestTagIsALanguage(t *testing.T) {
	for in, want := range map[string]bool{"en": true, "pt-BR": true, "zh-Hant-TW": true, "": false, "not a locale!": false, "e": false, "en_US": false} {
		if got := text.Tag(in); got != want {
			t.Errorf("Tag(%q) = %v, want %v", in, got, want)
		}
	}
}

// FuzzEmail: whatever it is given it never panics, and an address it accepts holds
// no control, space, separator or format character and exactly one @.
func FuzzEmail(f *testing.F) {
	for _, s := range []string{"ada@example.com", "", "a@b.c", "a\x00@b.c", "a@b.c\u202e", "a@@b.c", "a b@c.d"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		if !text.Email(s) {
			return
		}
		if strings.Count(s, "@") != 1 {
			t.Fatalf("Email(%q) accepted %d @", s, strings.Count(s, "@"))
		}
		for _, r := range s {
			if unicode.IsControl(r) || unicode.IsSpace(r) || unicode.In(r, unicode.Cf, unicode.Zl, unicode.Zp, unicode.Zs) {
				t.Fatalf("Email(%q) accepted %U", s, r)
			}
		}
	})
}
