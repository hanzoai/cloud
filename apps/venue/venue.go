// Copyright 2025 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

// Package venue is the org-scoped "connect a cloud account" plane: an org links
// its native cloud-provider accounts (DigitalOcean / AWS / GCP), and Hanzo
// DISCOVERS the Kubernetes clusters in each account and FOLDS them into the ONE
// fleet (clients/fleet) — the same registry clients/visor surfaces at
// /v1/clusters and clients/ml federates workloads onto. There is no second
// cluster registry: discovery ends at fleet.Register, exactly where a hand-pasted
// BYO kubeconfig (visor.attachCluster) ends, so a discovered cluster appears in
// /v1/clusters and can run work like any managed or BYO cluster.
//
// Surface (subsystem "venue", prefix /v1/cloud — NOT /api/):
//
//	GET    /v1/cloud                               provider cards (what each needs)      -> {providers:[...]}
//	GET    /v1/cloud/accounts                      this org's linked cloud accounts       -> {accounts:[...]}
//	POST   /v1/cloud/:provider/accounts            link a labeled account: verify→seal→   -> {account, clusters:[...]}
//	                                               discover→fold                          (201)
//	POST   /v1/cloud/:provider/accounts/:label/sync re-discover + re-fold that account     -> {account, clusters:[...]}
//	DELETE /v1/cloud/:provider/accounts/:label     unlink: detach folded clusters +       -> {unlinked:true}
//	                                               forget the sealed credential
//
// MULTI-CREDENTIAL, PER ORG. An org may link MANY labeled accounts per provider
// (3 DO teams, 2 AWS accounts …). id = provider + label; the label is org-chosen
// (default "default"). Each credential is verified LIVE before anything is stored,
// sealed in the org's KMS namespace (/orgs/{org}/cloud/{provider}/{label}), and
// recorded in the org's account index (metadata only — the credential is never in
// the index, a response, or a log line). This is DISTINCT from the platform's own
// house DO key (clients/do, one DO_API_TOKEN for Hanzo's own VPCs/LBs): a venue
// account is the CUSTOMER's cloud account, org-scoped and isolated.
//
// TENANT ISOLATION. org is principal.Org (the ZAP-propagated, gateway-validated
// owner) — never a client field. Every KMS path, index, and fleet.Register call
// is scoped by that org, so one org can neither see, sync, nor unlink another's
// accounts, and its discovered clusters fold only into its own fleet shard. The
// fold target project is recorded per account (the caller's X-Project-Id at link
// time) so sync/unlink act on the same fleet shard deterministically.
//
// KEYLESS WHERE POSSIBLE. AWS uses cross-account role assumption (role ARN +
// external id via STS — no stored access keys); GCP prefers Workload Identity
// Federation (an external_account config, no service-account private key). Only
// DigitalOcean requires a stored secret (a PAT), sealed in KMS.
//
// EVERY ROUTE IS A TYPED OP (zip.Get/Post/Delete with concrete In/Out structs), so
// REST, the OpenAPI document, the MCP tool list and the CLI all derive from the one
// registration. Handler prose is lifted into the spec at build time by cmd/zipdoc.
package venue

//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/fleet"
	"github.com/hanzoai/cloud/apps/kms"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/zap-proto/zip"
)

const (
	// venueEnv is the KMS environment slug every venue secret is sealed under —
	// the same "default" env the integrations custody plane uses.
	venueEnv = "default"
	// credName is the KMS secret name the credential blob is sealed under, at
	// /orgs/{org}/cloud/{provider}/{label}.
	credName = "credential"
	// indexName is the KMS secret name of the per-org account index (metadata).
	indexName = "index"
	// foldClusterKind is the billing meter key. Discovered clusters are BYO
	// compute (the customer brings it), so they ride the SAME nominal management
	// fee as a hand-pasted BYO attach (clients/visor byoClusterKind), keyed off
	// the shared CLOUD_COMPUTE_FEE_CENTS — no bespoke env, one line item.
	foldClusterKind = "byo-cluster"
	// maxAccounts caps labeled accounts per (org, provider): it bounds the KMS /
	// storage amplification an org admin can create. Reconnecting an existing
	// label is always allowed (upsert), so this never dead-ends a re-sync.
	maxAccounts = 20
	// maxCredentialLen bounds a single credential field so a hostile body cannot
	// bloat a sealed blob.
	maxCredentialLen = 1 << 16
)

