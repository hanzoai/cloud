package forge

// member_test.go — a person's account on the forge and their seat in a made
// namespace, against a forge that answers as this one does: a taken name and a
// taken address are each 422 with the forge's own sentence, the forge keeps no
// login source (any named one is 422, as the forge answers since its migration
// 346) so an account is local and made only with a password, the admin search
// filters by login name (and by source only when one is named), and a team member
// is 404 until seated.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
)

type person struct {
	ID         int64  `json:"id"`
	Login      string `json:"login"`
	Email      string `json:"email"`
	Source     int64  `json:"source_id"`
	LoginName  string `json:"login_name"`
	Restricted bool   `json:"restricted"`
	Prohibited bool   `json:"prohibit_login"`
	Visibility string `json:"visibility"`
}

type crew struct {
	ID    int64          `json:"id"`
	Name  string         `json:"name"`
	All   bool           `json:"includes_all_repositories"`
	Make  bool           `json:"can_create_org_repo"`
	Perm  string         `json:"permission"`
	Units map[string]any `json:"units_map"`
	seats map[string]bool
}

// roll is a forge with people, orgs and teams.
type roll struct {
	*httptest.Server
	mu      sync.Mutex
	people  []*person
	orgs    map[string][]*crew
	writes  []string
	loose   bool // the admin search ignores its filters and answers everyone
	lax     bool // the forge drops prohibit_login on an edit
	nextID  int64
	created []map[string]any
	edited  []map[string]any
}

func newRoll(t *testing.T, orgs ...string) *roll {
	t.Helper()
	r := &roll{orgs: map[string][]*crew{}, nextID: 100}
	for _, o := range orgs {
		r.orgs[o] = nil
	}
	r.Server = httptest.NewServer(http.HandlerFunc(r.serve))
	t.Cleanup(r.Server.Close)
	return r
}

func (r *roll) machine(t *testing.T) *Client {
	t.Helper()
	c, err := New(r.URL, "tok")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c.Machine()
}

func (r *roll) find(login string) *person {
	for _, p := range r.people {
		if strings.EqualFold(p.Login, login) {
			return p
		}
	}
	return nil
}

