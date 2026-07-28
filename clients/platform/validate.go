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
//     depth for MED-3). Concurrent build fan-out is capped in k8s.go using
//     resourceLimits.maxConcurrentBuilds below.
//
// Every bound is a hard-coded safe default, operator-overridable via env — the
// values are resolved ONCE at k8sClient construction so a request path never
// re-reads the environment and the tests are deterministic.
package platform

import (
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// ── build-input validation (CRIT-1) ──────────────────────────────────────────

// defaultGitProviderHosts is the built-in allowlist of git apexes a build
// context may be fetched from (mirrors providerFromURL's recognized set). An
// allowlist — not a denylist — is the safe default: an unknown host is refused,
// never fetched. An exact apex OR a subdomain of one is accepted (so
// codeload.github.com under github.com is allowed).
var defaultGitProviderHosts = []string{"github.com", "gitlab.com", "bitbucket.org", "codeberg.org"}

// gitProviderHosts is the effective allowlist, resolved ONCE at package init:
// the operator may REPLACE it via CLOUD_PLATFORM_GIT_HOSTS (comma-separated
// apexes) for a self-hosted git provider, else the built-in set applies.
var gitProviderHosts = resolveGitHosts()

// selfGitHost is the cloud's OWN embedded-git apex (deps.Domain). The cloud
// serves its repos there (clients/git), so its own clone URLs are ALWAYS a
// trusted build source and git-push-to-deploy works with no extra config. Set
// once at platform Mount; an orthogonal trust from the external-provider
// allowlist above.
var selfGitHost string

func resolveGitHosts() []string {
	raw := strings.TrimSpace(getenv("CLOUD_PLATFORM_GIT_HOSTS", ""))
	if raw == "" {
		return defaultGitProviderHosts
	}
	var out []string
	for _, h := range strings.Split(raw, ",") {
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
// metacharacter can match (M1).
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
	for _, seg := range strings.Split(s, "/") {
		if seg == ".." {
			return "", fmt.Errorf("dockerfile path must not contain '..'")
		}
	}
	return s, nil
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
		if cleanRef, err = validateGitRef(ref); err != nil {
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
	maxBuilds       int    // concurrent build Jobs per org (shared build ns)
	maxDeploys      int    // concurrent in-flight SYNCHRONOUS image deploys per org (L1)
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
		maxReplicas:     atoiDefault(getenv("CLOUD_PLATFORM_MAX_REPLICAS", ""), defaultMaxReplicas),
		maxStorageGB:    atoiDefault(getenv("CLOUD_PLATFORM_MAX_STORAGE_GB", ""), defaultMaxStorageGB),
		maxBuilds:       atoiDefault(getenv("CLOUD_PLATFORM_MAX_CONCURRENT_BUILDS", ""), defaultMaxBuilds),
		maxDeploys:      atoiDefault(getenv("CLOUD_PLATFORM_MAX_CONCURRENT_DEPLOYS", ""), defaultMaxDeploys),
		quotaCPU:        getenv("CLOUD_PLATFORM_QUOTA_CPU", "20"),
		quotaMemory:     getenv("CLOUD_PLATFORM_QUOTA_MEMORY", "40Gi"),
		quotaPods:       getenv("CLOUD_PLATFORM_QUOTA_PODS", "50"),
		limitDefaultCPU: getenv("CLOUD_PLATFORM_LIMIT_DEFAULT_CPU", "500m"),
		limitDefaultMem: getenv("CLOUD_PLATFORM_LIMIT_DEFAULT_MEM", "512Mi"),
		limitReqCPU:     getenv("CLOUD_PLATFORM_LIMIT_REQ_CPU", "100m"),
		limitReqMem:     getenv("CLOUD_PLATFORM_LIMIT_REQ_MEM", "128Mi"),
		limitMaxCPU:     getenv("CLOUD_PLATFORM_LIMIT_MAX_CPU", "4"),
		limitMaxMem:     getenv("CLOUD_PLATFORM_LIMIT_MAX_MEM", "8Gi"),
	}
}

// defaultMaxReplicas / defaultMaxBuilds are the fail-safe bounds used when the
// resolved policy is unset (a zero-value resourceLimits). A missing limit must
// FAIL SECURE to the safe ceiling, never to zero (which would break every app)
// or to unlimited — so these back-stop clampReplicas + maxConcurrentBuilds.
const (
	defaultMaxReplicas = 20
	defaultMaxBuilds   = 3
	// defaultMaxDeploys bounds concurrent SYNCHRONOUS image deploys per org (L1).
	// Generous enough never to bite a legitimate bursty redeploy, yet bounds the
	// request goroutines that a wedged operator (tenant RBAC never landing) could
	// otherwise let one org pile up in waitForTenantRBAC's ~45s wait. Fail-secure:
	// an unset ceiling falls back here, never to zero or unlimited.
	defaultMaxDeploys = 8
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

// maxConcurrentBuilds is the per-org concurrent build ceiling, falling back to
// the safe default when unset (fail-secure).
func (r resourceLimits) maxConcurrentBuilds() int {
	if r.maxBuilds <= 0 {
		return defaultMaxBuilds
	}
	return r.maxBuilds
}

// maxConcurrentDeploys is the per-org in-flight SYNCHRONOUS image-deploy ceiling
// (L1), falling back to the safe default when unset (fail-secure). It mirrors
// maxConcurrentBuilds for the image path, which has no build Job to count.
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
