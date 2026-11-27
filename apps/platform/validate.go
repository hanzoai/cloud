// validate.go — the boundary validators + resource-bound policy for /v1/platform.
//
// Three concerns, one home (DRY), because all three feed the SAME privileged
// cluster path (a BuildKit Job + operator Service CR in tenant-<org>):
//
//  1. build-input validation — repo.url / dockerfile / git-ref are the ONLY
//     free-form strings that flow into the in-cluster build. They are validated
//     here so they can never be misread as a separate buildctl argument, a shell
//     token, a path escape, or a git-context fragment break. Combined with the
//     exec-form argv in launchBuildJob (no `sh -c`), this closes OS command
//     injection AND the `--output`/`--opt` override class (CRIT-1).
//  2. replica bounds — clampReplicas caps a tenant's requested replica count so a
//     single app cannot request an unbounded Deployment (part of MED-3).
//  3. namespace resource bounds — a ResourceQuota + LimitRange are applied to
//     every tenant namespace so a tenant's TOTAL footprint is capped in the
//     cluster regardless of what slipped past the per-object clamp (defense in
//     depth for MED-3). Builds are not bounded here: they wait their turn, under
//     ceilings an operator sets in admin.hanzo.ai (dispatch.go).
//
// Every bound is a hard-coded safe default, operator-overridable via env — the
// values are resolved ONCE at k8sClient construction so a request path never
// re-reads the environment and the tests are deterministic.

package platform

import (
	"fmt"
	"maps"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/hanzoai/cloud/sdk/env"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// ── build-input validation (CRIT-1) ──────────────────────────────────────────

// defaultGitProviderHosts is the built-in allowlist of git apexes a build
// context may be fetched from (mirrors remote.Provider's recognized set). An
// allowlist — not a denylist — is the safe default: an unknown host is refused,
// never fetched. An exact apex OR a subdomain of one is accepted (so
// codeload.github.com under github.com is allowed).
var defaultGitProviderHosts = []string{"github.com", "gitlab.com", "bitbucket.org", "codeberg.org"}

// gitProviderHosts is the effective allowlist, resolved ONCE at package init:
// the operator may REPLACE it via CLOUD_PLATFORM_GIT_HOSTS (comma-separated
// apexes) for a self-hosted git provider, else the built-in set applies.
var gitProviderHosts = resolveGitHosts()

// selfGitHost is the cloud's OWN embedded-git apex (cloud.Domain()). The cloud
// serves its repos there (clients/git), so its own clone URLs are ALWAYS a
// trusted build source and git-push-to-deploy works with no extra config. Set
// once at platform Mount; an orthogonal trust from the external-provider
// allowlist above.
var selfGitHost string

func resolveGitHosts() []string {
	raw := env.Or("CLOUD_PLATFORM_GIT_HOSTS", "")
	if raw == "" {
		return defaultGitProviderHosts
	}
	var out []string
	for h := range strings.SplitSeq(raw, ",") {
		if h = strings.ToLower(strings.TrimSpace(h)); h != "" {
			out = append(out, h)
		}
	}
	if len(out) == 0 {
		return defaultGitProviderHosts
	}
	return out
}

// gitRefRE is a strict, safe git ref: a branch/tag/commit expressed only with
// [A-Za-z0-9._/-]. It deliberately EXCLUDES every character with meaning to a
// shell, to buildctl's flag parser, or to the git-context fragment we build
// (`<url>.git#<ref>`): no whitespace, no ';', '&', '|', '$', '`', '(', ')',
// '<', '>', '#', '~', '^', ':', '?', '*', '[', ']', '\\', '\”. Leading '-' is
// excluded so a ref can never be misread as a flag. Length-bounded so a ref can
// never be a smuggling channel.
var gitRefRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,254}$`)

// hexSHARE matches a 7–64 char lowercase-or-mixed hex commit SHA (the common
// case). It is a subset of gitRefRE but kept explicit for clarity.
var hexSHARE = regexp.MustCompile(`^[A-Fa-f0-9]{7,64}$`)

// dockerfilePathRE is a safe RELATIVE path to a Dockerfile inside the repo: one
// or more [A-Za-z0-9._-] segments joined by single '/'. No leading '/', no
// drive/scheme, and (checked separately) no ".." segment — so it can never
// escape the build context or be misread as a flag/shell token.
var dockerfilePathRE = regexp.MustCompile(`^[A-Za-z0-9._-]+(/[A-Za-z0-9._-]+)*$`)

// ociRefRE is the strict grammar of a single OCI image reference:
//
//	<host>[:port]/<path-component>[/<path-component>...][:<tag>][@<digest>]
//
// It is an ALLOWLIST of exactly the characters legal in a reference and NOTHING
// else — so an `image` value can never smuggle a second BuildKit exporter
// attribute. The output is emitted as `--output type=image,name=<image>,push=true`,
// a CSV of key=value attrs; a comma or space in <image> would inject an attr
// (`name=…,registry.insecure=true`) or split the argv element. The domain is a
// dot-separated set of components with an optional port; each path component is
// lowercase alphanumerics with `.`/`_`/`-` separators (the distribution-spec
// `remote-name` grammar); the optional tag is `[\w][\w.-]{0,127}`; the optional
// digest is `<alg>:<hex>`. No comma, space, quote, newline, '=', or shell/CSV
// metacharacter can match (M1). A build that writes several names emits them as
// ONE quoted field, `"name=<a>,<b>"` (imageNames), which is sound for the same
// reason: no name can close the quote or open an attribute of its own.
var ociRefRE = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]*[A-Za-z0-9])?(?:\.[A-Za-z0-9](?:[A-Za-z0-9-]*[A-Za-z0-9])?)*(?::[0-9]+)?(?:/[a-z0-9]+(?:(?:[._]|__|[-]+)[a-z0-9]+)*)+(?::[A-Za-z0-9_][A-Za-z0-9_.-]{0,127})?(?:@[A-Za-z0-9]+:[A-Fa-f0-9]{32,})?$`)

