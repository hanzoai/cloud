package sandbox

// cred.go — WHAT A SANDBOX IS HANDED, and whose it is.
//
// A sandbox runs code its owner submitted, on our nodes, and the tools inside it
// — `hanzo`, `git`, the coding tools — are useless without an identity. So every
// lease is handed ONE credential: a short-lived IAM token for THE OWNER, and
// nothing else. Two facts make that safe to say out loud:
//
//	IT IS THE CALLER'S OWN. The token is not minted from an authority of ours; it
//	is EXCHANGED (RFC 8693) for the token the caller presented on the create call.
//	Possession of that token is the whole authorization, so this path cannot reach
//	an identity that did not just call us. A sandbox therefore holds exactly what
//	its owner already held and never a shared, admin, or cross-tenant credential.
//
//	IT DIES WITH THE LEASE. The exchange asks for the lease's own remaining life
//	(IAM clamps it one way, so this can only ever shorten), and no refresh token is
//	issued — nothing inside the pod can renew what it holds. A 15-minute exec
//	sandbox gets a 15-minute credential.
//
// EACH CREDENTIAL ARRIVES WHERE ITS TOOL LOOKS FOR IT, and every one of them
// arrives through the EXEC CHANNEL rather than the Pod spec. A value in the spec
// is a value in etcd and in every `kubectl describe` of that pod; written through
// exec it lives in the container's ephemeral rootfs, in NO Kubernetes object at
// all, and dies with the pod — which is exactly the lifetime a credential for one
// lease should have. That is why the ordinary lease's Pod spec is byte-identical
// to the one it had before any of this existed: it carries no `env` key at all.
//
//	hanzo   keeps its own credential store, so the token is PIPED to
//	        `hanzo auth login --token -` and this file never writes that format.
//	        One store, owned by the tool that owns it.
//	git     reads a credential helper, so the helper is configured to ask `hanzo`
//	        for the current token — one copy of the credential, never a second on
//	        disk to go stale, and SCOPED TO THE FORGE HOST so the token is offered
//	        to git.<brand> and to nothing else a checkout might point at.
//	kubectl reads a FILE and KUBECONFIG names its path, so the SuperAdmin's
//	        kubeconfig is written into the pod.
//
// THE SUPERADMIN'S OWN SHELL IS THE ONE THAT GETS MORE. `admin` below is the one
// predicate deciding both which bytes the pod boots and whether it also holds the
// DigitalOcean credentials the operator would otherwise paste in by hand. It is
// the same fact asked twice — a shell with the admin image but no credential has
// a kubectl that reaches nothing — so it is one line.

import (
	"context"
	"fmt"
	"maps"
	"strings"
	"time"

	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/admin/digitalocean"
	"github.com/hanzoai/cloud/apps/fleet"
	"github.com/hanzoai/cloud/brand"
	"github.com/hanzoai/cloud/internal/environ"
	"github.com/hanzoai/cloud/internal/iam"

	luxlog "github.com/luxfi/log"
)

// home is the image's own home directory — uid 1000, created by `useradd -m` in
// the sandbox image — and it is STATED rather than inherited: an exec session
// carries no HOME unless something sets one, so `~` is a guess about the shell
// while this is a fact about the pod.
//
// Nothing here is under the workdir on purpose. A `dev` sandbox mounts its
// project PVC there and a PVC OUTLIVES the lease, so a credential written into it
// would still be sitting there for the next session — and for whoever leases that
// project next. The container rootfs dies with the pod.
const home = "/home/sandbox"

// kubePath is where kubectl is TOLD to look, for the same reason.
const kubePath = home + "/.kube/config"

// admin is the ONE combination that is a SuperAdmin's own shell: platform sudo,
// asking for the class that has a toolchain to spend credentials with.
//
// It is a function and not two copies of `super && class == "dev"` because the
// two readers must never disagree — see the file comment. `super` reaches here
// from principal.IsSuperAdmin (owner == the reserved `admin` org) as a PARAMETER,
// never off a request body and never off the context, which is the property
// trust_test.go exists to keep.
func admin(class string, super bool) bool { return super && class == "dev" }

