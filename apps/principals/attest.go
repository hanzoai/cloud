package principals

// attest.go — which principal an agent answers to, as a statement anyone can
// verify offline.
//
// THE FORMAT is a JSON Web Signature in compact serialization (RFC 7515) over a
// JSON claim set, signed with Ed25519 (EdDSA, RFC 8037), and the keys are
// published as a JSON Web Key Set (RFC 7517) at GET /v1/principal/keys. No Lux
// attestation type for an agent's principal exists in the luxfi packages this
// module builds with — luxfi/kms's pkg/attestation attests a node's TEE, and
// luxfi/zap's attests a transport peer — so the statement is the one format every
// language already verifies. A verifier fetches the key set once and needs
// nothing of Hanzo's after that.
//
// The header's typ is "agent-principal+jwt" (RFC 8725 §3.11): a statement is
// never an access token, is signed by a key that signs nothing else, and names an
// issuer no token does, so neither can be taken for the other.
//
// THE KEY lives in KMS, at a deployment ref only the platform's own processes
// may read. KMS's signing op is threshold-MPC-backed and fails closed in this
// binary (apps/kms Client.Sign answers ErrSignUnavailable unless an MPC backend
// is wired, and then routes elsewhere), so the key is opened here: one private
// key, the one that signs, held for a few minutes, used only under a lock so a
// newer set zeroes it on replacing it, and printed by no formatter. A SuperAdmin
// rotates it; a rotated-out key stays published with its private half destroyed,
// so a statement signed before the rotation still verifies until it expires and
// nothing can sign as it again.
//
// SPAWNED AGENTS answer to their parent's principal: apps/agents resolves a parent
// only inside the org the new agent is created in, so every agent in a lineage is
// the same org's, and the statement names the chain.

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/hanzoai/cloud/sdk/iam"
	"github.com/hanzoai/cloud/sdk/secret"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/cloud/client"
	agentpeer "github.com/hanzoai/cloud/client/agent"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/audit"
	"github.com/hanzoai/cloud/sdk/spec"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// The key set is published for anyone: a verifier checking a statement is,
// by construction, not the org that holds it. It reaches nothing else.
func init() {
	spec.Open("/v1/principal/keys", "GET")
}

const (
	// keyRef is the key set's KMS ref: the deployment's own material, which the
	// store answers to the platform's processes and never to a tenant.
	keyRef = "principal/attestation"
	// keyTTL is how long a read key set is used before KMS is asked again, so a
	// rotation on one replica reaches the others within it.
	keyTTL = 5 * time.Minute
	// statementTTL is how long a statement is valid. An agent can be deleted, so a
	// statement says who it answered to recently, not for ever.
	statementTTL = 24 * time.Hour
)

// stored is one key as KMS holds it. Only the key that signs keeps its seed: a
// rotation destroys the retired key's private half and keeps its public one,
// which is all a retired key is for.
type stored struct {
	Kid     string `json:"kid"`
	Public  []byte `json:"public"`
	Seed    []byte `json:"seed,omitempty"`
	Created int64  `json:"created"`
	// Retired is when a rotation replaced it; a retired key verifies and never
	// signs.
	Retired int64 `json:"retired,omitempty"`
}

// Format prints a key's id and nothing of its material, whatever the verb.
func (k stored) Format(f fmt.State, _ rune) { _, _ = fmt.Fprintf(f, "key(%s)", k.Kid) }

// keyset is the set KMS holds. Version counts rotations: a rotation writes
// Version+1 over the Version it read, and refuses when KMS no longer holds it.
type keyset struct {
	Version int      `json:"version"`
	Keys    []stored `json:"keys"`
}

// Format prints the set's shape and nothing of its material, whatever the verb.
func (s keyset) Format(f fmt.State, _ rune) {
	_, _ = fmt.Fprintf(f, "keyset(v%d, %d keys)", s.Version, len(s.Keys))
}

// wipe zeroes every seed the set holds. Go promises no more than that this
// buffer is zero: a copy the runtime made on the way — a decode, a stack — is
// out of reach, which is why the set is held only while it is used.
func (s keyset) wipe() {
	for i := range s.Keys {
		clear(s.Keys[i].Seed)
	}
}

// keyring reads the key set from KMS and caches, for keyTTL, the published keys
// and the one private key that signs. The private key is used only under mu, so a
// newer set can zero it the moment it replaces it.
type keyring struct {
	kms secret.Store
	log luxlog.Logger
	// rot admits one rotation at a time.
	rot sync.Mutex

	mu      sync.Mutex
	version int
	public  iam.JWKS
	kid     string
	priv    ed25519.PrivateKey
	read    time.Time
}

// errNoKey is a deployment that has not had its first key rotated in.
var errNoKey = errors.New("principal: no attestation key has been provisioned; a SuperAdmin provisions one with POST /v1/principal/keys")

