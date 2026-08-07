package functions

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// TestMain hands cek a master key before any store opens. cek resolves it ONCE
// per process and refuses to open a database without one on a build that can
// encrypt, so this is what lets the tests exercise the SAME encrypted path a
// deployment runs rather than a plaintext shortcut.
func TestMain(m *testing.M) {
	os.Setenv("CLOUD_KMS_MASTER_KEY_REF", base64.StdEncoding.EncodeToString(make([]byte, 32)))
	os.Exit(m.Run())
}

// newService builds the subsystem over a throwaway data directory. It is the
// same build() the mount runs, so the tests exercise the real schema.
func newService(t *testing.T) *cloud.Service[state] {
	t.Helper()
	base := cloud.Base{Log: luxlog.NewNoOpLogger(), DataDir: t.TempDir()}
	st, err := build(base)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	t.Cleanup(func() { _ = st.db.Close() })
	return &cloud.Service[state]{Base: base, State: st}
}

// asOrg is a context carrying a VALIDATED caller: a user the identity boundary
// minted, so the org is trustworthy.
func asOrg(org string) context.Context {
	return zip.WithCaller(context.Background(), zip.Caller{Org: org, User: "u-" + org})
}

// anon is a context with no identity at all — the local namespace.
func anon() context.Context { return context.Background() }

// httpStatus reads the status an op refused with.
func httpStatus(t *testing.T, err error) int {
	t.Helper()
	var he *zip.HTTPError
	if !errors.As(err, &he) {
		t.Fatalf("want an HTTP error, got %v", err)
	}
	return he.Status
}

// mustCreate registers a function and fails the test if it cannot.
func mustCreate(t *testing.T, s *cloud.Service[state], ctx context.Context, in CreateIn) Function {
	t.Helper()
	f, err := create(s)(ctx, &in)
	if err != nil {
		t.Fatalf("create %q: %v", in.Name, err)
	}
	return *f
}

// needBash skips a test on a host with no bash. The registry is testable
// everywhere; running code is only testable where there is something to run it.
func needBash(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash is not installed on this host")
	}
}

func TestCheckName(t *testing.T) {
	cases := []struct {
		name string
		in   string
		ok   bool
	}{
		{"simple", "hello", true},
		{"digits", "fn2", true},
		{"interior hyphens", "resize-image-v2", true},
		{"trimmed", "  hello  ", true},
		{"at the length bound", strings.Repeat("a", maxNameLen), true},
		{"empty", "", false},
		{"blank", "   ", false},
		{"over the length bound", strings.Repeat("a", maxNameLen+1), false},
		{"uppercase", "Hello", false},
		{"underscore", "hello_world", false},
		{"dot", "hello.sh", false},
		{"slash", "a/b", false},
		{"path traversal", "../etc/passwd", false},
		{"leading hyphen", "-hello", false},
		{"trailing hyphen", "hello-", false},
		{"space inside", "hello world", false},
		{"shell metacharacter", "hello;rm", false},
		{"newline", "hello\nworld", false},
		{"reserved invoke", "invoke", false},
		{"reserved invocations", "invocations", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := checkName(tc.in)
			if tc.ok {
				if err != nil {
					t.Fatalf("checkName(%q) = %v, want it accepted", tc.in, err)
				}
				if got != strings.TrimSpace(tc.in) {
					t.Fatalf("checkName(%q) = %q, want the trimmed name", tc.in, got)
				}
				return
			}
			if err == nil {
				t.Fatalf("checkName(%q) was accepted, want it refused", tc.in)
			}
			if s := httpStatus(t, err); s != 400 {
				t.Fatalf("checkName(%q) refused with %d, want 400", tc.in, s)
			}
		})
	}
}

