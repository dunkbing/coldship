package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/spf13/cobra"

	"github.com/dunkbing/coldship/internal/apierr"
	"github.com/dunkbing/coldship/internal/runtime"
	"github.com/dunkbing/coldship/internal/update"
)

const (
	unitPath       = "/etc/systemd/system/coldship.service"
	defaultDataDir = "/var/lib/coldship"
)

func uninstallCmd() *cobra.Command {
	var yes, keepData bool
	cmd := &cobra.Command{
		Use:   "uninstall",
		Short: "Remove Coldship from this machine: the service, its containers, its data, and this binary",
		Long: `Remove Coldship from this machine:

  - the coldship service
  - every app, database and proxy container Coldship runs
  - their volumes (app and database data) and Coldship's state, unless --keep-data
  - images built from Git, the coldship Docker network
  - your saved CLI login, and the coldship binary

It shows what it will remove and asks before doing anything. It needs root,
and asks for sudo if needed.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if os.Geteuid() != 0 {
				return rerunAsRoot(cmd, uninstallArgs(yes, keepData))
			}
			return uninstall(cmd.Context(), yes, keepData)
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "don't ask for confirmation")
	cmd.Flags().BoolVar(&keepData, "keep-data", false, "keep app and database data (Docker volumes) and Coldship's state, so a reinstall picks them up")
	return cmd
}

func uninstallArgs(yes, keepData bool) []string {
	args := []string{"uninstall"}
	if yes {
		args = append(args, "--yes")
	}
	if keepData {
		args = append(args, "--keep-data")
	}
	return args
}

// rerunAsRoot runs this binary again with sudo, attached to the terminal.
func rerunAsRoot(cmd *cobra.Command, args []string) error {
	self, err := update.Executable()
	if err != nil {
		return err
	}
	sudo, err := exec.LookPath("sudo")
	if err != nil {
		return apierr.New(403, "permission_denied", "this needs root").WithHint("run it as root: " + self + " " + strings.Join(args, " "))
	}
	c := exec.CommandContext(cmd.Context(), sudo, append([]string{self}, args...)...)
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := c.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			os.Exit(ee.ExitCode())
		}
		return err
	}
	return nil
}

var (
	dataFlagRe   = regexp.MustCompile(`--data\s+(\S+)`)
	engineHostRe = regexp.MustCompile(`(?m)^Environment=DOCKER_HOST=(\S+)`)
)

// useServiceEngine points the Docker client at the engine the installed
// service uses (Podman's socket, say), so uninstall cleans up the right one
// even when both Docker and Podman are installed.
func useServiceEngine() {
	if os.Getenv("DOCKER_HOST") != "" {
		return
	}
	if b, err := os.ReadFile(unitPath); err == nil {
		if m := engineHostRe.FindSubmatch(b); m != nil {
			os.Setenv("DOCKER_HOST", string(m[1]))
		}
	}
}

// dataDir is where the installed service keeps its state.
func dataDir() string {
	if b, err := os.ReadFile(unitPath); err == nil {
		if m := dataFlagRe.FindSubmatch(b); m != nil {
			return filepath.Dir(string(m[1]))
		}
	}
	return defaultDataDir
}

// invokingUserConfig is the CLI config of the person who ran the command,
// also under sudo.
func invokingUserConfig() string {
	home := ""
	if name := os.Getenv("SUDO_USER"); name != "" && name != "root" {
		if u, err := user.Lookup(name); err == nil {
			home = u.HomeDir
		}
	}
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	if home == "" {
		return ""
	}
	return filepath.Join(home, ".config", "coldship")
}

func uninstall(ctx context.Context, yes, keepData bool) error {
	self, _ := update.Executable()
	dir := dataDir()
	_, unitErr := os.Stat(unitPath)
	hasUnit := unitErr == nil
	_, dirErr := os.Stat(dir)
	hasData := dirErr == nil
	cfg := invokingUserConfig()

	useServiceEngine()
	d := runtime.NewDocker()
	plan, planErr := d.PlanPurge(ctx)
	if planErr != nil {
		fmt.Fprintf(os.Stderr, "note: can't reach Docker (%v); Docker resources won't be removed\n", planErr)
	}

	fmt.Println("This removes:")
	if hasUnit {
		fmt.Println("  - the coldship service")
	}
	if n := len(plan.Containers); n > 0 {
		fmt.Printf("  - %d container(s): %s\n", n, strings.Join(plan.Containers, ", "))
	}
	if n := len(plan.Volumes); n > 0 && !keepData {
		fmt.Printf("  - %d volume(s), with all app and database data: %s\n", n, strings.Join(plan.Volumes, ", "))
	}
	if hasData && !keepData {
		fmt.Printf("  - %s: Coldship's state, secrets key and database snapshots\n", dir)
	}
	if n := len(plan.Images); n > 0 {
		fmt.Printf("  - %d image(s) built from Git\n", n)
	}
	if cfg != "" {
		fmt.Printf("  - your CLI login (%s)\n", cfg)
	}
	fmt.Printf("  - the coldship binary (%s)\n", self)
	if keepData {
		fmt.Printf("Kept: app and database volumes, and %s. Reinstalling picks them up.\n", dir)
	} else if len(plan.Volumes) > 0 || hasData {
		fmt.Println("App and database data is deleted for good. To keep it, use --keep-data.")
	}

	if !yes {
		if !isTerminal(os.Stdin) {
			return apierr.New(400, "confirmation_required", "uninstalling needs confirmation").WithHint("run it in a terminal, or pass --yes")
		}
		fmt.Print("\nType \"uninstall\" to continue: ")
		line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		if strings.TrimSpace(line) != "uninstall" {
			fmt.Println("Nothing was removed.")
			return nil
		}
	}
	fmt.Println()

	var problems []string
	if hasUnit {
		exec.Command("systemctl", "disable", "--now", "coldship").Run()
		if err := os.Remove(unitPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			problems = append(problems, err.Error())
		}
		exec.Command("systemctl", "daemon-reload").Run()
		fmt.Println("  removed the coldship service")
	}
	if planErr == nil {
		if err := d.Purge(ctx, plan, keepData, os.Stdout); err != nil {
			problems = append(problems, err.Error())
		}
	}
	if hasData && !keepData {
		if err := os.RemoveAll(dir); err != nil {
			problems = append(problems, err.Error())
		} else {
			fmt.Printf("  removed %s\n", dir)
		}
	}
	if cfg != "" {
		os.RemoveAll(cfg)
	}
	if self != "" {
		os.Remove(self + ".old")
		if err := os.Remove(self); err != nil && !errors.Is(err, os.ErrNotExist) {
			problems = append(problems, err.Error())
		} else {
			fmt.Printf("  removed %s\n", self)
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("Coldship was mostly removed, but:\n  %s", strings.Join(problems, "\n  "))
	}
	fmt.Println("\nColdship is uninstalled.")
	return nil
}
