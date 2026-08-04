package wallets

// wallets.go owns the HTTP surface (/v1/wallets/*), the Mount/config seam that
// selects the custody set, the process singleton, and the finance seam.
//
//	POST /v1/wallets/accounts   {name}                              -> create account
//	GET  /v1/wallets/accounts                                       -> list MY accounts
//	POST /v1/wallets            {accountId,name,custody,tier,chain}  -> create wallet (Provision)
//	GET  /v1/wallets                                                -> list MY wallets
//	GET  /v1/wallets/:id                                            -> get one (404 if not my org)
//	POST /v1/wallets/:id/keys                                       -> rotate key material
//	POST /v1/wallets/:id/sign   {message?|digest?}                  -> sign (digest=hex 32B, else Keccak256(message))
//
// Every handler derives the tenant through principal.Org (the ONE trust
// signal) and refuses with 403 when absent. Config selects the custody set:
// KMS is ALWAYS available (deps.KMS); MPC + treasury only when the cluster is
// wired (CLOUD_WALLETS_MPC_ADDR) and the JWT secret resolves from KMS — else
// those Kinds fail closed with ErrMPCNotConfigured.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/hanzoai/cloud/audit"
	"github.com/luxfi/crypto"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// Config env keys — the config seam.
const (
	envMPCAddr        = "CLOUD_WALLETS_MPC_ADDR"           // comma-sep ring node base URLs (:9800); unset ⇒ mpc/treasury/safe fail closed
	envMPCKeyRef      = "CLOUD_WALLETS_MPC_API_KEY_REF"    // KMS ref of the ring's MPC_INTERNAL_API_KEY bearer token; NEVER a plaintext value
	envDefaultCustody = "CLOUD_WALLETS_DEFAULT_CUSTODY"    // default wallet custody; default "kms"
	envSafeAPIAddr    = "CLOUD_WALLETS_MPC_API_ADDR"       // ring PRODUCT API base (:8081) for Safe smart wallets; unset ⇒ safe custody fail closed
	envSafeJWTRef     = "CLOUD_WALLETS_MPC_JWT_SECRET_REF" // KMS ref of the ring's MPC_JWT_SECRET (HS256); NEVER a plaintext value
)

// state is wallets's own data; shared deps live in the embedded cloud.Base,
// reached as s.Log.
type state struct {
	store          *store
	custody        map[Kind]Custody
	defaultCustody Kind
	audit          *audit.Recorder // best-effort; nil disables it
}

// mounted is the process singleton the finance seam resolves. nil when the
// subsystem is not linked/enabled, which makes WalletForLedgerAccount a no-op.
var mounted *cloud.Service[state]

// Mount wires the wallets surface onto app per HIP-0106. Complex flavour: it
// holds a package-global (mounted, the finance seam singleton) so it constructs
// the Service value directly rather than via cloud.Mount.
func Mount(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("wallets.Mount: nil app")
	}
	if deps.Logger == nil {
		return fmt.Errorf("wallets.Mount: nil deps.Logger")
	}
	log := deps.Logger.New("subsystem", "wallets")
	if deps.DataDir == "" {
		return fmt.Errorf("wallets.Mount: empty DataDir")
	}
	st, err := openStore(deps.DataDir)
	if err != nil {
		return fmt.Errorf("wallets.Mount: open store: %w", err)
	}

	custody := buildCustody(deps, log)
	def := Kind(strings.TrimSpace(os.Getenv(envDefaultCustody)))
	if def == "" {
		def = KindKMS
	}

	s := &cloud.Service[state]{Base: cloud.NewBase(deps, "wallets"), State: state{
		store:          st,
		custody:        custody,
		defaultCustody: def,
		audit:          deps.Audit,
	}}
	mounted = s
	if err := routes(app, s); err != nil {
		return err
	}
	// The wallet store lives here, so payee resolution answers here (rpc.go).
	exposePayee()

	_, mpcOK := custody[KindMPC]
	log.Info("wallets mounted", "brand", deps.Brand, "defaultCustody", def, "mpcConfigured", mpcOK)
	return nil
}

// zipdoc lifts the doc comment off each typed op and its In/Out fields into
// zipdoc_gen.go, which is the ONLY way that prose reaches the published document
// and the MCP tool list — Go drops comments at compile time. Run by
// `make -C apps/wallets openapi`.
//
//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

