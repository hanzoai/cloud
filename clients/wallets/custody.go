// Package wallets is the Hanzo Cloud accounts/wallets/custody/keys/sign surface
// (/v1/wallets/*): one configurable custody seam over three orthogonal signing
// backends, selected PER WALLET by its Kind.
//
// TOPOLOGY (HIP-0106). Custody composes the two canonical Hanzo key services
// without fusing either into the hot binary:
//
//   - KMS single-sig (KindKMS) is IN-PROCESS via the embedded luxfi/kms client
//     (deps.KMS, a cloud.KMSClient). This is the fully-exercised spine: a real
//     secp256k1 key is generated, its private bytes sealed under the KMS
//     envelope, and every Sign recovers to the wallet address. No network hop.
//
//   - MPC m-of-n (KindMPC) and treasury named-signer custody (KindTreasury)
//     DELEGATE over HTTP to the DEPLOYED luxfi/mpc cluster (mpcclient.go), a
//     thin typed REST client. cloud is a faithful CLIENT of the real service —
//     it never imports github.com/luxfi/mpc (that drags chi/Postgres/HSM/
//     webauthn into the binary). Exactly the clients/mpcseal precedent. When the
//     cluster is not configured these backends fail CLOSED (ErrMPCNotConfigured);
//     a signature is NEVER fabricated.
//
// The seam is the whole point: swapping a wallet's custody is a config value on
// one row, not a code path. custody.go owns the interface + the three backends;
// mpcclient.go owns the mpc wire; store.go owns persistence; wallets.go owns HTTP.
package wallets

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/luxfi/crypto"
)

// Kind selects a wallet's custody backend. One interface, three backends.
type Kind string

const (
	KindKMS      Kind = "kms"      // in-process single-sig via the embedded luxfi/kms
	KindMPC      Kind = "mpc"      // m-of-n threshold via the deployed luxfi/mpc cluster (HTTP)
	KindTreasury Kind = "treasury" // named-signer governance via luxfi/mpc's treasury surface (HTTP)
	KindSafe     Kind = "safe"     // Safe (Gnosis-Safe-style) smart wallet on the ring, owned by an MPC EOA (HTTP: :9800 keygen + :8081 deploy/propose)
)

// defaultSafeChainID is the EVM chain a Safe binds to when its wallet is
// chain-agnostic. A Safe (and its EIP-712 domain) MUST be chain-bound, so this
// defaults to the Hanzo L1 (36963); a wallet's Chain overrides it per-wallet.
const defaultSafeChainID = 36963

// Valid reports whether k is one of the three supported custody kinds.
func (k Kind) Valid() bool {
	return k == KindKMS || k == KindMPC || k == KindTreasury || k == KindSafe
}

// Tier mirrors luxfi/mpc's 9-tier wallet model (pkg/wallet/tier.go) as string
// constants so cloud never imports the mpc package. The values are the wire
// contract the cluster keys its TierPolicy on; a local mirror keeps them in ONE
// place here and refuses an unknown tier at the boundary.
type Tier string

const (
	TierHot           Tier = "hot"
	TierWarm          Tier = "warm"
	TierCold          Tier = "cold"
	TierGas           Tier = "gas"
	TierBridge        Tier = "bridge"
	TierContractAdmin Tier = "contract_admin"
	TierValidator     Tier = "validator"
	TierQuarantine    Tier = "quarantine"
	TierDR            Tier = "disaster_recovery"
)

// DefaultTier is the tier a wallet gets when the request omits one.
const DefaultTier = TierHot

// allTiers is the membership set for validTier — the mirror of AllTiers.
var allTiers = map[Tier]bool{
	TierHot: true, TierWarm: true, TierCold: true, TierGas: true, TierBridge: true,
	TierContractAdmin: true, TierValidator: true, TierQuarantine: true, TierDR: true,
}

// validTier reports whether t is one of the 9 mirrored tiers.
func validTier(t Tier) bool { return allTiers[t] }

// Account is a named grouping of wallets owned by exactly one org.
type Account struct {
	ID        string `json:"id"`
	Org       string `json:"org"`
	Name      string `json:"name"`
	CreatedAt int64  `json:"createdAt"`
}

// Wallet is one signing identity. Custody selects the backend; KeyRef is the
// custody-internal HANDLE to the signing material (a KMS secret ref, or the mpc
// wallet id) — set by Provision, never a private-key VALUE, and never returned
// over the API.
type Wallet struct {
	ID             string `json:"id"`
	Org            string `json:"org"`
	AccountID      string `json:"accountId"`
	Name           string `json:"name"`
	Custody        Kind   `json:"custody"`
	Tier           Tier   `json:"tier"`
	Chain          string `json:"chain"`
	Address        string `json:"address"`
	KeyRef         string `json:"-"` // custody-internal handle; never serialized
	FinanceAccount string `json:"financeAccount,omitempty"`
	CreatedAt      int64  `json:"createdAt"`
}

