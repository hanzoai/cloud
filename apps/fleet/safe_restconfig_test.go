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
		"https://127.0.0.1:6443",    // loopback
		"https://10.0.0.5:6443",     // private
		"https://169.254.169.254",   // IMDS / link-local
		"https://[::1]:6443",        // IPv6 loopback
		"https://192.168.1.10:6443", // private
		"https://localhost:6443",    // loopback name
	} {
		if _, err := SafeRESTConfig([]byte(kc(server, tokenUser))); err == nil {
			t.Fatalf("non-routable server %q must be rejected", server)
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

func TestSafeRESTConfig_BypassAllowsPrivateHostInTests(t *testing.T) {
	t.Setenv(allowPrivateHostsEnv, "1")
	if _, err := SafeRESTConfig([]byte(kc("https://127.0.0.1:6443", tokenUser))); err != nil {
		t.Fatalf("bypass must allow loopback for test apiservers, got %v", err)
	}
	// The bypass NEVER waives the exec-plugin rejection.
	if _, err := SafeRESTConfig([]byte(kc("https://127.0.0.1:6443", execUser))); err == nil {
		t.Fatal("bypass must not waive the exec-plugin rejection")
	}
}
