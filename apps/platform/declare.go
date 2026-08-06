// declare.go — the DECLARATION plane. An app IS a values file in universe.
//
// cd.hanzo.ai does not watch a registry and it does not take an API call. The
// `fleet` ApplicationSet (universe `infra/k8s/hanzo-cd/applicationset-fleet.yaml`)
// runs a git generator over `charts/app/values/*/*.yaml` on main, and every file
// it matches becomes one Application rendering the shared `charts/app` chart.
// So there is exactly one way to deploy: write a values file. This file is that
// write, expressed as a Go seam the /v1/platform surface can call.
//
// ── what the path decides, and why the caller never names it ─────────────────
//
// The generator derives FOUR facts from the file's own path and NOTHING from its
// contents: the Application name (`<dir>-<file>`), the destination namespace
// (`<dir>`), the Helm release name (`<file>`), and — load-bearing — the AppProject
// the sync is admitted under (`tenant-<org>` for a `tenant-` directory,
// `hanzo-platform` for everything else). project-tenants.yaml states the rule the
// generator implements: for org `<org>` the namespace, the project, the values
// directory and the CD RBAC role are ALL `tenant-<org>`, and that string is the
// same key IAM puts in the `owner` claim.
//
// hanzo-platform admits destination namespace `*` and cluster-scoped
// ClusterRole/ClusterRoleBinding. `tenant-<org>` admits ONE namespace, no
// cluster scope at all, and six harmless kinds. A caller who could name its own
// directory could therefore name its own fence — so `dir` here is DERIVED from
// the verified principal and is not a field of any request. Writing a customer's
// app into `values/hanzo/` is not a filing mistake, it is a tenant escaping into
// the platform's fence.
//
// ── create renders, update moves one scalar ─────────────────────────────────
//
// These files carry the reasoning for every value in them, so pin.go refuses a
// YAML round-trip and rewrites the tag scalar where it sits. That rule holds
// here: CREATE renders a fresh file (there is nothing to preserve yet), UPDATE
// reuses pinFile.setTag and touches one line. An update that would have to change
// anything else is REFUSED and names the file, rather than reflowing a
// hand-maintained declaration into whatever a marshaller emits.
//
// Reading is different and uses a real parser: reading cannot reflow anything,
// and a hand-rolled scanner over a union-typed `ingress.hosts` is how an
// inventory quietly goes half-empty.
//
// ── a branch is not a deploy ────────────────────────────────────────────────
//
// The generator reads `main`. A declaration pushed to a branch is therefore
// INERT: no Application is generated, nothing syncs, nothing runs. That is the
// default mode, and it is what makes this endpoint safe to expose — the
// deliberate act that deploys is a human merging the pull request, by which time
// the build that produced the tag has either landed or not.
//
// Committing straight to main is the other mode and it proves the image is
// pullable first (imagePullable, pin.go's probe), because a declaration naming an
// image the registry cannot serve is an ImagePullBackOff with no rollback path.

package platform

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/cloud"
	"gopkg.in/yaml.v3"
)

const (
	// declarePrefix is the ONE directory the inventory lives under, relative to
	// the repository root. Stated once; every path here is built from it.
	declarePrefix = "charts/app/values"

	// declareBranchPrefix namespaces every branch this seam pushes. Nothing else
	// writes refs under it, so a reviewer reading `git branch -r` can tell an
	// API-authored declaration from a human one without opening the diff.
	declareBranchPrefix = "deploy"

	// declareDeadline bounds one whole declaration — clone, edit, push. The
	// handler's request context bounds it further; this is the ceiling for the
	// detached case.
	declareDeadline = 5 * time.Minute

	// inventoryTTL is how long a read of the inventory is reused. The inventory
	// changes when someone deploys, which is human-paced, and the alternative is
	// a shallow clone of a 200 MB repository per dashboard poll.
	inventoryTTL = 30 * time.Second

	// declarePullSecret is the registry credential every hanzo-tier workload
	// already names. A declaration that omits it renders a pod that cannot pull
	// from a private repository.
	declarePullSecret = "ghcr-secret"

	// declareIssuer is the cert-manager ClusterIssuer the fleet's public hosts
	// are issued by (papers.yaml, www.yaml and every other ingress-bearing file).
	declareIssuer = "letsencrypt-prod-cf"

	// declarePort is the container port a built app is assumed to listen on, and
	// the port the Service publishes on 80. It is a DEFAULT, overridable per
	// declaration, not a constraint.
	declarePort = 3000
)

