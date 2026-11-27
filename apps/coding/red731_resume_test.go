package coding

// RED 731 H2: a run adopted after a cloud restart (Resume: same session, surviving sandbox, /tmp intact)
// keeps reading the DEAD FLIGHT's credential file. planted() writes the model file only when it is not
// there ("[ ! -s $f ]"), and the file is named by session alone (modelPath), so a new flight's freshly
// minted token never reaches the file dev's credential command reads first. The previous flight's last
// token is neither revoked (its process died) nor renewed: it ends at its own deadline, and from then on
// every model call of the resumed run is a 401 until the new flight's first renewal (35-40 minutes in).

import (
	"testing"
)

func TestRed731AFlightAdoptedAfterARestartReadsItsOwnCredential(t *testing.T) {
	home := t.TempDir()
	file := home + "/run/model.sess_1"
	inner := []string{"sh", "-c", `printf '%s' "$HANZO_API_KEY"`}

	// The first flight: its first turn plants its token, a renewal replaces it, then the process dies.
	const first, renewed = "eyJ.FIRST-FLIGHT-TOKEN.sig", "eyJ.FIRST-FLIGHT-RENEWED.sig"
	if out, err := runShell(t, home, first+"\nrun_ticket\n", planted(file, inner)...); err != nil {
		t.Fatalf("first flight: %v: %s", err, out)
	}
	if out, err := runShell(t, home, renewed+"\n", "sh", "-c", `IFS= read -r k; f=$1; `+put, "sh", file); err != nil {
		t.Fatalf("renewal: %v: %s", err, out)
	}

	// The second flight (Resume) mints its own token and starts its first turn in the same sandbox.
	const second = "eyJ.SECOND-FLIGHT-TOKEN.sig"
	if out, err := runShell(t, home, second+"\nrun_ticket\n", planted(file, inner)...); err != nil {
		t.Fatalf("second flight: %v: %s", err, out)
	}

	// dev's credential command (readKey) is what the resumed harness asks, first and after every 401.
	fake := t.TempDir()
	writeExec(t, fake+"/dev", "#!/bin/sh\necho dev-token-answer\n")
	got, _ := runShellPath(t, home, fake, "", "sh", "-c", readKey, "sh", file)
	if got != second {
		t.Fatalf("the resumed flight's harness is answered %q by its credential command; its own token is %q", got, second)
	}
}
