// Package kms embeds luxfi/kms in-process inside the unified Hanzo Cloud binary
// per HIP-0106 ("all Go embeds in cloud"), replacing the legacy Infisical fork.
//
// It has two faces, both backed by the SAME embedded luxfi/kms library:
//
//	KMSClient  — the in-process cloud.KMSClient (GetSecret/PutSecret/Sign) other
//	             subsystems call via deps.KMS. No RPC, no external DB. Built once by
//	             the factory this package registers (init, mount.go), filled into
//	             deps.KMS by build.go's BuildDeps before MountAll, and reused by Mount.
//	/v1/kms/*  — the secrets-manager REST surface the KMS console (kms.hanzo.ai)
//	             calls, mounted onto cloud's Fiber app: JWT-gated, org-scoped
//	             secrets CRUD + a real health probe + the SPA admin config (mount.go).
//
// STORAGE — sealed secrets persist to PER-ORG SQLite (store.go): each org's
// secrets live in ITS OWN encrypted file {CLOUD_DATA_DIR}/orgs/{org}/kms.db via
// the canonical cloud.OrgDB → cek seam, mirroring clients/finance. This REPLACES
// the single embedded ZapDB KV, whose exclusive OS lock pinned cloud to
// replicas=1: a per-org SQLite file has no single-opener lock, so different pods
// can serve different tenants and cloud scales horizontally (consistent-hash
// org→pod; see the package report). Two layers of at-rest protection, both rooted
// in the SAME env-only master key: (1) each secret is sealed with an AES-256-GCM
// envelope (store.Seal: a fresh per-secret DEK wrapped by the master key) BEFORE
// it reaches SQLite — plaintext never touches disk; (2) cek opens each file
// SQLCipher-encrypted under a per-db DEK wrapped by the master key (defense in
// depth). No PostgreSQL, no external DB, no ZapDB.
//
// BOOTSTRAP — cloud hosting the secret store is a chicken-and-egg: cloud cannot
// fetch its OWN master key from the KMS it hosts. The 32-byte master key is
// injected by the operator via a K8s Secret env (CLOUD_KMS_MASTER_KEY_REF,
// base64 of 32 bytes) and read ONLY from env — never from the store, never
// logged, never persisted in plaintext. When the master key is absent the
// subsystem mounts in a fail-closed HEALTH-ONLY mode: /v1/kms/health reports 503,
// every secret op returns a clear "master key not configured" error, and the
// store is backed by an EPHEMERAL in-memory KV (never an unencrypted on-disk one),
// so a later keyed boot opens a clean encrypted store rather than a bricked one.
// Never a silent insecure default.
//
// SIGN — luxfi/kms's Sign is MPC-backed (threshold signing via the MPC daemon).
// cloud does not co-host the MPC cluster; Sign therefore fails CLOSED with a
// clear error whenever the MPC backend is not configured (CLOUD_KMS_MPC_ADDR /
// CLOUD_KMS_MPC_VAULT_ID unset). A signature is NEVER fabricated.
//
// SECURITY — the REST surface (mount.go) reuses cloud's ONE auth boundary
// (SanitizeIdentity in serve.go establishes the validated principal; handlers
// read c.Org()/c.IsAdmin()) rather than a parallel JWT stack.
//
// LAYERING — the library face (Client/New here in kms.go) is the cloud-free CLIENT
// core: the types.KMSClient impl + sealed store access, importing only cloud/types.
// The REST face (mount.go) imports cloud to mount /v1/kms/* and register the
// subsystem. build.go does NOT import this package; it receives the embedded-client
// constructor via cloud.RegisterKMSClientFactory (init, mount.go), so deps.KMS is
// built by BuildDeps before MountAll with no cloud⇄kms import cycle — the same
// inversion cloud already uses to mount every subsystem it never imports.
package kms

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/hanzoai/cloud/types"
	kmsstore "github.com/luxfi/kms/pkg/store"
	luxlog "github.com/luxfi/log"
)

// masterKeyLen is the AES-256 KEK size store.Seal/Open require (32 bytes).
const masterKeyLen = 32

// defaultEnv is the secret environment slug used when a request omits ?env=,
// matching luxfi/kms's REST default. Secrets are namespaced (path, name, env).
const defaultEnv = "default"

