package provider

import (
	"encoding/base64"
	"encoding/json"
	"html"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/zap-proto/zip"
)

// github_adopt_test.go is connecting an account that ALREADY has the App.
//
// GitHub's install page cannot do it: for an account the App is on, it offers
// Configure, and saving there returns nothing the callback can bind, so the
// person was left on GitHub with the popup open. The org connect therefore
// starts at GitHub's authorization page, which always returns, and the callback
// OFFERS the installations the person controls that no organization holds, on the
// chooser page (choose.go): the person connects the one they mean, or goes on to
// the install page for another. The ones another organization holds are shown
// there too, unnamed and with nothing to connect them by, since the install page
// would strand the person on Configure. With none of either, it sends the same
// browser on to the install page.

// adoptHub is GitHub with the App on acme-gh, an organization whose two
// repositories octo administers.
func adoptHub(t *testing.T) (*fakeHub, *zip.App) {
	t.Helper()
	h, app := withHub(t)
	h.accounts[77] = "acme-gh"
	h.reach["octo"] = []int64{77}
	h.repos = append(h.repos,
		&hubRepo{full: "acme-gh/web", install: 77, branch: "main", admin: map[string]bool{"octo": true}},
		&hubRepo{full: "acme-gh/api", install: 77, branch: "main", admin: map[string]bool{"octo": true}},
	)
	return h, app
}

// startOrgConnect starts the org's GitHub connect from Integrations and answers the
// state GitHub's authorization page is handed.
func startOrgConnect(t *testing.T, app *zip.App, org string) string {
	t.Helper()
	r := asPerson(t, app, http.MethodPost, "/v1/provider/github/connect", org, "alice", true,
		map[string]string{"return": devIntegrations})
	if r.Code != http.StatusOK {
		t.Fatalf("connect want 200, got %d (%s)", r.Code, r.Body)
	}
	var out struct {
		AuthorizeURL string `json:"authorizeUrl"`
	}
	if err := json.Unmarshal(r.Body, &out); err != nil {
		t.Fatalf("connect body: %v (%s)", err, r.Body)
	}
	u, err := url.Parse(out.AuthorizeURL)
	if err != nil {
		t.Fatalf("authorizeUrl: %v", err)
	}
	q := u.Query()
	if u.Path != "/login/oauth/authorize" || q.Get("client_id") != "Iv23test" ||
		q.Get("redirect_uri") != "https://api.hanzo.ai/v1/provider/github/callback" || q.Get("state") == "" ||
		q.Get("prompt") != "select_account" {
		t.Fatalf("the connect must start at GitHub's authorization page, with its account picker, for the org callback, got %s", out.AuthorizeURL)
	}
	return q.Get("state")
}

// returnFrom is GitHub's return from its authorization page: a code and the
// state, and no installation, because none was made.
func returnFrom(t *testing.T, app *zip.App, state, login string) httpResult {
	t.Helper()
	return asPerson(t, app, http.MethodGet,
		"/v1/provider/github/callback?state="+url.QueryEscape(state)+"&code=code-"+login, "", "", false, nil)
}

// backOnThePage asserts the return landed on Integrations, connected as account.
func backOnThePage(t *testing.T, r httpResult, account string) {
	t.Helper()
	u, err := url.Parse(r.Location)
	if err != nil || r.Code != http.StatusFound || u.Host != "hanzo.ai" || u.Query().Get("at") != "-/settings/integrations" {
		t.Fatalf("the return must land on the page that asked, got %d %q", r.Code, r.Location)
	}
	if u.Query().Get("connected") != "github" || u.Query().Get("account") != account {
		t.Fatalf("want connected=github&account=%s, got %q", account, r.Location)
	}
}

// refusedOnThePage asserts the answer landed on the page that asked, refused for reason.
func refusedOnThePage(t *testing.T, r httpResult, reason string) {
	t.Helper()
	u, err := url.Parse(r.Location)
	if err != nil || r.Code != http.StatusFound || u.Query().Get("error") != "github" || u.Query().Get("reason") != reason {
		t.Fatalf("want error=github&reason=%q, got %d %q", reason, r.Code, r.Location)
	}
}

// onToInstall asserts the return went on to GitHub's install page, with a
// state of its own, and answers that state.
func onToInstall(t *testing.T, r httpResult) string {
	t.Helper()
	const page = "https://github.com/apps/hanzo/installations/new?state="
	if r.Code != http.StatusFound || !strings.HasPrefix(r.Location, page) {
		t.Fatalf("with nothing to bind the browser must go on to the install page, got %d %q", r.Code, r.Location)
	}
	return strings.TrimPrefix(r.Location, page)
}

