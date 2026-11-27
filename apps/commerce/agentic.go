// Copyright © 2026 Hanzo AI. MIT License.

package commerce

// agentic.go — THE AGENT CHECKOUT PROTOCOLS, each mounted only when its operator says so.
//
// hanzoai/commerce implements the published agent commerce wires as packages
// (HIP-1223): ACP (agentic/acp) and UCP (agentic/ucp) open, change and complete a
// commerce checkout over the same headless purchase surface /v1/billing/checkouts
// serves, priced from the catalog and charged by CompleteCheckout. Their Commerce
// backends call api/billing and commerce's datastore directly, so they run here, in
// the one process that embeds commerce, after the embed has booted.
//
// NOTHING IS CHOSEN FOR THE OPERATOR (HIP-1223 §1.3). Each protocol is switched on by
// its own variable naming a JSON file (CLOUD_ACP_CONFIG, CLOUD_UCP_CONFIG), the same
// shape as the OSS host's CLOUD_X402_CONFIG. With the variable unset the protocol
// registers no route: its API and its discovery document answer 404, and the purchase
// discovery names neither. A file that names a field this code does not know, leaves
// a required one out, or does not load is REFUSED AT START: the protocol is not
// mounted, the reason is logged, and /v1/health reports commerce/<protocol> degraded,
// which the release smoke refuses. It is never mounted on a guess, and a bad file
// never takes the rest of commerce down with it.
//
// WHO IS BUYING is cloud's answer, never a header the protocol trusts: the identity
// boundary validates the bearer, and the caller is the validated org, the wallet
// within it (meter.PayerIn, the same subject the prepaid gate and /v1/billing use) and
// the brand host. withBuyer hands that to the protocol's net/http handler through the
// request context; a request with no validated principal reaches the protocol
// anonymous and is refused in the protocol's own 401.
//
// NOT MOUNTED HERE, and why (each is the owner's call, see #424):
//   - AP2 (agentic/ap2) defines no wire of its own; the one that carries it is UCP's
//     AP2 Mandates extension, which agentic/ucp passes through untouched. It mounts
//     once commerce composes ap2.Seller into the UCP completion.
//   - MPP (agentic/mpp) is a gate in front of the priced route, and its store and its
//     seller live only in this process. The routes worth pricing (the x402 fees) are
//     served by other processes, so it needs the plane bridge x402 phase 1 built.
//
// UCP idempotency sweep: once mounted, ucp.go runs ucp.Idempotency.Sweep periodically
// under leader election (safe as of hanzoai/commerce v1.50.251 / #83, which scopes
// every UCP record under "ucp" and never touches billing webhook guards or other
// protocol ledgers).

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync/atomic"

	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/sdk/meter"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/hanzoai/cloud/sdk/readiness"
)

// The variables that switch each protocol on. Each names a JSON file; unset is off.
const (
	acpConfigEnv = "CLOUD_ACP_CONFIG"
	ucpConfigEnv = "CLOUD_UCP_CONFIG"
)

// Where each protocol answers. The APIs are leaves of commerce's own root (HIP-1220
// §1); the discovery documents are where their specifications put them, at the origin.
const (
	acpBase      = "/v1/commerce/acp"
	acpWellKnown = "/.well-known/acp.json"
	ucpBase      = "/v1/commerce/ucp"
	ucpWellKnown = "/.well-known/ucp"
)

// WellKnown is every address outside /v1/commerce this app answers, and only when the
// protocol that owns it is configured. manifest/apps.go routes these to commerce.
var WellKnown = []string{acpWellKnown, ucpWellKnown}

// secretReader reads a deployment secret by its KMS reference: hostSecrets in
// production, a fixed map in a test.
type secretReader interface {
	GetSecret(ctx context.Context, ref string) ([]byte, error)
}

// mountAgentic mounts every protocol whose variable is set, and records the discovery
// links of the ones that mounted. Use calls it once the embed is live.
func mountAgentic(app *zip.App, lg luxlog.Logger, secrets secretReader) {
	links := map[string]string{}
	for _, p := range []struct {
		name, env, link string
		mount           func(*zip.App, luxlog.Logger, secretReader, []byte) error
	}{
		{"acp", acpConfigEnv, acpWellKnown, mountACP},
		{"ucp", ucpConfigEnv, ucpWellKnown, mountUCP},
	} {
		path := strings.TrimSpace(os.Getenv(p.env))
		if path == "" {
			continue
		}
		err := func() error {
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			return p.mount(app, lg.New("protocol", p.name), secrets, raw)
		}()
		if err != nil {
			err = fmt.Errorf("%s: %s: %w", p.name, p.env, err)
			lg.Error("agent commerce protocol refused at start: not mounted", "protocol", p.name, "err", err)
			readiness.Degraded("commerce/"+p.name, err)
			continue
		}
		links[p.name] = p.link
		lg.Info("agent commerce protocol mounted", "protocol", p.name)
	}
	agenticLinks.Store(&links)
}

