package git

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/openapi"
	"github.com/zap-proto/zip"
)

// webhook.go is the CANONICAL-FORGE trigger of push-to-deploy: git.hanzo.ai (the
// hanzoai/git server, Service hanzo-git) POSTs every push here, we HMAC-verify it
// and hand the push to cloud.OnGitPush — the SAME single-registrant seam the
// embedded git server (smart_http.go) and the GitHub App (clients/integrations)
// fire. Third transport, one trigger; the build decision stays downstream in
// clients/platform, which is the one place that knows what a push MEANS.
//
// It exists because the canonical forge is a SEPARATE process from this binary:
// its pushes never touch our receive-pack, so without this door a push to the
// host we call canonical builds nothing and only the GitHub mirror releases.
//
// PUBLIC at the JWT layer (the forge has no Hanzo session) — auth is the
// signature, verified fail-closed here, exactly like the GitHub webhook.

const (
	// webhookSecretEnv holds the forge's shared webhook secret (KMS-synced into the
	// cloud CR env as GIT_WEBHOOK_SECRET, same as GITHUB_APP_WEBHOOK_SECRET). This
	// endpoint TRIGGERS BUILDS, so an unset secret refuses every delivery rather
	// than trusting it.
	webhookSecretEnv = "GIT_WEBHOOK_SECRET"
	// The forge's canonical header family. It also emits the X-Gitea-*/X-Gogs-*/
	// X-Hub-* aliases for third-party receivers written against those servers; we
	// read the one spelling that is ours.
	eventHeader = "X-Git-Event"
	sigHeader   = "X-Git-Signature"
	// zeroSHA is git's all-zero object id — the `after` of a ref delete.
	zeroSHA = "0000000000000000000000000000000000000000"
	// gitMaxWebhookBody bounds the payload we read + sign over. A hostile/oversized
	// body can neither exhaust memory nor slip past the HMAC (we verify exactly the
	// bytes we act on).
	gitMaxWebhookBody = 8 << 20 // 8 MiB
)

// "Cannot be a typed op" is not "must be undocumented". Every route in git's
// untypedByDesign list (typed_wire_test.go) published an operationId, tags and
// NOTHING ELSE, which no consumer of the document can tell apart from a route
// that takes no body — so every generated SDK offered a forge webhook with
// nowhere to put the delivery, and three ZAP procedures with nowhere to put the
// repo name. openapi.Register states the request each one really reads, keyed by
// the router's own pattern, without touching the route: pure DESCRIPTION, no
// status, field or byte moves, and the reasons those routes stay raw are
// untouched by it.
//
// What is declared, and what is deliberately not:
//
//   - the webhook reads a forge push envelope and answers 204 on every success
//     path (c.NoContent throughout), so its request is pushEvent and it has no
//     response body to state.
//   - the two pack POSTs read an application/x-git-*-request pack stream:
//     openapi.Binary, the same declaration a receipt upload gets. Their root-host
//     twins are separate router patterns and are declared separately.
//   - createRepo/getRepo/deleteRepo bind zapProcReq; listRepos and usage read NO
//     body at all (zap.go), so declaring one for them would be a fresh falsehood
//     in place of the silence.
//   - no RESPONSE is declared for the ZAP five. Their envelope is real
//     ({status, msg, data}) but its data is repoView / []repoView / usageView,
//     names zip's typed fold ALREADY publishes as components off the /v1 ops —
//     so reflecting them here a second time would put two derivations behind one
//     schema name, which is the collision openapi.Weave exists to refuse. One
//     name, one shape, one generator: the envelope waits until the two seams
//     agree on who owns a shared view type.
//   - the pack responses and the twelve HTML pages have no declaration to make:
//     openapi.Binary is request-only by design, and a text/html response is the
//     second half it deliberately does not invent.
//
// The PROSE those same routes were also missing is a separate seam
// (openapi.Describe) and lives beside each route's own registration, not here:
// the twelve smart-HTTP operations in smart_http.go, the five ZAP procedures in
// zap.go, the twelve pages in ui.go. Only the webhook's prose is stated here,
// because this is where the webhook is declared. Splitting a family's prose by
// which half of it happens to have a declarable body would put one thing in two
// places; a Register and a Describe for the SAME route stay together.
//
// init, not routes(): Register panics on a duplicate declaration and routes()
// runs once per Mount.
func init() {
	openapi.Register("/v1/git/webhook", "POST", pushEvent{}, nil)
	openapi.Describe("/v1/git/webhook", "POST",
		"Receive a push from the canonical forge and trigger its build",
		"The canonical forge's push-to-deploy door. git.hanzo.ai runs as a SEPARATE "+
			"process, so its pushes never reach this binary's receive-pack; without this "+
			"a push to the host we call canonical would build nothing. A verified push is "+
			"handed to the same single trigger the embedded git server and the GitHub App "+
			"fire, and the build decision itself stays downstream in the one place that "+
			"knows what a push means.\n\n"+
			"PUBLIC at the JWT layer, because the forge carries no Hanzo session: "+
			"AUTHENTICATION IS THE SIGNATURE. The HMAC covers the raw bytes and is "+
			"verified BEFORE the payload is parsed, so an unauthenticated body is never "+
			"decoded — which is also why this cannot be a typed op, since a typed op "+
			"decodes first. An UNSET webhook secret refuses every delivery rather than "+
			"trusting it: a door that starts builds fails closed. A bad signature is 401, "+
			"a payload over 8 MiB is 413, and a malformed one 400.\n\n"+
			"Every success answers 204 and no body, including the deliveries it "+
			"deliberately ignores: a non-push event, a payload whose ref is not a ref, a "+
			"ref DELETE (a zero `after` has no commit to build), and a BOT-authored push "+
			"— release automation pushes as the forge's own actions user, and a release "+
			"must never rebuild itself. Every other ref reaches the builder, branches and "+
			"tags alike, because releases are cut by tag and filtering here would silently "+
			"stop publishing. A trigger that fails is logged rather than returned, so a "+
			"push that already landed on the forge is not retried against us.")

	openapi.Register("/v1/git/zap/createRepo", "POST", zapProcReq{}, nil)
	openapi.Register("/v1/git/zap/getRepo", "POST", zapProcReq{}, nil)
	openapi.Register("/v1/git/zap/deleteRepo", "POST", zapProcReq{}, nil)

	openapi.Register("/v1/git/:org/:repo/git-upload-pack", "POST", openapi.Binary{}, nil)
	openapi.Register("/v1/git/:org/:repo/git-receive-pack", "POST", openapi.Binary{}, nil)
	openapi.Register("/:org/:repo/git-upload-pack", "POST", openapi.Binary{}, nil)
	openapi.Register("/:org/:repo/git-receive-pack", "POST", openapi.Binary{}, nil)

	// The same pack streams for a project-scoped repo, which names its project as
	// a middle segment because a git client has no header to carry it.
	openapi.Register("/v1/git/:org/:project/:repo/git-upload-pack", "POST", openapi.Binary{}, nil)
	openapi.Register("/v1/git/:org/:project/:repo/git-receive-pack", "POST", openapi.Binary{}, nil)
	openapi.Register("/:org/:project/:repo/git-upload-pack", "POST", openapi.Binary{}, nil)
	openapi.Register("/:org/:project/:repo/git-receive-pack", "POST", openapi.Binary{}, nil)
}

