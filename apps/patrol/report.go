package patrol

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/hanzoai/cloud/apps/framework"
	"github.com/hanzoai/cloud/sdk"
	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/zap-proto/zip"
)

// report.go writes what a shift, an incident or a client's estate amounted to.
//
// The body is composed from one estate read and stored on the document, so a
// report reads the same a month later as it did the minute it was filed. The PDF
// is produced on read from that body — one renderer, three kinds.

// Page shape for the rendered PDF: how many lines fit, and the type it is set
// in.
const (
	linesPerPage = 62
	bodyFont     = "Courier"
	bodyPoints   = 9
)

// compose builds a report body. A shift report is how the operation stands; a
// client report is that broken down by the customer each site is under contract
// for; an incident report is the one incident, its steps and its clocks.
func (o ops) compose(ctx context.Context, org string, in patrolReportIn) (string, error) {
	t, _ := framework.Get(ctx, org, DTTenant, DTTenant.Name)
	var b strings.Builder
	head(&b, t, in.Kind)
	switch in.Kind {
	case ReportIncident:
		if err := o.incidentBody(ctx, org, &b, in.Incident); err != nil {
			return "", err
		}
	case ReportClient:
		o.clientBody(ctx, org, &b, in.Client)
	default:
		o.estateBody(ctx, org, &b)
	}
	return b.String(), nil
}

// account is one customer's figures.
type account struct {
	sites, alarms, faults, keys, points, done, late int
}

// clientBody is the estate arranged by the customer it belongs to: what each one
// has under contract, how it stands, and how their rounds went. `only` names one
// customer; empty covers every one of them.
//
// A shift report answers "how did the operation do"; this answers "how did we do
// for you", which is a different question about the same three registers and not
// the same figures under another word. A site under no contract is the
// operation's own and appears in neither customer's account.
func (o ops) clientBody(ctx context.Context, org string, b *strings.Builder, only string) {
	filters := map[string]string{}
	if only != "" {
		filters["client"] = only
	}
	sites, _ := framework.Search(ctx, org, DTSite, filters, maxSites)
	owner := make(map[string]string, len(sites))
	held := map[string]*account{}
	for _, s := range sites {
		client := strField(s, "client")
		if client == "" {
			continue
		}
		owner[s.Name] = client
		a := held[client]
		if a == nil {
			a = &account{}
			held[client] = a
		}
		a.sites++
		switch strField(s, "status") {
		case SiteAlarm:
			a.alarms++
		case SiteFault:
			a.faults++
		}
	}
	keys, _ := framework.Search(ctx, org, DTKey, nil, maxSites)
	for _, k := range keys {
		if a := held[owner[strField(k, "site")]]; a != nil {
			a.keys++
		}
	}
	points, _ := framework.Search(ctx, org, DTPoint, nil, maxPoints)
	for _, p := range points {
		a := held[owner[strField(p, "site")]]
		if a == nil {
			continue
		}
		a.points++
		switch strField(p, "state") {
		case "done":
			a.done++
		case "late":
			a.late++
		}
	}

	b.WriteString("CLIENT ACCOUNTS\n")
	if len(held) == 0 {
		b.WriteString("  No estate under contract.\n\n")
		return
	}
	names := make([]string, 0, len(held))
	for name := range held {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		a := held[name]
		fmt.Fprintf(b, "  %s\n", name)
		row(b, "  Sites under contract", a.sites)
		row(b, "  Secure", a.sites-a.alarms-a.faults)
		row(b, "  Device / camera faults", a.faults)
		row(b, "  Active alarms", a.alarms)
		row(b, "  Key sets held", a.keys)
		row(b, "  Checkpoints scheduled", a.points)
		row(b, "  Confirmed", a.done)
		row(b, "  Late", a.late)
		b.WriteString("\n")
	}
}

// head is the letterhead every kind carries: who the company is and when the
// report was taken.
func head(b *strings.Builder, t framework.Document, kind string) {
	var co struct {
		LegalName    string `json:"legalName"`
		Address      string `json:"address"`
		Phone        string `json:"phone"`
		Licence      string `json:"licence"`
		Registration string `json:"registration"`
	}
	_ = json.Unmarshal(jsonField(t, "company"), &co)
	title := strings.ToUpper(strings.TrimSpace(co.LegalName))
	if title == "" {
		title = strings.ToUpper(kind)
	}
	fmt.Fprintf(b, "%s — %s REPORT\n", title, strings.ToUpper(kind))
	if co.Address != "" || co.Phone != "" {
		fmt.Fprintf(b, "%s · %s\n", co.Address, co.Phone)
	}
	if co.Licence != "" || co.Registration != "" {
		fmt.Fprintf(b, "%s · %s\n", co.Licence, co.Registration)
	}
	fmt.Fprintf(b, "Taken %s\n\n", now())
}