// validateImageRef enforces that the build OUTPUT image is a single, well-formed
// OCI reference with no exporter-CSV / argv-injection surface. `image` is the 4th
// attacker-controlled input to the privileged build (alongside repo.url /
// dockerfile / git-ref) and the ONLY one that reaches buildFrontendCmd's
// `--output type=image,name=<image>,push=true`. A prefix (owned-registry) check is
// NOT structural validation — imageAllowed("ghcr.io/hanzoai/x,registry.insecure=true")
// is a prefix match yet injects an exporter attribute — so the value is validated
// here to a strict single ref. Returns the cleaned (trimmed) ref or an error.
func validateImageRef(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", fmt.Errorf("image is required")
	}
	if len(s) > 512 {
		return "", fmt.Errorf("image ref is too long")
	}
	// Belt (explicit) before the suspenders (grammar): no control char, no
	// whitespace, and none of the CSV/shell metacharacters — most importantly ','
	// and '=' (the exporter-attribute separators) and quotes/newlines. This is the
	// exact injection class the ociRefRE allowlist also rejects; naming it makes the
	// intent legible and the failure loud.
	if strings.ContainsAny(s, " \t\r\n,=;&|$`(){}<>*?[]\\'\"^~#!") {
		return "", fmt.Errorf("image ref contains disallowed characters")
	}
	if !ociRefRE.MatchString(s) {
		return "", fmt.Errorf("image must be a single valid OCI reference")
	}
	return s, nil
}

// normalizeImageRef qualifies a PULL ref the way every container runtime does
// before validateImageRef judges it: `golang:1.26` → `docker.io/library/golang:1.26`,
// `curlimages/curl:8` → `docker.io/curlimages/curl:8`. ociRefRE requires a domain
// (correctly — an output image with no registry has nowhere to push), but a
// toolchain image a build PULLS is normally written the short way, and refusing
// `node:22` would make the recipe lie about what docker accepts. A first
// component containing '.' or ':' — or literally "localhost" — already IS a
// domain and is left alone.
func normalizeImageRef(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return s
	}
	head, _, hasPath := strings.Cut(s, "/")
	if hasPath && (head == "localhost" || strings.ContainsAny(head, ".:")) {
		return s
	}
	if hasPath {
		return "docker.io/" + s
	}
	return "docker.io/library/" + s
}

