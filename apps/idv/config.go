package idv

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// config.go selects the verification provider from deployment configuration. The
// default is the honest human-in-the-loop Manual provider; naming a real provider
// wires the REST adapter with its KMS-sealed key. Selection is fail-closed: a named
// provider whose key cannot be resolved fails the mount rather than silently
// downgrading to Manual (a silent downgrade would weaken the trust model without
// anyone noticing — the exact "auto-approve by misconfiguration" class of bug).
//
// Configuration (env, resolved by the composition root):
//
//	CLOUD_IDV_PROVIDER  manual (default) | persona | onfido | stripe
//	CLOUD_IDV_BASE_URL  override the provider API root (else the provider default)
//	CLOUD_IDV_KEY_REF   KMS secret reference for the provider bearer (required for a
//	                    real provider; the token itself lives ONLY in KMS)

// SecretFn resolves a KMS-sealed secret by reference. FromConfig is handed one wired
// to deps.KMS.GetSecret so this package never imports the KMS client type.
type SecretFn func(ctx context.Context, ref string) ([]byte, error)

// EnvFn reads a configuration value by name (os.Getenv in production; a map in
// tests). Injected so provider selection is testable without touching the process
// environment.
type EnvFn func(name string) string

// defaultBase is the API root per provider when CLOUD_IDV_BASE_URL is unset.
var defaultBase = map[string]string{
	"persona": "https://api.withpersona.com/api/v1",
	"onfido":  "https://api.onfido.com/v3.6",
	"stripe":  "https://api.stripe.com/v1",
}

// FromConfig returns the configured verification provider. With no provider named it
// returns Manual (never an error — the honest default). With a real provider named
// it returns a REST adapter after PROVING the key resolves now; an unknown provider
// name or an unresolvable key is an error the caller propagates to fail the mount.
func FromConfig(getSecret SecretFn, env EnvFn) (Provider, error) {
	name := strings.ToLower(strings.TrimSpace(env("CLOUD_IDV_PROVIDER")))
	switch name {
	case "", "manual":
		return Manual{}, nil
	case "persona", "onfido", "stripe":
		if getSecret == nil {
			return nil, fmt.Errorf("idv: provider %q requires KMS but none is available", name)
		}
		base := strings.TrimSpace(env("CLOUD_IDV_BASE_URL"))
		if base == "" {
			base = defaultBase[name]
		}
		ref := strings.TrimSpace(env("CLOUD_IDV_KEY_REF"))
		if ref == "" {
			return nil, fmt.Errorf("idv: provider %q requires CLOUD_IDV_KEY_REF (the KMS reference for its API key)", name)
		}
		// Fail closed at mount: verify the key resolves before wiring the provider, so
		// a misconfigured deployment is caught at boot, not on the first verification.
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := getSecret(ctx, ref); err != nil {
			return nil, fmt.Errorf("idv: provider %q key ref %q did not resolve: %w", name, ref, err)
		}
		return &REST{
			name:     name,
			base:     base,
			key:      func(ctx context.Context) ([]byte, error) { return getSecret(ctx, ref) },
			classify: classifierFor(name),
			http:     &http.Client{Timeout: httpTimeout},
		}, nil
	default:
		return nil, fmt.Errorf("idv: unknown provider %q (set manual|persona|onfido|stripe)", name)
	}
}

// classifierFor returns the provider's status-token table. Each table maps ONLY the
// provider's explicit terminal-success tokens to StatusVerified, its explicit
// terminal-failure tokens to StatusRejected, and its explicit human-review tokens to
// StatusReview. Every other token the provider might emit (created, processing,
// submitted, …) is deliberately absent, so classify defaults it to pending — the
// fail-closed guarantee. Tables are small and auditable by design.
func classifierFor(name string) map[string]Status {
	switch name {
	case "persona":
		return map[string]Status{
			"approved":     StatusVerified,
			"completed":    StatusVerified,
			"declined":     StatusRejected,
			"failed":       StatusRejected,
			"needs_review": StatusReview,
			"expired":      StatusExpired,
		}
	case "onfido":
		return map[string]Status{
			"clear":        StatusVerified,
			"consider":     StatusReview,
			"rejected":     StatusRejected,
			"unidentified": StatusRejected,
		}
	case "stripe":
		return map[string]Status{
			"verified":       StatusVerified,
			"canceled":       StatusRejected,
			"requires_input": StatusReview,
		}
	default:
		// A generic strict table (used only if a future provider reuses REST): the
		// union of unambiguous success/fail/review tokens. Unknown → pending.
		return map[string]Status{
			"approved": StatusVerified,
			"verified": StatusVerified,
			"clear":    StatusVerified,
			"passed":   StatusVerified,
			"declined": StatusRejected,
			"rejected": StatusRejected,
			"failed":   StatusRejected,
			"review":   StatusReview,
			"consider": StatusReview,
			"expired":  StatusExpired,
		}
	}
}