// estateBody is the figures a shift report turns on: how the estate stands, and
// how the round went.
func (o ops) estateBody(ctx context.Context, org string, b *strings.Builder) {
	sites, _ := framework.Search(ctx, org, DTSite, nil, maxSites)
	units, _ := framework.Search(ctx, org, DTUnit, nil, maxUnits)
	keys, _ := framework.Search(ctx, org, DTKey, nil, maxSites)
	points, _ := framework.Search(ctx, org, DTPoint, nil, maxPoints)

	alarms, faults := 0, 0
	for _, s := range sites {
		switch strField(s, "status") {
		case SiteAlarm:
			alarms++
		case SiteFault:
			faults++
		}
	}
	b.WriteString("ESTATE STATUS\n")
	row(b, "Sites under contract", len(sites))
	row(b, "Secure", len(sites)-alarms-faults)
	row(b, "Device / camera faults", faults)
	row(b, "Active alarms", alarms)
	row(b, "Client key sets held", len(keys))
	row(b, "Patrol units on duty", len(units))
	b.WriteString("\n")

	done, late, pending := 0, 0, 0
	for _, p := range points {
		switch strField(p, "state") {
		case "done":
			done++
		case "late":
			late++
		case PointPending:
			pending++
		}
	}
	b.WriteString("PATROL TOUR COMPLIANCE\n")
	row(b, "Checkpoints scheduled", len(points))
	row(b, "Confirmed", done)
	row(b, "Late", late)
	row(b, "Remaining in window", pending)
	b.WriteString("\n")
}

// incidentBody is the account of one response: what happened, when each act
// landed, and how the clocks ran against the contract.
func (o ops) incidentBody(ctx context.Context, org string, b *strings.Builder, name string) error {
	inc, err := framework.Get(ctx, org, DTIncident, name)
	if err != nil {
		return zip.ErrNotFound("incident not found")
	}
	fmt.Fprintf(b, "INCIDENT %s\n", inc.Name)
	line(b, "Site", strField(inc, "site"))
	line(b, "Zone", strField(inc, "zone"))
	line(b, "Trigger", strField(inc, "trigger"))
	line(b, "Tier", strField(inc, "tier"))
	line(b, "State", strField(inc, "state"))
	line(b, "Unit", strField(inc, "unit"))
	line(b, "Outcome", strField(inc, "outcome"))
	if intField(inc, "false") == 1 {
		line(b, "False alarm", "yes")
	}
	b.WriteString("\nSLA CLOCKS\n")
	for _, c := range o.clocks(ctx, org, inc) {
		settled := "not reached"
		if c.Settled >= 0 {
			settled = fmt.Sprintf("%ds", c.Settled)
		}
		fmt.Fprintf(b, "  %-24s %s (target %ds)\n", c.Label, settled, c.Target)
	}
	b.WriteString("\nTRAIL\n")
	// The whole trail: a filed account is what the operation stands behind, and
	// an attendance nobody is named for is not an account of one.
	for _, s := range o.steps(ctx, org, inc.Name, true) {
		fmt.Fprintf(b, "  %s  %-9s %s → %s  %s\n", s.At, s.Act, s.From, s.To, s.Actor)
	}
	b.WriteString("\n")
	return nil
}

// row writes one dotted figure line.
func row(b *strings.Builder, label string, n int) {
	fmt.Fprintf(b, "  %s %d\n", pad(label), n)
}

// line writes one dotted text line, skipping a value there is none of.
func line(b *strings.Builder, label, value string) {
	if strings.TrimSpace(value) == "" {
		return
	}
	fmt.Fprintf(b, "  %s %s\n", pad(label), value)
}

// pad dots a label out to a fixed column so the figures line up.
func pad(label string) string {
	const width = 34
	if len(label) >= width {
		return label
	}
	return label + " " + strings.Repeat(".", width-len(label)-1)
}

