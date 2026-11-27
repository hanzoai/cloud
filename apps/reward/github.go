package reward

// github.go — GitHub's half of the rewards feeders: the deliveries the provider app
// verifies (a merged pull request, an accepted issue, a star, a fork), the member's
// own "check my GitHub" (stars, follows and forks, which GitHub tells no webhook
// of) and the README badge verifier.
//
// WHO IT IS FOR. A GitHub account is matched to a Hanzo person by the numeric user
// id IAM wrote on the user row when they linked GitHub at sign-in (IAMGitHubPeople),
// never by a login a person typed or a name in a payload. An account nobody linked
// earns nothing.
//
// WHAT IS STATED. Every event leaves with its evidence (catalog.Evidence): the
// canonical URL, and the check this code ran against GitHub's API for it (fetched_at,
// HTTP status, public, author match). Where GitHub cannot be read at that moment (its
// rate limit, an outage) the event is stated needs_review, with no checks, rather than
// a check nobody ran. Nothing here awards anything: every rule is manual, and staff
// decide the claim this makes.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/authz"
	"github.com/hanzoai/reward/catalog"

	"github.com/hanzoai/cloud/client"
	iampeer "github.com/hanzoai/cloud/client/iam"
	"github.com/hanzoai/cloud/sdk"
)

// owners are the GitHub accounts whose repositories and followers earn: the orgs
// the hanzo-platform GitHub App is installed on.
var owners = []string{"hanzoai", "hanzo-apps", "hanzo-go", "hanzo-js", "luxfi", "lux-cpp", "zenlm", "zooai"}

// ours reports whether login is one of owners, without case.
func ours(login string) bool {
	return slices.ContainsFunc(owners, func(o string) bool { return strings.EqualFold(o, login) })
}

// errUnreadable is a delivery that is not the JSON GitHub sends.
var errUnreadable = errors.New("the delivery is not a GitHub event")

// errBusy is GitHub not answering now: its rate limit or an outage. It is the
// caller's to try again, never a reason to refuse the work.
var errBusy = errors.New("GitHub is not answering right now")

// ── GitHub's API ──────────────────────────────────────────────────────────────

const (
	apiHost    = "https://api.github.com"
	apiBudget  = 10 * time.Second
	apiLimit   = 4 << 20
	userAgent  = "HanzoRewards/1 (+https://hanzo.ai)"
	mimeStars  = "application/vnd.github.star+json"
	mimeGitHub = "application/vnd.github+json"
)

// pathOK is what one GitHub path may hold: segments of the characters a login or a
// repository name can have, so nothing a caller or a payload says can leave the
// path it was meant for.
var pathOK = regexp.MustCompile(`^(/[A-Za-z0-9][A-Za-z0-9._-]{0,99}|/[a-z]+)+(\?[a-z_]+=[A-Za-z0-9_]+(&[a-z_]+=[A-Za-z0-9_]+)*)?$`)

// apiFn is one GET of GitHub's REST API, a variable so a test feeds answers. It
// answers the status and the body; a transport failure is errBusy.
var apiFn = func(ctx context.Context, path, accept string) (int, []byte, error) {
	if !pathOK.MatchString(path) {
		return 0, nil, fmt.Errorf("reward: %q is not a path GitHub is asked", path)
	}
	ctx, cancel := context.WithTimeout(ctx, apiBudget)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiHost+path, nil)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	c := &http.Client{CheckRedirect: func(r *http.Request, _ []*http.Request) error {
		if r.URL.Host != "api.github.com" {
			return http.ErrUseLastResponse
		}
		return nil
	}}
	resp, err := c.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("%w: %v", errBusy, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, apiLimit))
	if err != nil {
		return 0, nil, fmt.Errorf("%w: %v", errBusy, err)
	}
	if resp.StatusCode == http.StatusTooManyRequests || (resp.StatusCode == http.StatusForbidden && resp.Header.Get("X-RateLimit-Remaining") == "0") || resp.StatusCode >= 500 {
		return resp.StatusCode, nil, fmt.Errorf("%w (%d)", errBusy, resp.StatusCode)
	}
	return resp.StatusCode, body, nil
}

