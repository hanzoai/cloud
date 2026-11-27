package cloud

// sqlgate_test.go — THE SHARED DIALECT, HELD BY THE SOURCE.
//
// A store's tests prove its SQL on both engines, but some mistakes in SQL text run
// green on SQLite and are wrong anyway, and a test that does not happen to hit them
// never finds them:
//
//   - SQLite numbers $n parameters by first appearance, not by n. `b = $2 AND a = $1`
//     binds the first argument to b, silently, and returns no rows or the wrong ones.
//     PostgreSQL binds by number. The numbers in a statement must be 1 to n, in the
//     order they are written, which is the one spelling both engines read alike.
//   - A `?` or `?N` placeholder is SQLite's, and PostgreSQL does not read it at all.
//   - INSERT OR <anything>, UPDATE OR <anything> and REPLACE INTO are SQLite's;
//     PostgreSQL has ON CONFLICT.
//
// So the source of every ported package is read, and these are refused wherever they
// are written. The check does not ask what shape a statement has. A placeholder in a
// fragment (` AND x = ?`), in the argument of a Sprintf, in a constant used three
// functions away, or in a piece of a strings.Builder is as wrong as one in a
// statement, and the way to get one past a check of statements is to not write one.
// So:
//
//   - every string literal is read for placeholder shapes, whatever it is part of;
//   - SQL comments are removed first, so `OR/**/IGNORE` is read as `OR IGNORE`;
//   - the text a statement is assembled into is followed through `+`, `+=`,
//     fmt.Sprintf and Fprintf arguments, constants, and strings.Builder writes, and
//     its numbers are held to 1..n in order of first appearance.
//
// What a reading of the source cannot see (a statement composed across a closure, a
// method value, a helper's return, a struct field, another package's constant) the
// harness reads at run time: internal/sqltest asks the same rules, from
// internal/sqlcheck, of every statement a test executes on PostgreSQL.
//
// A package joins portedDirs in the commit that ports it, beside its name in
// portedToSQL.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/internal/sqlcheck"
)

// portedDirs are the packages whose SQL is held to the shared dialect: the stores
// that have been ported, and the packages that implement the dialect itself. An entry
// that ends in .go names one file instead of a package, for a store that sits among
// code which is not SQL: every string literal is read, so a URL joined with "?", a
// ClickHouse insert or a regexp template ("$2 $1") in the same package would be
// refused as the SQLite placeholder it is not.
var portedDirs = []string{
	"apps/ad",
	"apps/admission",
	"apps/affiliate",
	"apps/agents",
	"apps/ai/limits",
	"apps/ai/store.go",
	"apps/author",
	"apps/auto",
	"apps/benchmark/legacy.go",
	"apps/benchmark/store.go",
	"apps/books",
	"apps/campaign",
	"apps/captable",
	"apps/code",
	"apps/coding/inbox.go",
	"apps/coding/ledger.go",
	"apps/coding/park.go",
	"apps/commerce/planlink_store.go",
	"apps/company",
	"apps/compliance",
	"apps/cyber/store.go",
	"apps/dataroom/index.go",
	"apps/dataroom",
	"apps/destination",
	"apps/dev/loop",
	"apps/domain/store.go",
	"apps/entitlement",
	"apps/environment",
	"apps/esign",
	"apps/eval",
	"apps/experiment",
	"apps/flags",
	"apps/framework",
	"apps/functions",
	"apps/gateway/edge",
	"apps/git",
	"apps/git/objects",
	"apps/graph",
	"apps/guide",
	"apps/index",
	"apps/ingress",
	"apps/kms",
	"apps/knowledge",
	"apps/label",
	"apps/leaderboard/store.go",
	"apps/legal",
	"apps/link",
	"apps/marketing",
	"apps/marketplace",
	"apps/o11y",
	"apps/patent",
	"apps/platform",
	"apps/pref",
	"apps/pricing",
	"apps/principals",
	"apps/projects",
	"apps/prompt",
	"apps/provider/bridge_dedupe.go",
	"apps/provider/github_bind_test.go",
	"apps/provider/slack_threads.go",
	"apps/provider/store.go",
	"apps/provider/store_test.go",
	"apps/provisioning",
	"apps/reference",
	"apps/referral",
	"apps/research",
	"apps/reward",
	"apps/risk",
	"apps/sandbox",
	"apps/security",
	"apps/settings",
	"apps/social",
	"apps/source",
	"apps/sync",
	"apps/task",
	"apps/tax",
	"apps/taxonomy",
	"apps/team",
	"apps/tel",
	"apps/tools",
	"apps/train",
	"apps/team/store.go",
	"apps/translate",
	"apps/transport",
	"apps/trust",
	"apps/treasury/ledger/sqlstore",
	"apps/validator",
	"apps/wallet",
	"apps/webhook",
	"apps/world",
	"apps/x402",
	"sdk/audit",
	"goja",
	"internal/driver/postgres",
	"sdk/dialect",
}

