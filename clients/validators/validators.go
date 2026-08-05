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
package validators

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/principal"
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
func Mount(app *zip.App, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("validators.Mount: nil zip.App")
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

	// The cluster comes from deps (cloud.BuildK8sClient resolved it at boot); this
	// subsystem never builds one. Unresolved is not fatal — Available() reports it
	// and the node degrades to an honest "pending".
	prov := &k8sProvisioner{k8s: deps.K8s, cfg: crConfig{
		Group:     envOr("VALIDATORS_CR_GROUP", "node.lux.cloud"),
		Namespace: envOr("VALIDATORS_NAMESPACE", "lux-validators"),
		NodeImage: envOr("VALIDATORS_NODE_IMAGE", "ghcr.io/luxfi/node:v1.36.15"),
		KMSHost:   envOr("VALIDATORS_KMS_HOST", "http://cloud."+deps.Brand+".svc.cluster.local:8000"),
		KMSCreds:  envOr("VALIDATORS_KMS_CREDS", "platform-kms-auth"),
		StorageGi: envInt("VALIDATORS_STORAGE_GI", 200),
	}}

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

	routes(app, s)
	b.Log.Info("validators mounted", "brand", deps.Brand, "network", network,
		"nftContract", nft.contract.Hex(), "clusterReady", prov.Available())
	return nil
}

func routes(app *zip.App, s *cloud.Service[state]) {
	// The collection root (/v1/validators) stays FLAT: Group("/v1/validators").
	// Get("")/Post("") would register "/v1/validators/" (trailing slash), which
	// the portal's bare /v1/validators calls would miss. Same gotcha the
	// clients/wallets, guide, link, … subsystems document.
	app.Get("/v1/validators", cloud.Handle(s, listValidators))
	app.Post("/v1/validators", cloud.Handle(s, provisionValidator))

	g := app.Group("/v1/validators")
	g.Get("/challenge", cloud.Handle(s, issueChallenge))
	g.Get("/:tokenId", cloud.Handle(s, getValidator))
}

// ── handlers ────────────────────────────────────────────────────────────────

// issueChallenge issues a single-use, org-bound nonce and the EXACT message the
// caller must personal_sign. Binding the nonce to (org, slot) here means a
// signature can never be replayed for a different org, slot, or session.
func issueChallenge(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := principal.Org(c)
	if !ok {
		return zip.ErrForbidden("validated identity required")
	}
	tokenID, err := parseTokenID(strings.TrimSpace(c.Query("tokenId")))
	if err != nil {
		return zip.ErrBadRequest("tokenId query param must be a positive integer")
	}
	if !s.State.nft.isValidatorTier(tokenID) {
		return zip.ErrBadRequest(fmt.Sprintf("token %d is not a Validator-tier slot (1..%d)", tokenID, s.State.nft.validatorSlots))
	}
	nonce, err := newNonce()
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "rng: %v", err)
	}
	now := time.Now()
	expiresAt := now.Add(s.State.ttl)
	if err := s.State.store.PutChallenge(c.Context(), nonce, org, expiresAt.Unix(), now.Unix()); err != nil {
		return zip.Errorf(http.StatusInternalServerError, "issue challenge: %v", err)
	}
	s.State.store.PurgeExpiredChallenges(c.Context(), now.Unix())
	return c.JSON(http.StatusOK, map[string]any{
		"nonce":      nonce,
		"message":    challengeMessage(org, tokenID, nonce),
		"tokenId":    tokenID,
		"expiresAt":  expiresAt.Unix(),
		"ttlSeconds": int(s.State.ttl.Seconds()),
	})
}

// provisionRequestBody is the POST body: the slot, the issued nonce, and the
// personal_sign signature over the challenge message.
type provisionRequestBody struct {
	TokenID   uint64 `json:"tokenId"`
	Nonce     string `json:"nonce"`
	Signature string `json:"signature"`
}