// validateRepoURL enforces that a git repo URL is a real https URL to an
// allowlisted host with no shell/flag metacharacters. The build context is
// derived from this string, so this is the injection guard for CRIT-1. Returns
// the cleaned URL (trimmed) or an error naming the exact violation.
func validateRepoURL(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", fmt.Errorf("repo.url is required")
	}
	// No control chars, no whitespace, no shell metacharacters anywhere. This is
	// belt-and-suspenders: the argv form already prevents shell evaluation, but a
	// URL containing these is never legitimate and we refuse it loudly.
	if strings.ContainsAny(s, " \t\r\n;&|$`(){}<>*?[]\\'\"^~#") {
		return "", fmt.Errorf("repo.url contains disallowed characters")
	}
	u, err := url.Parse(s)
	if err != nil {
		return "", fmt.Errorf("repo.url is not a valid URL: %v", err)
	}
	if u.Scheme != "https" {
		return "", fmt.Errorf("repo.url must use https (got %q)", u.Scheme)
	}
	if u.User != nil {
		return "", fmt.Errorf("repo.url must not embed credentials")
	}
	host := strings.ToLower(u.Hostname())
	if host == "" {
		return "", fmt.Errorf("repo.url has no host")
	}
	if !hostAllowed(host) {
		return "", fmt.Errorf("repo.url host %q is not an allowed git provider", host)
	}
	return s, nil
}

// hostAllowed reports whether host exactly matches, or is a subdomain of, the
// cloud's own embedded-git apex or an allowlisted external git provider apex.
func hostAllowed(host string) bool {
	if selfGitHost != "" && (host == selfGitHost || strings.HasSuffix(host, "."+selfGitHost)) {
		return true
	}
	for _, allowed := range gitProviderHosts {
		if host == allowed || strings.HasSuffix(host, "."+allowed) {
			return true
		}
	}
	return false
}

// validateDockerfile enforces a safe relative Dockerfile path. Empty is allowed
// by the caller (it defaults to "Dockerfile"); this validates a NON-empty value.
func validateDockerfile(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", fmt.Errorf("dockerfile path is empty")
	}
	if !dockerfilePathRE.MatchString(s) {
		return "", fmt.Errorf("dockerfile must be a relative path of [A-Za-z0-9._-] segments")
	}
	if slices.Contains(strings.Split(s, "/"), "..") {
		return "", fmt.Errorf("dockerfile path must not contain '..'")
	}
	return s, nil
}

// stageRE is a Dockerfile build stage name as buildkit accepts one: a letter,
// then letters, digits, '.', '_' or '-'. buildkit folds stage names to lower
// case, so either case selects the same stage.
var stageRE = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9._-]{0,127}$`)

// validateTarget checks the Dockerfile stage a build stops at. Empty builds the
// last stage.
func validateTarget(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if s != "" && !stageRE.MatchString(s) {
		return "", fmt.Errorf("dockerTarget must be a Dockerfile stage name")
	}
	return s, nil
}

// validateContextDir cleans the directory of the repository that is the build
// context. Empty and "." are the repository root, which needs no directory.
func validateContextDir(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", nil
	}
	s = path.Clean(s)
	if s == "." {
		return "", nil
	}
	if !dockerfilePathRE.MatchString(s) || slices.Contains(strings.Split(s, "/"), "..") {
		return "", fmt.Errorf("context must be a relative directory inside the repository")
	}
	return s, nil
}

// ── target platforms ─────────────────────────────────────────────────────────

// nativeArch maps a build platform to the node architecture that runs it without
// emulation. A platform is listed here when the fleet HAS a node of that
// architecture, and a platform absent from it is refused rather than emulated.
//
// The refusal is the point. Both nodes register qemu-binfmt with the F flag, so
// an unlisted platform would build — slowly, correctly, and invisibly. That is
// the worst of the three outcomes: a build nobody can tell from a native one
// except by the clock. Measured on our own images, emulation is not a tax we can
// absorb: cloud, iam, ci, team, sql and base declare no $BUILDPLATFORM stage, and
// cloud's plugin stage is CGO_ENABLED=1 against libsqlcipher, so a foreign
// platform there is 118 emulated C-linked compiles rather than a cross-compile.
// One node per architecture is what we have and what this map spends.
var nativeArch = map[string]string{
	"linux/amd64": "amd64",
	"linux/arm64": "arm64",
}

// buildPlatforms validates the requested target platforms and returns them in a
// stable order. An empty request means EVERY architecture the fleet has.
//
// The default used to be one platform, and one platform is what nearly every
// caller asks for, so 144 of the 185 images the estate runs carry amd64 alone
// against two nodes — one amd64, one arm64. An image with no arm64 half does not
// fail honestly there: where it is an index containerd refuses it outright, but
// where it is a bare manifest there is no platform check at all, so the node's
// qemu-binfmt runs the foreign binary until it faults. Measured on the fleet that
// is work-board at 678 restarts and exchange-api at 617, and the half of the
// cluster that cannot run our images sits idle. A default that has to be
// remembered was remembered 41 times out of 185.
//
// Sorted and deduplicated for the reason buildArgs is sorted — the platform list
// is part of the build's identity, and two requests naming the same platforms in
// two orders must not be two builds.
func buildPlatforms(ps []string) ([]string, error) {
	if len(ps) == 0 {
		return slices.Sorted(maps.Keys(nativeArch)), nil
	}
	out := make([]string, 0, len(nativeArch))
	for _, p := range ps {
		p = strings.TrimSpace(p)
		if _, ok := nativeArch[p]; !ok {
			return nil, fmt.Errorf("platform %q has no node to build it on (have %s)", p, strings.Join(slices.Sorted(maps.Keys(nativeArch)), ", "))
		}
		if !slices.Contains(out, p) {
			out = append(out, p)
		}
	}
	slices.Sort(out)
	return out, nil
}

// ── extra tags ───────────────────────────────────────────────────────────────

// tagRE is one OCI tag: the distribution-spec grammar, the same one ociRefRE
// accepts after a colon. It admits no ',', '"', '=', '@', '/' or whitespace, so
// a tag can neither split the exporter's name list nor name a digest or a
// different repository.
var tagRE = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$`)

