package platform

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud"
	luxlog "github.com/luxfi/log"
	"gopkg.in/yaml.v3"
)

// ── the path decides the fence, so the path is derived ───────────────────────

// The values DIRECTORY selects the AppProject: an org's own fence admits ONE
// namespace and no cluster scope, the platform fence admits every namespace and
// ClusterRole/ClusterRoleBinding. A caller that could name its own directory
// could name its own fence, so this is the single most load-bearing derivation
// on the surface — and with the prefix gone, RESERVATION is the whole of what
// keeps a customer out of the platform's namespace family.
func TestOrgIsDerivedAndReservedNamesAreSuperOnly(t *testing.T) {
	for _, tc := range []struct {
		name, own, asked string
		super            bool
		want, wantErr    string
	}{
		{name: "an org is its name", own: "acme", want: "acme"},
		{name: "a super acting normally is still its own org", own: "acme", super: true, want: "acme"},
		{name: "acting as another org needs sudo", own: "acme", asked: "other", wantErr: "requires SuperAdmin"},
		{name: "a super may act as another org", own: "admin", asked: "acme", super: true, want: "acme"},
		{name: "a super may act as the platform", own: "admin", asked: "hanzo", super: true, want: "hanzo"},
		{name: "naming your own org is not acting as another", own: "acme", asked: "acme", want: "acme"},

		// RESERVATION — the control the `tenant-` prefix used to provide, and the
		// reason dropping the prefix is safe. namespace.Sanitize is the identity on
		// a clean label, so without these an IAM org named `kube-system` would
		// resolve to the real `kube-system`.
		{name: "a brand is reserved", own: "hanzo", wantErr: "platform's own"},
		{name: "a brand environment is reserved", own: "hanzo-testnet", wantErr: "platform's own"},
		{name: "the delivery plane is reserved", own: "hanzo-cd", wantErr: "platform's own"},
		{name: "kubernetes' own is reserved", own: "kube-system", wantErr: "platform's own"},
		{name: "so is the whole kube- family", own: "kube-anything", wantErr: "platform's own"},
		{name: "default is reserved", own: "default", wantErr: "platform's own"},
		{name: "the admin org is reserved", own: "admin", wantErr: "platform's own"},
		{name: "a platform service directory is reserved", own: "zen", wantErr: "platform's own"},
		{name: "cert-manager is reserved", own: "cert-manager", wantErr: "platform's own"},
		{name: "a lux brand env is reserved", own: "lux-mainnet", wantErr: "platform's own"},
		{name: "a reserved org is not reachable by acting-as", own: "acme", asked: "kube-system", wantErr: "requires SuperAdmin"},
		{name: "a super may reach a reserved org", own: "admin", asked: "kube-system", super: true, want: "kube-system"},

		{name: "no org resolves to no name", own: "", wantErr: "does not resolve"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveOrg(tc.own, tc.asked, tc.super)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("want an error containing %q, got %q / %v", tc.wantErr, got, err)
				}
				if got != "" {
					t.Fatalf("a refused derivation must yield NO directory, got %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("org = %q, want %q", got, tc.want)
			}
		})
	}
}

// The fence the API reports and the fence the ApplicationSet applies must be the
// same string, or the record lies about what a sync may reach.
func TestProjectMatchesTheApplicationSetDerivation(t *testing.T) {
	for ns, want := range map[string]string{
		"acme":          "acme",
		"widgets-co":    "widgets-co",
		"hanzo":         "hanzo-platform",
		"hanzo-testnet": "hanzo-platform",
		"hanzo-cd":      "hanzo-platform",
		"kube-system":   "hanzo-platform",
		"zen":           "hanzo-platform",
		"admin":         "hanzo-platform",
	} {
		if got := declareProject(ns); got != want {
			t.Errorf("declareProject(%q) = %q, want %q", ns, got, want)
		}
	}
}

