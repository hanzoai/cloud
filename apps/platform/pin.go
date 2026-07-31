// pin.go — the last step of a release, and the ONLY one that deploys.
//
// A release builds an image, proves it boots, and mints a tag. None of that makes
// it live. cd.hanzo.ai does not watch the registry: the ApplicationSet at
// universe `infra/k8s/hanzo-cd/applicationset-fleet.yaml` runs a git generator over
// `charts/app/values/*/*.yaml` in hanzoai/universe, and the `hanzo-cloud`
// Application it generates renders `charts/app` against `values/hanzo/cloud.yaml`.
// The `image.tag` scalar in that file IS what runs. Until it moves, a release is a
// published image nobody pulls.
//
// WHAT THIS REPLACES. rolloutRelease used to patch an operator `hanzo.ai/v1` App CR
// and let the operator reconcile a Deployment. The CRD kind exists, but there has
// never been a `cloud` CR for it to patch — cloud is reconciled by cd.hanzo.ai from
// the values file, not by the operator — so the patch had nothing to write to and
// every release failed at its final step with the image built, smoked and tagged
// but NOT live. v1.801.335 was pinned by hand. Two beliefs about what makes an
// image live disagreed in the source; this is the reconciliation, and the values
// file wins because it is what the cluster actually reads.
//
// ── the rules are charts/app/pin.sh's rules ─────────────────────────────────
//
// universe carries the reference implementation of moving a pin safely, and every
// other service's CI calls it. This is that rule set, expressed in Go, refusal for
// refusal: non-semver, an image the registry cannot serve, a version older than the
// current pin, a repository the caller chose rather than the one the values file
// declares, and a service with no values file. Only the tag scalar is rewritten, in
// place, so the comment block those files carry survives byte-for-byte.
//
// WHY NOT SHELL OUT TO pin.sh, which would keep one implementation. Because it
// cannot run here and could not pin this service if it did:
//
//   - The runtime image is alpine with `git`, and no `bash`, `curl` or `jq`
//     (Dockerfile's final stage installs ca-certificates, tzdata, sqlcipher-libs,
//     git, tini; a live pod confirms bash/curl/jq absent). pin.sh needs all three —
//     `#!/usr/bin/env bash`, `mapfile`, `[[ =~ ]]`, curl and jq for the registry
//     probe. The container runs as nonroot, so it cannot install them either.
//     Carrying three more packages in the production image to shell out to logic
//     this package already has is a worse trade than expressing the rules here:
//     the semver gate is splitReleaseImage (STRICTER than pin.sh — it requires the
//     `v`), and the registry probe reuses registryPullToken, the exact ghcr token
//     flow release.go already speaks to enumerate published tags.
//
//   - pin.sh strips the leading `v` and pins the bare form, because that is what
//     the registries of the services that call it hold. Cloud's registry holds the
//     `v`: ghcr.io/hanzoai/cloud:v1.801.335 resolves, 1.801.335 is a 404. pin.sh
//     would therefore fail its own pullability probe on every cloud release. It
//     fails CLOSED (refuses, never writes a bad pin), so a human running it against
//     cloud is safe — it simply cannot be the mechanism here.
//
// The divergence that matters is not which language the rules are in, it is whether
// the two can disagree about what gets written. They cannot: the tag this pins is
// the exact string it proved pullable, so a pin can never name an image that is not
// there. That is stronger than pin.sh's "strip the v, then probe", and it is what
// removes the prefix question entirely.
//
// NO ROLLBACK LEVER. pin.sh takes PIN_ROLLBACK=1 because a human may deliberately
// need to go backward. Nothing on this path ever should: computeReleaseVersion
// mints a version strictly greater than every tag in git AND in the registry, so a
// backward pin here is always a bug, never an intention. There is no env knob to
// misread — backward is refused, full stop. The deliberate rollback lever stays
// where a deliberate act belongs, with the human running pin.sh.

package platform

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
)

