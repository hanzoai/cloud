package forge

// member.go — a person's account on the forge, and their seat in the namespace
// the platform made for their org ([Workspace]).
//
// The account is the person's forge identity, bound to their IAM identity: its
// login name is the IAM subject behind [seat], which is how [Client.Account]
// finds it. The forge keeps no login source ([throwaway] says why), so the login
// name is the whole binding, as it is for the forge itself. Nothing reads a name
// or an address to decide who an account is.
//
// It is made restricted and PROHIBITED FROM SIGNING IN, and its binding is one
// neither of the forge's own ways in resolves: its sign-in through IAM matches
// the bare subject, and an IAM bearer resolves only through a link that sign-in
// writes. A person reaches their org's code through Hanzo, which acts for them
// (Sudo) only inside the namespace recorded for the org their token validates;
// an account that could also be reached directly would keep its seat after IAM
// removed the person from the org. An account found under a seat binding that
// can sign in is closed before it is seated, and one that will not close is
// refused ([ErrSignsIn]).
//
// The seat is a place in the namespace's members team ([team]): read the code,
// write the issues, on every repository, creating none. That is what the
// forge's ACL reads when cloud acts as the person — the board's second control,
// beside the validated org.

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
)

// throwaway is the password an account is made with, which nobody ever holds.
//
// The forge keeps NO login source: migration 346 deleted them, and Hanzo IAM is
// configured in its env instead (universe charts/app/values/hanzo/hanzo-git.yaml).
// An IAM subject names the account a sign-in linked, else the one account whose
// login_name it is (the forge's services/auth/iam.go), so an account is bound by
// its login name alone. It used to be bound by the pair (login source 1, login
// name), and once the source was gone the forge refused every account made with it
// — "login source does not exist [id: 1]" — so no seat was made and no board read.
//
// An account with no source is a local one, and the forge makes a local one only
// with a password that passes its length, complexity and breach checks. Nobody
// signs in with it: a seat is prohibited from signing in, and a provisioned account
// signs in through IAM. So it is random, and thrown away.
func throwaway() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("forge: make an account's password: %w", err)
	}
	// One of each class the forge's complexity check may ask for.
	return base64.RawURLEncoding.EncodeToString(b[:]) + "aA1!", nil
}

// team is the members team of a made namespace.
const team = "members"

// seat prefixes the subject in a seat's login name, and the derived login in its
// username: the first keeps the forge's own sign-in from ever resolving to it, and
// the second keeps a seat from taking a name a person's own sign-in derives.
const (
	seat      = "seat:"
	seatLogin = "seat-"
)

// teamUnits is what a seat may do in every repository of the namespace.
var teamUnits = map[string]string{
	"repo.code":     "read",
	"repo.issues":   "write",
	"repo.pulls":    "read",
	"repo.releases": "read",
	"repo.wiki":     "read",
	"repo.projects": "read",
}

// Person is who an account is made for, as IAM holds them.
type Person struct {
	// Subject is the IAM subject (principal.SubjectFrom), and the binding.
	Subject string
	// Address is the address IAM holds for them, which the caller has confirmed
	// is verified.
	Address string
	// Name is their display name, shown beside what they file.
	Name string
}

// Enrolled is a person's place on the forge after [Client.Enroll].
type Enrolled struct {
	Login string
	// Made says this call created the account.
	Made bool
	// Seated says this call took the seat.
	Seated bool
}

// ErrAddressHeld is an address the forge already holds for an account bound to
// another sign-in. That account is never adopted. A seat is made with its own
// address ([Client.seatAddress]), so a seat meets this only when an account that
// is not that seat holds its address, which only an administrator's hand makes.
var ErrAddressHeld = errors.New("forge: this address belongs to a forge account tied to another sign-in")

// seatAddress is the address a seat's account is made with: its own, under the
// forge's no-reply domain (noreply.<host>, the forge's own NO_REPLY_ADDRESS
// default), and never the person's.
//
// A seat used to take the person's address, and the forge holds an address on one
// account only. A person with a forge account of their own — the one a sign-in on
// the forge makes, or Provision — held it there, so their seat could not be made
// ("your email address belongs to another sign-in on the code forge") and nobody
// with an account of their own could read a board. A seat is never signed into,
// is made with send_notify off and pushes nothing, so the address is only what the
// forge asks every account to have; a person's own account keeps theirs.
//
// It is a hash of the subject, so it is the same on every try and in every
// process — a racing enrol of the same person still meets its own seat — and it
// names nobody's address.
func (c *Client) seatAddress(subject string) string {
	host, _, _ := strings.Cut(c.host, ":")
	sum := sha256.Sum256([]byte(strings.TrimSpace(subject)))
	return seatLogin + fmt.Sprintf("%x", sum[:12]) + "@noreply." + host
}