// piece is the text an expression stands for: pure when it is made of literals alone,
// and lit when any literal went into it.
type piece struct {
	text      string
	pure, lit bool
}

func (a piece) then(b piece) piece {
	return piece{a.text + b.text, a.pure && b.pure, a.lit || b.lit}
}

// unknown stands for an operand the check cannot read: a call, a variable, a number
// that is worked out at run time. It is a space, so the text on either side of it is
// not run together into something it is not.
var unknown = piece{" ", false, false}

// reading carries what the check knows while it reads one package.
type reading struct {
	fset     *token.FileSet
	consts   map[string]piece
	handled  map[ast.Node]bool
	reported map[string]bool
	report   func(pos token.Pos, problem string)
}

// eval is the text an expression stands for.
func (r *reading) eval(e ast.Expr, env map[string]piece) piece {
	switch x := e.(type) {
	case *ast.BasicLit:
		if x.Kind != token.STRING {
			return unknown
		}
		s, err := strconv.Unquote(x.Value)
		if err != nil {
			return unknown
		}
		return piece{s, true, true}
	case *ast.ParenExpr:
		return r.eval(x.X, env)
	case *ast.Ident:
		if v, ok := env[x.Name]; ok {
			return v
		}
		if v, ok := r.consts[x.Name]; ok {
			return v
		}
	case *ast.BinaryExpr:
		if x.Op == token.ADD {
			return r.eval(x.X, env).then(r.eval(x.Y, env))
		}
	case *ast.CallExpr:
		return r.call(x, env)
	}
	return unknown
}

// call reads the calls that put text together: Sprintf, Repeat, Join.
func (r *reading) call(c *ast.CallExpr, env map[string]piece) piece {
	sel, ok := c.Fun.(*ast.SelectorExpr)
	if !ok {
		return unknown
	}
	pkg, _ := sel.X.(*ast.Ident)
	if pkg == nil {
		return unknown
	}
	switch pkg.Name + "." + sel.Sel.Name {
	case "fmt.Sprintf":
		if len(c.Args) == 0 {
			return unknown
		}
		return r.format(r.eval(c.Args[0], env), c.Args[1:], env)
	case "strings.Repeat":
		if len(c.Args) > 0 {
			p := r.eval(c.Args[0], env)
			p.pure = false // a count that is worked out at run time
			return p
		}
	case "strings.Join":
		if len(c.Args) == 2 {
			if list, ok := c.Args[0].(*ast.CompositeLit); ok {
				sep := r.eval(c.Args[1], env)
				out := piece{"", sep.pure, false}
				for i, el := range list.Elts {
					if i > 0 {
						out = out.then(sep)
					}
					out = out.then(r.eval(el, env))
				}
				return out
			}
		}
	}
	return unknown
}

