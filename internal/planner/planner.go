// Package planner diffs a current spec against a desired one and classifies
// each change (destructive or not), Terraform-style.
package planner

import (
	"fmt"
	"sort"
	"strings"

	"hangar/internal/spec"
)

const (
	OpCreate   = "create"
	OpUpdate   = "update"
	OpDelete   = "delete"
	OpRedeploy = "redeploy"
)

type Change struct {
	Path        string `json:"path"`
	Op          string `json:"op"`
	From        any    `json:"from,omitempty"`
	To          any    `json:"to,omitempty"`
	Destructive bool   `json:"destructive,omitempty"`
	Reason      string `json:"reason,omitempty"`
}

type Result struct {
	Changes     []Change
	Destructive bool
	NoOp        bool
}

// Diff compares current (nil = app does not exist) with desired (nil = delete
// the app). Env values never appear in the output, only keys.
func Diff(cur, des *spec.App) Result {
	var cs []Change
	switch {
	case cur == nil && des == nil:
	case cur == nil:
		cs = append(cs, Change{Path: "app", Op: OpCreate, To: des.Name})
	case des == nil:
		c := Change{Path: "app", Op: OpDelete, From: cur.Name, Destructive: true, Reason: "stops and removes the app"}
		var parts []string
		if len(cur.Volumes) > 0 {
			parts = append(parts, "volumes "+strings.Join(volNames(cur.Volumes), ", "))
		}
		if len(cur.Databases) > 0 {
			parts = append(parts, "databases "+strings.Join(dbNames(cur.Databases), ", ")+" (snapshotted first)")
		}
		if len(parts) > 0 {
			c.Reason = fmt.Sprintf("stops and removes the app and deletes %s with all their data", strings.Join(parts, " and "))
		}
		cs = append(cs, c)
	default:
		cs = scalar(cs, "stage", cur.Stage, des.Stage)
		cs = scalar(cs, "image", cur.Image, des.Image)
		cs = buildDiff(cs, cur.Build, des.Build)
		cs = scalar(cs, "port", cur.Port, des.Port)
		cs = scalar(cs, "host_port", cur.HostPort, des.HostPort)
		cs = scalar(cs, "memory", cur.Memory, des.Memory)
		cs = setDiff(cs, "domains.", cur.Domains, des.Domains)
		var co, do spec.OnPush
		if cur.OnPush != nil {
			co = *cur.OnPush
		}
		if des.OnPush != nil {
			do = *des.OnPush
		}
		cs = scalar(cs, "on_push.action", co.Action, do.Action)
		cs = scalar(cs, "on_push.previews", co.Previews, do.Previews)
		cs = scalar(cs, "on_push.preview_ttl", co.PreviewTTL, do.PreviewTTL)
		var ch, dh spec.Health
		if cur.Health != nil {
			ch = *cur.Health
		}
		if des.Health != nil {
			dh = *des.Health
		}
		cs = scalar(cs, "health.path", ch.Path, dh.Path)
		cs = scalar(cs, "health.timeout", ch.Timeout, dh.Timeout)
		cs = envDiff(cs, cur.Env, des.Env)
		cs = volDiff(cs, cur.Volumes, des.Volumes)
		cs = dbDiff(cs, cur.Databases, des.Databases)
	}
	r := Result{Changes: cs, NoOp: len(cs) == 0}
	for _, c := range cs {
		r.Destructive = r.Destructive || c.Destructive
	}
	return r
}

func scalar[T comparable](cs []Change, path string, from, to T) []Change {
	var zero T
	switch {
	case from == to:
		return cs
	case from == zero:
		return append(cs, Change{Path: path, Op: OpCreate, To: to})
	case to == zero:
		return append(cs, Change{Path: path, Op: OpDelete, From: from})
	default:
		return append(cs, Change{Path: path, Op: OpUpdate, From: from, To: to})
	}
}

func setDiff(cs []Change, prefix string, cur, des []string) []Change {
	in := func(xs []string, x string) bool {
		for _, y := range xs {
			if y == x {
				return true
			}
		}
		return false
	}
	keys := map[string]bool{}
	for _, x := range cur {
		keys[x] = true
	}
	for _, x := range des {
		keys[x] = true
	}
	for _, k := range sortedKeys(keys) {
		switch {
		case in(cur, k) && !in(des, k):
			cs = append(cs, Change{Path: prefix + k, Op: OpDelete, Reason: "stops routing this hostname to the app"})
		case !in(cur, k) && in(des, k):
			cs = append(cs, Change{Path: prefix + k, Op: OpCreate})
		}
	}
	return cs
}

func buildDiff(cs []Change, cur, des *spec.Build) []Change {
	switch {
	case cur == nil && des == nil:
		return cs
	case cur == nil:
		return append(cs, Change{Path: "build", Op: OpCreate, To: buildSummary(des)})
	case des == nil:
		return append(cs, Change{Path: "build", Op: OpDelete, From: buildSummary(cur)})
	}
	cs = scalar(cs, "build.git", cur.Git, des.Git)
	cs = scalar(cs, "build.ref", cur.Ref, des.Ref)
	cs = scalar(cs, "build.commit", cur.Commit, des.Commit)
	cs = scalar(cs, "build.context", cur.Context, des.Context)
	cs = scalar(cs, "build.dockerfile", cur.Dockerfile, des.Dockerfile)
	return mapDiff(cs, "build.args.", cur.Args, des.Args, false)
}

