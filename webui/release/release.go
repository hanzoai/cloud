// Package release is the console's bytes: the ACTIVE release of a published
// site, held in memory and kept current.
//
// The console used to be //go:embed'd into this binary, which welded the
// frontend's lifecycle to the backend's — a CSS fix cost a ~22-minute cloud build
// and a `strategy: Recreate` rollout that took api.hanzo.ai down for 2m15s. The
// console is now published like any other site (apps/projects → apps/sites): a
// publish is under a second, a rollback is faster, and neither builds or restarts
// anything.
//
// It is the SAME release, read from the SAME store, resolved through the SAME
// registry the site edge uses (sites.CurrentResolver) — not a second copy of any
// of it. What differs is only where the bytes go: the edge STREAMS them for a
// site host, and this loads them once and hands webui an fs.FS, because
// console.hanzo.ai is deliberately NOT a site host (it must keep answering /v1 on
// the same origin, or the console's first-party session cookie stops working).
//
// A leaf, like webui itself: apps/sites + apps/s3admin + stdlib, never package
// cloud — both composition roots import it.
package release

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"strings"
	"sync/atomic"
	"time"

	s3 "github.com/hanzos3/go"
	luxlog "github.com/luxfi/log"

	"github.com/hanzoai/cloud/apps/s3admin"
	"github.com/hanzoai/cloud/apps/sites"
)

// Source is a live fs.FS over one site's active release.
//
// The pointer to the loaded bundle is swapped ATOMICALLY, never mutated: a
// request that started against the old release finishes against it, and the next
// one gets the new. There is no window in which a shell from one release is
// served beside chunks from another.
type Source struct {
	cfg   Config
	admin s3admin.Admin
	log   luxlog.Logger
	cur   atomic.Pointer[bundle]
}

// bundle is one complete, immutable release in memory. prefix is its identity —
// it ends in the release id, so a changed prefix IS a changed release, and the
// poll needs to compare nothing else.
type bundle struct {
	prefix string
	files  int
	bytes  int64
	fsys   memFS
}

// Load reads the configured site's active release and returns it as an fs.FS.
//
// It FAILS rather than degrading. A console that came up empty, or on a
// placeholder shell, would be a front door serving a page that looks like the
// product and is not — and it would do so silently, which is how a broken deploy
// becomes a mystery instead of an alert. Every failure below names what could not
// be reached, so the log line is the diagnosis.
//
// The resolver must already be installed: the site edge is mounted before the
// console in both composition roots (cmd/cloud's TestSitesEdgeIsMountedInTheRouter
// pins that order for the front door), so "no resolver" means a caller mounted
// them backwards.
func Load(ctx context.Context, cfg Config, log luxlog.Logger) (*Source, error) {
	s := &Source{cfg: cfg, admin: s3admin.New(), log: log.New("subsystem", "console")}
	if _, err := s.refresh(ctx); err != nil {
		// The SOURCE comes back beside the error, empty. A caller that wants to keep
		// polling needs something to poll, and returning nil left it with nothing:
		// the boot read failed, the composition root had no Source to Watch, and the
		// console stayed down after the cause was repaired because nothing re-read
		// it. An empty Source answers ErrNotExist for every name — which is what
		// Open below already did — so a caller that mounts it serves 503 until a
		// poll fills it, and one that does not mount it is no worse off than before.
		return s, err
	}
	b := s.cur.Load()
	s.log.Info("console release loaded",
		"site", cfg.Org+"/"+cfg.Slug, "release", b.prefix, "files", b.files, "bytes", b.bytes)
	return s, nil
}

// Open implements fs.FS against the currently mounted release.
func (s *Source) Open(name string) (fs.File, error) {
	b := s.cur.Load()
	if b == nil { // Load failed and handed the caller this Source anyway, to poll
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
	}
	return b.fsys.Open(name)
}

// Release is the S3 prefix of the mounted release — the release id is its last
// segment. Reported at boot and on every swap so "which console is live" is a
// question the logs answer.
func (s *Source) Release() string {
	if b := s.cur.Load(); b != nil {
		return b.prefix
	}
	return ""
}

// Watch keeps the console current with what was published, and is the whole
// reason this indirection exists: a `hanzo sites publish` reaches users through
// THIS loop, in one poll interval, instead of through a cloud build and a
// rollout.
//
// The steady state costs one resolver call per interval and nothing else — the
// bytes are re-read only when the active-release pointer actually moves. A failed
// poll is logged and the mounted release keeps serving: the bundle in memory is
// already complete, so there is nothing to fall back to and nothing to gain by
// tearing it down.
func (s *Source) Watch(ctx context.Context) {
	t := time.NewTicker(s.cfg.Poll)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			changed, err := s.refresh(ctx)
			switch {
			case err != nil:
				s.log.Warn("console release poll failed — serving the release already mounted",
					"release", s.Release(), "err", err)
			case changed:
				b := s.cur.Load()
				s.log.Info("console release swapped", "release", b.prefix, "files", b.files, "bytes", b.bytes)
			}
		}
	}
}

