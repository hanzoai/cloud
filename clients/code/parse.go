package code

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path"
	"regexp"
	"strings"
)

// Symbol is one definition: a name of a given kind at file:line, with its
// verbatim signature and enclosing scope (a method's receiver/class). On a read
// it also carries Repo/Path from the store; the parser leaves those empty and the
// store stamps them on write.
type Symbol struct {
	Repo      string `json:"repo,omitempty"`
	Path      string `json:"file"`
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	Line      int    `json:"line"`
	EndLine   int    `json:"endLine"`
	Signature string `json:"signature,omitempty"`
	Scope     string `json:"scope,omitempty"`
	Lang      string `json:"lang,omitempty"`
}

// Edge is one reference: symbol Name is used at file:line inside From (the
// enclosing symbol / caller). It is the raw def→ref material; callersOf resolves
// a name to the enclosing symbols that reference it.
type Edge struct {
	Name string
	Path string
	Line int
	From string
}

// Chunk is an AST-boundary span (a function/class/type + its doc), the unit the
// semantic tier embeds — never a fixed window for parsed code.
type Chunk struct {
	Path      string
	StartLine int
	EndLine   int
	Symbol    string
	Kind      string
	Lang      string
	Text      string
}

// Parsed is the extracted index material for one file.
type Parsed struct {
	Lang    string
	Symbols []Symbol
	Edges   []Edge
	Chunks  []Chunk
}

// langOf maps a file path to one of the covered languages, else "text".
func langOf(p string) string {
	switch strings.ToLower(path.Ext(p)) {
	case ".go":
		return "go"
	case ".ts", ".tsx", ".mts", ".cts":
		return "ts"
	case ".js", ".jsx", ".mjs", ".cjs":
		return "js"
	case ".py", ".pyi":
		return "python"
	case ".rs":
		return "rust"
	case ".sol":
		return "solidity"
	default:
		return "text"
	}
}

// Parse extracts symbols, edges, and chunks from one file. Go uses the stdlib
// AST (precise defs + real call edges); the other covered languages use compact
// lexical extractors; anything else is chunked into bounded windows so the
// lexical + semantic tiers still cover it.
func Parse(p, content string) Parsed {
	switch langOf(p) {
	case "go":
		if out, ok := parseGo(p, content); ok {
			return out
		}
		return parseLexical(p, content, "go")
	case "text":
		return Parsed{Lang: "text", Chunks: windowChunks(p, content, "text")}
	default:
		return parseLexical(p, content, langOf(p))
	}
}

// ── Go (precise, stdlib AST) ─────────────────────────────────────────────────

func parseGo(p, content string) (Parsed, bool) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, p, content, parser.ParseComments|parser.SkipObjectResolution)
	if f == nil || err != nil {
		return Parsed{}, false
	}
	lines := strings.Split(content, "\n")
	out := Parsed{Lang: "go"}
	line := func(pos token.Pos) int { return fset.Position(pos).Line }
	offset := func(pos token.Pos) int { return fset.Position(pos).Offset }

	addChunk := func(startLine, endLine int, sym, kind string) {
		if startLine < 1 || endLine < startLine || startLine > len(lines) {
			return
		}
		if endLine > len(lines) {
			endLine = len(lines)
		}
		out.Chunks = append(out.Chunks, Chunk{
			Path: p, StartLine: startLine, EndLine: endLine, Symbol: sym, Kind: kind, Lang: "go",
			Text: strings.Join(lines[startLine-1:endLine], "\n"),
		})
	}

	for _, decl := range f.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			kind, scope := "func", ""
			if d.Recv != nil && len(d.Recv.List) > 0 {
				kind = "method"
				scope = receiverType(d.Recv.List[0].Type)
			}
			sigEnd := offset(d.End())
			if d.Body != nil {
				sigEnd = offset(d.Body.Pos())
			}
			sig := strings.TrimSpace(content[offset(d.Pos()):sigEnd])
			start := line(d.Pos())
			if d.Doc != nil {
				start = line(d.Doc.Pos())
			}
			end := line(d.End())
			out.Symbols = append(out.Symbols, Symbol{
				Path: p, Name: d.Name.Name, Kind: kind, Line: line(d.Name.Pos()),
				EndLine: end, Signature: sig, Scope: scope, Lang: "go",
			})
			addChunk(start, end, d.Name.Name, kind)
			out.Edges = append(out.Edges, goCallEdges(p, fset, d)...)
		case *ast.GenDecl:
			start := line(d.Pos())
			if d.Doc != nil {
				start = line(d.Doc.Pos())
			}
			end := line(d.End())
			var chunkSym string
			for _, spec := range d.Specs {
				switch sp := spec.(type) {
				case *ast.TypeSpec:
					kind := "type"
					switch sp.Type.(type) {
					case *ast.StructType:
						kind = "struct"
					case *ast.InterfaceType:
						kind = "interface"
					}
					if chunkSym == "" {
						chunkSym = sp.Name.Name
					}
					out.Symbols = append(out.Symbols, Symbol{
						Path: p, Name: sp.Name.Name, Kind: kind, Line: line(sp.Name.Pos()),
						EndLine: line(sp.End()), Signature: "type " + sp.Name.Name, Scope: "", Lang: "go",
					})
				case *ast.ValueSpec:
					kind := "var"
					if d.Tok == token.CONST {
						kind = "const"
					}
					for _, n := range sp.Names {
						if n.Name == "_" {
							continue
						}
						if chunkSym == "" {
							chunkSym = n.Name
						}
						out.Symbols = append(out.Symbols, Symbol{
							Path: p, Name: n.Name, Kind: kind, Line: line(n.Pos()),
							EndLine: line(sp.End()), Signature: kind + " " + n.Name, Lang: "go",
						})
					}
				}
			}
			if d.Tok == token.TYPE && chunkSym != "" {
				addChunk(start, end, chunkSym, "type")
			}
		}
	}
	return out, true
}

