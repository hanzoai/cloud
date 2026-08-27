package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"github.com/hanzoai/cloud/boundary"
	"github.com/spf13/cobra"
)

// cluster.go — bring THIS machine up as a Hanzo cloud node.
//
// A cloud node is two things at once: a Kubernetes cluster, and the isolation
// boundaries apps/sandbox schedules onto. Installing one without the other is
// the failure this file exists to prevent — a cluster with no boundary refuses
// every sandbox (gVisor is the floor, so an unconfigured node answers Pending
// with no explanation), and a boundary on no cluster is a binary nothing calls.
//
// THE BOUNDARY LIST IS NOT WRITTEN HERE. It is boundary.Names, the same
// closed set runtimeFor decides over, so what a machine installs and what the
// scheduler accepts cannot drift into two lists.
//
// k3s registers a containerd runtime for every handler it finds on PATH at
// start — which is why a stock local cluster already carries crun, spin and
// wasmedge classes nobody asked for. We rely on that for the HANDLER and create
// the CLASS ourselves, because the class name is ours (`gvisor`) and the handler
// is upstream's (`runsc`); letting k3s name it would put a second name for one
// boundary into the cluster.
//
// Every step is idempotent. `up` on a node that is already up installs nothing
// and says so.

// handlers maps a boundary to the containerd handler that runs it, the file
// whose presence proves the handler is installed, and — for a microVM — the
// HYPERVISOR that actually boots the guest.
//
// The hypervisor is a separate fact because a kata release ships the two
// independently, and the arm64 release proves why that matters: it carries
// `configuration-rs-fc.toml` and NO firecracker binary. A boundary judged by
// its config alone would report installed, schedule a pod, and fail when the
// shim looked for a VMM that is not there. runc names none: it IS the node's
// runtime, so k3s ships it and it is never installed and never absent.
var handlers = map[string]struct {
	handler, proof, hypervisor string
}{
	"runc":     {"runc", "", ""},
	"gvisor":   {"runsc", "/usr/local/bin/runsc", ""},
	"kata-fc":  {"kata-fc", "/usr/local/bin/containerd-shim-kata-fc-v2", "/opt/kata/bin/firecracker"},
	"kata-clh": {"kata-clh", "/usr/local/bin/containerd-shim-kata-clh-v2", "/opt/kata/bin/cloud-hypervisor"},
}

// hypervisor reports whether a boundary's VMM is on this machine. A boundary
// with none to name (runc, gvisor) always has it.
func hypervisor(boundary string) bool {
	h, ok := handlers[boundary]
	if !ok {
		return false
	}
	if h.hypervisor == "" {
		return true
	}
	_, err := os.Stat(h.hypervisor)
	return err == nil
}

// present reports whether a boundary's handler is installed on this machine.
// runc has no proof file because it is the runtime k3s itself runs on.
func present(boundary string) bool {
	h, ok := handlers[boundary]
	if !ok {
		return false
	}
	if h.proof == "" {
		return true
	}
	if _, err := os.Stat(h.proof); err == nil {
		return true
	}
	// A distro package puts the kata shims on PATH instead of under /opt/kata.
	// Either location runs the same guest, so either counts.
	_, err := exec.LookPath("containerd-shim-" + h.handler + "-v2")
	return err == nil
}

// kvm reports whether this machine can run a microVM at all. Both kata
// boundaries boot a guest kernel through KVM, so without /dev/kvm they are not
// installable here — an honest refusal beats a handler that fails at first use.
func kvm() bool {
	_, err := os.Stat("/dev/kvm")
	return err == nil
}

// ---------------------------------------------------------------------------
// status — what this machine can actually offer, read from the machine itself.
// ---------------------------------------------------------------------------

// Report is one machine's cloud-node readiness: the cluster, and each boundary
// it can hold. It is the same shape `up` prints when it finishes, so a node
// that is already up and a node that just came up read identically.
type Report struct {
	Arch       string          `json:"arch"`
	Cluster    bool            `json:"cluster"`
	KVM        bool            `json:"kvm"`
	Boundaries []BoundaryState `json:"boundaries"`
}

// BoundaryState is one isolation boundary on this machine.
type BoundaryState struct {
	Name      string `json:"name"`
	Handler   string `json:"handler"`
	Installed bool   `json:"installed"`
	// Why is set only when a boundary cannot be installed here at all, so the
	// operator reads a reason rather than a silent absence.
	Why string `json:"why,omitempty"`
}

