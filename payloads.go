package cloud

import (
	"bytes"
	"fmt"

	"github.com/hanzoai/money"
	zap "github.com/zap-proto/go"
)

// payloads.go — the wire contracts of the internal plane's structured methods,
// until zapc generates typed codecs per subsystem (the future deps.go already
// names). One file, imported by BOTH ends of every method it describes, so the
// two halves of a payload cannot drift. Everything here is ZAP messages built
// in wire layout — scalars, text, raw bytes — and nothing is JSON.
//
// A payload with repeated records carries them as inner frames: the same
// 4-byte framing the transport itself uses, applied inside the payload. That
// is the ONE repetition rule until zapc emits real nested messages.

// ---- git.files: (repo, ref, glob) -> (rev, files) ----
//
// The delivery plane's manifest inventory. File bytes travel AS BYTES: the
// base64 detour in the HTTP shape existed only because JSON cannot carry
// binary, and going native deletes that layer rather than porting it.

const (
	fqRepoOff = 0
	fqRefOff  = 8
	fqGlobOff = 16
	fqFixed   = 24
)

// PutFilesReq packs a git.files request.
func PutFilesReq(repo, ref, glob string) []byte {
	b := zap.NewBuilder(len(repo) + len(ref) + len(glob) + fqFixed + 64)
	ob := b.StartObject(fqFixed)
	ob.SetText(fqRepoOff, repo)
	ob.SetText(fqRefOff, ref)
	ob.SetText(fqGlobOff, glob)
	ob.FinishAsRoot()
	return b.Finish()
}

// FilesReq unpacks one.
func FilesReq(payload []byte) (repo, ref, glob string, err error) {
	m, err := zap.Parse(payload)
	if err != nil {
		return "", "", "", fmt.Errorf("files req: %w", err)
	}
	r := m.Root()
	return r.Text(fqRepoOff), r.Text(fqRefOff), r.Text(fqGlobOff), nil
}

// File is one file as git.files reports it. Truncated marks a file listed but
// larger than the read limit: its Data is absent, and a consumer assembling a
// COMPLETE set must refuse the whole read rather than proceed without it.
type File struct {
	Path      string
	Data      []byte
	Truncated bool
}

const (
	fhRevOff   = 0
	fhCountOff = 8
	fhFixed    = 12
)

const (
	fPathOff  = 0
	fDataOff  = 8
	fTruncOff = 16
	fFixed    = 17
)

// PutFiles packs a git.files reply: a header frame (rev + count), then one
// frame per file.
func PutFiles(rev string, files []File) []byte {
	var out bytes.Buffer
	h := zap.NewBuilder(len(rev) + fhFixed + 64)
	hb := h.StartObject(fhFixed)
	hb.SetText(fhRevOff, rev)
	hb.SetUint32(fhCountOff, uint32(len(files)))
	hb.FinishAsRoot()
	_ = writeFrame(&out, h.Finish())
	for _, f := range files {
		b := zap.NewBuilder(len(f.Path) + len(f.Data) + fFixed + 64)
		fb := b.StartObject(fFixed)
		fb.SetText(fPathOff, f.Path)
		fb.SetBytes(fDataOff, f.Data)
		fb.SetBool(fTruncOff, f.Truncated)
		fb.FinishAsRoot()
		_ = writeFrame(&out, b.Finish())
	}
	return out.Bytes()
}