// maxTags bounds how many more names one build writes. A release is a version,
// its v-prefixed spelling and latest; eight leaves room for more without making
// the exporter's name list unbounded.
const maxTags = 8

// imageTags validates the tags a build writes beside image and returns them in
// the order asked, duplicates and image's own tag removed.
//
// A tag names a ref in image's own repository and nowhere else (tagRef), so it
// reaches no repository, namespace or registry that image does not, and the push
// credential derived from image covers every one. Each composed ref is still held
// to validateImageRef: every name is joined into ONE exporter attribute
// (imageNames), and M1 is a property of each name, not of how it was composed.
//
// A digest names an image that already exists, and there is nothing to write a
// tag beside, so a digest image with tags is refused rather than either ignored.
func imageTags(image string, tags []string) ([]string, error) {
	if len(tags) == 0 {
		return nil, nil
	}
	if len(tags) > maxTags {
		return nil, fmt.Errorf("at most %d tags per build (got %d)", maxTags, len(tags))
	}
	if strings.Contains(image, "@") {
		return nil, fmt.Errorf("tags need image to be a tag ref, not a digest")
	}
	_, own := splitImageRef(image)
	var out []string
	for _, t := range tags {
		if !tagRE.MatchString(t) {
			return nil, fmt.Errorf("tag %q is not an OCI tag", t)
		}
		if t == own || slices.Contains(out, t) {
			continue
		}
		if _, err := validateImageRef(tagRef(image, t)); err != nil {
			return nil, fmt.Errorf("tag %q: %w", t, err)
		}
		out = append(out, t)
	}
	return out, nil
}

// tagRef names tag in image's repository.
func tagRef(image, tag string) string {
	repo, _ := splitImageRef(image)
	return repo + ":" + tag
}

// buildArgNameRE is a Dockerfile `ARG` name: the C-identifier shape docker
// itself accepts, and nothing else. A name is half of one `build-arg:K=V` argv
// element, so anything that could read as a second `=` or a leading dash is
// simply not a name.
var buildArgNameRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)

