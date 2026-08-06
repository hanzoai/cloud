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
	spec.Env = []declareEnv{{Name: "INNOCENT_NAME", Value: declareSecretValue}} // never split
	_, err := declare(serviceWithKMS(kmsWithPinToken(t)), context.Background(), spec, modeCommit)
	if err == nil {
		t.Fatal("a declaration carrying credential material was committed to universe")
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