// ── the value ────────────────────────────────────────────────────────────────

// Declaration is one app as the fleet declares it — the file, and the four facts
// the generator derives from where it sits. Everything here is READ from git; a
// Declaration never carries a running state, because "declared" and "running" are
// different questions and a board that blends them cannot report drift.
type Declaration struct {
	Name string `json:"name"` // the Helm release name — the file's basename
	// Namespace is the values DIRECTORY, which IS the destination namespace.
	Namespace string `json:"namespace"`
	// Application is the CD Application name the generator mints: <dir>-<file>.
	// It is the join key against /v1/platform/cd.
	Application string `json:"application"`
	// Project is the AppProject the sync is admitted under, derived from the
	// directory exactly as the ApplicationSet derives it.
	Project string `json:"project"`
	// Path is the file, relative to the repository root.
	Path string `json:"path"`

	Repository string   `json:"repository,omitempty"` // image.repository
	Tag        string   `json:"tag,omitempty"`        // image.tag
	Digest     string   `json:"digest,omitempty"`     // image.digest — wins over tag
	Hosts      []string `json:"hosts"`                // ingress.hosts, both shapes flattened
	Replicas   int      `json:"replicas,omitempty"`
	// Automated is cd.automated: false means the Application reports drift and
	// NOTHING moves. It is off by default for a new file on purpose.
	Automated bool `json:"automated"`
}

// declareSpec is a declaration about to be written. Every field is resolved —
// the handler derives Namespace, Repository and Hosts before constructing one,
// so nothing here is still a caller's opinion.
type declareSpec struct {
	Name       string
	Namespace  string
	Repository string
	Tag        string
	Hosts      []string
	Env        []declareEnv
	Port       int
	Replicas   int
	// Automated is written as cd.automated. A branch declaration carries it
	// truthfully — the file is what will be merged — but nothing acts on it until
	// the merge lands on main.
	Automated bool
	// Origin is the git URL the image was built from, recorded in the file's
	// header so a reader can get from the declaration back to the source.
	Origin string
}

// declareEnv is one environment entry, in the chart's shape: a LIST of
// {name,value}, never a map. The schema sets additionalProperties:false, so a
// third key fails the render rather than being ignored.
type declareEnv struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// project mirrors the ApplicationSet's own derivation. Stated here so the record
// this API returns and the fence the cluster applies cannot disagree.
func declareProject(namespace string) string {
	if strings.HasPrefix(namespace, "tenant-") {
		return namespace
	}
	return "hanzo-platform"
}

// declarePath is the ONE place a declaration's path is spelled.
//
// Both segments are checked by checkLoc before any path is built from them —
// never here, because a function that returns a string has nowhere to refuse.
func declarePath(namespace, name string) string {
	return declarePrefix + "/" + namespace + "/" + name + ".yaml"
}

// checkLoc holds the two path segments to the grammar the cluster holds them to
// anyway: a namespace is a DNS-1123 label and so is an app name.
//
// It is DEFENCE IN DEPTH and it is not redundant. The HTTP surface derives both
// from a validated principal (declareNamespace, declareName), so nothing a
// caller sends reaches here unchecked today — but every function in this file
// takes them as plain strings, filepath.Join CLEANS `..` out of a segment rather
// than refusing it, and a single future caller that forgets is a read or a write
// in another tenant's directory. The guard belongs where the path is built, so
// there is no way to build one without it.
func checkLoc(ns, name string) error {
	if !appNameRE.MatchString(ns) {
		return fmt.Errorf("%q is not a namespace: a values directory is a DNS-1123 label", ns)
	}
	if !slugRE.MatchString(name) {
		return fmt.Errorf("%q is not an app name: a declaration is a DNS-1123 label", name)
	}
	return nil
}

// checkNS is checkLoc's namespace half, for the reads that name no app.
func checkNS(ns string) error {
	if !appNameRE.MatchString(ns) {
		return fmt.Errorf("%q is not a namespace: a values directory is a DNS-1123 label", ns)
	}
	return nil
}

// declareBranch is the ref one declaration is pushed to. It carries the tag, so
// a re-declare of the SAME build is idempotent (the push is a no-op) and a new
// build never has to force anything: a fresh tag is a fresh ref.
func declareBranch(namespace, name, tag string) string {
	return declareBranchPrefix + "/" + namespace + "/" + name + "/" + tag
}

// ── rendering (pure) ─────────────────────────────────────────────────────────