// routes registers the wallets surface as TYPED ops: one registry entry each,
// which is what the OpenAPI operation, the MCP tool, the CLI command and every
// generated SDK method are all projected from. All eight are typed.
//
// Static /v1/wallets/accounts routes register BEFORE the /v1/wallets/:id param
// route so the static segment wins. The collection root is declared on the /v1
// PARENT with a non-empty leaf — `zip.Post(g, "", …)` would name /v1/wallets/, a
// path this API has never served, and op.Path is the identity every projection
// keys on.
func routes(app cloud.Router, s *cloud.Service[state]) error {
	// The typed registrars take the App behind the Router: a typed op is a route
	// PLUS a registry entry, and the registry lives on the App. A subsystem that
	// cannot reach it must fail its mount rather than serve routes no projection
	// knows about.
	zapp := cloud.ZipApp(app)
	if zapp == nil {
		return fmt.Errorf("wallets.Mount: router exposes no zip.App, so no typed op could be registered")
	}
	// cloud.Bridge parks the validated org on the context a typed op receives; it
	// is the composer's install — once at the root of every program — so this
	// package does not install its own.
	v1 := app.Group("/v1")
	g := v1.Group("/wallets")

	o := ops{s: s}
	zip.Post(g, "/accounts", o.createAccount)
	zip.Get(g, "/accounts", o.listAccounts)
	zip.Post(v1, "/wallets", o.createWallet)
	zip.Get(v1, "/wallets", o.listWallets)
	zip.Get(g, "/:id", o.getWallet)
	zip.Post(g, "/:id/keys", o.rotateKeys)
	zip.Post(g, "/:id/sign", o.sign)
	zip.Post(g, "/:id/transactions", o.proposeTransaction)
	return nil
}

// ops binds the store and the custody set to the typed wallets ops. A
// TypedHandler is func(context.Context, *In) (*Out, error) — no parameter for the
// service — so it arrives as a RECEIVER and every op is a method value, which is
// also the only bound form cmd/zipdoc can lift prose from.
type ops struct{ s *cloud.Service[state] }

// noInput is the In of an op addressed entirely by the caller's own validated
// principal: it takes nothing off the wire.
type noInput struct{}

// tenant is the VALIDATED org for a typed op — the one the gateway asserted and
// cloud.Bridge parked on the context, never a field of In. An In field is
// caller-supplied, so a tenant key read from one is a cross-tenant read the
// caller asserted for itself. Fails closed off the HTTP path.
func tenant(ctx context.Context) (string, error) {
	org, ok := principal.OrgFrom(ctx)
	if !ok {
		return "", zip.ErrForbidden("sign in")
	}
	return org, nil
}

// actor is the AUDIT SUBJECT for a typed op: the validated user id the
// tamper-evident trail attributes a wallet action to. It is X-User-Id, which
// principal.OrgFrom does not carry, so this is one of the two facts this package
// reaches for the REQUEST for. Empty off the HTTP path, where emitAudit falls
// back to the subsystem's own name exactly as it did for an unattributed call.
func actor(ctx context.Context) string {
	c, ok := cloud.Request(ctx)
	if !ok {
		return ""
	}
	return c.User()
}

// ambientProject is the caller's AMBIENT project scope — the narrowing a new
// wallet's key ref and store row are addressed under, folded so the org's default
// project keeps the un-suffixed ref. It rides in X-Project-Id, which the gateway
// and cloud's own identity boundary mint SERVER-SIDE from a validated claim after
// stripping any client copy; principal.OrgFrom does not carry it. It must NEVER
// become an In field: a caller-supplied project would let a request address key
// material under a scope no minter ever validated. Empty off the HTTP path, which
// is the org's default scope.
func ambientProject(ctx context.Context) string {
	c, ok := cloud.Request(ctx)
	if !ok {
		return ""
	}
	if p := principal.Project(c); !principal.IsDefaultProject(p) {
		return p
	}
	return ""
}