// get reads path from GitHub as JSON into v and answers the check that read is:
// when it was fetched, the status, and that the answer was public (an
// unauthenticated read of a private thing is a 404). A status other than 200 is
// returned with a zero v.
func get(ctx context.Context, path, accept string, v any) (catalog.Check, error) {
	status, body, err := apiFn(ctx, path, accept)
	if err != nil {
		return catalog.Check{}, err
	}
	c := catalog.Check{FetchedAt: time.Now().UTC(), Status: status, Public: status == http.StatusOK, AuthorMatch: catalog.Unknown}
	if status == http.StatusOK && v != nil {
		if err := json.Unmarshal(body, v); err != nil {
			return c, fmt.Errorf("%w: GitHub answered something else than JSON", errBusy)
		}
	}
	return c, nil
}

// ── who ───────────────────────────────────────────────────────────────────────

// peopleFn resolves GitHub user ids to the IAM people who linked them, acting for
// the platform. A variable so a test answers it.
var peopleFn = func(ctx context.Context, ids []string) ([]client.GitHubPerson, error) {
	out, err := iampeer.IAMGitHubPeople(cloud.For(context.WithoutCancel(ctx), authz.AdminOrg), &client.GitHubPeopleIn{IDs: ids})
	if err != nil {
		return nil, err
	}
	return out.People, nil
}

// linkedFn is the GitHub user id the caller linked at sign-in, "" when they linked
// none; a variable so a test answers it.
var linkedFn = func(ctx context.Context) (string, error) {
	out, err := iampeer.IAMLinked(ctx, &client.LinkedIn{Provider: "github"})
	if err != nil {
		return "", err
	}
	return out.Subject, nil
}

// person is the IAM subject who linked GitHub account id, or "" when nobody did.
func person(ctx context.Context, id int64) (string, error) {
	if id <= 0 {
		return "", nil
	}
	ids := strconv.FormatInt(id, 10)
	ps, err := peopleFn(ctx, []string{ids})
	if err != nil {
		return "", err
	}
	for _, p := range ps {
		if p.ID == ids {
			return p.Subject, nil
		}
	}
	return "", nil
}

// ── evidence ──────────────────────────────────────────────────────────────────

const githubURL = "https://github.com/"

// seen is evidence of one check GitHub's API answered.
func seen(url string, c catalog.Check) *catalog.Evidence {
	return &catalog.Evidence{Source: "github", URL: url, Checks: []catalog.Check{c}}
}

// unread is evidence for a thing GitHub did not answer for now: staff verify it by
// hand, and no check is claimed.
func unread(url string) *catalog.Evidence {
	return &catalog.Evidence{Source: "github", URL: url, NeedsReview: true}
}

// ── deliveries ────────────────────────────────────────────────────────────────

type actor struct {
	ID    int64  `json:"id"`
	Login string `json:"login"`
	Type  string `json:"type"`
}

type labelled []struct {
	Name string `json:"name"`
}

func (l labelled) names() []string {
	out := make([]string, 0, len(l))
	for _, x := range l {
		out = append(out, strings.ToLower(strings.TrimSpace(x.Name)))
	}
	return out
}

// payload is what a delivery says of the thing it is about.
type payload struct {
	Action     string `json:"action"`
	Sender     actor  `json:"sender"`
	Repository struct {
		FullName      string `json:"full_name"`
		Private       bool   `json:"private"`
		DefaultBranch string `json:"default_branch"`
	} `json:"repository"`
	Pull *struct {
		Number            int      `json:"number"`
		Title             string   `json:"title"`
		Body              string   `json:"body"`
		Merged            bool     `json:"merged"`
		User              actor    `json:"user"`
		MergedBy          actor    `json:"merged_by"`
		AuthorAssociation string   `json:"author_association"`
		Labels            labelled `json:"labels"`
		Base              struct {
			Ref string `json:"ref"`
		} `json:"base"`
	} `json:"pull_request"`
	Issue *struct {
		Number            int              `json:"number"`
		Title             string           `json:"title"`
		User              actor            `json:"user"`
		AuthorAssociation string           `json:"author_association"`
		Labels            labelled         `json:"labels"`
		PullRequest       *json.RawMessage `json:"pull_request"`
	} `json:"issue"`
	Label struct {
		Name string `json:"name"`
	} `json:"label"`
	Forkee *struct {
		FullName string `json:"full_name"`
	} `json:"forkee"`
}

// staff are the author associations of the people who can write to a repository.
var staff = []string{"OWNER", "MEMBER", "COLLABORATOR"}

