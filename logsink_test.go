package cloud

import (
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	luxlog "github.com/luxfi/log"
	otellog "go.opentelemetry.io/otel/log"
	sdklog "go.opentelemetry.io/otel/sdk/log"
)

// caught collects what the SDK hands an exporter. The records MUST come from a
// real LoggerProvider: a hand-built sdklog.Record carries an attribute length
// limit of zero, and the SDK reads >= 0 as a real limit, so every string
// attribute would arrive empty while every int arrived intact — a sink that
// looks like it works and delivers half of each line.
type caught struct{ out *[]sdklog.Record }

func (c caught) OnEmit(_ context.Context, r *sdklog.Record) error {
	*c.out = append(*c.out, *r)
	return nil
}
func (caught) Enabled(context.Context, sdklog.EnabledParameters) bool { return true }
func (caught) Shutdown(context.Context) error                         { return nil }
func (caught) ForceFlush(context.Context) error                       { return nil }

// wire attaches a capturing transport to s and returns what it captured.
func wire(t *testing.T, s *logSink) *[]sdklog.Record {
	t.Helper()
	var got []sdklog.Record
	lp := sdklog.NewLoggerProvider(sdklog.WithProcessor(caught{&got}))
	t.Cleanup(func() { _ = lp.Shutdown(context.Background()) })
	s.attach(lp.Logger("test"))
	return &got
}

func holding(s *logSink) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.held)
}

// One real request line, end to end: what the process prints is what the plane
// receives. The fields are the ones every reader keys on — the level it filters
// by, the ids it joins on, and the two numbers it aggregates.
func TestALineCarriesWhatAReaderQueries(t *testing.T) {
	s := &logSink{}
	got := wire(t, s)

	const trace, span = "42a955c492853ae712c26509654b127f", "ee90855933048377"
	line := `{"level":"error","module":"zip","method":"POST","path":"/v1/x",` +
		`"trace":"` + trace + `","span":"` + span + `","status":503,"duration_ms":12,` +
		`"time":"2026-08-25T17:14:39-07:00","message":"request"}` + "\n"
	if n, err := s.Write([]byte(line)); n != len(line) || err != nil {
		t.Fatalf("Write = (%d, %v), want (%d, nil)", n, err, len(line))
	}
	if len(*got) != 1 {
		t.Fatalf("captured %d records, want 1", len(*got))
	}
	rec := (*got)[0]

	if body := rec.Body().AsString(); body != "request" {
		t.Errorf("body = %q, want the line's message", body)
	}
	if rec.SeverityText() != "error" || rec.Severity() != otellog.SeverityError {
		t.Errorf("severity = %d/%q, want the OTel scale beside the word — a record carrying only the word is unfilterable",
			rec.Severity(), rec.SeverityText())
	}
	if rec.TraceID().String() != trace || rec.SpanID().String() != span {
		t.Errorf("ids = %s/%s, want %s/%s — a line the console cannot join to its span is why this leg exists",
			rec.TraceID(), rec.SpanID(), trace, span)
	}
	if want, _ := time.Parse(time.RFC3339, "2026-08-25T17:14:39-07:00"); !rec.Timestamp().Equal(want) {
		t.Errorf("timestamp = %s, want the line's own %s", rec.Timestamp(), want)
	}

	attrs := map[string]otellog.Value{}
	rec.WalkAttributes(func(kv otellog.KeyValue) bool { attrs[kv.Key] = kv.Value; return true })
	if attrs["module"].AsString() != "zip" || attrs["path"].AsString() != "/v1/x" {
		t.Errorf("string attributes = %v, want them intact", attrs)
	}
	if attrs["status"].AsInt64() != 503 || attrs["duration_ms"].AsInt64() != 12 {
		t.Errorf("status/duration = %v/%v, want numbers — a %q cannot be compared to 500 by any query",
			attrs["status"], attrs["duration_ms"], "503")
	}
	for _, k := range []string{"trace", "span", "time", "level", "message"} {
		if _, dup := attrs[k]; dup {
			t.Errorf("%q is an attribute AND a column — the same fact twice on every row", k)
		}
	}
}

