package translate

// keep.go — what a translation must carry over from its source. A model that
// drops a {placeholder}, breaks an ICU argument, adds a tag or an attribute, or
// moves a link returns a string that breaks the program it ships in — or sends
// its reader somewhere the source never did — and the memory would serve it on
// every rebuild after. So a string whose shape moved is an error for that string:
// the quality engine asks for it once more, and what still fails is never stored.
//
// A shape is a SET. A language that needs more plural branches repeats an
// argument, one that needs fewer drops a repeat, and both are right; what may not
// change is which arguments, which selectors, which tags with which attributes,
// and which links there are. The words in an ICU branch, and in the four
// attributes a reader reads, are translated like any other words.
//
// Every reader here is one pass over the string, so a hostile string costs what
// its length costs.

import (
	"html"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// The classes a failure is reported by, in the order they are checked. The
// reason names the class and never the token: it is the caller's text.
const (
	classPlaceholder = "a placeholder"
	classICU         = "an ICU argument"
	classTag         = "a markup tag"
	classURL         = "a URL"
)

var classes = []string{classPlaceholder, classICU, classTag, classURL}

const (
	// maxDepth is how deep nested braces are read; anything nested deeper is one
	// opaque token.
	maxDepth = 32
	// maxTokens is how many things one string's shape holds.
	maxTokens = 1024
)

// shape maps each thing a string must carry over to its class.
type shape map[string]string

func (sh shape) add(tok, class string) {
	if len(sh) < maxTokens {
		sh[tok] = class
	}
}

// kept reports which class of thing translation lost or added against source,
// or "" when its shape is source's.
func kept(source, translation string) string {
	want, got := shapeOf(source), shapeOf(translation)
	for _, class := range classes {
		if differs(want, got, class) {
			return "the translation lost " + class
		}
		if differs(got, want, class) {
			return "the translation added " + class
		}
	}
	return ""
}

// differs reports whether a holds a token of class that b does not.
func differs(a, b shape, class string) bool {
	for tok, c := range a {
		if c == class && b[tok] != class {
			return true
		}
	}
	return false
}

var (
	// A URL, in the characters a URL is spelled with, without the punctuation of
	// the sentence it ends: a full stop of any script is not part of it.
	urlRE = regexp.MustCompile(`https?://[A-Za-z0-9\-._~:/?#@!$&*+,;=%]*[A-Za-z0-9\-_~/#@$&*+=%]`)
	// A markdown link or image target: [text](target "title").
	targetRE = regexp.MustCompile(`\]\(\s*<?([^\s()<>]*)>?(?:\s+(?:"[^"]*"|'[^']*'))?\s*\)`)
	// A printf conversion (%s, %1$d, %@, %(name)s); %% is a percent sign.
	printfRE = regexp.MustCompile(`%%|%(\d+)\$[-+#0]*\d*(?:\.\d+)?[sdif@]|%\([A-Za-z0-9_]+\)[sdif]|%[-+#0]*\d*(?:\.\d+)?[sdif@]`)
)

// shapeOf reads what s must carry over: its message arguments (ICU and template
// braces), its printf conversions, its tags with their attributes, its markdown
// link targets, and its URLs.
func shapeOf(s string) shape {
	sh := shape{}
	newReader(s, sh).text()
	tags(s, sh)
	for _, m := range targetRE.FindAllStringSubmatch(s, -1) {
		sh.add("]("+clean(m[1])+")", classURL)
	}
	for _, u := range urlRE.FindAllString(s, -1) {
		sh.add(u, classURL)
	}
	printf(s, sh)
	return sh
}

// printf files s's conversions. Unnumbered ones fill the arguments in order, so
// their ORDER is the shape (%s then %d is not %d then %s, and two %s are not
// one); a numbered or named one names its argument, so it is kept by itself.
func printf(s string, sh shape) {
	var seq []string
	for _, m := range printfRE.FindAllStringSubmatch(s, -1) {
		switch {
		case m[0] == "%%":
		case m[1] != "" || strings.HasPrefix(m[0], "%("):
			sh.add(m[0], classPlaceholder)
		default:
			seq = append(seq, m[0])
		}
	}
	if len(seq) > 0 {
		sh.add("printf "+strings.Join(seq, " "), classPlaceholder)
	}
}

// ---- tags ----

// words are the attributes whose values are words a reader reads, and so are
// translated; every other attribute — href, src, action, an event handler — must
// come back exactly as it went.
var words = map[string]bool{"alt": true, "title": true, "aria-label": true, "placeholder": true}

// tags files each tag of s — whether it closes, its name, and its attributes
// with their values, or only the name of one in words — reading s the way a
// browser does: a quoted value holds '<' and '>', and a tag left open runs to the
// end of the string, where a browser drops it.
func tags(s string, sh shape) {
	for i := 0; i < len(s); {
		if s[i] != '<' {
			i++
			continue
		}
		j := i + 1
		closes := j < len(s) && s[j] == '/'
		if closes {
			j++
		}
		if j >= len(s) || !isLetter(s[j]) {
			i++
			continue
		}
		k := j
		for k < len(s) && !isSpace(s[k]) && s[k] != '/' && s[k] != '>' {
			k++
		}
		attrs, end := attributes(s, k)
		if end >= len(s) {
			return
		}
		sort.Strings(attrs)
		tok := "<" + strings.ToLower(s[j:k])
		if closes {
			tok = "</" + strings.ToLower(s[j:k])
		}
		sh.add(strings.Join(append([]string{tok}, attrs...), " ")+">", classTag)
		i = end + 1
	}
}

// attributes reads a tag's attributes from i to its '>' and returns them with the
// index of that '>', or len(s) when the tag never closes.
func attributes(s string, i int) ([]string, int) {
	var out []string
	for {
		for i < len(s) && (isSpace(s[i]) || s[i] == '/') {
			i++
		}
		if i >= len(s) || s[i] == '>' {
			return out, i
		}
		j := i + 1 // a name's first character may be anything, '=' included
		for j < len(s) && !isSpace(s[j]) && s[j] != '/' && s[j] != '>' && s[j] != '=' {
			j++
		}
		name := strings.ToLower(s[i:j])
		i = j
		for i < len(s) && isSpace(s[i]) {
			i++
		}
		value := ""
		if i < len(s) && s[i] == '=' {
			i++
			for i < len(s) && isSpace(s[i]) {
				i++
			}
			switch {
			case i < len(s) && (s[i] == '"' || s[i] == '\''):
				q := strings.IndexByte(s[i+1:], s[i])
				if q < 0 {
					return out, len(s)
				}
				value, i = s[i+1:i+1+q], i+q+2
			default:
				j := i
				for j < len(s) && !isSpace(s[j]) && s[j] != '>' {
					j++
				}
				value, i = s[i:j], j
			}
		}
		if words[name] {
			out = append(out, name)
		} else {
			out = append(out, name+"="+clean(value))
		}
	}
}

// clean is a value as a browser resolves it: entities decoded, and the
// whitespace and control characters it ignores in a URL dropped — so
// `java&#x09;script:` reads as what it is.
func clean(v string) string {
	return strings.Map(func(r rune) rune {
		if r <= ' ' || r == 0x7f {
			return -1
		}
		return r
	}, html.UnescapeString(v))
}

// ---- ICU ----

// categories are the plural categories CLDR defines. A plural or selectordinal
// selector is one of them or an exact =N; anything else, or a plural with no
// other, is refused by every ICU runtime.
var categories = map[string]bool{"zero": true, "one": true, "two": true, "few": true, "many": true, "other": true}

// reader reads a string as an ICU message: text, {arguments} — simple ({name}),
// typed ({n, number}) or complex ({n, plural|selectordinal|select, ...}) — a
// plural's #, and the {{name}} of a template. An apostrophe is read as text,
// which is what it is in most catalogs (l'{app}), not as ICU's quote.
//
// match pairs every brace in one pass, so no brace is ever searched for twice.
type reader struct {
	s     string
	i     int
	match []int // match[i] is the '}' closing the '{' at i, -1 when none
	sh    shape
	open  []map[string]bool // the branches being read, innermost last, each collecting the arguments directly inside it
	depth int
}

func newReader(s string, sh shape) *reader {
	match := make([]int, len(s))
	var stack []int
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '{':
			match[i] = -1
			stack = append(stack, i)
		case '}':
			if n := len(stack); n > 0 {
				match[stack[n-1]] = i
				stack = stack[:n-1]
			}
		}
	}
	return &reader{s: s, match: match, sh: sh}
}