// isBot reports whether a GitHub account is no person.
func isBot(a actor) bool {
	return a.ID <= 0 || strings.EqualFold(a.Type, "Bot") || strings.HasSuffix(strings.ToLower(a.Login), "[bot]")
}

// delivery states what one verified GitHub delivery earns, and says how many events
// that was or why none.
func (s *state) delivery(ctx context.Context, event, id string, body []byte) (int, string, error) {
	var p payload
	if json.Unmarshal(body, &p) != nil || p.Repository.FullName == "" {
		return 0, "", errUnreadable
	}
	repo := strings.ToLower(p.Repository.FullName)
	owner, _, _ := strings.Cut(repo, "/")
	switch {
	case p.Repository.Private:
		return 0, "a private repository", nil
	case !ours(owner):
		return 0, "not a repository of ours", nil
	}
	switch event {
	case "pull_request":
		return s.merged(ctx, repo, p)
	case "issues":
		return s.accepted(ctx, repo, p)
	case "star", "watch":
		if p.Action != "created" && p.Action != "started" {
			return 0, "not a new star", nil
		}
		return s.starred(ctx, repo, p.Sender)
	case "fork":
		if p.Action != "" && p.Action != "created" {
			return 0, "not a new fork", nil
		}
		if p.Forkee == nil {
			return 0, "", errUnreadable
		}
		return s.forked(ctx, repo, strings.ToLower(p.Forkee.FullName), p.Sender)
	}
	return 0, "an event that earns nothing", nil
}

// bountyTier reads a pull request's or issue's labels for the tier of the bounty it completes.
func bountyTier(labels []string) string {
	for _, l := range labels {
		l = strings.ToLower(strings.TrimSpace(l))
		for _, t := range []string{"starter", "standard", "advanced"} {
			if l == "bounty-"+t || l == "bounty:"+t || l == "bounty/"+t {
				return t
			}
		}
		switch l {
		case "bounty:p0", "bounty-p0", "bounty/p0", "bounty:l", "bounty-l", "bounty/l":
			return "advanced"
		case "bounty:p1", "bounty-p1", "bounty/p1", "bounty:m", "bounty-m", "bounty/m":
			return "standard"
		case "bounty:p2", "bounty-p2", "bounty/p2", "bounty:s", "bounty-s", "bounty/s":
			return "starter"
		}
	}
	for _, l := range labels {
		if strings.EqualFold(strings.TrimSpace(l), "bounty") {
			return "standard"
		}
	}
	return ""
}

// isAcceptedLabel reports whether a label marks an issue as accepted or bounty-tagged.
func isAcceptedLabel(name string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	return name == "accepted" || bountyTier([]string{name}) != ""
}

var issueRefRE = regexp.MustCompile(`(?i)\b(?:fix(?:es|ed)?|close[sd]?|resolve[sd]?)\s+(?:([A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+)?#([1-9][0-9]*)|https://github\.com/([A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+)/issues/([1-9][0-9]*))`)

type issueRef struct {
	repo string
	num  string
}

func linkedIssues(defaultRepo, text string) []issueRef {
	var out []issueRef
	for _, m := range issueRefRE.FindAllStringSubmatch(text, 5) {
		r, n := defaultRepo, m[2]
		if m[1] != "" {
			r = strings.ToLower(m[1])
		} else if m[3] != "" {
			r, n = strings.ToLower(m[3]), m[4]
		}
		if owner, _, _ := strings.Cut(r, "/"); n != "" && ours(owner) && pathOK.MatchString("/repos/"+r+"/issues/"+n) {
			ref := issueRef{repo: r, num: n}
			if !slices.Contains(out, ref) {
				out = append(out, ref)
			}
		}
	}
	return out
}

// prKind reads a merged pull request's labels for what it is. A label that names
// none is a feature.
func prKind(labels []string) string {
	for _, k := range []string{"fix", "docs", "translation", "tests"} {
		if slices.Contains(labels, k) || (k == "fix" && slices.Contains(labels, "bug")) {
			return k
		}
	}
	return "feature"
}

