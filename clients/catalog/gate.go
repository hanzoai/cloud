package catalog

// gate.go is the admission rule for the PUBLISHED catalog: what a live site has
// to BE before the world is told about it.
//
// sync.go's org allowlist answers "WHOSE site is this". That is a question about
// a place, and a place cannot tell a shipped app from a deploy probe, from an
// untouched scaffolding placeholder, or from the same page entered twice. Those
// three are what actually rotted the public catalog, and all three are properties
// of ONE value — the document a visitor is served — so one function reads that
// document and judges it. Whose it is and what it is are now separate questions
// asked in separate places.
//
// Three refusals, and nothing else:
//
//	placeholder  the page is one our own scaffolding emits — a template directory
//	             deployed instead of a build of it.
//	thin         under a kilobyte AND inert. Size ALONE would be a bug: a
//	             single-page app's shell and a redirect stub are both far under a
//	             kilobyte and both are real apps. What has nothing to show is a
//	             small page that will never fetch anything either.
//	duplicate    byte-identical to a page already admitted this pass. One app, one
//	             entry; a second row is a lie about how much there is to see.
//
// Refusal is DEMOTION, not deletion. A refused site is still live, still the
// owner's project, and still in the owner's OWN corpus with Note saying why it
// is not public. Nothing in this file removes anything.
//
// Fail OPEN. A page we could not read is not evidence of junk — an edge blip must
// never prune the catalog, the same reason a rejected GitHub token does not
// (sync.go). Only positive evidence refuses.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	// minBody is the size below which a page owes an explanation. A kilobyte is
	// roughly where hand-written HTML stops being able to say anything; every
	// junk row this gate was written for came in under half of it.
	minBody = 1 << 10

	// maxBody bounds one read. Past it we stop and admit, because an unread page
	// is an unjudged one — see fail open.
	maxBody = 1 << 20

	// readers is how many pages are read at once and bodyWait is each one's
	// budget. Their product must stay inside the reconcile's own deadline (5m in
	// loop) with room for the GitHub half of the pass.
	readers  = 12
	bodyWait = 10 * time.Second
)

// Refusal reasons, as they are stored on the demoted row.
const (
	whyThin        = "held from the public catalog: under a kilobyte and loads nothing"
	whyPlaceholder = "held from the public catalog: an unbuilt scaffolding placeholder"
	whyDuplicate   = "held from the public catalog: byte-identical to "
)

// scaffolds are the exact placeholder pages our own tooling serves when a
// template DIRECTORY is deployed instead of a build of it. They are matched on
// the page's collapsed, lower-cased text, so markup or whitespace churn cannot
// slip one past. This list is the extension point: a new placeholder page is one
// more line here, never a new mechanism.
var scaffolds = []string{
	"template placeholder",
	"this template directory is used for scaffolding",
	"welcome to project acme",
	"get started by editing", // an untouched Next.js starter
	"save to reload",         // an untouched create-react-app starter
}

// admit splits the platform's live sites into the ones the world is shown and the
// ones held back, each held row carrying the reason.
//
// The pass runs in ID order, not the store's newest-first order, so the row that
// WINS a duplicate is the same row every hour. A catalog that renamed its own
// survivor on every sync would be worse than the duplicate it removed.
func admit(ctx context.Context, rows []Entry) (pub, held []Entry) {
	rows = append([]Entry(nil), rows...)
	sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
	served := bodies(ctx, rows)
	seen := map[string]string{}
	for _, e := range rows {
		body, read := served[e.ID]
		if !read {
			pub = append(pub, e) // unread ⇒ unjudged ⇒ admitted
			continue
		}
		sum := digest(body)
		switch {
		// Placeholder before thin: both catch a scaffold stub, and "you deployed
		// the template directory" is a reason its owner can act on.
		case scaffolded(body):
			e.Note = whyPlaceholder
		case len(body) < minBody && inert(body):
			e.Note = whyThin
		case seen[sum] != "":
			e.Note = whyDuplicate + seen[sum]
		default:
			seen[sum] = e.ID
			pub = append(pub, e)
			continue
		}
		held = append(held, e)
	}
	// A gate that refuses MOST of the corpus has learned something about the
	// READER, not about the sites: egress intercepted, the edge serving an error
	// page to us, a redirect we followed into a login. Acting on that would empty
	// a catalog that is fine — the same failure sync.go already refuses to make
	// when GitHub rejects a token. So we don't act on it.
	if len(held) > len(rows)/2 {
		for i := range rows {
			rows[i].Note = ""
		}
		return rows, nil
	}
	return pub, held
}

