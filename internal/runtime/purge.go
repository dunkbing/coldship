package runtime

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"
)

// PurgePlan lists what Coldship created in Docker.
type PurgePlan struct {
	Containers []string // app, database, data service and proxy containers
	Volumes    []string // volumes those containers use, and the proxy's
	Images     []string // images built from Git (coldship-build/*)
}

// proxyContainer is the proxy package's container; runtime can't import it.
const proxyContainer = "coldship-proxy"

// PlanPurge finds Coldship's containers (by label, plus the proxy), the
// named volumes they mount, and the images it built. It only looks at what
// Coldship made: an unrelated volume named coldship-something is left alone.
func (d *Docker) PlanPurge(ctx context.Context) (PurgePlan, error) {
	var p PurgePlan
	cli, err := d.api()
	if err != nil {
		return p, err
	}
	seen := map[string]bool{}
	vols := map[string]bool{"coldship-proxy-data": true, "coldship-proxy-config": true}
	for _, f := range []client.Filters{
		client.Filters{}.Add("label", "coldship.app"),
		client.Filters{}.Add("label", "coldship.data"),
		client.Filters{}.Add("label", AgentLabel),
		client.Filters{}.Add("label", JobLabel),
		client.Filters{}.Add("name", "^/?"+proxyContainer+"$"),
	} {
		res, err := cli.ContainerList(ctx, client.ContainerListOptions{All: true, Filters: f})
		if err != nil {
			return p, unavailable("could not list containers", err)
		}
		for _, c := range res.Items {
			name := c.ID
			if len(c.Names) > 0 {
				name = strings.TrimPrefix(c.Names[0], "/")
			}
			if seen[name] {
				continue
			}
			seen[name] = true
			p.Containers = append(p.Containers, name)
			for _, m := range c.Mounts {
				if m.Type == mount.TypeVolume && m.Name != "" {
					vols[m.Name] = true
				}
			}
		}
	}
	// Agents' home volumes (npm cache, agent settings), mounted only while
	// an agent runs.
	if vl, err := cli.VolumeList(ctx, client.VolumeListOptions{Filters: client.Filters{}.Add("name", AgentVolumePrefix)}); err == nil {
		for _, v := range vl.Items {
			if strings.HasPrefix(v.Name, AgentVolumePrefix) {
				vols[v.Name] = true
			}
		}
	}
	for v := range vols {
		if _, err := cli.VolumeInspect(ctx, v, client.VolumeInspectOptions{}); err == nil {
			p.Volumes = append(p.Volumes, v)
		}
	}
	imgs, err := cli.ImageList(ctx, client.ImageListOptions{Filters: client.Filters{}.Add("reference", "coldship-build/*")})
	if err != nil {
		return p, unavailable("could not list images", err)
	}
	for _, im := range imgs.Items {
		if len(im.RepoTags) > 0 {
			p.Images = append(p.Images, im.RepoTags...)
		} else {
			p.Images = append(p.Images, im.ID)
		}
	}
	sort.Strings(p.Containers)
	sort.Strings(p.Volumes)
	sort.Strings(p.Images)
	return p, nil
}

// Purge removes what PlanPurge found, and the Coldship network. With
// keepVolumes, volumes (app and database data) are left in place.
func (d *Docker) Purge(ctx context.Context, p PurgePlan, keepVolumes bool, log io.Writer) error {
	cli, err := d.api()
	if err != nil {
		return err
	}
	var failed []string
	for _, c := range p.Containers {
		if err := d.ForceRemove(ctx, c); err != nil {
			failed = append(failed, "container "+c+": "+err.Error())
		} else {
			fmt.Fprintf(log, "  removed container %s\n", c)
		}
	}
	if !keepVolumes {
		for _, v := range p.Volumes {
			if err := d.removeVolume(ctx, v); err != nil {
				failed = append(failed, "volume "+v+": "+err.Error())
			} else {
				fmt.Fprintf(log, "  removed volume %s\n", v)
			}
		}
	}
	for _, im := range p.Images {
		if _, err := cli.ImageRemove(ctx, im, client.ImageRemoveOptions{Force: true}); err != nil && !isNotFound(err) {
			failed = append(failed, "image "+im+": "+err.Error())
		}
	}
	if len(p.Images) > 0 {
		fmt.Fprintf(log, "  removed %d built image(s)\n", len(p.Images))
	}
	if _, err := cli.NetworkRemove(ctx, Network, client.NetworkRemoveOptions{}); err != nil && !isNotFound(err) {
		failed = append(failed, "network "+Network+": "+err.Error())
	}
	if len(failed) > 0 {
		return fmt.Errorf("some Docker resources couldn't be removed:\n  %s", strings.Join(failed, "\n  "))
	}
	return nil
}
