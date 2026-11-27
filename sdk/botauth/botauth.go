// Package botauth signs this deployment's outbound agent requests with Web Bot
// Auth: HTTP Message Signatures (RFC 9421) over the request's authority, made
// with an Ed25519 key whose public half the deployment publishes at
// /.well-known/http-message-signatures-directory. A site that reads the
// signature can tell a Hanzo agent from anything else that copies its
// User-Agent (hanzo-inc/cloud#423).
//
// # One signer, one key set, one transport
//
// [Signer] holds where the key set is kept ([Store]: the deployment's KMS in
// production) and the address of the directory that publishes it ([Signer.Agent]).
// The host builds one per process and installs it with [Use]; every outbound
// path that fetches the web on an agent's behalf sends through [Transport] or
// [Client], which signs with whatever [Default] is at the moment the request
// leaves. A process with no signer, or whose key set cannot be read, sends the
// request unsigned, exactly as before: a signature is an identity assertion,
// never a reason to drop a fetch.
//
// # The wire is Cloudflare's
//
// Cloudflare implements draft-meunier-http-message-signatures-directory-03 and
// says verification fails for the later Dictionary form of Signature-Agent, so
// this sends the form it verifies:
//
//	Signature-Agent: "https://api.hanzo.ai/.well-known/http-message-signatures-directory"
//	Signature-Input: sig1=("@authority" "signature-agent");created=…;keyid="…";alg="ed25519";expires=…;nonce="…";tag="web-bot-auth"
//	Signature: sig1=:…:
//
// keyid is the key's JWK thumbprint (RFC 7638 over RFC 8037's OKP members),
// nonce is 64 random bytes, and a signature is good for [Window]. The value of
// Signature-Agent is the directory's own URL, which is what the drafts say a
// verifier fetches. TestTheCloudflareVectorSignsByteForByte holds the base and
// the signature to the published test vector.
//
// # Rotation
//
// The stored value is a key SET: one base64url Ed25519 seed per line. The first
// line signs; every line is published. So a rotation never strands a verifier
// holding a cached directory: add the new seed as a second line, wait out
// [MaxAge], move it to the first line, wait out MaxAge again, then remove the old
// one. [Parse] reads the set; [NewKeySet] mints a set of one.
package botauth

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// Path is where a deployment publishes its signing keys.
	Path = "/.well-known/http-message-signatures-directory"
	// Media is the directory's media type.
	Media = "application/http-message-signatures-directory+json"
	// Tag marks a request signature as Web Bot Auth.
	Tag = "web-bot-auth"
	// DirectoryTag marks the directory's own signatures, one per key, which prove
	// the server holds each key it publishes.
	DirectoryTag = "http-message-signatures-directory"
	// Window is how long a signature is good for after it is made: long enough
	// for a verifier's clock to disagree with ours, short enough that a captured
	// signature is worth little.
	Window = 5 * time.Minute
	// MaxAge is how long a verifier may cache the directory, and so how long a
	// rotation waits between its steps.
	MaxAge = time.Hour
	// Refresh is how long a process trusts the key set it read before reading it
	// again, so a rotation reaches every process within this.
	Refresh = 5 * time.Minute
	// retry is how soon a process asks again after it could not read the set.
	retry = time.Minute
	// label is the signature's name in Signature-Input and Signature.
	label = "sig1"
	// nonceBytes is the nonce's length before encoding, as the drafts recommend.
	nonceBytes = 64
)

// ErrNoKeys is a [Store] that holds no key set.
var ErrNoKeys = errors.New("botauth: no signing key is stored")

// ErrRefused is every reason [Verify] does not accept a signature.
var ErrRefused = errors.New("botauth: signature refused")

// Store is where the key set is kept. Load answers [ErrNoKeys] when it holds
// none. Neither method may put the value in an error.
type Store interface {
	Load(ctx context.Context) ([]byte, error)
	Save(ctx context.Context, set []byte) error
}