func (r *roll) serve(w http.ResponseWriter, q *http.Request) {
	path := strings.TrimPrefix(q.URL.Path, API)
	r.mu.Lock()
	defer r.mu.Unlock()
	if q.Header.Get("Sudo") != "" {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	if q.Method != http.MethodGet {
		r.writes = append(r.writes, q.Method+" "+path)
	}
	fail := func(code int, msg string) {
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(map[string]any{"message": msg})
	}
	parts := strings.Split(strings.Trim(path, "/"), "/")
	switch {
	case path == "/admin/users" && q.Method == http.MethodGet:
		src, _ := strconv.ParseInt(q.URL.Query().Get("source_id"), 10, 64)
		name := q.URL.Query().Get("login_name")
		out := []*person{}
		for _, p := range r.people {
			if r.loose || ((src == 0 || p.Source == src) && p.LoginName == name) {
				out = append(out, p)
			}
		}
		_ = json.NewEncoder(w).Encode(out)

	case path == "/admin/users" && q.Method == http.MethodPost:
		var in map[string]any
		_ = json.NewDecoder(q.Body).Decode(&in)
		r.created = append(r.created, in)
		login, _ := in["username"].(string)
		email, _ := in["email"].(string)
		// The source first, then the password a local account needs, as the
		// forge's CreateUser asks them.
		if src, _ := in["source_id"].(float64); src != 0 {
			fail(http.StatusUnprocessableEntity, fmt.Sprintf("login source does not exist [id: %d]", int64(src)))
			return
		}
		if pw, _ := in["password"].(string); len(pw) < 8 {
			fail(http.StatusBadRequest, "PasswordIsRequired")
			return
		}
		if r.find(login) != nil || r.orgs[strings.ToLower(login)] != nil {
			fail(http.StatusUnprocessableEntity, "user already exists [name: "+login+"]")
			return
		}
		if _, isOrg := r.orgs[strings.ToLower(login)]; isOrg {
			fail(http.StatusUnprocessableEntity, "user already exists [name: "+login+"]")
			return
		}
		for _, p := range r.people {
			if strings.EqualFold(p.Email, email) {
				fail(http.StatusUnprocessableEntity, "e-mail already in use [email: "+email+"]")
				return
			}
		}
		name, _ := in["login_name"].(string)
		restricted, _ := in["restricted"].(bool)
		vis, _ := in["visibility"].(string)
		r.nextID++
		p := &person{ID: r.nextID, Login: login, Email: email, LoginName: name, Restricted: restricted, Visibility: vis}
		r.people = append(r.people, p)
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(p)

	case len(parts) == 3 && parts[0] == "admin" && parts[1] == "users" && q.Method == http.MethodPatch:
		p := r.find(parts[2])
		if p == nil {
			fail(http.StatusNotFound, "no user")
			return
		}
		var in map[string]any
		_ = json.NewDecoder(q.Body).Decode(&in)
		r.edited = append(r.edited, in)
		// The forge's EditUser requires a login name and refuses a source it does not hold.
		if name, _ := in["login_name"].(string); name == "" {
			fail(http.StatusUnprocessableEntity, "LoginName: Required")
			return
		}
		if src, _ := in["source_id"].(float64); src != 0 {
			fail(http.StatusUnprocessableEntity, fmt.Sprintf("login source does not exist [id: %d]", int64(src)))
			return
		}
		if v, ok := in["prohibit_login"].(bool); ok && !r.lax {
			p.Prohibited = v
		}
		if v, ok := in["restricted"].(bool); ok {
			p.Restricted = v
		}
		if v, ok := in["email"].(string); ok && v != "" {
			p.Email = v
		}
		_ = json.NewEncoder(w).Encode(p)

	case len(parts) == 3 && parts[0] == "admin" && parts[1] == "users" && q.Method == http.MethodDelete:
		for i, p := range r.people {
			if strings.EqualFold(p.Login, parts[2]) {
				r.people = append(r.people[:i], r.people[i+1:]...)
				break
			}
		}
		w.WriteHeader(http.StatusNoContent)

	case len(parts) == 2 && parts[0] == "users" && q.Method == http.MethodGet:
		p := r.find(parts[1])
		if p == nil {
			fail(http.StatusNotFound, "no user")
			return
		}
		_ = json.NewEncoder(w).Encode(p)

	case len(parts) == 3 && parts[0] == "orgs" && parts[2] == "teams" && q.Method == http.MethodGet:
		teams, ok := r.orgs[parts[1]]
		if !ok {
			fail(http.StatusNotFound, "no org")
			return
		}
		if teams == nil {
			teams = []*crew{}
		}
		_ = json.NewEncoder(w).Encode(teams)

	case len(parts) == 3 && parts[0] == "orgs" && parts[2] == "teams" && q.Method == http.MethodPost:
		if _, ok := r.orgs[parts[1]]; !ok {
			fail(http.StatusNotFound, "no org")
			return
		}
		var in crew
		_ = json.NewDecoder(q.Body).Decode(&in)
		for _, t := range r.orgs[parts[1]] {
			if strings.EqualFold(t.Name, in.Name) {
				fail(http.StatusUnprocessableEntity, "team already exists")
				return
			}
		}
		r.nextID++
		in.ID, in.seats = r.nextID, map[string]bool{}
		r.orgs[parts[1]] = append(r.orgs[parts[1]], &in)
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(in)

	case len(parts) == 4 && parts[0] == "teams" && parts[2] == "members":
		id, _ := strconv.ParseInt(parts[1], 10, 64)
		var t *crew
		for _, teams := range r.orgs {
			for _, x := range teams {
				if x.ID == id {
					t = x
				}
			}
		}
		if t == nil || r.find(parts[3]) == nil {
			fail(http.StatusNotFound, "no team or user")
			return
		}
		switch q.Method {
		case http.MethodGet:
			if !t.seats[strings.ToLower(parts[3])] {
				fail(http.StatusNotFound, "not a member")
				return
			}
			_ = json.NewEncoder(w).Encode(r.find(parts[3]))
		case http.MethodPut:
			t.seats[strings.ToLower(parts[3])] = true
			w.WriteHeader(http.StatusNoContent)
		}

	default:
		fail(http.StatusNotFound, "unrouted "+q.Method+" "+path)
	}
}

func (r *roll) wrote() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.writes...)
}

var josh = Person{Subject: "8f1c-josh", Address: "josh@webby.example", Name: "Josh"}