// buildArgs turns a declared `args:` map into ordered `build-arg:K=V` opts.
//
// SORTED, because a map has no order and two builds of one commit that pass the
// same args in two orders are two different cache keys — the thing the whole
// digest-pinned lane exists to prevent. Sorting makes the argv a function of the
// declaration alone.
//
// These are NOT a privilege boundary and the limits here do not pretend to be
// one: `args:` lives in hanzo.yml, in the repo, beside the Dockerfile that
// declares which ARGs exist. Whoever can add an arg can already edit the
// Dockerfile that reads it. What the limits close is the argv itself — a name
// carrying `=` or a leading `-`, or a value carrying a NUL or a newline, is how
// one opt becomes two.
func buildArgs(args map[string]string) ([]any, error) {
	if len(args) == 0 {
		return nil, nil
	}
	if len(args) > 64 {
		return nil, fmt.Errorf("too many build args (%d, max 64)", len(args))
	}
	names := slices.Sorted(maps.Keys(args))
	out := make([]any, 0, 2*len(names))
	for _, k := range names {
		if !buildArgNameRE.MatchString(k) {
			return nil, fmt.Errorf("build arg %q is not a valid ARG name", k)
		}
		v := args[k]
		if len(v) > 1024 {
			return nil, fmt.Errorf("build arg %q value is too long (%d, max 1024)", k, len(v))
		}
		if strings.ContainsAny(v, "\x00\n\r") {
			return nil, fmt.Errorf("build arg %q value contains a control character", k)
		}
		out = append(out, "--opt", "build-arg:"+k+"="+v)
	}
	return out, nil
}

// validateGitRef enforces a safe git ref/commit that can be placed into the
// `#<ref>` fragment of a buildctl git context without breaking it or smuggling a
// flag/shell token. Empty is allowed by the caller (it defaults to the branch);
// this validates a NON-empty value.
func validateGitRef(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", fmt.Errorf("git ref is empty")
	}
	if hexSHARE.MatchString(s) {
		return s, nil
	}
	if !gitRefRE.MatchString(s) {
		return "", fmt.Errorf("git ref must match a safe branch/tag/commit pattern")
	}
	if strings.Contains(s, "..") {
		return "", fmt.Errorf("git ref must not contain '..'")
	}
	return s, nil
}

// validateBuildInputs validates the FOUR free-form strings that reach the
// privileged BuildKit Job. url + image are required; dockerfile and ref are
// optional (empty ⇒ caller substitutes a safe default) but validated when
// present. Returns cleaned values. This is the ONE choke point every build call
// funnels through (createApp validates url+dockerfile early for a 400;
// launchBuildJob/launchDirectBuild validate all four so a hostile persisted row,
// a deploy-body ref, OR a caller-supplied output image still fails closed). The
// image is validated here — not merely prefix-checked at the handler — so the
// `--output` exporter attribute-injection class is closed on the privileged path
// itself (M1), matching the url/dockerfile/ref treatment.
// validateBuildRef is validateGitRef plus the one rule a BUILD adds: a hex ref must
// be a whole commit.
//
// Separate from validateGitRef because an abbreviated sha is a perfectly good ref in
// general — it just cannot build. buildkit resolves a git source by exact object, so
// a short sha dies nine seconds in with "repository does not contain ref"; and
// REVISION is stamped only for a full commit, so an image built from one could never
// name itself and /v1/health would answer revision "unknown" forever — which makes
// the deployed commit unreadable at exactly the moment it is worth reading.
//
// A BRANCH NAME IS NOT CAUGHT. A branch is a legitimate build context and its image
// honestly reports no revision; only a value that looks like a commit and is not one
// is a mistake. Both build endpoints call this, so the rule has one home.
func validateBuildRef(raw string) (string, error) {
	s, err := validateGitRef(raw)
	if err != nil {
		return "", err
	}
	if hexSHARE.MatchString(s) && len(s) < 40 {
		return "", fmt.Errorf("git ref %q is an abbreviated commit; pass the full 40-character sha", s)
	}
	return s, nil
}

func validateBuildInputs(rawURL, dockerfile, ref, image string) (cleanURL, cleanDockerfile, cleanRef, cleanImage string, err error) {
	if cleanURL, err = validateRepoURL(rawURL); err != nil {
		return "", "", "", "", err
	}
	if strings.TrimSpace(dockerfile) != "" {
		if cleanDockerfile, err = validateDockerfile(dockerfile); err != nil {
			return "", "", "", "", err
		}
	}
	if strings.TrimSpace(ref) != "" {
		if cleanRef, err = validateBuildRef(ref); err != nil {
			return "", "", "", "", err
		}
	}
	if cleanImage, err = validateImageRef(image); err != nil {
		return "", "", "", "", err
	}
	return cleanURL, cleanDockerfile, cleanRef, cleanImage, nil
}

// ── resource bounds (MED-3) ───────────────────────────────────────────────────

// tenantQuotaName / tenantLimitRangeName are the fixed names of the per-tenant
// bound objects ensureNamespace maintains in each tenant namespace.
const (
	tenantQuotaName      = "tenant-quota"
	tenantLimitRangeName = "tenant-limits"
)

