package coding

// artifact.go — what a run leaves, kept after its sandbox is gone.
//
// A run's sandbox is kept while it is used and parked after it, and a park is
// retired with its disk. What the run made must outlive that, so at the end of
// every run its change is saved beside it in the object store:
//
//	<org>/<session>/files/<path>    each file it added or changed, as it left it
//	<org>/<session>/changes.patch   the whole change against where it started
//	<org>/<session>/index.json      the list: those, its commits, and its links —
//	                                the ports it served, its pull request, where
//	                                its work was published
//
// The org is the run's validated org and the session is the run, so a key names
// one run of one org. The sandbox uploads the bytes against a grant that writes
// that run's prefix alone (the same curl form publish.go posts a site with), and
// this process writes the index. Who may read a run reads what it left: every
// read goes through the run's session (agents.Visible).

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/hanzoai/cloud/sdk/object"
	"io"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/cloud/client"
	sandboxpeer "github.com/hanzoai/cloud/client/sandbox"
	"github.com/hanzoai/cloud/sdk/env"
	"github.com/zap-proto/zip"
)

const (
	// artifactFiles, artifactFile and artifactBytes bound what one run saves: how
	// many files, how large one may be, and all of them together.
	artifactFiles = 200
	artifactFile  = 8 << 20
	artifactBytes = 64 << 20
	// artifactGrant is how long the sandbox's upload grant is good for.
	artifactGrant = 4 * time.Hour
	// patchPath is where the sandbox writes the run's patch, beside its key.
	patchPath = "/tmp/hanzo-run/changes.patch"
	// Artifact kinds.
	kindFile    = "file"
	kindPatch   = "patch"
	kindPreview = "preview"
	kindPull    = "pull"
	kindDeploy  = "deploy"
)

// objects is where runs' artifacts are kept: a grant the sandbox uploads a run's
// files against, and the index this process writes and reads.
type objects interface {
	grant(ctx context.Context, org, session string) (client.SiteUpload, error)
	put(ctx context.Context, key string, data []byte) error
	open(ctx context.Context, key string) (io.ReadCloser, int64, error)
}

// runs is the bucket every run's artifacts are kept in. A var so a test can
// stand in for the store; never reassigned in production.
var runs objects = &bucket{store: object.Default(), name: env.Or("CODING_ARTIFACTS_BUCKET", "hanzo-runs")}

// bucket is the object store.
type bucket struct {
	store object.Store
	name  string
	made  object.Made
}

// ready makes the bucket on first use.
func (b *bucket) ready(ctx context.Context) error { return b.made.Ensure(ctx, b.store, b.name) }

// prefix is where one run's artifacts are kept.
func prefix(org, session string) string { return org + "/" + session }

// grant is a form upload that writes under one run's prefix and nowhere else,
// signed for the address the sandbox reaches the store at.
func (b *bucket) grant(ctx context.Context, org, session string) (client.SiteUpload, error) {
	if err := b.ready(ctx); err != nil {
		return client.SiteUpload{}, err
	}
	pre := prefix(org, session)
	// The trailing slash keeps one run's prefix from naming another's.
	f, err := b.store.Form(ctx, object.Form{Bucket: b.name, Prefix: pre + "/", TTL: artifactGrant, MaxBytes: artifactFile})
	if errors.Is(err, object.ErrNoPresign) {
		return client.SiteUpload{}, fmt.Errorf("coding: the object store takes no uploads here")
	}
	if err != nil {
		return client.SiteUpload{}, fmt.Errorf("coding: artifact grant: %w", err)
	}
	return client.SiteUpload{URL: f.URL, Fields: client.Fields(f.Fields), Prefix: pre, MaxBytes: artifactFile, ExpiresAt: time.Now().Add(artifactGrant).Unix()}, nil
}

// put writes one object.
func (b *bucket) put(ctx context.Context, key string, data []byte) error {
	if err := b.ready(ctx); err != nil {
		return err
	}
	_, err := b.store.Put(ctx, b.name, key, bytes.NewReader(data), int64(len(data)), object.Put{ContentType: "application/json"})
	return err
}

// open reads one object, and errNoArtifact when there is none.
func (b *bucket) open(ctx context.Context, key string) (io.ReadCloser, int64, error) {
	body, err := b.store.Open(ctx, b.name, key, object.Get{})
	if object.Absent(err) {
		return nil, 0, errNoArtifact
	}
	if err != nil {
		return nil, 0, err
	}
	return body, body.Info.Size, nil
}

var errNoArtifact = errors.New("coding: no such artifact")

// index is what index.json holds.
type index struct {
	client.CodingArtifacts
	// Repo, Base and Head are the run's, and Commits its commits, newest first.
	Repo    string                `json:"repo,omitempty"`
	Base    string                `json:"base,omitempty"`
	Head    string                `json:"head,omitempty"`
	Commits []client.CodingCommit `json:"commits,omitempty"`
}

// saveIndex writes a run's index.
func saveIndex(ctx context.Context, org, session string, ix index) error {
	ix.Session = session
	ix.Saved = time.Now().UTC().Format(time.RFC3339)
	if ix.Artifacts == nil {
		ix.Artifacts = []client.Artifact{}
	}
	b, err := json.Marshal(ix)
	if err != nil {
		return err
	}
	return runs.put(ctx, prefix(org, session)+"/index.json", b)
}

