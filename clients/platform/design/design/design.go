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

import (
	. "goa.design/goa/v3/dsl"
)

var _ = API("platform", func() {
	Title("Hanzo Platform (PaaS) — /v1/platform")
	Description(`The per-org, user-facing Platform-as-a-Service control plane, ` +
		`native in the unified Hanzo cloud binary (HIP-0106). Users create ` +
		`projects and applications, trigger builds (arcd in-cluster BuildKit), ` +
		`and deploy them (operator hanzo.ai/v1 Service CR into the caller's ` +
		`tenant-<org> namespace). Every route is org-scoped by the ` +
		`gateway-minted, IAM-validated X-Org-Id; a tenant can never read, build, ` +
		`or deploy another org's resources. This is the native Go port that ` +
		`retires the standalone Dokploy (platform.hanzo.ai) tRPC backend.`)
	Version("1.0")
	Server("platform", func() {
		Host("production", func() {
			URI("https://api.hanzo.ai")
		})
	})
})

// ─── shared value types ──────────────────────────────────────────────────────

// RepoSpec is a git source binding for an application (source == "git").
var RepoSpec = Type("RepoSpec", func() {
	Attribute("url", String, "Git remote URL", func() {
		Example("https://github.com/hanzoai/example")
	})
	Attribute("branch", String, "Branch/ref to build", func() { Default("main") })
	// Default is "git" — Hanzo Git (our embedded git.hanzo.ai host). External
	// providers (github/gitlab/bitbucket/codeberg) stay available as sources.
	Attribute("provider", String, "github|gitlab|bitbucket|git", func() { Default("git") })
})

// ImageSpec is a container image binding (source == "image") and the build
// OUTPUT of a git-source app.
var ImageSpec = Type("ImageSpec", func() {
	Attribute("repository", String, "Image repository", func() {
		Example("ghcr.io/hanzoai/example")
	})
	Attribute("tag", String, "Image tag", func() { Example("v1.2.3") })
})

// EnvVar is one application environment variable. Secret values are stored via
// KMS (never plaintext in the metadata store); `secret` marks a KMS-sealed value.
var EnvVar = Type("EnvVar", func() {
	Attribute("key", String, func() { Pattern(`^[A-Za-z_][A-Za-z0-9_]*$`) })
	Attribute("value", String)
	Attribute("secret", Boolean, "true when value is KMS-sealed", func() { Default(false) })
	Required("key")
})

// Project is the org-scoped root of the deploy tree (Dokploy: project).
var Project = Type("Project", func() {
	Attribute("id", String, "proj_<token>")
	Attribute("org", String, "tenant slug (from validated JWT)")
	Attribute("slug", String, "org-unique handle", func() {
		Pattern(`^[a-z0-9]([a-z0-9-]{0,38}[a-z0-9])?$`)
	})
	Attribute("name", String)
	Attribute("description", String)
	Attribute("applications", Int, "count of applications in this project")
	Attribute("createdAt", Int64)
	Attribute("updatedAt", Int64)
	Required("id", "org", "slug", "name", "createdAt", "updatedAt")
})

// Application is a deployable unit under a project (Dokploy: application). It
// binds a source (git|image), a build type, env, port, replicas and domains,
// and deploys as an operator Service CR in the tenant-<org> namespace.
var Application = Type("Application", func() {
	Attribute("id", String, "app_<token>")
	Attribute("org", String)
	Attribute("projectId", String)
	Attribute("slug", String, func() { Pattern(`^[a-z0-9]([a-z0-9-]{0,38}[a-z0-9])?$`) })
	Attribute("name", String)
	Attribute("description", String)
	Attribute("environment", String, "logical env (default production)", func() { Default("production") })
	Attribute("source", String, "git|image", func() { Enum("git", "image") })
	Attribute("repo", RepoSpec)
	Attribute("image", ImageSpec)
	Attribute("buildType", String, "pack|dockerfile|image", func() {
		Enum("pack", "dockerfile", "image")
	})
	Attribute("dockerfile", String, "path to Dockerfile (buildType dockerfile)")
	Attribute("env", ArrayOf(EnvVar))
	Attribute("port", Int, "container port to expose", func() { Default(8080) })
	Attribute("replicas", Int, func() { Default(1) })
	Attribute("domains", ArrayOf(String), "ingress hosts")
	Attribute("status", String, "draft|building|deploying|live|stopped|error")
	Attribute("namespace", String, "tenant-<org> (derived, read-only)")
	Attribute("currentDeploymentId", String)
	Attribute("createdAt", Int64)
	Attribute("updatedAt", Int64)
	Required("id", "org", "projectId", "slug", "name", "source", "status", "createdAt", "updatedAt")
})