// fetch reads the set from KMS. A set KMS does not hold is the empty set at
// version 0; any other failure is an outage. The caller wipes what it answers.
func (k *keyring) fetch(ctx context.Context) (keyset, error) {
	if k.kms == nil {
		return keyset{}, zip.Errorf(http.StatusServiceUnavailable, "principal: no KMS on this process, so no statement is signed")
	}
	raw, err := k.kms.GetSecret(ctx, keyRef)
	switch {
	case errors.Is(err, secret.ErrNone):
		return keyset{Keys: []stored{}}, nil
	case err != nil:
		k.log.Error("principal: attestation key set read failed", "ref", keyRef, "err", err)
		return keyset{}, zip.Errorf(http.StatusServiceUnavailable, "principal: the attestation key set could not be read from KMS")
	}
	defer clear(raw)
	var set keyset
	if err := json.Unmarshal(raw, &set); err != nil {
		return keyset{}, zip.Errorf(http.StatusInternalServerError, "principal: the attestation key set is malformed")
	}
	for _, key := range set.Keys {
		if len(key.Public) != ed25519.PublicKeySize || (key.Seed != nil && (len(key.Seed) != ed25519.SeedSize ||
			!ed25519.NewKeyFromSeed(key.Seed).Public().(ed25519.PublicKey).Equal(ed25519.PublicKey(key.Public)))) {
			set.wipe()
			return keyset{}, zip.Errorf(http.StatusInternalServerError, "principal: the attestation key set is malformed")
		}
	}
	return set, nil
}

// install makes set the cached one: its public keys, and the private key of its
// newest unretired key. The private key it replaces is zeroed.
func (k *keyring) install(set keyset) {
	var kid string
	var priv ed25519.PrivateKey
	for i := len(set.Keys) - 1; i >= 0; i-- {
		if set.Keys[i].Retired == 0 && set.Keys[i].Seed != nil {
			kid, priv = set.Keys[i].Kid, ed25519.NewKeyFromSeed(set.Keys[i].Seed)
			break
		}
	}
	clear(k.priv)
	k.version, k.public, k.kid, k.priv, k.read = set.Version, publicOf(set), kid, priv, time.Now()
}

// current refreshes the cache from KMS when it is older than keyTTL. Called with
// mu held.
func (k *keyring) current(ctx context.Context) error {
	if !k.read.IsZero() && time.Since(k.read) < keyTTL {
		return nil
	}
	set, err := k.fetch(ctx)
	if err != nil {
		return err
	}
	defer set.wipe()
	k.install(set)
	return nil
}

// published is the key set as it is published.
func (k *keyring) published(ctx context.Context) (*iam.JWKS, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if err := k.current(ctx); err != nil {
		return nil, err
	}
	out := &iam.JWKS{Keys: slices.Clone(k.public.Keys)}
	return out, nil
}

// sign makes a statement of c with the key that signs, and answers it with the
// key's id.
func (k *keyring) sign(ctx context.Context, c iam.Claims) (string, string, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if err := k.current(ctx); err != nil {
		return "", "", err
	}
	if k.priv == nil {
		return "", "", zip.Errorf(http.StatusServiceUnavailable, "%v", errNoKey)
	}
	h, err := json.Marshal(iam.Header{Alg: "EdDSA", Typ: iam.Typ, Kid: k.kid})
	if err != nil {
		return "", "", err
	}
	p, err := json.Marshal(c)
	if err != nil {
		return "", "", err
	}
	input := b64(h) + "." + b64(p)
	return input + "." + b64(ed25519.Sign(k.priv, []byte(input))), k.kid, nil
}

// next is the set a rotation writes: a fresh key that signs, every earlier key
// retired with its private half destroyed, and the version one past the set KMS
// holds now. The caller wipes it.
func (k *keyring) next(ctx context.Context) (keyset, string, error) {
	set, err := k.fetch(ctx)
	if err != nil {
		return keyset{}, "", err
	}
	seed := make([]byte, ed25519.SeedSize)
	if _, err := rand.Read(seed); err != nil {
		set.wipe()
		return keyset{}, "", zip.Errorf(http.StatusInternalServerError, "principal: entropy: %v", err)
	}
	now := time.Now().Unix()
	for i := range set.Keys {
		clear(set.Keys[i].Seed)
		set.Keys[i].Seed = nil
		if set.Keys[i].Retired == 0 {
			set.Keys[i].Retired = now
		}
	}
	pub := ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey)
	kid := kidOf(pub)
	set.Keys = append(set.Keys, stored{Kid: kid, Public: pub, Seed: seed, Created: now})
	set.Version++
	return set, kid, nil
}

// swap writes a rotation's set over the one it was built from. KMS has no
// conditional write, so the compare is a read: under rot, which admits one
// rotation in this process, the set KMS holds must still be the version the
// rotation read, or the rotation is refused rather than written over a key it
// did not see.
func (k *keyring) swap(ctx context.Context, next keyset) error {
	cur, err := k.fetch(ctx)
	if err != nil {
		return err
	}
	cur.wipe()
	if cur.Version != next.Version-1 {
		return zip.Errorf(http.StatusConflict, "principal: the key set was rotated while this ran; read it and rotate again")
	}
	raw, err := json.Marshal(next)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "principal: %v", err)
	}
	defer clear(raw)
	if err := k.kms.PutSecret(ctx, keyRef, raw); err != nil {
		k.log.Error("principal: attestation key set write failed", "ref", keyRef, "err", err)
		return zip.Errorf(http.StatusServiceUnavailable, "principal: the attestation key set could not be written to KMS")
	}
	k.mu.Lock()
	k.install(next)
	k.mu.Unlock()
	return nil
}