func inspect() Report {
	r := Report{Arch: runtime.GOARCH, KVM: kvm()}
	_, err := exec.LookPath("k3s")
	r.Cluster = err == nil
	for _, b := range boundary.Names {
		st := BoundaryState{Name: b, Handler: handlers[b].handler, Installed: present(b)}
		switch {
		case st.Installed:
		case strings.HasPrefix(b, "kata-") && !r.KVM:
			st.Why = "no /dev/kvm — this machine cannot boot a guest kernel"
		case !hypervisor(b):
			// The VMM is not here YET, which is not the same as unavailable:
			// `up` supplies a firecracker that kata's own bundle omits. So this
			// names what is missing and stays a repairable state — a Why is for
			// something no download will fix, and only /dev/kvm is that.
			st.Why = ""
		}
		r.Boundaries = append(r.Boundaries, st)
	}
	return r
}

func printReport(w io.Writer, r Report) {
	tw := newTab(w)
	fmt.Fprintf(tw, "arch\t%s\n", r.Arch)
	fmt.Fprintf(tw, "cluster\t%s\n", yesNo(r.Cluster, "k3s", "absent"))
	fmt.Fprintf(tw, "kvm\t%s\n", yesNo(r.KVM, "yes", "no"))
	tw.Flush()
	fmt.Fprintln(w)
	tw = newTab(w)
	fmt.Fprintln(tw, "BOUNDARY\tHANDLER\tSTATE")
	for _, b := range r.Boundaries {
		state := "installed"
		if !b.Installed {
			state = "missing"
			if b.Why != "" {
				state = "unavailable — " + b.Why
			}
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\n", b.Name, b.Handler, state)
	}
	tw.Flush()
}

func yesNo(b bool, y, n string) string {
	if b {
		return y
	}
	return n
}

func newClusterStatusCmd(envOf func() *Env) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "What this machine can offer: cluster and isolation boundaries",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			r := inspect()
			return envOf().emit(r, func(w io.Writer) { printReport(w, r) })
		},
	}
}

// ---------------------------------------------------------------------------
// up — install what is missing, and nothing that is not.
// ---------------------------------------------------------------------------

func newClusterUpCmd(envOf func() *Env) *cobra.Command {
	var dry bool
	cmd := &cobra.Command{
		Use:   "up",
		Short: "Install the cluster and every isolation boundary this machine can hold",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return clusterUp(cmd.Context(), cmd.OutOrStdout(), dry)
		},
	}
	cmd.Flags().BoolVar(&dry, "dry-run", false, "print the steps that would run, install nothing")
	return cmd
}

// step is one installation the node needs. Keeping them as values means `up`
// and `--dry-run` walk the SAME list — a dry run that computes its plan
// separately is a dry run of a different program.
type step struct {
	what string
	run  func(context.Context) error
}

func clusterUp(ctx context.Context, w io.Writer, dry bool) error {
	before := inspect()
	var steps []step

	if !before.Cluster {
		steps = append(steps, step{"install k3s", installK3s})
	}
	for _, b := range before.Boundaries {
		if b.Installed || b.Why != "" {
			continue
		}
		switch {
		case b.Name == "gvisor":
			steps = append(steps, step{"install gvisor (runsc)", installGvisor})
		case strings.HasPrefix(b.Name, "kata-"):
			// Both kata boundaries ship in ONE release and one install; asking
			// for them twice would download the same tarball twice and race on
			// the same directory.
			if !hasStep(steps, "install kata") {
				steps = append(steps, step{"install kata", installKata})
			}
		}
	}
	// These two run EVERY time, not only when something was just installed,
	// and the reason is the same for both: they are what a boundary IS to the
	// cluster, and either can be lost while the handler on disk stays perfect.
	// A machine whose binaries are all present and whose containerd knows none
	// of them is exactly the state this was written in — `up` reported nothing
	// to do while every sandbox sat ContainerCreating. Declaring is also what
	// restarts k3s, so the restart happens ONCE at the end rather than once per
	// installer, and it happens after the last binary has landed.
	steps = append(steps,
		step{"declare containerd runtimes", restartK3s},
		step{"write RuntimeClasses", writeClasses})

	if dry {
		fmt.Fprintln(w, "would run:")
		for _, s := range steps {
			fmt.Fprintf(w, "  - %s\n", s.what)
		}
		return nil
	}
	for _, s := range steps {
		fmt.Fprintf(w, "==> %s\n", s.what)
		if err := s.run(ctx); err != nil {
			return fmt.Errorf("%s: %w", s.what, err)
		}
	}
	fmt.Fprintln(w)
	printReport(w, inspect())
	return nil
}

