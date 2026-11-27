package coding

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/hanzoai/cloud/client"
	"github.com/zap-proto/zip"
)

// A PUBLISH THAT FAILS ENDS ITS DEPLOYMENT WITH A REASON A PERSON READS WHOLE:
// one sentence that starts with a capital, names what went wrong in the build's
// own words, and carries none of the step labels or exit plumbing in front of it.
func TestAFailedPublishSaysWhyInASentence(t *testing.T) {
	// What findScript itself prints for a repository whose build made no site.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh", "-c", findScript)
	cmd.Dir = dir
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err == nil {
		t.Fatal("findScript found a site in a repository with only a package.json")
	}
	nosite := stderr.String()

	for name, c := range map[string]struct {
		answer func(argv []string) (client.Ran, bool)
		want   string
	}{
		"a build that made no site": {
			answer: func(argv []string) (client.Ran, bool) {
				if len(argv) == 3 && argv[2] == findScript {
					return client.Ran{ExitCode: 3, Stderr: nosite}, true
				}
				return client.Ran{}, false
			},
			want: "The build made no site",
		},
		"a build that exited": {
			answer: func(argv []string) (client.Ran, bool) {
				if len(argv) == 3 && argv[2] == buildScript {
					return client.Ran{ExitCode: 1, Stdout: "> next build\n", Stderr: "Error: Cannot find module 'tailwindcss'\n"}, true
				}
				return client.Ran{}, false
			},
			want: "The build exited 1: Error: Cannot find module 'tailwindcss'",
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, d, p, _ := publishWorld(t)
			tip := theSHA
			ok := siteAnswers(p, &tip)
			p.answer = func(argv []string) client.Ran {
				if r, hit := c.answer(argv); hit {
					return r
				}
				return ok(argv)
			}
			ctx := zip.WithCaller(context.Background(), zip.Caller{Org: "acme"})
			if _, err := Publish(ctx, "acme", client.PublishIn{Project: "site"}, nil); err != nil {
				t.Fatalf("Publish: %v", err)
			}
			var closed client.SiteCloseIn
			select {
			case closed = <-d.closedCh:
			case <-time.After(10 * time.Second):
				t.Fatal("the publish never ended its deployment")
			}
			why := closed.Message
			if closed.Live || !strings.HasPrefix(why, c.want) {
				t.Fatalf("the deployment ended %q, want it to start %q", why, c.want)
			}
			if strings.Contains(why, "coding:") || strings.Contains(why, "exit 3") || strings.HasSuffix(why, "…") {
				t.Fatalf("the reason carries plumbing or was cut: %q", why)
			}
			if r := []rune(why); len(r) == 0 || !unicode.IsUpper(r[0]) {
				t.Fatalf("the reason does not start a sentence: %q", why)
			}
			if name == "a build that made no site" && !slices.ContainsFunc(strings.Fields(why), func(w string) bool { return strings.Trim(w, ",.") == "public" }) {
				t.Fatalf("the reason does not name where a site is looked for: %q", why)
			}
		})
	}
}

// A reason past its bound is cut at a character, never inside one, and a search
// that failed saying nothing still says what failed.
func TestAReasonIsCutWholeAndNeverEmpty(t *testing.T) {
	long := strings.Repeat("é", reasonCap)
	got := reason(errors.New(long))
	if !utf8.ValidString(got) || !strings.HasSuffix(got, "…") {
		t.Fatalf("the cut reason is not whole text: %q", got[len(got)-8:])
	}
	if reason(errors.New("coding: the build exited 2: boom")) != "The build exited 2: boom" {
		t.Fatalf("the label was kept or the sentence not started: %q", reason(errors.New("coding: the build exited 2: boom")))
	}
}
