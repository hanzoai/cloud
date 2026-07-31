// Package validators mounts the Hanzo Cloud /v1/validators/* surface: the
// "click → provision node + queue registration" pipeline behind GDA/SDM
// validator onboarding on lux.cloud.
//
// The end-to-end claim, all server-enforced at cloud's ONE auth boundary
// (SanitizeIdentity → principal.Org):
//
//  1. GET  /v1/validators/challenge?tokenId=N  → a single-use, org-bound nonce +
//     the exact message to personal_sign.
//  2. POST /v1/validators {tokenId,nonce,signature} → verify the wallet controls
//     the signature AND holds Validator-tier GenesisNFT #tokenId on Ethereum
//     mainnet (ownerOf), then: generate a luxd staking identity → seal it into
//     KMS (never plaintext) → write a LuxNetwork CR for a NEW node (never the
//     live luxd) → ENQUEUE an owner-gated registration (NEVER auto-submitted to
//     any P-Chain). Returns the slot + node + registration status.
//  3. GET  /v1/validators            → the org's claimed slots + node status.
//  4. GET  /v1/validators/:tokenId   → one slot's detail.
//
// Tenant isolation is the org (principal.Org — the VALIDATED IAM owner, never a
// client header); every store query filters WHERE org=?. The tokenId IS the
// validator slot. serve.go auto-registers GET /v1/validators/health.
//
// EVERY ROUTE IS A TYPED OP (zip.Get/Post with concrete In/Out structs), so the
// surface is ONE registry with N projections — REST, the OpenAPI document, the
// MCP tool list and the CLI all derive from these same registrations. The prose
// in each handler's doc comment is lifted into the spec by the build-time
// cmd/zipdoc pass, because Go does not keep comments at run time.
package validators

//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/zap-proto/zip"
)

const (
	defaultListLimit = 200
	maxListLimit     = 1000
)

// networkIDs maps a network slug to the luxd primary networkID the new node
// syncs. Devnet is the default target for Phase-1 onboarding; mainnet join is
// owner-gated (the registration is queued, never auto-submitted, regardless).
var networkIDs = map[string]int32{
	"mainnet": 1, "testnet": 2, "devnet": 3, "localnet": 1337,
}

// state is the validators subsystem's own data; shared deps (logger, KMS, brand)
// live in the embedded cloud.Base.
type state struct {
	store   *Store
	nft     *nftReader
	prov    nodeProvisioner
	network string        // network slug new nodes join (default devnet)
	netID   int32         // resolved networkID
	ttl     time.Duration // challenge TTL
}

// mounted is the active service so Shutdown can release the store.
var mounted *cloud.Service[state]

// Mount wires the validators surface onto app per HIP-0106.
func Mount(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("validators.Mount: nil app")
	}
	zapp := cloud.ZipApp(app)
	if zapp == nil {
		return fmt.Errorf("validators.Mount: router is not backed by a *zip.App; typed ops have nowhere to register")
	}
	if deps.Logger == nil {
		return fmt.Errorf("validators.Mount: nil deps.Logger")
	}
	if deps.DataDir == "" {
		return fmt.Errorf("validators.Mount: empty DataDir")
	}
	if err := os.MkdirAll(deps.DataDir, 0o755); err != nil {
		return fmt.Errorf("validators.Mount: data dir: %w", err)
	}
	store, err := openStore(filepath.Join(deps.DataDir, "validators.db"))
	if err != nil {
		return fmt.Errorf("validators.Mount: open store: %w", err)
	}

	slots := uint64(envInt("VALIDATORS_SLOTS", 100))
	nft, err := newNFTReader(
		envOr("VALIDATORS_ETH_RPC", "https://ethereum-rpc.publicnode.com"),
		envOr("VALIDATORS_NFT_CONTRACT", GenesisNFTContract),
		slots,
	)
	if err != nil {
		_ = store.Close()
		return fmt.Errorf("validators.Mount: nft reader: %w", err)
	}

	network := envOr("VALIDATORS_NETWORK", "devnet")
	netID, ok := networkIDs[network]
	if !ok {
		_ = store.Close()
		return fmt.Errorf("validators.Mount: unknown VALIDATORS_NETWORK %q", network)
	}

	prov := newK8sProvisioner(crConfig{
		Group:     envOr("VALIDATORS_CR_GROUP", "node.lux.cloud"),
		Namespace: envOr("VALIDATORS_NAMESPACE", "lux-validators"),
		NodeImage: envOr("VALIDATORS_NODE_IMAGE", "ghcr.io/luxfi/node:v1.36.15"),
		KMSHost:   envOr("VALIDATORS_KMS_HOST", "http://cloud."+deps.Brand+".svc.cluster.local:8000"),
		KMSCreds:  envOr("VALIDATORS_KMS_CREDS", "platform-kms-auth"),
		StorageGi: envInt("VALIDATORS_STORAGE_GI", 200),
	})

	b := cloud.NewBase(deps, "validators")
	s := &cloud.Service[state]{Base: b, State: state{
		store:   store,
		nft:     nft,
		prov:    prov,
		network: network,
		netID:   netID,
		ttl:     time.Duration(envInt("VALIDATORS_CHALLENGE_TTL_SECONDS", 600)) * time.Second,
	}}
	mounted = s

	routes(app, zapp, s)
	b.Log.Info("validators mounted", "brand", deps.Brand, "network", network,
		"nftContract", nft.contract.Hex(), "clusterReady", prov.Available())
	return nil
}

