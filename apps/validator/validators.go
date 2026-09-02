// Package validators is one-click validator onboarding: prove your Genesis NFT,
// get a node provisioned, queue its registration.
//
// The /v1/validator/* routes are the "click → provision node + queue
// registration" pipeline behind GDA/SDM validator onboarding on lux.cloud.
//
// The end-to-end claim, all server-enforced at cloud's ONE auth boundary
// (SanitizeIdentity → principal.Org):
//
//  1. GET  /v1/validator/challenge?tokenId=N  → a single-use, org-bound nonce +
//     the exact message to personal_sign.
//  2. POST /v1/validator {tokenId,nonce,signature} → verify the wallet controls
//     the signature AND holds Validator-tier GenesisNFT #tokenId on Ethereum
//     mainnet (ownerOf), then: generate a luxd staking identity → seal it into
//     KMS (never plaintext) → write a LuxNetwork CR for a NEW node (never the
//     live luxd) → ENQUEUE an owner-gated registration (NEVER auto-submitted to
//     any P-Chain). Returns the slot + node + registration status.
//  3. GET  /v1/validator            → the org's claimed slots + node status.
//  4. GET  /v1/validator/:tokenId   → one slot's detail.
//
// Tenant isolation is the org (principal.Org — the VALIDATED IAM owner, never a
// client header); every store query filters WHERE org=?. The tokenId IS the
// validator slot. serve.go auto-registers GET /v1/validator/health.
package validator

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/hanzoai/cloud/internal/environ"
	"github.com/zap-proto/zip"
)

//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

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
func Use(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("validator.Use:  nil app")
	}
	if deps.DataDir == "" {
		return fmt.Errorf("validator.Use:  empty DataDir")
	}
	store, err := openStore(deps.DataDir)
	if err != nil {
		return fmt.Errorf("validator.Use:  open store: %w", err)
	}

	slots := uint64(environ.Int("VALIDATORS_SLOTS", 100))
	nft, err := newNFTReader(
		environ.Or("VALIDATORS_ETH_RPC", "https://ethereum-rpc.publicnode.com"),
		environ.Or("VALIDATORS_NFT_CONTRACT", GenesisNFTContract),
		slots,
	)
	if err != nil {
		_ = store.Close()
		return fmt.Errorf("validator.Use:  nft reader: %w", err)
	}

	network := environ.Or("VALIDATORS_NETWORK", "devnet")
	netID, ok := networkIDs[network]
	if !ok {
		_ = store.Close()
		return fmt.Errorf("validator.Use:  unknown VALIDATORS_NETWORK %q", network)
	}

	prov := newK8sProvisioner(crConfig{
		Group:     environ.Or("VALIDATORS_CR_GROUP", "node.lux.cloud"),
		Namespace: environ.Or("VALIDATORS_NAMESPACE", "lux-validators"),
		NodeImage: environ.Or("VALIDATORS_NODE_IMAGE", "ghcr.io/luxfi/node:v1.36.15"),
		KMSHost:   environ.Or("VALIDATORS_KMS_HOST", "http://cloud."+deps.Brand+".svc.cluster.local:8000"),
		KMSCreds:  environ.Or("VALIDATORS_KMS_CREDS", "platform-kms-auth"),
		StorageGi: environ.Int("VALIDATORS_STORAGE_GI", 200),
	})

	b := cloud.NewBase(deps, "validator")
	s := &cloud.Service[state]{Base: b, State: state{
		store:   store,
		nft:     nft,
		prov:    prov,
		network: network,
		netID:   netID,
		ttl:     time.Duration(environ.Int("VALIDATORS_CHALLENGE_TTL_SECONDS", 600)) * time.Second,
	}}
	mounted = s

	routes(app, s)
	b.Log.Info("validators mounted", "brand", deps.Brand, "network", network,
		"nftContract", nft.contract.Hex(), "clusterReady", prov.Available())
	return nil
}

