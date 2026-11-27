package agents

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/hanzoai/cloud/sdk/dialect"
)

// An A2A task, as this server keeps it.
//
// A task is one message the caller sent and the run that answered it. The run is
// the agent run every other door records (agent_runs, billed and traced as one),
// so the task row holds only what A2A asks of a task that a run does not have: its
// state while it is still working, the conversation (contextId) it belongs to, the
// caller's own message id, and a cancel a run cannot record. RunID joins the two
// once the run has happened.
//
// A task is the person's who sent it (Actor, billingActor's org/sub), exactly as a
// run is: another member of the org reads none of it, and neither does an admin.
type taskRow struct {
	ID        string
	Org       string
	Actor     string
	ContextID string
	MessageID string
	State     string
	Input     string
	Output    string
	Error     string
	RunID     string
	CreatedAt int64 // unix milliseconds
	UpdatedAt int64 // unix milliseconds: the status timestamp A2A sorts by
}

const taskCols = `id, org, actor, context_id, message_id, state, input, output, error, run_id, created_at, updated_at`

func scanTask(sc interface{ Scan(...any) error }) (taskRow, error) {
	var t taskRow
	if err := sc.Scan(&t.ID, &t.Org, &t.Actor, &t.ContextID, &t.MessageID, &t.State,
		&t.Input, &t.Output, &t.Error, &t.RunID, &t.CreatedAt, &t.UpdatedAt); err != nil {
		return taskRow{}, fmt.Errorf("scan task: %w", err)
	}
	return t, nil
}

// migrateA2A creates the task table in the org's own file, beside the runs it
// points at.
func (s *Store) migrateA2A(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS a2a_tasks (
		id         TEXT PRIMARY KEY,
		org        TEXT NOT NULL,
		actor      TEXT NOT NULL,
		context_id TEXT NOT NULL,
		message_id TEXT NOT NULL DEFAULT '',
		state      TEXT NOT NULL,
		input      TEXT NOT NULL DEFAULT '',
		output     TEXT NOT NULL DEFAULT '',
		error      TEXT NOT NULL DEFAULT '',
		run_id     TEXT NOT NULL DEFAULT '',
		created_at BIGINT NOT NULL,
		updated_at BIGINT NOT NULL
	);
	CREATE INDEX IF NOT EXISTS ix_a2a_tasks_actor_updated ON a2a_tasks(org, actor, updated_at);
	CREATE INDEX IF NOT EXISTS ix_a2a_tasks_actor_context ON a2a_tasks(org, actor, context_id, created_at);`); err != nil {
		return fmt.Errorf("migrate: a2a tasks: %w", err)
	}
	return nil
}

// InsertTask records a task as it starts.
func (s *Store) InsertTask(ctx context.Context, t taskRow) error {
	if err := dialect.Text(t.ID, t.Org, t.Actor, t.ContextID, t.MessageID, t.State, t.Input, t.Output, t.Error, t.RunID); err != nil {
		return fmt.Errorf("insert task: %w", err)
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO a2a_tasks (`+taskCols+`) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`,
		t.ID, t.Org, t.Actor, t.ContextID, t.MessageID, t.State, t.Input, t.Output, t.Error, t.RunID, t.CreatedAt, t.UpdatedAt)
	if err != nil {
		return fmt.Errorf("insert task: %w", err)
	}
	return nil
}

