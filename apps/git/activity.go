package git

// activity.go — the estate's year of work, as one public read.
//
//	GET /v1/git/activity?org=&group=&page=&limit=
//
// The estate is every GitHub org Hanzo, Lux and Zoo publish under, mirrored onto
// the forge (git.hanzo.ai). What it did in the last 365 days is not computed
// here: the forge's repositories are on the forge's disk, so the index runs
// beside them (universe charts/app/files/hanzo/estate/activity.py, every five
// minutes, reading only the repositories whose refs moved) and writes one
// document to the object store. This op serves that document.
//
// So a request never walks a repository and never asks GitHub. It reads memory:
// the document is fetched once a minute at most, a stale copy is answered while
// the next one is fetched, and a fetch that fails keeps the copy it had. Only
// the first request a process ever answers waits on the store.
//
// What the counts mean is the index's to state and the document carries it
// (`window`, `source`): commits are distinct SHAs reachable from a branch or a
// tag, by committer date, so one commit in a fork, a mirror and its original is
// one commit. `commits` and `merges` are the headline: every repository the
// estate holds, the upstream projects it carries and keeps current included.
// `months` says how each scope grew; `ours`, the part the estate's people
// authored, is a breakdown beside it.
//
// Private repositories add to every figure and are never named: the document
// has no private repository, message or author in it, so this op cannot leak
// one whatever it is asked. It is public and needs no principal.

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/hanzoai/cloud/sdk/object"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/zap-proto/zip"
)

const (
	// activityBucket and activityKey are where the index writes: the forge's
	// own bucket, beside the attachments and packages it keeps there.
	activityBucket = "git"
	activityKey    = "activity/activity.json"
	// activityFresh is how long a fetched document is answered before the next
	// fetch starts. The index writes every five minutes.
	activityFresh = time.Minute
	// activityWait bounds the one fetch a request can wait on.
	activityWait = 10 * time.Second
	// activityMax bounds the document read: it is about a megabyte.
	activityMax = 64 << 20
)

// errNotIndexed is a deployment whose index has never written a document.
var errNotIndexed = errors.New("git activity: the estate has not been indexed")

// activityFigures is one scope's year: the estate's, a group's or an org's.
type activityFigures struct {
	// Repos is how many repositories the scope holds on the forge.
	Repos int `json:"repos"`
	// Public is how many of them anyone can read.
	Public int `json:"public"`
	// Private is how many only their members can read. They are counted, never named.
	Private int `json:"private"`
	// Mirrors is how many are pull mirrors of github.com.
	Mirrors int `json:"mirrors"`
	// Empty is how many hold no commit.
	Empty int `json:"empty"`
	// Active is how many have a commit in the window.
	Active int `json:"active"`
	// Active30 is how many have a commit in the last 30 days.
	Active30 int `json:"active30"`
	// Commits is distinct commits in the window, each counted once in this
	// scope: every commit the scope's repositories hold, the upstream projects
	// carried and kept current included.
	Commits int `json:"commits"`
	// Months is Commits per calendar month, oldest first, one per entry of the
	// document's Months.
	Months []int `json:"months"`
	// MergeMonths is Merges per calendar month, as Months.
	MergeMonths []int `json:"mergeMonths"`
	// OursMonths is Ours per calendar month, as Months.
	OursMonths []int `json:"oursMonths"`
	// Ours is how many of those the estate's people authored: an address a
	// member of an estate org commits with, or one at an estate domain.
	Ours int `json:"ours"`
	// Merges is how many of those have more than one parent.
	Merges int `json:"merges"`
	// Contributors is distinct author addresses in the window, counted and never named.
	Contributors int `json:"contributors"`
	// People is how many of those are the estate's own.
	People int `json:"people"`
	// Last is the newest commit time in the scope, RFC 3339 UTC; empty when none.
	Last string `json:"last,omitempty"`
}

// activityTotals is the whole estate.
type activityTotals struct {
	activityFigures
	// Orgs is how many orgs the estate holds on the forge.
	Orgs int `json:"orgs"`
	// Own is commits found in at least one repository GitHub does not call a fork.
	Own int `json:"own"`
	// Forks is commits found only in forks, upstream history included.
	Forks int `json:"forks"`
}

// activityGroup is one group: hanzo, lux or zoo.
type activityGroup struct {
	activityFigures
	// Name is the group.
	Name string `json:"name"`
	// Orgs is how many orgs the group holds.
	Orgs int `json:"orgs"`
	// Daily is the group's distinct commits per UTC day, YYYY-MM-DD.
	Daily map[string]int `json:"daily"`
	// OursDaily is the group's own commits per UTC day.
	OursDaily map[string]int `json:"oursDaily"`
	// Active is how many of the group's repositories have a commit on each UTC day.
	Active map[string]int `json:"active"`
	// Latest is the group's newest commits of public repositories, newest first.
	Latest []activityCommit `json:"latest"`
}

