package crawl

// archive.go — every page we fetch is kept, so a crawl is a corpus rather than a
// throwaway. Kept pages can be re-read without paying the network again, indexed
// for search, and built against later.
//
// It rides the ONE object seam the binary already has (types.VFSClient, deps.VFS
// over the shared S3 gateway). No second S3 client, no second bucket, no second
// set of credentials to configure or rotate — clients/s3vfs already resolved all
// of that, and tenant isolation there is the org-scoped KEY PREFIX, which is
// exactly the shape this needs.
//
// FETCH AND KEEP ARE SEPARATE. Fetch is the pure network primitive: URL in, Page
// out, no IO beyond the request, which is what makes it testable without a store.
// Read is the door callers use — archive first, network second, keep what came
// back. One door, so no caller has to remember to persist and no two callers can
// disagree about where pages live.
//
// Best-effort by construction: a store that is absent, unreachable or slow costs a
// cache hit and nothing else. Crawling must not start failing because the corpus
// is down — that would trade a working feature for a bookkeeping one.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"sync/atomic"
	"time"

	"github.com/hanzoai/cloud/types"
)

// Scope is who a page belongs to. Both fields come from the VERIFIED principal —
// never from the request body — because they select the key prefix, and a
// caller-supplied prefix is a caller reading another tenant's corpus.
type Scope struct {
	Org     string
	Project string
}

// Archive keeps pages under a scope.
type Archive struct{ vfs types.VFSClient }

// store is the process-wide archive, bound once at Mount.
//
// Package-level because there is exactly ONE object store in a process, and the
// alternative — threading an archive through every call site — reaches functions
// like the answer engine's read stage that have no deps to thread. An atomic
// pointer rather than a plain var: Mount runs at boot while requests are already
// possible on other subsystems, and a torn read of an interface value is a data
// race, not a stale read.
var store atomic.Pointer[Archive]

// Bind installs the process-wide archive. A nil or absent VFS leaves crawling
// fully functional and unarchived, which is the correct posture for a deployment
// with no object store configured — fail soft, never fail closed, since nothing
// about answering a fetch depends on keeping it.
func Bind(vfs types.VFSClient) {
	if vfs == nil {
		store.Store(nil)
		return
	}
	store.Store(&Archive{vfs: vfs})
}

// Read is the door: return the archived page if we have one, otherwise fetch it
// and keep it.
//
// A hit skips the network entirely, which is the point — the same URL is read
// repeatedly across a research loop, a re-ask, and a re-index, and paying for it
// once is both faster and politer to the origin.
func Read(ctx context.Context, s Scope, url string) (*Page, error) {
	a := store.Load()
	if a != nil {
		if p, ok := a.Get(ctx, s, url); ok {
			return p, nil
		}
	}
	p, err := Fetch(ctx, url)
	if err != nil {
		return nil, err
	}
	if a != nil {
		// Deliberately ignored: a page we fetched is a page we can return. Failing
		// the read because we could not file it would turn a storage outage into a
		// crawl outage.
		_ = a.Put(ctx, s, url, p)
	}
	return p, nil
}

// record is what lands in the store: the page plus when we read it.
//
// A self-describing JSON document rather than the bare markdown, because the
// corpus is meant to be indexed and built against later, and a directory of
// markdown with no provenance cannot answer "when was this true" or "what URL is
// this". RequestedURL and URL are both kept and are NOT always equal — the first
// is what someone asked for, the second is where it landed after redirects, and an
// index needs both to dedupe honestly.
type record struct {
	RequestedURL string         `json:"requestedUrl"`
	URL          string         `json:"url"`
	Title        string         `json:"title,omitempty"`
	Markdown     string         `json:"markdown"`
	Metadata     map[string]any `json:"metadata,omitempty"`
	FetchedAt    time.Time      `json:"fetchedAt"`
}

