package platform

// project.go — the platform's projects: every declared app, grouped by the
// project its values file names.
//
// ── the model ───────────────────────────────────────────────────────────────
//
// A project is the value of `partOf` in a declaration. The chart already carries
// the key (charts/app/values.schema.json: "The product this belongs to.") and
// stamps it on every object it renders as app.kubernetes.io/part-of, so the fleet
// was grouped by product before this file existed; this file reads that grouping
// rather than inventing a second one. There is no project table and no project
// file: a project exists while a declaration names it, its members are exactly
// the declarations that name it, and the values directory stays the one
// inventory. An app belongs to exactly one project because a YAML key holds one
// value; universe's CI refuses a declaration that names none.
//
// A project belongs to whoever owns its apps' directories: an org's projects are
// the partOf values under charts/app/values/<org>/, and the platform's own are
// those under the platform's directories (owner, declare.go). The platform's
// projects span namespaces — insights runs in hanzo, adnexus in adnexus — which
// is why a project is keyed by (owner, name) and never by a directory.
//
// ── what a project shows ────────────────────────────────────────────────────
//
// Four facts per app, from four owners, never blended: DECLARED (the values file,
// merged with its cluster overlay as CD renders it), RECONCILED (the CD
// Application — sync and health), RUNNING (the pods carrying the app's selector)
// and LATEST (the newest semver tag the registry publishes). Drift is the
// difference between them, stated as flags (drift.go's kinds).
//
// ── how it changes ──────────────────────────────────────────────────────────
//
// Every write is a commit to universe through writeUniverse — the same clone,
// credential, fence check and race handling declare uses. Creating a project
// names it on the apps it starts with; renaming rewrites the key on every
// member; deleting one folds its members into another, because an app cannot be
// left in none. Each edit moves the one `partOf` scalar where it is declared and
// touches nothing else in a hand-maintained file. The default mode pushes a
// review branch and deploys nothing; `commit` writes main. A partOf change is a
// label change on the app's pods, so the sync that applies it rolls them.

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/hanzoai/cloud/k8s"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/zap-proto/zip"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// observeDeadline bounds the registry reads a board makes, so a slow registry
// costs a column rather than the page.
const observeDeadline = 8 * time.Second

// ── the value ────────────────────────────────────────────────────────────────

// Project is one platform project: the declarations that name it.
type Project struct {
	// Name is the project, as the values files spell it in `partOf`.
	Name string `json:"name"`
	// Org is the organization that owns the project. Absent for the platform's own
	// projects, whose apps live in the platform's directories.
	Org string `json:"org,omitempty"`
	// Apps is how many declarations name the project.
	Apps int `json:"apps"`
	// Namespaces are the namespaces its apps deploy into, sorted.
	Namespaces []string `json:"namespaces"`
	// Sync counts its apps by CD's sync verdict.
	Sync syncTally `json:"sync"`
	// Health counts its apps by CD's health verdict.
	Health healthTally `json:"health"`
	// Drift counts its apps by drift severity.
	Drift driftTally `json:"drift"`
}

// syncTally counts apps by CD sync verdict.
type syncTally struct {
	// Synced is how many run what git declares.
	Synced int `json:"synced"`
	// OutOfSync is how many differ from git until someone syncs them.
	OutOfSync int `json:"outOfSync"`
	// Unknown is how many CD holds no verdict for, or could not be read.
	Unknown int `json:"unknown"`
}

// healthTally counts apps by CD health verdict.
type healthTally struct {
	// Healthy is how many CD reports Healthy.
	Healthy int `json:"healthy"`
	// Progressing is how many are still rolling out.
	Progressing int `json:"progressing"`
	// Degraded is how many CD reports Degraded.
	Degraded int `json:"degraded"`
	// Missing is how many have no live objects at all.
	Missing int `json:"missing"`
	// Unknown is every other verdict, and apps CD could not be asked about.
	Unknown int `json:"unknown"`
}

// ProjectApp is one app of a project: its declaration and what became of it.
type ProjectApp struct {
	Declaration
	// Image is the image the declaration names: repository@digest when it pins a
	// digest, else repository:tag. Empty for a declaration that owns no workload.
	Image string `json:"image,omitempty"`
	// Running is what the cluster runs for it; null when no pod carries its
	// selector or the cluster could not be read.
	Running *Running `json:"running"`
	// Latest is the newest semver tag the registry publishes for its repository,
	// "" when that cannot be read.
	Latest string `json:"latest,omitempty"`
	// Drift is declared versus reconciled versus running versus latest, as flags.
	Drift Verdict `json:"drift"`
	// CD is the Application reconciling it; null when CD has none for this file.
	CD *CDApp `json:"cd"`
}

// declRef names one declaration: its directory and its file.
type declRef struct {
	// Org is the values directory the declaration lives in.
	Org string `json:"org"`
	// Name is the declaration's name, the file's basename.
	Name string `json:"name"`
}

// projectBoard is every project a caller may see.
type projectBoard struct {
	// Projects are sorted by owner, then name.
	Projects []Project `json:"projects"`
	// Unassigned are declarations that name no project. Every declaration names
	// one, so a non-empty list is drift in git itself.
	Unassigned []declRef `json:"unassigned"`
	// Unreadable are the values files in the caller's scope that do not parse, so
	// their projects are unknown. Absent when every file parsed.
	Unreadable []unreadableFile `json:"unreadable,omitempty"`
	// CD says why the reconciliation columns are missing, when they are.
	CD *unreadable `json:"cdUnavailable,omitempty"`
	// Cluster says why the running columns are missing, when they are.
	Cluster *unreadable `json:"clusterUnavailable,omitempty"`
}