func buildSummary(b *spec.Build) string {
	s := b.Git
	if b.Ref != "" {
		s += "@" + b.Ref
	}
	if b.Commit != "" {
		s += " " + b.Commit
	}
	return s
}

func envDiff(cs []Change, cur, des map[string]string) []Change {
	return mapDiff(cs, "env.", cur, des, true)
}

// mapDiff diffs two string maps. With redact, values never appear in the
// output, only which keys changed.
func mapDiff(cs []Change, prefix string, cur, des map[string]string, redact bool) []Change {
	keys := map[string]bool{}
	for k := range cur {
		keys[k] = true
	}
	for k := range des {
		keys[k] = true
	}
	for _, k := range sortedKeys(keys) {
		cv, inCur := cur[k]
		dv, inDes := des[k]
		switch {
		case inCur && !inDes:
			c := Change{Path: prefix + k, Op: OpDelete}
			if !redact {
				c.From = cv
			}
			cs = append(cs, c)
		case !inCur && inDes:
			c := Change{Path: prefix + k, Op: OpCreate}
			if !redact {
				c.To = dv
			}
			cs = append(cs, c)
		case cv != dv && redact:
			cs = append(cs, Change{Path: prefix + k, Op: OpUpdate, Reason: "value changed (redacted)"})
		case cv != dv:
			cs = append(cs, Change{Path: prefix + k, Op: OpUpdate, From: cv, To: dv})
		}
	}
	return cs
}

func volDiff(cs []Change, cur, des []spec.Volume) []Change {
	cm, dm := map[string]string{}, map[string]string{}
	keys := map[string]bool{}
	for _, v := range cur {
		cm[v.Name] = v.Path
		keys[v.Name] = true
	}
	for _, v := range des {
		dm[v.Name] = v.Path
		keys[v.Name] = true
	}
	for _, k := range sortedKeys(keys) {
		cp, inCur := cm[k]
		dp, inDes := dm[k]
		switch {
		case inCur && !inDes:
			cs = append(cs, Change{Path: "volumes." + k, Op: OpDelete, From: cp, Destructive: true,
				Reason: "volume " + k + " and all data in it will be deleted"})
		case !inCur && inDes:
			cs = append(cs, Change{Path: "volumes." + k, Op: OpCreate, To: dp})
		case cp != dp:
			cs = append(cs, Change{Path: "volumes." + k, Op: OpUpdate, From: cp, To: dp})
		}
	}
	return cs
}

func dbDiff(cs []Change, cur, des []spec.Database) []Change {
	cm, dm := map[string]spec.Database{}, map[string]spec.Database{}
	keys := map[string]bool{}
	for _, d := range cur {
		cm[d.Name] = d
		keys[d.Name] = true
	}
	for _, d := range des {
		dm[d.Name] = d
		keys[d.Name] = true
	}
	for _, k := range sortedKeys(keys) {
		c, inCur := cm[k]
		d, inDes := dm[k]
		switch {
		case inCur && !inDes:
			cs = append(cs, Change{Path: "databases." + k, Op: OpDelete, From: c.Engine + " " + c.Version, Destructive: true,
				Reason: "database " + k + " and all its data will be deleted (a snapshot is taken first)"})
		case !inCur && inDes:
			cs = append(cs, Change{Path: "databases." + k, Op: OpCreate, To: d.Engine + " " + d.Version, Reason: "connection URL injected as " + d.Env})
		default:
			cs = scalar(cs, "databases."+k+".engine", c.Engine, d.Engine)
			cs = scalar(cs, "databases."+k+".version", c.Version, d.Version)
			cs = scalar(cs, "databases."+k+".env", c.Env, d.Env)
		}
	}
	return cs
}

// RemovedDatabases lists databases in cur but not in des.
func RemovedDatabases(cur, des *spec.App) []spec.Database {
	if cur == nil {
		return nil
	}
	keep := map[string]bool{}
	if des != nil {
		for _, d := range des.Databases {
			keep[d.Name] = true
		}
	}
	var out []spec.Database
	for _, d := range cur.Databases {
		if !keep[d.Name] {
			out = append(out, d)
		}
	}
	return out
}

func dbNames(ds []spec.Database) []string {
	out := make([]string, len(ds))
	for i, d := range ds {
		out[i] = d.Name
	}
	return out
}

// RemovedVolumes lists volumes present in cur but not in des.
func RemovedVolumes(cur, des *spec.App) []string {
	if cur == nil {
		return nil
	}
	keep := map[string]bool{}
	if des != nil {
		for _, v := range des.Volumes {
			keep[v.Name] = true
		}
	}
	var out []string
	for _, v := range cur.Volumes {
		if !keep[v.Name] {
			out = append(out, v.Name)
		}
	}
	return out
}

func volNames(vs []spec.Volume) []string {
	out := make([]string, len(vs))
	for i, v := range vs {
		out[i] = v.Name
	}
	return out
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
