// Copyright 2025 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package venue

// azure.go discovers an org's AKS clusters over plain REST (net/http) — no
// azure-sdk-for-go. The org registers an Azure AD app (tenant + client); a
// client_secret drives the service-principal flow, and its ABSENCE selects
// KEYLESS Workload Identity Federation — Hanzo presents its OWN federated OIDC
// token (the projected AZURE_FEDERATED_TOKEN_FILE the Azure workload-identity
// webhook injects) as a client assertion, so no customer secret is stored. With
// the AAD token we GET the subscription's managedClusters and POST
// listClusterUserCredential (an ARM kubeconfig) per cluster, and fold.
//
// Test seam: AZURE_AD_ENDPOINT redirects the AAD token endpoint, AZURE_ARM_ENDPOINT
// the ARM API, at httptest stubs; AZURE_FEDERATED_TOKEN supplies the WIF assertion.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// armAPIVersion pins the managedClusters ARM API version.
const armAPIVersion = "2024-05-01"

var azureHTTP = &http.Client{Timeout: 20 * time.Second}

type azureDriver struct{}

func (azureDriver) id() string { return providerAzure }

func azureADBase() string {
	if e := strings.TrimSpace(os.Getenv("AZURE_AD_ENDPOINT")); e != "" {
		return strings.TrimRight(e, "/")
	}
	return "https://login.microsoftonline.com"
}

func azureARMBase() string {
	if e := strings.TrimSpace(os.Getenv("AZURE_ARM_ENDPOINT")); e != "" {
		return strings.TrimRight(e, "/")
	}
	return "https://management.azure.com"
}

// azureFederatedToken reads Hanzo's projected federated OIDC token for the keyless
// (WIF) path. AZURE_FEDERATED_TOKEN is the test seam; AZURE_FEDERATED_TOKEN_FILE is
// the standard Azure workload-identity projection.
func azureFederatedToken() (string, error) {
	if t := strings.TrimSpace(os.Getenv("AZURE_FEDERATED_TOKEN")); t != "" {
		return t, nil
	}
	f := strings.TrimSpace(os.Getenv("AZURE_FEDERATED_TOKEN_FILE"))
	if f == "" {
		return "", fmt.Errorf("azure workload identity token unavailable")
	}
	b, err := os.ReadFile(f)
	if err != nil {
		return "", fmt.Errorf("azure workload identity token unavailable")
	}
	return strings.TrimSpace(string(b)), nil
}

// azureToken mints an AAD access token for ARM (client-credentials, or keyless WIF
// when no client secret is set).
func azureToken(ctx context.Context, cr cred) (string, error) {
	tenant := strings.TrimSpace(cr.TenantID)
	if tenant == "" || strings.TrimSpace(cr.ClientID) == "" {
		return "", fmt.Errorf("tenantId and clientId are required")
	}
	form := url.Values{
		"grant_type": {"client_credentials"},
		"client_id":  {cr.ClientID},
		"scope":      {"https://management.azure.com/.default"},
	}
	if strings.TrimSpace(cr.ClientSecret) != "" {
		form.Set("client_secret", cr.ClientSecret)
	} else {
		assertion, err := azureFederatedToken()
		if err != nil {
			return "", err
		}
		form.Set("client_assertion_type", "urn:ietf:params:oauth:client-assertion-type:jwt-bearer")
		form.Set("client_assertion", assertion)
	}
	body := form.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, azureADBase()+"/"+tenant+"/oauth2/v2.0/token", strings.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := azureHTTP.Do(req)
	if err != nil {
		return "", fmt.Errorf("azure token request failed")
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode/100 != 2 {
		return "", fmt.Errorf("azure rejected the credential (%d)", resp.StatusCode)
	}
	var tr struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
	}
	if err := json.Unmarshal(raw, &tr); err != nil {
		return "", fmt.Errorf("azure token: malformed response")
	}
	if tr.Error != "" || tr.AccessToken == "" {
		return "", fmt.Errorf("azure token: no access_token")
	}
	return tr.AccessToken, nil
}

func (azureDriver) verify(ctx context.Context, cr cred) (identity, error) {
	if len(cr.SubscriptionIDs) == 0 {
		return identity{}, fmt.Errorf("at least one subscriptionId is required")
	}
	if _, err := azureToken(ctx, cr); err != nil {
		return identity{}, err
	}
	return identity{ExternalID: cr.TenantID, Display: firstNonEmpty(cr.SubscriptionIDs[0], cr.TenantID)}, nil
}

// aksCluster is the subset of a managedClusters entry we need.
type aksCluster struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Location   string `json:"location"`
	Properties struct {
		Fqdn string `json:"fqdn"`
	} `json:"properties"`
}

func (azureDriver) discover(ctx context.Context, cr cred) ([]discovered, error) {
	if len(cr.SubscriptionIDs) == 0 {
		return nil, fmt.Errorf("at least one subscriptionId is required")
	}
	token, err := azureToken(ctx, cr)
	if err != nil {
		return nil, err
	}
	var out []discovered
	for _, sub := range cr.SubscriptionIDs {
		sub = strings.TrimSpace(sub)
		if sub == "" {
			continue
		}
		clusters, lerr := aksList(ctx, token, sub)
		if lerr != nil {
			// A bad/denied subscription is not fatal to the whole account.
			continue
		}
		for _, cl := range clusters {
			if strings.TrimSpace(cl.ID) == "" {
				continue
			}
			kube, kerr := aksKubeconfig(ctx, token, cl.ID)
			if kerr != nil || len(kube) == 0 {
				continue
			}
			out = append(out, discovered{
				ID:         cl.ID,
				Name:       firstNonEmpty(cl.Name, cl.ID),
				Region:     cl.Location,
				Endpoint:   "https://" + strings.TrimPrefix(strings.TrimPrefix(cl.Properties.Fqdn, "https://"), "http://"),
				Kubeconfig: kube,
			})
		}
	}
	return out, nil
}

func aksList(ctx context.Context, token, subscription string) ([]aksCluster, error) {
	u := azureARMBase() + "/subscriptions/" + url.PathEscape(subscription) +
		"/providers/Microsoft.ContainerService/managedClusters?api-version=" + armAPIVersion
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := azureHTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("azure list clusters request failed")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("azure list clusters http %d", resp.StatusCode)
	}
	var body struct {
		Value []aksCluster `json:"value"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body); err != nil {
		return nil, fmt.Errorf("azure list clusters: malformed response")
	}
	return body.Value, nil
}

// aksKubeconfig POSTs listClusterUserCredential and decodes the first kubeconfig.
func aksKubeconfig(ctx context.Context, token, clusterID string) ([]byte, error) {
	u := azureARMBase() + clusterID + "/listClusterUserCredential?api-version=" + armAPIVersion
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, strings.NewReader("{}"))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := azureHTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("azure cluster credential request failed")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("azure cluster credential http %d", resp.StatusCode)
	}
	var body struct {
		Kubeconfigs []struct {
			Name  string `json:"name"`
			Value string `json:"value"`
		} `json:"kubeconfigs"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body); err != nil {
		return nil, fmt.Errorf("azure cluster credential: malformed response")
	}
	if len(body.Kubeconfigs) == 0 {
		return nil, fmt.Errorf("azure cluster credential: empty")
	}
	kube, err := base64.StdEncoding.DecodeString(strings.TrimSpace(body.Kubeconfigs[0].Value))
	if err != nil {
		return nil, fmt.Errorf("azure cluster credential: not valid base64")
	}
	return kube, nil
}
