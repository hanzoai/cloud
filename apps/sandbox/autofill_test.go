package sandbox

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk/secret"
	"k8s.io/client-go/tools/remotecommand"
)

type fakeKMS struct {
	secrets map[string]string
}

func (k *fakeKMS) GetSecret(_ context.Context, ref string) ([]byte, error) {
	v, ok := k.secrets[ref]
	if !ok {
		return nil, secret.ErrNone
	}
	return []byte(v), nil
}
func (k *fakeKMS) PutSecret(_ context.Context, ref string, val []byte) error {
	k.secrets[ref] = string(val)
	return nil
}
func (k *fakeKMS) DeleteSecret(_ context.Context, ref string) error {
	delete(k.secrets, ref)
	return nil
}
func (*fakeKMS) Sign(context.Context, string, []byte) ([]byte, error) { return nil, nil }

type stdinSpy struct {
	stdout map[string]string
	calls  []struct {
		argv  []string
		stdin string
	}
}

func (s *stdinSpy) stream(_ context.Context, _, _ string, argv []string, stdin io.Reader, stdout, _ io.Writer) error {
	var in string
	if stdin != nil {
		b, _ := io.ReadAll(stdin)
		in = string(b)
	}
	s.calls = append(s.calls, struct {
		argv  []string
		stdin string
	}{argv: append([]string(nil), argv...), stdin: in})
	if stdout != nil && s.stdout != nil {
		for k, v := range s.stdout {
			if strings.Contains(strings.Join(argv, " "), k) {
				_, _ = io.WriteString(stdout, v)
				delete(s.stdout, k)
				break
			}
		}
	}
	return nil
}

func (*stdinSpy) tty(context.Context, string, string, []string, io.Reader, io.Writer, remotecommand.TerminalSizeQueue) error {
	return nil
}

func seedDesktop(t *testing.T, s *Service, org, id string) Sandbox {
	t.Helper()
	store, err := storeFor(s, org)
	if err != nil {
		t.Fatalf("storeFor(%s): %v", org, err)
	}
	m := Sandbox{
		ID: id, Org: org, Kind: KindSandbox, Class: "desktop", Status: "running",
		Pod: podName(id), CreatedAt: 1, LastUsedAt: 1, ExpiresAt: time.Now().Unix() + 3600,
	}
	if err := store.Put(context.Background(), m); err != nil {
		t.Fatalf("seedDesktop: %v", err)
	}
	return m
}

func TestAutofillTypesViaStdinAndNeverRevealsSecrets(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		str := &stdinSpy{}
		s := service(t, str)
		s.KMS = &fakeKMS{secrets: map[string]string{
			"orgs/acme/settings/vault/item_gh_secret": "s3cr3t-P@ssw0rd!",
			"orgs/acme/settings/vault/item_gh_totp":   "otpauth://totp/GitHub:alice?secret=JBSWY3DPEHPK3PXP&issuer=GitHub",
		}}
		ready(t, s)
		m := seedDesktop(t, s, "acme", "m-desk")

		origRead := vaultRead
		t.Cleanup(func() { vaultRead = origRead })
		vaultRead = func(_ context.Context, org string) ([]byte, error) {
			if org != "acme" {
				t.Fatalf("vaultRead org = %q, want acme", org)
			}
			return []byte(`{"items":[{"id":"cred_1","kind":"login","name":"GitHub","domain":"github.com","username":"alice@hanzo.ai","secretKey":"item_gh_secret","totpSecretKey":"item_gh_totp","source":"1password"}]}`), nil
		}

		out, err := Computer(s, context.Background(), "acme", client.ComputerIn{
			ID:         m.ID,
			Action:     "autofill",
			Credential: "https://github.com/login",
			Submit:     true,
		})
		if err != nil {
			t.Fatalf("Computer autofill: %v", err)
		}
		if !out.OK {
			t.Fatalf("autofill failed: %s", out.Text)
		}
		if strings.Contains(out.Text, "s3cr3t-P@ssw0rd!") {
			t.Fatalf("autofill leaked secret in response text: %q", out.Text)
		}
		if !strings.Contains(out.Text, "GitHub") || !strings.Contains(out.Text, "1password") {
			t.Fatalf("unexpected autofill summary: %q", out.Text)
		}
		// Expect: username via --file -, Tab key, password via --file -, Return key.
		if len(str.calls) != 4 {
			t.Fatalf("calls = %d, want 4: %+v", len(str.calls), str.calls)
		}
		for i, c := range str.calls {
			joined := strings.Join(c.argv, " ")
			if strings.Contains(joined, "s3cr3t-P@ssw0rd!") {
				t.Fatalf("call %d leaked password in argv: %v", i, c.argv)
			}
		}
		if str.calls[0].stdin != "alice@hanzo.ai" || str.calls[2].stdin != "s3cr3t-P@ssw0rd!" {
			t.Fatalf("unexpected stdin sequence: %q then %q", str.calls[0].stdin, str.calls[2].stdin)
		}

		// TOTP autofill: types 6 digits via stdin without exposing the seed or code.
		str.calls = nil
		totpOut, err := Computer(s, context.Background(), "acme", client.ComputerIn{
			ID:         m.ID,
			Action:     "autofill",
			Credential: "GitHub",
			Field:      "totp",
		})
		if err != nil || !totpOut.OK {
			t.Fatalf("TOTP autofill failed: out=%+v err=%v", totpOut, err)
		}
		if len(str.calls) != 1 || len(str.calls[0].stdin) != 6 {
			t.Fatalf("expected 6-digit TOTP on stdin, got %+v", str.calls)
		}
		if strings.Contains(totpOut.Text, str.calls[0].stdin) || strings.Contains(totpOut.Text, "JBSWY3DPEHPK3PXP") {
			t.Fatalf("TOTP autofill leaked code or seed: %q", totpOut.Text)
		}
	})
}