// Fail-closed sentinels. ErrMPCNotConfigured is returned whenever an MPC/treasury
// wallet is provisioned or signed without a configured ring + internal API key —
// the operator must wire CLOUD_WALLETS_MPC_ADDR (and the KMS API-key ref).
var (
	ErrMPCNotConfigured = errors.New("mpc cluster not configured; set CLOUD_WALLETS_MPC_ADDR")
	ErrUnknownCustody   = errors.New("unknown custody kind")
)

// Custody is the ONE seam. Each backend creates signing material (Provision),
// signs a 32-byte digest (Sign), and rolls the material (Rotate). Provision and
// Rotate return the resulting pubkey ADDRESS and set w.KeyRef to the backend's
// handle for that material.
type Custody interface {
	Kind() Kind
	Provision(ctx context.Context, w *Wallet) (address string, err error)   // create signing material, set w.KeyRef, return address
	Sign(ctx context.Context, w *Wallet, digest []byte) (sig []byte, err error) // sign a 32-byte digest
	Rotate(ctx context.Context, w *Wallet) (address string, err error)      // roll key material, set w.KeyRef, return address
}

// keyRef is the org-scoped KMS secret ref for a KMS-custodied wallet's sealed
// signing key. Org-scoped so no tenant can address another's material.
func keyRef(w *Wallet) string { return "wallets/" + w.Org + "/" + w.ID }

// ── KMS single-sig custody (the fully-exercised spine) ───────────────────────

// kmsCustody generates a real secp256k1 key, seals the private bytes under the
// embedded KMS envelope, and signs by opening them in-process. The plaintext key
// is never logged, never returned, and never leaves this process except sealed.
type kmsCustody struct{ kms cloud.KMSClient }

func (kmsCustody) Kind() Kind { return KindKMS }

// Provision generates a key, seals its hex-encoded private bytes at keyRef(w),
// and returns the EVM address. It sets w.KeyRef to the KMS ref. Overwriting an
// existing ref (Rotate) is an upsert, so the same code rolls the key.
func (k kmsCustody) Provision(ctx context.Context, w *Wallet) (string, error) {
	priv, err := crypto.GenerateKey()
	if err != nil {
		return "", fmt.Errorf("wallets: generate key: %w", err)
	}
	addr := crypto.PubkeyToAddress(priv.PublicKey).Hex()
	ref := keyRef(w)
	// Seal the HEX of the 32-byte private key. crypto.FromECDSA returns the raw
	// scalar; we store its hex so GetSecret round-trips to HexToECDSA cleanly.
	if err := k.kms.PutSecret(ctx, ref, []byte(hex.EncodeToString(crypto.FromECDSA(priv)))); err != nil {
		return "", fmt.Errorf("wallets: seal key: %w", err)
	}
	w.KeyRef = ref
	return addr, nil
}

// Sign opens the sealed key and produces a recoverable secp256k1 signature over
// the 32-byte digest. The signature recovers to the wallet address.
func (k kmsCustody) Sign(ctx context.Context, w *Wallet, digest []byte) ([]byte, error) {
	if len(digest) != 32 {
		return nil, fmt.Errorf("wallets: digest must be 32 bytes, got %d", len(digest))
	}
	ref := w.KeyRef
	if ref == "" {
		ref = keyRef(w)
	}
	raw, err := k.kms.GetSecret(ctx, ref)
	if err != nil {
		return nil, fmt.Errorf("wallets: load key: %w", err)
	}
	priv, err := crypto.HexToECDSA(strings.TrimSpace(string(raw)))
	if err != nil {
		return nil, fmt.Errorf("wallets: parse sealed key: %w", err)
	}
	sig, err := crypto.Sign(digest, priv)
	if err != nil {
		return nil, fmt.Errorf("wallets: sign: %w", err)
	}
	return sig, nil
}

// Rotate generates fresh material and overwrites the sealed secret, returning
// the NEW address (a new key ⇒ a new address). PutSecret upserts, so this is
// exactly Provision over the same ref.
func (k kmsCustody) Rotate(ctx context.Context, w *Wallet) (string, error) {
	return k.Provision(ctx, w)
}

// ── MPC threshold custody (delegated to the luxfi/mpc cluster over HTTP) ──────