// render emits the values file for a NEW declaration.
//
// It writes only keys `charts/app/values.schema.json` declares — the schema sets
// additionalProperties:false at the top level, so an invented key does not get
// ignored, it fails the Helm render and the Application never syncs. The output
// is deliberately the shape of the files a human already maintains (papers.yaml),
// because the next edit to this file will be a human's.
func (d declareSpec) render() []byte {
	var b strings.Builder
	w := func(format string, args ...any) { fmt.Fprintf(&b, format, args...) }

	w("# %s — declared by platform.hanzo.ai.\n", d.Name)
	w("#\n")
	w("# This file IS the source of truth for this app: the `fleet` ApplicationSet\n")
	w("# renders charts/app against it, and the image.tag scalar below is what runs.\n")
	if d.Origin != "" {
		w("# Built from %s.\n", d.Origin)
	}
	w("# Edit it directly — nothing regenerates it.\n")

	// cd first, as the fleet's own files have it: it is the one key the
	// ApplicationSet (not the chart) reads, so it reads as a header.
	w("cd:\n")
	w("  automated: %t\n", d.Automated)

	if d.Replicas > 0 {
		w("replicas: %d\n", d.Replicas)
	}
	if len(d.Env) > 0 {
		w("env:\n")
		for _, e := range d.Env {
			w("- name: %s\n", e.Name)
			// Quoted always. The chart quotes it again on render; an unquoted
			// value here would still be PARSED by YAML first, and a version like
			// 1.10 or an address like 0x53141 is not the string the caller meant.
			w("  value: %s\n", yamlString(e.Value))
		}
	}
	w("resources:\n")
	w("  limits:\n    cpu: 500m\n    memory: 512Mi\n")
	w("  requests:\n    cpu: 100m\n    memory: 128Mi\n")
	w("imagePullSecrets:\n- name: %s\n", declarePullSecret)
	w("ports:\n")
	w("- containerPort: %d\n  name: http\n  servicePort: 80\n", d.Port)
	w("image:\n")
	w("  repository: %s\n", d.Repository)
	w("  tag: %s\n", d.Tag)
	w("  pullPolicy: IfNotPresent\n")
	if len(d.Hosts) > 0 {
		w("ingress:\n")
		w("  enabled: true\n")
		w("  hosts:\n")
		for _, h := range d.Hosts {
			w("  - %s\n", h)
		}
		w("  tls: true\n")
		w("  clusterIssuer: %s\n", declareIssuer)
	} else {
		w("ingress:\n  enabled: false\n")
	}
	return []byte(b.String())
}

// yamlString quotes a scalar for YAML in the one form that needs no escaping
// table: single quotes, where the only special sequence is a doubled quote.
func yamlString(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// ── reading (a real parser, on purpose) ──────────────────────────────────────

// valuesDoc is the subset of a values file this plane reads back. Unknown keys
// are ignored on READ — the chart supports 65 of them and an inventory has no
// business failing because a service declares a sidecar.
type valuesDoc struct {
	CD struct {
		Automated bool `yaml:"automated"`
	} `yaml:"cd"`
	Replicas int `yaml:"replicas"`
	Image    struct {
		Repository string `yaml:"repository"`
		Tag        string `yaml:"tag"`
		Digest     string `yaml:"digest"`
	} `yaml:"image"`
	Ingress struct {
		Enabled bool `yaml:"enabled"`
		// Hosts is a UNION in the schema: a bare hostname joins the one shared
		// Ingress, a map gets its own. Decoding into `any` and flattening is the
		// only total read of it — typing it as []string silently drops every
		// map-shaped host, and those are the wildcard and multi-backend ones.
		Hosts []any `yaml:"hosts"`
	} `yaml:"ingress"`
}

// readDeclaration parses one values file into the record this API publishes.
// A file that fails to parse is an ERROR and never an empty row: an inventory
// that renders an unreadable declaration as "no image, no hosts" is the same lie
// as an unreachable cluster rendering as an empty fleet.
func readDeclaration(root, namespace, name string) (Declaration, error) {
	if err := checkLoc(namespace, name); err != nil {
		return Declaration{}, err
	}
	rel := declarePath(namespace, name)
	b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		return Declaration{}, fmt.Errorf("read %s: %w", rel, err)
	}
	var v valuesDoc
	if err := yaml.Unmarshal(b, &v); err != nil {
		return Declaration{}, fmt.Errorf("parse %s: %w", rel, err)
	}
	d := Declaration{
		Name:        name,
		Namespace:   namespace,
		Application: namespace + "-" + name,
		Project:     declareProject(namespace),
		Path:        rel,
		Repository:  v.Image.Repository,
		Tag:         v.Image.Tag,
		Digest:      v.Image.Digest,
		Replicas:    v.Replicas,
		Automated:   v.CD.Automated,
		Hosts:       []string{},
	}
	if v.Ingress.Enabled {
		d.Hosts = flattenHosts(v.Ingress.Hosts)
	}
	return d, nil
}

