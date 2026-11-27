package crawl

// POST /v1/crawl/sign signs a request for an agent service that holds no key
// (hanzo-inc/cloud#423): the hanzo.bot gateway reads pages from its own pod, out of
// reach of the plane, and the key stays in KMS. The caller names the host, gets the
// three Web Bot Auth headers back, and attaches them. Who may ask is one fixed
// list of the platform's own applications (agents), so adding one is a reviewed change.

import (
	"context"
	"errors"
	"net/http"

	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/botauth"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/zap-proto/zip"
)

// SignPath is where a service asks for a signature.
const SignPath = Path + "/sign"

// agents is every application that may ask this deployment to sign, by brand and
// IAM client id. hanzo-bot is the hanzo.bot gateway's own identity (universe
// iam-provision.yaml: app `bot` of org hanzo, grant client_credentials), the one
// it already presents to cloud's billing.
var agents = map[string][]string{
	"hanzo": {"hanzo-bot"},
}

// agentSignRequest names the request to sign.
type agentSignRequest struct {
	// Authority is where the request goes: its host and, optionally, its port
	// (RFC 9421 @authority), e.g. `example.com` or `example.com:8443`. No scheme,
	// path or userinfo: nothing of the request but its host reaches this op.
	Authority string `json:"authority"`
	// Scheme is the request's scheme, `https` (the default) or `http`. It says
	// which port is the default, so `example.com:443` over https is signed as
	// `example.com`, the authority a verifier reads.
	Scheme string `json:"scheme,omitempty"`
}

// agentSignature is the three headers the request carries, verbatim.
type agentSignature struct {
	// SignatureAgent is the Signature-Agent header: the URL of the key directory
	// a verifier reads, as a structured-field string.
	SignatureAgent string `json:"signatureAgent"`
	// SignatureInput is the Signature-Input header.
	SignatureInput string `json:"signatureInput"`
	// Signature is the Signature header.
	Signature string `json:"signature"`
	// Expires is when the signature stops verifying, in Unix seconds. One
	// signature is for one request: ask again for the next, a redirect included.
	Expires int64 `json:"expires"`
}

// mayAsk reports whether the request's verified caller is one of [agents]: an
// application acting as itself, of this deployment's brand, issued by that
// brand's IAM, in its own org and switched into no other, and acting for nobody.
func mayAsk(c *zip.Ctx, brand string) bool {
	p, ok := principal.Minted(c)
	if !ok || p.Kind != principal.App || p.Act.Sub != "" || p.Org != brand || principal.Owner(c) != brand {
		return false
	}
	if issuer, ok := principal.Brand(c); !ok || issuer != brand {
		return false
	}
	for _, id := range agents[brand] {
		if p.Holder == id {
			return true
		}
	}
	return false
}

// signForAgent answers the Signature-Agent, Signature-Input and Signature headers for
// one request to the named authority, good for five minutes; a redirect is a new
// request. 403 for any caller not on the list, 400 for an authority that is not a
// host and optional port, 503 when no signing key can be read.
func signForAgent(ctx context.Context, in *agentSignRequest) (*agentSignature, error) {
	c, ok := cloud.Request(ctx)
	if !ok || !mayAsk(c, cloud.Brand()) {
		return nil, zip.ErrForbidden("only the platform's own agent applications may ask for a signature")
	}
	auth, ok := botauth.Authority(in.Authority)
	if !ok {
		return nil, zip.ErrBadRequest("authority must be a host and an optional port, like example.com or example.com:8443")
	}
	scheme := in.Scheme
	if scheme == "" {
		scheme = "https"
	}
	if scheme != "https" && scheme != "http" {
		return nil, zip.ErrBadRequest("scheme must be https or http")
	}
	s := botauth.Default()
	if s == nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "this deployment signs no requests")
	}
	h, expires, err := s.SignFor(ctx, scheme, auth)
	if err != nil {
		if errors.Is(err, botauth.ErrNoKeys) {
			return nil, zip.Errorf(http.StatusServiceUnavailable, "this deployment holds no signing key yet")
		}
		return nil, zip.Errorf(http.StatusServiceUnavailable, "the signing keys cannot be read right now")
	}
	return &agentSignature{
		SignatureAgent: h.Get("Signature-Agent"),
		SignatureInput: h.Get("Signature-Input"),
		Signature:      h.Get("Signature"),
		Expires:        expires.Unix(),
	}, nil
}

// useSign registers [SignPath].
func useSign(app cloud.Router) {
	app.Group(SignPath).Post("", signForAgent,
		zip.WithOperationID("sign_agent_request"),
		zip.WithSummary("Sign a request a Hanzo agent service sends itself"))
}
