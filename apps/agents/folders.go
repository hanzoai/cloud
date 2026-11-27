package agents

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/audit"
	"github.com/hanzoai/cloud/sdk/dialect"
	"github.com/hanzoai/cloud/sdk/geo"
	"github.com/hanzoai/cloud/sdk/mint"
	"github.com/hanzoai/cloud/sdk/text"
	"github.com/zap-proto/zip"
)

// A folder grant lets a run started from the web work in one folder of a linked
// machine (hanzo-inc/app#310). Only the machine's owner grants or revokes one; a
// run that names a folder is admitted only inside an active grant, checked when
// it starts, when it is queued and when the machine claims it, and the machine
// re-checks the real path itself.
//
//	POST   /v1/agent/targets/:id/folders            grant a folder -> folderView
//	GET    /v1/agent/targets/:id/folders            the machine's active grants
//	DELETE /v1/agent/targets/:id/folders?folder=…   revoke a folder

// FolderNotGranted is the code a run is refused under when the folder it names
// is not inside an active grant on its machine.
const FolderNotGranted = "folder_not_granted"

// The audit actions a folder grant writes.
const (
	folderGrant  = "machine.folder.grant"
	folderRevoke = "machine.folder.revoke"
	folderRun    = "machine.folder.run"
	folderDeny   = "machine.folder.deny"
)

// maxFolder bounds a folder path: PATH_MAX on Linux.
const maxFolder = 4096

// Folder is one grant: a folder on a machine the web may start runs in.
// RevokedAt is 0 while the grant is active.
type Folder struct {
	ID        string
	Org       string
	Target    string
	Path      string
	GrantedBy string
	GrantedAt int64
	RevokedAt int64
}

func (s *Store) migrateFolders(ctx context.Context, tx *sql.Tx) error {
	const ddl = `
CREATE TABLE IF NOT EXISTS agent_target_folders (
  id         TEXT PRIMARY KEY,
  org        TEXT NOT NULL,
  target_id  TEXT NOT NULL,
  folder     TEXT NOT NULL,
  granted_by TEXT NOT NULL,
  granted_at BIGINT NOT NULL,
  revoked_at BIGINT NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS ix_target_folders ON agent_target_folders(org, target_id, revoked_at);
`
	if _, err := tx.ExecContext(ctx, ddl); err != nil {
		return fmt.Errorf("migrate target folders: %w", err)
	}
	return nil
}

// cleanFolder is a folder as a grant keeps it: absolute, lexically clean, and
// with no `..` segment in what was sent, so no spelling of a path escapes the
// folder it names.
func cleanFolder(raw string) (string, error) {
	switch {
	case raw == "":
		return "", fmt.Errorf("folder is required")
	case len(raw) > maxFolder:
		return "", fmt.Errorf("folder is longer than %d bytes", maxFolder)
	case !strings.HasPrefix(raw, "/"):
		return "", fmt.Errorf("folder must be an absolute path")
	case strings.ContainsFunc(raw, func(r rune) bool { return r < 0x20 || r == 0x7f }):
		return "", fmt.Errorf("folder must not contain control characters")
	}
	for _, seg := range strings.Split(raw, "/") {
		if seg == ".." {
			return "", fmt.Errorf("folder must not contain `..`")
		}
	}
	return path.Clean(raw), nil
}

// within reports whether dir is grant or lies under it: `/a/b` is under `/a`,
// `/ab` is not.
func within(dir, grant string) bool {
	return dir == grant || strings.HasPrefix(dir, strings.TrimSuffix(grant, "/")+"/")
}

const folderCols = `id,org,target_id,folder,granted_by,granted_at,revoked_at`