// readIndex reads a run's index; a run that saved none answers an empty one.
func readIndex(ctx context.Context, org, session string) (index, error) {
	r, _, err := runs.open(ctx, prefix(org, session)+"/index.json")
	if errors.Is(err, errNoArtifact) {
		return index{CodingArtifacts: client.CodingArtifacts{Session: session, Artifacts: []client.Artifact{}}}, nil
	}
	if err != nil {
		return index{}, err
	}
	defer func() { _ = r.Close() }()
	var ix index
	if err := json.NewDecoder(io.LimitReader(r, 4<<20)).Decode(&ix); err != nil {
		return index{}, fmt.Errorf("coding: the run's index is unreadable: %w", err)
	}
	return ix, nil
}

// Artifacts lists what the run a session names left, as the caller on ctx may
// read it.
func Artifacts(ctx context.Context, session string) (*client.CodingArtifacts, error) {
	x, err := visibleRun(ctx, session)
	if err != nil {
		return nil, err
	}
	if x.Kind != kindCoding {
		return nil, zip.ErrNotFound("coding: no coding run has that session")
	}
	ix, err := readIndex(ctx, x.Org, x.ID)
	if err != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "%s", err.Error())
	}
	return &ix.CodingArtifacts, nil
}

// Artifact opens one stored artifact of the run a session names: its bytes and
// their length. Only a name the run's index lists is opened.
func Artifact(ctx context.Context, session, name string) (io.ReadCloser, int64, error) {
	x, err := visibleRun(ctx, session)
	if err != nil {
		return nil, 0, err
	}
	if x.Kind != kindCoding {
		return nil, 0, zip.ErrNotFound("coding: no coding run has that session")
	}
	ix, err := readIndex(ctx, x.Org, x.ID)
	if err != nil {
		return nil, 0, zip.Errorf(http.StatusBadGateway, "%s", err.Error())
	}
	for _, a := range ix.Artifacts {
		if a.Name != name || (a.Kind != kindFile && a.Kind != kindPatch) {
			continue
		}
		r, n, err := runs.open(ctx, prefix(x.Org, x.ID)+"/"+keyOf(a))
		if errors.Is(err, errNoArtifact) {
			return nil, 0, zip.ErrNotFound("coding: that artifact is not stored")
		}
		if err != nil {
			return nil, 0, zip.Errorf(http.StatusBadGateway, "coding: read the artifact: %v", err)
		}
		return r, n, nil
	}
	return nil, 0, zip.ErrNotFound("coding: the run left no artifact by that name")
}

// keyOf is where a stored artifact is kept under its run's prefix.
func keyOf(a client.Artifact) string {
	if a.Kind == kindPatch {
		return "changes.patch"
	}
	return "files/" + a.Name
}

// changeScript lists what the run changed in the checkout at $2 against its base
// ($1), in a scratch index
// so the checkout's own is untouched: the patch to patchPath, then one line per
// kept file, "size<TAB>sha256<TAB>path", NUL-free paths only. `git add -A` in the
// scratch index is what brings untracked files in and leaves ignored ones out.
const changeScript = `set -eu
cd "$2"
i=/tmp/hanzo-run/index
mkdir -p /tmp/hanzo-run
rm -f "$i"
[ -f .git/index ] && cp .git/index "$i"
export GIT_INDEX_FILE="$i"
git add -A >/dev/null 2>&1 || true
git -c core.quotepath=false diff --cached --binary ` + diffPrefixes + ` "$1" > ` + patchPath + `
printf 'patch\t%s\t%s\n' "$(stat -c %s ` + patchPath + `)" "$(sha256sum ` + patchPath + ` | cut -c1-64)"
git log -n 250 --format='commit%x09%H%x09%an%x09%aI%x09%s' "$1..HEAD"
git -c core.quotepath=false diff --cached --name-only --no-renames --diff-filter=d "$1" | while IFS= read -r f; do
  if [ -f "$f" ] && [ ! -L "$f" ]; then
    printf '%s\t%s\t%s\n' "$(stat -c %s -- "$f")" "$(sha256sum -- "$f" | cut -c1-64)" "$f"
  fi
done
rm -f "$i"`

// listed is one listed file: its path in the workspace, size and digest.
type listed struct {
	path string
	size int64
	sum  string
}

