package sandbox

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base32"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/hanzoai/cloud/client"
	settingspeer "github.com/hanzoai/cloud/client/settings"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/secret"
)

const (
	vaultProduct           = "vault"
	sandboxCredentialStage = "/dev/shm/.hanzo-credential.json"
)

var vaultKeyRE = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,62}$`)

// vaultDoc mirrors the non-secret JSON configuration stored under
// PUT /v1/settings/vault and shared across platform.hanzo.ai, hanzo.app
// Settings, and hanzo.bot. Secret values live exclusively in KMS at
// orgs/{org}/settings/vault/{key}.
type vaultDoc struct {
	Items []vaultItem `json:"items"`
}

type vaultItem struct {
	ID            string `json:"id"`
	Kind          string `json:"kind"` // "login" | "api_key" | "secret"
	Name          string `json:"name"`
	EnvVar        string `json:"envVar,omitempty"`
	Domain        string `json:"domain,omitempty"`
	URL           string `json:"url,omitempty"`
	Username      string `json:"username,omitempty"`
	SecretKey     string `json:"secretKey"`
	TOTPSecretKey string `json:"totpSecretKey,omitempty"`
	Source        string `json:"source,omitempty"` // "hanzo" | "1password" | "lastpass"
	VaultRef      string `json:"vaultRef,omitempty"`
	UpdatedAt     string `json:"updatedAt,omitempty"`
}

// stagedCredential is the ephemeral RAM-only JSON envelope written to
// /dev/shm/.hanzo-credential.json by a sandbox desktop app or signup script so
// save_credential can seal secret values directly into KMS without passing
// passwords or TOTP seeds through model context, chat logs, or /workspace disk.
type stagedCredential struct {
	Kind     string `json:"kind"`
	Name     string `json:"name"`
	EnvVar   string `json:"envVar"`
	Domain   string `json:"domain"`
	URL      string `json:"url"`
	Username string `json:"username"`
	Password string `json:"password"`
	Secret   string `json:"secret"`
	TOTP     string `json:"totp"`
}

// autofillFn executes a zero-knowledge credential autofill inside a screen
// sandbox without ever exposing secret values to the caller or model transcript.
type autofillFn func(ctx context.Context, credential, field string, submit bool) (string, bool)

// saveCredentialFn seals a staged credential from /dev/shm/.hanzo-credential.json
// inside the sandbox directly into Hanzo KMS and updates the org's Unified Vault
// metadata without exposing secret values to the model or persistent disk.
type saveCredentialFn func(ctx context.Context, nameOverride, urlOverride string) (string, bool)

// vaultRead loads the caller org's non-secret vault manifest. Replaced in unit
// tests where the settings peer is not mounted.
var vaultRead = func(ctx context.Context, org string) ([]byte, error) {
	got, err := settingspeer.SettingsOrg(cloud.For(ctx, org), &client.Product{Product: vaultProduct})
	if err != nil {
		return nil, err
	}
	if got == nil || strings.TrimSpace(got.Config) == "" {
		return nil, nil
	}
	return []byte(got.Config), nil
}

// vaultWrite updates the caller org's non-secret vault manifest and KMS
// secret-key index. Replaced in unit tests where the settings peer is not mounted.
var vaultWrite = func(ctx context.Context, org string, config []byte, secretKeys []string) error {
	_, err := settingspeer.SettingsOrgSet(cloud.For(ctx, org), &client.OrgSettingsSet{
		Product:    vaultProduct,
		Config:     string(config),
		SecretKeys: secretKeys,
	})
	return err
}

func cleanHost(raw string) string {
	s := strings.ToLower(strings.TrimSpace(raw))
	if s == "" {
		return ""
	}
	if strings.Contains(s, "://") {
		if u, err := url.Parse(s); err == nil && u.Hostname() != "" {
			s = strings.ToLower(u.Hostname())
		}
	} else {
		if idx := strings.IndexByte(s, '/'); idx > 0 {
			s = s[:idx]
		}
		if h, _, err := net.SplitHostPort(s); err == nil && h != "" {
			s = h
		}
	}
	return strings.TrimPrefix(s, "www.")
}

func matchVaultItem(items []vaultItem, query string) (vaultItem, []string, bool) {
	q := strings.TrimSpace(query)
	qLower := strings.ToLower(q)
	qHost := cleanHost(q)

	var names []string
	var logins []vaultItem
	for _, it := range items {
		label := strings.TrimSpace(it.Name)
		if host := cleanHost(it.Domain); host != "" && !strings.EqualFold(label, host) {
			if label != "" {
				label = label + " (" + host + ")"
			} else {
				label = host
			}
		} else if host := cleanHost(it.URL); host != "" && !strings.EqualFold(label, host) {
			if label != "" {
				label = label + " (" + host + ")"
			} else {
				label = host
			}
		}
		if label != "" {
			names = append(names, label)
		}
		if it.Kind == "" || it.Kind == "login" {
			logins = append(logins, it)
		}
	}

	if q == "" {
		if len(logins) == 1 {
			return logins[0], names, true
		}
		if len(items) == 1 {
			return items[0], names, true
		}
		return vaultItem{}, names, false
	}

	// 1. Exact ID, Name, or EnvVar match.
	for _, it := range items {
		if it.ID == q || strings.EqualFold(strings.TrimSpace(it.Name), q) ||
			(it.EnvVar != "" && strings.EqualFold(strings.TrimSpace(it.EnvVar), q)) {
			return it, names, true
		}
	}
	// 2. Exact or subdomain host match.
	if qHost != "" {
		for _, it := range items {
			for _, candidate := range []string{cleanHost(it.Domain), cleanHost(it.URL)} {
				if candidate == "" {
					continue
				}
				if candidate == qHost || strings.HasSuffix(qHost, "."+candidate) || strings.HasSuffix(candidate, "."+qHost) {
					return it, names, true
				}
			}
		}
	}
	// 3. Substring match on name or domain.
	for _, it := range items {
		if strings.Contains(strings.ToLower(it.Name), qLower) ||
			strings.Contains(strings.ToLower(it.Domain), qLower) ||
			strings.Contains(strings.ToLower(it.URL), qLower) {
			return it, names, true
		}
	}
	return vaultItem{}, names, false
}

// totpNow computes the RFC 6238 6-digit TOTP code for a Base32 seed or an
// otpauth:// URI at t.
func totpNow(rawSecret string, t time.Time) (string, error) {
	seed := strings.TrimSpace(rawSecret)
	if strings.HasPrefix(strings.ToLower(seed), "otpauth://") {
		u, err := url.Parse(seed)
		if err != nil {
			return "", errors.New("invalid otpauth URI")
		}
		seed = u.Query().Get("secret")
	}
	cleaned := strings.ToUpper(strings.NewReplacer(" ", "", "-", "").Replace(seed))
	if cleaned == "" {
		return "", errors.New("empty TOTP secret")
	}
	enc := base32.StdEncoding.WithPadding(base32.NoPadding)
	key, err := enc.DecodeString(strings.TrimRight(cleaned, "="))
	if err != nil || len(key) == 0 {
		return "", errors.New("TOTP secret must be Base32")
	}
	counter := uint64(t.Unix() / 30)
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], counter)
	mac := hmac.New(sha1.New, key)
	_, _ = mac.Write(buf[:])
	sum := mac.Sum(nil)
	offset := sum[len(sum)-1] & 0x0f
	binCode := (uint32(sum[offset])&0x7f)<<24 |
		(uint32(sum[offset+1])&0xff)<<16 |
		(uint32(sum[offset+2])&0xff)<<8 |
		(uint32(sum[offset+3]) & 0xff)
	return fmt.Sprintf("%06d", binCode%1_000_000), nil
}

func vaultSecretRef(org, key string) (string, error) {
	k := strings.TrimSpace(key)
	if !vaultKeyRE.MatchString(k) {
		return "", errors.New("vault secret key is invalid")
	}
	return "orgs/" + org + "/settings/" + vaultProduct + "/" + k, nil
}

// makeAutofill binds a sandbox's zero-knowledge credential typer for org and id.
func makeAutofill(s *Service, org, id string, screen bool) autofillFn {
	return func(ctx context.Context, credential, field string, submit bool) (string, bool) {
		if !screen {
			return "This computer has no screen, so autofill is not available.", false
		}
		raw, err := vaultRead(ctx, org)
		if err != nil {
			return "Could not read the organization's Unified Vault settings.", false
		}
		var doc vaultDoc
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &doc); err != nil {
				return "The organization's Unified Vault manifest could not be parsed.", false
			}
		}
		if len(doc.Items) == 0 {
			return "No credentials are saved in the Unified Vault yet. Add a login or connect 1Password / LastPass in Settings → Secrets & Autofill.", false
		}
		item, available, found := matchVaultItem(doc.Items, credential)
		if !found {
			return fmt.Sprintf("No vault credential matched %q. Available credentials: %s.", credential, strings.Join(available, ", ")), false
		}

		mode := strings.ToLower(strings.TrimSpace(field))
		if mode == "" {
			if item.Kind == "api_key" || item.Kind == "secret" || strings.TrimSpace(item.Username) == "" {
				mode = "password"
			} else {
				mode = "login"
			}
		}

		typeBlind := func(val string) error {
			if val == "" {
				return nil
			}
			r, rErr := Run(s, ctx, org, id, Cmd{
				Argv:       []string{"xdotool", "type", "--delay", "10", "--clearmodifiers", "--file", "-"},
				Stdin:      val,
				Blind:      []string{val},
				TimeoutSec: 30,
			})
			if rErr != nil {
				return rErr
			}
			if r.ExitCode != 0 {
				return fmt.Errorf("xdotool exited %d", r.ExitCode)
			}
			return nil
		}
		pressKey := func(k string) error {
			r, rErr := Run(s, ctx, org, id, Cmd{
				Argv:       []string{"xdotool", "key", "--clearmodifiers", k},
				TimeoutSec: 15,
			})
			if rErr != nil {
				return rErr
			}
			if r.ExitCode != 0 {
				return fmt.Errorf("xdotool key exited %d", r.ExitCode)
			}
			return nil
		}

		readKMS := func(key string) (string, error) {
			if s.KMS == nil {
				return "", errors.New("KMS is not configured on this deployment")
			}
			ref, refErr := vaultSecretRef(org, key)
			if refErr != nil {
				return "", refErr
			}
			b, gErr := s.KMS.GetSecret(ctx, ref)
			if errors.Is(gErr, secret.ErrNone) {
				return "", errors.New("the secret value is not sealed in KMS")
			}
			if gErr != nil {
				return "", errors.New("KMS could not unseal the credential")
			}
			return string(b), nil
		}

		label := strings.TrimSpace(item.Name)
		if label == "" {
			label = cleanHost(item.Domain)
		}
		if label == "" {
			label = item.ID
		}
		prov := strings.TrimSpace(item.Source)
		if prov == "" {
			prov = "hanzo"
		}

		switch mode {
		case "username":
			user := strings.TrimSpace(item.Username)
			if user == "" {
				return fmt.Sprintf("Credential %q has no username saved.", label), false
			}
			if err := typeBlind(user); err != nil {
				return "Could not type username into the desktop: " + err.Error(), false
			}
			if submit {
				_ = pressKey("Return")
			}
			return fmt.Sprintf("Autofilled username for %q (%s).", label, prov), true

		case "totp":
			if strings.TrimSpace(item.TOTPSecretKey) == "" {
				return fmt.Sprintf("Credential %q has no TOTP authenticator secret configured.", label), false
			}
			seed, err := readKMS(item.TOTPSecretKey)
			if err != nil {
				return fmt.Sprintf("Could not unseal TOTP secret for %q: %s.", label, err.Error()), false
			}
			code, err := totpNow(seed, time.Now())
			if err != nil {
				return fmt.Sprintf("Could not generate TOTP code for %q: %s.", label, err.Error()), false
			}
			if err := typeBlind(code); err != nil {
				return "Could not type TOTP code into the desktop: " + err.Error(), false
			}
			if submit {
				_ = pressKey("Return")
			}
			return fmt.Sprintf("Autofilled 6-digit TOTP code for %q.", label), true

		case "password", "secret":
			secret, err := readKMS(item.SecretKey)
			if err != nil {
				return fmt.Sprintf("Could not unseal secret for %q: %s.", label, err.Error()), false
			}
			if err := typeBlind(secret); err != nil {
				return "Could not type secret into the desktop: " + err.Error(), false
			}
			if submit {
				_ = pressKey("Return")
			}
			return fmt.Sprintf("Autofilled secret for %q (%s).", label, prov), true

		case "login", "login_totp":
			secret, err := readKMS(item.SecretKey)
			if err != nil {
				return fmt.Sprintf("Could not unseal password for %q: %s.", label, err.Error()), false
			}
			user := strings.TrimSpace(item.Username)
			if user != "" {
				if err := typeBlind(user); err != nil {
					return "Could not type username into the desktop: " + err.Error(), false
				}
				if err := pressKey("Tab"); err != nil {
					return "Could not advance to password field: " + err.Error(), false
				}
				time.Sleep(80 * time.Millisecond)
			}
			if err := typeBlind(secret); err != nil {
				return "Could not type password into the desktop: " + err.Error(), false
			}
			if mode == "login_totp" && strings.TrimSpace(item.TOTPSecretKey) != "" {
				seed, tErr := readKMS(item.TOTPSecretKey)
				if tErr != nil {
					return fmt.Sprintf("Could not unseal TOTP secret for %q: %s.", label, tErr.Error()), false
				}
				code, cErr := totpNow(seed, time.Now())
				if cErr != nil {
					return fmt.Sprintf("Could not generate TOTP code for %q: %s.", label, cErr.Error()), false
				}
				if err := pressKey("Tab"); err != nil {
					return "Could not advance to 2FA code field: " + err.Error(), false
				}
				time.Sleep(80 * time.Millisecond)
				if err := typeBlind(code); err != nil {
					return "Could not type 2FA code into the desktop: " + err.Error(), false
				}
			}
			if submit {
				time.Sleep(60 * time.Millisecond)
				_ = pressKey("Return")
			}
			if user != "" {
				return fmt.Sprintf("Autofilled login %q (%s, user %s)%s.", label, prov, user, map[bool]string{true: " and submitted"}[submit]), true
			}
			return fmt.Sprintf("Autofilled credential %q (%s)%s.", label, prov, map[bool]string{true: " and submitted"}[submit]), true

		default:
			return "field must be login, login_totp, username, password, totp, or secret", false
		}
	}
}

func mintVaultID() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return fmt.Sprintf("v_%x%s", time.Now().Unix(), hex.EncodeToString(b[:]))
}

func vaultSlugKey(id, suffix string) string {
	var sb strings.Builder
	sb.WriteString("item_")
	for _, r := range strings.ToLower(id) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '.' || r == '_' || r == '-' {
			sb.WriteRune(r)
		} else {
			sb.WriteByte('-')
		}
	}
	sb.WriteByte('_')
	sb.WriteString(suffix)
	out := sb.String()
	if len(out) > 62 {
		out = out[:62]
	}
	return out
}

// makeSaveCredential reads and immediately unlinks /dev/shm/.hanzo-credential.json
// inside the sandbox, seals the secret value (and optional TOTP seed) directly into
// Hanzo KMS under orgs/{org}/settings/vault/{key}, and indexes the non-secret item
// metadata in the org's Unified Vault settings.
func makeSaveCredential(s *Service, org, id string) saveCredentialFn {
	return func(ctx context.Context, nameOverride, urlOverride string) (string, bool) {
		if s.KMS == nil {
			return "KMS is not configured on this deployment; refusing to save credentials outside KMS.", false
		}
		// Atomically read and unlink the RAM-only staging file; Session is empty so
		// newTell is nil and stdout is never narrated anywhere.
		script := `f="$1"; [ -f "$f" ] && [ ! -L "$f" ] || exit 44; cat "$f"; rm -f "$f"`
		r, err := Run(s, ctx, org, id, Cmd{
			Argv:       []string{"sh", "-c", script, "sh", sandboxCredentialStage},
			TimeoutSec: 15,
		})
		if err != nil {
			return "Could not read staged credential from sandbox: " + err.Error(), false
		}
		if r.ExitCode == 44 || strings.TrimSpace(r.Stdout) == "" {
			return "No staged credential found at " + sandboxCredentialStage + ". Have the sandbox desktop app write {name, domain, url, username, password, totp?} with mode 0600 to " + sandboxCredentialStage + " (RAM tmpfs, never /workspace) and then call save_credential.", false
		}
		if r.ExitCode != 0 {
			return fmt.Sprintf("Reading staged credential exited %d.", r.ExitCode), false
		}

		var staged stagedCredential
		if err := json.Unmarshal([]byte(r.Stdout), &staged); err != nil {
			return "Staged credential at " + sandboxCredentialStage + " was not valid JSON (file has been shredded).", false
		}
		secretVal := strings.TrimSpace(staged.Password)
		if secretVal == "" {
			secretVal = strings.TrimSpace(staged.Secret)
		}
		if secretVal == "" {
			return "Staged credential had no password or secret value (file has been shredded).", false
		}
		if len(secretVal) > 8*1024 {
			return "Secret value exceeds 8 KiB KMS limit.", false
		}

		rawURL := strings.TrimSpace(urlOverride)
		if rawURL == "" {
			rawURL = strings.TrimSpace(staged.URL)
		}
		domain := strings.TrimSpace(staged.Domain)
		if domain == "" && rawURL != "" {
			if u, uErr := url.Parse(rawURL); uErr == nil && u.Host != "" {
				domain = strings.TrimPrefix(strings.ToLower(u.Host), "www.")
			} else {
				domain = cleanHost(rawURL)
			}
		}
		name := strings.TrimSpace(nameOverride)
		if name == "" {
			name = strings.TrimSpace(staged.Name)
		}
		if name == "" {
			name = domain
		}
		if name == "" {
			name = "Sandbox App Login"
		}
		kind := strings.ToLower(strings.TrimSpace(staged.Kind))
		switch kind {
		case "login", "api_key", "secret":
		default:
			kind = "login"
		}

		// Load existing non-secret vault config so we preserve providers & other items.
		existingRaw, err := vaultRead(ctx, org)
		if err != nil {
			return "Could not read Unified Vault settings before saving.", false
		}
		topObj := map[string]any{}
		var doc vaultDoc
		if len(existingRaw) > 0 {
			_ = json.Unmarshal(existingRaw, &topObj)
			_ = json.Unmarshal(existingRaw, &doc)
		}

		username := strings.TrimSpace(staged.Username)
		itemID := mintVaultID()
		secretKey := vaultSlugKey(itemID, "s")
		var totpKey string
		updateIdx := -1
		for i, it := range doc.Items {
			if strings.EqualFold(strings.TrimSpace(it.Name), name) &&
				strings.EqualFold(strings.TrimSpace(it.Username), username) &&
				cleanHost(it.Domain) == cleanHost(domain) {
				updateIdx = i
				itemID = it.ID
				if vaultKeyRE.MatchString(it.SecretKey) {
					secretKey = it.SecretKey
				}
				if vaultKeyRE.MatchString(it.TOTPSecretKey) {
					totpKey = it.TOTPSecretKey
				}
				break
			}
		}

		ref, err := vaultSecretRef(org, secretKey)
		if err != nil {
			return "Could not derive KMS reference: " + err.Error(), false
		}
		if err := s.KMS.PutSecret(ctx, ref, []byte(secretVal)); err != nil {
			return "KMS refused to seal the credential.", false
		}
		sealedKeys := []string{secretKey}

		totpVal := strings.TrimSpace(staged.TOTP)
		if totpVal != "" {
			if _, tErr := totpNow(totpVal, time.Now()); tErr != nil {
				return "Credential TOTP seed is invalid (" + tErr.Error() + ").", false
			}
			if totpKey == "" {
				totpKey = vaultSlugKey(itemID, "t")
			}
			tRef, rErr := vaultSecretRef(org, totpKey)
			if rErr != nil {
				return "Could not derive TOTP KMS reference: " + rErr.Error(), false
			}
			if err := s.KMS.PutSecret(ctx, tRef, []byte(totpVal)); err != nil {
				return "KMS refused to seal the TOTP seed.", false
			}
			sealedKeys = append(sealedKeys, totpKey)
		}

		newItem := vaultItem{
			ID:            itemID,
			Kind:          kind,
			Name:          name,
			EnvVar:        strings.TrimSpace(staged.EnvVar),
			Domain:        domain,
			URL:           rawURL,
			Username:      username,
			SecretKey:     secretKey,
			TOTPSecretKey: totpKey,
			Source:        "hanzo",
			UpdatedAt:     time.Now().UTC().Format(time.RFC3339),
		}
		if updateIdx >= 0 {
			doc.Items[updateIdx] = newItem
		} else {
			doc.Items = append([]vaultItem{newItem}, doc.Items...)
		}
		topObj["items"] = doc.Items
		if _, ok := topObj["providers"]; !ok {
			topObj["providers"] = map[string]any{}
		}
		cfgBytes, err := json.Marshal(topObj)
		if err != nil {
			return "Could not encode Unified Vault configuration.", false
		}
		if err := vaultWrite(ctx, org, cfgBytes, sealedKeys); err != nil {
			return "Could not save Unified Vault item metadata: " + err.Error(), false
		}
		if username != "" {
			return fmt.Sprintf("Sealed credential %q (%s, user %s) into Hanzo KMS and shredded %s.", name, domain, username, sandboxCredentialStage), true
		}
		return fmt.Sprintf("Sealed credential %q into Hanzo KMS and shredded %s.", name, sandboxCredentialStage), true
	}
}