// AN ACCOUNT TIED TO THE PERSON'S IAM SUBJECT: made by the machine under the
// login the forge's own sign-in would derive, bound to the login source and the
// subject, restricted and private, with nothing to change at a first sign-in —
// then prohibited from signing in, and seated in the namespace's members team,
// which reads code and writes issues across every repository and creates none.
func TestEnrollMakesAnAccountTiedToTheSubject(t *testing.T) {
	r := newRoll(t, "webby-ai")
	got, err := r.machine(t).Enroll(context.Background(), "webby-ai", josh)
	if err != nil {
		t.Fatalf("Enroll: %v", err)
	}
	if got != (Enrolled{Login: "seat-josh", Made: true, Seated: true}) {
		t.Fatalf("enrolled = %+v", got)
	}
	p := r.find("seat-josh")
	if p == nil || p.Source != 0 || p.LoginName != "seat:"+josh.Subject || p.Email != r.machine(t).seatAddress(josh.Subject) ||
		!p.Restricted || !p.Prohibited || p.Visibility != "private" {
		t.Fatalf("account = %+v", p)
	}
	// The seat's own address, under the forge's no-reply domain: never the person's.
	host, _, _ := strings.Cut(strings.TrimPrefix(r.URL, "http://"), ":")
	if p.Email == josh.Address || !strings.HasPrefix(p.Email, "seat-") || !strings.HasSuffix(p.Email, "@noreply."+host) {
		t.Fatalf("seat address = %q", p.Email)
	}
	made := r.created[0]
	if pw, _ := made["password"].(string); made["must_change_password"] != false || made["send_notify"] != false || len(pw) < 40 {
		t.Fatalf("create body = %v", made)
	}
	teams := r.orgs["webby-ai"]
	if len(teams) != 1 {
		t.Fatalf("teams = %+v", teams)
	}
	team := teams[0]
	if team.Name != "members" || !team.All || team.Make || team.Perm != "read" ||
		team.Units["repo.issues"] != "write" || team.Units["repo.code"] != "read" || !team.seats["seat-josh"] {
		t.Fatalf("team = %+v", team)
	}
}

// ENROLLING AGAIN CHANGES NOTHING: the account is found by its subject and the
// seat is already taken.
func TestEnrollIsIdempotent(t *testing.T) {
	r := newRoll(t, "webby-ai")
	m := r.machine(t)
	if _, err := m.Enroll(context.Background(), "webby-ai", josh); err != nil {
		t.Fatal(err)
	}
	before := len(r.wrote())
	got, err := m.Enroll(context.Background(), "webby-ai", josh)
	if err != nil {
		t.Fatal(err)
	}
	if got != (Enrolled{Login: "seat-josh"}) {
		t.Fatalf("second enrol = %+v", got)
	}
	if w := r.wrote(); len(w) != before {
		t.Fatalf("a second enrol wrote %v", w[before:])
	}
}

// A SECOND NAMESPACE IS A SECOND SEAT on the one account.
func TestEnrollSeatsTheSameAccountInAnotherNamespace(t *testing.T) {
	r := newRoll(t, "webby-ai", "globex")
	m := r.machine(t)
	if _, err := m.Enroll(context.Background(), "webby-ai", josh); err != nil {
		t.Fatal(err)
	}
	got, err := m.Enroll(context.Background(), "globex", josh)
	if err != nil {
		t.Fatal(err)
	}
	if got != (Enrolled{Login: "seat-josh", Seated: true}) || len(r.people) != 1 {
		t.Fatalf("second namespace = %+v, people %d", got, len(r.people))
	}
}

// A LOGIN SOMEBODY HOLDS IS NUMBERED PAST — never taken over — and a seat's
// login is never one a person's own sign-in derives, so it cannot take a name
// somebody will need later.
func TestEnrollNumbersPastATakenLogin(t *testing.T) {
	r := newRoll(t, "webby-ai")
	r.people = append(r.people, &person{ID: 1, Login: "seat-josh", Email: "josh@elsewhere.example", LoginName: "seat:someone-else"})
	got, err := r.machine(t).Enroll(context.Background(), "webby-ai", josh)
	if err != nil || got.Login != "seat-josh-2" {
		t.Fatalf("enrolled %+v, %v", got, err)
	}
	if p := r.find("seat-josh"); p.LoginName != "seat:someone-else" || p.Prohibited {
		t.Fatalf("the holder of seat-josh was touched: %+v", p)
	}
	a, err := r.machine(t).Enroll(context.Background(), "webby-ai", Person{Subject: "s-2", Address: "a@x.example"})
	if err != nil || a.Login != "seat-a" {
		t.Fatalf("a short address: %+v, %v", a, err)
	}
	for _, c := range r.created {
		if c["username"] == "a" || c["username"] == "josh" {
			t.Fatalf("a login a sign-in derives was asked for: %v", c["username"])
		}
	}
}

