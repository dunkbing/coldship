package hostinfo

import (
	"testing"
	"time"
)

func TestRead(t *testing.T) {
	in := Read(50*time.Millisecond, map[string]string{"/": "root", "/tmp": "tmp"})
	if in.CPUs == 0 || in.Memory.Total == 0 || in.Memory.Used > in.Memory.Total || len(in.Disks) == 0 {
		t.Fatalf("%+v", in)
	}
	if in.CPUUsed < 0 || in.CPUUsed > 100 {
		t.Fatalf("cpu %v", in.CPUUsed)
	}
	t.Logf("%s, %d CPUs, cpu %.1f%%, mem %d/%d MB, disks %+v", in.OS, in.CPUs, in.CPUUsed, in.Memory.Used>>20, in.Memory.Total>>20, in.Disks)
}