// buildCustody assembles the available custody backends. KMS is always present
// (the in-process spine); MPC + treasury only when the ring address + internal
// API key are both wired — otherwise those Kinds are absent and custodyFor
// fails closed.
func buildCustody(deps cloud.Deps, log luxlog.Logger) map[Kind]Custody {
	m := map[Kind]Custody{}
	if deps.KMS != nil {
		m[KindKMS] = kmsCustody{kms: deps.KMS}
	} else {
		log.Warn("wallets: deps.KMS is nil; kms custody unavailable")
	}
	nodes := splitNodes(os.Getenv(envMPCAddr))
	if len(nodes) == 0 {
		return m // mpc/treasury fail closed (ErrMPCNotConfigured)
	}
	key := loadMPCKey(deps, log)
	if len(key) == 0 {
		log.Warn("wallets: " + envMPCAddr + " set but MPC API key unresolved; mpc/treasury custody fail closed")
		return m
	}
	client := newMPCClient(nodes, key)
	m[KindMPC] = mpcCustody{http: client}
	m[KindTreasury] = treasuryCustody{http: client}
	log.Info("wallets: mpc custody configured", "nodes", len(nodes))

	// Safe smart-wallet custody additionally needs the ring PRODUCT API (:8081)
	// AND the HS256 MPC_JWT_SECRET (resolved from KMS). Absent either, KindSafe is
	// not offered (custodyFor fails it closed) while mpc/treasury stay available.
	if safeBase := strings.TrimSpace(os.Getenv(envSafeAPIAddr)); safeBase != "" {
		if secret := loadSafeJWTSecret(deps, log); len(secret) > 0 {
			m[KindSafe] = safeCustody{mpc: client, safe: newSafeClient(safeBase, secret)}
			log.Info("wallets: safe custody configured", "api", safeBase)
		} else {
			log.Warn("wallets: " + envSafeAPIAddr + " set but MPC JWT secret unresolved; safe custody fail closed")
		}
	}
	return m
}

// loadMPCKey fetches the ring's MPC_INTERNAL_API_KEY bearer token from KMS by
// the ref in CLOUD_WALLETS_MPC_API_KEY_REF. NEVER a plaintext env value. Empty
// ref or a KMS error ⇒ nil ⇒ mpc/treasury fail closed.
func loadMPCKey(deps cloud.Deps, log luxlog.Logger) []byte {
	ref := strings.TrimSpace(os.Getenv(envMPCKeyRef))
	if ref == "" || deps.KMS == nil {
		return nil
	}
	key, err := deps.KMS.GetSecret(context.Background(), ref)
	if err != nil {
		log.Warn("wallets: mpc API key ref did not resolve from KMS", "ref", ref, "err", err)
		return nil
	}
	return key
}

// loadSafeJWTSecret fetches the ring's MPC_JWT_SECRET (HS256) from KMS by the ref
// in CLOUD_WALLETS_MPC_JWT_SECRET_REF. NEVER a plaintext env value. Empty ref or a
// KMS error ⇒ nil ⇒ safe custody fail closed.
func loadSafeJWTSecret(deps cloud.Deps, log luxlog.Logger) []byte {
	ref := strings.TrimSpace(os.Getenv(envSafeJWTRef))
	if ref == "" || deps.KMS == nil {
		return nil
	}
	secret, err := deps.KMS.GetSecret(context.Background(), ref)
	if err != nil {
		log.Warn("wallets: mpc JWT secret ref did not resolve from KMS", "ref", ref, "err", err)
		return nil
	}
	return secret
}

// custodyFor resolves the backend for a kind. Missing mpc/treasury ⇒ fail closed
// (ErrMPCNotConfigured, 503-mappable); an unrecognized kind ⇒ ErrUnknownCustody
// (400-mappable). This is the config-selects-backend resolver.
func custodyFor(s *cloud.Service[state], kind Kind) (Custody, error) {
	if c, ok := s.State.custody[kind]; ok {
		return c, nil
	}
	switch kind {
	case KindMPC, KindTreasury, KindSafe:
		return nil, ErrMPCNotConfigured
	case KindKMS:
		return nil, fmt.Errorf("wallets: kms custody unavailable")
	default:
		return nil, fmt.Errorf("%w: %q", ErrUnknownCustody, kind)
	}
}

// ── In / Out shapes ──────────────────────────────────────────────────────────

// walletRef addresses one of the caller org's wallets. The id is the path
// segment: the URL is the addressing authority, so it binds from there whatever
// a body says — which is also what the untyped handlers did, reading
// c.Param("id"). `json:"-"` keeps it out of the body entirely.
type walletRef struct {
	// ID is the wallet to act on, from the path.
	ID string `json:"-" url:"id"`
}

// createAccountIn names a new wallet account. `url:"-"` keeps the name in the
// BODY: zip's binder fills an In field from the query string as well, and this
// route has never taken an account name there.
type createAccountIn struct {
	// Name is the account's label. Required, trimmed; it groups wallets and is
	// not itself a key.
	Name string `json:"name" url:"-"`
}

// accountList is the org's wallet accounts as one listing answers them.
type accountList struct {
	// Accounts are the org's accounts, newest first.
	Accounts []WalletAccount `json:"accounts"`
}