// parseListed reads changeScript's output: the patch's line, "patch<TAB>size<TAB>
// sha256", the commits, "commit<TAB>sha<TAB>author<TAB>date<TAB>subject", newest
// first, then the files — those past the bounds left out and named.
func parseListed(out string) (patch listed, commits []client.CodingCommit, files []listed, left []string) {
	var total int64
	for line := range strings.SplitSeq(strings.TrimRight(out, "\n"), "\n") {
		if c, ok := strings.CutPrefix(line, "commit\t"); ok {
			if f := strings.SplitN(c, "\t", 4); len(f) == 4 {
				commits = append(commits, client.CodingCommit{SHA: f[0], Author: f[1], Date: f[2], Message: f[3]})
			}
			continue
		}
		f := strings.SplitN(line, "\t", 3)
		if len(f) != 3 {
			continue
		}
		if f[0] == "patch" {
			if size, err := strconv.ParseInt(f[1], 10, 64); err == nil && len(f[2]) == 64 {
				patch = listed{path: patchPath, size: size, sum: f[2]}
			}
			continue
		}
		size, err := strconv.ParseInt(f[0], 10, 64)
		switch {
		case err != nil || size < 0 || len(f[1]) != 64 || !keepable(f[2]):
			continue
		case size > artifactFile || len(files) == artifactFiles || total+size > artifactBytes:
			left = append(left, f[2])
			continue
		}
		total += size
		files = append(files, listed{path: f[2], size: size, sum: f[1]})
	}
	return patch, commits, files, left
}

// keepable is a workspace path the upload can name as it is: a clean relative
// path of printable ASCII with none of the characters a curl form gives a
// meaning, outside .git.
func keepable(rel string) bool {
	if rel == "" || len(rel) > 512 || path.Clean(rel) != rel || strings.HasPrefix(rel, "/") ||
		rel == ".git" || strings.HasPrefix(rel, ".git/") || strings.HasPrefix(rel, "../") {
		return false
	}
	for i := 0; i < len(rel); i++ {
		if c := rel[i]; c <= ' ' || c >= 0x7f || c == '"' || c == '\\' || c == ';' || c == ',' {
			return false
		}
	}
	return true
}

// store saves the run's change from the sandbox — its patch and each file it
// kept — and answers them as artifacts, beside the commits on its branch.
func (in sandbox) store(org, base string, step func(name, msg, status string)) ([]client.Artifact, []client.CodingCommit, error) {
	_ = in.lend(false)
	ran, err := in.do([]string{"bash", "-c", changeScript, "bash", base, in.dir}, "list what the run changed")
	if err != nil {
		return nil, nil, err
	}
	patch, commits, files, left := parseListed(ran.Stdout)
	ship := make([]shipFile, 0, len(files)+1)
	arts := make([]client.Artifact, 0, len(files)+1)
	if patch.size > 0 && patch.size <= artifactFile {
		ship = append(ship, shipFile{rel: "changes.patch", src: patchPath, size: patch.size})
		arts = append(arts, client.Artifact{Name: "changes.patch", Kind: kindPatch, Size: patch.size, SHA256: patch.sum})
	}
	for _, f := range files {
		ship = append(ship, shipFile{rel: "files/" + f.path, src: f.path, size: f.size})
		arts = append(arts, client.Artifact{Name: f.path, Kind: kindFile, Size: f.size, SHA256: f.sum})
	}
	if len(left) > 0 {
		step("artifacts", "left out, over what a run keeps: "+strings.Join(left, ", "), "error")
	}
	if len(ship) == 0 {
		return arts, commits, nil
	}
	up, err := runs.grant(in.ctx, org, in.session)
	if err != nil {
		return nil, commits, err
	}
	step("artifacts", fmt.Sprintf("saving %d artifacts", len(ship)), "running")
	if err := in.upload(in.dir, ship, up); err != nil {
		return nil, commits, err
	}
	step("artifacts", fmt.Sprintf("saved %d artifacts", len(ship)), "ok")
	return arts, commits, nil
}

// previews are the ports something listens on in the sandbox, as artifacts.
func (in sandbox) previews() []client.Artifact {
	ps, err := sandboxpeer.SandboxPorts(in.ctx, &client.PortsIn{ID: in.id})
	if err != nil || ps == nil {
		return nil
	}
	out := make([]client.Artifact, 0, len(ps.Ports))
	for _, p := range ps.Ports {
		out = append(out, client.Artifact{Name: "port " + strconv.Itoa(p.Port), Kind: kindPreview,
			URL: "https://" + p.Host + "/", Port: p.Port, Sandbox: in.id})
	}
	return out
}

// links are the run's pull request and publication, as artifacts.
func links(pr, live string) []client.Artifact {
	var out []client.Artifact
	if pr != "" {
		out = append(out, client.Artifact{Name: "pull request", Kind: kindPull, URL: pr})
	}
	if live != "" {
		out = append(out, client.Artifact{Name: "published", Kind: kindDeploy, URL: live})
	}
	return out
}

// ArtifactType is what an artifact is served as: text a person can read, else
// bytes; never a type a browser would run.
func ArtifactType(name string, head []byte) string {
	if bytes.IndexByte(head, 0) < 0 && !binaryName(name) {
		return "text/plain; charset=utf-8"
	}
	return "application/octet-stream"
}

// binaryName is a file whose name says it is not text.
func binaryName(name string) bool {
	switch strings.ToLower(path.Ext(name)) {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".ico", ".pdf", ".zip", ".gz", ".tar", ".wasm", ".woff", ".woff2":
		return true
	}
	return false
}

// Disposition is how an artifact's name reads in a header: its base name, quoted.
func Disposition(name string) string {
	return strconv.Quote(cmp.Or(path.Base(name), "artifact"))
}
