package main

import (
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/dunkbing/coldship/internal/apierr"
	"github.com/dunkbing/coldship/internal/engine"
	"github.com/dunkbing/coldship/internal/planner"
	"github.com/dunkbing/coldship/internal/spec"
	"github.com/dunkbing/coldship/internal/store"
)

func jobsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "jobs <app>",
		Short: "Scheduled jobs: commands run on a cron schedule",
		Long: `Scheduled jobs run a command on a cron schedule (UTC), in a one-off
container from the app's current image, with its env, databases and volumes.

  coldship jobs set web cleanup --schedule '0 3 * * *' -- npm run cleanup
  coldship jobs web                  # list, with next and last runs
  coldship jobs run web cleanup      # run now
  coldship jobs logs web cleanup     # the last run's output
  coldship jobs rm web cleanup

Changing jobs doesn't redeploy the app.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			jobs, err := newClient().Jobs(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			return emit(jobs, func() { printJobs(args[0], jobs) })
		},
	}

	var schedule, command, timeout, reason string
	set := &cobra.Command{
		Use:   "set <app> <job> --schedule CRON [--command CMD | -- CMD...]",
		Short: "Add a job, or change one",
		Long: `Add a job, or change one. The schedule is 5-field cron in UTC
(minute hour day month weekday), or @hourly, @daily, @weekly, @monthly:

  coldship jobs set web cleanup --schedule '0 3 * * *' -- npm run cleanup
  coldship jobs set web sync --schedule '*/15 * * * *' --command './bin/sync --quiet' --timeout 5m