// Files unpacks one. The count is the header's promise and the frames are the
// delivery; a short payload is an error, never a shorter list — a partial
// inventory silently returned is how a pruning consumer sweeps a fleet.
func Files(payload []byte) (rev string, files []File, err error) {
	r := bytes.NewReader(payload)
	hb, err := readFrame(r)
	if err != nil {
		return "", nil, fmt.Errorf("files: header: %w", err)
	}
	hm, err := zap.Parse(hb)
	if err != nil {
		return "", nil, fmt.Errorf("files: header: %w", err)
	}
	hr := hm.Root()
	rev = hr.Text(fhRevOff)
	n := int(hr.Uint32(fhCountOff))
	files = make([]File, 0, n)
	for i := 0; i < n; i++ {
		fb, err := readFrame(r)
		if err != nil {
			return "", nil, fmt.Errorf("files: %d of %d: %w", i+1, n, err)
		}
		fm, err := zap.Parse(fb)
		if err != nil {
			return "", nil, fmt.Errorf("files: %d of %d: %w", i+1, n, err)
		}
		fr := fm.Root()
		files = append(files, File{
			Path:      fr.Text(fPathOff),
			Data:      fr.Bytes(fDataOff),
			Truncated: fr.Bool(fTruncOff),
		})
	}
	return rev, files, nil
}

// ---- finance.balance: (subject, currency) -> cents ----
//
// The prepaid ledger read. The per-org SQLite ledger has ONE writer, so only the
// process that mounts commerce may open it; everyone else asks that process
// here rather than importing it — which is what this codec is for. The reply is
// a single scalar, so it rides the shared PutI64/I64 above rather than growing
// a second one-field message.
//
// THERE IS NO ORG FIELD, DELIBERATELY. The org is the tenant whose books are
// read, and it is taken from the CAPABILITY the envelope carries (Ident.Org) —
// never from the payload. A request that could name its own org would be a
// request to read another tenant's ledger, so the wire simply cannot express
// it. The subject is a wallet WITHIN that org, which is the caller's to choose
// and harmless to name; making the org unrepresentable is stronger than
// validating it away, because there is no field left to get the check wrong on.

const (
	fbSubjectOff  = 0
	fbCurrencyOff = 8
	fbFixed       = 16
)

// PutBalanceReq packs a finance.balance request. The org is absent by design:
// it rides the capability (see above).
func PutBalanceReq(subject, currency string) []byte {
	b := zap.NewBuilder(len(subject) + len(currency) + fbFixed + 64)
	ob := b.StartObject(fbFixed)
	ob.SetText(fbSubjectOff, subject)
	ob.SetText(fbCurrencyOff, currency)
	ob.FinishAsRoot()
	return b.Finish()
}

// BalanceReq unpacks one. An absent currency reads as "" and the method applies
// its own default, so an older caller keeps working.
func BalanceReq(payload []byte) (subject, currency string, err error) {
	m, err := zap.Parse(payload)
	if err != nil {
		return "", "", fmt.Errorf("balance: %w", err)
	}
	r := m.Root()
	return r.Text(fbSubjectOff), r.Text(fbCurrencyOff), nil
}

// ---- platform.fleet: () -> []App ----
//
// The operator's view of every deployed app. admin's product board renders it
// and nothing more, so what crosses is the projection, not the k8s machinery
// that produced it: reading this by import pulled client-go, apimachinery and
// their applyconfigurations — about 160 packages — into a console that only
// prints strings, and still rendered empty, because CurrentFleet reads a
// package global that is nil in any binary platform does not mount.

// App is one deployed app as the platform observer sees it. Every field is a
// value the board prints; Drift is pre-rolled to the severity string the
// operator computed, because the board's only question is whether it is "ok".
type App struct {
	Org, Name, Env, Repo, Role         string
	Cluster, Namespace, Phase, Health  string
	DeclaredTag, RunningTag, LatestTag string
	// Registry is the image repository the workload actually runs, which is what
	// the board's tier classification reads — a real property of the deployment,
	// never an operator-typed label.
	Registry      string
	DriftSeverity string
}

const (
	aOrgOff     = 0
	aNameOff    = 8
	aEnvOff     = 16
	aRepoOff    = 24
	aRoleOff    = 32
	aClusterOff = 40
	aNsOff      = 48
	aPhaseOff   = 56
	aHealthOff  = 64
	aDeclOff    = 72
	aRunOff     = 80
	aLatestOff  = 88
	aDriftOff   = 96
	aRegOff     = 104
	aFixed      = 112
)