func routes(app cloud.Router, zapp *zip.App, s *cloud.Service[state]) {
	o := ops{s: s}
	// The bridge FIRST: fiber runs middleware in registration order, so one
	// installed after these leaves would never run — and every op below resolves
	// its tenant through it. Bounded to validators' own subtree.
	app.Group("/v1/validators").Use(cloud.Bridge())

	// The collection root (/v1/validators) stays FLAT: Group("/v1/validators").
	// Get("")/Post("") would register "/v1/validators/" (trailing slash), which
	// the portal's bare /v1/validators calls would miss. Same gotcha the
	// clients/wallets, guide, link, … subsystems document.
	zip.Get(zapp, "/v1/validators", o.listValidators)
	zip.Post(zapp, "/v1/validators", o.provisionValidator)

	// /challenge is registered BEFORE /:tokenId: zip is first-match, so the
	// literal must precede the param that would otherwise swallow it.
	zip.Get(zapp, "/v1/validators/challenge", o.issueChallenge)
	zip.Get(zapp, "/v1/validators/:tokenId", o.getValidator)
}

// ops binds the service to validators' typed handlers. A TypedHandler is
// func(context.Context, *In) (*Out, error) — no parameter for the service — so it
// arrives as a RECEIVER and every op is a method value, the only bound form
// cmd/zipdoc can lift prose from.
type ops struct{ s *cloud.Service[state] }

// tenant resolves the org — the tenant-isolation KEY — for an org-scoped op. It
// is EXACTLY what SanitizeIdentity minted from the validated IAM owner claim,
// carried across the typed seam by cloud.Bridge, never read from the input.
func tenant(ctx context.Context) (string, error) {
	org, ok := principal.OrgFrom(ctx)
	if !ok {
		return "", zip.ErrForbidden("validated identity required")
	}
	return org, nil
}

// ── wire types ──────────────────────────────────────────────────────────────

// SlotRef addresses one validator slot by its GenesisNFT token id.
type SlotRef struct {
	// TokenID is the GenesisNFT id of the slot; it IS the validator slot number.
	TokenID uint64 `json:"tokenId"`
}

// Challenge is the single-use, org-bound nonce plus the exact text to sign.
type Challenge struct {
	// Nonce is the single-use value bound to (org, slot); claiming burns it.
	Nonce string `json:"nonce"`
	// Message is the exact text the wallet must personal_sign, byte for byte.
	Message string `json:"message"`
	// TokenID echoes the slot the challenge is bound to.
	TokenID uint64 `json:"tokenId"`
	// ExpiresAt is the unix second after which the nonce is refused.
	ExpiresAt int64 `json:"expiresAt"`
	// TTLSeconds is the lifetime the nonce was issued with.
	TTLSeconds int `json:"ttlSeconds"`
}

// ProvisionRequest is the claim: the slot, the issued nonce, and the
// personal_sign signature over the challenge message.
type ProvisionRequest struct {
	// TokenID is the Validator-tier GenesisNFT id being claimed. Required.
	TokenID uint64 `json:"tokenId"`
	// Nonce is the value issued by GET /v1/validators/challenge. Required.
	Nonce string `json:"nonce"`
	// Signature is the wallet's personal_sign over the challenge message. Required.
	Signature string `json:"signature"`
}

// SlotList is the caller org's claimed slots on one network.
type SlotList struct {
	// Data is the page of claimed slots; empty when the org has claimed none.
	Data []SlotView `json:"data"`
	// Network is the network slug new nodes join (mainnet/testnet/devnet/localnet).
	Network string `json:"network"`
}

