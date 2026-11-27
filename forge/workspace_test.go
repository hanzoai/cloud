package forge

// workspace_test.go — the namespace the platform makes for an org, against a
// forge that answers as this one does: a taken org name is 422, a taken
// repository is 409, a copy the forge could not finish keeps its row, and a
// repository reports whether its Actions run.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// yard is a forge with memory: which orgs and repositories exist, and every
// request that could change something, in order.
type yard struct {
	*httptest.Server
	mu     sync.Mutex
	orgs   map[string]string // name → visibility
	repos  map[string]*plot  // owner/name → repository
	writes []string          // "METHOD path" of every non-GET
	made   int               // orgs the forge created
	public bool              // create every org public, whatever was asked
	loud   bool              // ignore a request to turn Actions off
	broken bool              // fail every copy after its row exists, as a failed clone does
	refuse bool              // refuse every clone address, as an unresolved or disallowed host is
	slow   chan struct{}     // when set, an org create waits for it, as a forge under load does
	asked  chan struct{}     // told when a waiting org create has arrived
	copies []map[string]any  // every migrate body
}

type plot struct {
	private bool
	actions bool
	branch  string
}

func newYard(t *testing.T, orgs ...string) *yard {
	t.Helper()
	y := &yard{orgs: map[string]string{}, repos: map[string]*plot{}}
	for _, o := range orgs {
		y.orgs[o] = "public"
	}
	y.Server = httptest.NewServer(http.HandlerFunc(y.serve))
	t.Cleanup(y.Server.Close)
	return y
}

func (y *yard) machine(t *testing.T) *Client {
	t.Helper()
	c, err := New(y.URL, "tok")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c.Machine()
}

