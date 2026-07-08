//go:build controlplane_deps

// controlplane_deps.go anchors the luxfi consensus stack as DIRECT go.mod
// requirements ahead of the control-plane wiring (consensus platform, Stage 0).
//
// Architecture DIRECTION (proposed / designed — NOT shipped in this stage): the
// control plane is designed to run Quasar, the post-quantum BFT protocol in
// github.com/luxfi/consensus (protocol/quasar, Submit -> Finalized), under a
// strict-PQ certificate profile (NetworkPolicyStrictPQ) with a Pulsar RoundSigner
// threshold signer — an epoch engine constructed via consensus.NewBFT over the
// engine/bft Epoch, the github.com/luxfi/p2p transport, and a
// github.com/luxfi/validators.Manager validator set. Stage 0 ships NONE of that:
// no engine is imported, constructed, or started, and no route is added.
//
// These blank imports promote consensus, bft, p2p, and validators from indirect
// to DIRECT requires at the EXACT versions the module graph already selects
// (consensus v1.25.15, bft v0.1.5, p2p v1.21.1, validators v1.2.0), so the
// promotion changes nothing about the compiled binary. The controlplane_deps
// build tag is never set in any normal build, test, or vet, so this file is not
// compiled into the cloud binary — nothing here is linked, imported, initialized,
// or started at runtime. go mod tidy still reads these imports (it considers files
// under every build tag) and therefore keeps the four modules direct. Deleting
// this file reverts them to indirect: the promotion is fully reversible.
//
// When the control plane lands and imports these packages for real, this anchor
// becomes redundant and should be removed.
package cloud

import (
	_ "github.com/luxfi/bft"
	_ "github.com/luxfi/consensus"
	_ "github.com/luxfi/p2p"
	_ "github.com/luxfi/validators"
)
