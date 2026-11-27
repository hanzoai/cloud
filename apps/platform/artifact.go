// artifact.go — the ARTIFACT lane of POST /v1/platform/runner: build a project and
// publish what it produced, when what it produced is not a container image.
//
// /v1/platform/runner could build exactly one shape — a Dockerfile → an image pushed to a
// registry (runner.go → launchDirectBuild). Everything else a repo can produce —
// a Go binary, a Rust binary, an npm tarball, a wheel — had no in-cluster build
// path at all, so the platform's answer to "build my project" was "write a
// Dockerfile". The declaration for those artifacts ALREADY EXISTED: hanzo.yml's
// `binaries:` + `bucket:` blocks, which hanzoai/ci's reusable workflow has read
// since it was written. Only the GitHub lane implemented them. This is the
// platform-native implementation of the SAME contract — one recipe, two lanes,
// exactly as `images:` is already built by buildx on a runner and by
// BuildKit in-cluster.
//
// Shape of one build (a k8s Job on the isolated build namespace + CI pool):
//
//	initContainers  one per `binaries:` entry, in the toolchain image that entry
//	                names, running the recipe against a shallow clone pinned to
//	                the requested ref. They run SEQUENTIALLY (k8s guarantees it)
//	                and share /w — so a repo whose Go binary and TS package need
//	                different toolchains is still ONE build with ONE index.
//	container       the publisher: hashes every file the recipe left in /w/dist,
//	                PUTs it to hanzoai/s3, and writes binaries.json last.
//
// The split is the security boundary, not a packaging detail. `run:` is
// arbitrary shell BY DESIGN (it is a build command, the same trust as a
// Dockerfile RUN), so it must never see a credential: the initContainers carry
// no object-store env and no service-account token, and the publisher — which
// holds the S3 credential — runs only artifactPublishScript, a constant.
//
// Every recipe value reaches the scripts as an ENVIRONMENT VARIABLE expanded in
// double quotes, never as interpolated script text, so nothing a caller sends
// can rewrite the script itself.

package platform

