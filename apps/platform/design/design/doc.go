// Package design is the design-first Goa v3 specification for the Hanzo
// Platform (PaaS) API served at /v1/platform by the unified cloud binary.
//
// WHY GOA-DESIGN-BUT-ZIP-RUNTIME (the deliberate architecture choice):
//
// The user's directive was "compile for goa backend so it runs in go process."
// The cloud binary's ONE HTTP router is github.com/zap-proto/zip (a Fiber-style
// app); every subsystem (functions, prompts, projects, paas, s3, provisioning)
// mounts native zip handlers behind the SanitizeIdentity trust-boundary
// middleware that VALIDATES the caller's org/admin claims (HIP-0026/0111).
// Mounting a Goa-generated net/http server via zip's adaptor is explicitly a
// "migration tool" (zip/adapt.go) and would make the identity trust boundary
// depend on fragile fiber<->net/http header propagation — a second HTTP stack
// that violates "one and only one way."
//
// So Goa is used design-first as the AUTHORITATIVE CONTRACT: this DSL is the
// single source of truth for the /v1/platform surface, and `goa gen` emits the
// OpenAPI 3 document (gen/http/openapi3.*) that console and external clients
// consume. The runtime handlers implement this exact contract natively on zip
// (clients/platform/*.go), staying behind SanitizeIdentity and keeping the
// binary's go.mod free of any goa.design runtime dependency. Design → Go
// handlers in the cloud process, one router, identity boundary intact.
//
// This module is DELIBERATELY nested (its own go.mod) so `go build ./...` from
// the cloud root never descends here and never takes on goa.design/goa/v3.
package design
