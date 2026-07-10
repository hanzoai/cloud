package treasury

import (
	"regexp"
	"strings"
	"testing"

	luxcrypto "github.com/luxfi/crypto"
)

var addrShape = regexp.MustCompile(`^0x[0-9a-f]{40}$`)

// A fixed, non-secret seed used only for deterministic test vectors.
var testSeed = []byte("hanzo-husd-org-derivation-test-seed-0001")

func TestDeriveAccount_Deterministic(t *testing.T) {
	a1, err := DeriveAccount(testSeed, "gzh2BOBnV6gKZQ0CP")
	if err != nil {
		t.Fatal(err)
	}
	a2, err := DeriveAccount(testSeed, "gzh2BOBnV6gKZQ0CP")
	if err != nil {
		t.Fatal(err)
	}
	if a1.Address != a2.Address {
		t.Fatalf("non-deterministic: %s != %s", a1.Address, a2.Address)
	}
	if a1.PrivateKeyHex() != a2.PrivateKeyHex() {
		t.Fatal("non-deterministic private key")
	}
	if !addrShape.MatchString(a1.Address) {
		t.Fatalf("address %q is not a lowercased 0x-address", a1.Address)
	}
}

// GOLDEN VECTOR: this address MUST NOT change for this (seed, org) pair. If a
// derivation change ever alters it, every org's on-chain balance home moves —
// a migration-breaking event. The prefix is versioned precisely so such a
// change is a NEW prefix, never a silent redefinition. Pin it here so CI fails
// loudly on any accidental drift.
func TestDeriveAccount_GoldenVector(t *testing.T) {
	const wantAddr = "0xe31e41e468893c44a4011d80b3315f1c362ba565"
	a, err := DeriveAccount(testSeed, "hanzo")
	if err != nil {
		t.Fatal(err)
	}
	if a.Address != wantAddr {
		t.Errorf("golden drift: hanzo=%s, want %s", a.Address, wantAddr)
	}
}

func TestDeriveAccount_DistinctOrgs(t *testing.T) {
	seen := map[string]string{}
	orgs := []string{"hanzo", "lux", "zoo", "pars", "acme", "gzh2BOBnV6gKZQ0CP", "a", "ab", "abc"}
	for _, org := range orgs {
		a, err := DeriveAccount(testSeed, org)
		if err != nil {
			t.Fatal(err)
		}
		if prev, ok := seen[a.Address]; ok {
			t.Fatalf("address collision: org %q and %q both -> %s", org, prev, a.Address)
		}
		seen[a.Address] = org
	}
}

// Different master seeds yield different addresses for the same org.
func TestDeriveAccount_SeedSensitivity(t *testing.T) {
	seedB := append([]byte(nil), testSeed...)
	seedB[0] ^= 0x01
	a, _ := DeriveAccount(testSeed, "hanzo")
	b, _ := DeriveAccount(seedB, "hanzo")
	if a.Address == b.Address {
		t.Fatal("changing the master seed must change the derived address")
	}
}

// The derived private key must actually control the derived address: recover
// the address from the key exactly as the chain would.
func TestDeriveAccount_KeyControlsAddress(t *testing.T) {
	a, err := DeriveAccount(testSeed, "hanzo")
	if err != nil {
		t.Fatal(err)
	}
	sk, err := luxcrypto.HexToECDSA(a.PrivateKeyHex())
	if err != nil {
		t.Fatalf("derived key not a valid secp256k1 key: %v", err)
	}
	recovered := strings.ToLower(luxcrypto.PubkeyToAddress(sk.PublicKey).Hex())
	if recovered != a.Address {
		t.Fatalf("key does not control address: key->%s but Address=%s", recovered, a.Address)
	}
}

func TestDeriveAccount_Errors(t *testing.T) {
	if _, err := DeriveAccount([]byte("short"), "hanzo"); err != ErrSeedTooShort {
		t.Errorf("short seed: want ErrSeedTooShort, got %v", err)
	}
	if _, err := DeriveAccount(testSeed, ""); err != ErrEmptyOrg {
		t.Errorf("empty org: want ErrEmptyOrg, got %v", err)
	}
}

func TestSeedFromHex(t *testing.T) {
	raw := "0011223344556677889900aabbccddeeff00112233445566778899aabbccddee" // 32 bytes
	b, err := SeedFromHex(raw)
	if err != nil || len(b) != 32 {
		t.Fatalf("SeedFromHex(bare): %v len=%d", err, len(b))
	}
	b2, err := SeedFromHex("0x" + raw)
	if err != nil {
		t.Fatalf("SeedFromHex(0x): %v", err)
	}
	if string(b) != string(b2) {
		t.Fatal("0x prefix changed decoded seed")
	}
	if _, err := SeedFromHex("nothex!!"); err == nil {
		t.Error("SeedFromHex(bad) should error")
	}
	if _, err := SeedFromHex("00112233"); err != ErrSeedTooShort {
		t.Errorf("SeedFromHex(short): want ErrSeedTooShort, got %v", err)
	}
}

func TestAddressForOrg(t *testing.T) {
	addr, err := AddressForOrg(testSeed, "hanzo")
	if err != nil {
		t.Fatal(err)
	}
	full, _ := DeriveAccount(testSeed, "hanzo")
	if addr != full.Address {
		t.Fatalf("AddressForOrg=%s != DeriveAccount.Address=%s", addr, full.Address)
	}
}