// The writer sits under every log call in the process, so it answers the full
// length and no error whatever it was handed. luxlog's multi-writer turns a
// short write into io.ErrShortWrite for the CALLER, which would make a telemetry
// leg able to fail a log call.
func TestAWriteNeverFails(t *testing.T) {
	s := &logSink{}
	wire(t, s)
	for _, p := range []string{
		"[projects] org storage: this build links no codec\n", // a subprocess's raw stderr shares the descriptor
		"{not json at all\n",
		"{}\n",
		"",
		"\n",
	} {
		if n, err := s.Write([]byte(p)); n != len(p) || err != nil {
			t.Errorf("Write(%q) = (%d, %v), want (%d, nil)", p, n, err, len(p))
		}
	}
}

// The boot window is the most valuable stretch a process logs, and it happens
// before the transport exists. Those lines are held and flushed on attach, in
// the order they were written.
func TestTheBootWindowIsCarriedRatherThanDropped(t *testing.T) {
	s := &logSink{}
	for _, msg := range []string{"first", "second", "third"} {
		_, _ = s.Write([]byte(`{"level":"info","message":"` + msg + `"}`))
	}
	if n := holding(s); n != 3 {
		t.Fatalf("held %d lines before attach, want 3", n)
	}
	got := wire(t, s)
	if len(*got) != 3 {
		t.Fatalf("flushed %d records on attach, want 3", len(*got))
	}
	for i, want := range []string{"first", "second", "third"} {
		if body := (*got)[i].Body().AsString(); body != want {
			t.Errorf("record %d = %q, want %q — the boot window arrives in the order it happened", i, body, want)
		}
	}
	if n := holding(s); n != 0 {
		t.Errorf("still holding %d lines after the flush", n)
	}
}

// Past the bound the sink counts rather than grows. A process whose plane never
// attaches must not carry its whole log in memory.
func TestWhatIsHeldIsBounded(t *testing.T) {
	s := &logSink{}
	for i := 0; i < holdMax+50; i++ {
		_, _ = s.Write([]byte(`{"level":"info","message":"x"}`))
	}
	if n := holding(s); n != holdMax {
		t.Errorf("held %d, want the bound %d", n, holdMax)
	}
	s.mu.Lock()
	lost := s.lost
	s.mu.Unlock()
	if lost != 50 {
		t.Errorf("lost = %d, want 50 counted", lost)
	}
}

// AND BOUNDED IN BYTES, which is the bound an operator can actually spend. The
// line count says how much history is kept; only the byte ceiling says what
// keeping it costs, and the two diverge exactly when a line carries a
// serialized value rather than a sentence. Here the lines are long enough that
// the ceiling stops the hold while the count is still nowhere near its own.
func TestWhatIsHeldIsBoundedInBytes(t *testing.T) {
	s := &logSink{}
	big := []byte(`{"level":"info","message":"` + strings.Repeat("x", 64<<10) + `"}`)
	fit := holdBytes / len(big)
	for i := 0; i < fit+10; i++ {
		_, _ = s.Write(big)
	}
	if n := holding(s); n != fit {
		t.Errorf("held %d lines, want the %d that fit under the byte ceiling", n, fit)
	}
	if n := holding(s); n >= holdMax {
		t.Fatalf("held %d lines — the COUNT did the bounding, so this proves nothing about bytes", n)
	}
	s.mu.Lock()
	bytes, lost := s.bytes, s.lost
	s.mu.Unlock()
	if bytes > holdBytes {
		t.Errorf("holding %d bytes, past the %d ceiling", bytes, holdBytes)
	}
	if lost != 10 {
		t.Errorf("lost = %d, want the 10 that did not fit counted", lost)
	}
}

// A deployment with no plane closes the sink, and a closed sink neither holds
// nor emits — but still answers every write.
func TestAClosedSinkHoldsNothing(t *testing.T) {
	s := &logSink{}
	got := wire(t, s)
	s.close()
	line := `{"level":"info","message":"after"}`
	if n, err := s.Write([]byte(line)); n != len(line) || err != nil {
		t.Fatalf("Write after close = (%d, %v)", n, err)
	}
	if len(*got) != 0 || holding(s) != 0 {
		t.Errorf("closed sink emitted %d and held %d, want neither", len(*got), holding(s))
	}
}

