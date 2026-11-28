package git

// link.go — a repository whose history lives on another host.
//
// A linked repository is a row here and its history there: the row names the
// host's clone address (Repo.Origin) and keeps nothing of it for good. Every read
// is served from the cache (reclaim.go), filled from the host on a miss and
// fetched again when the host last answered more than [linkFresh] ago; every
// write goes to the host (relay, send) and none lands here. Losing the cache
// loses nothing: the next read fetches it again.
//
// The HOST decides who may read and write, never this store. A repository its
// host serves to anyone is read by anyone. A private GitHub repository is read
// by a person GitHub's own view of them includes — their account's, or their
// org's installations when they administer the org — and by the org itself, a
// caller with no person (a plane peer), when the org's installation reaches it.
// A write needs the push GitHub grants that person. A private repository on any
// other host is read by no one here, since there is no one to ask about a person.
//
// The cache is filled with no credential when the host serves the repository to
// anyone, and with a read token of the org's own installation when it does not:
// never with a token this deployment holds for a host, so a link reaches nothing
// its org's GitHub does not.

import (
	"bytes"
	"cmp"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/cloud/client"
	providerpeer "github.com/hanzoai/cloud/client/provider"
	"github.com/hanzoai/cloud/forge"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/mint"
	"github.com/hanzoai/cloud/sdk/plane"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/hanzoai/outbound"
	"github.com/zap-proto/zip"
)

// providers names the provider each host's repositories are asked of. GitHub and
// the forge are the two this store can ask about a person; a host it does not
// name is "git", read as anyone reads it. A test names its loopback host here.
var providers = map[string]string{"github.com": "github", "git.hanzo.ai": "forge"}

