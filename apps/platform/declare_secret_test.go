package platform

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hanzoai/namespace"
)

// declare_secret_test.go — nothing credential-shaped may reach a values file.
//
// A values file is committed to universe git. Git history is replicated to every
// clone and cannot be unpublished, so a credential written into one is cleartext
// FOREVER for everyone who can read the repository — strictly worse than the
// database column the operator lane leaked into.

const declareSecretValue = "sk_live_51H8xQ2LkdIwHu7ixR0Nn7Yq2vB3mZ9pK"

// The render must carry a REFERENCE and never material — the chart's own rule
// (charts/app/templates/kmssecret.yaml: "Secrets are REFERENCES, never values").
func TestRenderCarriesAReferenceAndNeverTheSecret(t *testing.T) {
	spec := testSpec()
	spec.Env = []declareEnv{{Name: "PORT", Value: "3000"}}
	spec.Public = []string{"PORT"}
	spec.SecretKeys = []string{"STRIPE_SK"}

	out := string(spec.render())
	if strings.Contains(out, declareSecretValue) {
		t.Fatalf("GIT PLAINTEXT: the rendered declaration carries the secret:\n%s", out)
	}
	for _, want := range []string{
		"kmsSecrets:",
		"secretsPath: /platform/" + spec.Name,
		"projectSlug: " + spec.Org,
		"secretName: " + managedSecretName(spec.Name),
		"- STRIPE_SK",
		"valueFrom:",
		"secretKeyRef:",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the render is missing %q, so the reference does not resolve:\n%s", want, out)
		}
	}
	// The plain entry still carries its value — configuration is not a secret.
	if !strings.Contains(out, "value: '3000'") {
		t.Errorf("ordinary configuration was dropped:\n%s", out)
	}
}

// The KMS coordinate the file NAMES must be the one the value was SEALED at, or
// the pod waits forever on a Secret that never arrives.
func TestTheReferenceResolvesToTheSealedCoordinate(t *testing.T) {
	spec := testSpec()
	spec.SecretKeys = []string{"STRIPE_SK"}
	out := string(spec.render())

	// kmsSecretRef is orgs/<org>/platform/<app>/<KEY>; the chart composes
	// projectSlug=<org> with secretsPath=/platform/<app> and pulls key <KEY>.
	sealed := kmsSecretRef(spec.Org, spec.Name, "STRIPE_SK")
	if want := "orgs/" + spec.Org + "/platform/" + spec.Name + "/STRIPE_SK"; sealed != want {
		t.Fatalf("the seal coordinate moved: %q", sealed)
	}
	if !strings.Contains(out, "projectSlug: "+spec.Org) || !strings.Contains(out, "secretsPath: /platform/"+spec.Name) {
		t.Fatalf("the reference does not address the sealed coordinate %q:\n%s", sealed, out)
	}
}

// The last-line guard: even if the split were bypassed, the bytes about to be
// committed are refused.
func TestDeclareRefusesToCommitCredentialBytes(t *testing.T) {
	remote, bare := gitRemote(t, pinFixture)
	defer swapUniverseRemote(remote)()
	reg := fakeRegistryFunc(t, func(string) bool { return true })
	defer swapRegistryBase(reg.URL)()

	spec := testSpec()
	// Reaches the render WITHOUT the caller ever declaring it public — the shape
	// a bug in the split (or a future second writer) would produce. The name is
	// deliberately innocuous and the value deliberately credential-shaped, so
	// neither a key rule nor a value rule is what catches it: the audit does,
	// because the caller never authorised a cleartext write for this name.
	spec.Env = []declareEnv{{Name: "INNOCENT_NAME", Value: declareSecretValue}}
	spec.Public = nil
	_, err := declare(serviceWithKMS(kmsWithPinToken(t)), context.Background(), spec, modeCommit)
	if err == nil {
		t.Fatal("a declaration carrying unauthorised cleartext was committed to universe")
	}
	if !strings.Contains(err.Error(), "INNOCENT_NAME") {
		t.Errorf("the refusal must name the offending entry: %v", err)
	}
	if strings.Contains(err.Error(), declareSecretValue) {
		t.Fatal("the refusal echoed the credential")
	}
	if _, err := gitTry(bare, "show", universeBranch+":"+declarePath(spec.Org, spec.Name)); err == nil {
		t.Fatal("the refused declaration reached main anyway")
	}
}

