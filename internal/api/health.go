package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/hotid/hls-monitor/internal/agentproto"
	"github.com/hotid/hls-monitor/internal/probe"
	"github.com/hotid/hls-monitor/internal/store"
)

// agentReport is what a remote agent last told about itself.
type agentReport struct {
	status agentproto.Status
	at     time.Time
}

// noteAgentStatus keeps the status an agent sent with a request. It is not
// stored: the agent repeats it every few seconds.
func (s *Server) noteAgentStatus(id int64, header string) {
	if header == "" || len(header) > 4096 {
		return
	}
	var st agentproto.Status
	if json.Unmarshal([]byte(header), &st) != nil {
		return
	}
	s.reportsMu.Lock()
	s.reports[id] = agentReport{status: st, at: s.now()}
	s.reportsMu.Unlock()
}

// agentStatus is the latest status of an agent: of the server's own
// checks for the local one, what it reported for a remote one, nil when
// it has reported nothing lately.
func (s *Server) agentStatus(a *store.Agent) *agentproto.Status {
	if a.Kind == store.AgentLocal {
		load := s.cfg.Load.Status()
		st := &agentproto.Status{CPUCores: load.CPUCores, CPULimit: load.CPULimit, MemoryBytes: load.MemoryBytes}
		if load.Overloaded {
			st.Overloaded = load.Reason
		}
		if s.local != nil {
			rs := s.local.Stats()
			st.Streams, st.Behind = rs.Streams, rs.Behind
		}
		return st
	}
	s.reportsMu.Lock()
	defer s.reportsMu.Unlock()
	r, ok := s.reports[a.ID]
	if !ok || s.now().Sub(r.at) > s.cfg.AgentOfflineAfter {
		return nil
	}
	st := r.status
	return &st
}

// readiness is the answer of /readyz.
type readiness struct {
	// Status is "ok", "degraded" when the server works but something
	// needs attention, "unavailable" when it cannot do its job.
	Status string `json:"status"`
	// Checks: database, time_series ("ok", "down", "disabled"), load
	// ("ok" or what the process is short of), checks ("ok", "behind").
	Checks map[string]string `json:"checks"`
}

// readyz godoc
//
//	@Summary		Readiness: whether the server can do its job
//	@Description	Answers 503 when the database does not respond. An overloaded server, checks that fall behind and an unreachable time series storage are reported in the body with status "degraded" and code 200: the server still works.
//	@Tags			system
//	@Produce		json
//	@Success		200	{object}	readiness
//	@Failure		503	{object}	readiness
//	@Router			/readyz [get]
func (s *Server) readyz(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 3*time.Second)
	defer cancel()
	out := readiness{Status: "ok", Checks: map[string]string{"database": "ok", "time_series": "ok", "load": "ok", "checks": "ok"}}
	code := http.StatusOK
	if err := s.st.Ping(ctx); err != nil {
		out.Checks["database"] = "down"
		out.Status, code = "unavailable", http.StatusServiceUnavailable
	}
	switch {
	case !s.mx.Enabled():
		out.Checks["time_series"] = "disabled"
	case !s.mx.Healthy(ctx):
		out.Checks["time_series"] = "down"
	}
	if load := s.cfg.Load.Status(); load.Overloaded {
		out.Checks["load"] = load.Reason
	}
	if s.local != nil {
		if rs := s.local.Stats(); rs.Behind > 0 {
			out.Checks["checks"] = "behind"
		}
	}
	if code == http.StatusOK {
		for name, v := range out.Checks {
			if v != "ok" && !(name == "time_series" && v == "disabled") {
				out.Status = "degraded"
			}
		}
	}
	c.JSON(code, out)
}

// selfMetrics godoc
//
//	@Summary		The server's own metrics in the Prometheus text format
//	@Description	Load of the process, the checks it makes, storing of results, storage size and the state of every agent with its queue of undelivered results. Figures of the monitored streams are not here: see the external monitoring API.
//	@Tags			system
//	@Produce		plain
//	@Param			Authorization	header		string	true	"Bearer API token"
//	@Success		200	{string}	string
//	@Failure		401	{object}	apiError
//	@Router			/metrics [get]
func (s *Server) selfMetrics(c *gin.Context) {
	ctx := c.Request.Context()
	w := &promText{}
	w.metric("hlsmon_build_info", "gauge", "Version of the server.", 1, "version", s.cfg.Version)
	s.processMetrics(w)
	s.storageMetrics(ctx, w)
	agents, err := s.st.ListAgents(ctx)
	if s.storeError(c, err) {
		return
	}
	s.agentMetrics(w, agents)
	c.Data(http.StatusOK, "text/plain; version=0.0.4; charset=utf-8", []byte(w.b.String()))
}

// promText writes metrics in the text format of Prometheus.
type promText struct{ b strings.Builder }