// projectView is one project with its apps.
type projectView struct {
	Project
	// Members are the project's apps, sorted by namespace then name.
	Members []ProjectApp `json:"members"`
	// Unreadable are the values files in the owner's scope that do not parse, any
	// of which may name this project. Absent when every file parsed.
	Unreadable []unreadableFile `json:"unreadable,omitempty"`
	// CD says why the reconciliation columns are missing, when they are.
	CD *unreadable `json:"cdUnavailable,omitempty"`
	// Cluster says why the running columns are missing, when they are.
	Cluster *unreadable `json:"clusterUnavailable,omitempty"`
}

// ── scope ────────────────────────────────────────────────────────────────────

// projectScope resolves whose projects a request is about: the owner key every
// directory is compared against, "" for the platform's own. It is resolveOrg —
// the act-as and reservation rules the delivery board already applies — read one
// level up: a SuperAdmin whose home org is a brand acts in the platform scope, an
// org admin in its own, and naming another is an act-as only a SuperAdmin has.
func projectScope(c *zip.Ctx, asked string) (string, error) {
	org, ok := principal.Org(c)
	if !ok {
		return "", principal.Refused(c)
	}
	dir, err := resolveOrg(org, asked, principal.Super(c))
	if err != nil {
		return "", err
	}
	return owner(dir), nil
}

// inScope reports whether a directory's apps belong to scope.
func inScope(dir, scope string) bool { return owner(dir) == scope }

// ── observe ──────────────────────────────────────────────────────────────────

// observed is one read of all four planes for a set of declarations.
type observed struct {
	cd      map[string]*CDApp // by values path
	cdErr   error
	pods    map[string][]unstructured.Unstructured
	podsErr error
	latest  map[string]string // by repository
}

// observe reads CD, the pods and the registry for decls, each best-effort: a
// plane that cannot be read leaves its columns empty and says why, and never
// blanks the declarations.
func (d delivery) observe(ctx context.Context, p fleetPrincipal, decls []Declaration) observed {
	var o observed
	apps, err := cdApps(d.fs, ctx, p)
	if err != nil {
		o.cdErr = err
	} else {
		o.cd = make(map[string]*CDApp, len(apps))
		for i := range apps {
			o.cd[apps[i].Path] = &apps[i]
		}
	}
	nss := map[string]bool{}
	repos := make([]string, 0, len(decls))
	for _, dec := range decls {
		nss[dec.Org] = true
		repos = append(repos, dec.Repository)
	}
	o.pods, o.podsErr = podsIn(d.fs, ctx, sortedKeys(nss))
	rctx, cancel := context.WithTimeout(ctx, observeDeadline)
	defer cancel()
	o.latest = latestOf(rctx, repos)
	return o
}

// unreadable says why CD and the cluster could not be read, as the caller may
// read it (planeFault).
func (o observed) unreadable(c *zip.Ctx, s *cloud.Service[state]) (cd, cluster *unreadable) {
	return planeFault(c, s, "Hanzo CD", o.cdErr), planeFault(c, s, "the cluster", o.podsErr)
}

// member joins one declaration to what the planes observed of it.
func (o observed) member(dec Declaration) ProjectApp {
	m := ProjectApp{Declaration: dec, Image: pullRef(dec), CD: o.cd[dec.Path]}
	if o.podsErr == nil && dec.Repository != "" {
		m.Running = runningOf(dec, o.pods[dec.Org])
	}
	m.Latest = o.latest[dec.Repository]
	m.Drift = appDrift(m, o.podsErr == nil)
	return m
}

// pullRef is the reference a declaration pulls: a digest wins over a tag,
// exactly as the chart's app.image renders it.
func pullRef(d Declaration) string {
	switch {
	case d.Repository == "":
		return ""
	case d.Digest != "":
		return d.Repository + "@" + d.Digest
	case d.Tag != "":
		return d.Repository + ":" + d.Tag
	}
	return d.Repository
}

// appDrift states how one app differs from its declaration: CD out of sync, the
// pods running another image, no pods where the declaration asks for some, and a
// newer release than the one declared. clusterRead says whether the pods were
// read at all — an unreadable cluster is an unknown, never "missing".
func appDrift(m ProjectApp, clusterRead bool) Verdict {
	var flags []DriftFlag
	if m.CD != nil && m.CD.Sync == "OutOfSync" {
		flags = append(flags, flag(DriftOutOfSync, "CD reports "+m.CD.Name+" OutOfSync with git"))
	}
	if clusterRead && m.Repository != "" {
		switch r := m.Running; {
		case r == nil && m.wantsPods():
			flags = append(flags, flag(DriftMissing, "no pod runs "+m.Name+" and the declaration asks for some"))
		case r != nil && !m.runs(r):
			flags = append(flags, flag(DriftUnrolled, "pods run "+r.Image+", the declaration names "+m.Image))
		}
	}
	// The tag names the release whether or not a digest pins its bytes, so a
	// digest pin is judged stale by its tag like any other.
	if cur, ok := parseSemver(m.Tag); ok {
		if next, ok := parseSemver(m.Latest); ok && cur.less(next) {
			flags = append(flags, flag(DriftStale, "declared "+m.Tag+" is behind latest "+m.Latest))
		}
	}
	if flags == nil {
		flags = []DriftFlag{}
	}
	return Verdict{Severity: DriftSeverityOf(flags), Flags: flags}
}

// wantsPods reports whether the declaration asks for running pods: a replica
// count of zero is a deliberate stop, and an absent one is the chart's default
// of one.
func (m ProjectApp) wantsPods() bool { return m.Replicas != 0 || !m.replicasSet }

// runs reports whether pods running r satisfy the declaration: the digest when it
// pins one (and the kubelet reported one), else the tag.
func (m ProjectApp) runs(r *Running) bool {
	if m.Digest != "" && r.Digest != "" {
		return r.Digest == m.Digest
	}
	if m.Digest != "" {
		return strings.HasSuffix(r.Image, "@"+m.Digest)
	}
	return tagFromImageRef(r.Image) == m.Tag
}