func hasStep(steps []step, what string) bool {
	for _, s := range steps {
		if s.what == what {
			return true
		}
	}
	return false
}

// sh runs one shell line as root. Installing a container runtime is writing to
// /usr/local/bin and /etc — there is no unprivileged form of it, so this asks
// plainly rather than pretending otherwise.
func sh(ctx context.Context, line string) error {
	c := exec.CommandContext(ctx, "sudo", "sh", "-c", line)
	c.Stdout, c.Stderr, c.Stdin = os.Stdout, os.Stderr, os.Stdin
	return c.Run()
}

// installK3s brings up the cluster, with the eviction thresholds this fleet
// runs. Kubelet's stock imagefs<15% strands a seventh of a large disk and
// evicts pods on a machine with hundreds of gigabytes free — measured here on a
// 3.7T node that evicted a sandbox at 96% used. Memory keeps its own guard.
func installK3s(ctx context.Context) error {
	const cfg = `mkdir -p /etc/rancher/k3s && cat > /etc/rancher/k3s/config.yaml <<'EOF'
kubelet-arg:
  - "eviction-hard=memory.available<100Mi,nodefs.available<5%,nodefs.inodesFree<5%,imagefs.available<5%"
EOF`
	if err := sh(ctx, cfg); err != nil {
		return err
	}
	return sh(ctx, "curl -sfL https://get.k3s.io | sh -")
}

// installGvisor puts runsc on PATH. k3s registers the handler when it next
// starts, which is why the restart is part of the install and not a step an
// operator has to know to run.
func installGvisor(ctx context.Context) error {
	const script = `set -eu
arch=$(uname -m)
url="https://storage.googleapis.com/gvisor/releases/release/latest/${arch}"
tmp=$(mktemp -d)
cd "$tmp"
wget -q "${url}/runsc" "${url}/runsc.sha512" "${url}/containerd-shim-runsc-v1" "${url}/containerd-shim-runsc-v1.sha512"
sha512sum -c runsc.sha512 -c containerd-shim-runsc-v1.sha512
install -o root -g root -m 0755 runsc containerd-shim-runsc-v1 /usr/local/bin/
rm -rf "$tmp"`
	return sh(ctx, script)
}

