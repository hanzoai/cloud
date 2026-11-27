package coding

// github.go is a run whose repository lives on GitHub.
//
// Most of the estate sits on the forge as PULL MIRRORS of GitHub, and a mirror
// is a cache: it lags its origin, and nothing pushed to it reaches there. A run
// that cloned the mirror worked on a stale copy, pushed a branch that could not
// leave, and proposed it where nobody reviews. So a run's HOME is resolved once,
// from the forge's own word on the repository, and the clone, the push, the
// integrity gate and the proposal all go to the home. A repository that is not
// a mirror of GitHub is its own home, and the forge path stands unchanged.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/hanzoai/cloud/client"
	providerpeer "github.com/hanzoai/cloud/client/provider"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/retry"
	"github.com/zap-proto/zip"
)

// githubAPI is GitHub's REST root. A var so a test can stand in for it; never
// reassigned in production.
var githubAPI = "https://api.github.com"

var githubHTTP = &http.Client{Timeout: 30 * time.Second}

// ghName is GitHub's shape for an account or repository name.
var ghName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,99}$`)

// hubRefresh is when a run's one-hour GitHub installation token is replaced
// before the next remote operation, leaving a twenty-minute margin before
// GitHub expires it.
const hubRefresh = 40 * time.Minute

// hubNow is the clock token renewal reads; tests may advance it.
var hubNow = time.Now

// hub is a run's GitHub home and the token that reaches it: one repository,
// contents and pull requests, for this run only.
type hub struct {
	owner, name string
	mu          sync.Mutex
	token       string // write-only: never logged, never in an event
	minted      time.Time
	renew       func(context.Context) (string, error)
	// reads is the token this run's agent fetches with, which can only read: this hub when
	// it was admitted to read, else a second one (readable). revoke ends it with the first.
	reads *hub
}

// String names the repository and never the token, so a hub that reaches a log
// line carries nothing worth stealing.
func (h *hub) String() string { return "github.com/" + h.owner + "/" + h.name }

// remote is where the sandbox clones and pushes. HTTPS, because an installation
// token is an HTTP credential; TLS against the image's roots is the pin.
func (h *hub) remote() string { return "https://" + h.String() + ".git" }

func detachCaller(ctx context.Context) zip.Caller {
	w := cloud.Who(ctx)
	return zip.Caller{
		Org:       strings.Clone(w.Org),
		Project:   strings.Clone(w.Project),
		User:      strings.Clone(w.User),
		Name:      strings.Clone(w.Name),
		Email:     strings.Clone(w.Email),
		Owner:     strings.Clone(w.Owner),
		Admin:     w.Admin,
		OrgAdmin:  w.OrgAdmin,
		RequestID: strings.Clone(w.RequestID),
		IP:        strings.Clone(w.IP),
		Account:   strings.Clone(w.Account),
	}
}

func withCaller(_ context.Context, c zip.Caller) context.Context {
	return zip.WithCaller(context.Background(), c)
}

func mintOnce(ctx context.Context, owner, name string, read bool) (*hub, error) {
	ctx, cancel := bounded(ctx)
	defer cancel()
	out, err := providerpeer.IntegrationsGitHubGrant(ctx, &client.GitHubGrantIn{Owner: owner, Repo: name, Read: read})
	if err != nil {
		return nil, err
	}
	if out == nil || strings.TrimSpace(out.Token) == "" || !ghName.MatchString(out.Owner) || !ghName.MatchString(out.Repo) {
		return nil, fmt.Errorf("no token and repository in the answer")
	}
	return &hub{owner: out.Owner, name: out.Repo, token: out.Token, minted: hubNow()}, nil
}

// mint asks the process that holds the App key for a token to owner/name — one
// that can only read when read is set, a plan run's. The org rides the caller,
// so only an account this org connected can be minted on.
//
// The hub is where the answer says the repository lives, not where the mirror
// said: a transferred repository keeps its old name as a redirect, and a pull
// request POSTed through a redirect arrives as a GET.
func mint(ctx context.Context, owner, name string, read bool) (*hub, error) {
	caller := detachCaller(ctx)
	h, err := mintOnce(ctx, owner, name, read)
	if err != nil {
		return nil, err
	}
	h.renew = func(rctx context.Context) (string, error) {
		m, err := mintOnce(withCaller(rctx, caller), h.owner, h.name, read)
		if err != nil {
			return "", err
		}
		return m.token, nil
	}
	return h, nil
}

func admitOnce(ctx context.Context, owner, name string, read bool) (h *hub, base, login string, err error) {
	ctx, cancel := bounded(ctx)
	defer cancel()
	out, err := providerpeer.IntegrationsGitHubRun(ctx, &client.GitHubRunIn{Owner: owner, Repo: name, Read: read})
	if err != nil {
		return nil, "", "", err
	}
	if out == nil || strings.TrimSpace(out.Token) == "" || !ghName.MatchString(out.Owner) || !ghName.MatchString(out.Repo) {
		return nil, "", "", fmt.Errorf("no token and repository in the answer")
	}
	return &hub{owner: out.Owner, name: out.Repo, token: out.Token, minted: hubNow()}, out.Base, out.Login, nil
}

// admit asks the process that holds the App key whether the caller may run on
// owner/name, and for the run's token when they may: their own connected
// GitHub pushing there, or their org's installation with them administering
// the org. The caller — the org, the person and whether they administer it —
// rides ctx, never an argument. It answers the repository where it lives now,
// its default branch, and whose GitHub admitted it ("" for an admin). read asks
// for a token that can only read, a plan run's; the admission is the same.
func admit(ctx context.Context, owner, name string, read bool) (h *hub, base, login string, err error) {
	caller := detachCaller(ctx)
	h, base, login, err = admitOnce(ctx, owner, name, read)
	if err != nil {
		return nil, "", "", err
	}
	h.renew = func(rctx context.Context) (string, error) {
		m, _, _, err := admitOnce(withCaller(rctx, caller), h.owner, h.name, read)
		if err != nil {
			return "", err
		}
		return m.token, nil
	}
	return h, base, login, nil
}

// ensureToken returns a valid token for this run, minting a fresh one and
// revoking the previous token when force is true or the token has reached
// hubRefresh.
func (h *hub) ensureToken(ctx context.Context, force bool) (string, error) {
	if h == nil {
		return "", nil
	}
	h.mu.Lock()
	tok := h.token
	minted := h.minted
	renew := h.renew
	h.mu.Unlock()
	if tok == "" || renew == nil {
		return tok, nil
	}
	if !force && (minted.IsZero() || hubNow().Sub(minted) < hubRefresh) {
		return tok, nil
	}
	fresh, err := renew(ctx)
	if err != nil || strings.TrimSpace(fresh) == "" {
		if err == nil {
			err = fmt.Errorf("github: token renewal returned an empty token for %s", h)
		}
		return tok, err
	}
	h.mu.Lock()
	prev := h.token
	h.token = fresh
	h.minted = hubNow()
	h.mu.Unlock()
	if prev != "" && prev != fresh {
		rctx, cancel := bounded(context.Background())
		_ = (&hub{owner: h.owner, name: h.name, token: prev}).revoke(rctx)
		cancel()
	}
	return fresh, nil
}

// defaultBranch reads the repository's default_branch from GitHub.
func (h *hub) defaultBranch(ctx context.Context) string {
	if h == nil {
		return ""
	}
	ctx, cancel := bounded(ctx)
	defer cancel()
	var r struct {
		Branch string `json:"default_branch"`
	}
	if st, err := h.call(ctx, http.MethodGet, h.path(""), nil, &r); err == nil && st == http.StatusOK && BaseRE.MatchString(r.Branch) {
		return r.Branch
	}
	return ""
}

// commitSubject reads the tip commit subject of branch on GitHub.
func (h *hub) commitSubject(ctx context.Context, branch string) string {
	if h == nil || strings.TrimSpace(branch) == "" {
		return ""
	}
	ctx, cancel := bounded(ctx)
	defer cancel()
	var c struct {
		Commit struct {
			Message string `json:"message"`
		} `json:"commit"`
	}
	if st, err := h.call(ctx, http.MethodGet, h.path("commits/"+segments(branch)), nil, &c); err == nil && st == http.StatusOK {
		return firstLine(c.Commit.Message)
	}
	return ""
}

// landed is the integrity gate asked of the home: what the branch points at on
// GitHub, read with the run's own token. It has VerifyRef's shape, so a run
// bound to GitHub is verified where it pushed.
func (h *hub) landed(ctx context.Context, _, _, branch string) (string, bool) {
	ctx, cancel := bounded(ctx)
	defer cancel()
	var ref struct {
		Object struct {
			SHA string `json:"sha"`
		} `json:"object"`
	}
	st, err := h.call(ctx, http.MethodGet, h.path("git/ref/heads/"+segments(branch)), nil, &ref)
	if err != nil || st != http.StatusOK || ref.Object.SHA == "" {
		return "", false
	}
	return ref.Object.SHA, true
}

// propose opens the pull request on GitHub and answers where it is read.
//
// The App opens it: an installation token authors as nothing else, and cloud
// holds no person's GitHub credential to borrow. The body names the person.
//
// A branch that already has an open pull request answers with that one, for
// forge.Propose's reason: a retried run pushes the same branch again.
func (h *hub) propose(ctx context.Context, base, head, title, body string, draft bool) (string, int64, error) {
	if strings.TrimSpace(base) == "" {
		var r struct {
			Branch string `json:"default_branch"`
		}
		if _, err := h.call(ctx, http.MethodGet, h.path(""), nil, &r); err != nil || r.Branch == "" {
			return "", 0, fmt.Errorf("github: %s names no default branch", h)
		}
		base = r.Branch
	}
	var made struct {
		Number int64  `json:"number"`
		URL    string `json:"html_url"`
	}
	st, err := h.call(ctx, http.MethodPost, h.path("pulls"),
		map[string]any{"title": title, "head": head, "base": base, "body": body, "draft": draft}, &made)
	if err != nil {
		return "", 0, fmt.Errorf("github: propose %s on %s: %w", head, h, err)
	}
	if st == http.StatusCreated && made.URL != "" {
		return made.URL, made.Number, nil
	}
	var open []struct {
		Number int64  `json:"number"`
		URL    string `json:"html_url"`
	}
	q := "?state=open&head=" + url.QueryEscape(h.owner+":"+head)
	if _, err := h.call(ctx, http.MethodGet, h.path("pulls")+q, nil, &open); err == nil && len(open) > 0 && open[0].URL != "" {
		return open[0].URL, open[0].Number, nil
	}
	return "", 0, fmt.Errorf("github: propose %s into %s on %s: %d", head, base, h, st)
}

// errPeople is a pull request that opened but did not take every assignee or
// reviewer it was given: GitHub refuses a login that cannot be assigned to, or
// asked to review, its repository. The pull request stands; the error says who
// was left off.
var errPeople = errors.New("github: the pull request is open, but not everyone named was added")

// assign gives pull request number its assignees and asks its reviewers, each
// list in one call. Logins were checked when the request arrived (people).
func (h *hub) assign(ctx context.Context, number int64, assignees, reviewers []string) error {
	if number <= 0 || (len(assignees) == 0 && len(reviewers) == 0) {
		return nil
	}
	var failed []string
	ask := func(path, field string, logins []string) {
		if len(logins) == 0 {
			return
		}
		st, err := h.call(ctx, http.MethodPost, h.path(path), map[string]any{field: logins}, nil)
		switch {
		case err != nil:
			failed = append(failed, fmt.Sprintf("%s %s: %v", field, strings.Join(logins, ","), err))
		case st < 200 || st > 299:
			failed = append(failed, fmt.Sprintf("%s %s: %d", field, strings.Join(logins, ","), st))
		}
	}
	ask(fmt.Sprintf("issues/%d/assignees", number), "assignees", assignees)
	ask(fmt.Sprintf("pulls/%d/requested_reviewers", number), "reviewers", reviewers)
	if len(failed) > 0 {
		return fmt.Errorf("%w (%s)", errPeople, strings.Join(failed, "; "))
	}
	return nil
}

// readable is the token this run's agent fetches with, so it can bring in a branch the clone
// lacks and merge it. The hub's own when it was admitted only to read (readOnly); else a second,
// asked of ask, the admission this hub came from with read set, which GitHub mints for the
// repository's contents and nothing else, so no push can use it. A repository GitHub will not
// mint one for leaves the agent as it was: no credential.
func (h *hub) readable(ctx context.Context, readOnly bool, ask func(context.Context) (*hub, error)) *hub {
	switch {
	case h == nil || h.token == "":
		return nil
	case readOnly:
		h.reads = h
	default:
		r, err := ask(ctx)
		if err != nil || r == nil || r.token == "" {
			slog.Warn("coding: no read-only token for the run's agent", "repo", h.String(), "err", err)
			return nil
		}
		h.reads = r
	}
	return h.reads
}

// revoke ends the token now instead of at GitHub's hour, and the read-only one beside it. A 401
// is a token already gone, which is the state asked for.
func (h *hub) revoke(ctx context.Context) error {
	var rerr error
	if r := h.reads; r != nil && r != h {
		rerr = r.revoke(ctx)
	}
	st, err := h.call(ctx, http.MethodDelete, "/installation/token", nil, nil)
	if err != nil {
		return errors.Join(rerr, fmt.Errorf("github: revoke the run's token for %s: %w", h, err))
	}
	if st != http.StatusNoContent && st != http.StatusUnauthorized {
		return errors.Join(rerr, fmt.Errorf("github: revoke the run's token for %s: %d", h, st))
	}
	return rerr
}

// path is a route under this repository.
func (h *hub) path(rest string) string {
	p := "/repos/" + url.PathEscape(h.owner) + "/" + url.PathEscape(h.name)
	if rest != "" {
		p += "/" + rest
	}
	return p
}

// segments escapes a branch one path segment at a time, keeping its slashes.
func segments(branch string) string {
	parts := strings.Split(branch, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return strings.Join(parts, "/")
}

// call is one GitHub request carrying the run's token. The token rides the
// header and nothing else, so no error built here can carry it.
func (h *hub) call(ctx context.Context, method, path string, in, out any) (int, error) {
	revolving := path != "/installation/token"
	h.mu.Lock()
	tok := h.token
	h.mu.Unlock()
	if revolving {
		if fresh, err := h.ensureToken(ctx, false); err == nil && fresh != "" {
			tok = fresh
		}
	}
	st, err := h.doCall(ctx, method, path, tok, in, out)
	if err == nil && st == http.StatusUnauthorized && revolving {
		if fresh, rerr := h.ensureToken(ctx, true); rerr == nil && fresh != "" && fresh != tok {
			return h.doCall(ctx, method, path, fresh, in, out)
		}
	}
	return st, err
}

// doCall is one GitHub request, made again (sdk/retry) when GitHub rate limits it (429, or 403 carrying
// Retry-After) or fails in a way that passes: a 500, 502, 503 or 504, a timeout, a reset or a refused
// connection. Any other answer is the answer. Every call here is safe to repeat: a read, the merge
// (it names its sha), a pull request open (a second is answered 422 and propose reads back the open
// one), a people assignment and a token revoke.
func (h *hub) doCall(ctx context.Context, method, path, tok string, in, out any) (st int, err error) {
	var payload []byte
	if in != nil {
		if payload, err = json.Marshal(in); err != nil {
			return 0, err
		}
	}
	retry.Do(ctx, func() (bool, time.Duration) {
		var after string
		st, after, err = h.send(ctx, method, path, tok, payload, out)
		return again(st, after, err)
	})
	return st, err
}

// send is one try of doCall: the status, the Retry-After GitHub sent, and the error.
func (h *hub) send(ctx context.Context, method, path, tok string, payload []byte, out any) (int, string, error) {
	var body io.Reader
	if payload != nil {
		body = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, githubAPI+path, body)
	if err != nil {
		return 0, "", err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Accept", "application/vnd.github+json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := githubHTTP.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	if out != nil && resp.StatusCode/100 == 2 {
		if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(out); err != nil {
			return resp.StatusCode, "", err
		}
	}
	return resp.StatusCode, resp.Header.Get("Retry-After"), nil
}

// again says whether a try that ended in status st (0 for an error) with the Retry-After GitHub sent is
// worth making again, and the wait GitHub named.
func again(st int, after string, err error) (bool, time.Duration) {
	switch {
	case err != nil:
		return passing(err), 0
	case st == http.StatusTooManyRequests || st == http.StatusForbidden && after != "":
		return true, waitOf(after)
	}
	return st == http.StatusInternalServerError || st == http.StatusBadGateway ||
		st == http.StatusServiceUnavailable || st == http.StatusGatewayTimeout, 0
}

// waitOf is the wait a Retry-After header names, in seconds or as an HTTP date, and 0
// for one that is absent, malformed or already past.
func waitOf(v string) time.Duration {
	v = strings.TrimSpace(v)
	if n, err := strconv.ParseUint(v, 10, 31); err == nil {
		return time.Duration(n) * time.Second
	}
	if at, err := http.ParseTime(v); err == nil {
		return max(time.Until(at), 0)
	}
	return 0
}

// passing is a failure of the connection that a second try may not have: a timeout, a
// reset, a refusal, or a stream that ended early.
func passing(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout() || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) ||
		errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.ECONNREFUSED)
}

// onGitHub admits a run on owner/name as the caller, for the most the caller may
// do there. A build pushes through their own GitHub or their org's installation
// (admit); else it reads through the same and keeps its work in its workspace;
// else a public repository is read with no credential at all. A plan only ever
// reads. It answers the hub — with no token for a public read — the default
// branch, whose GitHub admitted it, and whether the run's work stays local.
func onGitHub(ctx context.Context, owner, name string, read bool) (h *hub, base, login string, local bool, err error) {
	if h, base, login, err = admit(ctx, owner, name, read); err == nil {
		return h, base, login, read, nil
	}
	if !read {
		if h, base, login, rerr := admit(ctx, owner, name, true); rerr == nil {
			return h, base, login, true, nil
		}
	}
	if o, n, b, ok := public(ctx, owner, name); ok {
		return &hub{owner: o, name: n}, b, "", true, nil
	}
	return nil, "", "", false, err
}

// public reads owner/name as anyone may: its canonical name and default branch
// when GitHub serves it to nobody in particular, and false otherwise.
func public(ctx context.Context, owner, name string) (o, n, base string, ok bool) {
	ctx, cancel := bounded(ctx)
	defer cancel()
	var r struct {
		FullName string `json:"full_name"`
		Private  bool   `json:"private"`
		Branch   string `json:"default_branch"`
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		githubAPI+"/repos/"+url.PathEscape(owner)+"/"+url.PathEscape(name), nil)
	if err != nil {
		return "", "", "", false
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := githubHTTP.Do(req)
	if err != nil {
		return "", "", "", false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&r) != nil || r.Private {
		return "", "", "", false
	}
	o, n, found := strings.Cut(r.FullName, "/")
	if !found || !ghName.MatchString(o) || !ghName.MatchString(n) || !BaseRE.MatchString(r.Branch) {
		return "", "", "", false
	}
	return o, n, r.Branch, true
}
