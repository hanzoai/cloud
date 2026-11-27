package sandbox

// What a desktop's container is told to run.
//
// A desktop image's CMD starts an X server, a window manager and the VNC/noVNC
// pair, and only then sleeps, as the other classes' first process (reaper) does
// outright. Stating a command in the pod spec replaces that script — so the one
// class that exists to have a display came up with none, indistinguishable from
// `dev` except by a label, and healthy-looking the whole time. A sleeping pod
// answers every probe; only an X client ever notices.
//
// These cases are that difference, stated where it cannot drift back.

import (
	"testing"

	"github.com/hanzoai/cloud/internal/sqltest"
)

// podFor is the container of an ORDINARY sandbox — the zero cred, which is what
// every lease but a SuperAdmin's own carries. podWith (cred_test.go) is the same
// render with the identity's credentials stated.
func podFor(t *testing.T, class string) map[string]any {
	t.Helper()
	_, c := podWith(t, class, cred{})
	return c
}

// Both cases below QUANTIFY OVER THE TABLE rather than naming desktop. They used
// to say `"desktop"` and `[]string{"exec", "dev"}`, which is the same list of
// classes written twice more — so `android`, which also runs its image, would
// have been asserted to sleep and would have failed a test that was right about
// the old set and wrong about the new one. Reading `classes[c].screen` makes the
// assertion follow the fact it is guarding.
func TestAScreenRunsItsImageAndTheRestSleep(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		for name, k := range classes {
			cmd, stated := podFor(t, name)["command"]
			if k.screen {
				// THE REGRESSION. A command here shadows the image's CMD, and that
				// entrypoint is the only thing that starts Xvfb. Its absence is not an
				// error anywhere: the pod runs, exec answers, and the class is silently
				// `dev`.
				if stated {
					t.Errorf("%s states a command (%v), which replaces the image CMD that starts its screen", name, cmd)
				}
				continue
			}
			// A class with no screen is a place to run commands, not a program. It must
			// keep sleeping — deferring to the image would make the pod's lifetime
			// depend on whatever CMD an operator-named image happens to carry — and
			// reap what its commands orphan (reaper).
			if !stated {
				t.Errorf("%s: no command, so its lifetime is the image's to decide", name)
				continue
			}
			got, ok := cmd.([]any)
			if !ok || len(got) != 3 || got[0] != "sh" || got[1] != "-c" || got[2] != reaper {
				t.Errorf("%s: command = %v, want sh -c the reaper", name, cmd)
			}
		}
	})
}

func TestOnlyAScreenDeclaresOne(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		for name, k := range classes {
			ports, stated := podFor(t, name)["ports"]
			if !k.screen {
				// A port on a class with nothing listening is a claim the image does not
				// keep. exec and dev have no X server and no VNC bridge in them at all.
				if stated {
					t.Errorf("%s declares ports %v, but nothing in that image listens", name, ports)
				}
				continue
			}
			if !stated {
				t.Errorf("%s declares no ports, so nothing names the screen it serves", name)
				continue
			}
			got := map[string]int64{}
			for _, p := range ports.([]any) {
				m := p.(map[string]any)
				got[m["name"].(string)] = m["containerPort"].(int64)
			}
			for port, want := range map[string]int64{"vnc": 5900, "novnc": 6080} {
				if got[port] != want {
					t.Errorf("%s port %q = %d, want %d", name, port, got[port], want)
				}
			}
		}
	})
}

// A class that names a device gets it on BOTH sides, because Kubernetes admits
// an extended resource only when request and limit agree — and a class that does
// not name one must never be handed it, since asking for a device is also asking
// to be scheduled where it exists.
func TestOnlyAClassThatNeedsAMachineAsksForOne(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		for name, k := range classes {
			res := podFor(t, name)["resources"].(map[string]any)
			for _, side := range []string{"requests", "limits"} {
				got, asked := res[side].(map[string]any)[kvmResource]
				switch {
				case k.kvm && !asked:
					t.Errorf("%s: no %s in %s, so it schedules onto a node that cannot run it", name, kvmResource, side)
				case k.kvm && got != int64(1):
					t.Errorf("%s: %s %s = %v, want 1", name, side, kvmResource, got)
				case !k.kvm && asked:
					t.Errorf("%s: asks for %s it does not need, which narrows where it may run", name, kvmResource)
				}
			}
		}
	})
}

// A class that states an envelope gets that envelope on both sides — which makes
// it Guaranteed QoS, and for the one class holding a whole emulated machine that
// is the point: node-pressure eviction takes the pod that asked for least first,
// and it ignores PodDisruptionBudgets on the way past.
func TestAStatedEnvelopeIsTheOneThePodGets(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		for name, k := range classes {
			if k.mem == "" {
				continue
			}
			res := podFor(t, name)["resources"].(map[string]any)
			for _, side := range []string{"requests", "limits"} {
				m := res[side].(map[string]any)
				for field, want := range map[string]string{"cpu": k.cpu, "memory": k.mem, "ephemeral-storage": k.disk} {
					if m[field] != want {
						t.Errorf("%s: %s.%s = %v, want %q", name, side, field, m[field], want)
					}
				}
			}
		}
	})
}