import (
	"context"
	_ "embed"
	"fmt"
	"regexp"
	"strings"

	"github.com/hanzoai/cloud/sdk/env"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// binarySpec mirrors ONE hanzo.yml `binaries:` entry. The JSON names ARE the
// YAML names, so the recipe a repo declares and the request a caller sends are
// the same document — there is no second format to keep in sync.
//
// It has TWO LANES and an entry picks exactly one: `main` builds a Go package
// (zero config, cross-compiled over `platforms`), `run` is any other toolchain's
// build command and must say through `out` what it produced. Declaring both is a
// 400.
type binarySpec struct {
	// Name is the artifact's base name: the prefix of every file published for
	// this entry, and the name a host later asks for. It must match
	// `^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`, which is what makes it safe as both a
	// filename and a URL path segment.
	Name string `json:"name"`
	// Main is the Go package to build, repo-relative (`.` or `./cmd/x`), and it
	// selects the GO LANE. Defaults to `.` when neither lane is named; declaring
	// it together with `run` is refused.
	Main string `json:"main,omitempty"`
	// Run is any other toolchain's build command, run by `sh -c` in this entry's
	// image, and it selects the OTHER LANE. Arbitrary shell is the point — it is
	// the same trust as a Dockerfile RUN — which is why it executes with no
	// object-store credential and no service-account token. It requires `out`.
	Run string `json:"run,omitempty"`
	// Out is the glob of files `run` produced, relative to the repo root; matching
	// nothing FAILS the build rather than publishing an empty entry. It expands
	// unquoted, so it is bounded to path and glob characters. The Go lane names
	// its own files and ignores this.
	Out string `json:"out,omitempty"`
	// Ldflags are the Go linker flags, `-s -w` when the recipe names none, on one
	// line. Go lane only.
	Ldflags string `json:"ldflags,omitempty"`
	// Platforms are the `<os>/<arch>` pairs the Go lane cross-compiles, [linux/amd64]
	// by default. Each one publishes as `<name>-<os>-<arch>`, which is the shape a
	// host resolves a binary BY — so the list is what a caller can ask for later.
	Platforms []string `json:"platforms,omitempty"`
	// Image is the toolchain image the recipe runs in, a Go bookworm image by
	// default. It is the one field the GitHub lane ignores: there the runner IS
	// the toolchain, and a cluster has to be told what a runner already is.
	Image string `json:"image,omitempty"`
}

const (
	// defaultToolchainImage builds the Go lane with no `image:` declared. Debian
	// bookworm, not alpine: the publisher's `curl --aws-sigv4` and a repo's own
	// cgo builds both need a real userland. Pinned by digest: a floating tag is a
	// toolchain nobody chose. A module that asks for a newer Go than this gets it
	// through GOTOOLCHAIN=auto (recipeEnv) rather than failing.
	defaultToolchainImage = "docker.io/library/golang:1.27.1-bookworm@sha256:69a7b9788769bec032d238959b61854e9ae87f57be9029ec04e9885fabf99195"

	// publishImage runs artifactPublishScript ONLY. curl (SigV4, the same PUT the
	// ci reusable makes) + busybox sha256sum, and nothing a build can influence.
	publishImage = "docker.io/curlimages/curl:8.11.1"

	// defaultArtifactBucket is where `bucket:` publishes when the recipe names
	// none — the same bucket hanzoai/ci defaults its plugin artifacts to.
	defaultArtifactBucket = "plugins"

	// maxArtifactBinaries bounds one request: each entry is an initContainer, and
	// a Job with an unbounded init chain is a pod that never schedules.
	maxArtifactBinaries = 16
)

// artifactNameRE is an artifact name that is safe as a FILENAME and as a URL
// path segment: no slash, no dot-dot, no shell or CSV metacharacter.
var artifactNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// platformRE is a Go os/arch pair (linux/amd64). Lowercase alphanumerics only —
// it is split on '/' and substituted into a filename.
var platformRE = regexp.MustCompile(`^[a-z0-9]+/[a-z0-9]+$`)

// bucketRE is an S3 bucket name; it becomes a URL path segment.
var bucketRE = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,62}$`)

// validate normalizes one recipe entry and rejects anything that could not be a
// filename, a URL segment, or a Go package path. `run` is deliberately NOT
// pattern-checked — it is a build command, and it executes with no credential
// (see the package comment).
func (b *binarySpec) validate() error {
	b.Name = strings.TrimSpace(b.Name)
	b.Main = strings.TrimSpace(b.Main)
	b.Run = strings.TrimSpace(b.Run)
	b.Out = strings.TrimSpace(b.Out)
	b.Image = strings.TrimSpace(b.Image)
	if !artifactNameRE.MatchString(b.Name) {
		return fmt.Errorf("binaries[].name must match %s", artifactNameRE)
	}
	if b.Run != "" {
		if b.Main != "" {
			return fmt.Errorf("%s: declare main: (the Go lane) or run: (any other), never both", b.Name)
		}
		if b.Out == "" {
			return fmt.Errorf("%s: run: must also declare out: — the glob of files it produces", b.Name)
		}
		// out is a shell glob, so it stays unquoted in the script. Bound it to
		// path characters + the glob metacharacters, so it can expand but can
		// never become a second command.
		if strings.ContainsAny(b.Out, " \t\r\n;&|$`(){}<>\\'\"") {
			return fmt.Errorf("%s: out: must be a plain glob of path characters", b.Name)
		}
	} else {
		if b.Main == "" {
			b.Main = "."
		}
		if b.Main != "." && !strings.HasPrefix(b.Main, "./") {
			return fmt.Errorf("%s: main: must be a repo-relative Go package (\".\" or \"./cmd/x\")", b.Name)
		}
		if strings.Contains(b.Main, "..") || strings.ContainsAny(b.Main, " \t\r\n;&|$`(){}<>*?[]\\'\"") {
			return fmt.Errorf("%s: main: contains disallowed characters", b.Name)
		}
		if b.Ldflags == "" {
			b.Ldflags = "-s -w"
		}
		if strings.ContainsAny(b.Ldflags, "\r\n") {
			return fmt.Errorf("%s: ldflags must be a single line", b.Name)
		}
		if len(b.Platforms) == 0 {
			b.Platforms = []string{"linux/amd64"}
		}
		for _, p := range b.Platforms {
			if !platformRE.MatchString(p) {
				return fmt.Errorf("%s: platform %q must be <os>/<arch>", b.Name, p)
			}
		}
	}
	if b.Image == "" {
		b.Image = defaultToolchainImage
	}
	img, err := validateImageRef(normalizeImageRef(b.Image))
	if err != nil {
		return fmt.Errorf("%s: toolchain image: %w", b.Name, err)
	}
	b.Image = img
	return nil
}