const (
	// universeBranch is the branch the fleet ApplicationSet's git generator reads.
	universeBranch = "main"

	// pinTokenRef is the KMS coordinate of the forge token allowed to push to
	// universe: org hanzo, path deploy, name UNIVERSE_PIN_TOKEN, env prod. Flat-ref
	// grammar (apps/kms parseRef): "orgs/<org>/<path>/<name>@<env>". The org prefix
	// is load-bearing — it is what scopes the read to hanzo's own secret store.
	pinTokenRef = "orgs/hanzo/deploy/UNIVERSE_PIN_TOKEN@prod"

	// pinUser is the basic-auth username half of a forge token. git ignores it; the
	// token is the secret. Same fixed label the inbound mirror credential uses.
	pinUser = "x-access-token"

	// pinAttempts bounds the concurrent-pin retry (see pinUniverse).
	pinAttempts = 5

	// pinDeadline bounds the whole pin. The release pipeline runs on a DETACHED
	// context with no deadline of its own, so without this a wedged clone or push
	// would hold the release goroutine — and the in-flight guard — forever.
	pinDeadline = 10 * time.Minute

	// pinCommitUser / pinCommitEmail are the identity every pin commits under, the
	// same one pin.sh uses, so `git log charts/app/values/` reads as one history
	// whoever moved the pin.
	pinCommitUser  = "hanzo-ci"
	pinCommitEmail = "ci@hanzo.ai"
)

// universeRemote is the clone/push URL of the repository that holds every pin.
// A var (not a const) ONLY so tests can point the pin at a local repository;
// production always uses the forge — the same idiom as githubAPIBase and
// registryBase in release.go.
var universeRemote = "https://git.hanzo.ai/hanzo/universe"

// ── the values file (pure) ───────────────────────────────────────────────────

// pinFile is one service's declaration: the repository it runs, the tag it
// currently pins, and the line that carries that tag.
type pinFile struct {
	path       string
	lines      []string
	repository string
	current    string
	tagLine    int
}

// imageBlockLine returns the index of the first line whose first whitespace-
// separated field is "<key>:" inside the top-level `image:` block, or -1.
//
// The block opens at a line beginning "image:" and closes at the next line with a
// non-space first character — so the indented comment block those files carry does
// NOT close it, and a blank line does not either. That is exactly the span pin.sh's
// awk walks, so both readers always agree on which scalar is the pin.
func imageBlockLine(lines []string, key string) int {
	in := false
	for i, ln := range lines {
		if strings.HasPrefix(ln, "image:") {
			in = true
			continue
		}
		if !in {
			continue
		}
		if ln != "" && ln[0] != ' ' && ln[0] != '\t' {
			in = false
			continue
		}
		if f := strings.Fields(ln); len(f) > 0 && f[0] == key+":" {
			return i
		}
	}
	return -1
}

// imageBlockValue returns the value of a scalar in the image block ("" if absent).
func imageBlockValue(lines []string, key string) string {
	i := imageBlockLine(lines, key)
	if i < 0 {
		return ""
	}
	if f := strings.Fields(lines[i]); len(f) > 1 {
		return f[1]
	}
	return ""
}

// readPinFile parses a values file's image block. A file that declares no
// repository or no tag is refused rather than repaired: this writes ONE scalar and
// must never be the thing that invents a service's image.
func readPinFile(path string) (*pinFile, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", filepath.Base(path), err)
	}
	f := &pinFile{path: path, lines: strings.Split(string(b), "\n")}
	if f.repository = imageBlockValue(f.lines, "repository"); f.repository == "" {
		return nil, fmt.Errorf("%s declares no image.repository", filepath.Base(path))
	}
	if f.tagLine = imageBlockLine(f.lines, "tag"); f.tagLine < 0 {
		return nil, fmt.Errorf("%s declares no image.tag", filepath.Base(path))
	}
	if f.current = imageBlockValue(f.lines, "tag"); f.current == "" {
		return nil, fmt.Errorf("%s declares an empty image.tag", filepath.Base(path))
	}
	return f, nil
}

// setTag rewrites the tag scalar where it sits: the indentation before it and
// anything after the value (a trailing comment) are preserved, and every other line
// is untouched. These files carry the reasoning for every value in them, so a YAML
// round-trip through a parser — which would reflow that away — is not an option.
func (f *pinFile) setTag(tag string) {
	ln := f.lines[f.tagLine]
	at := strings.Index(ln, "tag:")
	rest := ln[at+len("tag:"):]
	i := 0
	for i < len(rest) && (rest[i] == ' ' || rest[i] == '\t') {
		i++
	}
	j := i
	for j < len(rest) && rest[j] != ' ' && rest[j] != '\t' && rest[j] != '#' {
		j++
	}
	f.lines[f.tagLine] = ln[:at] + "tag: " + tag + rest[j:]
}

