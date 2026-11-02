package main

import (
	"bufio"
	"fmt"
	"os"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/dunkbing/coldship/internal/apierr"
	"github.com/dunkbing/coldship/internal/engine"
)

var engineNames = map[string]string{"postgres": "Postgres", "mysql": "MySQL", "redis": "Redis", "s3": "S3 storage", "rustfs": "RustFS storage"}

func dataCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "data",
		Short: "Databases and storage that apps connect to: Postgres, MySQL, Redis, S3",
		Long: `Data services are databases and object storage that stand on their own.
Create one, then connect apps to it:

  coldship data create postgres maindb
  coldship data create s3 uploads        # or rustfs, for RustFS
  coldship deploy --name web --use maindb --use uploads

Connected apps get DATABASE_URL (postgres, mysql), REDIS_URL (redis), or the
AWS_* and S3_* variables S3 libraries read (s3 storage).`,
	}

	list := &cobra.Command{
		Use:   "list",
		Short: "List data services, and the databases apps own",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			items, err := newClient().ListData(cmd.Context())
			if err != nil {
				return err
			}
			return emit(items, func() {
				if len(items) == 0 {
					fmt.Println("No data services yet. Create one: coldship data create postgres maindb")
					return
				}
				w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
				fmt.Fprintln(w, "NAME\tKIND\tSTATUS\tUSED BY\tGIVES")
				for _, d := range items {
					name := d.Name
					if d.App != "" {
						name = d.App + "/" + d.Name + " (app's own)"
					}
					status := "running"
					if !d.Status.Running {
						status = orDash(d.Status.State)
						if !d.Status.Exists {
							status = "missing"
						}
					}
					fmt.Fprintf(w, "%s\t%s %s\t%s\t%s\t%s\n", name, engineNames[d.Engine], d.Version, status,
						orDash(strings.Join(d.UsedBy, ", ")), envSummary(d.Env))
				}
				w.Flush()
			})
		},
	}

	var version string
	create := &cobra.Command{
		Use:   "create <postgres|mysql|redis|s3|rustfs> <name>",
		Short: "Create a data service and wait until it's ready",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !flagJSON {
				fmt.Fprintf(os.Stderr, "Creating %s %s...\n", engineNames[args[0]], args[1])
			}
			d, err := newClient().CreateData(cmd.Context(), engine.DataRequest{Engine: args[0], Name: args[1], Version: version})
			if err != nil {
				return err
			}
			return emit(d, func() {
				fmt.Printf("%s %s %s is ready at %s.\n", engineNames[d.Engine], d.Name, d.Version, d.Host)
				fmt.Printf("Connect an app: coldship deploy --name <app> --use %s  (it gets %s)\n", d.Name, envSummary(d.Env))
			})
		},
	}
	create.Flags().StringVar(&version, "version", "", "version (default: the latest supported)")

	show := &cobra.Command{
		Use:   "show <name>",
		Short: "A data service's status, connected apps and latest snapshot",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			d, err := newClient().GetData(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			return emit(d, func() {
				fmt.Printf("%s %s %s\n", engineNames[d.Engine], d.Name, d.Version)
				fmt.Printf("  Host:     %s (only on Coldship's network)\n", d.Host)
				fmt.Printf("  Status:   %s\n", map[bool]string{true: "running", false: "not running"}[d.Status.Running])
				fmt.Printf("  Used by:  %s\n", orDash(strings.Join(d.UsedBy, ", ")))
				fmt.Printf("  Gives:    %s\n", envSummary(d.Env))
				if s := d.LatestSnapshot; s != nil {
					fmt.Printf("  Snapshot: %s, %s (%s)\n", s.ID, s.CreatedAt.Local().Format("2006-01-02 15:04"), s.Reason)
				}
			})
		},
	}

	connection := &cobra.Command{
		Use:   "connection <name>",
		Short: "Show a data service's connection details, password included (admin)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			env, err := newClient().DataConnection(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			return emit(env, func() {
				keys := make([]string, 0, len(env))
				for k := range env {
					keys = append(keys, k)
				}
				sort.Strings(keys)
				for _, k := range keys {
					fmt.Printf("%s=%s\n", k, env[k])
				}
				fmt.Fprintln(os.Stderr, "\nThese work from Coldship's network (apps, or: sudo docker exec -it <container> ...), not from the internet.")
			})
		},
	}

	var yes bool
	var reason string
	del := &cobra.Command{
		Use:   "delete <name>",
		Short: "Delete a data service and its data, after a snapshot (admin)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			if !yes {
				if !isTerminal(os.Stdin) || flagJSON {
					return apierr.New(400, "confirmation_required", "deleting %s needs confirmation", name).WithHint("run it in a terminal, or pass --yes")
				}
				fmt.Printf("This deletes %s and all its data (a snapshot is kept, except for S3 storage).\nType %q to delete it: ", name, name)
				line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
				if strings.TrimSpace(line) != name {
					fmt.Println("Nothing was deleted.")
					return nil
				}
			}
			if err := newClient().DeleteData(cmd.Context(), name, reason); err != nil {
				return err
			}
			return emit(map[string]any{"name": name, "deleted": true}, func() { fmt.Printf("Deleted %s.\n", name) })
		},
	}
	del.Flags().BoolVarP(&yes, "yes", "y", false, "don't ask for confirmation")
	del.Flags().StringVar(&reason, "reason", "", "why, for the audit log")

	var snapReason string
	snapshot := &cobra.Command{
		Use:   "snapshot <name>",
		Short: "Snapshot a data service now",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			sn, err := newClient().SnapshotData(cmd.Context(), args[0], snapReason)
			if err != nil {
				return err
			}
			return emit(sn, func() { fmt.Printf("Snapshot %s of %s saved (%d B).\n", sn.ID, args[0], sn.Size) })
		},
	}
	snapshot.Flags().StringVar(&snapReason, "reason", "", "why, for the audit log")

	snapshots := &cobra.Command{
		Use:   "snapshots <name>",
		Short: "A data service's snapshots, newest first",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			snaps, err := newClient().DataSnapshots(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			return emit(snaps, func() {
				w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
				fmt.Fprintln(w, "SNAPSHOT\tCREATED\tSIZE\tREASON")
				for _, s := range snaps {
					fmt.Fprintf(w, "%s\t%s\t%d B\t%s\n", s.ID, s.CreatedAt.Local().Format("2006-01-02 15:04"), s.Size, s.Reason)
				}
				w.Flush()
			})
		},
	}

	var restoreReason, restoreBackup string
	restore := &cobra.Command{
		Use:   "restore <name> [snapshot]",
		Short: "Replace a data service's data with a snapshot or an off-site backup; the current data is snapshotted first (admin)",
		Example: `  coldship data restore maindb snap_1a2b3c --reason "undo bad migration"
  coldship data restore maindb --backup prod/data/maindb/20261102T030000.000000000Z-snap_1a2b3c.dump --reason "new server"`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if (len(args) == 2) == (restoreBackup != "") {
				return apierr.New(400, "invalid_request", "give a snapshot or --backup, not both").
					WithHint("coldship data snapshots " + args[0] + ", or coldship backups list --data " + args[0])
			}
			var msg string
			var err error
			if restoreBackup != "" {
				msg, err = newClient().RestoreDataBackup(cmd.Context(), args[0], restoreBackup, restoreReason)
			} else {
				msg, err = newClient().RestoreData(cmd.Context(), args[0], args[1], restoreReason)
			}
			if err != nil {
				return err
			}
			return emit(map[string]any{"message": msg}, func() { fmt.Println(msg) })
		},
	}
	restore.Flags().StringVar(&restoreReason, "reason", "", "why (required)")
	restore.Flags().StringVar(&restoreBackup, "backup", "", "an off-site backup's key (from coldship backups list)")
	restore.MarkFlagRequired("reason")

	cmd.AddCommand(list, create, show, connection, del, snapshot, snapshots, restore)
	return cmd
}

// envSummary shortens the S3 variables to "AWS_* and S3_* variables".
func envSummary(env []string) string {
	for _, e := range env {
		if strings.HasPrefix(e, "AWS_") {
			return "AWS_* and S3_* variables"
		}
	}
	return orDash(strings.Join(env, ", "))
}
