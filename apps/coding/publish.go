package coding

// publish.go — a project's site, built from its repository in a sandbox.
//
// A project whose code is in a workspace the platform made for its org
// (workspace.go) is published the way a run works: in a sandbox, the one place
// a tenant's code executes. Its build never reaches the estate's CI or a forge
// runner, and nothing in the forge namespace is delivered to either (forge.Org).
//
// Two ways in, one build:
//
//	a run        a build run on such a project publishes its branch when it
//	             pushes, from the checkout it already has, so the builder's
//	             preview — the project's own address — shows the result
//	Publish      a project published on its own: a template the org just took
//	             (apps/projects fork.go), or the builder's Publish at a branch
//	             (apps/platform apps.go) — a sandbox of its own, a key that can
//	             only read the one repository, a clone at the ref
//
// The site is the project app's. It opens the deployment and hands over a grant
// that writes this one site's prefix for half an hour (apps/projects build.go);
// the sandbox builds, lists what it built, and uploads it with curl against that
// grant; the project app ends the deployment and reconciles the prefix against
// the keys uploaded. The sandbox is handed no credential of the platform's: the
// read key opens one repository and dies with the publish, and the grant writes
// one prefix the org could already deploy to.

import (
	"bytes"
	"cmp"
	"compress/gzip"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/hanzoai/cloud/client"
	projectpeer "github.com/hanzoai/cloud/client/project"
	sandboxpeer "github.com/hanzoai/cloud/client/sandbox"
	"github.com/hanzoai/cloud/sdk/plane"
	"github.com/zap-proto/zip"
)

// Site is the project a run's work is published to, and the repository its
// site is built from. The zero value publishes nothing.
type Site struct {
	Project, Owner, Repo string
}

const (
	// publishTTL is how long a publish's own sandbox is leased, in seconds: an
	// install and a build, with room for a slow registry.
	publishTTL = 20 * 60
	// buildTimeout bounds the build command, in seconds.
	buildTimeout = 15 * 60
	// uploadTimeout bounds the upload command, in seconds.
	uploadTimeout = 10 * 60
	// publishBudget bounds a whole publish: the lease, the clone, the build and
	// the upload.
	publishBudget = 25 * time.Minute

	// siteFiles and siteBytes are the most one site may hold, the bounds the
	// project app puts on every other way a site arrives (apps/projects blob.go).
	siteFiles = 5000
	siteBytes = 512 << 20

	// uploadPath is where the upload's curl config is written: under /tmp beside
	// the run's key, never in the checkout the build writes.
	uploadPath = "/tmp/hanzo-run/upload"
)

// siteDirs are the places a build leaves a site, in the order they are tried.
// The repository's root is last, and only for a repository with no package.json:
// a plain static repository is its own site, while the root of one with a
// package.json is its source, and publishing it would publish the source.
var siteDirs = []string{"dist", "build", "out", ".output/public", "_site", "public", "."}

