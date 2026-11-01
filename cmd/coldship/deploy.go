package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/dunkbing/coldship/internal/apierr"
	"github.com/dunkbing/coldship/internal/engine"
	"github.com/dunkbing/coldship/internal/spec"
)

// deployFlags are the zero-config way to describe an app. Each flag changes
// only what it names; the server keeps everything else (including secrets).
type deployFlags struct {
	name, image, git, ref, memory string
	port, hostPort                int
	production                    bool
	env, unsetEnv, domains        []string
	removeDomains, dbs, volumes   []string
}

func (f *deployFlags) any(cmd *cobra.Command) bool {
	for _, n := range []string{"name", "image", "git", "ref", "memory", "port", "host-port", "production",
		"env", "unset-env", "domain", "remove-domain", "db", "volume"} {
		if cmd.Flags().Changed(n) {
			return true
		}
	}
	return false
}

func deployCmd() *cobra.Command {
	var f deployFlags
	var file, reason string
	var redeploy bool
	cmd := &cobra.Command{
		Use:   "deploy",
		Short: "Deploy an app: the Git repository you're in, an image, or a spec file",
		Long: `Deploy an app. No config file is needed:

  coldship deploy                                   # this Git repository, named after its folder
  coldship deploy --domain shop.example.com --db postgres
  coldship deploy --name web --image nginx:1.27 --port 80
  coldship deploy --name web --env API_KEY=... --unset-env OLD_FLAG

Flags change only what they name; everything else about an existing app,
including its secrets, stays as it is. With -f (or a coldship.yaml here and no
flags), the spec file describes the whole app instead.

Stops with exit code 3 if the change needs a human's approval.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var req engine.PlanRequest
			useFile := file != ""
			if !useFile && !f.any(cmd) {
				if _, err := os.Stat("coldship.yaml"); err == nil {
					useFile = true
				}
			}
			switch {
			case useFile && f.any(cmd):
				return fmt.Errorf("use either -f (a spec file) or app flags like --env, not both")
			case useFile:
				r, err := planRequestFromFlags(file, "", redeploy)
				if err != nil {
					return err
				}
				req = r
			default:
				p, err := f.patch(cmd)
				if err != nil {
					return err
				}
				req = engine.PlanRequest{Patch: p, Redeploy: redeploy}
			}
			if reason == "" {
				reason = "coldship deploy"
			}
			return planAndApply(cmd, req, reason)
		},
	}
	fl := cmd.Flags()
	fl.StringVarP(&file, "file", "f", "", "spec file describing the whole app")
	fl.StringVar(&reason, "reason", "", "why, for the audit log")
	fl.BoolVar(&redeploy, "redeploy", false, "restart even if nothing changed (rebuilds apps built from Git)")
	fl.StringVar(&f.name, "name", "", "app name (default: the current folder's name)")
	fl.StringVar(&f.image, "image", "", "run this container image")
	fl.StringVar(&f.git, "git", "", "build from this Git repository (default: the repository you're in)")
	fl.StringVar(&f.ref, "ref", "", "branch or tag to build (default: your current branch)")
	fl.IntVar(&f.port, "port", 0, "port the app listens on (detected for apps built from Git)")
	fl.IntVar(&f.hostPort, "host-port", 0, "publish the app on this port of the server")
	fl.StringArrayVar(&f.domains, "domain", nil, "serve the app on this domain, with HTTPS (repeatable)")
	fl.StringArrayVar(&f.removeDomains, "remove-domain", nil, "stop serving this domain (repeatable)")
	fl.StringArrayVarP(&f.env, "env", "e", nil, "set an environment variable, KEY=value (repeatable)")
	fl.StringArrayVar(&f.unsetEnv, "unset-env", nil, "remove an environment variable (repeatable)")
	fl.StringArrayVar(&f.dbs, "db", nil, "add a database: postgres or redis, optionally postgres:name (repeatable)")
	fl.StringArrayVar(&f.volumes, "volume", nil, "add a persistent volume, name:/path (repeatable)")
	fl.StringVar(&f.memory, "memory", "", "memory limit, e.g. 512m")
	fl.BoolVar(&f.production, "production", false, "mark the app as production: destructive changes then need a human's approval")
	return cmd
}

var slugNonAlnum = regexp.MustCompile(`[^a-z0-9]+`)

func (f *deployFlags) patch(cmd *cobra.Command) (*spec.Patch, error) {
	p := &spec.Patch{Name: f.name, Image: f.image, Port: f.port, HostPort: f.hostPort, Memory: f.memory,
		UnsetEnv: f.unsetEnv, AddDomains: f.domains, RemoveDomains: f.removeDomains}
	if f.production {
		p.Stage = spec.StageProduction
	}
	if f.image != "" && f.git != "" {
		return nil, fmt.Errorf("use either --image or --git, not both")
	}
	if f.git != "" {
		p.Build = &spec.Build{Git: f.git, Ref: f.ref}
	} else if f.ref != "" {
		return nil, fmt.Errorf("--ref needs --git, or run deploy inside the repository without --name")
	}
	// Without --name, the folder is the app: its name, and (in a Git
	// repository) its source.
	if p.Name == "" {
		top := gitOut("rev-parse", "--show-toplevel")
		dir := top
		if dir == "" {
			wd, err := os.Getwd()
			if err != nil {
				return nil, err
			}
			dir = wd
		}
		p.Name = strings.Trim(slugNonAlnum.ReplaceAllString(strings.ToLower(filepath.Base(dir)), "-"), "-")
		if p.Name == "" || p.Name[0] < 'a' || p.Name[0] > 'z' {
			return nil, fmt.Errorf("can't name the app after this folder; pass --name")
		}
		if len(p.Name) > 40 {
			p.Name = strings.Trim(p.Name[:40], "-")
		}
		if top != "" && p.Image == "" && p.Build == nil {
			b, err := buildFromHere()
			if err != nil {
				return nil, err
			}
			p.Build = b
		}
	}
	for _, kv := range f.env {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || k == "" {
			return nil, fmt.Errorf("--env %q must be KEY=value", kv)
		}
		if p.Env == nil {
			p.Env = map[string]string{}
		}
		p.Env[k] = v
	}
	for _, d := range f.dbs {
		eng, name, _ := strings.Cut(d, ":")
		p.AddDatabases = append(p.AddDatabases, spec.Database{Engine: eng, Name: name})
	}
	for _, v := range f.volumes {
		name, path, ok := strings.Cut(v, ":")
		if !ok || name == "" || path == "" {
			return nil, fmt.Errorf("--volume %q must be name:/path", v)
		}
		p.AddVolumes = append(p.AddVolumes, spec.Volume{Name: name, Path: path})
	}
	return p, nil
}

// buildFromHere builds the current branch of the repository's origin. The
// server builds what is pushed, so unpushed commits get a warning.
func buildFromHere() (*spec.Build, error) {
	remote := gitOut("remote", "get-url", "origin")
	if remote == "" {
		return nil, apierr.New(400, "invalid_request", "this Git repository has no origin remote, so the server can't fetch it").
			WithHint("push it somewhere the server can reach and add it as origin, or pass --image")
	}
	ref := gitOut("symbolic-ref", "--short", "-q", "HEAD")
	switch {
	case ref == "":
		ref = gitOut("rev-parse", "HEAD") // detached: build this commit
	case gitOut("rev-parse", "--abbrev-ref", "@{u}") == "":
		warn("branch %s isn't pushed; the server builds what's on %s", ref, remote)
	default:
		if n, _ := strconv.Atoi(gitOut("rev-list", "--count", "@{u}..HEAD")); n > 0 {
			warn("%d commit(s) on %s aren't pushed yet; the server builds what's pushed", n, ref)
		}
	}
	return &spec.Build{Git: httpsRemote(remote), Ref: ref}, nil
}

var scpRemote = regexp.MustCompile(`^git@(github\.com|gitlab\.com|codeberg\.org|bitbucket\.org):(.+)$`)

// httpsRemote turns git@github.com:o/r.git into https://github.com/o/r.git
// for well-known hosts, since the server usually has no SSH key for them.
func httpsRemote(u string) string {
	if m := scpRemote.FindStringSubmatch(u); m != nil {
		return "https://" + m[1] + "/" + m[2]
	}
	return u
}

func gitOut(args ...string) string {
	out, err := exec.Command("git", args...).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func warn(format string, a ...any) {
	if !flagJSON {
		fmt.Fprintf(os.Stderr, "note: "+format+"\n", a...)
	}
}

// planAndApply plans, stops if a human must approve, applies, and prints
// where the app is reachable.
func planAndApply(cmd *cobra.Command, req engine.PlanRequest, reason string) error {
	c := newClient()
	p, err := c.Plan(cmd.Context(), req)
	if err != nil {
		return err
	}
	if p.RequiresApproval {
		if !flagJSON {
			printPlan(p)
		}
		return apierr.New(403, "approval_required", "plan %s needs human approval before it can be applied", p.ID).
			WithHint("review it, run `coldship approve " + p.ID + "`, then `coldship apply " + p.ID + " --reason ...`")
	}
	if p.Action == "noop" {
		return emit(p, func() {
			fmt.Printf("Nothing to do: %s is already up to date (--redeploy restarts it anyway).\n", p.App)
		})
	}
	if !flagJSON {
		printPlan(p)
		fmt.Println()
	}
	res, err := c.Apply(cmd.Context(), p.ID, reason)
	if err != nil {
		return err
	}
	return emit(res, func() {
		printApply(res)
		if v, err := c.GetApp(cmd.Context(), p.App); err == nil {
			for _, u := range v.URLs {
				fmt.Printf("  → %s\n", u)
			}
		}
	})
}

func exportCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "export <app>",
		Short: "Print an app's spec as YAML, to keep in Git or deploy with -f",
		Long: `Print an app's spec as YAML. Secret values are shown as <redacted>;
deploying the file with -f keeps the current values for those.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			v, err := newClient().GetApp(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			s := v.Spec
			if s.Build != nil {
				s.Build.Commit = "" // build the branch, not this commit
			}
			if flagJSON {
				return emit(s, nil)
			}
			out, err := yaml.Marshal(s)
			if err != nil {
				return err
			}
			fmt.Print(string(out))
			return nil
		},
	}
}
