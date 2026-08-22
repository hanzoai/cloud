package ml

// typed.go is ml's typed-op surface: the reads and the deregistration of the one
// kserve CustomResource this subsystem bridges, declared as zip typed ops so ONE
// registration is the whole contract — the REST route, the OpenAPI operation, the
// MCP tool, the CLI command and every generated SDK method all project from it.
// An untyped route appends nothing to that registry and is therefore invisible to
// all five, which is what these conversions buy.
//
// Four of ml's seven operations are NOT here. Each is wire-bound, not
// un-migrated: create answers 402/503 in band with the fleet's nested
// billing-denial contract, PATCH relays an opaque RFC 7386 merge patch verbatim
// to the Kubernetes API, predict returns the predictor's own status, bytes and
// Content-Type, and health answers 503 carrying the degraded REPORT as its body.
// typed_wire_test.go holds that closed list and fails on any route that is
// neither typed nor named in it.

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/zap-proto/zip"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// zipdoc lifts the doc comment off each typed op and off every In/Out field into
// zipdoc_gen.go, which is the ONLY way that prose reaches the published document
// and the MCP tool list — Go drops comments at compile time. Run by
// `make -C apps/ml openapi` (generate is a prerequisite of build).
//
//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

// ops binds the service to ml's typed ops. A TypedHandler is
// func(context.Context, *In) (*Out, error) — it has no parameter for the service
// — so the service arrives as a RECEIVER and every op is a method value
// (o.listModels), which is also the only bound form cmd/zipdoc can lift prose
// from: a closure returned by a helper is a call expression with nothing to read.
type ops struct{ s *cloud.Service[state] }

// tenantFrom is tenant() for a typed op, and it needs the REQUEST rather than
// only the org. ml's tenant boundary is a per-org(+project) KUBERNETES NAMESPACE,
// and deriving it takes two facts principal.OrgFrom does not carry: the org
// SUB-SCOPE (X-Project-Id, which suffixes the namespace) and platform-admin-ness
// (X-User-IsAdmin, which buckets an org-less admin under "ml-admin" — OrgFrom
// refuses an empty org outright, so reading the tenant through it alone would
// turn that live admin bucket into a 403).
//
// FAIL CLOSED off the HTTP path: a CLI LocalInvoke has no request, so there is no
// validated principal and no namespace to name — the same 403 the untyped
// handlers give a forged X-Org-Id, from the same line, with no second gate to
// keep in sync.
func tenantFrom(ctx context.Context) (ns, org, project string, err error) {
	c, ok := cloud.Request(ctx)
	if !ok {
		return "", "", "", zip.ErrForbidden("no validated principal")
	}
	return tenant(c)
}

// ── the shapes the ops take and give ─────────────────────────────────────────
//
// A typed op's Go type name IS its schema name, and the fleet's schema namespace
// is FLAT — openapi.Compose refuses one name with two shapes across apps, because a
// generated SDK would bind whichever it read last. So every name below carries
// the product the namespace cannot: the obvious "resource", "resourceList" and
// "ref" are exactly the names another app will reach for next.

// mlNoInput is the In of an op that takes nothing off the wire — no body, no
// query parameter, no path segment. Its whole input is the caller's validated
// principal, which is what decides the tenant namespace it reads.
type mlNoInput struct{}

// mlRef addresses one resource by name. The name is the path segment: the URL is
// the addressing authority, so it binds from there whatever a body says.
type mlRef struct {
	// Name is the resource to act on, taken from the path. Lower-cased and
	// trimmed to the DNS-1123 label a CustomResource's metadata.name must be.
	Name string `json:"name"`
}

// mlResource is one CustomResource as this API renders it: an honest, non-bloated
// projection — the name, when Kubernetes admitted it, the operator-owned live
// status, and the spec on a single-object read. The Kubernetes namespace is
// deliberately absent, because the namespace IS the tenant boundary and an
// internal detail no tenant needs.
//
// Spec and Status are POINTERS because the wire distinguishes an absent key from
// a present-but-empty object: a resource the operator has not reconciled yet has
// no status at all, and omitting the key is what says so — where a plain map with
// `omitempty` would also drop a `{}` the resource really carries.
//
// The fields are in ALPHABETICAL order on purpose. This view replaced a
// map[string]any, encoding/json sorts a map's keys, and struct fields marshal in
// declaration order — so the order here is what keeps the bytes identical to what
// this route has always sent. Insert a new field alphabetically.
type mlResource struct {
	// CreatedAt is when Kubernetes admitted the object, RFC 3339 in UTC.
	CreatedAt string `json:"createdAt"`
	// Name is the object's metadata.name, unique within the caller's namespace.
	Name string `json:"name"`
	// Spec is the resource spec, verbatim as Kubernetes stores it. Present on a
	// single-object read, absent from a list.
	Spec *map[string]any `json:"spec,omitempty"`
	// Status is the live status kserve owns, verbatim. Absent until kserve has
	// written one.
	Status *map[string]any `json:"status,omitempty"`
}

