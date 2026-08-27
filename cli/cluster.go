package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
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

// handlers maps a boundary to the containerd handler that runs it and the file
// whose presence proves the handler is installed. runc is the node's own
// runtime: k3s ships it, so it is never installed and never absent.
var handlers = map[string]struct{ handler, proof string }{
	"runc":     {"runc", ""},
	"gvisor":   {"runsc", "/usr/local/bin/runsc"},
	"kata-fc":  {"kata-fc", "/opt/kata/bin/containerd-shim-kata-fc-v2"},
	"kata-clh": {"kata-clh", "/opt/kata/bin/containerd-shim-kata-clh-v2"},
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
		if !st.Installed && strings.HasPrefix(b, "kata-") && !r.KVM {
			st.Why = "no /dev/kvm — this machine cannot boot a guest kernel"
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
	// The classes are written every time, not only when a boundary was just
	// installed: a class deleted out from under a working handler is exactly
	// the drift this repairs, and writing an identical class costs nothing.
	steps = append(steps, step{"write RuntimeClasses", writeClasses})

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
	if err := sh(ctx, script); err != nil {
		return err
	}
	return restartK3s(ctx)
}

// installKata installs both microVM boundaries — Firecracker and
// Cloud-Hypervisor arrive in one release, so this is one download.
func installKata(ctx context.Context) error {
	const script = `set -eu
case "$(uname -m)" in
  x86_64) a=amd64 ;;
  aarch64|arm64) a=arm64 ;;
  *) echo "kata: unsupported arch $(uname -m)" >&2; exit 1 ;;
esac
v=$(curl -sfL https://api.github.com/repos/kata-containers/kata-containers/releases/latest | grep -m1 '"tag_name"' | cut -d'"' -f4)
[ -n "$v" ] || { echo "kata: could not resolve the latest release" >&2; exit 1; }
tmp=$(mktemp -d); cd "$tmp"
curl -sfLO "https://github.com/kata-containers/kata-containers/releases/download/${v}/kata-static-${v}-${a}.tar.xz"
tar -xJf "kata-static-${v}-${a}.tar.xz" -C /
for h in fc clh; do
  ln -sf /opt/kata/bin/containerd-shim-kata-v2 "/opt/kata/bin/containerd-shim-kata-${h}-v2"
  ln -sf "/opt/kata/bin/containerd-shim-kata-${h}-v2" "/usr/local/bin/containerd-shim-kata-${h}-v2"
done
rm -rf "$tmp"`
	if err := sh(ctx, script); err != nil {
		return err
	}
	return restartK3s(ctx)
}

// restartK3s is how a newly-installed handler becomes a containerd runtime:
// k3s reads PATH at start. Agents run a different unit from servers, and a
// machine is one or the other, so both are asked and the one that is not
// installed is not an error.
func restartK3s(ctx context.Context) error {
	return sh(ctx, "systemctl restart k3s 2>/dev/null || systemctl restart k3s-agent 2>/dev/null || true")
}

// writeClasses names every boundary in the cluster, mapping OUR name to the
// handler that runs it.
//
// runc gets no class. It is the node's own runtime, and apps/sandbox offers it
// only where the cluster keeps it to a pool of its own — a nodeSelector AND a
// toleration, on a pool no other boundary shares. That topology is a fleet
// decision about which machines exist, not something a single-node installer
// can honestly draw, so this writes the boundaries that isolate and leaves runc
// to the operator who can draw the pool.
func writeClasses(ctx context.Context) error {
	var b strings.Builder
	for _, name := range boundary.Names {
		if name == "runc" || !present(name) {
			continue
		}
		fmt.Fprintf(&b, "---\napiVersion: node.k8s.io/v1\nkind: RuntimeClass\nmetadata:\n  name: %s\nhandler: %s\n", name, handlers[name].handler)
	}
	if b.Len() == 0 {
		return fmt.Errorf("no boundary is installed, so there is no class to write")
	}
	kube := "/etc/rancher/k3s/k3s.yaml"
	if _, err := os.Stat(kube); err != nil {
		if home, e := os.UserHomeDir(); e == nil {
			kube = filepath.Join(home, ".kube", "config")
		}
	}
	c := exec.CommandContext(ctx, "sudo", "env", "KUBECONFIG="+kube, "kubectl", "apply", "-f", "-")
	c.Stdin, c.Stdout, c.Stderr = strings.NewReader(b.String()), os.Stdout, os.Stderr
	return c.Run()
}