// PutApps packs a platform.fleet reply: a count frame, then one frame per app.
func PutApps(apps []App) []byte {
	var out bytes.Buffer
	h := zap.NewBuilder(8 + 64)
	hb := h.StartObject(8)
	hb.SetUint32(0, uint32(len(apps)))
	hb.FinishAsRoot()
	_ = writeFrame(&out, h.Finish())
	for _, a := range apps {
		b := zap.NewBuilder(len(a.Org) + len(a.Name) + len(a.Repo) + len(a.Namespace) + aFixed + 256)
		ob := b.StartObject(aFixed)
		ob.SetText(aOrgOff, a.Org)
		ob.SetText(aNameOff, a.Name)
		ob.SetText(aEnvOff, a.Env)
		ob.SetText(aRepoOff, a.Repo)
		ob.SetText(aRoleOff, a.Role)
		ob.SetText(aClusterOff, a.Cluster)
		ob.SetText(aNsOff, a.Namespace)
		ob.SetText(aPhaseOff, a.Phase)
		ob.SetText(aHealthOff, a.Health)
		ob.SetText(aDeclOff, a.DeclaredTag)
		ob.SetText(aRunOff, a.RunningTag)
		ob.SetText(aLatestOff, a.LatestTag)
		ob.SetText(aDriftOff, a.DriftSeverity)
		ob.SetText(aRegOff, a.Registry)
		ob.FinishAsRoot()
		_ = writeFrame(&out, b.Finish())
	}
	return out.Bytes()
}

// Apps unpacks one. The count is the header's promise and the frames are the
// delivery: a short payload is an error, never a shorter list, so a board can
// never quietly render a partial fleet as the whole fleet.
func Apps(payload []byte) ([]App, error) {
	r := bytes.NewReader(payload)
	hb, err := readFrame(r)
	if err != nil {
		return nil, fmt.Errorf("apps: header: %w", err)
	}
	hm, err := zap.Parse(hb)
	if err != nil {
		return nil, fmt.Errorf("apps: header: %w", err)
	}
	n := int(hm.Root().Uint32(0))
	out := make([]App, 0, n)
	for i := 0; i < n; i++ {
		fb, err := readFrame(r)
		if err != nil {
			return nil, fmt.Errorf("apps: %d of %d: %w", i+1, n, err)
		}
		m, err := zap.Parse(fb)
		if err != nil {
			return nil, fmt.Errorf("apps: %d of %d: %w", i+1, n, err)
		}
		v := m.Root()
		out = append(out, App{
			Org: v.Text(aOrgOff), Name: v.Text(aNameOff), Env: v.Text(aEnvOff),
			Repo: v.Text(aRepoOff), Role: v.Text(aRoleOff),
			Cluster: v.Text(aClusterOff), Namespace: v.Text(aNsOff),
			Phase: v.Text(aPhaseOff), Health: v.Text(aHealthOff),
			DeclaredTag: v.Text(aDeclOff), RunningTag: v.Text(aRunOff),
			LatestTag: v.Text(aLatestOff), DriftSeverity: v.Text(aDriftOff),
			Registry: v.Text(aRegOff),
		})
	}
	return out, nil
}

// ---- iam.mailable: () -> recipients ----
//
// The org is not in the payload: it rides the capability, so a caller cannot ask
// for a tenant it is not acting for by naming one here.
//
// Four fields, and deliberately not model.User. A caller needs to address a person
// and to match them against a warehouse cohort — which may name them by opaque id,
// by "owner/name", by bare username, or by email — and nothing else. Putting the
// identity record on the wire would ship credential columns to answer an audience
// count.

const (
	rcCountOff = 0
	rcHdrFixed = 4
)

