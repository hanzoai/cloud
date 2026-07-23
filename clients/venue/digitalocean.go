// Copyright 2025 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package venue

// digitalocean.go discovers an org's DOKS clusters from a customer-supplied DO
// personal access token. verify hits GET /v2/account; discover lists
// /v2/kubernetes/clusters and pulls each cluster's kubeconfig
// (/v2/kubernetes/clusters/{id}/kubeconfig) to fold into the fleet. The token is
// the CUSTOMER's — org-scoped, KMS-sealed — NOT the platform's house DO key
// (clients/do). The DO API base is the real api.digitalocean.com in production,
// overridable via DIGITALOCEAN_API_URL for an httptest stub only.

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/digitalocean/godo"
	"golang.org/x/oauth2"
)

// doMaxPages bounds cluster-list pagination so a pathological account cannot
// wedge discovery.
const doMaxPages = 20

type doDriver struct{}

func (doDriver) id() string { return providerDO }

// doClient builds a godo client bound to the customer token. The base URL is the
// real DO API unless DIGITALOCEAN_API_URL overrides it (tests only).
func doClient(token string) (*godo.Client, error) {
	hc := oauth2.NewClient(context.Background(), oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token}))
	if base := strings.TrimSpace(os.Getenv("DIGITALOCEAN_API_URL")); base != "" {
		return godo.New(hc, godo.SetBaseURL(base))
	}
	return godo.New(hc)
}

// verify proves the token by reading the account. Fails closed; the error never
// contains the token.
func (doDriver) verify(ctx context.Context, cr cred) (identity, error) {
	token := strings.TrimSpace(cr.Token)
	if token == "" {
		return identity{}, fmt.Errorf("a DigitalOcean token is required")
	}
	client, err := doClient(token)
	if err != nil {
		return identity{}, fmt.Errorf("digitalocean client")
	}
	acct, _, err := client.Account.Get(ctx)
	if err != nil {
		return identity{}, fmt.Errorf("digitalocean rejected the token")
	}
	return identity{ExternalID: strings.TrimSpace(acct.UUID), Display: strings.TrimSpace(acct.Email)}, nil
}

// discover lists the account's DOKS clusters and pulls each kubeconfig.
func (doDriver) discover(ctx context.Context, cr cred) ([]discovered, error) {
	token := strings.TrimSpace(cr.Token)
	if token == "" {
		return nil, fmt.Errorf("a DigitalOcean token is required")
	}
	client, err := doClient(token)
	if err != nil {
		return nil, fmt.Errorf("digitalocean client")
	}
	var clusters []*godo.KubernetesCluster
	opt := &godo.ListOptions{PerPage: 200}
	for page := 1; page <= doMaxPages; page++ {
		opt.Page = page
		batch, resp, lerr := client.Kubernetes.List(ctx, opt)
		if lerr != nil {
			return nil, fmt.Errorf("digitalocean list clusters")
		}
		clusters = append(clusters, batch...)
		if resp == nil || resp.Links == nil || resp.Links.IsLastPage() {
			break
		}
	}

	out := make([]discovered, 0, len(clusters))
	for _, cl := range clusters {
		if cl == nil || strings.TrimSpace(cl.ID) == "" {
			continue
		}
		cfg, _, kerr := client.Kubernetes.GetKubeConfig(ctx, cl.ID, nil)
		if kerr != nil || cfg == nil || len(cfg.KubeconfigYAML) == 0 {
			// A cluster we cannot pull a kubeconfig for is skipped (not fatal to
			// the whole account); the caller sees only the clusters we can fold.
			continue
		}
		out = append(out, discovered{
			ID:         cl.ID,
			Name:       firstNonEmpty(cl.Name, cl.ID),
			Region:     cl.RegionSlug,
			Endpoint:   strings.TrimSpace(cl.Endpoint),
			Kubeconfig: cfg.KubeconfigYAML,
		})
	}
	return out, nil
}