// An org's image repository must be recoverable from the ref and distinct per
// org: two orgs must never derive one repository, or one org's declaration pulls
// another org's build.
func TestRepositoryIsPerOrgAndInjective(t *testing.T) {
	s := testDeclareService()
	a := declareRepository(s, "acme", "web")
	b := declareRepository(s, "acme-web", "x")
	if a == b {
		t.Fatalf("two distinct orgs derived ONE repository: %q", a)
	}
	if a != defaultBuildImagePrefix+"/acme/web" {
		t.Errorf("repository = %q, want <prefix>/<org>/<app>", a)
	}
	// The '/' is what makes the pair recoverable: neither an org nor an app name
	// may contain one, so no two (org, app) pairs can render the same ref. This
	// is what the `tenant-` prefix was NOT doing — the separator was always the
	// real control.
	if strings.Count(strings.TrimPrefix(a, defaultBuildImagePrefix+"/"), "/") != 1 {
		t.Errorf("the org/app boundary is not a single separator: %q", a)
	}
	if p := declareRepository(s, "hanzo", "papers"); p != defaultBuildImagePrefix+"/hanzo/papers" {
		t.Errorf("every org derives the same way, the platform included: %q", p)
	}
}

// ── the name is a Kubernetes object name ─────────────────────────────────────

func TestNameRefusesAnythingThatIsNotADNSLabel(t *testing.T) {
	for _, bad := range []string{
		"../../../etc/passwd", "..", "a/b", "web_app", "-web", "web-",
		"", ".", "a b", "web.yaml", strings.Repeat("a", 41),
	} {
		if _, err := declareName(declareReq{Name: bad}); err == nil {
			t.Errorf("declareName(%q) was accepted; it becomes a file path AND a k8s object name", bad)
		}
	}
	got, err := declareName(declareReq{Repo: "https://git.hanzo.ai/hanzo/Papers.git"})
	if err != nil || got != "papers" {
		t.Fatalf("a name defaults to the repository basename, lowercased: %q / %v", got, err)
	}
	// Case is NORMALISED, not refused: "Web" and "web" are one app, and the
	// label grammar still runs over the normalised form.
	if got, err := declareName(declareReq{Name: "Web"}); err != nil || got != "web" {
		t.Fatalf("declareName(\"Web\") = %q / %v, want the normalised label", got, err)
	}
}

// A path is built from the derived namespace and the validated name only, so it
// can never leave the inventory directory however either is spelled.
func TestPathStaysInsideTheInventory(t *testing.T) {
	p := declarePath("acme", "web")
	if p != "charts/app/values/acme/web.yaml" {
		t.Fatalf("path = %q", p)
	}
	if !strings.HasPrefix(filepath.ToSlash(filepath.Clean(p)), declarePrefix+"/") {
		t.Fatalf("%q escapes %q", p, declarePrefix)
	}
}

// ── a host is served only by whoever owns it ─────────────────────────────────

func TestHostIsConfinedToTheOrgSubtreeUnlessSuper(t *testing.T) {
	s := testDeclareService()
	s.State.sitesHost = "hanzo.app"

	if got, err := declareHost(s, "acme", "web", "", false); err != nil || got != "web.acme.hanzo.app" {
		t.Fatalf("the default host is the org's own subtree: %q / %v", got, err)
	}
	if _, err := declareHost(s, "acme", "web", "api.acme.hanzo.app", false); err != nil {
		t.Fatalf("a host inside the org subtree is allowed: %v", err)
	}
	for _, foreign := range []string{"other.hanzo.app", "web.other.hanzo.app", "hanzo.app"} {
		if _, err := declareHost(s, "acme", "web", foreign, false); err == nil {
			t.Errorf("an org admin claimed %q, which is not in its subtree", foreign)
		}
	}
	if _, err := declareHost(s, "acme", "web", "api.hanzo.ai", false); err == nil {
		t.Error("an org admin claimed a custom domain without the claim-and-verify flow")
	}
	if got, err := declareHost(s, "hanzo", "papers", "papers.hanzo.ai", true); err != nil || got != "papers.hanzo.ai" {
		t.Fatalf("a SuperAdmin may name the platform's own host: %q / %v", got, err)
	}
	for _, bad := range []string{"a b.com", "-x.com", "x..com", "localhost", "http://x.com", "x.com/../y"} {
		if _, err := declareHost(s, "acme", "web", bad, true); err == nil {
			t.Errorf("declareHost accepted %q as a hostname", bad)
		}
	}
}