func (y *yard) serve(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, API)
	if y.slow != nil && path == "/orgs" && r.Method == http.MethodPost {
		y.asked <- struct{}{}
		<-y.slow
	}
	y.mu.Lock()
	defer y.mu.Unlock()
	if r.Method != http.MethodGet {
		y.writes = append(y.writes, r.Method+" "+path)
	}
	fail := func(code int, msg string) {
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(map[string]any{"message": msg})
	}
	repoJSON := func(key string, p *plot) map[string]any {
		owner, name, _ := strings.Cut(key, "/")
		return map[string]any{"name": name, "full_name": key, "private": p.private, "has_actions": p.actions,
			"default_branch": p.branch, "ssh_url": "git@git.test:" + key + ".git",
			"owner": map[string]any{"login": owner}, "permissions": map[string]any{"admin": true, "push": true, "pull": true}}
	}
	switch {
	case path == "/orgs" && r.Method == http.MethodPost:
		var in struct{ Username, Visibility string }
		_ = json.NewDecoder(r.Body).Decode(&in)
		if _, taken := y.orgs[in.Username]; taken {
			fail(http.StatusUnprocessableEntity, "user already exists [name: "+in.Username+"]")
			return
		}
		vis := in.Visibility
		if y.public {
			vis = "public"
		}
		y.orgs[in.Username] = vis
		y.made++
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"username": in.Username, "visibility": vis})

	case strings.HasPrefix(path, "/orgs/") && strings.HasSuffix(path, "/repos") && r.Method == http.MethodPost:
		owner := strings.TrimSuffix(strings.TrimPrefix(path, "/orgs/"), "/repos")
		var in struct {
			Name    string
			Private bool
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		if _, ok := y.orgs[owner]; !ok {
			fail(http.StatusNotFound, "no org")
			return
		}
		key := owner + "/" + in.Name
		if _, taken := y.repos[key]; taken {
			fail(http.StatusConflict, "repository exists")
			return
		}
		y.repos[key] = &plot{private: in.Private, actions: true, branch: "main"}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(repoJSON(key, y.repos[key]))

	case strings.HasPrefix(path, "/orgs/") && strings.HasSuffix(path, "/repos") && r.Method == http.MethodGet:
		owner := strings.TrimSuffix(strings.TrimPrefix(path, "/orgs/"), "/repos")
		if _, ok := y.orgs[owner]; !ok {
			fail(http.StatusNotFound, "no org")
			return
		}
		var list []map[string]any
		for key := range y.repos {
			if strings.HasPrefix(key, owner+"/") {
				list = append(list, repoJSON(key, y.repos[key]))
			}
		}
		w.Header().Set("X-Total-Count", strconv.Itoa(len(list)))
		if len(list) > 1 {
			list = list[:1] // one page of one, as asked
		}
		_ = json.NewEncoder(w).Encode(list)

	case strings.HasPrefix(path, "/orgs/"):
		name := strings.TrimPrefix(path, "/orgs/")
		vis, ok := y.orgs[name]
		if !ok {
			fail(http.StatusNotFound, "no org")
			return
		}
		if r.Method == http.MethodDelete {
			delete(y.orgs, name)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"username": name, "visibility": vis})

	case path == "/repos/migrate":
		var in map[string]any
		_ = json.NewDecoder(r.Body).Decode(&in)
		y.copies = append(y.copies, in)
		// The forge judges the clone address BEFORE the name (routers/api/v1/repo
		// migrate.go: IsMigrateURLAllowed, then CreateRepositoryDirectly), so an
		// address it will not reach is 422 whether or not the name is taken.
		if y.refuse {
			fail(http.StatusUnprocessableEntity, "Invalid clone address: could not resolve host")
			return
		}
		key := fmt.Sprint(in["repo_owner"]) + "/" + fmt.Sprint(in["repo_name"])
		if _, taken := y.repos[key]; taken {
			fail(http.StatusConflict, "The repository with the same name already exists.")
			return
		}
		y.repos[key] = &plot{private: in["private"] == true, actions: true, branch: "main"}
		if y.broken {
			fail(http.StatusUnprocessableEntity, "Migration failed: fatal: could not read from remote.")
			return
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(repoJSON(key, y.repos[key]))

	case strings.HasPrefix(path, "/repos/"):
		rest := strings.TrimPrefix(path, "/repos/")
		parts := strings.SplitN(rest, "/", 3)
		if len(parts) < 2 {
			fail(http.StatusNotFound, "no repo")
			return
		}
		key := parts[0] + "/" + parts[1]
		p, ok := y.repos[key]
		if !ok {
			fail(http.StatusNotFound, "no repo")
			return
		}
		sub := ""
		if len(parts) == 3 {
			sub = parts[2]
		}
		switch {
		case sub == "" && r.Method == http.MethodDelete:
			delete(y.repos, key)
			w.WriteHeader(http.StatusNoContent)
		case sub == "" && r.Method == http.MethodPatch:
			var in struct {
				Actions *bool `json:"has_actions"`
				Private *bool `json:"private"`
			}
			_ = json.NewDecoder(r.Body).Decode(&in)
			if in.Actions != nil && !y.loud {
				p.actions = *in.Actions
			}
			if in.Private != nil {
				p.private = *in.Private
			}
			_ = json.NewEncoder(w).Encode(repoJSON(key, p))
		case sub == "":
			_ = json.NewEncoder(w).Encode(repoJSON(key, p))
		case sub == "teams":
			_ = json.NewEncoder(w).Encode([]map[string]any{{"name": "Owners", "permission": "owner"}})
		case strings.HasPrefix(sub, "collaborators"):
			_ = json.NewEncoder(w).Encode([]any{})
		case sub == "branch_protections" && r.Method == http.MethodPost:
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{})
		default:
			fail(http.StatusNotFound, "no route")
		}

	default:
		fail(http.StatusNotFound, "no route")
	}
}

// touched is every write that addressed name, as an org or as the owner of a
// repository — the attempt to create it included.
func (y *yard) touched(name string) []string {
	y.mu.Lock()
	defer y.mu.Unlock()
	var out []string
	for _, w := range y.writes {
		_, p, _ := strings.Cut(w, " ")
		if p == "/orgs/"+name || strings.HasPrefix(p, "/orgs/"+name+"/") || strings.HasPrefix(p, "/repos/"+name+"/") {
			out = append(out, w)
		}
	}
	return out
}

// ledger is a Book in memory, with the uniqueness the real one enforces.
type ledger struct {
	mu sync.Mutex
	m  map[string]string
}

func newLedger() *ledger { return &ledger{m: map[string]string{}} }

func (l *ledger) Read(_ context.Context, org string) (string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.m[org], nil
}