// mlResourceList is every object of one kind in the caller's tenant namespace, in
// the list shape (no spec). Items is never null — an empty tenant is an empty
// array.
type mlResourceList struct {
	// Items is one entry per object, newest LAST (the Kubernetes list order).
	Items []mlResource `json:"items"`
}

// ── the shared bodies ──────────────────────────────────────────────────
//
// The resource family has ONE CRUD shape, so the bodies below are written once
// and parameterised by resourceKind. The per-family ops are thin method values
// over them, which is what zipdoc needs to lift prose — and the parameterisation
// stays because it is the shape, not a count of callers.

// listOf lists every object of one kind in the caller's tenant namespace. A
// namespace that does not exist yet is an EMPTY LIST, not a 404: an org that has
// created nothing has nothing, which is not an error.
func (o ops) listOf(ctx context.Context, k resourceKind) (*mlResourceList, error) {
	if err := ready(o.s); err != nil {
		return nil, err
	}
	ns, _, _, err := tenantFrom(ctx)
	if err != nil {
		return nil, err
	}
	ul, err := o.s.State.dyn.Resource(k.gvr).Namespace(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) { // tenant namespace not created yet
			return &mlResourceList{Items: []mlResource{}}, nil
		}
		return nil, k8sErr(o.s, k, "list", err)
	}
	return &mlResourceList{Items: viewList(ul.Items)}, nil
}

// getOf reads one object by name from the caller's tenant namespace. A name the
// caller's org does not own is the SAME 404 an unknown name gives — the dynamic
// client is pinned to the caller's namespace, so a cross-tenant name simply is
// not there.
func (o ops) getOf(ctx context.Context, k resourceKind, in *mlRef) (*mlResource, error) {
	if err := ready(o.s); err != nil {
		return nil, err
	}
	ns, _, _, err := tenantFrom(ctx)
	if err != nil {
		return nil, err
	}
	obj, err := o.s.State.dyn.Resource(k.gvr).Namespace(ns).Get(ctx, normName(in.Name), metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, zip.ErrNotFound(k.kind + " not found")
		}
		return nil, k8sErr(o.s, k, "get", err)
	}
	v := view(obj, true)
	return &v, nil
}

// deleteOf deletes one object by name from the caller's tenant namespace and
// answers 204. A nil Out is what zip writes 204 for, which is the status this
// route has always sent.
func (o ops) deleteOf(ctx context.Context, k resourceKind, in *mlRef) (*struct{}, error) {
	if err := ready(o.s); err != nil {
		return nil, err
	}
	ns, _, _, err := tenantFrom(ctx)
	if err != nil {
		return nil, err
	}
	if err := o.s.State.dyn.Resource(k.gvr).Namespace(ns).Delete(ctx, normName(in.Name), metav1.DeleteOptions{}); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, zip.ErrNotFound(k.kind + " not found")
		}
		return nil, k8sErr(o.s, k, "delete", err)
	}
	return nil, nil
}

// mlCreate is what a create takes: a DNS-1123 name, the resource's own spec, and
// optional labels. The spec is carried as raw JSON because it is the KUBERNETES
// object's spec, whose shape belongs to the CRD rather than to this API — a Go
// struct here would publish a schema this surface does not own and could not keep
// current. zip publishes a json.RawMessage as `{}`, "any JSON", which is the true
// statement.
type mlCreate struct {
	// Name is the resource's name: a DNS-1123 label
	// (^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$), lowercased and trimmed. It is the
	// name the resource answers to for the life of the caller's org.
	Name string `json:"name" url:"-"`
	// Spec is the resource's own spec, passed to Kubernetes unchanged. Required —
	// an empty spec is 400 rather than an empty resource.
	Spec json.RawMessage `json:"spec" url:"-"`
	// Labels are extra labels to set on the object, merged UNDER the tenancy
	// labels this plane derives from the validated principal — so a label naming
	// another org's scope cannot displace the real one.
	Labels map[string]string `json:"labels,omitempty" url:"-"`
}