// resourceLimits is the resolved, per-tenant resource policy. All fields are set
// once from the environment (with safe defaults) at k8sClient construction, so
// the deploy/build path never re-reads env and tests are deterministic.
type resourceLimits struct {
	maxReplicas     int    // per-app replica ceiling
	maxStorageGB    int    // per-app persistent volume ceiling, in GiB
	maxDeploys      int    // concurrent in-flight SYNCHRONOUS image deploys per org (L1)
	maxRunners      int    // concurrent runner Jobs, CLUSTER-wide (job.go)
	quotaCPU        string // ResourceQuota: total requests.cpu / limits.cpu
	quotaMemory     string // ResourceQuota: total requests.memory / limits.memory
	quotaPods       string // ResourceQuota: total pods
	limitDefaultCPU string // LimitRange: default container cpu limit
	limitDefaultMem string // LimitRange: default container memory limit
	limitReqCPU     string // LimitRange: default container cpu request
	limitReqMem     string // LimitRange: default container memory request
	limitMaxCPU     string // LimitRange: max container cpu limit
	limitMaxMem     string // LimitRange: max container memory limit
}

// newResourceLimits resolves the per-tenant resource policy from the
// environment, falling back to safe hard-coded defaults. Defaults are sized for
// a small production tenant and are intentionally conservative — an operator
// raises them per cluster; nothing here needs to change per environment.
func newResourceLimits() resourceLimits {
	return resourceLimits{
		maxReplicas:     atoiDefault(env.Or("CLOUD_PLATFORM_MAX_REPLICAS", ""), defaultMaxReplicas),
		maxStorageGB:    atoiDefault(env.Or("CLOUD_PLATFORM_MAX_STORAGE_GB", ""), defaultMaxStorageGB),
		maxDeploys:      atoiDefault(env.Or("CLOUD_PLATFORM_MAX_CONCURRENT_DEPLOYS", ""), defaultMaxDeploys),
		maxRunners:      atoiDefault(env.Or("CLOUD_PLATFORM_MAX_CONCURRENT_RUNNERS", ""), defaultMaxRunners),
		quotaCPU:        env.Or("CLOUD_PLATFORM_QUOTA_CPU", "20"),
		quotaMemory:     env.Or("CLOUD_PLATFORM_QUOTA_MEMORY", "40Gi"),
		quotaPods:       env.Or("CLOUD_PLATFORM_QUOTA_PODS", "50"),
		limitDefaultCPU: env.Or("CLOUD_PLATFORM_LIMIT_DEFAULT_CPU", "500m"),
		limitDefaultMem: env.Or("CLOUD_PLATFORM_LIMIT_DEFAULT_MEM", "512Mi"),
		limitReqCPU:     env.Or("CLOUD_PLATFORM_LIMIT_REQ_CPU", "100m"),
		limitReqMem:     env.Or("CLOUD_PLATFORM_LIMIT_REQ_MEM", "128Mi"),
		limitMaxCPU:     env.Or("CLOUD_PLATFORM_LIMIT_MAX_CPU", "4"),
		limitMaxMem:     env.Or("CLOUD_PLATFORM_LIMIT_MAX_MEM", "8Gi"),
	}
}

// defaultMaxReplicas and the bounds below are the fail-safe values used when the
// resolved policy is unset (a zero-value resourceLimits). A missing limit must
// FAIL SECURE to the safe ceiling, never to zero (which would break every app)
// or to unlimited.
const (
	defaultMaxReplicas = 20
	// defaultMaxDeploys bounds concurrent SYNCHRONOUS image deploys per org (L1).
	// Generous enough never to bite a legitimate bursty redeploy, yet bounds the
	// request goroutines that a wedged operator (tenant RBAC never landing) could
	// otherwise let one org pile up in waitForTenantRBAC's ~45s wait. Fail-secure:
	// an unset ceiling falls back here, never to zero or unlimited.
	defaultMaxDeploys = 8
	// defaultMaxRunners bounds concurrent runner Jobs across the WHOLE cluster,
	// not per org: a runner's memory is its node's, so what it exhausts is the
	// node, and one tenant's fair share is not the constraint. Twelve ran at once
	// here as kata microVMs (they run under visor now), each holding 9-10 GB, and
	// the box began killing processes. Six leaves room for the fleet beside
	// them. A refused job is started by the queue loop (queue.go) once a slot frees.
	defaultMaxRunners = 6
	// defaultMaxStorageGB bounds one app's volume. Unlike replicas, storage is not
	// reclaimed when the app stops — a claim keeps costing until someone deletes
	// it — so the ceiling exists to bound spend, not just scheduling.
	defaultMaxStorageGB = 100
)