func (l *ledger) Write(_ context.Context, org, owner string) (string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if have := l.m[org]; have != "" {
		return have, nil
	}
	for o, n := range l.m {
		if n == owner && o != org {
			return "", fmt.Errorf("%w: %s is %s's", ErrExists, owner, o)
		}
	}
	l.m[org] = owner
	return owner, nil
}

// A NEW ORG GETS A NAMESPACE OF ITS OWN: made by the machine, private, recorded
// after the forge made it, and made once — the second ask reads the record.
func TestProvisionMakesAPrivateNamespaceOnce(t *testing.T) {
	y := newYard(t)
	book := newLedger()
	c := y.machine(t)

	ws, err := Provision(t.Context(), c, book, "acme")
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if ws != (Workspace{Owner: "acme", Made: true}) {
		t.Fatalf("Provision = %+v, want acme, made", ws)
	}
	if y.orgs["acme"] != "private" {
		t.Fatalf("the namespace is %q, want private", y.orgs["acme"])
	}
	if book.m["acme"] != "acme" {
		t.Fatalf("the record says %q", book.m["acme"])
	}
	again, err := Provision(t.Context(), c, book, "acme")
	if err != nil || again != ws || y.made != 1 {
		t.Fatalf("the second ask = %+v, %v with %d orgs made; want the same one and no second", again, err, y.made)
	}
	if got, err := Find(t.Context(), book, "acme"); err != nil || got != ws {
		t.Fatalf("Find = %+v, %v; want what Provision recorded", got, err)
	}
	if _, err := Org("acme"); err == nil {
		t.Fatal("a made namespace resolves in the delivery direction")
	}
}

// A TAKEN NAME IS NUMBERED, NEVER ADOPTED. The stranger's namespace is asked
// for once, refused, and never written to again.
func TestProvisionNumbersATakenNameAndNeverAdoptsIt(t *testing.T) {
	y := newYard(t, "acme", "acme-2")
	book := newLedger()

	ws, err := Provision(t.Context(), y.machine(t), book, "acme")
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if ws.Owner != "acme-3" || !ws.Made {
		t.Fatalf("Provision = %+v, want acme-3", ws)
	}
	for _, taken := range []string{"acme", "acme-2"} {
		if got := y.touched(taken); fmt.Sprint(got) != "[]" {
			t.Fatalf("the taken namespace %s was written to: %v", taken, got)
		}
		if y.orgs[taken] != "public" {
			t.Fatalf("the taken namespace %s changed: %q", taken, y.orgs[taken])
		}
	}
	if book.m["acme"] != "acme-3" {
		t.Fatalf("recorded %q", book.m["acme"])
	}
}

// AN ORG SPELLED LIKE THE ESTATE NEVER REACHES IT. A name either estate table
// knows is not even asked for, the estate's orgs keep answering from the closed
// table with no forge call at all, and nothing that is made resolves as a
// delivery.
func TestProvisionNeverReachesTheEstate(t *testing.T) {
	estateNames := []string{"hanzoai", "hanzo-inc", "hanzo-apps", "hanzo-community", "hanzozt", "charts", "hanzo-docs", "hanzo-dart", "HanzoAI"}
	y := newYard(t, append(slices.Clone(estateNames[:len(estateNames)-1]), "luxfi", "zooai")...)
	book := newLedger()
	c := y.machine(t)

	for _, org := range append(estateNames, "luxfi", "zooai") {
		ws, err := Provision(t.Context(), c, book, org)
		if err != nil {
			t.Fatalf("Provision(%q): %v", org, err)
		}
		if Reserved(ws.Owner) || !ws.Made {
			t.Fatalf("Provision(%q) = %+v, an estate namespace", org, ws)
		}
		if _, err := Org(ws.Owner); err == nil {
			t.Fatalf("Provision(%q) made %q, which a delivery would attribute to an org", org, ws.Owner)
		}
	}
	for _, name := range append(estateNames[:len(estateNames)-1], "luxfi", "zooai") {
		if y.orgs[name] != "public" {
			t.Fatalf("%s changed: %q", name, y.orgs[name])
		}
		for _, w := range y.touched(name) {
			if w != "POST /orgs/"+name {
				t.Fatalf("%s was written to: %v", name, w)
			}
		}
	}
	for _, w := range y.writes {
		_, p, _ := strings.Cut(w, " ")
		for _, name := range estateNames {
			if strings.Contains(p, "/"+strings.ToLower(name)+"/") || strings.HasSuffix(p, "/"+strings.ToLower(name)) {
				t.Fatalf("a held namespace was addressed: %s", w)
			}
		}
	}

	before := len(y.writes)
	for org, want := range owners {
		ws, err := Provision(t.Context(), c, book, org)
		if err != nil || ws != (Workspace{Owner: want}) {
			t.Fatalf("Provision(%q) = %+v, %v; the estate answers from its table", org, ws, err)
		}
	}
	if len(y.writes) != before {
		t.Fatalf("the estate's orgs made forge writes: %v", y.writes[before:])
	}
}

