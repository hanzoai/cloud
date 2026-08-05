package cloud

// seam_absence_test.go — the recurrence stopper for the zero-value defect class.
//
// THE DEFECT. A cross-app capability was reached through a package global that
// another app set in ITS Mount. The pod runs ~124 SEPARATE processes, so the
// setter and the reader are almost never the same process: the read compiles,
// finds nil, and the seam answers with the ZERO VALUE — nil error, empty map,
// false. Nothing anywhere says the capability was not consulted. RegisterReserve
// died this way; OnGitPush and OnServiceRelease returned a nil error for every
// push and every release in the split fleet, and a passing test asserted that
// they should.
//
// THE RULE, and it is one line: a cross-app seam with no peer must return an
// ERROR. Not nil, not an empty result, not false.
//
// TWO TESTS, because the rule needs both a check and a way to stay checked:
//
//   - TestCrossAppSeamAbsenceIsAnError CALLS every cross-app seam with nothing
//     registered, no socket and no router, and fails any that returns nil.
//   - TestEverySeamIsClassified parses this package for `func Register*` and
//     fails if one is not in the table below. A new seam therefore cannot be
//     added without a human deciding, in writing, which kind it is — which is
//     what makes a silent seam impossible to add rather than merely unlikely.

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// seamKind is the decision a seam's author has to record.
type seamKind int

const (
	// crossApp — the registrant and the caller are DIFFERENT apps, so in the split
	// fleet they are different processes. It must reach the owner over the plane
	// and must fail loudly when the owner is not deployed. Every one of these is
	// exercised by TestCrossAppSeamAbsenceIsAnError.
	crossApp seamKind = iota
	// inProcess — the registrant and the caller are the SAME process by
	// construction, and a registration is process-local ON PURPOSE. Converting one
	// of these to an RPC would be strictly worse. The reason is not optional.
	inProcess
)

// seamClassification is the whole inventory of cloud-root Register* seams and
// what each one is. TestEverySeamIsClassified proves it is complete.
var seamClassification = map[string]struct {
	kind   seamKind
	reason string
}{
	// ---- cross-app: these cross a process boundary and go over the plane ----
	"RegisterGitImporter": {crossApp,
		"integrations decides to import (it holds the provider credential); git owns the repo store"},
	"RegisterGitMirrorController": {crossApp,
		"the sync engine declares a mirror target; the git app owns the repos that push it"},
	"RegisterIssueSink": {crossApp,
		"integrations feeds external work items; the tracker app owns the work-item store"},
	"RegisterSync": {crossApp,
		"integrations and git trigger a reconcile; the sync app owns the engine"},
	"RegisterPushBuilder": {crossApp,
		"git receives the push; platform owns the builder that turns it into a deploy"},
	"RegisterServiceReleaser": {crossApp,
		"a proven build releases; platform owns the Service CR control plane"},
	"RegisterOrgScopeResolver": {crossApp,
		"the identity boundary asks; projects owns the project registry that answers"},

	// ---- in-process: a process telling ITSELF something ----
	"RegisterLifecycleSubscriber": {inProcess,
		"a best-effort fan-out to reactors registered in the emitting process; each " +
			"process notifies its own subscribers, and a subscriber in another process " +
			"is that process's business, not a call this one should make"},
	"RegisterTraceSink": {inProcess,
		"process-local BY CONSTRUCTION and already dual-deployment: a co-resident " +
			"o11y takes the Cost-0 leg, a plugin o11y leaves this router empty and the " +
			"identical Send falls through to the ZAP wire. The absence is already routed, " +
			"not swallowed"},
	"RegisterKMSClientFactory": {inProcess,
		"a CONSTRUCTOR for the embedded client, not the capability. Absent " +
			"co-residency pickKMSClient already returns KMSPeer{}, which is the plane"},
	"RegisterCommerceClientFactory": {inProcess,
		"a CONSTRUCTOR for the embedded client, not the capability, exactly like the " +
			"KMS factory above"},
}

