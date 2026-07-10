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
	"github.com/hanzoai/cloud/clients/principal"
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
func Mount(app *zip.App, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("wallets.Mount: nil zip.App")
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

// routes registers the wallets surface. Static /v1/wallets/accounts routes
// register BEFORE the /v1/wallets/:id param route so the static segment wins.
func routes(app *zip.App, s *cloud.Service[state]) {
	app.Post("/v1/wallets/accounts", cloud.Handle(s, createAccount))
	app.Get("/v1/wallets/accounts", cloud.Handle(s, listAccounts))
	app.Post("/v1/wallets", cloud.Handle(s, createWallet))
	app.Get("/v1/wallets", cloud.Handle(s, listWallets))
	app.Get("/v1/wallets/:id", cloud.Handle(s, getWallet))
	app.Post("/v1/wallets/:id/keys", cloud.Handle(s, rotateKeys))
	app.Post("/v1/wallets/:id/sign", cloud.Handle(s, sign))
	app.Post("/v1/wallets/:id/safe-tx", cloud.Handle(s, proposeSafeTx))
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

// init registers the subsystem. Order 127 — alongside the product control planes,
// before the AI /v1/* catch-all (150). Routes are specific so they bind ahead of it.
func init() {
	cloud.RegisterWithShutdown("wallets", 127, cloud.Typed(Mount), func(context.Context) error { return Shutdown() })
}

// ── account handlers ─────────────────────────────────────────────────────────

func createAccount(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := principal.Org(c)
	if !ok {
		return zip.ErrForbidden("sign in")
	}
	var body struct {
		Name string `json:"name"`
	}
	if err := c.Bind(&body); err != nil {
		return err
	}
	name := strings.TrimSpace(body.Name)
	if name == "" {
		return zip.ErrBadRequest("name is required")
	}
	a := &Account{ID: newID("acct"), Org: org, Name: name, CreatedAt: time.Now().Unix()}
	if err := s.State.store.createAccount(c.Context(), a); err != nil {
		return zip.Errorf(http.StatusInternalServerError, "create account: %v", err)
	}
	emitAudit(s, c.Context(), org, c.User(), "wallets.account.create", a.ID, map[string]any{"name": name})
	return c.JSON(http.StatusOK, a)
}

func listAccounts(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := principal.Org(c)
	if !ok {
		return zip.ErrForbidden("sign in")
	}
	accounts, err := s.State.store.listAccounts(c.Context(), org)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "list accounts: %v", err)
	}
	return c.JSON(http.StatusOK, map[string]any{"accounts": accounts})
}

// ── wallet handlers ──────────────────────────────────────────────────────────

func createWallet(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := principal.Org(c)
	if !ok {
		return zip.ErrForbidden("sign in")
	}
	var body struct {
		AccountID string `json:"accountId"`
		Name      string `json:"name"`
		Custody   string `json:"custody"`
		Tier      string `json:"tier"`
		Chain     string `json:"chain"`
	}
	if err := c.Bind(&body); err != nil {
		return err
	}
	accountID := strings.TrimSpace(body.AccountID)
	if accountID == "" {
		return zip.ErrBadRequest("accountId is required")
	}
	if _, found, err := s.State.store.getAccount(c.Context(), org, accountID); err != nil {
		return zip.Errorf(http.StatusInternalServerError, "get account: %v", err)
	} else if !found {
		return zip.ErrNotFound("account not found")
	}

	kind := Kind(strings.TrimSpace(body.Custody))
	if kind == "" {
		kind = s.State.defaultCustody
	}
	cust, err := custodyFor(s, kind)
	if err != nil {
		return custodyHTTPError(err)
	}
	tier := Tier(strings.TrimSpace(body.Tier))
	if tier == "" {
		tier = DefaultTier
	}
	if !validTier(tier) {
		return zip.ErrBadRequest("invalid tier: " + string(tier))
	}

	w := &Wallet{
		ID:        newID("wal"),
		Org:       org,
		AccountID: accountID,
		Name:      strings.TrimSpace(body.Name),
		Custody:   kind,
		Tier:      tier,
		Chain:     strings.TrimSpace(body.Chain),
		CreatedAt: time.Now().Unix(),
	}
	address, err := cust.Provision(c.Context(), w) // sets w.KeyRef
	if err != nil {
		return custodyHTTPError(err)
	}
	w.Address = address
	if err := s.State.store.createWallet(c.Context(), w); err != nil {
		return zip.Errorf(http.StatusInternalServerError, "create wallet: %v", err)
	}
	emitAudit(s, c.Context(), org, c.User(), "wallets.wallet.create", w.ID,
		map[string]any{"custody": string(kind), "tier": string(tier), "chain": w.Chain, "address": address})
	return c.JSON(http.StatusOK, w)
}

func listWallets(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := principal.Org(c)
	if !ok {
		return zip.ErrForbidden("sign in")
	}
	wallets, err := s.State.store.listWallets(c.Context(), org)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "list wallets: %v", err)
	}
	return c.JSON(http.StatusOK, map[string]any{"wallets": wallets})
}