func (r *reader) text() {
	for r.i < len(r.s) {
		r.message("")
		r.i++ // a '}' that closes nothing is text
	}
}

// add files an argument in the shape, and in the branch it is directly inside:
// an argument nested deeper is already part of the token of the one it is in.
func (r *reader) add(tok, class string) {
	r.sh.add(tok, class)
	if n := len(r.open); n > 0 {
		r.open[n-1][tok] = true
	}
}

// message reads up to the '}' that closes the message it is in, or the end.
// plural names the plural argument a '#' here stands for; "" outside one.
func (r *reader) message(plural string) {
	for r.i < len(r.s) {
		switch r.s[r.i] {
		case '}':
			return
		case '{':
			r.argument(plural)
		case '#':
			if plural != "" {
				r.sh.add("#"+plural, classICU)
			}
			r.i++
		default:
			r.i++
		}
	}
}

// argument reads what opens at r.i and leaves r.i past its close. A brace that
// closes nothing is text; braces that hold no argument are read through.
func (r *reader) argument(plural string) {
	start, end := r.i, r.match[r.i]
	if end < 0 {
		r.i++
		return
	}
	if r.depth >= maxDepth {
		r.add("{…}", classICU)
		r.i = end + 1
		return
	}
	if r.s[start+1] == '{' && r.match[start+1] == end-1 {
		r.add("{{"+strings.TrimSpace(r.s[start+2:end-1])+"}}", classPlaceholder)
		r.i = end + 1
		return
	}
	j := r.field(start+1, end)
	name := strings.TrimSpace(r.s[start+1 : j])
	if r.s[j] == '}' {
		r.add("{"+name+"}", classPlaceholder)
		r.i = end + 1
		return
	}
	if r.s[j] == '{' {
		r.through(start, end, plural)
		return
	}
	k := r.field(j+1, end)
	kind := strings.TrimSpace(r.s[j+1 : k])
	switch {
	case r.s[k] == '}':
		r.add("{"+name+","+kind+"}", classPlaceholder)
	case r.s[k] == '{':
		r.through(start, end, plural)
		return
	case kind == "plural" || kind == "selectordinal" || kind == "select":
		r.i = k + 1
		r.depth++
		r.options(name, kind, plural, end)
		r.depth--
	default:
		r.add("{"+name+","+kind+","+strings.TrimSpace(r.s[k+1:end])+"}", classPlaceholder)
	}
	r.i = end + 1
}