// merged states a merged pull request into the default branch of a public
// repository of ours, by a person, merged by another, and not by staff. A bounty
// label (on the PR or on a linked issue it fixes) makes it the bounty and not also
// the ordinary merge: bounties replace the ordinary award for the same work, and
// also credit the external contributor who filed the linked bounty issue.
func (s *state) merged(ctx context.Context, repo string, p payload) (int, string, error) {
	pr := p.Pull
	switch {
	case pr == nil:
		return 0, "", errUnreadable
	case p.Action != "closed" || !pr.Merged:
		return 0, "not a merge", nil
	case isBot(pr.User):
		return 0, "a bot's pull request", nil
	case slices.Contains(staff, strings.ToUpper(pr.AuthorAssociation)):
		return 0, "a member's own work", nil
	case isBot(pr.MergedBy) || pr.MergedBy.ID == pr.User.ID:
		return 0, "merged by no other person", nil
	case pr.Base.Ref == "" || pr.Base.Ref != p.Repository.DefaultBranch:
		return 0, "not merged into the default branch", nil
	}
	who, err := person(ctx, pr.User.ID)
	if err != nil {
		return 0, "", err
	}
	if who == "" {
		return 0, "no Hanzo account is linked to that GitHub account", nil
	}
	num := strconv.Itoa(pr.Number)
	url := githubURL + repo + "/pull/" + num
	var got struct {
		Title  string `json:"title"`
		Body   string `json:"body"`
		Merged bool   `json:"merged"`
		User   actor  `json:"user"`
		Base   struct {
			Repo struct {
				Private bool `json:"private"`
			} `json:"repo"`
		} `json:"base"`
	}
	ev, err := s.checked(ctx, "/repos/"+repo+"/pulls/"+num, mimeGitHub, &got, url, func(c *catalog.Check) bool {
		c.Public = c.Public && !got.Base.Repo.Private
		c.AuthorMatch = catalog.No
		if got.User.ID == pr.User.ID {
			c.AuthorMatch = catalog.Yes
		}
		return got.Merged && c.Public && c.AuthorMatch == catalog.Yes
	})
	if err != nil {
		return 0, "", err
	}
	if ev == nil {
		return 0, "GitHub does not show it as a public merge by that author", nil
	}
	labels := pr.Labels.names()
	tier := bountyTier(labels)
	bountyRepo, bountyNum := repo, num
	extraEvents := 0

	for _, ref := range linkedIssues(repo, pr.Title+"\n"+pr.Body+"\n"+got.Title+"\n"+got.Body) {
		var isGot struct {
			User              actor            `json:"user"`
			AuthorAssociation string           `json:"author_association"`
			Labels            labelled         `json:"labels"`
			PullRequest       *json.RawMessage `json:"pull_request"`
		}
		isURL := githubURL + ref.repo + "/issues/" + ref.num
		isEv, isErr := s.checked(ctx, "/repos/"+ref.repo+"/issues/"+ref.num, mimeGitHub, &isGot, isURL, func(c *catalog.Check) bool {
			c.AuthorMatch = catalog.Yes
			return isGot.PullRequest == nil && slices.ContainsFunc(isGot.Labels.names(), isAcceptedLabel)
		})
		if isErr != nil || isEv == nil || isEv.NeedsReview {
			continue
		}
		isLabels := isGot.Labels.names()
		if t := bountyTier(isLabels); t != "" && tier == "" {
			tier = t
			bountyRepo, bountyNum = ref.repo, ref.num
		}
		if !isBot(isGot.User) && !slices.Contains(staff, strings.ToUpper(isGot.AuthorAssociation)) {
			if isWho, _ := person(ctx, isGot.User.ID); isWho != "" {
				n, _, _ := s.stated(ctx, catalog.Event{
					ID: "github.issue.accepted:" + ref.repo + "#" + ref.num, Type: "github.issue.accepted", Subject: isWho, At: time.Now().UTC(),
					Fields: map[string]any{"repo": ref.repo, "labels": isLabels}, Evidence: isEv,
				})
				extraEvents += n
			}
		}
	}

	e := catalog.Event{Subject: who, At: time.Now().UTC(), Evidence: ev}
	if tier != "" {
		e.ID, e.Type = "bounty.completed:"+bountyRepo+"#"+bountyNum, "bounty.completed"
		e.Fields = map[string]any{"repo": bountyRepo, "issue": bountyNum, "tier": tier}
	} else {
		e.ID, e.Type = "github.pr.merged:"+repo+"#"+num, "github.pr.merged"
		e.Fields = map[string]any{"repo": repo, "labels": labels, "kind": prKind(labels)}
	}
	n, why, err := s.stated(ctx, e)
	return n + extraEvents, why, err
}

