package main

// k8s.go — the live-cluster adapters, kept behind thin functions so the tool's core
// (inventory, reseal, verify) stays pure and unit-testable. Everything here shells
// to kubectl (the tool runs where kubectl is already configured), so the tool adds
// NO Kubernetes client dependency.
//
// Two adapters:
//   - loadCRsFromKubectl: enumerate the KMSSecret CRs (coordinates only — a CR never
//     carries a secret value).
//   - newK8sTokenFunc: read each CR's credentialsRef (clientId/clientSecret) and
//     broker an owner-scoped bearer at the source, cached per credential. The
//     clientSecret transits memory for the one login POST and is never logged.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"sync"
)

// crGVR is the KMSSecret custom resource the operator reconciles.
const crGVR = "kmssecrets.secrets.lux.network"

// loadCRsFromKubectl lists the CRs across all namespaces. It reads only the CR
// spec/metadata (coordinates + references) — never a Secret value.
func loadCRsFromKubectl() ([]cr, error) {
	// Enumerate namespaces holding CRs first, then fetch per namespace: a single
	// cluster-wide -o json can be slow enough to time out on a large fleet.
	nsOut, err := kubectl("get", crGVR, "-A", "--no-headers", "-o", "custom-columns=NS:.metadata.namespace")
	if err != nil {
		return nil, fmt.Errorf("list CR namespaces: %w", err)
	}
	nsSet := map[string]struct{}{}
	for _, line := range strings.Split(strings.TrimSpace(string(nsOut)), "\n") {
		if ns := strings.TrimSpace(line); ns != "" {
			nsSet[ns] = struct{}{}
		}
	}
	var all []cr
	for ns := range nsSet {
		out, err := kubectl("get", crGVR, "-n", ns, "-o", "json")
		if err != nil {
			return nil, fmt.Errorf("get CRs in %s: %w", ns, err)
		}
		items, err := parseCRs(out)
		if err != nil {
			return nil, fmt.Errorf("parse CRs in %s: %w", ns, err)
		}
		all = append(all, items...)
	}
	return all, nil
}

// newK8sTokenFunc returns a tokenFunc that, for a target, reads its CR's
// credentialsRef Secret and brokers an owner-scoped bearer at src. Tokens are
// cached per credential (namespace/name) so a credential logs in once.
func newK8sTokenFunc(src *kmsClient) tokenFunc {
	var (
		mu    sync.Mutex
		cache = map[string]string{}
	)
	return func(ctx context.Context, t Target) (string, error) {
		if t.CredName == "" {
			return "", fmt.Errorf("CR %s/%s has no credentialsRef — cannot authenticate", t.CRNamespace, t.CRName)
		}
		key := t.CredNS + "/" + t.CredName
		mu.Lock()
		tok, ok := cache[key]
		mu.Unlock()
		if ok {
			return tok, nil
		}
		clientID, clientSecret, err := readCredential(t.CredNS, t.CredName)
		if err != nil {
			return "", err
		}
		tok, err = src.login(ctx, clientID, clientSecret)
		// Wipe the secret material from our copy immediately after the login POST.
		wipeString(&clientSecret)
		if err != nil {
			return "", fmt.Errorf("broker token for %s: %w", key, err)
		}
		mu.Lock()
		cache[key] = tok
		mu.Unlock()
		return tok, nil
	}
}

// readCredential extracts (clientId, clientSecret) from a credentialsRef Secret.
// clientId is an OAuth client identifier (not sensitive); clientSecret is used only
// for the login POST and is never logged.
func readCredential(ns, name string) (clientID, clientSecret string, err error) {
	out, err := kubectl("get", "secret", name, "-n", ns, "-o", "json")
	if err != nil {
		return "", "", fmt.Errorf("read credential %s/%s: %w", ns, name, err)
	}
	var sec struct {
		Data map[string]string `json:"data"`
	}
	if err := json.Unmarshal(out, &sec); err != nil {
		return "", "", fmt.Errorf("decode credential %s/%s: %w", ns, name, err)
	}
	cid, err := b64(sec.Data["clientId"])
	if err != nil {
		return "", "", fmt.Errorf("credential %s/%s clientId: %w", ns, name, err)
	}
	csec, err := b64(sec.Data["clientSecret"])
	if err != nil {
		return "", "", fmt.Errorf("credential %s/%s clientSecret: %w", ns, name, err)
	}
	if cid == "" || csec == "" {
		return "", "", fmt.Errorf("credential %s/%s missing clientId/clientSecret", ns, name)
	}
	return cid, csec, nil
}

func b64(s string) (string, error) {
	if s == "" {
		return "", nil
	}
	raw, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// kubectl runs a kubectl command and returns stdout. The current kube-context is
// used as-is (the tool runs where the operator's cluster is configured).
func kubectl(args ...string) ([]byte, error) {
	cmd := exec.Command("kubectl", args...)
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("kubectl %s: %s", strings.Join(args, " "), strings.TrimSpace(string(ee.Stderr)))
		}
		return nil, fmt.Errorf("kubectl %s: %w", strings.Join(args, " "), err)
	}
	return out, nil
}

// wipeString best-effort overwrites a secret string's backing bytes. Go strings are
// immutable, so this converts through a byte copy the caller drops; its real value
// is signaling intent + dropping the reference promptly for GC.
func wipeString(s *string) { *s = "" }