// format is a Sprintf: the format with each verb replaced by the text of its argument.
func (r *reading) format(f piece, args []ast.Expr, env map[string]piece) piece {
	out := piece{"", f.pure, f.lit}
	next := 0
	for i := 0; i < len(f.text); i++ {
		if f.text[i] != '%' {
			out.text += string(f.text[i])
			continue
		}
		j := i + 1
		for j < len(f.text) && strings.IndexByte("+-# 0123456789.*", f.text[j]) >= 0 {
			j++
		}
		if j >= len(f.text) {
			break
		}
		if f.text[j] == '%' {
			out.text += "%"
		} else if next < len(args) {
			out = out.then(r.eval(args[next], env))
			next++
		} else {
			out = out.then(unknown)
		}
		i = j
	}
	return out
}

// mark records that an expression and everything inside it has been read as a whole.
func (r *reading) mark(e ast.Expr) {
	ast.Inspect(e, func(n ast.Node) bool {
		if n != nil {
			r.handled[n] = true
		}
		return true
	})
}

func (r *reading) check(pos token.Pos, p piece) {
	if !p.lit {
		return
	}
	for _, problem := range sqlcheck.Problems(p.text, p.pure) {
		key := problem + "\x00" + p.text
		if r.reported[key] {
			continue
		}
		r.reported[key] = true
		r.report(pos, problem)
	}
}

// body reads one function: its assignments, its builder writes, and every literal.
func (r *reading) body(b *ast.BlockStmt) {
	env := map[string]piece{}
	ast.Inspect(b, func(n ast.Node) bool {
		if n == nil || r.handled[n] {
			return n != nil && !r.handled[n]
		}
		switch x := n.(type) {
		case *ast.FuncLit:
			r.body(x.Body) // its own scope
			return false
		case *ast.AssignStmt:
			if len(x.Lhs) == len(x.Rhs) {
				for i, lhs := range x.Lhs {
					id, ok := lhs.(*ast.Ident)
					if !ok {
						continue
					}
					p := r.eval(x.Rhs[i], env)
					if !p.lit {
						delete(env, id.Name)
						continue
					}
					if x.Tok == token.ADD_ASSIGN {
						p = env[id.Name].then(p)
					}
					env[id.Name] = p
					r.check(x.Pos(), p)
					r.mark(x.Rhs[i])
				}
			}
		case *ast.ValueSpec:
			for i, name := range x.Names {
				if i < len(x.Values) {
					if p := r.eval(x.Values[i], env); p.lit {
						env[name.Name] = p
						r.check(x.Pos(), p)
						r.mark(x.Values[i])
					}
				}
			}
		case *ast.CallExpr:
			if p := r.call(x, env); p.lit { // a Sprintf, Join or Repeat is read as the text it makes
				r.check(x.Pos(), p)
				r.mark(x)
				break
			}
			r.write(x, env)
		case *ast.BinaryExpr, *ast.BasicLit:
			if p := r.eval(n.(ast.Expr), env); p.lit {
				r.check(n.Pos(), p)
				r.mark(n.(ast.Expr))
			}
		}
		return true
	})
}

// write reads a call that appends to a strings.Builder or a bytes.Buffer, and the
// arguments of any other call.
func (r *reading) write(c *ast.CallExpr, env map[string]piece) {
	if sel, ok := c.Fun.(*ast.SelectorExpr); ok {
		if id, ok := sel.X.(*ast.Ident); ok {
			switch {
			case sel.Sel.Name == "WriteString" && len(c.Args) == 1:
				if p := r.eval(c.Args[0], env); p.lit {
					env[id.Name] = env[id.Name].then(p)
					r.check(c.Pos(), env[id.Name])
					r.mark(c.Args[0])
				}
				return
			case id.Name == "fmt" && (sel.Sel.Name == "Fprintf" || sel.Sel.Name == "Fprint" || sel.Sel.Name == "Fprintln") && len(c.Args) >= 2:
				target := c.Args[0]
				if u, ok := target.(*ast.UnaryExpr); ok && u.Op == token.AND {
					target = u.X
				}
				if tid, ok := target.(*ast.Ident); ok {
					p := r.eval(c.Args[1], env)
					if sel.Sel.Name == "Fprintf" {
						p = r.format(p, c.Args[2:], env)
					}
					if p.lit {
						env[tid.Name] = env[tid.Name].then(p)
						r.check(c.Pos(), env[tid.Name])
						r.mark(c.Args[1])
					}
					return
				}
			}
		}
	}
	for _, a := range c.Args {
		if _, isName := a.(*ast.Ident); isName || r.handled[a] {
			continue // a name was read where it was assigned
		}
		if p := r.eval(a, env); p.lit {
			r.check(a.Pos(), p)
			r.mark(a)
		}
	}
}

