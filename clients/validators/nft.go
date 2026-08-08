package validators

// nft.go — the NFT-ownership trust boundary. A caller proves control of a wallet
// (a challenge signed with personal_sign, address recovered here) AND that the
// wallet holds a Validator-tier GenesisNFT on Ethereum mainnet (ownerOf read
// against the canonical ERC-721). Only then is a slot entitlement admitted.
//
// Read logic mirrors ~/work/lux/state/pkg/bridge/nft_scanner.go (luxfi/geth
// ethclient + ERC-721 ABI); signature recovery mirrors clients/wallets/anchor.go
// (luxfi/crypto SigToPub/PubkeyToAddress). One dependency set, reused.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"math/big"
	"strconv"
	"strings"

	"github.com/luxfi/crypto"
	ethereum "github.com/luxfi/geth"
	"github.com/luxfi/geth/accounts/abi"
	"github.com/luxfi/geth/common"
	"github.com/luxfi/geth/ethclient"
)

// GenesisNFTContract is the ETH-mainnet ERC-721 whose Validator-tier tokens gate
// validator onboarding. Overridable via VALIDATORS_NFT_CONTRACT for testnet, but
// this is the live mainnet address (50 minted / 28 holders as of the Genesis
// re-mint). NEVER the dead C-Chain copy or the GaugeController mis-address.
const GenesisNFTContract = "0x31e0F919C67ceDd2Bc3E294340Dc900735810311"

// erc721OwnerBalanceABI is the minimal read surface: ownerOf(tokenId) proves who
// holds a SPECIFIC slot; balanceOf(owner) is retained for holder-count views.
const erc721OwnerBalanceABI = `[
  {"constant":true,"inputs":[{"name":"tokenId","type":"uint256"}],"name":"ownerOf","outputs":[{"name":"","type":"address"}],"stateMutability":"view","type":"function"},
  {"constant":true,"inputs":[{"name":"owner","type":"address"}],"name":"balanceOf","outputs":[{"name":"","type":"uint256"}],"stateMutability":"view","type":"function"}
]`

// nftReader reads ownership from an EVM chain. Config-driven (RPC + contract)
// so testnet points at a testnet deployment without a code change.
type nftReader struct {
	rpc      string
	contract common.Address
	// validatorSlots is the Validator-tier bound: tokenIds in [1, validatorSlots]
	// are validator slots (the tokenId IS the slot). Matches the portal's
	// VALIDATOR_SLOTS_AUTHORIZED and lux/tokenomics eth-authorized-supply.
	validatorSlots uint64
	parsed         abi.ABI
}

func newNFTReader(rpcURL, contract string, validatorSlots uint64) (*nftReader, error) {
	parsed, err := abi.JSON(strings.NewReader(erc721OwnerBalanceABI))
	if err != nil {
		return nil, fmt.Errorf("parse ERC-721 ABI: %w", err)
	}
	return &nftReader{
		rpc:            rpcURL,
		contract:       common.HexToAddress(contract),
		validatorSlots: validatorSlots,
		parsed:         parsed,
	}, nil
}

// isValidatorTier reports whether tokenId is a Validator-tier slot. The tokenId
// IS the slot; slots 1..validatorSlots are the authorized validator tier.
func (r *nftReader) isValidatorTier(tokenID uint64) bool {
	return tokenID >= 1 && tokenID <= r.validatorSlots
}

// observation is the complete, citable fact one verification produced: which
// collection, on which chain, at which block, held by whom. It is what an
// ownership attestation commits to, and it is persisted with the slot so a later
// config change cannot retroactively alter what was actually checked.
type observation struct {
	Chain      uint64
	Collection string
	Token      uint64
	Owner      common.Address
	Block      uint64
}