// ErrSignsIn is an account under a seat binding that can sign in or is not
// restricted, which no seat may be.
var ErrSignsIn = errors.New("forge: the account bound to this seat can sign in")

// account is a forge user as the admin API answers it.
type account struct {
	ID         int64  `json:"id"`
	Login      string `json:"login"`
	Email      string `json:"email"`
	LoginName  string `json:"login_name"`
	Restricted bool   `json:"restricted"`
	Prohibited bool   `json:"prohibit_login"`
}

// accountByLoginName finds the login of the forge account with loginName, or "" when
// there is none.
func (c *Client) accountByLoginName(ctx context.Context, loginName string) (string, error) {
	if !c.machine {
		return "", fmt.Errorf("forge: an account is looked up by the machine identity, not %q", c.actor)
	}
	loginName = strings.TrimSpace(loginName)
	if loginName == "" {
		return "", errors.New("forge: no login name to look up")
	}
	// By login name alone: the forge keeps no login source to filter by (throwaway).
	q := url.Values{
		"login_name": {loginName},
		"limit":      {"50"},
	}
	var rows []account
	if err := c.do(ctx, "/admin/users", q, &rows); err != nil {
		return "", fmt.Errorf("forge: find the account of %s: %w", loginName, err)
	}
	login := ""
	for _, u := range rows {
		if u.LoginName != loginName || u.Login == "" {
			continue
		}
		if login != "" {
			return "", fmt.Errorf("forge: two accounts are bound to %s", loginName)
		}
		login = u.Login
	}
	return login, nil
}

// Account is the login of the forge account seated for an IAM subject, "" when
// there is none. The search filters by the binding and every row it answers is
// checked against it, so a search that ignored its filters names nobody.
func (c *Client) Account(ctx context.Context, subject string) (string, error) {
	return c.accountByLoginName(ctx, seat+strings.TrimSpace(subject))
}

// Provision gives p an account bound to their IAM subject that can sign in
// (login_name: p.Subject, restricted: true, and no prohibit_login): the forge's IAM
// sign-in resolves the subject to it by that login name.
// If an account already exists for p.Subject, it is returned with made=false.
// On "e-mail already in use", it returns the existing account without error.
func (c *Client) Provision(ctx context.Context, p Person) (string, bool, error) {
	if !c.machine {
		return "", false, fmt.Errorf("forge: a person is provisioned by the machine identity, not %q", c.actor)
	}
	p.Subject, p.Address, p.Name = strings.TrimSpace(p.Subject), strings.TrimSpace(p.Address), strings.TrimSpace(p.Name)
	if p.Subject == "" || p.Address == "" {
		return "", false, errors.New("forge: a person is provisioned with a subject and a verified address")
	}
	mu := personLock(p.Subject)
	mu.Lock()
	defer mu.Unlock()

	login, err := c.accountByLoginName(ctx, p.Subject)
	if err != nil {
		return "", false, err
	}
	if login != "" {
		return login, false, nil
	}

	derived := Login(p.Address)
	if derived == "" {
		return "", false, fmt.Errorf("forge: no login can be derived from %q", p.Address)
	}
	pw, err := throwaway()
	if err != nil {
		return "", false, err
	}
	for n := 1; n <= makeTries; n++ {
		name := numbered(derived, n)
		if name == "" || Reserved(name) || validSegment(name) != nil {
			continue
		}
		err := c.send(ctx, sendOpts{
			method: http.MethodPost,
			path:   "/admin/users",
			body: map[string]any{
				"username":             name,
				"email":                p.Address,
				"full_name":            p.Name,
				"password":             pw,
				"login_name":           p.Subject,
				"must_change_password": false,
				"send_notify":          false,
				"restricted":           true,
				"visibility":           "private",
			},
		})
		switch {
		case err == nil:
			return name, true, nil
		case errors.Is(err, ErrInvalid) && strings.Contains(err.Error(), "e-mail already in use"):
			// The person already has a forge account, normally one linked by an earlier sign-in.
			if ex, aerr := c.accountByLoginName(ctx, p.Subject); aerr == nil && ex != "" {
				return ex, false, nil
			}
			var got account
			if aerr := c.do(ctx, "/users/"+url.PathEscape(derived), nil, &got); aerr == nil && got.Login != "" {
				return got.Login, false, nil
			}
			return derived, false, nil
		case errors.Is(err, ErrExists), errors.Is(err, ErrInvalid) && taken(err):
			continue
		default:
			return "", false, fmt.Errorf("forge: provision the account of %s: %w", p.Subject, err)
		}
	}
	return "", false, fmt.Errorf("forge: no free login for %q in %d tries", p.Address, makeTries)
}