// buildScript runs the repository's own build when package.json names one:
// the lockfile picks the package manager, a tree that is already installed is
// not installed again, and a build that fails fails the publish. A constant —
// nothing a caller sends is part of it.
//
// A Next.js app is a site only as its static export, so for one whose config is
// JavaScript, or that has none, the build reads that config through a
// next.config.mjs that adds `output: 'export'` and unoptimized images. Next.js
// reads next.config.js ahead of next.config.mjs, so the app's own file is kept
// beside it as .hanzo.<name> — where its own relative imports still resolve,
// and which the repository's local exclude keeps out of every commit — and put
// back when the build ends, however it ends (tidyNext). A TypeScript config
// alone is the app's to set.
const buildScript = `set -eu
if [ ! -f package.json ] || ! node -e 'process.exit(((require("./package.json").scripts)||{}).build ? 0 : 1)'; then
  echo "no build script; publishing the files as they are"
  exit 0
fi
pm=npm
if [ -f pnpm-lock.yaml ]; then pm=pnpm; elif [ -f yarn.lock ]; then pm=yarn; elif [ -f bun.lock ] || [ -f bun.lockb ]; then pm=bun; fi
if [ ! -d node_modules ]; then
  case $pm in
    pnpm) pnpm install --frozen-lockfile || pnpm install ;;
    yarn) yarn install --frozen-lockfile || yarn install ;;
    bun) bun install ;;
    *) if [ -f package-lock.json ]; then npm ci || npm install; else npm install; fi ;;
  esac
fi
if node -e 'const p=require("./package.json");process.exit({...p.dependencies,...p.devDependencies}.next?0:1)' &&
  { [ -f next.config.js ] || [ -f next.config.mjs ] || { [ ! -f next.config.ts ] && [ ! -f next.config.mts ]; }; }; then
` + tidyNext + `
  if ex=$(git rev-parse --git-path info/exclude 2>/dev/null); then
    grep -qxF '/.hanzo.*' "$ex" 2>/dev/null || { mkdir -p "$(dirname "$ex")" && echo '/.hanzo.*' >> "$ex"; }
  fi
  own=
  for f in next.config.mjs next.config.js; do
    if [ -f "$f" ]; then mv -f "$f" ".hanzo.$f"; own="./.hanzo.$f"; fi
  done
  trap tidy EXIT
  trap 'tidy; trap - EXIT; exit 129' HUP
  trap 'tidy; trap - EXIT; exit 130' INT
  trap 'tidy; trap - EXIT; exit 143' TERM
  if [ -n "$own" ]; then
    printf '%s\n' "$mark" "import own from '$own'" \
      "export default async (phase, ctx) => {" \
      "  const c = (await (typeof own === 'function' ? own(phase, ctx) : own)) || {}" \
      "  return { ...c, output: 'export', images: { ...c.images, unoptimized: true } }" \
      "}" > .hanzo.wrapper.mjs
  else
    printf '%s\n' "$mark" "export default { output: 'export', images: { unoptimized: true } }" > .hanzo.wrapper.mjs
  fi
  mv -f .hanzo.wrapper.mjs next.config.mjs
fi
if [ -f src/lib/hanzo.ts ] || [ -f lib/hanzo.ts ]; then
  node -e 'for(const p of["src/lib/hanzo.ts","lib/hanzo.ts"])try{const fs=require("fs"),s=fs.readFileSync(p,"utf8"),u=s.replace("(typeof process !== '\''undefined'\'' ? process.env.HANZO_API_KEY : undefined)","((globalThis as { process?: { env?: Record<string, string | undefined> } }).process?.env?.HANZO_API_KEY)");if(u!==s)fs.writeFileSync(p,u)}catch{}'
fi
$pm run build`

// tidyNext defines tidy, which puts back what a Next.js build moved, and runs
// it once: a build runs it before it moves anything and again when it ends, and
// a run runs it when it resumes a tree and before it stages, so what a killed
// build left is never worked in, built on or committed. The wrapper is written
// whole beside the config and moved onto it, and is known by its first line —
// the mark, or the import of the kept config. The app's own config
// comes back from where the build kept it or, when a lost sandbox took that
// too, from the commit; without the wrapper there is nothing of the build's to
// put back, so a config the app deleted stays deleted.
const tidyNext = `mark='// hanzo: the static export wrapper; the app config is kept as .hanzo.<name> while it builds'
tidy() {
  rm -f .hanzo.wrapper.mjs .git/index.lock
  if [ -d .git ]; then
    rm -f .git/HEAD.lock .git/shallow.lock .git/refs/heads/*.lock .git/refs/heads/*/*.lock 2>/dev/null || true
    for g in */.git */*/.git */*/*/.git; do
      [ -d "$g" ] || continue
      d=${g%/.git}
      git ls-files --error-unmatch "$d" >/dev/null 2>&1 && continue
      mkdir -p .git/info && ex=.git/info/exclude && : >> "$ex"
      pat="/$d/"; seen=
      while IFS= read -r l; do [ "$l" != "$pat" ] || seen=1; done < "$ex"
      [ -n "$seen" ] || printf '%s\n' "$pat" >> "$ex"
    done
  fi
  first=$(head -n 1 next.config.mjs 2>/dev/null || true)
  if [ "$first" = "$mark" ] || [ "${first#"import own from './.hanzo.next.config."}" != "$first" ]; then
    rm -f next.config.mjs
    for f in next.config.mjs next.config.js; do
      if [ -f ".hanzo.$f" ]; then mv -f ".hanzo.$f" "$f"
      elif git cat-file -e "HEAD:$f" 2>/dev/null; then git checkout -q HEAD -- "$f"; fi
    done
  fi
  for f in next.config.mjs next.config.js; do
    if [ -f ".hanzo.$f" ] && [ ! -f "$f" ]; then mv -f ".hanzo.$f" "$f"; fi
  done
}
tidy`