// write persists the file and reads it back to prove the pin took — the one write
// on this path, so it verifies itself rather than trusting the edit.
func (f *pinFile) write(tag string) error {
	if err := os.WriteFile(f.path, []byte(strings.Join(f.lines, "\n")), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", filepath.Base(f.path), err)
	}
	back, err := readPinFile(f.path)
	if err != nil {
		return err
	}
	if back.current != tag {
		return fmt.Errorf("wrote %s but it reads back as %q, expected %q", filepath.Base(f.path), back.current, tag)
	}
	return nil
}

// resolvePinFile resolves a service name to its ONE declaration under
// charts/app/values/<namespace>/<service>.yaml.
//
// Zero matches means the service is not in the inventory, and a release must not be
// able to enrol one as a side effect — adding a service is a deliberate act. More
// than one means two namespaces hold the same name and nothing here may guess which
// production the caller meant.
func resolvePinFile(root, service string) (string, error) {
	matches, err := filepath.Glob(filepath.Join(root, "charts", "app", "values", "*", service+".yaml"))
	if err != nil {
		return "", err
	}
	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		return "", fmt.Errorf("no values file for %q (looked for charts/app/values/*/%s.yaml) — a service is added deliberately, not by a release", service, service)
	default:
		return "", fmt.Errorf("%q is declared in %d namespaces (%s) — ambiguous, refusing to guess which production to move", service, len(matches), strings.Join(matches, ", "))
	}
}

// ── the refusals (pure) ──────────────────────────────────────────────────────

// checkPin applies every rule that does not need the network and reports whether
// the file must change. changed=false means the pin already reads tag, which is a
// success with nothing to do — a re-run of an unchanged tree is a no-op, not an
// empty commit.
func checkPin(f *pinFile, repository, tag string) (changed bool, err error) {
	// The repository comes from the FILE, never from the caller. A release hands
	// over a VERSION for a service, not an image of its choosing: the worst a
	// careless or compromised build can then do is move its own service to another
	// of its own published versions. A mismatch means the release built something
	// other than what this service declares, and pinning that tag would point
	// production at an image nobody built.
	if f.repository != repository {
		return false, fmt.Errorf("%s declares image.repository %s but the release built %s — refusing to pin a tag of one repository into another",
			filepath.Base(f.path), f.repository, repository)
	}
	if f.current == tag {
		return false, nil
	}
	// Monotonic. A re-run of an old build must not roll production back by
	// surprise. Both sides must parse: an unreadable current pin means nothing here
	// can tell which way it would move.
	cur, ok := parseSemver(f.current)
	if !ok {
		return false, fmt.Errorf("%s is pinned at %q, which is not semver — refusing to move a pin that cannot be ordered", filepath.Base(f.path), f.current)
	}
	next, ok := parseSemver(tag)
	if !ok {
		return false, fmt.Errorf("%q is not semver — refusing to pin", tag)
	}
	if next.less(cur) {
		return false, fmt.Errorf("%s is pinned at %s and %s is older — refusing to roll production back", filepath.Base(f.path), f.current, tag)
	}
	return true, nil
}

// imagePullable proves the registry can serve repository:tag BEFORE production is
// pointed at it. A pin naming an image the registry does not have is an
// ImagePullBackOff with no rollback path. Anything but a 200 fails: a 404 is a
// phantom tag, and a network or auth error means we cannot tell — which is not good
// enough to deploy on.
//
// It reuses registryPullToken, the same anonymous ghcr token flow publishedTags
// uses, so there is ONE way this binary asks a registry a question.
func imagePullable(ctx context.Context, repository, tag string) error {
	host, repo, ok := strings.Cut(repository, "/")
	if !ok {
		return fmt.Errorf("repository %q names no registry host", repository)
	}
	if host != "ghcr.io" {
		return fmt.Errorf("unsupported registry %q — Hanzo publishes to ghcr.io", host)
	}
	token, err := registryPullToken(ctx, host, repo)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, registryBase+"/v2/"+repo+"/manifests/"+tag, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", strings.Join([]string{
		"application/vnd.oci.image.index.v1+json",
		"application/vnd.docker.distribution.manifest.list.v2+json",
		"application/vnd.docker.distribution.manifest.v2+json",
	}, ","))
	resp, err := releaseHTTP.Do(req)
	if err != nil {
		return fmt.Errorf("manifest probe %s:%s: %w", repository, tag, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s:%s is not pullable (manifest probe returned %d) — refusing to pin an image the registry does not have",
			repository, tag, resp.StatusCode)
	}
	return nil
}