const (
	rIDOff    = 0
	rOwnerOff = 8
	rNameOff  = 16
	rEmailOff = 24
	rFixed    = 32
)

// Recipient is one mailable person.
type Recipient struct {
	ID    string
	Owner string
	Name  string
	Email string
}

// PutRecipients packs an iam.mailable reply: a count header, then one frame each.
func PutRecipients(rs []Recipient) []byte {
	var out bytes.Buffer
	h := zap.NewBuilder(rcHdrFixed + 16)
	hb := h.StartObject(rcHdrFixed)
	hb.SetUint32(rcCountOff, uint32(len(rs)))
	hb.FinishAsRoot()
	_ = writeFrame(&out, h.Finish())
	for _, r := range rs {
		b := zap.NewBuilder(len(r.ID) + len(r.Owner) + len(r.Name) + len(r.Email) + rFixed + 64)
		rb := b.StartObject(rFixed)
		rb.SetText(rIDOff, r.ID)
		rb.SetText(rOwnerOff, r.Owner)
		rb.SetText(rNameOff, r.Name)
		rb.SetText(rEmailOff, r.Email)
		rb.FinishAsRoot()
		_ = writeFrame(&out, b.Finish())
	}
	return out.Bytes()
}

// Recipients unpacks one. The count is the header's promise and the frames are the
// delivery: a short payload is an error, never a shorter roster. A silently
// truncated audience is one that mails some of the people it was asked to.
func Recipients(payload []byte) ([]Recipient, error) {
	r := bytes.NewReader(payload)
	hb, err := readFrame(r)
	if err != nil {
		return nil, fmt.Errorf("recipients: header: %w", err)
	}
	hm, err := zap.Parse(hb)
	if err != nil {
		return nil, fmt.Errorf("recipients: header: %w", err)
	}
	n := int(hm.Root().Uint32(rcCountOff))
	out := make([]Recipient, 0, n)
	for i := 0; i < n; i++ {
		fb, err := readFrame(r)
		if err != nil {
			return nil, fmt.Errorf("recipients: %d of %d: %w", i+1, n, err)
		}
		fm, err := zap.Parse(fb)
		if err != nil {
			return nil, fmt.Errorf("recipients: %d of %d: %w", i+1, n, err)
		}
		rr := fm.Root()
		out = append(out, Recipient{
			ID:    rr.Text(rIDOff),
			Owner: rr.Text(rOwnerOff),
			Name:  rr.Text(rNameOff),
			Email: rr.Text(rEmailOff),
		})
	}
	return out, nil
}

// ---- kms.get / kms.put / kms.sign: (ref, value) -> value ----
//
// Secret material travels AS BYTES. The base64 a JSON shape would force is not a
// safety measure, it is an encoding tax paid because JSON cannot carry binary —
// and paying it here would put every secret through two extra copies on a path
// whose whole point is that it does not leave the machine.

const (
	skRefOff   = 0
	skValueOff = 8
	skFixed    = 16
)

// PutSecretMsg packs a kms request (ref alone for a read; ref + value for a write
// or a signature).
func PutSecretMsg(ref string, value []byte) []byte {
	b := zap.NewBuilder(len(ref) + len(value) + skFixed + 64)
	ob := b.StartObject(skFixed)
	ob.SetText(skRefOff, ref)
	ob.SetBytes(skValueOff, value)
	ob.FinishAsRoot()
	return b.Finish()
}

// SecretMsg unpacks one. It serves both directions: a reply carries the value with
// an empty ref.
func SecretMsg(payload []byte) (ref string, value []byte, err error) {
	m, err := zap.Parse(payload)
	if err != nil {
		return "", nil, fmt.Errorf("secret: %w", err)
	}
	r := m.Root()
	return r.Text(skRefOff), r.Bytes(skValueOff), nil
}

