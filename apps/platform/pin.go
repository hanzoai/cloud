// pin.go — what a declaration is made of: the values file, the version, the
// registry proof and the git write. declare.go is the one caller and the one act.
//
// A release builds an image, proves it boots, and mints a tag. None of that makes
// it live. cd.hanzo.ai does not watch the registry: the ApplicationSet at
// universe `infra/k8s/hanzo-cd/applicationset-fleet.yaml` runs a git generator over
// `charts/app/values/*/*.yaml` in hanzo-inc/universe, and the Application it
// generates renders `charts/app` against that file. The `image.tag` scalar in it IS
// what runs. Until it moves, a release is a published image nobody pulls.
//
// THE VALUES FILE, NOT AN OPERATOR CR. There is a `hanzo.ai/v1` App CRD and no
// `cloud` CR for it: cloud is reconciled by cd.hanzo.ai from the values file. So
// the values file is what this writes, because it is what the cluster reads.
//
// ── the rules are charts/app/pin.sh's rules ─────────────────────────────────
//
// universe carries the reference implementation of moving a pin safely, and every
// other service's CI calls it. The providers here are that rule set for the one writer
// of those files that is NOT pin.sh: the file reader that rewrites the tag scalar
// in place so the comment block survives byte-for-byte, the semver order behind
// declare's rollback refusal, the anonymous ghcr token flow that proves an image is
// pullable before main points at it, and the git env that carries the credential.
//
// pin.sh cannot run here and could not pin this service if it did: the runtime is
// alpine with `git` and no `bash`, `curl` or `jq`, as nonroot, so it cannot install
// them either — and pin.sh strips the leading `v` while cloud's registry holds it
// (ghcr.io/hanzoai/cloud:v1.801.335 resolves, 1.801.335 is a 404), so its own
// pullability probe would refuse every cloud release. It fails CLOSED, so a human
// running it is safe; it simply cannot be the mechanism in-process.
//
// The divergence that matters is not which language the rules are in, it is whether
// the two can disagree about what gets written. They cannot: the tag declare pins is
// the exact string it proved pullable, so a pin can never name an image that is not
// there.
//
// NO ROLLBACK LEVER. pin.sh takes PIN_ROLLBACK=1 because a human may deliberately
// need to go backward. Nothing on this path ever should: a release claims a version
// strictly greater than every one already claimed, so a backward pin here is a bug
// rather than an intention, and declare refuses it outright with no env knob to
// misread. The deliberate rollback stays where a deliberate act belongs — a revert
// on universe, signed by whoever meant it.

package platform

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/cloud/client"
	providerpeer "github.com/hanzoai/cloud/client/provider"
	"github.com/hanzoai/cloud/internal/cluster"
	"github.com/hanzoai/cloud/sdk"
)

const (
	// universeBranch is the branch the fleet ApplicationSet's git generator reads.
	universeBranch = "main"

	// pinUser is the basic-auth username the token is presented under. The host
	// ignores it; the token is the secret. GitHub documents x-access-token.
	pinUser = "x-access-token"

	// pinAttempts bounds the concurrent-pin retry (see pinUniverse).
	pinAttempts = 5

	// pinDeadline bounds the whole pin. A caller may hand this a context with no
	// deadline of its own, so without this a wedged clone or push would hold its
	// goroutine forever.
	pinDeadline = 10 * time.Minute

	// grantBudget bounds minting universe's token: provider resolves the
	// repository and asks GitHub for the token, two calls.
	grantBudget = 30 * time.Second

	// pinCommitUser / pinCommitEmail are the identity every pin commits under, the
	// same one pin.sh uses, so `git log charts/app/values/` reads as one history
	// whoever moved the pin.
	pinCommitUser  = "hanzo-ci"
	pinCommitEmail = "ci@hanzo.ai"
)

// universeRemote names the repository that holds every declaration: the one the
// fleet ApplicationSet's git generator reads (universe
// infra/k8s/hanzo-cd/applicationset-fleet.yaml, repoURL). It is the name asked
// for, not the address used: universeGrant reads where GitHub says the repository
// lives now and clones and pushes there, so a transferred or renamed universe is
// read and written at its new name with no release in between. git.hanzo.ai
// carries a read-only mirror of it that trails main, so reading the mirror shows a
// stale fleet and writing it is refused. A var (not a const) ONLY so tests can
// point it at a local repository — the same idiom as registryBase below.
var universeRemote = "https://github.com/hanzo-inc/universe"

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
	before, after, _ := strings.Cut(ln, "tag:")
	rest := after
	i := 0
	for i < len(rest) && (rest[i] == ' ' || rest[i] == '\t') {
		i++
	}
	j := i
	for j < len(rest) && rest[j] != ' ' && rest[j] != '\t' && rest[j] != '#' {
		j++
	}
	f.lines[f.tagLine] = before + "tag: " + tag + rest[j:]
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