// providerDO / providerAWS / providerGCP are the :provider slugs.
const (
	providerDO    = "digitalocean"
	providerAWS   = "aws"
	providerGCP   = "gcp"
	providerAzure = "azure"
)

// cred is the customer's per-provider credential, parsed from the /link body and
// sealed VERBATIM in KMS. Every field here is secret-adjacent (a PAT, a shared
// external id, a WIF/SA config) and never surfaces in a response, index, or log.
type cred struct {
	// DigitalOcean: a personal access token.
	Token string `json:"token,omitempty"`
	// AWS (keyless cross-account): the role to assume + the external id that
	// pins the assumption to Hanzo (confused-deputy protection). Regions bounds
	// the eks:ListClusters sweep.
	RoleARN    string   `json:"roleArn,omitempty"`
	ExternalID string   `json:"externalId,omitempty"`
	Regions    []string `json:"regions,omitempty"`
	// GCP: a google credentials JSON — an external_account (Workload Identity
	// Federation, keyless) OR a service_account key. ProjectIDs bounds the
	// container.clusters.list sweep.
	CredentialJSON string   `json:"credentialJson,omitempty"`
	ProjectIDs     []string `json:"projectIds,omitempty"`
	// Azure: an AAD app (tenant + client). ClientSecret drives the service-principal
	// flow; its ABSENCE selects keyless Workload Identity Federation (a federated
	// OIDC assertion from Hanzo's own identity). SubscriptionIDs bounds the
	// managedClusters sweep.
	TenantID        string   `json:"tenantId,omitempty"`
	ClientID        string   `json:"clientId,omitempty"`
	ClientSecret    string   `json:"clientSecret,omitempty"`
	SubscriptionIDs []string `json:"subscriptionIds,omitempty"`
}

// identity is the non-secret account identity a driver's verify returns.
type identity struct {
	ExternalID string // DO account uuid / AWS account id / GCP project
	Display    string // DO email / AWS account / GCP project label
}

// discovered is one cluster a driver found: a foldable kubeconfig plus the
// metadata the fold + SSRF guard need. Kubeconfig is what fleet.Register consumes.
type discovered struct {
	ID         string
	Name       string
	Region     string
	Endpoint   string // the apiserver URL fleet will dial — guarded before fold
	Kubeconfig []byte
}

// driver is a cloud provider's verify + discover. Implementations MUST fail
// closed and their errors MUST NOT contain credential material (errors are logged).
type driver interface {
	id() string
	verify(ctx context.Context, cr cred) (identity, error)
	discover(ctx context.Context, cr cred) ([]discovered, error)
}

// folder is the fold sink — the ONE cluster registry. *fleet.Registry satisfies
// it in production (build wires fleet.New); tests inject a faithful fake. This is
// dependency inversion for testability, NOT a second registry: prod folds into
// clients/fleet, exactly the surface visor's /v1/clusters reads.
type folder interface {
	Register(ctx context.Context, org, project, name, kubeconfig, provider string, isDefault bool) (fleet.Cluster, error)
	Deregister(org, project, name string) (bool, error)
	List(org, project string) ([]fleet.Cluster, error)
}

// compile-time proof that the real fleet registry is the production fold sink.
var _ folder = (*fleet.Registry)(nil)

// Account is the non-secret record of a linked cloud account (index entry).
type Account struct {
	Provider   string   `json:"provider"`
	Label      string   `json:"label"`
	ExternalID string   `json:"externalId"`
	Display    string   `json:"display"`
	Project    string   `json:"project"`  // fold-target fleet shard (caller's X-Project-Id at link)
	Clusters   []string `json:"clusters"` // fold names this account folded (for sync reconcile + unlink)
	LinkedAt   string   `json:"linkedAt"`
	SyncedAt   string   `json:"syncedAt,omitempty"`
}

// state is venue's data: the KMS custody client (nil ⇒ secret ops fail closed),
// the fold sink, and the provider drivers.
type state struct {
	kms     *kms.Client
	fleet   folder
	drivers map[string]driver
}

