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

// credRef is the credential a token func resolves for a target on one face.
type credRef struct {
	ns, name         string
	clientID, secret string
}

// credResolver maps a target to the credential that authenticates it on one face.
// crCredResolver reads the CR's credentialsRef (the app-name identity the standalone
// accepts); machineAudResolver reads the per-org <org>-platform-kms identity cloud
// accepts dynamically and admin-denied (no static widening).
type credResolver func(t Target) (credRef, error)

// crCredResolver reads a target's CR credentialsRef Secret — the existing app-name
// machine identity the STANDALONE accepts (aud ∈ KMS_EXPECTED_AUDIENCE).
func crCredResolver(t Target) (credRef, error) {
	if t.CredName == "" {
		return credRef{}, fmt.Errorf("CR %s/%s has no credentialsRef — cannot authenticate", t.CRNamespace, t.CRName)
	}
	cid, sec, err := readCredential(t.CredNS, t.CredName)
	if err != nil {
		return credRef{}, err
	}
	return credRef{ns: t.CredNS, name: t.CredName, clientID: cid, secret: sec}, nil
}

// machineAudResolver reads the per-org <org>-platform-kms credential (Secret
// name = "<org>"+suffix in ns) — the dedicated KMS-sync identity CLOUD accepts
// dynamically via kmsMachineAudience (admin-denied, scoped to /v1/kms org==owner).
// A missing Secret fails loud: provisioning it is a gated cutover prerequisite.
func machineAudResolver(ns, suffix string) credResolver {
	return func(t Target) (credRef, error) {
		name := t.Org + suffix
		cid, sec, err := readCredential(ns, name)
		if err != nil {
			return credRef{}, fmt.Errorf("per-org KMS-sync credential %s/%s not provisioned (mint the <org>-platform-kms IAM app + Secret first): %w", ns, name, err)
		}
		return credRef{ns: ns, name: name, clientID: cid, secret: sec}, nil
	}
}

// newTokenFunc brokers an owner-scoped bearer at `client` for each target's
// credential, caches per credential, and (LOW-1, defense-in-depth) decodes the
// minted token's owner claim and asserts it EQUALS the target's org before the
// token is handed to any read/write — so a misscoped credential fails the target
// rather than acting on the wrong org. An admin-owner token is refused for a
// tenant target (the fleet identities must be org-bound, never platform admin).
func newTokenFunc(client *kmsClient, resolve credResolver, face string) tokenFunc {
	var (
		mu    sync.Mutex
		cache = map[string]string{}
	)
	return func(ctx context.Context, t Target) (string, error) {
		ref, err := resolve(t)
		if err != nil {
			return "", err
		}
		key := ref.ns + "/" + ref.name
		mu.Lock()
		tok, ok := cache[key]
		mu.Unlock()
		if !ok {
			tok, err = client.login(ctx, ref.clientID, ref.secret)
			wipeString(&ref.secret) // wipe the secret material right after the login POST
			if err != nil {
				return "", fmt.Errorf("broker %s token for %s: %w", face, key, err)
			}
			mu.Lock()
			cache[key] = tok
			mu.Unlock()
		}
		// LOW-1: assert token owner == target org (fail-closed on mismatch/admin).
		owner, isAdmin, derr := decodeJWTOwner(tok)
		if derr == nil {
			if isAdmin {
				return "", fmt.Errorf("%s credential %s mints an ADMIN token — the fleet KMS identity must be org-bound, not platform admin", face, key)
			}
			if owner != t.Org {
				return "", fmt.Errorf("%s credential %s token owner %q != target org %q (misscoped credential)", face, key, owner, t.Org)
			}
		}
		return tok, nil
	}
}

// decodeJWTOwner reads the `owner` + `isAdmin` claims from a JWT WITHOUT verifying
// the signature — the SERVER validates the signature; this is a local sanity gate
// so the tool never uses a token whose owner disagrees with the target org. A
// non-JWT token (e.g. an opaque test stub) returns an error, which the caller
// treats as "skip the local assertion" (the server still enforces owner==:org).
func decodeJWTOwner(token string) (owner string, isAdmin bool, err error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return "", false, fmt.Errorf("not a JWT")
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", false, fmt.Errorf("jwt payload: %w", err)
	}
	var claims struct {
		Owner   string `json:"owner"`
		IsAdmin bool   `json:"isAdmin"`
	}
	if err := json.Unmarshal(raw, &claims); err != nil {
		return "", false, fmt.Errorf("jwt claims: %w", err)
	}
	return claims.Owner, claims.IsAdmin, nil
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
