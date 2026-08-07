package coding

// Where the sandbox credential is allowed to appear.
//
// The run hands a bearer to a process that is about to execute a language
// model's output, in a pod that also holds the model's own tools. Argv is the
// one place it must never be: /proc makes another process's command line
// readable, and this argv is echoed into the run's session narration and its
// audit line, so a credential there is published three ways at once.

import (
	"strings"
	"testing"
)

func TestTheCredentialIsNeverAnArgument(t *testing.T) {
	const secret = "eyJhbGciOiJSUzI1NiJ9.THE-BEARER.sig"
	got := keyed([]string{"dev", "exec", "--full-auto", "--", "fix the bug"})
	for i, a := range got {
		if strings.Contains(a, secret) {
			t.Fatalf("argv[%d] carries the credential: %q", i, a)
		}
	}
	// It is read from stdin into the environment, and the agent still runs.
	if !strings.Contains(got[2], "read -r HANZO_API_KEY") {
		t.Errorf("the wrapper does not read the credential: %q", got[2])
	}
	if !strings.Contains(got[2], "export HANZO_API_KEY") {
		t.Errorf("the credential is read but never exported: %q", got[2])
	}
	// `exec` matters: without it the shell outlives the agent and swallows its
	// signals and exit code, so a cancelled run would report the shell's status.
	if !strings.Contains(got[2], `exec "$@"`) {
		t.Errorf("the wrapper does not exec the agent, so it keeps the process: %q", got[2])
	}
}

func TestTheAgentArgvSurvivesIntact(t *testing.T) {
	// A wrapper that drops or reorders the agent's own arguments would change the
	// task being run. `$0` is spent on the shell's name, so the agent's argv must
	// begin immediately after it.
	argv := []string{"dev", "exec", "--full-auto", "--skip-git-repo-check", "--", "a prompt with spaces"}
	got := keyed(argv)
	if len(got) != len(argv)+4 {
		t.Fatalf("got %d words, want %d: %v", len(got), len(argv)+4, got)
	}
	if got[0] != "sh" || got[1] != "-c" || got[3] != "sh" {
		t.Fatalf("wrapper prefix is not `sh -c <script> sh`: %v", got[:4])
	}
	for i, want := range argv {
		if got[i+4] != want {
			t.Errorf("argv[%d] = %q, want %q", i, got[i+4], want)
		}
	}
}

func TestNoIdentityIsNotAnError(t *testing.T) {
	// A dev box holds no machine identity. That must read as "no credential" and
	// let the run proceed to the harness's own honest failure, not as a broken
	// deployment — the lease, the clone and the branch all still happened.
	t.Setenv("IAM_CLIENT_ID", "")
	t.Setenv("IAM_CLIENT_SECRET", "")
	if got := source(); got != nil {
		// source() is memoized per process, so this only asserts the shape when
		// this test is the one that built it.
		t.Skip("a machine identity was already resolved in this process")
	}
	k, err := key(t.Context())
	if err != nil {
		t.Fatalf("no identity reported an error: %v", err)
	}
	if k != "" {
		t.Fatalf("no identity produced a credential: %q", k)
	}
}