// activityOrg is one org on the forge.
type activityOrg struct {
	activityFigures
	// Name is the org, as GitHub and the forge both spell it.
	Name string `json:"name"`
	// Group is the group that owns it.
	Group string `json:"group"`
	// Forge is the org's page on the forge.
	Forge string `json:"forge"`
	// GitHub is the org's page on GitHub; empty for a namespace only the forge has.
	GitHub string `json:"github,omitempty"`
}

// activityCommit is one recent commit of a public repository.
type activityCommit struct {
	// Repo is org/name.
	Repo string `json:"repo"`
	// SHA is the commit.
	SHA string `json:"sha"`
	// At is the commit time, RFC 3339 UTC.
	At string `json:"at"`
	// Message is the subject line.
	Message string `json:"message"`
	// URL is the commit on the forge.
	URL string `json:"url"`
	// Ours is whether the estate's people authored it.
	Ours bool `json:"ours"`
}

// activityRepo is one public repository's year.
type activityRepo struct {
	// Repo is org/name.
	Repo string `json:"repo"`
	// Org is the org that holds it.
	Org string `json:"org"`
	// Group is the group that owns the org.
	Group string `json:"group"`
	// Description is the repository's own.
	Description string `json:"description,omitempty"`
	// Commits is its distinct commits in the window.
	Commits int `json:"commits"`
	// Months is Commits per calendar month, oldest first, one per entry of the
	// document's Months.
	Months []int `json:"months"`
	// Ours is how many of those the estate's people authored.
	Ours int `json:"ours"`
	// Merges is how many of those are merges.
	Merges int `json:"merges"`
	// Active30 is whether it has a commit in the last 30 days.
	Active30 bool `json:"active30"`
	// Last is its newest commit time, RFC 3339 UTC; empty when none in the window.
	Last string `json:"last,omitempty"`
	// Stars is its stars on the forge.
	Stars int `json:"stars"`
	// Mirror is whether the forge pulls it from github.com.
	Mirror bool `json:"mirror"`
	// Fork is whether GitHub calls it a fork.
	Fork bool `json:"fork"`
	// Forge is its page on the forge.
	Forge string `json:"forge"`
	// GitHub is its page on GitHub when GitHub publishes it.
	GitHub string `json:"github,omitempty"`
}

// activityWindow is the span counted.
type activityWindow struct {
	// From is the first UTC day counted, YYYY-MM-DD.
	From string `json:"from"`
	// To is the last UTC day counted, YYYY-MM-DD.
	To string `json:"to"`
	// By is the commit date counted: "committer", when the work landed.
	By string `json:"by"`
}

// activityDoc is the document the index writes.
type activityDoc struct {
	Months    []string         `json:"months"`
	Read      string           `json:"read"`
	Source    string           `json:"source"`
	Window    activityWindow   `json:"window"`
	Complete  bool             `json:"complete"`
	Unread    int              `json:"unread"`
	GitHub    string           `json:"github"`
	Totals    activityTotals   `json:"totals"`
	Groups    []activityGroup  `json:"groups"`
	Orgs      []activityOrg    `json:"orgs"`
	Daily     map[string]int   `json:"daily"`
	OursDaily map[string]int   `json:"oursDaily"`
	Active    map[string]int   `json:"active"`
	Latest    []activityCommit `json:"latest"`
	Repos     []activityRepo   `json:"repos"`
}

// activityIn narrows the repository page. The figures are always the estate's.
type activityIn struct {
	// Org lists one org's repositories; empty lists every org's.
	Org string `json:"org"`
	// Group lists one group's repositories (hanzo, lux, zoo); empty lists every group's.
	Group string `json:"group"`
	// Page is the repository page, from 1. Anything not positive means 1.
	Page int `json:"page"`
	// Limit is the page size. Anything not positive means 50; the cap is 200.
	Limit int `json:"limit"`
}

// activityPage is one page of public repositories, most commits first.
type activityPage struct {
	// Data is the page.
	Data []activityRepo `json:"data"`
	// Page is this page's number, from 1.
	Page int `json:"page"`
	// Limit is the page size asked for.
	Limit int `json:"limit"`
	// Total is how many public repositories match, across every page.
	Total int `json:"total"`
}

// activityOut is the estate's year: its figures, each group's and each org's,
// the per-day series, the newest public commits, and one page of public
// repositories.
type activityOut struct {
	// Read is when the index wrote this, RFC 3339 UTC.
	Read string `json:"read"`
	// Source names what was read: "forge", its repositories and database.
	Source string `json:"source"`
	// Window is the span counted.
	Window activityWindow `json:"window"`
	// Months names the calendar months every Months series counts, YYYY-MM,
	// oldest first.
	Months []string `json:"months"`
	// Complete is false while a repository the forge lists has never been read.
	Complete bool `json:"complete"`
	// Unread is how many such repositories there are.
	Unread int `json:"unread"`
	// GitHub is when the mirror last read GitHub, RFC 3339 UTC.
	GitHub string `json:"github,omitempty"`
	// Totals is the whole estate.
	Totals activityTotals `json:"totals"`
	// Groups is each group, most commits first.
	Groups []activityGroup `json:"groups"`
	// Orgs is each org, most commits first.
	Orgs []activityOrg `json:"orgs"`
	// Daily is the estate's distinct commits per UTC day, YYYY-MM-DD.
	Daily map[string]int `json:"daily"`
	// OursDaily is the estate's own commits per UTC day.
	OursDaily map[string]int `json:"oursDaily"`
	// Active is how many repositories have a commit on each UTC day.
	Active map[string]int `json:"active"`
	// Latest is the newest commits of public repositories, newest first.
	Latest []activityCommit `json:"latest"`
	// Repos is the page of public repositories asked for.
	Repos activityPage `json:"repos"`
}