// findScript answers the first of siteDirs holding an index.html, the root only
// when there is no package.json.
var findScript = `for d in ` + strings.Join(quotedAll(siteDirs[:len(siteDirs)-1]), " ") + `; do
  if [ -f "$d/index.html" ]; then printf '%s' "$d"; exit 0; fi
done
if [ ! -f package.json ] && [ -f index.html ]; then printf '.'; exit 0; fi
echo "The build made no site: none of ` + strings.Join(siteDirs[:len(siteDirs)-2], ", ") + ` or ` + siteDirs[len(siteDirs)-2] +
	` holds an index.html, and the repository's root counts only when it has no package.json." >&2
exit 3`

// listScript lists the regular files under $1 as "size<TAB>path". Hidden paths
// are never part of a site — a checkout's .git and .env are hidden — except
// .well-known, and neither is node_modules. Links are not listed, so none is
// followed out of the tree.
const listScript = `cd "$1" && find . -mindepth 1 \( -name '.*' ! -name .well-known -o -name node_modules \) -prune -o -type f -printf '%s\t%P\n'`

func quotedAll(ss []string) []string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = "'" + s + "'"
	}
	return out
}

// built is what a publish put on the site.
type built struct {
	commit string
	keys   []string
	bytes  int64
}

// shipFile is one file to upload: its key under the grant's prefix, the path it
// is read from when that is not the key itself, and its size.
type shipFile struct {
	rel  string
	src  string
	size int64
}

// publish builds the checkout in the sandbox and uploads the site it made
// through up. It answers the keys uploaded, relative to up.Prefix.
func (in sandbox) publish(up client.SiteUpload, step func(name, msg, status string)) (built, error) {
	dir, err := in.build(step)
	if err != nil {
		return built{}, err
	}
	files, err := in.manifest(dir)
	if err != nil {
		return built{}, err
	}
	step("upload", fmt.Sprintf("uploading %d files", len(files)), "running")
	if err := in.upload(dir, files, up); err != nil {
		step("upload", err.Error(), "error")
		return built{}, err
	}
	out := built{keys: make([]string, 0, len(files))}
	for _, f := range files {
		out.keys = append(out.keys, f.rel)
		out.bytes += f.size
	}
	step("upload", fmt.Sprintf("uploaded %d files", len(files)), "ok")
	return out, nil
}

