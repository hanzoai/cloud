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
package venue

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

// zipdoc lifts the doc comment off each typed op and its In/Out fields into
// zipdoc_gen.go, which is the ONLY way that prose reaches the published document
// and the MCP tool list — Go drops comments at compile time. Run by
// `make -C apps/venue openapi` and by the Dockerfile before every build.
//
//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

// ops binds the mounted Service so each op can be a method value — the only bound
// form cmd/zipdoc can lift prose from. It carries STATE and no logic: every op
// resolves its own tenant and then calls the same helpers below.
type ops struct{ s *cloud.Service[state] }

func routes(app cloud.Router, s *cloud.Service[state]) {
	// Bridge FIRST: a typed op receives only a context, so the validated org — and,
	// for the three writes, the request their ORG-ADMIN claim and their billing
	// attribution ride on — reach it by being parked there. fiber runs middleware in
	// registration order, so one installed after its leaves never runs. Installed
	// through the scope's Use, once per declared prefix, which is /v1/cloud: this
	// subsystem is named "venue" and serves /v1/cloud, so the /v1/<name> default
	// would have covered nothing it registers.
	app.Use(cloud.Bridge())

	o := ops{s: s}
	// Static /v1/cloud/accounts registers before the /:provider wildcards.
	z := cloud.ZipApp(app)
	zip.Get(z, "/v1/cloud", o.listProviders)
	zip.Get(z, "/v1/cloud/accounts", o.listAccounts)
	zip.Post(z, "/v1/cloud/:provider/accounts", o.linkAccount, zip.WithStatus(http.StatusCreated))
	zip.Post(z, "/v1/cloud/:provider/accounts/:label/sync", o.syncAccount)
	zip.Delete(z, "/v1/cloud/:provider/accounts/:label", o.unlinkAccount)
}

// ── identity / validation ───────────────────────────────────────────────────

// tenant resolves the validated org every op is scoped by, from the org Bridge
// parked — never from an In field, which is caller-supplied. Missing identity is
// 403 (a ready-made *zip.HTTPError); off the HTTP path there is no principal, so
// every op refuses.
func tenant(ctx context.Context) (string, error) {
	org, ok := principal.OrgFrom(ctx)
	if !ok {
		return "", zip.ErrForbidden("a validated principal is required")
	}
	if !validSegment(org) {
		return "", zip.ErrBadRequest("org must be a DNS-1123 label")
	}
	return org, nil
}