// SlotView is one claimed slot: its node identity, CR placement and status.
type SlotView struct {
	// Slot is the validator slot number (the same value as tokenId).
	Slot uint64 `json:"slot"`
	// TokenID is the GenesisNFT id that entitles the slot.
	TokenID uint64 `json:"tokenId"`
	// Wallet is the lower-cased address that proved ownership of the NFT.
	Wallet string `json:"wallet"`
	// NodeID is the luxd node identity generated for the slot.
	NodeID string `json:"nodeID"`
	// BLSPubkey is the node's BLS public key, hex-encoded.
	BLSPubkey string `json:"blsPubkey"`
	// NodeStatus is provisioning, node_created or node_pending (no cluster reached).
	NodeStatus string `json:"nodeStatus"`
	// CRName is the LuxNetwork custom resource written for the node.
	CRName string `json:"crName"`
	// Namespace is the cluster namespace the node CR lives in.
	Namespace string `json:"namespace"`
	// Network is the network slug the node syncs.
	Network string `json:"network"`
	// CreatedAt is unix seconds, server-assigned at claim time.
	CreatedAt int64 `json:"createdAt"`
	// UpdatedAt is unix seconds of the last status change.
	UpdatedAt int64 `json:"updatedAt"`
	// Registration is the queued, owner-gated P-Chain registration; absent until enqueued.
	Registration *RegistrationView `json:"registration,omitempty"`
}

// RegistrationView is the queued registration — never auto-submitted to a P-Chain.
type RegistrationView struct {
	// ID is the registration id.
	ID string `json:"id"`
	// Status is the queue state; new registrations are pending_owner_approval.
	Status string `json:"status"`
	// NodeID is the node the registration would add.
	NodeID string `json:"nodeID"`
}

// ── handlers ────────────────────────────────────────────────────────────────

// issueChallenge issues a single-use, org-bound nonce and the EXACT message the
// caller must personal_sign. Binding the nonce to (org, slot) here means a
// signature can never be replayed for a different org, slot, or session.
//
// Example: {"tokenId": 42}
func (o ops) issueChallenge(ctx context.Context, in *SlotRef) (*Challenge, error) {
	s := o.s
	org, err := tenant(ctx)
	if err != nil {
		return nil, err
	}
	tokenID := in.TokenID
	if tokenID == 0 {
		return nil, zip.ErrBadRequest("tokenId query param must be a positive integer")
	}
	if !s.State.nft.isValidatorTier(tokenID) {
		return nil, zip.ErrBadRequest(fmt.Sprintf("token %d is not a Validator-tier slot (1..%d)", tokenID, s.State.nft.validatorSlots))
	}
	nonce, err := newNonce()
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "rng: %v", err)
	}
	now := time.Now()
	expiresAt := now.Add(s.State.ttl)
	if err := s.State.store.PutChallenge(ctx, nonce, org, expiresAt.Unix(), now.Unix()); err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "issue challenge: %v", err)
	}
	s.State.store.PurgeExpiredChallenges(ctx, now.Unix())
	return &Challenge{
		Nonce:      nonce,
		Message:    challengeMessage(org, tokenID, nonce),
		TokenID:    tokenID,
		ExpiresAt:  expiresAt.Unix(),
		TTLSeconds: int(s.State.ttl.Seconds()),
	}, nil
}