// Deployment is one immutable build+deploy attempt for an application (Dokploy:
// deployment), versioned monotonically. 1:1 with a Build for git-source apps.
var Deployment = Type("Deployment", func() {
	Attribute("id", String, "dep_<token>")
	Attribute("org", String)
	Attribute("applicationId", String)
	Attribute("version", Int, "monotonic per application")
	Attribute("status", String, "queued|building|deploying|live|error|canceled")
	Attribute("source", String, "git|image|manual")
	Attribute("commit", String)
	Attribute("image", String, "resolved image ref deployed")
	Attribute("buildId", String, "bld_<token> (git-source)")
	Attribute("message", String, "error or note")
	Attribute("createdAt", Int64)
	Attribute("updatedAt", Int64)
	Required("id", "org", "applicationId", "version", "status", "source", "createdAt", "updatedAt")
})

// Build is one arcd (in-cluster BuildKit) build record producing an image
// (Dokploy fork: build_job).
var Build = Type("Build", func() {
	Attribute("id", String, "bld_<token>")
	Attribute("org", String)
	Attribute("applicationId", String)
	Attribute("deploymentId", String)
	Attribute("status", String, "queued|building|succeeded|failed|canceled")
	Attribute("image", String, "output image ref")
	Attribute("jobName", String, "in-cluster BuildKit Job name")
	Attribute("logsRef", String, "log location")
	Attribute("createdAt", Int64)
	Attribute("updatedAt", Int64)
	Required("id", "org", "applicationId", "status", "createdAt", "updatedAt")
})

// ─── error + shared HTTP responses ───────────────────────────────────────────