// A branch write is refused too. A branch is inert for DEPLOYMENT, but it is
// still a commit in the same repository — the leak is the history, not the ref.
func TestABranchDeclarationIsAlsoRefused(t *testing.T) {
	remote, _ := gitRemote(t, pinFixture)
	defer swapUniverseRemote(remote)()
	spec := testSpec()
	spec.Env = []declareEnv{{Name: "X", Value: declareSecretValue}}
	spec.Public = nil
	if _, err := declare(serviceWithKMS(kmsWithPinToken(t)), context.Background(), spec, modeBranch); err == nil {
		t.Fatal("credential material reached a branch; git history is the leak, not the ref")
	}
}

// ── V1: the namespaces the cluster actually runs ────────────────────────────

func TestReservedCoversTheClusterInfraNamespaces(t *testing.T) {
	for _, ns := range []string{
		"adnexus", "bootnode", "kms-operator-system", "nchain-system",
		"operator-system", "pars-mainnet", "preview", "registry", "team-go",
		// the suffix rule, so the next operator installed is covered too
		"some-new-operator-system", "cert-manager-system",
	} {
		if !reserved(ns) {
			t.Errorf("RESERVATION GAP: %q is a live cluster namespace but is claimable by an org of that name", ns)
		}
		if owner(ns) != "" {
			t.Errorf("RESERVATION GAP: %q is attributed to org %q on the read path", ns, owner(ns))
		}
	}
	// A real customer org is unaffected.
	for _, ns := range []string{"acme", "globex", "widgets-co", namespace.Sanitize("Acme")} {
		if reserved(ns) {
			t.Errorf("ordinary org %q was reserved", ns)
		}
	}
}

// ── V2: templatePatch and the env functions ─────────────────────────────────

func TestFenceRefusesATemplatePatchThatTouchesTheProject(t *testing.T) {
	body := "spec:\n" +
		"  template:\n    spec:\n      project: '{{ .path.basename }}'\n" +
		"  templatePatch: |\n    spec:\n      project: hanzo-platform\n"
	if _, err := fenceOf(writeSet(t, body), "acme"); err == nil {
		t.Fatal("a templatePatch overriding the project was not detected; it merges OVER the template")
	}
}

// A template reading the PROCESS environment renders one way here and another in
// the controller, which strips those functions. It must be refused, not guessed.
func TestFenceRefusesATemplateReadingTheEnvironment(t *testing.T) {
	for _, expr := range []string{`{{ env "HOME" }}`, `{{ expandenv "$HOME" }}`} {
		body := "spec:\n  template:\n    spec:\n      project: '" + expr + "'\n"
		if _, err := fenceOf(writeSet(t, body), "acme"); err == nil {
			t.Errorf("a project template using %s was accepted; it renders from cloud's environment, not the controller's", expr)
		}
	}
}

func writeSet(t *testing.T, body string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, filepath.FromSlash(filepath.Dir(fleetSet))), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(fleetSet)), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

// ── seal by default kills the miss CLASS, not eight instances ───────────────

// Red proved mustSeal missed 8 of 8 real credential shapes, each for a different
// reason. Under seal-by-default none of them can be published, and no new shape
// can either — the default does not depend on recognising anything.
func TestSealByDefaultPublishesNothingUnmarked(t *testing.T) {
	for _, tc := range []struct{ name, value, why string }{
		{"PGPASSWORD", "s3cr3t", "libpq's own variable; one token to any splitter"},
		{"DB_PW", "s3cr3t", "`pw` is not a token"},
		{"ADMIN_PW", "Tr0ub4dor&3", "short and symbol-rich"},
		{"APP_PASSPHRASE", "correct horse battery staple", "spaces, so no token shape at all"},
		{"KUBECONFIG", "apiVersion: v1\nclusters:\n- cluster:\n    certificate-authority-data: LS0tL", "cluster-admin, base64 material, no PEM armour"},
		{"MFA_SEED", "JBSWY3DPEHPK3PXP", "base32, short, low entropy"},
		{"SIGNING_MATERIAL", "n0t-v3ry-r4ndom", "symbol-rich but short"},
		{"OPAQUE", "aB3", "too short for any entropy rule"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Unmarked ⇒ sealed. It never reaches spec.Env, so it cannot render.
			e := declareEnv{Name: tc.name, Value: tc.value}
			if e.Public {
				t.Fatal("the zero value of declareEnv must be SECRET")
			}
			spec := testSpec()
			spec.SecretKeys = []string{tc.name}
			body := string(spec.render())
			if strings.Contains(body, tc.value) {
				t.Fatalf("GIT PLAINTEXT (%s): %s reached the values file:\n%s", tc.why, tc.name, body)
			}
			if !strings.Contains(body, "secretKeyRef:") {
				t.Fatalf("%s did not render as a reference:\n%s", tc.name, body)
			}
		})
	}
}

