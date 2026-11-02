package engine

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/dunkbing/coldship/internal/apierr"
	"github.com/dunkbing/coldship/internal/runtime"
	"github.com/dunkbing/coldship/internal/spec"
	"github.com/dunkbing/coldship/internal/store"
)

// Scheduled jobs: each runs in a one-off container from the app's current
// image, with its env, databases and volumes. Missed runs (the server was
// down) aren't caught up, and a job never overlaps its own last run.

const (
	maxJobLog      = 64 << 10
	jobRunsKept    = 50
	maxJobsRunning = 4
)

type jobsState struct {
	mu      sync.Mutex
	running map[string]string    // app/job -> run id
	next    map[string]time.Time // app/job|schedule -> next run
	failing map[string]bool      // app/job whose last run failed
	sem     chan struct{}
	wg      sync.WaitGroup
}

func (e *Engine) jobState() *jobsState {
	e.jobsOnce.Do(func() {
		e.js = &jobsState{running: map[string]string{}, next: map[string]time.Time{}, failing: map[string]bool{}, sem: make(chan struct{}, maxJobsRunning)}
	})
	return e.js
}

// JobsRunning counts running jobs (auto-upgrade waits for them).
func (e *Engine) JobsRunning() int {
	js := e.jobState()
	js.mu.Lock()
	defer js.mu.Unlock()
	return len(js.running)
}

