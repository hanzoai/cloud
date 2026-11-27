package deploy

// live.go — GET /v1/deploy/live: which image, and which commit, each service is
// running, as Hanzo CD itself reports it, and whether that is what it is pinned to.
//
// Every fact here is CD's own except two. The images are status.summary.images,
// which CD collects from the live pods it manages; the commit applied, the sync
// and health verdicts and the deploy history are the same reads /v1/deploy/gitops
// makes (observeGitOpsApp). CD does not hold the commit an image was BUILT from,
// which only the registry knows (revision.go), nor the image a service is PINNED
// to, which is a value in the universe files CD renders (pin.go).

import (
	"context"
	"slices"
	"sort"
	"strings"
	"sync"

	"golang.org/x/sync/errgroup"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/k8s"
)

// Live is the reply: one row per CD Application the caller may see.
type Live struct {
	// Installed is whether this cluster serves the CD Application CRD at all.
	// False is a fact about the cluster, not a failure of the request.
	Installed bool `json:"installed"`
	// Reason says why the plane is absent. Empty when Installed.
	Reason string `json:"reason,omitempty"`
	// Services is every visible Application the query keeps, ordered by namespace
	// then name. Empty, never null.
	Services []Running `json:"services"`
	// Live answers the commit query: whether any listed service runs an image
	// built from that commit. Absent when no commit was asked, and when it cannot
	// be told: an image whose build commit the registry did not answer, such as a
	// private one this deployment holds no credential for, could be the one.
	Live *bool `json:"live,omitempty"`
}

// liveQuery narrows GET /v1/deploy/live.
type liveQuery struct {
	// Service lists one service, by the name it is listed under. Empty lists them all.
	Service string `query:"service" json:"service,omitempty"`
	// Commit asks whether the listed services run an image built from this source
	// commit: at least seven hex characters of its sha, matched as a prefix.
	Commit string `query:"commit" json:"commit,omitempty"`
}

// Running is what one service has live.
type Running struct {
	// Name is the CD Application's name, which the fleet mints as
	// <namespace>-<app>.
	Name string `json:"name"`
	// Namespace is where the service's workloads run (spec.destination.namespace).
	Namespace string `json:"namespace"`
	// Images are the images the service's pods are running now, as CD collected
	// them from the live pods. Two of one repository is a rollout in progress.
	Images []Image `json:"images"`
	// RepoURL is the git repository CD applies this service from, usually universe.
	RepoURL string `json:"repoURL,omitempty"`
	// Revision is the commit of that repository CD last applied. Empty means it
	// has applied none.
	Revision string `json:"revision,omitempty"`
	// Sync is CD's verdict on git against the cluster: Synced, OutOfSync or Unknown.
	Sync string `json:"sync"`
	// Health is CD's verdict on the objects it manages: Healthy, Progressing,
	// Degraded, Suspended, Missing or Unknown.
	Health string `json:"health"`
	// SyncedAt is when CD last finished applying a revision, RFC 3339. Empty when
	// it never has.
	SyncedAt string `json:"syncedAt,omitempty"`
	// Declared is the image the service's values files pin at Revision: what
	// universe says it should run. Absent for a service CD renders from plain
	// manifests, one that pins no image, and one whose files could not be read
	// (Drift says unread).
	Declared *Image `json:"declared,omitempty"`
	// Converged is whether nothing disagrees: Drift is empty.
	Converged bool `json:"converged"`
	// Drift names what disagrees, from: image, the pods do not run the declared
	// tag and digest; sync, CD's verdict is not Synced; health, CD's verdict is not
	// Healthy; unread, the declaration could not be read so agreement is unproven.
	// Empty, never null, when converged.
	Drift []string `json:"drift"`
	// Previous is the image CD deployed before the one it deployed last: the
	// newest earlier deploy in CD's history whose pin differs, which is what a
	// rollback returns to. Deploys that changed something else are passed over.
	// Absent when CD's history, which it bounds itself, holds no earlier image.
	Previous *Image `json:"previous,omitempty"`
}

// Image is one image reference a service runs.
type Image struct {
	// Ref is the reference exactly as the pods run it.
	Ref string `json:"ref"`
	// Repository is the reference without its tag or digest.
	Repository string `json:"repository"`
	// Tag is the release name, when the reference carries one.
	Tag string `json:"tag,omitempty"`
	// Digest is the sha256 of the bytes, when the reference is pinned by one.
	Digest string `json:"digest,omitempty"`
	// Commit is the source commit the image was built from — its
	// org.opencontainers.image.revision label — where the registry answers it:
	// a digest-pinned image in one of our registries that this deployment can
	// read. Absent means unknown, never "built from nothing".
	Commit string `json:"commit,omitempty"`
	// Repo is the repository that commit is in — the image's
	// org.opencontainers.image.source label, as the build wrote it — where the
	// registry answers it. Absent as Commit is.
	Repo string `json:"repo,omitempty"`
}