func TestEveryClassStillDropsEverythingAndRunsAsNobody(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		// The desktop's exemption is its COMMAND and nothing else. A class that runs
		// its own process is exactly the one somebody would be tempted to hand a
		// capability or a root uid to, so the shared floor is asserted per class
		// rather than assumed from the one that has no process of its own.
		for _, class := range []string{"exec", "dev", "desktop", "browser", "android"} {
			sc, ok := podFor(t, class)["securityContext"].(map[string]any)
			if !ok {
				t.Fatalf("%s: no container securityContext", class)
			}
			if sc["allowPrivilegeEscalation"] != false {
				t.Errorf("%s: allowPrivilegeEscalation = %v, want false", class, sc["allowPrivilegeEscalation"])
			}
			caps, _ := sc["capabilities"].(map[string]any)
			drop, _ := caps["drop"].([]any)
			if len(drop) != 1 || drop[0] != "ALL" {
				t.Errorf("%s: capabilities.drop = %v, want [ALL]", class, caps["drop"])
			}
		}
	})
}

// A sandbox is spread across the fleet's machines, and the constraint has to
// select EVERY sandbox to do it.
//
// The selector is the half worth a test. `labSandbox` carries each sandbox's own
// id, so a constraint matching it selects exactly one pod — itself — and spreads
// that pod against nothing. That is a silent no-op: the field is present, the
// pod is valid, the spec reads as configured, and the burst still lands on one
// machine. Matching on the label's EXISTENCE is what makes the set the whole
// population.
//
// It stays a PREFERENCE. `DoNotSchedule` on an uneven cluster is a lease that
// cannot start, which trades a caller's answer for a tidy distribution.
func TestSandboxesSpreadAcrossMachines(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		spec, _ := podWith(t, "exec", cred{})

		cs, ok := spec["topologySpreadConstraints"].([]any)
		if !ok || len(cs) != 1 {
			t.Fatalf("want one spread constraint, got %v", spec["topologySpreadConstraints"])
		}
		c, _ := cs[0].(map[string]any)

		if got := c["topologyKey"]; got != "kubernetes.io/hostname" {
			t.Fatalf("topologyKey = %v, want the node", got)
		}
		if got := c["whenUnsatisfiable"]; got != "ScheduleAnyway" {
			t.Fatalf("whenUnsatisfiable = %v — a sandbox must never go Pending to balance a cluster", got)
		}

		// The selector must reach every sandbox, not this one.
		sel, _ := c["labelSelector"].(map[string]any)
		if _, byValue := sel["matchLabels"]; byValue {
			t.Fatal("matchLabels selects this sandbox's own id — the constraint would spread it against nothing")
		}
		exprs, ok := sel["matchExpressions"].([]any)
		if !ok || len(exprs) != 1 {
			t.Fatalf("want one match expression, got %v", sel["matchExpressions"])
		}
		e, _ := exprs[0].(map[string]any)
		if e["key"] != labSandbox || e["operator"] != "Exists" {
			t.Fatalf("selector = %v, want %s Exists", e, labSandbox)
		}
	})
}

// A browser is the desktop with one more layer, and its row says so: a screen, no
// device, the desktop's lease and the desktop's hour. A row that drifted from the
// desktop's would price the same pod two ways, or drop the screen the page is
// watched on.
func TestABrowserHoldsWhatADesktopHolds(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		b, d := classes["browser"], classes["desktop"]
		if !b.screen {
			t.Fatal("browser has no screen: a page nobody can watch is not what the class is for")
		}
		if b != d {
			t.Errorf("browser = %+v, desktop = %+v: the rows differ", b, d)
		}
		if !b.scratch() {
			t.Error("browser is not scratch: a lease with no disk is how a person starts to look at a page")
		}
	})
}

// A CLASS THAT RUNS AN AGENT'S BUILD MAY BURST PAST ITS REQUEST (hanzo-inc/cloud#553):
// its limits are the measured build envelope, its requests the fleet's, so it
// reserves and is priced what exec is and may still take no project. In 2 CPU /
// 4 GiB / 8 GiB a `make test` timed out its packages and its pod was killed.
//
// MUTATION: drop burst from the limits and the build classes keep 2 / 4Gi / 8Gi.
func TestABuildClassMayBurstPastItsRequest(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		for _, name := range []string{"dev", "desktop", "browser"} {
			res := podFor(t, name)["resources"].(map[string]any)
			limits, requests := res["limits"].(map[string]any), res["requests"].(map[string]any)
			for field, want := range map[string]string{"cpu": builder.cpu, "memory": builder.mem, "ephemeral-storage": builder.disk} {
				if limits[field] != want {
					t.Errorf("%s: limits.%s = %v, want %s", name, field, limits[field], want)
				}
			}
			for field, want := range map[string]string{"cpu": "250m", "memory": "512Mi", "ephemeral-storage": "2Gi"} {
				if requests[field] != want {
					t.Errorf("%s: requests.%s = %v, want the fleet's %s", name, field, requests[field], want)
				}
			}
			if !classes[name].scratch() {
				t.Errorf("%s may no longer run with no project, which every coding run does", name)
			}
		}
		if lim := podFor(t, "exec")["resources"].(map[string]any)["limits"].(map[string]any); lim["memory"] != "4Gi" {
			t.Errorf("exec's limit moved: %v", lim)
		}
	})
}
