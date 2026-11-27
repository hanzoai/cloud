package kms_test

// RED — rollout safety of the grant rule, kept as the regression for both.
//
// 1. A measuring document (`enforce: false`) with one edit a looser YAML reader
//    accepts but ParsePolicy refuses must not change the verdict a freshly
//    booted pod reaches. With nothing kept, no declaration MEASURES; with a
//    good one kept, the restart decides as the process before it did
//    (policy_test.go). Health says the mount is refused.
//
// 2. The live grant document (universe kms-grants.yaml, enforce line deleted)
//    must admit cloud's own release pipeline: .hanzo/workflows/cicd.yml logs in
//    as hanzo-admin-agent to read review/IAM_CLIENT_{ID,SECRET}, and its pin
//    and fanout steps (.hanzo/scripts/app-token.sh) as hanzo-cloud-release to
//    read the platform App key. The key reaches
//    that client and not the org-wide one. testdata/red_universe_grants.yaml is
//    that document as universe declares it.

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/internal/sqltest"
)

// Each document is `enforce: false` plus one edit a looser YAML reader accepts
// and ParsePolicy refuses. universe's gate refuses each of them too.
var ciPassesGoRefuses = map[string]string{
	"hand grant keys as a scalar": `
enforce: false
grants:
- holder: hanzo-operator
  org: hanzo
  from: charts
  read: [{path: /attendant, env: prod, keys: [HANZO_TOKEN]}]
- holder: zoo-ci
  org: zoo
  why: zoo's CI keeps its own under /ci
  read: [{path: /ci, env: prod, keys: "*"}]
`,
	"a top-level note": `
enforce: false
note: measuring since release N
grants:
- holder: hanzo-operator
  org: hanzo
  from: charts
  read: [{path: /attendant, env: prod, keys: [HANZO_TOKEN]}]
`,
	"a repeated read block": `
enforce: false
grants:
- holder: hanzo-operator
  org: hanzo
  from: charts
  read: [{path: /attendant, env: prod, keys: [HANZO_TOKEN]}]
- holder: hanzo-sites-ci
  org: hanzo
  why: the sites lane
  read: [{path: /, env: prod, keys: [HANZO_DEPLOY_TOKEN]}]
  read: [{path: /, env: prod, keys: [HANZO_DEPLOY_TOKEN]}]
`,
}

func TestRed_AMeasuringDocWithOneTypoStillMeasures(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		op := machine("hanzo-operator", "hanzo")

		// Control: the same document without the typo measures, and the operator reads.
		good := newGrantWorld(t, strings.Replace(ciPassesGoRefuses["a top-level note"], "note: measuring since release N\n", "", 1), true)
		good.read(t, "control: operator reads under enforce:false", "attendant/HANZO_TOKEN?env=prod", op, nil).isValue(t, valHanzoToken)

		for what, doc := range ciPassesGoRefuses {
			t.Run(what, func(t *testing.T) {
				w := newGrantWorld(t, doc, true)
				p := w.read(t, "operator reads under a measuring doc with "+what, "attendant/HANZO_TOKEN?env=prod", op, nil)
				if p.status != 200 {
					t.Errorf("DEFECT: the document says `enforce: false` and grants this read, CI accepted it, "+
						"and a pod booted on it refuses the operator: %d %s", p.status, p.body)
				}
				// The probe names the refused mount, so the typo is visible now and
				// not at the next restart.
				h := w.call(t, "health", "GET", "/v1/kms/health", op, nil, "")
				if !strings.Contains(h.body, `"refused":"`) || !strings.Contains(h.body, `"enforcing":false`) {
					t.Errorf("health does not say the mounted declaration was refused: %s", h.body)
				}
			})
		}
	})
}

func TestRed_TheLiveGrantsAdmitCloudsOwnReleasePipeline(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		b, err := os.ReadFile("testdata/red_universe_grants.yaml")
		if err != nil {
			t.Fatalf("testdata: %v", err)
		}
		doc := strings.Replace(string(b), "enforce: false\n", "", 1) // step 4 of the rollout
		w := newGrantWorld(t, doc, true)
		for ref, v := range map[string]string{
			"orgs/hanzo/review/IAM_CLIENT_ID@prod":            "rid",
			"orgs/hanzo/platform/GITHUB_APP_ID@prod":          "app",
			"orgs/hanzo/platform/GITHUB_APP_PRIVATE_KEY@prod": "key",
		} {
			if err := w.deps.KMS.PutSecret(context.Background(), ref, []byte(v)); err != nil {
				t.Fatalf("seed %s: %v", ref, err)
			}
		}

		// The reviewer logs in as the org-wide CI client.
		agent := machine("hanzo-admin-agent", "hanzo")
		w.read(t, "hanzo-admin-agent reads the reviewer's client", "review/IAM_CLIENT_ID?env=prod", agent, nil).isValue(t, "rid")

		// The pin step logs in as its own client and signs with the App key.
		release := machine("hanzo-cloud-release", "hanzo")
		w.read(t, "hanzo-cloud-release reads the App id", "platform/GITHUB_APP_ID?env=prod", release, nil).isValue(t, "app")
		w.read(t, "hanzo-cloud-release reads the App key", "platform/GITHUB_APP_PRIVATE_KEY?env=prod", release, nil).isValue(t, "key")

		// The key goes no wider: every hanzo-org repository's CI holds the org-wide
		// client, and the release client reads nothing but the key.
		w.read(t, "hanzo-admin-agent reads the App key", "platform/GITHUB_APP_PRIVATE_KEY?env=prod", agent, nil).refused(t, "")
		w.read(t, "hanzo-cloud-release reads the reviewer's client", "review/IAM_CLIENT_ID?env=prod", release, nil).refused(t, "")
	})
}