// TWO FIRST RUNS AT ONCE MAKE ONE NAMESPACE, and both are told the same one.
func TestConcurrentFirstRunsConvergeOnOneNamespace(t *testing.T) {
	y := newYard(t)
	book := newLedger()
	c := y.machine(t)

	const runs = 16
	got := make([]Workspace, runs)
	errs := make([]error, runs)
	var wg sync.WaitGroup
	for i := range runs {
		wg.Go(func() { got[i], errs[i] = Provision(t.Context(), c, book, "acme") })
	}
	wg.Wait()
	for i := range runs {
		if errs[i] != nil || got[i] != got[0] {
			t.Fatalf("run %d = %+v, %v; run 0 = %+v", i, got[i], errs[i], got[0])
		}
	}
	if y.made != 1 || len(y.orgs) != 1 {
		t.Fatalf("%d namespaces were made (%v), want one", y.made, y.orgs)
	}
}

// A CALLER THAT STOPS WAITING DOES NOT STRAND A NAMESPACE. The forge makes the
// name whether or not anybody is still listening for its answer, so the create
// and its record finish together, and the org's next ask finds what was made
// rather than a name no record reaches and the next number along.
func TestAnAbandonedMakingStillLandsItsRecord(t *testing.T) {
	y := newYard(t)
	y.slow, y.asked = make(chan struct{}), make(chan struct{}, 1)
	book := newLedger()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		_, err := Provision(ctx, y.machine(t), book, "acme")
		done <- err
	}()
	<-y.asked
	cancel()
	close(y.slow)
	if err := <-done; err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if book.m["acme"] != "acme" || len(y.orgs) != 1 || y.orgs["acme"] != "private" {
		t.Fatalf("recorded %v, the forge holds %v: want one private acme, recorded", book.m, y.orgs)
	}
	ws, err := Find(t.Context(), book, "acme")
	if err != nil || ws.Owner != "acme" || !ws.Made {
		t.Fatalf("the next ask found %+v, %v", ws, err)
	}
}

// early is a Book another process wrote first: its Write answers the namespace
// already recorded, whatever it was handed.
type early struct {
	*ledger
	first string
}

func (e early) Write(ctx context.Context, org, _ string) (string, error) {
	return e.ledger.Write(ctx, org, e.first)
}

// A RACE LOST TO ANOTHER PROCESS KEEPS THE WINNER'S namespace, and the one this
// call made a moment ago is taken back rather than left holding a name no
// record reaches.
func TestALostRecordRaceTakesBackTheLoser(t *testing.T) {
	y := newYard(t, "acme-9")
	ws, err := Provision(t.Context(), y.machine(t), early{newLedger(), "acme-9"}, "acme")
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if ws.Owner != "acme-9" {
		t.Fatalf("Provision = %+v, want the recorded acme-9", ws)
	}
	if _, left := y.orgs["acme"]; left {
		t.Fatal("the namespace this call made and lost is still there")
	}
}

// torn is a Book that cannot write.
type torn struct{ *ledger }

func (torn) Write(context.Context, string, string) (string, error) {
	return "", errors.New("disk full")
}

