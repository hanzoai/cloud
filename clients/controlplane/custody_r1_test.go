//go:build controlplane

package controlplane

// custody_r1_test.go — RED finding R1: the registry is the trusted key source
// for the whole cert, so key binding must be first-writer-wins with a
// self-authorized rotation path. These tests lock that.

import (
	"crypto/rand"
	"errors"
	"testing"

	"github.com/luxfi/crypto/mldsa"
)

func r1Key(t *testing.T) *mldsa.PrivateKey {
	t.Helper()
	sk, err := mldsa.GenerateKey(rand.Reader, mldsa.MLDSA65)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	return sk
}

// TestRegistry_FirstWriterWins_RefusesKeySwap — a node already bound to a key
// cannot be re-registered under a DIFFERENT key (the rogue-key swap). The
// original key is unchanged.
func TestRegistry_FirstWriterWins_RefusesKeySwap(t *testing.T) {
	reg := NewValidatorRegistry()
	node := NodeIDFromName("cloud-0")
	sk1, sk2 := r1Key(t), r1Key(t)

	if err := reg.Register(Share{Index: 1, Node: node, idKey: sk1}); err != nil {
		t.Fatalf("first register: %v", err)
	}
	if err := reg.Register(Share{Index: 1, Node: node, idKey: sk2}); !errors.Is(err, ErrIdentityKeyConflict) {
		t.Fatalf("key swap: want ErrIdentityKeyConflict, got %v", err)
	}
	if pub, ok := reg.idVerifier[node]; !ok || !pubKeyEqual(pub, sk1.PublicKey) {
		t.Fatal("the swap attempt overwrote the original key")
	}
}

// TestRegistry_IdempotentSameKey — re-registering the IDENTICAL key is allowed
// (crash-restart / re-sync must not fail).
func TestRegistry_IdempotentSameKey(t *testing.T) {
	reg := NewValidatorRegistry()
	node := NodeIDFromName("cloud-0")
	sk := r1Key(t)
	if err := reg.Register(Share{Index: 1, Node: node, idKey: sk}); err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := reg.Register(Share{Index: 1, Node: node, idKey: sk}); err != nil {
		t.Fatalf("idempotent re-register of the same key must succeed: %v", err)
	}
}

// TestRegistry_Rekey_SelfAuthorized — the sanctioned rotation: a signature under
// the CURRENT key authorizes the new key, and the registry updates.
func TestRegistry_Rekey_SelfAuthorized(t *testing.T) {
	reg := NewValidatorRegistry()
	node := NodeIDFromName("cloud-0")
	old, newk := r1Key(t), r1Key(t)
	if err := reg.Register(Share{Index: 1, Node: node, idKey: old}); err != nil {
		t.Fatalf("register: %v", err)
	}
	authSig, err := old.SignCtxDeterministic(rekeyTBS(node, newk.PublicKey), rekeyContext)
	if err != nil {
		t.Fatalf("authorize: %v", err)
	}
	if err := reg.Rekey(node, newk.PublicKey, authSig); err != nil {
		t.Fatalf("authorized rekey: %v", err)
	}
	if !pubKeyEqual(reg.idVerifier[node], newk.PublicKey) {
		t.Fatal("rekey did not install the new key")
	}
}

// TestRegistry_Rekey_UnauthorizedRefused — an attacker without the current
// secret key cannot rotate: a rekey authorized under any OTHER key is refused
// and the bound key is untouched.
func TestRegistry_Rekey_UnauthorizedRefused(t *testing.T) {
	reg := NewValidatorRegistry()
	node := NodeIDFromName("cloud-0")
	old, newk, attacker := r1Key(t), r1Key(t), r1Key(t)
	if err := reg.Register(Share{Index: 1, Node: node, idKey: old}); err != nil {
		t.Fatalf("register: %v", err)
	}
	badSig, _ := attacker.SignCtxDeterministic(rekeyTBS(node, newk.PublicKey), rekeyContext)
	if err := reg.Rekey(node, newk.PublicKey, badSig); !errors.Is(err, ErrRekeyUnauthorized) {
		t.Fatalf("unauthorized rekey: want ErrRekeyUnauthorized, got %v", err)
	}
	if !pubKeyEqual(reg.idVerifier[node], old.PublicKey) {
		t.Fatal("unauthorized rekey mutated the key")
	}
}

// TestRegistry_Rekey_UnknownNode — rotating a node that was never registered is
// refused (no current key to authorize against).
func TestRegistry_Rekey_UnknownNode(t *testing.T) {
	reg := NewValidatorRegistry()
	node := NodeIDFromName("ghost")
	newk := r1Key(t)
	sig, _ := newk.SignCtxDeterministic(rekeyTBS(node, newk.PublicKey), rekeyContext)
	if err := reg.Rekey(node, newk.PublicKey, sig); !errors.Is(err, ErrRekeyUnknownNode) {
		t.Fatalf("rekey unknown node: want ErrRekeyUnknownNode, got %v", err)
	}
}