// ErrMasterKeyMissing is the fail-closed error every secret op returns when no
// master key is configured. It is honest (mirrors the DisabledKMS pattern): the
// caller knows the operator must inject CLOUD_KMS_MASTER_KEY_REF.
var ErrMasterKeyMissing = errors.New("kms: master key not configured (operator must inject CLOUD_KMS_MASTER_KEY_REF)")

// ErrSignUnavailable is the fail-closed error Sign returns when the MPC backend
// is not configured. Signing is threshold-MPC-backed; cloud never fabricates a
// signature.
var ErrSignUnavailable = errors.New("kms: signing unavailable — MPC backend not configured (set CLOUD_KMS_MPC_ADDR and CLOUD_KMS_MPC_VAULT_ID)")

// ErrInvalidKey is returned when a secret coordinate (name/env) contains a byte
// that would smuggle structure into the store key (a '/', NUL, or control char)
// or is out of length bounds. Enforced in ONE place — the store-access methods —
// so every entry point (the HTTP subsystem AND the in-process KMSClient facade)
// keys clean, unambiguous records.
var ErrInvalidKey = errors.New("kms: invalid secret coordinate (name/env must be non-empty, within length bounds, and contain no '/', NUL, or control characters)")

// Store-key component bounds. The store keys are opaque byte strings, so a '/'
// in a name is not filesystem traversal — but forbidding separators + control
// chars keeps one key = one secret and closes the door on any future backend
// that treats '/' structurally.
const (
	maxNameLen    = 253
	maxEnvLen     = 63
	maxSubpathLen = 253
)

// Client is the in-process cloud.KMSClient backed by the embedded luxfi/kms
// SecretStore. GetSecret/PutSecret seal/open through the AES-256-GCM envelope;
// Sign fails closed (MPC is never co-hosted here).
//
// The zero value is not usable; construct with New.
type Client struct {
	store     *secretStore // per-org SQLite persistence (store.go); no OS lock
	masterKey []byte       // 32-byte KEK; nil ⇒ health-only fail-closed mode
	mpcAddr   string       // MPC daemon address; "" ⇒ Sign fails closed
	vaultID   string       // MPC vault id; "" ⇒ Sign fails closed
	log       luxlog.Logger
}

var _ types.KMSClient = (*Client)(nil)

// Config is the embedded KMS configuration resolved from cloud.Config + env by
// New. All fields are optional: an empty MasterKeyB64 yields the fail-closed
// health-only mode; empty MPC fields make Sign fail closed.
type Config struct {
	DataDir      string // CLOUD_DATA_DIR; the store opens under {DataDir}/kms
	MasterKeyB64 string // base64 of the 32-byte master key (CLOUD_KMS_MASTER_KEY_REF)
	MPCAddr      string // MPC daemon host:port(,...) — CLOUD_KMS_MPC_ADDR
	MPCVaultID   string // MPC vault id — CLOUD_KMS_MPC_VAULT_ID

	// ReadOnly opens the store in reader mode: mutations fail closed so a replica
	// never forks the authoritative writer's state, and a reader with no restored
	// store under {DataDir}/orgs fails closed at New rather than serving nothing.
	// Set by the reader HA role. Unlike the former ZapDB store, per-org SQLite is
	// RO-shareable over WAL, so a reader CAN open the files locally — the reader
	// no longer needs to reverse-proxy KMS to the writer (see the package report).
	ReadOnly bool
}

