package deploy

// release.go — GET /v1/deploy/release: why a service is, or is not, live.
//
// A release of a service moves through facts that each have one home, and this
// reads every one where it is kept rather than keeping any of them:
//
//	claimed   the version tag on the source commit         the forge (refs/tags/v*)
//	ran       the pipeline's runs on that commit, its jobs  the forge (Actions)
//	smoked    refs/tags/smoked/sha256-<digest> at it        the forge (the RECORD fact)
//	pinned    the image universe's values files name        universe at CD's commit (pin.go)
//	live      the image the pods run, and its commit        CD and the registry (live.go)
//
// The service's row is live's own (one reading of an Application); the source
// repository is the one the running image says it was built from. The forge is
// read as the caller, in a namespace the caller's org owns, so the forge's ACL
// decides what comes back.

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"

	"golang.org/x/sync/errgroup"

	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/forge"
	"github.com/hanzoai/cloud/sdk"
)

// releaseForge is the forge as the request's caller: their own login, proved by
// forge.Client.Caller, so the forge's ACL decides what they may read. A test
// stands in for it.
var releaseForge = func(ctx context.Context) (*forge.Client, error) {
	c, err := forge.Dial(ctx, cloud.KMSPeer{})
	if err != nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "the forge is not configured: %v", err)
	}
	login, err := c.Caller(ctx)
	if err != nil {
		return nil, zip.ErrForbidden(err.Error())
	}
	return c.As(login), nil
}

// releaseQuery names the service whose release to read.
type releaseQuery struct {
	// Service is the service, by the name live lists it under, such as hanzo-cloud.
	Service string `query:"service" json:"service"`
	// Repo is the forge repository the service is built from, owner/name. Empty
	// reads it off the image the service runs.
	Repo string `query:"repo" json:"repo,omitempty"`
	// Versions is how many of the newest claimed versions to read; 0 is 3, and 10
	// is the ceiling. The pinned and the live version are read as well.
	Versions int `query:"versions" json:"versions,omitempty"`
}

// Release is where a service's release stands.
type Release struct {
	// Service is the service asked about.
	Service string `json:"service"`
	// Repo is the forge repository it is built from, owner/name.
	Repo string `json:"repo"`
	// Stuck says in one sentence where the newest claimed version stopped on its
	// way to live, and what is live instead. Empty when the newest claimed
	// version is the one live.
	Stuck string `json:"stuck,omitempty"`
	// Pinned is the image universe pins the service to.
	Pinned *Image `json:"pinned,omitempty"`
	// Live is the service's image of Repo its pods run now, with the commit it
	// was built from where the registry answers it.
	Live *Image `json:"live,omitempty"`
	// Sync is CD's verdict on git against the cluster: Synced, OutOfSync or Unknown.
	Sync string `json:"sync"`
	// Health is CD's verdict on the objects it manages: Healthy, Progressing,
	// Degraded, Suspended, Missing or Unknown.
	Health string `json:"health"`
	// Drift names what disagrees between the pin and the pods, as live does.
	Drift []string `json:"drift"`
	// Head is the default branch's tip and what ran on it.
	Head Built `json:"head"`
	// Versions are the newest claimed versions, newest first, with the pinned
	// and the live one among them.
	Versions []Version `json:"versions"`
}

// Version is one claimed version: a v-tag on a source commit.
type Version struct {
	// Tag is the version, such as v8.5.1131.
	Tag string `json:"tag"`
	// Smoked are the image digests the pipeline booted and recorded at the
	// version's commit (refs/tags/smoked/sha256-<digest>). Empty means no image of
	// it was ever proven to boot, so nothing may pin it.
	Smoked []string `json:"smoked"`
	// Pinned is whether universe pins the service to it.
	Pinned bool `json:"pinned"`
	// Live is whether the service's pods run it.
	Live bool `json:"live"`
	Built
}