// Put files a page under the scope, keyed by the url that was REQUESTED.
//
// requested is a separate argument from p.URL on purpose: Fetch returns where the
// page landed after redirects, and keying by that would file every redirecting
// page under an address no caller ever asks for. Get would then miss it forever —
// a cache that silently never hits for exactly the pages that redirect, which is a
// large share of the real web.
func (a *Archive) Put(ctx context.Context, s Scope, requested string, p *Page) error {
	if a == nil || a.vfs == nil || p == nil {
		return nil
	}
	body, err := json.Marshal(record{
		RequestedURL: requested,
		URL:          p.URL,
		Title:        p.Title,
		Markdown:     p.Markdown,
		Metadata:     p.Metadata,
		FetchedAt:    time.Now().UTC(),
	})
	if err != nil {
		return err
	}
	return a.vfs.Put(ctx, key(s, requested), body)
}

// Get returns a previously archived page.
//
// Any failure — missing, unreachable, corrupt — is reported the same way: not
// found. A caller's only sensible response to each is to fetch, so distinguishing
// them at this seam would create a decision nobody makes differently.
func (a *Archive) Get(ctx context.Context, s Scope, url string) (*Page, bool) {
	if a == nil || a.vfs == nil {
		return nil, false
	}
	body, err := a.vfs.Get(ctx, key(s, url))
	if err != nil || len(body) == 0 {
		return nil, false
	}
	var r record
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, false
	}
	if strings.TrimSpace(r.Markdown) == "" {
		return nil, false
	}
	return &Page{URL: r.URL, Title: r.Title, Markdown: r.Markdown, Metadata: r.Metadata}, true
}

// key is where a page lives: crawl/<org>/<project>/<sha256(url)>.json
//
// The URL is HASHED rather than embedded. A URL contains "/", "?", "#", "%" and
// arbitrary unicode; splicing one into a key would let a crafted URL walk out of
// its own prefix into another tenant's, which is the whole isolation boundary
// here. A fixed-width hex digest cannot. It also sidesteps key-length limits and
// makes the mapping deterministic, so Get recomputes what Put wrote.
//
// The scope segments are sanitised even though both come from a verified
// principal: this function's correctness should not depend on a claim's shape
// being enforced somewhere else, and defence in depth costs one pass here.
func key(s Scope, url string) string {
	sum := sha256.Sum256([]byte(url))
	return "crawl/" + seg(s.Org) + "/" + seg(s.Project) + "/" + hex.EncodeToString(sum[:]) + ".json"
}

// seg turns one scope value into a key segment that is BOTH readable and
// injective: a sanitised form for humans, then a digest of the RAW value.
//
// Sanitising alone is lossy, and lossy is a tenant-isolation bug rather than a
// cosmetic one: folding everything outside [a-z0-9_-] to '-' maps "a/b" and "a-b"
// onto the same segment, and two distinct orgs that collide share one corpus
// prefix — each reading the other's pages. Orgs come from a verified claim and are
// probably already constrained, but this function should not be the place that
// depends on that being true somewhere else.
//
// So the digest decides identity and the readable part is decoration. Distinct
// inputs always differ in the suffix even when their readable halves are
// identical, and the prefix still tells a human whose corpus they are looking at —
// which matters, because the whole point of keeping these is browsing and indexing
// them later.
func seg(s string) string {
	sum := sha256.Sum256([]byte(s))
	digest := hex.EncodeToString(sum[:])[:12]

	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	// Collapse runs and trim: purely cosmetic now that the digest carries identity,
	// so it cannot cause a collision.
	readable := strings.Trim(b.String(), "-")
	for strings.Contains(readable, "--") {
		readable = strings.ReplaceAll(readable, "--", "-")
	}
	if readable == "" {
		return digest // nothing printable survived; the digest alone is the segment
	}
	if len(readable) > 40 { // keep keys bounded; the digest still disambiguates
		readable = strings.Trim(readable[:40], "-")
	}
	return readable + "-" + digest
}
