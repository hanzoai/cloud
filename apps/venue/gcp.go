// Copyright 2025 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package venue

// gcp.go discovers an org's GKE clusters over PLAIN REST (net/http) — no
// google.golang.org/api container SDK, no gRPC tree. The only google dependency
// is golang.org/x/oauth2/google for the token source, which handles BOTH an
// external_account (Workload Identity Federation — KEYLESS, preferred, no private
// key) and a service_account key uniformly. We GET
// container.googleapis.com/v1/projects/{proj}/locations/-/clusters with a bearer
// token, and build a kubeconfig bearing that same google access token (GKE accepts
// it directly).
//
// Test seam: GCP_STATIC_TOKEN supplies a token source without google's token
// endpoint; GCP_CONTAINER_ENDPOINT redirects the container API at an httptest stub.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

// cloudPlatformScope is the OAuth scope for the container REST API.
const cloudPlatformScope = "https://www.googleapis.com/auth/cloud-platform"

var gcpHTTP = &http.Client{Timeout: 20 * time.Second}

type gcpDriver struct{}

func (gcpDriver) id() string { return providerGCP }

func gcpContainerBase() string {
	if ep := strings.TrimSpace(os.Getenv("GCP_CONTAINER_ENDPOINT")); ep != "" {
		return strings.TrimRight(ep, "/")
	}
	return "https://container.googleapis.com"
}

// gcpTokenSource returns the OAuth token source for the credential plus the
// resolved project. The static-token test seam bypasses google's token endpoint.
func gcpTokenSource(ctx context.Context, cr cred) (oauth2.TokenSource, string, error) {
	proj := firstProject(cr)
	if tok := strings.TrimSpace(os.Getenv("GCP_STATIC_TOKEN")); tok != "" {
		return oauth2.StaticTokenSource(&oauth2.Token{AccessToken: tok}), proj, nil
	}
	if strings.TrimSpace(cr.CredentialJSON) == "" {
		return nil, "", fmt.Errorf("credentialJson is required")
	}
	if err := validateGoogleCredential([]byte(cr.CredentialJSON)); err != nil {
		return nil, "", err
	}
	creds, err := google.CredentialsFromJSON(ctx, []byte(cr.CredentialJSON), cloudPlatformScope)
	if err != nil {
		return nil, "", fmt.Errorf("gcp credentials rejected")
	}
	return creds.TokenSource, firstNonEmpty(proj, creds.ProjectID), nil
}

// validateGoogleCredential fail-closes the customer-supplied google credentials
// JSON to a SAFE shape BEFORE it reaches google.CredentialsFromJSON. An
// external_account (Workload Identity Federation) config lets its AUTHOR choose
// where the pod fetches a subject token (credential_source.url → SSRF from the
// pod, credential_source.file → arbitrary local file read) and where tokens are
// sent (token_url, which x/oauth2 does not validate) — a full read/exfiltration
// primitive that fires during verify(), before anything is sealed. So only a
// service_account key is accepted, and its token_uri is pinned to a google host.
// Keyless WIF is offered instead via a HANZO-OWNED config keyed by project (a
// follow-on), never a customer-authored external_account.
func validateGoogleCredential(raw []byte) error {
	var probe struct {
		Type     string `json:"type"`
		TokenURI string `json:"token_uri"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return fmt.Errorf("credentialJson is not valid JSON")
	}
	if probe.Type != "service_account" {
		return fmt.Errorf("only a service_account key is accepted (external_account/WIF must be Hanzo-configured)")
	}
	if t := strings.TrimSpace(probe.TokenURI); t != "" && !isGoogleHost(t) {
		return fmt.Errorf("service_account token_uri must be a google endpoint")
	}
	return nil
}

func isGoogleHost(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme != "https" {
		return false
	}
	h := strings.ToLower(u.Hostname())
	return h == "googleapis.com" || strings.HasSuffix(h, ".googleapis.com") || h == "accounts.google.com"
}

func firstProject(cr cred) string {
	for _, p := range cr.ProjectIDs {
		if s := strings.TrimSpace(p); s != "" {
			return s
		}
	}
	return ""
}

func (gcpDriver) verify(ctx context.Context, cr cred) (identity, error) {
	ts, proj, err := gcpTokenSource(ctx, cr)
	if err != nil {
		return identity{}, err
	}
	if _, terr := ts.Token(); terr != nil {
		return identity{}, fmt.Errorf("gcp credential could not mint a token")
	}
	if proj == "" {
		return identity{}, fmt.Errorf("at least one projectId is required")
	}
	return identity{ExternalID: proj, Display: proj}, nil
}

// gkeCluster is the subset of a GKE cluster the REST list returns that we need.
type gkeCluster struct {
	Name       string `json:"name"`
	Location   string `json:"location"`
	Endpoint   string `json:"endpoint"`
	SelfLink   string `json:"selfLink"`
	MasterAuth struct {
		ClusterCaCertificate string `json:"clusterCaCertificate"`
	} `json:"masterAuth"`
}

func (gcpDriver) discover(ctx context.Context, cr cred) ([]discovered, error) {
	if len(cr.ProjectIDs) == 0 {
		return nil, fmt.Errorf("at least one projectId is required")
	}
	ts, _, err := gcpTokenSource(ctx, cr)
	if err != nil {
		return nil, err
	}
	tok, terr := ts.Token()
	if terr != nil {
		return nil, fmt.Errorf("gcp token unavailable")
	}
	var out []discovered
	for _, proj := range cr.ProjectIDs {
		proj = strings.TrimSpace(proj)
		if proj == "" {
			continue
		}
		clusters, lerr := gkeList(ctx, tok.AccessToken, proj)
		if lerr != nil {
			// A bad/denied project is not fatal to the whole account.
			continue
		}
		for _, cl := range clusters {
			if strings.TrimSpace(cl.Endpoint) == "" {
				continue
			}
			caPEM, caerr := decodeCA(cl.MasterAuth.ClusterCaCertificate)
			if caerr != nil {
				continue
			}
			endpoint := "https://" + strings.TrimPrefix(strings.TrimPrefix(cl.Endpoint, "https://"), "http://")
			kube, kerr := buildKubeconfig(cl.Name, endpoint, caPEM, tok.AccessToken)
			if kerr != nil {
				continue
			}
			out = append(out, discovered{
				ID:         firstNonEmpty(cl.SelfLink, cl.Name),
				Name:       cl.Name,
				Region:     cl.Location,
				Endpoint:   endpoint,
				Kubeconfig: kube,
			})
		}
	}
	return out, nil
}

// gkeList calls container.clusters.list for a project (all locations).
func gkeList(ctx context.Context, bearer, project string) ([]gkeCluster, error) {
	u := gcpContainerBase() + "/v1/projects/" + project + "/locations/-/clusters"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	resp, err := gcpHTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("gcp list clusters")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("gcp list clusters http %d", resp.StatusCode)
	}
	var body struct {
		Clusters []gkeCluster `json:"clusters"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body); err != nil {
		return nil, fmt.Errorf("gcp list clusters: malformed response")
	}
	return body.Clusters, nil
}