// Keys is a parsed key set: the first signs, all are published.
type Keys []ed25519.PrivateKey

// Public is every key's public half, in order.
func (k Keys) Public() []ed25519.PublicKey {
	out := make([]ed25519.PublicKey, len(k))
	for i, key := range k {
		out[i] = key.Public().(ed25519.PublicKey)
	}
	return out
}

// Parse reads a key set: one Ed25519 seed per line, base64url or standard
// base64, padded or not. Blank lines and lines starting with # are skipped. An
// error names the line, never its contents.
func Parse(set []byte) (Keys, error) {
	var out Keys
	sc := bufio.NewScanner(bytes.NewReader(set))
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		seed, err := decode(line)
		if err != nil || len(seed) != ed25519.SeedSize {
			return nil, fmt.Errorf("botauth: line %d of the key set is not a %d-byte Ed25519 seed", n, ed25519.SeedSize)
		}
		out = append(out, ed25519.NewKeyFromSeed(seed))
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("botauth: the key set will not read: %w", err)
	}
	if len(out) == 0 {
		return nil, ErrNoKeys
	}
	return out, nil
}

// decode reads base64 in either alphabet, padded or not.
func decode(s string) ([]byte, error) {
	s = strings.TrimRight(s, "=")
	if strings.ContainsAny(s, "+/") {
		return base64.RawStdEncoding.DecodeString(s)
	}
	return base64.RawURLEncoding.DecodeString(s)
}

// NewKeySet mints a key set of one fresh key, in the form [Parse] reads.
func NewKeySet() ([]byte, error) {
	seed := make([]byte, ed25519.SeedSize)
	if _, err := io.ReadFull(rand.Reader, seed); err != nil {
		return nil, fmt.Errorf("botauth: no randomness for a key: %w", err)
	}
	return []byte(base64.RawURLEncoding.EncodeToString(seed) + "\n"), nil
}

// Thumbprint is a key's JWK SHA-256 thumbprint (RFC 7638, with RFC 8037's
// members for an OKP key), base64url: the keyid a signature names it by.
func Thumbprint(pub ed25519.PublicKey) string {
	sum := sha256.Sum256([]byte(`{"crv":"Ed25519","kty":"OKP","x":"` + base64.RawURLEncoding.EncodeToString(pub) + `"}`))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// jwk is one published key. kid is the thumbprint, which is the only value a
// kid at the well-known address may carry.
type jwk struct {
	Kty string `json:"kty"`
	Crv string `json:"crv"`
	Kid string `json:"kid,omitempty"`
	X   string `json:"x"`
}

// directory is the published document: a JWK Set.
type directory struct {
	Keys []jwk `json:"keys"`
}

// Directory is the document that publishes pubs, in order.
func Directory(pubs []ed25519.PublicKey) []byte {
	d := directory{Keys: make([]jwk, 0, len(pubs))}
	for _, p := range pubs {
		d.Keys = append(d.Keys, jwk{Kty: "OKP", Crv: "Ed25519", Kid: Thumbprint(p), X: base64.RawURLEncoding.EncodeToString(p)})
	}
	b, _ := json.MarshalIndent(d, "", "  ")
	return append(b, '\n')
}

// Published reads a directory document into the keys it publishes, by
// thumbprint. A member that is not an Ed25519 public key is skipped, as a
// verifier skips what it cannot use.
func Published(doc []byte) (map[string]ed25519.PublicKey, error) {
	var d directory
	if err := json.Unmarshal(doc, &d); err != nil {
		return nil, fmt.Errorf("%w: the directory is not a JWK Set", ErrRefused)
	}
	out := map[string]ed25519.PublicKey{}
	for _, k := range d.Keys {
		if k.Kty != "OKP" || k.Crv != "Ed25519" {
			continue
		}
		x, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(k.X, "="))
		if err != nil || len(x) != ed25519.PublicKeySize {
			continue
		}
		out[Thumbprint(x)] = ed25519.PublicKey(x)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%w: the directory publishes no Ed25519 key", ErrRefused)
	}
	return out, nil
}