// cred is what one lease is handed. It is a VALUE passed alongside the sandbox
// and never a field on it: the row is STORED, so a credential on it would be a
// credential at rest in the org's own database, returned by every later read of
// that row and outliving the pod it was for.
type cred struct {
	// session is the owner's own short-lived token. It reaches the pod through the
	// exec channel and this file never writes the CLI's store format — the tool
	// that owns the store writes it.
	session iam.Session
	// env reaches the pod in its spec, because that is where doctl looks. Empty for
	// every lease but a SuperAdmin's own, so the ordinary Pod spec states no `env`.
	env map[string]string
	// kube reaches the pod through the exec channel, because kubectl reads a file
	// and no Kubernetes object should hold a cluster-admin credential.
	kube []byte
}

// sessionFor exchanges the caller's own token for one bound to this lease.
//
// bearer is the credential the caller authenticated the create call with, relayed
// unchanged from the request. It is the SUBJECT of the exchange, which is what
// makes the result the caller's own identity and nobody else's: IAM mints for the
// subject the presented token names, so a caller can only ever land their own
// identity in their own sandbox.
//
// ttl is the lease. The token is asked to live no longer, so a credential that
// escapes a pod outlives that pod by nothing.
//
// An empty bearer is NOT an error — an API key is not a relayable bearer
// (cloud.CallerBearer returns "" for one) and the agent plane carries no user
// token at all. Those leases simply start without a session, exactly as every
// lease did before this existed.
func sessionFor(ctx context.Context, bearer string, ttl time.Duration) (iam.Session, error) {
	if strings.TrimSpace(bearer) == "" {
		return iam.Session{}, nil
	}
	id, secret := environ.Or("IAM_MINT_CLIENT_ID", ""), environ.Or("IAM_MINT_CLIENT_SECRET", "")
	if id == "" || secret == "" {
		return iam.Session{}, nil // the deployment wires no mint client
	}
	return iam.Exchange(ctx, cloud.IAMBase(), id, secret, bearer, ttl)
}

// signIn is the script that makes the tools inside a sandbox work as its owner.
// It runs ONCE, with the token on stdin and never in argv or the environment,
// where `ps` and /proc would publish it to every process in the pod.
//
// `git config` writes the file rather than this composing one, so git owns the
// quoting of its own format. The helper asks `hanzo` for the current token
// instead of holding a copy, so there is ONE credential in the pod and no second
// one on disk to be left behind when the first is replaced.
//
// A TOOL THAT IS NOT THERE IS NOT A FAILURE. A caller may name its own image, and
// an image without `git` has nothing to configure and one without `hanzo` has
// nothing to sign in — so each half is skipped rather than failing a lease over a
// credential its image could not have spent. A tool that IS there and fails still
// fails the lease, which is the case worth hearing about.
//
// HOME is the IMAGE's, and /home/sandbox only when the image states none. An exec
// session inherits whatever the image set, and writing to our own guess would put
// the credential where that image's tools do not look.
func signIn(s iam.Session, brandID string) string {
	q := func(v string) string { return shellQuote(v) }
	set := func(k, v string) string {
		if strings.TrimSpace(v) == "" {
			return ""
		}
		return "  git config --global " + k + " " + q(v) + "\n"
	}
	helper := `!f() { test "$1" = get && printf "username=hanzo\npassword=%s\n" "$(hanzo auth token)"; }; f`
	return "set -e\numask 077\nexport HOME=\"${HOME:-" + home + "}\"\n" +
		"if command -v git >/dev/null; then\n" +
		set("user.name", s.Display) +
		set("user.email", s.Email) +
		"  git config --global " + q("credential.https://"+brand.GitHost(brandID)+".helper") + " " + q(helper) + "\n" +
		"fi\n" +
		"if command -v hanzo >/dev/null; then\n" +
		"  hanzo auth login --brand " + q(brandID) + " --token - >/dev/null\n" +
		"fi\n"
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
	// DigitalOcean reader in this binary gets it: the deployment's KMSSecret syncs
	// it into the environment under the name the app already reads. Asking KMS for
	// it directly here would be a SECOND way to the same secret in the same
	// process, which is the thing we do not do.
	token := environ.Or("DO_API_TOKEN", "")
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