// build runs the repository's build and answers the directory holding the site.
func (in sandbox) build(step func(name, msg, status string)) (string, error) {
	step("build", "building the site", "running")
	ran, err := runIn(in.ctx, in.id, in.sourced([]string{"sh", "-c", buildScript}), min(in.ttl, buildTimeout), in.session, "", in.secrets()...)
	if err != nil {
		return "", fmt.Errorf("coding: build: %s", in.scrub(err.Error()))
	}
	if ran.ExitCode != 0 {
		last := lastLine(in.scrub(tail(ran.Stdout, ran.Stderr)))
		step("build", fmt.Sprintf("exit %d: %s", ran.ExitCode, last), "error")
		return "", fmt.Errorf("coding: the build exited %d: %s", ran.ExitCode, last)
	}
	found, err := runIn(in.ctx, in.id, []string{"sh", "-c", findScript}, in.ttl, in.session, "", in.secrets()...)
	if err != nil {
		err = fmt.Errorf("coding: find the built site: %s", in.scrub(err.Error()))
		step("build", err.Error(), "error")
		return "", err
	}
	if found.ExitCode != 0 {
		// findScript's own sentence says where it looked; nothing is put in front of it.
		why := cmp.Or(lastLine(in.scrub(tail(found.Stdout, found.Stderr))), fmt.Sprintf("The search for the built site exited %d.", found.ExitCode))
		step("build", why, "error")
		return "", errors.New(why)
	}
	dir := strings.TrimSpace(found.Stdout)
	if !slices.Contains(siteDirs, dir) {
		return "", fmt.Errorf("coding: the site was found in %q, which is not a place a site is built to", dir)
	}
	step("build", "built into "+dir, "ok")
	return dir, nil
}

// manifest lists the site under dir and checks it against what a site may be:
// bounded in count and size, an index.html at its root, and every path one the
// upload can name exactly.
func (in sandbox) manifest(dir string) ([]shipFile, error) {
	ran, err := in.do([]string{"sh", "-c", listScript, "sh", dir}, "list the built site")
	if err != nil {
		return nil, err
	}
	return parseManifest(ran.Stdout)
}

// parseManifest reads listScript's output.
func parseManifest(out string) ([]shipFile, error) {
	var (
		files []shipFile
		total int64
		index bool
	)
	for line := range strings.SplitSeq(strings.TrimRight(out, "\n"), "\n") {
		if line == "" {
			continue
		}
		size, rel, ok := strings.Cut(line, "\t")
		n, err := strconv.ParseInt(size, 10, 64)
		if !ok || err != nil || n < 0 {
			return nil, fmt.Errorf("coding: the site's file list is unreadable at %q", clip(line))
		}
		if !shippable(rel) {
			return nil, fmt.Errorf("coding: %q is not a name a site file may have", clip(rel))
		}
		if len(files) == siteFiles {
			return nil, fmt.Errorf("coding: the site has more than %d files", siteFiles)
		}
		if total += n; total > siteBytes {
			return nil, fmt.Errorf("coding: the site is larger than %d MiB", siteBytes>>20)
		}
		index = index || rel == "index.html"
		files = append(files, shipFile{rel: rel, size: n})
	}
	if !index {
		return nil, fmt.Errorf("coding: the site has no index.html at its root")
	}
	return files, nil
}

// shippable reports whether rel is a site path the upload can name as it is: a
// clean relative path of printable ASCII, with none of the characters a curl
// form or config gives a meaning (`"`, `\`, `;`, `,`), no space, and no hidden
// segment but .well-known.
func shippable(rel string) bool {
	if rel == "" || len(rel) > 512 || path.Clean(rel) != rel || strings.HasPrefix(rel, "/") || strings.HasPrefix(rel, "../") {
		return false
	}
	for i := 0; i < len(rel); i++ {
		c := rel[i]
		if c <= ' ' || c >= 0x7f || c == '"' || c == '\\' || c == ';' || c == ',' {
			return false
		}
	}
	for seg := range strings.SplitSeq(rel, "/") {
		if seg == ".." || strings.HasPrefix(seg, ".") && seg != ".well-known" {
			return false
		}
	}
	return true
}

// reasonCap bounds why a deployment failed. The preview reads it whole, so it is
// long enough for the build's own last line and short enough to stay one line of
// thought.
const reasonCap = 400