// absentSeamCalls is every cross-app seam, as a call. Each is invoked with
// nothing registered, no socket and no router — the state a process is in when
// the owning app is simply not deployed beside it.
//
// The signatures differ, so each entry adapts its own to `error`. A seam that
// returns a VALUE and an error returns both; the test checks the error, because
// a seam that answered honestly cannot also have produced a usable value.
var absentSeamCalls = []struct {
	seam string // the Register* it belongs to, so a failure names the seam to fix
	name string
	call func(context.Context) error
}{
	{"RegisterGitImporter", "ImportGitRepo", func(ctx context.Context) error {
		return ImportGitRepo(ctx, GitImportReq{Org: "acme", Repo: "r", CloneURL: "https://x/y.git"})
	}},
	{"RegisterGitImporter", "InboundGitSync", func(ctx context.Context) error {
		_, err := InboundGitSync(ctx, GitInboundReq{Org: "acme", Repo: "r", Ref: "refs/heads/main"})
		return err
	}},
	{"RegisterGitImporter", "GitRepoStatuses", func(ctx context.Context) error {
		_, err := GitRepoStatuses(ctx, "acme", "", []string{"r"})
		return err
	}},
	{"RegisterGitMirrorController", "EnsureGitMirror", func(ctx context.Context) error {
		return EnsureGitMirror(ctx, "acme", "p", "r", "https://x/y.git", true)
	}},
	{"RegisterIssueSink", "UpsertIssue", func(ctx context.Context) error {
		_, err := UpsertIssue(ctx, IssueUpsert{Org: "acme", ExtRef: "github:o/r#1", Title: "t"})
		return err
	}},
	{"RegisterSync", "Sync", func(ctx context.Context) error {
		_, err := Sync(ctx, SyncEvent{Kind: "git", Provider: "github", Org: "acme"})
		return err
	}},
	{"RegisterPushBuilder", "OnGitPush", func(ctx context.Context) error {
		return OnGitPush(ctx, GitPushEvent{Org: "acme", Repo: "r", Ref: "refs/heads/main"})
	}},
	{"RegisterServiceReleaser", "OnServiceRelease", func(ctx context.Context) error {
		return OnServiceRelease(ctx, ServiceReleaseEvent{Service: "cloud", Image: "ghcr.io/hanzoai/cloud:v1.0.0"})
	}},
	{"RegisterOrgScopeResolver", "ProjectOwnership", func(ctx context.Context) error {
		_, _, err := ProjectOwnership(ctx, "acme", "some-project")
		return err
	}},
}

// isolate puts the process in the state this test is about: an empty runtime
// directory (so no app's socket resolves) and no ZIP_ADDR (so nothing claims a
// router could start one). reach() then answers ErrNoPeer immediately rather
// than spending the 90s wake budget, which is also what keeps this test fast.
func isolate(t *testing.T) {
	t.Helper()
	t.Setenv("ZIP_RUNTIME_DIR", t.TempDir())
	t.Setenv("ZIP_ADDR", "")
	t.Setenv("CLOUD_RUN_DIR", "")
}

// TestCrossAppSeamAbsenceIsAnError is the check itself: with the owning app
// absent, every cross-app seam must say so.
//
// A nil here is not a cosmetic failure. It is the exact shape of the outages this
// test exists to end — a push that triggered no build, a release that patched no
// CR, an issue that reached no tracker — each reported as success to a caller
// that had no way to learn otherwise.
func TestCrossAppSeamAbsenceIsAnError(t *testing.T) {
	isolate(t)
	clearSeams(t)

	for _, c := range absentSeamCalls {
		t.Run(c.name, func(t *testing.T) {
			err := c.call(context.Background())
			if err == nil {
				t.Fatalf("%s returned nil with %s unregistered and no peer reachable.\n"+
					"That is a SILENT NO-OP: the caller is told it succeeded and the work "+
					"never happened. Absence must be an error.", c.name, c.seam)
			}
			// Absence must also be TELLABLE from a failure, or a caller cannot decide
			// whether to fall back or to alarm. ErrNoPeer is that distinction.
			t.Logf("%s → %v", c.name, err)
		})
	}
}

