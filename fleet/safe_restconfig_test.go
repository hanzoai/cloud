// Copyright 2025 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package fleet

import (
	"strings"
	"testing"
)

// SafeRESTConfig is the ONE fold-safety gate (discovery folds AND hand-pasted BYO
// attach both funnel through it). These pin the two exploit classes Red found:
// exec/auth-provider credential plugins (RCE via the pod's env) and an apiserver
// host that is not a publicly routable https endpoint (SSRF on the ACTUAL dial
// target).

func kc(server, userBlock string) string {
	return `apiVersion: v1
kind: Config
clusters:
- cluster:
    server: ` + server + `
    insecure-skip-tls-verify: true
  name: c
contexts:
- context: {cluster: c, user: u}
  name: c
current-context: c
users:
- name: u
  user:
` + userBlock
}

const tokenUser = "    token: t"

const execUser = `    exec:
      apiVersion: client.authentication.k8s.io/v1beta1
      command: /bin/sh
      args: ["-c", "cat /var/run/secrets/kubernetes.io/serviceaccount/token"]`

const authProviderUser = `    auth-provider:
      name: gcp`

func TestSafeRESTConfig_RejectsExecPlugin(t *testing.T) {
	_, err := SafeRESTConfig([]byte(kc("https://8.8.8.8:6443", execUser)))
	if err == nil || !strings.Contains(err.Error(), "exec credential plugin") {
		t.Fatalf("exec kubeconfig must be rejected, got %v", err)
	}
}

func TestSafeRESTConfig_RejectsAuthProvider(t *testing.T) {
	_, err := SafeRESTConfig([]byte(kc("https://8.8.8.8:6443", authProviderUser)))
	if err == nil || !strings.Contains(err.Error(), "auth-provider plugin") {
		t.Fatalf("auth-provider kubeconfig must be rejected, got %v", err)
	}
}

func TestSafeRESTConfig_RejectsNonRoutableHost(t *testing.T) {
	for _, server := range []string{
		"https://127.0.0.1:6443",            // loopback
		"https://10.0.0.5:6443",             // private
		"https://169.254.169.254",           // IMDS / link-local
		"https://[::1]:6443",                // IPv6 loopback
		"https://192.168.1.10:6443",         // private
		"https://localhost:6443",            // loopback name
		"https://100.100.100.200",           // Alibaba metadata, in CGNAT
		"https://[64:ff9b::a9fe:a9fe]:6443", // the metadata endpoint spelled as NAT64
	} {
		if _, err := SafeRESTConfig([]byte(kc(server, tokenUser))); err == nil {
			t.Fatalf("non-routable server %q must be rejected", server)
		}
	}
}

// client-go dials a cluster's proxy-url rather than its server, so a proxy is an
// address the host check never judged.
func TestSafeRESTConfig_RejectsAProxy(t *testing.T) {
	for _, proxy := range []string{"http://169.254.169.254:80", "socks5://10.0.0.1:1080", "https://8.8.4.4"} {
		k := strings.Replace(kc("https://8.8.8.8:6443", tokenUser),
			"insecure-skip-tls-verify: true", "insecure-skip-tls-verify: true\n    proxy-url: "+proxy, 1)
		if _, err := SafeRESTConfig([]byte(k)); err == nil {
			t.Errorf("a kubeconfig naming proxy %s was accepted", proxy)
		}
	}
}

func TestSafeRESTConfig_RejectsNonHTTPS(t *testing.T) {
	if _, err := SafeRESTConfig([]byte(kc("http://8.8.8.8:6443", tokenUser))); err == nil {
		t.Fatal("non-https apiserver must be rejected")
	}
}

func TestSafeRESTConfig_AllowsPublicHTTPS(t *testing.T) {
	cfg, err := SafeRESTConfig([]byte(kc("https://8.8.8.8:6443", tokenUser)))
	if err != nil || cfg == nil {
		t.Fatalf("a public https token kubeconfig must be accepted, got %v", err)
	}
	if cfg.ExecProvider != nil || cfg.AuthProvider != nil {
		t.Fatal("accepted config must carry no credential plugin")
	}
}
