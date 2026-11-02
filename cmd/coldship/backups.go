package main

import (
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/dunkbing/coldship/internal/apierr"
	"github.com/dunkbing/coldship/internal/engine"
	"github.com/dunkbing/coldship/internal/offsite"
)

func backupsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "backups",
		Short: "Off-site backups: copies of every database snapshot in an S3-compatible bucket",
		Long: `Coldship snapshots every database daily and before anything destructive.
With a bucket set, each snapshot is also copied off the server, next to a
manifest, so a new server can restore from the bucket alone.

  COLDSHIP_BACKUP_SECRET_KEY=... coldship backups set --endpoint https://s3.us-east-1.amazonaws.com \
    --bucket my-backups --access-key AKIA... --region us-east-1
  coldship backups list --data maindb
  coldship data restore maindb --backup <key> --reason "new server"`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			st, err := newClient().BackupStatus(cmd.Context())
			if err != nil {
				return err
			}
			return emit(st, func() { printBackupStatus(st) })
		},
	}

	var cfg offsite.Config
	set := &cobra.Command{
		Use:   "set",
		Short: "Set the bucket backups go to; it's checked first (admin)",
		Long: `Set the bucket backups go to. The secret key is read from
COLDSHIP_BACKUP_SECRET_KEY, or asked for; leave it empty to keep the saved one.
Settings you don't pass keep their saved values.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c := newClient()
			cur, err := c.BackupStatus(cmd.Context())
			if err != nil {
				return err
			}
			changed := cmd.Flags().Changed
			if cur.Configured {
				keep := func(flag string, dst *string, v string) {
					if !changed(flag) {
						*dst = v
					}
				}
				keep("endpoint", &cfg.Endpoint, cur.Endpoint)
				keep("bucket", &cfg.Bucket, cur.Bucket)
				keep("region", &cfg.Region, cur.Region)
				keep("prefix", &cfg.Prefix, cur.Prefix)
				keep("access-key", &cfg.AccessKey, cur.AccessKey)
				if !changed("path-style") {
					cfg.PathStyle = cur.PathStyle
				}
				if !changed("keep") {
					cfg.Keep = cur.Keep
				}
			} else {
				for _, f := range []string{"endpoint", "bucket", "access-key"} {
					if !changed(f) {
						return apierr.New(400, "invalid_request", "--%s is required to set up backups", f)
					}
				}
			}
			cfg.SecretKey = os.Getenv("COLDSHIP_BACKUP_SECRET_KEY")
			if cfg.SecretKey == "" && isTerminal(os.Stdin) {
				fmt.Fprint(os.Stderr, "Secret key (empty keeps the saved one): ")
				b, err := term.ReadPassword(int(os.Stdin.Fd()))
				fmt.Fprintln(os.Stderr)
				if err != nil {
					return err
				}
				cfg.SecretKey = strings.TrimSpace(string(b))
			}
			st, err := c.SaveBackup(cmd.Context(), cfg)
			if err != nil {
				return err
			}
			return emit(st, func() {
				fmt.Println("Saved. Snapshots are copied to the bucket from now on, starting with the ones already here.")
				printBackupStatus(st)
			})
		},
	}
	set.Flags().StringVar(&cfg.Endpoint, "endpoint", "", "S3 endpoint, e.g. https://s3.us-east-1.amazonaws.com or https://<account>.r2.cloudflarestorage.com")
	set.Flags().StringVar(&cfg.Bucket, "bucket", "", "bucket name")
	set.Flags().StringVar(&cfg.Region, "region", "", "region, if the provider needs one")
	set.Flags().StringVar(&cfg.Prefix, "prefix", "coldship", "folder in the bucket")
	set.Flags().StringVar(&cfg.AccessKey, "access-key", "", "access key ID")
	set.Flags().BoolVar(&cfg.PathStyle, "path-style", false, "path-style URLs (MinIO, SeaweedFS and other self-hosted S3)")
	set.Flags().IntVar(&cfg.Keep, "keep", offsite.DefaultKeep, "backups to keep per database")

	off := &cobra.Command{
		Use:   "off",
		Short: "Stop copying snapshots off-site; the bucket's contents stay (admin)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := newClient().DeleteBackup(cmd.Context()); err != nil {
				return err
			}
			return emit(map[string]any{"deleted": true}, func() { fmt.Println("Off-site backups are off. Nothing in the bucket was deleted.") })
		},
	}

	sync := &cobra.Command{
		Use:   "sync",
		Short: "Copy snapshots to the bucket now (admin)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			st, err := newClient().SyncBackups(cmd.Context())
			if err != nil {
				return err
			}
			return emit(st, func() { printBackupStatus(st) })
		},
	}

	var app, db, data string
	list := &cobra.Command{
		Use:   "list",
		Short: "Backups in the bucket, newest first",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if data != "" {
				app, db = "", data
			} else if (app == "") != (db == "") {
				return apierr.New(400, "invalid_request", "give --data <service>, or --app and --database together")
			}
			items, err := newClient().Backups(cmd.Context(), app, db)
			if err != nil {
				return err
			}
			return emit(items, func() {
				if len(items) == 0 {
					fmt.Println("No backups in the bucket yet.")
					return
				}
				w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
				fmt.Fprintln(w, "DATABASE\tENGINE\tCREATED\tSIZE\tKEY")
				for _, b := range items {
					name := b.Database
					if b.App != "" {
						name = b.App + "/" + b.Database
					}
					fmt.Fprintf(w, "%s\t%s %s\t%s\t%s\t%s\n", name, b.Engine, b.Version, b.CreatedAt.Local().Format("2006-01-02 15:04"), size(b.Size), b.Key)
				}
				w.Flush()
			})
		},
	}
	list.Flags().StringVar(&data, "data", "", "a data service")
	list.Flags().StringVar(&app, "app", "", "an app (with --database)")
	list.Flags().StringVar(&db, "database", "", "the app's database")

	cmd.AddCommand(set, off, sync, list)
	return cmd
}

func printBackupStatus(st *engine.BackupView) {
	if !st.Configured {
		fmt.Println("Off-site backups: off. Snapshots stay on this server only.")
		fmt.Println("Set a bucket: coldship backups set --endpoint ... --bucket ... --access-key ...")
		return
	}
	fmt.Printf("Bucket:    %s (%s)\n", st.Bucket, st.Endpoint)
	if st.Prefix != "" {
		fmt.Printf("Folder:    %s/\n", st.Prefix)
	}
	fmt.Printf("Keeping:   %d backups per database\n", st.Keep)
	fmt.Printf("Copied:    %d snapshots, %d waiting\n", st.Uploaded, st.Pending)
	if st.LastSync != nil {
		fmt.Printf("Last sync: %s\n", st.LastSync.Local().Format("2006-01-02 15:04:05"))
	}
	if st.LastError != "" {
		fmt.Printf("Error:     %s\n", st.LastError)
	}
}

func size(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