// artifactBuildScript is the recipe interpreter — the in-cluster twin of the
// `Build & publish binaries` step in hanzoai/ci's reusable, reading the same two
// lanes off the same declaration. It runs ONCE per `binaries:` entry, in that
// entry's toolchain image, with the entry's fields in the environment.
//
// It clones with `git init` + a depth-1 fetch of the exact ref rather than
// `git clone --branch`, because the ref is usually a COMMIT and a shallow clone
// cannot name one.
//
// CGO_ENABLED=0 -trimpath is not a preference (it is the ci lane's rule too): an
// artifact is installed on whatever host fetches it, so it may not link this
// image's glibc, and its digest must be a function of the source rather than of
// the checkout path.
const artifactBuildScript = `set -eu
mkdir -p /w/src /w/dist
cd /w/src
if [ ! -d .git ]; then
  git init -q
  git remote add origin "$REPO_URL"
  git -c protocol.version=2 fetch -q --depth 1 origin "$REF"
  git checkout -q FETCH_HEAD
fi
if [ -n "${RUN:-}" ]; then
  echo "== $NAME: ${RUN:-}"
  sh -c "${RUN:-}"
  n=0
  for f in ${OUT:-}; do
    [ -f "$f" ] || continue
    b="$(basename "$f")"
    [ "$f" -ef "/w/dist/$b" ] || cp "$f" /w/dist/
    # THE FILENAME DECIDES, when it carries the triple.
    #
    # A wheel or a tarball is not per-platform, so the recipe's own name and
    # "any" are the honest answer for it. But a recipe that emits
    # <name>-<os>-<arch> — the shape the Go lane below writes, and the shape
    # manifest/release.go resolves a plugin BY — is naming its platform, and
    # recording the recipe name and "any" for it makes the index unreadable:
    # every file lands under one name and a host asking for (o11y, linux,
    # amd64) matches nothing. One naming convention, either lane.
    fos=any; farch=any; fname="$NAME"; stem="${b%.exe}"
    case "$stem" in
      *-linux-amd64|*-linux-arm64|*-darwin-amd64|*-darwin-arm64|*-windows-amd64|*-windows-arm64)
        farch="${stem##*-}"; rest="${stem%-*}"; fos="${rest##*-}"; fname="${rest%-*}" ;;
    esac
    printf '%s\t%s\t%s\t%s\n' "$b" "$fos" "$farch" "$fname" >> /w/meta.txt
    n=$((n+1))
  done
  [ "$n" -gt 0 ] || { echo "$NAME: out: '${OUT:-}' matched no file the recipe produced"; exit 1; }
else
  for plat in ${PLATFORMS:-}; do
    os="${plat%%/*}"; arch="${plat##*/}"
    f="$NAME-$os-$arch"
    case "$os" in windows) f="$f.exe";; esac
    echo "== $NAME: go build ${MAIN:-} -> $f ($os/$arch)"
    CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" GOFLAGS=-mod=mod \
      go build -trimpath -ldflags "${LDFLAGS:-}" -o "/w/dist/$f" "${MAIN:-}"
    printf '%s\t%s\t%s\t%s\n' "$f" "$os" "$arch" "$NAME" >> /w/meta.txt
  done
fi
ls -l /w/dist
`

