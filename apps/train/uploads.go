package train

// uploads.go — the org's artifacts as objects: rows it uploads for jobs to read,
// what it holds, and deleting what it no longer wants.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/audit"
	"github.com/zap-proto/zip"
)

type uploadIn struct {
	// Kind is dataset: JSON Lines rows a job reads as `artifact:<sha256>`.
	Kind string `json:"kind"`
	// Name labels it.
	Name string `json:"name"`
	// SHA256 is the hex digest of the bytes.
	SHA256 string `json:"sha256"`
	// Size is the byte count.
	Size int64 `json:"size"`
}

// UploadTrainArtifact records rows the org is about to upload, named by the
// SHA-256 of their bytes, and answers the same grant an executor's artifact
// gets: a presigned PUT S3 accepts only with that checksum and length, encrypted
// under the org's key — a PUT per 64 MiB part over 64 MiB — or stored: true when
// the org already holds those bytes. A job names them as `dataset.uri`
// `artifact:<sha256>`, and its create reads the upload back before it is
// accepted. Storage is billed by the GB-month while the org keeps them, so an
// upload is refused 402/503 when the uploader's wallet cannot pay a month of
// keeping these bytes with what it already keeps in the org and what the org has
// pending.
//
// Example: {"kind": "dataset", "name": "tickets", "sha256": "…", "size": 1048576}
func (o ops) upload(ctx context.Context, in *uploadIn) (*registerOut, error) {
	st, org, err := o.tenant(ctx)
	if err != nil {
		return nil, err
	}
	if in.Kind != "dataset" || !sha.MatchString(in.SHA256) || in.Size <= 0 || in.Size > maxArtifact || (in.Name != "" && !name.MatchString(in.Name)) {
		return nil, invalid("an upload needs kind dataset, a sha256, a size in (0, %d] and a name", int64(maxArtifact))
	}
	p := cloud.PayerOf(ctx)
	if !p.Billable() {
		return nil, cloud.Denied(cloud.ErrNoLedger)
	}
	scope := project(ctx)
	a := artifact{SHA256: in.SHA256, Kind: in.Kind, Name: in.Name, Size: in.Size, Meta: json.RawMessage(`{}`), Created: unix(),
		payer: p.Wallet.String()}
	return o.admit(ctx, st, org, a, jsonlType, scope, p.Validated && p.Project == scope)
}

// Object is one of the org's stored or pending objects: the bytes every artifact
// with its sha256 names.
type Object struct {
	// SHA256 names the bytes.
	SHA256 string `json:"sha256"`
	// Kind is dataset for an upload, else the output kind a job produced it as.
	Kind string `json:"kind"`
	// Name labels it.
	Name string `json:"name"`
	// Size is its byte count.
	Size int64 `json:"size"`
	// State is pending until its bytes are read back, then stored.
	State string `json:"state"`
	// Jobs are the jobs that produced or hold it; none for an upload.
	Jobs []string `json:"jobs"`
	// Published says a job of the org has it published.
	Published bool `json:"published"`
	// Stored is when its bytes were read back, unix seconds.
	Stored int64 `json:"stored,omitempty"`
	// Expires is when retention deletes it unless published, unix seconds; absent
	// while it is published, pending, or retention keeps everything.
	Expires int64 `json:"expires,omitempty"`
	// URL fetches the bytes for ten minutes, on a read of one stored object.
	URL string `json:"url,omitempty"`
}

type objectsOut struct {
	// Data is the org's objects, newest first.
	Data []Object `json:"data"`
}

// ListTrainArtifacts answers the org's objects — its uploads and its jobs'
// outputs, one per sha256 — with what produced each, whether it is published and
// when retention deletes it.
func (o ops) listObjects(ctx context.Context, _ *cloud.Unit) (*objectsOut, error) {
	st, org, err := o.tenant(ctx)
	if err != nil {
		return nil, err
	}
	as, err := st.arts(ctx, `SELECT `+artCols+` FROM artifacts WHERE state != 'deleted' ORDER BY created DESC, sha256`)
	if err != nil {
		return nil, o.fault("artifact read", org, err)
	}
	obs, err := o.objectsOf(ctx, st, as)
	if err != nil {
		return nil, o.fault("artifact read", org, err)
	}
	return &objectsOut{Data: obs}, nil
}

// GetTrainArtifact answers one of the org's objects, as the list does, with an
// address that fetches its bytes for ten minutes once it is stored: how an
// executor reads an `artifact:<sha256>` dataset its job names, and how the org
// downloads what it uploaded. Another org's sha256 is not found, exactly as an
// unknown one is.
func (o ops) getObject(ctx context.Context, in *shaIn) (*Object, error) {
	st, org, err := o.tenant(ctx)
	if err != nil {
		return nil, err
	}
	if !sha.MatchString(in.SHA256) {
		return nil, missing("artifact")
	}
	as, err := st.arts(ctx, `SELECT `+artCols+` FROM artifacts WHERE sha256 = $1 AND state != 'deleted' ORDER BY created DESC`, in.SHA256)
	if err != nil {
		return nil, o.fault("artifact read", org, err)
	}
	if len(as) == 0 {
		return nil, missing("artifact")
	}
	obs, err := o.objectsOf(ctx, st, as)
	if err != nil {
		return nil, o.fault("artifact read", org, err)
	}
	ob := obs[0]
	if ob.State == "stored" {
		if ob.URL, _, err = o.download(ctx, st, org, ob.SHA256); err != nil {
			return nil, o.fault("artifact link", org, err)
		}
	}
	return &ob, nil
}