// createWalletIn provisions one signing identity. Every field is BODY-only
// (`url:"-"`): zip's binder fills an In field from the query string too, and
// this route has never taken a wallet's fields there — without the opt-out
// `?custody=kms` would silently choose a backend the body did not ask for.
//
// The wallet's PROJECT is not here on purpose. It is the caller's ambient,
// server-minted X-Project-Id scope, so putting it on the body would let a
// request address key material under a scope no minter ever validated.
type createWalletIn struct {
	// AccountID is the account this wallet belongs to. Required, and it must be
	// an account of the caller's own org — an unknown one is a 404.
	AccountID string `json:"accountId" url:"-"`
	// Agent optionally narrows the wallet to one agent within the org. It becomes
	// a segment of the key ref, so it must be a url-safe segment with no slash.
	Agent string `json:"agent" url:"-"`
	// Name is the wallet's display label. Optional.
	Name string `json:"name" url:"-"`
	// Custody selects the signing backend: "kms" (in-process, always available),
	// "mpc" or "treasury" (the deployed MPC ring), or "safe" (a Safe smart wallet
	// owned by an MPC key). Empty uses the deployment's default. A backend that is
	// not configured fails CLOSED with 503 rather than fabricating a signature.
	Custody string `json:"custody" url:"-"`
	// Tier is the MPC wallet tier: hot, warm, cold, gas, bridge, contract_admin,
	// validator, quarantine or disaster_recovery. Empty defaults to hot.
	Tier string `json:"tier" url:"-"`
	// Chain is the EVM chain this wallet is for, as "eip155:<n>" or a bare
	// decimal chain id. Optional; a Safe defaults to the Hanzo L1 (36963).
	Chain string `json:"chain" url:"-"`
}

// listWalletsIn narrows the org's wallet listing. All three come from the query
// string and only NARROW within the caller's own org — none of them can widen
// past it, because the org is the bound isolation boundary.
type listWalletsIn struct {
	// Project narrows to wallets scoped to one project. Must be a url-safe segment.
	Project string `json:"project"`
	// Agent narrows to wallets scoped to one agent. Must be a url-safe segment.
	Agent string `json:"agent"`
	// Account narrows to wallets under one account id. Must be a url-safe segment.
	Account string `json:"account"`
}

// walletList is the org's wallets as one listing answers them.
type walletList struct {
	// Wallets are the matching wallets, newest first.
	Wallets []Wallet `json:"wallets"`
}

// signIn asks one wallet to sign. Exactly one of digest or message is required;
// both are BODY-only, because a message to sign has never ridden in a URL and a
// query string lands in access logs.
type signIn struct {
	// ID is the wallet that signs, from the path.
	ID string `json:"-" url:"id"`
	// Message is arbitrary text to hash with Keccak256 and sign. Used only when
	// digest is empty.
	Message string `json:"message" url:"-"`
	// Digest is a pre-computed 32-byte digest as hex, with or without the 0x
	// prefix. When present it is signed verbatim and message is ignored.
	Digest string `json:"digest" url:"-"`
}

// signature is what a wallet's signing op answers. Field order is the
// ALPHABETICAL key order the map this replaced marshalled in, so the bytes on
// the wire are unchanged.
type signature struct {
	// Address is the wallet's on-chain address, the one this signature recovers to.
	Address string `json:"address"`
	// Digest is the 32-byte digest that was signed, hex with an 0x prefix.
	Digest string `json:"digest"`
	// Signature is the 65-byte secp256k1 signature, hex with an 0x prefix.
	Signature string `json:"signature"`
	// WalletID is the wallet that signed.
	WalletID string `json:"walletId"`
}

// safeTxIn proposes one Safe transaction. Every field but the path id is
// BODY-only, for the same reason signIn's are.
type safeTxIn struct {
	// ID is the Safe-custody wallet to propose on, from the path.
	ID string `json:"-" url:"id"`
	// To is the transaction's target address.
	To string `json:"to" url:"-"`
	// Value is the native-token amount to send, as a decimal string in wei.
	Value string `json:"value" url:"-"`
	// Data is the call data, hex-encoded.
	Data string `json:"data" url:"-"`
	// ChainID is the EVM chain the Safe transaction is bound to. 0 uses the
	// wallet's own chain, or the Hanzo L1 (36963) when it is chain-agnostic.
	ChainID int64 `json:"chainId" url:"-"`
	// Nonce is the Safe's transaction nonce.
	Nonce int `json:"nonce" url:"-"`
}