// New opens the embedded KMS store under cfg.DataDir and returns the in-process
// Client. A malformed or missing master key is NOT fatal: the store still opens
// (so health can report + list metadata) but the Client runs in health-only mode
// where every secret op fails closed with ErrMasterKeyMissing. A bad DataDir /
// store-open failure IS fatal (the subsystem cannot serve at all).
//
// The master key is decoded from base64, validated to be exactly 32 bytes, and
// held in memory only. It is never logged and never written to the store.
func New(cfg Config, log luxlog.Logger) (*Client, error) {
	if log == nil {
		return nil, fmt.Errorf("kms.New: nil logger")
	}
	dir := strings.TrimSpace(cfg.DataDir)
	if dir == "" {
		return nil, fmt.Errorf("kms.New: empty DataDir")
	}

	masterKey, keyErr := decodeMasterKey(cfg.MasterKeyB64)

	// Fail-SECURE across the health-only↔keyed transition. There is no single store
	// to open here: per-org files open LAZILY on first access (store.go), each via
	// cloud.OrgDB → cek. So the health-only↔keyed distinction is purely the presence
	// of a valid master key (Ready()): with a key, secret ops seal/open and cek
	// encrypts each file at rest; without one, every secret op fails closed with
	// ErrMasterKeyMissing and NO org file is created (nothing to persist, nothing to
	// brick). The former ZapDB KEYREGISTRY brick foot-gun is gone: cek rotates by
	// re-wrapping a per-file sidecar (no page is rewritten), and a wrong/absent key
	// makes cek.Open FAIL at first access — never a silent plaintext downgrade.
	//
	// Reader HA role fails CLOSED at New (not at first read) so a mis-provisioned
	// reader never boots "healthy" over nothing:
	//   no master key → cannot decrypt any file at rest → refuse.
	//   no restored store under {DataDir}/orgs → nothing to serve → refuse.
	if cfg.ReadOnly {
		if keyErr != nil {
			return nil, fmt.Errorf("kms.New: reader mode requires a master key to open the encrypted store: %w", keyErr)
		}
		if !hasRestoredStore(dir) {
			return nil, fmt.Errorf("kms.New: reader mode but no restored store under %s — hydrate before serving KMS reads", filepath.Join(dir, "orgs"))
		}
	}

	c := &Client{
		store:     newSecretStore(dir, cfg.ReadOnly),
		masterKey: masterKey, // nil when keyErr != nil
		mpcAddr:   strings.TrimSpace(cfg.MPCAddr),
		vaultID:   strings.TrimSpace(cfg.MPCVaultID),
		log:       log.New("subsystem", "kms"),
	}
	if keyErr != nil {
		c.log.Warn("kms master key not configured; secret ops fail closed (health-only mode)", "err", keyErr)
	}
	// One-time cutover from the legacy embedded ZapDB store to per-org SQLite. Writer
	// + keyed only: a reader must not migrate, and the encrypted-at-rest legacy store
	// can only be read with the master key. FATAL on error — booting the empty per-org
	// store while legacy secrets sit unmigrated would orphan every secret (cloud KMS is
	// the source the kms-operator syncs out), so refuse to serve rather than lose them.
	if !cfg.ReadOnly && keyErr == nil {
		if err := migrateLegacyZapDB(dir, masterKey, c.store, c.log); err != nil {
			return nil, fmt.Errorf("kms.New: legacy ZapDB migration: %w", err)
		}
	}
	return c, nil
}

// decodeMasterKey decodes and validates the base64 master key. A key that is
// absent, unparseable, or the wrong length yields (nil, err) so the caller runs
// health-only. The raw key bytes are never logged.
func decodeMasterKey(b64 string) ([]byte, error) {
	b64 = strings.TrimSpace(b64)
	if b64 == "" {
		return nil, ErrMasterKeyMissing
	}
	key, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, fmt.Errorf("kms: master key is not valid base64: %w", err)
	}
	if len(key) != masterKeyLen {
		return nil, fmt.Errorf("kms: master key must decode to %d bytes, got %d", masterKeyLen, len(key))
	}
	return key, nil
}

// Ready reports whether the Client can perform secret ops (a valid master key is
// configured). Used to fail closed uniformly across the KMSClient + REST paths,
// and surfaced to build.go for the boot log.
func (c *Client) Ready() bool { return c != nil && len(c.masterKey) == masterKeyLen }

// SigningConfigured reports whether an MPC backend is wired. Sign fails closed
// when false — no signature is fabricated.
func (c *Client) SigningConfigured() bool { return c != nil && c.mpcAddr != "" && c.vaultID != "" }

// ── cloud.KMSClient ──────────────────────────────────────────────────────────

// GetSecret resolves a flat ref to (path, name, env), reads the sealed record
// from the store, and returns the AES-256-GCM-opened plaintext. Fails closed
// with ErrMasterKeyMissing when no master key is configured.
//
// ref grammar (see parseRef): "name" | "path/name" | "path/name@env". A bare
// name resolves to (path="/", name, env="default").
func (c *Client) GetSecret(ctx context.Context, ref string) ([]byte, error) {
	path, name, env := parseRef(ref)
	return c.Get(path, name, env)
}