// through reads the inside of braces that hold no argument as text, and leaves
// r.i past their close.
func (r *reader) through(start, end int, plural string) {
	r.i = start + 1
	r.depth++
	r.message(plural)
	r.depth--
	r.i = end + 1
}

// field is the index of the first ',', '{' or '}' from i; end at the latest.
func (r *reader) field(i, end int) int {
	for i < end && r.s[i] != ',' && r.s[i] != '{' && r.s[i] != '}' {
		i++
	}
	return i
}

// options reads a complex argument's selectors and their messages from r.i up to
// end, its closing brace, and files the argument as: its name; its type; the
// selectors a translation must keep — a select's keys, which are values in the
// data, and a plural's exact =N matches and offset, since its categories (one,
// few, many) are the target language's own; the arguments EVERY branch holds,
// so a branch that drops one fails; and whether it is well formed (an other
// branch, and only real plural categories).
func (r *reader) options(name, kind, plural string, end int) {
	inner := plural
	if kind != "select" {
		inner = name
	}
	var keys []string
	var each map[string]bool
	other, valid := false, true
	for {
		r.space(end)
		j := r.i
		for j < end && r.s[j] != '{' && r.s[j] != '}' && !isSpace(r.s[j]) {
			j++
		}
		sel := r.s[r.i:j]
		r.i = j
		r.space(end)
		if strings.HasPrefix(sel, "offset:") {
			keys = append(keys, sel)
			continue
		}
		if r.i >= end || r.s[r.i] != '{' {
			break // malformed past here: what was read stands
		}
		switch {
		case sel == "other":
			other = true
		case kind != "select" && !strings.HasPrefix(sel, "=") && !categories[sel]:
			valid = false
		}
		if kind == "select" || strings.HasPrefix(sel, "=") {
			keys = append(keys, sel)
		}
		close := r.match[r.i]
		branch := map[string]bool{}
		r.open = append(r.open, branch)
		r.i++
		r.message(inner)
		r.open = r.open[:len(r.open)-1]
		r.i = close + 1
		if each == nil {
			each = branch
			continue
		}
		for tok := range each {
			if !branch[tok] {
				delete(each, tok)
			}
		}
	}
	held := make([]string, 0, len(each))
	for tok := range each {
		held = append(held, tok)
	}
	sort.Strings(keys)
	sort.Strings(held)
	tok := "{" + name + "," + kind + "," + strings.Join(keys, " ") + "|" + strings.Join(held, " ") +
		"|" + strconv.FormatBool(other && valid) + "}"
	r.add(tok, classICU)
}

func (r *reader) space(end int) {
	for r.i < end && isSpace(r.s[r.i]) {
		r.i++
	}
}

func isSpace(b byte) bool { return b == ' ' || b == '\t' || b == '\n' || b == '\r' || b == '\f' }

func isLetter(b byte) bool { return 'a' <= b && b <= 'z' || 'A' <= b && b <= 'Z' }