var (
	chooserToken   = regexp.MustCompile(`name="state" value="([^"]+)"`)
	chooserLogin   = regexp.MustCompile(`<b>([^<]+)</b>`)
	chooserAccount = regexp.MustCompile(`name="account" value="([^"]+)"`)
)

// chooserOn asserts the return answered the chooser, and reads it as the browser
// would: the token its forms carry, and the accounts it offers, login to id.
func chooserOn(t *testing.T, r httpResult) (string, map[string]string) {
	t.Helper()
	token, offered, _ := chooserRead(t, r)
	return token, offered
}

// chooserRead is chooserOn with the accounts it shows held by another workspace:
// drawn, and given nothing to connect them with.
func chooserRead(t *testing.T, r httpResult) (string, map[string]string, []string) {
	t.Helper()
	if r.Code != http.StatusOK || !strings.Contains(string(r.Body), "Choose what to connect") {
		t.Fatalf("the return must answer the chooser, got %d %q %.200s", r.Code, r.Location, r.Body)
	}
	m := chooserToken.FindSubmatch(r.Body)
	if m == nil {
		t.Fatalf("the chooser carries no token: %s", r.Body)
	}
	offered := map[string]string{}
	var held []string
	for _, row := range strings.Split(string(r.Body), "<li")[1:] {
		login := chooserLogin.FindStringSubmatch(row)
		if login == nil {
			continue
		}
		name := html.UnescapeString(login[1])
		if id := chooserAccount.FindStringSubmatch(row); id != nil {
			offered[name] = html.UnescapeString(id[1])
		} else {
			held = append(held, name)
		}
	}
	return html.UnescapeString(string(m[1])), offered, held
}

