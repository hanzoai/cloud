// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package event

// symbolicate.go — browser stack frames read back through the release's own
// sourcemap, on ingest, BEFORE the fault is grouped.
//
// WHY ON INGEST. fingerprint() hashes the first own frame's function, file and line.
// For a minified bundle that is a mangled name, a content-hashed chunk URL and line 1,
// so every deploy that rebuilt a chunk opened a fresh group for a fault nothing had
// changed about. Mapped back to src/app.tsx:42 the frame is the same string in every
// build, and the group survives the deploy. Resolving on read would leave the groups
// split: the key is computed here, once.
//
// WHERE THE MAPS ARE. A build uploads them (sourcemapUpload, sourcemap_upload.go)
// into the process's one object store under event/sourcemaps/<org>/, keyed by a hash
// of the release and of the served path of the generated file. Nothing serves that
// prefix: a map is the org's source, so it goes in and is read back by this file
// only.
//
// WHAT IT COSTS, and why that is bounded. A frame from a release that uploaded
// nothing costs one cached lookup of the release marker, so every app that sends a
// release and never uploads a map (most of them, today) pays one object read per
// release per negTTL rather than one per event. A map is read and parsed once and
// held in a byte-bounded LRU; at most maxMapsPerEvent maps are consulted per event,
// at most maxLoads load at once, a map over maxMapBytes is never parsed, and the
// whole of one request's resolution waits at most resolveBudget. A load that outlives
// the budget keeps running in the background and lands in the cache for the next
// event, so a slow store slows nothing down for longer than the budget.
//
// A MISSING MAP IS NOT AN ERROR. The frame stays exactly as the browser sent it, and
// the fault is grouped as it always was.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/cloud/sdk/object"
)

const (
	// sourcemapPrefix is this surface's box in the shared blob bucket.
	sourcemapPrefix = "event/sourcemaps/"

	// maxMapBytes bounds one map, at upload and again at load. It sits under the
	// fleet's 16 MiB body limit so an upload that fits the door fits the store.
	maxMapBytes = 15 << 20

	// maxMapsPerEvent bounds how many distinct generated files one fault may ask to
	// resolve. A real stack touches a handful of chunks; a crafted one naming sixty
	// files is resolved through its first eight and left alone after that.
	maxMapsPerEvent = 8

	// maxLoads bounds the object reads and parses in flight across the process.
	maxLoads = 4

	// resolveBudget is how long one ingest request waits on resolution in total.
	resolveBudget = 1500 * time.Millisecond

	// loadTimeout bounds one background load: the read and the parse.
	loadTimeout = 20 * time.Second

	// cacheBytes and cacheEntries bound the parsed maps held in memory.
	cacheBytes   = 192 << 20
	cacheEntries = 256

	// posTTL is how long a parsed map or a seen release is trusted before it is read
	// again, so a re-uploaded map reaches every replica. negTTL is how long an absent
	// one is remembered, which is also how long after an upload a replica that had
	// already looked may take to notice it.
	posTTL = time.Hour
	negTTL = 2 * time.Minute

	// negEntries bounds the memory of absences, which a caller choosing random
	// releases could otherwise grow without limit.
	negEntries = 8192

	// maxMinifiedBytes bounds the forensic copy of the frames as the browser sent
	// them, kept in attributes so a reader can still see what was mapped.
	maxMinifiedBytes = 16 << 10
)

// sourcemaps is where maps live. The fail-closed store until build binds the
// process's object store, so a binary with none resolves nothing and refuses uploads
// with 503 rather than pretending to keep them.
var (
	sourcemapsMu sync.RWMutex
	sourcemaps   = object.NoBlobs()
	mapCache     = newSymbolCache()
)

func currentSourcemaps() (object.Blobs, *symbolCache) {
	sourcemapsMu.RLock()
	b, c := sourcemaps, mapCache
	sourcemapsMu.RUnlock()
	return b, c
}

// bindSourcemaps installs the store and starts from an empty cache.
func bindSourcemaps(b object.Blobs) {
	c := newSymbolCache()
	sourcemapsMu.Lock()
	sourcemaps = b
	mapCache = c
	sourcemapsMu.Unlock()
}

// ── keys ─────────────────────────────────────────────────────────────────────

func mapDigest(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:16])
}

// releaseDir is the prefix of one org's release. The org is server-resolved (never
// a caller's word) and escaped; the release is hashed, so no value a caller chooses
// can shape the key.
func releaseDir(org, release string) string {
	return sourcemapPrefix + url.PathEscape(org) + "/" + mapDigest(release) + "/"
}

// mapKey is where the map of one generated file of one release lives.
func mapKey(org, release, file string) string {
	return releaseDir(org, release) + mapDigest(file) + ".map"
}

// releaseKey marks a release that uploaded at least one map.
func releaseKey(org, release string) string { return releaseDir(org, release) + "release" }