// PutSecret seals value under a fresh per-secret DEK (wrapped by the master key)
// and upserts it into the store. Plaintext is never persisted. Fails closed with
// ErrMasterKeyMissing when no master key is configured.
func (c *Client) PutSecret(ctx context.Context, ref string, value []byte) error {
	path, name, env := parseRef(ref)
	return c.Put(path, name, env, value)
}

// Sign is threshold-MPC-backed in luxfi/kms and cloud does not co-host the MPC
// cluster, so it fails closed with ErrSignUnavailable unless an MPC backend is
// explicitly configured. It NEVER returns a fabricated signature.
//
// When an MPC backend IS configured the caller should route signing to the
// dedicated MPC/keys deployment (deps.KMS ZAP RPC); in-process co-hosting of the
// MPC signer is intentionally out of scope for the application-tier binary.
func (c *Client) Sign(ctx context.Context, keyRef string, payload []byte) ([]byte, error) {
	if !c.SigningConfigured() {
		return nil, ErrSignUnavailable
	}
	// An MPC backend is configured but co-hosting the threshold signer inside the
	// application binary is out of scope (the MPC cluster is its own trust/scaling
	// tier). Fail closed loudly rather than fabricate — the operator wires the KMS
	// ZAP RPC endpoint (CLOUD_KMS_ZAP_ADDR) for signing instead.
	return nil, fmt.Errorf("%w: in-process MPC signing is not co-hosted; route signing via CLOUD_KMS_ZAP_ADDR", ErrSignUnavailable)
}

// ── sealed store access (the ONE place seal/open lives) ──────────────────────
//
// The REST subsystem (clients/kms) and the KMSClient facade both go through
// these four methods with explicit (path, name, env) coordinates, so the
// AES-256-GCM envelope is applied in exactly one place. ErrSecretNotFound is
// returned verbatim so callers can map it to a 404.

// SecretMeta is a secret's non-sensitive descriptor (never any ciphertext or
// plaintext), returned by List for the console's secret browser.
type SecretMeta struct {
	Name   string `json:"name"`
	Path   string `json:"path"`
	Env    string `json:"env"`
	Scheme string `json:"scheme"`
}

// Get reads a sealed secret at (path, name, env) and returns the opened
// plaintext. Fails closed with ErrMasterKeyMissing when no master key is set.
func (c *Client) Get(path, name, env string) ([]byte, error) {
	if !c.Ready() {
		return nil, ErrMasterKeyMissing
	}
	if err := validCoords(path, name, env); err != nil {
		return nil, err
	}
	sec, err := c.store.get(path, name, env)
	if err != nil {
		if errors.Is(err, kmsstore.ErrSecretNotFound) {
			return nil, kmsstore.ErrSecretNotFound
		}
		return nil, fmt.Errorf("kms: read secret: %w", err)
	}
	pt, err := kmsstore.Open(c.masterKey, sec)
	if err != nil {
		return nil, fmt.Errorf("kms: open secret: %w", err)
	}
	return pt, nil
}

// Put seals value under a fresh per-secret DEK (wrapped by the master key) and
// upserts it. Plaintext is sealed before it reaches the store — never stored raw.
// Fails closed with ErrMasterKeyMissing when no master key is set.
func (c *Client) Put(path, name, env string, value []byte) error {
	if !c.Ready() {
		return ErrMasterKeyMissing
	}
	if err := validCoords(path, name, env); err != nil {
		return err
	}
	sec, err := kmsstore.Seal(c.masterKey, path, name, env, value)
	if err != nil {
		return fmt.Errorf("kms: seal secret: %w", err)
	}
	if err := c.store.put(sec); err != nil {
		return fmt.Errorf("kms: write secret: %w", err)
	}
	return nil
}

// List returns the metadata (never ciphertext/plaintext) of the secrets at a
// path/env. It does not require the master key: nothing sensitive is decrypted.
func (c *Client) List(path, env string) ([]SecretMeta, error) {
	if !ValidSegment(env, maxEnvLen) {
		return nil, ErrInvalidKey
	}
	secs, err := c.store.list(path, env)
	if err != nil {
		return nil, fmt.Errorf("kms: list secrets: %w", err)
	}
	out := make([]SecretMeta, 0, len(secs))
	for _, s := range secs {
		out = append(out, SecretMeta{Name: s.Name, Path: s.Path, Env: s.Env, Scheme: s.Scheme})
	}
	return out, nil
}

