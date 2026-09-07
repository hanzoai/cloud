// Copyright (c) 2026 Hanzo AI Inc.

package main

// The store's identities are Hanzo IAM's, and it holds none of its own.
//
// github.com/hanzoai/s3 carries the whole mechanism already: an OIDC provider
// verifies a bearer against the issuer's published keys, an STS endpoint trades
// one for the temporary credentials an ordinary AWS client signs with, and the
// policy engine reads that token's own claims. So nothing here authenticates
// anybody. It writes down what this deployment already knows about its IAM and
// hands the store the file.
//
// WHY A ROLE PER ORG, rather than one role and a claim variable. A physical
// bucket carries its org as a HASH of the org's name (apps/provisioning's
// BucketPrefix: "o" + sha256(org)[:16] + "-"), and a policy variable cannot
// compute a hash — ${oidc:owner} expands to the org, never to the prefix that
// names its buckets. So the scope has to be written per org, which is also the
// shape AWS uses for a tenant: the trust policy admits only a token whose own
// `owner` claim IS that org, and the attached policy names only that org's
// prefix. What a caller reaching s3.hanzo.ai directly may touch is then exactly
// what the /v1/s3 surface in front of it would allow, rather than more.

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/provisioning"
	"github.com/hanzoai/cloud/apps/s3"
	"github.com/hanzoai/cloud/internal/environ"
	"github.com/hanzoai/s3/s3/iam/integration"
	"github.com/hanzoai/s3/s3/iam/policy"
	"github.com/hanzoai/s3/s3/s3api"
	luxlog "github.com/luxfi/log"
)

// federation is the provider name a trust policy names. It is this deployment's
// IAM under the name the store knows it by, not a second identity.
const federation = "hanzo"

// sessionKeyRef is where the key that signs STS sessions lives. Those sessions
// ARE credentials, so the key is a secret and belongs in the broker with every
// other one — never beside the data it protects.
const sessionKeyRef = "orgs/hanzo/hanzo-s3/sts-session-key@prod"

// held is the manager the server hands back through s3api.OnIAM. A tenant
// appears while the store is running, so the handle has to outlive the boot that
// produced it.
var held atomic.Pointer[integration.IAMManager]

func init() {
	s3api.OnIAM = func(m *integration.IAMManager) { held.Store(m) }
	s3.Enroll = enroll
}

// configure writes the IAM configuration and answers where it went.
//
// It returns the empty string when this deployment states no issuer: the store
// then runs on the admin credential alone, which is what a developer's laptop
// wants and is a state worth having rather than a boot failure.
func configure(dataDir string) (string, error) {
	issuer := environ.Or("CLOUD_IAM_ISSUER", "")
	if issuer == "" {
		return "", nil
	}
	key, err := sessionKey()
	if err != nil {
		return "", err
	}
	return write(dataDir, document(issuer, key))
}

// document is what the store will read.
//
// Pure, and separate from where the key came from, so what this deployment
// declares about its IAM can be checked without a broker to ask.
func document(issuer string, key []byte) map[string]any {
	return map[string]any{
		"sts": map[string]any{
			// An hour is what a browser upload or a CI job needs; a day is what an
			// exfiltrated credential would like. The ceiling bounds a renewal chain,
			// so a session cannot be extended indefinitely off one sign-in.
			"tokenDuration":    "1h",
			"maxSessionLength": "12h",
			"issuer":           "hanzo-s3",
			"signingKey":       key,
			"providers": []any{map[string]any{
				"name":    federation,
				"type":    "oidc",
				"enabled": true,
				"config": map[string]any{
					"issuer": issuer,
					// The audiences this deployment's own tokens carry, stated rather
					// than inferred: a provider that accepts ANY audience accepts a
					// token minted for somebody else's service. More than one because
					// one IAM issues to several of our own clients — a console bearer
					// and an app bearer name the same person and the same org, and
					// both are ours.
					"clientIds": audiences(),
					"jwksUri":   cloud.JWKSURLFor(issuer),
				},
			}},
		},
		// Deny is the only safe default for a store whose roles are written one
		// tenant at a time: an org with no role yet must reach nothing, not
		// everything.
		"policy": map[string]any{"defaultEffect": "Deny"},
	}
}

