// Copyright 2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

// managed.go — clusters the platform provisioned, in the same fleet as the ones
// customers attach.
//
// An attached cluster comes with a kubeconfig, and a kubeconfig carries a
// credential that does not expire. A provisioned cluster does not work that way:
// every managed cloud mints an apiserver token that dies within the hour, so
// there is nothing durable to seal. What IS durable is where the apiserver is
// and who signed its certificate, and that is what a managed record holds. The
// token is fetched when a connection needs one and kept only until it expires.
//
// Compute owns the provider account and is the only thing that can mint. So the
// mint is a call to Compute with cloud's own service credential — the same
// credential the compute surface uses — and the answer reaches exactly one
// cluster until it expires. The provider key stays in Compute.

package fleet

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/cloud/sdk/env"
	"golang.org/x/oauth2"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/transport"
)

// KindManaged marks a fleet record the platform provisioned, as against the
// attached kinds (byo, byo-gpu, metal) that arrive with a kubeconfig. It is the
// ONE discriminator: a record that is not this reaches its cluster by unsealing
// a kubeconfig, and a record that is this reaches it by minting a token.
const KindManaged = "managed"

// Managed is how to reach a provisioned cluster, minus the part that expires.
// Sealed in the org's KMS in the same slot an attached cluster's kubeconfig
// occupies — one slot per cluster, and the record's Kind says how to read it.
//
// There is deliberately no token field. A token belongs to a connection, not to
// a record: writing one down would make it stale within the hour and would put a
// live apiserver credential at rest for nothing.
type Managed struct {
	// Provider is the cloud, as Compute spells it ("DigitalOcean").
	Provider string `json:"provider"`
	// ID is the provider's own id for the cluster — what Compute is asked to mint
	// against.
	ID string `json:"providerId"`
	// Endpoint is the apiserver URL.
	Endpoint string `json:"endpoint"`
	// CAData is the PEM certificate authority that signed the apiserver.
	CAData []byte `json:"caData"`
}

// Adopt records a cluster the platform provisioned into the org+project fleet,
// so everything that already runs work on an attached cluster runs it here too.
//
// It does NOT reach the cluster first, and that is the difference from Register:
// a cluster minutes old has no workloads and often no reachable apiserver yet,
// so a probe here would refuse to record a cluster the org is already paying
// for. The node counts stay zero until something reads them.
func (r *Registry) Adopt(ctx context.Context, org, project, name string, m Managed) (Cluster, error) {
	if !r.Enabled() {
		return Cluster{}, fmt.Errorf("fleet registration requires KMS (set CLOUD_KMS_NODES + CLOUD_KMS_PASSPHRASE)")
	}
	if strings.TrimSpace(m.Endpoint) == "" || m.ID == "" {
		return Cluster{}, fmt.Errorf("a managed record needs the cluster's endpoint and provider id")
	}
	if err := guardHost(m.Endpoint); err != nil {
		return Cluster{}, err
	}
	sealed, err := json.Marshal(m)
	if err != nil {
		return Cluster{}, err
	}
	if err := r.kms.PutSecret(ctx, configRef(org, project, name), sealed); err != nil {
		return Cluster{}, fmt.Errorf("seal managed record: %w", err)
	}
	list, err := r.List(ctx, org, project)
	if err != nil {
		return Cluster{}, err
	}
	rec := Cluster{
		Name: name, Org: org, Kind: KindManaged, Provider: m.Provider,
		Endpoint: m.Endpoint, Registered: time.Now().UTC().Format(time.RFC3339),
	}
	if err := r.writeIndex(ctx, org, project, upsert(list, rec)); err != nil {
		return Cluster{}, err
	}
	return rec, nil
}

// adoptWindow bounds how long AdoptWhenReady keeps trying. A control plane takes
// minutes to come up, and one that has not answered within this has a problem no
// retry will fix.
const adoptWindow = 15 * time.Minute

// adoptEvery is the gap between attempts inside that window.
const adoptEvery = 20 * time.Second

// AdoptWhenReady records a freshly provisioned cluster as soon as its control
// plane can mint a credential, and returns immediately.
//
// It waits in the background because it has to: nothing can be minted against a
// control plane that is still coming up, and the request that provisioned the
// cluster is not the place to sit out five minutes. A cluster that never becomes
// reachable is logged as the problem it is — it is provisioned and billing
// either way, and an operator needs to know nothing can be scheduled on it.
func (r *Registry) AdoptWhenReady(org, project, name, provider, id string) {
	if !r.Enabled() || id == "" || name == "" {
		return
	}
	go func() {
		deadline := time.Now().Add(adoptWindow)
		for {
			err := r.adoptOnce(org, project, name, provider, id)
			if err == nil {
				r.log.Info("cluster adopted into the fleet", "org", org, "cluster", name, "provider", provider)
				return
			}
			if time.Now().After(deadline) {
				r.log.Error("cluster provisioned but NOT adopted into the fleet - it is billing and nothing can be scheduled on it",
					"org", org, "cluster", id, "provider", provider, "err", err)
				return
			}
			time.Sleep(adoptEvery)
		}
	}()
}

