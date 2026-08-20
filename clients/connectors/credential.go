package connectors

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/smtp"
	"net/url"
	"strings"
)

// The connectors you cannot OAuth into.
//
// WhatsApp Cloud API hands you a permanent token and a phone-number id in a
// dashboard. A Twilio-style SMS account has an account SID and an auth token.
// SMTP is a host, a user and a password. None of them has a consent screen, so
// none of them can produce a code to come back with — and dressing them as OAuth
// would mean a connect that returns a URL we invented and a callback route
// nothing ever calls.
//
// They are KindCredential over the SAME plane: same catalog, same org gate, same
// KMS custody, same disconnect. Only the connect leg differs, and it differs
// honestly — the caller POSTs the fields and gets {connected:true}.
//
// EVERY ONE VERIFIES BEFORE IT STORES. That is the rule that makes a credential
// connector worth having: the connect either proves the credentials work or
// refuses. Storing an unverified token would move the failure to somebody's
// first real send, far from the paste that caused it.

func init() {
	register(whatsapp())
	register(sms())
	register(email())
}

// A credential connector is configured wherever it is USED, not on this
// deployment: the org pastes its own account. So `available` is unconditionally
// true — there is no deployment-level client id to be missing, and reporting
// false would hide a card that works.
func alwaysAvailable() bool { return true }

// ── WhatsApp ────────────────────────────────────────────────────────────────

func whatsapp() *Provider {
	return &Provider{
		ID:          "whatsapp",
		Name:        "WhatsApp",
		Description: "Send WhatsApp messages from your business number.",
		Category:    "Communication",
		Kind:        KindCredential,
		Configured:  alwaysAvailable,
		Creds:       func() OAuthConfig { return OAuthConfig{} },
		Fields: []Field{
			{Name: "phone_number_id", Label: "Phone number ID", Required: true},
			{Name: "access_token", Label: "Permanent access token", Secret: true, Required: true},
		},
		Secrets: []string{"access_token"},
		Verify:  verifyWhatsApp,
	}
}

// verifyWhatsApp reads the phone number the token is actually for. This both
// proves the token works and names the connection with the number rather than
// with whatever the pasting operator believed it was.
func verifyWhatsApp(ctx context.Context, v map[string]string) (*ExchangeResult, error) {
	id := v["phone_number_id"]
	if !isID(id) {
		return nil, errors.New("phone number id must be digits")
	}
	var out struct {
		ID           string `json:"id"`
		DisplayPhone string `json:"display_phone_number"`
		Verified     string `json:"verified_name"`
	}
	url := "https://graph.facebook.com/v21.0/" + id + "?fields=display_phone_number,verified_name"
	if err := getJSON(ctx, url, v["access_token"], &out); err != nil {
		return nil, err
	}
	label := firstNonEmpty(out.DisplayPhone, out.Verified, id)
	return &ExchangeResult{
		Tokens:       map[string]string{"access_token": v["access_token"]},
		ExternalID:   id,
		AccountLabel: label,
	}, nil
}

// ── SMS ─────────────────────────────────────────────────────────────────────

func sms() *Provider {
	return &Provider{
		ID:          "sms",
		Name:        "SMS",
		Description: "Send text messages from your own carrier account.",
		Category:    "Communication",
		Kind:        KindCredential,
		Configured:  alwaysAvailable,
		Creds:       func() OAuthConfig { return OAuthConfig{} },
		Fields: []Field{
			{Name: "account_sid", Label: "Account SID", Required: true},
			{Name: "auth_token", Label: "Auth token", Secret: true, Required: true},
			{Name: "from_number", Label: "From number", Required: true},
		},
		Secrets: []string{"auth_token"},
		Verify:  verifySMS,
	}
}

// verifySMS fetches the account itself with the pasted credentials. The carrier
// API is Basic-authed rather than Bearer, so this cannot use getJSON.
func verifySMS(ctx context.Context, v map[string]string) (*ExchangeResult, error) {
	sid := v["account_sid"]
	if !isToken(sid) {
		return nil, errors.New("account sid is malformed")
	}
	if !isPhone(v["from_number"]) {
		return nil, errors.New("from number must be E.164, e.g. +15551234567")
	}
	endpoint := "https://api.twilio.com/2010-04-01/Accounts/" + url.PathEscape(sid) + ".json"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth(sid, v["auth_token"])
	req.Header.Set("Accept", "application/json")
	resp, err := oauthHTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("carrier said %d", resp.StatusCode)
	}
	return &ExchangeResult{
		Tokens:       map[string]string{"auth_token": v["auth_token"]},
		ExternalID:   sid,
		AccountLabel: v["from_number"],
	}, nil
}

