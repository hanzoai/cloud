package cloud

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/cloud/sdk/brand"
	"github.com/hanzoai/cloud/sdk/env"
	luxlog "github.com/luxfi/log"
)

// OIDC discovery is how this binary learns who signs its tokens: the issuer a token
// must name and the JWKS its signature is checked against both come from one
// document the identity provider publishes, never from a value an operator typed.
//
// ONE input names a provider — its discovery URL, https://<host>/.well-known/
// openid-configuration (OpenID Connect Discovery 1.0 §4). The issuer is that URL
// without the well-known suffix (§4.3), and a document that says any other issuer
// is refused: a host cannot vouch for an identity it does not own.
//
// The document is cached and refreshed behind the request that finds it stale, so
// no request waits on a fetch once there has been one good copy. A refresh that
// fails keeps the last good copy; only a provider that has never answered fails
// closed, and it is asked again at most every retryEvery, never per request.

// WellKnown is the discovery document's path under an issuer.
const WellKnown = "/.well-known/openid-configuration"

const (
	discoveryTTL     = time.Hour
	discoveryTimeout = 10 * time.Second
	discoveryMax     = 1 << 20
	retryEvery       = 5 * time.Second
)

// ErrNoDiscovery is a provider that has never published a good document: nothing
// is known about who signs its tokens, so none of its tokens is accepted.
var ErrNoDiscovery = errors.New("oidc discovery: no good document yet")

// Document is the part of a discovery document this binary reads.
type Document struct {
	Issuer  string `json:"issuer"`
	JWKSURI string `json:"jwks_uri"`
}

// Discovery is one provider's cached document.
type Discovery struct {
	url    string
	issuer string
	client *http.Client
	ttl    time.Duration
	now    func() time.Time

	mu         sync.Mutex
	doc        Document
	good       bool
	fetchedAt  time.Time
	attemptAt  time.Time
	refreshing bool
	inFlight   chan struct{}
}

// issuerOf is the issuer a discovery URL implies (OpenID Connect Discovery 1.0
// §4.3): the URL with the well-known path removed. A URL that is not one is an error.
func issuerOf(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return "", fmt.Errorf("oidc discovery: %q is not a discovery URL", raw)
	}
	if u.Scheme != "https" && !loopback(u.Hostname()) {
		return "", fmt.Errorf("oidc discovery: %q must be https", raw)
	}
	path, ok := strings.CutSuffix(u.Path, WellKnown)
	if !ok {
		return "", fmt.Errorf("oidc discovery: %q does not end in %s", raw, WellKnown)
	}
	return u.Scheme + "://" + u.Host + path, nil
}

// loopback reports a host that is this machine, the one place plain http is allowed:
// a developer's own identity provider.
func loopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

var (
	sharedMu sync.Mutex
	shared   = map[string]*Discovery{}
)

// DiscoveryFor is the process's one Discovery for url, so every verifier of one
// provider shares one document. A client of nil is a default with a 10 s timeout.
func DiscoveryFor(rawURL string, client *http.Client) (*Discovery, error) {
	if client != nil {
		return newDiscovery(rawURL, client, discoveryTTL)
	}
	sharedMu.Lock()
	defer sharedMu.Unlock()
	if d, ok := shared[rawURL]; ok {
		return d, nil
	}
	d, err := newDiscovery(rawURL, nil, discoveryTTL)
	if err != nil {
		return nil, err
	}
	shared[rawURL] = d
	return d, nil
}

func newDiscovery(rawURL string, client *http.Client, ttl time.Duration) (*Discovery, error) {
	iss, err := issuerOf(rawURL)
	if err != nil {
		return nil, err
	}
	if client == nil {
		client = &http.Client{Timeout: discoveryTimeout}
	}
	return &Discovery{url: rawURL, issuer: iss, client: client, ttl: ttl, now: time.Now}, nil
}

