package forge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// seen records what actually reached the wire. Every security property this
// package claims is a property of the REQUEST it emits, so the tests assert on
// the request rather than on the returned value: a client that returns the right
// issues while leaking the machine identity has still failed.
type seen struct {
	mu   sync.Mutex
	reqs []*http.Request
}

func (s *seen) add(r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := r.Clone(context.Background())
	s.reqs = append(s.reqs, c)
}

func (s *seen) all() []*http.Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*http.Request(nil), s.reqs...)
}

// forgeStub is a fake forge that enforces the REAL forge's sudo semantics, which
// this package's whole safety argument rests on: a sudoed request sees only what
// that user may see, and an unknown sudo user is 404. Measured against the live
// forge (see the package comment) before being written down here.
type forgeStub struct {
	*httptest.Server
	got *seen

	// visible maps a forge login to the orgs that user may see. A user absent
	// from the map does not exist and every request sudoing as them is 404.
	visible map[string][]string
	// issues and milestones are keyed by org and repo respectively.
	issues     map[string][]Issue
	milestones map[string][]Milestone
	repos      map[string][]Repo
	// refs maps "org/repo@ref" to the commit it names, for both routes a ref can
	// be asked through — /git/commits/{sha} and the /branches/* wildcard — so a
	// test cannot make the two disagree.
	refs map[string]string
	// trees maps "org/repo@sha" to the tar.gz the archive route serves.
	trees map[string][]byte
	token string
}