func TestCreateRejects(t *testing.T) {
	s := newService(t)
	ctx := asOrg("acme")
	cases := []struct {
		name string
		in   CreateIn
	}{
		{"no name", CreateIn{Runtime: "bash", Code: "true"}},
		{"bad name", CreateIn{Name: "Bad Name", Runtime: "bash", Code: "true"}},
		{"reserved name", CreateIn{Name: "invoke", Runtime: "bash", Code: "true"}},
		{"no runtime", CreateIn{Name: "a", Code: "true"}},
		{"unknown runtime", CreateIn{Name: "a", Runtime: "ruby", Code: "true"}},
		{"interpreter path as runtime", CreateIn{Name: "a", Runtime: "/bin/sh", Code: "true"}},
		{"runtime with an argument", CreateIn{Name: "a", Runtime: "bash -c", Code: "true"}},
		{"shell is not on the list", CreateIn{Name: "a", Runtime: "sh", Code: "true"}},
		{"python2 is not on the list", CreateIn{Name: "a", Runtime: "python", Code: "true"}},
		{"no code", CreateIn{Name: "a", Runtime: "bash"}},
		{"blank code", CreateIn{Name: "a", Runtime: "bash", Code: "   "}},
		{"oversized code", CreateIn{Name: "a", Runtime: "bash", Code: strings.Repeat("x", maxCodeBytes+1)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := create(s)(ctx, &tc.in); err == nil {
				t.Fatal("create was accepted, want it refused")
			} else if st := httpStatus(t, err); st != 400 {
				t.Fatalf("create refused with %d, want 400", st)
			}
		})
	}
	// Nothing above may have landed in the store.
	out, err := list(s)(ctx, &ListIn{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(out.Functions) != 0 {
		t.Fatalf("registry holds %d functions after only-refused creates, want 0", len(out.Functions))
	}
}

func TestRegistryCRUD(t *testing.T) {
	s := newService(t)
	ctx := asOrg("acme")

	if out, err := list(s)(ctx, &ListIn{}); err != nil {
		t.Fatalf("list: %v", err)
	} else if len(out.Functions) != 0 {
		t.Fatalf("a fresh registry holds %d functions, want 0", len(out.Functions))
	}

	created := mustCreate(t, s, ctx, CreateIn{Name: "hello", Runtime: "bash", Code: "echo hi", TimeoutSeconds: 5})
	if created.Runtime != "bash" || created.Code != "echo hi" || created.TimeoutSeconds != 5 {
		t.Fatalf("create stored %+v", created)
	}
	mustCreate(t, s, ctx, CreateIn{Name: "another", Runtime: "python3", Code: "print(1)"})

	if out, err := list(s)(ctx, &ListIn{}); err != nil {
		t.Fatalf("list: %v", err)
	} else if len(out.Functions) != 2 || out.Functions[0].Name != "another" || out.Functions[1].Name != "hello" {
		t.Fatalf("list = %+v, want another then hello", out.Functions)
	}

	got, err := get(s)(ctx, &GetIn{Name: "hello"})
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Function.Code != "echo hi" {
		t.Fatalf("get returned code %q", got.Function.Code)
	}
	if len(got.Invocations) != 0 {
		t.Fatalf("a function that never ran has %d invocations", len(got.Invocations))
	}

	// Timeouts are clamped, not trusted: a caller cannot lend itself an hour.
	for _, tc := range []struct{ asked, want int }{
		{0, defaultTimeout}, {-5, defaultTimeout}, {1, 1}, {30, 30}, {maxTimeout + 1, maxTimeout}, {1 << 20, maxTimeout},
	} {
		f := mustCreate(t, s, ctx, CreateIn{Name: "hello", Runtime: "bash", Code: "echo hi", TimeoutSeconds: tc.asked})
		if f.TimeoutSeconds != tc.want {
			t.Fatalf("timeout %d stored as %d, want %d", tc.asked, f.TimeoutSeconds, tc.want)
		}
	}

	// Re-registering a name EDITS that function: the code changes, the birthday
	// does not. Backdate the row so the assertion means something.
	if _, err := s.State.db.ExecContext(ctx,
		`UPDATE functions SET created_at = 1000 WHERE org = ? AND name = ?`, "acme", "hello"); err != nil {
		t.Fatalf("backdate: %v", err)
	}
	replaced := mustCreate(t, s, ctx, CreateIn{Name: "hello", Runtime: "python3", Code: "print(2)"})
	if replaced.CreatedAt != 1000 {
		t.Fatalf("replace moved createdAt to %d, want it kept at 1000", replaced.CreatedAt)
	}
	if replaced.Code != "print(2)" || replaced.Runtime != "python3" {
		t.Fatalf("replace stored %+v", replaced)
	}
	if replaced.UpdatedAt < replaced.CreatedAt {
		t.Fatalf("updatedAt %d predates createdAt %d", replaced.UpdatedAt, replaced.CreatedAt)
	}

	if _, err := remove(s)(ctx, &DeleteIn{Name: "hello"}); err != nil {
		t.Fatalf("delete: %v", err)
	}

	// Everything addressing a gone function is a 404, not a 500 and not an empty 200.
	if _, err := get(s)(ctx, &GetIn{Name: "hello"}); httpStatus(t, err) != 404 {
		t.Fatalf("get after delete = %v, want 404", err)
	}
	if _, err := remove(s)(ctx, &DeleteIn{Name: "hello"}); httpStatus(t, err) != 404 {
		t.Fatalf("second delete = %v, want 404", err)
	}
	if _, err := history(s)(ctx, &InvocationsIn{Name: "hello"}); httpStatus(t, err) != 404 {
		t.Fatalf("invocations after delete = %v, want 404", err)
	}
	if _, err := invoke(s)(ctx, &InvokeIn{Name: "hello"}); httpStatus(t, err) != 404 {
		t.Fatalf("invoke after delete = %v, want 404", err)
	}
	if _, err := get(s)(ctx, &GetIn{Name: "never-registered"}); httpStatus(t, err) != 404 {
		t.Fatalf("get of an unknown name = %v, want 404", err)
	}
}

func TestTenantIsolation(t *testing.T) {
	s := newService(t)
	acme, globex, local := asOrg("acme"), asOrg("globex"), anon()

	mustCreate(t, s, acme, CreateIn{Name: "shared-name", Runtime: "bash", Code: "echo acme"})
	mustCreate(t, s, globex, CreateIn{Name: "shared-name", Runtime: "bash", Code: "echo globex"})
	mustCreate(t, s, local, CreateIn{Name: "shared-name", Runtime: "bash", Code: "echo local"})
	mustCreate(t, s, acme, CreateIn{Name: "acme-only", Runtime: "bash", Code: "echo private"})

	// The same name in three namespaces is three functions, each holding its own code.
	for _, tc := range []struct {
		name string
		ctx  context.Context
		code string
	}{
		{"validated org", acme, "echo acme"},
		{"other validated org", globex, "echo globex"},
		{"local namespace", local, "echo local"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := get(s)(tc.ctx, &GetIn{Name: "shared-name"})
			if err != nil {
				t.Fatalf("get: %v", err)
			}
			if got.Function.Code != tc.code {
				t.Fatalf("code = %q, want %q", got.Function.Code, tc.code)
			}
			out, err := list(s)(tc.ctx, &ListIn{})
			if err != nil {
				t.Fatalf("list: %v", err)
			}
			want := 1
			if tc.ctx == acme {
				want = 2
			}
			if len(out.Functions) != want {
				t.Fatalf("list returned %d functions, want %d — a neighbour leaked", len(out.Functions), want)
			}
		})
	}

	// A neighbour's function is invisible, not merely unreadable.
	for _, tc := range []struct {
		name string
		ctx  context.Context
	}{
		{"other org cannot see it", globex},
		{"unauthenticated cannot see it", local},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := get(s)(tc.ctx, &GetIn{Name: "acme-only"}); httpStatus(t, err) != 404 {
				t.Fatalf("get = %v, want 404", err)
			}
			if _, err := invoke(s)(tc.ctx, &InvokeIn{Name: "acme-only"}); httpStatus(t, err) != 404 {
				t.Fatalf("invoke = %v, want 404", err)
			}
			if _, err := history(s)(tc.ctx, &InvocationsIn{Name: "acme-only"}); httpStatus(t, err) != 404 {
				t.Fatalf("invocations = %v, want 404", err)
			}
			if _, err := remove(s)(tc.ctx, &DeleteIn{Name: "acme-only"}); httpStatus(t, err) != 404 {
				t.Fatalf("delete = %v, want 404", err)
			}
		})
	}

	// A delete in one namespace leaves the neighbours' rows alone.
	if _, err := remove(s)(globex, &DeleteIn{Name: "shared-name"}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	for _, ctx := range []context.Context{acme, local} {
		if _, err := get(s)(ctx, &GetIn{Name: "shared-name"}); err != nil {
			t.Fatalf("a neighbour's delete removed this namespace's function: %v", err)
		}
	}

	// An org header with NO validated user is the local namespace, never the org
	// it names — this is the forgery the tenant rule exists to refuse.
	forged := zip.WithCaller(context.Background(), zip.Caller{Org: "acme"})
	got, err := get(s)(forged, &GetIn{Name: "shared-name"})
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Function.Code != "echo local" {
		t.Fatalf("an unvalidated X-Org-Id read %q, want the local namespace's own row", got.Function.Code)
	}
	if _, err := get(s)(forged, &GetIn{Name: "acme-only"}); httpStatus(t, err) != 404 {
		t.Fatalf("an unvalidated X-Org-Id reached acme's private function: %v", err)
	}
}