// ── Email ───────────────────────────────────────────────────────────────────

func email() *Provider {
	return &Provider{
		ID:          "email",
		Name:        "Email",
		Description: "Send mail through your own SMTP server.",
		Category:    "Communication",
		Kind:        KindCredential,
		Configured:  alwaysAvailable,
		Creds:       func() OAuthConfig { return OAuthConfig{} },
		Fields: []Field{
			{Name: "host", Label: "SMTP host", Required: true},
			{Name: "port", Label: "Port", Required: true},
			{Name: "username", Label: "Username", Required: true},
			{Name: "password", Label: "Password", Secret: true, Required: true},
			{Name: "from_address", Label: "From address", Required: true},
		},
		Secrets: []string{"password"},
		Verify:  verifyEmail,
	}
}

// verifyEmail opens a real SMTP session and authenticates. It sends no mail —
// AUTH succeeding is the proof, and delivering a probe message would put a
// stranger's inbox in the middle of a settings screen.
//
// STARTTLS IS MANDATORY. `smtp.PlainAuth` refuses to hand a password to an
// unencrypted connection anyway, and that refusal is the right one to keep: the
// password is the org's real mail credential, and a server that cannot do TLS
// in 2026 is a server that should not be given it.
func verifyEmail(ctx context.Context, v map[string]string) (*ExchangeResult, error) {
	host, port := v["host"], v["port"]
	if !isHost(host) {
		return nil, errors.New("host is malformed")
	}
	if !isPort(port) {
		return nil, errors.New("port must be a number")
	}
	if !strings.Contains(v["from_address"], "@") {
		return nil, errors.New("from address must be an email address")
	}

	addr := net.JoinHostPort(host, port)
	dialer := &net.Dialer{Timeout: oauthHTTP.Timeout}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	client, err := smtp.NewClient(conn, host)
	if err != nil {
		return nil, err
	}
	defer client.Close()

	if ok, _ := client.Extension("STARTTLS"); !ok {
		return nil, errors.New("server does not offer STARTTLS")
	}
	if err := client.StartTLS(nil); err != nil {
		return nil, err
	}
	if err := client.Auth(smtp.PlainAuth("", v["username"], v["password"], host)); err != nil {
		return nil, err
	}
	return &ExchangeResult{
		Tokens:       map[string]string{"password": v["password"]},
		ExternalID:   v["from_address"],
		AccountLabel: v["from_address"],
	}, nil
}

// ── Field shapes ────────────────────────────────────────────────────────────
//
// Checked here, before a value reaches a URL or a dial. These are not
// cosmetic: `phone_number_id` is concatenated into a graph URL, and `host` and
// `port` are dialed. A caller-supplied value that reaches either without a
// shape check is how a settings form becomes a request forgery.

func isID(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func isToken(s string) bool {
	if s == "" || len(s) > 128 {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

func isPhone(s string) bool {
	if len(s) < 8 || len(s) > 20 || s[0] != '+' {
		return false
	}
	return isID(s[1:])
}

func isPort(s string) bool {
	if !isID(s) || len(s) > 5 {
		return false
	}
	return s != "0"
}

// isHost accepts a DNS name and nothing else — no scheme, no port, no path, and
// no IP literal. A hostname is what SMTP and TLS both want, and refusing the
// other shapes here means the dial cannot be pointed somewhere structural.
func isHost(s string) bool {
	if s == "" || len(s) > 253 || strings.ContainsAny(s, "/:@ \t\r\n") {
		return false
	}
	for _, label := range strings.Split(s, ".") {
		if label == "" || len(label) > 63 {
			return false
		}
		for i, r := range label {
			alnum := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9'
			if !alnum && !(r == '-' && i > 0 && i < len(label)-1) {
				return false
			}
		}
	}
	return strings.Contains(s, ".")
}