// A tag reaches a declaration the cluster pulls. The release path (a tag with no
// build) does not pass through validateImageRef, so the grammar is enforced here.
func TestTagGrammar(t *testing.T) {
	for _, ok := range []string{"v1.2.3", "bld_AbC-1.2", "latest", "_x"} {
		if !isTag(ok) {
			t.Errorf("isTag(%q) = false", ok)
		}
	}
	for _, bad := range []string{"", "-x", ".x", "a b", "a,b", "a=b", "a/b", "a:b", "a@b", "a\nb", strings.Repeat("a", 129)} {
		if isTag(bad) {
			t.Errorf("isTag(%q) = true", bad)
		}
	}
}

// ── the rendered file is what the chart accepts ──────────────────────────────

// Everything render emits must be a key charts/app/values.schema.json declares.
// The schema sets additionalProperties:false, so an invented key does not get
// ignored — it fails the Helm render and the Application never syncs.
func TestRenderedKeysAreAllInTheChartSchema(t *testing.T) {
	// The chart's own top-level property set, as of charts/app/values.schema.json.
	allowed := map[string]bool{}
	for _, k := range []string{
		"affinity", "annotations", "args", "autoscaling", "cd", "chart", "claims",
		"command", "component", "configMaps", "containerName", "cronJobs",
		"defaultReadinessProbe", "e2e", "enableServiceLinks", "env", "envFrom",
		"fsGroup", "global", "image", "imagePullSecrets", "ingress",
		"initContainers", "kms", "kmsSecrets", "labels", "lifecycle",
		"livenessProbe", "middlewares", "minReadySeconds", "nameOverride",
		"networkPolicy", "nodeSelector", "partOf", "pdb", "persistence",
		"persistentVolumeClaimRetentionPolicy", "podAnnotations", "podLabels",
		"podSecurityContext", "ports", "priorityClassName", "rbac",
		"readinessProbe", "replicas", "resources", "rollingUpdate", "routes",
		"securityContext", "selectorLabels", "service", "serviceAccountName",
		"serviceAliases", "serviceMonitor", "sidecars", "startupProbe", "strategy",
		"surgeColocation", "terminationGracePeriodSeconds", "tolerations",
		"topologySpread", "volumeClaimTemplates", "volumeMounts", "volumes",
		"workload",
	} {
		allowed[k] = true
	}
	var top map[string]any
	if err := yaml.Unmarshal(testSpec().render(), &top); err != nil {
		t.Fatalf("the rendered declaration is not valid YAML: %v", err)
	}
	for k := range top {
		if !allowed[k] {
			t.Errorf("render emits %q, which charts/app rejects (additionalProperties: false)", k)
		}
	}
	// The four the generator and the chart actually act on.
	if top["cd"] == nil || top["image"] == nil || top["ingress"] == nil || top["ports"] == nil {
		t.Fatalf("a declaration must carry cd, image, ingress and ports; got %v", mapKeys(top))
	}
}

