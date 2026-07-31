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
package wallets

//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/audit"
	"github.com/hanzoai/cloud/apps/principal"
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
	if err := os.MkdirAll(deps.DataDir, 0o755); err != nil {
		return fmt.Errorf("wallets.Mount: data dir: %w", err)
	}
	st, err := openStore(filepath.Join(deps.DataDir, "wallets.db"))
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
	routes(app, s)

	_, mpcOK := custody[KindMPC]
	log.Info("wallets mounted", "brand", deps.Brand, "defaultCustody", def, "mpcConfigured", mpcOK)
	return nil
}

// ops binds the wallets state to the typed handlers. A zip TypedHandler is
// func(context.Context, *In) (*Out, error) — no parameter for the service — so the
// service arrives as a RECEIVER and every op is a method value.
type ops struct{ s *cloud.Service[state] }

// routes registers the wallets surface. Every route is a zip TYPED op, so the REST
// route, the OpenAPI document, the MCP tool and the CLI command all derive from ONE
// declaration. Static /v1/wallets/accounts routes register BEFORE the
// /v1/wallets/:id param route so the static segment wins.
func routes(app cloud.Router, s *cloud.Service[state]) {
	o := ops{s: s}
	z := cloud.ZipApp(app)
	// The bridge FIRST: fiber runs middleware in registration order, so one
	// installed after these leaves would never run — and every op below resolves its
	// tenant (and its ambient project) through the request it parks.
	app.Group("/v1/wallets").Use(cloud.Bridge())

	zip.Post(z, "/v1/wallets/accounts", o.createAccount)
	zip.Get(z, "/v1/wallets/accounts", o.listAccounts)
	// Collection root (/v1/wallets) stays flat — Group(p).Post("") yields "p/".
	zip.Post(z, "/v1/wallets", o.createWallet)
	zip.Get(z, "/v1/wallets", o.listWallets)
	zip.Get(z, "/v1/wallets/:id", o.getWallet)
	zip.Post(z, "/v1/wallets/:id/keys", o.rotateKeys)
	zip.Post(z, "/v1/wallets/:id/sign", o.sign)
	zip.Post(z, "/v1/wallets/:id/safe-tx", o.proposeSafeTx)
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

// ── account handlers ─────────────────────────────────────────────────────────

// ── wire shapes ──────────────────────────────────────────────────────────────
//
// Account and Wallet embed Scope, which Go's encoder INLINES: the bytes carry
// org/project/agent/accountId at the top level. A schema generated off the Go
// type states a nested "Scope" object instead — a shape no response has ever
// had — so the boundary declares its own FLAT mirrors. Same json tags, same
// values, one mapping each: what the document promises is what the wire sends.

// walletView is one wallet as it goes on the wire. KeyRef is absent by
// construction — it is the custody-internal handle and never serialized.
type walletView struct {
	// ID is the wallet id.
	ID string `json:"id"`
	// Org is the owning tenant — the isolation boundary, never crossed.
	Org string `json:"org"`
	// Project is the org project the wallet is narrowed to, absent when org-wide.
	Project string `json:"project,omitempty"`
	// Agent is the agent the wallet is narrowed to, absent when unassigned.
	Agent string `json:"agent,omitempty"`
	// AccountID is the account grouping the wallet belongs to.
	AccountID string `json:"accountId"`
	// Name is the human label for the wallet.
	Name string `json:"name"`
	// Custody is the signing backend: kms, mpc, treasury or safe.
	Custody Kind `json:"custody"`
	// Tier is one of the nine wallet tiers, e.g. hot or cold.
	Tier Tier `json:"tier"`
	// Chain is the EVM chain the wallet addresses, empty when chain-agnostic.
	Chain string `json:"chain"`
	// Address is the wallet's on-chain address, as custody provisioned it.
	Address string `json:"address"`
	// FinanceAccount is the ledger account this wallet backs, absent when unbound.
	FinanceAccount string `json:"financeAccount,omitempty"`
	// CreatedAt is the creation time, unix seconds.
	CreatedAt int64 `json:"createdAt"`
}

// toWalletView is the ONE wallet→wire mapping; every wallet-returning op goes
// through it, so no op can drift from the declared shape.
func toWalletView(w *Wallet) walletView {
	return walletView{
		ID: w.ID, Org: w.Org, Project: w.Project, Agent: w.Agent, AccountID: w.AccountID,
		Name: w.Name, Custody: w.Custody, Tier: w.Tier, Chain: w.Chain, Address: w.Address,
		FinanceAccount: w.FinanceAccount, CreatedAt: w.CreatedAt,
	}
}

// walletAccount is one account grouping as it goes on the wire.
type walletAccount struct {
	// ID is the account id, the value a wallet's accountId references.
	ID string `json:"id"`
	// Org is the owning tenant.
	Org string `json:"org"`
	// Name is the human label for the grouping.
	Name string `json:"name"`
	// CreatedAt is the creation time, unix seconds.
	CreatedAt int64 `json:"createdAt"`
}

// toWalletAccount is the ONE account→wire mapping.
func toWalletAccount(a Account) walletAccount {
	return walletAccount{ID: a.ID, Org: a.Org, Name: a.Name, CreatedAt: a.CreatedAt}
}

// createAccountReq names a new wallet grouping.
type createAccountReq struct {
	// Name is the human label for the grouping. Required.
	Name string `json:"name"`
}

// walletAccountList is the account-listing envelope.
type walletAccountList struct {
	// Accounts are the wallet groupings owned by the caller's org.
	Accounts []walletAccount `json:"accounts"`
}

// createAccount creates a named wallet grouping in the caller's org. An account is
// the addressing narrowing a wallet is later assigned to; it holds no key material.
//
// Example: {"name": "treasury"}
func (o ops) createAccount(ctx context.Context, in *createAccountReq) (*walletAccount, error) {
	c, org, err := o.begin(ctx)
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return nil, zip.ErrBadRequest("name is required")
	}
	a := &Account{ID: newID("acct"), Org: org, Name: name, CreatedAt: time.Now().Unix()}
	if err := o.s.State.store.createAccount(ctx, a); err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "create account: %v", err)
	}
	emitAudit(o.s, ctx, org, c.User(), "wallets.account.create", a.ID, map[string]any{"name": name})
	out := toWalletAccount(*a)
	return &out, nil
}

