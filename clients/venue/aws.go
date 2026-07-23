// Copyright 2025 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package venue

// aws.go discovers an org's EKS clusters KEYLESSLY over plain REST (net/http +
// the owned SigV4 signer) — no aws-sdk-go-v2. The org grants Hanzo's platform AWS
// identity permission to assume a cross-account role, pinned by an external id
// (confused-deputy protection — STS AssumeRole FAILS without the exact external id
// the org set on the trust policy). No customer access keys are stored; only the
// role ARN + external id are sealed. With the temporary assumed credentials we
// eks:ListClusters + eks:DescribeCluster per region, mint the standard EKS
// ("k8s-aws-v1.") bearer via a presigned STS GetCallerIdentity URL, and build a
// kubeconfig to fold.
//
// Test seam: AWS_ENDPOINT_URL redirects STS + EKS at an httptest stub; base creds
// come from the ambient env (AWS_ACCESS_KEY_ID/SECRET in tests, instance profile
// in prod).

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const awsMaxPages = 20

var awsHTTP = &http.Client{Timeout: 20 * time.Second}

type awsDriver struct{}

func (awsDriver) id() string { return providerAWS }

func awsEndpointURL() string { return strings.TrimSpace(os.Getenv("AWS_ENDPOINT_URL")) }

func stsEndpoint(region string) string {
	if e := awsEndpointURL(); e != "" {
		return e
	}
	return "https://sts." + region + ".amazonaws.com"
}

func eksEndpoint(region string) string {
	if e := awsEndpointURL(); e != "" {
		return e
	}
	return "https://eks." + region + ".amazonaws.com"
}

// awsBaseCreds reads Hanzo's own AWS identity from the ambient env (instance
// profile in prod). This is the platform base that assumes the customer's role.
func awsBaseCreds() awsCreds {
	return awsCreds{
		accessKey:    strings.TrimSpace(os.Getenv("AWS_ACCESS_KEY_ID")),
		secretKey:    strings.TrimSpace(os.Getenv("AWS_SECRET_ACCESS_KEY")),
		sessionToken: strings.TrimSpace(os.Getenv("AWS_SESSION_TOKEN")),
	}
}

// stsRegion picks the region STS assume-role signs under.
func stsRegion(cr cred) string {
	if r := strings.TrimSpace(os.Getenv("AWS_REGION")); r != "" {
		return r
	}
	for _, r := range cr.Regions {
		if s := strings.TrimSpace(r); s != "" {
			return s
		}
	}
	return "us-east-1"
}

type assumeRoleResp struct {
	XMLName xml.Name `xml:"AssumeRoleResponse"`
	Result  struct {
		Credentials struct {
			AccessKeyID     string `xml:"AccessKeyId"`
			SecretAccessKey string `xml:"SecretAccessKey"`
			SessionToken    string `xml:"SessionToken"`
		} `xml:"Credentials"`
		AssumedRoleUser struct {
			Arn string `xml:"Arn"`
		} `xml:"AssumedRoleUser"`
	} `xml:"AssumeRoleResult"`
}

// assumeRole performs STS AssumeRole with the external id, returning temporary
// credentials and the account id (parsed from the assumed-role ARN).
func assumeRole(ctx context.Context, cr cred) (awsCreds, string, error) {
	base := awsBaseCreds()
	if base.accessKey == "" || base.secretKey == "" {
		return awsCreds{}, "", fmt.Errorf("aws base identity unavailable")
	}
	region := stsRegion(cr)
	form := url.Values{
		"Action":          {"AssumeRole"},
		"Version":         {"2011-06-15"},
		"RoleArn":         {cr.RoleARN},
		"RoleSessionName": {"hanzo-venue-discovery"},
		"ExternalId":      {cr.ExternalID},
		"DurationSeconds": {"900"},
	}
	body := []byte(form.Encode())
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, stsEndpoint(region)+"/", strings.NewReader(string(body)))
	if err != nil {
		return awsCreds{}, "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=utf-8")
	signV4(req, base, region, "sts", body, time.Now())
	resp, err := awsHTTP.Do(req)
	if err != nil {
		return awsCreds{}, "", fmt.Errorf("sts assume-role request failed")
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode/100 != 2 {
		return awsCreds{}, "", fmt.Errorf("aws rejected the role assumption")
	}
	var out assumeRoleResp
	if err := xml.Unmarshal(raw, &out); err != nil {
		return awsCreds{}, "", fmt.Errorf("sts assume-role: malformed response")
	}
	creds := awsCreds{
		accessKey:    out.Result.Credentials.AccessKeyID,
		secretKey:    out.Result.Credentials.SecretAccessKey,
		sessionToken: out.Result.Credentials.SessionToken,
	}
	if creds.accessKey == "" {
		return awsCreds{}, "", fmt.Errorf("sts assume-role: no credentials returned")
	}
	return creds, accountFromArn(out.Result.AssumedRoleUser.Arn), nil
}

