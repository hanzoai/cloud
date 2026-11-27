package release

import (
	"context"
	"fmt"
	"github.com/hanzoai/cloud/sdk/object"
	"io"
	"io/fs"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sync/errgroup"

	luxlog "github.com/luxfi/log"

	"github.com/hanzoai/cloud/apps/sites"
)

// Source is a live fs.FS over one site's active release.
//
// The pointer to the loaded bundle is swapped ATOMICALLY, never mutated: a
// request that started against the old release finishes against it, and the next
// one gets the new. Within this process there is no window in which a shell from
// one release is served beside chunks from another.
//
// That guarantee is per-process, and the fleet is not one process: every app
// child mounts the release itself, and one whose read failed used to keep the
// bundle it had and go on serving it — so a host fronted by several could answer
// a shell from one release and a chunk from another. `want` is what the resolver
// last named; a process not on it is Stale and serves nothing.
type Source struct {
	cfg   Config
	store object.Store
	log   luxlog.Logger
	cur   atomic.Pointer[bundle]
	// want is set BEFORE the read that would mount it, so a failed read still
	// leaves the process knowing it has fallen behind.
	want atomic.Pointer[string]
	// checked is when the resolver was last asked (unix nano), and one is asked at
	// a time. A read refreshes; nothing is on a timer.
	checked atomic.Int64
	asking  sync.Mutex
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
// placeholder shell, would be an entry point serving a page that looks like the
// product and is not — and it would do so silently, which is how a broken deploy
// becomes a mystery instead of an alert. Every failure below names what could not
// be reached, so the log line is the diagnosis.
//
// The resolver must already be installed: the site edge is mounted before the
// console in both composition roots (cmd/cloud's TestSitesEdgeIsMountedInTheRouter
// pins that order for the host), so "no resolver" means a caller mounted
// them backwards.
// first bounds the read that happens ON THE BOOT PATH.
//
// A published release is a handful of objects from a store that is either local
// or one hop away, so a healthy read is milliseconds. An UNHEALTHY one used to
// cost whatever the S3 client's own retries cost, unbounded, and boot waited:
// measured against a dead store, 21 seconds before the process began listening.
//
// Giving up early costs only the first browser: the read path mounts on demand
// (`ensure`), so the release lands on the first request for it instead of on the
// way to the listener. The bound is therefore about STARTUP, not about the
// console: it decides how long a process delays serving its API for a browser
// bundle nothing headless asks for.
//
// It is load-bearing, not advisory. This process has a 300s startup probe and
// already spends ~120s of it composing subsystems; a boot that also pulled the
// bundle would spend ~180s more and be killed mid-mount, taking the API down
// with a console nothing headless reads.
const first = 2 * time.Second

func Load(ctx context.Context, cfg Config, log luxlog.Logger) (*Source, error) {
	s := New(cfg, log)
	// The SOURCE comes back beside the error, empty. A caller that wants to keep
	// polling needs something to poll, and returning nil left it with nothing:
	// the boot read failed, the composition root had no Source to Watch, and the
	// console stayed down after the cause was repaired because nothing re-read
	// it. An empty Source answers ErrNotExist for every name — which is what
	// Open below already did — so a caller that mounts it serves 503 until a
	// poll fills it, and one that does not mount it is no worse off than before.
	return s, s.Prime(ctx)
}

// New is a Source with nothing mounted. The first read mounts the release
// (ensure), so a caller that must serve before the release is reachable — the
// host, which listens before its children start — mounts New and Primes later.
func New(cfg Config, log luxlog.Logger) *Source {
	return &Source{cfg: cfg, store: object.Default(), log: log.New("subsystem", "console")}
}

// Prime reads the active release now, bounded by first, so the first browser
// does not pay the mount.
func (s *Source) Prime(ctx context.Context) error {
	boot, done := context.WithTimeout(ctx, first)
	defer done()
	if _, err := s.refresh(boot); err != nil {
		return err
	}
	b := s.cur.Load()
	s.log.Info("console release loaded",
		"site", s.cfg.Org+"/"+s.cfg.Slug, "release", b.prefix, "files", b.files, "bytes", b.bytes)
	return nil
}

// Open implements fs.FS against the currently mounted release.
// freshness coalesces the resolves a single page load would otherwise cause —
// one document plus its thirty chunks is one question, not thirty-one.
const freshness = time.Second

// resolveTimeout bounds the check on the read path. The cached bundle is what a
// cache is for: a resolver that is slow must not make the console slow.
const resolveTimeout = 2 * time.Second

// mountTimeout bounds MOUNTING a release, which is a different question from
// asking which one is published. Resolving reads one small record; mounting
// lists a release and downloads every file in it — for this console, 1,735
// objects and 93 MiB, fetched one at a time.
//
// Both ran on the resolve budget, so a mount had two seconds to move ninety-odd
// megabytes and never once finished. What it reported was `list: context
// deadline exceeded`, which reads like slow storage and is really a deadline
// sized for a metadata call being handed a bulk transfer.
const mountTimeout = 5 * time.Minute

// ensure asks what is published if nobody has asked recently, and mounts it if it
// moved. This is why nothing polls: a console nobody opens costs nothing, and a
// console somebody opens is current when they open it.
//
// Failure is not fatal here. It leaves `want` set, so a process that fell behind
// reports Stale and refuses rather than serving bytes its siblings are not.
func (s *Source) ensure() {
	// Nothing configured to resolve — a Source assembled directly (tests, and the
	// nil-bundle case) has no site to ask about.
	if s.cfg.Slug == "" || s.log == nil {
		return
	}
	now := time.Now().UnixNano()
	if last := s.checked.Load(); now-last < int64(freshness) {
		return
	}
	if !s.asking.TryLock() {
		return // another request is already asking; serve what we have
	}
	defer s.asking.Unlock()
	if now := time.Now().UnixNano(); now-s.checked.Load() < int64(freshness) {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), mountTimeout)
	defer cancel()
	changed, err := s.refresh(ctx)
	s.checked.Store(time.Now().UnixNano())
	switch {
	case err != nil:
		s.log.Warn("console release check failed — serving the release already in use",
			"release", s.Release(), "err", err)
	case changed:
		b := s.cur.Load()
		s.log.Info("console release swapped", "release", b.prefix, "files", b.files, "bytes", b.bytes)
	}
}