func TestTOTPNowMatchesRFC6238(t *testing.T) {
	// JBSWY3DPEHPK3PXP ("Hello!\xde\xad\xbe\xef") at Unix 0 => 282760.
	got, err := totpNow("JBSWY3DPEHPK3PXP", time.Unix(0, 0))
	if err != nil {
		t.Fatalf("totpNow: %v", err)
	}
	if got != "282760" {
		t.Fatalf("totpNow(0) = %q, want 282760", got)
	}
}

func TestSaveCredentialSealsFromRAMStageIntoKMSAndAutofillsPortHost(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		kms := &fakeKMS{secrets: map[string]string{}}
		str := &stdinSpy{
			stdout: map[string]string{
				sandboxCredentialStage: `{"name":"Sandbox Portal","domain":"127.0.0.1:8787","url":"http://127.0.0.1:8787/login","username":"vault-user@hanzo.ai","password":"HzDemo!VaultSecret99","totp":"JBSWY3DPEHPK3PXP"}`,
			},
		}
		s := service(t, str)
		s.KMS = kms
		ready(t, s)
		m := seedDesktop(t, s, "acme", "m-save")

		var vaultState []byte = []byte(`{"providers":{"1password":{"enabled":true}},"items":[]}`)
		var recordedKeys []string
		origRead, origWrite := vaultRead, vaultWrite
		t.Cleanup(func() {
			vaultRead = origRead
			vaultWrite = origWrite
		})
		vaultRead = func(_ context.Context, org string) ([]byte, error) {
			if org != "acme" {
				t.Fatalf("vaultRead org = %q, want acme", org)
			}
			return vaultState, nil
		}
		vaultWrite = func(_ context.Context, org string, cfg []byte, keys []string) error {
			if org != "acme" {
				t.Fatalf("vaultWrite org = %q, want acme", org)
			}
			vaultState = append([]byte(nil), cfg...)
			recordedKeys = append([]string(nil), keys...)
			return nil
		}

		saved, err := Computer(s, context.Background(), "acme", client.ComputerIn{
			ID:     m.ID,
			Action: "save_credential",
		})
		if err != nil {
			t.Fatalf("Computer save_credential: %v", err)
		}
		if !saved.OK {
			t.Fatalf("save_credential failed: %s", saved.Text)
		}
		if strings.Contains(saved.Text, "HzDemo!VaultSecret99") || strings.Contains(saved.Text, "JBSWY3DPEHPK3PXP") {
			t.Fatalf("save_credential leaked secret in response: %q", saved.Text)
		}
		if strings.Contains(string(vaultState), "HzDemo!VaultSecret99") || strings.Contains(string(vaultState), "JBSWY3DPEHPK3PXP") {
			t.Fatalf("vaultWrite stored plaintext secret in non-secret config: %s", string(vaultState))
		}
		if !strings.Contains(string(vaultState), `"1password"`) {
			t.Fatalf("vaultWrite dropped existing providers config: %s", string(vaultState))
		}
		if len(recordedKeys) != 2 || len(kms.secrets) != 2 {
			t.Fatalf("expected password + TOTP sealed in KMS (2 keys), got keys=%v kms=%d", recordedKeys, len(kms.secrets))
		}

		// Now autofill login_totp against http://127.0.0.1:8787/login (verifying host:port cleanHost matching + 2FA Tab sequence).
		str.calls = nil
		fillOut, err := Computer(s, context.Background(), "acme", client.ComputerIn{
			ID:         m.ID,
			Action:     "autofill",
			Credential: "http://127.0.0.1:8787/login",
			Field:      "login_totp",
			Submit:     true,
		})
		if err != nil || !fillOut.OK {
			t.Fatalf("Computer autofill login_totp failed: out=%+v err=%v", fillOut, err)
		}
		if strings.Contains(fillOut.Text, "HzDemo!VaultSecret99") {
			t.Fatalf("autofill leaked password: %q", fillOut.Text)
		}
		// Expect: username -> Tab -> password -> Tab -> 6-digit TOTP -> Return (6 calls).
		if len(str.calls) != 6 {
			t.Fatalf("login_totp calls = %d, want 6: %+v", len(str.calls), str.calls)
		}
		if str.calls[0].stdin != "vault-user@hanzo.ai" || str.calls[2].stdin != "HzDemo!VaultSecret99" || len(str.calls[4].stdin) != 6 {
			t.Fatalf("unexpected login_totp stdin sequence: %+v", str.calls)
		}
	})
}