// ── the git seam ─────────────────────────────────────────────────────────────

// pinGitEnv is the environment every git subprocess on this path runs with. It is
// built FROM SCRATCH and inherits nothing but PATH, which is what keeps the pin
// credential — and every other secret in cloud's environment — out of the child.
// Building it from nothing also means an operator-set GIT_TRACE cannot be inherited
// to echo the Authorization header into a log.
//
// The token travels as an http.extraHeader in this environment, NEVER in argv and
// never as URL userinfo: /proc/<pid>/cmdline is world-readable inside the container,
// and a URL credential is echoed back by git in its own error messages. The empty
// credential.helper stops git from consulting any helper, and followRedirects=false
// stops the header from riding a redirect to a host that is not the forge.
func pinGitEnv(token string) []string {
	env := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + os.TempDir(),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_ALLOW_PROTOCOL=http:https",
		"LC_ALL=C",
	}
	cfg := []string{"credential.helper="}
	// A credential is presented over https ONLY. A test remote is a local path with
	// no host to authenticate to, and plaintext http would put the token on the wire.
	if token != "" && strings.HasPrefix(universeRemote, "https://") {
		cfg = append(cfg,
			"http.followRedirects=false",
			"http.extraHeader=Authorization: Basic "+base64.StdEncoding.EncodeToString([]byte(pinUser+":"+token)),
		)
	}
	env = append(env, fmt.Sprintf("GIT_CONFIG_COUNT=%d", len(cfg)))
	for i, kv := range cfg {
		k, v, _ := strings.Cut(kv, "=")
		env = append(env, fmt.Sprintf("GIT_CONFIG_KEY_%d=%s", i, k), fmt.Sprintf("GIT_CONFIG_VALUE_%d=%s", i, v))
	}
	return env
}

// runGit runs one git subcommand and returns its combined output. Arguments are a
// slice — never a shell string — so nothing in a version or a service name can be
// read as a command. WaitDelay bounds the reaping of a child that ignores the
// context's kill, so a wedged git can never outlive the pin.
func runGit(ctx context.Context, dir string, env []string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = env
	cmd.WaitDelay = 10 * time.Second
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

// pushRaced reports whether a failed push is the benign case: another service
// landed its own pin between our read and our push. That is normal traffic on a
// repository the whole fleet deploys through, not a failure — the answer is to
// re-read the new tip and re-apply, since a pin is idempotent. Every OTHER push
// failure (auth, network, a hook refusing the ref) is real and must surface.
func pushRaced(out string) bool {
	return strings.Contains(out, "[rejected]") ||
		strings.Contains(out, "non-fast-forward") ||
		strings.Contains(out, "fetch first") ||
		strings.Contains(out, "cannot lock ref")
}

// ── the pin ──────────────────────────────────────────────────────────────────

// pinToken reads the forge token that may push to universe. Fail-closed: an
// unmounted KMS, a KMS that cannot answer, or an absent/empty secret each return an
// error and NEVER a value, so a release stops rather than attempting an anonymous
// push that would fail deep in the git seam. The error names the REF, never the
// value — the ref is a path and is safe to log.
func pinToken(s *cloud.Service[state], ctx context.Context) (string, error) {
	if s.KMS == nil {
		return "", fmt.Errorf("no KMS client mounted: cannot read %s", pinTokenRef)
	}
	b, err := s.KMS.GetSecret(ctx, pinTokenRef)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", pinTokenRef, err)
	}
	token := strings.TrimSpace(string(b))
	if token == "" {
		return "", fmt.Errorf("%s is empty", pinTokenRef)
	}
	return token, nil
}

