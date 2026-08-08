package sandbox

// cred.go — WHAT A SUPERADMIN'S OWN SANDBOX IS HANDED, and why nothing else is
// handed anything.
//
// runtime.go states the invariant this file is the other half of: the admin image
// carries no credential, so kubectl with no kubeconfig and doctl with no token
// are argument parsers, and what a pod is handed is decided by the IDENTITY that
// leased it rather than by the bytes it booted. This is that decision, made once
// and in one place.
//
// ONE PREDICATE answers both halves. `admin` below decides which bytes the pod
// boots AND which credentials it holds, because they are the same fact asked
// twice: a lease that took the admin image without the credentials is a shell
// whose kubectl reaches nothing, and a lease that took the credentials without
// the image has no kubectl to spend them. Two lines that must agree are one line.
//
// NOTHING IS MINTED HERE. IAM is all auth and tokens, and this file issues no
// credential, names no new one and validates none. It READS a secret KMS already
// holds and DigitalOcean already issued, and puts the SuperAdmin's own account
// credentials in the SuperAdmin's own pod — the same two values they would paste
// in by hand. That is what makes it a delivery rather than the fourth rejected
// credential design: there is no new authority, and no second answer to "may this
// pod do X".
//
// EACH CREDENTIAL ARRIVES WHERE ITS TOOL LOOKS FOR IT. There are two routes and
// neither is an accident:
//
//	doctl   reads DIGITALOCEAN_ACCESS_TOKEN from the environment, so the token is
//	        an env var on the pod.
//	kubectl reads a FILE, and KUBECONFIG names its path — no env var carries
//	        kubeconfig CONTENT — so the kubeconfig is written into the pod
//	        through the exec channel, which is already the one way into a sandbox.
//
// THE BIGGER CREDENTIAL TAKES THE SAFER ROUTE, on purpose. A DOKS kubeconfig is
// cluster-admin for that cluster; written through exec it lives in the pod's
// ephemeral rootfs and in NO Kubernetes object at all — not in the Pod spec, not
// in a Secret, nowhere in etcd, nowhere a pod-listing dashboard renders it.
//
// The DO token does sit inline in the Pod spec, and the alternative was worse.
// A Secret needs `hanzo-sandboxes` RBAC widened to secrets; the names are minted
// per lease so the grant cannot be narrowed with resourceNames; so it would let
// this process read EVERY secret in that namespace for EVERY sandbox, forever, in
// order to hide one value from a Pod object that exists for one admin lease and
// is deleted with it. And a Secret is base64, not encryption: it buys obscurity
// and costs a permanent widening of a boundary that today reads "no secrets at
// all". The narrower change is the inline one.
//
// AND NOTHING ELSE GETS ANY OF IT. credFor is called on exactly one branch and
// the branch is `admin`. Every other lease carries the zero cred: its env is
// empty, so podSpec states no `env` key — absent, not empty — and start writes no
// file. An ordinary caller's pod is byte-identical to the one they got before this
// file existed. cred_test.go asserts precisely that, because it is the half that
// would be a P0.

import (
	"context"
	"fmt"
	"maps"
	"strings"

	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/hanzoai/cloud/apps/admin/digitalocean"
	"github.com/hanzoai/cloud/apps/fleet"

	luxlog "github.com/luxfi/log"
)

// kubePath is where kubectl is TOLD to look, rather than left to find its own
// default. A `kubectl exec` session inherits no HOME unless something sets one,
// so `~/.kube/config` is a guess about the shell while this is a fact about the
// pod. The directory is the image's own home — /home/sandbox, uid 1000, created
// by `useradd -m` in hanzoai/bot's Dockerfile.box.
//
// It is deliberately NOT under the workdir. A `dev` sandbox mounts its project
// PVC there and a PVC OUTLIVES the lease, so a credential written into it would
// still be sitting there for the next session — and for whoever leases that
// project next. The container rootfs dies with the pod, which is exactly the
// lifetime this credential should have.
const kubePath = "/home/sandbox/.kube/config"

// admin is the ONE combination that is a SuperAdmin's own shell: platform sudo,
// asking for the class that has a toolchain to spend credentials with.
//
// It is a function and not two copies of `super && class == "dev"` because the
// two readers must never disagree — see the file comment. `super` reaches here
// from principal.IsSuperAdmin (owner == the reserved `admin` org) as a PARAMETER,
// never off a request body and never off the context, which is the property
// trust_test.go exists to keep.
func admin(class string, super bool) bool { return super && class == "dev" }