// mpcCustody creates an m-of-n wallet on the deployed luxfi/mpc ring (a
// threshold keygen) and signs by delegating the digest to the ring. org_id is
// the tenant scope; w.KeyRef holds the ring wallet id so Sign/Rotate can
// address it.
type mpcCustody struct{ http *mpcClient }

func (mpcCustody) Kind() Kind { return KindMPC }

func (m mpcCustody) Provision(ctx context.Context, w *Wallet) (string, error) {
	if !m.http.configured() {
		return "", ErrMPCNotConfigured
	}
	walletID, address, err := m.http.keygen(ctx, w.Org, "")
	if err != nil {
		return "", err
	}
	w.KeyRef = walletID // the ring wallet id — the Sign handle
	return address, nil
}

func (m mpcCustody) Sign(ctx context.Context, w *Wallet, digest []byte) ([]byte, error) {
	if !m.http.configured() {
		return nil, ErrMPCNotConfigured
	}
	if len(digest) != 32 {
		return nil, fmt.Errorf("wallets: digest must be 32 bytes, got %d", len(digest))
	}
	return m.http.sign(ctx, w.Org, w.KeyRef, evmChainID(w.Chain), digest)
}

// Rotate is a no-op for a ring wallet: the threshold shares are managed inside
// the fixed-membership ring, and the public key — and therefore the address —
// is invariant. So it returns w.Address unchanged.
func (m mpcCustody) Rotate(ctx context.Context, w *Wallet) (string, error) {
	if !m.http.configured() {
		return "", ErrMPCNotConfigured
	}
	return w.Address, nil
}

// ── Treasury custody (luxfi/mpc named-signer governance over HTTP) ────────────

// treasuryCustody is a reserve/treasury wallet backed by the SAME m-of-n ring
// threshold primitive as KindMPC. The distinction is governance, not signing
// mechanics: a treasury wallet's quorum policy (e.g. 3-of-5 named signers) is
// enforced by the finance/treasury policy layer over this primitive, not by a
// separate ring route. Same fail-closed rule.
type treasuryCustody struct{ http *mpcClient }

func (treasuryCustody) Kind() Kind { return KindTreasury }

func (t treasuryCustody) Provision(ctx context.Context, w *Wallet) (string, error) {
	if !t.http.configured() {
		return "", ErrMPCNotConfigured
	}
	walletID, address, err := t.http.keygen(ctx, w.Org, "")
	if err != nil {
		return "", err
	}
	w.KeyRef = walletID
	return address, nil
}

func (t treasuryCustody) Sign(ctx context.Context, w *Wallet, digest []byte) ([]byte, error) {
	if !t.http.configured() {
		return nil, ErrMPCNotConfigured
	}
	if len(digest) != 32 {
		return nil, fmt.Errorf("wallets: digest must be 32 bytes, got %d", len(digest))
	}
	return t.http.sign(ctx, w.Org, w.KeyRef, evmChainID(w.Chain), digest)
}

// Rotate preserves the address (ring-managed shares, invariant public key).
func (t treasuryCustody) Rotate(ctx context.Context, w *Wallet) (string, error) {
	if !t.http.configured() {
		return "", ErrMPCNotConfigured
	}
	return w.Address, nil
}

// evmChainID extracts the numeric EVM chain id from a wallet Chain string. It
// accepts a CAIP-2 "eip155:<n>" form or a bare decimal; anything else ⇒ 0
// (chain-agnostic), which the ring treats as an unbound digest sign.
func evmChainID(chain string) int {
	s := strings.TrimSpace(chain)
	if i := strings.LastIndex(s, ":"); i >= 0 {
		s = s[i+1:]
	}
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0
		}
		n = n*10 + int(r-'0')
	}
	if s == "" {
		return 0
	}
	return n
}

// ── Safe smart-wallet custody (deployed luxfi/mpc ring: :9800 + :8081) ────────

// safeCustody is a Safe (Gnosis-Safe-style) smart wallet owned by a threshold MPC
// EOA on the deployed ring. It composes the TWO ring planes without importing
// github.com/luxfi/mpc: the :9800 internal threshold API (mpcClient — keygen for
// the owner EOA + owner-approval signing) and the :8081 product API (safeClient —
// CREATE2 Safe deploy + EIP-712 Safe-tx propose). A Safe is a CONTRACT (no key of
// its own): its "signature" is the MPC EOA owner's approval. Same fail-closed rule
// as KindMPC — a Safe/address/signature is never fabricated.
type safeCustody struct {
	mpc  *mpcClient
	safe *safeClient
}