// ---- finance.authorize: (subject, currency, cents, scope) -> verdict ----
//
// The GATE. It extends the balance read's shape with the amount to authorize and
// the scope a spend cap is resolved on, and it keeps that read's rule: THERE IS NO
// ORG FIELD. The org is the tenant being billed and it rides the capability, so a
// request cannot express billing someone else.
//
// projectValidated travels because the caller is the only one who knows whether the
// project came from a claim or from a header a client could forge. A forgeable
// project must not be able to hard-stop OR to evade a cap, so the bit is carried
// rather than inferred.

// MONEY IS AN EXACT DECIMAL, not a count of cents. hanzoai/money.Amount is a
// decimal.Decimal plus a Currency, and it exists because a minor-unit integer
// cannot represent every currency this platform books in — HUSD carries 18
// decimals, so "cents" is not even the smallest unit there. The wire therefore
// carries the amount as its exact decimal text and the currency beside it, which
// is what Amount.String and money.ParseAmount round-trip without loss.
//
// int64 cents would be the third representation of one value and the only lossy
// one, which is precisely the kind of near-miss that reconciles to a rounding
// difference nobody can find later.
const (
	faSubjectOff   = 0
	faAmountOff    = 8
	faCurrencyOff  = 16
	faProjectOff   = 24
	faServiceOff   = 32
	faValidatedOff = 40
	faFixed        = 41
)

// PutAuthorizeReq packs a gate request. The amount carries its own currency, so
// the two cannot be separated in transit.
func PutAuthorizeReq(subject string, amount money.Amount, project, service string, projectValidated bool) []byte {
	dec, code := amount.String(), amount.Currency().Code
	b := zap.NewBuilder(len(subject) + len(dec) + len(code) + len(project) + len(service) + faFixed + 64)
	ob := b.StartObject(faFixed)
	ob.SetText(faSubjectOff, subject)
	ob.SetText(faAmountOff, dec)
	ob.SetText(faCurrencyOff, code)
	ob.SetText(faProjectOff, project)
	ob.SetText(faServiceOff, service)
	ob.SetBool(faValidatedOff, projectValidated)
	ob.FinishAsRoot()
	return b.Finish()
}

// AuthorizeReq unpacks one. A malformed amount is an error rather than a zero: a
// gate that reads an unparseable charge as "nothing to authorize" would let the
// work through free.
func AuthorizeReq(payload []byte) (subject string, amount money.Amount, project, service string, projectValidated bool, err error) {
	m, perr := zap.Parse(payload)
	if perr != nil {
		return "", money.Amount{}, "", "", false, fmt.Errorf("authorize: %w", perr)
	}
	r := m.Root()
	amt, perr := money.ParseAmount(r.Text(faAmountOff), r.Text(faCurrencyOff))
	if perr != nil {
		return "", money.Amount{}, "", "", false, fmt.Errorf("authorize: amount: %w", perr)
	}
	return r.Text(faSubjectOff), amt, r.Text(faProjectOff), r.Text(faServiceOff), r.Bool(faValidatedOff), nil
}

// The gate's verdict. Out-of-funds and a spend cap are DIFFERENT refusals with
// different remedies — one says add money, the other says the period has to roll
// over — so they are separate bits rather than one string the caller matches on.
const (
	gvOKOff      = 0
	gvNoFundsOff = 1
	gvCapOff     = 2
	gvReasonOff  = 8
	gvFixed      = 16
)

// Verdict is the answer to a gate.
type Verdict struct {
	OK       bool
	NoFunds  bool
	CapSpent bool
	// Reason is anything that is neither refusal — an upstream failure the caller
	// must treat as UNKNOWN and fail closed on, never as permission.
	Reason string
}

// PutVerdict packs one.
func PutVerdict(v Verdict) []byte {
	b := zap.NewBuilder(len(v.Reason) + gvFixed + 64)
	ob := b.StartObject(gvFixed)
	ob.SetBool(gvOKOff, v.OK)
	ob.SetBool(gvNoFundsOff, v.NoFunds)
	ob.SetBool(gvCapOff, v.CapSpent)
	ob.SetText(gvReasonOff, v.Reason)
	ob.FinishAsRoot()
	return b.Finish()
}