// agenticLinks are the discovery documents of the protocols that mounted, by protocol.
// The purchase discovery lists them (agent_rpc.go), so it never names one that 404s.
var agenticLinks atomic.Pointer[map[string]string]

// discoveryLinks are the purchase discovery's links with the mounted protocols' added.
func discoveryLinks(base map[string]string) map[string]string {
	out := maps.Clone(base)
	if l := agenticLinks.Load(); l != nil {
		maps.Copy(out, *l)
	}
	return out
}

// decodeStrict reads an operator's file into v. A field it does not know is an error,
// so a misspelled key cannot leave a value at its zero.
func decodeStrict(raw []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		return errors.New("trailing data after the document")
	}
	return nil
}

// origin checks that s is an https origin, scheme and host only, and answers it
// without a trailing slash.
func origin(field, s string) (string, error) {
	u, err := url.Parse(s)
	if err != nil || u.Scheme != "https" || u.Host == "" || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return "", fmt.Errorf("%s must be an https origin, like https://api.example.com", field)
	}
	return "https://" + u.Host, nil
}

// template checks that s is an absolute https URL holding {id}, and answers the
// function that fills it.
func template(field, s string) (func(string) string, error) {
	u, err := url.Parse(strings.ReplaceAll(s, "{id}", "id"))
	if err != nil || u.Scheme != "https" || u.Host == "" || !strings.Contains(s, "{id}") {
		return nil, fmt.Errorf("%s must be an https URL holding {id}", field)
	}
	return func(id string) string { return strings.ReplaceAll(s, "{id}", url.PathEscape(id)) }, nil
}

// httpsURL checks that s is an absolute https URL.
func httpsURL(field, s string) error {
	if u, err := url.Parse(s); err != nil || u.Scheme != "https" || u.Host == "" {
		return fmt.Errorf("%s must be an absolute https URL", field)
	}
	return nil
}

// signingKey reads a PKCS#8 private key from KMS: ECDSA P-256 or P-384, or Ed25519.
func signingKey(ctx context.Context, secrets secretReader, ref string) (crypto.Signer, error) {
	if ref == "" {
		return nil, errors.New("signingKey.secret names no KMS secret")
	}
	if secrets == nil {
		return nil, errors.New("this process has no secret plane to read the signing key from")
	}
	raw, err := secrets.GetSecret(ctx, ref)
	if err != nil {
		return nil, fmt.Errorf("signing key %s: %w", ref, err)
	}
	block, _ := pem.Decode(raw)
	if block == nil || block.Type != "PRIVATE KEY" {
		return nil, fmt.Errorf("signing key %s is not a PEM PRIVATE KEY (PKCS#8)", ref)
	}
	k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("signing key %s: %w", ref, err)
	}
	switch s := k.(type) {
	case *ecdsa.PrivateKey:
		return s, nil
	case ed25519.PrivateKey:
		return s, nil
	}
	return nil, fmt.Errorf("signing key %s is a %T; ECDSA P-256, P-384 or Ed25519 signs UCP", ref, k)
}

// agentBuyer is who a protocol request buys for: the validated org, the wallet within it,
// and the brand host the request addressed.
type agentBuyer struct{ org, subject, host string }

// buyerKey and hostKey are the request-context slots withBuyer fills.
type (
	buyerKey struct{}
	hostKey  struct{}
)

// withBuyer serves h, a protocol's net/http handler, with the caller cloud's identity
// boundary validated. The adapted request's context is the request's own, so a value
// set on the zip context here is what h reads back (buyerOf, hostOf).
func withBuyer(h http.Handler) zip.Handler {
	serve := zip.AdaptNetHTTP(h)
	return func(c *zip.Ctx) error {
		c.Locals(hostKey{}, c.Host())
		if org, ok := principal.Org(c); ok && principal.Unopened(c) == "" {
			c.Locals(buyerKey{}, agentBuyer{org: org, subject: meter.PayerIn(c, org).Subject(), host: c.Host()})
		}
		return serve(c)
	}
}

// buyerOf is the validated caller of a request withBuyer served, if it has one.
func buyerOf(r *http.Request) (agentBuyer, bool) {
	b, ok := r.Context().Value(buyerKey{}).(agentBuyer)
	return b, ok && b.org != "" && b.subject != ""
}

// authorityOf is the host the caller addressed, as the router read it.
func authorityOf(r *http.Request) string {
	if h, ok := r.Context().Value(hostKey{}).(string); ok && h != "" {
		return h
	}
	return r.Host
}

// errNoBuyer is a protocol request with no validated caller.
var errNoBuyer = errors.New("no validated caller")