func (safeCustody) Kind() Kind { return KindSafe }

// Provision mints the owner EOA (threshold keygen on :9800), then deploys a
// counterfactual Safe owned by that EOA (threshold 1) on the wallet's EVM chain
// (:8081). w.Address becomes the CREATE2-predicted Safe contract address; w.KeyRef
// encodes BOTH ring handles ("<mpcWalletID>|<smartWalletID>").
func (s safeCustody) Provision(ctx context.Context, w *Wallet) (string, error) {
	if !s.mpc.configured() || !s.safe.configured() {
		return "", ErrMPCNotConfigured
	}
	walletID, mpcEOA, err := s.mpc.keygen(ctx, w.Org, "")
	if err != nil {
		return "", err
	}
	sw, err := s.safe.deploySafe(ctx, w.Org, walletID, safeChain(w.Chain), []string{mpcEOA}, 1)
	if err != nil {
		return "", err
	}
	w.KeyRef = walletID + "|" + sw.ID
	return sw.ContractAddress, nil
}

// Sign produces an OWNER-approval signature: the Safe's MPC EOA owner signs the
// 32-byte digest via the :9800 threshold API. The signature recovers to the owner
// EOA (a Safe has no key), keeping /v1/wallets/:id/sign uniform across custodies.
// The richer EIP-712 Safe-transaction flow is ProposeTx (safeProposer).
func (s safeCustody) Sign(ctx context.Context, w *Wallet, digest []byte) ([]byte, error) {
	if !s.mpc.configured() {
		return nil, ErrMPCNotConfigured
	}
	if len(digest) != 32 {
		return nil, fmt.Errorf("wallets: digest must be 32 bytes, got %d", len(digest))
	}
	walletID, _ := splitSafeRef(w.KeyRef)
	return s.mpc.sign(ctx, w.Org, walletID, evmChainID(w.Chain), digest)
}

// Rotate is a no-op: the Safe address is counterfactual (CREATE2 over the fixed
// owner set) and the owner's threshold shares are ring-managed, so both are
// invariant. Returns w.Address unchanged.
func (s safeCustody) Rotate(ctx context.Context, w *Wallet) (string, error) {
	if !s.mpc.configured() || !s.safe.configured() {
		return "", ErrMPCNotConfigured
	}
	return w.Address, nil
}

// ProposeTx proposes (and MPC-signs the EIP-712 hash of) a Safe transaction on the
// ring, satisfying the safeProposer capability. swID is the second KeyRef handle.
func (s safeCustody) ProposeTx(ctx context.Context, w *Wallet, tx SafeTx) (*SafeTxResult, error) {
	if !s.safe.configured() {
		return nil, ErrMPCNotConfigured
	}
	_, swID := splitSafeRef(w.KeyRef)
	if swID == "" {
		return nil, fmt.Errorf("wallets: wallet %s has no safe smart-wallet id", w.ID)
	}
	if tx.ChainID == 0 {
		tx.ChainID = int64(evmChainID(safeChain(w.Chain)))
	}
	return s.safe.proposeSafeTx(ctx, w.Org, swID, tx)
}

// safeProposer is the OPTIONAL capability a custody backend exposes when it can
// propose (and MPC-sign) a Safe transaction. Only safeCustody implements it; the
// /v1/wallets/:id/safe-tx handler type-asserts for it and 400s otherwise. This is
// the "values, not places" seam: the HTTP layer asks the backend whether it can
// propose, it does not switch on the Kind.
type safeProposer interface {
	ProposeTx(ctx context.Context, w *Wallet, tx SafeTx) (*SafeTxResult, error)
}

// splitSafeRef splits a KindSafe KeyRef "<mpcWalletID>|<smartWalletID>" into its
// two ring handles. A ref without the separator is treated as the MPC wallet id
// with no Safe id (Sign still works; ProposeTx fails closed).
func splitSafeRef(ref string) (mpcWalletID, smartWalletID string) {
	if i := strings.IndexByte(ref, '|'); i >= 0 {
		return ref[:i], ref[i+1:]
	}
	return ref, ""
}

// safeChain resolves the numeric EVM chain-id STRING the ring binds a Safe to
// (deploy label + EIP-712 domain). A chain-agnostic wallet defaults to the Hanzo
// L1 (36963); a wallet's Chain ("eip155:<n>" or bare decimal) overrides it.
func safeChain(chain string) string {
	if n := evmChainID(chain); n > 0 {
		return strconv.Itoa(n)
	}
	return strconv.Itoa(defaultSafeChainID)
}