// hubName is GitHub's shape for an account or repository name.
var hubName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,99}$`)

// link is where a linked repository's history lives: the provider that decides
// who may read and write it, its owner and name there (a "git" host's are not
// read), and the address it is cloned from.
type link struct {
	// Provider is "github", "forge" (git.hanzo.ai, Hanzo's own), or "git" for
	// any other host.
	Provider string `json:"provider"`
	// Owner is the account the repository belongs to on its provider.
	Owner string `json:"owner,omitempty"`
	// Name is the repository's name on its provider.
	Name string `json:"name,omitempty"`
	// CloneURL is the address its history is fetched from and pushed to.
	CloneURL string `json:"cloneUrl"`
}

// linkOf is r's link, and whether r has one.
func linkOf(r Repo) (*link, bool) {
	if r.Origin == "" {
		return nil, false
	}
	k := parseLink(r.Origin)
	return &k, true
}

// parseLink reads a clone address as a link: a provider's own address names its
// owner and name, as /<owner>/<name>(.git); anything else is a "git" host's.
func parseLink(clone string) link {
	k := link{Provider: "git", CloneURL: clone}
	u, err := url.Parse(clone)
	if err != nil {
		return k
	}
	p, ok := providers[strings.ToLower(u.Hostname())]
	if !ok {
		return k
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) != 2 {
		return k
	}
	owner, name := parts[0], strings.TrimSuffix(parts[1], ".git")
	if !hubName.MatchString(owner) || !hubName.MatchString(name) {
		return k
	}
	k.Provider, k.Owner, k.Name = p, owner, name
	return k
}

// linkSource is the link a caller-supplied source names: an http(s) address with
// a host the SSRF gate admits, its userinfo stripped, and a provider's address
// spelled the one way, scheme://host/<owner>/<name>.git.
func linkSource(raw string) (link, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return link{}, zip.ErrBadRequest("source is required")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return link{}, zip.ErrBadRequest("source must be an http(s) git URL")
	}
	u.User = nil
	if err := admit(context.Background(), u.Hostname()); err != nil {
		// Generic message: never confirm whether a host is internal (probe oracle).
		return link{}, zip.ErrBadRequest("source host is not permitted")
	}
	u.RawQuery, u.Fragment = "", ""
	k := parseLink(u.String())
	if k.Provider != "git" {
		k.CloneURL = u.Scheme + "://" + u.Host + "/" + k.Owner + "/" + k.Name + ".git"
	}
	return k, nil
}

// hub is what GitHub answers about one repository for the caller on ctx: whether
// they may see it and its name now (repo), a token that reads it for their org
// (read), and one that pushes there as they may, with the func that revokes it
// (write). The provider app holds the App key and answers all three; a test
// stands in for it.
type hub interface {
	repo(ctx context.Context, owner, name string) (string, string, error)
	read(ctx context.Context, owner, name string) (string, error)
	write(ctx context.Context, owner, name string) (string, func(), error)
}

// asker is the provider app, over the peer plane.
var asker hub = peer{}

type peer struct{}

func (peer) repo(ctx context.Context, owner, name string) (string, string, error) {
	out, err := providerpeer.IntegrationsGitHubRepo(ctx, &client.GitHubRepoIn{Owner: owner, Repo: name})
	if err != nil {
		return "", "", err
	}
	if out == nil || !hubName.MatchString(out.Owner) || !hubName.MatchString(out.Repo) {
		return "", "", errors.New("github repo: no repository in the answer")
	}
	return out.Owner, out.Repo, nil
}

func (peer) read(ctx context.Context, owner, name string) (string, error) {
	out, err := providerpeer.IntegrationsGitHubGrant(ctx, &client.GitHubGrantIn{Owner: owner, Repo: name, Read: true})
	if err == nil && out != nil && strings.TrimSpace(out.Token) != "" {
		return out.Token, nil
	}
	if who := zip.CallerOf(ctx); strings.TrimSpace(who.User) != "" {
		if run, rerr := providerpeer.IntegrationsGitHubRun(ctx, &client.GitHubRunIn{Owner: owner, Repo: name, Read: true}); rerr == nil && run != nil && strings.TrimSpace(run.Token) != "" {
			return run.Token, nil
		}
	}
	if err != nil {
		return "", err
	}
	return "", errors.New("github grant: no token in the answer")
}

func (peer) write(ctx context.Context, owner, name string) (string, func(), error) {
	out, err := providerpeer.IntegrationsGitHubRun(ctx, &client.GitHubRunIn{Owner: owner, Repo: name})
	if err != nil {
		return "", nil, err
	}
	if out == nil || strings.TrimSpace(out.Token) == "" {
		return "", nil, errors.New("github run: no token in the answer")
	}
	tok := out.Token
	return tok, func() { revoke(tok) }, nil
}

// revoke ends a write token once its push is answered; GitHub would expire it
// within the hour. Detached and bounded: the push is already answered.
func revoke(tok string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, api+"/installation/token", nil)
	if err != nil {
		return
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Accept", "application/vnd.github+json")
	if resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req); err == nil {
		_ = resp.Body.Close()
	}
}

// How long an answer is kept. Each is far shorter than the host's own: a
// repository made private, or a person taken off it, is read here at most that
// long after.
const (
	// openFor is how long "the host serves this to anyone", or "to no one
	// without a credential", stands.
	openFor = 5 * time.Minute
	// seeFor is how long "this caller may read this" stands.
	seeFor = time.Minute
	// readFor is how long an org's read token is used; GitHub's last an hour.
	readFor = 30 * time.Minute
)

// answers is what hosts said, briefly: which clone addresses serve anyone and
// which do not, which callers may read which repository, and the read token an
// org fills its cache with.
type answers struct {
	mu     sync.Mutex
	open   map[string]time.Time
	shut   map[string]time.Time
	seen   map[string]time.Time
	tokens map[string]minted
}

type minted struct {
	value string
	until time.Time
}

func newAnswers() *answers {
	return &answers{open: map[string]time.Time{}, shut: map[string]time.Time{},
		seen: map[string]time.Time{}, tokens: map[string]minted{}}
}

// recall is whether key is still answered in m.
func (a *answers) recall(m map[string]time.Time, key string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	until, ok := m[key]
	return ok && time.Now().Before(until)
}

// keep holds an answer for d. A map past answerCap is emptied first: every entry
// is a cheap question to ask again.
func (a *answers) keep(m map[string]time.Time, key string, d time.Duration) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(m) >= answerCap {
		clear(m)
	}
	m[key] = time.Now().Add(d)
}

// answerCap bounds each map of answers.
const answerCap = 4096

// see is nil when the caller on ctx, acting in org, may read k, as k's host
// answers it. A caller with no org (anonymous) reads only what the host serves
// to anyone; one with no person is the org itself, which reads what its own
// installation reaches.
func (a *answers) see(ctx context.Context, org string, k *link) error {
	if a.anyone(ctx, k) {
		return nil
	}
	if k.Provider == "git" {
		return zip.ErrNotFound(fmt.Sprintf("%s serves this repository to no one this store can ask for", host(k.CloneURL)))
	}
	who := zip.CallerOf(ctx)
	if strings.TrimSpace(who.Org) != org {
		// Anonymous, or acting for another org: the host serves it to neither.
		return zip.ErrNotFound("repo not found")
	}
	user := strings.TrimSpace(who.User)
	key := org + "\x00" + user + "\x00" + k.CloneURL
	if a.recall(a.seen, key) {
		return nil
	}
	call := plane.For(ctx, org)
	var err error
	switch {
	case k.Provider == "forge":
		err = forgeSee(call, org, user, k)
	case user == "":
		_, err = a.token(call, org, k)
	default:
		_, _, err = asker.repo(call, k.Owner, k.Name)
	}
	if err != nil {
		return denied(k, err)
	}
	a.keep(a.seen, key, seeFor)
	return nil
}

// anyone is whether k's host serves k with no credential at all: its ref
// advertisement answers a request that carries none.
func (a *answers) anyone(ctx context.Context, k *link) bool {
	switch {
	case a.recall(a.open, k.CloneURL):
		return true
	case a.recall(a.shut, k.CloneURL):
		return false
	case advertised(ctx, k.CloneURL):
		a.keep(a.open, k.CloneURL, openFor)
		return true
	}
	a.keep(a.shut, k.CloneURL, openFor)
	return false
}

// cred is the credential org fills k's cache under: none when the host serves k
// to anyone, else a read token of the org's own GitHub installation, or on the
// forge the deployment's own credential, asked for only after the forge said who
// may read (see).
func (a *answers) cred(ctx context.Context, org string, k *link) (gitCred, error) {
	if a.anyone(ctx, k) {
		return gitCred{}, nil
	}
	switch k.Provider {
	case "git":
		return gitCred{}, zip.ErrNotFound(fmt.Sprintf("%s serves this repository to no one this store can ask for", host(k.CloneURL)))
	case "forge":
		return forgeFill(ctx)
	}
	tok, err := a.token(plane.For(ctx, org), org, k)
	if err != nil {
		return gitCred{}, denied(k, err)
	}
	return gitCred{User: "x-access-token", Token: tok}, nil
}

// token is org's read token for k, minted by its own installation and used until
// readFor has passed.
func (a *answers) token(ctx context.Context, org string, k *link) (string, error) {
	key := org + "\x00" + k.CloneURL
	a.mu.Lock()
	t, ok := a.tokens[key]
	a.mu.Unlock()
	if ok && time.Now().Before(t.until) {
		return t.value, nil
	}
	v, err := asker.read(ctx, k.Owner, k.Name)
	if err != nil {
		return "", err
	}
	a.mu.Lock()
	if len(a.tokens) >= answerCap {
		clear(a.tokens)
	}
	a.tokens[key] = minted{value: v, until: time.Now().Add(readFor)}
	a.mu.Unlock()
	return v, nil
}

// denied is a host's no as the caller is answered: not visible is 404, refused
// is 403, and a host that could not be asked is 502.
func denied(k *link, err error) error {
	where, remedy := "GitHub", "connect your GitHub account, or ask an org admin"
	if k.Provider == "forge" {
		where, remedy = host(k.CloneURL), "sign in to it once with your Hanzo account, or ask an org admin"
	}
	var he *zip.HTTPError
	if errors.As(err, &he) {
		switch he.Status {
		case http.StatusNotFound:
			return zip.ErrNotFound(fmt.Sprintf("%s/%s is not readable as you on %s: %s", k.Owner, k.Name, where, remedy))
		case http.StatusForbidden, http.StatusUnauthorized:
			return zip.ErrForbidden(fmt.Sprintf("%s refused %s/%s: %s", where, k.Owner, k.Name, he.Msg))
		}
	}
	return zip.Errorf(http.StatusBadGateway, "%s could not be asked about %s/%s: %v", where, k.Owner, k.Name, err)
}

// host is the host a clone address names.
func host(clone string) string {
	if u, err := url.Parse(clone); err == nil && u.Host != "" {
		return u.Host
	}
	return "its host"
}

// trust is a PEM file of certificates the git CLI verifies an upstream's TLS
// against in place of the system's (linkEnv). Empty in a deployment; a test points
// it at its own server's certificate.
var trust = ""

// upstream is the HTTP client every request to a host goes through:
// hanzoai/outbound's, so the address is judged where it is dialled and a name that
// passed admit cannot connect somewhere else; bounded; and following no redirect,
// which could carry a credential to another host (outbound refuses the first).
// A test points it at its own server.
var upstream = func(timeout time.Duration) *http.Client {
	return outbound.Client(outbound.Options{Timeout: timeout})
}

// advertised is whether clone's host answers its fetch advertisement to a
// request with no credential.
func advertised(ctx context.Context, clone string) bool {
	u, err := url.Parse(clone)
	if err != nil || admit(ctx, u.Hostname()) != nil {
		return false
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, clone+"/info/refs?service="+svcUploadPack, nil)
	if err != nil {
		return false
	}
	req.Header.Set("User-Agent", "git/2 (hanzo)")
	resp, err := upstream(10 * time.Second).Do(req)
	if err != nil {
		return false
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode == http.StatusOK &&
		resp.Header.Get("Content-Type") == "application/x-"+svcUploadPack+"-advertisement"
}

// linkEnv is the git environment a link is fetched or pushed under: http(s) only,
// no redirects, the cred in an env-only header over https and nothing at all
// otherwise — never a token this deployment holds for a host.
func linkEnv(clone string, cred gitCred) []string {
	env := []string{"GIT_ALLOW_PROTOCOL=http:https"}
	cfg := []string{"http.followRedirects=false"}
	if trust != "" {
		cfg = append(cfg, "http.sslCAInfo="+trust)
	}
	if u, err := url.Parse(clone); err == nil && u.Scheme == "https" && cred.Token != "" {
		cfg = append(cfg, "http.extraHeader=Authorization: Basic "+
			base64.StdEncoding.EncodeToString([]byte(cred.User+":"+cred.Token)))
	}
	return append(env, gitConfigEnv(cfg...)...)
}

// writer is the credential a push to k goes up under, and the func that ends it:
// on GitHub the push GitHub grants the caller on ctx (provider's
// IntegrationsGitHubRun: their own, or their org's installation when they
// administer it); on the forge the caller's own Hanzo IAM token, which the forge
// takes as a git credential for the account their Hanzo sign-in linked, so the
// push is theirs and the forge's ACL decides it. Any other host is pushed to
// directly, never through here.
func writer(ctx context.Context, org string, k *link) (gitCred, func(), error) {
	switch k.Provider {
	case "forge":
		tok := bearerOf(ctx)
		if tok == "" {
			return gitCred{}, nil, zip.ErrForbidden(fmt.Sprintf(
				"a push to %s goes as you, and this request carries no Hanzo sign-in to push with (an API key is not one)", host(k.CloneURL)))
		}
		return gitCred{User: "hanzo", Token: tok}, func() {}, nil
	case "git":
		return gitCred{}, nil, zip.ErrForbidden(fmt.Sprintf(
			"this repository's history is on %s: push there directly", host(k.CloneURL)))
	}
	tok, release, err := asker.write(plane.For(ctx, org), k.Owner, k.Name)
	if err != nil {
		return gitCred{}, nil, denied(k, err)
	}
	return gitCred{User: "x-access-token", Token: tok}, release, nil
}

// relay carries one smart-HTTP push request to k's host — the ref advertisement
// (GET info/refs) or the pack (POST git-receive-pack) — under the caller's write
// credential, and answers with what the host answered. Nothing lands here.
func relay(ctx context.Context, c *zip.Ctx, org string, k *link, method, path string, body []byte) error {
	u, err := url.Parse(k.CloneURL)
	if err != nil || u.Scheme != "https" {
		return zip.ErrForbidden(fmt.Sprintf("this repository's history is on %s: push there directly", host(k.CloneURL)))
	}
	if err := admit(ctx, u.Hostname()); err != nil {
		return zip.Errorf(http.StatusBadGateway, "%s is not reachable", host(k.CloneURL))
	}
	cred, release, err := writer(ctx, org, k)
	if err != nil {
		return err
	}
	defer release()
	send := func() (*http.Response, []byte, error) {
		req, err := http.NewRequestWithContext(ctx, method, k.CloneURL+"/"+path, bytes.NewReader(body))
		if err != nil {
			return nil, nil, zip.Errorf(http.StatusInternalServerError, "%v", err)
		}
		req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(cred.User+":"+cred.Token)))
		req.Header.Set("User-Agent", "git/2 (hanzo)")
		if ct := c.Header("Content-Type"); ct != "" && method == http.MethodPost {
			req.Header.Set("Content-Type", ct)
		}
		if p := gitProtocol(c); p != "" {
			req.Header.Set("Git-Protocol", p)
		}
		resp, err := upstream(relayTimeout).Do(req)
		if err != nil {
			return nil, nil, zip.Errorf(http.StatusBadGateway, "%s did not answer the push: %v", host(k.CloneURL), err)
		}
		defer func() { _ = resp.Body.Close() }()
		out, err := io.ReadAll(io.LimitReader(resp.Body, relayBytes))
		if err != nil {
			return nil, nil, zip.Errorf(http.StatusBadGateway, "%s answer: %v", host(k.CloneURL), err)
		}
		return resp, out, nil
	}

	resp, out, err := send()
	if err != nil {
		return err
	}

	if resp.StatusCode == http.StatusUnauthorized && k.Provider == "forge" {
		var sub string
		if p, ok := principal.Minted(c); ok && p.Kind == principal.Person && p.Stable && p.Subject != "" {
			sub = p.Subject
		} else if s, err := seated(ctx); err == nil && s != "" {
			sub = s
		} else if u := c.User(); u != "" {
			sub = u
		}
		if sub != "" {
			who, err := madeEmail(ctx)
			if err != nil || who == nil || !who.Verified || strings.TrimSpace(who.Address) == "" {
				if email := c.UserEmail(); email != "" {
					name := c.UserName()
					if name == "" {
						name = c.User()
					}
					who = &client.Email{Address: email, Verified: true, Name: name}
					err = nil
				}
			}
			if err == nil && who != nil && who.Verified && strings.TrimSpace(who.Address) != "" {
				if fc, ferr := madeSource(ctx); ferr == nil {
					m := fc.Machine()
					login, _, perr := m.Provision(ctx, forge.Person{Subject: sub, Address: who.Address, Name: who.Name})
					if perr == nil && login != "" {
						if org == "hanzo" && !supporting(ctx, org) {
							_, _ = m.Admit(ctx, org, login)
						}
						if resp2, out2, serr := send(); serr == nil {
							resp, out = resp2, out2
						}
					}
				}
			}
		}
	}

	switch {
	case resp.StatusCode == http.StatusNotFound:
		return zip.ErrNotFound(fmt.Sprintf("%s/%s is not on %s", k.Owner, k.Name, host(k.CloneURL)))
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return zip.ErrForbidden(fmt.Sprintf("%s refused the push to %s/%s", host(k.CloneURL), k.Owner, k.Name))
	case resp.StatusCode/100 != 2:
		return zip.Errorf(http.StatusBadGateway, "%s answered the push %d", host(k.CloneURL), resp.StatusCode)
	}
	c.SetHeader("Content-Type", resp.Header.Get("Content-Type"))
	setGitNoCache(c)
	return c.Bytes(http.StatusOK, out)
}

// relayTimeout bounds one relayed push; relayBytes bounds its answer, which is a
// ref advertisement or a report, never a pack.
const (
	relayTimeout = 10 * time.Minute
	relayBytes   = 64 << 20
)

// caller is c's context carrying who asked: the validated principal, or nobody
// for an anonymous request. A raw handler's own context states no caller, and a
// linked repository is read as its host lets the caller read it.
func caller(c *zip.Ctx) context.Context {
	o, authed := org(c)
	if !authed {
		return c.Context()
	}
	ctx := context.WithValue(c.Context(), bearerKey{}, cloud.CallerBearer(c))
	return zip.WithCaller(ctx, zip.CallerOf(cloud.As(c, o)))
}

// bearerKey holds the caller's own Hanzo token on a raw handler's context.
type bearerKey struct{}

// bearerOf is the caller's own Hanzo token: the one a raw handler stated
// (caller), or the typed request's; empty for an API key, which is no person's
// sign-in, and for a caller with no request (a plane peer, an arrival).
func bearerOf(ctx context.Context) string {
	if b, ok := ctx.Value(bearerKey{}).(string); ok {
		return b
	}
	if c, ok := cloud.Request(ctx); ok {
		return cloud.CallerBearer(c)
	}
	return ""
}

// linkReq links one of the caller's repositories to its history on another host.
type linkReq struct {
	// Name is the repository, from the :name path segment. It is made when the
	// org has none of that name.
	Name string `json:"name"`
	// Source is where its history lives: an http(s) clone address, such as
	// https://github.com/acme/widgets.git. Its host must let the caller read it.
	Source string `json:"source"`
	// Project is the sub-scope to make it in; empty uses the caller's own.
	Project string `json:"project"`
}

// linkRepo records that a repository's history lives on another host, making
// the repository first when the org has none of that name. Nothing is copied:
// a read is served from a cache filled from the host, as the host lets the
// caller read, and a push goes to the host. A repository whose history is HERE
// is refused (409): a link would hide what only this store holds.
//
// Example: {"name": "widgets", "source": "https://github.com/acme/widgets.git"}
//
//	Response: {"id": "repo_9f3c", "org": "acme", "name": "widgets",
//		"defaultBranch": "main", "public": false,
//		"cloneUrl": "https://api.hanzo.ai/v1/git/acme/widgets.git",
//		"sshUrl": "git@git.hanzo.ai:acme/widgets.git", "sizeBytes": 0,
//		"link": {"provider": "github", "owner": "acme", "name": "widgets",
//		"cloneUrl": "https://github.com/acme/widgets.git"},
//		"createdAt": "2026-10-08T10:00:00Z"}
func (o ops) linkRepo(ctx context.Context, in *linkReq) (*repoView, error) {
	t, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	store, err := storeFor(o.s, t.org)
	if err != nil {
		return nil, internalErr(err)
	}
	name := normalizeName(in.Name)
	if !nameRE.MatchString(name) {
		return nil, zip.ErrBadRequest("name must match ^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$")
	}
	project := strings.TrimSpace(in.Project)
	if project == "" {
		project = t.project
	} else if !projectRE.MatchString(project) {
		return nil, zip.ErrBadRequest("project must match ^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$")
	}
	k, err := linkSource(in.Source)
	if err != nil {
		return nil, err
	}
	if err := o.s.State.links.see(ctx, t.org, &k); err != nil {
		return nil, err
	}
	r, err := linkTo(o.s, ctx, store, t.org, project, name, k)
	if err != nil {
		return nil, err
	}
	view := toView(o.s, r, nil, "")
	return &view, nil
}

// linkTo makes (org, project, name) a link to k: made with the link when the org
// has no such repository, linked when it holds no history here, and refused
// when it does. A link it makes arrives: its default branch goes to the code
// index and the skills, as a first push would (arrive).
func linkTo(s *cloud.Service[state], ctx context.Context, store *Store, org, project, name string, k link) (Repo, error) {
	r, err := store.Get(ctx, org, project, name)
	switch {
	case errors.Is(err, errNotFound):
		now := time.Now().Unix()
		r = Repo{ID: mint.ID("repo"), Org: org, Project: project, Name: name,
			DefaultBranch: defaultBranchName, Origin: k.CloneURL, CreatedAt: now, UpdatedAt: now}
		if err := provision(s, ctx, store, r); err != nil && !errors.Is(err, errConflict) {
			return Repo{}, internalErr(err)
		} else if err != nil {
			return linkTo(s, ctx, store, org, project, name, k)
		}
		arrive(s, r, "", "")
		return r, nil
	case err != nil:
		return Repo{}, internalErr(err)
	case r.Origin == k.CloneURL:
		return r, nil
	case r.Origin == "" && keeps(ctx, s, r):
		return Repo{}, zip.ErrConflict(fmt.Sprintf(
			"%s holds its own history here; a link would hide it", name))
	}
	if r.Origin != "" {
		// The copy in the store is of another host's repository: it is nobody's now.
		if objs, err := objectsFor(s, org); err == nil {
			_ = objs.Remove(ctx, r.ID)
		}
	}
	if err := store.SetOrigin(ctx, org, project, name, k.CloneURL); err != nil {
		return Repo{}, internalErr(err)
	}
	s.State.cache.stale(org, project, name)
	r.Origin = k.CloneURL
	arrive(s, r, "", "")
	return r, nil
}

// keeps is whether r holds a history here, in the store or in a directory.
func keeps(ctx context.Context, s *cloud.Service[state], r Repo) bool {
	objs, err := s.State.vault.home(ctx, s, r)
	if err != nil {
		return true // unreadable is kept as if it held something, as history does for a directory
	}
	if objs != nil {
		return hasHistory(objs.Repo(ctx, r.ID))
	}
	return history(ctx, s.State.storage.absRepoPath(r.Org, r.Project, r.Name))
}

// history is whether the bare repository at dir holds any ref: a history here.
func history(ctx context.Context, dir string) bool {
	if _, err := os.Stat(dir); err != nil {
		return false
	}
	cmd, err := gitCmd(ctx, nil, "--git-dir="+dir, "for-each-ref", "--count=1", "--format=%(refname)")
	if err != nil {
		return true
	}
	out, err := cmd.Output()
	// A directory git cannot read is kept as if it held something: a link
	// would hide whatever it holds.
	return err != nil || len(bytes.TrimSpace(out)) > 0
}

// githubBase is where a sweep looks for an org's repositories. A test points it
// at its own server.
var githubBase = "https://github.com"

// linkAll asks for every repository of the caller's org that holds no history
// here and names no link to be linked to its namesake on GitHub.
type linkAll struct {
	// Owner is the GitHub account a repository named n is looked for under, as
	// github.com/<owner>/<n>. Empty is the account the estate's table names for
	// the org; an org it does not name must give one.
	Owner string `json:"owner"`
}

// linked is what a sweep did, repository by repository.
type linked struct {
	// Linked are the repositories it linked, each to its namesake on GitHub.
	Linked []repoView `json:"linked"`
	// Unmatched are the repositories it left as they were, each with what GitHub
	// answered: no namesake readable as the caller is never replaced by a guess.
	Unmatched []unmatched `json:"unmatched"`
	// Held are the repositories that hold their own history here, which a link
	// would hide.
	Held []string `json:"held"`
}

// unmatched is one repository a sweep did not link, and why.
type unmatched struct {
	// Name is the repository.
	Name string `json:"name"`
	// Reason is what GitHub answered about its namesake.
	Reason string `json:"reason"`
}

// sweepWidth is how many repositories a sweep asks GitHub about at once.
const sweepWidth = 8

// linkRepos links every repository of the caller's org that holds no history
// here and names no link to github.com/<owner>/<name>, where GitHub lets the
// caller read it. A repository with no such namesake is listed and left alone,
// and one holding history here is listed and never touched. Nothing is fetched:
// a linked repository's cache fills on its first read. Org admins only.
//
// Example: {"owner": "acme"}
//
//	Response: {"linked": [{"id": "repo_9f3c", "org": "acme", "name": "widgets",
//		"defaultBranch": "main", "public": false,
//		"cloneUrl": "https://api.hanzo.ai/v1/git/acme/widgets.git",
//		"sshUrl": "git@git.hanzo.ai:acme/widgets.git", "sizeBytes": 0,
//		"link": {"provider": "github", "owner": "acme", "name": "widgets",
//		"cloneUrl": "https://github.com/acme/widgets.git"},
//		"createdAt": "2026-10-08T10:00:00Z"}],
//		"unmatched": [{"name": "scratch", "reason": "acme/scratch is not readable as you on GitHub: connect your GitHub account, or ask an org admin"}],
//		"held": ["native"]}
func (o ops) linkRepos(ctx context.Context, in *linkAll) (*linked, error) {
	t, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	if !t.admin {
		return nil, zip.ErrForbidden("linking an org's repositories takes an org admin")
	}
	owner := strings.TrimSpace(in.Owner)
	if owner == "" {
		if owner, err = forge.Owner(t.org); err != nil {
			return nil, zip.ErrBadRequest("owner is required: the estate's table names no GitHub account for " + t.org)
		}
	}
	if !hubName.MatchString(owner) {
		return nil, zip.ErrBadRequest("owner must be a GitHub account name")
	}
	store, err := storeFor(o.s, t.org)
	if err != nil {
		return nil, internalErr(err)
	}
	rows, err := store.ListOrg(ctx, t.org)
	if err != nil {
		return nil, internalErr(err)
	}
	out := &linked{Linked: []repoView{}, Unmatched: []unmatched{}, Held: []string{}}
	var mu sync.Mutex
	var wg sync.WaitGroup
	gate := make(chan struct{}, sweepWidth)
	for _, r := range rows {
		if r.Origin != "" {
			continue
		}
		if keeps(ctx, o.s, r) {
			out.Held = append(out.Held, r.Name)
			continue
		}
		wg.Add(1)
		gate <- struct{}{}
		go func(r Repo) {
			defer func() { <-gate; wg.Done() }()
			k := parseLink(githubBase + "/" + owner + "/" + r.Name + ".git")
			err := o.s.State.links.see(ctx, t.org, &k)
			if err == nil {
				r, err = linkTo(o.s, ctx, store, r.Org, r.Project, r.Name, k)
			}
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				out.Unmatched = append(out.Unmatched, unmatched{Name: r.Name, Reason: reason(err)})
				return
			}
			out.Linked = append(out.Linked, toView(o.s, r, nil, ""))
		}(r)
	}
	wg.Wait()
	slices.SortFunc(out.Linked, func(a, b repoView) int { return strings.Compare(a.Name, b.Name) })
	slices.SortFunc(out.Unmatched, func(a, b unmatched) int { return strings.Compare(a.Name, b.Name) })
	slices.Sort(out.Held)
	o.s.Log.Info("git.link swept", "org", t.org, "owner", owner, "user", t.user,
		"linked", len(out.Linked), "unmatched", len(out.Unmatched), "held", len(out.Held))
	return out, nil
}

// reason is an error as a sweep reports it.
func reason(err error) string {
	var he *zip.HTTPError
	if errors.As(err, &he) {
		return he.Msg
	}
	return err.Error()
}

// same is whether two links name one repository: a provider's by owner and
// name, as the provider compares them (GitHub ignores case), any other host's by
// its address.
func same(a, b link) bool {
	if a.Provider != "git" && a.Provider == b.Provider {
		return strings.EqualFold(a.Owner, b.Owner) && strings.EqualFold(a.Name, b.Name)
	}
	trim := func(u string) string { return strings.TrimSuffix(strings.TrimSuffix(u, "/"), ".git") }
	return strings.EqualFold(trim(a.CloneURL), trim(b.CloneURL))
}

// arrivals bounds how many linked repositories fill and index at once, so a
// sweep that links hundreds reaches their hosts a few at a time.
var arrivals = make(chan struct{}, 2)

// arriveFor bounds one arrival: the fill of a large repository and its read.
const arriveFor = 30 * time.Minute

// arrive hands a linked repository's default-branch tip to the code index and
// the skills, as a push landing here does for a repository whose history is
// here: on its first link (branch empty: the host's default branch and its
// tip), and on each push its host reports (planeLanded). It runs detached, as
// the org itself, under the service's life, and Shutdown waits for it.
func arrive(s *cloud.Service[state], r Repo, branch, after string) {
	if s.State.life.Err() != nil {
		return
	}
	s.State.arriving.Add(1)
	go func() {
		defer s.State.arriving.Done()
		select {
		case arrivals <- struct{}{}:
		case <-s.State.life.Done():
			return
		}
		defer func() { <-arrivals }()
		ctx, cancel := context.WithTimeout(cloud.For(s.State.life, r.Org), arriveFor)
		defer cancel()
		repo, done, err := open(ctx, s, r)
		if err != nil {
			s.Log.Warn("git.link arrival: read failed", "org", r.Org, "repo", r.Name, "link", r.Origin, "err", err)
			return
		}
		if branch == "" {
			b, sha, ok := defaultBranchTip(ctx, repo)
			if !ok {
				done()
				return // an empty repository: nothing to index
			}
			branch, after = b, sha
		}
		done()
		ev := cloud.LifecycleEvent{Kind: cloud.LifecyclePushLanded, Org: r.Org, Project: r.Project,
			Repo: r.Name, Branch: branch, After: after}
		indexOnPush(s, ctx, ev)
		skillsOnPush(s, ctx, ev)
	}()
}

// defaultBranchTip is a repository's default branch (its symbolic HEAD) and
// that branch's tip; ok is false for an empty or detached repository.
func defaultBranchTip(ctx context.Context, r Repository) (branch, sha string, ok bool) {
	branch, err := r.DefaultBranch(ctx)
	if err != nil || branch == "" {
		return "", "", false
	}
	rev, _, err := r.Resolve(ctx, "")
	if err != nil {
		return "", "", false
	}
	return branch, rev.String(), true
}

// exposeLanded serves what a linked repository's host says landed on it.
func exposeLanded() {
	cloud.Plane().Post("/git/landed", planeLanded,
		zip.WithOperationID(client.GitLanded),
		zip.WithSummary("A push landed on a linked repository's host"))
}

// planeLanded takes a push its caller verified on a repository's host — the
// provider app, from GitHub's signed push webhook — for the caller's org. Every
// repository of the org linked to that host address reads the push on its next
// read, and a push to its default branch goes to the code index and the skills
// (arrive). A ref that is not a branch, or a branch that is not the default of
// a cache already here, moves nothing else. The org is the caller's plane
// identity, never an argument, so a push one org's installation reported can
// never reach another org's repositories.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func planeLanded(ctx context.Context, in *client.LandedIn) (*client.Landed, error) {
	org := cloud.Who(ctx).Org
	if org == "" {
		return nil, zip.ErrForbidden("git landed: org required")
	}
	s := mounted.Load()
	if s == nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "git not mounted")
	}
	store, err := storeFor(s, org)
	if err != nil {
		return nil, internalErr(err)
	}
	rows, err := store.ListOrg(ctx, org)
	if err != nil {
		return nil, internalErr(err)
	}
	k := parseLink(strings.TrimSpace(in.CloneURL))
	branch, isBranch := strings.CutPrefix(in.Ref, "refs/heads/")
	out := &client.Landed{Repos: []string{}}
	for _, r := range rows {
		rk, ok := linkOf(r)
		if !ok || !same(*rk, k) {
			continue
		}
		s.State.cache.stale(r.Org, r.Project, r.Name)
		out.Repos = append(out.Repos, r.Name)
		dir := s.State.storage.absRepoPath(r.Org, r.Project, r.Name)
		if !isBranch || (s.State.storage.exists(r.Org, r.Project, r.Name) && defaultBranchOf(ctx, dir) != branch) {
			continue
		}
		arrive(s, r, branch, in.After)
	}
	if len(out.Repos) == 0 && isBranch && k.Provider == "forge" && k.Name != "" {
		if owner, err := forgeHome(org); err == nil && strings.EqualFold(owner, k.Owner) {
			if r, err := linkTo(s, ctx, store, org, "", k.Name, k); err == nil {
				out.Repos = append(out.Repos, r.Name)
			}
		}
	}
	return out, nil
}

// forgeAs is the forge as the person on ctx: their own login, proved by
// [forge.Client.Caller] (a confirmed address whose login is theirs), so the
// forge's ACL and not the machine's decides what they may read. A test stands
// in for it.
var forgeAs = func(ctx context.Context) (*forge.Client, error) {
	c, err := madeSource(ctx)
	if err != nil {
		// The forge's own refusal of the platform credential is a gateway fault the
		// caller can read, not an opaque 500.
		return nil, zip.Errorf(http.StatusBadGateway, "the code forge is unreachable: %v", err)
	}
	login, err := c.Caller(ctx)
	if err != nil {
		return nil, &zip.HTTPError{Status: http.StatusForbidden, Msg: err.Error()}
	}
	return c.As(login), nil
}

// forgeSee is nil when the forge lets the caller read k: a person as their own
// forge login; the org itself (no person) only in the org's own namespace there.
func forgeSee(ctx context.Context, org, user string, k *link) error {
	if user == "" {
		if owner, err := forgeHome(org); err == nil && strings.EqualFold(owner, k.Owner) {
			return nil
		}
		return &zip.HTTPError{Status: http.StatusNotFound, Msg: "not this org's own namespace on the forge"}
	}
	m, err := forgeAs(ctx)
	if err != nil {
		return err
	}
	if _, err := m.Repo(ctx, k.Owner, k.Name); err != nil {
		if errors.Is(err, forge.ErrUnknownActor) || errors.Is(err, forge.ErrNotFound) {
			return &zip.HTTPError{Status: http.StatusNotFound, Msg: "not found"}
		}
		return err
	}
	return nil
}

// forgeHome is the org's own namespace on the forge: the estate's table's, else
// the code workspace the platform made for it.
func forgeHome(org string) (string, error) {
	if owner, err := forge.Owner(org); err == nil {
		return owner, nil
	}
	ws, err := madeWorkspace(org, false)
	if err != nil {
		return "", err
	}
	if !ws.Made {
		return "", forge.ErrNoOwner
	}
	return ws.Owner, nil
}

// forgeFill is the credential a forge link's cache is filled under: the
// deployment's own, asked for only after the forge said, as the caller, that
// they may read (see). The forge requires a sign-in even to read.
var forgeFill = func(ctx context.Context) (gitCred, error) {
	c, err := madeSource(ctx)
	if err != nil {
		return gitCred{}, zip.Errorf(http.StatusBadGateway, "the code forge is unreachable: %v", err)
	}
	user, pass := c.Machine().Basic()
	return gitCred{User: user, Token: pass}, nil
}

// homeReq names a repository whose history is here, to be moved to the forge.
type homeReq struct {
	// Name is the repository, from the :name path segment.
	Name string `json:"name"`
}

// homeRepo moves a repository whose history is HERE to the forge (git.hanzo.ai),
// the one home of a repository no other host holds, and links it there: the
// repository is made in the org's own namespace on the forge, every branch and
// tag is pushed to it, the forge's refs are read back and must equal these, and
// only then does the row name the forge as its link, so this copy becomes the
// cache it can be fetched back into. A forge repository of that name that
// already exists is refused, never overwritten. Org admins only.
//
// Example: {"name": "widgets"}
//
//	Response: {"id": "repo_9f3c", "org": "acme", "name": "widgets",
//		"defaultBranch": "main", "public": false,
//		"cloneUrl": "https://api.hanzo.ai/v1/git/acme/widgets.git",
//		"sshUrl": "git@git.hanzo.ai:acme/widgets.git", "sizeBytes": 4096,
//		"link": {"provider": "forge", "owner": "acme", "name": "widgets",
//		"cloneUrl": "https://git.hanzo.ai/acme/widgets.git"},
//		"createdAt": "2026-10-08T10:00:00Z"}
func (o ops) homeRepo(ctx context.Context, in *homeReq) (*repoView, error) {
	t, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	if !t.admin {
		return nil, zip.ErrForbidden("moving a repository to the forge takes an org admin")
	}
	name := normalizeName(in.Name)
	store, err := storeFor(o.s, t.org)
	if err != nil {
		return nil, internalErr(err)
	}
	r, err := store.Get(ctx, t.org, t.project, name)
	if err != nil {
		return nil, zip.ErrNotFound("repo not found")
	}
	if r.Origin != "" {
		return nil, zip.ErrConflict("this repository is linked to " + r.Origin + ", which already holds its history")
	}
	dir := o.s.State.storage.absRepoPath(r.Org, r.Project, r.Name)
	objs, err := o.s.State.vault.home(ctx, o.s, r)
	if err != nil {
		return nil, internalErr(err)
	}
	// A repository kept in the store is written to a throwaway directory for the CLI
	// to push from (export.go); one that is still a directory is pushed from where it is.
	if objs != nil {
		repo := objs.Repo(ctx, r.ID)
		refs, err := tipsAndTags(repo)
		if err != nil {
			return nil, internalErr(err)
		}
		var cleanup func()
		if dir, cleanup, err = exportDir(ctx, repo, refs, headBranch(repo)); err != nil {
			return nil, internalErr(err)
		}
		defer cleanup()
	}
	if !history(ctx, dir) {
		return nil, zip.ErrConflict("this repository holds no history here to move; link it instead")
	}
	owner, err := forgeHome(t.org)
	if err != nil {
		return nil, zip.ErrConflict("this org has no namespace on the forge: " + err.Error())
	}
	branch := cmp.Or(defaultBranchOf(ctx, dir), r.DefaultBranch, defaultBranchName)
	clone, err := forgeReceive(ctx, owner, name, r.Description, branch, func(ctx context.Context, clone string, cred gitCred) error {
		return moveRefs(ctx, dir, clone, cred)
	})
	switch {
	case errors.Is(err, forge.ErrExists):
		return nil, zip.ErrConflict(fmt.Sprintf("%s/%s already exists on the forge; it is not overwritten", owner, name))
	case err != nil:
		return nil, zip.Errorf(http.StatusBadGateway, "moving %s to the forge: %v", name, err)
	}
	k := parseLink(clone)
	if err := store.SetOrigin(ctx, r.Org, r.Project, r.Name, k.CloneURL); err != nil {
		return nil, internalErr(err)
	}
	o.s.State.cache.stale(r.Org, r.Project, r.Name)
	r.Origin = k.CloneURL
	if objs != nil {
		// The forge holds the history now, proved equal above, and the cache is filled
		// from it: the rows are a second copy that nothing reads.
		if err := objs.Remove(ctx, r.ID); err != nil {
			o.s.Log.Warn("git.home: rows left behind", "org", t.org, "repo", name, "err", err)
		}
	}
	o.s.Log.Info("git.home moved", "org", t.org, "repo", name, "to", k.CloneURL, "user", t.user)
	view := toView(o.s, r, nil, "")
	return &view, nil
}

// moveRefs pushes every branch and tag of the bare repository at dir to clone,
// forced (the forge repository was made for it), then reads the forge's refs
// back and requires them equal to these: the move is proved before the row
// says the history lives there.
func moveRefs(ctx context.Context, dir, clone string, cred gitCred) error {
	env := linkEnv(clone, cred)
	args := append([]string{"-c", "credential.helper=", "--git-dir=" + dir, "push", "--force", "--porcelain", clone}, fillRefSpecs...)
	cmd, err := gitCmd(ctx, env, args...)
	if err != nil {
		return err
	}
	var out bytes.Buffer
	stderr := &cappedBuffer{cap: stderrCap}
	cmd.Stdout, cmd.Stderr = &out, stderr
	if err := withPackSlot(ctx, cmd.Run); err != nil {
		return fmt.Errorf("push: %w: %s", err, sanitizeGitErr(out.String()+"\n"+stderr.String()))
	}
	here, err := refsAt(ctx, nil, "--git-dir="+dir, "for-each-ref", "--format=%(objectname) %(refname)", "refs/heads", "refs/tags")
	if err != nil {
		return err
	}
	there, err := refsAt(ctx, env, "-c", "credential.helper=", "ls-remote", "--heads", "--tags", clone)
	if err != nil {
		return err
	}
	for ref, sha := range here {
		if there[ref] != sha {
			return fmt.Errorf("%s is %s here and %q on the forge", ref, sha, there[ref])
		}
	}
	return nil
}

// refsAt runs git and reads "<sha> <ref>" lines (either separator) into a map,
// peeled tags (^{}) left out.
func refsAt(ctx context.Context, env []string, args ...string) (map[string]string, error) {
	cmd, err := gitCmd(ctx, env, args...)
	if err != nil {
		return nil, err
	}
	stderr := &cappedBuffer{cap: stderrCap}
	cmd.Stderr = stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git %s: %w: %s", args[len(args)-1], err, sanitizeGitErr(stderr.String()))
	}
	refs := map[string]string{}
	for line := range strings.SplitSeq(strings.TrimSpace(string(out)), "\n") {
		sha, ref, ok := strings.Cut(strings.Replace(line, "\t", " ", 1), " ")
		if ok && !strings.HasSuffix(ref, "^{}") {
			refs[ref] = sha
		}
	}
	return refs, nil
}

// forgeReceive makes owner/name on the forge, empty, in the org's namespace,
// hands move its clone address and the deployment's credential to push the
// history in with, and seals it ([forge.Client.Receive]); it answers the clone
// address. A test stands in for it.
var forgeReceive = func(ctx context.Context, owner, name, description, branch string,
	move func(ctx context.Context, clone string, cred gitCred) error) (string, error) {
	c, err := madeSource(ctx)
	if err != nil {
		return "", err
	}
	m := c.Machine()
	clone := m.Origin() + "/" + owner + "/" + name + ".git"
	user, pass := m.Basic()
	if _, err := m.Receive(ctx, owner, name, description, branch, func(ctx context.Context) error {
		return move(ctx, clone, gitCred{User: user, Token: pass})
	}); err != nil {
		return "", err
	}
	return clone, nil
}