// FinishTask writes a working task's outcome. It changes nothing once the task
// has left the working state, so a task canceled while its run was still going
// stays canceled: the cancel is what the caller was told.
func (s *Store) FinishTask(ctx context.Context, org, id, state, output, errMsg, runID string, at int64) (bool, error) {
	if err := dialect.Text(org, id, state, output, errMsg, runID); err != nil {
		return false, fmt.Errorf("finish task: %w", err)
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE a2a_tasks SET state=$1, output=$2, error=$3, run_id=$4, updated_at=$5 WHERE org=$6 AND id=$7 AND state=$8`,
		state, output, errMsg, runID, at, org, id, taskWorking)
	if err != nil {
		return false, fmt.Errorf("finish task: %w", err)
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// CancelTask moves the actor's working task to canceled, and reports whether it
// was working to be moved.
func (s *Store) CancelTask(ctx context.Context, org, actor, id string, at int64) (bool, error) {
	if err := dialect.Text(org, actor, id); err != nil {
		return false, fmt.Errorf("cancel task: %w", err)
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE a2a_tasks SET state=$1, updated_at=$2 WHERE org=$3 AND actor=$4 AND id=$5 AND state=$6`,
		taskCanceled, at, org, actor, id, taskWorking)
	if err != nil {
		return false, fmt.Errorf("cancel task: %w", err)
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// Task reads one of the actor's tasks. Another person's task is errNotFound, the
// same answer as one that does not exist, so a task id reveals nothing about
// whether someone else holds it (A2A §3.3.2).
func (s *Store) Task(ctx context.Context, org, actor, id string) (taskRow, error) {
	t, err := scanTask(s.db.QueryRowContext(ctx,
		`SELECT `+taskCols+` FROM a2a_tasks WHERE org=$1 AND actor=$2 AND id=$3`, org, actor, id))
	if errors.Is(err, sql.ErrNoRows) {
		return taskRow{}, errNotFound
	}
	return t, err
}

// ContextTurns are the actor's answered turns in one conversation, oldest first:
// the newest limit of them, which is the history the next message is sent with.
func (s *Store) ContextTurns(ctx context.Context, org, actor, contextID string, limit int) ([]taskRow, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+taskCols+` FROM a2a_tasks WHERE org=$1 AND actor=$2 AND context_id=$3 AND state=$4
		 ORDER BY created_at DESC LIMIT $5`, org, actor, contextID, taskCompleted, limit)
	if err != nil {
		return nil, fmt.Errorf("context turns: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []taskRow
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
}

// taskFilter is what ListTasks narrows by. Zero values narrow nothing.
type taskFilter struct {
	contextID string
	state     string
	after     int64 // status timestamp, unix ms, exclusive
	size      int
	cursor    string // the page token: where the previous page stopped
}

// ListTasks is a page of the actor's tasks, newest status first (A2A §3.1.4), the
// count of every task the filter matches, and the token for the next page ("" on
// the last).
func (s *Store) ListTasks(ctx context.Context, org, actor string, f taskFilter) ([]taskRow, int, string, error) {
	where := ` WHERE org=$1 AND actor=$2`
	args := []any{org, actor}
	if f.contextID != "" {
		args = append(args, f.contextID)
		where += ` AND context_id=$` + strconv.Itoa(len(args))
	}
	if f.state != "" {
		args = append(args, f.state)
		where += ` AND state=$` + strconv.Itoa(len(args))
	}
	if f.after > 0 {
		args = append(args, f.after)
		where += ` AND updated_at>$` + strconv.Itoa(len(args))
	}
	var total int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM a2a_tasks`+where, args...).Scan(&total); err != nil {
		return nil, 0, "", fmt.Errorf("count tasks: %w", err)
	}
	page, pageArgs := where, append([]any(nil), args...)
	if f.cursor != "" {
		at, id, ok := parseTaskCursor(f.cursor)
		if !ok {
			return nil, 0, "", errBadCursor
		}
		n := len(pageArgs)
		pageArgs = append(pageArgs, at, at, id)
		page += fmt.Sprintf(` AND (updated_at<$%d OR (updated_at=$%d AND id<$%d))`, n+1, n+2, n+3)
	}
	pageArgs = append(pageArgs, f.size+1)
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+taskCols+` FROM a2a_tasks`+page+` ORDER BY updated_at DESC, id DESC LIMIT $`+strconv.Itoa(len(pageArgs)),
		pageArgs...)
	if err != nil {
		return nil, 0, "", fmt.Errorf("list tasks: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []taskRow
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, 0, "", err
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, "", err
	}
	next := ""
	if len(out) > f.size {
		out = out[:f.size]
		last := out[len(out)-1]
		next = taskCursor(last.UpdatedAt, last.ID)
	}
	return out, total, next, nil
}

// errBadCursor is a page token this server did not write.
var errBadCursor = errors.New("a2a: page token is not one this server wrote")

// taskCursor is the page token: the last row's status timestamp and id, opaque to
// the caller.
func taskCursor(at int64, id string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatInt(at, 10) + "|" + id))
}

func parseTaskCursor(tok string) (int64, string, bool) {
	b, err := base64.RawURLEncoding.DecodeString(tok)
	if err != nil {
		return 0, "", false
	}
	at, id, ok := strings.Cut(string(b), "|")
	if !ok || id == "" {
		return 0, "", false
	}
	n, err := strconv.ParseInt(at, 10, 64)
	if err != nil {
		return 0, "", false
	}
	return n, id, true
}