// writer gates a mutation on the caller being an admin of their OWN org
// (principal.IsOrgAdmin — NOT SuperAdmin), parity with the integrations AdminOnly
// connectors: linking cloud infra is an org-admin action. It returns the REQUEST,
// which is the ONE place this package reaches one, because the three writes need
// more of the validated principal than the tenant — the org-admin claim
// (X-User-IsOrgAdmin), the project the fold is recorded in and whether that project
// was validated, the ledger the fold is billed to, the request id and the client
// IP the meter attributes it by. principal.OrgFrom carries none of those.
//
// Fails closed off the HTTP path: no request, no attested admin, no mutation.
func writer(ctx context.Context) (*zip.Ctx, error) {
	c, ok := cloud.Request(ctx)
	if !ok || !principal.IsOrgAdmin(c) {
		return nil, zip.ErrForbidden("org admin required to manage cloud accounts")
	}
	return c, nil
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

func (o ops) driverFor(provider string) (driver, bool) {
	d, ok := o.s.State.drivers[strings.TrimSpace(provider)]
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

// listAccounts reads the org's account index. Absent ⇒ empty (not an error).
func listAccounts(s *cloud.Service[state], org string) ([]Account, error) {
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

// cloudAccountView is one linked cloud account as an operator sees it. It carries only
// metadata: the sealed credential is never in a response, an index, or a log line.
type cloudAccountView struct {
	// Provider is the cloud the account belongs to: digitalocean, aws, gcp or
	// azure.
	Provider string `json:"provider"`
	// Label is the org-chosen name for this account within the provider, which is
	// how a second account at the same provider is addressed. Defaults to
	// "default".
	Label string `json:"label"`
	// ExternalID is the provider's own identifier for the account — a
	// DigitalOcean account uuid, an AWS account id, a GCP project.
	ExternalID string `json:"externalId"`
	// Account is the provider's human label for it, e.g. the DigitalOcean team
	// email.
	Account string `json:"account"`
	// Project is the fleet shard the account's clusters were folded into,
	// recorded at link time so a later sync or unlink acts on the same shard.
	Project string `json:"project,omitempty"`
	// Clusters is the fleet names this account currently owns. Unlinking detaches
	// exactly these and nothing else.
	Clusters []string `json:"clusters"`
	// LinkedAt is when the account was first linked, RFC3339 UTC. Re-linking the
	// same label keeps the original.
	LinkedAt string `json:"linkedAt"`
	// SyncedAt is when it was last discovered, RFC3339 UTC.
	SyncedAt string `json:"syncedAt,omitempty"`
}

func viewOf(a Account) cloudAccountView {
	return cloudAccountView{
		Provider: a.Provider, Label: a.Label, ExternalID: a.ExternalID,
		Account: a.Display, Project: a.Project, Clusters: nonNil(a.Clusters),
		LinkedAt: a.LinkedAt, SyncedAt: a.SyncedAt,
	}
}

// clusterResult is what happened to ONE discovered cluster. A per-cluster failure
// is DATA, not a failed link: the account still links and the other clusters still
// fold.
type clusterResult struct {
	// Cluster is the stable fleet name the cluster was folded under — the name
	// /v1/clusters shows and a workload targets.
	Cluster string `json:"cluster"`
	// Source is the cluster's own name at the provider.
	Source string `json:"source"`
	// Region is the provider region it runs in.
	Region string `json:"region,omitempty"`
	// Folded is whether it reached the fleet. False means Error says why, and
	// this cluster alone was skipped.
	Folded bool `json:"folded"`
	// Nodes is how many nodes the fleet counted in it.
	Nodes int `json:"nodes,omitempty"`
	// NvidiaGPU is how many NVIDIA GPUs those nodes advertise.
	NvidiaGPU int `json:"nvidiaGpu,omitempty"`
	// AmdGPU is how many AMD GPUs those nodes advertise.
	AmdGPU int `json:"amdGpu,omitempty"`
	// Error is why this cluster did not fold — a billing denial, an unsafe
	// kubeconfig, or an unreachable apiserver. It never contains credential
	// material.
	Error string `json:"error,omitempty"`
}

// providerCard is one connectable cloud and what linking it needs.
type providerCard struct {
	// ID is the provider slug used in the path: digitalocean, aws, gcp, azure.
	ID string `json:"id"`
	// Name is the provider's display name.
	Name string `json:"name"`
	// Keyless is whether the provider can be linked WITHOUT storing a long-lived
	// secret — AWS by role assumption, GCP by workload identity federation, Azure
	// by federated credential. DigitalOcean is not: it needs a stored token.
	Keyless bool `json:"keyless"`
	// Requires names the credential fields a link body must carry for this
	// provider.
	Requires []string `json:"requires"`
}

// providersView is the connect-a-cloud catalog.
type providersView struct {
	// Providers is every cloud this deployment can link, with what each needs.
	Providers []providerCard `json:"providers"`
}

// cloudAccountsView is an org's linked cloud accounts.
type cloudAccountsView struct {
	// Accounts is every account this org has linked, across all providers. Empty
	// when it has linked none.
	Accounts []cloudAccountView `json:"accounts"`
}

// accountFoldView is the answer to a link or a sync: the account as stored, and
// what happened to each cluster discovered in it.
type accountFoldView struct {
	// Account is the account as it is now recorded.
	Account cloudAccountView `json:"account"`
	// Clusters is one entry per cluster discovered in the account. It is empty
	// when discovery itself failed, which leaves the previously folded set
	// untouched rather than mass-detaching it.
	Clusters []clusterResult `json:"clusters"`
}

// unlinkedView is the answer to an unlink.
type unlinkedView struct {
	// Unlinked is always true. Unlinking is idempotent: an account this org does
	// not hold answers the same, so a repeated call is not an error and is not an
	// existence oracle either.
	Unlinked bool `json:"unlinked"`
}

// venueAccountRef addresses one linked account: the provider and the org-chosen
// label, both from the path.
type venueAccountRef struct {
	// Label is the org-chosen name of the account within that provider. Empty
	// means "default"; anything outside 1–64 of [A-Za-z0-9._-] is refused.
	Label string `json:"label"`
	// Provider is the cloud the account belongs to: digitalocean, aws, gcp or
	// azure. An unknown provider is not found.
	Provider string `json:"provider"`
}

// UnmarshalJSON decodes what parses and refuses NOTHING. It is here because
// POST .../sync has never read its request body: a caller that sends one — an
// empty object, a stray payload, malformed bytes — is answered exactly as one that
// sends none, and zip's decoder would otherwise turn that into a 400 the route has
// never sent. Both fields are path parameters, bound after the body, so nothing a
// body carries can redirect the sync.
func (in *venueAccountRef) UnmarshalJSON(b []byte) error {
	type body venueAccountRef // sheds the method, so this does not recurse
	var v body
	// The error is deliberately dropped, and dropping it IS the wire: see above.
	_ = json.Unmarshal(b, &v)
	*in = venueAccountRef(v)
	return nil
}

// venueProviderRef names the provider a link is being made against, from the path.
type venueProviderRef struct {
	// Provider is the cloud to link: digitalocean, aws, gcp or azure. An unknown
	// provider is not found.
	Provider string `json:"provider"`
}

// venueNoInput is the empty input of the two reads, which take nothing: what a
// caller sees is entirely their own validated org's.
type venueNoInput struct{}

// ── handlers ────────────────────────────────────────────────────────────────

// listProviders returns the clouds this deployment can link and what linking each
// one needs — the DigitalOcean token, the AWS role and external id, the GCP
// credential JSON, the Azure app — plus whether the provider can be linked without
// storing any long-lived secret. It is the catalog a "connect a cloud" screen
// renders; it reports no account and no credential.
func (o ops) listProviders(ctx context.Context, _ *venueNoInput) (*providersView, error) {
	if _, err := tenant(ctx); err != nil {
		return nil, err
	}
	return &providersView{Providers: []providerCard{
		{ID: providerDO, Name: "DigitalOcean", Keyless: false, Requires: []string{"token"}},
		{ID: providerAWS, Name: "AWS", Keyless: true, Requires: []string{"roleArn", "externalId", "regions"}},
		{ID: providerGCP, Name: "Google Cloud", Keyless: true, Requires: []string{"credentialJson", "projectIds"}},
		{ID: providerAzure, Name: "Azure", Keyless: true, Requires: []string{"tenantId", "clientId", "subscriptionIds"}},
	}}, nil
}

// listAccounts lists the caller org's linked cloud accounts across every provider:
// which account each one is at the provider, which fleet clusters it folded, and
// when it was last discovered. Metadata only — a sealed credential never appears in
// a response. Another org's accounts are not visible and not countable.
func (o ops) listAccounts(ctx context.Context, _ *venueNoInput) (*cloudAccountsView, error) {
	org, err := tenant(ctx)
	if err != nil {
		return nil, err
	}
	if !kmsReady(o.s) {
		return nil, kmsUnavailable()
	}
	list, err := listAccounts(o.s, org)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list: %v", err)
	}
	out := make([]cloudAccountView, 0, len(list))
	for _, a := range list {
		out = append(out, viewOf(a))
	}
	return &cloudAccountsView{Accounts: out}, nil
}

// venueLinkRequest is a link: the org-chosen label plus the provider's credential.
//
// The credential fields are spelled out here rather than embedded. encoding/json
// PROMOTES an embedded struct's fields so the wire would be identical, but zip's
// schema walk skips an embedded unexported type — which would publish a body of
// `label` alone and document none of the credential a link actually requires.
//
// The two unexported fields record whether the body decoded into this shape, so the
// route can keep answering 403 to a non-admin and 404 to an unknown provider BEFORE
// it complains about the body, which is the order it has always decided them in.
// That holds for a body that is valid JSON of the wrong shape and NOT for one that
// is not valid JSON at all: encoding/json validates the whole document before
// invoking any custom Unmarshaler, so zip's decoder refuses a syntax error before
// this input is built. TestSyntaxErrorIs400BeforeTheAdminGate measures it.
type venueLinkRequest struct {
	parsed    bool
	malformed bool

	// Label is the org-chosen name for this account within the provider, which is
	// how a second account at the same provider is addressed later. Empty means
	// "default"; anything outside 1–64 of [A-Za-z0-9._-] is refused.
	Label string `json:"label,omitempty"`
	// Provider is the cloud being linked, from the path: digitalocean, aws, gcp
	// or azure.
	Provider string `json:"provider"`

	// Token is the DigitalOcean personal access token. DigitalOcean only, and it
	// is the one provider that requires storing a secret.
	Token string `json:"token,omitempty"`

	// RoleARN is the AWS role Hanzo assumes into the account — the keyless path,
	// so no access key is ever stored.
	RoleARN string `json:"roleArn,omitempty"`
	// ExternalID pins that role assumption to Hanzo, which is what closes the
	// confused-deputy hole. AWS only.
	ExternalID string `json:"externalId,omitempty"`
	// Regions bounds the AWS EKS cluster sweep. AWS only.
	Regions []string `json:"regions,omitempty"`

	// CredentialJSON is a Google credentials document — an external_account
	// (workload identity federation, keyless) or a service-account key. GCP only.
	CredentialJSON string `json:"credentialJson,omitempty"`
	// ProjectIDs bounds the GKE cluster sweep. GCP only.
	ProjectIDs []string `json:"projectIds,omitempty"`

	// TenantID is the Azure AD tenant of the app. Azure only.
	TenantID string `json:"tenantId,omitempty"`
	// ClientID is the Azure AD application id. Azure only.
	ClientID string `json:"clientId,omitempty"`
	// ClientSecret selects the service-principal flow. LEAVING IT OUT selects
	// keyless workload identity federation instead, so omitting it is a choice
	// rather than an omission. Azure only.
	ClientSecret string `json:"clientSecret,omitempty"`
	// SubscriptionIDs bounds the AKS cluster sweep. Azure only.
	SubscriptionIDs []string `json:"subscriptionIds,omitempty"`
}

// UnmarshalJSON records whether the body was readable and refuses NOTHING, so the
// gates ahead of the body — the org-admin check, the unknown provider, the KMS
// probe — still answer first. See venueLinkRequest.
func (in *venueLinkRequest) UnmarshalJSON(b []byte) error {
	type body venueLinkRequest // sheds the method, so this does not recurse
	var v body
	malformed := json.Unmarshal(b, &v) != nil
	*in = venueLinkRequest(v)
	in.parsed, in.malformed = true, malformed
	return nil
}

// credential rebuilds the provider credential from the request. It is the ONE
// place the wire fields become the value that is verified and sealed.
func (in *venueLinkRequest) credential() cred {
	return cred{
		Token:           in.Token,
		RoleARN:         in.RoleARN,
		ExternalID:      in.ExternalID,
		Regions:         in.Regions,
		CredentialJSON:  in.CredentialJSON,
		ProjectIDs:      in.ProjectIDs,
		TenantID:        in.TenantID,
		ClientID:        in.ClientID,
		ClientSecret:    in.ClientSecret,
		SubscriptionIDs: in.SubscriptionIDs,
	}
}

// linkAccount links one of the caller org's cloud accounts and folds the Kubernetes
// clusters it finds there into the ONE Hanzo fleet, so they appear at /v1/clusters
// and can run work like any managed or bring-your-own cluster. Answers 201.
//
// The credential is verified LIVE against the provider BEFORE anything is stored,
// so a bad one is refused and nothing is written; it is then sealed in the org's own
// KMS namespace and never appears in a response, the account index, or a log line.
// Discovery follows, and a cluster that fails to fold is reported as DATA in the
// clusters list rather than failing the link.
//
// Re-linking a label that already exists re-seals its credential and re-folds it, so
// this is how a rotated token is replaced. Requires org admin.
func (o ops) linkAccount(ctx context.Context, in *venueLinkRequest) (*accountFoldView, error) {
	org, err := tenant(ctx)
	if err != nil {
		return nil, err
	}
	c, err := writer(ctx)
	if err != nil {
		return nil, err
	}
	d, ok := o.driverFor(in.Provider)
	if !ok {
		return nil, zip.ErrNotFound("unknown provider")
	}
	if !kmsReady(o.s) {
		return nil, kmsUnavailable()
	}
	if !in.parsed || in.malformed {
		return nil, zip.ErrBadRequest("invalid request body")
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
	list, err := listAccounts(o.s, org)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list: %v", err)
	}
	if _, exists := findAccount(list, d.id(), label); !exists && countForProvider(list, d.id()) >= maxAccounts {
		return nil, zip.ErrBadRequest("too many accounts for provider")
	}

	// Verify LIVE before sealing anything.
	ident, verr := d.verify(ctx, cr)
	if verr != nil {
		o.s.Log.Warn("cloud account verify failed", "provider", d.id(), "org", org, "err", verr)
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
	if err := o.s.State.kms.Put(path, credName, venueEnv, blob); err != nil {
		o.s.Log.Warn("cloud account seal failed", "provider", d.id(), "org", org, "err", err)
		return nil, zip.Errorf(http.StatusServiceUnavailable, "credential custody failed")
	}

	prev, _ := findAccount(list, d.id(), label)
	acct := Account{
		Provider: d.id(), Label: label, ExternalID: ident.ExternalID, Display: ident.Display,
		Project: principal.Project(c), Clusters: prev.Clusters,
		LinkedAt: firstNonEmpty(prev.LinkedAt, nowRFC3339()),
	}
	// Discover + fold. Per-cluster failures are DATA, not a link failure.
	results, acct := discoverAndFold(o.s, c, org, cr, d, acct)
	acct.SyncedAt = nowRFC3339()
	list = upsertAccount(list, acct)
	if err := writeIndex(o.s, org, list); err != nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "persist account: %v", err)
	}
	return &accountFoldView{Account: viewOf(acct), Clusters: results}, nil
}

// syncAccount re-discovers one already-linked cloud account and reconciles what it
// folded: kubeconfigs are refreshed, clusters that appeared since the last sync are
// folded, and clusters this account folded that the provider no longer returns are
// detached — only this account's own, in the fleet shard it was linked into.
//
// It is idempotent, it reads the credential already sealed at link time, and a
// discovery failure leaves the existing fold set alone rather than mass-detaching
// it. An account this org has not linked is not found. Requires org admin.
func (o ops) syncAccount(ctx context.Context, in *venueAccountRef) (*accountFoldView, error) {
	org, err := tenant(ctx)
	if err != nil {
		return nil, err
	}
	c, err := writer(ctx)
	if err != nil {
		return nil, err
	}
	d, ok := o.driverFor(in.Provider)
	if !ok {
		return nil, zip.ErrNotFound("unknown provider")
	}
	label, err := labelIn(in.Label)
	if err != nil {
		return nil, err
	}
	if !kmsReady(o.s) {
		return nil, kmsUnavailable()
	}
	list, err := listAccounts(o.s, org)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list: %v", err)
	}
	acct, found := findAccount(list, d.id(), label)
	if !found {
		return nil, zip.ErrNotFound("cloud account not linked")
	}
	cr, err := loadCred(o.s, org, d.id(), label)
	if err != nil {
		return nil, err
	}
	results, acct := discoverAndFold(o.s, c, org, cr, d, acct)
	acct.SyncedAt = nowRFC3339()
	list = upsertAccount(list, acct)
	if err := writeIndex(o.s, org, list); err != nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "persist account: %v", err)
	}
	return &accountFoldView{Account: viewOf(acct), Clusters: results}, nil
}