// tally folds members into a project's counts.
func tally(p *Project, members []ProjectApp) {
	nss := map[string]bool{}
	for _, m := range members {
		nss[m.Org] = true
		switch {
		case m.CD == nil:
			p.Sync.Unknown++
			p.Health.Unknown++
		default:
			switch m.CD.Sync {
			case "Synced":
				p.Sync.Synced++
			case "OutOfSync":
				p.Sync.OutOfSync++
			default:
				p.Sync.Unknown++
			}
			switch m.CD.Health {
			case "Healthy":
				p.Health.Healthy++
			case "Progressing":
				p.Health.Progressing++
			case "Degraded":
				p.Health.Degraded++
			case "Missing":
				p.Health.Missing++
			default:
				p.Health.Unknown++
			}
		}
		switch m.Drift.Severity {
		case SeverityRed:
			p.Drift.Red++
		case SeverityYellow:
			p.Drift.Yellow++
		default:
			p.Drift.OK++
		}
	}
	p.Apps = len(members)
	p.Namespaces = sortedKeys(nss)
}

// projectsQuery narrows the project board to one owner.
type projectsQuery struct {
	// Org names whose projects to read. Omitted, a SuperAdmin reads every owner's
	// and anyone else reads their own; naming another org is an act-as only a
	// SuperAdmin has, and naming a platform directory reads the platform's own.
	Org string `json:"org"`
}

// projectAddr addresses one project.
type projectAddr struct {
	// Project is the project's name, from the path.
	Project string `json:"project"`
	// Org names the project's owner, defaulting to the caller's own scope — which
	// for a SuperAdmin whose home is a brand org is the platform's.
	Org string `json:"org"`
}

// listProjects answers every project the caller may see, with how its apps
// stand.
//
// A project is the `partOf` its apps' values files name, so this is the
// declaration inventory grouped by that key, joined per app with CD's
// reconciliation, the pods the cluster runs and the newest release the registry
// publishes, and folded into counts: apps, namespaces, sync, health and drift.
// Declarations that name no project are listed separately, because each one is
// drift in git.
//
// A SuperAdmin sees every owner's projects, the platform's own among them; an
// org admin sees only its own org's. `org` narrows a SuperAdmin to one owner.
// A plane that cannot be read leaves its counts at unknown and says why in
// cdUnavailable or clusterUnavailable; it never empties the board. A values file
// that does not parse is listed in unreadable and costs only its own row.
func (d delivery) listProjects(ctx context.Context, in *projectsQuery) (*projectBoard, error) {
	c, err := admit(ctx, cloud.Admin)
	if err != nil {
		return nil, err
	}
	all := principal.Super(c) && strings.TrimSpace(in.Org) == ""
	scope, err := projectScope(c, in.Org)
	if err != nil {
		return nil, err
	}
	inv, err := fleetInventory(ctx)
	if err != nil {
		return nil, inventoryFault(c, d.s, err)
	}
	keep := func(dir string) bool { return all || inScope(dir, scope) }
	var decls []Declaration
	for dir, ds := range inv.dirs {
		if keep(dir) {
			decls = append(decls, ds...)
		}
	}
	o := d.observe(ctx, requestPrincipal(c), decls)
	type key struct{ org, name string }
	members := map[key][]ProjectApp{}
	out := projectBoard{Projects: []Project{}, Unassigned: []declRef{},
		Unreadable: inv.unreadableIn(keep, principal.Super(c))}
	for _, dec := range decls {
		if dec.PartOf == "" {
			out.Unassigned = append(out.Unassigned, declRef{Org: dec.Org, Name: dec.Name})
			continue
		}
		k := key{owner(dec.Org), dec.PartOf}
		members[k] = append(members[k], o.member(dec))
	}
	for k, ms := range members {
		p := Project{Name: k.name, Org: k.org}
		tally(&p, ms)
		out.Projects = append(out.Projects, p)
	}
	sort.Slice(out.Projects, func(i, j int) bool {
		if out.Projects[i].Org != out.Projects[j].Org {
			return out.Projects[i].Org < out.Projects[j].Org
		}
		return out.Projects[i].Name < out.Projects[j].Name
	})
	sort.Slice(out.Unassigned, func(i, j int) bool {
		return out.Unassigned[i].Org+"/"+out.Unassigned[i].Name < out.Unassigned[j].Org+"/"+out.Unassigned[j].Name
	})
	out.CD, out.Cluster = o.unreadable(c, d.s)
	return &out, nil
}

// getProject answers one project and every app in it.
//
// Each app carries its declaration — image, hosts, replicas, the KMS paths it
// reads (never a value) — together with the CD Application reconciling it, what
// its pods run, the newest release its registry publishes, and the drift between
// those four. 404 when no declaration in the caller's scope names the project.
func (d delivery) getProject(ctx context.Context, in *projectAddr) (*projectView, error) {
	c, err := admit(ctx, cloud.Admin)
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(in.Project)
	if !appNameRE.MatchString(name) {
		return nil, zip.ErrBadRequest("project must be a DNS-1123 label")
	}
	scope, err := projectScope(c, in.Org)
	if err != nil {
		return nil, err
	}
	inv, err := fleetInventory(ctx)
	if err != nil {
		return nil, inventoryFault(c, d.s, err)
	}
	decls := projectMembers(inv.dirs, scope, name)
	if len(decls) == 0 {
		return nil, zip.ErrNotFound("no declaration names project " + name)
	}
	o := d.observe(ctx, requestPrincipal(c), decls)
	v := projectView{Project: Project{Name: name, Org: scope}, Members: make([]ProjectApp, 0, len(decls)),
		Unreadable: inv.unreadableIn(func(dir string) bool { return inScope(dir, scope) }, principal.Super(c))}
	for _, dec := range decls {
		v.Members = append(v.Members, o.member(dec))
	}
	tally(&v.Project, v.Members)
	v.CD, v.Cluster = o.unreadable(c, d.s)
	return &v, nil
}