func routes(app cloud.Router, s *cloud.Service[state]) {
	o := validatorOps{s: s}

	g := app.Group("/v1/validator")
	// The composer owns cloud.Bridge: the fused host installs it once at its root
	// and the plugin constructor does the same for a plugin program, so no
	// subsystem installs it. requireOrgOnWrite keeps the identity refusal exactly
	// where it has always been — see its own comment.
	g.Use(requireOrgOnWrite())

	// The collection root (/v1/validator) stays FLAT: joining "/v1/validator"
	// with "" yields "/v1/validator/" (trailing slash), which the portal's bare
	// /v1/validator calls would miss. Same gotcha the clients/wallets, guide,
	// link, … subsystems document.
	//
	// The gate rides the ROUTER rather than a prefix. zip middleware is scoped to
	// the group INSTANCE it was installed on (and that group's children) and is
	// never matched by prefix across the App, so declared straight on the App the
	// root sat outside g entirely: requireOrgOnWrite did not run for it, and an
	// anonymous POST fell through to zip's decode — answering 400 on a malformed
	// body where this surface has always answered 403.
	//
	// With() wraps the leaf it registers and installs nothing at "/v1", which is
	// what keeps this legal: a Use there would gate every other subsystem's /v1
	// routes, and cloud's ownership gate (scope.err) refuses a subsystem that
	// installs middleware outside the prefixes it owns. .Group("/v1") is then only
	// an address — it joins to exactly "/v1/validator", and it is a prefix
	// cmd/zipdoc can read, so both ops keep their prose. Same shape apps/sync uses.
	// Pinned by TestIdentityRefusalStillPrecedesTheBody.
	gated := cloud.ZipApp(app).With(gateWrite).Group("/v1")
	zip.Get(gated, "/validator", o.list)
	zip.Post(gated, "/validator", o.provision)

	zip.Get(g, "/challenge", o.challenge)
	zip.Get(g, "/:tokenId", o.get)
}

// identity is THE decision, once: nil when a request may go on to the decoder, a
// 403 when it may not. The two shapes below are two ways to ASK it, never two
// copies of it — which they were, each carrying its own method test.
//
// A typed op runs after the decode, so moving the check into the op would answer
// 400 to an unauthenticated caller whose body is also malformed, where this surface
// has always answered 403. The check therefore lives where the untyped handler's
// ran: ahead of the body.
//
// IT EXEMPTS READS, NOT NON-POSTS. It named POST, which is a method standing in for
// a property — the property is "a body will be decoded before an op could answer",
// and the next verb this surface takes carries the property without carrying the
// name. Reads pass because they answer 403 from inside their own handlers, with no
// body to decode first, and because the auto-registered GET /v1/validator/health
// must stay answerable to a kubelet.
func identity(c *zip.Ctx) error {
	switch c.Method() {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return nil
	}
	if _, ok := principal.Org(c); !ok {
		return zip.ErrForbidden("validated identity required")
	}
	return nil
}

// requireOrgOnWrite asks [identity] in the shape a group's Use takes.
func requireOrgOnWrite() zip.Handler {
	return func(c *zip.Ctx) error {
		if err := identity(c); err != nil {
			return err
		}
		return c.Continue()
	}
}

// gateWrite asks [identity] in the shape With() takes, for the two collection-root
// ops that hang off a router rather than a prefixed group (see routes). A signature
// adapter, not a second rule: a group's Use takes a handler that calls c.Continue(),
// With() takes func(Handler) Handler.
func gateWrite(next zip.Handler) zip.Handler {
	return func(c *zip.Ctx) error {
		if err := identity(c); err != nil {
			return err
		}
		return next(c)
	}
}

// validatorOps binds the service to the typed validator ops. A TypedHandler takes
// no service parameter, so the service arrives as a RECEIVER and every op is a
// method value — also the only bound form cmd/zipdoc can lift prose from.
type validatorOps struct{ s *cloud.Service[state] }

// ── handlers ────────────────────────────────────────────────────────────────

// challengeIn selects the slot a challenge is issued for.
type challengeIn struct {
	// TokenID is the Validator-tier GenesisNFT token id, as a decimal string in
	// the `?tokenId=` query. A value that is not a positive integer is 400. It is
	// a string rather than a number because the parse that has always served this
	// route trims surrounding whitespace, and one parse rule is better than two.
	TokenID string `json:"tokenId"`
}