// metric writes one sample; help and typ go before the first sample of a
// name, and are empty for the rest.
func (w *promText) metric(name, typ, help string, v float64, labels ...string) {
	b := &w.b
	if help != "" {
		fmt.Fprintf(b, "# HELP %s %s\n# TYPE %s %s\n", name, help, name, typ)
	}
	b.WriteString(name)
	if len(labels) > 0 {
		b.WriteByte('{')
		for i := 0; i+1 < len(labels); i += 2 {
			if i > 0 {
				b.WriteByte(',')
			}
			fmt.Fprintf(b, "%s=%q", labels[i], labels[i+1])
		}
		b.WriteByte('}')
	}
	fmt.Fprintf(b, " %g\n", v)
}

func flag(v bool) float64 {
	if v {
		return 1
	}
	return 0
}

// processMetrics tells how the server process fares and how its own
// checks keep up.
func (s *Server) processMetrics(w *promText) {
	metric := w.metric
	load := s.cfg.Load.Status()
	metric("hlsmon_overloaded", "gauge", "1 while the server process does not get the processor time it needs.", flag(load.Overloaded))
	metric("hlsmon_cpu_cores", "gauge", "Processors used by the server process.", load.CPUCores)
	metric("hlsmon_cpu_limit_cores", "gauge", "Processors the server process may use.", load.CPULimit)
	metric("hlsmon_cpu_throttled_ratio", "gauge", "Seconds per second the kernel kept processors from the container for having used up its quota.", load.CPUThrottled)
	metric("hlsmon_wakeup_delay_seconds", "gauge", "How late the process wakes up when it asked to be woken, over the last seconds.", load.Delay, "quantile", "0.9")
	metric("hlsmon_wakeup_delay_seconds", "", "", load.DelayMax, "quantile", "1")
	metric("hlsmon_scheduler_delay_seconds", "gauge", "How long the slowest one in a hundred goroutines waited for a processor.", load.SchedulerDelay)
	metric("hlsmon_memory_bytes", "gauge", "Memory of the container, or of the process when there is no limit.", float64(load.MemoryBytes))
	metric("hlsmon_memory_limit_bytes", "gauge", "Memory limit of the container; 0 when there is none.", float64(load.MemoryLimit))
	metric("hlsmon_goroutines", "gauge", "Goroutines of the server process.", float64(load.Goroutines))

	used, limit, waiting := probe.BodyMemory()
	metric("hlsmon_body_memory_bytes", "gauge", "Bytes of segment bodies held in memory by the server's own checks.", float64(used))
	metric("hlsmon_body_memory_limit_bytes", "gauge", "How many may be held.", float64(limit))
	metric("hlsmon_body_memory_waiting", "gauge", "Downloads waiting for room.", float64(waiting))

	if s.local != nil {
		rs := s.local.Stats()
		metric("hlsmon_local_streams", "gauge", "Streams the server checks itself.", float64(rs.Streams))
		metric("hlsmon_local_checks_running", "gauge", "Check cycles running now.", float64(rs.Running))
		metric("hlsmon_local_streams_behind", "gauge", "Streams whose checks start later than they should by more than their interval.", float64(rs.Behind))
		metric("hlsmon_local_check_lag_seconds", "gauge", "How late check cycles start.", rs.LagP95, "quantile", "0.95")
		metric("hlsmon_local_check_lag_seconds", "", "", rs.LagMax, "quantile", "1")
		metric("hlsmon_local_checks_total", "counter", "Check cycles made by the server.", float64(rs.Cycles))
		metric("hlsmon_local_checks_crashed_total", "counter", "Check cycles that ended in a fault of the checking code; see the server log.", float64(rs.Crashed))
		metric("hlsmon_local_checks_short_total", "counter", "Check cycles that started late and looked only at the latest segment of each playlist.", float64(rs.Short))
		metric("hlsmon_local_checks_unreliable_total", "counter", "Of them, made while the server was overloaded.", float64(rs.Unreliable))
	}

}

// storageMetrics tells what became of the results and how full the
// database, the journal and the time series storage are.
func (s *Server) storageMetrics(ctx context.Context, w *promText) {
	metric := w.metric
	res := s.ing.Stats()
	metric("hlsmon_results_total", "counter", "Check results by what became of them.", float64(res.Stored), "outcome", "stored")
	metric("hlsmon_results_total", "", "", float64(res.Repeated), "outcome", "repeated")
	metric("hlsmon_results_total", "", "", float64(res.Failed), "outcome", "failed")

	u := s.st.Usage(ctx)
	metric("hlsmon_database_bytes", "gauge", "Size of the database with its write-ahead log.", float64(u.DatabaseBytes))
	metric("hlsmon_journal_bytes", "gauge", "Size of the segment journal.", float64(u.JournalBytes))
	metric("hlsmon_journal_limit_bytes", "gauge", "Size the segment journal cannot exceed.", float64(u.JournalMaxBytes))
	metric("hlsmon_journal_rows", "gauge", "Rows of the segment journal.", float64(u.JournalRows))
	metric("hlsmon_disk_free_bytes", "gauge", "Free room on the volume of the database.", float64(u.DiskFreeBytes))
	metric("hlsmon_disk_total_bytes", "gauge", "Size of that volume.", float64(u.DiskTotalBytes))

	mx := s.mx.Stats()
	metric("hlsmon_time_series_enabled", "gauge", "1 when a time series storage is configured.", flag(mx.Enabled))
	if mx.Enabled {
		metric("hlsmon_time_series_up", "gauge", "1 when the time series storage answers.", flag(s.mx.Healthy(ctx)))
	}
	metric("hlsmon_time_series_queued_bytes", "gauge", "Samples waiting in memory to be written to it.", float64(mx.QueuedBytes))
	metric("hlsmon_time_series_disk_bytes", "gauge", "Samples waiting on disk, compressed: it is, or was until now, away.", float64(mx.DiskBytes))
	metric("hlsmon_time_series_dropped_batches_total", "counter", "Batches of samples given up.", float64(mx.DroppedBatches))

}