// Enroll gives p an account bound to their subject, made when they have none,
// and a seat in owner's members team. Both halves are idempotent.
//
// owner is a namespace the platform made for the person's validated org; the
// caller resolves it from the org's record, never from anything the person sent.
func (c *Client) Enroll(ctx context.Context, owner string, p Person) (Enrolled, error) {
	if !c.machine {
		return Enrolled{}, fmt.Errorf("forge: a person is enrolled by the machine identity, not %q", c.actor)
	}
	if err := validSegment(owner); err != nil {
		return Enrolled{}, err
	}
	p.Subject, p.Address, p.Name = strings.TrimSpace(p.Subject), strings.TrimSpace(p.Address), strings.TrimSpace(p.Name)
	if p.Subject == "" || p.Address == "" {
		return Enrolled{}, errors.New("forge: a person is enrolled with a subject and a verified address")
	}
	mu := personLock(p.Subject)
	mu.Lock()
	defer mu.Unlock()
	login, err := c.Account(ctx, p.Subject)
	if err != nil {
		return Enrolled{}, err
	}
	out := Enrolled{Login: login}
	if login != "" {
		err := c.sealed(ctx, login, p)
		if errors.Is(err, ErrSignsIn) {
			// Only the platform writes a seat binding, so an open one is a seat
			// whose closing was cut short: it is closed now.
			err = c.shut(ctx, login, p)
		}
		if err != nil {
			return Enrolled{}, err
		}
		// Pre-#725 seats were created with the person's real email address, which
		// blocks them from signing into the forge interactively (hanzo-inc/cloud#965).
		// Move any such seat onto its synthetic noreply address now.
		if err := c.freeSeatEmail(ctx, login, p); err != nil {
			return Enrolled{}, err
		}
	} else {
		if out.Login, err = c.register(ctx, p); err != nil {
			return Enrolled{}, err
		}
		if out.Login == "" {
			return Enrolled{}, fmt.Errorf("forge: no account was made for %s", p.Subject)
		}
		out.Made = true
	}
	id, err := c.members(ctx, owner)
	if err != nil {
		return Enrolled{}, err
	}
	if out.Seated, err = c.seat(ctx, id, owner, out.Login); err != nil {
		return Enrolled{}, err
	}
	return out, nil
}

// register makes p's account under the first free login — [seatLogin] and the
// login the forge's own sign-in derives from the address, then `-2`, `-3`, … —
// skipping every name the estate keeps. Its address is the seat's own
// ([Client.seatAddress]), never p's.
func (c *Client) register(ctx context.Context, p Person) (string, error) {
	derived := Login(p.Address)
	if derived == "" {
		return "", fmt.Errorf("forge: no login can be derived from %q", p.Address)
	}
	base := seatLogin + derived
	address := c.seatAddress(p.Subject)
	pw, err := throwaway()
	if err != nil {
		return "", err
	}
	for n := 1; n <= makeTries; n++ {
		name := numbered(base, n)
		if name == "" || Reserved(name) || validSegment(name) != nil {
			continue
		}
		err := c.send(ctx, sendOpts{
			method: http.MethodPost,
			path:   "/admin/users",
			body: map[string]any{
				"username":             name,
				"email":                address,
				"full_name":            p.Name,
				"password":             pw,
				"login_name":           seat + p.Subject,
				"must_change_password": false,
				"send_notify":          false,
				"restricted":           true,
				"visibility":           "private",
			},
		})
		switch {
		case err == nil:
			return name, c.close(ctx, name, p)
		case errors.Is(err, ErrInvalid) && strings.Contains(err.Error(), "e-mail already in use"):
			// The seat's address is held. A racing enrol of the same person in another
			// process holds it under their own seat; anyone else's is refused.
			if login, aerr := c.Account(ctx, p.Subject); aerr == nil && login != "" {
				return login, c.sealed(ctx, login, p)
			}
			return "", fmt.Errorf("%w: %s", ErrAddressHeld, address)
		case errors.Is(err, ErrExists), errors.Is(err, ErrInvalid) && taken(err):
			continue // the name is somebody's, or one the forge does not allow
		default:
			return "", fmt.Errorf("forge: make the account of %s: %w", p.Subject, err)
		}
	}
	return "", fmt.Errorf("forge: no free login for %q in %d tries", p.Address, makeTries)
}