// Mount wires /v1/cloud/* onto app.
func Mount(app cloud.Router, deps cloud.Deps) error {
	return cloud.Mount(app, deps, "venue", build, routes)
}

func build(b cloud.Base) (state, error) {
	// deps.KMS is the in-process cloud KMS. Per-secret Get/Put/Delete live on the
	// concrete client (type-asserted exactly as clients/integrations does); a
	// non-KMS impl leaves this nil and every secret op fails closed.
	kc, _ := b.KMS.(*kms.Client)
	return state{
		kms:   kc,
		fleet: fleet.New(b.Brand, b.Log.New("component", "fleet")),
		drivers: map[string]driver{
			providerDO:    doDriver{},
			providerAWS:   awsDriver{},
			providerGCP:   gcpDriver{},
			providerAzure: azureDriver{},
		},
	}, nil
}

func routes(app cloud.Router, s *cloud.Service[state]) {
	o := ops{s: s}
	z := cloud.ZipApp(app)
	// The bridge FIRST: fiber runs middleware in registration order, so one
	// installed after these leaves would never run — and every op below takes the
	// request (and the validated org it proves) off the context it parks.
	app.Group("/v1/cloud").Use(cloud.Bridge())

	// Static /v1/cloud/accounts registers before the /:provider wildcards.
	zip.Get(z, "/v1/cloud", o.listProviders)
	zip.Get(z, "/v1/cloud/accounts", o.listAccounts)
	zip.Post(z, "/v1/cloud/:provider/accounts", o.linkAccount, zip.WithStatus(http.StatusCreated))
	zip.Post(z, "/v1/cloud/:provider/accounts/:label/sync", o.syncAccount)
	zip.Delete(z, "/v1/cloud/:provider/accounts/:label", o.unlinkAccount)
}

// ops binds the service to venue's typed handlers. A TypedHandler is
// func(context.Context, *In) (*Out, error) — no parameter for the service — so it
// arrives as a RECEIVER and every op is a method value, the only bound form
// cmd/zipdoc can lift prose from.
type ops struct{ s *cloud.Service[state] }

// ── identity / validation ───────────────────────────────────────────────────

// tenant resolves the validated org every handler is scoped by. Missing identity
// is 403 (a ready-made *zip.HTTPError). It also hands back the request, which the
// billing gate, the project scope and the admin bit are read from — absent off the
// HTTP path, where there is no caller, so the op refuses.
func tenant(ctx context.Context) (string, *zip.Ctx, error) {
	c, ok := cloud.Request(ctx)
	if !ok {
		return "", nil, zip.ErrForbidden("a validated principal is required")
	}
	org, ok := principal.Org(c)
	if !ok {
		return "", nil, zip.ErrForbidden("a validated principal is required")
	}
	if !validSegment(org) {
		return "", nil, zip.ErrBadRequest("org must be a DNS-1123 label")
	}
	return org, c, nil
}

// requireAdmin gates a mutation on the caller being an admin of their OWN org
// (principal.IsOrgAdmin — NOT SuperAdmin), parity with the integrations
// AdminOnly connectors. Linking cloud infra is an org-admin action.
func requireAdmin(c *zip.Ctx) error {
	if !principal.IsOrgAdmin(c) {
		return zip.ErrForbidden("org admin required to manage cloud accounts")
	}
	return nil
}

func validSegment(s string) bool { return kms.ValidSegment(s, 253) }

// validLabel: 1–64 of [A-Za-z0-9._-] (the integrations label grammar). ':' and
// '/' are excluded so the id (provider:label) and KMS path split unambiguously.
func validLabel(l string) bool {
	if l == "" || len(l) > 64 {
		return false
	}
	for _, r := range l {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '.', r == '_', r == '-':
		default:
			return false
		}
	}
	return true
}

func labelIn(raw string) (string, error) {
	l := strings.TrimSpace(raw)
	if l == "" {
		return "default", nil
	}
	if !validLabel(l) {
		return "", zip.ErrBadRequest("label must be 1-64 of [A-Za-z0-9._-]")
	}
	return l, nil
}