// read reads a package's files and answers every problem in them.
func read(fset *token.FileSet, files []*ast.File) []string {
	var out []string
	r := &reading{
		fset:     fset,
		consts:   map[string]piece{},
		handled:  map[ast.Node]bool{},
		reported: map[string]bool{},
		report: func(pos token.Pos, problem string) {
			out = append(out, fset.Position(pos).String()+": "+problem)
		},
	}
	// Constants first, in as many passes as it takes for one to name another.
	for range 3 {
		for _, f := range files {
			ast.Inspect(f, func(n ast.Node) bool {
				if g, ok := n.(*ast.GenDecl); ok && (g.Tok == token.CONST || g.Tok == token.VAR) {
					for _, spec := range g.Specs {
						v := spec.(*ast.ValueSpec)
						for i, name := range v.Names {
							if i < len(v.Values) {
								if p := r.eval(v.Values[i], nil); p.lit {
									r.consts[name.Name] = p
								}
							}
						}
					}
				}
				return true
			})
		}
	}
	for _, f := range files {
		for _, d := range f.Decls {
			switch x := d.(type) {
			case *ast.FuncDecl:
				if x.Body != nil {
					r.body(x.Body)
				}
			case *ast.GenDecl:
				// package-level constants and variables, read where they are declared
				if x.Tok == token.CONST || x.Tok == token.VAR {
					for _, spec := range x.Specs {
						for _, val := range spec.(*ast.ValueSpec).Values {
							if p := r.eval(val, nil); p.lit {
								r.check(val.Pos(), p)
								r.mark(val)
							}
							ast.Inspect(val, func(n ast.Node) bool {
								switch y := n.(type) {
								case *ast.FuncLit:
									r.body(y.Body)
									return false
								case *ast.BasicLit:
									if !r.handled[n] {
										if p := r.eval(y, nil); p.lit {
											r.check(y.Pos(), p)
										}
									}
								}
								return true
							})
						}
					}
				}
			}
		}
	}
	return out
}

func parseSnippet(t *testing.T, src string) []string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "snippet.go", "package p\n"+src, 0)
	if err != nil {
		t.Fatalf("snippet does not parse: %v\n%s", err, src)
	}
	return read(fset, []*ast.File{f})
}