// Built is one commit and what the pipeline did with it.
type Built struct {
	// Commit is the source commit.
	Commit string `json:"commit"`
	// State answers "is this commit green?": passed, failed, running, queued or
	// none, over the latest attempt of each workflow on it.
	State string `json:"state"`
	// Run is the newest run on the commit, by its id on the forge, as `hanzo git
	// runs get` takes it with the repository.
	Run int64 `json:"run,omitempty"`
	// Number is that run's number in the repository, as the forge's page shows it.
	Number int `json:"number,omitempty"`
	// URL is that run's page on the forge.
	URL string `json:"url,omitempty"`
	// Failed are the jobs that failed in the runs on the commit that count, with
	// the lines of each log that say what failed.
	Failed []FailedJob `json:"failed"`
}

// FailedJob is one job that failed and what it said.
type FailedJob struct {
	// Run is the forge id of the run the job is in, as `hanzo git runs logs` takes it.
	Run int64 `json:"run"`
	// ID is the job's id on the forge.
	ID int64 `json:"id"`
	// Name is the job's name in the workflow.
	Name string `json:"name"`
	// Failing are the lines of its log that say what failed: a failing test and
	// what it reported, a failing package, a panic, a diagnostic, an annotated
	// error. Read for the head and the versions newer than the pin, six logs a
	// request; absent otherwise, and for a job that wrote no log of its own — a
	// reusable workflow's caller, whose own jobs are listed beside it.
	Failing []string `json:"failing,omitempty"`
	// Fault is why the job's log could not be read; the rest of the answer stands.
	Fault string `json:"fault,omitempty"`
}

// version is a release tag: v<major>.<minor>.<patch>, nothing after it.
var version = regexp.MustCompile(`^v(\d+)\.(\d+)\.(\d+)$`)

// failedLogs bounds the job logs one request reads for their failing lines —
// the head's first, then the newest versions' — and failingLines what one job
// reports of them.
const (
	failedLogs   = 6
	failingLines = 20
)

// release answers why a service is, or is not, live: for the newest versions
// claimed in its source repository, the commit each names, whether the
// pipeline's runs on it passed and which jobs failed with the lines that say
// why, whether an image of it was smoked, whether universe pins it, and whether
// it is what the pods run — and stuck, one sentence saying where the newest
// version stopped.
//
// The service is scoped exactly as live is; its source repository is read off
// the image it runs unless repo names it, and is read from the forge as the
// caller, in a namespace the caller's org owns.
//
// Example: {"service": "hanzo-cloud"}
func (o ops) release(ctx context.Context, in *releaseQuery) (*Release, error) {
	if strings.TrimSpace(in.Service) == "" {
		return nil, zip.ErrBadRequest("service is required")
	}
	sc, err := scopeOf(ctx)
	if err != nil {
		return nil, err
	}
	lv, err := o.live(ctx, &liveQuery{Service: in.Service})
	if err != nil {
		return nil, err
	}
	if len(lv.Services) != 1 {
		return nil, zip.ErrNotFound("no service " + in.Service + " you can see")
	}
	row := lv.Services[0]
	running := ours(row)
	owner, name, err := sourceOf(in.Repo, running)
	if err != nil {
		return nil, err
	}
	if o, err := forge.Org(owner); !sc.superAdmin && (err != nil || !strings.EqualFold(o, sc.org)) {
		return nil, zip.ErrForbidden(owner + " is not a forge namespace of your org")
	}
	fc, err := releaseForge(ctx)
	if err != nil {
		return nil, err
	}

	out := &Release{Service: row.Name, Repo: owner + "/" + name, Pinned: row.Declared, Live: running,
		Sync: row.Sync, Health: row.Health, Drift: row.Drift, Versions: []Version{}}
	var tags, smoked []forge.Ref
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() (err error) { out.Head.Commit, err = fc.Resolve(gctx, owner, name, ""); return err })
	g.Go(func() (err error) { tags, err = fc.Refs(gctx, owner, name, "tags/v"); return err })
	g.Go(func() (err error) { smoked, err = fc.Refs(gctx, owner, name, "tags/smoked/sha256-"); return err })
	if err := g.Wait(); err != nil {
		return nil, forgeRead(err)
	}
	kept := versions(tags, out.Pinned, out.Live, cmp.Or(min(max(in.Versions, 0), 10), 3))
	out.Versions = make([]Version, len(kept))

	// Only the kept tags are peeled, so a repository of annotated tags costs a
	// read per version shown and not one per tag it holds. A receipt is a
	// lightweight ref the pipeline writes at the commit, so smoked refs are read
	// as they are listed.
	g, gctx = errgroup.WithContext(ctx)
	g.SetLimit(4)
	for i, k := range kept {
		out.Versions[i] = k.v
		g.Go(func() error {
			sha, err := fc.Peel(gctx, owner, name, k.ref)
			out.Versions[i].Commit = sha
			return err
		})
	}
	if err := g.Wait(); err != nil {
		return nil, forgeRead(err)
	}
	for i := range out.Versions {
		for _, sm := range smoked {
			if !sm.Annotated && sm.SHA == out.Versions[i].Commit {
				out.Versions[i].Smoked = append(out.Versions[i].Smoked, "sha256:"+strings.TrimPrefix(sm.Name, "refs/tags/smoked/sha256-"))
			}
		}
	}

	// The failing lines are read for the commits that did not make it: the head,
	// and each version newer than the one pinned.
	newer := len(out.Versions)
	for i, v := range out.Versions {
		if v.Pinned {
			newer = i
			break
		}
	}
	// The head first, so its failing lines are read before any version's.
	logs := &atomic.Int32{}
	logs.Store(failedLogs)
	if out.Head, err = built(ctx, fc, owner, name, out.Head.Commit, logs); err != nil {
		return nil, forgeRead(err)
	}
	g, gctx = errgroup.WithContext(ctx)
	g.SetLimit(4)
	for i := range out.Versions {
		budget := logs
		if i >= newer {
			budget = &atomic.Int32{}
		}
		g.Go(func() (err error) {
			out.Versions[i].Built, err = built(gctx, fc, owner, name, out.Versions[i].Commit, budget)
			return err
		})
	}
	if err := g.Wait(); err != nil {
		return nil, forgeRead(err)
	}
	out.Stuck = stuck(out)
	return out, nil
}