// ownerOf reads the on-chain owner of tokenId AT an explicit block and reports the
// whole observation. A revert (nonexistent token) surfaces as an error, never a
// zero address masquerading as an owner.
//
// The block is pinned rather than left implicit ("latest") because ownership is a
// fact about a moment. An unpinned read cannot be re-checked by anyone else: a
// later reader sees a different chain tip and cannot tell a transfer from a lie.
// Pinning is what makes the answer citable instead of an opinion.
//
// The chain id comes from the RPC itself rather than from configuration, so the
// observation always names the chain the read actually happened on.
func (r *nftReader) ownerOf(ctx context.Context, tokenID uint64) (observation, error) {
	obs := observation{Collection: strings.ToLower(r.contract.Hex()), Token: tokenID}
	cl, err := ethclient.DialContext(ctx, r.rpc)
	if err != nil {
		return obs, fmt.Errorf("dial eth rpc: %w", err)
	}
	defer cl.Close()

	chain, err := cl.ChainID(ctx)
	if err != nil {
		return obs, fmt.Errorf("read eth chain id: %w", err)
	}
	obs.Chain = chain.Uint64()
	block, err := cl.BlockNumber(ctx)
	if err != nil {
		return obs, fmt.Errorf("read eth block number: %w", err)
	}
	obs.Block = block

	data, err := r.parsed.Pack("ownerOf", new(big.Int).SetUint64(tokenID))
	if err != nil {
		return obs, fmt.Errorf("pack ownerOf: %w", err)
	}
	out, err := cl.CallContract(ctx, ethereum.CallMsg{To: &r.contract, Data: data}, new(big.Int).SetUint64(block))
	if err != nil {
		return obs, fmt.Errorf("ownerOf(%d) call at block %d: %w", tokenID, block, err)
	}
	vals, err := r.parsed.Unpack("ownerOf", out)
	if err != nil || len(vals) == 0 {
		return obs, fmt.Errorf("ownerOf(%d) decode: %w", tokenID, err)
	}
	owner, ok := vals[0].(common.Address)
	if !ok {
		return obs, fmt.Errorf("ownerOf(%d): unexpected return type", tokenID)
	}
	obs.Owner = owner
	return obs, nil
}

// verifyOwnership confirms `addr` is the on-chain owner of a Validator-tier
// tokenId and returns the observation that proves it. It fails closed: a
// non-validator tier, a read error, or an owner mismatch all reject. This is the
// on-chain half of the entitlement proof; the wallet-control half is recoverSigner.
//
// The returned observation is what a later attestation commits to, so the fact this
// service checked is the same fact anyone else can re-check.
func (r *nftReader) verifyOwnership(ctx context.Context, tokenID uint64, addr common.Address) (observation, error) {
	if !r.isValidatorTier(tokenID) {
		return observation{}, fmt.Errorf("token %d is not a Validator-tier slot (1..%d)", tokenID, r.validatorSlots)
	}
	obs, err := r.ownerOf(ctx, tokenID)
	if err != nil {
		return observation{}, err
	}
	if obs.Owner != addr {
		return observation{}, fmt.Errorf("wallet %s does not own validator slot %d (owner is %s)", addr.Hex(), tokenID, obs.Owner.Hex())
	}
	return obs, nil
}

// ── wallet-control proof (EIP-191 personal_sign) ────────────────────────────

// challengeMessage is the canonical human-readable string the caller signs with
// personal_sign. The server reconstructs it verbatim from the VALIDATED org
// (JWT), the requested tokenId, and the issued nonce — so a signature is bound
// to (org, slot, nonce) and cannot be replayed for a different org/slot/session.
func challengeMessage(org string, tokenID uint64, nonce string) string {
	return "Lux Validator Onboarding\n" +
		"Organization: " + org + "\n" +
		"Validator slot: " + strconv.FormatUint(tokenID, 10) + "\n" +
		"Nonce: " + nonce
}

// recoverSigner recovers the Ethereum address that produced an EIP-191
// personal_sign signature over `message`. sigHex is the 65-byte r‖s‖v signature
// (v as 27/28 or 0/1) as returned by window.ethereum personal_sign. Mirrors the
// recovery in clients/wallets/anchor.go.
func recoverSigner(message, sigHex string) (common.Address, error) {
	sig, err := hex.DecodeString(strings.TrimPrefix(strings.TrimSpace(sigHex), "0x"))
	if err != nil {
		return common.Address{}, fmt.Errorf("decode signature hex: %w", err)
	}
	if len(sig) != 65 {
		return common.Address{}, fmt.Errorf("signature must be 65 bytes, got %d", len(sig))
	}
	// personal_sign yields v ∈ {27,28}; crypto.SigToPub wants {0,1}.
	if sig[64] >= 27 {
		sig[64] -= 27
	}
	if sig[64] > 1 {
		return common.Address{}, fmt.Errorf("invalid signature recovery id %d", sig[64])
	}
	pub, err := crypto.SigToPub(personalSignHash(message), sig)
	if err != nil {
		return common.Address{}, fmt.Errorf("recover pubkey: %w", err)
	}
	// luxfi/crypto returns its OWN common.Address ([20]byte); convert to the
	// luxfi/geth common.Address the ethclient/ABI path uses (identical layout).
	return common.Address(crypto.PubkeyToAddress(*pub)), nil
}

// personalSignHash is the EIP-191 digest: keccak256("\x19Ethereum Signed
// Message:\n" + len(msg) + msg). This is exactly what an EIP-1193 wallet hashes
// for personal_sign, so recovering over it yields the signing wallet.
func personalSignHash(message string) []byte {
	prefixed := "\x19Ethereum Signed Message:\n" + strconv.Itoa(len(message)) + message
	return crypto.Keccak256([]byte(prefixed))
}

// newNonce returns a 128-bit random hex nonce for a challenge.
func newNonce() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}