func TestInvokeBash(t *testing.T) {
	needBash(t)
	s := newService(t)
	ctx := asOrg("acme")

	cases := []struct {
		name     string
		code     string
		input    string
		status   string
		exitCode int
		stdout   string
		stderr   string
	}{
		{
			name: "prints", code: `printf 'hello'`,
			status: statusOK, exitCode: 0, stdout: "hello",
		},
		{
			name: "reads stdin", code: "read -r line || true\nprintf 'got:%s' \"$line\"",
			input: "payload", status: statusOK, exitCode: 0, stdout: "got:payload",
		},
		{
			name: "separates the streams", code: `printf 'out'; printf 'err' >&2`,
			status: statusOK, exitCode: 0, stdout: "out", stderr: "err",
		},
		{
			name: "reports a failing exit", code: `printf 'partial'; printf 'boom' >&2; exit 3`,
			status: statusError, exitCode: 3, stdout: "partial", stderr: "boom",
		},
		{
			name: "reports a signal death", code: `kill -9 $$`,
			status: statusError, exitCode: noExit,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mustCreate(t, s, ctx, CreateIn{Name: "fn", Runtime: "bash", Code: tc.code, TimeoutSeconds: 10})
			out, err := invoke(s)(ctx, &InvokeIn{Name: "fn", Input: tc.input})
			if err != nil {
				t.Fatalf("invoke: %v", err)
			}
			if out.Status != tc.status {
				t.Fatalf("status = %q, want %q (stderr %q)", out.Status, tc.status, out.Stderr)
			}
			if out.ExitCode != tc.exitCode {
				t.Fatalf("exitCode = %d, want %d", out.ExitCode, tc.exitCode)
			}
			if out.Stdout != tc.stdout {
				t.Fatalf("stdout = %q, want %q", out.Stdout, tc.stdout)
			}
			if tc.stderr != "" && out.Stderr != tc.stderr {
				t.Fatalf("stderr = %q, want %q", out.Stderr, tc.stderr)
			}
			if out.Truncated {
				t.Fatal("a short run reported truncated output")
			}
		})
	}
}