// Every way the review found to put a SQLite-only spelling or a mis-numbered
// placeholder past a check of statements, and the forms that are right.
func TestTheGateReadsEveryWayToWriteAPlaceholder(t *testing.T) {
	slips := map[string]string{
		"a constant fragment holding ?": `const where = " AND x = ?"
func f() { _ = "SELECT a FROM t WHERE y = $1" + where }`,
		"$2 then $1 across +=": `func f() string {
	q := "SELECT a FROM t WHERE b = $2"
	q += " AND a = $1"
	return q
}`,
		"?1":                            `func f() { _ = "SELECT a FROM t WHERE a = ?1" }`,
		"a = ?;":                        `func f() { _ = "SELECT a FROM t WHERE a = ?;" }`,
		"REPLACE INTO":                  `func f() { _ = "REPLACE INTO t (a) VALUES ($1)" }`,
		"INSERT OR FAIL":                `func f() { _ = "INSERT OR FAIL INTO t (a) VALUES ($1)" }`,
		"OR/**/IGNORE":                  `func f() { _ = "INSERT OR/**/IGNORE INTO t (a) VALUES ($1)" }`,
		"UPDATE OR IGNORE after a note": `func f() { _ = "INSERT INTO t (a) VALUES ($1) ON CONFLICT DO NOTHING -- not OR IGNORE\n UPDATE OR IGNORE t SET a = 1" }`,
		"? in a Sprintf arg of a concatenation": `import "fmt"
func f(n int) { _ = "SELECT a FROM t WHERE " + fmt.Sprintf("a = %s AND b = %d", "?", n) }`,
		"? inside a paren": `func f() { _ = "SELECT a FROM t WHERE a IN (?)" }`,
		"a lone $2":        `func f() { _ = "SELECT a FROM t WHERE b = $2" }`,
		"a gap":            `func f() { _ = "SELECT a FROM t WHERE a = $1 AND b = $3" }`,
		"strings.Builder": `import "strings"
func f() string {
	var b strings.Builder
	b.WriteString("SELECT a FROM t WHERE b = $2")
	b.WriteString(" AND a = $1")
	return b.String()
}`,
		"Fprintf into a builder": `import ("fmt"; "strings")
func f() string {
	var b strings.Builder
	fmt.Fprintf(&b, "SELECT a FROM t WHERE b = $2 AND %s", "a = $1")
	return b.String()
}`,
		"Sprintf argument holding the other half": `import "fmt"
func f() string { return fmt.Sprintf("SELECT a FROM t WHERE b = $2 AND %s", "a = $1") }`,
		"Repeat of a mark": `import "strings"
func f(n int) string { return "SELECT a FROM t WHERE a IN (" + strings.Repeat("?,", n) + ")" }`,
		"a fragment of marks": `func f() string { return ",?" }`,
		"a join of fragments": `import "strings"
func f() string { return strings.Join([]string{"SELECT a FROM t WHERE b = $2", "a = $1"}, " AND ") }`,
		"a const of a const": `const a = "SELECT a FROM t WHERE b = $2"
const b = a + " AND a = $1"`,
	}
	for name, src := range slips {
		if got := parseSnippet(t, src); len(got) == 0 {
			t.Errorf("the gate missed: %s\n%s", name, src)
		}
	}

	good := map[string]string{
		"in order":            `func f() { _ = "SELECT a FROM t WHERE a = $1 AND b = $2 ORDER BY c LIMIT $3" }`,
		"a number used twice": `func f() { _ = "SELECT a FROM t WHERE a = $1 OR b = $1 OR c = $2" }`,
		"a dynamic tail": `import "strconv"
func f(n int) string { return "SELECT a FROM t WHERE a = $1 LIMIT $" + strconv.Itoa(n) }`,
		"an IN list built at run time": `func f(n int) string { return "SELECT a FROM t WHERE x = $1 AND k IN (" + marks(2, n) + ")" }
func marks(a, b int) string { return "" }`,
		"a regular expression": `const rule = "^(?:true)$"
var also = "(?i)acme|(?P<x>y)"`,
		"prose with a question": `func f() { _ = "which orgs are meant?" }`,
		"a comment in SQL":      `func f() { _ = "SELECT a FROM t WHERE a = $1 /* the ? is in a note */ -- or REPLACE INTO\n" }`,
		"a fragment alone":      `func f() string { return " AND x = $2" }`,
		"a const used in order": `const where = " AND b = $2"
func f() string { return "SELECT a FROM t WHERE a = $1" + where }`,
		"ON CONFLICT": `func f() { _ = "INSERT INTO t (a) VALUES ($1) ON CONFLICT (a) DO NOTHING" }`,
		"a bare ? is a character": `import "strings"
func f(dsn string) bool { return strings.Contains(dsn, "?") }`,
		"prose naming the SQLite spelling": `func f() error { return errString("INSERT OR IGNORE is SQLite's; write ON CONFLICT") }
func errString(string) error { return nil }`,
		"columns called ignore and fail": `func f() { _ = "SELECT a FROM t WHERE x = $1 OR ignore = $2 OR fail = $3" }`,
	}
	for name, src := range good {
		if got := parseSnippet(t, src); len(got) != 0 {
			t.Errorf("the gate refused a form that is right (%s): %v\n%s", name, got, src)
		}
	}
}