// live answers, for each service, what it is running and whether that is what
// universe says it should run.
//
// For every Hanzo CD Application the caller may see: the images its live pods run
// (tag, digest, and the source commit and repository each was built from where
// the registry answers it), the universe commit CD last applied, CD's sync and
// health verdicts, when it last synced, the image its values files pin at that
// commit, whether the two agree and which of them does not, and the image CD
// deployed before this one.
//
// `service` lists one service. `commit` — at least seven hex characters of a sha
// — adds a top-level `live`: whether any listed service runs an image built from
// that commit. Together they answer "is commit X live for service Y", and a
// service's `previous` is where to roll back to.
//
// It reads CD's own state and derives nothing CD already says. The declared image
// is read from the universe files the Application names, at the commits CD
// names. A value the registry or the repository has not yet answered is left out
// of this answer and read again on the next. Read-only, and scoped exactly as
// /v1/deploy/gitops is: a SuperAdmin sees every Application, a validated org
// member only its own org's, anyone else 403s.
//
// installed is false — with a reason and an empty list — when the CD CRD is not
// served in this cluster.
func (o ops) live(ctx context.Context, in *liveQuery) (*Live, error) {
	sc, err := scopeOf(ctx)
	if err != nil {
		return nil, err
	}
	commit := strings.ToLower(in.Commit)
	if commit != "" && (len(commit) < 7 || strings.Trim(commit, "0123456789abcdef") != "") {
		return nil, zip.ErrBadRequest("commit must be at least seven hex characters of a sha")
	}
	if err := ready(o.s); err != nil {
		return nil, err
	}
	list, err := o.s.State.dyn.Resource(k8s.CDApplications).Namespace("").List(ctx, metav1.ListOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return &Live{
				Reason:   "Hanzo CD is not installed in this cluster (no apps.hanzo.ai/Application CRD)",
				Services: []Running{},
			}, nil
		}
		return nil, k8sErr(o.s, "list", err)
	}

	type found struct {
		row Running
		src track
	}
	var kept []found
	for i := range list.Items {
		cr := &list.Items[i]
		g := observeGitOpsApp(cr)
		if !sc.allowsCD(g, cr) || (in.Service != "" && g.Name != in.Service) {
			continue
		}
		kept = append(kept, found{running(g, cr), trackOf(g, cr)})
	}
	sort.Slice(kept, func(i, j int) bool {
		a, b := kept[i].row, kept[j].row
		if a.Namespace != b.Namespace {
			return a.Namespace < b.Namespace
		}
		return a.Name < b.Name
	})
	out := make([]Running, len(kept))
	tracks := make([]track, len(kept))
	for i, k := range kept {
		out[i], tracks[i] = k.row, k.src
	}

	unknown := enrich(ctx, out, tracks)
	reply := &Live{Installed: true, Services: out}
	if commit != "" {
		hit := slices.ContainsFunc(out, func(r Running) bool {
			return slices.ContainsFunc(r.Images, func(im Image) bool {
				return im.Commit != "" && strings.HasPrefix(im.Commit, commit)
			})
		})
		if hit || !unknown {
			reply.Live = &hit
		}
	}
	return reply, nil
}

// running reads one Application. Everything but the images comes from the one
// reading of an Application this package has, so this row and /v1/deploy/gitops
// cannot disagree about it.
func running(g GitOpsApp, cr *unstructured.Unstructured) Running {
	ns, _, _ := unstructured.NestedString(cr.Object, "spec", "destination", "namespace")
	refs, _, _ := unstructured.NestedStringSlice(cr.Object, "status", "summary", "images")
	sort.Strings(refs)
	images := make([]Image, 0, len(refs))
	for _, ref := range refs {
		images = append(images, imageOf(ref))
	}
	r := Running{
		Name: g.Name, Namespace: ns, Images: images, RepoURL: g.RepoURL,
		Revision: g.Revision, Sync: g.Sync, Health: g.Health,
	}
	switch {
	case len(g.History) > 0:
		r.SyncedAt = g.History[0].DeployedAt
	case g.Operation != nil && g.Operation.Phase == "Succeeded":
		r.SyncedAt = g.Operation.FinishedAt
	}
	return r
}

// enrich fills what only the registry and the universe repository know, inside one
// budget: each image's commit and repo, each service's declared and previous
// image, and then its drift. It reports whether any image's labels went unanswered.
func enrich(ctx context.Context, rows []Running, tracks []track) (unknown bool) {
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	var g errgroup.Group
	g.SetLimit(reads)

	var mu sync.Mutex
	got := map[string]label{}
	asked := map[string]bool{}
	for _, r := range rows {
		for _, img := range r.Images {
			ref, ok := pinned(img)
			if !ok || asked[ref] {
				continue
			}
			asked[ref] = true
			g.Go(func() error {
				l, ok := labels.get(ctx, ref, func(ctx context.Context) (label, error) { return configLabels(ctx, ref) })
				if ok {
					mu.Lock()
					got[ref] = l
					mu.Unlock()
				}
				return nil
			})
		}
	}
	read := make([]bool, len(rows))
	for i := range rows {
		g.Go(func() error {
			read[i] = pin(ctx, &rows[i], tracks[i])
			return nil
		})
	}
	_ = g.Wait()

	for i := range rows {
		for j, img := range rows[i].Images {
			ref, ok := pinned(img)
			if !ok {
				continue
			}
			l, answered := got[ref]
			if !answered {
				unknown = true
			}
			rows[i].Images[j].Commit, rows[i].Images[j].Repo = l.Commit, l.Repo
		}
		rows[i].Drift = drift(rows[i], read[i])
		rows[i].Converged = len(rows[i].Drift) == 0
	}
	return unknown
}