// TestAbsenceIsDistinguishable proves the error is not merely non-nil but
// INSPECTABLE: a caller must be able to separate "this app is not deployed here"
// from "it is deployed and the call failed". Collapsing those is how a fleet
// reads an outage as an absence and quietly serves nothing.
func TestAbsenceIsDistinguishable(t *testing.T) {
	isolate(t)
	clearSeams(t)

	for _, c := range absentSeamCalls {
		t.Run(c.name, func(t *testing.T) {
			err := c.call(context.Background())
			if err == nil {
				t.Fatalf("%s: nil (covered by TestCrossAppSeamAbsenceIsAnError)", c.name)
			}
			if !errors.Is(err, ErrNoPeer) {
				t.Fatalf("%s returned %v, which does not wrap ErrNoPeer.\n"+
					"A caller cannot tell 'not deployed here' from 'deployed and broken', "+
					"and those need opposite responses.", c.name, err)
			}
		})
	}
}

// TestEverySeamIsClassified is what keeps the rule enforced after today.
//
// It reads the package's own source for `func Register*` and fails if one is not
// in seamClassification. So a new seam cannot be added without its author
// recording whether it crosses a process boundary — and if it does, the entry is
// useless unless it also appears in absentSeamCalls, which the check below
// requires. There is no way to add a silent seam and have the suite stay green.
func TestEverySeamIsClassified(t *testing.T) {
	declared := seamsInSource(t)

	for _, name := range declared {
		c, ok := seamClassification[name]
		if !ok {
			t.Errorf("%s is not classified.\n"+
				"Add it to seamClassification: crossApp (it must reach the owner over "+
				"the plane and fail loudly when absent) or inProcess (with the reason it "+
				"is genuinely one process talking to itself).", name)
			continue
		}
		if c.kind == inProcess && strings.TrimSpace(c.reason) == "" {
			t.Errorf("%s is classified inProcess with no reason", name)
		}
	}

	// The table may not outlive the code it describes: a stale entry is a claim
	// nobody is checking any more.
	for name := range seamClassification {
		if !namesSeam(declared, name) {
			t.Errorf("seamClassification names %s, which this package no longer declares", name)
		}
	}

	// Every crossApp seam must actually be exercised by the absence test. An entry
	// that is classified and never called is a classification, not a guarantee.
	covered := map[string]bool{}
	for _, c := range absentSeamCalls {
		covered[c.seam] = true
	}
	for name, c := range seamClassification {
		if c.kind == crossApp && !covered[name] {
			t.Errorf("%s is classified crossApp but no entry in absentSeamCalls calls it.\n"+
				"Add one, or its loud-failure property is asserted nowhere.", name)
		}
	}
}

// seamsInSource parses this package for exported Register* function declarations.
// Source, not reflection: a Go test cannot enumerate a package's functions at
// runtime, and parsing is what makes the inventory self-maintaining.
func seamsInSource(t *testing.T) []string {
	t.Helper()
	entries, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	fset := token.NewFileSet()
	var out []string
	for _, path := range entries {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		for _, d := range f.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || fn.Recv != nil {
				continue
			}
			if name := fn.Name.Name; strings.HasPrefix(name, "Register") {
				out = append(out, name)
			}
		}
	}
	sort.Strings(out)
	if len(out) == 0 {
		t.Fatal("found no Register* declarations; the parse is not reading this package")
	}
	return out
}

func namesSeam(hay []string, needle string) bool {
	for _, h := range hay {
		if h == needle {
			return true
		}
	}
	return false
}

// clearSeams unregisters every cross-app seam so the tests observe a process
// that genuinely has no co-resident owner. Tests in this package run in one
// process and registration is a package global, so an earlier test's
// registration would otherwise answer this one's call.
func clearSeams(t *testing.T) {
	t.Helper()
	RegisterGitImporter(nil)
	RegisterGitMirrorController(nil)
	RegisterIssueSink(nil)
	RegisterSync(nil)
	RegisterPushBuilder(nil)
	RegisterServiceReleaser(nil)
	ResetOrgScopeResolvers()
	t.Cleanup(func() {
		RegisterGitImporter(nil)
		RegisterGitMirrorController(nil)
		RegisterIssueSink(nil)
		RegisterSync(nil)
		RegisterPushBuilder(nil)
		RegisterServiceReleaser(nil)
		ResetOrgScopeResolvers()
	})
	_ = os.Unsetenv("ZIP_ADDR")
}
