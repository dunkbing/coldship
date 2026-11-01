// Package hostinfo reads the server's CPU, memory, swap, disk and uptime
// from /proc and statfs. Coldship runs directly on the host (as a service),
// so these are the host's numbers. On other systems the fields stay zero.
package hostinfo

import (
	"bufio"
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type Info struct {
	Hostname string     `json:"hostname"`
	OS       string     `json:"os"`
	Kernel   string     `json:"kernel"`
	Arch     string     `json:"arch"`
	CPUs     int        `json:"cpus"`
	CPUUsed  float64    `json:"cpu_used_percent"` // over a short sample
	Load     [3]float64 `json:"load"`
	UptimeS  int64      `json:"uptime_seconds"`
	Memory   Usage      `json:"memory"`
	Swap     Usage      `json:"swap"`
	// Disks are the filesystems holding Coldship's data and the container
	// engine's images and volumes (one entry if they are the same).
	Disks []Disk `json:"disks"`
}

// Usage is bytes used out of total.
type Usage struct {
	Total uint64 `json:"total"`
	Used  uint64 `json:"used"`
}

type Disk struct {
	Path string `json:"path"`
	For  string `json:"for"`
	Usage
}

// Read samples CPU usage over sample, so it takes that long.
func Read(sample time.Duration, paths map[string]string) Info {
	in := Info{Arch: runtime.GOARCH, CPUs: runtime.NumCPU()}
	in.Hostname, _ = os.Hostname()
	in.OS = osName()
	if b, err := os.ReadFile("/proc/sys/kernel/osrelease"); err == nil {
		in.Kernel = strings.TrimSpace(string(b))
	}
	if b, err := os.ReadFile("/proc/loadavg"); err == nil {
		f := strings.Fields(string(b))
		for i := 0; i < 3 && i < len(f); i++ {
			in.Load[i], _ = strconv.ParseFloat(f[i], 64)
		}
	}
	if b, err := os.ReadFile("/proc/uptime"); err == nil {
		if f := strings.Fields(string(b)); len(f) > 0 {
			up, _ := strconv.ParseFloat(f[0], 64)
			in.UptimeS = int64(up)
		}
	}
	in.Memory, in.Swap = memory()
	in.CPUUsed = cpuUsed(sample)
	seen := map[uint64]int{}
	keys := make([]string, 0, len(paths))
	for p := range paths {
		keys = append(keys, p)
	}
	sort.Strings(keys)
	for _, path := range keys {
		what := paths[path]
		var st syscall.Statfs_t
		if path == "" || syscall.Statfs(path, &st) != nil {
			continue
		}
		total := uint64(st.Blocks) * uint64(st.Bsize)
		free := uint64(st.Bavail) * uint64(st.Bsize)
		// The same filesystem shows up once, for everything it holds.
		var key uint64
		if fi, err := os.Stat(path); err == nil {
			if sys, ok := fi.Sys().(*syscall.Stat_t); ok {
				key = uint64(sys.Dev)
			}
		}
		if i, ok := seen[key]; ok {
			in.Disks[i].For += ", " + what
			continue
		}
		seen[key] = len(in.Disks)
		in.Disks = append(in.Disks, Disk{Path: path, For: what, Usage: Usage{Total: total, Used: total - free}})
	}
	return in
}

func osName() string {
	f, err := os.Open("/etc/os-release")
	if err != nil {
		return runtime.GOOS
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if v, ok := strings.CutPrefix(sc.Text(), "PRETTY_NAME="); ok {
			return strings.Trim(v, `"`)
		}
	}
	return runtime.GOOS
}

func memory() (mem, swap Usage) {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return
	}
	defer f.Close()
	kb := map[string]uint64{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		name, rest, ok := strings.Cut(sc.Text(), ":")
		if !ok {
			continue
		}
		if fs := strings.Fields(rest); len(fs) > 0 {
			kb[name], _ = strconv.ParseUint(fs[0], 10, 64)
		}
	}
	mem = Usage{Total: kb["MemTotal"] * 1024, Used: (kb["MemTotal"] - kb["MemAvailable"]) * 1024}
	swap = Usage{Total: kb["SwapTotal"] * 1024, Used: (kb["SwapTotal"] - kb["SwapFree"]) * 1024}
	return
}

// cpuUsed compares /proc/stat's totals before and after sample.
func cpuUsed(sample time.Duration) float64 {
	read := func() (idle, total uint64, ok bool) {
		b, err := os.ReadFile("/proc/stat")
		if err != nil {
			return 0, 0, false
		}
		line, _, _ := strings.Cut(string(b), "\n")
		f := strings.Fields(line)
		if len(f) < 5 || f[0] != "cpu" {
			return 0, 0, false
		}
		for i, v := range f[1:] {
			n, _ := strconv.ParseUint(v, 10, 64)
			total += n
			if i == 3 || i == 4 { // idle, iowait
				idle += n
			}
		}
		return idle, total, true
	}
	i1, t1, ok1 := read()
	time.Sleep(sample)
	i2, t2, ok2 := read()
	if !ok1 || !ok2 || t2 <= t1 {
		return 0
	}
	return 100 * float64((t2-t1)-(i2-i1)) / float64(t2-t1)
}