// ours is the image of the service's own build among those its pods run: the
// one of the repository the pin names, else the first the registry says was
// built from a source.
func ours(r Running) *Image {
	for i, im := range r.Images {
		if r.Declared != nil && im.Repository == r.Declared.Repository {
			return &r.Images[i]
		}
	}
	for i, im := range r.Images {
		if im.Repo != "" {
			return &r.Images[i]
		}
	}
	return nil
}

// sourceOf is the forge repository a service is built from: the one named, or
// the one its running image's source label says — the host is not read,
// github.com and git.hanzo.ai holding the same repository at the same name.
func sourceOf(named string, running *Image) (owner, name string, err error) {
	src := strings.TrimSuffix(strings.TrimSpace(named), ".git")
	if src == "" && running != nil && running.Repo != "" {
		u, perr := url.Parse(running.Repo)
		if perr == nil {
			src = strings.TrimSuffix(strings.Trim(u.Path, "/"), ".git")
		}
	}
	owner, name, ok := strings.Cut(src, "/")
	if !ok || owner == "" || name == "" || strings.Contains(name, "/") {
		return "", "", zip.ErrBadRequest("name the repository the service is built from: repo=owner/name")
	}
	return owner, name, nil
}

// kept is one version chosen to read, and the reference that claims it.
type kept struct {
	v   Version
	ref forge.Ref
}

// versions are the newest n claimed versions, and the pinned and live ones,
// newest first, chosen by their names alone: nothing is read to choose them.
func versions(tags []forge.Ref, pinned, live *Image, n int) []kept {
	type tagged struct {
		kept
		key [3]int
	}
	var all []tagged
	for _, r := range tags {
		tag := strings.TrimPrefix(r.Name, "refs/tags/")
		m := version.FindStringSubmatch(tag)
		if m == nil {
			continue
		}
		var key [3]int
		for i := range key {
			key[i], _ = strconv.Atoi(m[i+1])
		}
		all = append(all, tagged{kept{Version{Tag: tag, Smoked: []string{}}, r}, key})
	}
	slices.SortFunc(all, func(a, b tagged) int { return slices.Compare(b.key[:], a.key[:]) })
	out := []kept{}
	for i, t := range all {
		t.v.Pinned = pinned != nil && pinned.Tag == t.v.Tag
		t.v.Live = live != nil && live.Tag == t.v.Tag
		if i >= n && !t.v.Pinned && !t.v.Live {
			continue
		}
		out = append(out, t.kept)
	}
	return out
}

