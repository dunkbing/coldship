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
		if len(cur.Volumes) > 0 {
			c.Reason = fmt.Sprintf("stops and removes the app and deletes volumes %s with all their data", strings.Join(volNames(cur.Volumes), ", "))
		}
		cs = append(cs, c)
	default:
		cs = scalar(cs, "stage", cur.Stage, des.Stage)
		cs = scalar(cs, "image", cur.Image, des.Image)
		cs = scalar(cs, "port", cur.Port, des.Port)
		cs = scalar(cs, "host_port", cur.HostPort, des.HostPort)
		cs = scalar(cs, "memory", cur.Memory, des.Memory)
		cs = envDiff(cs, cur.Env, des.Env)
		cs = volDiff(cs, cur.Volumes, des.Volumes)
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

func envDiff(cs []Change, cur, des map[string]string) []Change {
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
			cs = append(cs, Change{Path: "env." + k, Op: OpDelete})
		case !inCur && inDes:
			cs = append(cs, Change{Path: "env." + k, Op: OpCreate})
		case cv != dv:
			cs = append(cs, Change{Path: "env." + k, Op: OpUpdate, Reason: "value changed (redacted)"})
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