The command runs with sh -c, so the image needs a shell.`,
		Args: cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			app, name := args[0], args[1]
			if rest := args[2:]; len(rest) > 0 {
				if command != "" {
					return apierr.Invalid("command", "give the command once: with --command, or after --")
				}
				command = strings.Join(rest, " ")
			}
			j := spec.Job{Name: name, Schedule: schedule, Command: command, Timeout: timeout}
			// Flags not given keep the job's current settings.
			if cur, err := newClient().Jobs(cmd.Context(), app); err == nil {
				for _, c := range cur {
					if c.Name == name {
						if !cmd.Flags().Changed("schedule") {
							j.Schedule = c.Schedule
						}
						if j.Command == "" {
							j.Command = c.Command
						}
						if !cmd.Flags().Changed("timeout") {
							j.Timeout = c.Timeout
						}
					}
				}
			}
			if j.Schedule == "" {
				return apierr.Invalid("schedule", "give a --schedule, like '0 3 * * *' (03:00 UTC daily)")
			}
			if j.Command == "" {
				return apierr.Invalid("command", "give the command after --, like: coldship jobs set %s %s --schedule '%s' -- npm run %s", app, name, j.Schedule, name)
			}
			return applyJobs(cmd, &spec.Patch{Name: app, SetJobs: []spec.Job{j}}, orDefault(reason, "set job "+name))
		},
	}
	set.Flags().StringVar(&schedule, "schedule", "", "when to run, in UTC: cron like '0 3 * * *', or @hourly, @daily, @weekly, @monthly")
	set.Flags().StringVar(&command, "command", "", "the command (or give it after --)")
	set.Flags().StringVar(&timeout, "timeout", "", "stop it after this long, like 10m (default 30m)")
	set.Flags().StringVar(&reason, "reason", "", "why, for the audit log")

	var rmReason string
	rm := &cobra.Command{
		Use:   "rm <app> <job>...",
		Short: "Remove jobs",
		Args:  cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return applyJobs(cmd, &spec.Patch{Name: args[0], RemoveJobs: args[1:]}, orDefault(rmReason, "remove job "+strings.Join(args[1:], ", ")))
		},
	}
	rm.Flags().StringVar(&rmReason, "reason", "", "why, for the audit log")

	var wait bool
	run := &cobra.Command{
		Use:   "run <app> <job>",
		Short: "Run a job now",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			c := newClient()
			r, err := c.RunJob(cmd.Context(), args[0], args[1])
			if err != nil {
				return err
			}
			if !wait {
				return emit(r, func() {
					fmt.Printf("Started %s (%s). See its output: coldship jobs logs %s %s\n", args[1], r.ID, args[0], args[1])
				})
			}
			for r.Status == store.JobRunning {
				select {
				case <-cmd.Context().Done():
					return cmd.Context().Err()
				case <-time.After(time.Second):
				}
				if r, err = c.JobRun(cmd.Context(), args[0], r.ID); err != nil {
					return err
				}
			}
			if err := emit(r, func() { printRun(r) }); err != nil {
				return err
			}
			if r.Status != store.JobSucceeded {
				return apierr.New(422, "job_failed", "%s %s: %s", args[1], strings.ReplaceAll(r.Status, "_", " "), r.Error)
			}
			return nil
		},
	}
	run.Flags().BoolVar(&wait, "wait", false, "wait for it to finish and print its output (exits non-zero if it fails)")

	logs := &cobra.Command{
		Use:   "logs <app> <job> [run]",
		Short: "Show a run's output (the last run by default)",
		Args:  cobra.RangeArgs(2, 3),
		RunE: func(cmd *cobra.Command, args []string) error {
			c := newClient()
			id := ""
			if len(args) == 3 {
				id = args[2]
			} else {
				runs, err := c.JobRuns(cmd.Context(), args[0], args[1])
				if err != nil {
					return err
				}
				if len(runs) == 0 {
					return apierr.New(404, "no_runs", "%s hasn't run yet", args[1]).WithHint("run it now: coldship jobs run " + args[0] + " " + args[1])
				}
				id = runs[0].ID
			}
			r, err := c.JobRun(cmd.Context(), args[0], id)
			if err != nil {
				return err
			}
			return emit(r, func() { printRun(r) })
		},
	}

	history := &cobra.Command{
		Use:   "runs <app> [job]",
		Short: "List recent runs",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			job := ""
			if len(args) == 2 {
				job = args[1]
			}
			runs, err := newClient().JobRuns(cmd.Context(), args[0], job)
			if err != nil {
				return err
			}
			return emit(runs, func() {
				if len(runs) == 0 {
					fmt.Println("No runs yet.")
					return
				}
				w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
				fmt.Fprintln(w, "RUN\tJOB\tSTATUS\tSTARTED\tTOOK\tBY")
				for _, r := range runs {
					fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", r.ID, r.Job, runStatus(&r), ago(r.StartedAt), took(&r), r.Trigger)
				}
				w.Flush()
			})
		},
	}
	cmd.AddCommand(set, rm, run, logs, history)
	return cmd
}

// applyJobs plans a jobs change and applies it, refusing anything that
// would redeploy the app.
func applyJobs(cmd *cobra.Command, patch *spec.Patch, reason string) error {
	c := newClient()
	p, err := c.Plan(cmd.Context(), engine.PlanRequest{Patch: patch})
	if err != nil {
		return err
	}
	switch {
	case p.Action == "noop":
		return emit(p, func() { fmt.Println("Nothing to change.") })
	case p.Action != planner.OpConfig:
		if !flagJSON {
			printChanges(p, false)
		}
		return apierr.New(409, "not_jobs_only", "plan %s would %s %s, not only change its jobs", p.ID, p.Action, p.App).
			WithHint("review it and apply it with `coldship apply " + p.ID + " --reason ...`, or change jobs after the app's next deploy")
	case p.RequiresApproval:
		return apierr.New(403, "approval_required", "plan %s needs human approval before it can be applied", p.ID).
			WithHint("review it, run `coldship approve " + p.ID + "`, then `coldship apply " + p.ID + " --reason ...`")
	}
	if !flagJSON {
		printChanges(p, false)
	}
	res, err := c.Apply(cmd.Context(), p.ID, reason)
	if err != nil {
		return err
	}
	return emit(res, func() { printApply(res) })
}

func printJobs(app string, jobs []engine.JobView) {
	if len(jobs) == 0 {
		fmt.Printf("%s has no jobs. Add one:\n  coldship jobs set %s cleanup --schedule '0 3 * * *' -- npm run cleanup\n", app, app)
		return
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "JOB\tSCHEDULE (UTC)\tCOMMAND\tNEXT\tLAST")
	for _, j := range jobs {
		next, last := "-", "never"
		if j.Next != nil {
			next = "in " + time.Until(*j.Next).Round(time.Minute).String()
		}
		if j.Running != "" {
			last = "running now"
		} else if j.Last != nil {
			last = runStatus(j.Last) + ", " + ago(j.Last.StartedAt)
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", j.Name, j.Schedule, clipCmd(j.Command), next, last)
	}
	w.Flush()
}

func printRun(r *store.JobRun) {
	fmt.Printf("%s %s: %s (started %s by %s, took %s)\n", r.Job, r.ID, runStatus(r), r.StartedAt.Local().Format(time.DateTime), r.Trigger, took(r))
	if r.Error != "" && r.Status != store.JobSucceeded {
		fmt.Println(r.Error)
	}
	if r.Log != "" {
		fmt.Println(strings.TrimRight(r.Log, "\n"))
	}
}

func runStatus(r *store.JobRun) string { return strings.ReplaceAll(r.Status, "_", " ") }

func took(r *store.JobRun) string {
	if r.FinishedAt == nil {
		return "-"
	}
	return r.FinishedAt.Sub(r.StartedAt).Round(100 * time.Millisecond).String()
}

func ago(t time.Time) string {
	d := time.Since(t)
	if d < time.Minute {
		return "just now"
	}
	return d.Round(time.Minute).String() + " ago"
}

func clipCmd(s string) string {
	if len(s) > 40 {
		return s[:39] + "…"
	}
	return s
}
