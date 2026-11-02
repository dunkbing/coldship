package engine

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/dunkbing/coldship/internal/hostinfo"
	"github.com/dunkbing/coldship/internal/store"
)

// The watcher checks apps and the disk every minute, and emits events when
// something changes: an app stops or starts running, the disk fills up.

type watchState struct {
	mu       sync.Mutex
	started  bool
	apps     map[string]*appWatch
	diskLow  bool
	diskSent time.Time
}

type appWatch struct {
	down     bool // as last reported
	bad      int  // consecutive bad checks
	good     int  // consecutive good checks
	restarts int  // restart count at the last check
}

// Thresholds for disk.low: alert at 90% used, clear below 85%, and repeat
// at most daily.
const (
	diskAlert   = 0.90
	diskClear   = 0.85
	diskRepeat  = 24 * time.Hour
	watchPeriod = time.Minute
)

// RunWatch checks apps and the disk until ctx ends.
func (e *Engine) RunWatch(ctx context.Context) {
	t := time.NewTicker(watchPeriod)
	defer t.Stop()
	for {
		e.watchOnce(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// disks returns the filesystems to watch; tests swap it.
func (e *Engine) disks() []hostinfo.Disk {
	if e.DiskInfo != nil {
		return e.DiskInfo()
	}
	paths := map[string]string{}
	if e.DataDir != "" {
		paths[e.DataDir] = "Coldship's data"
	}
	return hostinfo.Read(0, paths).Disks
}

func (e *Engine) watchOnce(ctx context.Context) {
	w := &e.watch
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.apps == nil {
		w.apps = map[string]*appWatch{}
	}
	// The first pass after a start only records how things are, so a
	// restart doesn't announce every app.
	baseline := !w.started
	w.started = true

	apps, err := e.st.ListApps(ctx)
	if err == nil {
		seen := map[string]bool{}
		for i := range apps {
			a := &apps[i]
			if a.Spec == nil || a.CurrentDeployment == "" || a.PreviewOf != "" || e.isStatic(ctx, a) {
				continue
			}
			seen[a.Name] = true
			st, err := e.rt.Status(ctx, a.Name)
			if err != nil {
				continue // can't tell; don't guess
			}
			aw := w.apps[a.Name]
			if aw == nil {
				aw = &appWatch{}
				w.apps[a.Name] = aw
			}
			// A crash-looping app can look running between restarts: a
			// growing restart count is bad too.
			restarted := st.RestartCount > aw.restarts
			aw.restarts = st.RestartCount
			if baseline {
				aw.down = !st.Running
				continue
			}
			if e.appBusy(a.Name) {
				continue // a deploy is replacing its container
			}
			if st.Running && !restarted {
				aw.bad = 0
				// Two good checks, so a flapping app doesn't recover every minute.
				if aw.good++; aw.down && aw.good >= 2 {
					aw.down = false
					e.emit(ctx, Event{Kind: EvAppRecovered, App: a.Name, Severity: "ok", Title: a.Name + " is running again"})
				}
				continue
			}
			aw.good = 0
			if aw.bad++; aw.bad >= 2 && !aw.down {
				aw.down = true
				detail := "Its container " + containerState(st.State, st.Exists)
				switch {
				case st.OOMKilled:
					detail += " (killed: out of memory)"
				case st.Exists && st.ExitCode != 0:
					detail += fmt.Sprintf(" (exit code %d)", st.ExitCode)
				}
				if st.Running {
					detail = "It keeps crashing and restarting"
				}
				if st.RestartCount > 0 {
					detail += fmt.Sprintf(" (%d restarts so far)", st.RestartCount)
				}
				e.emit(ctx, Event{Kind: EvAppDown, App: a.Name, Severity: "error", Title: a.Name + " is down",
					Detail: detail + ". See what's wrong: coldship diagnose " + a.Name})
			}
		}
		for name := range w.apps {
			if !seen[name] {
				delete(w.apps, name) // deleted or now a static site
			}
		}
	}

	var worst *hostinfo.Disk
	for _, d := range e.disks() {
		if d.Total > 0 && (worst == nil || frac(d) > frac(*worst)) {
			d := d
			worst = &d
		}
	}
	if worst == nil {
		return
	}
	used := frac(*worst)
	switch {
	case used >= diskAlert && (!w.diskLow || e.Now().Sub(w.diskSent) >= diskRepeat):
		// Not baselined: a full disk is a standing problem, and restarts
		// mustn't keep postponing the alert.
		w.diskLow, w.diskSent = true, e.Now()
		e.emit(ctx, Event{Kind: EvDiskLow, Org: store.DefaultOrg, Severity: "warning",
			Title: fmt.Sprintf("The server's disk is %.0f%% full", used*100),
			Detail: fmt.Sprintf("%s (%s) has %s free of %s. Remove unused images (docker image prune) or old snapshots, or grow the disk.",
				worst.Path, strings.ToLower(worst.For), humanBytes(worst.Total-worst.Used), humanBytes(worst.Total))})
	case used < diskClear:
		w.diskLow = false
	}
}

func frac(d hostinfo.Disk) float64 { return float64(d.Used) / float64(d.Total) }

func containerState(state string, exists bool) string {
	switch {
	case !exists:
		return "is gone"
	case state == "exited" || state == "dead":
		return "has " + map[string]string{"exited": "exited", "dead": "died"}[state]
	case state == "":
		return "isn't running"
	}
	return "is " + state
}

func humanBytes(n uint64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := uint64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

// appBusy reports whether a change to app is in progress.
func (e *Engine) appBusy(app string) bool {
	e.locksMu.Lock()
	m := e.locks[app]
	e.locksMu.Unlock()
	if m == nil {
		return false
	}
	if m.TryLock() {
		m.Unlock()
		return false
	}
	return true
}