// listAccounts lists the wallet groupings the caller's org owns. Org is the bound
// isolation boundary, so another tenant's accounts are never returned.
//
// Response: {"accounts": [{"id": "acct_9f2c", "org": "acme", "name": "treasury", "createdAt": 1780000000}]}
func (o ops) listAccounts(ctx context.Context, _ *struct{}) (*walletAccountList, error) {
	_, org, err := o.begin(ctx)
	if err != nil {
		return nil, err
	}
	accounts, err := o.s.State.store.listAccounts(ctx, org)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list accounts: %v", err)
	}
	out := make([]walletAccount, 0, len(accounts))
	for _, a := range accounts {
		out = append(out, toWalletAccount(a))
	}
	return &walletAccountList{Accounts: out}, nil
}

// ── wallet handlers ──────────────────────────────────────────────────────────

// createWalletReq provisions one wallet under an account of the caller's org.
type createWalletReq struct {
	// AccountID is the account grouping the wallet belongs to. Required, and it
	// must already exist in the caller's org.
	AccountID string `json:"accountId"`
	// Agent optionally narrows the wallet to one agent within the org.
	Agent string `json:"agent"`
	// Name is the human label for the wallet.
	Name string `json:"name"`
	// Custody selects the signing backend: kms, mpc, treasury or safe. Empty takes
	// the deployment's default; mpc/treasury/safe are 503 until the ring is wired.
	Custody string `json:"custody"`
	// Tier is one of the nine wallet tiers (hot, warm, cold, gas, bridge,
	// contract_admin, validator, quarantine, disaster_recovery). Empty means hot.
	Tier string `json:"tier"`
	// Chain is the EVM chain the wallet addresses; empty leaves it chain-agnostic.
	Chain string `json:"chain"`
}