// provisionValidator claims a slot: it verifies the signature and on-chain NFT
// ownership, generates a luxd staking identity into KMS, writes the node CR and
// ENQUEUES an owner-gated registration. It fails CLOSED at every gate — bad
// signature, non-owner, non-tier, KMS unavailable — none of which persist a claim
// or leak key material. Re-claiming a slot this org already holds re-applies the
// CR without regenerating keys.
//
// Example: {"tokenId": 42, "nonce": "deadbeefcafef00d", "signature": "0x1c…"}
func (o ops) provisionValidator(ctx context.Context, in *ProvisionRequest) (*SlotView, error) {
	s := o.s
	org, err := tenant(ctx)
	if err != nil {
		return nil, err
	}
	body := *in
	if body.TokenID == 0 || strings.TrimSpace(body.Nonce) == "" || strings.TrimSpace(body.Signature) == "" {
		return nil, zip.ErrBadRequest("tokenId, nonce, and signature are required")
	}
	now := time.Now()

	// 1) Burn the challenge (atomic single-use, org-bound, unexpired). Do this
	// FIRST so a replayed or forged nonce is rejected before any on-chain read.
	if err := s.State.store.ConsumeChallenge(ctx, body.Nonce, org, now.Unix()); err != nil {
		return nil, zip.Errorf(http.StatusUnauthorized, "challenge invalid: request a fresh /v1/validators/challenge")
	}

	// 2) Recover the signer from the EXACT message the challenge issued (server
	// reconstructs it from the validated org + tokenId + nonce).
	addr, err := recoverSigner(challengeMessage(org, body.TokenID, body.Nonce), body.Signature)
	if err != nil {
		return nil, zip.ErrBadRequest("signature does not recover: " + err.Error())
	}

	// 3) On-chain ownership: the recovered wallet must own Validator-tier NFT
	// #tokenId on Ethereum mainnet.
	if err := s.State.nft.verifyOwnership(ctx, body.TokenID, addr); err != nil {
		return nil, zip.ErrForbidden(err.Error())
	}

	// 4) Idempotency: a slot already claimed by THIS org re-provisions (keys stay,
	// NodeID stable); a slot held by ANOTHER org is a conflict (defense-in-depth —
	// ownerOf already bound the slot to this caller's wallet).
	existing, gerr := s.State.store.GetSlot(ctx, body.TokenID)
	if gerr == nil {
		if existing.Org != org {
			return nil, zip.ErrConflict("validator slot already claimed by another organization")
		}
		return reprovision(s, ctx, existing)
	}

	// 5) NEW claim. Generate the staking identity and seal it into KMS BEFORE
	// persisting anything — fail closed so a claim never exists without its keys.
	id, err := generateStakingIdentity()
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "generate staking identity: %v", err)
	}
	kmsBase := kmsStakingBaseRef(org, body.TokenID)
	if err := id.seal(ctx, s.KMS, kmsBase); err != nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "seal staking keys: %v", err)
	}

	name := crName(org, body.TokenID)
	ns := envOr("VALIDATORS_NAMESPACE", "lux-validators")
	slot := Slot{
		TokenID:   body.TokenID,
		Org:       org,
		Wallet:    strings.ToLower(addr.Hex()),
		NodeID:    id.NodeID,
		KMSRef:    kmsBase,
		CRName:    name,
		Namespace: ns,
		BLSPubkey: id.BLSPubkeyHex,
		Status:    "provisioning",
		CreatedAt: now.Unix(),
		UpdatedAt: now.Unix(),
	}
	if _, err := s.State.store.ClaimSlot(ctx, slot); err != nil {
		if err == errConflict {
			return nil, zip.ErrConflict("validator slot already claimed by another organization")
		}
		return nil, zip.Errorf(http.StatusInternalServerError, "claim slot: %v", err)
	}

	// 6) Materialize the node CR (best-effort — honest "pending" if no cluster).
	nodeStatus, crName := materialize(s, ctx, slot)

	// 7) ENQUEUE the owner-gated registration. NEVER auto-submitted to any
	// P-Chain — the owner co-signs the AddPermissionlessValidatorTx out of band.
	reg := Registration{
		ID:        newRegID(),
		TokenID:   body.TokenID,
		Org:       org,
		NodeID:    id.NodeID,
		BLSPubkey: id.BLSPubkeyHex,
		Weight:    0, // owner sets the stake weight at co-sign time (never NFT-derived)
		Status:    "pending_owner_approval",
		CreatedAt: now.Unix(),
		UpdatedAt: now.Unix(),
	}
	saved, err := s.State.store.EnqueueRegistration(ctx, reg)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "enqueue registration: %v", err)
	}
	_ = crName

	slot.Status = nodeStatus
	// A NEW claim answers 201; the re-provision path above keeps its 200.
	cloud.Created(ctx)
	return slotView(slot, saved, s.State.network), nil
}

// reprovision re-applies the node CR for an already-claimed slot (idempotent
// retry) and returns the current state without regenerating keys.
func reprovision(s *cloud.Service[state], ctx context.Context, slot Slot) (*SlotView, error) {
	reg, _ := s.State.store.getRegByToken(ctx, slot.TokenID)
	nodeStatus, _ := materialize(s, ctx, slot)
	slot.Status = nodeStatus
	return slotView(slot, reg, s.State.network), nil
}

// materialize writes the node CR (best-effort) and returns the resulting node
// status + CR name. A cluster-less deployment degrades to an honest
// "node_pending" — the slot + keys + registration still persist.
func materialize(s *cloud.Service[state], ctx context.Context, slot Slot) (status, crName string) {
	if !s.State.prov.Available() {
		s.Log.Info("validators: no cluster resolved — node stays pending (slot claimed, keys sealed)",
			"org", slot.Org, "slot", slot.TokenID)
		_ = s.State.store.SetSlotStatus(ctx, slot.TokenID, "node_pending", time.Now().Unix())
		return "node_pending", slot.CRName
	}
	name, ns, err := s.State.prov.Provision(ctx, provisionRequest{
		Org:        slot.Org,
		TokenID:    slot.TokenID,
		NodeID:     slot.NodeID,
		KMSBaseRef: slot.KMSRef,
		NetworkID:  s.State.netID,
	})
	if err != nil {
		s.Log.Warn("validators: node CR provisioning failed — slot claimed, keys sealed, node pending",
			"org", slot.Org, "slot", slot.TokenID, "err", err)
		_ = s.State.store.SetSlotStatus(ctx, slot.TokenID, "node_pending", time.Now().Unix())
		return "node_pending", slot.CRName
	}
	_ = ns
	_ = s.State.store.SetSlotStatus(ctx, slot.TokenID, "node_created", time.Now().Unix())
	return "node_created", name
}