// safeProposal is what a Safe transaction proposal answers. Field order is the
// ALPHABETICAL key order the map this replaced marshalled in, so the bytes on
// the wire are unchanged.
type safeProposal struct {
	// R is the r component of the MPC threshold signature over the Safe-tx hash.
	R string `json:"r"`
	// S is the s component of that signature.
	S string `json:"s"`
	// SafeAddress is the Safe contract this transaction is for.
	SafeAddress string `json:"safeAddress"`
	// SafeTxHash is the EIP-712 Safe transaction hash, bound to the Safe contract
	// and the chain id — the value the owner approval signs.
	SafeTxHash string `json:"safeTxHash"`
	// WalletID is the wallet whose Safe this is.
	WalletID string `json:"walletId"`
}

// ── account ops ──────────────────────────────────────────────────────────────

// createAccount opens a named wallet account for the caller's org. An account is
// a GROUPING of wallets, not a key or a balance: wallets are created under one
// and can be listed by it. The org is stamped by the server from the validated
// principal, so a request can never open an account in another tenant.
//
// Example: {"name": "treasury"}
func (o ops) createAccount(ctx context.Context, in *createAccountIn) (*WalletAccount, error) {
	org, err := tenant(ctx)
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return nil, zip.ErrBadRequest("name is required")
	}
	a := &WalletAccount{ID: newID("acct"), Org: org, Name: name, CreatedAt: time.Now().Unix()}
	if err := o.s.State.store.createAccount(ctx, a); err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "create account: %v", err)
	}
	emitAudit(o.s, ctx, org, actor(ctx), "wallets.account.create", a.ID, map[string]any{"name": name})
	return a, nil
}

// listAccounts returns the caller org's wallet accounts, newest first. Accounts
// are physically org-scoped, so another tenant's are not reachable from here.
func (o ops) listAccounts(ctx context.Context, _ *noInput) (*accountList, error) {
	org, err := tenant(ctx)
	if err != nil {
		return nil, err
	}
	accounts, err := o.s.State.store.listAccounts(ctx, org)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list accounts: %v", err)
	}
	return &accountList{Accounts: accounts}, nil
}

// ── wallet ops ───────────────────────────────────────────────────────────────

// createWallet provisions a new signing identity under one of the caller org's
// accounts and answers the stored wallet including its on-chain address. The
// custody backend generates the key material — a KMS-sealed secp256k1 key, an
// MPC threshold key on the ring, or a Safe smart wallet owned by one — and the
// HANDLE to it is kept server-side and never returned. A custody kind the
// deployment has not wired fails CLOSED with 503: a signature is never
// fabricated. The wallet is scoped to the org, the caller's ambient project, and
// optionally an agent and the named account; those narrowings are what its key
// ref is derived from, so each must be a url-safe segment.
//
// Example: {"accountId": "acct_9f8c1d", "name": "ops hot wallet", "custody": "kms", "tier": "hot", "chain": "eip155:36963"}
func (o ops) createWallet(ctx context.Context, in *createWalletIn) (*Wallet, error) {
	s := o.s
	org, err := tenant(ctx)
	if err != nil {
		return nil, err
	}
	accountID := strings.TrimSpace(in.AccountID)
	if accountID == "" {
		return nil, zip.ErrBadRequest("accountId is required")
	}
	if _, found, err := s.State.store.getAccount(ctx, org, accountID); err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "get account: %v", err)
	} else if !found {
		return nil, zip.ErrNotFound("account not found")
	}

	// Resolve the wallet's scope within the org. Project is the request's ambient
	// scope (X-Project-Id via principal); the DEFAULT project collapses to empty so
	// an org's default-scope wallet keeps the un-suffixed ref (the backward-compat
	// invariant keyed surfaces honor). Agent + account are explicit narrowings the
	// caller assigns. All are validated to a slash-free segment so no value can
	// cross into another wallet's KMS ref.
	scopeProject := ambientProject(ctx)
	agent := strings.TrimSpace(in.Agent)
	if !validNarrowing(scopeProject) || !validNarrowing(agent) || !validNarrowing(accountID) {
		return nil, zip.ErrBadRequest("project, agent, and accountId must be url-safe segments")
	}

	kind := Kind(strings.TrimSpace(in.Custody))
	if kind == "" {
		kind = s.State.defaultCustody
	}
	cust, err := custodyFor(s, kind)
	if err != nil {
		return nil, custodyHTTPError(err)
	}
	tier := Tier(strings.TrimSpace(in.Tier))
	if tier == "" {
		tier = DefaultTier
	}
	if !validTier(tier) {
		return nil, zip.ErrBadRequest("invalid tier: " + string(tier))
	}

	w := &Wallet{
		ID:        newID("wal"),
		Scope:     Scope{Org: org, Project: scopeProject, Agent: agent, AccountID: accountID},
		Name:      strings.TrimSpace(in.Name),
		Custody:   kind,
		Tier:      tier,
		Chain:     strings.TrimSpace(in.Chain),
		CreatedAt: time.Now().Unix(),
	}
	address, err := cust.Provision(ctx, w) // sets w.KeyRef
	if err != nil {
		return nil, custodyHTTPError(err)
	}
	w.Address = address
	if err := s.State.store.createWallet(ctx, w); err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "create wallet: %v", err)
	}
	emitAudit(s, ctx, org, actor(ctx), "wallets.wallet.create", w.ID,
		map[string]any{"custody": string(kind), "tier": string(tier), "chain": w.Chain, "address": address,
			"project": w.Project, "agent": w.Agent, "accountId": w.AccountID})
	return w, nil
}