// taken reports whether a refused create named a login the forge will not give:
// held, reserved, or outside its alphabet. Any other refusal — a login source
// the forge does not hold, an address it will not take — is the answer.
func taken(err error) bool {
	m := err.Error()
	return strings.Contains(m, "user already exists") || strings.Contains(m, "name is reserved") ||
		strings.Contains(m, "name pattern") || strings.Contains(m, "name is invalid")
}

// sealed confirms the account under p's seat is bound to it, restricted and
// prohibited from signing in. The login name is the binding (throwaway); a seat made
// while the forge still kept a login source is the same seat.
func (c *Client) sealed(ctx context.Context, login string, p Person) error {
	var got account
	if err := c.do(ctx, "/users/"+url.PathEscape(login), nil, &got); err != nil {
		return fmt.Errorf("forge: read the account %s: %w", login, err)
	}
	switch {
	case got.LoginName != seat+p.Subject:
		return fmt.Errorf("forge: %s is bound as %q", login, got.LoginName)
	case !got.Prohibited || !got.Restricted:
		return fmt.Errorf("%w: %s", ErrSignsIn, login)
	}
	return nil
}

// freeSeatEmail moves a pre-#725 seat that still holds the person's real email
// onto its synthetic noreply address so interactive IAM sign-in can claim the
// real address (#965). Seats already on their synthetic address are untouched.
func (c *Client) freeSeatEmail(ctx context.Context, login string, p Person) error {
	want := c.seatAddress(p.Subject)
	var got account
	if err := c.do(ctx, "/users/"+url.PathEscape(login), nil, &got); err != nil {
		return fmt.Errorf("forge: read the account %s: %w", login, err)
	}
	if strings.EqualFold(got.Email, want) {
		return nil
	}
	if err := c.send(ctx, sendOpts{
		method: http.MethodPatch,
		path:   "/admin/users/" + url.PathEscape(login),
		body: map[string]any{
			"login_name":     seat + p.Subject,
			"email":          want,
			"prohibit_login": true,
			"restricted":     true,
		},
	}); err != nil {
		return fmt.Errorf("forge: move seat %s off %q: %w", login, got.Email, err)
	}
	return nil
}

// shut prohibits sign-in on login's account and reads the binding back.
func (c *Client) shut(ctx context.Context, login string, p Person) error {
	err := c.send(ctx, sendOpts{
		method: http.MethodPatch,
		path:   "/admin/users/" + url.PathEscape(login),
		body: map[string]any{
			"login_name":     seat + p.Subject,
			"prohibit_login": true,
			"restricted":     true,
		},
	})
	if err != nil {
		return fmt.Errorf("forge: close the account %s: %w", login, err)
	}
	return c.sealed(ctx, login, p)
}

// close shuts the account register just made. One that does not shut is deleted.
func (c *Client) close(ctx context.Context, login string, p Person) error {
	err := c.shut(ctx, login, p)
	if err != nil {
		_ = c.send(context.WithoutCancel(ctx), sendOpts{method: http.MethodDelete, path: "/admin/users/" + url.PathEscape(login)})
		return fmt.Errorf("forge: close the account %s: %w", login, err)
	}
	return nil
}