// file composes one report and stores it. The ask carries everything the body is
// composed from and everything the stored row records, so what a report is about
// is one value from one place.
func (o ops) file(ctx context.Context, org string, in patrolReportIn) (framework.Document, error) {
	body, err := o.compose(ctx, org, in)
	if err != nil {
		return framework.Document{}, err
	}
	row := map[string]any{"kind": in.Kind, "body": body, "at": now()}
	set(row, "incident", in.Incident)
	set(row, "client", in.Client)
	set(row, "from", in.From)
	set(row, "to", in.To)
	made, err := framework.Ingest(ctx, org, DTReport, row, "")
	if err != nil {
		return framework.Document{}, o.fail(err, "file report")
	}
	doc, err := framework.Get(ctx, org, DTReport, made.Name)
	if err != nil {
		return framework.Document{}, o.fail(err, "read report")
	}
	o.raise(ctx, org, LevelCyan, "Report "+in.Kind+" filed", strField(doc, "site"), "", doc.Name)
	o.tellControllers(ctx, org, "Report ready", "Report "+doc.Name+" ("+in.Kind+") is ready.")
	if in.Kind == ReportClient {
		o.tellClient(ctx, org, doc)
	}
	return doc, nil
}

// reported reports whether an incident already carries a report, which is what
// decides whether CLOSE writes one.
func (o ops) reported(ctx context.Context, org, incident string) bool {
	docs, err := framework.Search(ctx, org, DTReport, map[string]string{"incident": incident}, 1)
	return err == nil && len(docs) > 0
}

// fileReport writes the incident report FILE and CLOSE produce. Best effort: the
// incident has already moved.
func (o ops) fileReport(ctx context.Context, org string, inc framework.Document) {
	if _, err := o.file(ctx, org, patrolReportIn{Kind: ReportIncident, Incident: inc.Name}); err != nil {
		o.s.Log.Warn("patrol: incident report not filed", "org", org, "incident", inc.Name, "err", err)
	}
}

// tellClient emails the customer a filed report is about: the one it names, or
// the customer behind the incident's site. A report about the whole operation
// names neither and tells nobody.
func (o ops) tellClient(ctx context.Context, org string, doc framework.Document) {
	name := strField(doc, "client")
	if name == "" {
		inc, err := framework.Get(ctx, org, DTIncident, strField(doc, "incident"))
		if err != nil {
			return
		}
		st, err := framework.Get(ctx, org, DTSite, strField(inc, "site"))
		if err != nil {
			return
		}
		name = strField(st, "client")
	}
	cl, err := framework.Get(ctx, org, DTClient, name)
	if err != nil {
		return
	}
	o.mail(ctx, org, strField(cl, "contact"), "Report ready", "Report "+doc.Name+" is ready.")
}