// Issuer is the issuer this provider's tokens name, known without a fetch.
func (d *Discovery) Issuer() string { return d.issuer }

// Document is the last good document. With none yet it asks once, and a provider
// that cannot answer is ErrNoDiscovery until it can, asked again no more often than
// every retryEvery. A copy older than the TTL is served as it is while one refresh
// runs behind it.
func (d *Discovery) Document() (Document, error) {
	for {
		d.mu.Lock()
		if d.good {
			doc, stale := d.doc, d.now().Sub(d.fetchedAt) >= d.ttl
			if stale && !d.refreshing && d.now().Sub(d.attemptAt) >= retryEvery {
				d.refreshing, d.attemptAt = true, d.now()
				go d.refresh()
			}
			d.mu.Unlock()
			return doc, nil
		}
		// A cold-start fetch is already running: wait for it instead of failing
		// concurrent first-arrival callers closed with ErrNoDiscovery.
		if ch := d.inFlight; ch != nil {
			d.mu.Unlock()
			<-ch
			continue
		}
		if d.now().Sub(d.attemptAt) < retryEvery && !d.attemptAt.IsZero() {
			d.mu.Unlock()
			return Document{}, ErrNoDiscovery
		}
		ch := make(chan struct{})
		d.inFlight = ch
		d.attemptAt = d.now()
		d.mu.Unlock()

		doc, err := d.fetch(context.Background())
		d.mu.Lock()
		d.inFlight = nil
		if err != nil {
			good, kept := d.good, d.doc
			d.mu.Unlock()
			close(ch)
			if good {
				return kept, nil
			}
			luxlog.Default().Warn("oidc discovery: no good document", "url", d.url, "err", err)
			return Document{}, ErrNoDiscovery
		}
		d.doc, d.good, d.fetchedAt = doc, true, d.now()
		d.mu.Unlock()
		close(ch)
		return doc, nil
	}
}

// refresh replaces the held document, or keeps it and says so.
func (d *Discovery) refresh() {
	doc, err := d.fetch(context.Background())
	d.mu.Lock()
	defer d.mu.Unlock()
	d.refreshing = false
	if err != nil {
		luxlog.Default().Warn("oidc discovery: refresh failed, keeping the last good document", "url", d.url, "err", err)
		return
	}
	d.doc, d.fetchedAt = doc, d.now()
}

// fetch reads and validates one document.
func (d *Discovery) fetch(ctx context.Context) (Document, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, d.url, nil)
	if err != nil {
		return Document{}, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := d.client.Do(req)
	if err != nil {
		return Document{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return Document{}, fmt.Errorf("status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, discoveryMax+1))
	if err != nil {
		return Document{}, err
	}
	if len(body) > discoveryMax {
		return Document{}, errors.New("document too large")
	}
	var doc Document
	if err := json.Unmarshal(body, &doc); err != nil {
		return Document{}, fmt.Errorf("not a discovery document: %w", err)
	}
	if doc.Issuer != d.issuer {
		return Document{}, fmt.Errorf("document names issuer %q, but %s is the discovery document of %q", doc.Issuer, d.url, d.issuer)
	}
	ju, err := url.Parse(doc.JWKSURI)
	if err != nil || ju.Host == "" || (ju.Scheme != "https" && !loopback(ju.Hostname())) {
		return Document{}, fmt.Errorf("jwks_uri %q is not an https URL", doc.JWKSURI)
	}
	return doc, nil
}

// DiscoveryURL is the discovery URL of the provider this process trusts, for the
// callers that hold no Config: the one CLOUD_OIDC_DISCOVERY names, else the
// deployment brand's own (CLOUD_BRAND).
func DiscoveryURL() string {
	return cmp.Or(strings.TrimSpace(env.Or("CLOUD_OIDC_DISCOVERY", "")), brand.DiscoveryFor(env.Or("CLOUD_BRAND", brand.Default)))
}