// ── ordering a version (pure) ────────────────────────────────────────────────

// semver is a version this file can order. Only the ordering is modelled, because
// ordering is the only question asked of it: is the tag being pinned newer than the
// one already there.
type semver struct{ major, minor, patch int }

// parseSemver accepts "X.Y.Z" or "vX.Y.Z" with non-negative integer parts; anything
// else (latest, sha-…, the major_minor "1.786", empty) reports ok=false. A pin
// refuses rather than guesses at a version it cannot order, so the false is a
// refusal and never a default.
func parseSemver(s string) (semver, bool) {
	p := strings.Split(strings.TrimPrefix(strings.TrimSpace(s), "v"), ".")
	if len(p) != 3 {
		return semver{}, false
	}
	var v semver
	var err error
	if v.major, err = atoiNonNeg(p[0]); err != nil {
		return semver{}, false
	}
	if v.minor, err = atoiNonNeg(p[1]); err != nil {
		return semver{}, false
	}
	if v.patch, err = atoiNonNeg(p[2]); err != nil {
		return semver{}, false
	}
	return v, true
}

func atoiNonNeg(s string) (int, error) {
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("not a non-negative int: %q", s)
	}
	return n, nil
}

func (v semver) less(o semver) bool {
	if v.major != o.major {
		return v.major < o.major
	}
	if v.minor != o.minor {
		return v.minor < o.minor
	}
	return v.patch < o.patch
}

// registryBase is the OCI registry root. A var (not a const) ONLY so tests can point
// it at an httptest server; production always uses the real registry.
var registryBase = "https://ghcr.io"

// pinHTTP is the one client for the registry reads on this path — a bounded timeout
// so a hung registry cannot hold the pin, which runs on a detached context.
var pinHTTP = &http.Client{Timeout: 30 * time.Second}

// registryPullToken exchanges nothing for a pull-scoped bearer — the anonymous half
// of the Docker registry token flow, all a public image's manifest requires.
func registryPullToken(ctx context.Context, host, repo string) (string, error) {
	var body struct {
		Token string `json:"token"`
	}
	u := registryBase + "/token?service=" + url.QueryEscape(host) +
		"&scope=" + url.QueryEscape("repository:"+repo+":pull")
	if err := registryGet(ctx, u, "", &body); err != nil {
		return "", fmt.Errorf("registry pull token: %w", err)
	}
	if body.Token == "" {
		return "", fmt.Errorf("registry pull token: empty")
	}
	return body.Token, nil
}

// registryGet performs one registry read and decodes it into out. A non-200 is an
// error: every read on this path is something the pin must know for certain, and a
// partial answer is not one.
func registryGet(ctx context.Context, endpoint, token string, out any) error {
	return registryGetWith(ctx, endpoint, token, out, nil)
}

// registryGetWith is registryGet handing the response headers to headers first,
// for a read whose answer continues in a header (a paged tag listing).
func registryGetWith(ctx context.Context, endpoint, token string, out any, headers func(http.Header)) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := pinHTTP.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: status %d", endpoint, resp.StatusCode)
	}
	if headers != nil {
		headers(resp.Header)
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decode %s: %w", endpoint, err)
	}
	return nil
}

// imagePullable proves the registry can serve repository:tag BEFORE production is
// pointed at it. A pin naming an image the registry does not have is an
// ImagePullBackOff with no rollback path. Anything but a 200 fails: a 404 is a
// phantom tag, and a network or auth error means we cannot tell — which is not good
// enough to deploy on.
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
	// Every media type an image of ours can be, because ghcr honours Accept
	// STRICTLY: ask for a manifest whose type you did not offer and it answers 404
	// — the identical status a tag that was never pushed returns. A probe that
	// cannot tell those apart reports a present image as a phantom tag and refuses
	// to pin it, which is the failure this function exists to prevent.
	//
	// The OCI image manifest is the one that used to be missing, and its absence
	// was invisible only because every image here was gzip: buildkit writes Docker
	// schema2 for a gzip layer and application/vnd.oci.image.manifest.v1+json for a
	// zstd one. So this list was coupled to the BUILDER'S COMPRESSION SETTING
	// without saying so, and the day that setting changed, every release pin would
	// have failed while truthfully insisting the registry did not have the image.
	// Measured against ghcr 2026-08-06 with a real zstd image: without the OCI type
	// 404, with it 200, and a nonexistent tag 404 either way.
	req.Header.Set("Accept", strings.Join([]string{
		"application/vnd.oci.image.index.v1+json",
		"application/vnd.docker.distribution.manifest.list.v2+json",
		"application/vnd.oci.image.manifest.v1+json",
		"application/vnd.docker.distribution.manifest.v2+json",
	}, ","))
	resp, err := pinHTTP.Do(req)
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