// artifactPublishScript is the ONLY thing that ever sees the object-store
// credential. Its index entry is field-for-field the ci lane's — `name` is the
// RECIPE entry's name (what a host asks for; `zip.Load(zip.Plugin{Name})`), NOT
// the file name, which the url already carries. It hashes each artifact, PUTs
// it, and writes binaries.json LAST —
// the index is the one file a host reads, so between the two writes it must
// never name an object that is not there yet (the ci lane's rule, kept).
//
// Every PUT carries x-amz-checksum-sha256, so the store refuses bytes that do
// not hash to the index (400 BadDigest) and keeps the sum. It ends by asking the
// store, with the same credential, for each artifact's stored sha256 (HEAD, no
// download) and for the index's full bytes. A missing, unreadable or unsummed
// object, or a sum that is not the index's, fails the publish. That proves what
// is stored, not who can read it: the bucket is private, and no host has a
// credential for it yet (manifest/release.go and zip's fetch read anonymously),
// which LLM.md carries as the open item.
//
//go:embed artifact_publish.sh
var artifactPublishScript string

// artifactS3Secret is the Secret in the build namespace holding the object-store
// credential (S3_ADMIN_ACCESS_KEY / S3_ADMIN_SECRET_KEY), synced from KMS by the
// KMS operator — cloud holds NO secrets grant in the build namespace (the R6
// fix), so it references this Secret and never writes one. Marked optional so a
// cluster without it still schedules the build; the publisher then fails loudly
// on an unauthenticated PUT rather than the Job being unschedulable.
const artifactS3Secret = "artifact-s3"

// The forge read credential this lane presents is forgeTokenSecret (k8s.go) —
// the same Secret the BuildKit lane fetches its git context with, because it is
// the same authority against the same host. It is mounted on the `prepare`
// container alone — never on a recipe container, and never on the publisher.

// artifactPrepareHead is the first half of artifactPrepareScript: the clone, in a
// file of its own because it is shell, and shell is not SQL.
//
//go:embed artifact_prepare.sh
var artifactPrepareHead string

// artifactPrepareScript puts the SOURCE in /w/src and the MODULES in /w/go, and
// is the ONLY script that ever sees a credential.
//
// One step, because it is one job: fetch everything this build needs from the
// places that require a credential, so nothing after it needs one. A recipe's
// `run:` is arbitrary shell with the same trust as a Dockerfile RUN, and the
// whole design is that it inherits a ready workspace and an empty environment.
//
// Both halves genuinely need the token. The source is private —
// git.hanzo.ai/hanzoai/cloud redirects to hanzo-inc/cloud, which answers 401
// anonymously — and go.mod names private modules: a build without this reached
// 122 of 124 apps and died on zen, licensing and authz with "could not read
// Username for 'https://github.com'".
//
// ONE mechanism covers both, and it is the ENVIRONMENT rather than a file.
// GIT_CONFIG_COUNT is read by every git invocation in this container, including
// the ones `go mod download` makes internally — which is why the per-call `-c`
// this used to rely on cannot serve the module half — and it writes nothing to a
// .gitconfig, so the token never lands on /w, the volume every recipe container
// reads. Nothing to place correctly, nothing to clean up.
//
//	KEY_0    authenticates every request to the forge: the clone, and the module
//	         fetches it serves
//	KEY_1..  one per github.com/hanzoai/* repository go.sum names, written by
//	         artifactModuleRoutes
//
// GOPATH is the recipe containers' own, so the cache this writes is GOPATH/pkg/mod
// as they read it. `go mod download` with no argument is this module's build
// list; `all` drags in test dependencies of dependencies that nothing here links.
// Modules resolve by moduleEnv, the same environment the image build states, so
// the proxy serves what it can and git fetches only what it refuses.
//
// Measured in-cluster against the real repo: clone plus download, 54s, 4.4 GB.
var artifactPrepareScript = artifactPrepareHead + artifactModuleRoutes + `
export GOPATH=/w/go ` + shellWords(moduleEnv) + `
go mod download
echo "prepared: source in /w/src, modules in /w/go"
`