// TestInvokeIsolatedFromTheServer proves the two properties that make running a
// caller's code survivable: it does not inherit the server's environment, and it
// does not run anywhere the server cares about.
func TestInvokeIsolatedFromTheServer(t *testing.T) {
	needBash(t)
	t.Setenv("HANZO_FUNCTIONS_TEST_SECRET", "the-kms-master-key")
	s := newService(t)
	ctx := asOrg("acme")

	code := `printf 'secret=[%s] home=[%s] pwd=[%s] path=[%s]' ` +
		`"${HANZO_FUNCTIONS_TEST_SECRET-}" "$HOME" "$PWD" "${PATH:+set}"`
	mustCreate(t, s, ctx, CreateIn{Name: "env", Runtime: "bash", Code: code})
	out, err := invoke(s)(ctx, &InvokeIn{Name: "env"})
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if !strings.Contains(out.Stdout, "secret=[]") {
		t.Fatalf("the function read the server's environment: %q", out.Stdout)
	}
	if !strings.Contains(out.Stdout, "path=[set]") {
		t.Fatalf("PATH did not reach the function: %q", out.Stdout)
	}
	if home := os.Getenv("HOME"); home != "" && strings.Contains(out.Stdout, "home=["+home+"]") {
		t.Fatalf("the function ran with the server's HOME: %q", out.Stdout)
	}
	if wd, err := os.Getwd(); err == nil && strings.Contains(out.Stdout, "pwd=["+wd+"]") {
		t.Fatalf("the function ran in the server's working directory: %q", out.Stdout)
	}
	// The run's directory dies with the run.
	if i := strings.Index(out.Stdout, "pwd=["); i >= 0 {
		dir := out.Stdout[i+len("pwd=[") : i+len("pwd=[")+strings.Index(out.Stdout[i+len("pwd=["):], "]")]
		if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("the run's directory %q outlived the run (%v)", dir, err)
		}
	}
}