// accepted states an issue a maintainer labelled accepted or bounty, to the person who opened
// it. The maintainer is not its author and not a bot; a pull request is no issue.
func (s *state) accepted(ctx context.Context, repo string, p payload) (int, string, error) {
	is := p.Issue
	switch {
	case is == nil:
		return 0, "", errUnreadable
	case p.Action != "labeled" || !isAcceptedLabel(p.Label.Name):
		return 0, "not an accepted label", nil
	case is.PullRequest != nil:
		return 0, "a pull request, not an issue", nil
	case isBot(is.User) || isBot(p.Sender):
		return 0, "a bot's act", nil
	case p.Sender.ID == is.User.ID:
		return 0, "the author's own act", nil
	case slices.Contains(staff, strings.ToUpper(is.AuthorAssociation)):
		return 0, "a member's own issue", nil
	}
	who, err := person(ctx, is.User.ID)
	if err != nil {
		return 0, "", err
	}
	if who == "" {
		return 0, "no Hanzo account is linked to that GitHub account", nil
	}
	num := strconv.Itoa(is.Number)
	url := githubURL + repo + "/issues/" + num
	var got struct {
		User   actor    `json:"user"`
		Labels labelled `json:"labels"`
	}
	ev, err := s.checked(ctx, "/repos/"+repo+"/issues/"+num, mimeGitHub, &got, url, func(c *catalog.Check) bool {
		c.AuthorMatch = catalog.No
		if got.User.ID == is.User.ID {
			c.AuthorMatch = catalog.Yes
		}
		return slices.ContainsFunc(got.Labels.names(), isAcceptedLabel) && c.AuthorMatch == catalog.Yes
	})
	if err != nil {
		return 0, "", err
	}
	if ev == nil {
		return 0, "GitHub does not show it accepted, opened by that author", nil
	}
	return s.stated(ctx, catalog.Event{ID: "github.issue.accepted:" + repo + "#" + num, Type: "github.issue.accepted", Subject: who, At: time.Now().UTC(),
		Fields: map[string]any{"repo": repo, "labels": got.Labels.names()}, Evidence: ev})
}

// starred states a star on a repository of ours by the person who linked the
// account. GitHub lists a person's stars newest first, so the first page holds the
// star a delivery announces.
func (s *state) starred(ctx context.Context, repo string, who actor) (int, string, error) {
	sub, err := person(ctx, who.ID)
	if err != nil || sub == "" {
		return 0, "no Hanzo account is linked to that GitHub account", err
	}
	if !pathOK.MatchString("/users/" + who.Login) {
		return 0, "", errUnreadable
	}
	var list []struct {
		Repo struct {
			FullName string `json:"full_name"`
		} `json:"repo"`
	}
	url := githubURL + repo
	ev, err := s.checked(ctx, "/users/"+who.Login+"/starred?per_page=100", mimeStars, &list, url, func(c *catalog.Check) bool {
		c.AuthorMatch = catalog.Yes // the list is the linked account's own
		return slices.ContainsFunc(list, func(x struct {
			Repo struct {
				FullName string `json:"full_name"`
			} `json:"repo"`
		}) bool {
			return strings.EqualFold(x.Repo.FullName, repo)
		})
	})
	if err != nil {
		return 0, "", err
	}
	if ev == nil {
		return 0, "GitHub does not list that star", nil
	}
	return s.stated(ctx, catalog.Event{ID: "github.star:" + repo + ":" + strconv.FormatInt(who.ID, 10), Type: "github.star", Subject: sub, At: time.Now().UTC(),
		Fields: map[string]any{"repo": repo}, Evidence: ev})
}