// Signer signs requests with the key set its Store holds.
type Signer struct {
	// Agent is the directory's URL, sent as Signature-Agent.
	Agent string
	// Store holds the key set.
	Store Store
	// Now is the clock; nil is time.Now.
	Now func() time.Time

	mu     sync.Mutex
	keys   Keys
	err    error
	until  time.Time
	loaded bool
}

func (s *Signer) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Keys is the key set, read from the Store at most once per [Refresh]. A read
// that fails keeps the last set this process read, so a store that blinks does
// not unsign every request.
func (s *Signer) Keys(ctx context.Context) (Keys, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.keysLocked(ctx, false)
}

// Provision is Keys, and mints and stores a set of one key when the Store holds
// none. It is asked by the process that serves the directory, so a deployment's
// first key is made where it is first published.
func (s *Signer) Provision(ctx context.Context) (Keys, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.keysLocked(ctx, true)
}

func (s *Signer) keysLocked(ctx context.Context, mint bool) (Keys, error) {
	now := s.now()
	if s.loaded && now.Before(s.until) && (s.err == nil || !mint) {
		return s.keys, s.err
	}
	if s.Store == nil {
		return nil, ErrNoKeys
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	set, err := s.Store.Load(ctx)
	if errors.Is(err, ErrNoKeys) && mint {
		// Saved, then read back: two processes minting at once both read the one
		// the store kept, or the loser reads it at its next refresh.
		var fresh []byte
		if fresh, err = NewKeySet(); err == nil {
			if err = s.Store.Save(ctx, fresh); err == nil {
				set, err = s.Store.Load(ctx)
			}
		}
	}
	var keys Keys
	if err == nil {
		keys, err = Parse(set)
	}
	switch {
	case err == nil:
		s.keys, s.err, s.until = keys, nil, now.Add(Refresh)
	case len(s.keys) > 0:
		// Keep signing with the last set read; ask again soon.
		s.err, s.until = nil, now.Add(retry)
	default:
		s.err, s.until = err, now.Add(retry)
	}
	s.loaded = true
	return s.keys, s.err
}

// Sign signs r in place with the first key of the set: Signature-Agent,
// Signature-Input and Signature replace any r already carried.
func (s *Signer) Sign(r *http.Request) error {
	keys, err := s.Keys(r.Context())
	if err != nil {
		return err
	}
	nonce, err := newNonce()
	if err != nil {
		return err
	}
	now := s.now()
	return signRequest(r, keys[0], s.Agent, now, now.Add(Window), nonce)
}

// SignFor signs for a request this process will not send: the request goes to
// authority over scheme ("https" or "http"), and the answer is the three headers
// it must carry, good until expires. It is how a service that holds no key signs
// what it sends itself (crawl's sign op), so the key stays where the deployment
// keeps it.
//
// The signature covers the authority and Signature-Agent only, exactly as [Sign]'s
// does, so it binds no path, method or body. The authority is signed as RFC 9421
// §2.2.3 has a verifier read it: lowercased, and without the scheme's default
// port. Anything that is not a host and an optional port is refused.
func (s *Signer) SignFor(ctx context.Context, scheme, authority string) (h http.Header, expires time.Time, err error) {
	auth, ok := Authority(authority)
	if !ok {
		return nil, time.Time{}, fmt.Errorf("botauth: %q is not a host and an optional port", authority)
	}
	if scheme != "https" && scheme != "http" {
		return nil, time.Time{}, fmt.Errorf("botauth: %q is not http or https", scheme)
	}
	auth = normalAuthority(auth, scheme)
	keys, err := s.Keys(ctx)
	if err != nil {
		return nil, time.Time{}, err
	}
	nonce, err := newNonce()
	if err != nil {
		return nil, time.Time{}, err
	}
	now := s.now()
	h = http.Header{}
	if err := signAuthority(h, auth, keys[0], s.Agent, now, now.Add(Window), nonce); err != nil {
		return nil, time.Time{}, err
	}
	return h, now.Add(Window), nil
}

// maxAuthority bounds an authority: a DNS name is at most 253 octets, then a
// colon and five digits.
const maxAuthority = 253 + 6

// Authority is s as an @authority, lowercased, and whether it is one: a DNS name
// or an IPv4 address, or an IPv6 address in brackets, then optionally a colon and
// a port. No scheme, userinfo, path, query, space or control character: the
// value is signed as given, and a verifier compares it with the Host it received.
func Authority(s string) (string, bool) {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" || len(s) > maxAuthority {
		return "", false
	}
	host, port := s, ""
	if strings.HasPrefix(s, "[") {
		end := strings.IndexByte(s, ']')
		if end < 0 {
			return "", false
		}
		host, port = s[:end+1], s[end+1:]
		if ip := net.ParseIP(host[1 : len(host)-1]); ip == nil || ip.To4() != nil {
			return "", false
		}
	} else if i := strings.LastIndexByte(s, ':'); i >= 0 {
		host, port = s[:i], s[i:]
	}
	if port != "" {
		p, err := strconv.Atoi(port[1:])
		if port[0] != ':' || err != nil || p < 1 || p > 65535 || port[1:] != strconv.Itoa(p) {
			return "", false
		}
	}
	if !strings.HasPrefix(host, "[") {
		if host == "" || strings.HasPrefix(host, ".") || strings.HasSuffix(host, ".") || strings.Contains(host, "..") {
			return "", false
		}
		for i := 0; i < len(host); i++ {
			c := host[i]
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '.') {
				return "", false
			}
		}
	}
	return s, true
}