// GateVerdict unpacks one.
func GateVerdict(payload []byte) (Verdict, error) {
	m, err := zap.Parse(payload)
	if err != nil {
		return Verdict{}, fmt.Errorf("verdict: %w", err)
	}
	r := m.Root()
	return Verdict{
		OK:       r.Bool(gvOKOff),
		NoFunds:  r.Bool(gvNoFundsOff),
		CapSpent: r.Bool(gvCapOff),
		Reason:   r.Text(gvReasonOff),
	}, nil
}

// ---- usage: what a debit records beyond its amount ----

const (
	uModelOff     = 0
	uProjectOff   = 8
	uProviderOff  = 16
	uServiceOff   = 24
	uRequestIDOff = 32
	uClientIPOff  = 40
	uFixed        = 48
)

// Usage is the attribution a debit carries.
type Usage struct {
	Model     string
	Project   string
	Provider  string
	Service   string
	RequestID string
	ClientIP  string
}

// PutUsage packs the attribution frame that follows a money request.
func PutUsage(u Usage) []byte {
	b := zap.NewBuilder(len(u.Model) + len(u.Project) + len(u.Provider) + len(u.Service) + len(u.RequestID) + len(u.ClientIP) + uFixed) //nolint:lll
	ob := b.StartObject(uFixed)
	ob.SetText(uModelOff, u.Model)
	ob.SetText(uProjectOff, u.Project)
	ob.SetText(uProviderOff, u.Provider)
	ob.SetText(uServiceOff, u.Service)
	ob.SetText(uRequestIDOff, u.RequestID)
	ob.SetText(uClientIPOff, u.ClientIP)
	ob.FinishAsRoot()
	return b.Finish()
}

// UsageOf unpacks one.
func UsageOf(payload []byte) (Usage, error) {
	m, err := zap.Parse(payload)
	if err != nil {
		return Usage{}, fmt.Errorf("usage: %w", err)
	}
	r := m.Root()
	return Usage{
		Model:     r.Text(uModelOff),
		Project:   r.Text(uProjectOff),
		Provider:  r.Text(uProviderOff),
		Service:   r.Text(uServiceOff),
		RequestID: r.Text(uRequestIDOff),
		ClientIP:  r.Text(uClientIPOff),
	}, nil
}

// ---- git.publish: Visibility -> () ----
//
// A project's visibility, on its way to the canonical repo. projects decides it
// and git applies it; they are separate processes, so this crosses the plane.
// It used to cross a Register* seam, which resolves in-process and therefore
// never fired at all once each app became its own binary — public projects
// silently stopped getting repos.

// Visibility is one project's visibility as the world should see it.
//
//   - Org/Slug identify the project, and Org is also its AUTHORSHIP: the account
//     that pays for it. There is no separate author field because there is no
//     second copy of that fact.
//   - Listed is the RESOLVED answer to "may a stranger see this" — public AND
//     not moderated. git never re-derives it from parts, so the rule lives in
//     exactly one place (projects.Project.listed).
//   - Name/Description seed the repo the first time it is created and are never
//     re-imposed, so an author who edits their own repo description keeps it.
type Visibility struct {
	Org         string
	Slug        string
	Name        string
	Description string
	Listed      bool
}

const (
	vOrgOff    = 0
	vSlugOff   = 8
	vNameOff   = 16
	vDescOff   = 24
	vListedOff = 32
	vFixed     = 33
)