// unlinkAccount forgets one linked cloud account: it detaches every fleet cluster
// THIS account folded (its own names, in its own shard — a neighbour's cluster of
// the same name is untouched), deletes the sealed credential, and drops the index
// row.
//
// It is idempotent and deliberately not an existence oracle: an account this org
// does not hold answers exactly the same as one it just removed. A cluster that
// fails to detach is logged and the unlink continues, so a dead provider cannot
// strand a credential. Requires org admin.
func (o ops) unlinkAccount(ctx context.Context, in *venueAccountRef) (*unlinkedView, error) {
	org, err := tenant(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := writer(ctx); err != nil {
		return nil, err
	}
	d, ok := o.driverFor(in.Provider)
	if !ok {
		return nil, zip.ErrNotFound("unknown provider")
	}
	label, err := labelIn(in.Label)
	if err != nil {
		return nil, err
	}
	if !kmsReady(o.s) {
		return nil, kmsUnavailable()
	}
	list, err := listAccounts(o.s, org)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list: %v", err)
	}
	acct, found := findAccount(list, d.id(), label)
	if !found {
		return &unlinkedView{Unlinked: true}, nil
	}
	// Detach the clusters this account folded (only its own fold names, in its
	// recorded fleet shard) — reverses the fold.
	for _, name := range acct.Clusters {
		if _, derr := o.s.State.fleet.Deregister(org, acct.Project, name); derr != nil {
			o.s.Log.Warn("cloud account detach cluster failed (continuing)", "provider", d.id(), "org", org, "cluster", name, "err", derr)
		}
	}
	if path, perr := credPath(org, d.id(), label); perr == nil {
		if derr := o.s.State.kms.Delete(path, credName, venueEnv); derr != nil && !errors.Is(derr, kms.ErrSecretNotFound) {
			o.s.Log.Warn("cloud account cred delete failed (continuing)", "provider", d.id(), "org", org, "err", derr)
		}
	}
	out := list[:0]
	for _, a := range list {
		if a.Provider == d.id() && a.Label == label {
			continue
		}
		out = append(out, a)
	}
	if err := writeIndex(o.s, org, out); err != nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "persist account: %v", err)
	}
	return &unlinkedView{Unlinked: true}, nil
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
