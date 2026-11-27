package provider

import (
	"bytes"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"strings"

	"github.com/hanzoai/cloud/sdk"
	"github.com/zap-proto/zip"
)

// choose.go is the chooser an APP provider's connect stops at when the person
// already controls installations no org holds (finishInstalled): which account to
// connect, or on to the install page for another account or other repositories.
//
// It used to bind every one of them at once. A person who disconnected one account
// to connect another came straight back to the first, and GitHub's own page, where
// they would have chosen, never showed.
//
// The page is answered by the callback itself, so it works wherever the connect
// was opened: Quick setup's popup, Sync or Integrations in the tab. It carries no
// session. Its authority is a token signed like the callback's state and carrying
// the accounts offered under the MAC (statePayload.Offered), with a nonce of its
// own: one answer, from one of those accounts, within the state's life.
//
// It also shows the accounts the person controls that another org holds, without
// naming that org and with nothing to connect them by: they are drawn, never
// offered, so the token does not carry them and no answer can bind one.

// offered is one account the chooser offers: what binds, and what the person reads.
type offered struct {
	ID    string `json:"id"`
	Login string `json:"login"`
	Kind  string `json:"kind,omitempty"`
}

// maxOffered bounds the accounts one page offers, so the signed token stays well
// inside maxStateLen. The install page reaches every other account.
const maxOffered = 20

// chooseLeaf is the chooser's answer, under the provider group.
const chooseLeaf = "/:provider/choose"

// choosePath is where the chooser's forms post, on the same host as the callback.
func choosePath(provider string) string { return "/v1/provider/" + provider + "/choose" }

// cancelled is the reason a page that asked is given when the person backs out.
const cancelled = "cancelled"

// offer answers the callback with the chooser: open is what the person may
// connect, elsewhere what they control that another org holds, back where the
// answer lands.
func offer(s *cloud.Service[state], c *zip.Ctx, p *Provider, payload statePayload, back string, open, elsewhere []offered) error {
	if len(open) > maxOffered {
		open = open[:maxOffered]
	}
	if len(elsewhere) > maxOffered {
		elsewhere = elsewhere[:maxOffered]
	}
	nonce, err := genToken()
	if err != nil {
		return failRedirect(s, c, back, p.ID, "state error", err)
	}
	if err := s.State.store.PutNonce(c.Context(), nonce, payload.Org, p.ID, back); err != nil {
		return failRedirect(s, c, back, p.ID, "state error", err)
	}
	token, err := signPayload(s, statePayload{Org: payload.Org, User: payload.User, Provider: p.ID, Nonce: nonce, Offered: open, Chooser: true})
	for err == nil && len(token) > maxStateLen && len(open) > 1 {
		open = open[:len(open)-1]
		token, err = signPayload(s, statePayload{Org: payload.Org, User: payload.User, Provider: p.ID, Nonce: nonce, Offered: open, Chooser: true})
	}
	if err != nil {
		return failRedirect(s, c, back, p.ID, "state error", err)
	}
	page, err := chooserPage(p, token, open, elsewhere, returnURL(s.State.consoleURL, back, "error", p.ID, "reason", cancelled))
	if err != nil {
		return failRedirect(s, c, back, p.ID, "state error", err)
	}
	return answerPage(c, page)
}