// moduleEnv is how a build resolves Go modules, and there is one way.
//
// It is the environment the image build states in the Dockerfile's ENV, and
// TestPrepareResolvesModulesAsTheImageBuildDoes holds the two equal. The proxy
// comes first because it is the only copy that survives a repository being
// re-rooted: a module may pin a pseudo-version commit that
// only refs/archive and proxy.golang.org still hold. GOPRIVATE bypasses the
// checksum database, which has no entry for a module we publish, and GONOPROXY
// undoes the other half of that shorthand so hanzoai modules still use the
// proxy. A module the proxy does not serve, for any error from it (`|`, not `,`),
// falls back to git, where
// artifactModuleRoutes decides the host. -mod=readonly makes the committed go.sum
// the only source of truth.
var moduleEnv = []string{
	"GOPRIVATE=github.com/hanzoai/*,github.com/hanzo-inc/*,zap-proto.io/*",
	"GONOPROXY=none",
	"GOPROXY=https://proxy.golang.org|direct",
	"GOFLAGS=-mod=readonly",
}

// shellWords renders NAME=value pairs as single-quoted shell words.
func shellWords(kv []string) string {
	q := make([]string, len(kv))
	for i, s := range kv {
		q[i] = "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
	}
	return strings.Join(q, " ")
}

// artifactModuleRoutes decides, per repository, where each github.com/hanzoai/*
// module go.sum names is fetched from.
//
// The forge serves a hanzoai module only when it holds the repository AND
// everything go.sum pins from it: every release tag, and every commit a
// pseudo-version names. Otherwise the module comes from github.com, where every
// hanzoai repository the build list names is public. A forge mirror is a pull
// that lags a new tag or commit, a copy can diverge from GitHub, and a
// repository nobody mirrored is not there at all, so sending the whole prefix to
// the forge failed the build on the first module it did not hold.
//
// A module below the repository root (github.com/hanzoai/git/modules/actions/
// runner) is released by the tag `<subdir>/vX.Y.Z`, so that is the tag checked.
// A commit is checked through the forge API (FORGE_API), because a tag list
// cannot say whether an arbitrary commit exists.
//
// Every repository gets its own entry, including the ones that stay on GitHub,
// because git applies the LONGEST matching insteadOf by string prefix: without
// an entry of its own, github.com/hanzoai/agents would be rewritten by the entry
// for github.com/hanzoai/agent.
//
// It reads go.sum in the working directory, appends from the GIT_CONFIG_COUNT
// the caller set, and asks FORGE_URL and FORGE_API (git.hanzo.ai unless set)
// with the caller's credentials. A name that is not a plain repository name is
// skipped rather than written into git config.
//
//go:embed artifact_routes.sh
var artifactModuleRoutes string

// artifactBase is the PUBLISHED URL prefix for one build:
//
//	https://<public s3 host>/<bucket>/<owner>/<repo>/<tag>/
//
// byte-identical to the layout hanzoai/ci publishes to, so an artifact built by
// a push through GitHub and one built by the platform land at the same URL and a
// host reads ONE index either way. This is the URL RECORDED in binaries.json.
func artifactBase(bucket, repo, tag string) string {
	scheme := "https"
	if strings.EqualFold(env.Or("S3_PUBLIC_SECURE", "true"), "false") {
		scheme = "http"
	}
	host := strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(env.Or("S3_PUBLIC_ENDPOINT", "s3.hanzo.ai"), "https://"), "http://"), "/")
	return scheme + "://" + host + "/" + bucket + "/" + repo + "/" + tag
}