// A NAMESPACE THAT CANNOT BE RECORDED IS NOT KEPT: nothing could ever reach it.
func TestAnUnrecordedNamespaceIsTakenBack(t *testing.T) {
	y := newYard(t)
	if _, err := Provision(t.Context(), y.machine(t), torn{newLedger()}, "acme"); err == nil {
		t.Fatal("a namespace nothing recorded was answered")
	}
	if len(y.orgs) != 0 {
		t.Fatalf("left behind: %v", y.orgs)
	}
}

// A RECORD NAMING THE ESTATE IS REFUSED, not acted in — whatever wrote it.
func TestFindRefusesARecordNamingTheEstate(t *testing.T) {
	for _, owner := range []string{"hanzoai", "hanzo-inc", "charts", "lux", "../x"} {
		book := newLedger()
		book.m["acme"] = owner
		if ws, err := Find(t.Context(), book, "acme"); err == nil {
			t.Fatalf("a record naming %q answered %+v", owner, ws)
		}
	}
	if ws, err := Find(t.Context(), newLedger(), "acme"); err != nil || ws != (Workspace{}) {
		t.Fatalf("an org with no record = %+v, %v; want none", ws, err)
	}
}

// MAKING A NAMESPACE IS THE MACHINE'S, never a person's.
func TestOnlyTheMachineMakesANamespace(t *testing.T) {
	y := newYard(t)
	c, _ := New(y.URL, "tok")
	if _, err := Provision(t.Context(), c.As("zoe"), newLedger(), "acme"); err == nil {
		t.Fatal("a person made a namespace")
	}
	if err := c.As("zoe").CreateOrg(t.Context(), "acme"); err == nil {
		t.Fatal("a person created an org")
	}
	if _, err := c.As("zoe").Create(t.Context(), "acme", "site", "", ""); err == nil {
		t.Fatal("a person created a repository through the strict create")
	}
	if len(y.writes) != 0 {
		t.Fatalf("the forge was written: %v", y.writes)
	}
}

// AN ORG THAT DOES NOT COME BACK PRIVATE IS TAKEN BACK and refused.
func TestCreateOrgRefusesAPublicNamespace(t *testing.T) {
	y := newYard(t)
	y.public = true
	if err := y.machine(t).CreateOrg(t.Context(), "acme"); err == nil {
		t.Fatal("a namespace that came back public was accepted")
	}
	if len(y.orgs) != 0 {
		t.Fatalf("left behind: %v", y.orgs)
	}
}

// A TEMPLATE IS COPIED, NOT MIRRORED: cloned anonymously as plain git into a
// private repository whose Actions are off and whose default branch refuses a
// run's key.
func TestCreateCopiesPrivatelyWithActionsOff(t *testing.T) {
	y := newYard(t, "acme")
	r, err := y.machine(t).Create(t.Context(), "acme", "synapse", "Synapse", "https://github.com/hanzo-apps/template-synapse")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if !r.Private || r.Actions == nil || *r.Actions {
		t.Fatalf("the copy is %+v, want private with Actions off", r)
	}
	p := y.repos["acme/synapse"]
	if p == nil || !p.private || p.actions {
		t.Fatalf("the forge holds %+v", p)
	}
	if len(y.copies) != 1 {
		t.Fatalf("copies: %v", y.copies)
	}
	in := y.copies[0]
	if in["clone_addr"] != "https://github.com/hanzo-apps/template-synapse" || in["service"] != "git" ||
		in["mirror"] != false || in["private"] != true {
		t.Fatalf("the copy asked for %v", in)
	}
	for _, k := range []string{"auth_username", "auth_password", "auth_token"} {
		if _, ok := in[k]; ok {
			t.Fatalf("the copy carried %s: a copy reads only what the world can", k)
		}
	}
	if !slices.Contains(y.writes, "POST /repos/acme/synapse/branch_protections") {
		t.Fatalf("the default branch was not protected: %v", y.writes)
	}
}

// A NEW REPOSITORY WITH NO SOURCE is born with a commit, private, Actions off.
func TestCreateStartsAnEmptyRepositoryClosed(t *testing.T) {
	y := newYard(t, "acme")
	r, err := y.machine(t).Create(t.Context(), "acme", "todo", "Build a todo app", "")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if !r.Private || r.Actions == nil || *r.Actions || y.repos["acme/todo"].actions {
		t.Fatalf("made %+v", r)
	}
}

