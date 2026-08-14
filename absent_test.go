package cloud

// absent_test.go — what happens when the app you are calling is not there.
//
// THE BUG. One app reached another through a package global that the other app
// set in its Mount. Every plugin runs one app, so the writer and the reader are
// different processes and the global is always nil. The call compiled, found
// nil, and returned the zero value: nil error, empty map, false. Nothing said
// the work had not happened. OnGitPush and OnServiceRelease returned nil for
// every push and every release in the fleet, and a passing test said they should.
//
// THE RULE. Calling an app that is not there must return an ERROR. Not nil, not
// an empty result, not false.
//
// Two tests: one checks the rule, one keeps it checked.

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/internal/planetest"
)

// Where a capability lives.
const (
	// remote — the app that registers it and the app that calls it are different,
	// so they are different processes. It has to go over the plane and has to fail
	// loudly when the owner is not deployed. Every one of these is called by
	// TestAbsentErrors.
	remote = iota
	// local — one process telling itself something. A registration here is
	// process-local on purpose, and turning it into an RPC would be worse. The
	// reason is not optional.
	local
)

// kinds is every Register* in this package and which of the two it is.
// TestAllListed proves the list is complete.
var kinds = map[string]struct {
	where int
	why   string
}{
	"RegisterGitImporter":         {remote, "integrations decides to import; git holds the repos"},
	"RegisterGitMirrorController": {remote, "sync declares the mirror; git holds the repo that pushes it"},
	"RegisterIssueSink":           {remote, "integrations feeds the items; tracker holds the store"},
	"RegisterSync":                {remote, "integrations and git trigger; sync holds the engine"},
	"RegisterPushBuilder":         {remote, "git takes the push; platform holds the builder"},
	"RegisterServiceReleaser":     {remote, "a build releases; platform holds the CR control plane"},
	"RegisterOrgScopeResolver":    {remote, "the identity check asks; projects holds the registry"},

	"RegisterLifecycleSubscriber": {local,
		"best-effort fan-out to reactors registered in the emitting process. A " +
			"reactor in another process is that process's business, not a call this " +
			"one should make"},
	"RegisterTraceSink": {local,
		"process-local by construction and already routed both ways: a co-resident " +
			"o11y takes the cost-0 leg, a plugin o11y leaves this router empty and the " +
			"same Send falls through to the ZAP wire. Absence is routed, not swallowed"},
	"RegisterKMSClientFactory": {local,
		"builds the embedded client; it is not the capability. Without it " +
			"pickKMSClient already returns KMSPeer{}, which is the plane"},
	"RegisterCommerceClientFactory": {local,
		"builds the embedded client, exactly like the KMS factory above"},
}

// probes is every remote capability, as a call. Each runs with nothing
// registered, no socket and no router — what a process sees when the app it
// wants is simply not deployed beside it.
//
// The signatures differ, so each adapts its own to error. One that returns a
// value and an error returns both; the test reads the error, since a call that
// answered honestly cannot also have produced a usable value.
var probes = []struct {
	from string // the Register* it belongs to, so a failure names what to fix
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
		_, err := OnGitPush(ctx, GitPushEvent{Org: "acme", Repo: "r", Ref: "refs/heads/main"})
		return err
	}},
	{"RegisterServiceReleaser", "OnServiceRelease", func(ctx context.Context) error {
		return OnServiceRelease(ctx, ServiceReleaseEvent{Service: "cloud", Image: "ghcr.io/hanzoai/cloud:v1.0.0"})
	}},
	{"RegisterOrgScopeResolver", "ProjectOwnership", func(ctx context.Context) error {
		_, _, err := ProjectOwnership(ctx, "acme", "some-project")
		return err
	}},
}

// isolate gives the process an empty runtime directory, so no app's socket
// resolves, and no ZIP_ADDR, so nothing claims a router could start one. reach()
// then answers ErrNoPeer at once instead of spending the 90s wake budget, which
// is also what keeps this fast.
func isolate(t *testing.T) {
	t.Helper()
	t.Setenv("ZIP_RUNTIME_DIR", planetest.Dir(t))
	t.Setenv("ZIP_ADDR", "")
	t.Setenv("CLOUD_RUN_DIR", "")
}