// choose takes the chooser's answer: one account it offered, or on to the install
// page. PUBLIC and raw like the callback, because it is a browser's form post that
// answers with a redirect; the token is all it trusts, and its nonce is spent
// before anything binds, so one page is one answer.
func choose(s *cloud.Service[state], c *zip.Ctx) error {
	p, ok := orgProvider(s, providerParam(c))
	if !ok || p.Installed == nil {
		return failRedirect(s, c, "", providerParam(c), "unknown provider", nil)
	}
	payload, err := verify(s, c.Fiber().FormValue("state"), p.ID)
	if err == nil && !payload.chooser() {
		err = errors.New("a callback state came to the chooser")
	}
	if err != nil {
		return failRedirect(s, c, "", p.ID, "invalid state", err)
	}
	back, consumed, err := s.State.store.ConsumeNonce(c.Context(), payload.Nonce, payload.Org, p.ID)
	if err != nil {
		return failRedirect(s, c, "", p.ID, "state error", err)
	}
	if !consumed {
		return failRedirect(s, c, "", p.ID, "state already used or expired",
			fmt.Errorf("org %s: chooser nonce not present (answered already, or GC'd after %s)", payload.Org, stateTTL))
	}
	if c.Fiber().FormValue("install") != "" {
		creds := credsFor(s, payload.Org, p)
		if creds.ClientID == "" && p.Creds != nil {
			creds = p.Creds()
		}
		return installNext(s, c, p, payload, back, creds)
	}
	id := strings.TrimSpace(c.Fiber().FormValue("account"))
	var pick *offered
	for i := range payload.Offered {
		if payload.Offered[i].ID == id {
			pick = &payload.Offered[i]
			break
		}
	}
	if pick == nil {
		return failRedirect(s, c, back, p.ID, "that account was not offered",
			fmt.Errorf("org %s: account %q is not among the %d offered", payload.Org, truncate(id, 32), len(payload.Offered)))
	}
	res := &ExchangeResult{ExternalID: pick.ID, AccountLabel: pick.Login, Kind: pick.Kind}
	conn := Connection{
		Org: payload.Org, Provider: p.ID, Label: connOwner(p, res),
		ExternalID: res.ExternalID, AccountLabel: res.AccountLabel, Installer: payload.User,
	}
	if err := bind(c.Context(), s, p, conn, nil); err != nil {
		if errors.Is(err, errBound) {
			return failRedirect(s, c, back, p.ID, heldElsewhere, fmt.Errorf("org %s: %w", payload.Org, err))
		}
		return failRedirect(s, c, back, p.ID, "persist failed", fmt.Errorf("org %s: %w", payload.Org, err))
	}
	s.Log.Info("integration connected", "provider", p.ID, "org", payload.Org, "account", res.AccountLabel, "externalId", res.ExternalID, "chosen", true)
	return successRedirect(s, c, back, p.ID, res.AccountLabel)
}

// answerPage answers the page itself, not to be cached, framed, or to load anything: the
// only places it reaches are its own form and links the person follows. There is
// no form-action: browsers hold a form's redirects to it too, and the answer
// redirects on to GitHub or to whichever Hanzo page asked.
func answerPage(c *zip.Ctx, page []byte) error {
	c.SetHeader("Cache-Control", "no-store")
	c.SetHeader("X-Frame-Options", "DENY")
	c.SetHeader("Referrer-Policy", "no-referrer")
	c.SetHeader("Content-Security-Policy",
		"default-src 'none'; style-src 'unsafe-inline'; frame-ancestors 'none'; base-uri 'none'")
	c.Fiber().Status(http.StatusOK).Type("html")
	return c.Fiber().Send(page)
}

// choiceRow is one account as the page draws it. A Held row is another org's:
// drawn with where to free it, and nothing to connect it by.
type choiceRow struct {
	ID, Login, Kind, Manage string
	Held                    bool
}

// chooserPage draws the chooser. Every value is escaped by html/template; the
// accounts' names are the provider's, so they are drawn as given.
func chooserPage(p *Provider, token string, open, elsewhere []offered, cancel string) ([]byte, error) {
	rows := make([]choiceRow, 0, len(open)+len(elsewhere))
	row := func(o offered, held bool) choiceRow {
		kind := "Organization"
		if strings.EqualFold(o.Kind, "User") {
			kind = "Personal account"
		}
		manage := ""
		if p.Manage != nil {
			manage = p.Manage(o)
		}
		return choiceRow{ID: o.ID, Login: o.Login, Kind: kind, Manage: manage, Held: held}
	}
	for _, o := range open {
		rows = append(rows, row(o, false))
	}
	for _, o := range elsewhere {
		rows = append(rows, row(o, true))
	}
	var b bytes.Buffer
	err := chooserTmpl.Execute(&b, map[string]any{
		"Name":   p.Name,
		"Action": choosePath(p.ID),
		"Token":  token,
		"Rows":   rows,
		"Open":   len(open) > 0,
		"Held":   len(elsewhere) > 0,
		"Cancel": cancel,
	})
	return b.Bytes(), err
}

