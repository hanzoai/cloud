package cloud

// confined.go — what a delegated token may reach.
//
// A coding run works for a person inside a sandbox that executes a model's
// output, and its harness needs a credential for its model calls. IAM mints one
// per run (iam LLM.md "Delegation"): the person's token, in the one org the run
// bills, naming cloud in `act`, with scope ai:inference, dying with the run
// (apps/coding key.go asks for it). Whatever that token reaches, the model's
// output reaches. So it reaches the model and nothing else.
//
// THE RULE IS THE SCOPE, NOT THE ACTOR. A token whose scope names ai:inference is
// CONFINED whoever minted it and however: a scope only narrows, so a person who
// asks for it at sign-in confines their own token and gains nothing, and no claim
// beside it can widen it.
//
// DECIDED AT THE BOUNDARY, before any subsystem sees the request, and the doors
// are NOT deferred. A key's grant lets MCP, the call plane and the graph through
// to be judged at the op-invoke seam, on the operation reached. No operation
// behind a door is a model call, so a confined token is refused at the door itself
// and the seam never has to know it exists.
//
// SIGNED BY A KEY ONLY THIS DEPLOYMENT HOLDS. IAM signs a delegated token with a
// key the public JWKS never carries, so every other verifier refuses it without
// knowing what it is; this deployment reads that key from IAM with its own client
// credential (delegation.go) and verifies a confined token against it and nothing
// else, addressed to its own API origin and naming the actor that delegated.
//
// ALL OR NOTHING. A confined token that does not verify — expired, above all,
// which is how every one of them ends — is refused rather than continuing
// anonymous (confinedRefusal). Anonymous would reach every public address, and the
// sandbox that held it should learn its run is over, not that it has become nobody.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/hanzoai/ai/retrieval"
	"github.com/zap-proto/zip"
)

// inferenceScope is IAM's schema.Inference, the scope of a delegated token. This
// binary pins an iam that predates it, so the word is stated here; it is the
// issuer's word, and confinedScope below is the one reading of it here.
const (
	inferenceScope   = "ai:inference"
	researchScope    = "research"
	workflowGPUScope = "workflow:gpu"
)

// confinedScope reports whether scope — the space-separated OAuth list — names
// inferenceScope.
func confinedScope(scope string) bool {
	return slices.Contains(strings.Fields(scope), inferenceScope)
}

func hasConfinedSubscope(scope, target string) bool {
	return slices.Contains(strings.Fields(scope), target)
}

// modelCalls is every address a confined token reaches: what a harness calls for
// its model — dev and codex speak Responses, claude speaks Messages and counts its
// tokens first, an interpreter's script speaks Chat Completions, Kai's controller
// speaks Decisions — and the model catalogue they read. Exact addresses as the
// router matches them (RoutePath), each with the one method that serves it; the
// catalogue's subtree is reads only, because a POST there asks for model access
// in the person's name.
//
// It is narrower than spend.go's inference set on purpose. That set is what costs
// money; this is what a run's harness needs, and every address beyond it is one
// more thing a model's output could do in the person's name.
var modelCalls = []struct{ method, path string }{
	{http.MethodPost, "/v1/responses"},
	{http.MethodPost, "/v1/chat/completions"},
	{http.MethodPost, "/v1/messages"},
	{http.MethodPost, "/v1/messages/count_tokens"},
	{http.MethodPost, "/v1/decisions"},
}

// catalogue is the model list and everything under it, read-only.
const catalogue = "/v1/models"

// inferenceOnly is the boundary's answer for a plain ai:inference confined token
// at path (already RoutePath) by method: nil, or the 403 that says what the
// token was for.
func inferenceOnly(path, method string) error {
	return confinedAllowed(inferenceScope, path, method)
}

// confinedAllowed evaluates whether a confined delegated token carrying scope
// may reach path (already RoutePath) by method.
//
//   - Every confined token reaches modelCalls and read-only /v1/models.
//   - A token additionally delegated `research` reaches org-scoped /v1/research/*
//     (experiments, runs, benchmarks, hypotheses, claims, candidates, artifacts,
//     evidence, regressions, xp), while visibility/consent grants (/v1/research/grants)
//     and action authorization (/v1/research/actions/.../authorize) remain blocked.
//   - A token additionally delegated `workflow:gpu` reaches org-scoped
//     /v1/workflow/namespaces/gpu-jobs/* and /v1/compute/fleet/*, while every other
//     workflow namespace, machine/cluster mutation, KMS, IAM, billing, and MCP/call
//     doors remain blocked.
func confinedAllowed(scope, path, method string) error {
	for _, a := range modelCalls {
		if path == a.path && method == a.method {
			return nil
		}
	}
	if UnderPrefix(path, catalogue) && (method == http.MethodGet || method == http.MethodHead) {
		return nil
	}
	readOrPost := method == http.MethodGet || method == http.MethodHead || method == http.MethodPost
	if hasConfinedSubscope(scope, researchScope) && strings.HasPrefix(path, "/v1/research/") && readOrPost {
		if !UnderPrefix(path, "/v1/research/grants") &&
			!(strings.HasPrefix(path, "/v1/research/actions/") && strings.HasSuffix(path, "/authorize")) {
			return nil
		}
	}
	if hasConfinedSubscope(scope, workflowGPUScope) && readOrPost {
		if strings.HasPrefix(path, "/v1/workflow/namespaces/gpu-jobs/") {
			return nil
		}
		if path == "/v1/compute/fleet" && (method == http.MethodGet || method == http.MethodHead) {
			return nil
		}
		if strings.HasPrefix(path, "/v1/compute/fleet/") {
			return nil
		}
	}
	return zip.Errorf(http.StatusForbidden, "this token was delegated for model calls only; it may not reach %s %s", method, path)
}