// pushEvent is the subset of the forge's push payload we act on. Owner and pusher
// each accept both spellings the payload has carried across versions (login vs
// username); first non-empty wins.
type pushEvent struct {
	Ref        string `json:"ref"`
	After      string `json:"after"`
	Repository struct {
		Name     string `json:"name"`
		CloneURL string `json:"clone_url"`
		Owner    struct {
			Login    string `json:"login"`
			Username string `json:"username"`
		} `json:"owner"`
	} `json:"repository"`
	Pusher struct {
		Login    string `json:"login"`
		Username string `json:"username"`
	} `json:"pusher"`
}

// webhook verifies + processes an inbound forge webhook. It ALWAYS answers a benign
// 204 for deliveries it does not act on (non-push event, non-ref, ref delete, bot
// author) so the forge does not retry-storm — only a bad signature (401) and a
// malformed body (400) are non-2xx.
func webhook(s *cloud.Service[state], c *zip.Ctx) error {
	body := c.Body()
	if len(body) > gitMaxWebhookBody {
		return zip.Errorf(http.StatusRequestEntityTooLarge, "payload too large")
	}
	// Verify BEFORE parse so an unauthenticated body is never decoded.
	if !validSignature(strings.TrimSpace(os.Getenv(webhookSecretEnv)), c.Header(sigHeader), body) {
		return zip.Errorf(http.StatusUnauthorized, "invalid signature")
	}
	if c.Header(eventHeader) != "push" {
		return c.NoContent(http.StatusNoContent)
	}

	var ev pushEvent
	if err := json.Unmarshal(body, &ev); err != nil {
		return zip.ErrBadRequest("invalid push payload")
	}
	// EVERY ref reaches the builder — branches and tags alike, matching the GitHub
	// path. Releases are cut by tag, so a filter here silently stops publishing.
	if !strings.HasPrefix(ev.Ref, "refs/") {
		return c.NoContent(http.StatusNoContent)
	}
	// A zero `after` is a ref delete: nothing to build at a commit that is gone.
	if ev.After == "" || ev.After == zeroSHA {
		return c.NoContent(http.StatusNoContent)
	}
	org := firstNonEmptyStr(ev.Repository.Owner.Login, ev.Repository.Owner.Username)
	if org == "" || ev.Repository.Name == "" {
		return zip.ErrBadRequest("missing repository owner or name")
	}
	// Bot-authored pushes are excluded by the same guard the GitHub path uses: our
	// release automation pushes AS the forge's Actions user, and a release must
	// never rebuild itself.
	if cloud.IsBotActor(firstNonEmptyStr(ev.Pusher.Login, ev.Pusher.Username)) {
		return c.NoContent(http.StatusNoContent)
	}

	// Best-effort by the seam's contract: the push already landed on the forge, so a
	// trigger failure is logged, never answered as an error the forge would retry.
	if err := cloud.OnGitPush(c.Context(), cloud.GitPushEvent{
		Org: org, Repo: ev.Repository.Name, Ref: ev.Ref,
		Commit: ev.After, CloneURL: ev.Repository.CloneURL,
	}); err != nil {
		s.Log.Warn("forge push: build trigger failed",
			"org", org, "repo", ev.Repository.Name, "ref", ev.Ref, "err", err)
	}
	return c.NoContent(http.StatusNoContent)
}

// validSignature reports whether sigHex is the hex HMAC-SHA256 of body under
// secret, compared in constant time. An empty secret or malformed hex is false
// (fail-closed) — never a bypass.
func validSignature(secret, sigHex string, body []byte) bool {
	if secret == "" || sigHex == "" {
		return false
	}
	sig, err := hex.DecodeString(sigHex)
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hmac.Equal(sig, mac.Sum(nil))
}