// pinUniverse moves the ONE value that makes an image live: image.tag in
// charts/app/values/<namespace>/<service>.yaml in hanzoai/universe, which
// cd.hanzo.ai reconciles.
//
// It clones the branch CD reads, applies the rule set above, proves the tag is
// pullable, rewrites the one scalar, commits and pushes. Every refusal returns an
// error: the caller must be able to say "the image is built, smoke-passed and
// tagged but NOT live", because a release that claims otherwise is worse than one
// that fails.
//
// THE RACE. universe is the repository the whole fleet deploys through, so another
// service can land its own pin between the clone and the push. The push is a plain
// fast-forward — never forced — so that case is REJECTED rather than silently
// overwriting someone else's deploy. It is then retried, bounded, by re-reading the
// new tip and re-applying: a pin is idempotent, and re-applying re-runs the whole
// rule set against what is now declared, so a concurrent pin that already moved
// this service PAST our version is correctly refused by the monotonic rule instead
// of being clobbered. Nothing is merged — there is one line, and the newest read of
// it wins.
func pinUniverse(s *cloud.Service[state], ctx context.Context, service, image, sha string) error {
	repository, tag, err := splitReleaseImage(image)
	if err != nil {
		return err
	}
	token, err := pinToken(s, ctx)
	if err != nil {
		return fmt.Errorf("pin credential: %w", err)
	}
	env := pinGitEnv(token)

	ctx, cancel := context.WithTimeout(ctx, pinDeadline)
	defer cancel()

	dir, err := os.MkdirTemp("", "pin-universe-")
	if err != nil {
		return fmt.Errorf("workdir: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()

	// Shallow and single-branch: the pin needs the tip of the branch CD reads and
	// nothing else, and this runs in a production pod.
	if _, err := runGit(ctx, "", env, "clone", "--depth", "1", "--single-branch",
		"--branch", universeBranch, universeRemote, dir); err != nil {
		return fmt.Errorf("clone %s: %w", universeRemote, err)
	}

	for attempt := 1; attempt <= pinAttempts; attempt++ {
		if attempt > 1 {
			// Re-read the new tip and drop our rejected commit. reset --hard makes
			// the next apply run against what is declared NOW, not against a stale
			// read plus our edit.
			if _, err := runGit(ctx, dir, env, "fetch", "--depth", "1", "origin", universeBranch); err != nil {
				return fmt.Errorf("re-read %s: %w", universeBranch, err)
			}
			if _, err := runGit(ctx, dir, env, "reset", "--hard", "FETCH_HEAD"); err != nil {
				return fmt.Errorf("re-read %s: %w", universeBranch, err)
			}
		}

		path, err := resolvePinFile(dir, service)
		if err != nil {
			return err
		}
		f, err := readPinFile(path)
		if err != nil {
			return err
		}
		changed, err := checkPin(f, repository, tag)
		if err != nil {
			return err
		}
		if !changed {
			s.Log.Info("pin already live", "service", service, "tag", tag, "file", filepath.Base(path))
			return nil
		}
		// Prove the image before production is pointed at it — build first, pin
		// second, never the reverse.
		if err := imagePullable(ctx, repository, tag); err != nil {
			return err
		}

		was := f.current
		f.setTag(tag)
		if err := f.write(tag); err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		if _, err := runGit(ctx, dir, env, "add", "--", rel); err != nil {
			return err
		}
		msg := fmt.Sprintf("%s %s -> %s\n\nBuilt from %s and published as %s:%s, verified pullable before this\npin moved. cd.hanzo.ai reconciles the change from here.",
			service, was, tag, sha, repository, tag)
		if _, err := runGit(ctx, dir, env,
			"-c", "user.name="+pinCommitUser, "-c", "user.email="+pinCommitEmail,
			"commit", "-m", msg); err != nil {
			return err
		}

		out, err := runGit(ctx, dir, env, "push", "origin", "HEAD:refs/heads/"+universeBranch)
		if err == nil {
			s.Log.Info("pin moved: the image is live once CD syncs",
				"service", service, "from", was, "to", tag, "file", filepath.Base(path), "attempt", attempt)
			return nil
		}
		if !pushRaced(out) {
			return fmt.Errorf("push pin: %w", err)
		}
		s.Log.Info("another pin landed first; re-reading the tip",
			"service", service, "tag", tag, "attempt", attempt)
	}
	return fmt.Errorf("pin %s to %s: %d concurrent pins landed first — image %s:%s is tagged but NOT live",
		service, tag, pinAttempts, repository, tag)
}