// A TAKEN REPOSITORY NAME IS NEVER ADOPTED: the create is refused and the
// repository that was there is not written.
func TestCreateNeverAdoptsATakenRepository(t *testing.T) {
	y := newYard(t, "acme")
	y.repos["acme/synapse"] = &plot{private: false, actions: true, branch: "main"}
	for _, source := range []string{"", "https://github.com/hanzo-apps/template-synapse"} {
		_, err := y.machine(t).Create(t.Context(), "acme", "synapse", "", source)
		if !errors.Is(err, ErrExists) {
			t.Fatalf("source %q: Create over a taken name = %v, want ErrExists", source, err)
		}
	}
	p := y.repos["acme/synapse"]
	if p == nil || p.private || !p.actions {
		t.Fatalf("the repository that was there changed: %+v", p)
	}
	for _, w := range y.writes {
		if strings.HasPrefix(w, "PATCH") || strings.HasPrefix(w, "DELETE") || strings.Contains(w, "branch_protections") {
			t.Fatalf("the taken repository was written: %v", y.writes)
		}
	}
}

// A COPY THE FORGE COULD NOT FINISH IS TAKEN BACK, and so is one whose Actions
// would not turn off: either way nothing half-made is left.
func TestCreateLeavesNothingHalfMade(t *testing.T) {
	y := newYard(t, "acme")
	y.broken = true
	if _, err := y.machine(t).Create(t.Context(), "acme", "synapse", "", "https://github.com/hanzo-apps/template-synapse"); err == nil {
		t.Fatal("a failed copy was accepted")
	}
	if _, left := y.repos["acme/synapse"]; left {
		t.Fatal("a failed copy's row is still there")
	}

	y.broken, y.loud = false, true
	if _, err := y.machine(t).Create(t.Context(), "acme", "synapse", "", "https://github.com/hanzo-apps/template-synapse"); err == nil {
		t.Fatal("a copy whose Actions stayed on was accepted")
	}
	if _, left := y.repos["acme/synapse"]; left {
		t.Fatal("a copy whose Actions stayed on is still there")
	}
}

// A REPOSITORY THE COPY DID NOT MAKE IS NEVER TAKEN BACK. The forge refuses an
// address it will not reach before it looks at the name, so a refused copy over
// a name that is somebody's work must leave that work exactly where it was.
func TestCreateNeverTakesBackARepositoryItDidNotMake(t *testing.T) {
	y := newYard(t, "acme")
	y.repos["acme/folio"] = &plot{private: true, branch: "main"}
	y.refuse = true
	_, err := y.machine(t).Create(t.Context(), "acme", "folio", "", "https://github.com/hanzo-templates/folio")
	if !errors.Is(err, ErrExists) {
		t.Fatalf("Create over a taken name = %v, want ErrExists", err)
	}
	if _, there := y.repos["acme/folio"]; !there {
		t.Fatal("the repository that was there was deleted")
	}
	for _, w := range y.writes {
		if strings.HasPrefix(w, "DELETE") || strings.HasPrefix(w, "PATCH") {
			t.Fatalf("the taken repository was written: %v", y.writes)
		}
	}

	// A free name whose copy the forge refused left nothing, and says why.
	_, err = y.machine(t).Create(t.Context(), "acme", "folio-2", "", "https://github.com/hanzo-templates/folio")
	if err == nil || errors.Is(err, ErrExists) {
		t.Fatalf("a refused copy answered %v", err)
	}
	if _, there := y.repos["acme/folio-2"]; there {
		t.Fatal("a refused copy left a row")
	}
}

// A SOURCE THE MACHINE MUST NOT CLONE is refused before the forge hears of it:
// not https, carrying a credential, a local path, or this forge itself.
func TestCreateRefusesASourceItMustNotClone(t *testing.T) {
	y := newYard(t, "acme")
	c := y.machine(t)
	for _, source := range []string{
		"http://github.com/hanzo-apps/template-synapse",
		"https://x-access-token:secret@github.com/hanzo-apps/template-synapse",
		"/data/git/repositories/hanzoai/cloud.git",
		"file:///data/git/repositories/hanzoai/cloud.git",
		"https://" + c.Host() + "/hanzoai/cloud",
	} {
		if _, err := c.Create(t.Context(), "acme", "copy", "", source); err == nil {
			t.Fatalf("copied from %q", source)
		}
	}
	if len(y.writes) != 0 {
		t.Fatalf("the forge heard: %v", y.writes)
	}
}