// flattenHosts reduces the union-typed ingress.hosts to the hostnames it names,
// in declaration order. A map entry contributes its `host`; anything else is
// skipped rather than guessed at.
func flattenHosts(items []any) []string {
	out := make([]string, 0, len(items))
	for _, it := range items {
		switch h := it.(type) {
		case string:
			if h != "" {
				out = append(out, h)
			}
		case map[string]any:
			if s, ok := h["host"].(string); ok && s != "" {
				out = append(out, s)
			}
		}
	}
	return out
}

// ── the git seam ─────────────────────────────────────────────────────────────
//
// Clone, credential and race handling are pin.go's — the same shallow
// single-branch clone, the same token read from KMS and carried as an
// http.extraHeader rather than in argv, the same fast-forward-only push retried
// by re-reading the tip. Nothing about writing to universe is different here, so
// nothing about it is written twice.

// universeClone clones the branch CD reads into a fresh temp dir and hands it to
// fn. The directory is removed on return, always: it holds a checkout of the
// repository the whole fleet deploys through.
func universeClone(ctx context.Context, env []string, fn func(dir string) error) error {
	dir, err := os.MkdirTemp("", "declare-universe-")
	if err != nil {
		return fmt.Errorf("workdir: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	if _, err := runGit(ctx, "", env, "clone", "--depth", "1", "--single-branch",
		"--branch", universeBranch, universeRemote, dir); err != nil {
		return fmt.Errorf("clone %s: %w", universeRemote, err)
	}
	return fn(dir)
}

// ── the inventory (read) ─────────────────────────────────────────────────────

// inventoryCache holds the last read of one directory. universe is one
// repository and a dashboard polls; without this every poll is a clone.
type inventoryCache struct {
	mu   sync.Mutex
	at   time.Time
	dirs map[string][]Declaration
}

var inventory = &inventoryCache{dirs: map[string][]Declaration{}}

// invalidate drops the cache. Called after a write to main, so a caller that
// declares and immediately lists sees its own write rather than a 30-second-old
// inventory that does not contain it.
func (c *inventoryCache) invalidate() {
	c.mu.Lock()
	c.dirs = map[string][]Declaration{}
	c.at = time.Time{}
	c.mu.Unlock()
}

// declarations reads every declaration in one namespace directory.
//
// A directory that does not exist is an EMPTY inventory and not an error: an org
// that has declared nothing yet is a real, correct answer. A directory that
// exists and cannot be read IS an error — the two must never look alike.
func declarations(s *cloud.Service[state], ctx context.Context, namespace string) ([]Declaration, error) {
	if err := checkNS(namespace); err != nil {
		return nil, err
	}
	inventory.mu.Lock()
	if time.Since(inventory.at) < inventoryTTL {
		if d, ok := inventory.dirs[namespace]; ok {
			out := append([]Declaration(nil), d...)
			inventory.mu.Unlock()
			return out, nil
		}
	}
	inventory.mu.Unlock()

	token, err := pinToken(s, ctx)
	if err != nil {
		return nil, fmt.Errorf("universe credential: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, declareDeadline)
	defer cancel()

	var out []Declaration
	err = universeClone(ctx, pinGitEnv(token), func(dir string) error {
		names, err := declaredNames(dir, namespace)
		if err != nil {
			return err
		}
		for _, n := range names {
			d, err := readDeclaration(dir, namespace, n)
			if err != nil {
				return err
			}
			out = append(out, d)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	inventory.mu.Lock()
	if time.Since(inventory.at) >= inventoryTTL {
		inventory.dirs = map[string][]Declaration{}
		inventory.at = time.Now()
	}
	inventory.dirs[namespace] = append([]Declaration(nil), out...)
	inventory.mu.Unlock()
	return out, nil
}

// declaredNames lists the app names declared in one directory, sorted. The glob
// is anchored at the directory, so a name can never reach a sibling namespace's
// files however it is spelled.
func declaredNames(root, namespace string) ([]string, error) {
	if err := checkNS(namespace); err != nil {
		return nil, err
	}
	matches, err := filepath.Glob(filepath.Join(root, filepath.FromSlash(declarePrefix), namespace, "*.yaml"))
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(matches))
	for _, m := range matches {
		names = append(names, strings.TrimSuffix(filepath.Base(m), ".yaml"))
	}
	sort.Strings(names)
	return names, nil
}

// ── the write ────────────────────────────────────────────────────────────────

// declareMode is where a declaration is pushed. The two modes differ in exactly
// one respect and it is the only one that matters: whether the generator, which
// reads main, will see it.
type declareMode string

const (
	// modeBranch pushes to refs/heads/deploy/<ns>/<name>/<tag> and NOTHING
	// deploys. It is the default because a deploy should be a merge.
	modeBranch declareMode = "branch"
	// modeCommit pushes to main. The Application appears (or moves) on CD's next
	// reconcile, and with cd.automated it applies without further review — so
	// this mode proves the image is pullable before it writes.
	modeCommit declareMode = "commit"
)

// declareResult is what a write did, in enough detail to audit it: which file,
// on which ref, moving which tag, and — for a branch — where to open the review.
type declareResult struct {
	Declaration Declaration `json:"declaration"`
	Mode        declareMode `json:"mode"`
	// Ref is the branch the declaration was pushed to: "main" for a commit.
	Ref string `json:"ref"`
	// Commit is the sha this write created, empty when nothing changed.
	Commit string `json:"commit,omitempty"`
	// Created distinguishes a new declaration from a tag move on an existing one.
	Created bool `json:"created"`
	// Changed is false when the declaration already read exactly this — a
	// re-declare of the same build is a success with nothing to do, never an
	// empty commit.
	Changed bool `json:"changed"`
	// Review is the URL that opens the pull request for a branch write. It is
	// empty for a commit, which has no review to open.
	Review string `json:"review,omitempty"`
	// Live states plainly whether anything can deploy from this write. A branch
	// is inert; the generator reads main.
	Live bool `json:"live"`
}

// declare writes one declaration to universe.
//
// CREATE renders the file. UPDATE moves the image.tag scalar and NOTHING else —
// see the header. An update whose spec disagrees with the declared file in any
// other respect is refused by checkDeclared before a byte is written, so this
// never silently drops a host or an environment variable a human added.
func declare(s *cloud.Service[state], ctx context.Context, spec declareSpec, mode declareMode) (declareResult, error) {
	res := declareResult{Mode: mode}
	if err := checkLoc(spec.Namespace, spec.Name); err != nil {
		return res, err
	}

	// Prove the image BEFORE main is pointed at it. A branch is inert, so the
	// proof is not required there — and requiring it would make the common case
	// (declare the app, then build it) impossible.
	if mode == modeCommit {
		if err := imagePullable(ctx, spec.Repository, spec.Tag); err != nil {
			return res, err
		}
	}

	token, err := pinToken(s, ctx)
	if err != nil {
		return res, fmt.Errorf("universe credential: %w", err)
	}
	env := pinGitEnv(token)

	ctx, cancel := context.WithTimeout(ctx, declareDeadline)
	defer cancel()

	target := universeBranch
	if mode == modeBranch {
		target = declareBranch(spec.Namespace, spec.Name, spec.Tag)
	}

	err = universeClone(ctx, env, func(dir string) error {
		for attempt := 1; attempt <= pinAttempts; attempt++ {
			if attempt > 1 {
				if _, err := runGit(ctx, dir, env, "fetch", "--depth", "1", "origin", universeBranch); err != nil {
					return fmt.Errorf("re-read %s: %w", universeBranch, err)
				}
				if _, err := runGit(ctx, dir, env, "reset", "--hard", "FETCH_HEAD"); err != nil {
					return fmt.Errorf("re-read %s: %w", universeBranch, err)
				}
			}

			rel := declarePath(spec.Namespace, spec.Name)
			abs := filepath.Join(dir, filepath.FromSlash(rel))
			_, statErr := os.Stat(abs)
			created := os.IsNotExist(statErr)
			if statErr != nil && !created {
				return fmt.Errorf("stat %s: %w", rel, statErr)
			}

			changed := true
			switch {
			case created:
				if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
					return fmt.Errorf("create %s: %w", filepath.Dir(rel), err)
				}
				if err := os.WriteFile(abs, spec.render(), 0o644); err != nil {
					return fmt.Errorf("write %s: %w", rel, err)
				}
			default:
				f, err := readPinFile(abs)
				if err != nil {
					return err
				}
				if err := checkDeclared(dir, spec); err != nil {
					return err
				}
				if f.repository != spec.Repository {
					return fmt.Errorf("%s declares image.repository %s but this build produced %s — refusing to point one app's declaration at another's image",
						rel, f.repository, spec.Repository)
				}
				if f.current == spec.Tag {
					changed = false
					break
				}
				f.setTag(spec.Tag)
				if err := f.write(spec.Tag); err != nil {
					return err
				}
			}

			d, err := readDeclaration(dir, spec.Namespace, spec.Name)
			if err != nil {
				return err
			}
			res.Declaration, res.Created, res.Changed = d, created, changed

			if !changed {
				// Already exactly this. There is nothing to commit and nothing to
				// push; the declaration is already what the caller asked for.
				res.Ref, res.Live = universeBranch, true
				return nil
			}

			if _, err := runGit(ctx, dir, env, "add", "--", rel); err != nil {
				return err
			}
			verb := "declare"
			if !created {
				verb = "release"
			}
			msg := fmt.Sprintf("%s %s/%s at %s\n\n%s renders charts/app against this file; the image.tag scalar is what runs.\nDeclared through platform.hanzo.ai from %s.",
				verb, spec.Namespace, spec.Name, spec.Tag, d.Application, orDash(spec.Origin))
			if _, err := runGit(ctx, dir, env,
				"-c", "user.name="+pinCommitUser, "-c", "user.email="+pinCommitEmail,
				"commit", "-m", msg); err != nil {
				return err
			}
			sha, err := runGit(ctx, dir, env, "rev-parse", "HEAD")
			if err != nil {
				return err
			}
			res.Commit = strings.TrimSpace(sha)

			out, err := runGit(ctx, dir, env, "push", "origin", "HEAD:refs/heads/"+target)
			if err == nil {
				res.Ref = target
				res.Live = mode == modeCommit
				if mode == modeBranch {
					res.Review = reviewURL(target)
				}
				return nil
			}
			// A branch ref carries the tag, so it is ours alone and a rejection
			// there is not a race — it is a real failure. Only main is shared.
			if mode != modeCommit || !pushRaced(out) {
				return fmt.Errorf("push %s: %w", target, err)
			}
			s.Log.Info("another write landed on universe first; re-reading the tip",
				"app", spec.Name, "namespace", spec.Namespace, "attempt", attempt)
		}
		return fmt.Errorf("declare %s/%s: %d concurrent writes landed first — the declaration was NOT written",
			spec.Namespace, spec.Name, pinAttempts)
	})
	if err != nil {
		return declareResult{Mode: mode}, err
	}
	if mode == modeCommit {
		inventory.invalidate()
	}
	return res, nil
}

// checkDeclared refuses an UPDATE that would need to change more than the tag.
//
// The file is hand-maintainable and a human may have added a host, a resource
// bound, a sidecar. Rewriting it from a request body would silently discard
// that, so a spec that disagrees is an error naming the file — the caller edits
// the declaration, which is where the decision belongs.
func checkDeclared(root string, spec declareSpec) error {
	d, err := readDeclaration(root, spec.Namespace, spec.Name)
	if err != nil {
		return err
	}
	if len(spec.Hosts) > 0 && !sameHosts(d.Hosts, spec.Hosts) {
		return fmt.Errorf("%s already declares hosts %v and this request asks for %v — refusing to rewrite a declaration; edit %s to change its hosts",
			d.Path, d.Hosts, spec.Hosts, d.Path)
	}
	if len(spec.Env) > 0 {
		return fmt.Errorf("%s already exists and env is set on it there — refusing to rewrite a declaration; edit %s to change its environment",
			d.Path, d.Path)
	}
	return nil
}

func sameHosts(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	x, y := append([]string(nil), a...), append([]string(nil), b...)
	sort.Strings(x)
	sort.Strings(y)
	for i := range x {
		if x[i] != y[i] {
			return false
		}
	}
	return true
}

// reviewURL is where a human opens the pull request for a pushed branch. The
// forge serves a compare view at this address; opening the PR from it is one
// click. This API does not open it: doing that needs a forge API client, which
// this deployment does not have yet, and returning a URL that works is honest
// where returning a pull-request id that does not would not be.
func reviewURL(branch string) string {
	return strings.TrimSuffix(universeRemote, ".git") + "/compare/" + universeBranch + "..." + branch
}

func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "an unrecorded source"
	}
	return s
}