// provisionValidator is the "click → provision node + queue registration"
// pipeline. It fails CLOSED at every gate: bad signature, non-owner, non-tier,
// KMS unavailable — none of these persist a claim or leak key material.
func provisionValidator(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := principal.Org(c)
	if !ok {
		return zip.ErrForbidden("validated identity required")
	}
	var body provisionRequestBody
	if err := c.Bind(&body); err != nil {
		return err
	}
	if body.TokenID == 0 || strings.TrimSpace(body.Nonce) == "" || strings.TrimSpace(body.Signature) == "" {
		return zip.ErrBadRequest("tokenId, nonce, and signature are required")
	}
	ctx := c.Context()
	now := time.Now()

	// 1) Burn the challenge (atomic single-use, org-bound, unexpired). Do this
	// FIRST so a replayed or forged nonce is rejected before any on-chain read.
	if err := s.State.store.ConsumeChallenge(ctx, body.Nonce, org, now.Unix()); err != nil {
		return zip.Errorf(http.StatusUnauthorized, "challenge invalid: request a fresh /v1/validators/challenge")
	}

	// 2) Recover the signer from the EXACT message the challenge issued (server
	// reconstructs it from the validated org + tokenId + nonce).
	addr, err := recoverSigner(challengeMessage(org, body.TokenID, body.Nonce), body.Signature)
	if err != nil {
		return zip.ErrBadRequest("signature does not recover: " + err.Error())
	}

	// 3) On-chain ownership: the recovered wallet must own Validator-tier NFT
	// #tokenId on Ethereum mainnet.
	if err := s.State.nft.verifyOwnership(ctx, body.TokenID, addr); err != nil {
		return zip.ErrForbidden(err.Error())
	}

	// 4) Idempotency: a slot already claimed by THIS org re-provisions (keys stay,
	// NodeID stable); a slot held by ANOTHER org is a conflict (defense-in-depth —
	// ownerOf already bound the slot to this caller's wallet).
	existing, gerr := s.State.store.GetSlot(ctx, body.TokenID)
	if gerr == nil {
		if existing.Org != org {
			return zip.ErrConflict("validator slot already claimed by another organization")
		}
		return reprovision(s, c, existing)
	}

	// 5) NEW claim. Generate the staking identity and seal it into KMS BEFORE
	// persisting anything — fail closed so a claim never exists without its keys.
	id, err := generateStakingIdentity()
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "generate staking identity: %v", err)
	}
	kmsBase := kmsStakingBaseRef(org, body.TokenID)
	if err := id.seal(ctx, s.KMS, kmsBase); err != nil {
		return zip.Errorf(http.StatusServiceUnavailable, "seal staking keys: %v", err)
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
			return zip.ErrConflict("validator slot already claimed by another organization")
		}
		return zip.Errorf(http.StatusInternalServerError, "claim slot: %v", err)
	}

	// 6) Materialize the node CR (best-effort — honest "pending" if no cluster).
	nodeStatus, crName := materialize(s, c, slot)

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
		return zip.Errorf(http.StatusInternalServerError, "enqueue registration: %v", err)
	}
	_ = crName

	slot.Status = nodeStatus
	return c.JSON(http.StatusCreated, slotView(slot, saved, s.State.network))
}

// reprovision re-applies the node CR for an already-claimed slot (idempotent
// retry) and returns the current state without regenerating keys.
func reprovision(s *cloud.Service[state], c *zip.Ctx, slot Slot) error {
	reg, _ := s.State.store.getRegByToken(c.Context(), slot.TokenID)
	nodeStatus, _ := materialize(s, c, slot)
	slot.Status = nodeStatus
	return c.JSON(http.StatusOK, slotView(slot, reg, s.State.network))
}

