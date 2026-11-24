package api

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"
)

// agentStats is what a vantage point did over the last minutes.
type agentStats struct {
	// Streams by their state as this vantage point sees them.
	OK   int `json:"ok"`
	Warn int `json:"warn"`
	Fail int `json:"fail"`
	// Open incidents found from here, by severity; events are not counted.
	IncidentsCrit int `json:"incidents_crit"`
	IncidentsWarn int `json:"incidents_warn"`
	// TrafficBps is what its checks download, as a rate; SegmentsPerMin
	// how many segments it checks a minute; SegmentErrors how many of
	// them failed in the window.
	TrafficBps     float64 `json:"traffic_bps"`
	SegmentsPerMin float64 `json:"segments_per_min"`
	SegmentErrors  float64 `json:"segment_errors"`
	// CheckP95 is how long a check takes, the 95th percentile over its
	// streams, and LagMax how late the latest one started, in seconds.
	CheckP95 float64 `json:"check_p95_seconds"`
	LagMax   float64 `json:"lag_max_seconds"`
}

type agentStatsResponse struct {
	// Window is the time the rates and counts are over, in seconds.
	Window int                   `json:"window_seconds"`
	Agents map[string]agentStats `json:"agents"`
	// Metrics is false when the time series storage did not answer: the
	// figures that come from it are zero then.
	Metrics bool `json:"metrics"`
}

// agentStatsWindow is the time the figures are over.
const agentStatsWindow = 300

// agentQueries are the figures taken from the time series, by agent.
var agentQueries = []struct {
	expr string
	set  func(*agentStats, float64)
}{
	{`count by (agent) (max by (agent, stream) (max_over_time(hls_stream_status[2m])) == 0)`, func(a *agentStats, v float64) { a.OK = int(v) }},
	{`count by (agent) (max by (agent, stream) (max_over_time(hls_stream_status[2m])) == 1)`, func(a *agentStats, v float64) { a.Warn = int(v) }},
	{`count by (agent) (max by (agent, stream) (max_over_time(hls_stream_status[2m])) == 2)`, func(a *agentStats, v float64) { a.Fail = int(v) }},
	{`sum by (agent) (sum_over_time(hls_check_bytes[%[1]ds])) * 8 / %[1]d`, func(a *agentStats, v float64) { a.TrafficBps = v }},
	{`sum by (agent) (sum_over_time(hls_segments_checked[%[1]ds])) * 60 / %[1]d`, func(a *agentStats, v float64) { a.SegmentsPerMin = v }},
	{`sum by (agent) (sum_over_time(hls_segment_errors[%[1]ds]))`, func(a *agentStats, v float64) { a.SegmentErrors = v }},
	{`quantile by (agent) (0.95, avg_over_time(hls_check_duration_seconds[%[1]ds]))`, func(a *agentStats, v float64) { a.CheckP95 = v }},
	{`max by (agent) (max_over_time(hls_check_lag_seconds[1m]))`, func(a *agentStats, v float64) { a.LagMax = v }},
}

// agentsStats godoc
//
//	@Summary	What every vantage point is doing
//	@Description	For the server's own checks and for every agent: the streams it checks by their state as it sees them, the open incidents found from it, what its checks download, how many segments it checks a minute and how many of them failed, how long a check takes and how late checks start. Rates and counts are over the last five minutes. Keyed by agent ID.
//	@Tags		agents
//	@Security	SessionCookie
//	@Produce	json
//	@Success	200	{object}	agentStatsResponse
//	@Router		/api/v1/agents/stats [get]
func (s *Server) agentsStats(c *gin.Context) {
	ctx := c.Request.Context()
	out := agentStatsResponse{Window: agentStatsWindow, Agents: map[string]agentStats{}, Metrics: true}
	open, err := s.st.OpenIncidentsByAgent(ctx)
	if s.storeError(c, err) {
		return
	}
	for id, n := range open {
		out.Agents[strconv.FormatInt(id, 10)] = agentStats{IncidentsCrit: n[0], IncidentsWarn: n[1]}
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	for _, q := range agentQueries {
		wg.Add(1)
		go func() {
			defer wg.Done()
			expr := q.expr
			if strings.Contains(expr, "%[1]d") {
				expr = fmt.Sprintf(expr, agentStatsWindow)
			}
			res, err := s.mx.Query(ctx, expr, s.now())
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				out.Metrics = false
				return
			}
			for _, series := range res {
				id := series.Labels["agent"]
				if id == "" || len(series.Points) == 0 {
					continue
				}
				a := out.Agents[id]
				q.set(&a, series.Points[len(series.Points)-1][1])
				out.Agents[id] = a
			}
		}()
	}
	wg.Wait()
	c.JSON(http.StatusOK, out)
}
