package main

import (
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/dunkbing/coldship/internal/apierr"
	"github.com/dunkbing/coldship/internal/engine"
)

func dbCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "db", Short: "Managed databases: status, snapshots and restores"}

	list := &cobra.Command{
		Use:   "list <app>",
		Short: "List an app's databases",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dbs, err := newClient().Databases(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			return emit(dbs, func() {
				if len(dbs) == 0 {
					fmt.Println("No databases. Add them under `databases:` in the app's spec.")
					return
				}
				w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
				fmt.Fprintln(w, "DATABASE\tENGINE\tSTATUS\tENV\tHOST\tSNAPSHOTS\tLATEST")
				for _, d := range dbs {
					status := d.Status.State
					if !d.Status.Exists {
						status = "missing"
					}
					latest := "-"
					if d.LastSnapshot != nil {
						latest = time.Since(d.LastSnapshot.CreatedAt).Round(time.Minute).String() + " ago"
					}
					fmt.Fprintf(w, "%s\t%s %s\t%s\t%s\t%s\t%d\t%s\n", d.Name, d.Engine, d.Version, status, d.Env, d.Host, d.Snapshots, latest)
				}
				w.Flush()
			})
		},
	}

	var reason string
	snap := &cobra.Command{
		Use:   "snapshot <app> <database>",
		Short: "Take a snapshot of a database now",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			sn, err := newClient().Snapshot(cmd.Context(), args[0], args[1], reason)
			if err != nil {
				return err
			}
			return emit(sn, func() {
				fmt.Printf("Snapshot %s of %s/%s saved (%s).\n", sn.ID, sn.App, sn.Database, humanBytes(sn.Size))
			})
		},
	}
	snap.Flags().StringVar(&reason, "reason", "", "why (recorded with the snapshot)")

	var onlyDB string
	snaps := &cobra.Command{
		Use:   "snapshots <app>",
		Short: "List an app's database snapshots, newest first",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			list, err := newClient().Snapshots(cmd.Context(), args[0], onlyDB)
			if err != nil {
				return err
			}
			return emit(list, func() {
				w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
				fmt.Fprintln(w, "SNAPSHOT\tDATABASE\tSIZE\tCREATED\tBY\tREASON")
				for _, s := range list {
					fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", s.ID, s.Database, humanBytes(s.Size), s.CreatedAt.Local().Format("2006-01-02 15:04:05"), s.Actor, s.Reason)
				}
				w.Flush()
			})
		},
	}
	snaps.Flags().StringVar(&onlyDB, "database", "", "only this database")

	var restoreReason string
	restore := &cobra.Command{
		Use:   "restore <app> <database> <snapshot>",
		Short: "Restore a database from a snapshot (plans first; production needs approval)",
		Long: "Plan and apply restoring a database from a snapshot. The current data is snapshotted first.\n" +
			"For production apps the plan needs human approval: the command stops with exit code 3.",
		Args: cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			c := newClient()
			p, err := c.Plan(cmd.Context(), engine.PlanRequest{Restore: &engine.RestoreRequest{App: args[0], Database: args[1], Snapshot: args[2]}})
			if err != nil {
				return err
			}
			if !flagJSON {
				printPlan(p)
				fmt.Println()
			}
			if p.RequiresApproval {
				return apierr.New(403, "approval_required", "restoring a production database needs human approval (plan %s)", p.ID).
					WithHint("review it, run `coldship approve " + p.ID + "`, then `coldship apply " + p.ID + " --reason ...`")
			}
			res, err := c.Apply(cmd.Context(), p.ID, restoreReason)
			if err != nil {
				return err
			}
			return emit(res, func() { fmt.Println(res.Message) })
		},
	}
	restore.Flags().StringVar(&restoreReason, "reason", "", "why (recorded in the audit log)")
	restore.MarkFlagRequired("reason")

	var backupKey string
	imp := &cobra.Command{
		Use:   "import <app> <database> --backup KEY",
		Short: "Copy an off-site backup in as a snapshot of the app's database, to restore (admin)",
		Example: `  coldship backups list --app shop --database main
  coldship db import shop main --backup coldship/apps/shop/main/20261102T030000.000000000Z-snap_1a2b3c.dump
  coldship db restore shop main <snapshot> --reason "new server"`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			sn, err := newClient().ImportBackup(cmd.Context(), args[0], args[1], backupKey)
			if err != nil {
				return err
			}
			return emit(sn, func() {
				fmt.Printf("Copied in as snapshot %s. Restore it:\n  coldship db restore %s %s %s --reason \"...\"\n", sn.ID, args[0], args[1], sn.ID)
			})
		},
	}
	imp.Flags().StringVar(&backupKey, "backup", "", "the backup's key (from coldship backups list)")
	imp.MarkFlagRequired("backup")

	cmd.AddCommand(list, snap, snaps, restore, imp)
	return cmd
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}