// THE AMPLIFICATION LOOP. The sink's own voice must not arrive back at the sink:
// one line per failure is a diagnostic, a line that reproduces is an outage. So
// the default logger is wired to a sink exactly as BuildDeps wires it, the sink
// reports trouble, and the sink must not have caught it.
func TestTheSinkDoesNotFeedItself(t *testing.T) {
	s := &logSink{}
	prev := luxlog.Default()
	luxlog.SetDefault(luxlog.New("cloud").Output(luxlog.MultiLevelWriter(io.Discard, s)))
	t.Cleanup(func() { luxlog.SetDefault(prev) })

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	stderr := os.Stderr
	os.Stderr = w
	sinkLog().Warn("the sink is in trouble")
	os.Stderr = stderr
	_ = w.Close()
	out, _ := io.ReadAll(r)

	if !strings.Contains(string(out), "the sink is in trouble") {
		t.Errorf("stderr = %q, want the sink's own line — it has nowhere else to say it", out)
	}
	if n := holding(s); n != 0 {
		t.Errorf("the sink caught %d of its own lines — that is the loop", n)
	}
}

// The wire address is the LOG receiver's. luxfi/log defaults to 127.0.0.1:4317,
// which is the SPAN wire: a batch sent there meets a receiver that decodes span
// envelopes, and the leg reads as configured while delivering nothing.
func TestTheLogWireIsNotTheSpanWire(t *testing.T) {
	if planeLogEndpoint != "127.0.0.1:4318" {
		t.Errorf("planeLogEndpoint = %q, want the plane's log ear", planeLogEndpoint)
	}
	if planeLogEndpoint == localPlaneEndpoint {
		t.Error("the log wire is the span wire — two receivers, two ports")
	}
}

// The line the framework prints for a request is the shape this leg parses.
// Asking luxlog itself, rather than restating its format, is what keeps the two
// from drifting apart silently.
func TestTheParserReadsWhatTheLoggerWrites(t *testing.T) {
	s := &logSink{}
	got := wire(t, s)
	luxlog.New("cloud").Output(luxlog.MultiLevelWriter(io.Discard, s)).
		New("subsystem", "kv").Warn("store unavailable", "err", "dial tcp: refused", "attempt", 3)

	if len(*got) != 1 {
		t.Fatalf("captured %d records, want 1", len(*got))
	}
	rec := (*got)[0]
	if rec.Body().AsString() != "store unavailable" || rec.Severity() != otellog.SeverityWarn {
		t.Fatalf("record = %q/%d, want the line the logger wrote", rec.Body().AsString(), rec.Severity())
	}
	if rec.Timestamp().IsZero() {
		t.Error("timestamp is zero — the logger stamps every line and the parser must read it")
	}
	attrs := map[string]otellog.Value{}
	rec.WalkAttributes(func(kv otellog.KeyValue) bool { attrs[kv.Key] = kv.Value; return true })
	if attrs["subsystem"].AsString() != "kv" || attrs["err"].AsString() != "dial tcp: refused" || attrs["attempt"].AsInt64() != 3 {
		t.Errorf("attributes = %v, want the logger's own fields", attrs)
	}
}

// ONE LOGGER, AND ONE PLACE IT IS BUILT.
//
// A logger built with luxlog.New writes to bare stderr: it never passed through
// BuildDeps, so it carries no plane leg, and every line it writes is invisible to
// the fleet while looking perfectly logged. Six packages built their own, which
// is how a rule that reads "127 packages inherit it" quietly means 121.
//
// Three sites are allowed and each states why: the install point itself, the
// sink's own voice (breaking the amplification loop), and a test helper that has
// no composition root to inherit from.
//
// The check PARSES rather than searches, and the difference is the whole of
// whether it holds. A search matches the spelling `luxlog.New(`, and a spelling
// is a convention: the same construction under a different import alias, or
// under no alias at all, is the same logger and a different string. Parsing asks
// the question the compiler asks — which import does this identifier bind, and
// what is being called on it — so the answer does not depend on how anyone typed
// it. Redirecting a logger (Default().Output) is not construction and is not
// caught: it inherits the install point and then says where its own lines go.
//
// A file that will not parse is REPORTED, never skipped. That is the same rule
// the whole-file read was here for — one NUL byte makes a search skip a source
// file silently — and an instrument that cannot run must not read as a pass.
func TestOnlyOnePlaceBuildsALogger(t *testing.T) {
	allowed := map[string]bool{
		"logsink.go":                            true,
		"build.go":                              true,
		"internal/manifesttest/manifesttest.go": true,
	}
	skip := map[string]bool{"vendor": true, "node_modules": true, "testdata": true, ".git": true}

	var offenders []string
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if skip[d.Name()] {
				return fs.SkipDir
			}
			return nil
		}
		path = filepath.ToSlash(path)
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") || allowed[path] {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			offenders = append(offenders, path+": unreadable: "+err.Error())
			return nil
		}
		found, err := loggerBuilds(path, body)
		if err != nil {
			offenders = append(offenders, path+": unparseable, so nothing about it is known: "+err.Error())
			return nil
		}
		offenders = append(offenders, found...)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(offenders) > 0 {
		t.Errorf("these build their own logger, so their lines reach stderr and nothing else "+
			"— take luxlog.Default() instead:\n\t%s", strings.Join(offenders, "\n\t"))
	}
}