// accountFromArn extracts the account id from arn:aws:sts::<account>:assumed-role/…
func accountFromArn(arn string) string {
	parts := strings.Split(arn, ":")
	if len(parts) >= 5 {
		return parts[4]
	}
	return ""
}

func (awsDriver) verify(ctx context.Context, cr cred) (identity, error) {
	if strings.TrimSpace(cr.RoleARN) == "" || strings.TrimSpace(cr.ExternalID) == "" {
		return identity{}, fmt.Errorf("roleArn and externalId are required")
	}
	_, account, err := assumeRole(ctx, cr)
	if err != nil {
		return identity{}, err
	}
	return identity{ExternalID: account, Display: cr.RoleARN}, nil
}

type eksClusterInfo struct {
	Name                 string `json:"name"`
	Arn                  string `json:"arn"`
	Status               string `json:"status"`
	Endpoint             string `json:"endpoint"`
	CertificateAuthority struct {
		Data string `json:"data"`
	} `json:"certificateAuthority"`
}

func (awsDriver) discover(ctx context.Context, cr cred) ([]discovered, error) {
	if strings.TrimSpace(cr.RoleARN) == "" || strings.TrimSpace(cr.ExternalID) == "" {
		return nil, fmt.Errorf("roleArn and externalId are required")
	}
	if len(cr.Regions) == 0 {
		return nil, fmt.Errorf("at least one region is required")
	}
	creds, _, err := assumeRole(ctx, cr)
	if err != nil {
		return nil, err
	}
	var out []discovered
	for _, region := range cr.Regions {
		region = strings.TrimSpace(region)
		if region == "" {
			continue
		}
		names, lerr := eksListClusters(ctx, creds, region)
		if lerr != nil {
			// A bad/denied region is not fatal to the whole account.
			continue
		}
		for _, name := range names {
			cl, derr := eksDescribeCluster(ctx, creds, region, name)
			if derr != nil || cl.Status != "ACTIVE" {
				continue
			}
			caPEM, caerr := decodeCA(cl.CertificateAuthority.Data)
			if caerr != nil {
				continue
			}
			token := "k8s-aws-v1." + base64.RawURLEncoding.EncodeToString(
				[]byte(presignGetCallerIdentity(stsEndpoint(region), creds, region, name, 60, time.Now())))
			kube, kerr := buildKubeconfig(name, cl.Endpoint, caPEM, token)
			if kerr != nil {
				continue
			}
			out = append(out, discovered{
				ID:         cl.Arn,
				Name:       name,
				Region:     region,
				Endpoint:   cl.Endpoint,
				Kubeconfig: kube,
			})
		}
	}
	return out, nil
}

func eksListClusters(ctx context.Context, creds awsCreds, region string) ([]string, error) {
	var names []string
	next := ""
	for page := 0; page < awsMaxPages; page++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, eksEndpoint(region)+"/clusters", nil)
		if err != nil {
			return nil, err
		}
		if next != "" {
			req.URL.RawQuery = "nextToken=" + awsURIEscape(next)
		}
		signV4(req, creds, region, "eks", nil, time.Now())
		resp, err := awsHTTP.Do(req)
		if err != nil {
			return nil, fmt.Errorf("eks list clusters request failed")
		}
		body, code := drain(resp)
		if code/100 != 2 {
			return nil, fmt.Errorf("eks list clusters http %d", code)
		}
		var out struct {
			Clusters  []string `json:"clusters"`
			NextToken string   `json:"nextToken"`
		}
		if err := json.Unmarshal(body, &out); err != nil {
			return nil, fmt.Errorf("eks list clusters: malformed response")
		}
		names = append(names, out.Clusters...)
		if out.NextToken == "" {
			break
		}
		next = out.NextToken
	}
	return names, nil
}

func eksDescribeCluster(ctx context.Context, creds awsCreds, region, name string) (eksClusterInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, eksEndpoint(region)+"/clusters/"+url.PathEscape(name), nil)
	if err != nil {
		return eksClusterInfo{}, err
	}
	signV4(req, creds, region, "eks", nil, time.Now())
	resp, err := awsHTTP.Do(req)
	if err != nil {
		return eksClusterInfo{}, fmt.Errorf("eks describe cluster request failed")
	}
	body, code := drain(resp)
	if code/100 != 2 {
		return eksClusterInfo{}, fmt.Errorf("eks describe cluster http %d", code)
	}
	var out struct {
		Cluster eksClusterInfo `json:"cluster"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return eksClusterInfo{}, fmt.Errorf("eks describe cluster: malformed response")
	}
	return out.Cluster, nil
}

func drain(resp *http.Response) ([]byte, int) {
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return b, resp.StatusCode
}