// challengeView is the single-use proof-of-ownership challenge. Field order is
// the alphabetical key order the map it replaced marshalled in, so the bytes on
// the wire did not move when this route became a typed op.
type challengeView struct {
	// ExpiresAt is when the nonce stops being redeemable, as a Unix timestamp.
	ExpiresAt int64 `json:"expiresAt"`
	// Message is the EXACT text to personal_sign. It is reconstructed server-side
	// from the validated org, the slot and the nonce at redemption, so signing
	// anything else cannot claim the slot.
	Message string `json:"message"`
	// Nonce is the single-use, org-bound challenge value to send back with the
	// signature.
	Nonce string `json:"nonce"`
	// TokenID is the slot the challenge was issued for.
	TokenID uint64 `json:"tokenId"`
	// TTLSeconds is the challenge lifetime in seconds.
	TTLSeconds int `json:"ttlSeconds"`
}

// challenge issues the single-use nonce and the exact message a wallet must sign
// to claim a validator slot.
//
// The nonce is bound to (validated org, slot) and stored server-side, so a
// signature obtained for one org or one slot can never be replayed for another,
// and the message POST /v1/validator verifies is rebuilt from those same server
// facts rather than trusted from the caller. Redeem it with
// POST /v1/validator before it expires; it can be redeemed once.
//
// A tokenId outside the Validator tier is refused here rather than after signing.
func (o validatorOps) challenge(ctx context.Context, in *challengeIn) (*challengeView, error) {
	s := o.s
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	tokenID, err := parseTokenID(in.TokenID)
	if err != nil {
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
	return &challengeView{
		ExpiresAt:  expiresAt.Unix(),
		Message:    challengeMessage(org, tokenID, nonce),
		Nonce:      nonce,
		TokenID:    tokenID,
		TTLSeconds: int(s.State.ttl.Seconds()),
	}, nil
}

// validatorClaim is the POST /v1/validator body: the slot, the issued nonce, and
// the personal_sign signature over the challenge message.
//
// Every field is `url:"-"`. zip binds query and path OVER a decoded body, and this
// route has never read either — a `?tokenId=` that outranked the body would be a
// new way to address the write, so the three fields stay body-only.
type validatorClaim struct {
	// TokenID is the Validator-tier GenesisNFT token id being claimed. It IS the
	// validator slot.
	TokenID uint64 `json:"tokenId" url:"-"`
	// Nonce is the value GET /v1/validator/challenge issued for this slot.
	Nonce string `json:"nonce" url:"-"`
	// Signature is the wallet's personal_sign over the challenge message, hex with
	// a 0x prefix.
	Signature string `json:"signature" url:"-"`
}

// provision claims a validator slot and provisions its node, after proving the
// caller's wallet owns the slot's NFT.
//
// The pipeline, all server-enforced: burn the single-use challenge (so a replayed
// or forged nonce dies before any chain read), recover the signer from the message
// this server rebuilds, require that wallet to hold Validator-tier GenesisNFT
// #tokenId on Ethereum mainnet, generate a fresh luxd staking identity and seal it
// into KMS, write a LuxNetwork CR for a NEW node, and ENQUEUE an owner-gated
// registration. The registration is never auto-submitted to any P-Chain — the
// owner co-signs it out of band — and the stake weight is set at co-sign time,
// never derived from the NFT.
//
// It fails CLOSED at every gate: a bad signature, a non-owner, a non-tier slot or
// an unavailable KMS all leave no claim persisted and no key material exposed.
// Re-claiming a slot this org already holds re-applies the node CR and returns 200
// with the existing identity (keys and NodeID are stable); a slot held by another
// org is 409. A cluster-less deployment still claims the slot, seals the keys and
// queues the registration, reporting the node as "node_pending".
//
// Example: {"tokenId": 7, "nonce": "5f3a…", "signature": "0x…"}
func (o validatorOps) provision(ctx context.Context, body *validatorClaim) (*slotView, error) {
	s := o.s
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	if body.TokenID == 0 || strings.TrimSpace(body.Nonce) == "" || strings.TrimSpace(body.Signature) == "" {
		return nil, zip.ErrBadRequest("tokenId, nonce, and signature are required")
	}
	now := time.Now()

	// 1) Burn the challenge (atomic single-use, org-bound, unexpired). Do this
	// FIRST so a replayed or forged nonce is rejected before any on-chain read.
	if err := s.State.store.ConsumeChallenge(ctx, body.Nonce, org, now.Unix()); err != nil {
		return nil, zip.Errorf(http.StatusUnauthorized, "challenge invalid: request a fresh /v1/validator/challenge")
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
	ns := environ.Or("VALIDATORS_NAMESPACE", "lux-validators")
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
	//
	// This is the act that costs money: a node with storage, running until it is
	// deleted. Authorized before the CR is applied; billed only if one actually
	// was. See meter.go.
	ch, err := afford(s, ctx)
	if err != nil {
		return nil, cloud.Denied(err)
	}
	defer ch.Release()
	nodeStatus, crName := materialize(s, ctx, slot)
	if nodeStatus != "node_pending" {
		charge(ch)
	}

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

	// A FIRST claim answers 201, a re-claim of a slot this org already holds
	// answers 200 (reprovision, below). zip.WithStatus declares ONE unconditional
	// status and cannot express the pair, so this op stays typed-but-shimmed:
	// cloud.Created marks the create branch only, exactly as the untyped handler
	// did. It converts when zip can declare multi-status responses.
	cloud.Created(ctx)
	slot.Status = nodeStatus
	return viewOf(slot, saved, s.State.network), nil
}

// reprovision re-applies the node CR for an already-claimed slot (idempotent
// retry) and returns the current state without regenerating keys.
func reprovision(s *cloud.Service[state], ctx context.Context, slot Slot) (*slotView, error) {
	reg, _ := s.State.store.getRegByToken(ctx, slot.TokenID)
	nodeStatus, _ := materialize(s, ctx, slot)
	slot.Status = nodeStatus
	return viewOf(slot, reg, s.State.network), nil
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

// listIn bounds the org's slot listing.
type listIn struct {
	// Limit is how many slots to return, as a decimal string in the `?limit=`
	// query. Absent, unparseable or non-positive means 200; over 1000 is clamped
	// to 1000. It is a string rather than a number because the parse that has
	// always served this route trims surrounding whitespace, and one parse rule is
	// better than two.
	Limit string `json:"limit"`
}

// validatorList is the GET /v1/validator envelope.
type validatorList struct {
	// Data is one entry per slot this org has claimed.
	Data []slotView `json:"data"`
	// Network is the luxd network slug new nodes join on this deployment.
	Network string `json:"network"`
}

// list returns the validator slots the caller's org has claimed.
//
// One entry per claimed slot with its node identity, its live-ish node status and
// the owner-gated registration queued for it, if any. Slots are org-scoped by the
// validated identity, so a caller can only ever see their own — a slot claimed by
// another org is not merely hidden from this list, it is unreachable through the
// whole surface.
func (o validatorOps) list(ctx context.Context, in *listIn) (*validatorList, error) {
	s := o.s
	org, err := principal.Acting(ctx)
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
	out := make([]slotView, 0, len(slots))
	for _, sl := range slots {
		out = append(out, *viewOf(sl, byToken[sl.TokenID], s.State.network))
	}
	return &validatorList{Data: out, Network: s.State.network}, nil
}

// slotRef addresses one claimed slot by its token id.
type slotRef struct {
	// TokenID is the slot's GenesisNFT token id, from the path, as a decimal
	// string. A value that is not a positive integer is 400. It is a string
	// rather than a number because the parse that has always served this route
	// trims surrounding whitespace, and one parse rule is better than two.
	TokenID string `json:"tokenId"`
}

// get returns one claimed validator slot, scoped to the caller's org.
//
// A slot another org holds, and a slot nobody holds, are both 404 — never a
// different status, so this route cannot be used to probe which slots are taken.
func (o validatorOps) get(ctx context.Context, in *slotRef) (*slotView, error) {
	s := o.s
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	tokenID, err := parseTokenID(in.TokenID)
	if err != nil {
		return nil, zip.ErrBadRequest("tokenId must be a positive integer")
	}
	sl, err := s.State.store.GetSlot(ctx, tokenID)
	if err != nil || sl.Org != org {
		return nil, zip.ErrNotFound("validator slot not found")
	}
	reg, _ := s.State.store.getRegByToken(ctx, tokenID)
	return viewOf(sl, reg, s.State.network), nil
}

// ── helpers ─────────────────────────────────────────────────────────────────

// registrationView is the owner-gated P-Chain registration queued for a slot.
type registrationView struct {
	// ID is the registration's handle.
	ID string `json:"id"`
	// Status is the registration's lifecycle state; "pending_owner_approval" until
	// the owner co-signs it out of band.
	Status string `json:"status"`
	// NodeID is the luxd node the registration is for.
	NodeID string `json:"nodeID"`
}

// slotView is the wire shape for a claimed slot + its owner-gated registration.
// Field order is the alphabetical key order the map it replaced marshalled in, so
// the bytes on the wire did not move when these routes became typed ops.
type slotView struct {
	// BLSPubkey is the node's BLS public key, hex.
	BLSPubkey string `json:"blsPubkey"`
	// CRName is the LuxNetwork custom resource that materializes the node.
	CRName string `json:"crName"`
	// CreatedAt is when the slot was first claimed, as a Unix timestamp.
	CreatedAt int64 `json:"createdAt"`
	// Namespace is the Kubernetes namespace the node's CR lives in.
	Namespace string `json:"namespace"`
	// Network is the luxd network slug the node joins.
	Network string `json:"network"`
	// NodeID is the luxd node id derived from the sealed staking identity. It is
	// stable across re-claims of the same slot.
	NodeID string `json:"nodeID"`
	// NodeStatus is the provisioning state of the node: "node_created" once the CR
	// is applied, "node_pending" when no cluster is reachable (the slot is still
	// claimed and the keys are still sealed).
	NodeStatus string `json:"nodeStatus"`
	// Registration is the queued owner-gated registration, absent until one exists.
	Registration *registrationView `json:"registration,omitempty"`
	// Slot is the validator slot number — the same value as tokenId, under the
	// name the portal reads.
	Slot uint64 `json:"slot"`
	// TokenID is the GenesisNFT token id that IS this slot.
	TokenID uint64 `json:"tokenId"`
	// UpdatedAt is when the slot last changed, as a Unix timestamp.
	UpdatedAt int64 `json:"updatedAt"`
	// Wallet is the lowercase Ethereum address that proved ownership of the NFT.
	Wallet string `json:"wallet"`
}

// viewOf projects a stored slot + its registration into the wire shape. ONE
// projection for every route that returns a slot, so the shape can never depend on
// which route served it.
func viewOf(sl Slot, reg Registration, network string) *slotView {
	v := &slotView{
		BLSPubkey:  sl.BLSPubkey,
		CRName:     sl.CRName,
		CreatedAt:  sl.CreatedAt,
		Namespace:  sl.Namespace,
		Network:    network,
		NodeID:     sl.NodeID,
		NodeStatus: sl.Status,
		Slot:       sl.TokenID,
		TokenID:    sl.TokenID,
		UpdatedAt:  sl.UpdatedAt,
		Wallet:     sl.Wallet,
	}
	if reg.ID != "" {
		v.Registration = &registrationView{ID: reg.ID, Status: reg.Status, NodeID: reg.NodeID}
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

// limitOf is the ONE `?limit=` rule for this surface: an absent, unparseable or
// non-positive value is the default, and anything above the ceiling is clamped.
func limitOf(v string) int {
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || n <= 0 {
		return defaultListLimit
	}
	if n > maxListLimit {
		return maxListLimit
	}
	return n
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
