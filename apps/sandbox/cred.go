package sandbox

// cred.go — WHAT A SANDBOX IS HANDED, and whose it is.
//
// A sandbox runs code its owner submitted, on our nodes, and the tools inside it
// — `hanzo`, `git`, the coding tools — are useless without an identity. So every
// lease is handed ONE credential: a short-lived IAM token for THE OWNER, and
// nothing else. Three facts make that safe to say out loud:
//
//	IT IS THE CALLER'S OWN. The token is not minted from an authority of ours; it
//	is EXCHANGED (RFC 8693) for the token the caller presented on the create call.
//	Possession of that token is the whole authorization, so this path cannot reach
//	an identity that did not just call us.
//
//	AND IT IS NEVER A PLATFORM ONE. The exchange acts as `hanzo-sandbox`, a client
//	kept for this and nothing else, and IAM will not act for a reserved-org subject
//	unless the acting client holds the separate admin-mint capability — which this
//	one is deliberately not granted. So the ceiling on what a pod can be handed is
//	the CLIENT that fetched it, not the privilege of whoever asked: an operator's
//	own lease starts with no session at all rather than with platform sudo inside a
//	boundary built to run code we assume is hostile.
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
//	doctl   reads a FILE too, under $XDG_CONFIG_HOME, so the DigitalOcean token
//	        goes the same way. It used to be the one exception, on the grounds that
//	        doctl wants an environment variable — which is not true, and the cost
//	        of believing it was the platform's own provider token sitting in a Pod
//	        spec, in etcd and in every `kubectl describe`, for the life of a lease.
//	        Measured before changing it: with the file and no variable doctl
//	        authenticates; with the variable removed and the file taken away it
//	        refuses with "access token is required". The file is the authority.
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

// configHome and doctlPath are the same fact for doctl. It reads its credential
// from a file under $XDG_CONFIG_HOME, so the credential can travel the way every
// other one here does and only the PATH has to be stated in the spec.
const (
	configHome = home + "/.config"
	doctlPath  = configHome + "/doctl/config.yaml"
)

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
	// env reaches the pod in its spec, so nothing in it is a credential — only the
	// two PATHS that tell kubectl and doctl where to look. Empty for every lease
	// but a SuperAdmin's own, so the ordinary Pod spec states no `env`.
	env map[string]string
	// kube reaches the pod through the exec channel, because kubectl reads a file
	// and no Kubernetes object should hold a cluster-admin credential.
	kube []byte
	// doctl is the same, and for a sharper reason: this token is the platform's
	// own, it is not scoped to a lease, and DigitalOcean accepts it at the
	// inference endpoint as well as the control plane — so a copy of it in a Pod
	// spec is uncapped model spend readable by anything that can get a pod.
	doctl []byte
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
//
// IAM_SANDBOX_* is this subsystem's OWN client, and the separation is the point.
// The console credential a few files over may act for a reserved-org subject
// because minting an operator their own bearer is what a console is for; this one
// may not, because what it fetches is handed to a pod. Two capabilities cannot
// share one registration, so they do not share one name — and there is no fallback
// to the wider client, since a fallback is just the wide capability arriving late.
func sessionFor(ctx context.Context, bearer string, ttl time.Duration) (iam.Session, error) {
	if strings.TrimSpace(bearer) == "" {
		return iam.Session{}, nil
	}
	id, secret := environ.Or("IAM_SANDBOX_CLIENT_ID", ""), environ.Or("IAM_SANDBOX_CLIENT_SECRET", "")
	if id == "" || secret == "" {
		return iam.Session{}, nil // the deployment wires no sandbox client
	}
	return iam.Exchange(ctx, cloud.IAMBase(), id, secret, bearer, ttl)
}

// script is what makes the tools inside a sandbox work as its owner. It reads the
// token from STDIN and never from argv or the environment, where `ps` and /proc
// would publish it to every process in the pod.
//
// `git config` writes the file rather than this composing one, so git owns the
// quoting of its own format. The helper asks `hanzo` for the current token
// instead of holding a copy, so there is ONE credential in the pod and no second
// one on disk to be left behind when the first is replaced.
//
// A TOOL THAT IS NOT THERE IS NOT ASKED. A caller may name its own image, and an
// image without `git` has nothing to configure and one without `hanzo` has
// nothing to sign in — so each half asks before it acts, and an image that
// carries neither is a clean exit rather than a line in the log about a
// credential it could not have spent.
//
// HOME is the IMAGE's, and /home/sandbox only when the image states none. An exec
// session inherits whatever the image set, and writing to our own guess would put
// the credential where that image's tools do not look.
func script(s iam.Session, brandID string) string {
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
			// Both of these are PATHS. An exec session carries no HOME, so each
			// tool has to be told where its own credential is; neither value is
			// the credential, so the Pod spec still states no secret.
			"KUBECONFIG":      kubePath,
			"XDG_CONFIG_HOME": configHome,
		},
		kube:  kube,
		doctl: doctlConfig(token),
	}, nil
}

// doctlConfig is the credential in the shape doctl reads it. It exists so the
// token can travel the exec channel with the kubeconfig instead of riding the Pod
// spec, which is what the rest of this file already does and what the header
// states as the rule.
//
// The file is the whole authority: with it and no token in the environment, doctl
// authenticates; without it, doctl refuses with "access token is required". So
// this is not a second copy of a credential that is also somewhere else — it is
// the only one the pod gets.
func doctlConfig(token string) []byte {
	return []byte("access-token: " + token + "\n")
}