func driverFor(s *cloud.Service[state], provider string) (driver, bool) {
	d, ok := s.State.drivers[strings.TrimSpace(provider)]
	return d, ok
}

// ── KMS custody ─────────────────────────────────────────────────────────────

func credPath(org, provider, label string) (string, error) {
	p := "/orgs/" + org + "/cloud/" + provider + "/" + label
	if !kms.ValidSubpath(p) {
		return "", zip.ErrBadRequest("provider and label combine into a custody path that is too long")
	}
	return p, nil
}

func indexPath(org string) string { return "/orgs/" + org + "/cloud" }

func kmsReady(s *cloud.Service[state]) bool { return s.State.kms != nil && s.State.kms.Ready() }

func kmsUnavailable() error {
	return zip.Errorf(http.StatusServiceUnavailable, "%s", kms.ErrMasterKeyMissing.Error())
}

// readIndex reads the org's account index. Absent ⇒ empty (not an error).
func readIndex(s *cloud.Service[state], org string) ([]Account, error) {
	raw, err := s.State.kms.Get(indexPath(org), indexName, venueEnv)
	if errors.Is(err, kms.ErrSecretNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 {
		return nil, nil
	}
	var list []Account
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, fmt.Errorf("corrupt cloud-account index")
	}
	return list, nil
}

func writeIndex(s *cloud.Service[state], org string, list []Account) error {
	raw, err := json.Marshal(list)
	if err != nil {
		return err
	}
	return s.State.kms.Put(indexPath(org), indexName, venueEnv, raw)
}

func upsertAccount(list []Account, a Account) []Account {
	for i := range list {
		if list[i].Provider == a.Provider && list[i].Label == a.Label {
			list[i] = a
			return list
		}
	}
	return append(list, a)
}

func findAccount(list []Account, provider, label string) (Account, bool) {
	for _, a := range list {
		if a.Provider == provider && a.Label == label {
			return a, true
		}
	}
	return Account{}, false
}

func countForProvider(list []Account, provider string) int {
	n := 0
	for _, a := range list {
		if a.Provider == provider {
			n++
		}
	}
	return n
}

// ── views ───────────────────────────────────────────────────────────────────

type accountView struct {
	Provider   string   `json:"provider"`
	Label      string   `json:"label"`
	ExternalID string   `json:"externalId"`
	Account    string   `json:"account"`
	Project    string   `json:"project,omitempty"`
	Clusters   []string `json:"clusters"`
	LinkedAt   string   `json:"linkedAt"`
	SyncedAt   string   `json:"syncedAt,omitempty"`
}

func viewOf(a Account) accountView {
	return accountView{
		Provider: a.Provider, Label: a.Label, ExternalID: a.ExternalID,
		Account: a.Display, Project: a.Project, Clusters: nonNil(a.Clusters),
		LinkedAt: a.LinkedAt, SyncedAt: a.SyncedAt,
	}
}

type clusterResult struct {
	Cluster   string `json:"cluster"` // fold name (fleet)
	Source    string `json:"source"`  // provider cluster name
	Region    string `json:"region,omitempty"`
	Folded    bool   `json:"folded"`
	Nodes     int    `json:"nodes,omitempty"`
	NvidiaGPU int    `json:"nvidiaGpu,omitempty"`
	AmdGPU    int    `json:"amdGpu,omitempty"`
	Error     string `json:"error,omitempty"`
}

type providerCard struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Keyless  bool     `json:"keyless"`
	Requires []string `json:"requires"`
}

// ── wire types ──────────────────────────────────────────────────────────────

// providerList is the connectable-provider card set.
type providerList struct {
	// Providers is one card per cloud venue can link, with the fields each needs.
	Providers []providerCard `json:"providers"`
}

// accountList is this org's linked cloud accounts, across every provider.
type accountList struct {
	// Accounts is the index (metadata only — a credential is never in a response).
	Accounts []accountView `json:"accounts"`
}

// accountRef addresses one linked account: the provider slug and the org-chosen label.
type accountRef struct {
	// Provider is the cloud slug from the path: digitalocean, aws, gcp or azure.
	Provider string `json:"provider"`
	// Label names the account within that provider; empty means "default".
	Label string `json:"label"`
}