// V3's other half: a config value the operator NEEDS to read is marked public
// and stays readable. Seal-by-default is not seal-everything.
func TestPublicConfigurationStaysReadable(t *testing.T) {
	spec := testSpec()
	spec.Env = []declareEnv{
		{Name: "GIT_COMMIT", Value: "a9af1cb6", Public: true},
		{Name: "IMAGE_DIGEST", Value: "sha256:abc", Public: true},
		{Name: "TENANT_ID", Value: "acme", Public: true},
	}
	spec.Public = []string{"GIT_COMMIT", "IMAGE_DIGEST", "TENANT_ID"}
	body := spec.render()
	if err := auditRender(body, spec.publicSet()); err != nil {
		t.Fatalf("public configuration was refused: %v", err)
	}
	for _, want := range []string{"value: 'a9af1cb6'", "value: 'sha256:abc'", "value: 'acme'"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("public configuration was sealed away: missing %q", want)
		}
	}
}

// The audit is INDEPENDENT of the classifier: an unauthorised cleartext value is
// refused even when it looks like nothing at all.
func TestAuditRefusesUnauthorisedCleartextWhateverItLooksLike(t *testing.T) {
	spec := testSpec()
	spec.Env = []declareEnv{{Name: "HARMLESS", Value: "3000"}} // not credential-shaped
	spec.Public = nil                                          // ...and never authorised
	if err := auditRender(spec.render(), spec.publicSet()); err == nil {
		t.Fatal("the audit accepted a cleartext value the caller never marked public")
	}
}

// ── V6: the patch guard reads what the patch PRODUCES ───────────────────────

// A substring scan for "project" is defeated by the engine that runs it.
func TestFenceRefusesAPatchThatEvadesASubstringScan(t *testing.T) {
	for _, patch := range []string{
		`spec:` + "\n" + `  {{ "pro" }}ject: hanzo-platform`,
		`spec:` + "\n" + `  "\x70roject": hanzo-platform`,
		`spec:` + "\n" + `  {{ printf "%s%s" "pro" "ject" }}: hanzo-platform`,
	} {
		body := "spec:\n  template:\n    spec:\n      project: '{{ .path.basename }}'\n" +
			"  templatePatch: |\n    " + strings.ReplaceAll(patch, "\n", "\n    ") + "\n"
		if _, err := fenceOf(writeSet(t, body), "acme"); err == nil {
			t.Errorf("a templatePatch evading a substring scan was accepted:\n%s", patch)
		}
	}
}

// The REAL fleet patch touches no fence and must still be admitted, or this
// check refuses the very template it exists to confirm.
func TestFenceAdmitsTheRealFleetPatch(t *testing.T) {
	body := "spec:\n  template:\n    spec:\n      project: '{{ .path.basename }}'\n" +
		"  templatePatch: |\n" +
		"    {{- if dig \"cd\" \"automated\" false . }}\n" +
		"    spec:\n      syncPolicy:\n        automated:\n          prune: false\n          selfHeal: true\n" +
		"    {{- else }}\n    {}\n    {{- end }}\n"
	got, err := fenceOf(writeSet(t, body), "acme")
	if err != nil {
		t.Fatalf("the real fleet patch was refused: %v", err)
	}
	if got != "acme" {
		t.Fatalf("fence = %q, want acme", got)
	}
}