// reason is why a publish failed, as the person who asked for it reads it: the
// failure's own words as a sentence, with no "coding:" label in front and the
// first letter a capital.
func reason(err error) string {
	s := strings.TrimSpace(strings.TrimPrefix(err.Error(), "coding: "))
	if len(s) > reasonCap {
		n := reasonCap
		for n > 0 && !utf8.RuneStart(s[n]) {
			n--
		}
		s = strings.TrimSpace(s[:n]) + "…"
	}
	if r, n := utf8.DecodeRuneInString(s); n > 0 {
		s = string(unicode.ToUpper(r)) + s[n:]
	}
	return s
}

// clip keeps a string a person is shown short.
func clip(s string) string {
	if len(s) > 120 {
		return s[:120] + "…"
	}
	return s
}

// upload writes every file to the site through the grant, one curl process
// over one connection, and fails unless each file was accepted.
//
// The config is rendered here, where every value in it is known, and crosses
// compressed on stdin into a file only this uid reads. curl is started with -q
// so no .curlrc a build left behind is read, and the grant's policy and
// signature are blinded out of every stream.
func (in sandbox) upload(dir string, files []shipFile, up client.SiteUpload) error {
	cfg, err := uploadConfig(up, files)
	if err != nil {
		return err
	}
	var z bytes.Buffer
	w := gzip.NewWriter(&z)
	if _, err := w.Write([]byte(cfg)); err != nil {
		return fmt.Errorf("coding: compress the upload: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("coding: compress the upload: %w", err)
	}
	for _, f := range up.Fields {
		in.grant = append(in.grant, f.Value)
	}
	if _, err := in.exec([]string{"sh", "-c", `umask 077 && mkdir -p "$(dirname "$1")" && base64 -d | gzip -dc > "$1"`, "sh", uploadPath},
		base64.StdEncoding.EncodeToString(z.Bytes())+"\n", "carry the upload grant"); err != nil {
		return err
	}
	ran, err := runIn(in.ctx, in.id, []string{"sh", "-c", `trap 'rm -f "$2"' EXIT; cd "$1" && curl -q --config "$2"`, "sh", dir, uploadPath},
		min(in.ttl, uploadTimeout), in.session, "", in.secrets()...)
	if err != nil {
		return fmt.Errorf("coding: upload: %s", in.scrub(err.Error()))
	}
	return accepted(files, ran.Stdout, in.scrub(ran.Stderr))
}

// accepted checks curl's answer: one status line per file, in order, every one
// of them a 2xx.
func accepted(files []shipFile, stdout, stderr string) error {
	codes := strings.Fields(stdout)
	for i, f := range files {
		if i >= len(codes) {
			return fmt.Errorf("coding: the upload stopped at %s: %s", f.rel, lastLine(cmp.Or(stderr, "no answer")))
		}
		if n, err := strconv.Atoi(codes[i]); err != nil || n < 200 || n > 299 {
			return fmt.Errorf("coding: the store refused %s with %s: %s", f.rel, codes[i], lastLine(cmp.Or(stderr, "no reason given")))
		}
	}
	return nil
}

// uploadConfig is the curl config that POSTs every file to the grant's URL: per
// file, the grant's fields, the object's key under the grant's prefix, its
// type, and the file itself last, as the store requires. Only https is spoken
// and no redirect is followed.
//
// The grant's own `key` is the prefix its policy starts with, echoed by the
// signer; each file names its own key in its place, since the store refuses a
// form carrying a field twice.
func uploadConfig(up client.SiteUpload, files []shipFile) (string, error) {
	if !strings.HasPrefix(up.URL, "https://") {
		return "", fmt.Errorf("coding: the upload grant names %q, which is not an https address", up.URL)
	}
	prefix := strings.Trim(up.Prefix, "/")
	if prefix == "" {
		return "", fmt.Errorf("coding: the upload grant names no prefix")
	}
	fields := make([]client.Field, 0, len(up.Fields))
	for _, f := range up.Fields {
		switch strings.ToLower(f.Name) {
		case "key", "content-type", "file":
			continue
		}
		fields = append(fields, f)
	}
	slices.SortFunc(fields, func(a, b client.Field) int { return strings.Compare(a.Name, b.Name) })
	var b strings.Builder
	for i, f := range files {
		if i > 0 {
			b.WriteString("next\n")
		}
		fmt.Fprintf(&b, "url = %s\nsilent\nshow-error\nmax-time = 300\noutput = \"/dev/null\"\nwrite-out = \"%%{http_code}\\n\"\n", curlQuote(up.URL))
		for _, f := range fields {
			fmt.Fprintf(&b, "form-string = %s\n", curlQuote(f.Name+"="+f.Value))
		}
		ct := mime.TypeByExtension(path.Ext(f.rel))
		if ct == "" {
			ct = "application/octet-stream"
		}
		fmt.Fprintf(&b, "form-string = %s\nform-string = %s\nform = %s\n",
			curlQuote("key="+prefix+"/"+f.rel), curlQuote("Content-Type="+ct), curlQuote("file=@"+cmp.Or(f.src, f.rel)))
	}
	return b.String(), nil
}

// curlQuote is s as a quoted curl config value: backslash and double quote
// escaped, and a control character refused by the callers above ever reaching it.
func curlQuote(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	s = strings.ReplaceAll(s, "\n", `\n`)
	s = strings.ReplaceAll(s, "\r", `\r`)
	s = strings.ReplaceAll(s, "\t", `\t`)
	return `"` + s + `"`
}

// ── the project app's half ───────────────────────────────────────────────────

// openSite opens a deployment of org's project s on the project app, which
// checks the project, its repository and the org's funds, and answers the grant.
func openSite(org string, s Site) (*client.SiteOpened, error) {
	ctx, cancel := context.WithTimeout(plane.For(context.Background(), org), clientTimeout)
	defer cancel()
	return projectpeer.ProjectsSiteOpen(ctx, &client.SiteOpenIn{Slug: s.Project, Owner: s.Owner, Repo: s.Repo})
}

// closeSite ends a deployment openSite opened: live with what was built, or
// failed with why. It is on a context of its own, because the publish that
// ends it may be out of time, and a deployment left building reads as work
// still going.
func closeSite(org, project, id string, b built, cause error) (*client.SiteClosed, error) {
	ctx, cancel := context.WithTimeout(plane.For(context.Background(), org), clientTimeout)
	defer cancel()
	in := &client.SiteCloseIn{Slug: project, ID: id, Commit: b.commit}
	if cause != nil {
		in.Message = reason(cause)
	} else {
		in.Live, in.Files, in.Bytes, in.Keys = true, len(b.keys), b.bytes, b.keys
	}
	return projectpeer.ProjectsSiteClose(ctx, in)
}

// site publishes a run's pushed work to its project's site, from the checkout
// the run already has. What it did is said on the result, and a site that could
// not be published is said there too: the work is pushed either way, so it is
// never the run's failure.
func (in sandbox) site(org string, s Site, commit string, out *RunResult, step func(name, msg, status string)) {
	step("publish", "publishing to "+s.Project, "running")
	fail := func(err error) {
		out.Unpublished = in.scrub(err.Error())
		step("publish", out.Unpublished, "error")
	}
	opened, err := openSite(org, s)
	if err != nil {
		fail(err)
		return
	}
	var b built
	if strings.EqualFold(opened.Owner, s.Owner) && strings.EqualFold(opened.Repo, s.Repo) {
		b, err = in.publish(opened.Upload, step)
	} else {
		err = fmt.Errorf("coding: %s is built from %s/%s, not from this run's repository", s.Project, opened.Owner, opened.Repo)
	}
	b.commit = commit
	closed, cerr := closeSite(org, s.Project, opened.ID, b, err)
	switch {
	case err != nil:
		fail(err)
	case cerr != nil:
		fail(cerr)
	case closed.Status != "live":
		fail(fmt.Errorf("coding: %s", cmp.Or(closed.Message, "the site did not go live")))
	default:
		out.Live = closed.LiveURL
		step("publish", cmp.Or(closed.LiveURL, closed.Message, "published"), "ok")
	}
}

// ── a publish on its own ─────────────────────────────────────────────────────

// Publish builds org's project from its repository at ref in a sandbox and puts
// it on the project's site. It answers once the build is admitted; the build
// runs after, and the deployment it answers with is how it is followed.
//
// org is the CALLER's validated org and the only one anything here acts in. The
// project, its repository and the org's funds are the project app's to check,
// and the repository must be in the workspace the platform made for org — the
// estate's projects build in their own CI, and this refuses them.
func Publish(ctx context.Context, org string, in client.PublishIn, log func(msg string, kv ...any)) (*client.Publishing, error) {
	org = strings.TrimSpace(org)
	if !OrgRE.MatchString(org) {
		return nil, zip.ErrForbidden("coding: a publish needs a tenant")
	}
	project := strings.TrimSpace(in.Project)
	if !RepoRE.MatchString(project) {
		return nil, zip.ErrBadRequest(fmt.Sprintf("coding: %q is not a project name", project))
	}
	ref := strings.TrimSpace(in.Ref)
	if ref != "" && !BaseRE.MatchString(ref) {
		return nil, zip.ErrBadRequest(fmt.Sprintf("coding: %q is not a branch name", ref))
	}
	for _, s := range []string{in.Owner, in.Repo} {
		if s != "" && !RepoRE.MatchString(s) {
			return nil, zip.ErrBadRequest(fmt.Sprintf("coding: %q is not a repository name", s))
		}
	}
	ws, err := workspace(ctx, org, false)
	if err != nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "%s: %s", ErrWorkspace.Error(), err.Error())
	}
	if !ws.Made {
		return nil, zip.ErrForbidden("coding: a site is built in a sandbox for an organization whose code is in a Hanzo workspace; this one's builds in its own CI")
	}
	Engine(log)
	opened, err := openSite(org, Site{Project: project, Owner: in.Owner, Repo: in.Repo})
	if err != nil {
		return nil, err
	}
	// The answer crossed a process boundary, so it is checked on this side too:
	// the repository is in this org's workspace, and the ref is a branch.
	ref = cmp.Or(ref, opened.Branch)
	if !strings.EqualFold(opened.Owner, ws.Owner) || !RepoRE.MatchString(opened.Repo) || !BaseRE.MatchString(ref) {
		why := fmt.Errorf("coding: %s/%s at %q is not a branch of this organization's workspace", opened.Owner, opened.Repo, ref)
		if _, cerr := closeSite(org, project, opened.ID, built{}, why); cerr != nil && log != nil {
			log("coding: a refused publish's deployment was not ended", "org", org, "project", project, "err", cerr)
		}
		return nil, zip.ErrForbidden(why.Error())
	}
	// The person who asked, read while the call is still here: the build's sandbox
	// is leased as theirs (lessee), and the context the build runs on states only
	// the tenant.
	who := zip.CallerOf(ctx)
	go func() {
		runCtx, cancel := context.WithTimeout(plane.For(context.Background(), org), publishBudget)
		defer cancel()
		var b built
		var cause error
		defer func() {
			if r := recover(); r != nil {
				cause = fmt.Errorf("coding: the publish crashed: %v", r)
			}
			if _, err := closeSite(org, project, opened.ID, b, cause); err != nil && log != nil {
				log("coding: a publish's deployment was not ended", "org", org, "project", project, "deployment", opened.ID, "err", err)
			}
		}()
		b, cause = publishOnce(runCtx, org, ws.Owner, opened, ref, who, log)
		if cause != nil && log != nil {
			log("coding: a publish failed", "org", org, "project", project, "deployment", opened.ID, "err", cause)
		}
	}()
	return &client.Publishing{Deployment: opened.ID, Project: project, Ref: ref, Status: "building"}, nil
}

