package platform

// checkout.go is the one credential a customer's build of a PRIVATE GitHub
// repository is handed (#766): a GitHub App installation token that reads that
// repository's contents and nothing else, minted for this build, carried in a
// Secret the build's Job owns, and revoked when the Job ends.
//
//	minted    by provider (integrations_github_run, read), as the caller: their
//	          own GitHub can push to the repository, or they administer the org
//	          whose installation reaches it — asked at the mint and audited there
//	scoped    by GitHub: one repository, contents:read (plus the metadata:read
//	          GitHub adds to every token); provider checks GitHub's answer
//	delivered as buildkit's per-host git credential, GIT_AUTH_TOKEN.github.com,
//	          so the clone of the context presents it to github.com alone
//	ended     by a DELETE /installation/token once the Job is done, and by
//	          GitHub within the hour whatever happens here
//
// The Dockerfile the build runs can read it. Any RUN step can mount a declared
// secret, and the customer's solve shares buildkitd's user and PID namespaces
// (customerJob). So the token is scoped to what the person who could have written
// that Dockerfile can already do — read this one repository — and to nothing a
// build of another repository or another org could use.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/hanzoai/cloud/client"
	providerpeer "github.com/hanzoai/cloud/client/provider"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/env"
	"github.com/zap-proto/zip"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// gitCheckout is one build's read token and the repository it reads. Its String
// names the repository and never the token.
type gitCheckout struct {
	owner, repo string
	token       string
}

func (g *gitCheckout) String() string { return "github.com/" + g.owner + "/" + g.repo }

// clones reports whether repoURL — a validated build URL — is the repository g
// reads: https on github.com, owner/name case-folded, `.git` optional.
func (g *gitCheckout) clones(repoURL string) bool {
	rest, ok := strings.CutPrefix(strings.ToLower(strings.TrimSpace(repoURL)), "https://github.com/")
	if !ok {
		return false
	}
	return strings.TrimSuffix(strings.TrimSuffix(rest, "/"), ".git") == strings.ToLower(g.owner+"/"+g.repo)
}

// checkoutID is the build-secret id buildkit's git source reads the credential
// for github.com from. The per-host form keeps it from every other host a
// Dockerfile names.
const checkoutID = "GIT_AUTH_TOKEN.github.com"

// checkoutLife is how long a minted token can live: GitHub expires an
// installation token an hour after the mint, so its revoker waits no longer.
const checkoutLife = time.Hour

// checkoutSecretName is the Secret one build Job's token rides in: named for the
// Job, so two builds never share one and `get job` shows no credential.
func checkoutSecretName(jobName string) string { return jobName + "-checkout" }

// checkoutSecret is the token as the solve is handed it. Required, not optional:
// a pod that starts before its Secret exists waits for it, and a clone with no
// credential would fail as a missing repository.
func checkoutSecret(jobName string) solveSecret {
	return solveSecret{id: checkoutID, secret: checkoutSecretName(jobName), key: "token", perBuild: true}
}

// mintCheckout asks provider, as the caller, for a token that reads owner/repo.
// The refusal keeps provider's status and says what it was for.
func mintCheckout(ctx context.Context, owner, repo string) (*gitCheckout, error) {
	ctx, cancel := context.WithTimeout(ctx, grantBudget)
	defer cancel()
	out, err := providerpeer.IntegrationsGitHubRun(ctx, &client.GitHubRunIn{Owner: owner, Repo: repo, Read: true})
	if err != nil {
		return nil, checkoutRefusal(owner, repo, err)
	}
	if out == nil || strings.TrimSpace(out.Token) == "" {
		return nil, zip.Errorf(http.StatusBadGateway, "building the private repository %s/%s: GitHub minted no read token", owner, repo)
	}
	g := &gitCheckout{owner: out.Owner, repo: out.Repo, token: out.Token}
	if !strings.EqualFold(out.Owner+"/"+out.Repo, owner+"/"+repo) {
		// The lookup and the mint disagree about where the repository lives, so
		// the token reads something other than what this build clones.
		_ = revokeCheckout(context.WithoutCancel(ctx), g)
		return nil, zip.Errorf(http.StatusConflict, "building the private repository %s/%s: GitHub answers it as %s/%s now; deploy it again",
			owner, repo, out.Owner, out.Repo)
	}
	return g, nil
}

// checkoutRefusal is githubRefusal for the mint: a refusal keeps its 4xx, and a
// deployment that cannot ask is 503.
func checkoutRefusal(owner, repo string, err error) error {
	var he *zip.HTTPError
	switch {
	case errors.Is(err, cloud.ErrNoPeer):
		return zip.Errorf(http.StatusServiceUnavailable, "building the private repository %s/%s: GitHub is not reachable from this deployment", owner, repo)
	case errors.As(err, &he) && he.Status < 500:
		return zip.Errorf(he.Status, "building the private repository %s/%s needs a token that reads it: %s", owner, repo, he.Error())
	}
	return zip.Errorf(http.StatusBadGateway, "building the private repository %s/%s: mint a read token: %v", owner, repo, err)
}