func receiverType(e ast.Expr) string {
	switch t := e.(type) {
	case *ast.StarExpr:
		return receiverType(t.X)
	case *ast.Ident:
		return t.Name
	case *ast.IndexExpr: // generic receiver Foo[T]
		return receiverType(t.X)
	case *ast.IndexListExpr:
		return receiverType(t.X)
	}
	return ""
}

// goCallEdges records a call edge for every function call inside fd, attributed
// to fd as the caller. Selector calls (x.Method()) record the method name.
func goCallEdges(p string, fset *token.FileSet, fd *ast.FuncDecl) []Edge {
	from := fd.Name.Name
	if fd.Recv != nil && len(fd.Recv.List) > 0 {
		from = receiverType(fd.Recv.List[0].Type) + "." + fd.Name.Name
	}
	var edges []Edge
	seen := map[string]bool{}
	ast.Inspect(fd, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		name := calleeName(call.Fun)
		if name == "" || len(name) < 3 || isKeyword(name) {
			return true
		}
		key := name + "\x00" + from
		if seen[key] {
			return true
		}
		seen[key] = true
		edges = append(edges, Edge{Name: name, Path: p, Line: fset.Position(call.Pos()).Line, From: from})
		return true
	})
	return edges
}

func calleeName(e ast.Expr) string {
	switch t := e.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.SelectorExpr:
		return t.Sel.Name
	case *ast.IndexExpr:
		return calleeName(t.X)
	case *ast.IndexListExpr:
		return calleeName(t.X)
	}
	return ""
}

// ── lexical (ts/js/python/rust/solidity, and Go fallback) ────────────────────

type declPattern struct {
	re   *regexp.Regexp
	kind string
	// nameGroup is the submatch index holding the symbol name.
	nameGroup int
}

// declPatterns is the per-language declaration grammar. Each entry names the kind
// and which capture group is the identifier. Order matters only for reporting; a
// line is attributed to the first pattern that matches.
var declPatterns = map[string][]declPattern{
	"ts": tsPatterns, "js": tsPatterns,
	"python":   pyPatterns,
	"rust":     rustPatterns,
	"solidity": solPatterns,
	"go":       goLexPatterns,
}

var tsPatterns = []declPattern{
	{regexp.MustCompile(`^\s*(?:export\s+)?(?:default\s+)?(?:async\s+)?function\s*\*?\s*([A-Za-z_$][\w$]*)`), "func", 1},
	{regexp.MustCompile(`^\s*(?:export\s+)?(?:abstract\s+)?class\s+([A-Za-z_$][\w$]*)`), "class", 1},
	{regexp.MustCompile(`^\s*(?:export\s+)?interface\s+([A-Za-z_$][\w$]*)`), "interface", 1},
	{regexp.MustCompile(`^\s*(?:export\s+)?enum\s+([A-Za-z_$][\w$]*)`), "enum", 1},
	{regexp.MustCompile(`^\s*(?:export\s+)?type\s+([A-Za-z_$][\w$]*)\s*=`), "type", 1},
	{regexp.MustCompile(`^\s*(?:export\s+)?(?:const|let|var)\s+([A-Za-z_$][\w$]*)\s*=\s*(?:async\s+)?(?:\([^)]*\)|[A-Za-z_$][\w$]*)\s*(?::[^=]+)?=>`), "func", 1},
}