// built reads what the pipeline did with one commit: every run on it, the
// verdict over the runs it is judged by (forge.Counted), the newest run, and the
// jobs that failed in those runs — with their failing lines while logs, the
// request's one budget of log reads, lasts. Jobs are read for the counted runs
// only, so a commit with a thousand runs costs a read per workflow.
func built(ctx context.Context, fc *forge.Client, owner, name, sha string, logs *atomic.Int32) (Built, error) {
	out := Built{Commit: sha, State: forge.None, Failed: []FailedJob{}}
	runs, err := fc.Runs(ctx, owner, name, forge.RunQuery{SHA: sha})
	if err != nil || len(runs) == 0 {
		return out, err
	}
	counted := forge.Counted(runs)
	jobs := make([][]forge.Job, len(runs))
	for _, i := range counted {
		if jobs[i], err = fc.Jobs(ctx, owner, name, runs[i].ID); err != nil {
			return out, err
		}
	}
	out.State = forge.Verdict(runs, jobs)
	out.Run, out.Number, out.URL = runs[0].ID, runs[0].Number, runs[0].URL
	for _, i := range counted {
		for _, j := range jobs[i] {
			if j.Conclusion != "failure" {
				continue
			}
			f := FailedJob{Run: runs[i].ID, ID: j.ID, Name: j.Name}
			if len(j.Steps) > 0 && logs.Add(-1) >= 0 {
				if l, err := fc.Log(ctx, owner, name, j.ID); err != nil {
					f.Fault = err.Error()
				} else {
					f.Failing = l.Failing[:min(len(l.Failing), failingLines)]
				}
			}
			out.Failed = append(out.Failed, f)
		}
	}
	return out, nil
}

// stuck is where the newest claimed version stopped, in one sentence: claimed
// and never smoked, smoked and never pinned, pinned and not what the pods run.
// Empty when the newest claimed version is live.
func stuck(r *Release) string {
	if len(r.Versions) == 0 {
		return "no version of " + r.Repo + " has been claimed"
	}
	v := r.Versions[0]
	if v.Live {
		return ""
	}
	at := v.Tag + " (" + short(v.Commit) + ")"
	var why string
	switch {
	case len(v.Smoked) == 0:
		why = at + " was claimed and never smoked: " + said(v.Built)
	case !v.Pinned:
		why = at + " was smoked and universe does not pin it"
	default:
		why = at + " is pinned and its pods do not run it: CD is " + r.Sync + "/" + r.Health
	}
	if r.Live != nil && r.Live.Tag != "" {
		why += "; " + r.Live.Tag + " is live"
	}
	return why
}

// said is what a commit's runs said, in a clause.
func said(b Built) string {
	if b.State == forge.None {
		return "nothing ran on it"
	}
	names := make([]string, 0, len(b.Failed))
	for _, f := range b.Failed {
		if !slices.Contains(names, f.Name) {
			names = append(names, f.Name)
		}
	}
	if len(names) == 0 {
		return "its runs are " + b.State
	}
	return fmt.Sprintf("its runs %s in %s", b.State, strings.Join(names, ", "))
}

func short(sha string) string { return sha[:min(len(sha), 10)] }

// forgeRead says whose fault a failed forge read was.
func forgeRead(err error) error {
	switch {
	case errors.Is(err, forge.ErrBadName):
		return zip.ErrBadRequest(err.Error())
	case errors.Is(err, forge.ErrNotFound), errors.Is(err, forge.ErrUnknownActor):
		return zip.ErrNotFound("no such repository on the forge, or not one you can read")
	case errors.Is(err, forge.ErrRefused):
		return zip.ErrForbidden("the forge refused this read for you")
	}
	var he *zip.HTTPError
	if errors.As(err, &he) {
		return err
	}
	return zip.Errorf(http.StatusBadGateway, "the forge could not be read: %v", err)
}