var chooserTmpl = template.Must(template.New("chooser").Parse(`<!doctype html>
<html lang="en"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>Connect {{.Name}} to Hanzo</title>
<style>
:root{color-scheme:light dark;--ink:#111;--soft:#666;--edge:#e3e3e3;--panel:#fff;--bg:#f6f6f6;--accent:#111;--on:#fff}
@media (prefers-color-scheme:dark){:root{--ink:#eee;--soft:#999;--edge:#2c2c2c;--panel:#161616;--bg:#0b0b0b;--accent:#eee;--on:#111}}
*{box-sizing:border-box}body{margin:0;background:var(--bg);color:var(--ink);font:15px/1.45 system-ui,-apple-system,"Segoe UI",sans-serif}
main{max-width:30rem;margin:3rem auto;padding:0 1rem}h1{font-size:1.25rem;margin:0 0 .35rem}p{margin:0 0 1.25rem;color:var(--soft)}
ul{list-style:none;margin:0 0 1.25rem;padding:0;border:1px solid var(--edge);border-radius:12px;background:var(--panel)}
li{display:flex;align-items:center;gap:.75rem;padding:.8rem 1rem}li+li{border-top:1px solid var(--edge)}
.who{flex:1;min-width:0}.who b{display:block;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.who span,.who a{font-size:.85rem;color:var(--soft)}
form{margin:0}button{font:inherit;border-radius:8px;padding:.45rem .9rem;cursor:pointer;border:1px solid var(--accent);background:var(--accent);color:var(--on)}
button.quiet{background:transparent;color:var(--ink);border-color:var(--edge);width:100%}a{color:inherit}.foot{margin-top:1rem;text-align:center;font-size:.9rem}
.held b{color:var(--soft)}.note{font-size:.9rem}
</style>
<main>
<h1>Choose what to connect</h1>
{{if .Open}}<p>The Hanzo app is already on these {{.Name}} accounts. Connect one, or install the app on another.</p>
{{else}}<p>The Hanzo app is already on these {{.Name}} accounts, but each is connected to another Hanzo workspace. Install the app on another account, or free one of these first.</p>
{{end}}<ul>
{{range .Rows}}{{if .Held}}<li class="held"><div class="who"><b>{{.Login}}</b><span>{{.Kind}} · In another Hanzo workspace</span>{{if .Manage}} · <a href="{{.Manage}}" target="_blank" rel="noopener noreferrer">Manage on {{$.Name}}</a>{{end}}</div></li>
{{else}}<li><div class="who"><b>{{.Login}}</b><span>{{.Kind}}</span>{{if .Manage}} · <a href="{{.Manage}}" target="_blank" rel="noopener noreferrer">Change repositories on {{$.Name}}</a>{{end}}</div>
<form method="post" action="{{$.Action}}"><input type="hidden" name="state" value="{{$.Token}}"><input type="hidden" name="account" value="{{.ID}}"><button type="submit">Connect</button></form></li>
{{end}}{{end}}</ul>
{{if .Held}}<p class="note">An account connects to one Hanzo workspace at a time. To connect one that is in another workspace, disconnect it there first. Uninstalling the Hanzo app from it on {{.Name}} frees it too, but also disconnects it from the workspace that has it.</p>
{{end}}<form method="post" action="{{.Action}}"><input type="hidden" name="state" value="{{.Token}}"><input type="hidden" name="install" value="1"><button class="quiet" type="submit">Install on another account or organization</button></form>
<p class="foot"><a href="{{.Cancel}}">Cancel</a></p>
</main>
</html>
`))