func TestPortedPackagesWriteSQLInTheSharedDialect(t *testing.T) {
	for _, dir := range portedDirs {
		fset := token.NewFileSet()
		var pkgs map[string]*ast.Package
		if strings.HasSuffix(dir, ".go") {
			f, err := parser.ParseFile(fset, dir, nil, 0)
			if err != nil {
				t.Fatalf("%s: %v (a ported file that moved must be renamed in portedDirs)", dir, err)
			}
			pkgs = map[string]*ast.Package{f.Name.Name: {Name: f.Name.Name, Files: map[string]*ast.File{dir: f}}}
		} else {
			var err error
			if pkgs, err = parser.ParseDir(fset, dir, nil, 0); err != nil {
				t.Fatalf("%s: %v", dir, err)
			}
		}
		if len(pkgs) == 0 {
			t.Fatalf("%s: no Go files; a ported package that moved must be renamed in portedDirs", dir)
		}
		for _, pkg := range pkgs {
			for _, problem := range read(fset, sharedDialectFiles(pkg)) {
				t.Error(problem)
			}
		}
	}
}

// clickhouseFiles are the files of a ported package that write hanzo-datastore's
// statements instead: ClickHouse binds `?` positionally and speaks neither of the
// engines a store runs on. A package keeps its telemetry beside its metadata store
// (eval, label, link), and this is the one list of what the gate does not read there,
// a file at a time and a reason each. A file joins in the commit that ports its
// package; none holds a statement for the engines the stores share.
var clickhouseFiles = []string{
	"apps/eval/metrics.go",
	"apps/eval/metrics_test.go",
	"apps/eval/telemetry.go",
	"apps/label/grain_test.go",
	"apps/label/mirror.go",
	"apps/label/mirror_test.go",
	"apps/label/resolve_test.go",
	"apps/link/datastore.go",
	"apps/link/usage_test.go",
	"apps/o11y/alerts_egress_test.go",
	"apps/o11y/alerts_test.go",
	"apps/o11y/metricsgauge.go",
	"apps/o11y/metricsread.go",
	"apps/o11y/traces.go",
	"apps/o11y/traces_test.go",
	"apps/reference/derive.go",
	"apps/reference/reference_test.go",
	"apps/reference/store.go",
	"apps/research/datastore.go",
	"apps/reward/profiles.go",
	"apps/reward/referral.go",
	"apps/reward/tenure.go",
	"apps/reward/usage.go",
	"apps/risk/baseline.go",
	"apps/risk/baseline_test.go",
	"apps/risk/feature.go",
	"apps/risk/feature_test.go",
}

// sharedDialectFiles is the files of pkg that write SQL for the engines a store runs
// on: all but those clickhouseFiles names.
func sharedDialectFiles(pkg *ast.Package) []*ast.File {
	var files []*ast.File
	for name, f := range pkg.Files {
		if !slices.Contains(clickhouseFiles, filepath.ToSlash(name)) {
			files = append(files, f)
		}
	}
	return files
}

// A file the list names must still be there, or the list holds a place for a file
// that moved and a statement written in its stead goes unread.
func TestEveryClickhouseFileExists(t *testing.T) {
	for _, name := range clickhouseFiles {
		if _, err := os.Stat(name); err != nil {
			t.Errorf("%s is in clickhouseFiles and is not there: %v", name, err)
		}
	}
}