// writeCheckout writes g beside job, in a Secret job owns, so the garbage
// collector deletes it with the Job. cloud may create Secrets in the build
// namespace and nothing else there — no get, no update, no delete — so this is
// one create, and a name already taken is a failure, never an overwrite.
func (k *k8sClient) writeCheckout(ctx context.Context, job *unstructured.Unstructured, g *gitCheckout) error {
	secret := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "Secret",
		"metadata": map[string]any{
			"name":      checkoutSecretName(job.GetName()),
			"namespace": k.buildNS,
			"labels": map[string]any{
				"hanzo.ai/managed-by": "platform",
				"hanzo.ai/build-id":   job.GetLabels()["hanzo.ai/build-id"],
			},
			"ownerReferences": []any{map[string]any{
				"apiVersion": "batch/v1", "kind": "Job",
				"name": job.GetName(), "uid": string(job.GetUID()),
			}},
		},
		"type":       "Opaque",
		"stringData": map[string]any{"token": g.token},
	}}
	_, err := k.dyn.Resource(secretsGVR).Namespace(k.buildNS).Create(ctx, secret, metav1.CreateOptions{})
	switch {
	case err == nil:
		return nil
	case apierrors.IsForbidden(err):
		return fmt.Errorf("%w: this deployment may not create the build's credential Secret in %s", errNoCheckout, k.buildNS)
	}
	return fmt.Errorf("%w: write the build's credential Secret: %v", errNoCheckout, err)
}

// errNoCheckout is a build that could not be handed its token. The Job was
// deleted with it; the deploy answers 503.
var errNoCheckout = errors.New("the build could not be handed a read token for its repository")

// parseGitHubRepo parses an https://github.com/<owner>/<repo>[.git] URL into its
// owner and repository name, rejecting credentials, ports, queries, fragments or
// extra path segments.
func parseGitHubRepo(repoURL string) (owner, repo string, ok bool) {
	u, err := url.Parse(strings.TrimSpace(repoURL))
	if err != nil || u.Scheme != "https" || u.User != nil || !strings.EqualFold(u.Host, "github.com") ||
		u.RawQuery != "" || u.Fragment != "" {
		return "", "", false
	}
	parts := strings.Split(strings.TrimSuffix(strings.TrimSuffix(strings.TrimPrefix(u.Path, "/"), "/"), ".git"), "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	return parts[0], parts[1], true
}

// mintEstateCheckout asks provider, on behalf of org's own GitHub App installation,
// for a short-lived token that reads owner/repo and nothing else (contents:read).
// Used by POST /v1/build on the estate lane when building an org-owned private
// GitHub repository that does not need the standing cross-repo gh-read secret.
func mintEstateCheckout(ctx context.Context, org, owner, repo string) (*gitCheckout, error) {
	gctx, cancel := context.WithTimeout(cloud.For(context.Background(), org), grantBudget)
	defer cancel()
	defer context.AfterFunc(ctx, cancel)()
	out, err := providerpeer.IntegrationsGitHubGrant(gctx, &client.GitHubGrantIn{Owner: owner, Repo: repo, Read: true})
	if err != nil {
		return nil, checkoutRefusal(owner, repo, err)
	}
	if out == nil || strings.TrimSpace(out.Token) == "" {
		return nil, zip.Errorf(http.StatusBadGateway, "building %s/%s: GitHub minted no read token", owner, repo)
	}
	g := &gitCheckout{owner: out.Owner, repo: out.Repo, token: out.Token}
	if !strings.EqualFold(out.Owner+"/"+out.Repo, owner+"/"+repo) {
		_ = revokeCheckout(context.WithoutCancel(ctx), g)
		return nil, zip.Errorf(http.StatusConflict, "building %s/%s: GitHub answers it as %s/%s now", owner, repo, out.Owner, out.Repo)
	}
	return g, nil
}

// revokeWhenDone ends g once the build Job it was minted for is done or gone,
// and at checkoutLife whatever the Job is doing. A revocation lost to a restart
// leaves the token to GitHub's own expiry, within the hour.
func (k *k8sClient) revokeWhenDone(jobName string, g *gitCheckout, logf func(msg string, kv ...any)) {
	k.revokeWhenDoneAll([]string{jobName}, g, logf)
}

// revokeWhenDoneAll ends g once every Job in jobNames is done or gone, or at
// checkoutLife. A multi-platform fan-out shares one read-only token across its
// arch halves, so revoking only after every half finishes keeps the second half
// from losing its clone credential when the first half finishes early.
func (k *k8sClient) revokeWhenDoneAll(jobNames []string, g *gitCheckout, logf func(msg string, kv ...any)) {
	wait, poll := cmpDur(k.checkoutWait, checkoutLife), cmpDur(k.checkoutPoll, jobPollInterval)
	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()
	t := time.NewTicker(poll)
	defer t.Stop()
	for {
		allDone := true
		for _, jobName := range jobNames {
			done, _, err := k.jobResult(ctx, jobName)
			if !done && !apierrors.IsNotFound(err) {
				allDone = false
				break
			}
		}
		if allDone || ctx.Err() != nil {
			break
		}
		select {
		case <-ctx.Done():
		case <-t.C:
		}
	}
	if err := revokeCheckout(context.Background(), g); err != nil && logf != nil {
		logf("revoke a build's checkout token (GitHub expires it within the hour)", "jobs", strings.Join(jobNames, ","), "repo", g.String(), "err", err)
	}
}

// revokeCheckout ends g now, with itself. GITHUB_API_URL points it elsewhere,
// as it does the rest of this package's GitHub calls.
func revokeCheckout(ctx context.Context, g *gitCheckout) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	base := strings.TrimRight(env.Or("GITHUB_API_URL", "https://api.github.com"), "/")
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, base+"/installation/token", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+g.token)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("revoke: request failed")
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusUnauthorized {
		return fmt.Errorf("revoke: GitHub answered %d", resp.StatusCode)
	}
	return nil
}

func cmpDur(d, def time.Duration) time.Duration {
	if d > 0 {
		return d
	}
	return def
}