// forked states a fork of a repository of ours by the person who linked the
// account that made it.
func (s *state) forked(ctx context.Context, repo, fork string, who actor) (int, string, error) {
	sub, err := person(ctx, who.ID)
	if err != nil || sub == "" {
		return 0, "no Hanzo account is linked to that GitHub account", err
	}
	if !pathOK.MatchString("/repos/" + fork) {
		return 0, "", errUnreadable
	}
	var got struct {
		Fork   bool  `json:"fork"`
		Owner  actor `json:"owner"`
		Parent struct {
			FullName string `json:"full_name"`
		} `json:"parent"`
	}
	url := githubURL + fork
	ev, err := s.checked(ctx, "/repos/"+fork, mimeGitHub, &got, url, func(c *catalog.Check) bool {
		c.AuthorMatch = catalog.No
		if got.Owner.ID == who.ID {
			c.AuthorMatch = catalog.Yes
		}
		return got.Fork && strings.EqualFold(got.Parent.FullName, repo) && c.AuthorMatch == catalog.Yes
	})
	if err != nil {
		return 0, "", err
	}
	if ev == nil {
		return 0, "GitHub does not show that fork", nil
	}
	return s.stated(ctx, catalog.Event{ID: "github.fork:" + repo + ":" + strconv.FormatInt(who.ID, 10), Type: "github.fork", Subject: sub, At: time.Now().UTC(),
		Fields: map[string]any{"repo": repo, "forker": strings.ToLower(who.Login)}, Evidence: ev})
}

// checked reads path from GitHub into v, runs holds over the check that read is,
// and answers the evidence of it: nil when GitHub answered and the thing did not
// hold, needs_review evidence when GitHub could not be read now (rate limit,
// outage), so a delivery GitHub signed is never lost to a busy API and no check is
// claimed that was not run.
func (s *state) checked(ctx context.Context, path, accept string, v any, url string, holds func(*catalog.Check) bool) (*catalog.Evidence, error) {
	c, err := get(ctx, path, accept, v)
	switch {
	case errors.Is(err, errBusy):
		s.base.Log.Warn("reward: GitHub could not be read; the claim goes to review", "path", path, "err", err)
		return unread(url), nil
	case err != nil:
		return nil, err
	case c.Status != http.StatusOK:
		return nil, nil
	}
	if !holds(&c) {
		return nil, nil
	}
	return seen(url, c), nil
}

// stated sends e to the engine and reports how many claims it made.
func (s *state) stated(ctx context.Context, e catalog.Event) (int, string, error) {
	out, err := s.emit(ctx, e)
	if err != nil {
		return 0, "", err
	}
	if n := claimed(out); n > 0 {
		return n, "", nil
	}
	return 0, "no rule claims it, or it was claimed before", nil
}

// ── the member's own check ────────────────────────────────────────────────────

// readme is a repository's README as GitHub serves it.
type readme struct {
	Content  string `json:"content"`
	Encoding string `json:"encoding"`
}

// text is the README's text, or "" when it is not base64 text.
func (r readme) text() string {
	if r.Encoding != "base64" {
		return ""
	}
	b, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(r.Content, "\n", ""))
	if err != nil {
		return ""
	}
	return string(b)
}

// badgeIn names the project whose README carries the Hanzo badge.
type badgeIn struct {
	// Repo is the project as owner/name on GitHub.
	Repo string `json:"repo"`
}

// badgeOut is what the verifier found.
type badgeOut struct {
	// Repo is the project, as GitHub names it.
	Repo string `json:"repo"`
	// Badges are the kinds the README carries: profile (the live badge of a rewards
	// profile), rank (the live rank badge) and built (the static Built with Hanzo
	// badge). Each earns once for the project.
	Badges []string `json:"badges"`
	// Claimed is how many claims this check made for staff: 0 when every badge of
	// the project had been claimed already.
	Claimed int `json:"claimed"`
}

// The Hanzo badges, by the image address a README carries. The match is on the image
// inside a markdown image or an HTML img tag, and ignores the query string: a link to
// Hanzo is not a badge, and a badge's size or style parameters are the author's.
var (
	imageMarkdown = regexp.MustCompile(`!\[[^\]]*\]\(\s*<?([^)\s>]+)>?(?:\s+"[^"]*")?\s*\)`)
	imageHTML     = regexp.MustCompile(`(?is)<img\b[^>]*?\bsrc\s*=\s*["']([^"']+)["']`)
	profileImage  = regexp.MustCompile(`^/v1/reward/badge/[A-Za-z0-9_-]{1,64}\.svg$`)
	rankImage     = regexp.MustCompile(`^/v1/leaderboard/badge/[A-Za-z0-9_.-]{1,64}\.svg$`)
)