var _ = Service("platform", func() {
	Description("Per-org PaaS control plane (/v1/platform).")

	Error("unauthorized", func() { Description("missing/invalid IAM bearer token") })
	Error("forbidden", func() { Description("no validated org, or cross-tenant access") })
	Error("not_found")
	Error("conflict", func() { Description("slug already exists in this org") })
	Error("bad_request")
	Error("unavailable", func() { Description("cluster/build backend not reachable (fail-closed)") })

	HTTP(func() {
		Response("unauthorized", StatusUnauthorized)
		Response("forbidden", StatusForbidden)
		Response("not_found", StatusNotFound)
		Response("conflict", StatusConflict)
		Response("bad_request", StatusBadRequest)
		Response("unavailable", StatusServiceUnavailable)
	})

	// ── projects ──
	Method("listProjects", func() {
		Description("List the caller org's projects, newest-updated first.")
		Payload(func() { orgHeader() })
		Result(ArrayOf(Project))
		HTTP(func() {
			GET("/v1/platform/projects")
			Response(StatusOK)
		})
	})
	Method("createProject", func() {
		Payload(func() {
			orgHeader()
			Attribute("name", String)
			Attribute("slug", String)
			Attribute("description", String)
			Required("name")
		})
		Result(Project)
		HTTP(func() {
			POST("/v1/platform/projects")
			Response(StatusCreated)
		})
	})
	Method("getProject", func() {
		Payload(func() {
			orgHeader()
			Attribute("slug", String)
			Required("slug")
		})
		Result(Project)
		HTTP(func() {
			GET("/v1/platform/projects/{slug}")
			Response(StatusOK)
		})
	})
	Method("deleteProject", func() {
		Payload(func() {
			orgHeader()
			Attribute("slug", String)
			Required("slug")
		})
		HTTP(func() {
			DELETE("/v1/platform/projects/{slug}")
			Response(StatusNoContent)
		})
	})

	// ── applications ──
	Method("listApplications", func() {
		Description("List a project's applications.")
		Payload(func() {
			orgHeader()
			Attribute("project", String)
			Required("project")
		})
		Result(ArrayOf(Application))
		HTTP(func() {
			GET("/v1/platform/projects/{project}/apps")
			Response(StatusOK)
		})
	})
	Method("createApplication", func() {
		Payload(func() {
			orgHeader()
			Attribute("project", String)
			Attribute("name", String)
			Attribute("slug", String)
			Attribute("description", String)
			Attribute("source", String, func() { Enum("git", "image") })
			Attribute("repo", RepoSpec)
			Attribute("image", ImageSpec)
			Attribute("buildType", String)
			Attribute("port", Int)
			Attribute("replicas", Int)
			Attribute("env", ArrayOf(EnvVar))
			Attribute("domains", ArrayOf(String))
			Required("project", "name", "source")
		})
		Result(Application)
		HTTP(func() {
			POST("/v1/platform/projects/{project}/apps")
			Response(StatusCreated)
		})
	})
	Method("getApplication", func() {
		Payload(func() {
			orgHeader()
			Attribute("project", String)
			Attribute("app", String)
			Required("project", "app")
		})
		Result(Application)
		HTTP(func() {
			GET("/v1/platform/projects/{project}/apps/{app}")
			Response(StatusOK)
		})
	})
	Method("deleteApplication", func() {
		Payload(func() {
			orgHeader()
			Attribute("project", String)
			Attribute("app", String)
			Required("project", "app")
		})
		HTTP(func() {
			DELETE("/v1/platform/projects/{project}/apps/{app}")
			Response(StatusNoContent)
		})
	})

	// ── deploy lifecycle ──
	Method("deploy", func() {
		Description("Build (git-source, via arcd) and deploy an application " +
			"(operator Service CR into tenant-<org>). Returns the new Deployment.")
		Payload(func() {
			orgHeader()
			Attribute("project", String)
			Attribute("app", String)
			Attribute("commit", String, "git commit to build (git-source)")
			Attribute("tag", String, "image tag to deploy (image-source)")
			Required("project", "app")
		})
		Result(Deployment)
		HTTP(func() {
			POST("/v1/platform/projects/{project}/apps/{app}/deploy")
			Response(StatusAccepted)
		})
	})
	Method("stop", func() {
		Description("Scale an application to zero (operator CR replicas=0).")
		Payload(func() {
			orgHeader()
			Attribute("project", String)
			Attribute("app", String)
			Required("project", "app")
		})
		Result(Application)
		HTTP(func() {
			POST("/v1/platform/projects/{project}/apps/{app}/stop")
			Response(StatusOK)
		})
	})
	Method("start", func() {
		Description("Scale an application back to its declared replicas.")
		Payload(func() {
			orgHeader()
			Attribute("project", String)
			Attribute("app", String)
			Required("project", "app")
		})
		Result(Application)
		HTTP(func() {
			POST("/v1/platform/projects/{project}/apps/{app}/start")
			Response(StatusOK)
		})
	})

	// ── deployments + builds (history/logs) ──
	Method("listDeployments", func() {
		Payload(func() {
			orgHeader()
			Attribute("project", String)
			Attribute("app", String)
			Required("project", "app")
		})
		Result(ArrayOf(Deployment))
		HTTP(func() {
			GET("/v1/platform/projects/{project}/apps/{app}/deployments")
			Response(StatusOK)
		})
	})
	Method("getDeployment", func() {
		Payload(func() {
			orgHeader()
			Attribute("project", String)
			Attribute("app", String)
			Attribute("id", String)
			Required("project", "app", "id")
		})
		Result(Deployment)
		HTTP(func() {
			GET("/v1/platform/projects/{project}/apps/{app}/deployments/{id}")
			Response(StatusOK)
		})
	})
	Method("deploymentLogs", func() {
		Description("Build+deploy logs for a deployment (arcd Job logs + rollout).")
		Payload(func() {
			orgHeader()
			Attribute("project", String)
			Attribute("app", String)
			Attribute("id", String)
			Required("project", "app", "id")
		})
		Result(func() {
			Attribute("deploymentId", String)
			Attribute("logs", String)
			Required("deploymentId", "logs")
		})
		HTTP(func() {
			GET("/v1/platform/projects/{project}/apps/{app}/deployments/{id}/logs")
			Response(StatusOK)
		})
	})

	// ── health (real cluster reachability, fail-closed) ──
	Method("health", func() {
		Payload(func() {})
		Result(func() {
			Attribute("service", String)
			Attribute("status", String)
			Attribute("k8s", Boolean)
			Required("service", "status")
		})
		HTTP(func() {
			GET("/v1/platform/health")
			Response(StatusOK)
			Response("unavailable", StatusServiceUnavailable)
		})
	})
})

// orgHeader is intentionally a no-op in the client-facing contract.
//
// Tenancy is NOT a client parameter: the browser must never send X-Org-Id (the
// edge strips any client copy), and the gateway injects it from the validated
// JWT `owner` claim (HIP-0026/0111). Exposing it as an OpenAPI parameter would
// be misleading, so it is omitted from every payload. The zip runtime reads the
// gateway-minted, SanitizeIdentity-validated X-Org-Id via c.Org(); every method
// scopes to it, and no route trusts an org from the body or path. This helper
// documents that contract at each call site without polluting the generated
// OpenAPI.
func orgHeader() {}