// linkRequest is the link body: the org-chosen label plus the credential fields
// of the provider named in the path. Only that provider's fields are read.
type linkRequest struct {
	// Provider is the cloud slug from the path: digitalocean, aws, gcp or azure.
	Provider string `json:"provider"`
	// Label names this account within the provider; empty means "default".
	Label string `json:"label"`
	// Token is the DigitalOcean personal access token. DigitalOcean only.
	Token string `json:"token,omitempty"`
	// RoleARN is the AWS role Hanzo assumes cross-account (no stored keys). AWS only.
	RoleARN string `json:"roleArn,omitempty"`
	// ExternalID pins the AWS role assumption to Hanzo (confused-deputy protection).
	ExternalID string `json:"externalId,omitempty"`
	// Regions bounds the AWS eks:ListClusters sweep.
	Regions []string `json:"regions,omitempty"`
	// CredentialJSON is a Google credentials JSON — an external_account (keyless
	// Workload Identity Federation) or a service_account key. GCP only.
	CredentialJSON string `json:"credentialJson,omitempty"`
	// ProjectIDs bounds the GCP container.clusters.list sweep.
	ProjectIDs []string `json:"projectIds,omitempty"`
	// TenantID is the Azure AAD tenant. Azure only.
	TenantID string `json:"tenantId,omitempty"`
	// ClientID is the Azure AAD application. Azure only.
	ClientID string `json:"clientId,omitempty"`
	// ClientSecret selects the Azure service-principal flow; omitting it selects
	// keyless Workload Identity Federation.
	ClientSecret string `json:"clientSecret,omitempty"`
	// SubscriptionIDs bounds the Azure managedClusters sweep.
	SubscriptionIDs []string `json:"subscriptionIds,omitempty"`
}

// credential is the sealed blob this request carries — the same shape the KMS
// custody path stores, assembled from the flat body.
func (r linkRequest) credential() cred {
	return cred{
		Token: r.Token, RoleARN: r.RoleARN, ExternalID: r.ExternalID, Regions: r.Regions,
		CredentialJSON: r.CredentialJSON, ProjectIDs: r.ProjectIDs,
		TenantID: r.TenantID, ClientID: r.ClientID, ClientSecret: r.ClientSecret,
		SubscriptionIDs: r.SubscriptionIDs,
	}
}

// accountResult is a linked (or re-synced) account with the per-cluster fold outcome.
type accountResult struct {
	// Account is the account's non-secret index record.
	Account accountView `json:"account"`
	// Clusters is one row per discovered cluster: folded, or the reason it was not.
	Clusters []clusterResult `json:"clusters"`
}

// unlinkResult is the idempotent answer to an unlink.
type unlinkResult struct {
	// Unlinked is always true — unlinking an account that is not linked is a no-op.
	Unlinked bool `json:"unlinked"`
}

// ── handlers ────────────────────────────────────────────────────────────────

// listProviders returns the cloud providers an org can connect and the credential
// fields each one needs. Keyless providers take no stored secret.
func (o ops) listProviders(ctx context.Context, _ *struct{}) (*providerList, error) {
	if _, _, err := tenant(ctx); err != nil {
		return nil, err
	}
	return &providerList{Providers: []providerCard{
		{ID: providerDO, Name: "DigitalOcean", Keyless: false, Requires: []string{"token"}},
		{ID: providerAWS, Name: "AWS", Keyless: true, Requires: []string{"roleArn", "externalId", "regions"}},
		{ID: providerGCP, Name: "Google Cloud", Keyless: true, Requires: []string{"credentialJson", "projectIds"}},
		{ID: providerAzure, Name: "Azure", Keyless: true, Requires: []string{"tenantId", "clientId", "subscriptionIds"}},
	}}, nil
}

// listAccounts returns the caller org's linked cloud accounts across every
// provider — metadata only; a sealed credential never appears in a response.
func (o ops) listAccounts(ctx context.Context, _ *struct{}) (*accountList, error) {
	s := o.s
	org, _, err := tenant(ctx)
	if err != nil {
		return nil, err
	}
	if !kmsReady(s) {
		return nil, kmsUnavailable()
	}
	list, err := readIndex(s, org)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list: %v", err)
	}
	out := make([]accountView, 0, len(list))
	for _, a := range list {
		out = append(out, viewOf(a))
	}
	return &accountList{Accounts: out}, nil
}

