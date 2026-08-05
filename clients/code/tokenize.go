package code

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"unicode"
)

// operators are the multi- and single-char code operators the tokenizer keeps as
// standalone terms (the SOTA "keep operators" rule) so a search for `->` or `::`
// is a real token, not lost to whitespace splitting. Longest-first so `==` is one
// token, not two `=`.
var operators = []string{
	"<<=", ">>=", "...", "===", "!==",
	"->", "=>", "::", "==", "!=", "<=", ">=", "&&", "||", "++", "--",
	":=", "+=", "-=", "*=", "/=", "%=", "|=", "&=", "^=", "<<", ">>", "**",
	"+", "-", "*", "/", "%", "<", ">", "=", "&", "|", "^", "!", "~", "?",
}

// codeTokens splits source text into search terms the way a code-search index
// should: identifiers are lowercased and ALSO split on camelCase and snake_case
// boundaries (getUserName → getusername, get, user, name; MAX_RETRIES →
// max_retries, max, retries), numbers are kept, and operators survive as their
// own tokens. The original (joined) identifier is always emitted alongside its
// subwords so both an exact identifier search and a natural-language "user name"
// search hit the same chunk.
func codeTokens(s string) []string {
	var out []string
	seen := map[string]bool{}
	emit := func(t string) {
		if t == "" || seen[t] {
			return
		}
		seen[t] = true
		out = append(out, t)
	}
	runes := []rune(s)
	for i := 0; i < len(runes); {
		r := runes[i]
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_':
			j := i
			for j < len(runes) && (unicode.IsLetter(runes[j]) || unicode.IsDigit(runes[j]) || runes[j] == '_') {
				j++
			}
			word := string(runes[i:j])
			lw := strings.ToLower(word)
			emit(lw)
			for _, sub := range splitIdent(word) {
				emit(strings.ToLower(sub))
			}
			i = j
		case unicode.IsSpace(r):
			i++
		default:
			if op := matchOperator(runes[i:]); op != "" {
				emit(op)
				i += len([]rune(op))
			} else {
				i++
			}
		}
	}
	return out
}

// splitIdent breaks one identifier on snake_case and camelCase boundaries,
// returning its subwords (never the whole identifier — the caller emits that).
// Handles acronym runs: HTTPServer → HTTP, Server; parseURLPath → parse, URL, Path.
func splitIdent(word string) []string {
	var parts []string
	for _, seg := range strings.FieldsFunc(word, func(r rune) bool { return r == '_' }) {
		parts = append(parts, splitCamel(seg)...)
	}
	if len(parts) <= 1 {
		return nil // single word — no additional subword beyond the whole identifier
	}
	return parts
}

func splitCamel(seg string) []string {
	runes := []rune(seg)
	if len(runes) == 0 {
		return nil
	}
	var words []string
	start := 0
	for i := 1; i < len(runes); i++ {
		prev, cur := runes[i-1], runes[i]
		var next rune
		if i+1 < len(runes) {
			next = runes[i+1]
		}
		lowerToUpper := unicode.IsLower(prev) && unicode.IsUpper(cur)
		digitBoundary := (unicode.IsDigit(prev) != unicode.IsDigit(cur))
		acronymEnd := unicode.IsUpper(prev) && unicode.IsUpper(cur) && next != 0 && unicode.IsLower(next)
		if lowerToUpper || acronymEnd || digitBoundary {
			words = append(words, string(runes[start:i]))
			start = i
		}
	}
	words = append(words, string(runes[start:]))
	return words
}

func matchOperator(runes []rune) string {
	for _, op := range operators {
		or := []rune(op)
		if len(runes) < len(or) {
			continue
		}
		if string(runes[:len(or)]) == op {
			return op
		}
	}
	return ""
}

// ftsBody renders chunk text into the string stored in the FTS `body` column: the
// code-tokenized terms joined by spaces. Feeding tokenized text to the trigram
// tokenizer makes both split subwords and original identifiers substring-matchable.
func ftsBody(text string) string {
	return strings.Join(codeTokens(text), " ")
}

// buildFTSMatch turns a user query into an FTS5 trigram MATCH expression: an OR of
// the query's code-tokens (each quoted so punctuation is literal). Tokens shorter
// than 3 chars are dropped — the trigram tokenizer cannot index them. Returns
// ok=false when nothing usable remains (the caller then degrades honestly).
func buildFTSMatch(query string) (string, bool) {
	var terms []string
	seen := map[string]bool{}
	for _, t := range codeTokens(query) {
		if len(t) < 3 || seen[t] {
			continue
		}
		seen[t] = true
		terms = append(terms, `"`+strings.ReplaceAll(t, `"`, `""`)+`"`)
	}
	if len(terms) == 0 {
		return "", false
	}
	return strings.Join(terms, " OR "), true
}

func escapeLike(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// estimateTokens approximates the LLM token count of text with the ~4-chars-per-
// token industry heuristic (plus 1 so empty is non-zero). It is deliberately
// cheap and provider-agnostic — the /context budget packer only needs a stable
// monotone estimate to pack against, not exact tokenization.
func estimateTokens(text string) int {
	return len(text)/4 + 1
}