// activityCache holds the last document and fetches the next one. fetch is the
// store read in production; a test hands its own.
type activityCache struct {
	fetch func(context.Context) ([]byte, error)

	mu      sync.Mutex
	doc     *activityDoc
	at      time.Time
	running bool
	first   chan struct{} // closed once the first fetch has ended, either way
	err     error
}

func newActivityCache(fetch func(context.Context) ([]byte, error)) *activityCache {
	return &activityCache{fetch: fetch, first: make(chan struct{})}
}

// readActivity reads the index's document from the object store.
func readActivity(ctx context.Context) ([]byte, error) {
	body, err := object.Default().Open(ctx, activityBucket, activityKey, object.Get{})
	if err != nil {
		if object.Absent(err) {
			return nil, errNotIndexed
		}
		return nil, err
	}
	defer body.Close()
	data, err := io.ReadAll(io.LimitReader(body, activityMax))
	if err != nil {
		return nil, err
	}
	return data, nil
}

// get answers the newest document it has, starting a fetch when that one is
// older than activityFresh. Only a cache that has never held a document waits.
func (a *activityCache) get(ctx context.Context) (*activityDoc, error) {
	a.mu.Lock()
	if !a.running && time.Since(a.at) > activityFresh {
		a.running = true
		go a.refresh()
	}
	doc, first := a.doc, a.first
	a.mu.Unlock()
	if doc != nil {
		return doc, nil
	}
	wait, cancel := context.WithTimeout(ctx, activityWait)
	defer cancel()
	select {
	case <-first:
	case <-wait.Done():
		return nil, zip.Errorf(http.StatusServiceUnavailable, "git activity: the index has not answered yet")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.doc == nil {
		if errors.Is(a.err, errNotIndexed) {
			return nil, zip.Errorf(http.StatusServiceUnavailable, "git activity: the estate has not been indexed yet")
		}
		return nil, zip.Errorf(http.StatusServiceUnavailable, "git activity: the index is unreadable")
	}
	return a.doc, nil
}

// refresh fetches once and keeps what it had when the fetch fails.
func (a *activityCache) refresh() {
	ctx, cancel := context.WithTimeout(context.Background(), activityWait)
	defer cancel()
	var doc *activityDoc
	raw, err := a.fetch(ctx)
	if err == nil {
		doc = &activityDoc{}
		if err = json.Unmarshal(raw, doc); err != nil {
			doc = nil
		}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.running = false
	a.at = time.Now()
	a.err = err
	if doc != nil {
		a.doc = doc
	}
	select {
	case <-a.first:
	default:
		close(a.first)
	}
}

// page cuts the asked-for page of public repositories from doc.
func (doc *activityDoc) page(in *activityIn) activityPage {
	limit := in.Limit
	if limit <= 0 {
		limit = 50
	}
	limit = min(limit, 200)
	page := max(in.Page, 1)
	org, group := strings.ToLower(strings.TrimSpace(in.Org)), strings.ToLower(strings.TrimSpace(in.Group))
	match := doc.Repos
	if org != "" || group != "" {
		match = make([]activityRepo, 0, len(doc.Repos))
		for _, r := range doc.Repos {
			if (org == "" || strings.ToLower(r.Org) == org) && (group == "" || r.Group == group) {
				match = append(match, r)
			}
		}
	}
	out := activityPage{Data: []activityRepo{}, Page: page, Limit: limit, Total: len(match)}
	if from := (page - 1) * limit; from < len(match) {
		out.Data = match[from:min(from+limit, len(match))]
	}
	return out
}

// activity answers the estate's last 365 days: distinct commits, merges,
// active repositories and contributors, for the whole estate, each group and
// each org, a commit count per day, the newest public commits, and one page of
// public repositories. Public: private repositories are counted and never named.
//
// Example: {"group": "lux", "page": 1, "limit": 20}
func (o ops) activity(ctx context.Context, in *activityIn) (*activityOut, error) {
	doc, err := o.s.State.activity.get(ctx)
	if err != nil {
		return nil, err
	}
	return &activityOut{
		Read: doc.Read, Source: doc.Source, Window: doc.Window, Months: doc.Months,
		Complete: doc.Complete, Unread: doc.Unread, GitHub: doc.GitHub,
		Totals: doc.Totals, Groups: doc.Groups, Orgs: doc.Orgs, Daily: doc.Daily,
		OursDaily: doc.OursDaily, Active: doc.Active, Latest: doc.Latest, Repos: doc.page(in),
	}, nil
}
