package cloud

import (
	"bytes"
	"fmt"

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