// linkAccount links a labeled cloud account: it verifies the credential LIVE,
// seals it in the org's KMS namespace, then discovers the account's Kubernetes
// clusters and folds each into the org's fleet. Fail-closed — a credential that
// does not verify is refused and NOTHING is stored. Org admin only.
//
// Example: {"provider": "digitalocean", "label": "team-a", "token": "dop_v1_…"}
func (o ops) linkAccount(ctx context.Context, in *linkRequest) (*accountResult, error) {
	s := o.s
	org, c, err := tenant(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireAdmin(c); err != nil {
		return nil, err
	}
	d, ok := driverFor(s, in.Provider)
	if !ok {
		return nil, zip.ErrNotFound("unknown provider")
	}
	if !kmsReady(s) {
		return nil, kmsUnavailable()
	}
	cr := in.credential()
	if err := boundsOf(cr); err != nil {
		return nil, err
	}
	label, err := labelIn(in.Label)
	if err != nil {
		return nil, err
	}
	// Cap NEW labels per provider (an existing label re-links / re-seals freely).
	list, err := readIndex(s, org)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list: %v", err)
	}
	if _, exists := findAccount(list, d.id(), label); !exists && countForProvider(list, d.id()) >= maxAccounts {
		return nil, zip.ErrBadRequest("too many accounts for provider")
	}

	// Verify LIVE before sealing anything.
	ident, verr := d.verify(ctx, cr)
	if verr != nil {
		s.Log.Warn("cloud account verify failed", "provider", d.id(), "org", org, "err", verr)
		return nil, zip.ErrBadRequest("credential verification failed")
	}
	// Seal the credential before writing any row.
	path, err := credPath(org, d.id(), label)
	if err != nil {
		return nil, err
	}
	blob, err := json.Marshal(cr)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "marshal credential")
	}
	if err := s.State.kms.Put(path, credName, venueEnv, blob); err != nil {
		s.Log.Warn("cloud account seal failed", "provider", d.id(), "org", org, "err", err)
		return nil, zip.Errorf(http.StatusServiceUnavailable, "credential custody failed")
	}

	prev, _ := findAccount(list, d.id(), label)
	acct := Account{
		Provider: d.id(), Label: label, ExternalID: ident.ExternalID, Display: ident.Display,
		Project: principal.Project(c), Clusters: prev.Clusters,
		LinkedAt: firstNonEmpty(prev.LinkedAt, nowRFC3339()),
	}
	// Discover + fold. Per-cluster failures are DATA, not a link failure.
	results, acct := discoverAndFold(s, c, org, cr, d, acct)
	acct.SyncedAt = nowRFC3339()
	list = upsertAccount(list, acct)
	if err := writeIndex(s, org, list); err != nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "persist account: %v", err)
	}
	return &accountResult{Account: viewOf(acct), Clusters: results}, nil
}

// syncAccount re-discovers and re-folds an already linked account: kubeconfigs are
// refreshed, clusters deleted upstream are detached, new ones are folded.
// Idempotent. Org admin only.
//
// Example: {"provider": "digitalocean", "label": "team-a"}
func (o ops) syncAccount(ctx context.Context, in *accountRef) (*accountResult, error) {
	s := o.s
	org, c, err := tenant(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireAdmin(c); err != nil {
		return nil, err
	}
	d, ok := driverFor(s, in.Provider)
	if !ok {
		return nil, zip.ErrNotFound("unknown provider")
	}
	label, err := labelIn(in.Label)
	if err != nil {
		return nil, err
	}
	if !kmsReady(s) {
		return nil, kmsUnavailable()
	}
	list, err := readIndex(s, org)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list: %v", err)
	}
	acct, found := findAccount(list, d.id(), label)
	if !found {
		return nil, zip.ErrNotFound("cloud account not linked")
	}
	cr, err := loadCred(s, org, d.id(), label)
	if err != nil {
		return nil, err
	}
	results, acct := discoverAndFold(s, c, org, cr, d, acct)
	acct.SyncedAt = nowRFC3339()
	list = upsertAccount(list, acct)
	if err := writeIndex(s, org, list); err != nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "persist account: %v", err)
	}
	return &accountResult{Account: viewOf(acct), Clusters: results}, nil
}

