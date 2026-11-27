package crawl

// THE CRAWLER'S KEYS (hanzo-inc/cloud#423). Every page this deployment's agents
// fetch is signed with Web Bot Auth (sdk/botauth); this answers the address a site
// reads to verify the signature, /.well-known/http-message-signatures-directory,
// with the public half of each key in the deployment's set.
//
// It is crawl's because crawl is the process that signs, so it already holds the
// key the directory must prove it holds: each published key signs the response
// for the host it was asked on. The door the host answers its other well-known
// documents from (openapi/agent.go) runs in the light host, which holds no key and
// must not; so the manifest names this one address for crawl, exactly as it names
// iam's three for iam.
//
// The first request a deployment answers here mints its key into KMS when KMS
// holds none (Signer.Provision), which is how the key is made where it is first
// published. Rotation is an edit of the stored set; see sdk/botauth.

import (
	"net/http"
	"strconv"

	luxlog "github.com/luxfi/log"

	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/botauth"
	"github.com/hanzoai/cloud/sdk/spec"
	"github.com/zap-proto/zip"
)

func init() {
	// No credential: a site verifying a signature reads this before it knows
	// anything about the caller, exactly as the door's well-known documents are read.
	spec.Open(botauth.Path, http.MethodGet)
	// A raw route, so zipdoc has no doc comment to lift; the prose is declared here.
	spec.Describe(botauth.Path, http.MethodGet,
		"The keys this deployment's agents sign their web requests with",
		"The Web Bot Auth key directory (HTTP Message Signatures directory): a JWK Set of the "+
			"Ed25519 public keys whose signatures a Hanzo agent's requests carry in `Signature`, "+
			"`Signature-Input` and `Signature-Agent` (RFC 9421, tag `web-bot-auth`). A site reads "+
			"it to verify that a request claiming to be a Hanzo agent is one.\n\n"+
			"Each key is `kty` OKP, `crv` Ed25519, `x`, and `kid` its JWK thumbprint, which is the "+
			"`keyid` a signature names. The response is signed too, once per key over "+
			"`(\"@authority\";req)` with tag `http-message-signatures-directory`, so it proves this "+
			"server holds every key it publishes. Answers "+
			"`application/http-message-signatures-directory+json` with `Cache-Control: public, "+
			"max-age=3600`; during a rotation it lists both keys.\n\n"+
			"Public: no bearer, no tenant scope. 404 when this deployment signs nothing, 503 when "+
			"its keys cannot be read; never an empty set.")
}

// useDirectory answers botauth.Path on GET. It is a ROUTE, not middleware: the
// composition root builds each subsystem on an app of its own and runs what it
// installs only for the routes it registered (cloud.UseAll), so a middleware
// answering an address crawl declares no route at would never run.
//
// It is RAW by design, crawl's one raw operation (openapi/untyped.json): the
// response is signed over the authority the directory was fetched at, which is
// the request's Host, a fact a typed op never sees; and an MCP tool or CLI
// command that fetches a public key set has no caller. Its prose is declared in
// init above, so the document still says what the address is.
func useDirectory(app cloud.Router, log luxlog.Logger) {
	app.Raw(http.MethodGet, botauth.Path, func(c *zip.Ctx) error {
		s := botauth.Default()
		if s == nil {
			return zip.ErrNotFound("this process signs no requests, so it publishes no keys")
		}
		doc, signed, err := s.Directory(c.Context(), c.Host())
		if err != nil {
			// The store's error, never the key: botauth's errors carry no material.
			log.Error("crawl: the Web Bot Auth key directory is unavailable", "err", err)
			return zip.Errorf(http.StatusServiceUnavailable, "the signing keys cannot be read right now")
		}
		for k, v := range signed {
			c.SetHeader(k, v[0])
		}
		c.SetHeader("Content-Type", botauth.Media)
		c.SetHeader("Cache-Control", "public, max-age="+strconv.Itoa(int(botauth.MaxAge.Seconds())))
		c.SetHeader("Access-Control-Allow-Origin", "*")
		return c.Bytes(http.StatusOK, doc)
	})
}
