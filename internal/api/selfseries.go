package api

import (
	"github.com/hotid/hls-monitor/internal/metrics"
	"github.com/hotid/hls-monitor/internal/store"
	"github.com/hotid/hls-monitor/internal/types/metricname"
)

// RecordSelf writes what the server itself uses to the time series, for
// the overview: /metrics tells the same to whoever scrapes it, but keeps
// no history. The series belong to no stream.
func (s *Server) RecordSelf() {
	if s.cfg.Load == nil {
		return
	}
	load := s.cfg.Load.Status()
	now := s.now()
	sample := func(name metricname.Name, v float64) metrics.Sample {
		return metrics.Sample{Name: name, Agent: store.LocalAgentID(), Value: v, Time: now}
	}
	out := []metrics.Sample{
		sample(metricname.SelfCPUCores(), load.CPUCores),
		sample(metricname.SelfMemoryBytes(), float64(load.MemoryBytes)),
	}
	if load.CPUAllCores > 0 {
		out = append(out, sample(metricname.SelfCPUAllCores(), load.CPUAllCores))
	}
	s.mx.Write(out)
}
