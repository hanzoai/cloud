package code

import (
	"slices"
	"strings"
	"testing"
)

func findSym(syms []Symbol, name string) (Symbol, bool) {
	for _, s := range syms {
		if s.Name == name {
			return s, true
		}
	}
	return Symbol{}, false
}

func hasEdge(edges []Edge, name, fromContains string) bool {
	return slices.ContainsFunc(edges, func(e Edge) bool {
		return e.Name == name && strings.Contains(e.From, fromContains)
	})
}

func TestParseGoSymbolsAndEdges(t *testing.T) {
	p := Parse("greeter.go", goFixture)
	if p.Lang != "go" {
		t.Fatalf("lang=%q want go", p.Lang)
	}

	greeter, ok := findSym(p.Symbols, "Greeter")
	if !ok || greeter.Kind != "struct" {
		t.Errorf("Greeter: found=%v kind=%q want struct", ok, greeter.Kind)
	}
	hello, ok := findSym(p.Symbols, "Hello")
	if !ok {
		t.Fatal("missing method Hello")
	}
	if hello.Kind != "method" {
		t.Errorf("Hello kind=%q want method", hello.Kind)
	}
	if hello.Scope != "Greeter" {
		t.Errorf("Hello scope=%q want Greeter", hello.Scope)
	}
	if !strings.Contains(hello.Signature, "func (g *Greeter) Hello()") {
		t.Errorf("Hello signature=%q missing receiver+name", hello.Signature)
	}
	if _, ok := findSym(p.Symbols, "greet"); !ok {
		t.Error("missing func greet")
	}
	if c, ok := findSym(p.Symbols, "MaxNameLen"); !ok || c.Kind != "const" {
		t.Errorf("MaxNameLen: found=%v kind=%q want const", ok, c.Kind)
	}

	// Real def→ref edge: Hello() calls greet(), attributed to Greeter.Hello.
	if !hasEdge(p.Edges, "greet", "Hello") {
		t.Errorf("missing edge greet<-Greeter.Hello: %+v", p.Edges)
	}

	// AST-boundary chunk carries the symbol + its doc comment.
	var helloChunk *Chunk
	for i := range p.Chunks {
		if p.Chunks[i].Symbol == "Hello" {
			helloChunk = &p.Chunks[i]
		}
	}
	if helloChunk == nil {
		t.Fatal("no chunk for Hello")
	}
	if !strings.Contains(helloChunk.Text, "returns a greeting") {
		t.Errorf("Hello chunk missing doc comment: %q", helloChunk.Text)
	}
}

func TestParseTypeScript(t *testing.T) {
	p := Parse("user.ts", tsFixture)
	if p.Lang != "ts" {
		t.Fatalf("lang=%q want ts", p.Lang)
	}
	if s, ok := findSym(p.Symbols, "getUser"); !ok || s.Kind != "func" {
		t.Errorf("getUser: found=%v kind=%q want func", ok, s.Kind)
	}
	if s, ok := findSym(p.Symbols, "UserService"); !ok || s.Kind != "class" {
		t.Errorf("UserService: found=%v kind=%q want class", ok, s.Kind)
	}
	// find() calls getUser() — a lexical ref edge from the enclosing symbol.
	if !hasEdge(p.Edges, "getUser", "") {
		t.Errorf("missing edge getUser: %+v", p.Edges)
	}
}

func TestParsePython(t *testing.T) {
	p := Parse("animal.py", pyFixture)
	if p.Lang != "python" {
		t.Fatalf("lang=%q want python", p.Lang)
	}
	if s, ok := findSym(p.Symbols, "Animal"); !ok || s.Kind != "class" {
		t.Errorf("Animal: found=%v kind=%q want class", ok, s.Kind)
	}
	speak, ok := findSym(p.Symbols, "speak")
	if !ok {
		t.Fatal("missing def speak")
	}
	// Indentation bounds the block: speak ends before the top-level make_sound.
	if speak.EndLine < speak.Line {
		t.Errorf("speak span invalid: %d-%d", speak.Line, speak.EndLine)
	}
	if _, ok := findSym(p.Symbols, "make_sound"); !ok {
		t.Error("missing def make_sound")
	}
}