// teamNamed finds team with name in owner, or makes it if owner has none.
func (c *Client) teamNamed(ctx context.Context, owner, name, permission string, units map[string]string) (int64, error) {
	path := "/orgs/" + url.PathEscape(owner) + "/teams"
	find := func() (int64, error) {
		var teams []struct {
			ID   int64  `json:"id"`
			Name string `json:"name"`
		}
		if err := c.do(ctx, path, url.Values{"limit": {"50"}}, &teams); err != nil {
			return 0, fmt.Errorf("forge: read the teams of %s: %w", owner, err)
		}
		for _, t := range teams {
			if strings.EqualFold(t.Name, name) {
				return t.ID, nil
			}
		}
		return 0, nil
	}
	if id, err := find(); err != nil || id != 0 {
		return id, err
	}
	var made struct {
		ID int64 `json:"id"`
	}
	err := c.send(ctx, sendOpts{
		method: http.MethodPost,
		path:   path,
		body: map[string]any{
			"name":                      name,
			"description":               "The " + name + " team of " + owner,
			"permission":                permission,
			"includes_all_repositories": true,
			"can_create_org_repo":       false,
			"units_map":                 units,
		},
		out: &made,
	})
	if errors.Is(err, ErrInvalid) {
		return find() // a racing call made it first
	}
	if err != nil {
		return 0, fmt.Errorf("forge: make team %q in %s: %w", name, owner, err)
	}
	if made.ID == 0 {
		return find()
	}
	return made.ID, nil
}

// members is the id of owner's members team, made when owner has none.
func (c *Client) members(ctx context.Context, owner string) (int64, error) {
	return c.teamNamed(ctx, owner, team, "read", teamUnits)
}

// writerUnits is what a writer may do in every repository of the namespace.
var writerUnits = map[string]string{
	"repo.code":     "write",
	"repo.issues":   "write",
	"repo.pulls":    "write",
	"repo.releases": "write",
	"repo.wiki":     "write",
	"repo.projects": "write",
}

// Admit seats login in org's write team on the forge (per the declared writers table).
// It refuses any org not in the writers table.
func (c *Client) Admit(ctx context.Context, org, login string) (bool, error) {
	if !c.machine {
		return false, fmt.Errorf("forge: a person is admitted by the machine identity, not %q", c.actor)
	}
	target, ok := writers[strings.ToLower(strings.TrimSpace(org))]
	if !ok {
		return false, fmt.Errorf("forge: org %q has no write policy", org)
	}
	id, err := c.teamNamed(ctx, target.Owner, target.Team, "write", writerUnits)
	if err != nil {
		return false, err
	}
	return c.seat(ctx, id, target.Owner, login)
}


// seat puts login in team id, and says whether this call did.
func (c *Client) seat(ctx context.Context, id int64, owner, login string) (bool, error) {
	path := "/teams/" + strconv.FormatInt(id, 10) + "/members/" + url.PathEscape(login)
	var u account
	err := c.do(ctx, path, nil, &u)
	if err == nil {
		return false, nil
	}
	if !isMissing(err) {
		return false, fmt.Errorf("forge: read %s's seat in %s: %w", login, owner, err)
	}
	if err := c.send(ctx, sendOpts{method: http.MethodPut, path: path}); err != nil {
		return false, fmt.Errorf("forge: seat %s in %s: %w", login, owner, err)
	}
	return true, nil
}

// numbered is the n-th login for base: base itself, then `-n`, cut to the
// forge's bound.
func numbered(base string, n int) string {
	suffix := ""
	if n > 1 {
		suffix = "-" + strconv.Itoa(n)
	}
	if len(base)+len(suffix) > nameMax {
		base = strings.TrimRight(base[:nameMax-len(suffix)], "-.")
	}
	if base == "" {
		return ""
	}
	return base + suffix
}

// Imported is the name a repository an org brings from another host takes in
// its workspace: the upstream account and name, folded, joined by '_'
// (apps/sync). An empty account is a name of its own, `_name`.
func Imported(account, name string) string {
	return strings.ToLower(strings.TrimSpace(account)) + "_" + strings.ToLower(strings.TrimSpace(name))
}

// people serializes [Client.Enroll] per subject within the process.
var people sync.Map // subject → *sync.Mutex

func personLock(subject string) *sync.Mutex {
	mu, _ := people.LoadOrStore(subject, &sync.Mutex{})
	return mu.(*sync.Mutex)
}