// kidOf names a public key by its own digest (RFC 7638 in spirit: the id is the
// key, so two keys can never share one).
func kidOf(pub ed25519.PublicKey) string {
	sum := sha256.Sum256(pub)
	return b64(sum[:16])
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func publicOf(set keyset) iam.JWKS {
	out := iam.JWKS{Keys: []iam.JWK{}}
	for _, k := range set.Keys {
		out.Keys = append(out.Keys, iam.JWK{Kty: "OKP", Crv: "Ed25519", X: b64(k.Public), Kid: k.Kid, Alg: "EdDSA", Use: "sig"})
	}
	return out
}

// agentRef names one of the caller's agents.
type agentRef struct {
	// Ref is the agent's id or its name in the caller's org.
	Ref string `json:"ref"`
}

// Attests which principal one of the caller's agents answers to: the org, the
// chain of agents that spawned it, and the same signed with the platform's key as
// a JWS (EdDSA) a verifier checks offline against GET /v1/principal/keys. Any
// member of the agent's org.
//
// An agent of another org is not found. A spawned agent answers to its parent's
// org, which is its own: a parent is resolved only in the org the agent is created
// in. The statement is valid for 24 hours.
func (o ops) attest(ctx context.Context, in *agentRef) (*iam.Attestation, error) {
	org, err := o.member(ctx)
	if err != nil {
		return nil, err
	}
	ref := strings.TrimSpace(in.Ref)
	if ref == "" {
		return nil, zip.ErrBadRequest("ref names an agent")
	}
	l, err := agentpeer.AgentsLineage(ctx, &client.LineageIn{Ref: ref})
	switch {
	case errors.Is(err, cloud.ErrNoPeer):
		return nil, zip.Errorf(http.StatusServiceUnavailable, "principal: this deployment runs no agents")
	case err != nil:
		var he *zip.HTTPError
		if errors.As(err, &he) && he.Status == http.StatusNotFound {
			return nil, zip.ErrNotFound("agent not found")
		}
		return nil, zip.Errorf(http.StatusBadGateway, "principal: agents: %v", err)
	}
	now := time.Now()
	c := iam.Claims{Iss: issuer(), Sub: l.Agent.ID, Name: l.Agent.Name, Org: org, Parents: make([]string, 0, len(l.Parents)),
		Iat: now.Unix(), Exp: now.Add(statementTTL).Unix(), Jti: jti()}
	for _, p := range l.Parents {
		c.Parents = append(c.Parents, p.ID)
	}
	st, kid, err := o.s.State.keys.sign(ctx, c)
	if err != nil {
		return nil, err
	}
	noStore(ctx)
	return &iam.Attestation{Agent: l.Agent, Org: org, Parents: l.Parents, Statement: st, Kid: kid, Expires: c.Exp}, nil
}

// issuer is this deployment's /v1/principal, the iss every statement names.
func issuer() string { return "https://api." + cloud.Domain() + "/v1/principal" }

func jti() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return b64(b)
}

// Publishes the keys attestations are signed with, as a JSON Web Key Set (RFC
// 7517; Ed25519 keys as RFC 8037 writes them). Open to anyone: a verifier
// verifying an agent's statement fetches this once and verifies offline.
//
// A key a rotation retired stays here and signs nothing, so a statement signed
// before the rotation verifies until it expires.
func (o ops) published(ctx context.Context, _ *cloud.Unit) (*iam.JWKS, error) {
	return o.s.State.keys.published(ctx)
}

// Rotates the attestation signing key: a new Ed25519 key is generated, sealed in
// KMS beside the others, and signs every statement from now on; every earlier key
// is retired — still published, its private half destroyed. SuperAdmin only, on
// the audit trail before the new set is written, and followed on the trail by a
// correction if the write does not take. One rotation runs at a time, and one
// that finds the set rotated under it since it read it is refused (409). The
// first call provisions the deployment's first key.
func (o ops) rotate(ctx context.Context, _ *cloud.Unit) (*iam.JWKS, error) {
	org, err := o.super(ctx, "rotate the attestation key")
	if err != nil {
		return nil, err
	}
	k := o.s.State.keys
	k.rot.Lock()
	defer k.rot.Unlock()
	next, kid, err := k.next(ctx)
	if err != nil {
		return nil, err
	}
	defer next.wipe()
	rec, err := o.record(ctx, org, "principal.key.rotated", audit.Resource{Type: "principal.key", ID: kid},
		map[string]any{"kid": kid, "keys": len(next.Keys), "version": next.Version}, true)
	if err != nil {
		return nil, err
	}
	if err := o.append(ctx, rec); err != nil {
		return nil, err
	}
	if err := k.swap(ctx, next); err != nil {
		o.unmade(ctx, []audit.Record{rec})
		return nil, err
	}
	out := publicOf(next)
	return &out, nil
}