// createOf provisions one object of kind k in the caller's tenant namespace, and
// is the whole preamble the per-kind ops share: readiness, tenancy, validation,
// the pre-create balance gate, the namespace, the Create, and the debit.
//
// THE GATE IS THE LAST CHECK BEFORE THE WRITE and it stays there. Lifting it into
// middleware would run it before the body is decoded, turning today's 400 on a
// malformed spec into a 402 — a caller told they cannot afford a request that was
// never valid. The refusal itself is cloud.Denied, which carries the fleet-wide
// {"error":{"code","message"}} money contract rather than a second vocabulary
// this surface would have to invent.
func (o ops) createOf(ctx context.Context, k resourceKind, in *mlCreate) (*mlResource, error) {
	if err := ready(o.s); err != nil {
		return nil, err
	}
	ns, org, project, err := tenantFrom(ctx)
	if err != nil {
		return nil, err
	}
	c, ok := cloud.Request(ctx)
	if !ok {
		return nil, zip.ErrForbidden("no validated principal")
	}
	name := normName(in.Name)
	if !nameRE.MatchString(name) {
		return nil, zip.ErrBadRequest("'name' must be a DNS-1123 label: ^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$")
	}
	if len(in.Spec) == 0 {
		return nil, zip.ErrBadRequest("'spec' is required (the " + k.kind + " spec)")
	}
	var spec map[string]any
	if err := json.Unmarshal(in.Spec, &spec); err != nil {
		return nil, zip.Errorf(http.StatusBadRequest, "invalid 'spec': %v", err)
	}

	fee := cloud.ResourceFeeCents(computeFeeEnvPrefix, k.kind)
	_, projectValidated := principal.ValidatedProject(c)
	if err := o.s.State.bill.Gate(ctx, principal.Ledger(c), project, projectValidated, k.kind, fee); err != nil {
		return nil, cloud.Denied(err)
	}

	if err := ensureNamespace(o.s, ctx, ns, org, project); err != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "ensure tenant namespace: %v", err)
	}
	obj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": k.apiVersion,
		"kind":       k.kind,
		"metadata": map[string]any{
			"name":      name,
			"namespace": ns,
			"labels":    labelsFor(org, project, in.Labels),
		},
		"spec": spec,
	}}
	out, err := o.s.State.dyn.Resource(k.gvr).Namespace(ns).Create(ctx, obj, metav1.CreateOptions{})
	if err != nil {
		switch {
		case apierrors.IsAlreadyExists(err):
			return nil, zip.ErrConflict(k.kind + " already exists")
		case apierrors.IsInvalid(err), apierrors.IsBadRequest(err):
			return nil, zip.Errorf(http.StatusUnprocessableEntity, "%s rejected by kubernetes: %v", k.kind, err)
		default:
			return nil, k8sErr(o.s, k, "create", err)
		}
	}
	// Created — debit the caller's org ledger for the compute submission (per-org,
	// env-attributed, async best-effort, so a debit failure never corrupts a 201).
	o.s.State.bill.Meter(principal.Ledger(c), project, k.kind, fee, c.RequestID(), cloud.ClientIP(c))
	v := view(out, true)
	return &v, nil
}

// ── models (kserve InferenceService) ─────────────────────────────────────────

// ListModels lists the inference models deployed in the caller's org. Each entry
// carries the model's name, when Kubernetes admitted it, and kserve's live status
// — the spec is on the single-model read. An org that has deployed nothing gets
// an empty list.
func (o ops) listModels(ctx context.Context, _ *mlNoInput) (*mlResourceList, error) {
	return o.listOf(ctx, modelKind)
}

// CreateModel deploys one inference model for the caller's org, and answers 201
// with the model as Kubernetes admitted it.
//
// The `spec` is a kserve InferenceService spec, passed through unchanged — this
// plane owns the tenancy, the billing and the namespace, and kserve owns what a
// model IS. An unfunded org is refused BEFORE anything is created, so nobody runs
// free GPU compute and nobody is charged for a resource that was never made.
//
// Example: {"name":"sentiment","spec":{"predictor":{"model":{"modelFormat":{"name":"sklearn"},"storageUri":"s3://models/sentiment"}}}}
func (o ops) createModel(ctx context.Context, in *mlCreate) (*mlResource, error) {
	return o.createOf(ctx, modelKind, in)
}

// GetModel returns one deployed inference model. Its spec comes with it, and
// kserve's live status, which is where readiness and the serving address appear.
// A name the caller's org does not own answers 404, exactly as an unknown name
// does, so a probe learns nothing about another tenant's models.
//
// Example: {"name": "sentiment"}
func (o ops) getModel(ctx context.Context, in *mlRef) (*mlResource, error) {
	return o.getOf(ctx, modelKind, in)
}

// DeleteModel deletes a deployed inference model. kserve owns the teardown: the
// InferenceService goes away and the serving deployment behind it follows, so the
// model stops answering predict calls. Answers 204, or 404 for a name the
// caller's org does not own.
//
// Example: {"name": "sentiment"}
func (o ops) deleteModel(ctx context.Context, in *mlRef) (*struct{}, error) {
	return o.deleteOf(ctx, modelKind, in)
}