// unlinkAccount detaches every cluster the account folded, forgets the sealed
// credential and drops the index row. Idempotent — unlinking an account that is
// not linked succeeds. Org admin only.
//
// Example: {"provider": "digitalocean", "label": "team-a"}
func (o ops) unlinkAccount(ctx context.Context, in *accountRef) (*unlinkResult, error) {
	s := o.s
	org, c, err := tenant(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireAdmin(c); err != nil {
		return nil, err
	}
	d, ok := driverFor(s, in.Provider)
	if !ok {
		return nil, zip.ErrNotFound("unknown provider")
	}
	label, err := labelIn(in.Label)
	if err != nil {
		return nil, err
	}
	if !kmsReady(s) {
		return nil, kmsUnavailable()
	}
	list, err := readIndex(s, org)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list: %v", err)
	}
	acct, found := findAccount(list, d.id(), label)
	if !found {
		return &unlinkResult{Unlinked: true}, nil
	}
	// Detach the clusters this account folded (only its own fold names, in its
	// recorded fleet shard) — reverses the fold.
	for _, name := range acct.Clusters {
		if _, derr := s.State.fleet.Deregister(org, acct.Project, name); derr != nil {
			s.Log.Warn("cloud account detach cluster failed (continuing)", "provider", d.id(), "org", org, "cluster", name, "err", derr)
		}
	}
	if path, perr := credPath(org, d.id(), label); perr == nil {
		if derr := s.State.kms.Delete(path, credName, venueEnv); derr != nil && !errors.Is(derr, kms.ErrSecretNotFound) {
			s.Log.Warn("cloud account cred delete failed (continuing)", "provider", d.id(), "org", org, "err", derr)
		}
	}
	out := list[:0]
	for _, a := range list {
		if a.Provider == d.id() && a.Label == label {
			continue
		}
		out = append(out, a)
	}
	if err := writeIndex(s, org, out); err != nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "persist account: %v", err)
	}
	return &unlinkResult{Unlinked: true}, nil
}

// ── discovery + fold ────────────────────────────────────────────────────────

// loadCred opens the sealed credential blob for (org, provider, label).
func loadCred(s *cloud.Service[state], org, provider, label string) (cred, error) {
	path, err := credPath(org, provider, label)
	if err != nil {
		return cred{}, err
	}
	raw, err := s.State.kms.Get(path, credName, venueEnv)
	if errors.Is(err, kms.ErrSecretNotFound) {
		return cred{}, zip.ErrNotFound("cloud account credential missing")
	}
	if err != nil {
		return cred{}, zip.Errorf(http.StatusServiceUnavailable, "credential unavailable")
	}
	var cr cred
	if err := json.Unmarshal(raw, &cr); err != nil {
		return cred{}, zip.Errorf(http.StatusInternalServerError, "corrupt credential")
	}
	return cr, nil
}