// Directory is the published document for the key set, and the headers that
// sign it for authority (the Host the directory was asked for): one signature
// per key, each over ("@authority";req), which proves this server holds every
// key it publishes.
func (s *Signer) Directory(ctx context.Context, authority string) ([]byte, http.Header, error) {
	keys, err := s.Provision(ctx)
	if err != nil {
		return nil, nil, err
	}
	now := s.now()
	h := http.Header{}
	var inputs, sigs []string
	for i, key := range keys {
		nonce, err := newNonce()
		if err != nil {
			return nil, nil, err
		}
		params := `("@authority";req);alg="ed25519";keyid="` + Thumbprint(key.Public().(ed25519.PublicKey)) +
			`";nonce="` + nonce + `";tag="` + DirectoryTag + `";created=` + unix(now) + `;expires=` + unix(now.Add(Window))
		base := `"@authority";req: ` + normalAuthority(authority, "https") + "\n" + `"@signature-params": ` + params
		name := "binding" + strconv.Itoa(i)
		inputs = append(inputs, name+"="+params)
		sigs = append(sigs, name+"=:"+base64.StdEncoding.EncodeToString(ed25519.Sign(key, []byte(base)))+":")
	}
	h.Set("Signature-Input", strings.Join(inputs, ", "))
	h.Set("Signature", strings.Join(sigs, ", "))
	return Directory(keys.Public()), h, nil
}

// signRequest is Sign with every input stated, which is how the test vector is
// reproduced.
func signRequest(r *http.Request, key ed25519.PrivateKey, agent string, created, expires time.Time, nonce string) error {
	auth := authority(r)
	if auth == "" {
		return errors.New("botauth: the request names no authority")
	}
	return signAuthority(r.Header, auth, key, agent, created, expires, nonce)
}

// signAuthority writes the three headers that sign a request to auth into h.
func signAuthority(h http.Header, auth string, key ed25519.PrivateKey, agent string, created, expires time.Time, nonce string) error {
	if !sfString(agent) {
		return fmt.Errorf("botauth: the signature agent %q is not a structured-field string", agent)
	}
	sa := `"` + agent + `"`
	params := `("@authority" "signature-agent");created=` + unix(created) + `;keyid="` +
		Thumbprint(key.Public().(ed25519.PublicKey)) + `";alg="ed25519";expires=` + unix(expires) +
		`;nonce="` + nonce + `";tag="` + Tag + `"`
	base := `"@authority": ` + auth + "\n" + `"signature-agent": ` + sa + "\n" + `"@signature-params": ` + params
	h.Set("Signature-Agent", sa)
	h.Set("Signature-Input", label+"="+params)
	h.Set("Signature", label+"=:"+base64.StdEncoding.EncodeToString(ed25519.Sign(key, []byte(base)))+":")
	return nil
}