// listWallets returns the caller org's wallets, newest first, optionally NARROWED
// within the org by project, agent or account. The org is always the bound
// isolation boundary — the filters only ever narrow inside it, so a caller can
// never widen past its own org.
//
// Example: {"account": "acct_9f8c1d"}
func (o ops) listWallets(ctx context.Context, in *listWalletsIn) (*walletList, error) {
	org, err := tenant(ctx)
	if err != nil {
		return nil, err
	}
	sc := Scope{
		Org:       org,
		Project:   strings.TrimSpace(in.Project),
		Agent:     strings.TrimSpace(in.Agent),
		AccountID: strings.TrimSpace(in.Account),
	}
	if !validNarrowing(sc.Project) || !validNarrowing(sc.Agent) || !validNarrowing(sc.AccountID) {
		return nil, zip.ErrBadRequest("project, agent, and account filters must be url-safe segments")
	}
	wallets, err := o.s.State.store.listWalletsByScope(ctx, sc)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list wallets: %v", err)
	}
	return &walletList{Wallets: wallets}, nil
}

// getWallet returns one of the caller org's wallets: its scope, custody kind,
// tier, chain and on-chain address. The custody handle to the signing material is
// never part of the answer. A wallet id another org owns reads as not found, so
// the response cannot confirm that it exists.
//
// Example: {"id": "wal_4b1e77"}
func (o ops) getWallet(ctx context.Context, in *walletRef) (*Wallet, error) {
	org, err := tenant(ctx)
	if err != nil {
		return nil, err
	}
	w, found, err := o.s.State.store.getWallet(ctx, org, strings.TrimSpace(in.ID))
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "get wallet: %v", err)
	}
	if !found {
		return nil, zip.ErrNotFound("wallet not found") // tenant isolation: another org's id is not-found
	}
	return w, nil
}

// rotateKeys rolls one wallet's signing material through its own custody backend
// and answers the wallet with whatever address that produced. For KMS custody a
// fresh secp256k1 key is generated and sealed, which CHANGES the address — funds
// and approvals at the old address do not move. For a Safe the address is
// counterfactual and the owner shares are ring-managed, so rotation is a no-op
// and the address is unchanged. A backend that is not configured fails closed
// with 503 rather than leaving the wallet half-rotated.
//
// Example: {"id": "wal_4b1e77"}
func (o ops) rotateKeys(ctx context.Context, in *walletRef) (*Wallet, error) {
	s := o.s
	org, err := tenant(ctx)
	if err != nil {
		return nil, err
	}
	w, found, err := s.State.store.getWallet(ctx, org, strings.TrimSpace(in.ID))
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "get wallet: %v", err)
	}
	if !found {
		return nil, zip.ErrNotFound("wallet not found")
	}
	cust, err := custodyFor(s, w.Custody)
	if err != nil {
		return nil, custodyHTTPError(err)
	}
	address, err := cust.Rotate(ctx, w) // may set w.KeyRef
	if err != nil {
		return nil, custodyHTTPError(err)
	}
	w.Address = address
	if err := s.State.store.updateWalletKey(ctx, org, w.ID, w.Address, w.KeyRef); err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "persist rotation: %v", err)
	}
	emitAudit(s, ctx, org, actor(ctx), "wallets.wallet.rotate", w.ID, map[string]any{"address": address})
	return w, nil
}

