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
//
// THE SUPERADMIN'S OWN SHELL IS THE ONE THAT GETS MORE. `admin` below is the one
// predicate deciding both which bytes the pod boots and whether it also holds the
// cluster credentials the operator would otherwise paste in by hand. It is the
// same fact asked twice — a shell with the admin image but no credential has a
// kubectl that reaches nothing — so it is one line.

import (
	"context"
	"fmt"
	"strings"
	"time"

	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/hanzoai/cloud/fleet"
	"github.com/hanzoai/cloud/sdk/brand"
	"github.com/hanzoai/cloud/sdk/env"
	"github.com/hanzoai/cloud/sdk/iam"

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
// from principal.Super (owner == the reserved `admin` org) as a PARAMETER,
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
	// PATH that tells kubectl where to look. Empty for every lease but a
	// SuperAdmin's own, so the ordinary Pod spec states no `env`.
	env map[string]string
	// kube reaches the pod through the exec channel, because kubectl reads a file
	// and no Kubernetes object should hold a cluster-admin credential.
	kube []byte
}

// sessionFor exchanges the caller's own token for one bound to this lease.
//
// bearer is the credential the caller authenticated the create call with, relayed
// unchanged from the request — or, on a plane lease, the person's own token a sibling
// validated on their call and relayed (client.LeaseIn.Bearer). It is the SUBJECT of
// the exchange, which is what makes the result the caller's own identity and nobody
// else's: IAM mints for the subject the presented token names, so a caller can only
// ever land their own identity in their own sandbox.
//
// ttl is the lease. The token is asked to live no longer, so a credential that
// escapes a pod outlives that pod by nothing.
//
// An empty bearer is NOT an error — an API key is not a relayable bearer
// (cloud.CallerBearer returns "" for one) and a plane lease carries a user token only
// when a sibling relays one. Those leases simply start without a session, exactly as
// every lease did before this existed.
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
	id, secret := env.Or("IAM_SANDBOX_CLIENT_ID", ""), env.Or("IAM_SANDBOX_CLIENT_SECRET", "")
	if id == "" || secret == "" {
		return iam.Session{}, nil // the deployment wires no sandbox client
	}
	return iam.Exchange(ctx, iam.Base(), id, secret, bearer, ttl)
}