// lessee is ctx stating who as the person a publish's sandbox is leased by: its
// lease, every command in it and its end. The sandbox app pays a lease from the
// stated caller's wallet (principal.PayerFrom), and a caller that names only the
// tenant names no wallet, so the lease was refused ("this lease names no wallet")
// and no publish ever got a sandbox. It states what a coding run's lessee states
// (sandboxRunner.Run and paying): the person, the name their wallet is under, and
// the account their request named. Only the sandbox calls carry it — the read key
// and the site stay the organization's, so nothing on the forge acts as a person.
func lessee(ctx context.Context, org string, who zip.Caller) context.Context {
	c := zip.CallerOf(ctx)
	c.Org, c.User = org, strings.Clone(strings.TrimSpace(who.User))
	c.Name, c.Account = strings.Clone(strings.TrimSpace(who.Name)), strings.Clone(strings.TrimSpace(who.Account))
	return zip.WithCaller(ctx, c)
}

// publishOnce is one publish's work: a key that reads the one repository, a
// sandbox of its own, a clone at ref, the build and the upload. The key is
// withdrawn and the sandbox ended on every path. The sandbox is who's (lessee);
// everything else is the organization's.
func publishOnce(ctx context.Context, org, owner string, opened *client.SiteOpened, ref string, who zip.Caller, log func(msg string, kv ...any)) (built, error) {
	ws, err := workspace(ctx, org, false)
	if err != nil {
		return built{}, err
	}
	if !ws.Made || !strings.EqualFold(ws.Owner, owner) {
		return built{}, fmt.Errorf("coding: this organization's workspace moved while its publish was admitted")
	}
	if err := affordable(ctx, org); err != nil {
		return built{}, fmt.Errorf("coding: %w", err)
	}
	g, err := delegate(ctx, ws, "", opened.Repo, opened.ID, true)
	if err != nil {
		return built{}, err
	}
	defer func() {
		if err := withdraw(context.WithoutCancel(ctx), org, g); err != nil && log != nil {
			log("coding: a publish's read key was not withdrawn", "org", org, "repo", opened.Repo, "grant", g.String(), "err", err)
		}
	}()
	sbx := lessee(ctx, org, who)
	leased, err := sandboxpeer.SandboxLease(sbx, &client.LeaseIn{Class: "exec", TTLSec: publishTTL})
	if err != nil {
		return built{}, fmt.Errorf("coding: lease sandbox: %w", err)
	}
	if leased == nil || strings.TrimSpace(leased.ID) == "" {
		return built{}, fmt.Errorf("coding: lease sandbox: no id")
	}
	defer func() {
		end, cancel := context.WithTimeout(context.WithoutCancel(sbx), 30*time.Second)
		defer cancel()
		_, _ = sandboxpeer.SandboxEnd(end, &client.EndIn{ID: leased.ID})
	}()
	req := RunRequest{Remote: g.Remote, Key: g.Key, Known: g.Known, BaseBranch: ref}
	box := sandbox{ctx: sbx, id: leased.ID, dir: leased.Workdir, ttl: publishTTL, key: g.Key}
	if err := box.arm(req); err != nil {
		return built{}, err
	}
	if _, err := box.do([]string{"git", "config", "--global", "--add", "safe.directory", leased.Workdir}, "trust the checkout"); err != nil {
		return built{}, err
	}
	if err := box.cloneRemote(&req); err != nil {
		return built{}, err
	}
	commit, err := box.tip()
	if err != nil {
		return built{}, err
	}
	quiet := func(string, string, string) {}
	b, err := box.publish(opened.Upload, quiet)
	b.commit = commit
	return b, err
}