// The record this API returns must be what the file says — including the two
// union shapes ingress.hosts takes, since typing it as []string silently drops
// every map-shaped host and those are the wildcard ones.
func TestReadDeclarationRoundTripsAndFlattensBothHostShapes(t *testing.T) {
	root := t.TempDir()
	spec := testSpec()
	writeDecl(t, root, spec.Org, spec.Name, string(spec.render()))

	d, err := readDeclaration(root, spec.Org, spec.Name)
	if err != nil {
		t.Fatalf("readDeclaration: %v", err)
	}
	if d.Repository != spec.Repository || d.Tag != spec.Tag {
		t.Errorf("image did not round-trip: %+v", d)
	}
	if len(d.Hosts) != 1 || d.Hosts[0] != spec.Hosts[0] {
		t.Errorf("hosts did not round-trip: %v", d.Hosts)
	}
	if !d.Automated {
		t.Error("cd.automated did not round-trip")
	}
	if d.Application != spec.Org+"-"+spec.Name {
		t.Errorf("Application = %q; it is the ApplicationSet's <dir>-<file> join key", d.Application)
	}
	if d.Project != declareProject(spec.Org) {
		t.Errorf("Project = %q", d.Project)
	}

	// The map shape, as hanzo-app-sites.yaml uses it.
	writeDecl(t, root, "hanzo", "sites", `chart: app
ingress:
  enabled: true
  hosts:
  - host: '*.hanzo.app'
    paths:
    - path: /
      pathType: Prefix
cd:
  automated: true
`)
	m, err := readDeclaration(root, "hanzo", "sites")
	if err != nil {
		t.Fatalf("readDeclaration (map hosts): %v", err)
	}
	if len(m.Hosts) != 1 || m.Hosts[0] != "*.hanzo.app" {
		t.Fatalf("a map-shaped host was dropped: %v", m.Hosts)
	}
}

// An unreadable declaration must be an ERROR, never an empty row: an inventory
// that renders a broken file as "no image, no hosts" is the same lie as an
// unreachable cluster rendering as an empty fleet.
func TestUnreadableDeclarationIsAnErrorNotAnEmptyRow(t *testing.T) {
	root := t.TempDir()
	writeDecl(t, root, "acme", "web", "image:\n\ttag: [unbalanced\n")
	if _, err := readDeclaration(root, "acme", "web"); err == nil {
		t.Fatal("a values file that does not parse was reported as a declaration")
	}
	if _, err := readDeclaration(root, "acme", "absent"); err == nil {
		t.Fatal("an absent file was reported as a declaration")
	}
}

// A value is quoted, always. Unquoted, YAML parses `1.10` as a float and
// `0x53141` as an integer, and the chart would then render a number where the
// caller meant a string.
func TestEnvValuesAreQuoted(t *testing.T) {
	spec := testSpec()
	spec.Env = []declareEnv{{Name: "V", Value: "1.10"}, {Name: "Q", Value: "it's"}, {Name: "Y", Value: "yes"}}
	var doc struct {
		Env []struct {
			Name  string `yaml:"name"`
			Value string `yaml:"value"`
		} `yaml:"env"`
	}
	if err := yaml.Unmarshal(spec.render(), &doc); err != nil {
		t.Fatalf("render: %v", err)
	}
	want := map[string]string{"V": "1.10", "Q": "it's", "Y": "yes"}
	if len(doc.Env) != len(want) {
		t.Fatalf("env = %+v", doc.Env)
	}
	for _, e := range doc.Env {
		if want[e.Name] != e.Value {
			t.Errorf("env %s = %q, want %q", e.Name, e.Value, want[e.Name])
		}
	}
}

// ── an update moves ONE scalar ───────────────────────────────────────────────

// The declaration is hand-maintainable. An update that would need to change more
// than the tag is refused and names the file, rather than rewriting a human's
// edits out of existence from a request body.
func TestUpdateRefusesToRewriteADeclaration(t *testing.T) {
	root := t.TempDir()
	spec := testSpec()
	writeDecl(t, root, spec.Org, spec.Name, string(spec.render()))

	other := spec
	other.Hosts = []string{"elsewhere.acme.hanzo.app"}
	if err := checkDeclared(root, other); err == nil || !strings.Contains(err.Error(), "refusing to rewrite") {
		t.Errorf("a host change on an existing declaration must be refused, got %v", err)
	}
	withEnv := spec
	withEnv.Env = []declareEnv{{Name: "NEW", Value: "1"}}
	if err := checkDeclared(root, withEnv); err == nil || !strings.Contains(err.Error(), "refusing to rewrite") {
		t.Errorf("an env change on an existing declaration must be refused, got %v", err)
	}
	same := spec
	if err := checkDeclared(root, same); err != nil {
		t.Errorf("an unchanged spec must be accepted: %v", err)
	}
}