// THE FORGE KEEPS NO LOGIN SOURCE, so none is named: an account is a local one,
// made with a password nobody holds, and bound by its login name alone. Naming
// the source it used to keep was refused ("login source does not exist [id: 1]"),
// and no seat was ever made. Every account made and every edit names none, and two
// accounts never share a password.
func TestEnrollAndProvisionNameNoLoginSource(t *testing.T) {
	r := newRoll(t, "webby-ai")
	m := r.machine(t)
	if _, err := m.Enroll(context.Background(), "webby-ai", josh); err != nil {
		t.Fatalf("Enroll: %v", err)
	}
	if _, _, err := m.Provision(context.Background(), Person{Subject: "s-2", Address: "ann@webby.example", Name: "Ann"}); err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if len(r.created) != 2 || len(r.edited) == 0 {
		t.Fatalf("made %d, edited %d", len(r.created), len(r.edited))
	}
	for _, body := range append(append([]map[string]any(nil), r.created...), r.edited...) {
		if _, named := body["source_id"]; named {
			t.Fatalf("a request named a login source: %v", body)
		}
	}
	if r.created[0]["password"] == r.created[1]["password"] {
		t.Fatal("two accounts were made with one password")
	}
}

// A SEAT MADE WHILE THE FORGE STILL KEPT A LOGIN SOURCE IS THE SAME SEAT: its
// login name is the binding, so it is found, kept and seated, and nothing is made.
func TestASeatMadeBeforeTheSourceWentIsStillTheSeat(t *testing.T) {
	r := newRoll(t, "webby-ai")
	r.people = append(r.people, &person{ID: 1, Login: "seat-josh", Email: josh.Address, Source: 1, LoginName: "seat:" + josh.Subject, Restricted: true, Prohibited: true})
	got, err := r.machine(t).Enroll(context.Background(), "webby-ai", josh)
	if err != nil || got != (Enrolled{Login: "seat-josh", Seated: true}) {
		t.Fatalf("enrolled %+v, %v", got, err)
	}
	if len(r.created) != 0 || len(r.people) != 1 {
		t.Fatalf("made %v", r.created)
	}
}

// AN ACCOUNT UNDER A SEAT BINDING THAT CAN SIGN IN IS CLOSED BEFORE IT IS
// SEATED, and one the forge will not close is never seated.
func TestEnrollClosesAnOpenSeat(t *testing.T) {
	r := newRoll(t, "webby-ai")
	r.people = append(r.people, &person{ID: 1, Login: "seat-josh", Email: josh.Address, LoginName: "seat:" + josh.Subject, Restricted: true})
	got, err := r.machine(t).Enroll(context.Background(), "webby-ai", josh)
	if err != nil || got != (Enrolled{Login: "seat-josh", Seated: true}) || !r.find("seat-josh").Prohibited {
		t.Fatalf("enrolled %+v, %v, %+v", got, err, r.find("seat-josh"))
	}

	r = newRoll(t, "webby-ai")
	r.lax = true
	r.people = append(r.people, &person{ID: 1, Login: "seat-josh", Email: josh.Address, LoginName: "seat:" + josh.Subject, Restricted: true})
	if _, err := r.machine(t).Enroll(context.Background(), "webby-ai", josh); !errors.Is(err, ErrSignsIn) {
		t.Fatalf("err = %v", err)
	}
	if len(r.orgs["webby-ai"]) != 0 || r.find("seat-josh") == nil {
		t.Fatal("an open account was seated, or deleted")
	}
}

// A PERSON'S OWN SIGN-IN ACCOUNT IS NOT A SEAT, AND DOES NOT STAND IN ITS WAY: an
// account bound to the bare subject (the forge's sign-in, or Provision) is not
// found as the seat, and the seat is made beside it under its own binding and its
// own address. It used to take the person's address, which their own account
// already held, so nobody with a forge account of their own could be seated.
// Their own account is left as it was: it still signs in, and is seated nowhere.
func TestASignInAccountIsNotASeat(t *testing.T) {
	r := newRoll(t, "webby-ai")
	r.people = append(r.people, &person{ID: 1, Login: "josh", Email: josh.Address, LoginName: josh.Subject})
	if login, err := r.machine(t).Account(context.Background(), josh.Subject); err != nil || login != "" {
		t.Fatalf("Account = %q, %v", login, err)
	}
	got, err := r.machine(t).Enroll(context.Background(), "webby-ai", josh)
	if err != nil || got != (Enrolled{Login: "seat-josh", Made: true, Seated: true}) {
		t.Fatalf("enrolled %+v, %v", got, err)
	}
	own := r.find("josh")
	if own.Email != josh.Address || own.LoginName != josh.Subject || own.Prohibited || r.orgs["webby-ai"][0].seats["josh"] {
		t.Fatalf("the person's own account was touched: %+v", own)
	}
}