// sign produces a secp256k1 signature from one of the caller org's wallets over
// a 32-byte digest, through whichever custody backend that wallet uses. Give it
// either a `digest` (32 bytes as hex, signed verbatim) or a `message` (hashed
// with Keccak256 first) — exactly one is required. The private key never leaves
// its backend: KMS custody opens the sealed key in-process, MPC custody produces
// a threshold signature on the ring. The answer carries the digest that was
// signed alongside the signature, so a caller can verify what it got.
//
// Example: {"id": "wal_4b1e77", "message": "approve withdrawal 42"}
func (o ops) sign(ctx context.Context, in *signIn) (*signature, error) {
	s := o.s
	org, err := tenant(ctx)
	if err != nil {
		return nil, err
	}
	w, found, err := s.State.store.getWallet(ctx, org, strings.TrimSpace(in.ID))
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "get wallet: %v", err)
	}
	if !found {
		return nil, zip.ErrNotFound("wallet not found")
	}
	digest, err := resolveDigest(in.Digest, in.Message)
	if err != nil {
		return nil, zip.ErrBadRequest(err.Error())
	}
	cust, err := custodyFor(s, w.Custody)
	if err != nil {
		return nil, custodyHTTPError(err)
	}
	sig, err := cust.Sign(ctx, w, digest)
	if err != nil {
		return nil, custodyHTTPError(err)
	}
	emitAudit(s, ctx, org, actor(ctx), "wallets.wallet.sign", w.ID,
		map[string]any{"digest": "0x" + hex.EncodeToString(digest)})
	return &signature{
		Address:   w.Address,
		Digest:    "0x" + hex.EncodeToString(digest),
		Signature: "0x" + hex.EncodeToString(sig),
		WalletID:  w.ID,
	}, nil
}

// proposeTransaction composes a Safe transaction on the MPC ring and answers its
// EIP-712 hash together with the owner approval the ring's threshold signature
// produced. Only a wallet whose custody is "safe" can do this — any other custody
// is a 400, because the backend itself is asked whether it can propose rather
// than the kind being switched on. The ring computes the Safe-tx hash bound to
// the Safe contract and the chain id, so the hash a caller gets back is the one
// the Safe will verify. This PROPOSES: it does not execute the transaction.
//
// Example: {"id": "wal_4b1e77", "to": "0x1f9840a85d5aF5bf1D1762F925BDADdC4201F984", "value": "0", "data": "0x", "chainId": 36963, "nonce": 7}
func (o ops) proposeTransaction(ctx context.Context, in *safeTxIn) (*safeProposal, error) {
	s := o.s
	org, err := tenant(ctx)
	if err != nil {
		return nil, err
	}
	w, found, err := s.State.store.getWallet(ctx, org, strings.TrimSpace(in.ID))
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "get wallet: %v", err)
	}
	if !found {
		return nil, zip.ErrNotFound("wallet not found")
	}
	cust, err := custodyFor(s, w.Custody)
	if err != nil {
		return nil, custodyHTTPError(err)
	}
	proposer, ok := cust.(safeProposer)
	if !ok {
		return nil, zip.ErrBadRequest("wallet custody " + string(w.Custody) + " does not support safe transactions")
	}
	res, err := proposer.ProposeTx(ctx, w, SafeTx{
		To: strings.TrimSpace(in.To), Value: strings.TrimSpace(in.Value),
		Data: strings.TrimSpace(in.Data), ChainID: in.ChainID, Nonce: in.Nonce,
	})
	if err != nil {
		return nil, custodyHTTPError(err)
	}
	emitAudit(s, ctx, org, actor(ctx), "wallets.wallet.safe_tx", w.ID,
		map[string]any{"to": in.To, "chainId": in.ChainID, "safeTxHash": res.SafeTxHash})
	return &safeProposal{
		R:           res.R,
		S:           res.S,
		SafeAddress: w.Address,
		SafeTxHash:  res.SafeTxHash,
		WalletID:    w.ID,
	}, nil
}

// ── finance seam (seam ONLY — no live wiring, does NOT touch treasury) ────────

// WalletForLedgerAccount resolves the on-chain wallet bound to a finance ledger
// account — the seam by which the treasury reserve signer BECOMES an MPC treasury
// wallet later. Pure lookup; ("",false) when unmounted/unbound. Does NOT modify treasury.
func WalletForLedgerAccount(ctx context.Context, org, ledgerAccount string) (address string, ok bool) {
	s := mounted
	if s == nil || strings.TrimSpace(org) == "" || strings.TrimSpace(ledgerAccount) == "" {
		return "", false
	}
	w, found, err := s.State.store.walletForFinanceAccount(ctx, org, ledgerAccount)
	if err != nil || !found {
		return "", false
	}
	return w.Address, true
}

// PaymentTarget is a wallet resolved for RECEIVING a payment: its on-chain address
// (the x402 payee) plus the org + ledger subject whose books the earnings credit.
type PaymentTarget struct {
	Address string
	Org     string
	Subject string // ledger subject for the earnings credit (the wallet id)
}