// ── the write, end to end, against a real git remote ─────────────────────────

// The default mode pushes a BRANCH. The generator reads main, so this must leave
// main untouched — that is the whole safety property of the default.
func TestDeclareToABranchLeavesMainUntouched(t *testing.T) {
	remote, bare := gitRemote(t, pinFixture)
	defer swapUniverseRemote(remote)()
	s := serviceWithKMS(kmsWithPinToken(t))

	spec := testSpec()
	res, err := declare(s, context.Background(), spec, modeBranch)
	if err != nil {
		t.Fatalf("declare: %v", err)
	}
	if !res.Created || !res.Changed {
		t.Fatalf("a first declaration is created and changed: %+v", res)
	}
	if res.Live {
		t.Fatal("a branch declaration reported itself LIVE; the generator reads main")
	}
	branch := declareBranch(spec.Org, spec.Name, spec.Tag)
	if res.Ref != branch {
		t.Fatalf("ref = %q, want %q", res.Ref, branch)
	}
	if res.Review == "" || !strings.Contains(res.Review, branch) {
		t.Errorf("a branch write must return where to open the review, got %q", res.Review)
	}

	got := mustGit(t, bare, "show", branch+":"+declarePath(spec.Org, spec.Name))
	if !strings.Contains(got, "repository: "+spec.Repository) || !strings.Contains(got, "tag: "+spec.Tag) {
		t.Fatalf("the branch does not carry the declaration:\n%s", got)
	}
	// main must not have it.
	if _, err := gitTry(bare, "show", universeBranch+":"+declarePath(spec.Org, spec.Name)); err == nil {
		t.Fatal("the declaration reached main; a branch write must deploy NOTHING")
	}
}

// A commit to main proves the image is pullable first: a declaration naming an
// image the registry cannot serve is an ImagePullBackOff with no rollback path.
func TestCommitRefusesAnImageTheRegistryDoesNotHave(t *testing.T) {
	remote, bare := gitRemote(t, pinFixture)
	defer swapUniverseRemote(remote)()
	reg := fakeRegistry(t, map[string]bool{})
	defer swapRegistryBase(reg.URL)()

	spec := testSpec()
	if _, err := declare(serviceWithKMS(kmsWithPinToken(t)), context.Background(), spec, modeCommit); err == nil {
		t.Fatal("a commit to main was allowed for an image the registry does not have")
	}
	if _, err := gitTry(bare, "show", universeBranch+":"+declarePath(spec.Org, spec.Name)); err == nil {
		t.Fatal("the refused declaration was written to main anyway")
	}
}

// A commit to main with a pullable image lands, and lands ONLY the one file.
func TestCommitWritesMainWhenTheImageIsReal(t *testing.T) {
	remote, bare := gitRemote(t, pinFixture)
	defer swapUniverseRemote(remote)()
	spec := testSpec()
	reg := fakeRegistry(t, map[string]bool{spec.Tag: true})
	defer swapRegistryBase(reg.URL)()

	res, err := declare(serviceWithKMS(kmsWithPinToken(t)), context.Background(), spec, modeCommit)
	if err != nil {
		t.Fatalf("declare: %v", err)
	}
	if !res.Live || res.Ref != universeBranch {
		t.Fatalf("a commit must report itself live on main: %+v", res)
	}
	got := mustGit(t, bare, "show", universeBranch+":"+declarePath(spec.Org, spec.Name))
	if !strings.Contains(got, "tag: "+spec.Tag) {
		t.Fatalf("main does not carry the declaration:\n%s", got)
	}
	// The seeded inventory must be intact — a declaration adds a file, it never
	// rewrites the fleet.
	if seeded := mustGit(t, bare, "show", universeBranch+":charts/app/values/hanzo/cloud.yaml"); seeded != pinFixture {
		t.Errorf("declaring an app changed another service's declaration:\n%s", seeded)
	}
}