// discoverAndFold lists the account's clusters and folds each into the fleet.
// It returns the per-cluster results (folded vs error — all secret-free) and the
// account with its fold set reconciled: clusters that vanished from the account
// are detached, survivors are refreshed (re-registered with a fresh kubeconfig),
// new ones are billed + folded. A discovery error yields an empty result set and
// leaves the existing fold set untouched (never a silent mass-detach).
func discoverAndFold(s *cloud.Service[state], c *zip.Ctx, org string, cr cred, d driver, acct Account) ([]clusterResult, Account) {
	project := acct.Project
	discs, err := d.discover(c.Context(), cr)
	if err != nil {
		s.Log.Warn("cloud account discovery failed", "provider", d.id(), "org", org, "err", err)
		return []clusterResult{}, acct
	}
	prev := map[string]bool{}
	for _, n := range acct.Clusters {
		prev[n] = true
	}
	results := make([]clusterResult, 0, len(discs))
	kept := map[string]bool{}
	var foldNames []string
	fee := cloud.ResourceFeeCents("CLOUD_COMPUTE_FEE_CENTS", foldClusterKind)
	_, projectValidated := principal.ValidatedProject(c)

	for _, dc := range discs {
		name := foldName(d.id(), acct.Label, dc.Name, dc.ID)
		res := clusterResult{Cluster: name, Source: dc.Name, Region: dc.Region}
		// Fold safety (exec-plugin rejection + SSRF guard on the ACTUAL apiserver
		// dial target) is enforced inside fleet.Register/SafeRESTConfig — the ONE
		// gate every attach path shares; a rejected kubeconfig surfaces as this
		// cluster's fold error, not a whole-account failure.
		// Bill NEW folds fail-closed (an existing fold refreshes free); the fee
		// keys on the HOME (paying) org, the fold on the operating org.
		if !prev[name] {
			if berr := s.Bill.Gate(c.Context(), principal.Ledger(c), principal.Project(c), projectValidated, foldClusterKind, fee); berr != nil {
				res.Error = "billing gate denied"
				results = append(results, res)
				continue
			}
		}
		rec, ferr := s.State.fleet.Register(c.Context(), org, project, name, string(dc.Kubeconfig), d.id(), false)
		if ferr != nil {
			res.Error = foldError(ferr)
			results = append(results, res)
			continue
		}
		if !prev[name] {
			s.Bill.Meter(principal.Ledger(c), principal.Project(c), foldClusterKind, fee, c.RequestID(), cloud.ClientIP(c))
		}
		res.Folded = true
		res.Nodes = rec.Nodes
		res.NvidiaGPU = rec.NvidiaGPU
		res.AmdGPU = rec.AmdGPU
		results = append(results, res)
		kept[name] = true
		foldNames = append(foldNames, name)
	}
	// Reconcile: detach fold names this account owned that discovery no longer
	// returned (cluster deleted upstream). Only this account's own names.
	for _, name := range acct.Clusters {
		if !kept[name] {
			if _, derr := s.State.fleet.Deregister(org, project, name); derr != nil {
				s.Log.Warn("cloud account reconcile detach failed (continuing)", "provider", d.id(), "org", org, "cluster", name, "err", derr)
			}
		}
	}
	sort.Strings(foldNames)
	acct.Clusters = foldNames
	return results, acct
}

// foldName is the STABLE, collision-free fleet name for a discovered cluster:
// <provider>-<label>-<cluster>-<id6>. (provider,label) is unique per org and the
// id6 suffix (sha256 of the provider cluster id) guarantees uniqueness even when
// two clusters share a name. Sanitized to a DNS-ish KMS-path-safe segment.
func foldName(provider, label, cluster, id string) string {
	base := sanitize(provider) + "-" + sanitize(label) + "-" + sanitize(cluster)
	if len(base) > 200 {
		base = base[:200]
	}
	sum := sha256.Sum256([]byte(provider + "\x00" + id))
	return base + "-" + hex.EncodeToString(sum[:])[:6]
}

// sanitize lowercases and maps every non-[a-z0-9-] rune to '-', collapsing runs
// and trimming, so the result is a safe KMS path / DNS-ish segment.
func sanitize(s string) string {
	var b strings.Builder
	prevDash := false
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			prevDash = false
			continue
		}
		if !prevDash {
			b.WriteByte('-')
			prevDash = true
		}
	}
	return strings.Trim(b.String(), "-")
}

// foldError keeps fleet.Register's error client-safe: fleet errors already
// describe reachability/KMS state without secrets, but we cap and generalize.
func foldError(err error) string {
	msg := err.Error()
	if len(msg) > 200 {
		msg = msg[:200]
	}
	return msg
}

// boundsOf caps each credential field so a hostile body can't bloat a sealed blob.
func boundsOf(cr cred) error {
	for _, v := range []string{cr.Token, cr.RoleARN, cr.ExternalID, cr.CredentialJSON, cr.ClientSecret} {
		if len(v) > maxCredentialLen {
			return zip.ErrBadRequest("credential field too large")
		}
	}
	return nil
}

// ── small helpers ───────────────────────────────────────────────────────────

func nowRFC3339() string { return time.Now().UTC().Format(time.RFC3339) }

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
