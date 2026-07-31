package manifest

import "testing"

// TestExactlyOneAppIsOpen: zip refuses a second open plugin at Load, which would
// abort a host mid-boot. The manifest is where that is decided, so it is where it
// is checked — before a deployment finds out by not starting.
func TestExactlyOneAppIsOpen(t *testing.T) {
	var open []string
	for _, a := range Apps {
		if a.Open {
			open = append(open, a.Name)
		}
	}
	if len(open) != 1 || open[0] != "tools" {
		t.Fatalf("exactly one app may be open and it is the tool plane, got %v", open)
	}
	for _, a := range Apps {
		if a.Open && a.Coresident {
			t.Fatalf("%s is open and co-resident: a co-resident app is never Load'ed, so it can never be asked", a.Name)
		}
	}
}

// TestOpenReachesTheSpec: the flag is a property of the APP, so it must survive
// every rung of the binary-resolution ladder — an operator's address, an
// operator's path, the binary on disk, a published release.
func TestOpenReachesTheSpec(t *testing.T) {
	for _, a := range Apps {
		if a.Open && !a.Plugin().Open {
			t.Fatalf("%s is open in the manifest and not in the spec the host loads", a.Name)
		}
	}
	t.Setenv("CLOUD_TOOLS_ADDR", "http://127.0.0.1:9999")
	for _, a := range Apps {
		if a.Name == "tools" && !a.Plugin().Open {
			t.Fatal("a remotely-mounted tools app lost its open flag")
		}
	}
}