var pyPatterns = []declPattern{
	{regexp.MustCompile(`^\s*(?:async\s+)?def\s+([A-Za-z_]\w*)`), "func", 1},
	{regexp.MustCompile(`^\s*class\s+([A-Za-z_]\w*)`), "class", 1},
}

var rustPatterns = []declPattern{
	{regexp.MustCompile(`^\s*(?:pub(?:\([^)]*\))?\s+)?(?:async\s+)?(?:unsafe\s+)?fn\s+([A-Za-z_]\w*)`), "func", 1},
	{regexp.MustCompile(`^\s*(?:pub(?:\([^)]*\))?\s+)?struct\s+([A-Za-z_]\w*)`), "struct", 1},
	{regexp.MustCompile(`^\s*(?:pub(?:\([^)]*\))?\s+)?enum\s+([A-Za-z_]\w*)`), "enum", 1},
	{regexp.MustCompile(`^\s*(?:pub(?:\([^)]*\))?\s+)?trait\s+([A-Za-z_]\w*)`), "trait", 1},
	{regexp.MustCompile(`^\s*impl(?:\s*<[^>]*>)?\s+(?:[A-Za-z_][\w:]*\s+for\s+)?([A-Za-z_][\w:]*)`), "impl", 1},
	{regexp.MustCompile(`^\s*(?:pub(?:\([^)]*\))?\s+)?(?:const|static)\s+([A-Za-z_]\w*)`), "const", 1},
}

var solPatterns = []declPattern{
	{regexp.MustCompile(`^\s*(?:abstract\s+)?contract\s+([A-Za-z_]\w*)`), "contract", 1},
	{regexp.MustCompile(`^\s*interface\s+([A-Za-z_]\w*)`), "interface", 1},
	{regexp.MustCompile(`^\s*library\s+([A-Za-z_]\w*)`), "library", 1},
	{regexp.MustCompile(`^\s*function\s+([A-Za-z_]\w*)`), "func", 1},
	{regexp.MustCompile(`^\s*(?:event|error)\s+([A-Za-z_]\w*)`), "event", 1},
	{regexp.MustCompile(`^\s*modifier\s+([A-Za-z_]\w*)`), "modifier", 1},
	{regexp.MustCompile(`^\s*struct\s+([A-Za-z_]\w*)`), "struct", 1},
}

var goLexPatterns = []declPattern{
	{regexp.MustCompile(`^\s*func\s+(?:\([^)]*\)\s*)?([A-Za-z_]\w*)`), "func", 1},
	{regexp.MustCompile(`^\s*type\s+([A-Za-z_]\w*)`), "type", 1},
}

var callRe = regexp.MustCompile(`([A-Za-z_$][\w$]*)\s*\(`)

// parseLexical extracts symbols/edges/chunks for a brace or indentation language
// by scanning declaration lines and bounding each block. It is intentionally
// heuristic (an MVP without a per-language grammar) yet deterministic.
func parseLexical(p, content, lang string) Parsed {
	lines := strings.Split(content, "\n")
	pats := declPatterns[lang]
	out := Parsed{Lang: lang}

	for i := 0; i < len(lines); i++ {
		raw := lines[i]
		for _, pat := range pats {
			m := pat.re.FindStringSubmatch(raw)
			if m == nil {
				continue
			}
			name := m[pat.nameGroup]
			if name == "" {
				continue
			}
			startLine := i + 1
			var endLine int
			if lang == "python" {
				endLine = pyBlockEnd(lines, i)
			} else {
				endLine = braceBlockEnd(lines, i)
			}
			out.Symbols = append(out.Symbols, Symbol{
				Path: p, Name: name, Kind: pat.kind, Line: startLine, EndLine: endLine,
				Signature: signatureOf(raw), Lang: lang,
			})
			out.Chunks = append(out.Chunks, Chunk{
				Path: p, StartLine: startLine, EndLine: endLine, Symbol: name, Kind: pat.kind, Lang: lang,
				Text: strings.Join(lines[startLine-1:endLine], "\n"),
			})
			break
		}
	}

	// A file with no recognized declarations (config, data, prose) still needs
	// lexical + semantic coverage, so fall back to bounded windows.
	if len(out.Chunks) == 0 {
		out.Chunks = windowChunks(p, content, lang)
	}
	out.Edges = lexicalEdges(p, lines, out.Symbols)
	return out
}