// agentMetrics tells how every agent that is expected to be in contact fares.
func (s *Server) agentMetrics(w *promText, agents []*store.Agent) {
	metric := w.metric
	sort.Slice(agents, func(i, j int) bool { return agents[i].ID < agents[j].ID })
	now := s.now()
	type row struct {
		name   string
		online bool
		st     *agentproto.Status
	}
	rows := make([]row, 0, len(agents))
	for _, a := range agents {
		// An agent that is switched off, or was never connected, is not
		// expected to be in contact: nothing to watch yet.
		if !a.Enabled || (a.Kind != store.AgentLocal && !a.Enrolled) {
			continue
		}
		r := row{name: a.Name, online: s.agentOnline(a, now)}
		if r.online {
			r.st = s.agentStatus(a)
		}
		rows = append(rows, r)
	}
	series := func(name, typ, help string, value func(st *agentproto.Status) float64) {
		first := true
		for _, r := range rows {
			if r.st == nil {
				continue
			}
			h := ""
			if first {
				h, first = help, false
			}
			metric(name, typ, h, value(r.st), "agent", r.name)
		}
	}
	for i, r := range rows {
		h := ""
		if i == 0 {
			h = "1 when the agent is in contact with the server."
		}
		metric("hlsmon_agent_online", "gauge", h, flag(r.online), "agent", r.name)
	}
	series("hlsmon_agent_overloaded", "gauge", "1 while the agent does not get the processor time it needs.", func(st *agentproto.Status) float64 { return flag(st.Overloaded != "") })
	series("hlsmon_agent_streams", "gauge", "Streams the agent checks.", func(st *agentproto.Status) float64 { return float64(st.Streams) })
	series("hlsmon_agent_streams_behind", "gauge", "Streams the agent checks later than it should.", func(st *agentproto.Status) float64 { return float64(st.Behind) })
	series("hlsmon_agent_queue_results", "gauge", "Results the agent has not delivered yet.", func(st *agentproto.Status) float64 { return float64(st.QueueResults) })
	series("hlsmon_agent_queue_bytes", "gauge", "Their size.", func(st *agentproto.Status) float64 { return float64(st.QueueBytes) })
	series("hlsmon_agent_queue_oldest_seconds", "gauge", "How long the oldest of them has been waiting.", func(st *agentproto.Status) float64 { return st.QueueOldestSec })
	series("hlsmon_agent_queue_dropped_total", "counter", "Results the agent dropped because its queue was full.", func(st *agentproto.Status) float64 { return float64(st.QueueDropped) })
	series("hlsmon_agent_queue_failing", "gauge", "1 while the agent cannot write to its queue.", func(st *agentproto.Status) float64 { return flag(st.QueueFailing) })
	// The server's own checks have no way to the server to speak of.
	link := func(name, typ, help string, get func(l *agentproto.Link) float64) {
		first := true
		for _, r := range rows {
			if r.st == nil || r.st.Link == nil {
				continue
			}
			h := ""
			if first {
				h, first = help, false
			}
			metric(name, typ, h, get(r.st.Link), "agent", r.name)
		}
	}
	link("hlsmon_agent_link_rtt_seconds", "gauge", "Round trip time from the agent to the server, by the kernel's count; 0 where it cannot be read.", func(l *agentproto.Link) float64 { return l.RTTMs / 1000 })
	link("hlsmon_agent_link_retransmits_total", "counter", "TCP segments the agent had to send to the server again: loss on the way.", func(l *agentproto.Link) float64 { return float64(l.Retransmits) })
	link("hlsmon_agent_link_request_seconds", "gauge", "How long the agent's latest request to the server took.", func(l *agentproto.Link) float64 { return l.RequestMs / 1000 })
	link("hlsmon_agent_link_requests_total", "counter", "Requests the agent made to the server.", func(l *agentproto.Link) float64 { return float64(l.Requests) })
	link("hlsmon_agent_link_failures_total", "counter", "Of them, those that got no answer.", func(l *agentproto.Link) float64 { return float64(l.Failures) })
	series("hlsmon_agent_cpu_cores", "gauge", "Processors used by the agent.", func(st *agentproto.Status) float64 { return st.CPUCores })
	series("hlsmon_agent_memory_bytes", "gauge", "Memory of the agent.", func(st *agentproto.Status) float64 { return float64(st.MemoryBytes) })

}