// RunJobs runs scheduled jobs until ctx ends.
func (e *Engine) RunJobs(ctx context.Context, log *slog.Logger) {
	if n, err := e.st.InterruptJobRuns(ctx, e.Now()); err != nil {
		log.Warn("marking interrupted jobs failed", "err", err)
	} else if n > 0 {
		log.Info("jobs were interrupted by a restart", "runs", n)
	}
	if err := e.rt.RemoveJobs(ctx, ""); err != nil {
		log.Debug("removing leftover job containers failed", "err", err)
	}
	t := time.NewTicker(15 * time.Second)
	defer t.Stop()
	for {
		e.scheduleJobs(ctx, log)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// scheduleJobs starts the jobs that are due.
func (e *Engine) scheduleJobs(ctx context.Context, log *slog.Logger) {
	apps, err := e.st.ListApps(ctx)
	if err != nil {
		log.Warn("listing apps for jobs failed", "err", err)
		return
	}
	now := e.Now()
	js := e.jobState()
	seen := map[string]bool{}
	var due []struct {
		app store.App
		job spec.Job
	}
	js.mu.Lock()
	for _, a := range apps {
		if a.Spec == nil || a.PreviewOf != "" || a.CurrentDeployment == "" {
			continue
		}
		for _, j := range a.Spec.Jobs {
			sched, err := spec.ParseSchedule(j.Schedule)
			if err != nil {
				continue
			}
			key := a.Name + "/" + j.Name + "|" + j.Schedule
			seen[key] = true
			next, ok := js.next[key]
			if !ok {
				js.next[key] = sched.Next(now)
				continue
			}
			if now.Before(next) {
				continue
			}
			js.next[key] = sched.Next(now)
			due = append(due, struct {
				app store.App
				job spec.Job
			}{a, j})
		}
	}
	for k := range js.next {
		if !seen[k] {
			delete(js.next, k)
		}
	}
	js.mu.Unlock()
	for _, d := range due {
		if _, err := e.startJob(ctx, &d.app, d.job, "schedule"); err != nil {
			log.Warn("starting a job failed", "app", d.app.Name, "job", d.job.Name, "err", err)
		}
	}
}

// NextJobRun is when a job runs next (zero if unknown yet).
func (e *Engine) nextJobRun(app string, j spec.Job) time.Time {
	js := e.jobState()
	js.mu.Lock()
	defer js.mu.Unlock()
	if t, ok := js.next[app+"/"+j.Name+"|"+j.Schedule]; ok {
		return t
	}
	if s, err := spec.ParseSchedule(j.Schedule); err == nil {
		return s.Next(e.Now())
	}
	return time.Time{}
}

// RunJob runs an app's job now, for a person or agent.
func (e *Engine) RunJob(ctx context.Context, actor, appName, job string) (*store.JobRun, error) {
	a, err := e.st.GetApp(ctx, appName)
	if err != nil {
		return nil, err
	}
	if a == nil || a.Spec == nil {
		return nil, apierr.NotFound("app", appName)
	}
	for _, j := range a.Spec.Jobs {
		if j.Name == job {
			r, err := e.startJob(ctx, a, j, actor)
			if err == nil {
				e.audit(ctx, actor, "job.run", appName, "", map[string]any{"job": job, "run": r.ID})
			}
			return r, err
		}
	}
	return nil, apierr.NotFound("job", job).WithHint("list the app's jobs: coldship jobs " + appName)
}

// startJob records a run and starts it in the background.
func (e *Engine) startJob(ctx context.Context, a *store.App, j spec.Job, trigger string) (*store.JobRun, error) {
	ctx = context.WithoutCancel(ctx)
	js := e.jobState()
	key := a.Name + "/" + j.Name
	r := &store.JobRun{ID: store.NewID("run"), Org: store.OrgOr(a.Org), App: a.Name, Job: j.Name, Trigger: trigger, Command: j.Command,
		Status: store.JobRunning, StartedAt: e.Now()}
	js.mu.Lock()
	if prev := js.running[key]; prev != "" {
		js.mu.Unlock()
		if trigger != "schedule" {
			return nil, apierr.New(409, "job_running", "%s is still running (%s)", j.Name, prev).WithHint("wait for it, or see: coldship jobs logs " + a.Name + " " + j.Name)
		}
		fin := r.StartedAt
		r.Status, r.Error, r.FinishedAt = store.JobSkipped, "the previous run ("+prev+") was still going", &fin
		return r, e.st.SaveJobRun(ctx, r)
	}
	jspec, err := e.jobSpec(ctx, a, j, r.ID)
	if err != nil {
		js.mu.Unlock()
		if trigger != "schedule" {
			return nil, err
		}
		fin := r.StartedAt
		r.Status, r.Error, r.FinishedAt = store.JobFailed, err.Error(), &fin
		e.finishJob(ctx, r)
		return r, nil
	}
	js.running[key] = r.ID
	js.wg.Add(1)
	js.mu.Unlock()
	if err := e.st.SaveJobRun(ctx, r); err != nil {
		js.mu.Lock()
		delete(js.running, key)
		js.mu.Unlock()
		js.wg.Done()
		return nil, err
	}
	started := *r // the goroutine keeps changing r
	go func() {
		defer js.wg.Done()
		defer func() {
			js.mu.Lock()
			delete(js.running, key)
			js.mu.Unlock()
		}()
		js.sem <- struct{}{}
		defer func() { <-js.sem }()
		out := &tailBuffer{max: maxJobLog}
		code, err := e.rt.RunJob(ctx, jspec, out)
		fin := e.Now()
		r.FinishedAt, r.Log = &fin, out.String()
		switch {
		case errors.Is(err, runtime.ErrJobTimeout):
			r.Status, r.Error = store.JobTimedOut, fmt.Sprintf("stopped after %s", j.JobTimeout())
		case err != nil:
			r.Status, r.Error = store.JobFailed, err.Error()
		case code != 0:
			r.Status, r.Error = store.JobFailed, fmt.Sprintf("exited with code %d", code)
			r.ExitCode = &code
		default:
			r.Status, r.ExitCode = store.JobSucceeded, &code
		}
		e.finishJob(ctx, r)
	}()
	return &started, nil
}

// finishJob saves a finished run and tells people when a job starts or
// stops failing.
func (e *Engine) finishJob(ctx context.Context, r *store.JobRun) {
	if err := e.st.SaveJobRun(ctx, r); err != nil {
		slog.Warn("saving a job run failed", "run", r.ID, "err", err)
	}
	_ = e.st.PruneJobRuns(ctx, r.App, r.Job, jobRunsKept)
	js := e.jobState()
	key := r.App + "/" + r.Job
	ok := r.Status == store.JobSucceeded
	js.mu.Lock()
	was := js.failing[key]
	js.failing[key] = !ok
	js.mu.Unlock()
	actor := ""
	if r.Trigger != "schedule" {
		actor = r.Trigger
	}
	switch {
	case !ok && !was:
		detail := r.Error
		if r.Log != "" {
			detail += "\n" + lastLines(r.Log, 15)
		}
		e.emit(ctx, Event{Kind: EvJobFailed, Org: r.Org, App: r.App, Actor: actor, Severity: "error",
			Title:  fmt.Sprintf("%s: job %s failed", r.App, r.Job),
			Detail: clipText(detail+"\nLog: coldship jobs logs "+r.App+" "+r.Job, 1500)})
	case ok && was:
		e.emit(ctx, Event{Kind: EvJobRecovered, Org: r.Org, App: r.App, Actor: actor, Severity: "ok",
			Title: fmt.Sprintf("%s: job %s works again", r.App, r.Job)})
	}
}

// jobSpec resolves what a run needs: the image that's deployed and the
// env the app gets, databases included, without starting anything.
func (e *Engine) jobSpec(ctx context.Context, a *store.App, j spec.Job, runID string) (runtime.JobSpec, error) {
	d, err := e.st.GetDeployment(ctx, a.CurrentDeployment)
	if err != nil {
		return runtime.JobSpec{}, err
	}
	if d == nil {
		return runtime.JobSpec{}, apierr.New(409, "app_not_deployed", "%s isn't deployed, so its jobs can't run", a.Name)
	}
	if d.Static {
		return runtime.JobSpec{}, apierr.New(409, "static_app", "%s is served as static files, with no image to run jobs in", a.Name).
			WithHint("jobs need an app that runs in a container")
	}
	image := d.Image
	if image == "" {
		image = a.Spec.Image
	}
	env, err := e.appEnv(ctx, a.Spec)
	if err != nil {
		return runtime.JobSpec{}, err
	}
	s := runtime.JobSpec{App: a.Name, Job: j.Name, RunID: runID, Image: image, Command: j.Command, Memory: a.Spec.Memory, Timeout: j.JobTimeout()}
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		s.Env = append(s.Env, k+"="+env[k])
	}
	for _, v := range a.Spec.Volumes {
		s.Binds = append(s.Binds, runtime.VolumeName(a.Name, v.Name)+":"+v.Path)
	}
	return s, nil
}

// appEnv is the env an app runs with: its own, plus its databases' and
// data services' connection settings.
func (e *Engine) appEnv(ctx context.Context, a *spec.App) (map[string]string, error) {
	env := map[string]string{}
	for k, v := range a.Env {
		env[k] = v
	}
	for _, d := range a.Databases {
		db, err := e.dbCreds(ctx, a.Name, d)
		if err != nil {
			return nil, err
		}
		env[d.Env] = db.URL()
	}
	for _, u := range a.Uses {
		name, override := spec.ParseUse(u)
		db, err := e.serviceDB(ctx, name)
		if err != nil {
			return nil, err
		}
		for k, v := range envFor(db, override) {
			env[k] = v
		}
	}
	return env, nil
}

// JobView is a job with when it runs next and how it last went.
type JobView struct {
	spec.Job
	Next    *time.Time    `json:"next,omitempty"`
	Running string        `json:"running,omitempty"` // the run in progress
	Last    *store.JobRun `json:"last,omitempty"`
}

// Jobs lists an app's jobs.
func (e *Engine) Jobs(ctx context.Context, appName string) ([]JobView, error) {
	a, err := e.st.GetApp(ctx, appName)
	if err != nil {
		return nil, err
	}
	if a == nil || a.Spec == nil {
		return nil, apierr.NotFound("app", appName)
	}
	runs, err := e.st.ListJobRuns(ctx, appName, "", 200)
	if err != nil {
		return nil, err
	}
	js := e.jobState()
	out := []JobView{}
	for _, j := range a.Spec.Jobs {
		v := JobView{Job: j}
		if a.PreviewOf == "" {
			if n := e.nextJobRun(a.Name, j); !n.IsZero() {
				v.Next = &n
			}
		}
		js.mu.Lock()
		v.Running = js.running[a.Name+"/"+j.Name]
		js.mu.Unlock()
		for i := range runs {
			if runs[i].Job == j.Name && runs[i].Status != store.JobRunning {
				r := runs[i]
				v.Last = &r
				break
			}
		}
		out = append(out, v)
	}
	return out, nil
}

// JobRuns lists an app's runs, of one job or all.
func (e *Engine) JobRuns(ctx context.Context, appName, job string) ([]store.JobRun, error) {
	return e.st.ListJobRuns(ctx, appName, job, jobRunsKept)
}

// JobRun returns one of an app's runs, with its log.
func (e *Engine) JobRun(ctx context.Context, appName, id string) (*store.JobRun, error) {
	r, err := e.st.GetJobRun(ctx, id)
	if err != nil {
		return nil, err
	}
	if r == nil || r.App != appName {
		return nil, apierr.NotFound("job_run", id)
	}
	return r, nil
}

// tailBuffer keeps the last max bytes written to it.
type tailBuffer struct {
	mu   sync.Mutex
	b    []byte
	max  int
	lost bool
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.b = append(t.b, p...)
	if len(t.b) > t.max {
		t.b = append([]byte(nil), t.b[len(t.b)-t.max:]...)
		t.lost = true
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.lost {
		return "… (earlier output cut)\n" + string(t.b)
	}
	return string(t.b)
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