// Mounted reports whether this process holds the wallet store.
//
// It exists because ResolvePaymentTarget folds two facts into one false — "there is
// no such wallet" and "the wallets subsystem is not in this binary" — and a caller
// that must ask elsewhere when it is absent has to tell them apart. Answering the
// first over a socket would be a second lookup of a question already answered NO;
// answering the second in memory is impossible.
func Mounted() bool { return mounted != nil }

// ResolvePaymentTarget resolves a payout wallet {org, walletID} to its address +
// ledger subject — the seam the x402 settlement uses to route payment to a
// recipient wallet. The lookup is org-scoped (getWallet), so a resource can only
// ever name a wallet WITHIN the org it declared: no cross-org payee spoofing.
// ("", false) when wallets is unmounted or the wallet is not found in that org.
func ResolvePaymentTarget(ctx context.Context, org, walletID string) (PaymentTarget, bool) {
	s := mounted
	if s == nil || strings.TrimSpace(org) == "" || strings.TrimSpace(walletID) == "" {
		return PaymentTarget{}, false
	}
	w, found, err := s.State.store.getWallet(ctx, org, strings.TrimSpace(walletID))
	if err != nil || !found {
		return PaymentTarget{}, false
	}
	return PaymentTarget{Address: w.Address, Org: w.Org, Subject: w.ID}, true
}

// ── helpers ──────────────────────────────────────────────────────────────────

func idParam(c *zip.Ctx) string { return strings.TrimSpace(c.Param("id")) }

// custodyHTTPError maps a custody error to the right HTTP status: fail-closed MPC
// ⇒ 503, unknown custody ⇒ 400, delegation failures ⇒ 502, else 500.
func custodyHTTPError(err error) error {
	switch {
	case errors.Is(err, ErrMPCNotConfigured):
		return zip.Errorf(http.StatusServiceUnavailable, "%s", err.Error())
	case errors.Is(err, ErrUnknownCustody):
		return zip.ErrBadRequest(err.Error())
	default:
		return zip.Errorf(http.StatusBadGateway, "custody: %v", err)
	}
}

// resolveDigest returns the 32-byte digest to sign: a supplied hex digest (0x
// optional, must be 32 bytes) or Keccak256(message). One of the two is required.
func resolveDigest(digestHex, message string) ([]byte, error) {
	digestHex = strings.TrimSpace(digestHex)
	if digestHex != "" {
		d, err := hex.DecodeString(trim0x(digestHex))
		if err != nil {
			return nil, fmt.Errorf("digest must be hex: %v", err)
		}
		if len(d) != 32 {
			return nil, fmt.Errorf("digest must be 32 bytes, got %d", len(d))
		}
		return d, nil
	}
	if strings.TrimSpace(message) != "" {
		return crypto.Keccak256([]byte(message)), nil
	}
	return nil, errors.New("provide a message or a 32-byte hex digest")
}

// splitNodes parses comma-separated node URLs, trimming and dropping empties.
func splitNodes(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// newID mints a short random id with a type prefix (e.g. "wal_9f8c...").
func newID(prefix string) string {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand failure is unrecoverable; fall back to a time-based id.
		return prefix + "_" + fmt.Sprintf("%x", time.Now().UnixNano())
	}
	return prefix + "_" + hex.EncodeToString(b)
}

// emitAudit records a wallet action in cloud's tamper-evident trail. Best-effort;
// a nil store is a no-op. Never records key material — only ids and names.
func emitAudit(s *cloud.Service[state], ctx context.Context, org, sub, action, resourceID string, after map[string]any) {
	if s.State.audit == nil {
		return
	}
	if sub == "" {
		sub = "wallets"
	}
	rec := audit.Record{
		Actor:    audit.Actor{Org: org, Sub: sub},
		Action:   action,
		Resource: audit.Resource{Type: "wallet", ID: resourceID},
		Auth:     audit.AuthContext{Method: "session"},
		Outcome:  audit.Outcome{Result: "success", Status: 200},
		After:    audit.Redact(mustJSON(after)),
	}
	if _, err := s.State.audit.Append(ctx, rec); err != nil {
		s.Log.Error("wallets: audit emit failed", "action", action, "err", err)
	}
}

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return b
}

// Shutdown closes the store. Idempotent.
func Shutdown() error {
	if mounted == nil || mounted.State.store == nil {
		return nil
	}
	err := mounted.State.store.Close()
	mounted = nil
	return err
}
