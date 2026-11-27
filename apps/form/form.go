package form

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/hanzoai/cloud/apps/crm"
	"github.com/hanzoai/cloud/client"
	basepeer "github.com/hanzoai/cloud/client/base"
	eventpeer "github.com/hanzoai/cloud/client/event"
	marketingpeer "github.com/hanzoai/cloud/client/marketing"
	notifypeer "github.com/hanzoai/cloud/client/notify"
	"github.com/hanzoai/cloud/internal/consent"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/geo"
	"github.com/hanzoai/cloud/sdk/plane"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/hanzoai/cloud/sdk/secret"
	"github.com/hanzoai/cloud/sdk/spec"
	addr "github.com/hanzoai/ai/address"
	"github.com/hanzoai/authz"
	forms "github.com/hanzoai/form"
	"github.com/zap-proto/zip"
)

//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

// The two public operations need no credential: a form is read and answered by
// whoever its page is open in. Each is declared by method, never by prefix.
func init() {
	spec.Open(forms.Prefix+"/public/:id", http.MethodGet)
	spec.Open(forms.Prefix+"/public/:id/responses", http.MethodPost)
}

// Use mounts the forms App and the SuperAdmin settings ops.
func Use(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return errors.New("form: nil app")
	}
	log := slog.Default().With("subsystem", "form")
	app.Use(forms.New(Deps(log)))
	adminRoutes(app)
	log.Info("form mounted", "prefix", forms.Prefix)
	return nil
}

// Deps is everything the forms App asks of its host, answered by cloud.
func Deps(log *slog.Logger) forms.Deps {
	return forms.Deps{
		Store:    store{},
		Platform: authz.AdminOrg,
		Caller:   caller,
		Client:   visitor,
		Intended: cloud.CSRF,
		Allow:    allow,
		Settings: current,
		Person:   crm.PersonID,
		Emit:     emit,
		File:     file,
		Mail:     mail,
		Confirm:  confirm,
		Secrets:  secrets{},
		Log:      log,
	}
}

// ── who is calling ────────────────────────────────────────────────────────────

// caller is the identity boundary's answer: the org a request acts in, whether
// the caller administers it, and a signed-in person's subject and address. The
// subject is set only for a person holding their own token (principal.Signed)
// AND presenting it in a header (cloud.Presented): a session cookie rides any
// request a page on a sibling host can make, so it names no respondent. An API
// key, an application and an operator acting as someone are nobody's response.
func caller(ctx context.Context) forms.Caller {
	var c forms.Caller
	if org, ok := principal.OrgFrom(ctx); ok {
		c.Org = org
		c.Admin = cloud.Admin.Admits(cloud.AuthorityIn(ctx))
	}
	if r, ok := cloud.Request(ctx); ok && cloud.Presented(r) != "" {
		if p, ok := principal.Signed(r); ok {
			c.Subject = p.Subject
			c.Email = strings.TrimSpace(r.Header(authz.HeaderUserEmail))
		}
	}
	return c
}

// visitor is the machine a request came from.
func visitor(ctx context.Context) forms.Client {
	r, ok := cloud.Request(ctx)
	if !ok {
		return forms.Client{}
	}
	return forms.Client{IP: geo.IP(r), OptedOut: consent.OptedOut(r)}
}

// windows counts submissions and views across the deployment.
var windows = cloud.NewWindow("form", 1, time.Hour)

// allow spends one of limit per window for the address ip under name, counted
// by addr.Buckets as the edge counts (an IPv6 address by its /64 and its /48),
// and an address the host could not read in one shared bucket, never in none.
func allow(_ context.Context, name, ip string, limit int, window time.Duration) bool {
	buckets := []addr.Bucket{{Key: name + "|-", Scale: 1}}
	if ip != "" {
		buckets = addr.Buckets(ip)
		for i := range buckets {
			buckets[i].Key = name + "|" + buckets[i].Key
		}
	}
	left, _ := windows.Spend(buckets, limit, window)
	return left >= 0
}

// ── where records live ────────────────────────────────────────────────────────

// planeBudget bounds one store call.
const planeBudget = 10 * time.Second

// on is a context acting for org on the plane: a fresh one, because a request's
// own caller outranks an org stated over it, bounded and cancelled with ctx.
func on(ctx context.Context, org string) (context.Context, context.CancelFunc) {
	c, cancel := context.WithTimeout(cloud.For(context.Background(), org), planeBudget)
	stop := context.AfterFunc(ctx, cancel)
	return c, func() { stop(); cancel() }
}

// store is each org's Base over the base plane.
type store struct{}

func (store) Install(ctx context.Context, org string, collections json.RawMessage) error {
	c, done := on(ctx, org)
	defer done()
	_, err := basepeer.BaseImport(plane.For(c, org), &client.BaseImportIn{Collections: collections})
	return err
}