// refresh resolves the site's active release and mounts it if it moved. It
// reports whether anything changed.
func (s *Source) refresh(ctx context.Context) (bool, error) {
	site, err := s.resolve(ctx)
	if err != nil {
		return false, err
	}
	// The prefix carries the release id, so an unchanged prefix is an unchanged
	// release — no listing, no GETs, no allocation.
	if cur := s.cur.Load(); cur != nil && cur.prefix == site.Prefix {
		return false, nil
	}
	if !s.admin.Configured() {
		return false, fmt.Errorf("console release %s: S3_ADMIN_ACCESS_KEY/S3_ADMIN_SECRET_KEY are not set", site.Prefix)
	}
	cli, err := s.admin.Client()
	if err != nil {
		return false, fmt.Errorf("console release %s: %w", site.Prefix, err)
	}
	b, err := read(ctx, cli, site.Bucket, site.Prefix)
	if err != nil {
		return false, err
	}
	s.cur.Store(b)
	return true, nil
}

// resolve asks which release is active, PINNED to the console's owning org — the
// same org-pinned lookup a first-party host uses, so a customer project named
// hanzo-console can never become the console.
func (s *Source) resolve(ctx context.Context) (sites.Site, error) {
	r := sites.CurrentResolver()
	if r == nil {
		return sites.Site{}, fmt.Errorf("console site %s/%s: no site resolver installed — the site edge must be mounted before the console", s.cfg.Org, s.cfg.Slug)
	}
	site, found, err := r.ResolveOrg(ctx, s.cfg.Org, s.cfg.Slug)
	switch {
	case err != nil:
		return sites.Site{}, fmt.Errorf("console site %s/%s: resolve: %w", s.cfg.Org, s.cfg.Slug, err)
	case !found:
		return sites.Site{}, fmt.Errorf("console site %s/%s: no such site — publish it before serving it", s.cfg.Org, s.cfg.Slug)
	case site.Status != "live":
		return sites.Site{}, fmt.Errorf("console site %s/%s: status %q, not live", s.cfg.Org, s.cfg.Slug, site.Status)
	case site.Prefix == "":
		return sites.Site{}, fmt.Errorf("console site %s/%s: resolved to no prefix", s.cfg.Org, s.cfg.Slug)
	}
	return site, nil
}

// read loads every object under a release prefix into memory.
//
// A partial read is an ERROR, not a smaller bundle: half a console is worse than
// none, because it renders. The listing is the same strict directory listing the
// publish path uses (trailing slash, recursive), so a sibling prefix that merely
// shares a name stem can never be pulled in.
func read(ctx context.Context, cli *s3.Client, bucket, prefix string) (*bundle, error) {
	files := memFS{}
	var total int64
	for obj := range cli.ListObjects(ctx, bucket, s3.ListObjectsOptions{Prefix: prefix + "/", Recursive: true}) {
		if obj.Err != nil {
			return nil, fmt.Errorf("console release %s: list: %w", prefix, obj.Err)
		}
		rel := strings.TrimPrefix(obj.Key, prefix+"/")
		if rel == "" || strings.HasSuffix(rel, "/") {
			continue // the prefix placeholder / a directory marker carries no bytes
		}
		body, err := cli.GetObject(ctx, bucket, obj.Key, s3.GetObjectOptions{})
		if err != nil {
			return nil, fmt.Errorf("console release %s: get %s: %w", prefix, rel, err)
		}
		data, err := io.ReadAll(body)
		_ = body.Close()
		if err != nil {
			return nil, fmt.Errorf("console release %s: read %s: %w", prefix, rel, err)
		}
		files[rel] = &memFile{name: rel, data: data, mod: obj.LastModified}
		total += int64(len(data))
	}
	// The SPA shell is the fallback for every client-side route, so a bundle
	// without one is not a console. Caught here, where the prefix can be named,
	// rather than at the first deep link that 404s in a browser.
	if _, ok := files["index.html"]; !ok {
		return nil, fmt.Errorf("console release %s: no index.html (%d objects) — not a console bundle", prefix, len(files))
	}
	return &bundle{prefix: prefix, files: len(files), bytes: total, fsys: files}, nil
}

// FS presents a Source as the fs.FS webui.Mount takes, INCLUDING the nil case.
//
// A nil *Source assigned straight to an fs.FS is a non-nil interface holding a
// nil pointer — Go's oldest trap — and here it would turn "this process mounted
// no console" into a nil dereference on the first request instead of the 503
// webui is written to answer. One conversion, so neither composition root can
// spell it wrong.
func FS(s *Source) fs.FS {
	if s == nil {
		return nil
	}
	return s
}

// Polled says this source re-reads its release on an interval, so an empty read
// is a MOMENT and not a verdict.
//
// It exists for the one caller that has to tell those apart. webui refuses a
// static bundle with no index.html, and it is right to: a baked bundle missing
// its shell is broken and every deep link would 404. A release read from the
// object store is a different thing — it can be empty at boot because S3 was
// briefly unreachable, and it fills in on the next poll. Conflating the two cost
// a console outage that outlived its own cause: one unreadable auxiliary object
// failed the boot read, the process mounted nothing, and it stayed down after the
// object was repaired because nothing re-read it.
//
// A constant, because this is a property of the TYPE rather than of an instance:
// what makes a Source refillable is that Watch polls it, and a Source that is
// never watched serves the release it already has.
func (s *Source) Polled() bool { return true }

// compile-time assertion: a Source is what webui.Mount takes.
var _ fs.FS = (*Source)(nil)