func unix(t time.Time) string { return strconv.FormatInt(t.Unix(), 10) }

// newNonce is nonceBytes of randomness, standard base64 as the drafts' examples.
func newNonce() (string, error) {
	b := make([]byte, nonceBytes)
	if _, err := io.ReadFull(rand.Reader, b); err != nil {
		return "", fmt.Errorf("botauth: no randomness for a nonce: %w", err)
	}
	return base64.StdEncoding.EncodeToString(b), nil
}

// sfString reports whether s can be sent as a structured-field string without
// escaping: printable ASCII with no quote or backslash.
func sfString(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if c := s[i]; c < 0x20 || c > 0x7e || c == '"' || c == '\\' {
			return false
		}
	}
	return true
}

// AuthorityOf is the @authority a request to u is signed as and a verifier
// reads: its host, lowercased, with the scheme's default port dropped.
func AuthorityOf(u *url.URL) string { return normalAuthority(u.Host, u.Scheme) }

// authority is the request's @authority (RFC 9421 §2.2.3): its host, lowercased,
// with the scheme's default port dropped.
func authority(r *http.Request) string {
	h := r.Host
	if h == "" && r.URL != nil {
		h = r.URL.Host
	}
	scheme := "https"
	if r.URL != nil && r.URL.Scheme != "" {
		scheme = r.URL.Scheme
	} else if r.TLS == nil && r.URL != nil && r.URL.Host == "" {
		// A request a server received: plain HTTP unless it arrived over TLS.
		scheme = "http"
	}
	return normalAuthority(h, scheme)
}

func normalAuthority(h, scheme string) string {
	h = strings.ToLower(strings.TrimSpace(h))
	host, port, err := net.SplitHostPort(h)
	if err != nil {
		return h
	}
	if (scheme == "https" && port == "443") || (scheme == "http" && port == "80") {
		if strings.Contains(host, ":") {
			return "[" + host + "]"
		}
		return host
	}
	return h
}

// def is the process's signer, installed by the host.
var def atomic.Pointer[Signer]

// Use installs s as the process's signer; nil removes it.
func Use(s *Signer) { def.Store(s) }

// Default is the process's signer, or nil when it has none.
func Default() *Signer { return def.Load() }

// Transport signs every request it carries with the process's signer as it is
// at that moment, then sends it on next (http.DefaultTransport when nil). A
// redirect is a new request through the same transport, so each hop is signed
// for its own authority. With no signer, or a key set that cannot be read, the
// request goes unsigned.
func Transport(next http.RoundTripper) http.RoundTripper {
	if next == nil {
		next = http.DefaultTransport
	}
	return signing{next: next}
}

// Client is c with its transport wrapped in [Transport].
func Client(c *http.Client) *http.Client {
	out := *c
	out.Transport = Transport(c.Transport)
	return &out
}

// Signs reports whether rt is [Transport]'s: how a package pins that its
// outbound client signs.
func Signs(rt http.RoundTripper) bool {
	_, ok := rt.(signing)
	return ok
}

type signing struct{ next http.RoundTripper }

func (t signing) RoundTrip(r *http.Request) (*http.Response, error) {
	s := Default()
	if s == nil {
		return t.next.RoundTrip(r)
	}
	// A RoundTripper must not change the request it was handed.
	signed := r.Clone(r.Context())
	if err := s.Sign(signed); err != nil {
		return t.next.RoundTrip(r)
	}
	return t.next.RoundTrip(signed)
}
