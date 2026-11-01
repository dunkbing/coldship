package engine

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/dunkbing/coldship/internal/builder"
	"github.com/dunkbing/coldship/internal/proxy"
	"github.com/dunkbing/coldship/internal/runtime"
	"github.com/dunkbing/coldship/internal/spec"
	"github.com/dunkbing/coldship/internal/store"
)

func TestMetricsRates(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "h.db"), testKeys)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	rt, px := runtime.NewFake(), &proxy.Fake{}
	e := New(st, rt, builder.NewFake(), px)
	deploy(t, e, &spec.App{Name: "web", Image: "web:1", Port: 80, Domains: []string{"web.example.com"}})
	ctx := context.Background()
	t0 := time.Now()
	rt.Stats["coldship-web"] = runtime.RawStats{At: t0, CPUNanos: 0, SysNano: 0, CPUs: 4, Mem: 100 << 20, NetRx: 0}
	px.Hosts = map[string]proxy.HostCounters{"web.example.com": {Requests: 100, ByClass: map[string]uint64{"2xx": 100}, DurationSec: 1}}
	e.sampleMetrics(ctx)
	// 15s later: a quarter of the host's CPU time, 15 MB in, 150 more requests (15 of them 5xx).
	rt.Stats["coldship-web"] = runtime.RawStats{At: t0.Add(15 * time.Second), CPUNanos: 15e9, SysNano: 60e9, CPUs: 4, Mem: 120 << 20, NetRx: 15 << 20}
	px.Hosts = map[string]proxy.HostCounters{"web.example.com": {Requests: 250, ByClass: map[string]uint64{"2xx": 235, "5xx": 15}, DurationSec: 4}}
	e.sampleMetrics(ctx)
	m, err := e.AppMetrics(ctx, "web")
	if err != nil || len(m.Points) != 1 || !m.HTTP || !m.Container {
		t.Fatalf("%+v %v", m, err)
	}
	p := m.Points[0]
	if p.CPU != 100 || p.Memory != 120<<20 || p.NetIn != float64(1<<20) || p.Requests != 10 || p.Errors5xx != 1 || p.AvgLatency != 20 {
		t.Fatalf("point: %+v", p)
	}
}
