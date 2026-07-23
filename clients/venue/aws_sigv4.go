// Copyright 2025 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package venue

// aws_sigv4.go is a small, self-contained AWS Signature Version 4 signer (stdlib
// crypto only) — so venue talks to STS and EKS over plain net/http without the
// aws-sdk-go-v2 service trees. One signer, service-parameterized (sts / eks),
// covering the header-signed calls (AssumeRole, GetCallerIdentity, ListClusters,
// DescribeCluster) and the query-signed presign (the EKS "k8s-aws-v1." token).
// Correctness is pinned by the canonical AWS "get-vanilla" test vector
// (aws_sigv4_test.go), which the stubs cannot exercise (they don't validate
// signatures).

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

const sigV4Algo = "AWS4-HMAC-SHA256"

// awsCreds is a base (platform) or assumed (temporary) credential set.
type awsCreds struct {
	accessKey, secretKey, sessionToken string
}

func hmacSHA256(key []byte, data string) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(data))
	return h.Sum(nil)
}

func sha256Hex(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func signingKey(secret, dateStamp, region, service string) []byte {
	kDate := hmacSHA256([]byte("AWS4"+secret), dateStamp)
	kRegion := hmacSHA256(kDate, region)
	kService := hmacSHA256(kRegion, service)
	return hmacSHA256(kService, "aws4_request")
}

// canonicalURI is the URI-encoded path (single-encoded; our paths are simple).
func canonicalURI(u *url.URL) string {
	p := u.EscapedPath()
	if p == "" {
		return "/"
	}
	return p
}

// signV4 signs req in place with the Authorization header for (region, service).
// body is the exact request payload already set on req. It signs a fixed header
// set (host, x-amz-date, and — when present — content-type, x-amz-security-token,
// x-k8s-aws-id), which is all these STS/EKS calls carry.
func signV4(req *http.Request, c awsCreds, region, service string, body []byte, now time.Time) {
	amzDate := now.UTC().Format("20060102T150405Z")
	dateStamp := now.UTC().Format("20060102")
	req.Header.Set("X-Amz-Date", amzDate)
	if c.sessionToken != "" {
		req.Header.Set("X-Amz-Security-Token", c.sessionToken)
	}

	signed := map[string]string{
		"host":       req.URL.Host,
		"x-amz-date": amzDate,
	}
	if ct := req.Header.Get("Content-Type"); ct != "" {
		signed["content-type"] = ct
	}
	if c.sessionToken != "" {
		signed["x-amz-security-token"] = c.sessionToken
	}
	if v := req.Header.Get("X-K8s-Aws-Id"); v != "" {
		signed["x-k8s-aws-id"] = v
	}

	names := make([]string, 0, len(signed))
	for k := range signed {
		names = append(names, k)
	}
	sort.Strings(names)
	var ch strings.Builder
	for _, n := range names {
		ch.WriteString(n)
		ch.WriteByte(':')
		ch.WriteString(strings.TrimSpace(signed[n]))
		ch.WriteByte('\n')
	}
	signedHeaders := strings.Join(names, ";")

	canonicalReq := req.Method + "\n" +
		canonicalURI(req.URL) + "\n" +
		req.URL.RawQuery + "\n" +
		ch.String() + "\n" +
		signedHeaders + "\n" +
		sha256Hex(body)

	scope := dateStamp + "/" + region + "/" + service + "/aws4_request"
	stringToSign := sigV4Algo + "\n" + amzDate + "\n" + scope + "\n" + sha256Hex([]byte(canonicalReq))
	sig := hex.EncodeToString(hmacSHA256(signingKey(c.secretKey, dateStamp, region, service), stringToSign))
	req.Header.Set("Authorization", sigV4Algo+" Credential="+c.accessKey+"/"+scope+
		", SignedHeaders="+signedHeaders+", Signature="+sig)
}

// awsURIEscape encodes a query value per AWS canonical rules (RFC-3986; space as
// %20, and it does NOT escape the unreserved set). url.QueryEscape uses '+' for
// space, which SigV4 rejects, so we post-process.
func awsURIEscape(s string) string {
	e := url.QueryEscape(s)
	e = strings.ReplaceAll(e, "+", "%20")
	return e
}

// presignGetCallerIdentity returns the presigned STS GetCallerIdentity URL that
// is the EKS "k8s-aws-v1." bearer body: a query-signed GET whose signed headers
// are host;x-k8s-aws-id (the cluster name). Signed with the assumed identity so
// the cluster's access entries authorize it.
func presignGetCallerIdentity(endpoint string, c awsCreds, region, clusterName string, expires int, now time.Time) string {
	u, err := url.Parse(endpoint)
	if err != nil {
		return ""
	}
	amzDate := now.UTC().Format("20060102T150405Z")
	dateStamp := now.UTC().Format("20060102")
	scope := dateStamp + "/" + region + "/sts/aws4_request"
	signedHeaders := "host;x-k8s-aws-id"

	// Canonical query — sorted keys, AWS-escaped values, joined by '&'.
	params := map[string]string{
		"Action":              "GetCallerIdentity",
		"Version":             "2011-06-15",
		"X-Amz-Algorithm":     sigV4Algo,
		"X-Amz-Credential":    c.accessKey + "/" + scope,
		"X-Amz-Date":          amzDate,
		"X-Amz-Expires":       strconv.Itoa(expires),
		"X-Amz-SignedHeaders": signedHeaders,
	}
	if c.sessionToken != "" {
		params["X-Amz-Security-Token"] = c.sessionToken
	}
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var q strings.Builder
	for i, k := range keys {
		if i > 0 {
			q.WriteByte('&')
		}
		q.WriteString(awsURIEscape(k))
		q.WriteByte('=')
		q.WriteString(awsURIEscape(params[k]))
	}
	canonicalQuery := q.String()

	canonicalHeaders := "host:" + u.Host + "\n" + "x-k8s-aws-id:" + clusterName + "\n"
	canonicalReq := "GET\n" + canonicalURI(u) + "\n" + canonicalQuery + "\n" +
		canonicalHeaders + "\n" + signedHeaders + "\n" + "UNSIGNED-PAYLOAD"
	stringToSign := sigV4Algo + "\n" + amzDate + "\n" + scope + "\n" + sha256Hex([]byte(canonicalReq))
	sig := hex.EncodeToString(hmacSHA256(signingKey(c.secretKey, dateStamp, region, "sts"), stringToSign))
	return u.String() + "?" + canonicalQuery + "&X-Amz-Signature=" + sig
}