// clampStorage bounds a requested volume size to [0, maxStorageGB] GiB. ZERO IS
// MEANINGFUL and is the default: it says the app is stateless and gets no volume
// at all, which is why this cannot reuse clampReplicas' "0 becomes 1" floor. A
// request above the ceiling is capped rather than rejected, matching replicas: a
// fat-fingered size degrades to the maximum allowed instead of failing a deploy.
// An unset ceiling falls back to the safe default, never to unlimited.
func (r resourceLimits) clampStorage(gb int) int {
	max := r.maxStorageGB
	if max <= 0 {
		max = defaultMaxStorageGB
	}
	if gb < 1 {
		return 0
	}
	if gb > max {
		return max
	}
	return gb
}

// clampReplicas bounds a requested replica count to [1, maxReplicas]. A request
// for 0 (or negative) becomes 1 (an app is at least one replica); a request
// above the ceiling is capped, never rejected — the tenant gets the maximum
// allowed, not an error, so a fat-fingered value degrades gracefully. An unset
// ceiling falls back to the safe default (fail-secure), never to zero.
func (r resourceLimits) clampReplicas(n int) int {
	max := r.maxReplicas
	if max <= 0 {
		max = defaultMaxReplicas
	}
	if n < 1 {
		return 1
	}
	if n > max {
		return max
	}
	return n
}

// maxConcurrentDeploys is the per-org in-flight SYNCHRONOUS image-deploy ceiling
// (L1), falling back to the safe default when unset (fail-secure). It mirrors
// maxConcurrentBuilds for the image path, which has no build Job to count.
// maxConcurrentRunners is the CLUSTER-wide ceiling on live runner Jobs, falling
// back to the safe default when unset (fail-secure), exactly as the build and
// deploy ceilings do.
func (r resourceLimits) maxConcurrentRunners() int {
	if r.maxRunners <= 0 {
		return defaultMaxRunners
	}
	return r.maxRunners
}

func (r resourceLimits) maxConcurrentDeploys() int {
	if r.maxDeploys <= 0 {
		return defaultMaxDeploys
	}
	return r.maxDeploys
}

// resourceQuota renders the namespaced ResourceQuota that caps a tenant's TOTAL
// scheduled footprint. Applied idempotently by ensureNamespace.
func (r resourceLimits) resourceQuota(ns string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "ResourceQuota",
		"metadata": map[string]any{
			"name":      tenantQuotaName,
			"namespace": ns,
			"labels":    map[string]any{"hanzo.ai/managed-by": "platform"},
		},
		"spec": map[string]any{
			"hard": map[string]any{
				"requests.cpu":    r.quotaCPU,
				"requests.memory": r.quotaMemory,
				"limits.cpu":      r.quotaCPU,
				"limits.memory":   r.quotaMemory,
				"pods":            r.quotaPods,
			},
		},
	}}
}

// limitRange renders the namespaced LimitRange that gives every container a
// default request+limit and a hard per-container maximum, so a single container
// cannot request the whole quota and an unspecified container still counts
// against it. Applied idempotently by ensureNamespace.
func (r resourceLimits) limitRange(ns string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "LimitRange",
		"metadata": map[string]any{
			"name":      tenantLimitRangeName,
			"namespace": ns,
			"labels":    map[string]any{"hanzo.ai/managed-by": "platform"},
		},
		"spec": map[string]any{
			"limits": []any{map[string]any{
				"type":           "Container",
				"default":        map[string]any{"cpu": r.limitDefaultCPU, "memory": r.limitDefaultMem},
				"defaultRequest": map[string]any{"cpu": r.limitReqCPU, "memory": r.limitReqMem},
				"max":            map[string]any{"cpu": r.limitMaxCPU, "memory": r.limitMaxMem},
			}},
		},
	}}
}

func atoiDefault(s string, def int) int {
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || n <= 0 {
		return def
	}
	return n
}
