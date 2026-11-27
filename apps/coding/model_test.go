package coding

import (
	"slices"
	"testing"
)

// The agent's model is named by the platform on every run. Without it `dev`
// uses whatever default its image baked in, and sandbox 1.3.4's was a retired
// model: runs died on a stream that never completed.
func TestTheAgentModelIsNamedNotInherited(t *testing.T) {
	for _, tool := range []string{"", "dev", "gen"} {
		argv := argvFor(tool, "", "", "")
		i := slices.Index(argv, "-m")
		if i < 0 || i+1 >= len(argv) || argv[i+1] != codingModel {
			t.Fatalf("tool %q: argv %q does not name the model", tool, argv)
		}
		// The task is read from stdin: `-` is the last argument, after `--`.
		if argv[len(argv)-1] != "-" || argv[len(argv)-2] != "--" {
			t.Fatalf("tool %q: argv does not end reading the task from stdin: %q", tool, argv)
		}
	}
}

// The model is a flag's value when one is set: a run names what the flag names.
func TestTheAgentModelIsTheFlagsValue(t *testing.T) {
	argv := argvFor("dev", "", "zen6", "")
	if i := slices.Index(argv, "-m"); i < 0 || argv[i+1] != "zen6" {
		t.Fatalf("argv %q does not name the flag's model", argv)
	}
}