func TestInvokeTimeout(t *testing.T) {
	needBash(t)
	s := newService(t)
	ctx := asOrg("acme")

	// The function backgrounds work that outlives its parent, then blocks. The
	// deadline has to reach BOTH: the interpreter, and what the interpreter left
	// running. The marker is written two seconds in, so it exists afterwards only
	// if the backgrounded process survived a deadline of one.
	marker := filepath.Join(t.TempDir(), "grandchild-was-here")
	code := fmt.Sprintf("(sleep 2; printf alive > %q) &\nprintf 'before'\nsleep 30", marker)
	mustCreate(t, s, ctx, CreateIn{Name: "slow", Runtime: "bash", Code: code, TimeoutSeconds: 1})

	out, err := invoke(s)(ctx, &InvokeIn{Name: "slow"})
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if out.Status != statusTimeout {
		t.Fatalf("status = %q, want %q", out.Status, statusTimeout)
	}
	if out.ExitCode != noExit {
		t.Fatalf("exitCode = %d, want %d for a process that was killed", out.ExitCode, noExit)
	}
	if out.Stdout != "before" {
		t.Fatalf("stdout = %q — output written before the kill was lost", out.Stdout)
	}
	// A run whose grandchild still held the output pipes could not return before
	// the deadline plus waitDelay, so this bound is also a containment check.
	if out.DurationMS < 900 || out.DurationMS > 1900 {
		t.Fatalf("a 1s timeout took %dms — the deadline is not what ended it", out.DurationMS)
	}
	time.Sleep(3 * time.Second)
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("work the function backgrounded outlived its deadline (%s: %v)", marker, err)
	}
	// A timeout is a recorded outcome, not a lost one.
	ledger, err := history(s)(ctx, &InvocationsIn{Name: "slow"})
	if err != nil {
		t.Fatalf("invocations: %v", err)
	}
	if len(ledger.Invocations) != 1 || ledger.Invocations[0].Status != statusTimeout {
		t.Fatalf("ledger = %+v, want one timeout", ledger.Invocations)
	}
}

// TestLedgerSurvivesACallerHangingUp holds the line the ledger exists for: the
// run already happened, so dropping the connection must not be a way to execute
// code on this host and leave no record of it.
func TestLedgerSurvivesACallerHangingUp(t *testing.T) {
	needBash(t)
	s := newService(t)
	live := asOrg("acme")
	mustCreate(t, s, live, CreateIn{Name: "slow", Runtime: "bash", Code: "printf 'ran'; sleep 30", TimeoutSeconds: 30})

	ctx, cancel := context.WithCancel(live)
	go func() { time.Sleep(300 * time.Millisecond); cancel() }()
	if _, err := invoke(s)(ctx, &InvokeIn{Name: "slow"}); err != nil {
		t.Fatalf("invoke: %v", err)
	}

	ledger, err := history(s)(live, &InvocationsIn{Name: "slow"})
	if err != nil {
		t.Fatalf("invocations: %v", err)
	}
	if len(ledger.Invocations) != 1 {
		t.Fatalf("the ledger holds %d entries after a cancelled request, want the run recorded", len(ledger.Invocations))
	}
	if got := ledger.Invocations[0].Stdout; got != "ran" {
		t.Fatalf("recorded stdout = %q, want what the run printed before it was cut off", got)
	}
}

func TestInvokeOutputIsCapped(t *testing.T) {
	needBash(t)
	s := newService(t)
	ctx := asOrg("acme")

	// 16 * 2^13 = 131072 bytes, twice the cap, without leaving bash.
	code := "s=0123456789abcdef\nfor ((i=0;i<13;i++)); do s=\"$s$s\"; done\nprintf '%s' \"$s\"\nprintf '%s' \"$s\" >&2"
	mustCreate(t, s, ctx, CreateIn{Name: "loud", Runtime: "bash", Code: code, TimeoutSeconds: 30})
	out, err := invoke(s)(ctx, &InvokeIn{Name: "loud"})
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if out.Status != statusOK {
		t.Fatalf("status = %q, want %q (stderr %q)", out.Status, statusOK, out.Stderr)
	}
	if len(out.Stdout) != maxOutputBytes || len(out.Stderr) != maxOutputBytes {
		t.Fatalf("kept %d stdout and %d stderr bytes, want %d of each", len(out.Stdout), len(out.Stderr), maxOutputBytes)
	}
	if !out.Truncated {
		t.Fatal("output was cut but the answer did not say so")
	}
}