// An UPDATE moves the tag scalar where it sits and touches nothing else — the
// same rule pin.go enforces, because these are the same files.
func TestUpdateMovesOnlyTheTagAndKeepsTheComments(t *testing.T) {
	remote, bare := gitRemote(t, pinFixture)
	defer swapUniverseRemote(remote)()
	spec := testSpec()
	reg := fakeRegistryFunc(t, func(string) bool { return true })
	defer swapRegistryBase(reg.URL)()
	s := serviceWithKMS(kmsWithPinToken(t))

	if _, err := declare(s, context.Background(), spec, modeCommit); err != nil {
		t.Fatalf("first declare: %v", err)
	}
	before := mustGit(t, bare, "show", universeBranch+":"+declarePath(spec.Org, spec.Name))

	next := spec
	next.Tag = "bld_second"
	// A caller re-sending the SAME hosts must not be treated as a rewrite.
	res, err := declare(s, context.Background(), next, modeCommit)
	if err != nil {
		t.Fatalf("second declare: %v", err)
	}
	if res.Created {
		t.Error("the second declare reported Created on an existing declaration")
	}
	after := mustGit(t, bare, "show", universeBranch+":"+declarePath(spec.Org, spec.Name))
	if after != strings.Replace(before, "tag: "+spec.Tag, "tag: "+next.Tag, 1) {
		t.Fatalf("an update changed more than the tag scalar:\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

// A re-declare of the SAME build is a success with nothing to do — never an
// empty commit, and never a claim that something moved.
func TestRedeclaringTheSameBuildIsANoOp(t *testing.T) {
	remote, bare := gitRemote(t, pinFixture)
	defer swapUniverseRemote(remote)()
	spec := testSpec()
	reg := fakeRegistryFunc(t, func(string) bool { return true })
	defer swapRegistryBase(reg.URL)()
	s := serviceWithKMS(kmsWithPinToken(t))

	if _, err := declare(s, context.Background(), spec, modeCommit); err != nil {
		t.Fatalf("first declare: %v", err)
	}
	head := strings.TrimSpace(mustGit(t, bare, "rev-parse", universeBranch))
	res, err := declare(s, context.Background(), spec, modeCommit)
	if err != nil {
		t.Fatalf("second declare: %v", err)
	}
	if res.Changed {
		t.Error("a re-declare of the same build reported a change")
	}
	if now := strings.TrimSpace(mustGit(t, bare, "rev-parse", universeBranch)); now != head {
		t.Errorf("a re-declare of the same build made a commit: %s -> %s", head, now)
	}
}

// One app's declaration may never be pointed at another app's image.
func TestUpdateRefusesAForeignRepository(t *testing.T) {
	remote, _ := gitRemote(t, pinFixture)
	defer swapUniverseRemote(remote)()
	reg := fakeRegistryFunc(t, func(string) bool { return true })
	defer swapRegistryBase(reg.URL)()
	s := serviceWithKMS(kmsWithPinToken(t))

	spec := testSpec()
	if _, err := declare(s, context.Background(), spec, modeCommit); err != nil {
		t.Fatalf("first declare: %v", err)
	}
	foreign := spec
	foreign.Repository = defaultBuildImagePrefix + "/other/web"
	if _, err := declare(s, context.Background(), foreign, modeCommit); err == nil ||
		!strings.Contains(err.Error(), "refusing to point one app's declaration at another's image") {
		t.Fatalf("want the foreign-repository refusal, got %v", err)
	}
}

// Without the universe credential nothing is attempted and the error names the
// ref, never a value. Fail-closed: an anonymous push would fail deep in the git
// seam with a worse message.
func TestDeclareFailsClosedWithNoCredential(t *testing.T) {
	s := &cloud.Service[state]{Base: cloud.Base{Log: luxlog.New("test")}}
	_, err := declare(s, context.Background(), testSpec(), modeBranch)
	if err == nil || !strings.Contains(err.Error(), "universe credential") {
		t.Fatalf("want a credential refusal, got %v", err)
	}
	if strings.Contains(err.Error(), "sk-") {
		t.Fatal("the error leaked a credential value")
	}
}

// The inventory read is confined to ONE directory: a name can never reach a
// sibling namespace's files however it is spelled.
func TestDeclaredNamesStayInOneDirectory(t *testing.T) {
	root := t.TempDir()
	writeDecl(t, root, "acme", "web", "image:\n  repository: r\n  tag: t\n")
	writeDecl(t, root, "other", "secret", "image:\n  repository: r\n  tag: t\n")

	names, err := declaredNames(root, "acme")
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 1 || names[0] != "web" {
		t.Fatalf("the inventory crossed a tenant boundary: %v", names)
	}
	for _, escape := range []string{"acme/../other", "*", ".."} {
		got, _ := declaredNames(root, escape)
		for _, n := range got {
			if n == "secret" {
				t.Fatalf("namespace %q reached another tenant's declaration", escape)
			}
		}
	}
}

// ── harness ──────────────────────────────────────────────────────────────────

func testDeclareService() *cloud.Service[state] {
	return &cloud.Service[state]{
		Base:  cloud.Base{Log: luxlog.New("test"), Brand: "hanzo"},
		State: state{k8s: &k8sClient{imagePrefix: defaultBuildImagePrefix}, sitesHost: "hanzo.app"},
	}
}

func testSpec() declareSpec {
	return declareSpec{
		Name:       "web",
		Org:        "acme",
		Repository: defaultBuildImagePrefix + "/acme/web",
		Tag:        "bld_first",
		Hosts:      []string{"web.acme.hanzo.app"},
		Port:       declarePort,
		Replicas:   1,
		Automated:  true,
		Origin:     "https://git.hanzo.ai/acme/web",
	}
}

func writeDecl(t *testing.T, root, ns, name, body string) {
	t.Helper()
	dir := filepath.Join(root, filepath.FromSlash(declarePrefix), ns)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name+".yaml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// gitTry runs git and returns the error rather than failing the test — for the
// assertions that a ref does NOT exist.
func gitTry(dir string, args ...string) (string, error) {
	return runGit(context.Background(), dir, pinGitEnv(""), args...)
}

func mapKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// A retry of the same deploy must SUCCEED. Any client retries, and the first cut
// of this seam did not: it based every branch write on main, so a second call a
// second later committed the same tree at a different timestamp — a different
// sha, a non-fast-forward push, and a raw git hint the caller could not act on.
//
// The sleep is load-bearing and is the point of the test. Without it both
// commits land in the SAME second, git mints the identical sha, the push is
// "everything up-to-date" and the bug is invisible — which is exactly how it
// passed the first time it was written.
func TestRetryingTheSameBranchDeploySucceeds(t *testing.T) {
	remote, bare := gitRemote(t, pinFixture)
	defer swapUniverseRemote(remote)()
	s := serviceWithKMS(kmsWithPinToken(t))
	spec := testSpec()

	first, err := declare(s, context.Background(), spec, modeBranch)
	if err != nil {
		t.Fatalf("first declare: %v", err)
	}
	time.Sleep(1100 * time.Millisecond)

	again, err := declare(s, context.Background(), spec, modeBranch)
	if err != nil {
		t.Fatalf("a retry of the same deploy failed: %v", err)
	}
	if again.Changed {
		t.Error("a retry of an identical declaration reported a change")
	}
	if again.Ref != first.Ref || again.Review == "" {
		t.Errorf("a retry must report the same branch and review: %+v", again)
	}
	if again.Live {
		t.Error("a retry reported itself live; a branch deploys nothing")
	}
	// And the branch still holds exactly one declaration commit.
	n := strings.Count(mustGit(t, bare, "rev-list", "--count", first.Ref), "")
	if n == 0 {
		t.Fatal("the branch vanished")
	}
	if got := strings.TrimSpace(mustGit(t, bare, "rev-list", "--count", universeBranch+".."+first.Ref)); got != "1" {
		t.Errorf("the retry stacked a second commit on the branch: %s commits ahead of main", got)
	}
}

// A re-declare that repeats the SAME environment is the same declaration, not a
// rewrite. Refusing it would make every retry of a create-with-env fail.
func TestRepeatingTheSameEnvIsNotARewrite(t *testing.T) {
	root := t.TempDir()
	spec := testSpec()
	spec.Env = []declareEnv{{Name: "A", Value: "1"}, {Name: "B", Value: "2"}}
	writeDecl(t, root, spec.Org, spec.Name, string(spec.render()))

	reordered := spec
	reordered.Env = []declareEnv{{Name: "B", Value: "2"}, {Name: "A", Value: "1"}}
	if err := checkDeclared(root, reordered); err != nil {
		t.Errorf("the same environment in another order was treated as a rewrite: %v", err)
	}
	changed := spec
	changed.Env = []declareEnv{{Name: "A", Value: "9"}}
	if err := checkDeclared(root, changed); err == nil {
		t.Error("a genuinely different environment was accepted as an update")
	}
}

// ── the fence is verified against the repository that enforces it ────────────

// A commit to main is REFUSED while the ApplicationSet would fence the
// declaration wider than this API reports. The template is the thing that
// actually decides, and it lives in another repository — so it is read out of
// the clone on the write path rather than trusted.
//
// This is the mutation proof of checkFence: the same declaration that succeeds
// against the reservation-based template must FAIL against the prefix-based one,
// and the failure must name the fence that would really apply.
func TestCommitRefusesAStaleFence(t *testing.T) {
	stale := strings.Replace(fleetSetFixture,
		`{{ if has .path.basename (list "hanzo" "lux" "zoo" "admin" "default" "hanzo-cd" "hanzo-build") }}hanzo-platform{{ else }}{{ .path.basename }}{{ end }}`,
		`{{ if hasPrefix "tenant-" .path.basename }}{{ .path.basename }}{{ else }}hanzo-platform{{ end }}`, 1)
	if stale == fleetSetFixture {
		t.Fatal("the mutation did not apply; the fixture's fence rule changed shape")
	}

	remote, bare := gitRemote(t, pinFixture)
	defer swapUniverseRemote(remote)()
	// Rewrite the remote's template to the stale form, on main.
	work := t.TempDir()
	mustGit(t, "", "clone", "-q", bare, work)
	set := filepath.Join(work, "infra", "k8s", "hanzo-cd", "applicationset-fleet.yaml")
	if err := os.WriteFile(set, []byte(stale), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGit(t, work, "add", "-A")
	mustGit(t, work, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-qm", "stale fence")
	mustGit(t, work, "push", "-q", "origin", "HEAD:"+universeBranch)

	reg := fakeRegistryFunc(t, func(string) bool { return true })
	defer swapRegistryBase(reg.URL)()
	s := serviceWithKMS(kmsWithPinToken(t))
	spec := testSpec()

	_, err := declare(s, context.Background(), spec, modeCommit)
	if err == nil {
		t.Fatal("a commit to main was allowed while the ApplicationSet would fence the org under the platform project")
	}
	if !strings.Contains(err.Error(), platformProject) {
		t.Errorf("the refusal must name the fence that would actually apply: %v", err)
	}
	if _, err := gitTry(bare, "show", universeBranch+":"+declarePath(spec.Org, spec.Name)); err == nil {
		t.Fatal("the refused declaration was written to main anyway")
	}

	// A BRANCH write is unaffected: nothing is generated from a branch, and its
	// pull request is exactly where a human sees the mismatch.
	if _, err := declare(s, context.Background(), spec, modeBranch); err != nil {
		t.Errorf("a branch declaration was blocked by a fence that cannot apply to it: %v", err)
	}

	// And the platform's own directory is never blocked — it is fenced the same
	// way under either template, so a release must still be able to move.
	reserved := spec
	reserved.Org, reserved.Repository = "hanzo", defaultBuildImagePrefix+"/hanzo/web"
	if _, err := declare(s, context.Background(), reserved, modeCommit); err != nil {
		t.Errorf("a reserved directory was blocked by the stale-fence refusal: %v", err)
	}
}