// artifactPutBase is the same prefix on the INTERNAL admin endpoint — where the
// bytes are actually written. The split is not an optimization: s3.hanzo.ai is
// this cluster's own LoadBalancer, and a pod dialling its own LB's public IP
// does not come back (no hairpin), so an in-cluster publisher MUST write through
// the Service. It is the identical internal-vs-public split internal/driver/s3
// already draws — control operations internal, published URLs public.
func artifactPutBase(bucket, repo, tag string) string {
	scheme := "http"
	if strings.EqualFold(env.Or("S3_SECURE", "false"), "true") {
		scheme = "https"
	}
	return scheme + "://" + env.Or("S3_ADMIN_ENDPOINT", "s3.hanzo.svc:9000") + "/" + bucket + "/" + repo + "/" + tag
}

// repoSlug reduces a validated clone URL to the <owner>/<repo> the publish path
// uses — the same `github.repository` the ci lane writes into that path.
func repoSlug(cloneURL string) string {
	s := strings.TrimSuffix(strings.Trim(strings.TrimPrefix(cloneURL, "https://"), "/"), ".git")
	parts := strings.Split(s, "/")
	if len(parts) < 3 {
		return ""
	}
	return parts[len(parts)-2] + "/" + parts[len(parts)-1]
}

// launchArtifactBuild launches the artifact Job and returns its name. It shares
// every bound the image lane has — the isolated build namespace, the CI pool,
// the per-org concurrency ceiling, and the same source rules — so an artifact
// build cannot outrun a container build or escape into another namespace.
// buildID is the idempotency key: a retry collides on the Job name (409) rather
// than spawning a duplicate.
func (k *k8sClient) launchArtifactBuild(ctx context.Context, org, repoURL, ref, tag, base, putBase string, bins []binarySpec, buildID string) (string, error) {
	if err := k.ready(); err != nil {
		return "", err
	}
	// The source a Job clones is checked where the Job is built, as it is in
	// launchBuildJob and launchDirectBuild: an allowlisted git host over https
	// carrying no credentials and no metacharacters, and a ref naming one
	// commit. Every constructor of a build states the rule, so a caller cannot
	// be the only thing holding it.
	cleanURL, err := validateRepoURL(repoURL)
	if err != nil {
		return "", fmt.Errorf("invalid build input: %w", err)
	}
	cleanRef, err := validateBuildRef(ref)
	if err != nil {
		return "", fmt.Errorf("invalid build input: %w", err)
	}
	if err := k.admitBuild(ctx, org); err != nil {
		return "", err
	}
	jobName := truncate("pf-artifact-"+jobIDSuffix(buildID), 63)
	job := k.artifactJobSpec(jobName, org, cleanURL, cleanRef, tag, base, putBase, bins)
	if _, err := k.createBuildJob(ctx, job); err != nil {
		return "", err
	}
	return jobName, nil
}

// envVar is one Job container env entry — the ONLY way a recipe value reaches a
// script (never by interpolating it into the script text).
func envVar(name, value string) any {
	return map[string]any{"name": name, "value": value}
}

// secretEnv is one env entry sourced from the object-store Secret. The KEY names
// are the ones the KMS entry already uses (access-key / secret-key, as in the
// hanzo-s3 Secret every service reads), so the build-namespace copy is a mirror
// of the same KMS path and not a second credential with a second spelling.
// optional: a cluster with no credential still schedules; the publish then fails
// loudly on an unauthenticated PUT rather than the Job being unschedulable.
func secretEnv(name, key string) any {
	return map[string]any{"name": name, "valueFrom": map[string]any{
		"secretKeyRef": map[string]any{"name": artifactS3Secret, "key": key, "optional": true},
	}}
}

