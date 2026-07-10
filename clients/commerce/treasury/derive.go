// Package treasury is the chain-backed credit ledger's mint authority boundary.
//
// The recurring C1 money-mint vulnerability class exists because credits are a
// mutable DB number any code path can increment. This package removes that by
// construction: a credit exists ONLY as HUSD (Hanzo USD, ERC-20 on the Hanzo
// EVM) minted by the treasury key — which lives in KMS, never in this process.
// Commerce can only REQUEST a mint (treasury.Mint); the request is refused
// unless the caller carries proven mint authority (mintauth), and the value is
// created by a treasury-signed on-chain transaction, not a DB write.
//
// This file (derive.go) is Step 1: deterministic per-org on-chain addresses.
// The same (master seed, orgID) yields the same EVM address byte-for-byte
// across processes and restarts, so an org's HUSD balance has one stable home
// on chain; distinct orgs get distinct addresses with cryptographic certainty.
// The master seed is KMS-sourced (like the treasury key) and never persisted.
package treasury

import (
	"crypto/hmac"
	"crypto/sha512"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	luxcrypto "github.com/luxfi/crypto"
)

// derivationInfoPrefix domain-separates HUSD org-address derivation from any
// other use of the same master seed. Versioned so a future derivation change is
// a new prefix (old addresses stay reproducible), never a silent redefinition.
const derivationInfoPrefix = "hanzo-husd-org-v1:"

// minSeedLen is the smallest master seed we accept (128 bits of entropy).
const minSeedLen = 16

// ErrSeedTooShort is returned when the master derivation seed is missing or has
// insufficient entropy. Fail closed: no seed ⇒ no addresses ⇒ no mint target.
var ErrSeedTooShort = fmt.Errorf("treasury: master derivation seed must be >= %d bytes", minSeedLen)

// ErrEmptyOrg is returned when an empty org id is passed to derivation.
var ErrEmptyOrg = errors.New("treasury: org id must not be empty")

// Account is a per-org on-chain HUSD holding: a deterministic EVM address (where
// the org's HUSD balance lives — safe to expose and index) and the secp256k1
// signing key for it. The private key is held in memory only, never persisted
// and never logged; it is needed only to SIGN settlement transfers (org →
// treasury) in Step 5. Minting only ever needs the Address.
type Account struct {
	OrgID   string
	Address string // 0x-prefixed, lowercased for stable indexing/matching

	privateKeyHex string // hex (no 0x); in-memory only
}

// PrivateKeyHex returns the account's secp256k1 private key as hex (no 0x). It
// is the settlement-signing key; callers must treat it as a secret and must
// never log or persist it.
func (a Account) PrivateKeyHex() string { return a.privateKeyHex }

// DeriveAccount deterministically derives an org's HUSD account from a master
// seed, BIP-32-style: I = HMAC-SHA512(seed, "hanzo-husd-org-v1:"||orgID||ctr),
// take the left 256 bits as the secp256k1 scalar. The counter increments only
// on the astronomically rare event that the scalar is not a valid key (>= curve
// order or zero) — so derivation is total and deterministic. Distinct orgIDs
// produce distinct scalars (HMAC collision resistance) hence distinct
// addresses; the mapping is collision-safe for arbitrary string org ids (no
// lossy string→uint32 BIP-44 index).
func DeriveAccount(masterSeed []byte, orgID string) (Account, error) {
	if len(masterSeed) < minSeedLen {
		return Account{}, ErrSeedTooShort
	}
	if orgID == "" {
		return Account{}, ErrEmptyOrg
	}

	for ctr := 0; ctr < 256; ctr++ {
		mac := hmac.New(sha512.New, masterSeed)
		mac.Write([]byte(derivationInfoPrefix))
		mac.Write([]byte(orgID))
		mac.Write([]byte{byte(ctr)})
		I := mac.Sum(nil)

		sk, err := luxcrypto.ToECDSA(I[:32]) // validates 1 <= scalar < N
		if err != nil {
			continue // invalid scalar (~2^-128), advance the counter deterministically
		}
		return Account{
			OrgID:         orgID,
			Address:       normalizeAddr(luxcrypto.PubkeyToAddress(sk.PublicKey).Hex()),
			privateKeyHex: hex.EncodeToString(luxcrypto.FromECDSA(sk)),
		}, nil
	}
	// 256 consecutive invalid scalars is impossible for secp256k1 (P ≈ 2^-32768).
	return Account{}, errors.New("treasury: derivation exhausted counter (unreachable)")
}

// AddressForOrg is the mint-side convenience: it derives only the org's on-chain
// address, discarding the private key (minting a credit only needs where to send
// the HUSD). Use DeriveAccount when you also need the settlement-signing key.
func AddressForOrg(masterSeed []byte, orgID string) (string, error) {
	a, err := DeriveAccount(masterSeed, orgID)
	if err != nil {
		return "", err
	}
	return a.Address, nil
}

// AddressForKey returns the lowercased 0x EVM address controlled by a secp256k1
// private key (hex, with or without 0x). It is the inverse of "what address does
// this signer send FROM" — used to locate the treasury's own on-chain address
// (settlement destination, Step 5) and to reconcile a derived org account's
// address against the key that will sign its settlement transfers.
func AddressForKey(privHex string) (string, error) {
	privHex = strings.TrimPrefix(strings.TrimPrefix(privHex, "0x"), "0X")
	sk, err := luxcrypto.HexToECDSA(privHex)
	if err != nil {
		return "", fmt.Errorf("treasury: decode signing key: %w", err)
	}
	return normalizeAddr(luxcrypto.PubkeyToAddress(sk.PublicKey).Hex()), nil
}

// SeedFromHex decodes a hex master seed (with or without 0x) sourced from KMS
// (env HUSD_ORG_DERIVATION_SEED). It enforces the minimum entropy so a
// misconfigured deploy fails closed rather than deriving weak addresses.
func SeedFromHex(s string) ([]byte, error) {
	if len(s) >= 2 && (s[:2] == "0x" || s[:2] == "0X") {
		s = s[2:]
	}
	b, err := hex.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("treasury: decode derivation seed: %w", err)
	}
	if len(b) < minSeedLen {
		return nil, ErrSeedTooShort
	}
	return b, nil
}

// normalizeAddr lowercases a 0x EVM address for stable equality + index keys.
// On-chain matching (Transfer topics, balanceOf) is case-insensitive; we pick
// lowercase as the one canonical form used everywhere in this package.
func normalizeAddr(a string) string {
	out := make([]byte, len(a))
	for i := 0; i < len(a); i++ {
		c := a[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		out[i] = c
	}
	return string(out)
}