// projectMembers is every declaration in scope that names project, sorted by
// directory then name.
func projectMembers(dirs map[string][]Declaration, scope, project string) []Declaration {
	var out []Declaration
	for dir, ds := range dirs {
		if !inScope(dir, scope) {
			continue
		}
		for _, dec := range ds {
			if dec.PartOf == project {
				out = append(out, dec)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Org != out[j].Org {
			return out[i].Org < out[j].Org
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// inventoryFault answers a read of the inventory that failed whole: no
// credential, no clone, or no ApplicationSet. The cause is logged and a
// SuperAdmin reads it; anyone else reads that the declarations could not be
// read, because the cause names the platform's own repository and variables.
func inventoryFault(c *zip.Ctx, s *cloud.Service[state], err error) error {
	s.Log.Error("inventory read failed", "err", err)
	if principal.Super(c) {
		return zip.Errorf(universeStatus(err), "could not read the declarations: %v", err)
	}
	return zip.Errorf(universeStatus(err), "could not read the declarations; the cause is in the platform log")
}

// universeStatus is what a failed read or write of universe answers: 503 when
// its token could not be minted, which is the platform's configuration and an
// owner's to fix, and 502 for a repository that refused or could not be reached.
func universeStatus(err error) int {
	var ng *noGrant
	if errors.As(err, &ng) {
		return http.StatusServiceUnavailable
	}
	return http.StatusBadGateway
}

// planeFault says why a plane could not be read, nil when it was. The cause is
// logged and a SuperAdmin reads it; anyone else reads which plane, because the
// cause names the platform's cluster, namespaces and service account.
func planeFault(c *zip.Ctx, s *cloud.Service[state], plane string, err error) *unreadable {
	if err == nil {
		return nil
	}
	s.Log.Warn(plane+" could not be read", "err", err)
	if principal.Super(c) {
		return &unreadable{Reason: err.Error()}
	}
	return &unreadable{Reason: plane + " could not be read; the cause is in the platform log"}
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ── change ───────────────────────────────────────────────────────────────────

// relabel is one move of apps between projects: each edit sets one file's partOf.
type relabel struct {
	Org  string `json:"org"`
	Name string `json:"name"`
	From string `json:"from"`
	To   string `json:"to"`
}

// projectWrite is what a project change did.
type projectWrite struct {
	// Project is the project the change leaves the moved apps in.
	Project string `json:"project"`
	// Moved are the declarations whose partOf moved, and from what.
	Moved []relabel `json:"moved"`
	// Mode is branch (a review; nothing deploys) or commit (main).
	Mode declareMode `json:"mode"`
	// Ref is the branch the change was pushed to, main for a commit.
	Ref string `json:"ref"`
	// Commit is the sha the change created, "" when nothing had to move.
	Commit string `json:"commit,omitempty"`
	// Review is where to open the pull request for a branch.
	Review string `json:"review,omitempty"`
	// Live says whether the generator will see it: true only on main.
	Live bool `json:"live"`
}

// plan decides, from a fresh checkout's inventory, which files move. It runs on
// the checkout the write lands on, so a member added since the caller last read
// the board moves with the rest.
type plan func(inv inventoryRead) ([]relabel, error)

// moveApps lands one project change: every edit plan names, as one commit.
//
// A review branch is named for the owner, the project, and the change as it lands
// on main's tip — deploy/projects/<owner>/<project>/<sha12 of tip and message> —
// and always starts from that tip. Two owners' changes to one project name are two
// branches, a change planned after main moved is a new branch planned on the new
// tip, and a retry of the same change on the same tip is the same commit.
func moveApps(s *cloud.Service[state], ctx context.Context, mode declareMode, scope, label, project string, p plan) (projectWrite, error) {
	out := projectWrite{Project: project, Mode: mode}
	w := universeWrite{mode: mode, target: universeBranch}
	if mode == modeBranch {
		w.target = ""
		w.review = func(tip, message string) string {
			sum := sha256.Sum256([]byte(tip + "\n" + message))
			return declareBranchPrefix + "/projects/" + cmp.Or(scope, "platform") + "/" + project + "/" + hex.EncodeToString(sum[:])[:12]
		}
	}
	w.apply = func(root string) ([]string, string, error) {
		inv, err := readInventory(root)
		if err != nil {
			return nil, "", err
		}
		edits, err := p(inv)
		if err != nil {
			return nil, "", err
		}
		out.Moved = edits
		var paths []string
		seen := map[string]bool{}
		for _, e := range edits {
			if mode == modeCommit && !seen[e.Org] {
				if err := checkFence(root, e.Org); err != nil {
					return nil, "", err
				}
			}
			seen[e.Org] = true
			rel, changed, err := setPartOf(root, e.Org, e.Name, e.To)
			if err != nil {
				return nil, "", err
			}
			if changed {
				paths = append(paths, rel)
			}
		}
		if len(paths) == 0 {
			return nil, "", nil
		}
		var b strings.Builder
		fmt.Fprintf(&b, "%s\n\n", label)
		for _, e := range edits {
			fmt.Fprintf(&b, "%s/%s: partOf %s -> %s\n", e.Org, e.Name, cmpDash(e.From), e.To)
		}
		b.WriteString("\npartOf names the platform project a declaration belongs to.\nChanged through platform.hanzo.ai.")
		return paths, b.String(), nil
	}
	at, err := writeUniverse(s, ctx, w)
	if err != nil {
		return projectWrite{Project: project, Mode: mode}, err
	}
	out.Ref, out.Commit, out.Review, out.Live = at.Ref, at.Commit, at.Review, at.Live
	if out.Moved == nil {
		out.Moved = []relabel{}
	}
	return out, nil
}

func cmpDash(s string) string {
	if s == "" {
		return "(none)"
	}
	return s
}

// setPartOf moves one declaration's partOf to project, in the file that decides
// it: the cluster overlay when the overlay sets the key, else the values file.
// The one line moves and every other byte stays; a file that names no project
// gains the key before its first top-level key, below its header comment and any
// document marker.
//
// It proves the move two ways before the edit is kept: Helm, which reads YAML
// 1.1, reads the key as exactly this string, and the merged declaration reads
// exactly as it did before but for partOf.
func setPartOf(root, org, name, project string) (string, bool, error) {
	if err := checkLoc(org, name); err != nil {
		return "", false, err
	}
	before, err := readDeclaration(root, org, name)
	if err != nil {
		return "", false, err
	}
	rel := declarePath(org, name)
	if over := overlayPath(org, name); hasTopKey(filepath.Join(root, filepath.FromSlash(over)), "partOf") {
		rel = over
	}
	abs := filepath.Join(root, filepath.FromSlash(rel))
	changed, err := setTopScalar(abs, "partOf", project)
	if err != nil {
		return "", false, fmt.Errorf("%s: %w", rel, err)
	}
	b, err := os.ReadFile(abs)
	if err != nil {
		return "", false, fmt.Errorf("%s: %w", rel, err)
	}
	if got, ok := helmString(b, "partOf"); !ok || got != project {
		return "", false, fmt.Errorf("wrote %s but Helm does not read its partOf as %q", rel, project)
	}
	after, err := readDeclaration(root, org, name)
	if err != nil {
		return "", false, err
	}
	want := before
	want.PartOf = project
	if !reflect.DeepEqual(after, want) {
		return "", false, fmt.Errorf("wrote %s but it no longer reads as the same declaration: only partOf may move", rel)
	}
	return rel, changed, nil
}

// hasTopKey reports whether a YAML file sets key at the top level.
func hasTopKey(path, key string) bool {
	b, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	return topKeyLine(strings.Split(string(b), "\n"), key) >= 0
}

func topKeyLine(lines []string, key string) int {
	for i, ln := range lines {
		if strings.HasPrefix(ln, key+":") {
			return i
		}
	}
	return -1
}

// setTopScalar sets a top-level string where it sits, or inserts it above the
// file's first top-level key when absent — below any header comment, directive
// and document-start marker. The value is written quoted, a trailing comment on
// the line is kept, and a line Helm already reads as the value is left alone.
func setTopScalar(path, key, value string) (bool, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	lines := strings.Split(string(b), "\n")
	eol := ""
	if strings.Contains(string(b), "\r\n") {
		eol = "\r"
	}
	line := key + ": " + yamlString(value)
	if i := topKeyLine(lines, key); i >= 0 {
		if got, ok := helmString([]byte(lines[i]), key); ok && got == value {
			return false, nil
		}
		cur := strings.TrimSuffix(lines[i], "\r")
		if _, comment, found := strings.Cut(strings.TrimPrefix(cur, key+":"), " #"); found {
			line += " #" + comment
		}
		lines[i] = line + eol
	} else {
		at := len(lines)
		for i, ln := range lines {
			if top := strings.TrimSuffix(ln, "\r"); top != "" && !preamble(top) {
				at = i
				break
			}
		}
		lines = append(lines[:at], append([]string{line + eol}, lines[at:]...)...)
	}
	return true, os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o644)
}

// preamble reports whether a line may precede a document's first top-level key:
// a comment, an indented line, a directive, or a document-start marker — `---`
// alone or followed by whitespace, which is where a trailing comment sits.
func preamble(ln string) bool {
	switch ln[0] {
	case '#', ' ', '\t', '%':
		return true
	}
	return ln == "---" || strings.HasPrefix(ln, "--- ") || strings.HasPrefix(ln, "---\t")
}

// ── the writes ───────────────────────────────────────────────────────────────

// projectCreate names a new project and the apps it starts with.
type projectCreate struct {
	// Name is the project, a DNS-1123 label; it becomes each app's `partOf`.
	Name string `json:"name" url:"-"`
	// Apps are the declarations it starts with, each by values directory (`org`,
	// defaulting to the caller's own) and name. At least one: a project is the apps
	// that name it.
	Apps []declRef `json:"apps" url:"-"`
	// Org names the project's owner, defaulting to the caller's own scope; naming
	// another is an act-as only a SuperAdmin has.
	Org string `json:"org" url:"-"`
	// Mode is `branch` (the default: a review branch, nothing deploys) or `commit`
	// (main).
	Mode string `json:"mode" url:"-"`
}

// projectRename is a project's new name.
type projectRename struct {
	// Project is the project to rename, from the path.
	Project string `json:"project"`
	// Name is its new name, which no app may already name.
	Name string `json:"name" url:"-"`
	// Org names the project's owner, defaulting to the caller's own scope.
	Org string `json:"org" url:"-"`
	// Mode is `branch` (the default) or `commit`.
	Mode string `json:"mode" url:"-"`
}

// projectFold names the project a deleted project's apps move into.
type projectFold struct {
	// Project is the project to delete, from the path.
	Project string `json:"project"`
	// Into is the existing project its apps move into. Required: an app always
	// belongs to exactly one project.
	Into string `json:"into"`
	// Org names the projects' owner, defaulting to the caller's own scope.
	Org string `json:"org"`
	// Mode is `branch` (the default) or `commit`.
	Mode string `json:"mode"`
}

// appMove moves one app to a project.
type appMove struct {
	// App is the declaration's name, from the path.
	App string `json:"app"`
	// PartOf is the project to move it to. A name nothing names yet creates it.
	PartOf string `json:"partOf" url:"-"`
	// Org is the values directory the app lives in, defaulting to the caller's own
	// org.
	Org string `json:"org" url:"-"`
	// Mode is `branch` (the default) or `commit`.
	Mode string `json:"mode" url:"-"`
}

// createProject creates a project from the apps it starts with.
//
// A project is the `partOf` its apps' values files name, so creating one names it
// on the listed apps: one commit to `hanzoai/universe` that moves each file's
// `partOf` scalar and touches nothing else. There is no empty project; to start
// one with a new app, create the app with `partOf` (POST /v1/platform/apps).
//
// `mode` is `branch` (the default: a review branch, nothing deploys) or `commit`
// (main). A `partOf` change relabels the app's pods, so the sync that applies it
// rolls them. 409 when the name already names apps — move apps into an existing
// project with PUT /v1/platform/apps/{app}/project. An org admin changes its own
// org's projects; the platform's own, and another org's, are SuperAdmin.
func (d delivery) createProject(ctx context.Context, in *projectCreate) (*projectWrite, error) {
	c, err := admit(ctx, cloud.Admin)
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(in.Name)
	if !appNameRE.MatchString(name) {
		return nil, zip.ErrBadRequest("name must be a DNS-1123 label")
	}
	if len(in.Apps) == 0 {
		return nil, zip.ErrBadRequest("a project is the apps that name it: list the apps it starts with, or create an app in it")
	}
	mode, err := declareModeOf(in.Mode)
	if err != nil {
		return nil, err
	}
	scope, err := projectScope(c, in.Org)
	if err != nil {
		return nil, err
	}
	want := map[string]bool{}
	for _, a := range in.Apps {
		dir, err := appDir(c, a.Org)
		if err != nil {
			return nil, err
		}
		if !inScope(dir, scope) {
			return nil, zip.ErrForbidden(dir + " does not belong to this project's owner")
		}
		if !appNameRE.MatchString(a.Name) {
			return nil, zip.ErrBadRequest("app names must be DNS-1123 labels")
		}
		want[dir+"/"+a.Name] = true
	}
	res, err := moveApps(d.s, ctx, mode, scope, "project "+name+": create", name, func(inv inventoryRead) ([]relabel, error) {
		if len(projectMembers(inv.dirs, scope, name)) > 0 {
			return nil, zip.ErrConflict("project " + name + " already exists; move apps into it with PUT /v1/platform/apps/{app}/project")
		}
		return pick(inv, want, name)
	})
	if err != nil {
		return nil, writeErr(c, d.s, "create project", err)
	}
	return &res, nil
}

// renameProject renames a project.
//
// It rewrites `partOf` on every declaration that names the project, in one commit
// to `hanzoai/universe`. 409 when the new name is already a project: fold into it
// with DELETE /v1/platform/projects/{project}?into=. `mode` as for create.
func (d delivery) renameProject(ctx context.Context, in *projectRename) (*projectWrite, error) {
	c, err := admit(ctx, cloud.Admin)
	if err != nil {
		return nil, err
	}
	from, to := strings.TrimSpace(in.Project), strings.TrimSpace(in.Name)
	if !appNameRE.MatchString(from) || !appNameRE.MatchString(to) {
		return nil, zip.ErrBadRequest("project names must be DNS-1123 labels")
	}
	mode, err := declareModeOf(in.Mode)
	if err != nil {
		return nil, err
	}
	scope, err := projectScope(c, in.Org)
	if err != nil {
		return nil, err
	}
	res, err := moveApps(d.s, ctx, mode, scope, "project "+from+": rename to "+to, to, func(inv inventoryRead) ([]relabel, error) {
		if len(projectMembers(inv.dirs, scope, to)) > 0 {
			return nil, zip.ErrConflict("project " + to + " already exists; fold " + from + " into it with DELETE /v1/platform/projects/" + from + "?into=" + to)
		}
		return every(inv, scope, from, to)
	})
	if err != nil {
		return nil, writeErr(c, d.s, "rename project", err)
	}
	return &res, nil
}

// deleteProject folds a project into another.
//
// An app always belongs to exactly one project, so a project ends by moving its
// apps: `into` names the existing project they move to, and the project is gone
// once no file names it. One commit to `hanzoai/universe`; `mode` as for create.
// 404 when `into` is not a project.
func (d delivery) deleteProject(ctx context.Context, in *projectFold) (*projectWrite, error) {
	c, err := admit(ctx, cloud.Admin)
	if err != nil {
		return nil, err
	}
	from, into := strings.TrimSpace(in.Project), strings.TrimSpace(in.Into)
	if !appNameRE.MatchString(from) {
		return nil, zip.ErrBadRequest("project must be a DNS-1123 label")
	}
	if into == "" {
		return nil, zip.ErrBadRequest("an app always belongs to a project: name the one this project's apps move into with ?into=")
	}
	if !appNameRE.MatchString(into) || into == from {
		return nil, zip.ErrBadRequest("into must be another project's name")
	}
	mode, err := declareModeOf(in.Mode)
	if err != nil {
		return nil, err
	}
	scope, err := projectScope(c, in.Org)
	if err != nil {
		return nil, err
	}
	res, err := moveApps(d.s, ctx, mode, scope, "project "+from+": fold into "+into, into, func(inv inventoryRead) ([]relabel, error) {
		if len(projectMembers(inv.dirs, scope, into)) == 0 {
			return nil, zip.ErrNotFound("project " + into + " does not exist; to give " + from + " a new name, rename it")
		}
		return every(inv, scope, from, into)
	})
	if err != nil {
		return nil, writeErr(c, d.s, "delete project", err)
	}
	return &res, nil
}

// moveApp moves an app to a project.
//
// It sets the declaration's `partOf` — in its cluster overlay when the overlay
// sets the key, else in its values file — and nothing else. Naming a project
// nothing names yet creates it. `org` is the values directory the app lives in;
// `mode` as for create.
func (d delivery) moveApp(ctx context.Context, in *appMove) (*projectWrite, error) {
	c, err := admit(ctx, cloud.Admin)
	if err != nil {
		return nil, err
	}
	app, to := strings.TrimSpace(in.App), strings.TrimSpace(in.PartOf)
	if !appNameRE.MatchString(app) || !appNameRE.MatchString(to) {
		return nil, zip.ErrBadRequest("app and partOf must be DNS-1123 labels")
	}
	mode, err := declareModeOf(in.Mode)
	if err != nil {
		return nil, err
	}
	dir, err := appDir(c, in.Org)
	if err != nil {
		return nil, err
	}
	want := map[string]bool{dir + "/" + app: true}
	res, err := moveApps(d.s, ctx, mode, owner(dir), "project "+to+": move "+dir+"/"+app, to, func(inv inventoryRead) ([]relabel, error) {
		return pick(inv, want, to)
	})
	if err != nil {
		return nil, writeErr(c, d.s, "move app", err)
	}
	return &res, nil
}

// appDir resolves the directory an app is addressed in: the caller's own org, or
// another under the act-as and reservation rules the delivery board applies.
func appDir(c *zip.Ctx, asked string) (string, error) {
	org, ok := principal.Org(c)
	if !ok {
		return "", principal.Refused(c)
	}
	return resolveOrg(org, asked, principal.Super(c))
}

// pick plans moving exactly the named declarations to project; a name the
// inventory does not hold is 404 rather than a silent skip, and one whose file
// does not parse is 409, because its partOf cannot be moved without reading it.
func pick(inv inventoryRead, want map[string]bool, project string) ([]relabel, error) {
	for k := range want {
		if dir, name, _ := strings.Cut(k, "/"); inv.unreadable(dir, name) {
			return nil, zip.ErrConflict("the declaration " + k + " does not parse; fix it in universe before moving it")
		}
	}
	var out []relabel
	found := map[string]bool{}
	for dir, ds := range inv.dirs {
		for _, dec := range ds {
			if k := dir + "/" + dec.Name; want[k] {
				found[k] = true
				out = append(out, relabel{Org: dir, Name: dec.Name, From: dec.PartOf, To: project})
			}
		}
	}
	for k := range want {
		if !found[k] {
			return nil, zip.ErrNotFound("no declaration " + k)
		}
	}
	sortRelabels(out)
	return out, nil
}

// every plans moving every member of from, in scope, to project. A values file in
// scope that does not parse may be a member, so it refuses rather than leave one
// behind under the old name.
func every(inv inventoryRead, scope, from, project string) ([]relabel, error) {
	if bad := inv.unreadableIn(func(dir string) bool { return inScope(dir, scope) }, false); len(bad) > 0 {
		return nil, zip.ErrConflict(fmt.Sprintf("%d declaration(s) in this scope do not parse, so the members of %s cannot all be known; fix them in universe first (%s)",
			len(bad), from, bad[0].Path))
	}
	var out []relabel
	for _, dec := range projectMembers(inv.dirs, scope, from) {
		out = append(out, relabel{Org: dec.Org, Name: dec.Name, From: from, To: project})
	}
	if len(out) == 0 {
		return nil, zip.ErrNotFound("no declaration names project " + from)
	}
	return out, nil
}

func sortRelabels(r []relabel) {
	sort.Slice(r, func(i, j int) bool { return r[i].Org+"/"+r[i].Name < r[j].Org+"/"+r[j].Name })
}

// writeErr keeps a refusal's own status and reports anything else as a failed
// write to universe (universeStatus). The cause is logged and a SuperAdmin
// reads it; anyone else reads that the write failed, because the cause names
// the platform's own repository, files and variables.
func writeErr(c *zip.Ctx, s *cloud.Service[state], what string, err error) error {
	var he *zip.HTTPError
	if errors.As(err, &he) {
		return err
	}
	s.Log.Error(what+" failed", "err", err)
	if principal.Super(c) {
		return zip.Errorf(universeStatus(err), "%s: %v", what, err)
	}
	return zip.Errorf(universeStatus(err), "%s failed; the cause is in the platform log", what)
}

// ── sync ─────────────────────────────────────────────────────────────────────

// CDResource is one object a CD Application manages, and whether it matches git.
type CDResource struct {
	// Group is the object's API group, "" for the core group.
	Group string `json:"group,omitempty"`
	// Kind is the object's kind.
	Kind string `json:"kind"`
	// Namespace is the object's namespace, "" for a cluster-scoped one.
	Namespace string `json:"namespace,omitempty"`
	// Name is the object's name.
	Name string `json:"name"`
	// Status is CD's verdict on the object: Synced or OutOfSync.
	Status string `json:"status"`
	// Health is the object's health, "" for a kind CD holds no health for.
	Health string `json:"health,omitempty"`
	// Prune is true for a live object git no longer declares. A platform sync
	// never prunes, so a sync leaves it running.
	Prune bool `json:"prune,omitempty"`
}

// cdDiff is what a sync of one app would change.
type cdDiff struct {
	// Application is the CD Application the declaration renders as.
	Application string `json:"application"`
	// Sync is CD's verdict: Synced or OutOfSync.
	Sync string `json:"sync"`
	// Compared is the universe commit CD compared the cluster against — the one
	// a sync applies.
	Compared string `json:"compared,omitempty"`
	// Applied is the universe commit CD last synced, "" before its first sync.
	Applied string `json:"applied,omitempty"`
	// Pending are the objects that differ from git: what a sync would apply.
	Pending []CDResource `json:"pending"`
	// Resources is every object the Application manages.
	Resources []CDResource `json:"resources"`
	// Operation is the phase of a sync in flight or last run, "" when none ran.
	Operation string `json:"operation,omitempty"`
}

// appCD addresses one declaration's CD Application.
type appCD struct {
	// App is the declaration's name, from the path.
	App string `json:"app"`
	// Org is the values directory the declaration lives in, defaulting to the
	// caller's own org.
	Org string `json:"org"`
}

// getDiff answers what syncing one app would change.
//
// It reads the CD Application the declaration renders as: the commit CD compared
// the cluster against, the commit it last applied, and every object it manages
// with CD's verdict on each — the ones that differ from git are what a sync
// applies. Object-level, as CD reports it; the field-level diff stays on
// cd.hanzo.ai. SuperAdmin only, the same predicate CD's own RBAC applies to
// viewing an Application. 404 when CD holds no Application for the declaration.
func (d delivery) getDiff(ctx context.Context, in *appCD) (*cdDiff, error) {
	obj, err := d.cdApplication(ctx, in)
	if err != nil {
		return nil, err
	}
	diff := diffOf(obj)
	return &diff, nil
}

// syncApp syncs one app now: CD applies the commit it compared, to this one
// Application.
//
// Every Application is manual — its AppProject admits only a manual sync — so this
// is the deploy step, requested the way cd.hanzo.ai requests one: an operation on
// the Application, initiated by the caller, applying the compared commit with the
// Application's own sync options and no pruning. It answers the Application with
// the operation queued; poll the app's cd read for how it ends. SuperAdmin only,
// the predicate CD's own RBAC holds sync to, and recorded in the audit trail like
// every mutation. 409 while another operation is queued, running or terminating,
// before CD has compared the Application with git, and when the Application
// changes between the read and the request.
func (d delivery) syncApp(ctx context.Context, in *appCD) (*cdDiff, error) {
	c, err := admit(ctx, cloud.Super)
	if err != nil {
		return nil, err
	}
	obj, err := d.cdApplication(ctx, in)
	if err != nil {
		return nil, err
	}
	// Who asked, as CD's history will show it: the email the identity boundary
	// minted, else the user id.
	actor := strings.TrimSpace(c.UserEmail())
	if actor == "" {
		actor = c.User()
	}
	got, err := k8s.RequestSync(ctx, d.fs.State.dyn, obj, actor, "synced from platform.hanzo.ai")
	if err != nil {
		if errors.Is(err, k8s.ErrSyncRefused) {
			return nil, zip.ErrConflict(err.Error())
		}
		if apierrors.IsConflict(err) {
			return nil, zip.ErrConflict(obj.GetName() + " changed while this sync was requested; read it again and sync")
		}
		if apierrors.IsForbidden(err) {
			return nil, zip.Errorf(http.StatusServiceUnavailable,
				"platform cd: the platform service may not patch %s in %s — grant it and sync answers",
				k8s.CDApplications.Resource, cdNamespace)
		}
		d.fs.Log.Error("cd sync failed", "application", obj.GetName(), "err", err)
		return nil, zip.Errorf(http.StatusBadGateway, "platform cd: could not request a sync of %s", obj.GetName())
	}
	d.fs.Log.Info("cd sync requested", "application", obj.GetName(), "actor", actor)
	diff := diffOf(got)
	diff.Operation = "Requested"
	return &diff, nil
}

// cdApplication reads the CD Application one declaration renders as, SuperAdmin
// only. The Application is named from the declaration's path by the generator's
// own rule (applicationName) and confirmed by the values path it renders, so a
// name that resolves to some other file's Application is 404.
func (d delivery) cdApplication(ctx context.Context, in *appCD) (*unstructured.Unstructured, error) {
	c, err := admit(ctx, cloud.Super)
	if err != nil {
		return nil, err
	}
	org, ok := principal.Org(c)
	if !ok {
		return nil, principal.Refused(c)
	}
	if !appNameRE.MatchString(in.App) {
		return nil, zip.ErrBadRequest("app must be a DNS-1123 label")
	}
	dir, err := resolveOrg(org, in.Org, true)
	if err != nil {
		return nil, err
	}
	if err := fleetReady(d.fs); err != nil {
		return nil, err
	}
	name := applicationName(dir, in.App)
	obj, err := d.fs.State.dyn.Resource(k8s.CDApplications).Namespace(cdNamespace).Get(ctx, name, metav1.GetOptions{})
	switch {
	case apierrors.IsNotFound(err):
		return nil, zip.ErrNotFound("the delivery plane has no Application " + name +
			" — a declaration on a branch has none until the branch is merged")
	case apierrors.IsForbidden(err):
		return nil, zip.Errorf(http.StatusServiceUnavailable,
			"platform cd: the platform service may not read %s in %s — grant it and this answers",
			k8s.CDApplications.Resource, cdNamespace)
	case err != nil:
		d.fs.Log.Error("cd get failed", "application", name, "err", err)
		return nil, zip.Errorf(http.StatusBadGateway, "platform cd: could not read %s", name)
	}
	if want := declarePath(dir, in.App); observeCDApp(obj).Path != want {
		return nil, zip.ErrNotFound("Application " + name + " does not render " + want)
	}
	return obj, nil
}

// diffOf projects an Application's status into what a sync would change.
func diffOf(obj *unstructured.Unstructured) cdDiff {
	d := cdDiff{
		Application: obj.GetName(),
		Sync:        nested(obj.Object, "status", "sync", "status"),
		Compared:    nested(obj.Object, "status", "sync", "revision"),
		Operation:   nested(obj.Object, "status", "operationState", "phase"),
		Pending:     []CDResource{},
		Resources:   []CDResource{},
	}
	if hist, ok, _ := unstructured.NestedSlice(obj.Object, "status", "history"); ok && len(hist) > 0 {
		if last, ok := hist[len(hist)-1].(map[string]any); ok {
			d.Applied, _ = last["revision"].(string)
		}
	}
	raw, _, _ := unstructured.NestedSlice(obj.Object, "status", "resources")
	for _, r := range raw {
		m, ok := r.(map[string]any)
		if !ok {
			continue
		}
		res := CDResource{Status: str(m, "status")}
		res.Group, res.Kind, res.Namespace, res.Name = str(m, "group"), str(m, "kind"), str(m, "namespace"), str(m, "name")
		if h, ok := m["health"].(map[string]any); ok {
			res.Health = str(h, "status")
		}
		res.Prune, _ = m["requiresPruning"].(bool)
		d.Resources = append(d.Resources, res)
		if res.Status != "Synced" || res.Prune {
			d.Pending = append(d.Pending, res)
		}
	}
	return d
}

func str(m map[string]any, k string) string {
	s, _ := m[k].(string)
	return s
}