func newStub(t *testing.T) *forgeStub {
	t.Helper()
	s := &forgeStub{
		got:        &seen{},
		visible:    map[string][]string{},
		issues:     map[string][]Issue{},
		repos:      map[string][]Repo{},
		milestones: map[string][]Milestone{},
		refs:       map[string]string{},
		trees:      map[string][]byte{},
		token:      "machine-token-value",
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/", func(w http.ResponseWriter, r *http.Request) {
		s.got.add(r)

		if r.Header.Get("Authorization") != "token "+s.token {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		actor := r.Header.Get("Sudo")
		// NO Sudo header is the MACHINE, and it sees everything. Forgejo requires
		// the token behind Sudo to belong to a site administrator — that is what
		// makes Sudo work at all — so an unsudoed call is made by an administrator.
		// Modelling it as an unknown actor instead would make the machine calls
		// (Tip, Grant, the delivery tree read) untestable against this stub, and
		// they are exactly the calls whose reach needs pinning.
		machine := actor == ""
		orgs, known := s.visible[actor]
		if !machine && !known {
			// The real forge's answer for an unknown sudo user.
			w.WriteHeader(http.StatusNotFound)
			return
		}
		canSee := func(org string) bool {
			if machine {
				return true
			}
			for _, o := range orgs {
				if o == org {
					return true
				}
			}
			return false
		}

		path := strings.TrimPrefix(r.URL.Path, "/v1")
		switch {
		case path == "/repos/issues/search":
			org := r.URL.Query().Get("owner")
			if !canSee(org) {
				// The forge answers an empty set for an org the actor cannot see,
				// rather than 404 — which is exactly why cloud must not rely on the
				// status code alone for tenancy.
				writeJSON(w, []Issue{})
				return
			}
			writeJSON(w, pageOf(s.issues[org], r))
		case strings.HasSuffix(path, "/repos") && strings.HasPrefix(path, "/orgs/"):
			org := strings.TrimSuffix(strings.TrimPrefix(path, "/orgs/"), "/repos")
			if !canSee(org) {
				w.Header().Set("X-Total-Count", "0")
				writeJSON(w, []Repo{})
				return
			}
			// The real forge counts its paginated lists, and the repository walk
			// fetches its pages concurrently off that count. A stub that omitted it
			// would silently exercise only the serial fallback.
			w.Header().Set("X-Total-Count", strconv.Itoa(len(s.repos[org])))
			writeJSON(w, pageOf(s.repos[org], r))
		case strings.HasSuffix(path, "/milestones"):
			parts := strings.Split(strings.TrimPrefix(path, "/repos/"), "/")
			if len(parts) < 2 || !canSee(parts[0]) {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			writeJSON(w, s.milestones[parts[0]+"/"+parts[1]])
		case strings.Contains(path, "/git/commits/"):
			// The single-SEGMENT route: a sha, a tag, an unslashed branch, HEAD.
			owner, repo, ref, ok := repoRef(path, "/git/commits/")
			if !ok || !canSee(owner) {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			sha, known := s.refs[owner+"/"+repo+"@"+ref]
			if !known {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			writeJSON(w, map[string]any{"sha": sha})
		case strings.Contains(path, "/branches/"):
			// The WILDCARD route, which is the only one a slashed name reaches.
			owner, repo, ref, ok := repoRef(path, "/branches/")
			if !ok || !canSee(owner) {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			sha, known := s.refs[owner+"/"+repo+"@"+ref]
			if !known {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			writeJSON(w, map[string]any{"commit": map[string]any{"id": sha}})
		case strings.Contains(path, "/archive/"):
			owner, repo, name, ok := repoRef(path, "/archive/")
			if !ok || !canSee(owner) {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			tgz, known := s.trees[owner+"/"+repo+"@"+strings.TrimSuffix(name, ".tar.gz")]
			if !known {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "application/gzip")
			_, _ = w.Write(tgz)
		case strings.HasPrefix(path, "/repos/"):
			// One repository by name, which is how a single board is read.
			parts := strings.Split(strings.TrimPrefix(path, "/repos/"), "/")
			if len(parts) != 2 || !canSee(parts[0]) {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			for _, r := range s.repos[parts[0]] {
				if strings.EqualFold(r.Name, parts[1]) {
					writeJSON(w, r)
					return
				}
			}
			w.WriteHeader(http.StatusNotFound)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	s.Server = httptest.NewServer(mux)
	t.Cleanup(s.Server.Close)
	return s
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// repoRef splits "/repos/{owner}/{repo}{route}{rest}" into its three parts. rest
// is taken WHOLE — it is what a wildcard route receives, so a slashed branch
// name arrives here exactly as the forge would see it.
func repoRef(path, route string) (owner, repo, rest string, ok bool) {
	head, rest, found := strings.Cut(path, route)
	if !found {
		return "", "", "", false
	}
	parts := strings.Split(strings.TrimPrefix(head, "/repos/"), "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" || rest == "" {
		return "", "", "", false
	}
	return parts[0], parts[1], rest, true
}

// pageOf applies the forge's limit/page paging to a slice.
func pageOf[T any](all []T, r *http.Request) []T {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 {
		limit = page
	}
	pg, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if pg <= 0 {
		pg = 1
	}
	start := (pg - 1) * limit
	if start >= len(all) {
		return nil
	}
	end := min(start+limit, len(all))
	return all[start:end]
}

// client builds a client against the stub. The stub is http, so it exercises the
// loopback exemption in New deliberately.
func (s *forgeStub) client(t *testing.T) *Client {
	t.Helper()
	c, err := New(s.URL, s.token)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

// ── construction: the credential cannot be absent or in the clear ────────────

func TestNew_RefusesUnusableConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name, host, token string
		want              error
	}{
		{name: "no token", host: "https://git.hanzo.ai", token: "", want: ErrNoToken},
		{name: "blank token", host: "https://git.hanzo.ai", token: "   ", want: ErrNoToken},
		{name: "no host", host: "", token: "t"},
		{name: "cleartext remote host", host: "http://git.hanzo.ai", token: "t"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, err := New(tc.host, tc.token)
			if err == nil {
				t.Fatalf("New(%q) succeeded, want refusal; client=%+v", tc.host, c)
			}
			if tc.want != nil && !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestNew_BareHostBecomesHTTPSAndCarriesTheV1Prefix(t *testing.T) {
	c, err := New("git.hanzo.ai", "t")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// /v1 and never /api/v1 — the one wire fact this package pins.
	if want := "https://git.hanzo.ai/v1"; c.base != want {
		t.Fatalf("base = %q, want %q", c.base, want)
	}
}

// ── the fail-closed property: no actor is a refusal, never the machine ───────

// A client with no actor must refuse EVERY call. This is the single most
// important property in the package: if an unscoped client fell back to the raw
// machine token, one forgotten As() would read every private repo on the forge
// with admin-equivalent rights.
func TestNoActor_EveryCallRefusesAndNothingReachesTheWire(t *testing.T) {
	s := newStub(t)
	s.visible["alice"] = []string{"acme"}
	s.issues["acme"] = []Issue{{Number: 1, Title: "hello"}}
	c := s.client(t)

	calls := map[string]func() error{
		"Issues":     func() error { _, err := c.Issues(t.Context(), "acme", IssueFilter{}); return err },
		"Repos":      func() error { _, err := c.Repos(t.Context(), "acme"); return err },
		"Milestones": func() error { _, err := c.Milestones(t.Context(), "acme"); return err },
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			if err := call(); !errors.Is(err, ErrNoActor) {
				t.Fatalf("%s with no actor: err = %v, want ErrNoActor", name, err)
			}
		})
	}
	if n := len(s.got.all()); n != 0 {
		t.Fatalf("%d requests reached the forge from an unscoped client; want 0", n)
	}
}

// As("") must not silently produce a working machine-identity client.
func TestAs_BlankLoginStaysRefusing(t *testing.T) {
	s := newStub(t)
	c := s.client(t).As("   ")
	if _, err := c.Issues(t.Context(), "acme", IssueFilter{}); !errors.Is(err, ErrNoActor) {
		t.Fatalf("As(blank): err = %v, want ErrNoActor", err)
	}
}

// ── what actually reaches the wire ───────────────────────────────────────────

func TestWire_CredentialAndActorAreSentCorrectly(t *testing.T) {
	s := newStub(t)
	s.visible["alice"] = []string{"acme"}
	s.issues["acme"] = []Issue{{Number: 1}}

	if _, err := s.client(t).As("alice").Issues(t.Context(), "acme", IssueFilter{}); err != nil {
		t.Fatalf("Issues: %v", err)
	}
	reqs := s.got.all()
	if len(reqs) == 0 {
		t.Fatal("no request reached the forge")
	}
	r := reqs[0]
	if got := r.Header.Get("Authorization"); got != "token "+s.token {
		t.Fatalf("Authorization = %q, want the machine token", got)
	}
	if got := r.Header.Get("Sudo"); got != "alice" {
		t.Fatalf("Sudo = %q, want alice", got)
	}
	// The actor must NOT ride in the query string, where it lands in access logs
	// and proxy traces on every hop.
	if v := r.URL.Query().Get("sudo"); v != "" {
		t.Fatalf("actor leaked into the query string as sudo=%q", v)
	}
	// The tenancy of the call is the owner parameter, and it must equal the org
	// argument exactly.
	if got := r.URL.Query().Get("owner"); got != "acme" {
		t.Fatalf("owner = %q, want acme", got)
	}
	// /v1, never /api/v1.
	if !strings.HasPrefix(r.URL.Path, "/v1/") || strings.HasPrefix(r.URL.Path, "/api/") {
		t.Fatalf("path = %q, want a /v1 path", r.URL.Path)
	}
}

// The credential must never appear in an error string — errors are logged, and a
// logged token is a leaked token.
func TestErrors_NeverCarryTheCredential(t *testing.T) {
	s := newStub(t)
	s.visible["alice"] = []string{"acme"}
	c := s.client(t)

	_, errNoActor := c.Issues(t.Context(), "acme", IssueFilter{})
	_, errUnknown := c.As("nobody").Issues(t.Context(), "acme", IssueFilter{})
	_, errBadOrg := c.As("alice").Issues(t.Context(), "../admin", IssueFilter{})
	// A transport failure, which is the error most likely to embed the request.
	dead, err := New("https://127.0.0.1:1/", s.token)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, errDial := dead.As("alice").Issues(t.Context(), "acme", IssueFilter{})

	for _, e := range []error{errNoActor, errUnknown, errBadOrg, errDial} {
		if e == nil {
			continue
		}
		if strings.Contains(e.Error(), s.token) {
			t.Fatalf("error leaked the credential: %v", e)
		}
	}
}

// ── tenancy: the org is an argument, and it is the only thing that scopes ─────

// The whole point of the design. Alice may see acme; she may not see umbrella.
// Asking for umbrella with alice's actor returns umbrella's data ONLY if the
// forge lets her have it — the client neither adds nor removes rows.
func TestTenancy_ActorCannotReadAnotherOrgsIssues(t *testing.T) {
	s := newStub(t)
	s.visible["alice"] = []string{"acme"}
	s.visible["mallory"] = []string{"umbrella"}
	s.issues["acme"] = []Issue{{Number: 1, Title: "acme private work"}}
	s.issues["umbrella"] = []Issue{{Number: 7, Title: "umbrella secret"}}
	c := s.client(t)

	// Mallory, aiming at acme, gets nothing — the forge refused her, not us.
	got, err := c.As("mallory").Issues(t.Context(), "acme", IssueFilter{})
	if err != nil {
		t.Fatalf("Issues: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("mallory read %d of acme's issues: %+v", len(got), got)
	}

	// Alice, aiming at acme, gets acme's work.
	got, err = c.As("alice").Issues(t.Context(), "acme", IssueFilter{})
	if err != nil {
		t.Fatalf("Issues: %v", err)
	}
	if len(got) != 1 || got[0].Title != "acme private work" {
		t.Fatalf("alice got %+v, want acme's one issue", got)
	}
}

// An IssueFilter must not be able to carry tenancy. This is a COMPILE-TIME
// property — there is no Org field to set — and the test pins it so that adding
// one becomes a deliberate act that breaks a named test rather than a quiet
// convenience. A filter field would be caller-supplied, and a tenant key read
// from caller-supplied data is a cross-tenant read the caller asserted for
// itself (apps/tracker/typed.go states the same rule for In fields).
func TestIssueFilter_CarriesNoTenancy(t *testing.T) {
	for _, forbidden := range []string{"Org", "Owner", "Tenant", "Repo", "Sudo", "Actor", "User"} {
		if fieldExists[IssueFilter](forbidden) {
			t.Fatalf("IssueFilter has a %s field: tenancy must be an argument, never a filter", forbidden)
		}
	}
}

func fieldExists[T any](name string) bool {
	rt := reflect.TypeFor[T]()
	for i := range rt.NumField() {
		if rt.Field(i).Name == name {
			return true
		}
	}
	return false
}

// An unknown actor is a distinct, explicit answer — never an empty board.
func TestUnknownActor_IsAnErrorNotAnEmptyBoard(t *testing.T) {
	s := newStub(t)
	s.visible["alice"] = []string{"acme"}

	got, err := s.client(t).As("ghost").Issues(t.Context(), "acme", IssueFilter{})
	if !errors.Is(err, ErrUnknownActor) {
		t.Fatalf("err = %v, want ErrUnknownActor", err)
	}
	if got != nil {
		t.Fatalf("got %+v alongside the error; want nil", got)
	}
}

// The org is interpolated into a URL path, so a value bearing a separator would
// address an endpoint the call site did not write.
func TestValidOrg_RefusesAnythingThatIsNotAPathSegment(t *testing.T) {
	s := newStub(t)
	s.visible["alice"] = []string{"acme"}
	c := s.client(t).As("alice")

	for _, bad := range []string{
		"", "   ", "acme/../admin", "acme/repos", "..", "a%2fb", "acme#frag", "acme?x=1", " acme",
	} {
		t.Run(fmt.Sprintf("%q", bad), func(t *testing.T) {
			if _, err := c.Repos(t.Context(), bad); err == nil {
				t.Fatalf("Repos(%q) succeeded; want refusal", bad)
			}
			if _, err := c.Milestones(t.Context(), bad); err == nil {
				t.Fatalf("Milestones(%q) succeeded; want refusal", bad)
			}
		})
	}
}

// ── the org rollup the forge does not offer ──────────────────────────────────

// Milestones is repo-scoped upstream, so the org view is a server-side fan-out.
// It must cover every live repo, stamp each milestone with the repo it came
// from, and skip archived repos.
func TestMilestones_FansOutOverTheOrgsRepos(t *testing.T) {
	s := newStub(t)
	s.visible["alice"] = []string{"acme"}
	s.repos["acme"] = []Repo{
		{Name: "api", FullName: "acme/api"},
		{Name: "web", FullName: "acme/web"},
		{Name: "old", FullName: "acme/old", Archived: true},
	}
	s.milestones["acme/api"] = []Milestone{{ID: 1, Title: "v1", State: "open", Open: 3}}
	s.milestones["acme/web"] = []Milestone{{ID: 2, Title: "launch", State: "open", Open: 5}}
	s.milestones["acme/old"] = []Milestone{{ID: 3, Title: "ancient"}}

	got, err := s.client(t).As("alice").Milestones(t.Context(), "acme")
	if err != nil {
		t.Fatalf("Milestones: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d milestones, want 2 (archived repo excluded): %+v", len(got), got)
	}
	byRepo := map[string]Milestone{}
	for _, m := range got {
		byRepo[m.Repo] = m
	}
	if m, ok := byRepo["api"]; !ok || m.Title != "v1" {
		t.Fatalf("missing api/v1; got %+v", got)
	}
	if m, ok := byRepo["web"]; !ok || m.Title != "launch" {
		t.Fatalf("missing web/launch; got %+v", got)
	}
	for _, m := range got {
		if m.Repo == "" {
			t.Fatalf("milestone %q has no repo stamped: an org rollup that cannot say where a milestone came from is unusable", m.Title)
		}
		if m.Title == "ancient" {
			t.Fatal("archived repo's milestone leaked into the rollup")
		}
	}
}

// A partial rollup presented as a complete one is a wrong answer. One repo
// failing must fail the whole call.
func TestMilestones_OneRepoFailingFailsTheRollup(t *testing.T) {
	s := newStub(t)
	s.visible["alice"] = []string{"acme"}
	s.repos["acme"] = []Repo{{Name: "api"}, {Name: "web"}}
	s.milestones["acme/api"] = []Milestone{{Title: "v1"}}
	// acme/web has no milestones entry; the stub 404s only on an org the actor
	// cannot see, so make the failure explicit by swapping the handler.
	s.Server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.got.add(r)
		if strings.Contains(r.URL.Path, "/web/milestones") {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/repos") {
			writeJSON(w, s.repos["acme"])
			return
		}
		writeJSON(w, s.milestones["acme/api"])
	})

	if _, err := s.client(t).As("alice").Milestones(t.Context(), "acme"); err == nil {
		t.Fatal("rollup succeeded while a repo failed; a partial answer must not read as complete")
	}
}

// The fan-out must stay bounded, or a large org turns a board load into a
// denial-of-service against our own forge.
func TestMilestones_FanOutIsBounded(t *testing.T) {
	s := newStub(t)
	s.visible["alice"] = []string{"acme"}
	for i := range 40 {
		s.repos["acme"] = append(s.repos["acme"], Repo{Name: fmt.Sprintf("r%d", i)})
	}

	var live, peak int64
	s.Server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/milestones") {
			n := atomic.AddInt64(&live, 1)
			for {
				old := atomic.LoadInt64(&peak)
				if n <= old || atomic.CompareAndSwapInt64(&peak, old, n) {
					break
				}
			}
			defer atomic.AddInt64(&live, -1)
			writeJSON(w, []Milestone{{Title: "m"}})
			return
		}
		writeJSON(w, pageOf(s.repos["acme"], r))
	})

	if _, err := s.client(t).As("alice").Milestones(t.Context(), "acme"); err != nil {
		t.Fatalf("Milestones: %v", err)
	}
	if p := atomic.LoadInt64(&peak); p > fanout {
		t.Fatalf("peak concurrent repo requests = %d, want <= %d", p, fanout)
	}
}

// ── pagination ───────────────────────────────────────────────────────────────

func TestIssues_PaginatesAndTerminates(t *testing.T) {
	s := newStub(t)
	s.visible["alice"] = []string{"acme"}
	for i := range 120 {
		s.issues["acme"] = append(s.issues["acme"], Issue{Number: int64(i + 1)})
	}
	got, err := s.client(t).As("alice").Issues(t.Context(), "acme", IssueFilter{})
	if err != nil {
		t.Fatalf("Issues: %v", err)
	}
	if len(got) != 120 {
		t.Fatalf("got %d issues, want 120", len(got))
	}
}

func TestIssues_LimitIsHonoured(t *testing.T) {
	s := newStub(t)
	s.visible["alice"] = []string{"acme"}
	for i := range 120 {
		s.issues["acme"] = append(s.issues["acme"], Issue{Number: int64(i + 1)})
	}
	got, err := s.client(t).As("alice").Issues(t.Context(), "acme", IssueFilter{Limit: 10})
	if err != nil {
		t.Fatalf("Issues: %v", err)
	}
	if len(got) != 10 {
		t.Fatalf("got %d issues, want 10", len(got))
	}
}

// A forge that never stops answering full pages must not spin this process
// forever.
func TestIssues_StopsAtMaxPagesAgainstAnEndlessForge(t *testing.T) {
	s := newStub(t)
	var hits int64
	s.Server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&hits, 1)
		full := make([]Issue, page)
		writeJSON(w, full)
	})
	got, err := s.client(t).As("alice").Issues(t.Context(), "acme", IssueFilter{})
	if err != nil {
		t.Fatalf("Issues: %v", err)
	}
	if n := atomic.LoadInt64(&hits); n > maxPages {
		t.Fatalf("made %d requests against an endless forge, want <= %d", n, maxPages)
	}
	if len(got) != page*maxPages {
		t.Fatalf("got %d issues, want the bounded %d", len(got), page*maxPages)
	}
}

// ── As() must not mutate a shared client ─────────────────────────────────────

// Two concurrent requests derive two actors from one shared client. If As
// mutated the receiver they would interleave and one user's request would be
// attributed to the other — a cross-user attribution race.
func TestAs_DoesNotMutateTheSharedClient(t *testing.T) {
	s := newStub(t)
	s.visible["alice"] = []string{"acme"}
	s.visible["bob"] = []string{"acme"}
	shared := s.client(t)

	if shared.Actor() != "" {
		t.Fatalf("fresh client already has actor %q", shared.Actor())
	}
	a, b := shared.As("alice"), shared.As("bob")
	if a.Actor() != "alice" || b.Actor() != "bob" {
		t.Fatalf("actors crossed: a=%q b=%q", a.Actor(), b.Actor())
	}
	if shared.Actor() != "" {
		t.Fatalf("As mutated the shared client: actor is now %q", shared.Actor())
	}

	var wg sync.WaitGroup
	for range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if got := shared.As("alice").Actor(); got != "alice" {
				t.Errorf("concurrent As returned actor %q", got)
			}
		}()
	}
	wg.Wait()
}

// ── writes ───────────────────────────────────────────────────────────────────

// A write with no actor must never fall back to the machine identity: that would
// file issues as a shared bot and destroy attribution, on top of the escalation.
func TestWrites_RefuseWithoutAnActor(t *testing.T) {
	s := newStub(t)
	c := s.client(t)
	title := "x"
	calls := map[string]func() error{
		"CreateIssue": func() error { _, err := c.CreateIssue(t.Context(), "acme", "api", NewIssue{Title: "t"}); return err },
		"PatchIssue":  func() error { return c.PatchIssue(t.Context(), "acme", "api", 1, IssuePatch{Title: &title}) },
		"SetLabels":   func() error { return c.SetLabels(t.Context(), "acme", "api", 1, []string{"todo"}) },
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			if err := call(); !errors.Is(err, ErrNoActor) {
				t.Fatalf("%s unscoped: err = %v, want ErrNoActor", name, err)
			}
		})
	}
	if n := len(s.got.all()); n != 0 {
		t.Fatalf("%d write requests reached the forge unscoped; want 0", n)
	}
}

// Every write must be attributed to the human via Sudo, so the forge records who
// really did it.
func TestWrites_AreAttributedToTheActor(t *testing.T) {
	s := newStub(t)
	var got []*http.Request
	var mu sync.Mutex
	s.Server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		got = append(got, r.Clone(context.Background()))
		mu.Unlock()
		writeJSON(w, Issue{Number: 5, Title: "filed"})
	})
	c := s.client(t).As("alice")

	if _, err := c.CreateIssue(t.Context(), "acme", "api", NewIssue{Title: "filed", Labels: []string{"todo"}}); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	if err := c.SetLabels(t.Context(), "acme", "api", 5, []string{"in_progress"}); err != nil {
		t.Fatalf("SetLabels: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 2 {
		t.Fatalf("got %d requests, want 2", len(got))
	}
	for _, r := range got {
		if r.Header.Get("Sudo") != "alice" {
			t.Fatalf("%s %s carried Sudo=%q, want alice", r.Method, r.URL.Path, r.Header.Get("Sudo"))
		}
		if r.Header.Get("Authorization") != "token "+s.token {
			t.Fatalf("%s %s did not carry the machine credential", r.Method, r.URL.Path)
		}
	}
	if got[0].Method != http.MethodPost {
		t.Fatalf("CreateIssue used %s, want POST", got[0].Method)
	}
	// A move is a RELABEL — PUT on the labels sub-resource, replacing the set.
	if got[1].Method != http.MethodPut || !strings.HasSuffix(got[1].URL.Path, "/issues/5/labels") {
		t.Fatalf("SetLabels sent %s %s, want PUT .../issues/5/labels", got[1].Method, got[1].URL.Path)
	}
}

