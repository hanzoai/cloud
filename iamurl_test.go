package cloud

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	luxlog "github.com/luxfi/log"
)

// ONE ADDRESS, ONE POLICY, ONE ENV READ.
//
// IAMBaseURL's own doc has claimed "there is exactly one now" since it was
// written, and it was wrong: three more callers resolved the IAM address
// themselves and grew three DIFFERENT fallbacks — the public issuer, nothing at
// all, and a hardcoded cluster address — plus a fifth env name (IAM_INTERNAL_URL)
// that no cloud deployment sets. A copy of a policy does not disagree until one
// is edited, so the guard is structural rather than a comment.
func TestIAMAddressHasOneReader(t *testing.T) {
	var offenders []string
	pat := regexp.MustCompile(`(?:Getenv|getenv|env)\(\s*"IAM_URL"|"IAM_INTERNAL_URL"`)

	err := filepath.WalkDir(".", func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != "." && strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		if path == "iamurl.go" { // the one legitimate reader
			return nil
		}
		b, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		if pat.Match(b) {
			offenders = append(offenders, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if len(offenders) > 0 {
		t.Errorf("these read the IAM address themselves instead of asking iamurl.go: %v\n"+
			"use IAMBase() for the address, IAMExternal() for whether one is named — a second\n"+
			"reader is a second policy, and the last three grew three different fallbacks", offenders)
	}
}

// The two questions are DIFFERENT, and conflating them is the bug: a deployment
// with only a public issuer resolves a real address while naming no external IAM.
func TestIAMExternalIsNotTheSameQuestionAsTheAddress(t *testing.T) {
	t.Setenv("IAM_URL", "")
	t.Setenv("CLOUD_IAM_ISSUER", "https://hanzo.id")

	if IAMExternal() {
		t.Error("no IAM_URL, yet IAMExternal() says an external IAM is named")
	}
	if got := IAMBase(); got != "https://hanzo.id" {
		t.Errorf("IAMBase() = %q, want the public issuer as the last resort", got)
	}

	t.Setenv("IAM_URL", "http://iam.hanzo.svc/")
	if !IAMExternal() {
		t.Error("IAM_URL is set, yet IAMExternal() says no external IAM")
	}
	if got := IAMBase(); got != "http://iam.hanzo.svc" {
		t.Errorf("IAMBase() = %q, want the in-cluster service with the slash trimmed", got)
	}
}

// A SIBLING is reached by name, not by its public URL.
//
// `ai` is a plugin of this same binary running as its own process. Resolving it
// to https://api.hanzo.ai sent the pod out through Cloudflare and back, minting
// an OAuth token to authenticate to its own deployment — to reach code one
// socket away. Which client a process gets is decided by WHAT IT IS: a process
// that does not carry `ai` has a sibling and asks the plane; the process that IS
// `ai` takes the real transport, because asking the plane there would be it
// calling itself.
func TestASiblingReachesAIByNameNotByURL(t *testing.T) {
	log := luxlog.NewNoOpLogger()

	sibling := &Config{
		Enable:             []string{"agents"}, // does not carry ai
		AIBaseURL:          "https://api.hanzo.ai/v1",
		AIAPIKey:           "sk-live",
		AIAuthClientID:     "hanzo-cloud",
		AIAuthClientSecret: "shh",
	}
	if _, ok := pickCompletionsClient(sibling, log).(peerAI); !ok {
		t.Error("a sibling took the public gateway while `ai` was one socket away")
	}

	// The ai process itself must NOT ask the plane — that is a self-call.
	self := &Config{Enable: []string{"ai"}, AIBaseURL: "https://api.hanzo.ai/v1", AIAPIKey: "sk-live"}
	if _, ok := pickCompletionsClient(self, log).(peerAI); ok {
		t.Error("the ai process resolved itself to the plane — it would call itself forever")
	}

	// Embeddings follow the same rule, so one is not a cheaper way round.
	if _, ok := pickEmbedClient(sibling, log).(peerAI); !ok {
		t.Error("embeddings took the public gateway while `ai` was one socket away")
	}
}