func getWallet(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := principal.Org(c)
	if !ok {
		return zip.ErrForbidden("sign in")
	}
	w, found, err := s.State.store.getWallet(c.Context(), org, idParam(c))
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "get wallet: %v", err)
	}
	if !found {
		return zip.ErrNotFound("wallet not found") // tenant isolation: another org's id is not-found
	}
	return c.JSON(http.StatusOK, w)
}

func rotateKeys(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := principal.Org(c)
	if !ok {
		return zip.ErrForbidden("sign in")
	}
	w, found, err := s.State.store.getWallet(c.Context(), org, idParam(c))
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "get wallet: %v", err)
	}
	if !found {
		return zip.ErrNotFound("wallet not found")
	}
	cust, err := custodyFor(s, w.Custody)
	if err != nil {
		return custodyHTTPError(err)
	}
	address, err := cust.Rotate(c.Context(), w) // may set w.KeyRef
	if err != nil {
		return custodyHTTPError(err)
	}
	w.Address = address
	if err := s.State.store.updateWalletKey(c.Context(), org, w.ID, w.Address, w.KeyRef); err != nil {
		return zip.Errorf(http.StatusInternalServerError, "persist rotation: %v", err)
	}
	emitAudit(s, c.Context(), org, c.User(), "wallets.wallet.rotate", w.ID, map[string]any{"address": address})
	return c.JSON(http.StatusOK, w)
}

func sign(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := principal.Org(c)
	if !ok {
		return zip.ErrForbidden("sign in")
	}
	w, found, err := s.State.store.getWallet(c.Context(), org, idParam(c))
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "get wallet: %v", err)
	}
	if !found {
		return zip.ErrNotFound("wallet not found")
	}
	var body struct {
		Message string `json:"message"`
		Digest  string `json:"digest"`
	}
	if err := c.Bind(&body); err != nil {
		return err
	}
	digest, err := resolveDigest(body.Digest, body.Message)
	if err != nil {
		return zip.ErrBadRequest(err.Error())
	}
	cust, err := custodyFor(s, w.Custody)
	if err != nil {
		return custodyHTTPError(err)
	}
	sig, err := cust.Sign(c.Context(), w, digest)
	if err != nil {
		return custodyHTTPError(err)
	}
	emitAudit(s, c.Context(), org, c.User(), "wallets.wallet.sign", w.ID,
		map[string]any{"digest": "0x" + hex.EncodeToString(digest)})
	return c.JSON(http.StatusOK, map[string]any{
		"walletId":  w.ID,
		"address":   w.Address,
		"digest":    "0x" + hex.EncodeToString(digest),
		"signature": "0x" + hex.EncodeToString(sig),
	})
}

// proposeSafeTx composes the ring's Safe transaction propose + MPC-sign for a
// KindSafe wallet (POST /v1/wallets/:id/safe-tx). The custody backend must
// implement safeProposer (only safeCustody does); any other custody ⇒ 400. The
// ring computes the EIP-712 Safe-tx hash (bound to the Safe contract + chainId)
// and returns it with the threshold (r,s) its MPC produced — the owner approval.
func proposeSafeTx(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := principal.Org(c)
	if !ok {
		return zip.ErrForbidden("sign in")
	}
	w, found, err := s.State.store.getWallet(c.Context(), org, idParam(c))
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "get wallet: %v", err)
	}
	if !found {
		return zip.ErrNotFound("wallet not found")
	}
	cust, err := custodyFor(s, w.Custody)
	if err != nil {
		return custodyHTTPError(err)
	}
	proposer, ok := cust.(safeProposer)
	if !ok {
		return zip.ErrBadRequest("wallet custody " + string(w.Custody) + " does not support safe transactions")
	}
	var body struct {
		To      string `json:"to"`
		Value   string `json:"value"`
		Data    string `json:"data"`
		ChainID int64  `json:"chainId"`
		Nonce   int    `json:"nonce"`
	}
	if err := c.Bind(&body); err != nil {
		return err
	}
	res, err := proposer.ProposeTx(c.Context(), w, SafeTx{
		To: strings.TrimSpace(body.To), Value: strings.TrimSpace(body.Value),
		Data: strings.TrimSpace(body.Data), ChainID: body.ChainID, Nonce: body.Nonce,
	})
	if err != nil {
		return custodyHTTPError(err)
	}
	emitAudit(s, c.Context(), org, c.User(), "wallets.wallet.safe_tx", w.ID,
		map[string]any{"to": body.To, "chainId": body.ChainID, "safeTxHash": res.SafeTxHash})
	return c.JSON(http.StatusOK, map[string]any{
		"walletId":    w.ID,
		"safeAddress": w.Address,
		"safeTxHash":  res.SafeTxHash,
		"r":           res.R,
		"s":           res.S,
	})
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