func TestInvocationLedger(t *testing.T) {
	needBash(t)
	s := newService(t)
	ctx := asOrg("acme")
	mustCreate(t, s, ctx, CreateIn{Name: "counter", Runtime: "bash", Code: `read -r n || true; printf 'n=%s' "$n"`})

	for _, n := range []string{"1", "2", "3"} {
		if _, err := invoke(s)(ctx, &InvokeIn{Name: "counter", Input: n}); err != nil {
			t.Fatalf("invoke %s: %v", n, err)
		}
	}

	// Newest first, with the run's own output on the row.
	ledger, err := history(s)(ctx, &InvocationsIn{Name: "counter"})
	if err != nil {
		t.Fatalf("invocations: %v", err)
	}
	if len(ledger.Invocations) != 3 {
		t.Fatalf("ledger holds %d entries, want 3", len(ledger.Invocations))
	}
	for i, want := range []string{"n=3", "n=2", "n=1"} {
		v := ledger.Invocations[i]
		if v.Stdout != want {
			t.Fatalf("entry %d stdout = %q, want %q", i, v.Stdout, want)
		}
		if v.Name != "counter" || v.Status != statusOK || v.ExitCode != 0 {
			t.Fatalf("entry %d = %+v", i, v)
		}
		if v.CreatedAt == 0 {
			t.Fatalf("entry %d has no timestamp", i)
		}
	}
	if ledger.Invocations[0].ID <= ledger.Invocations[2].ID {
		t.Fatal("ledger ids do not rise with time")
	}

	// Limits are clamped, never trusted.
	for _, tc := range []struct{ asked, want int }{{0, 3}, {-1, 3}, {1, 1}, {2, 2}, {maxLedger + 100, 3}} {
		got, err := history(s)(ctx, &InvocationsIn{Name: "counter", Limit: tc.asked})
		if err != nil {
			t.Fatalf("invocations(limit=%d): %v", tc.asked, err)
		}
		if len(got.Invocations) != tc.want {
			t.Fatalf("limit %d returned %d entries, want %d", tc.asked, len(got.Invocations), tc.want)
		}
	}

	// get carries the recent entries, so reading a function shows what it has done.
	one, err := get(s)(ctx, &GetIn{Name: "counter"})
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(one.Invocations) != 3 {
		t.Fatalf("get returned %d invocations, want 3", len(one.Invocations))
	}

	// A neighbour's ledger is not readable, and its runs are not counted here.
	other := asOrg("globex")
	mustCreate(t, s, other, CreateIn{Name: "counter", Runtime: "bash", Code: `printf 'other'`})
	if _, err := invoke(s)(other, &InvokeIn{Name: "counter"}); err != nil {
		t.Fatalf("invoke: %v", err)
	}
	got, err := history(s)(other, &InvocationsIn{Name: "counter"})
	if err != nil {
		t.Fatalf("invocations: %v", err)
	}
	if len(got.Invocations) != 1 || got.Invocations[0].Stdout != "other" {
		t.Fatalf("a neighbour's ledger leaked: %+v", got.Invocations)
	}
	if ledger, err := history(s)(ctx, &InvocationsIn{Name: "counter"}); err != nil {
		t.Fatalf("invocations: %v", err)
	} else if len(ledger.Invocations) != 3 {
		t.Fatalf("a neighbour's run landed in this ledger: %d entries", len(ledger.Invocations))
	}

	// The ledger outlives the registration: what ran here is a fact.
	if _, err := remove(s)(ctx, &DeleteIn{Name: "counter"}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	var n int
	if err := s.State.db.QueryRow(
		`SELECT COUNT(*) FROM invocations WHERE org = ? AND name = ?`, "acme", "counter").Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 3 {
		t.Fatalf("delete left %d ledger entries, want 3", n)
	}
}

// TestRunRefusesUnknownRuntime covers the allow-list at the RUN boundary, where
// it has to hold: create's check is the first door, and this is the one that
// matters if a row ever gets past it.
func TestRunRefusesUnknownRuntime(t *testing.T) {
	for _, name := range []string{"", "sh", "ruby", "/bin/sh", "bash -c 'id'", "node;id", "BASH", "python"} {
		t.Run("runtime "+name, func(t *testing.T) {
			r := run(context.Background(), name, "id", "", time.Second)
			if r.status != statusUnavailable {
				t.Fatalf("run(%q) = %q, want %q — the allow-list let it through", name, r.status, statusUnavailable)
			}
			if r.exitCode != noExit {
				t.Fatalf("run(%q) reported exit %d, want %d", name, r.exitCode, noExit)
			}
			if r.stdout != "" {
				t.Fatalf("run(%q) produced output %q — something executed", name, r.stdout)
			}
			if r.stderr == "" {
				t.Fatal("a refusal with no reason on it")
			}
		})
	}
}

// TestInvokeUnavailableRuntime drives the same refusal through the op: a stored
// row naming a runtime this host cannot run answers 503 and is still recorded.
// The row is written straight to the store because create would never let it in
// — which is the point: the run boundary does not depend on that.
func TestInvokeUnavailableRuntime(t *testing.T) {
	s := newService(t)
	ctx := asOrg("acme")
	if _, err := s.State.db.ExecContext(ctx,
		`INSERT INTO functions (org, name, runtime, code, timeout_s, created_at, updated_at)
		      VALUES (?, ?, ?, ?, ?, ?, ?)`,
		"acme", "legacy", "ruby", "puts 1", 5, 1000, 1000); err != nil {
		t.Fatalf("seed: %v", err)
	}

	_, err := invoke(s)(ctx, &InvokeIn{Name: "legacy"})
	if err == nil {
		t.Fatal("invoke ran a function whose runtime is not on the allow-list")
	}
	if st := httpStatus(t, err); st != 503 {
		t.Fatalf("invoke refused with %d, want 503", st)
	}
	if msg := err.Error(); !strings.Contains(msg, "ruby") {
		t.Fatalf("the 503 does not name the missing runtime: %q", msg)
	}

	// The registry keeps working, and the refusal is on the record.
	if _, err := get(s)(ctx, &GetIn{Name: "legacy"}); err != nil {
		t.Fatalf("get after an unavailable invoke: %v", err)
	}
	ledger, err := history(s)(ctx, &InvocationsIn{Name: "legacy"})
	if err != nil {
		t.Fatalf("invocations: %v", err)
	}
	if len(ledger.Invocations) != 1 {
		t.Fatalf("ledger holds %d entries, want the refused attempt recorded", len(ledger.Invocations))
	}
	if v := ledger.Invocations[0]; v.Status != statusUnavailable || v.ExitCode != noExit || !strings.Contains(v.Stderr, "ruby") {
		t.Fatalf("ledger entry = %+v", v)
	}
}

func TestAllowListIsExactlyThree(t *testing.T) {
	want := []string{"bash", "node", "python3"}
	got := names()
	if len(got) != len(want) {
		t.Fatalf("allow-list = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("allow-list = %v, want %v", got, want)
		}
	}
	// A runtime maps to a BARE binary name, never a path and never arguments:
	// exec.LookPath resolves it, so a separator here would sidestep PATH entirely.
	for name, rt := range runtimes {
		if strings.ContainsAny(rt.bin, "/\\ \t") || rt.bin == "" {
			t.Fatalf("runtime %q maps to %q, which is not a bare binary name", name, rt.bin)
		}
	}
}

func TestInputBounds(t *testing.T) {
	s := newService(t)
	ctx := asOrg("acme")
	mustCreate(t, s, ctx, CreateIn{Name: "fn", Runtime: "bash", Code: "true"})
	if _, err := invoke(s)(ctx, &InvokeIn{Name: "fn", Input: strings.Repeat("x", maxInputBytes+1)}); err == nil {
		t.Fatal("an oversized input was accepted")
	} else if st := httpStatus(t, err); st != 400 {
		t.Fatalf("oversized input refused with %d, want 400", st)
	}
}

func TestShutdownIsIdempotent(t *testing.T) {
	if err := Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown with nothing mounted: %v", err)
	}
	s := newService(t)
	mounted = s
	if err := Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	if err := Shutdown(context.Background()); err != nil {
		t.Fatalf("second shutdown: %v", err)
	}
	if mounted != nil {
		t.Fatal("shutdown left the service behind")
	}
}
