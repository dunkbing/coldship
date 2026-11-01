package engine

import (
	"context"
	"sync"
	"time"

	"github.com/dunkbing/coldship/internal/apierr"
	"github.com/dunkbing/coldship/internal/proxy"
	"github.com/dunkbing/coldship/internal/runtime"
)

const (
	metricsEvery = 15 * time.Second
	metricsKeep  = 240 // one hour
)

// MetricPoint is one sample of an app; rates are per second since the last.
type MetricPoint struct {
	T          time.Time `json:"t"`
	CPU        float64   `json:"cpu_percent"` // 100 = one core
	Memory     uint64    `json:"memory_bytes"`
	MemLimit   uint64    `json:"memory_limit_bytes"`
	NetIn      float64   `json:"net_in_bps"`
	NetOut     float64   `json:"net_out_bps"`
	DiskRead   float64   `json:"disk_read_bps"`
	DiskWrite  float64   `json:"disk_write_bps"`
	Requests   float64   `json:"requests_per_s"`
	Errors4xx  float64   `json:"errors_4xx_per_s"`
	Errors5xx  float64   `json:"errors_5xx_per_s"`
	AvgLatency float64   `json:"avg_latency_ms"`
}

// Metrics is what the API returns for an app.
type Metrics struct {
	IntervalSeconds int           `json:"interval_seconds"`
	Container       bool          `json:"container"` // false for static sites
	HTTP            bool          `json:"http"`      // served through the proxy
	Points          []MetricPoint `json:"points"`
}

type appSeries struct {
	points  []MetricPoint
	last    *runtime.RawStats
	lastReq *proxy.HostCounters
}

type metricsStore struct {
	mu   sync.Mutex
	apps map[string]*appSeries
}

type statsReader interface {
	ContainerStats(ctx context.Context, name string) (runtime.RawStats, error)
}

type httpStatsReader interface {
	HTTPStats(ctx context.Context) (map[string]proxy.HostCounters, error)
}

// RunMetrics samples every app until ctx ends.
func (e *Engine) RunMetrics(ctx context.Context) {
	t := time.NewTicker(metricsEvery)
	defer t.Stop()
	for {
		e.sampleMetrics(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (e *Engine) sampleMetrics(ctx context.Context) {
	apps, err := e.st.ListApps(ctx)
	if err != nil {
		return
	}
	var hosts map[string]proxy.HostCounters
	if hr, ok := e.px.(httpStatsReader); ok && e.px != nil {
		hosts, _ = hr.HTTPStats(ctx)
	}
	sr, _ := e.rt.(statsReader)
	now := e.Now()
	e.metrics.mu.Lock()
	defer e.metrics.mu.Unlock()
	live := map[string]bool{}
	for _, a := range apps {
		if a.Spec == nil || a.CurrentDeployment == "" {
			continue
		}
		live[a.Name] = true
		s := e.metrics.apps[a.Name]
		if s == nil {
			s = &appSeries{}
			e.metrics.apps[a.Name] = s
		}
		p := MetricPoint{T: now}
		have := false
		if sr != nil && !e.isStatic(ctx, &a) {
			if raw, err := sr.ContainerStats(ctx, runtime.ContainerName(a.Name)); err == nil {
				p.Memory, p.MemLimit = raw.Mem, raw.MemLimit
				if prev := s.last; prev != nil && raw.At.After(prev.At) {
					secs := raw.At.Sub(prev.At).Seconds()
					if dSys := float64(raw.SysNano) - float64(prev.SysNano); dSys > 0 && raw.CPUNanos >= prev.CPUNanos {
						p.CPU = float64(raw.CPUNanos-prev.CPUNanos) / dSys * float64(raw.CPUs) * 100
					}
					p.NetIn, p.NetOut = rate(raw.NetRx, prev.NetRx, secs), rate(raw.NetTx, prev.NetTx, secs)
					p.DiskRead, p.DiskWrite = rate(raw.DiskRead, prev.DiskRead, secs), rate(raw.DiskOut, prev.DiskOut, secs)
					have = true
				}
				s.last = &raw
			}
		}
		if hosts != nil && len(a.Spec.Domains) > 0 {
			var c proxy.HostCounters
			c.ByClass = map[string]uint64{}
			for _, d := range a.Spec.Domains {
				h := hosts[d]
				c.Requests += h.Requests
				c.DurationSec += h.DurationSec
				for k, v := range h.ByClass {
					c.ByClass[k] += v
				}
			}
			if prev := s.lastReq; prev != nil && c.Requests >= prev.Requests {
				secs := metricsEvery.Seconds()
				n := float64(c.Requests - prev.Requests)
				p.Requests = n / secs
				p.Errors4xx = rate(c.ByClass["4xx"], prev.ByClass["4xx"], secs)
				p.Errors5xx = rate(c.ByClass["5xx"], prev.ByClass["5xx"], secs)
				if n > 0 {
					p.AvgLatency = (c.DurationSec - prev.DurationSec) / n * 1000
				}
				have = true
			}
			s.lastReq = &c
		}
		if have {
			s.points = append(s.points, p)
			if len(s.points) > metricsKeep {
				s.points = s.points[len(s.points)-metricsKeep:]
			}
		}
	}
	for name := range e.metrics.apps {
		if !live[name] {
			delete(e.metrics.apps, name)
		}
	}
}

func rate(now, prev uint64, secs float64) float64 {
	if now < prev || secs <= 0 {
		return 0 // counters reset (a new container)
	}
	return float64(now-prev) / secs
}

// AppMetrics returns the last hour of an app's samples.
func (e *Engine) AppMetrics(ctx context.Context, name string) (*Metrics, error) {
	a, err := e.st.GetApp(ctx, name)
	if err != nil {
		return nil, err
	}
	if a == nil {
		return nil, apierr.NotFound("app", name)
	}
	m := &Metrics{IntervalSeconds: int(metricsEvery.Seconds()), Container: !e.isStatic(ctx, a),
		HTTP: e.px != nil && a.Spec != nil && len(a.Spec.Domains) > 0, Points: []MetricPoint{}}
	e.metrics.mu.Lock()
	if s := e.metrics.apps[name]; s != nil {
		m.Points = append(m.Points, s.points...)
	}
	e.metrics.mu.Unlock()
	return m, nil
}