// enclosingSymbol returns the innermost symbol whose span contains line.
func enclosingSymbol(syms []Symbol, line int) string {
	best := ""
	bestSpan := 1 << 30
	for _, s := range syms {
		if line >= s.Line && line <= s.EndLine {
			if span := s.EndLine - s.Line; span < bestSpan {
				bestSpan = span
				best = s.Name
				if s.Scope != "" {
					best = s.Scope + "." + s.Name
				}
			}
		}
	}
	return best
}

// lexicalEdges records call-site references (name followed by "(") attributed to
// the enclosing symbol. Deduped by (callee,caller); keywords and sub-3-char names
// are dropped. This is the def→ref-lite material callersOf resolves against the
// symbol table.
func lexicalEdges(p string, lines []string, syms []Symbol) []Edge {
	var edges []Edge
	seen := map[string]bool{}
	for i, raw := range lines {
		for _, m := range callRe.FindAllStringSubmatch(raw, -1) {
			name := m[1]
			if len(name) < 3 || isKeyword(name) {
				continue
			}
			from := enclosingSymbol(syms, i+1)
			if from == "" {
				continue
			}
			key := name + "\x00" + from
			if seen[key] {
				continue
			}
			seen[key] = true
			edges = append(edges, Edge{Name: name, Path: p, Line: i + 1, From: from})
		}
	}
	return edges
}

// braceBlockEnd returns the 1-based line where the block opened on/after startIdx
// closes, by brace balance. A declaration with no brace before its terminator
// (one-line type alias, abstract method) is a single line.
func braceBlockEnd(lines []string, startIdx int) int {
	depth := 0
	opened := false
	for i := startIdx; i < len(lines); i++ {
		for _, r := range lines[i] {
			switch r {
			case '{':
				depth++
				opened = true
			case '}':
				depth--
			}
		}
		if opened && depth <= 0 {
			return i + 1
		}
		if !opened && (strings.Contains(lines[i], ";") || strings.HasSuffix(strings.TrimSpace(lines[i]), ">")) {
			return i + 1
		}
	}
	return len(lines)
}

// pyBlockEnd returns the 1-based line where a def/class block (opened at startIdx)
// ends by dedent: the last line more-indented than the header.
func pyBlockEnd(lines []string, startIdx int) int {
	headIndent := indentOf(lines[startIdx])
	end := startIdx + 1
	for i := startIdx + 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "" {
			continue
		}
		if indentOf(lines[i]) <= headIndent {
			break
		}
		end = i + 1
	}
	return end
}

func indentOf(s string) int {
	n := 0
	for _, r := range s {
		switch r {
		case ' ':
			n++
		case '\t':
			n += 4
		default:
			return n
		}
	}
	return n
}

func signatureOf(line string) string {
	s := strings.TrimSpace(line)
	if i := strings.IndexAny(s, "{"); i >= 0 {
		s = strings.TrimSpace(s[:i])
	}
	return strings.TrimRight(s, " =")
}

// windowChunks splits unparsed content into bounded, overlapping line windows so
// the lexical + semantic tiers cover files without recognized declarations.
func windowChunks(p, content, lang string) []Chunk {
	lines := strings.Split(content, "\n")
	const win = 50
	var out []Chunk
	for start := 0; start < len(lines); start += win {
		end := start + win
		if end > len(lines) {
			end = len(lines)
		}
		text := strings.TrimSpace(strings.Join(lines[start:end], "\n"))
		if text == "" {
			continue
		}
		out = append(out, Chunk{
			Path: p, StartLine: start + 1, EndLine: end, Kind: "block", Lang: lang,
			Text: strings.Join(lines[start:end], "\n"),
		})
	}
	return out
}

var keywords = map[string]bool{
	"if": true, "for": true, "while": true, "switch": true, "return": true,
	"func": true, "function": true, "def": true, "class": true, "catch": true,
	"typeof": true, "new": true, "await": true, "async": true, "import": true,
	"export": true, "const": true, "let": true, "var": true, "type": true,
	"struct": true, "enum": true, "match": true, "range": true, "make": true,
	"len": true, "cap": true, "append": true, "panic": true, "recover": true,
	"print": true, "println": true, "require": true, "assert": true, "super": true,
}

func isKeyword(s string) bool { return keywords[s] }