// refreshSession signs a sandbox that is ALREADY RUNNING in again, for a resume that
// carries its lessee's bearer.
//
// A pod is otherwise signed in only when it starts, so a sandbox first leased with no
// bearer — a sibling's plane lease, the cell a chat session's tool call took — kept no
// identity for the rest of its life, even once its lessee came back with one: a
// /v1/dev session that found the cell it already held started Dev beside no login
// (#907). The session lives as long as what is left of the lease.
//
// ONLY THE LESSEE'S OWN BEARER, the rule a relocated pod already follows: an admin
// taking back somebody else's sandbox does not replace its owner's identity with
// theirs. No bearer is nothing to do, so a resume that carries none answers as fast
// as it always did. A session that cannot be had or delivered is LOUD and costs the
// resume nothing, for the reason a lease gives: the pod keeps what it held.
func refreshSession(s *Service, ctx context.Context, h holder, m Sandbox, bearer string) {
	if strings.TrimSpace(bearer) == "" || !h.leased(m) {
		return
	}
	sess, err := sessionFor(ctx, bearer, time.Until(time.Unix(m.ExpiresAt, 0)))
	if err != nil {
		s.Log.Warn("no owner session to hand the running sandbox", "sandbox", m.ID, "err", err)
		return
	}
	if err := s.State.rt.signIn(ctx, m, sess); err != nil {
		s.Log.Warn("the running sandbox could not take the owner session", "sandbox", m.ID, "err", err)
	}
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

// fleetCred is what a SuperAdmin's dev lease holds beside its session: the fleet's
// kubeconfig when one can be built, and nothing when it cannot.
//
// ONE POLICY FOR BOTH CREDENTIALS, the one sessionFor already follows: a
// credential that cannot be had is LOUD and never costs the shell. The caller
// asked for a dev sandbox; the admin image and its kubeconfig are what their
// identity adds, not what they asked for. Refusing the lease denied nothing a
// credential-less pod is not already denied, and it made an unreadable fleet a
// total outage of the operator's own shell. Both lease and resume ask here, so
// the two cannot answer differently.
func fleetCred(ctx context.Context, s *Service, org, class string, super bool) cred {
	if !admin(class, super) {
		return cred{}
	}
	cr, err := credFor(ctx, s.State.rt.attached, org, s.Log)
	if err != nil {
		s.Log.Warn("the SuperAdmin's sandbox starts without cluster credentials", "org", org, "err", err)
		return cred{}
	}
	return cr
}

// credFor builds the SuperAdmin's kubeconfig from every cluster the org has
// attached to its fleet: one context per cluster, named by its fleet name.
//
// EVERY attached cluster, because the registry already holds the answer, and it
// stays right when a cluster is attached without anybody remembering to say so.
// The default project shard is read, which is where a lease that names a cluster
// finds it (runtime.at).
//
// ONLY THE SCREENED CONTEXT TRAVELS, under the fleet name. fleet.SafeRESTConfig is
// THE gate every kubeconfig in this binary passes, and nothing about the source
// makes this one exempt: it refuses exec-credential plugins (arbitrary execution at
// dial time) and non-routable apiserver hosts (SSRF), and it reads the current
// context. So that context's cluster and user are what the pod is handed, and
// nothing else in the file. Names inside an attached kubeconfig collide across
// clusters (every k3s one says `default`); a fleet name is unique in its shard.
//
// A cluster that cannot be read, or fails the gate, is SKIPPED and named in the
// log, and the rest still ship, because one sick cluster should not cost the
// operator the shell they were reaching for it with. Clusters attached but none
// readable is an error — an empty kubeconfig is a kubectl that fails later and
// blames the wrong thing — and fleetCred decides what that error costs.
//
// NOTHING ATTACHED IS NOT AN ERROR. It is an org with no fleet, and the answer is
// the one a caller naming the admin image by hand already gets: kubectl with
// nothing to reach. An error here refused every dev sandbox a SuperAdmin asked
// for in an org that had attached nothing — the dock's shell included.
func credFor(ctx context.Context, registry attached, org string, log luxlog.Logger) (cred, error) {
	if registry == nil {
		return cred{}, nil
	}
	clusters, err := registry.List(ctx, org, "")
	if err != nil {
		return cred{}, fmt.Errorf("list clusters: %w", err)
	}
	if len(clusters) == 0 {
		return cred{}, nil
	}
	merged := clientcmdapi.NewConfig()
	for _, cl := range clusters {
		raw, err := registry.Kubeconfig(ctx, org, "", cl.Name)
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
		// The gate read the current context, so that context — its cluster and its
		// user — is what travels. A user it names but does not define is anonymous,
		// as client-go reads it.
		at, ok := cfg.Contexts[cfg.CurrentContext]
		if !ok || cfg.Clusters[at.Cluster] == nil {
			log.Warn("admin kubeconfig names no current context", "cluster", cl.Name)
			continue
		}
		user := cfg.AuthInfos[at.AuthInfo]
		if user == nil {
			user = clientcmdapi.NewAuthInfo()
		}
		merged.Clusters[cl.Name] = cfg.Clusters[at.Cluster]
		merged.AuthInfos[cl.Name] = user
		merged.Contexts[cl.Name] = &clientcmdapi.Context{Cluster: cl.Name, AuthInfo: cl.Name, Namespace: at.Namespace}
		if merged.CurrentContext == "" {
			merged.CurrentContext = cl.Name
		}
	}
	if len(merged.Contexts) == 0 {
		return cred{}, fmt.Errorf("no readable attached cluster (%d attached)", len(clusters))
	}
	kube, err := clientcmd.Write(*merged)
	if err != nil {
		return cred{}, fmt.Errorf("render kubeconfig: %w", err)
	}
	return cred{
		// A PATH. An exec session carries no HOME, so kubectl has to be told where
		// its credential is; the value is not the credential, so the Pod spec still
		// states no secret.
		env:  map[string]string{"KUBECONFIG": kubePath},
		kube: kube,
	}, nil
}