// cred is what one admin lease is handed. It is a VALUE passed alongside the
// sandbox and never a field on it: the row is STORED, so a credential on it would
// be a credential at rest in the org's own database, returned by every later read
// of that row and outliving the pod it was for. Same argument that made `super` a
// parameter on Lease — this is a fact about the lease, not about the sandbox.
type cred struct {
	// env reaches the pod in its spec, because that is where doctl looks.
	env map[string]string
	// kube reaches the pod through the exec channel, because kubectl reads a file
	// and no Kubernetes object should hold a cluster-admin credential for this.
	kube []byte
}

// credFor reads the SuperAdmin's DigitalOcean credentials and builds one
// kubeconfig covering every cluster the token can see.
//
// EVERY cluster, because the alternative is a cluster id in the deployment env
// and the answer is already in the API. One context per DOKS cluster is what an
// operator wants anyway — `kubectl config get-contexts` is the map — and it stays
// right when a cluster is added without anybody remembering to say so.
//
// THE PUBLIC ENDPOINT IS THE ONLY ONE THAT WORKS, which is a property of the
// isolation rather than a way around it. DO hands back a token-based kubeconfig
// against the cluster's public https endpoint; the in-cluster 10/8 apiserver
// address is refused by the sandbox NetworkPolicy, which is what that policy is
// for. This is the same door an operator's laptop knocks on.
//
// fleet.SafeRESTConfig is THE gate every kubeconfig in this binary passes, and
// nothing about the source makes this one exempt: it refuses exec-credential
// plugins (arbitrary execution at dial time) and non-routable apiserver hosts
// (SSRF). A cluster that fails it is dropped and named in the log, never silently
// folded in.
//
// A cluster that cannot be read is SKIPPED and the rest still ship, because one
// sick cluster should not cost the operator the shell they were reaching for it
// with. Zero readable clusters is an error — an empty kubeconfig is a kubectl
// that fails later and blames the wrong thing.
func credFor(ctx context.Context, log luxlog.Logger) (cred, error) {
	// DO_API_TOKEN is KMS-sourced and reaches this process the one way every other
	// DigitalOcean reader in this binary gets it: credz materializes the KMSSecret
	// into the deployment env under the name the app already reads — credz/scope.go,
	// where the store path IS the variable name, /orgs/{adminOrg}/svc/_shared/
	// DO_API_TOKEN. Asking KMS for it directly here would be a SECOND way to the
	// same secret in the same process, which is the thing we do not do.
	token := strings.TrimSpace(envOr("DO_API_TOKEN", ""))
	do := digitalocean.New(token)
	if !do.Ready() {
		return cred{}, fmt.Errorf("DO_API_TOKEN not configured")
	}
	clusters, err := do.Clusters(ctx)
	if err != nil {
		return cred{}, fmt.Errorf("list clusters: %w", err)
	}
	merged := clientcmdapi.NewConfig()
	for _, cl := range clusters {
		raw, err := do.Kubeconfig(ctx, cl.ID)
		if err != nil {
			log.Warn("admin kubeconfig unavailable", "cluster", cl.Name, "err", err)
			continue
		}
		if _, err := fleet.SafeRESTConfig(raw); err != nil {
			log.Warn("admin kubeconfig refused", "cluster", cl.Name, "err", err)
			continue
		}
		cfg, err := clientcmd.Load(raw)
		if err != nil {
			log.Warn("admin kubeconfig unreadable", "cluster", cl.Name, "err", err)
			continue
		}
		// DOKS names every entry `do-<region>-<cluster>`, so the three maps merge
		// without collision and each cluster keeps the context name an operator
		// already knows from `doctl kubernetes cluster kubeconfig save`.
		maps.Copy(merged.Clusters, cfg.Clusters)
		maps.Copy(merged.AuthInfos, cfg.AuthInfos)
		maps.Copy(merged.Contexts, cfg.Contexts)
		if merged.CurrentContext == "" {
			merged.CurrentContext = cfg.CurrentContext
		}
	}
	if len(merged.Contexts) == 0 {
		return cred{}, fmt.Errorf("no readable DOKS cluster (%d listed)", len(clusters))
	}
	kube, err := clientcmd.Write(*merged)
	if err != nil {
		return cred{}, fmt.Errorf("render kubeconfig: %w", err)
	}
	return cred{
		env: map[string]string{
			// doctl's own name for it, which is the whole reason this one is an
			// env var rather than a file.
			"DIGITALOCEAN_ACCESS_TOKEN": token,
			// And ours, so a script run in this shell reads the same name it
			// reads everywhere else in the fleet.
			"DO_API_TOKEN": token,
			"KUBECONFIG":   kubePath,
		},
		kube: kube,
	}, nil
}
