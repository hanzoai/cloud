package sandbox

// What a desktop's container is told to run.
//
// A desktop image's CMD starts an X server, a window manager and the VNC/noVNC
// pair, and only then becomes the same `sleep infinity` the other classes run
// outright. Stating a command in the pod spec replaces that script — so the one
// class that exists to have a display came up with none, indistinguishable from
// `dev` except by a label, and healthy-looking the whole time. A sleeping pod
// answers every probe; only an X client ever notices.
//
// These cases are that difference, stated where it cannot drift back.

import "testing"

// podFor is the container of an ORDINARY sandbox — the zero cred, which is what
// every lease but a SuperAdmin's own carries. podWith (cred_test.go) is the same
// render with the identity's credentials stated.
func podFor(t *testing.T, class string) map[string]any {
	t.Helper()
	_, c := podWith(t, class, cred{})
	return c
}

func TestDesktopRunsItsImageAndTheOthersSleep(t *testing.T) {
	// THE REGRESSION. A command here shadows the image's CMD, and the desktop
	// entrypoint is the only thing that starts Xvfb. Its absence is not an error
	// anywhere: the pod runs, exec answers, and the class is silently `dev`.
	if cmd, stated := podFor(t, "desktop")["command"]; stated {
		t.Errorf("desktop states a command (%v), which replaces the image CMD that starts its screen", cmd)
	}
	// The other two are a place to run commands, not a program. They must keep
	// sleeping — deferring to the image would make the pod's lifetime depend on
	// whatever CMD an operator-named image happens to carry.
	for _, class := range []string{"exec", "dev"} {
		cmd, stated := podFor(t, class)["command"]
		if !stated {
			t.Errorf("%s: no command, so its lifetime is the image's to decide", class)
			continue
		}
		got, ok := cmd.([]any)
		if !ok || len(got) != 2 || got[0] != "sleep" || got[1] != "infinity" {
			t.Errorf("%s: command = %v, want [sleep infinity]", class, cmd)
		}
	}
}

func TestOnlyADesktopDeclaresAScreen(t *testing.T) {
	ports, stated := podFor(t, "desktop")["ports"]
	if !stated {
		t.Fatal("desktop declares no ports, so nothing names the screen it serves")
	}
	got := map[string]int64{}
	for _, p := range ports.([]any) {
		m := p.(map[string]any)
		got[m["name"].(string)] = m["containerPort"].(int64)
	}
	for name, want := range map[string]int64{"vnc": 5900, "novnc": 6080} {
		if got[name] != want {
			t.Errorf("desktop port %q = %d, want %d", name, got[name], want)
		}
	}
	// A port on a class with nothing listening is a claim the image does not
	// keep. exec and dev have no X server and no VNC bridge in them at all.
	for _, class := range []string{"exec", "dev"} {
		if p, stated := podFor(t, class)["ports"]; stated {
			t.Errorf("%s declares ports %v, but nothing in that image listens", class, p)
		}
	}
}

func TestEveryClassStillDropsEverythingAndRunsAsNobody(t *testing.T) {
	// The desktop's exemption is its COMMAND and nothing else. A class that runs
	// its own process is exactly the one somebody would be tempted to hand a
	// capability or a root uid to, so the shared floor is asserted per class
	// rather than assumed from the one that has no process of its own.
	for _, class := range []string{"exec", "dev", "desktop"} {
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
}