// answer is the browser posting the chooser's form, with no session, as it does.
func answer(t *testing.T, app *zip.App, form url.Values) httpResult {
	t.Helper()
	rq := httptest.NewRequest(http.MethodPost, "/v1/provider/github/choose", strings.NewReader(form.Encode()))
	rq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := app.Test(rq, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("Test POST choose: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return httpResult{Code: resp.StatusCode, Location: resp.Header.Get("Location"), Body: b}
}

// connectChoosing walks the connect through the chooser and connects login.
func connectChoosing(t *testing.T, app *zip.App, org, account string) httpResult {
	t.Helper()
	token, offered := chooserOn(t, returnFrom(t, app, startOrgConnect(t, app, org), "octo"))
	id, ok := offered[account]
	if !ok {
		t.Fatalf("the chooser must offer %s, offered %v", account, offered)
	}
	return answer(t, app, url.Values{"state": {token}, "account": {id}})
}

func bound(t *testing.T, org string) []string {
	t.Helper()
	var ids []string
	for _, c := range Connections(org, "github") {
		ids = append(ids, c.AccountLabel+"="+c.ExternalID)
	}
	return ids
}

// TestAnInstalledOrgIsOfferedNotBound is the bug: the App is on acme-gh and nobody
// holds it. The return offers it, and binds nothing until the person chooses, so
// a person who disconnected acme-gh to connect another is not sent straight back.
func TestAnInstalledOrgIsOfferedNotBound(t *testing.T) {
	_, app := adoptHub(t)
	r := returnFrom(t, app, startOrgConnect(t, app, "acme"), "octo")
	_, offered := chooserOn(t, r)
	if len(offered) != 1 || offered["acme-gh"] != "77" {
		t.Fatalf("the chooser must offer acme-gh alone, got %v", offered)
	}
	if got := bound(t, "acme"); len(got) != 0 {
		t.Fatalf("nothing binds before the person chooses, got %v", got)
	}
	// Beside it, the installation's own page on GitHub, where its repositories change.
	if !strings.Contains(string(r.Body), `href="https://github.com/organizations/acme-gh/settings/installations/77"`) {
		t.Fatalf("the chooser must link acme-gh's installation settings: %s", r.Body)
	}
}

// TestAnInstalledOrgItsAdminChoosesIsBound: choosing it binds it, without GitHub's
// Configure page, and lands on the page that asked.
func TestAnInstalledOrgItsAdminChoosesIsBound(t *testing.T) {
	_, app := adoptHub(t)
	backOnThePage(t, connectChoosing(t, app, "acme", "acme-gh"), "acme-gh")
	if got := bound(t, "acme"); len(got) != 1 || got[0] != "acme-gh=77" {
		t.Fatalf("acme must hold acme-gh's installation, got %v", got)
	}
}

// TestTheirOwnAccountIsOffered: a personal account's installation is its owner's,
// and its settings live under the person's own GitHub settings.
func TestTheirOwnAccountIsOffered(t *testing.T) {
	h, app := withHub(t)
	h.accounts[78], h.types[78] = "octo", "User"
	h.reach["octo"] = []int64{78}
	r := returnFrom(t, app, startOrgConnect(t, app, "acme"), "octo")
	token, offered := chooserOn(t, r)
	if !strings.Contains(string(r.Body), "Personal account") ||
		!strings.Contains(string(r.Body), `href="https://github.com/settings/installations/78"`) {
		t.Fatalf("octo's own account must read as personal, with its own settings page: %s", r.Body)
	}
	backOnThePage(t, answer(t, app, url.Values{"state": {token}, "account": {offered["octo"]}}), "octo")
	if got := bound(t, "acme"); len(got) != 1 || got[0] != "octo=78" {
		t.Fatalf("acme must hold octo's own installation, got %v", got)
	}
}

// TestOfTwoOnlyTheChosenBinds: the person controls two installations and means one.
func TestOfTwoOnlyTheChosenBinds(t *testing.T) {
	h, app := adoptHub(t)
	h.accounts[78], h.types[78] = "octo", "User"
	h.reach["octo"] = []int64{77, 78}
	token, offered := chooserOn(t, returnFrom(t, app, startOrgConnect(t, app, "acme"), "octo"))
	if len(offered) != 2 {
		t.Fatalf("both installations must be offered, got %v", offered)
	}
	backOnThePage(t, answer(t, app, url.Values{"state": {token}, "account": {offered["octo"]}}), "octo")
	if got := bound(t, "acme"); len(got) != 1 || got[0] != "octo=78" {
		t.Fatalf("only the chosen installation binds, got %v", got)
	}
}

// TestTheChooserGoesOnToInstall: "install on another account" is the install page
// under a fresh state, with nothing bound, and that install's return binds.
func TestTheChooserGoesOnToInstall(t *testing.T) {
	h, app := adoptHub(t)
	token, _ := chooserOn(t, returnFrom(t, app, startOrgConnect(t, app, "acme"), "octo"))
	state := onToInstall(t, answer(t, app, url.Values{"state": {token}, "install": {"1"}}))
	if got := bound(t, "acme"); len(got) != 0 {
		t.Fatalf("going on to install binds nothing, got %v", got)
	}
	h.mu.Lock()
	h.accounts[81] = "fresh-gh"
	h.reach["octo"] = append(h.reach["octo"], 81)
	h.mu.Unlock()
	r := asPerson(t, app, http.MethodGet, "/v1/provider/github/callback?state="+state+
		"&code=code-octo&installation_id=81&setup_action=install", "", "", false, nil)
	backOnThePage(t, r, "fresh-gh")
	if got := bound(t, "acme"); len(got) != 1 || got[0] != "fresh-gh=81" {
		t.Fatalf("the install must bind fresh-gh alone, got %v", got)
	}
}

// TestOnlyAnOfferedAccountBinds: the account is the form's word, and the form can
// be edited; the offer under the signature is what decides.
func TestOnlyAnOfferedAccountBinds(t *testing.T) {
	h, app := adoptHub(t)
	h.accounts[90] = "someone-else"
	token, _ := chooserOn(t, returnFrom(t, app, startOrgConnect(t, app, "acme"), "octo"))
	refusedOnThePage(t, answer(t, app, url.Values{"state": {token}, "account": {"90"}}), "that account was not offered")
	if got := bound(t, "acme"); len(got) != 0 {
		t.Fatalf("an account the chooser did not offer must not bind, got %v", got)
	}
}

// TestATamperedOfferIsRefused: rewriting the offer breaks the signature.
func TestATamperedOfferIsRefused(t *testing.T) {
	h, app := adoptHub(t)
	h.accounts[90] = "someone-else"
	token, _ := chooserOn(t, returnFrom(t, app, startOrgConnect(t, app, "acme"), "octo"))
	dot := strings.IndexByte(token, '.')
	raw, err := base64.URLEncoding.DecodeString(token[:dot])
	if err != nil {
		t.Fatalf("token payload: %v", err)
	}
	forged := strings.Replace(string(raw), `"id":"77","login":"acme-gh"`, `"id":"90","login":"someone-else"`, 1)
	if forged == string(raw) {
		t.Fatalf("the offer was not where the test expects it: %s", raw)
	}
	bad := base64.URLEncoding.EncodeToString([]byte(forged)) + token[dot:]
	refused := answer(t, app, url.Values{"state": {bad}, "account": {"90"}})
	if refused.Code != http.StatusFound || !strings.Contains(refused.Location, "reason=invalid+state") {
		t.Fatalf("a forged offer must be refused as an invalid state, got %d %q", refused.Code, refused.Location)
	}
	if got := bound(t, "acme"); len(got) != 0 {
		t.Fatalf("a forged offer must bind nothing, got %v", got)
	}
}

// TestAChooserAnswersOnce: one page, one answer.
func TestAChooserAnswersOnce(t *testing.T) {
	_, app := adoptHub(t)
	token, offered := chooserOn(t, returnFrom(t, app, startOrgConnect(t, app, "acme"), "octo"))
	form := url.Values{"state": {token}, "account": {offered["acme-gh"]}}
	backOnThePage(t, answer(t, app, form), "acme-gh")
	again := answer(t, app, form)
	if again.Code != http.StatusFound || !strings.Contains(again.Location, "error=github") {
		t.Fatalf("a second answer must be refused, got %d %q", again.Code, again.Location)
	}
}

// TestAChooserTokenIsNoCallbackState: the chooser's token is signed with the
// state's key, but it is not a state. The callback refuses it without spending
// it, so the page can still be answered.
func TestAChooserTokenIsNoCallbackState(t *testing.T) {
	_, app := adoptHub(t)
	token, offered := chooserOn(t, returnFrom(t, app, startOrgConnect(t, app, "acme"), "octo"))
	misused := returnFrom(t, app, token, "octo")
	if misused.Code != http.StatusFound || !strings.Contains(misused.Location, "reason=invalid+state") {
		t.Fatalf("a chooser's token must be refused as a callback state, got %d %q", misused.Code, misused.Location)
	}
	backOnThePage(t, answer(t, app, url.Values{"state": {token}, "account": {offered["acme-gh"]}}), "acme-gh")
}

// TestACallbackStateIsNoChooserToken: a state carries no offer, so it chooses nothing.
func TestACallbackStateIsNoChooserToken(t *testing.T) {
	_, app := adoptHub(t)
	refused := answer(t, app, url.Values{"state": {startOrgConnect(t, app, "acme")}, "account": {"77"}})
	if refused.Code != http.StatusFound || !strings.Contains(refused.Location, "reason=invalid+state") {
		t.Fatalf("a callback state must be refused by the chooser, got %d %q", refused.Code, refused.Location)
	}
	if got := bound(t, "acme"); len(got) != 0 {
		t.Fatalf("a callback state must bind nothing, got %v", got)
	}
}

// TestTheChooserCanBeLeft: Cancel goes back to the page that asked, saying so.
func TestTheChooserCanBeLeft(t *testing.T) {
	_, app := adoptHub(t)
	r := returnFrom(t, app, startOrgConnect(t, app, "acme"), "octo")
	chooserOn(t, r)
	if !strings.Contains(html.UnescapeString(string(r.Body)), "error=github&reason=cancelled") {
		t.Fatalf("the chooser must offer a way back to the page that asked: %s", r.Body)
	}
}

// TestSomeoneElsesAccountIsNotOffered: reaching another person's account is not
// owning it.
func TestSomeoneElsesAccountIsNotOffered(t *testing.T) {
	h, app := withHub(t)
	h.accounts[79], h.types[79] = "mona", "User"
	h.reach["octo"] = []int64{79}
	onToInstall(t, returnFrom(t, app, startOrgConnect(t, app, "acme"), "octo"))
	if got := bound(t, "acme"); len(got) != 0 {
		t.Fatalf("another person's account must not be bound, got %v", got)
	}
}

// TestAMemberWhoAdministersNotEveryRepoIsOfferedNothing: the installation reaches a
// repository octo does not administer, so binding it would give acme more than
// octo holds.
func TestAMemberWhoAdministersNotEveryRepoIsOfferedNothing(t *testing.T) {
	h, app := adoptHub(t)
	h.repos[1].admin = map[string]bool{}
	onToInstall(t, returnFrom(t, app, startOrgConnect(t, app, "acme"), "octo"))
	if got := bound(t, "acme"); len(got) != 0 {
		t.Fatalf("a member who does not administer every repository binds nothing, got %v", got)
	}
}

// TestAnInstallationWithNoRepositoriesIsNotOffered: administering none of nothing
// proves nothing, and repositories added later would arrive unasked.
func TestAnInstallationWithNoRepositoriesIsNotOffered(t *testing.T) {
	h, app := withHub(t)
	h.accounts[80] = "empty-gh"
	h.reach["octo"] = []int64{80}
	onToInstall(t, returnFrom(t, app, startOrgConnect(t, app, "acme"), "octo"))
	if got := bound(t, "acme"); len(got) != 0 {
		t.Fatalf("an installation with no repositories must not be bound, got %v", got)
	}
}

// heldBy binds acme-gh's installation to another org, as a workspace the person
// has since left would hold it.
func heldBy(t *testing.T, org string) {
	t.Helper()
	if err := mounted.State.store.Upsert(t.Context(), Connection{
		Org: org, Provider: "github", Label: "acme-gh", ExternalID: "77", AccountLabel: "acme-gh",
	}); err != nil {
		t.Fatalf("%s upsert: %v", org, err)
	}
}

// TestAnInstallationAnotherOrgHoldsIsShownNotOffered: one account, one tenant. The
// other org keeps it, but the person who controls it is told where it is and how
// to free it, rather than being sent to GitHub's install page, where the account
// offers only Configure and saving there returns nothing.
func TestAnInstallationAnotherOrgHoldsIsShownNotOffered(t *testing.T) {
	_, app := adoptHub(t)
	heldBy(t, "left-workspace")
	r := returnFrom(t, app, startOrgConnect(t, app, "acme"), "octo")
	_, offered, held := chooserRead(t, r)
	if len(offered) != 0 || len(held) != 1 || held[0] != "acme-gh" {
		t.Fatalf("acme-gh must be shown held elsewhere and offered to nobody, got offered %v held %v", offered, held)
	}
	page := string(r.Body)
	if !strings.Contains(page, "another Hanzo workspace") ||
		!strings.Contains(page, `href="https://github.com/organizations/acme-gh/settings/installations/77"`) {
		t.Fatalf("the chooser must say acme-gh is in another workspace, and link where it is managed: %s", page)
	}
	if !strings.Contains(page, "also disconnects it from the workspace that has it") || strings.Contains(page, "Uninstall or change") {
		t.Fatalf("the chooser must warn that uninstalling disconnects the workspace that has it, not invite it: %s", page)
	}
	if strings.Contains(page, "left-workspace") {
		t.Fatalf("the chooser must not name another tenant: %s", page)
	}
	if got := bound(t, "acme"); len(got) != 0 {
		t.Fatalf("acme must not take another org's installation, got %v", got)
	}
	if got := bound(t, "left-workspace"); len(got) != 1 || got[0] != "acme-gh=77" {
		t.Fatalf("the other org's binding must be unchanged, got %v", got)
	}
}

// TestAnAccountHeldElsewhereCannotBeChosen: the page draws it, but the offer under
// the signature does not carry it, so a form edited to name it binds nothing.
func TestAnAccountHeldElsewhereCannotBeChosen(t *testing.T) {
	_, app := adoptHub(t)
	heldBy(t, "left-workspace")
	token, _, _ := chooserRead(t, returnFrom(t, app, startOrgConnect(t, app, "acme"), "octo"))
	refusedOnThePage(t, answer(t, app, url.Values{"state": {token}, "account": {"77"}}), "that account was not offered")
	if got := bound(t, "acme"); len(got) != 0 {
		t.Fatalf("an account held elsewhere must not bind, got %v", got)
	}
	if got := bound(t, "left-workspace"); len(got) != 1 {
		t.Fatalf("the other org's binding must be unchanged, got %v", got)
	}
}

// TestWithOnlyAnAccountHeldElsewhereInstallStillFollows: the page offers nothing to
// connect, and its install button still goes on to the install page.
func TestWithOnlyAnAccountHeldElsewhereInstallStillFollows(t *testing.T) {
	_, app := adoptHub(t)
	heldBy(t, "left-workspace")
	token, _, _ := chooserRead(t, returnFrom(t, app, startOrgConnect(t, app, "acme"), "octo"))
	onToInstall(t, answer(t, app, url.Values{"state": {token}, "install": {"1"}}))
}

// TestAHeldElsewhereChooserTokenIsNoCallbackState: a chooser token that offers
// nothing is still a chooser's, and the callback refuses it.
func TestAHeldElsewhereChooserTokenIsNoCallbackState(t *testing.T) {
	_, app := adoptHub(t)
	heldBy(t, "left-workspace")
	token, _, _ := chooserRead(t, returnFrom(t, app, startOrgConnect(t, app, "acme"), "octo"))
	misused := returnFrom(t, app, token, "octo")
	if misused.Code != http.StatusFound || !strings.Contains(misused.Location, "reason=invalid+state") {
		t.Fatalf("a chooser's token must be refused as a callback state, got %d %q", misused.Code, misused.Location)
	}
}

// TestOneFreeAndOneHeldElsewhere: the free account is offered, the held one shown.
func TestOneFreeAndOneHeldElsewhere(t *testing.T) {
	h, app := adoptHub(t)
	h.accounts[78], h.types[78] = "octo", "User"
	h.reach["octo"] = []int64{77, 78}
	heldBy(t, "left-workspace")
	token, offered, held := chooserRead(t, returnFrom(t, app, startOrgConnect(t, app, "acme"), "octo"))
	if len(offered) != 1 || offered["octo"] != "78" || len(held) != 1 || held[0] != "acme-gh" {
		t.Fatalf("octo must be offered and acme-gh shown held, got offered %v held %v", offered, held)
	}
	backOnThePage(t, answer(t, app, url.Values{"state": {token}, "account": {"78"}}), "octo")
	if got := bound(t, "acme"); len(got) != 1 || got[0] != "octo=78" {
		t.Fatalf("acme must hold octo alone, got %v", got)
	}
}

// TestAChosenAccountTakenMeanwhileIsRefused: another org binding the account
// between the page and the answer wins, and this one is told so.
func TestAChosenAccountTakenMeanwhileIsRefused(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		_, app := adoptHub(t)
		token, offered := chooserOn(t, returnFrom(t, app, startOrgConnect(t, app, "acme"), "octo"))
		if err := mounted.State.store.Upsert(t.Context(), Connection{
			Org: "other", Provider: "github", Label: "acme-gh", ExternalID: "77", AccountLabel: "acme-gh",
		}); err != nil {
			t.Fatalf("other upsert: %v", err)
		}
		refusedOnThePage(t, answer(t, app, url.Values{"state": {token}, "account": {offered["acme-gh"]}}), heldElsewhere)
		if got := bound(t, "acme"); len(got) != 0 {
			t.Fatalf("acme must not take an account another org holds, got %v", got)
		}
	})
}