// AN ADDRESS ANOTHER ACCOUNT HOLDS IS NEVER ADOPTED, and is not the seat's: the
// seat is made under its own address, and the account holding the person's is
// left alone.
func TestEnrollNeverAdoptsAnAddress(t *testing.T) {
	r := newRoll(t, "webby-ai")
	r.people = append(r.people, &person{ID: 1, Login: "jj", Email: josh.Address})
	got, err := r.machine(t).Enroll(context.Background(), "webby-ai", josh)
	if err != nil || got.Login != "seat-josh" {
		t.Fatalf("enrolled %+v, %v", got, err)
	}
	if jj := r.find("jj"); jj.Email != josh.Address || jj.LoginName != "" || jj.Prohibited || r.orgs["webby-ai"][0].seats["jj"] {
		t.Fatalf("the account holding the address was touched: %+v", jj)
	}
}

// THE SEAT'S OWN ADDRESS HELD BY AN ACCOUNT THAT IS NOT THE SEAT IS REFUSED: that
// account is never adopted, and nothing is made or seated.
func TestASeatsAddressHeldElsewhereIsRefused(t *testing.T) {
	r := newRoll(t, "webby-ai")
	m := r.machine(t)
	r.people = append(r.people, &person{ID: 1, Login: "squat", Email: m.seatAddress(josh.Subject)})
	if _, err := m.Enroll(context.Background(), "webby-ai", josh); !errors.Is(err, ErrAddressHeld) {
		t.Fatalf("err = %v", err)
	}
	if len(r.people) != 1 || len(r.orgs["webby-ai"]) != 0 {
		t.Fatalf("something was made: people %d teams %d", len(r.people), len(r.orgs["webby-ai"]))
	}
}

// A SEAT'S ADDRESS IS ITS SUBJECT'S, the same every time, and nobody else's.
func TestASeatsAddressIsItsOwn(t *testing.T) {
	m := newRoll(t).machine(t)
	a, b := m.seatAddress(josh.Subject), m.seatAddress(" "+josh.Subject+" ")
	if a != b || a == m.seatAddress("s-2") || strings.Contains(a, josh.Subject) {
		t.Fatalf("addresses %q, %q", a, m.seatAddress("s-2"))
	}
}

// AN ACCOUNT THAT DOES NOT COME BACK PROHIBITED IS TAKEN BACK and the enrol
// fails: an account the person could sign in to directly would outlive their
// membership of the org.
func TestEnrollTakesBackAnAccountThatCanSignIn(t *testing.T) {
	r := newRoll(t, "webby-ai")
	r.lax = true
	if _, err := r.machine(t).Enroll(context.Background(), "webby-ai", josh); err == nil {
		t.Fatal("an account that can sign in was kept")
	}
	if len(r.people) != 0 {
		t.Fatalf("people left: %+v", r.people[0])
	}
}

// THE BINDING IS READ EXACTLY: a search that answers people it was not asked
// for names nobody.
func TestAccountReadsOnlyAnExactBinding(t *testing.T) {
	r := newRoll(t)
	r.people = append(r.people, &person{ID: 1, Login: "z", Email: "z@hanzo.example", Source: 0})
	r.loose = true
	login, err := r.machine(t).Account(context.Background(), josh.Subject)
	if err != nil || login != "" {
		t.Fatalf("Account = %q, %v", login, err)
	}
	r.people = append(r.people, &person{ID: 2, Login: "seat-josh", Email: josh.Address, LoginName: "seat:" + josh.Subject})
	if login, err = r.machine(t).Account(context.Background(), josh.Subject); err != nil || login != "seat-josh" {
		t.Fatalf("Account = %q, %v", login, err)
	}
}

