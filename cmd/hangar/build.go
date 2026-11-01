package main

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"hangar/internal/engine"
	"hangar/internal/spec"
)

func buildCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "build", Short: "Inspect how apps are built from Git"}
	var file, app string
	var showDockerfile bool
	detect := &cobra.Command{
		Use:   "detect",
		Short: "Show how Hangar would build an app, without building it",
		Long: "Check out the app's commit and report whether it builds from its Dockerfile or from a generated\n" +
			"one, what was detected and why. Use -f for a spec file or --app for a deployed app.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			req := engine.DetectRequest{App: app}
			if app == "" {
				if file == "" {
					file = "hangar.yaml"
				}
				a, err := spec.Load(file)
				if err != nil {
					return err
				}
				req.Spec = a
			}
			res, err := newClient().DetectBuild(cmd.Context(), req)
			if err != nil {
				return err
			}
			return emit(res, func() {
				d := res.Detection
				fmt.Printf("Commit %s builds with %s.\n", res.Commit[:12], d.Summary())
				for _, n := range d.Notes {
					fmt.Println("  - " + n)
				}
				if d.Mode == "auto" {
					if showDockerfile {
						fmt.Println("\nGenerated Dockerfile:")
						fmt.Println(strings.TrimRight(d.Dockerfile, "\n"))
					} else {
						fmt.Println("\nRun with --dockerfile to see the generated Dockerfile. Override detection with build.auto in the spec.")
					}
				}
			})
		},
	}
	detect.Flags().StringVarP(&file, "file", "f", "", "spec file (default hangar.yaml)")
	detect.Flags().StringVar(&app, "app", "", "a deployed app instead of a spec file")
	detect.Flags().BoolVar(&showDockerfile, "dockerfile", false, "print the generated Dockerfile")
	cmd.AddCommand(detect)
	return cmd
}