// TestAnAccountThisOrgHoldsGoesOnToInstallAnother: Sync's "install on another
// account" starts this same connect, so an account already held must not answer
// it as done.
func TestAnAccountThisOrgHoldsGoesOnToInstallAnother(t *testing.T) {
	_, app := adoptHub(t)
	backOnThePage(t, connectChoosing(t, app, "acme", "acme-gh"), "acme-gh")
	onToInstall(t, returnFrom(t, app, startOrgConnect(t, app, "acme"), "octo"))
	if got := bound(t, "acme"); len(got) != 1 {
		t.Fatalf("acme must still hold exactly acme-gh, got %v", got)
	}
}

// TestWithNothingInstalledTheInstallFollows walks both legs: nothing to offer, on
// to the install page with a fresh state, and GitHub's return from the install
// binds the new account and lands on the page that asked.
func TestWithNothingInstalledTheInstallFollows(t *testing.T) {
	h, app := withHub(t)
	state := onToInstall(t, returnFrom(t, app, startOrgConnect(t, app, "acme"), "octo"))

	h.mu.Lock()
	h.accounts[81] = "fresh-gh"
	h.reach["octo"] = []int64{81}
	h.mu.Unlock()
	r := asPerson(t, app, http.MethodGet, "/v1/provider/github/callback?state="+state+
		"&code=code-octo&installation_id=81&setup_action=install", "", "", false, nil)
	backOnThePage(t, r, "fresh-gh")
	if got := bound(t, "acme"); len(got) != 1 || got[0] != "fresh-gh=81" {
		t.Fatalf("the install must bind fresh-gh, got %v", got)
	}
}

// TestAnAuthorizationIsSpentOnce: the state the authorization page carried is
// single-use, like every other.
func TestAnAuthorizationIsSpentOnce(t *testing.T) {
	_, app := adoptHub(t)
	state := startOrgConnect(t, app, "acme")
	chooserOn(t, returnFrom(t, app, state, "octo"))
	r := returnFrom(t, app, state, "octo")
	if r.Code != http.StatusFound || !strings.Contains(r.Location, "error=github") {
		t.Fatalf("a replayed state must be refused, got %d %q", r.Code, r.Location)
	}
}