func (store) Get(ctx context.Context, org, collection, id string) (forms.Record, error) {
	c, done := on(ctx, org)
	defer done()
	out, err := basepeer.BaseGet(plane.For(c, org), &client.BaseGetIn{Collection: collection, ID: id})
	if err != nil {
		var he *zip.HTTPError
		if errors.As(err, &he) && he.Status == http.StatusNotFound && strings.Contains(he.Error(), "no record") {
			return nil, forms.ErrNotFound
		}
		return nil, err
	}
	var r forms.Record
	if err := json.Unmarshal(out.Record, &r); err != nil {
		return nil, err
	}
	return r, nil
}

func (store) List(ctx context.Context, org, collection string, q forms.Query) (*forms.Page, error) {
	c, done := on(ctx, org)
	defer done()
	in := &client.BaseListIn{Collection: collection, Sort: q.Sort, Page: q.Page, PerPage: q.PerPage, SkipTotal: !q.Count}
	if q.Where != nil {
		w, err := json.Marshal(q.Where)
		if err != nil {
			return nil, err
		}
		in.Where = w
	}
	out, err := basepeer.BaseList(plane.For(c, org), in)
	if err != nil {
		return nil, err
	}
	page := &forms.Page{Items: make([]forms.Record, 0, len(out.Items)), Total: out.TotalItems}
	for _, raw := range out.Items {
		var r forms.Record
		if err := json.Unmarshal(raw, &r); err != nil {
			return nil, err
		}
		page.Items = append(page.Items, r)
	}
	return page, nil
}

func (store) Put(ctx context.Context, org, collection string, records ...forms.Record) error {
	c, done := on(ctx, org)
	defer done()
	in := &client.BaseUpsertIn{Collection: collection, Source: "application"}
	for _, r := range records {
		raw, err := json.Marshal(r)
		if err != nil {
			return err
		}
		in.Records = append(in.Records, raw)
	}
	_, err := basepeer.BaseUpsert(plane.For(c, org), in)
	return err
}

func (store) Delete(ctx context.Context, org, collection string, ids ...string) error {
	c, done := on(ctx, org)
	defer done()
	_, err := basepeer.BaseDelete(plane.For(c, org), &client.BaseDeleteIn{Collection: collection, IDs: ids, Source: "application"})
	return err
}

// ── sinks ─────────────────────────────────────────────────────────────────────

// sendFn is the event plane's one call, a variable so a test sees what leaves.
// Only a test reassigns it.
var sendFn = eventpeer.EventCapture

// stating bounds the statements in flight; past it one is dropped, not queued.
var stating = make(chan struct{}, 32)

// emit states an occurrence on the org's event plane, detached. The subject is
// the response (or the form, for a view); the person rides as person_id, never
// as an address.
func emit(_ context.Context, org string, e forms.Event) {
	select {
	case stating <- struct{}{}:
	default:
		return
	}
	in := &client.EventIn{Name: e.Name, Product: "form", Subject: e.Response}
	if in.Subject == "" {
		in.Subject = e.Form
	}
	for _, a := range []client.Signal{
		{Name: "form", Value: e.Form}, {Name: "public_id", Value: e.Public}, {Name: "response", Value: e.Response},
		{Name: "person_id", Value: e.PersonID}, {Name: "visitor", Value: e.Visitor}, {Name: "fields", Value: strings.Join(e.Fields, ",")},
	} {
		if a.Value != "" {
			in.Attributes = append(in.Attributes, a)
		}
	}
	go func() {
		defer func() { <-stating }()
		defer func() { _ = recover() }()
		ctx, cancel := context.WithTimeout(cloud.For(context.Background(), org), 5*time.Second)
		defer cancel()
		_, _ = sendFn(ctx, in)
	}()
}

// file files a completed response on the org's CRM timeline.
func file(ctx context.Context, org string, f forms.Filing) error {
	c, done := on(ctx, org)
	defer done()
	if err := crm.Install(c, org); err != nil {
		return fmt.Errorf("the org's CRM could not be installed: %w", err)
	}
	answers := make([]map[string]any, 0, len(f.Answers))
	for _, qa := range f.Answers {
		answers = append(answers, map[string]any{"question": qa.Question, "answer": qa.Answer})
	}
	at := f.At
	if at.IsZero() {
		at = time.Now()
	}
	return crm.FileForm(c, org, crm.FormFiling{
		Ref: f.Ref, Title: f.Title, Form: f.Form, Public: f.Public, Kind: f.Kind,
		Person: f.PersonID, Email: f.Email, Name: f.Name, Company: f.Company, List: f.List,
		Answers: answers, Hidden: f.Hidden, Visitor: f.Visitor, Subject: f.Subject, Path: f.Path, Verified: f.Verified,
	}, at)
}

// mailing counts each form's mails across the deployment, forwards and welcomes
// apart, each by the platform's own hourly limit (settings limits.forward and
// limits.welcome), so a flood of one cannot silence the other.
var mailing = cloud.NewWindow("form:mail", 1, time.Hour)