// Delete removes a secret. Returns ErrSecretNotFound verbatim for a 404 mapping.
func (c *Client) Delete(path, name, env string) error {
	if err := validCoords(path, name, env); err != nil {
		return err
	}
	if err := c.store.del(path, name, env); err != nil {
		if errors.Is(err, kmsstore.ErrSecretNotFound) {
			return kmsstore.ErrSecretNotFound
		}
		return fmt.Errorf("kms: delete secret: %w", err)
	}
	return nil
}

// ErrSecretNotFound is re-exported so the REST subsystem can map a missing
// secret to a 404 without importing luxfi/kms's store package directly.
var ErrSecretNotFound = kmsstore.ErrSecretNotFound

// ── key-shape validation (ONE place, both faces) ─────────────────────────────

// validCoords is the single boundary guard applied by every store-access method
// (Get/Put/Delete and, for env, List), so the HTTP subsystem AND the in-process
// KMSClient facade (which reaches the store via parseRef) enforce identically —
// no entry point can key a malformed record. path is a '/'-separated subpath
// (validated as a whole); name and env are single segments.
func validCoords(path, name, env string) error {
	if !ValidSegment(name, maxNameLen) || !ValidSegment(env, maxEnvLen) || !ValidSubpath(path) {
		return ErrInvalidKey
	}
	return nil
}

// ValidSegment reports whether s is a valid single store-key segment (name or
// env): non-empty, within max bytes, and free of '/', NUL, and ASCII control
// characters. Exported so the HTTP subsystem reuses the exact same rule.
func ValidSegment(s string, max int) bool {
	if s == "" || len(s) > max {
		return false
	}
	for _, r := range s {
		if r == '/' || r == 0 || r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

// ValidSubpath reports whether p is a valid store subpath: '/'-separated
// non-empty segments, none of which is "." or ".." or contains a control char,
// within the length bound. An empty/"/"-only path is valid (the org/collection
// root). Exported so the HTTP subsystem reuses the exact same rule.
func ValidSubpath(p string) bool {
	if len(p) > maxSubpathLen {
		return false
	}
	p = strings.Trim(p, "/")
	if p == "" {
		return true
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return false
		}
		for _, r := range seg {
			if r == 0 || r < 0x20 || r == 0x7f {
				return false
			}
		}
	}
	return true
}

// MaxSegmentLens exposes the name/env bounds so the HTTP subsystem can produce
// specific 400 messages using the same limits the store methods enforce.
const (
	MaxNameLen = maxNameLen
	MaxEnvLen  = maxEnvLen
)

// Close releases the embedded store. Safe to call once at shutdown.
func (c *Client) Close() error {
	if c == nil || c.store == nil {
		return nil
	}
	return c.store.close()
}

// ── ref parsing ──────────────────────────────────────────────────────────────

// parseRef maps a flat KMSClient ref to the store's (path, name, env) coordinate.
//
//	"DATABASE_URL"              → ("/",        "DATABASE_URL",   "default")
//	"myservice/DATABASE_URL"    → ("/myservice","DATABASE_URL",  "default")
//	"myservice/DB@main"         → ("/myservice","DB",            "main")
//
// The path is normalized to a leading "/" (the store keys on it verbatim, so a
// stable normalization keeps refs and REST paths addressing the same record).
func parseRef(ref string) (path, name, env string) {
	ref = strings.TrimSpace(ref)
	env = defaultEnv
	if at := strings.LastIndex(ref, "@"); at >= 0 {
		if e := strings.TrimSpace(ref[at+1:]); e != "" {
			env = e
		}
		ref = ref[:at]
	}
	if slash := strings.LastIndex(ref, "/"); slash >= 0 {
		name = ref[slash+1:]
		path = normalizePath(ref[:slash])
	} else {
		name = ref
		path = "/"
	}
	return path, name, env
}

// normalizePath ensures a single leading slash and no trailing slash, so
// "myservice", "/myservice/", "//myservice" all key the same record.
func normalizePath(p string) string {
	p = strings.Trim(strings.TrimSpace(p), "/")
	if p == "" {
		return "/"
	}
	return "/" + p
}
