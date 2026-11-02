package spec

import (
	"reflect"
	"slices"

	"github.com/dunkbing/coldship/internal/apierr"
)

// Patch changes an app without restating its whole spec: what `coldship
// deploy --env K=V` and similar flags send. The server merges it into the
// app's current spec, so secrets never have to leave the server. A patch only
// sets and adds; removing a volume or database (which deletes data) needs a
// full spec or a delete plan.
type Patch struct {
	Name string `json:"name" jsonschema:"app name; created if it doesn't exist"`
	// Source: setting one clears the other.
	Image string `json:"image,omitempty" jsonschema:"run this container image (replaces build)"`
	Build *Build `json:"build,omitempty" jsonschema:"build from this Git repository (replaces image)"`

	Stage    Stage  `json:"stage,omitempty" jsonschema:"production or staging"`
	Port     int    `json:"port,omitempty" jsonschema:"port the app listens on. Detected for apps built from Git when omitted"`
	HostPort int    `json:"host_port,omitempty"`
	Memory   string `json:"memory,omitempty"`
	// RemoveHostPort and RemoveMemory clear those settings.
	RemoveHostPort bool `json:"remove_host_port,omitempty"`
	RemoveMemory   bool `json:"remove_memory,omitempty"`

	Env           map[string]string `json:"env,omitempty" jsonschema:"environment variables to set or change"`
	UnsetEnv      []string          `json:"unset_env,omitempty" jsonschema:"environment variables to remove"`
	AddDomains    []string          `json:"add_domains,omitempty"`
	RemoveDomains []string          `json:"remove_domains,omitempty"`
	AddVolumes    []Volume          `json:"add_volumes,omitempty"`
	AddDatabases  []Database        `json:"add_databases,omitempty" jsonschema:"databases to add, e.g. {engine: postgres}. Name defaults to main"`
	AddUses       []string          `json:"add_uses,omitempty" jsonschema:"data services to connect, e.g. maindb or reports=REPORTS_URL (see coldship data list)"`
	RemoveUses    []string          `json:"remove_uses,omitempty" jsonschema:"data services to disconnect (their data stays)"`
	SetJobs       []Job             `json:"set_jobs,omitempty" jsonschema:"scheduled jobs to add, or replace by name"`
	RemoveJobs    []string          `json:"remove_jobs,omitempty" jsonschema:"scheduled jobs to remove, by name"`
}

// OnlyJobs reports whether the patch changes nothing but jobs.
func (p *Patch) OnlyJobs() bool {
	q := *p
	q.Name, q.SetJobs, q.RemoveJobs = "", nil, nil
	return (len(p.SetJobs) > 0 || len(p.RemoveJobs) > 0) && reflect.ValueOf(q).IsZero()
}

// Apply returns cur (nil for a new app) with the patch applied. cur is not
// modified.
func (p *Patch) Apply(cur *App) (*App, error) {
	var a *App
	if cur == nil {
		a = &App{Name: p.Name}
		if p.Image == "" && p.Build == nil {
			return nil, apierr.Invalid("image", "app %s doesn't exist yet; give it an image or a Git repository to build", p.Name).
				WithHint("coldship deploy --image nginx:1.27 --port 80, or run coldship deploy inside a Git repository")
		}
	} else {
		a = cur.Clone()
	}
	switch {
	case p.Image != "" && p.Build != nil:
		return nil, apierr.Invalid("image", "give either an image or a Git repository, not both")
	case p.Image != "":
		a.Image, a.Build, a.OnPush = p.Image, nil, nil
	case p.Build != nil:
		b := p.Build.Clone()
		if a.Build != nil && a.Build.Git == b.Git {
			// Keep build settings (context, Dockerfile, args, auto) the
			// patch doesn't mention.
			kept := a.Build.Clone()
			kept.Ref, kept.Commit = b.Ref, ""
			if b.Context != "" {
				kept.Context = b.Context
			}
			if b.Dockerfile != "" {
				kept.Dockerfile = b.Dockerfile
			}
			b = kept
		}
		a.Image, a.Build = "", b
	case a.Build != nil && !p.OnlyJobs():
		// Redeploying builds the branch's current tip, not the old commit.
		a.Build.Commit = ""
	}
	if p.Stage != "" {
		a.Stage = p.Stage
	}
	if p.Port != 0 {
		a.Port = p.Port
	}
	if p.HostPort != 0 {
		a.HostPort = p.HostPort
	}
	if p.RemoveHostPort {
		a.HostPort = 0
	}
	if p.Memory != "" {
		a.Memory = p.Memory
	}
	if p.RemoveMemory {
		a.Memory = ""
	}
	if len(p.Env) > 0 && a.Env == nil {
		a.Env = map[string]string{}
	}
	for k, v := range p.Env {
		a.Env[k] = v
	}
	for _, k := range p.UnsetEnv {
		delete(a.Env, k)
	}
	a.Domains = addAll(a.Domains, p.AddDomains)
	a.Domains = slices.DeleteFunc(a.Domains, func(d string) bool { return slices.Contains(p.RemoveDomains, d) })
	for _, v := range p.AddVolumes {
		if !hasVolume(a.Volumes, v.Name) {
			a.Volumes = append(a.Volumes, v)
		}
	}
	for _, u := range p.AddUses {
		name, _ := ParseUse(u)
		a.Uses = slices.DeleteFunc(a.Uses, func(x string) bool { n, _ := ParseUse(x); return n == name })
		a.Uses = append(a.Uses, u)
	}
	a.Uses = slices.DeleteFunc(a.Uses, func(x string) bool {
		n, _ := ParseUse(x)
		return slices.ContainsFunc(p.RemoveUses, func(r string) bool { rn, _ := ParseUse(r); return rn == n })
	})
	for _, d := range p.AddDatabases {
		if d.Name == "" {
			d.Name = "main"
		}
		if !hasDatabase(a.Databases, d.Name) {
			a.Databases = append(a.Databases, d)
		}
	}
	for _, name := range p.RemoveJobs {
		if !slices.ContainsFunc(a.Jobs, func(j Job) bool { return j.Name == name }) {
			return nil, apierr.Invalid("remove_jobs", "%s has no job %q", a.Name, name)
		}
	}
	a.Jobs = slices.DeleteFunc(a.Jobs, func(j Job) bool { return slices.Contains(p.RemoveJobs, j.Name) })
	for _, j := range p.SetJobs {
		if i := slices.IndexFunc(a.Jobs, func(o Job) bool { return o.Name == j.Name }); i >= 0 {
			a.Jobs[i] = j
		} else {
			a.Jobs = append(a.Jobs, j)
		}
	}
	return a, nil
}

func addAll(list, add []string) []string {
	for _, s := range add {
		if !slices.Contains(list, s) {
			list = append(list, s)
		}
	}
	return list
}

func hasVolume(vs []Volume, name string) bool {
	return slices.ContainsFunc(vs, func(v Volume) bool { return v.Name == name })
}

func hasDatabase(ds []Database, name string) bool {
	return slices.ContainsFunc(ds, func(d Database) bool { return d.Name == name })
}