// objectsOf folds artifact rows into the objects they name, in the rows' order:
// one per sha256, stored once any row is, with the jobs naming it, whether it is
// published, and when retention deletes it.
func (o ops) objectsOf(ctx context.Context, st *store, as []artifact) ([]Object, error) {
	out := []Object{}
	at := map[string]int{}
	for _, a := range as {
		i, ok := at[a.SHA256]
		if !ok {
			i = len(out)
			at[a.SHA256] = i
			out = append(out, Object{SHA256: a.SHA256, Kind: a.Kind, Name: a.Name, Size: a.Size, State: a.State, Jobs: []string{}})
		}
		ob := &out[i]
		if a.State == "stored" {
			ob.State = "stored"
		}
		if a.Job != "" {
			ob.Jobs = append(ob.Jobs, a.Job)
		}
	}
	for i := range out {
		ob := &out[i]
		pub, _, err := st.uses(ctx, ob.SHA256)
		if err != nil {
			return nil, err
		}
		ob.Published = pub
		x, err := st.object(ctx, ob.SHA256)
		if errors.Is(err, errNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		ob.Stored = x.Stored
		if !pub && o.s.State.retain > 0 {
			ob.Expires = x.Stored + o.s.State.retain
		}
	}
	return out, nil
}

type shaIn struct {
	// SHA256 names the object, from the path.
	SHA256 string `json:"sha256"`
}

type deletedOut struct {
	// SHA256 names the object deleted.
	SHA256 string `json:"sha256"`
	// Deleted is true.
	Deleted bool `json:"deleted"`
}

// DeleteTrainArtifact deletes one of the org's objects — an upload or a job's
// output — from the store: every artifact naming it then reads deleted, and its
// storage is no longer billed. A published object is 409 published, and one a
// job that has not ended reads or produced is 409 in_use. Another org's sha256
// is not found, exactly as an unknown one is.
func (o ops) deleteArtifact(ctx context.Context, in *shaIn) (*deletedOut, error) {
	st, org, err := o.tenant(ctx)
	if err != nil {
		return nil, err
	}
	if !sha.MatchString(in.SHA256) {
		return nil, missing("artifact")
	}
	as, err := st.arts(ctx, `SELECT `+artCols+` FROM artifacts WHERE sha256 = $1 AND state != 'deleted'`, in.SHA256)
	if err != nil {
		return nil, o.fault("artifact read", org, err)
	}
	if len(as) == 0 {
		return nil, missing("artifact")
	}
	pub, live, err := st.uses(ctx, in.SHA256)
	if err != nil {
		return nil, o.fault("artifact read", org, err)
	}
	if pub {
		return nil, conflict("published", "the artifact is published; publish another output under its name first")
	}
	if live {
		return nil, conflict("in_use", "a job that has not ended reads or produced the artifact")
	}
	if err := o.forget(ctx, st, org, in.SHA256); err != nil {
		return nil, err
	}
	o.s.Log.Info("train artifact deleted", "org", org, "sha256", in.SHA256, "by", caller(ctx))
	return &deletedOut{SHA256: in.SHA256, Deleted: true}, nil
}

// forget removes an object from the store, then from the org's file, and ships
// the file. The store goes first: a removal that fails leaves the rows naming the
// object, and the next attempt removes it again.
func (o ops) forget(ctx context.Context, st *store, org, sum string) error {
	k := key(org, sum)
	if ob, err := st.object(ctx, sum); err == nil && ob.Key != "" {
		k = ob.Key
	} else if err != nil && !errors.Is(err, errNotFound) {
		return o.fault("artifact read", org, err)
	}
	ids, err := st.uploadIDs(ctx, sum)
	if err != nil {
		return o.fault("artifact read", org, err)
	}
	if err := o.s.State.objects.remove(ctx, k, ids); err != nil {
		o.s.Log.Error("train artifact remove failed", "org", org, "sha256", sum, "err", err)
		return zip.Errorf(http.StatusServiceUnavailable, "object store unavailable")
	}
	if err := st.forget(ctx, sum); err != nil {
		return o.fault("artifact delete", org, err)
	}
	return o.ship(org)
}

// deleting puts a deletion the platform makes on its own schedule on the audit
// trail: an org's data destroyed with no request of the org's behind it. One the
// trail cannot record is not made.
func (o ops) deleting(ctx context.Context, org, sum, action, why string, facts map[string]any) error {
	if o.s.State.audit == nil {
		return errors.New("train: no audit trail to record a deletion")
	}
	facts["org"], facts["sha256"] = org, sum
	after, _ := json.Marshal(facts)
	_, err := o.s.State.audit.Append(ctx, audit.Record{
		Actor:    audit.Actor{Org: org, Sub: "train-sweep"},
		Action:   action,
		Resource: audit.Resource{Type: "train.artifact", ID: org + "/" + sum},
		Auth:     audit.AuthContext{Method: "system"},
		Outcome:  audit.Outcome{Result: "success", Status: http.StatusOK, Reason: why},
		After:    audit.Redact(after),
	})
	return err
}
