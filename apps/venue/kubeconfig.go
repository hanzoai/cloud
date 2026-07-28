// Copyright 2025 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package venue

// kubeconfig.go builds a foldable kubeconfig from an apiserver endpoint, a CA,
// and a bearer token — the shape AWS (EKS) and GCP (GKE) discovery produce (DO
// hands back a ready kubeconfig, so it never calls this). Built via the typed
// clientcmd/api model + clientcmd.Write, so the endpoint/CA/token can never
// inject YAML.

import (
	"encoding/base64"
	"fmt"
	"strings"

	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

// buildKubeconfig renders a single-context kubeconfig. caData is raw PEM (already
// base64-DECODED from the provider's describe response); clientcmd.Write re-encodes
// it. A bearer token authenticates the fold's immediate node inventory; discovery
// re-mints it on each sync (provider tokens are short-lived by design).
func buildKubeconfig(name, endpoint string, caData []byte, bearer string) ([]byte, error) {
	if strings.TrimSpace(endpoint) == "" {
		return nil, fmt.Errorf("empty apiserver endpoint")
	}
	cfg := clientcmdapi.NewConfig()
	cfg.Clusters[name] = &clientcmdapi.Cluster{
		Server:                   endpoint,
		CertificateAuthorityData: caData,
	}
	cfg.AuthInfos[name] = &clientcmdapi.AuthInfo{Token: bearer}
	cfg.Contexts[name] = &clientcmdapi.Context{Cluster: name, AuthInfo: name}
	cfg.CurrentContext = name
	return clientcmd.Write(*cfg)
}

// decodeCA decodes a base64 CA (EKS certificateAuthority.data / GKE
// masterAuth.clusterCaCertificate) into raw PEM. An empty input yields nil (a
// cluster with insecure-skip would be rejected at fold — we never fabricate a CA).
func decodeCA(b64 string) ([]byte, error) {
	b64 = strings.TrimSpace(b64)
	if b64 == "" {
		return nil, nil
	}
	pem, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, fmt.Errorf("cluster CA is not valid base64")
	}
	return pem, nil
}