// recipeEnv is what a recipe container is told, and the rule is ONE line long:
// state what the recipe DECLARED, and be silent about the rest.
//
// This exists because the same defect bit three times, each time as a different
// field, each time reported as success:
//
//   - the index recorded {os: any, arch: any} for every file, so 242 plugins
//     landed under one name and no host could resolve any of them;
//   - PLATFORMS was exported EMPTY for a run: recipe, and an empty variable is
//     still SET, which defeats a makefile's conditional default — "0 binaries
//     for 0 platforms", no error;
//   - and the same shape waits in LDFLAGS, MAIN and OUT for whoever declares a
//     recipe that reads them.
//
// Every one is the Job asserting a value it does not have. An unset variable
// lets the recipe's own default stand; an empty one overrides it with nothing,
// which is the one answer that is never true. So an undeclared field is OMITTED
// rather than sent empty, and the next field cannot repeat this on its own.
//
// The fixed values below are not recipe data: HOME/GOPATH/npm_config_cache are
// this Job's workspace, which the Job genuinely does know, and a non-root uid has
// no writable HOME in a toolchain image without them. GOTOOLCHAIN=auto undoes the
// golang image's `local`: a go.mod needing a newer Go than the image fetches it,
// where `local` refused the build ("go.mod requires go >= 1.27.1").
func recipeEnv(repoURL, ref string, b binarySpec) []any {
	out := []any{envVar("REPO_URL", repoURL), envVar("REF", ref), envVar("NAME", b.Name)}
	for _, kv := range [][2]string{
		{"MAIN", b.Main}, {"RUN", b.Run}, {"OUT", b.Out}, {"LDFLAGS", b.Ldflags},
		{"PLATFORMS", strings.Join(b.Platforms, " ")},
	} {
		if kv[1] != "" {
			out = append(out, envVar(kv[0], kv[1]))
		}
	}
	return append(out, envVar("HOME", "/w"), envVar("GOPATH", "/w/go"), envVar("npm_config_cache", "/w/.npm"), envVar("GOTOOLCHAIN", "auto"))
}

// confined is the security context of every container in the artifact Job. A
// recipe runs its own shell in an image it names, on the CI pool beside visor
// sandboxes and runners, and needs no capability and no setuid transition: the
// uid it starts as is the uid it keeps, whatever the image ships.
func confined() map[string]any {
	return map[string]any{
		"allowPrivilegeEscalation": false,
		"capabilities":             map[string]any{"drop": []any{"ALL"}},
	}
}

