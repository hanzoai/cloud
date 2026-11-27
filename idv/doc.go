// Package idv is identity verification: run a KYC or KYB check through a licensed
// provider.
//
// It is the ONE identity/business verification client the Hanzo cloud binary uses to
// orchestrate that check. It is deliberately small and provider-agnostic so every consumer wires
// the SAME contract: company formation (founder KYC) and the compliance product
// (org-side onboarding KYB/KYC) both drive verifications through a idv.Provider, and
// a real provider (Persona, Onfido, Stripe Identity) is a config-driven swap for the
// honest human-in-the-loop default — no consumer branches on which is wired.
//
// THE BOUNDARY (a design invariant, not a comment). This package ORCHESTRATES a
// licensed verification provider and TRACKS what the provider reported. It never
// asserts a subject is "compliant" or "verified" on its own authority: the only
// terminal status it can produce is a status the PROVIDER returned. The default
// (Manual) provider NEVER auto-approves — it returns pending and waits for an
// out-of-band decision recorded by a human reviewer or a provider webhook. Every
// status in this package is provider-reported or pending, never platform-asserted.
//
// FAIL-CLOSED. Every path that cannot obtain a positive provider decision resolves
// to a NON-terminal status (pending/review) or an error — never to verified. A
// transport error, a non-2xx response, an unparseable body, or an unrecognized
// provider status all map to pending. A verification can only reach provider_verified
// through an explicit, recognized provider success token (see classify).
package idv