// materialize writes the node CR (best-effort) and returns the resulting node
// status + CR name. A cluster-less deployment degrades to an honest
// "node_pending" — the slot + keys + registration still persist.
func materialize(s *cloud.Service[state], c *zip.Ctx, slot Slot) (status, crName string) {
	if !s.State.prov.Available() {
		s.Log.Info("validators: no cluster resolved — node stays pending (slot claimed, keys sealed)",
			"org", slot.Org, "slot", slot.TokenID)
		_ = s.State.store.SetSlotStatus(c.Context(), slot.TokenID, "node_pending", time.Now().Unix())
		return "node_pending", slot.CRName
	}
	name, ns, err := s.State.prov.Provision(c.Context(), provisionRequest{
		Org:        slot.Org,
		TokenID:    slot.TokenID,
		NodeID:     slot.NodeID,
		KMSBaseRef: slot.KMSRef,
		NetworkID:  s.State.netID,
	})
	if err != nil {
		s.Log.Warn("validators: node CR provisioning failed — slot claimed, keys sealed, node pending",
			"org", slot.Org, "slot", slot.TokenID, "err", err)
		_ = s.State.store.SetSlotStatus(c.Context(), slot.TokenID, "node_pending", time.Now().Unix())
		return "node_pending", slot.CRName
	}
	_ = ns
	_ = s.State.store.SetSlotStatus(c.Context(), slot.TokenID, "node_created", time.Now().Unix())
	return "node_created", name
}

// listValidators returns the org's claimed slots (each with its live-ish status).
func listValidators(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := principal.Org(c)
	if !ok {
		return zip.ErrForbidden("validated identity required")
	}
	slots, err := s.State.store.ListSlots(c.Context(), org, limitOf(c))
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "list validators: %v", err)
	}
	regs, err := s.State.store.ListRegistrations(c.Context(), org, maxListLimit)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "list registrations: %v", err)
	}
	byToken := make(map[uint64]Registration, len(regs))
	for _, r := range regs {
		byToken[r.TokenID] = r
	}
	out := make([]map[string]any, 0, len(slots))
	for _, sl := range slots {
		out = append(out, slotView(sl, byToken[sl.TokenID], s.State.network))
	}
	return c.JSON(http.StatusOK, map[string]any{"data": out, "network": s.State.network})
}

// getValidator returns one slot's detail (org-scoped).
func getValidator(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := principal.Org(c)
	if !ok {
		return zip.ErrForbidden("validated identity required")
	}
	tokenID, err := parseTokenID(c.Param("tokenId"))
	if err != nil {
		return zip.ErrBadRequest("tokenId must be a positive integer")
	}
	sl, err := s.State.store.GetSlot(c.Context(), tokenID)
	if err != nil || sl.Org != org {
		return zip.ErrNotFound("validator slot not found")
	}
	reg, _ := s.State.store.getRegByToken(c.Context(), tokenID)
	return c.JSON(http.StatusOK, slotView(sl, reg, s.State.network))
}

// ── helpers ─────────────────────────────────────────────────────────────────

// slotView is the wire shape for a claimed slot + its owner-gated registration.
func slotView(sl Slot, reg Registration, network string) map[string]any {
	v := map[string]any{
		"slot":       sl.TokenID,
		"tokenId":    sl.TokenID,
		"wallet":     sl.Wallet,
		"nodeID":     sl.NodeID,
		"blsPubkey":  sl.BLSPubkey,
		"nodeStatus": sl.Status,
		"crName":     sl.CRName,
		"namespace":  sl.Namespace,
		"network":    network,
		"createdAt":  sl.CreatedAt,
		"updatedAt":  sl.UpdatedAt,
	}
	if reg.ID != "" {
		v["registration"] = map[string]any{
			"id":     reg.ID,
			"status": reg.Status,
			"nodeID": reg.NodeID,
		}
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

func parseTokenID(v string) (uint64, error) {
	n, err := strconv.ParseUint(strings.TrimSpace(v), 10, 64)
	if err != nil || n == 0 {
		return 0, fmt.Errorf("invalid tokenId %q", v)
	}
	return n, nil
}

func newRegID() string {
	nonce, _ := newNonce()
	return "vreg_" + nonce
}

func limitOf(c *zip.Ctx) int {
	n, err := strconv.Atoi(strings.TrimSpace(c.Query("limit")))
	if err != nil || n <= 0 {
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