// badgeKinds reads a README for the Hanzo badges it carries: profile (the live badge
// of a rewards profile), rank (the live rank badge of an org) and built (the static
// Built with Hanzo badge, light or dark), each once however many times it appears,
// in that order. A README with none carries []. Deploy to Hanzo is not built yet and
// counts for nothing.
func badgeKinds(text string) []string {
	var found []string
	for _, m := range append(imageMarkdown.FindAllStringSubmatch(text, -1), imageHTML.FindAllStringSubmatch(text, -1)...) {
		u, err := url.Parse(strings.TrimSpace(m[1]))
		if err != nil || (u.Scheme != "https" && u.Scheme != "http" && u.Scheme != "") {
			continue
		}
		host, path := strings.ToLower(u.Hostname()), u.Path
		switch {
		case host == "api.hanzo.ai" && profileImage.MatchString(path):
			found = append(found, "profile")
		case host == "api.hanzo.ai" && rankImage.MatchString(path):
			found = append(found, "rank")
		case (host == "hanzo.ai" || host == "www.hanzo.ai") && (path == "/badges/built-with-hanzo.svg" || path == "/badges/built-with-hanzo-dark.svg"):
			found = append(found, "built")
		}
	}
	var kinds []string
	for _, want := range []string{"profile", "rank", "built"} {
		if slices.Contains(found, want) {
			kinds = append(kinds, want)
		}
	}
	return kinds
}

// badge verifies, against GitHub's API, that a project's README links Hanzo, and
// states it. The project must be public and owned by the account the caller linked
// at sign-in (a person's own repository, not an org's), and its README must carry
// the badge. Anything else is a refusal that says which.
func (s *state) badge(ctx context.Context, who, repo string) (*badgeOut, error) {
	linked, err := linkedFn(ctx)
	if err != nil {
		return nil, err
	}
	gid, _ := strconv.ParseInt(strings.TrimSpace(linked), 10, 64)
	if gid <= 0 {
		return nil, errNoGitHub
	}
	repo = strings.ToLower(strings.TrimSpace(repo))
	if !strings.Contains(repo, "/") || !pathOK.MatchString("/repos/"+repo) {
		return nil, errBadRepo
	}
	var got struct {
		ID       int64  `json:"id"`
		FullName string `json:"full_name"`
		Private  bool   `json:"private"`
		Fork     bool   `json:"fork"`
		Owner    actor  `json:"owner"`
	}
	url := githubURL + repo
	c, err := get(ctx, "/repos/"+repo, mimeGitHub, &got)
	switch {
	case err != nil:
		return nil, err
	case c.Status != http.StatusOK || got.Private:
		return nil, errNotPublic
	case got.Owner.ID != gid || !strings.EqualFold(got.Owner.Type, "User"):
		return nil, errNotOwner
	case got.Fork || got.ID <= 0:
		// A fork inherits its parent's README, so the badge it carries is not the
		// member's work.
		return nil, errNotOwner
	}
	var rm readme
	rc, err := get(ctx, "/repos/"+repo+"/readme", mimeGitHub, &rm)
	switch {
	case err != nil:
		return nil, err
	case rc.Status != http.StatusOK:
		return nil, errNoBadge
	}
	kinds := badgeKinds(rm.text())
	if len(kinds) == 0 {
		return nil, errNoBadge
	}
	rc.AuthorMatch = catalog.Yes // the owner id is the id the caller linked
	res := &badgeOut{Repo: strings.ToLower(got.FullName), Badges: kinds}
	for _, kind := range kinds {
		out, err := s.emit(ctx, catalog.Event{ID: "github.badge:" + strconv.FormatInt(got.ID, 10) + ":" + kind, Type: "github.badge", Subject: who, At: time.Now().UTC(),
			Fields: map[string]any{"repo": repo, "owner": strings.ToLower(got.Owner.Login), "badge": kind}, Evidence: seen(url, rc)})
		if err != nil {
			return nil, err
		}
		res.Claimed += claimed(out)
	}
	return res, nil
}

// checkOut is what a member's own GitHub check made claims of.
type checkOut struct {
	// Login is the GitHub account the caller linked.
	Login string `json:"login"`
	// Stars, Follows and Forks are the claims this check made, each for one
	// repository or account of ours that GitHub shows the caller has starred,
	// followed or forked, and nobody had claimed before.
	Stars   int `json:"stars"`
	Follows int `json:"follows"`
	Forks   int `json:"forks"`
}