// audiences is the allowlist an incoming token's `aud` or `azp` must match.
//
// A LIST, and never empty: the zero value of this setting is the one that
// accepts everything, so an unset variable falls back to the clients this
// deployment is known to issue for rather than to "any".
func audiences() []string {
	var out []string
	for _, a := range strings.Split(environ.Or("IAM_AUDIENCE", "hanzo-cloud,hanzo-app"), ",") {
		if a = strings.TrimSpace(a); a != "" {
			out = append(out, a)
		}
	}
	if len(out) == 0 {
		return []string{"hanzo-cloud"}
	}
	return out
}

// write puts the document where the store's flag will point, readable only by
// the process that runs it — it carries the session signing key.
func write(dataDir string, doc map[string]any) (string, error) {
	raw, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return "", fmt.Errorf("s3 iam config: %w", err)
	}
	path := filepath.Join(dataDir, "iam.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		return "", fmt.Errorf("s3 iam config: %w", err)
	}
	return path, nil
}

// sessionKey reads the STS signing key, minting one the first time.
//
// It is read through the broker rather than generated per boot because a key
// that changed on restart would invalidate every session in flight, and because
// two stores sharing one bucket must agree on it.
func sessionKey() ([]byte, error) {
	ctx := context.Background()
	kms := cloud.KMSPeer{}
	switch v, err := kms.GetSecret(ctx, sessionKeyRef); {
	case err == nil && len(v) > 0:
		return v, nil
	case err != nil && !missing(err):
		return nil, fmt.Errorf("s3 sts key: %w", err)
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("s3 sts key: %w", err)
	}
	if err := kms.PutSecret(ctx, sessionKeyRef, key); err != nil {
		return nil, fmt.Errorf("s3 sts key: %w", err)
	}
	return key, nil
}

// missing reports the broker's "no such secret", which is the one error that is
// not a failure here — it is the first boot.
func missing(err error) bool {
	var notFound interface{ NotFound() bool }
	if errors.As(err, &notFound) {
		return notFound.NotFound()
	}
	return true // the broker reports absence as an opaque error; treat it as first boot
}

// enroll gives one org its role, and is idempotent.
//
// Called where an org first needs storage rather than at boot, because the org
// set is not knowable at boot: this deployment holds thousands of them and only
// the ones that ask for a bucket need a role.
func enroll(ctx context.Context, org string) error {
	m := held.Load()
	if m == nil {
		return nil // no IAM configured on this deployment; the admin credential is the only identity
	}
	name := roleName(org)
	if err := m.CreateRole(ctx, "", name, role(org)); err != nil {
		return fmt.Errorf("s3 enroll %s: %w", org, err)
	}
	if err := m.CreatePolicy(ctx, "", name, scope(org)); err != nil {
		return fmt.Errorf("s3 enroll %s: %w", org, err)
	}
	luxlog.Default().Info("s3 tenant enrolled", "org", org, "prefix", provisioning.BucketPrefix(org))
	return nil
}

// roleName is the one spelling of an org's role, so the role, the policy
// attached to it and the ARN a caller assumes cannot drift apart.
func roleName(org string) string { return "org-" + org }

// role is what the store is told about an org: who may assume it, and nothing
// about what it may then do.
//
// The Condition IS the tenancy boundary. Without it the trust policy admits
// every token the provider authenticates, which is every user of this
// deployment, and one tenant assumes another's role by naming it.
func role(org string) *integration.RoleDefinition {
	name := roleName(org)
	return &integration.RoleDefinition{
		RoleName: name,
		RoleArn:  "arn:aws:iam::role/" + name,
		TrustPolicy: &policy.PolicyDocument{
			Version: "2012-10-17",
			Statement: []policy.Statement{{
				Effect:    "Allow",
				Principal: map[string]interface{}{"Federated": federation},
				Action:    []string{"sts:AssumeRoleWithWebIdentity"},
				Condition: map[string]map[string]interface{}{
					"StringEquals": {"oidc:owner": org},
				},
			}},
		},
		AttachedPolicies: []string{name},
		Description:      "objects belonging to " + org,
	}
}

// scope is what that role may reach: the org's own buckets, named by the prefix
// their names carry, and nothing else.
//
// Both ARNs are needed and they are not the same thing — the first is the bucket
// (which is what a listing acts on) and the second is the objects in it. A policy
// carrying only the second cannot list.
func scope(org string) *policy.PolicyDocument {
	bucket := "arn:aws:s3:::" + provisioning.BucketPrefix(org) + "*"
	return &policy.PolicyDocument{
		Version: "2012-10-17",
		Statement: []policy.Statement{{
			Effect:   "Allow",
			Action:   []string{"s3:*"},
			Resource: []string{bucket, bucket + "/*"},
		}},
	}
}
