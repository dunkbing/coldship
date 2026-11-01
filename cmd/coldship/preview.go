package main

import (
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/dunkbing/coldship/internal/engine"
)

func previewCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "preview",
		Short: "Throwaway copies of an app, for testing changes (deleted automatically)",
	}

	var req engine.PreviewRequest
	var env []string
	var inherit, noInherit bool
	create := &cobra.Command{
		Use:   "create <app>",
		Short: "Create or update a preview of an app at a Git ref or image",
		Long: "Create a preview: a copy of <app> at --ref (apps built from Git) or --image, with fresh empty\n" +
			"volumes, deleted automatically after --ttl. Running it again for the same preview redeploys it\n" +
			"(e.g. after a push) and resets its expiry. Previews of production apps don't get the app's env\n" +
			"unless you pass --inherit-env.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			req.App = args[0]
			for _, kv := range env {
				k, v, ok := strings.Cut(kv, "=")
				if !ok {
					return fmt.Errorf("--env %q must look like KEY=value", kv)
				}
				if req.Env == nil {
					req.Env = map[string]string{}
				}
				req.Env[k] = v
			}
			switch {
			case inherit && noInherit:
				return fmt.Errorf("use either --inherit-env or --no-inherit-env")
			case inherit:
				req.InheritEnv = &inherit
			case noInherit:
				f := false
				req.InheritEnv = &f
			}
			res, err := newClient().CreatePreview(cmd.Context(), req)
			if err != nil {
				return err
			}
			return emit(res, func() {
				verb := "Updated"
				if res.Created {
					verb = "Created"
				}
				fmt.Printf("%s preview %s of %s: %s\n", verb, res.App, res.PreviewOf, res.Message)
				for _, u := range res.URLs {
					fmt.Println("  " + u)
				}
				fmt.Printf("Expires %s (in %s).\n", res.ExpiresAt.Local().Format("Mon 15:04"), time.Until(res.ExpiresAt).Round(time.Minute))
			})
		},
	}
	create.Flags().StringVar(&req.Ref, "ref", "", "Git branch, tag or commit to deploy")
	create.Flags().StringVar(&req.Image, "image", "", "image to run (apps that use a prebuilt image)")
	create.Flags().StringVar(&req.Name, "name", "", "preview name suffix (default: from --ref)")
	create.Flags().StringVar(&req.TTL, "ttl", "", "how long the preview lives, e.g. 2h (server default 24h)")
	create.Flags().StringArrayVar(&env, "env", nil, "KEY=value to add or override (repeatable)")
	create.Flags().BoolVar(&inherit, "inherit-env", false, "copy the app's env even if it is a production app")
	create.Flags().BoolVar(&noInherit, "no-inherit-env", false, "don't copy the app's env")
	create.Flags().StringVar(&req.Reason, "reason", "", "why (recorded in the audit log)")

	list := &cobra.Command{
		Use:   "list",
		Short: "List previews, soonest-expiring first",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			previews, err := newClient().ListPreviews(cmd.Context())
			if err != nil {
				return err
			}
			return emit(previews, func() {
				if len(previews) == 0 {
					fmt.Println("No previews.")
					return
				}
				w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
				fmt.Fprintln(w, "PREVIEW\tOF\tSOURCE\tEXPIRES")
				for _, p := range previews {
					src, exp := "-", "-"
					if p.Spec != nil {
						src = p.Spec.Source()
					}
					if p.ExpiresAt != nil {
						exp = "in " + time.Until(*p.ExpiresAt).Round(time.Minute).String()
					}
					fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", p.Name, p.PreviewOf, src, exp)
				}
				w.Flush()
			})
		},
	}

	var delReason string
	del := &cobra.Command{
		Use:   "delete <preview>",
		Short: "Delete a preview and its volumes",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			res, err := newClient().DeletePreview(cmd.Context(), args[0], delReason)
			if err != nil {
				return err
			}
			return emit(res, func() { fmt.Printf("Deleted preview %s.\n", args[0]) })
		},
	}
	del.Flags().StringVar(&delReason, "reason", "", "why (recorded in the audit log)")

	cmd.AddCommand(create, list, del)
	return cmd
}
