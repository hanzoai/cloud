package cloud

import (
	"os"
	"regexp"
	"strconv"
	"testing"
)

// The image puts the per-app binaries in a fixed number of layers: the build
// stage divides a hash of each app's name by that number, and the final stage
// names one COPY per group. The number is therefore written TWICE, in two
// languages, and neither can derive it from the other — .dockerignore keeps the
// Dockerfile out of the context it builds, so the build cannot read its own COPY
// lines.
//
// A count raised past the COPY lines does not fail anything: the apps that hash
// into the missing groups are built, land in a directory nothing copies, and are
// simply absent from the image. The first evidence is a host fork/exec'ing a file
// that is not there, on the first request to that subsystem, in production.
//
// So the two are held together here, against the bytes in git.
var (
	layerCount = regexp.MustCompile(`(?m)^\s*export layers=([0-9]+);`)
	layerCopy  = regexp.MustCompile(`(?m)^COPY --link --from=build /plugins/([0-9]+)/ /$`)
)

func TestPluginLayersAndTheirCopiesAgree(t *testing.T) {
	image, err := os.ReadFile("Dockerfile")
	if err != nil {
		t.Fatalf("read Dockerfile: %v", err)
	}

	declared := layerCount.FindSubmatch(image)
	if declared == nil {
		t.Fatalf("no `export layers=<n>;` in the Dockerfile — the plugin build no longer " +
			"states how many layers it divides the apps into, so nothing here can hold it " +
			"to the COPY lines")
	}
	n, err := strconv.Atoi(string(declared[1]))
	if err != nil || n < 1 {
		t.Fatalf("layers=%q is not a count", declared[1])
	}

	copies := layerCopy.FindAllSubmatch(image, -1)
	if len(copies) != n {
		t.Fatalf("the build divides the apps into %d layers and the final stage copies %d of them: "+
			"every app whose name hashes past the last COPY is built and then left out of the image", n, len(copies))
	}
	for i, c := range copies {
		if got := string(c[1]); got != strconv.Itoa(i) {
			t.Fatalf("COPY %d names /plugins/%s/ — the groups must be 0..%d, each once and in order, "+
				"because the build writes into exactly those directories", i, got, n-1)
		}
	}
}