const (
	// listPages bounds the pages of a person's stars, follows or repositories read.
	listPages = 5
	// forkProbes bounds the forks looked into for their parent in one check.
	forkProbes = 15
)

// mine reads the linked GitHub account's public stars, follows and forks from
// GitHub's API and states each that is ours. GitHub sends no delivery for a follow,
// so this is the one way one is found; stars and forks are found here for the ones
// made before the program began, and each is idempotent by the account and the
// repository, so a second check claims nothing twice.
func (s *state) mine(ctx context.Context, who string) (*checkOut, error) {
	linked, err := linkedFn(ctx)
	if err != nil {
		return nil, err
	}
	gid, _ := strconv.ParseInt(strings.TrimSpace(linked), 10, 64)
	if gid <= 0 {
		return nil, errNoGitHub
	}
	gs := strconv.FormatInt(gid, 10)
	var acct actor
	if _, err := get(ctx, "/user/"+gs, mimeGitHub, &acct); err != nil {
		return nil, err
	}
	if acct.ID != gid || !pathOK.MatchString("/users/"+acct.Login) {
		return nil, errNoGitHub
	}
	login := strings.ToLower(acct.Login)
	out := &checkOut{Login: login}
	state := func(c catalog.Check, typ, key, url string, fields map[string]any) (bool, error) {
		c.AuthorMatch = catalog.Yes // the list is the linked account's own
		res, err := s.emit(ctx, catalog.Event{ID: typ + ":" + key + ":" + gs, Type: typ, Subject: who, At: time.Now().UTC(), Fields: fields, Evidence: seen(url, c)})
		return claimed(res) > 0, err
	}

	for page := 1; page <= listPages; page++ {
		var stars []struct {
			Repo struct {
				FullName string `json:"full_name"`
			} `json:"repo"`
		}
		c, err := get(ctx, fmt.Sprintf("/users/%s/starred?per_page=100&page=%d", login, page), mimeStars, &stars)
		if err != nil {
			return out, err
		}
		for _, x := range stars {
			repo := strings.ToLower(x.Repo.FullName)
			if owner, _, _ := strings.Cut(repo, "/"); ours(owner) {
				if ok, err := state(c, "github.star", repo, githubURL+repo, map[string]any{"repo": repo}); err != nil {
					return out, err
				} else if ok {
					out.Stars++
				}
			}
		}
		if len(stars) < 100 {
			break
		}
	}
	for page := 1; page <= listPages; page++ {
		var follows []actor
		c, err := get(ctx, fmt.Sprintf("/users/%s/following?per_page=100&page=%d", login, page), mimeGitHub, &follows)
		if err != nil {
			return out, err
		}
		for _, x := range follows {
			if target := strings.ToLower(x.Login); ours(target) {
				if ok, err := state(c, "github.follow", target, githubURL+target, map[string]any{"target": target}); err != nil {
					return out, err
				} else if ok {
					out.Follows++
				}
			}
		}
		if len(follows) < 100 {
			break
		}
	}
	probes := 0
	for page := 1; page <= listPages && probes < forkProbes; page++ {
		var repos []struct {
			FullName string `json:"full_name"`
			Fork     bool   `json:"fork"`
		}
		if _, err := get(ctx, fmt.Sprintf("/users/%s/repos?type=owner&per_page=100&page=%d", login, page), mimeGitHub, &repos); err != nil {
			return out, err
		}
		for _, r := range repos {
			if !r.Fork || probes >= forkProbes || !pathOK.MatchString("/repos/"+r.FullName) {
				continue
			}
			probes++
			var one struct {
				Fork   bool  `json:"fork"`
				Owner  actor `json:"owner"`
				Parent struct {
					FullName string `json:"full_name"`
				} `json:"parent"`
			}
			c, err := get(ctx, "/repos/"+r.FullName, mimeGitHub, &one)
			if err != nil {
				return out, err
			}
			parent := strings.ToLower(one.Parent.FullName)
			if owner, _, _ := strings.Cut(parent, "/"); c.Status == http.StatusOK && one.Fork && one.Owner.ID == gid && ours(owner) {
				if ok, err := state(c, "github.fork", parent, githubURL+strings.ToLower(r.FullName), map[string]any{"repo": parent, "forker": login}); err != nil {
					return out, err
				} else if ok {
					out.Forks++
				}
			}
		}
		if len(repos) < 100 {
			break
		}
	}
	return out, nil
}