// listValidators returns the caller org's claimed validator slots, each with its
// node status and its queued registration.
//
// Example: {"limit": 50}
func (o ops) listValidators(ctx context.Context, in *Page) (*SlotList, error) {
	s := o.s
	org, err := tenant(ctx)
	if err != nil {
		return nil, err
	}
	slots, err := s.State.store.ListSlots(ctx, org, limitOf(in.Limit))
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list validators: %v", err)
	}
	regs, err := s.State.store.ListRegistrations(ctx, org, maxListLimit)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list registrations: %v", err)
	}
	byToken := make(map[uint64]Registration, len(regs))
	for _, r := range regs {
		byToken[r.TokenID] = r
	}
	out := make([]SlotView, 0, len(slots))
	for _, sl := range slots {
		out = append(out, *slotView(sl, byToken[sl.TokenID], s.State.network))
	}
	return &SlotList{Data: out, Network: s.State.network}, nil
}

// Page is the bound shared by every list that filters on nothing but size.
type Page struct {
	// Limit caps the rows returned; 0 means 200 and nothing above 1000 is honoured.
	Limit int `json:"limit"`
}

// getValidator returns one of the caller org's claimed slots. A slot held by
// another org reads as not found.
//
// Example: {"tokenId": 42}
func (o ops) getValidator(ctx context.Context, in *SlotRef) (*SlotView, error) {
	s := o.s
	org, err := tenant(ctx)
	if err != nil {
		return nil, err
	}
	if in.TokenID == 0 {
		return nil, zip.ErrBadRequest("tokenId must be a positive integer")
	}
	sl, err := s.State.store.GetSlot(ctx, in.TokenID)
	if err != nil || sl.Org != org {
		return nil, zip.ErrNotFound("validator slot not found")
	}
	reg, _ := s.State.store.getRegByToken(ctx, in.TokenID)
	return slotView(sl, reg, s.State.network), nil
}

// ── helpers ─────────────────────────────────────────────────────────────────

// slotView is the wire shape for a claimed slot + its owner-gated registration.
func slotView(sl Slot, reg Registration, network string) *SlotView {
	v := &SlotView{
		Slot:       sl.TokenID,
		TokenID:    sl.TokenID,
		Wallet:     sl.Wallet,
		NodeID:     sl.NodeID,
		BLSPubkey:  sl.BLSPubkey,
		NodeStatus: sl.Status,
		CRName:     sl.CRName,
		Namespace:  sl.Namespace,
		Network:    network,
		CreatedAt:  sl.CreatedAt,
		UpdatedAt:  sl.UpdatedAt,
	}
	if reg.ID != "" {
		v.Registration = &RegistrationView{ID: reg.ID, Status: reg.Status, NodeID: reg.NodeID}
	}
	return v
}

// kmsStakingBaseRef is the org-scoped KMS coordinate base the staking artifacts
// seal under: orgs/<org>/validators/<tokenId>. The kms-operator (via the
// KMSSecret CR) reads /v1/kms/orgs/<org>/secrets/validators/<tokenId>/<KEY>,
// which cloud's org-scope guard admits ONLY for owner==<org>.
func kmsStakingBaseRef(org string, tokenID uint64) string {
	return "orgs/" + org + "/validators/" + strconv.FormatUint(tokenID, 10)
}

func newRegID() string {
	nonce, _ := newNonce()
	return "vreg_" + nonce
}

// limitOf bounds a caller's page size: absent, unparseable or non-positive means
// defaultListLimit, and nothing above maxListLimit is honoured.
func limitOf(n int) int {
	if n <= 0 {
		return defaultListLimit
	}
	if n > maxListLimit {
		return maxListLimit
	}
	return n
}

func envOr(key, dflt string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return dflt
}

func envInt(key string, dflt int) int {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return dflt
}

// Shutdown closes the validators store. Idempotent.
func Shutdown() error {
	if mounted == nil || mounted.State.store == nil {
		return nil
	}
	err := mounted.State.store.Close()
	mounted = nil
	return err
}