func (s *Source) Open(name string) (fs.File, error) {
	s.ensure()
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

// Wanted is the release the resolver last named — what this process SHOULD be
// serving. Empty before the first successful resolve.
func (s *Source) Wanted() string {
	if p := s.want.Load(); p != nil {
		return *p
	}
	return ""
}

// Stale reports a newer release is published and not mounted. The caller must
// refuse: these bytes are coherent with themselves but not with what siblings
// serve. Before the first resolve nothing is known, and that is not staleness.
func (s *Source) Stale() bool {
	want := s.Wanted()
	return want != "" && want != s.Release()
}

// Watch is retained for the composition roots that start it, and no longer polls.
//
// It used to tick every 30s, which made staleness a function of the clock: between
// two ticks a process served bytes the publisher had already replaced, at 200. The
// refresh happens on read now (see ensure), so a console nobody opens costs
// nothing and a console somebody opens is current when they open it. This blocks
// until the context ends so callers that `go Watch(ctx)` keep their shape.
func (s *Source) Watch(ctx context.Context) {
	<-ctx.Done()
}

// refresh resolves the site's active release and mounts it if it moved. It
// reports whether anything changed.
// THE CALLER STATES THE WHOLE BUDGET, and only the RESOLVE is sub-bounded here.
// Resolving reads one small record; mounting lists a release and downloads every
// file in it — for this console, 1,735 objects and 93 MiB fetched one at a time,
// about three minutes. Those are different questions and a single number cannot
// answer both, so the caller says which it is asking: `ensure` grants a mount,
// `Load` grants a boot's worth and takes whatever fits.
//
// Detaching the mount from the caller's deadline instead (WithoutCancel) gave it
// the long budget everywhere INCLUDING boot, where the point of the boot bound is
// that nothing may hold the listener.
func (s *Source) refresh(ctx context.Context) (bool, error) {
	rctx, cancel := context.WithTimeout(ctx, resolveTimeout)
	defer cancel()
	site, err := s.resolve(rctx)
	if err != nil {
		return false, err
	}
	// The prefix carries the release id, so an unchanged prefix is an unchanged
	// release — no listing, no GETs, no allocation.
	if cur := s.cur.Load(); cur != nil && cur.prefix == site.Prefix {
		return false, nil
	}
	want := site.Prefix
	s.want.Store(&want)
	if err := object.Check(); err != nil {
		return false, fmt.Errorf("console release %s: %w", site.Prefix, err)
	}
	b, err := read(ctx, s.store, site.Bucket, site.Prefix)
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
		return sites.Site{}, fmt.Errorf("console site %s/%s: no site resolver installed — the site edge must be composed before the console", s.cfg.Org, s.cfg.Slug)
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
//
// The objects are fetched readers at a time. One at a time, a release of 1,735
// small files took ~45s against an object store answering each in ~25ms, which
// no boot budget holds; in parallel it is the listing plus a second or two.
func read(ctx context.Context, store object.Store, bucket, prefix string) (*bundle, error) {
	type entry struct {
		key, rel string
		mod      time.Time
	}
	var objects []entry
	for obj, err := range store.List(ctx, bucket, object.List{Prefix: prefix + "/"}) {
		if err != nil {
			return nil, fmt.Errorf("console release %s: list: %w", prefix, err)
		}
		rel := strings.TrimPrefix(obj.Key, prefix+"/")
		if rel == "" || strings.HasSuffix(rel, "/") {
			continue // the prefix placeholder / a directory marker carries no bytes
		}
		objects = append(objects, entry{key: obj.Key, rel: rel, mod: obj.Modified})
	}

	data := make([][]byte, len(objects))
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(readers)
	for i, o := range objects {
		g.Go(func() error {
			body, err := store.Open(gctx, bucket, o.key, object.Get{})
			if err != nil {
				return fmt.Errorf("console release %s: get %s: %w", prefix, o.rel, err)
			}
			b, err := io.ReadAll(body)
			_ = body.Close()
			if err != nil {
				return fmt.Errorf("console release %s: read %s: %w", prefix, o.rel, err)
			}
			data[i] = b
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}

	files := memFS{}
	var total int64
	for i, o := range objects {
		files[o.rel] = &memFile{name: o.rel, data: data[i], mod: o.mod}
		total += int64(len(data[i]))
	}
	// The SPA shell is the fallback for every client-side route, so a bundle
	// without one is not a console. Caught here, where the prefix can be named,
	// rather than at the first deep link that 404s in a browser.
	if _, ok := files["index.html"]; !ok {
		return nil, fmt.Errorf("console release %s: no index.html (%d objects) — not a console bundle", prefix, len(files))
	}
	return &bundle{prefix: prefix, files: len(files), bytes: total, fsys: files}, nil
}

// readers is how many objects a mount fetches at once.
const readers = 32

// FS presents a Source as the fs.FS webui.Use takes, INCLUDING the nil case.
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

// compile-time assertion: a Source is what webui.Use takes.
var _ fs.FS = (*Source)(nil)