// installKata installs both microVM boundaries — Firecracker and
// Cloud-Hypervisor arrive in one release, so this is one download.
//
// The release is resolved from the REDIRECT that /releases/latest issues, not
// from api.github.com. The API is rate-limited to 60 unauthenticated requests an
// hour per address, and a machine that has been installing things all morning
// gets a 403 where it expects a version — measured at 3 remaining on the box
// this was written on. The redirect costs no quota and answers the same tag.
//
// The asset is `.tar.zst`. It was `.tar.xz` and the release stopped carrying
// one; a missing asset is an HTTP 404, which `curl -f` reports as exit 22 and
// nothing else, so each step says what it was doing when it failed.
func installKata(ctx context.Context) error {
	const script = `set -eu
case "$(uname -m)" in
  x86_64) a=amd64 ;;
  aarch64|arm64) a=arm64 ;;
  *) echo "kata: no release is published for $(uname -m)" >&2; exit 1 ;;
esac
u=$(curl -sIL -o /dev/null -w '%{url_effective}' https://github.com/kata-containers/kata-containers/releases/latest) ||
  { echo "kata: could not reach github to resolve the latest release" >&2; exit 1; }
v=${u##*/tag/}
case "$v" in ""|*/*) echo "kata: could not read a version out of $u" >&2; exit 1 ;; esac
tmp=$(mktemp -d); cd "$tmp"
f="kata-static-${v}-${a}.tar.zst"
curl -sfLO "https://github.com/kata-containers/kata-containers/releases/download/${v}/${f}" ||
  { echo "kata: $v publishes no $f" >&2; exit 1; }
tar --zstd -xf "$f" -C / ||
  { echo "kata: $f did not unpack" >&2; exit 1; }
# KATA'S BUNDLE IS NOT FIRECRACKER'S RELEASE. kata-static ships a firecracker
# binary for amd64 and, at 4.1.0, none for arm64 — while still shipping
# configuration-rs-fc.toml, which names /opt/kata/bin/firecracker. Firecracker
# itself has published aarch64 since 2020; it is the bundle that is partial, not
# the hypervisor. So where kata left the slot empty, fill it from upstream and
# the boundary is native on both arches rather than absent on one.
if [ ! -x /opt/kata/bin/firecracker ]; then
  case "$(uname -m)" in x86_64) fa=x86_64 ;; aarch64|arm64) fa=aarch64 ;; *) fa="" ;; esac
  if [ -n "$fa" ]; then
    fu=$(curl -sIL -o /dev/null -w '%{url_effective}' https://github.com/firecracker-microvm/firecracker/releases/latest) || fu=""
    fv=${fu##*/tag/}
    case "$fv" in ""|*/*) fv="" ;; esac
    if [ -n "$fv" ]; then
      ft=$(mktemp -d)
      if curl -sfL -o "$ft/fc.tgz" \
           "https://github.com/firecracker-microvm/firecracker/releases/download/${fv}/firecracker-${fv}-${fa}.tgz"; then
        tar -xzf "$ft/fc.tgz" -C "$ft"
        # jailer is OPTIONAL to kata (unset means no jail) so it is copied when
        # present and never required.
        for n in firecracker jailer; do
          b=$(find "$ft" -type f -name "${n}-${fv}-${fa}" | head -1)
          [ -n "$b" ] && install -m 0755 "$b" "/opt/kata/bin/${n}"
        done
        echo "kata: supplied firecracker ${fv} (${fa}) that this kata release omits" >&2
      fi
      rm -rf "$ft"
    fi
  fi
fi
shim=""
for c in /opt/kata/runtime-rs/bin/containerd-shim-kata-v2 /opt/kata/bin/containerd-shim-kata-v2; do
  [ -x "$c" ] && { shim="$c"; break; }
done
[ -n "$shim" ] || { echo "kata: $v unpacked no containerd-shim-kata-v2" >&2; exit 1; }
linked=0
for pair in "fc:firecracker" "clh:cloud-hypervisor"; do
  h=${pair%%:*}; vmm=${pair#*:}
  l="/usr/local/bin/containerd-shim-kata-${h}-v2"
  if [ -x "/opt/kata/bin/${vmm}" ]; then
    ln -sf "$shim" "$l"; linked=$((linked+1))
  else
    # No VMM, no handler. A shim linked over a missing hypervisor is a
    # RuntimeClass that schedules and then cannot boot a guest, which is a
    # worse answer than saying this arch does not carry that boundary.
    rm -f "$l"
    echo "kata: $(uname -m) carries no ${vmm}, so kata-${h} is not installed" >&2
  fi
done
[ "$linked" -gt 0 ] || { echo "kata: $v has no hypervisor this machine can run" >&2; exit 1; }
rm -rf "$tmp"`
	return sh(ctx, script)
}