// The forge refusing a sudoed write (the ACTOR lacks the permission) must surface
// as a refusal, not be swallowed into a success.
func TestWrites_ForgeRefusalSurfaces(t *testing.T) {
	s := newStub(t)
	s.Server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	})
	err := s.client(t).As("alice").SetLabels(t.Context(), "acme", "api", 1, []string{"done"})
	if err == nil {
		t.Fatal("a 403 from the forge was swallowed; a refused write must not read as success")
	}
	if strings.Contains(err.Error(), s.token) {
		t.Fatalf("refusal leaked the credential: %v", err)
	}
}

// A repo name is interpolated into the path exactly like an org, so it needs the
// same refusal.
func TestWrites_RefuseARepoThatIsNotAPathSegment(t *testing.T) {
	s := newStub(t)
	c := s.client(t).As("alice")
	for _, bad := range []string{"", "../../admin", "a/b", "x?y"} {
		if err := c.SetLabels(t.Context(), "acme", bad, 1, []string{"done"}); err == nil {
			t.Fatalf("SetLabels(repo=%q) succeeded; want refusal", bad)
		}
	}
}

// An IssuePatch must omit absent fields, or a patch that only moves a card would
// blank the title and body.
func TestIssuePatch_OmitsAbsentFields(t *testing.T) {
	s := newStub(t)
	var body []byte
	s.Server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		writeJSON(w, Issue{})
	})
	state := "closed"
	if err := s.client(t).As("alice").PatchIssue(t.Context(), "acme", "api", 3, IssuePatch{State: &state}); err != nil {
		t.Fatalf("PatchIssue: %v", err)
	}
	var sent map[string]any
	if err := json.Unmarshal(body, &sent); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, ok := sent["title"]; ok {
		t.Fatalf("patch sent a title it was not given: %s", body)
	}
	if _, ok := sent["body"]; ok {
		t.Fatalf("patch sent a body it was not given: %s", body)
	}
	if sent["state"] != "closed" {
		t.Fatalf("patch did not carry the state: %s", body)
	}
}