// THE NAME IS THE ORG'S, FOLDED TO THE FORGE'S ALPHABET, and it always passes
// the path-segment rule.
func TestCandidate(t *testing.T) {
	for _, c := range []struct {
		org  string
		n    int
		want string
	}{
		{"acme", 1, "acme"},
		{"acme", 2, "acme-2"},
		{"Acme Corp", 1, "acme-corp"},
		{"a23", 3, "a23-3"},
		{"--weird..org__", 1, "weird-org"},
		{"../../hanzoai", 1, "hanzoai"},
		{"", 1, ""},
		{"...", 1, ""},
		{strings.Repeat("a", 60), 12, strings.Repeat("a", 37) + "-12"},
	} {
		got := candidate(c.org, c.n)
		if got != c.want {
			t.Errorf("candidate(%q, %d) = %q, want %q", c.org, c.n, got, c.want)
		}
		if got != "" && validSegment(got) != nil {
			t.Errorf("candidate(%q, %d) = %q is not a path segment", c.org, c.n, got)
		}
	}
}

// A MADE WORKSPACE IS BOUNDED: at capacity a create is refused before the forge
// clones anything, and a create that finds it over after racing past the first
// question takes back its own repository — never one that was there.
func TestCreateStopsAtCapacity(t *testing.T) {
	y := newYard(t, "acme")
	for i := range capacity {
		y.repos["acme/r"+strconv.Itoa(i)] = &plot{private: true, branch: "main"}
	}
	c := y.machine(t)
	for _, source := range []string{"", "https://github.com/hanzo-apps/template-synapse"} {
		_, err := c.Create(t.Context(), "acme", "one-more", "", source)
		if !errors.Is(err, ErrFull) {
			t.Fatalf("source %q: a create in a full workspace = %v, want ErrFull", source, err)
		}
	}
	if len(y.writes) != 0 || len(y.copies) != 0 {
		t.Fatalf("a full workspace was written: %v %v", y.writes, y.copies)
	}

	// One short of full, a create lands; the next is refused.
	delete(y.repos, "acme/r0")
	if _, err := c.Create(t.Context(), "acme", "last", "", ""); err != nil {
		t.Fatalf("the last free slot was refused: %v", err)
	}
	if _, err := c.Create(t.Context(), "acme", "over", "", ""); !errors.Is(err, ErrFull) {
		t.Fatalf("a create past capacity = %v", err)
	}
}

// crowd is a yard where another writer fills the workspace while a create is in
// flight: the count before the create says there is room, the count after says
// it is over.
func TestCreateOverCapacityTakesBackOnlyItsOwn(t *testing.T) {
	y := newYard(t, "acme")
	for i := range capacity - 1 {
		y.repos["acme/r"+strconv.Itoa(i)] = &plot{private: true, branch: "main"}
	}
	c := y.machine(t)
	// The racing writer's repository lands between this create's question and
	// its answer: it is made by the forge on the create itself, below.
	inner := y.Server.Config.Handler
	y.Server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/orgs/acme/repos") {
			y.mu.Lock()
			y.repos["acme/theirs"] = &plot{private: true, branch: "main"}
			y.mu.Unlock()
		}
		inner.ServeHTTP(w, r)
	})
	if _, err := c.Create(t.Context(), "acme", "mine", "", ""); !errors.Is(err, ErrFull) {
		t.Fatalf("a create that left the workspace over = %v, want ErrFull", err)
	}
	if _, left := y.repos["acme/mine"]; left {
		t.Fatal("the create that went over kept its repository")
	}
	if _, kept := y.repos["acme/theirs"]; !kept {
		t.Fatal("the other writer's repository was taken back")
	}
}