// createWallet provisions one wallet under an account of the caller's org. The
// custody backend creates the signing material and returns its address; the
// wallet's scope (org, ambient project, agent, account) is what its key material
// is addressed by, so nothing here can reach another tenant's keys.
//
// Example: {"accountId": "acct_9f2c", "name": "ops hot wallet", "custody": "kms", "tier": "hot", "chain": "36963"}
func (o ops) createWallet(ctx context.Context, in *createWalletReq) (*walletView, error) {
	c, org, err := o.begin(ctx)
	if err != nil {
		return nil, err
	}
	body := *in
	s := o.s
	accountID := strings.TrimSpace(body.AccountID)
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
	scopeProject := ""
	if p := principal.Project(c); !principal.IsDefaultProject(p) {
		scopeProject = p
	}
	agent := strings.TrimSpace(body.Agent)
	if !validNarrowing(scopeProject) || !validNarrowing(agent) || !validNarrowing(accountID) {
		return nil, zip.ErrBadRequest("project, agent, and accountId must be url-safe segments")
	}

	kind := Kind(strings.TrimSpace(body.Custody))
	if kind == "" {
		kind = s.State.defaultCustody
	}
	cust, err := custodyFor(s, kind)
	if err != nil {
		return nil, custodyHTTPError(err)
	}
	tier := Tier(strings.TrimSpace(body.Tier))
	if tier == "" {
		tier = DefaultTier
	}
	if !validTier(tier) {
		return nil, zip.ErrBadRequest("invalid tier: " + string(tier))
	}

	w := &Wallet{
		ID:        newID("wal"),
		Scope:     Scope{Org: org, Project: scopeProject, Agent: agent, AccountID: accountID},
		Name:      strings.TrimSpace(body.Name),
		Custody:   kind,
		Tier:      tier,
		Chain:     strings.TrimSpace(body.Chain),
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
	emitAudit(s, ctx, org, c.User(), "wallets.wallet.create", w.ID,
		map[string]any{"custody": string(kind), "tier": string(tier), "chain": w.Chain, "address": address,
			"project": w.Project, "agent": w.Agent, "accountId": w.AccountID})
	out := toWalletView(w)
	return &out, nil
}

// walletFilter narrows a wallet listing WITHIN the caller's org. Org itself is
// never a field here — it is the bound isolation boundary the server supplies.
type walletFilter struct {
	// Project narrows to one org project; empty means every project.
	Project string `json:"project"`
	// Agent narrows to one agent; empty means every agent.
	Agent string `json:"agent"`
	// Account narrows to one account grouping; empty means every account.
	Account string `json:"account"`
}

// walletList is the wallet-listing envelope.
type walletList struct {
	// Wallets are the caller's own wallets matching the narrowings.
	Wallets []walletView `json:"wallets"`
}

// listWallets lists the caller's wallets, narrowed within the org. Project, agent
// and account are optional filters — the API face of the ONE scope lookup path —
// while org is always the bound isolation boundary, so a caller can never widen
// past its own org.
//
// Response: {"wallets": [{"id": "wal_3d81", "org": "acme", "accountId": "acct_9f2c", "name": "ops hot wallet", "custody": "kms", "tier": "hot", "chain": "36963", "address": "0x5b1c…", "createdAt": 1780000000}]}
func (o ops) listWallets(ctx context.Context, in *walletFilter) (*walletList, error) {
	_, org, err := o.begin(ctx)
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
	out := make([]walletView, 0, len(wallets))
	for i := range wallets {
		out = append(out, toWalletView(&wallets[i]))
	}
	return &walletList{Wallets: out}, nil
}

// walletRef addresses one wallet by id.
type walletRef struct {
	// ID is the wallet id from the path.
	ID string `json:"id"`
}

// getWallet reads one wallet of the caller's org. Another org's id is reported
// not-found, so the wallet id space leaks no existence across tenants.
//
// Example: {"id": "wal_3d81"}
func (o ops) getWallet(ctx context.Context, in *walletRef) (*walletView, error) {
	_, org, err := o.begin(ctx)
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
	out := toWalletView(w)
	return &out, nil
}

// rotateKeys rolls one wallet's signing material. The new material is created by
// the wallet's own custody backend and its address persisted, while the wallet id
// and scope are unchanged, so every reference to the wallet keeps resolving.
//
// Example: {"id": "wal_3d81"}
func (o ops) rotateKeys(ctx context.Context, in *walletRef) (*walletView, error) {
	c, org, err := o.begin(ctx)
	if err != nil {
		return nil, err
	}
	s := o.s
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
	emitAudit(s, ctx, org, c.User(), "wallets.wallet.rotate", w.ID, map[string]any{"address": address})
	out := toWalletView(w)
	return &out, nil
}

// signReq asks one wallet to sign a 32-byte digest.
type signReq struct {
	// ID is the wallet id from the path.
	ID string `json:"id"`
	// Message is plain text to hash into the digest. Ignored when digest is set.
	Message string `json:"message"`
	// Digest is a pre-computed 32-byte digest, hex, with or without the 0x prefix.
	Digest string `json:"digest"`
}

// signOut is a wallet signature over one digest.
type signOut struct {
	// WalletID is the wallet that signed.
	WalletID string `json:"walletId"`
	// Address is the wallet's on-chain address.
	Address string `json:"address"`
	// Digest is the 32-byte digest that was signed, 0x-prefixed hex.
	Digest string `json:"digest"`
	// Signature is the produced signature, 0x-prefixed hex.
	Signature string `json:"signature"`
}

// sign signs a 32-byte digest with one wallet of the caller's org. Signing runs in
// that wallet's own custody backend, so the key material never leaves it; supply
// digest directly, or message to have it hashed.
//
// Example: {"id": "wal_3d81", "message": "transfer 10 to 0x5b1c"}
func (o ops) sign(ctx context.Context, in *signReq) (*signOut, error) {
	c, org, err := o.begin(ctx)
	if err != nil {
		return nil, err
	}
	s := o.s
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
	emitAudit(s, ctx, org, c.User(), "wallets.wallet.sign", w.ID,
		map[string]any{"digest": "0x" + hex.EncodeToString(digest)})
	return &signOut{
		WalletID:  w.ID,
		Address:   w.Address,
		Digest:    "0x" + hex.EncodeToString(digest),
		Signature: "0x" + hex.EncodeToString(sig),
	}, nil
}

// safeTxReq proposes one Safe transaction for a safe-custody wallet.
type safeTxReq struct {
	// ID is the wallet id from the path; it must be a safe-custody wallet.
	ID string `json:"id"`
	// To is the destination address of the Safe transaction.
	To string `json:"to"`
	// Value is the native-token amount, as a decimal string in base units.
	Value string `json:"value"`
	// Data is the calldata, hex, empty for a plain transfer.
	Data string `json:"data"`
	// ChainID is the EVM chain the Safe is bound to; 0 takes the wallet's chain.
	ChainID int64 `json:"chainId"`
	// Nonce is the Safe's transaction nonce.
	Nonce int `json:"nonce"`
}

// safeTxOut is the ring's Safe-tx hash plus the threshold owner approval.
type safeTxOut struct {
	// WalletID is the safe-custody wallet the transaction was proposed for.
	WalletID string `json:"walletId"`
	// SafeAddress is the Safe contract address.
	SafeAddress string `json:"safeAddress"`
	// SafeTxHash is the EIP-712 Safe-tx hash the ring computed.
	SafeTxHash string `json:"safeTxHash"`
	// R is the r component of the threshold signature.
	R string `json:"r"`
	// S is the s component of the threshold signature.
	S string `json:"s"`
}

// proposeSafeTx composes propose and MPC-sign for a safe-custody wallet. The ring
// computes the EIP-712 Safe-tx hash — bound to the Safe contract and chain — and
// returns it with the threshold (r,s) its MPC produced, which is the owner
// approval; any other custody kind is a 400.
//
// Example: {"id": "wal_3d81", "to": "0x5b1c9e7a3f4d20e8b6c1a9d7f3025e4b8c6a1d90", "value": "1000000000000000000", "data": "0x", "chainId": 36963, "nonce": 7}
func (o ops) proposeSafeTx(ctx context.Context, in *safeTxReq) (*safeTxOut, error) {
	c, org, err := o.begin(ctx)
	if err != nil {
		return nil, err
	}
	s := o.s
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
	emitAudit(s, ctx, org, c.User(), "wallets.wallet.safe_tx", w.ID,
		map[string]any{"to": in.To, "chainId": in.ChainID, "safeTxHash": res.SafeTxHash})
	return &safeTxOut{
		WalletID:    w.ID,
		SafeAddress: w.Address,
		SafeTxHash:  res.SafeTxHash,
		R:           res.R,
		S:           res.S,
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

// begin resolves the request and the caller's org in ONE place. The org NEVER
// comes from an In field — an In field is caller-supplied, so a tenant key read
// from one is a cross-tenant read the caller asserted for itself. It comes from
// the request cloud.Bridge parked; off the HTTP path (the CLI projection's
// LocalInvoke) there is no request, so the op refuses. The request is returned
// alongside because the audit trail records the acting user, not just the org.
func (o ops) begin(ctx context.Context) (*zip.Ctx, string, error) {
	c, ok := cloud.Request(ctx)
	if !ok {
		return nil, "", zip.ErrForbidden("sign in")
	}
	org, ok := principal.Org(c)
	if !ok {
		return nil, "", zip.ErrForbidden("sign in")
	}
	return c, org, nil
}

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