// TestAbsentErrors: with the owning app gone, every call must say so.
//
// A nil here is not cosmetic. It is the shape of the outages this exists to end
// — a push that built nothing, a release that patched nothing, an issue that
// reached no tracker — each reported as success to a caller with no way to learn
// otherwise.
func TestAbsentErrors(t *testing.T) {
	isolate(t)
	unregister(t)

	for _, c := range probes {
		t.Run(c.name, func(t *testing.T) {
			err := c.call(context.Background())
			if err == nil {
				t.Fatalf("%s returned nil with %s unregistered and no peer.\n"+
					"That is a silent no-op: the caller is told it worked and the work "+
					"never happened.", c.name, c.from)
			}
			t.Logf("%s → %v", c.name, err)
		})
	}
}

// TestAbsentIsNoPeer: the error must also be readable. A caller has to tell "not
// deployed here" from "deployed and broken" — those need opposite responses, and
// a fleet that cannot tell them apart has shipped that mistake both ways.
func TestAbsentIsNoPeer(t *testing.T) {
	isolate(t)
	unregister(t)

	for _, c := range probes {
		t.Run(c.name, func(t *testing.T) {
			err := c.call(context.Background())
			if err == nil {
				t.Fatalf("%s: nil (see TestAbsentErrors)", c.name)
			}
			if !errors.Is(err, ErrNoPeer) {
				t.Fatalf("%s returned %v, which does not wrap ErrNoPeer.\n"+
					"A caller cannot tell 'not deployed here' from 'here and failing'.", c.name, err)
			}
		})
	}
}

// TestAllListed is what keeps this enforced after today.
//
// It reads the package's own source for func Register* and fails if one is
// missing from kinds. A new one cannot be added without its author writing down
// whether it crosses a process boundary; if it does, it must also appear in
// probes, which is checked below. Adding a silent one now means deleting a test
// that says not to.
func TestAllListed(t *testing.T) {
	found := registers(t)

	for _, name := range found {
		k, ok := kinds[name]
		if !ok {
			t.Errorf("%s is not listed.\n"+
				"Add it to kinds: remote (goes over the plane, fails loudly when the "+
				"owner is absent) or local (with the reason it is one process talking "+
				"to itself).", name)
			continue
		}
		if k.where == local && strings.TrimSpace(k.why) == "" {
			t.Errorf("%s is listed local with no reason", name)
		}
	}

	// The list may not outlive the code: a stale entry is a claim nobody checks.
	for name := range kinds {
		if !slices.Contains(found, name) {
			t.Errorf("kinds names %s, which this package no longer declares", name)
		}
	}

	// Every remote one must actually be called by the tests above. One that is
	// listed and never called is a claim, not a guarantee.
	called := map[string]bool{}
	for _, c := range probes {
		called[c.from] = true
	}
	for name, k := range kinds {
		if k.where == remote && !called[name] {
			t.Errorf("%s is listed remote but nothing in probes exercises it.\n"+
				"Add one, or its loud-failure property is asserted nowhere.", name)
		}
	}
}

// registers parses this package for exported Register* declarations. Source, not
// reflection: Go cannot enumerate a package's functions at runtime, and parsing
// is what makes the list keep itself honest.
func registers(t *testing.T) []string {
	t.Helper()
	paths, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	fset := token.NewFileSet()
	var out []string
	for _, path := range paths {
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

// unregister clears every remote registration so the tests see a process with no
// co-resident owner. This package's tests share one process and registration is
// a package global, so an earlier test's registration would answer this one.
func unregister(t *testing.T) {
	t.Helper()
	drop := func() {
		RegisterGitImporter(nil)
		RegisterGitMirrorController(nil)
		RegisterIssueSink(nil)
		RegisterSync(nil)
		RegisterPushBuilder(nil)
		RegisterServiceReleaser(nil)
		ResetOrgScopeResolvers()
	}
	drop()
	t.Cleanup(drop)
	_ = os.Unsetenv("ZIP_ADDR")
}