// ── the git client ─────────────────────────────────────────────────────────────

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
func pinGitEnv(u universe) []string {
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
	if u.token != "" && strings.HasPrefix(u.remote, "https://") {
		cfg = append(cfg,
			"http.followRedirects=false",
			"http.extraHeader=Authorization: Basic "+base64.StdEncoding.EncodeToString([]byte(pinUser+":"+u.token)),
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

// universe is where the declarations are read and written and the credential
// that reaches them: the remote as GitHub names the repository now, and a token
// for it ("" for a remote that is not https, which is presented none).
type universe struct {
	remote, token string
}

// universeGrant resolves universeRemote to where the repository lives now and
// the credential universe is read and written with: a GitHub App token that
// reaches that one repository, minted by provider from the installation the
// platform org (cluster.Platform) connected for the repository's owner. The App
// key stays in provider and no person's token is read here.
//
// Provider answers the repository's full name as GitHub states it, following a
// transfer or a rename, and the token it mints names that repository, so the
// remote is built from its answer and never from the name asked: a token minted
// for the new owner reaches nothing at the old URL.
//
// The platform org is stated on a context of its own, never the caller's: the
// inventory is the platform's repository and one read serves every tenant, so a
// caller's org choosing the installation would make the board depend on who
// asked first.
//
// Fail-closed: a token that cannot be minted is a *noGrant naming the
// installation an owner connects, never an anonymous clone that fails deep in
// git asking for a username.
func universeGrant(ctx context.Context) (universe, error) {
	u, err := url.Parse(universeRemote)
	if err != nil {
		return universe{}, fmt.Errorf("universe remote %q: %w", universeRemote, err)
	}
	if u.Scheme != "https" {
		return universe{remote: universeRemote}, nil
	}
	owner, repo, ok := strings.Cut(strings.TrimSuffix(strings.Trim(u.Path, "/"), ".git"), "/")
	if !strings.EqualFold(u.Hostname(), "github.com") || !ok || owner == "" || repo == "" || strings.Contains(repo, "/") {
		return universe{}, fmt.Errorf("universe remote %q is not a GitHub repository", universeRemote)
	}
	gctx, cancel := context.WithTimeout(cloud.For(context.Background(), cluster.Platform), grantBudget)
	defer cancel()
	defer context.AfterFunc(ctx, cancel)()
	out, err := providerpeer.IntegrationsGitHubGrant(gctx, &client.GitHubGrantIn{Owner: owner, Repo: repo})
	switch {
	case err != nil:
	case out == nil || strings.TrimSpace(out.Token) == "":
		err = errors.New("provider answered no token")
	case !ghName.MatchString(out.Owner) || !ghName.MatchString(out.Repo):
		err = fmt.Errorf("provider named no repository (%q/%q)", out.Owner, out.Repo)
	}
	if err != nil {
		return universe{}, &noGrant{org: cluster.Platform, owner: owner, repo: repo, cause: err}
	}
	return universe{remote: "https://github.com/" + out.Owner + "/" + out.Repo, token: out.Token}, nil
}

// noGrant is universe's token failing to mint: the platform org holds no
// installation of the GitHub App on the repository's owner, the installation
// lacks what the token asks for, or provider could not be asked. It is the
// platform's configuration, never the caller's request, so it answers 503
// (universeStatus) and names what an owner connects on GitHub.
//
// It does not unwrap. The cause is provider's refusal, and a refusal passed
// through as its own status would read to a tenant as theirs.
type noGrant struct {
	org, owner, repo string
	cause            error
}

func (e *noGrant) Error() string {
	return fmt.Sprintf("no GitHub App token for %s/%s: the installation on %s, connected by org %s, minted none: %v",
		e.owner, e.repo, e.owner, e.org, e.cause)
}