// A CUSTOMER ORG NEVER GETS A NAME THE ESTATE GOES BY. Every name the estate
// keeps — its GitHub orgs and its brands', not only this forge's tables — is
// numbered past without the forge being asked for it, and a record naming one
// is refused rather than acted in.
func TestProvisionNumbersPastEveryNameTheEstateKeeps(t *testing.T) {
	for name := range kept {
		if !Reserved(name) || !Reserved(strings.ToUpper(name)) {
			t.Fatalf("%q is kept and not reserved", name)
		}
	}
	for _, name := range []string{"acme", "hanzo-labs", "luxury", "zooey"} {
		if Reserved(name) {
			t.Fatalf("%q is reserved and belongs to nobody", name)
		}
	}
	y := newYard(t)
	book := newLedger()
	c := y.machine(t)
	for _, org := range []string{"zenlm", "hanzo-js", "luxdao", "webby-ai", "zap-proto", "Hanzo Templates", "hanzos3", "HanzoKV", "luxwallet"} {
		ws, err := Provision(t.Context(), c, book, org)
		if err != nil {
			t.Fatalf("Provision(%q): %v", org, err)
		}
		if Reserved(ws.Owner) || ws.Owner != candidate(org, 2) {
			t.Fatalf("Provision(%q) = %+v, want %q", org, ws, candidate(org, 2))
		}
		if got := y.touched(candidate(org, 1)); len(got) != 0 {
			t.Fatalf("the kept name %q was asked for: %v", candidate(org, 1), got)
		}
	}
	for _, name := range []string{"zenlm", "hanzo-templates", "bootnode"} {
		b := newLedger()
		b.m["acme"] = name
		if ws, err := Find(t.Context(), b, "acme"); err == nil {
			t.Fatalf("a record naming %q answered %+v", name, ws)
		}
	}
}

// PAST THE NUMBERED NAMES, A RANDOM ONE. A squatter holding acme through
// acme-20 does not keep acme out of a workspace: the next name is one nobody
// could have taken in advance, and none of the squatted ones is written.
func TestProvisionDrawsARandomNamePastTheNumberedOnes(t *testing.T) {
	squatted := []string{"acme"}
	for n := 2; n <= makeTries; n++ {
		squatted = append(squatted, candidate("acme", n))
	}
	y := newYard(t, squatted...)
	ws, err := Provision(t.Context(), y.machine(t), newLedger(), "acme")
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	rest, ok := strings.CutPrefix(ws.Owner, "acme-")
	if !ok || len(rest) != 8 || strings.Trim(rest, "0123456789abcdefghijklmnopqrstuvwxyz") != "" {
		t.Fatalf("Provision = %+v, want acme- and eight random characters", ws)
	}
	if validSegment(ws.Owner) != nil || y.orgs[ws.Owner] != "private" {
		t.Fatalf("the drawn namespace %q is %q", ws.Owner, y.orgs[ws.Owner])
	}
	for _, name := range squatted {
		if y.orgs[name] != "public" {
			t.Fatalf("the squatted %s changed", name)
		}
	}
}

// A FORGE THAT ANSWERS EVERY NAME AS TAKEN ENDS THE SEARCH, bounded.
func TestProvisionEndsWhenEveryNameIsTaken(t *testing.T) {
	y := newYard(t)
	var asked int
	inner := y.Server.Config.Handler
	y.Server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == API+"/orgs" {
			asked++
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = w.Write([]byte(`{"message":"user already exists"}`))
			return
		}
		inner.ServeHTTP(w, r)
	})
	if _, err := Provision(t.Context(), y.machine(t), newLedger(), "acme"); err == nil {
		t.Fatal("a forge with no free name answered a workspace")
	}
	if asked != makeTries+strays {
		t.Fatalf("asked for %d names, want %d", asked, makeTries+strays)
	}
}

// A DRAWN NAME FITS THE FORGE'S BOUND and passes the path-segment rule.
func TestStrayFitsTheForge(t *testing.T) {
	for _, org := range []string{"a", "acme", strings.Repeat("long-", 20)} {
		got, err := stray(org)
		if err != nil || len(got) > nameMax || validSegment(got) != nil || !strings.HasPrefix(got, candidate(org, 1)[:1]) {
			t.Fatalf("stray(%q) = %q, %v", org, got, err)
		}
	}
	if got, _ := stray("..."); got != "" {
		t.Fatalf("an org that folds to nothing drew %q", got)
	}
}