// declareRuntimes writes the containerd runtimes k3s is to serve, and it exists
// because AUTO-DETECTION IS NOT A CONTRACT. k3s does detect runtimes at start —
// it logs "Found nvidia container runtime at /usr/bin/nvidia-container-runtime"
// on this very machine — but it searches paths of its own choosing, and it found
// neither runsc in /usr/local/bin nor kata, whose 4.x shim moved to
// /opt/kata/runtime-rs/bin. The symptom is not a warning: containerd simply has
// no such runtime, the RuntimeClass admits the pod anyway, and the kubelet loops
// on "unable to get OCI runtime for sandbox" while the pod sits ContainerCreating
// forever. Measured exactly that way before this function existed.
//
// So the boundaries are DECLARED, from the same list everything else here reads.
// containerd resolves a runtime_type `io.containerd.<name>.<ver>` to a binary
// `containerd-shim-<name>-<ver>` on its PATH, which is the shape the installer
// above links into /usr/local/bin — one naming rule, stated once at each end.
//
// `config-v3.toml.tmpl` is k3s's own extension point for a version-3 config, and
// `{{ template "base" . }}` keeps everything k3s would have written: this ADDS
// runtimes, it does not replace a config we would then have to maintain.
func declareRuntimes(ctx context.Context) error {
	var b strings.Builder
	b.WriteString("{{ template \"base\" . }}\n")
	for _, name := range boundary.Names {
		if name == "runc" || !present(name) {
			continue
		}
		// THE RUNTIME IS NAMED BY ITS HANDLER, NOT BY THE BOUNDARY. A
		// RuntimeClass carries a `handler`, and that string is what containerd
		// is asked for — so a runtime declared under the boundary's own name is
		// a class that admits the pod and a kubelet that then loops on
		// `no runtime for "runsc" is configured`. gvisor is the case that
		// proves it: the boundary is `gvisor`, the handler is `runsc`, and only
		// one of those two names may appear here. Production agrees — its
		// containerd carries runtimes.runsc, never runtimes.gvisor.
		h := handlers[name].handler
		typ := "io.containerd." + h + ".v2"
		if h == "runsc" {
			typ = "io.containerd.runsc.v1" // runsc's shim is v1, and only its own
		}
		fmt.Fprintf(&b, "\n[plugins.'io.containerd.cri.v1.runtime'.containerd.runtimes.%s]\n"+
			"  runtime_type = %q\n", h, typ)
	}
	const dir = "/var/lib/rancher/k3s/agent/etc/containerd"
	c := exec.CommandContext(ctx, "sudo", "sh", "-c",
		"mkdir -p "+dir+" && cat > "+dir+"/config-v3.toml.tmpl")
	c.Stdin, c.Stdout, c.Stderr = strings.NewReader(b.String()), os.Stdout, os.Stderr
	return c.Run()
}

// restartK3s is what makes a declared runtime real: k3s renders the template and
// containerd reloads at start. Agents run a different unit from servers, and a
// machine is one or the other, so both are asked and the one that is not
// installed is not an error.
func restartK3s(ctx context.Context) error {
	if err := declareRuntimes(ctx); err != nil {
		return err
	}
	return sh(ctx, "systemctl restart k3s 2>/dev/null || systemctl restart k3s-agent 2>/dev/null || true")
}

// writeClasses names every boundary in the cluster, mapping OUR name to the
// handler that runs it.
//
// IT WRITES ONLY TO A LOCAL k3s SERVER, and never to whatever kubeconfig is
// lying around. A RuntimeClass is CLUSTER-scoped, so it is the server's to
// declare and an agent has no business declaring one — an agent needs the
// binaries and the containerd config, which is all the steps above give it.
// This used to fall back to ~/.kube/config when /etc/rancher/k3s/k3s.yaml was
// absent, which is exactly the agent case, and on the first agent it ran on
// that file named a PRODUCTION cluster. It got as far as the apiserver and was
// stopped by an expired credential, which is luck and not a design. A node
// installer may configure the node it is running on; reaching a cluster it was
// merely pointed at is a different act, and one nobody asked for.
//
// runc gets no class. It is the node's own runtime, and apps/sandbox offers it
// only where the cluster keeps it to a pool of its own — a nodeSelector AND a
// toleration, on a pool no other boundary shares. That topology is a fleet
// decision about which machines exist, not something a single-node installer
// can honestly draw, so this writes the boundaries that isolate and leaves runc
// to the operator who can draw the pool.
func writeClasses(ctx context.Context) error {
	const kube = "/etc/rancher/k3s/k3s.yaml"
	if _, err := os.Stat(kube); err != nil {
		fmt.Println("   this node runs no k3s server, so its RuntimeClasses are the server's to declare — " +
			"the boundaries are installed and containerd knows them")
		return nil
	}
	var b strings.Builder
	for _, name := range boundary.Names {
		if name == "runc" || !present(name) {
			continue
		}
		fmt.Fprintf(&b, "---\napiVersion: node.k8s.io/v1\nkind: RuntimeClass\nmetadata:\n  name: %s\nhandler: %s\n",
			name, handlers[name].handler)
	}
	if b.Len() == 0 {
		return fmt.Errorf("no boundary is installed, so there is no class to write")
	}
	c := exec.CommandContext(ctx, "sudo", "env", "KUBECONFIG="+kube, "kubectl", "apply", "-f", "-")
	c.Stdin, c.Stdout, c.Stderr = strings.NewReader(b.String()), os.Stdout, os.Stderr
	return c.Run()
}