// ONLY THE MACHINE ENROLS, only a person with a subject and an address is
// enrolled, and only into a namespace that is a path segment.
func TestEnrollRefusals(t *testing.T) {
	r := newRoll(t, "webby-ai")
	m := r.machine(t)
	for name, call := range map[string]func() error{
		"sudoed": func() error { _, err := m.As("z").Enroll(context.Background(), "webby-ai", josh); return err },
		"no subject": func() error {
			_, err := m.Enroll(context.Background(), "webby-ai", Person{Address: "a@b.example"})
			return err
		},
		"no address": func() error { _, err := m.Enroll(context.Background(), "webby-ai", Person{Subject: "s"}); return err },
		"bad owner":  func() error { _, err := m.Enroll(context.Background(), "../hanzoai", josh); return err },
		"lookup":     func() error { _, err := m.As("z").Account(context.Background(), "s"); return err },
	} {
		if call() == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if w := r.wrote(); len(w) != 0 {
		t.Fatalf("a refusal wrote %v", w)
	}
}

// AN IMPORTED REPOSITORY'S NAME is the upstream account and name, folded,
// joined by '_'.
func TestImported(t *testing.T) {
	for _, c := range [][3]string{{"webby-ai", "intel-hub", "webby-ai_intel-hub"}, {"Hanzo-Apps", "AI", "hanzo-apps_ai"}, {"", "x", "_x"}} {
		if got := Imported(c[0], c[1]); got != c[2] {
			t.Errorf("Imported(%q, %q) = %q, want %q", c[0], c[1], got, c[2])
		}
	}
	_ = fmt.Sprint
}

func TestProvision(t *testing.T) {
	r := newRoll(t)
	m := r.machine(t)
	ctx := context.Background()

	// Provision creates an account that can sign in (no prohibit_login)
	login, made, err := m.Provision(ctx, josh)
	if err != nil || !made || login != "josh" {
		t.Fatalf("Provision = %q, %v, %v", login, made, err)
	}
	p := r.find(login)
	if p == nil || p.LoginName != josh.Subject || p.Prohibited || !p.Restricted || p.Source != 0 {
		t.Fatalf("bad provisioned user: %+v", p)
	}

	// Idempotent: second call returns existing login and made == false
	login2, made2, err := m.Provision(ctx, josh)
	if err != nil || made2 || login2 != login {
		t.Fatalf("second Provision = %q, %v, %v", login2, made2, err)
	}

	// Refuses non-machine client
	if _, _, err := m.As("z").Provision(ctx, josh); err == nil {
		t.Fatal("non-machine Provision accepted")
	}
}

func TestAdmit(t *testing.T) {
	r := newRoll(t, "hanzoai")
	m := r.machine(t)
	ctx := context.Background()

	login, _, err := m.Provision(ctx, josh)
	if err != nil {
		t.Fatalf("Provision = %v", err)
	}

	// Admit into hanzo (which maps to hanzoai / members)
	ok, err := m.Admit(ctx, "hanzo", login)
	if err != nil || !ok {
		t.Fatalf("Admit = %v, %v", ok, err)
	}

	// Idempotent: second admit succeeds but returns ok == false (already seated)
	ok2, err := m.Admit(ctx, "hanzo", login)
	if err != nil || ok2 {
		t.Fatalf("second Admit = %v, %v", ok2, err)
	}

	// Refuses unmapped org
	if _, err := m.Admit(ctx, "unknown-org", login); err == nil {
		t.Fatal("unmapped org Admit accepted")
	}

	// Refuses non-machine client
	if _, err := m.As("z").Admit(ctx, "hanzo", login); err == nil {
		t.Fatal("non-machine Admit accepted")
	}
}

// A seat created before #725 holds the person's real email address. Re-enrolling
// moves it onto its synthetic noreply address so interactive IAM sign-in can
// claim the real address (#965), while keeping the seat prohibited and restricted.
func TestEnrollMigratesPre725SeatOffRealEmail(t *testing.T) {
	r := newRoll(t, "webby-ai")
	r.people = append(r.people, &person{
		ID:         1,
		Login:      "seat-josh",
		Email:      josh.Address,
		LoginName:  "seat:" + josh.Subject,
		Restricted: true,
		Prohibited: true,
	})
	m := r.machine(t)
	got, err := m.Enroll(context.Background(), "webby-ai", josh)
	if err != nil {
		t.Fatalf("Enroll = %v", err)
	}
	if got.Login != "seat-josh" || got.Made {
		t.Fatalf("Enroll = %+v, want existing seat-josh", got)
	}
	p := r.find("seat-josh")
	if p == nil || !p.Prohibited || !p.Restricted {
		t.Fatalf("seat state = %+v, want prohibited & restricted", p)
	}
	if strings.EqualFold(p.Email, josh.Address) || !strings.HasPrefix(p.Email, "seat-") {
		t.Fatalf("seat email = %q, want synthetic seat-* address", p.Email)
	}
}