// clearFn is the suppression check, a variable so a test answers it. Only a test
// reassigns it.
var clearFn = marketingpeer.MarketingClearance

// tellFn is the notify rail, a variable so a test sees what leaves. Only a test
// reassigns it.
var tellFn = notifypeer.NotifySend

// mail sends one mail in the org's name on its own notify credential. A welcome
// goes to an address on no suppression list: the org's, or the platform's.
func mail(ctx context.Context, org string, m forms.Mail) error {
	limits := current(ctx).Limits
	most := limits.Forward
	if m.Kind == "welcome" {
		most = limits.Welcome
	}
	if left, _ := mailing.Spend([]addr.Bucket{{Key: org + "/" + m.Form + "/" + m.Kind, Scale: 1}}, most, time.Hour); left < 0 || most == 0 {
		return errors.New("the form's " + m.Kind + " mails this hour are at the ceiling")
	}
	c, done := on(ctx, org)
	defer done()
	if m.Kind == "welcome" {
		cl, err := clearFn(c, &client.Addressee{Org: org, Channel: "email", Address: m.To})
		if err != nil {
			return fmt.Errorf("the suppression list did not answer: %w", err)
		}
		if cl.Refused != "" {
			return errors.New("the address is on a suppression list: " + cl.Refused)
		}
	}
	_, err := tellFn(c, &client.Send{Org: org, Channel: "email", To: m.To, Subject: m.Subject, Body: letter(m.Text)})
	return err
}

// letter is plain text as the notify rail's HTML body: escaped, a blank line a
// paragraph and a line break a break, so nothing a respondent typed is markup.
func letter(text string) string {
	var b strings.Builder
	for _, p := range strings.Split(strings.ReplaceAll(text, "\r", ""), "\n\n") {
		if p = strings.TrimSpace(p); p == "" {
			continue
		}
		lines := strings.Split(p, "\n")
		for i, l := range lines {
			lines[i] = html.EscapeString(l)
		}
		b.WriteString("<p>" + strings.Join(lines, "<br>") + "</p>\n")
	}
	return b.String()
}

// subscribeFn is the double opt-in, a variable so a test sees what is asked. Only
// a test reassigns it.
var subscribeFn = marketingpeer.MarketingSubscribe

// confirm asks the double opt-in for an address a response only claimed. The one
// double opt-in this deployment runs is the brand newsletter's, so a claim on a
// brand form whose list is the newsletter is mailed its link; any other list has
// none, and a claim joins nothing.
func confirm(ctx context.Context, org string, c forms.Confirmation) error {
	if org != cloud.Brand() || !strings.EqualFold(strings.TrimSpace(c.List), crm.NewsletterList) {
		return nil
	}
	pc, done := on(ctx, org)
	defer done()
	_, err := subscribeFn(pc, &client.SubscribeAsk{Address: c.Email, Source: "form:" + c.Public, From: c.IP})
	return err
}

// secrets keeps webhook signing secrets in the org's KMS, at
// orgs/<org>/form/<NAME>@prod, on a call acting for that org.
type secrets struct{}

func ref(org, name string) string { return "orgs/" + org + "/form/" + name + "@prod" }

func (secrets) Put(ctx context.Context, org, name string, v []byte) error {
	c, done := on(ctx, org)
	defer done()
	return cloud.KMSPeer{}.PutSecret(c, ref(org, name), v)
}

func (secrets) Get(ctx context.Context, org, name string) ([]byte, error) {
	c, done := on(ctx, org)
	defer done()
	v, err := cloud.KMSPeer{}.GetSecret(c, ref(org, name))
	if errors.Is(err, secret.ErrNone) {
		return nil, forms.ErrNoSecret
	}
	return v, err
}

func (secrets) Delete(ctx context.Context, org, name string) error {
	c, done := on(ctx, org)
	defer done()
	err := cloud.KMSPeer{}.DeleteSecret(c, ref(org, name))
	if errors.Is(err, secret.ErrNone) {
		return forms.ErrNoSecret
	}
	return err
}

// ── settings ──────────────────────────────────────────────────────────────────

// held is the settings last read, and when.
type held struct {
	st forms.Settings
	at time.Time
}

var cached atomic.Pointer[held]

// fresh is how long a read of the settings stands.
const fresh = 30 * time.Second

// current is the platform's settings of forms: the saved document, read at most
// every 30 seconds, the last one read while the store does not answer, and the
// defaults when nothing is saved.
func current(ctx context.Context) forms.Settings {
	if h := cached.Load(); h != nil && time.Since(h.at) < fresh {
		return h.st
	}
	st, _, err := fetch(ctx)
	if err != nil {
		if h := cached.Load(); h != nil {
			return h.st
		}
		return forms.Defaults()
	}
	cached.Store(&held{st: st, at: time.Now()})
	return st
}