// PutVisibility packs one project's resolved visibility.
func PutVisibility(v Visibility) []byte {
	b := zap.NewBuilder(len(v.Org) + len(v.Slug) + len(v.Name) + len(v.Description) + vFixed + 64)
	ob := b.StartObject(vFixed)
	ob.SetText(vOrgOff, v.Org)
	ob.SetText(vSlugOff, v.Slug)
	ob.SetText(vNameOff, v.Name)
	ob.SetText(vDescOff, v.Description)
	ob.SetBool(vListedOff, v.Listed)
	ob.FinishAsRoot()
	return b.Finish()
}

// ReadVisibility unpacks one.
func ReadVisibility(payload []byte) (Visibility, error) {
	m, err := zap.Parse(payload)
	if err != nil {
		return Visibility{}, fmt.Errorf("visibility: %w", err)
	}
	r := m.Root()
	return Visibility{
		Org: r.Text(vOrgOff), Slug: r.Text(vSlugOff),
		Name: r.Text(vNameOff), Description: r.Text(vDescOff),
		Listed: r.Bool(vListedOff),
	}, nil
}

// ---- finance.usage: () -> usage rows ----
//
// The rows, not a rendered page. The ledger's owner knows what was debited; the
// HTTP surface knows what its customers' usage page looks like. Sending the
// envelope over the plane would put one app's response shape inside another app's
// process, and would need the renderer to live with the ledger — which is the
// import cycle that shape implies, made visible.

const (
	ucCountOff = 0
	ucHdrFixed = 4
)

const (
	uwIDOff      = 0
	uwModelOff   = 8
	uwCentsOff   = 16
	uwCreatedOff = 24
	uwFixed      = 32
)

// UsageRow is one recorded debit: the magnitude in USD minor units, the metered
// unit it was for, and when.
type UsageRow struct {
	ID        string
	Model     string
	Cents     int64
	CreatedAt int64
}

// PutUsageRows packs a finance.usage reply: a count header, then one frame each.
func PutUsageRows(rows []UsageRow) []byte {
	var out bytes.Buffer
	h := zap.NewBuilder(ucHdrFixed + 16)
	hb := h.StartObject(ucHdrFixed)
	hb.SetUint32(ucCountOff, uint32(len(rows)))
	hb.FinishAsRoot()
	_ = writeFrame(&out, h.Finish())
	for _, r := range rows {
		b := zap.NewBuilder(len(r.ID) + len(r.Model) + uwFixed + 64)
		rb := b.StartObject(uwFixed)
		rb.SetText(uwIDOff, r.ID)
		rb.SetText(uwModelOff, r.Model)
		rb.SetInt64(uwCentsOff, r.Cents)
		rb.SetInt64(uwCreatedOff, r.CreatedAt)
		rb.FinishAsRoot()
		_ = writeFrame(&out, b.Finish())
	}
	return out.Bytes()
}

// UsageRows unpacks one. A short payload is an error, never a shorter list: a
// silently truncated usage page is a customer being shown less than they were
// charged for.
func UsageRows(payload []byte) ([]UsageRow, error) {
	r := bytes.NewReader(payload)
	hb, err := readFrame(r)
	if err != nil {
		return nil, fmt.Errorf("usage rows: header: %w", err)
	}
	hm, err := zap.Parse(hb)
	if err != nil {
		return nil, fmt.Errorf("usage rows: header: %w", err)
	}
	n := int(hm.Root().Uint32(ucCountOff))
	out := make([]UsageRow, 0, n)
	for i := 0; i < n; i++ {
		fb, err := readFrame(r)
		if err != nil {
			return nil, fmt.Errorf("usage rows: %d of %d: %w", i+1, n, err)
		}
		fm, err := zap.Parse(fb)
		if err != nil {
			return nil, fmt.Errorf("usage rows: %d of %d: %w", i+1, n, err)
		}
		rr := fm.Root()
		out = append(out, UsageRow{
			ID:        rr.Text(uwIDOff),
			Model:     rr.Text(uwModelOff),
			Cents:     rr.Int64(uwCentsOff),
			CreatedAt: rr.Int64(uwCreatedOff),
		})
	}
	return out, nil
}