// Grant records an active grant of folder on (org, target) by grantedBy, or
// answers the active one already there; created says which.
func (s *Store) Grant(ctx context.Context, org, target, folder, grantedBy string, now int64) (f Folder, created bool, err error) {
	if err := dialect.Text(org, target, folder, grantedBy); err != nil {
		return Folder{}, false, fmt.Errorf("grant folder: %w", err)
	}
	active, err := s.Folders(ctx, org, target)
	if err != nil {
		return Folder{}, false, err
	}
	for _, f := range active {
		if f.Path == folder {
			return f, false, nil
		}
	}
	f = Folder{ID: mint.ID("fld"), Org: org, Target: target, Path: folder, GrantedBy: grantedBy, GrantedAt: now}
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO agent_target_folders (`+folderCols+`) VALUES ($1,$2,$3,$4,$5,$6,0)`,
		f.ID, f.Org, f.Target, f.Path, f.GrantedBy, f.GrantedAt); err != nil {
		return Folder{}, false, fmt.Errorf("grant folder: %w", err)
	}
	return f, true, nil
}

// Revoke ends every active grant of folder on (org, target) at now and reports
// whether there was one.
func (s *Store) Revoke(ctx context.Context, org, target, folder string, now int64) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE agent_target_folders SET revoked_at=$1 WHERE org=$2 AND target_id=$3 AND folder=$4 AND revoked_at=0`,
		now, org, target, folder)
	if err != nil {
		return false, fmt.Errorf("revoke folder: %w", err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// Folders returns the active grants on (org, target), oldest first.
func (s *Store) Folders(ctx context.Context, org, target string) ([]Folder, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+folderCols+` FROM agent_target_folders WHERE org=$1 AND target_id=$2 AND revoked_at=0
		 ORDER BY granted_at ASC, id ASC`, org, target)
	if err != nil {
		return nil, fmt.Errorf("list folders: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []Folder{}
	for rows.Next() {
		var f Folder
		if err := rows.Scan(&f.ID, &f.Org, &f.Target, &f.Path, &f.GrantedBy, &f.GrantedAt, &f.RevokedAt); err != nil {
			return nil, fmt.Errorf("scan folder: %w", err)
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// Granted reports whether dir is an active grant on (org, target) or lies under
// one. A dir that is relative or carries a `..` segment is never granted.
func (s *Store) Granted(ctx context.Context, org, target, dir string) (bool, error) {
	dir, err := cleanFolder(dir)
	if err != nil {
		return false, nil
	}
	active, err := s.Folders(ctx, org, target)
	if err != nil {
		return false, err
	}
	for _, f := range active {
		if within(dir, f.Path) {
			return true, nil
		}
	}
	return false, nil
}

// inside is nil when dir is granted on the machine, else the folder_not_granted
// refusal.
func (s *Store) inside(ctx context.Context, org, target, dir string) error {
	ok, err := s.Granted(ctx, org, target, dir)
	if err != nil {
		return err
	}
	if !ok {
		return &zip.HTTPError{Status: http.StatusForbidden, Code: FolderNotGranted,
			Msg: "the folder " + dir + " is not one the machine's owner has let the web use"}
	}
	return nil
}

// paths is the active grants' folders, which a machine reads on every heartbeat
// to drop a revoked folder.
func paths(fs []Folder) []string {
	out := make([]string, 0, len(fs))
	for _, f := range fs {
		out = append(out, f.Path)
	}
	return out
}

// Admit is the folder check of a run put on a machine: nil when dir is empty or
// granted there, else the folder_not_granted refusal. run is the run's session,
// empty before one is opened; a run admitted with a session is recorded as
// started in that folder, and every refusal is recorded as a denial.
func Admit(ctx context.Context, org, target, dir, run string) error {
	if dir == "" {
		return nil
	}
	sto, org, err := mountedStore(org)
	if err != nil {
		return err
	}
	return sto.admit(ctx, org, target, dir, run)
}

func (s *Store) admit(ctx context.Context, org, target, dir, run string) error {
	if err := s.inside(ctx, org, target, dir); err != nil {
		folderAudit(ctx, folderDeny, org, target, dir, run, err)
		return err
	}
	if run != "" {
		folderAudit(ctx, folderRun, org, target, dir, run, nil)
	}
	return nil
}

// folderAudit appends one machine.folder.* record naming the actor, org,
// machine, folder and run. A failure is logged, never the caller's: the grant or
// refusal it records has already happened.
func folderAudit(ctx context.Context, action, org, target, folder, run string, refused error) {
	if mounted == nil || mounted.State.audit == nil {
		return
	}
	who := cloud.Who(ctx)
	entry := audit.Record{
		Actor:    audit.Actor{Org: org, Sub: who.User, Email: who.Email},
		Action:   action,
		Resource: audit.Resource{Type: "machine", ID: target},
		Auth:     audit.AuthContext{Method: "gateway", IsAdmin: who.Admin},
		Outcome:  audit.Outcome{Result: "success", Status: http.StatusOK},
	}
	if refused != nil {
		entry.Outcome = audit.Outcome{Result: "deny", Status: http.StatusForbidden, Reason: FolderNotGranted}
	}
	if c, ok := cloud.Request(ctx); ok {
		entry.Method, entry.Path, entry.SourceIP = c.Method(), c.Path(), geo.IP(c)
		entry.UserAgent, entry.RequestID = c.Header("User-Agent"), c.RequestID()
	}
	after, err := json.Marshal(map[string]string{"machine": target, "folder": folder, "run": run})
	if err == nil {
		entry.After = after
	}
	if _, err := mounted.State.audit.Append(ctx, entry); err != nil {
		mounted.Log.Warn("agents: a folder grant event was not audited", "action", action, "org", org, "machine", target, "err", err)
	}
}

// ---- HTTP shapes ----

// folderIn names one folder on one machine.
type folderIn struct {
	// ID is the machine, from the path.
	ID string `json:"id"`
	// Folder is an absolute path on the machine, as its own file system spells
	// it. It is kept lexically clean, and a path with a `..` segment or that is
	// not absolute is refused.
	Folder string `json:"folder"`
}

// folderView is one active grant.
type folderView struct {
	// Folder is the granted path. A run may work in it or in any folder under it.
	Folder string `json:"folder"`
	// GrantedBy is the machine's owner, who granted it.
	GrantedBy string `json:"grantedBy"`
	// GrantedAt is when, RFC 3339 in UTC.
	GrantedAt string `json:"grantedAt"`
}

// folderList is a machine's active grants.
type folderList struct {
	// Folders are the folders the web may start runs in on this machine, oldest
	// grant first.
	Folders []folderView `json:"folders"`
}

// folderRevoked acknowledges a revoke.
type folderRevoked struct {
	// Revoked is true when an active grant was ended.
	Revoked bool `json:"revoked"`
	// Folder is the folder that may no longer be used.
	Folder string `json:"folder"`
}

func toFolderView(f Folder) folderView {
	return folderView{Folder: f.Path, GrantedBy: f.GrantedBy, GrantedAt: text.Unix(f.GrantedAt)}
}

// owned resolves a machine the caller reaches and requires them to be its
// owner: a machine they cannot reach is not found, one of a colleague's is 403.
func owned(ctx context.Context, sto *Store, org, id string) (Target, error) {
	t, err := sto.reachable(ctx, org, id)
	if err == errTargetNotFound {
		return Target{}, zip.ErrNotFound("target not found")
	}
	if err != nil {
		return Target{}, zip.Errorf(http.StatusInternalServerError, "get: %v", err)
	}
	me, err := self(ctx, org)
	if err != nil {
		return Target{}, err
	}
	if !owner(t, me) {
		return Target{}, zip.ErrForbidden("only the machine's owner may grant or revoke its folders")
	}
	return sto.carry(ctx, t), nil
}

// GrantFolder lets runs started from the web work in one folder of this
// machine and every folder under it. Only the machine's owner may grant, from
// the machine itself (the desktop or `hanzo link grant`); another member gets
// 403. Granting a folder already granted answers that grant with 200; a new one
// answers 201.
//
// Example: {"id": "tgt_1", "folder": "/home/me/site"}
func (o targetOps) grantFolder(ctx context.Context, in *folderIn) (*folderView, error) {
	sto, org, err := targetStore(ctx, &o.s.State)
	if err != nil {
		return nil, err
	}
	folder, err := cleanFolder(in.Folder)
	if err != nil {
		return nil, zip.ErrBadRequest(err.Error())
	}
	t, err := owned(ctx, sto, org, in.ID)
	if err != nil {
		return nil, err
	}
	f, created, err := sto.Grant(ctx, org, t.ID, folder, t.Owner, time.Now().Unix())
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "persist: %v", err)
	}
	if created {
		folderAudit(ctx, folderGrant, org, t.ID, folder, "", nil)
		cloud.Created(ctx)
	}
	v := toFolderView(f)
	return &v, nil
}

// ListFolders returns the folders of this machine the web may start runs in:
// the picker's choices. Anyone who can see the machine may read them.
//
// Example: {"id": "tgt_1"}
func (o targetOps) listFolders(ctx context.Context, in *targetRef) (*folderList, error) {
	sto, org, err := targetStore(ctx, &o.s.State)
	if err != nil {
		return nil, err
	}
	t, err := sto.reachable(ctx, org, in.ID)
	if err == errTargetNotFound {
		return nil, zip.ErrNotFound("target not found")
	}
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "get: %v", err)
	}
	active, err := sto.Folders(ctx, org, t.ID)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list: %v", err)
	}
	out := make([]folderView, 0, len(active))
	for _, f := range active {
		out = append(out, toFolderView(f))
	}
	return &folderList{Folders: out}, nil
}

// RevokeFolder ends a folder's grant on this machine. Only the machine's owner
// may revoke, from the machine or the web. The machine reads its folders on
// every heartbeat and drops this one, and a queued run in it is refused when
// the machine claims it. A folder with no active grant is 404.
//
// Example: {"id": "tgt_1", "folder": "/home/me/site"}
func (o targetOps) revokeFolder(ctx context.Context, in *folderIn) (*folderRevoked, error) {
	sto, org, err := targetStore(ctx, &o.s.State)
	if err != nil {
		return nil, err
	}
	folder, err := cleanFolder(in.Folder)
	if err != nil {
		return nil, zip.ErrBadRequest(err.Error())
	}
	t, err := owned(ctx, sto, org, in.ID)
	if err != nil {
		return nil, err
	}
	revoked, err := sto.Revoke(ctx, org, t.ID, folder, time.Now().Unix())
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "revoke: %v", err)
	}
	if !revoked {
		return nil, zip.ErrNotFound("folder not granted")
	}
	folderAudit(ctx, folderRevoke, org, t.ID, folder, "", nil)
	return &folderRevoked{Revoked: true, Folder: folder}, nil
}