// artifactJobSpec renders the build: one initContainer per recipe entry (they
// run in order and share /w) followed by the publisher. The pod is non-root with
// fsGroup 1000 so the shared emptyDir is writable without any container being
// root, mounts NO service-account token, and runs in the same isolated namespace
// and on the same CI pool (placeBuild) as every other build. Unlike the image
// lane, whose rootless BuildKit needs seccomp and AppArmor unconfined, it runs
// under the runtime's default seccomp profile, named here because the kubelet
// applies none by default.
func (k *k8sClient) artifactJobSpec(jobName, org, repoURL, ref, tag, base, putBase string, bins []binarySpec) *unstructured.Unstructured {
	// [0] PREPARE: the one credentialed container. Everything after it runs with
	// a ready workspace and an empty environment — the whole reason it exists.
	//
	// It needs no change to artifactBuildScript: that script guards its fetch with
	// `[ ! -d .git ]`, so with the source already present every recipe skips it.
	inits := make([]any, 0, len(bins)+1)
	inits = append(inits, map[string]any{
		"name":            "prepare",
		"image":           defaultToolchainImage,
		"command":         []any{"/bin/sh", "-c", artifactPrepareScript},
		"workingDir":      "/w",
		"securityContext": confined(),
		"env": []any{
			envVar("REPO_URL", repoURL), envVar("REF", ref),
			// Required: the forge serves no repository anonymously, and the
			// module fetch is redirected onto it too (GIT_CONFIG_KEY_1). A pod
			// that cannot have the credential should say so by name.
			map[string]any{"name": "GIT_TOKEN", "valueFrom": map[string]any{
				"secretKeyRef": map[string]any{"name": forgeTokenSecret, "key": "token"},
			}},
		},
		"volumeMounts": []any{map[string]any{"name": "w", "mountPath": "/w"}},
	})
	for _, b := range bins {
		inits = append(inits, map[string]any{
			"name":            truncate("build-"+strings.ToLower(b.Name), 63),
			"image":           b.Image,
			"command":         []any{"/bin/sh", "-c", artifactBuildScript},
			"workingDir":      "/w",
			"securityContext": confined(),
			"env":             recipeEnv(repoURL, ref, b),
			"volumeMounts":    []any{map[string]any{"name": "w", "mountPath": "/w"}},
		})
	}
	job := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "batch/v1",
		"kind":       "Job",
		"metadata": map[string]any{
			"name":      jobName,
			"namespace": k.buildNS,
			"labels": map[string]any{
				"hanzo.ai/org":         org,
				"hanzo.ai/managed-by":  "platform",
				"hanzo.ai/application": "runner",
				"hanzo.ai/build":       "true",
			},
		},
		"spec": map[string]any{
			"suspend":                 true, // the Job is its place in the line (dispatch.go)
			"backoffLimit":            int64(0),
			"podFailurePolicy":        lostNodeRetries(),
			"ttlSecondsAfterFinished": int64(3600),
			"template": map[string]any{
				// hanzo.ai/publish=artifact is what the artifact-publish-egress
				// CiliumNetworkPolicy selects (a Cilium selector matches a POD, so
				// it has to be here and not on the Job): the build namespace denies
				// every internal CIDR, and the publisher needs the s3 Service.
				"metadata": map[string]any{"labels": map[string]any{
					"hanzo.ai/org": org, "hanzo.ai/build": "true", "hanzo.ai/publish": "artifact",
				}},
				"spec": map[string]any{
					"restartPolicy": "Never",
					// Builds yield. hanzo-ci sits below hanzo-infra-critical and
					// hanzo-money-critical, so a queue of them can never push a serving
					// workload off a node, and raising a single run's class is how one
					// build gets moved to the front without stopping the others.
					"priorityClassName":            "hanzo-ci",
					"automountServiceAccountToken": false,
					"securityContext": map[string]any{
						"runAsUser": int64(1000), "runAsGroup": int64(1000),
						"runAsNonRoot": true, "fsGroup": int64(1000),
						"seccompProfile": map[string]any{"type": "RuntimeDefault"},
					},
					"initContainers": inits,
					"containers": []any{map[string]any{
						"name":            "publish",
						"image":           publishImage,
						"command":         []any{"/bin/sh", "-c", artifactPublishScript},
						"workingDir":      "/w",
						"securityContext": confined(),
						"env":             []any{envVar("BASE", base), envVar("PUT_BASE", putBase), envVar("REPO", repoSlug(repoURL)), envVar("TAG", tag), secretEnv("S3_ADMIN_ACCESS_KEY", "access-key"), secretEnv("S3_ADMIN_SECRET_KEY", "secret-key"), envVar("S3_REGION", env.Or("S3_REGION", "us-east-1"))},
						"volumeMounts":    []any{map[string]any{"name": "w", "mountPath": "/w"}},
					}},
					"volumes": []any{map[string]any{"name": "w", "emptyDir": map[string]any{"sizeLimit": artifactWorkspaceLimit}}},
				},
			},
		},
	}}
	if nativeForgeBuilds() {
		pod := podSpec(job.Object)
		prepare := pod["initContainers"].([]any)[0].(map[string]any)
		prepare["env"] = []any{envVar("REPO_URL", repoURL), envVar("REF", ref)}
		prepare["volumeMounts"] = append(prepare["volumeMounts"].([]any), forgeIdentityMount(true))
		prepare["command"] = []any{"/bin/sh", "-c", "GIT_TOKEN=\"$(cat " + readerTokenPath + ")\"\n" + artifactPrepareScript}
		addForgeIdentity(pod)
	}
	// No architecture is asked for, because none is needed. This lane
	// cross-compiles — CGO_ENABLED=0 with GOOS and GOARCH from the request — and
	// both images it runs, the golang toolchain and the curl publisher, are
	// manifest indexes. The output is byte-identical whichever machine produced
	// it, so the pod takes whichever CI pool node has room.
	placeBuild(podSpec(job.Object), "")
	return job
}
