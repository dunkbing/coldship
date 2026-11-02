package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Job run statuses.
const (
	JobRunning     = "running"
	JobSucceeded   = "succeeded"
	JobFailed      = "failed"
	JobTimedOut    = "timed_out"
	JobInterrupted = "interrupted"
	JobSkipped     = "skipped" // the previous run was still going
)

// JobRun is one run of an app's scheduled job.
type JobRun struct {
	ID         string     `json:"id"`
	Org        string     `json:"org"`
	App        string     `json:"app"`
	Job        string     `json:"job"`
	Trigger    string     `json:"trigger"` // "schedule", or who ran it
	Command    string     `json:"command"`
	Status     string     `json:"status"`
	ExitCode   *int       `json:"exit_code,omitempty"`
	Error      string     `json:"error,omitempty"`
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	Log        string     `json:"log,omitempty"`
}

func (s *Store) SaveJobRun(ctx context.Context, r *JobRun) error {
	var exit sql.NullInt64
	if r.ExitCode != nil {
		exit = sql.NullInt64{Int64: int64(*r.ExitCode), Valid: true}
	}
	fin := ""
	if r.FinishedAt != nil {
		fin = ts(*r.FinishedAt)
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO job_runs(id, org, app, job, trigger, command, status, exit_code, error, started_at, finished_at, log)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET status=excluded.status, exit_code=excluded.exit_code, error=excluded.error,
		finished_at=excluded.finished_at, log=excluded.log`,
		r.ID, r.Org, r.App, r.Job, r.Trigger, r.Command, r.Status, exit, r.Error, ts(r.StartedAt), fin, r.Log)
	return err
}

const jobRunCols = `id, org, app, job, trigger, command, status, exit_code, error, started_at, finished_at`

func scanJobRun(r scanner, withLog bool) (*JobRun, error) {
	var j JobRun
	var exit sql.NullInt64
	var started, fin string
	dest := []any{&j.ID, &j.Org, &j.App, &j.Job, &j.Trigger, &j.Command, &j.Status, &exit, &j.Error, &started, &fin}
	if withLog {
		dest = append(dest, &j.Log)
	}
	if err := r.Scan(dest...); err != nil {
		return nil, err
	}
	if exit.Valid {
		c := int(exit.Int64)
		j.ExitCode = &c
	}
	j.StartedAt, _ = time.Parse(tsFormat, started)
	if t, err := time.Parse(tsFormat, fin); err == nil {
		j.FinishedAt = &t
	}
	return &j, nil
}

// GetJobRun returns a run with its log.
func (s *Store) GetJobRun(ctx context.Context, id string) (*JobRun, error) {
	j, err := scanJobRun(s.db.QueryRowContext(ctx, `SELECT `+jobRunCols+`, log FROM job_runs WHERE id = ?`, id), true)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return j, err
}

// ListJobRuns lists an app's runs (of one job, or all with job ""),
// newest first, without logs.
func (s *Store) ListJobRuns(ctx context.Context, app, job string, limit int) ([]JobRun, error) {
	q, args := `SELECT `+jobRunCols+` FROM job_runs WHERE app = ?`, []any{app}
	if job != "" {
		q += ` AND job = ?`
		args = append(args, job)
	}
	q += ` ORDER BY started_at DESC, rowid DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []JobRun{}
	for rows.Next() {
		j, err := scanJobRun(rows, false)
		if err != nil {
			return nil, err
		}
		out = append(out, *j)
	}
	return out, rows.Err()
}

// PruneJobRuns keeps the newest keep runs of a job.
func (s *Store) PruneJobRuns(ctx context.Context, app, job string, keep int) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM job_runs WHERE app = ? AND job = ? AND id NOT IN
		(SELECT id FROM job_runs WHERE app = ? AND job = ? ORDER BY started_at DESC, rowid DESC LIMIT ?)`, app, job, app, job, keep)
	return err
}

func (s *Store) DeleteJobRuns(ctx context.Context, app string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM job_runs WHERE app = ?`, app)
	return err
}

// InterruptJobRuns marks runs left running (by a restart) as interrupted.
func (s *Store) InterruptJobRuns(ctx context.Context, at time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE job_runs SET status = ?, error = ?, finished_at = ? WHERE status = ?`,
		JobInterrupted, "the server restarted while it ran", ts(at), JobRunning)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