// bodies reads every row's page, at most readers at a time. A page that errors is
// simply absent from the result — the caller reads that as "unjudged".
func bodies(ctx context.Context, rows []Entry) map[string][]byte {
	out, work := make(map[string][]byte, len(rows)), make(chan Entry)
	var mu sync.Mutex
	var wg sync.WaitGroup
	for range readers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for e := range work {
				b, err := fetchBody(ctx, e.URL)
				if err != nil {
					continue
				}
				mu.Lock()
				out[e.ID] = b
				mu.Unlock()
			}
		}()
	}
	for _, e := range rows {
		if e.URL != "" {
			work <- e
		}
	}
	close(work)
	wg.Wait()
	return out
}

// fetchBody is the read seam: the page a visitor would be served, or an error.
// A package var so the gate is testable without a live edge.
var fetchBody = func(ctx context.Context, url string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, bodyWait)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "text/html")
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("catalog: %s: %s", url, resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxBody {
		return nil, fmt.Errorf("catalog: %s: body over %d bytes", url, maxBody)
	}
	return b, nil
}

func digest(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// inert reports that a page will never become more than it already is: it runs no
// script of its own, pulls in no first-party asset, opens no frame and goes
// nowhere. This is the half of "thin" that keeps a real app's 700-byte shell —
// which loads its bundle — apart from a probe that only ever says "x".
//
// Scripts and links to ANOTHER host do not count: the platform injects its own
// analytics and the edge injects its beacon into every page we serve, so counting
// those would make every page look alive.
func inert(b []byte) bool {
	if metaRefresh.Match(b) || frame.Match(b) {
		return false
	}
	for _, m := range inlineScript.FindAllSubmatch(b, -1) {
		if len(strings.TrimSpace(string(m[1]))) > 0 {
			return false
		}
	}
	for _, m := range linked.FindAllSubmatch(b, -1) {
		if firstParty(string(m[1])) {
			return false
		}
	}
	return true
}

// firstParty is true for a reference the SITE itself owns — a relative URL. An
// absolute one belongs to whoever we injected, and a fragment or data: URI
// fetches nothing at all.
func firstParty(v string) bool {
	v = strings.ToLower(strings.TrimSpace(v))
	if v == "" {
		return false
	}
	for _, p := range []string{"http://", "https://", "//", "#", "data:", "mailto:", "tel:", "javascript:"} {
		if strings.HasPrefix(v, p) {
			return false
		}
	}
	return true
}

func scaffolded(b []byte) bool {
	t := text(b)
	for _, s := range scaffolds {
		if strings.Contains(t, s) {
			return true
		}
	}
	return false
}

// text reduces a page to what a reader actually sees: script, style and comment
// bodies gone, tags gone, whitespace collapsed, lower-cased. Matching placeholder
// copy on THIS rather than on the raw bytes is what makes the scaffold list
// survive a re-indent or a wrapped <span>.
func text(b []byte) string {
	s := notContent.ReplaceAllString(string(b), " ")
	s = tags.ReplaceAllString(s, " ")
	return strings.ToLower(strings.TrimSpace(spaces.ReplaceAllString(s, " ")))
}

var (
	inlineScript = regexp.MustCompile(`(?is)<script\b[^>]*>(.*?)</script>`)
	linked       = regexp.MustCompile(`(?is)\b(?:src|href)\s*=\s*["']?([^"'\s>]+)`)
	metaRefresh  = regexp.MustCompile(`(?is)<meta\b[^>]*http-equiv\s*=\s*["']?\s*refresh`)
	frame        = regexp.MustCompile(`(?i)<iframe\b`)
	notContent   = regexp.MustCompile(`(?is)<script\b[^>]*>.*?</script>|<style\b[^>]*>.*?</style>|<!--.*?-->`)
	tags         = regexp.MustCompile(`(?s)<[^>]*>`)
	spaces       = regexp.MustCompile(`\s+`)
)