// noRetrieval refuses any confined request (model call or autonomous science
// endpoint) that asks the gateway to search the org's document index into the
// prompt. A request that reads the org's documents is a read of them, and what a
// sandbox asks for in the person's name must not include the tenant's knowledge base.
//
// The question is ai's own (retrieval.Asked, the leaf its controllers answer it
// with), asked of the same header and body, so this refuses exactly what would
// have turned retrieval on there — and a spelling ai learns later is refused here
// the day it ships, with no copy to fall behind.
func noRetrieval(z *zip.Ctx) error {
	if retrieval.Asked(func(name string) string { return z.Header(name) }, z.Body()) {
		return zip.Errorf(http.StatusForbidden,
			"this token was delegated for model calls only; it may not search the org's documents (retrieval)")
	}
	return nil
}

// confinedRefusal answers a confined token that was not honoured, saying which of
// the things happened, because they ask for different responses: IAM could not
// be read — its delegation key, or whether the grant stands (retry, nothing is
// wrong with the token); the token expired or was revoked (the run it was minted
// for is over); or it does not verify (it was not minted by a delegation to this
// API, and no retry will change that).
func confinedRefusal(err error) error {
	switch {
	case errors.Is(err, errDelegationKeys):
		return zip.Errorf(http.StatusServiceUnavailable,
			"delegated tokens cannot be verified right now: IAM's delegation keys are unreachable; retry")
	case errors.Is(err, errLivenessUnsure):
		return zip.Errorf(http.StatusServiceUnavailable,
			"cannot confirm this delegated token is still live: IAM is unreachable; retry")
	case errors.Is(err, jwt.ErrTokenExpired):
		return zip.Errorf(http.StatusUnauthorized, "this delegated token has expired; the run it was minted for is over")
	case errors.Is(err, errRevoked):
		return zip.Errorf(http.StatusUnauthorized, "this delegated token was revoked; the run it was minted for is over")
	}
	return zip.Errorf(http.StatusUnauthorized,
		"this delegated token does not verify: it was not signed by IAM's delegation key for this API")
}

// presentsConfined reports whether a token that did NOT verify says it is
// confined. Its payload is read unverified, which is safe for exactly one use:
// deciding a refusal. A forger who writes the scope into a payload buys a 401 for
// a request that would otherwise have gone on as nobody, and nothing else.
func presentsConfined(tok string) bool {
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		return false
	}
	b, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return false
	}
	var p struct {
		Scope string `json:"scope"`
	}
	return json.Unmarshal(b, &p) == nil && confinedScope(p.Scope)
}

// warden is the identity validator this process's boundary runs
// (SanitizeIdentity installs it), so a reader behind the boundary asks the same
// verifier the boundary asked.
var warden atomic.Pointer[Boundary]

// Confined is this process's verdict on a confined token: nil when it is a
// delegated token this deployment's boundary verifies — under IAM's delegation
// key, addressed to this API, naming its actor, its grant still standing — and
// otherwise why not.
//
// It exists for hanzoai/ai, mounted in this process (apps/ai installs it as the
// module's confined verifier). ai re-reads the bearer on every model call, and
// what it verifies a JWT against is the public JWKS, which never carries the
// delegation key — so it would refuse every delegated token the boundary had just
// admitted. Rather than hand ai the key, which would make two verifiers for one
// token, ai asks this one about the exact bytes it is about to believe. A token
// the boundary verified on this request is in its memo, so the answer costs a
// lookup; anything else is verified here in full.
func Confined(token string) error {
	v := warden.Load()
	if v == nil {
		return errors.New("identity: this process runs no identity boundary to verify a delegated token")
	}
	claims := v.claims.get(token, time.Now())
	if claims == nil {
		var err error
		if claims, err = v.validate(token); err != nil {
			return confinedRefusal(err)
		}
		v.claims.put(token, claims)
	}
	if !claims.confined {
		return errors.New("identity: not a delegated token")
	}
	if err := v.live.check(context.Background(), token, claims.ExpiresAt); err != nil {
		return confinedRefusal(err)
	}
	return nil
}