// servedPath is a generated file as both sides name it: the path it is served at,
// without scheme, host, query or fragment. A browser frame says
// https://hanzo.app/assets/index-3f9a.js?v=2 and a build knows /assets/index-3f9a.js;
// both read as the second. "" for anything that is not a script path.
func servedPath(file string) string {
	file = strings.TrimSpace(file)
	if i := strings.Index(file, "://"); i >= 0 {
		rest := file[i+3:]
		j := strings.IndexByte(rest, '/')
		if j < 0 {
			return ""
		}
		file = rest[j:]
	}
	if i := strings.IndexAny(file, "?#"); i >= 0 {
		file = file[:i]
	}
	if !strings.HasPrefix(file, "/") || len(file) > 1024 {
		return ""
	}
	low := strings.ToLower(file)
	if !strings.HasSuffix(low, ".js") && !strings.HasSuffix(low, ".mjs") && !strings.HasSuffix(low, ".cjs") {
		return ""
	}
	return file
}

// ── the cache ────────────────────────────────────────────────────────────────

type cached struct {
	sm   *sourceMap // nil for a known release marker
	at   time.Time
	prev *cached
	next *cached
	key  string
}

type pending struct {
	done chan struct{}
	sm   *sourceMap
	ok   bool
}

type symbolCache struct {
	mu      sync.Mutex
	entries map[string]*cached
	head    *cached // most recent
	tail    *cached
	bytes   int
	absent  map[string]time.Time
	loading map[string]*pending
	slots   chan struct{}
	now     func() time.Time
}

func newSymbolCache() *symbolCache {
	return &symbolCache{
		entries: map[string]*cached{},
		absent:  map[string]time.Time{},
		loading: map[string]*pending{},
		slots:   make(chan struct{}, maxLoads),
		now:     time.Now,
	}
}

// forget drops what is known about key, so the replica that took an upload reads
// the new map at once.
func (c *symbolCache) forget(keys ...string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, k := range keys {
		delete(c.absent, k)
		if e := c.entries[k]; e != nil {
			c.unlink(e)
		}
	}
}

func (c *symbolCache) unlink(e *cached) {
	if e.prev != nil {
		e.prev.next = e.next
	} else {
		c.head = e.next
	}
	if e.next != nil {
		e.next.prev = e.prev
	} else {
		c.tail = e.prev
	}
	e.prev, e.next = nil, nil
	delete(c.entries, e.key)
	if e.sm != nil {
		c.bytes -= e.sm.size
	}
}

func (c *symbolCache) front(e *cached) {
	e.prev, e.next = nil, c.head
	if c.head != nil {
		c.head.prev = e
	}
	c.head = e
	if c.tail == nil {
		c.tail = e
	}
}

// hit answers from memory: (map, true, true) for a cached map or marker, (nil,
// false, true) for a remembered absence, and known=false when the store must be
// asked.
func (c *symbolCache) hit(key string) (sm *sourceMap, present, known bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.hitLocked(key)
}

func (c *symbolCache) hitLocked(key string) (sm *sourceMap, present, known bool) {
	now := c.now()
	if e := c.entries[key]; e != nil {
		if now.Sub(e.at) < posTTL {
			c.unlink(e)
			c.entries[key] = e
			if e.sm != nil {
				c.bytes += e.sm.size
			}
			c.front(e)
			return e.sm, true, true
		}
		c.unlink(e)
	}
	if at, ok := c.absent[key]; ok {
		if now.Sub(at) < negTTL {
			return nil, false, true
		}
		delete(c.absent, key)
	}
	return nil, false, false
}

func (c *symbolCache) keep(key string, sm *sourceMap, present bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	if !present {
		if len(c.absent) >= negEntries {
			// Shed the stale first, and everything if nothing is stale: forgetting
			// an absence costs one read, remembering unboundedly costs the process.
			for k, at := range c.absent {
				if now.Sub(at) >= negTTL {
					delete(c.absent, k)
				}
			}
			if len(c.absent) >= negEntries {
				c.absent = map[string]time.Time{}
			}
		}
		c.absent[key] = now
		return
	}
	if old := c.entries[key]; old != nil {
		c.unlink(old)
	}
	e := &cached{sm: sm, at: now, key: key}
	c.entries[key] = e
	if sm != nil {
		c.bytes += sm.size
	}
	c.front(e)
	for (c.bytes > cacheBytes || len(c.entries) > cacheEntries) && c.tail != nil && c.tail != e {
		c.unlink(c.tail)
	}
}

// load returns the parsed map (or the marker) at key, reading the store at most
// once across concurrent callers. The read runs detached under loadTimeout, so a
// caller whose ctx ends stops waiting without abandoning the load.
func (c *symbolCache) load(ctx context.Context, key string, isMap bool) (*sourceMap, bool) {
	c.mu.Lock()
	// Asked under the same lock that registers a load, so a load that finished a
	// moment ago is read from memory rather than started again.
	if sm, present, known := c.hitLocked(key); known {
		c.mu.Unlock()
		return sm, present
	}
	p := c.loading[key]
	if p == nil {
		p = &pending{done: make(chan struct{})}
		c.loading[key] = p
		go c.fetch(key, isMap, p)
	}
	c.mu.Unlock()
	select {
	case <-p.done:
		return p.sm, p.ok
	case <-ctx.Done():
		return nil, false
	}
}