// adoptOnce is one attempt: mint, and record what came back.
func (r *Registry) adoptOnce(org, project, name, provider, id string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	creds, err := mint(ctx, org, id)
	if err != nil {
		return err
	}
	if creds.Endpoint == "" {
		return fmt.Errorf("compute minted no endpoint for cluster %s", id)
	}
	_, err = r.Adopt(ctx, org, project, name, Managed{
		Provider: provider, ID: id, Endpoint: creds.Endpoint, CAData: creds.CAData,
	})
	return err
}

// restForManaged builds the REST config that reaches a managed cluster: the
// endpoint and CA from the sealed record, and a token fetched per connection
// from Compute. Nothing here parses a kubeconfig, so nothing here can be handed an
// exec plugin — the refusals SafeRESTConfig exists for have no surface.
func restForManaged(org string, sealed []byte) (*rest.Config, error) {
	var m Managed
	if err := json.Unmarshal(sealed, &m); err != nil {
		return nil, fmt.Errorf("corrupt managed record: %w", err)
	}
	if err := guardHost(m.Endpoint); err != nil {
		return nil, err
	}
	cfg := &rest.Config{
		Host:      m.Endpoint,
		CAData:    m.CAData,
		UserAgent: "hanzo-cloud-fleet",
	}
	cfg.WrapTransport = transport.TokenSourceWrapTransport(&minter{org: org, cluster: m.ID})
	return cfg, nil
}

// early is how long before a token's stated expiry it is replaced. A token that
// expires while a request is in flight fails that request, so the last minutes
// of its life are spent on the next one instead.
const early = 2 * time.Minute

// minter fetches apiserver tokens for one cluster and holds the current one
// until it is nearly out of time. It is an oauth2.TokenSource because that is
// what client-go's transport takes; the tokens are not OAuth, they are whatever
// bearer the cloud provider signs.
type minter struct {
	org     string
	cluster string

	mu  sync.Mutex
	tok *oauth2.Token
}

// Token returns a live bearer for the cluster, minting a new one when the one in
// hand is inside early of expiry. The lock is held across the fetch so a burst of
// connections mints once rather than once each.
func (m *minter) Token() (*oauth2.Token, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.tok != nil && time.Until(m.tok.Expiry) > early {
		return m.tok, nil
	}
	creds, err := mint(context.Background(), m.org, m.cluster)
	if err != nil {
		return nil, err
	}
	if creds.Token == "" {
		return nil, fmt.Errorf("compute minted no token for cluster %s", m.cluster)
	}
	m.tok = &oauth2.Token{AccessToken: creds.Token, TokenType: "Bearer", Expiry: creds.Expiry}
	return m.tok, nil
}

// credentials is what Compute answers with: how to reach one cluster's apiserver
// and when that stops being true.
type credentials struct {
	Endpoint string    `json:"endpoint"`
	CAData   []byte    `json:"caData"`
	Token    string    `json:"token"`
	Expiry   time.Time `json:"expiry"`
}

// mint asks Compute for a credential to one cluster, as cloud itself rather than
// as a caller: this runs while a workload is being scheduled, which is nobody's
// request. The org still rides on ?owner=, and Compute refuses an id that org does
// not own — so a bug here cannot borrow another tenant's cluster.
func mint(ctx context.Context, org, cluster string) (credentials, error) {
	u := computeBase() + "/v1/k8s/clusters/" + url.PathEscape(cluster) + "/credentials" +
		"?owner=" + url.QueryEscape(org)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return credentials{}, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Org-Id", org)
	id, secret := env.Or("COMPUTE_CLIENT_ID", ""), env.Or("COMPUTE_CLIENT_SECRET", "")
	if id == "" || secret == "" {
		return credentials{}, fmt.Errorf("reaching a managed cluster needs COMPUTE_CLIENT_ID + COMPUTE_CLIENT_SECRET")
	}
	req.SetBasicAuth(id, secret)

	resp, err := computeClient.Do(req)
	if err != nil {
		return credentials{}, fmt.Errorf("compute unreachable: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return credentials{}, fmt.Errorf("compute answered %d minting for cluster %s", resp.StatusCode, cluster)
	}
	// Compute's compute surface answers {status,msg,data}; a logical failure is
	// status:"error" at HTTP 200, so the status code alone does not decide.
	var env struct {
		Status string          `json:"status"`
		Msg    string          `json:"msg"`
		Data   json.RawMessage `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		return credentials{}, fmt.Errorf("compute: decode credentials: %w", err)
	}
	if env.Status != "ok" {
		return credentials{}, fmt.Errorf("compute: %s", env.Msg)
	}
	var creds credentials
	if err := json.Unmarshal(env.Data, &creds); err != nil {
		return credentials{}, fmt.Errorf("compute: decode credentials: %w", err)
	}
	return creds, nil
}

// computeClient is the one HTTP client the mint path uses. Its timeout bounds a
// mint, which sits in front of a workload's first connection.
var computeClient = &http.Client{Timeout: 15 * time.Second}

// computeBase is where Compute answers. Same COMPUTE_URL the compute surface reads, so
// a deployment states it once.
func computeBase() string {
	if v := env.Or("COMPUTE_URL", ""); v != "" {
		return strings.TrimRight(v, "/")
	}
	return "http://compute.hanzo.svc:19000"
}