// render sets a report body as a PDF: one primitives description, one page per
// run of lines.
func render(body string) ([]byte, error) {
	lines := strings.Split(strings.TrimRight(body, "\n"), "\n")
	pages := map[string]pdfPage{}
	for i, n := 0, 1; i < len(lines); i, n = i+linesPerPage, n+1 {
		end := min(i+linesPerPage, len(lines))
		// A page is set FROM its text, and there is nothing to set one from when
		// a report has no text at all. Splitting a body always yields a line, so
		// the empty report is a page holding an empty string rather than no page.
		run := strings.Join(lines[i:end], "\n")
		if run == "" {
			run = " "
		}
		pages[fmt.Sprint(n)] = pdfPage{Content: pdfContent{Text: []pdfText{{
			Value:  run,
			Anchor: "tl",
			Font:   pdfFont{Name: "$body"},
		}}}}
	}
	desc, err := json.Marshal(pdfDoc{
		Paper:  "A4P",
		Origin: "UpperLeft",
		Margin: pdfMargin{Width: 36},
		Fonts:  map[string]pdfFont{"body": {Name: bodyFont, Size: bodyPoints}},
		Pages:  pages,
	})
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	if err := api.Create(nil, bytes.NewReader(desc), &out, model.NewDefaultConfiguration()); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// The primitives description api.Create reads. Only the keys a report uses.
type (
	pdfDoc struct {
		Paper  string             `json:"paper"`
		Origin string             `json:"origin"`
		Margin pdfMargin          `json:"margin"`
		Fonts  map[string]pdfFont `json:"fonts"`
		Pages  map[string]pdfPage `json:"pages"`
	}
	pdfMargin struct {
		Width float64 `json:"width"`
	}
	pdfFont struct {
		Name string `json:"name"`
		Size int    `json:"size,omitempty"`
	}
	pdfPage struct {
		Content pdfContent `json:"content"`
	}
	pdfContent struct {
		Text []pdfText `json:"text"`
	}
	pdfText struct {
		Value  string  `json:"value"`
		Anchor string  `json:"anchor"`
		Font   pdfFont `json:"font"`
	}
)

// download is GET /v1/patrol/report/{name}: the report as a PDF, or as its own
// text with ?format=text. A PDF is bytes under a filename, which no In/Out pair
// describes, so this one stays a raw handler.
func download(s *cloud.Service[state], c *zip.Ctx) error {
	o := ops{s: s}
	org, err := o.admit(c.Context(), audience...)
	if err != nil {
		return err
	}
	name := c.Param("name")
	doc, err := framework.Get(c.Context(), org, DTReport, name)
	if err != nil {
		if !errors.Is(err, framework.ErrNotFound) {
			s.Log.Warn("patrol: read report", "org", org, "report", name, "err", err)
		}
		return zip.ErrNotFound("report not found")
	}
	if !o.ownReport(c.Context(), org, doc) {
		return zip.ErrNotFound("report not found")
	}
	body := strField(doc, "body")
	if c.Query("format") == "text" {
		c.SetHeader("Content-Type", "text/plain; charset=utf-8")
		return c.Bytes(http.StatusOK, []byte(body))
	}
	pdf, err := render(body)
	if err != nil {
		s.Log.Warn("patrol: render report", "org", org, "report", name, "err", err)
		c.SetHeader("Content-Type", "text/plain; charset=utf-8")
		return c.Bytes(http.StatusOK, []byte(body))
	}
	c.SetHeader("Content-Type", "application/pdf")
	c.SetHeader("Content-Disposition", fmt.Sprintf(`attachment; filename="%s.pdf"`, name))
	return c.Bytes(http.StatusOK, pdf)
}

// ownReport reports whether the caller may read this report. The control room
// and above read every one; a customer reads an account ABOUT them — the one
// filed on their own estate, or on an incident at a site of theirs — through the
// same reach every other read passes through. A report about the whole
// operation names neither and is nobody's but the operation's.
func (o ops) ownReport(ctx context.Context, org string, doc framework.Document) bool {
	r := o.reachOf(ctx, org)
	if r.all {
		return true
	}
	if client := strField(doc, "client"); client != "" {
		return client == r.client
	}
	inc, err := framework.Get(ctx, org, DTIncident, strField(doc, "incident"))
	if err != nil {
		return false
	}
	return r.sees(strField(inc, "site"))
}

// clocks are the three SLA clocks, derived from the incident's stamps against
// the tenant's targets. Nothing stores them.
func (o ops) clocks(ctx context.Context, org string, inc framework.Document) []patrolClock {
	var target struct {
		Verify   int `json:"verifyTargetSeconds"`
		Dispatch int `json:"dispatchTargetSeconds"`
	}
	if t, err := framework.Get(ctx, org, DTTenant, DTTenant.Name); err == nil {
		_ = json.Unmarshal(jsonField(t, "operations"), &target)
	}
	if target.Verify == 0 {
		target.Verify = 90
	}
	if target.Dispatch == 0 {
		target.Dispatch = 180
	}
	onsite := intField(inc, "sla") * 60
	if onsite == 0 {
		onsite = 1800
	}
	opened, ok := at(strField(inc, "opened"))
	if !ok {
		return nil
	}
	clock := func(label, stamp string, target int) patrolClock {
		v := patrolClock{Label: label, Target: target, Settled: -1}
		if t, ok := at(strField(inc, stamp)); ok {
			v.Settled = int(t.Sub(opened).Seconds())
			v.Elapsed = v.Settled
		} else {
			v.Elapsed = int(time.Since(opened).Seconds())
		}
		return v
	}
	return []patrolClock{
		clock("Verify", "verified", target.Verify),
		clock("Dispatch", "dispatched", target.Dispatch),
		clock("On site", "onsite", int(onsite)),
	}
}