func (c *symbolCache) fetch(key string, isMap bool, p *pending) {
	defer func() {
		c.mu.Lock()
		delete(c.loading, key)
		c.mu.Unlock()
		close(p.done)
	}()
	ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
	defer cancel()
	select {
	case c.slots <- struct{}{}:
		defer func() { <-c.slots }()
	case <-ctx.Done():
		return
	}
	blobs, _ := currentSourcemaps()
	data, err := blobs.Get(ctx, key)
	switch {
	case errors.Is(err, object.ErrBlobNotFound):
		c.keep(key, nil, false)
		return
	case err != nil:
		// An unavailable store is not an absence: nothing is remembered, and the
		// next event asks again.
		return
	}
	if !isMap {
		p.ok = true
		c.keep(key, nil, true)
		return
	}
	if len(data) > maxMapBytes {
		c.keep(key, nil, false)
		return
	}
	sm, err := parseSourceMap(data)
	if err != nil {
		c.keep(key, nil, false)
		return
	}
	p.sm, p.ok = sm, true
	c.keep(key, sm, true)
}

// ── resolution ───────────────────────────────────────────────────────────────

// minifiedFrame is one frame as the browser sent it, kept for forensics.
type minifiedFrame struct {
	Function string `json:"function,omitempty"`
	File     string `json:"file"`
	Line     uint32 `json:"line,omitempty"`
	Column   uint32 `json:"column,omitempty"`
}

// minifiedKey is the attribute the browser's own frames are kept under once any of
// them was resolved.
const minifiedKey = "$minified_frames"

// symbolicate maps every frame of an error fact that has a map in its release, and
// reports whether any frame changed. The caller recomputes the grouping key when it
// did. Frames with no map, and every frame of a release that uploaded none, are left
// exactly as they were.
func symbolicate(ctx context.Context, f *fact) bool {
	if f.signal != signalError || f.org == "" || f.release == "" || len(f.frames) == 0 {
		return false
	}
	files := map[string]bool{}
	for _, fr := range f.frames {
		if p := servedPath(fr.file); p != "" && len(files) < maxMapsPerEvent {
			files[p] = true
		}
	}
	if len(files) == 0 {
		return false
	}
	if _, ok := mapCache.load(ctx, releaseKey(f.org, f.release), false); !ok {
		return false
	}
	maps := make(map[string]*sourceMap, len(files))
	for p := range files {
		if sm, ok := mapCache.load(ctx, mapKey(f.org, f.release, p), true); ok && sm != nil {
			maps[p] = sm
		}
	}
	if len(maps) == 0 {
		return false
	}
	original := make([]minifiedFrame, len(f.frames))
	changed := false
	for i := range f.frames {
		fr := &f.frames[i]
		original[i] = minifiedFrame{Function: fr.function, File: fr.file, Line: fr.line, Column: fr.column}
		sm := maps[servedPath(fr.file)]
		if sm == nil {
			continue
		}
		pos, ok := sm.lookup(fr.line, fr.column)
		if !ok {
			continue
		}
		// The NAME the map gives this position, or none. The browser's own name is a
		// minifier's choice and changes between builds, so keeping it would put a
		// per-build string back into the grouping key this exists to stabilize; it is
		// kept in the forensic copy instead.
		fr.function = pos.name
		fr.file = pos.source
		fr.line = pos.line
		fr.column = pos.column
		// Ownership is re-read from the ORIGINAL path: a bundle is one first-party
		// URL, and the vendored code inside it is only visible once mapped.
		fr.own = ownFile("/" + pos.source)
		changed = true
	}
	if !changed {
		return false
	}
	if raw := minifiedJSON(original); raw != "" {
		if f.attributes == nil {
			f.attributes = map[string]string{}
		}
		f.attributes[minifiedKey] = raw
	}
	return true
}

// minifiedJSON renders the forensic copy within maxMinifiedBytes, dropping frames
// from the far end of the stack (the crash site is first) until it fits.
func minifiedJSON(frames []minifiedFrame) string {
	for n := len(frames); n > 0; n-- {
		b, err := json.Marshal(frames[:n])
		if err != nil {
			return ""
		}
		if len(b) <= maxMinifiedBytes {
			return string(b)
		}
	}
	return ""
}

// resolveFaults symbolicates every error fact of one ingest batch inside one shared
// budget and regroups the ones that changed. A fact whose fingerprint a peer stated
// keeps it: the peer knows its own site better than any frame.
func resolveFaults(ctx context.Context, facts []fact, stated []bool) {
	var (
		rctx   context.Context
		cancel context.CancelFunc
	)
	for i := range facts {
		f := &facts[i]
		if f.signal != signalError || f.release == "" || len(f.frames) == 0 {
			continue
		}
		if rctx == nil {
			rctx, cancel = context.WithTimeout(ctx, resolveBudget)
			defer cancel()
		}
		if symbolicate(rctx, f) && !stated[i] {
			f.issue = fingerprint(f)
		}
	}
}