// logPath is the package the invariant is about, and builders are the calls in
// it that return a logger with a destination of its own. Default() is not one:
// it returns the logger the install point already built.
const logPath = "github.com/luxfi/log"

var builders = map[string]bool{"New": true, "NewWriter": true}

// logName is the identifier an UNALIASED import of logPath binds — the package's
// own name, asked of the toolchain rather than guessed from the path, because a
// guess is the convention this check exists to stop relying on.
var logName = sync.OnceValues(func() (string, error) {
	out, err := exec.Command("go", "list", "-f", "{{.Name}}", logPath).Output()
	return strings.TrimSpace(string(out)), err
})

// loggerBuilds reports every construction of a logger in one source file, by
// the name the file itself binds the log package to.
func loggerBuilds(path string, src []byte) ([]string, error) {
	name, err := logName()
	if err != nil {
		return nil, err
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, src, 0)
	if err != nil {
		return nil, err
	}

	local := ""
	for _, spec := range f.Imports {
		if p, err := strconv.Unquote(spec.Path.Value); err != nil || p != logPath {
			continue
		}
		switch {
		case spec.Name == nil:
			local = name
		case spec.Name.Name == "_":
			// Imported for a side effect; it binds no identifier and can
			// construct nothing.
		case spec.Name.Name == ".":
			// Every exported name lands unqualified, so a construction here is
			// spelled like anything else in the file and only the type checker
			// could tell them apart. The import is the finding.
			return []string{fmt.Sprintf("%s:%d: dot-imports %s, which puts its constructors in scope unqualified",
				path, fset.Position(spec.Pos()).Line, logPath)}, nil
		default:
			local = spec.Name.Name
		}
	}
	if local == "" {
		return nil, nil
	}

	var found []string
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || !builders[sel.Sel.Name] {
			return true
		}
		if id, ok := sel.X.(*ast.Ident); ok && id.Name == local {
			found = append(found, fmt.Sprintf("%s:%d: %s.%s(...)",
				path, fset.Position(call.Pos()).Line, local, sel.Sel.Name))
		}
		return true
	})
	return found, nil
}

// THE INSTRUMENT ITSELF, against known positives and known negatives. A guard
// that has quietly stopped matching is indistinguishable from a tree that has
// nothing to match, and the alias case is exactly the one the previous spelling
// search let through — so it is pinned here rather than assumed.
func TestTheLoggerGuardCatchesEveryAlias(t *testing.T) {
	for _, c := range []struct {
		what string
		src  string
		want int
	}{
		{"the alias this repo uses", `package p
import luxlog "github.com/luxfi/log"
func f() { _ = luxlog.New("x") }`, 1},
		{"no alias at all", `package p
import "github.com/luxfi/log"
func f() { _ = log.New("x") }`, 1},
		{"an alias nobody would guess", `package p
import zz "github.com/luxfi/log"
func f() { _ = zz.New("x") }`, 1},
		{"a writer of its own", `package p
import luxlog "github.com/luxfi/log"
import "os"
func f() { _ = luxlog.NewWriter(os.Stderr) }`, 1},
		{"unqualified through a dot import", `package p
import . "github.com/luxfi/log"
func f() { _ = New("x") }`, 1},
		{"the install point's logger, redirected", `package p
import luxlog "github.com/luxfi/log"
import "os"
func f() { _ = luxlog.Default().Output(os.Stderr).New("subsystem", "s") }`, 0},
		{"the same spelling in a comment and a string", `package p
// luxlog.New("x")
const s = "luxlog.New(\"x\")"
func f() {}`, 0},
		{"the standard library, which is a different log", `package p
import "log"
import "os"
func f() { _ = log.New(os.Stderr, "", 0) }`, 0},
	} {
		t.Run(c.what, func(t *testing.T) {
			got, err := loggerBuilds("fixture.go", []byte(c.src))
			if err != nil {
				t.Fatalf("loggerBuilds: %v", err)
			}
			if len(got) != c.want {
				t.Errorf("found %d constructions %v, want %d", len(got), got, c.want)
			}
		})
	}
}
