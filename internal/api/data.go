package api

import (
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/hotid/hls-monitor/internal/metrics"
	"github.com/hotid/hls-monitor/internal/store"
)

// agentView is an agent with its reachability.
type agentView struct {
	*store.Agent
	Online  bool `json:"online"`
	Streams int  `json:"streams"`
}

// overviewResponse is the data of the dashboard.
type overviewResponse struct {
	Time      time.Time         `json:"time"`
	Counts    map[string]int    `json:"counts"`
	Streams   []*streamView     `json:"streams"`
	Agents    []agentView       `json:"agents"`
	Incidents []*store.Incident `json:"incidents"`
}

func (s *Server) agentViews(agents map[int64]*store.Agent, streams []*store.Stream, now time.Time) []agentView {
	perAgent := map[int64]int{}
	for _, st := range streams {
		if !st.Enabled {
			continue
		}
		for _, id := range st.AgentIDs {
			perAgent[id]++
		}
	}
	out := make([]agentView, 0, len(agents))
	for _, a := range agents {
		out = append(out, agentView{Agent: a, Online: s.agentOnline(a, now), Streams: perAgent[a.ID]})
	}
	// Map order is random; the local agent first, then by ID.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].ID < out[j-1].ID; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// overview godoc
//
//	@Summary	Dashboard data: counters, streams, agents, open incidents
//	@Tags		overview
//	@Produce	json
//	@Success	200	{object}	overviewResponse
//	@Router		/api/v1/overview [get]
func (s *Server) overview(c *gin.Context) {
	ctx := c.Request.Context()
	streams, err := s.st.ListStreams(ctx)
	if s.storeError(c, err) {
		return
	}
	sc, err := s.loadStatusContext(ctx)
	if s.storeError(c, err) {
		return
	}
	open := true
	incidents, err := s.st.ListIncidents(ctx, store.IncidentFilter{Open: &open, Problems: true, Limit: 500})
	if s.storeError(c, err) {
		return
	}
	out := overviewResponse{
		Time:      sc.now.UTC(),
		Counts:    map[string]int{statusOK: 0, statusWarn: 0, statusFail: 0, statusNoData: 0, statusDisabled: 0},
		Streams:   make([]*streamView, 0, len(streams)),
		Agents:    s.agentViews(sc.agents, streams, sc.now),
		Incidents: incidents,
	}
	for _, st := range streams {
		v := s.view(st, sc, isAdmin(c))
		out.Counts[v.Status]++
		out.Streams = append(out.Streams, v)
	}
	out.Counts["total"] = len(streams)
	c.JSON(http.StatusOK, out)
}

// systemResponse describes the server itself.
type systemResponse struct {
	Version      string        `json:"version"`
	Time         time.Time     `json:"time"`
	LocalStreams int           `json:"local_streams"`
	Metrics      metrics.Stats `json:"metrics"`
	MetricsUp    bool          `json:"metrics_up"`
	PublicURL    string        `json:"public_url"`
	// Storage is how much room the database and the segment journal take.
	Storage store.Usage `json:"storage"`
}

// system godoc
//
//	@Summary	Server version and the state of the time series storage
//	@Tags		system
//	@Produce	json
//	@Success	200	{object}	systemResponse
//	@Router		/api/v1/system [get]
func (s *Server) system(c *gin.Context) {
	out := systemResponse{Version: s.cfg.Version, Time: s.now().UTC(), Metrics: s.mx.Stats(), PublicURL: s.publicURL(c)}
	if s.local != nil {
		out.LocalStreams = s.local.Count()
	}
	out.MetricsUp = s.mx.Healthy(c.Request.Context())
	out.Storage = s.st.Usage(c.Request.Context())
	c.JSON(http.StatusOK, out)
}

// publicURL is the address agents should use to reach the server.
func (s *Server) publicURL(c *gin.Context) string {
	if s.cfg.PublicURL != "" {
		return strings.TrimRight(s.cfg.PublicURL, "/")
	}
	scheme := "http"
	if c.Request.TLS != nil || strings.EqualFold(c.GetHeader("X-Forwarded-Proto"), "https") {
		scheme = "https"
	}
	return scheme + "://" + c.Request.Host + s.cfg.BasePath
}

// metricSeries is the result of a chart query for one metric.
type metricSeries struct {
	Metric string           `json:"metric"`
	Series []metrics.Series `json:"series"`
}

// seriesResponse is the result of a chart query.
type seriesResponse struct {
	From    float64        `json:"from"`
	To      float64        `json:"to"`
	Step    float64        `json:"step"`
	Metrics []metricSeries `json:"metrics"`
}

var aggregations = map[string]string{
	"avg":  "avg_over_time",
	"max":  "max_over_time",
	"min":  "min_over_time",
	"last": "last_over_time",
	"sum":  "sum_over_time",
}

// series godoc
//
//	@Summary	Time series of a stream for charts
//	@Description	Returns one series per agent and variant for each requested metric. Each point aggregates the samples of one step.
//	@Tags		streams
//	@Produce	json
//	@Param		id		path		int		true	"Stream ID"
//	@Param		metric	query		string	true	"Comma-separated metric names, e.g. hls_segment_total_seconds"
//	@Param		from	query		number	false	"Start, Unix seconds (default: one hour ago)"
//	@Param		to		query		number	false	"End, Unix seconds (default: now)"
//	@Param		step	query		number	false	"Step in seconds (default: range / 300)"
//	@Param		agg		query		string	false	"Aggregation within a step"	Enums(avg, max, min, last, sum)
//	@Param		agent	query		int		false	"Only this agent"
//	@Param		variant	query		int		false	"Only this variant"
//	@Success	200		{object}	seriesResponse
//	@Failure	400		{object}	apiError
//	@Failure	502		{object}	apiError
//	@Router		/api/v1/streams/{id}/series [get]
func (s *Server) series(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	names := strings.Split(c.Query("metric"), ",")
	if len(names) == 0 || len(names) > 12 {
		badRequest(c, "metric must list 1 to 12 names")
		return
	}
	for i, n := range names {
		names[i] = strings.TrimSpace(n)
		if !metrics.ValidName(names[i]) || !strings.HasPrefix(names[i], "hls_") {
			badRequest(c, "invalid metric name "+n)
			return
		}
	}
	fn, ok := aggregations[c.DefaultQuery("agg", "avg")]
	if !ok {
		badRequest(c, "agg must be one of: avg, max, min, last, sum")
		return
	}
	now := s.now()
	to := queryTime(c, "to")
	if to.IsZero() || to.After(now) {
		to = now
	}
	from := queryTime(c, "from")
	if from.IsZero() {
		from = to.Add(-time.Hour)
	}
	if !from.Before(to) {
		badRequest(c, "from must be earlier than to")
		return
	}
	step := time.Duration(queryInt(c, "step")) * time.Second
	if step <= 0 {
		step = to.Sub(from) / 300
	}
	step = max(step.Truncate(time.Second), 5*time.Second)
	if to.Sub(from)/step > 5000 {
		badRequest(c, "too many points: increase step or shorten the range")
		return
	}
	// Align to the step so that consecutive refreshes return the same buckets.
	from = from.Truncate(step)

	sel := fmt.Sprintf(`stream="%d"`, id)
	if a := queryInt(c, "agent"); a > 0 {
		sel += fmt.Sprintf(`,agent="%d"`, a)
	}
	if v := queryInt(c, "variant"); v > 0 {
		sel += fmt.Sprintf(`,variant="%d"`, v)
	}

	out := seriesResponse{
		From: float64(from.UnixMilli()) / 1000, To: float64(to.UnixMilli()) / 1000, Step: step.Seconds(),
		Metrics: make([]metricSeries, len(names)),
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	var firstErr error
	for i, name := range names {
		wg.Add(1)
		go func() {
			defer wg.Done()
			expr := fmt.Sprintf("%s(%s{%s}[%ds])", fn, name, sel, int(step.Seconds()))
			res, err := s.mx.QueryRange(c.Request.Context(), expr, from, to, step)
			if err != nil {
				mu.Lock()
				if firstErr == nil {
					firstErr = err
				}
				mu.Unlock()
				return
			}
			for j := range res {
				delete(res[j].Labels, "__name__")
				delete(res[j].Labels, "stream")
			}
			out.Metrics[i] = metricSeries{Metric: name, Series: res}
		}()
	}
	wg.Wait()
	if firstErr != nil {
		s.log.Warn("series query failed", "error", firstErr)
		fail(c, http.StatusBadGateway, "metrics_unavailable", "the time series storage is not available")
		return
	}
	c.JSON(http.StatusOK, out)
}

// listSegments godoc
//
//	@Summary	Journal of checked segments, newest first
//	@Tags		streams
//	@Produce	json
//	@Param		id			path	int		true	"Stream ID"
//	@Param		agent		query	int		false	"Only this agent"
//	@Param		variant		query	int		false	"Only this variant"
//	@Param		problems	query	bool	false	"Only segments with problems"
//	@Param		before		query	number	false	"Only segments checked before this Unix time"
//	@Param		limit		query	int		false	"Rows to return (default 100, max 1000)"
//	@Success	200			{array}	store.SegmentRecord
//	@Router		/api/v1/streams/{id}/segments [get]
func (s *Server) listSegments(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	rows, err := s.st.ListSegments(c.Request.Context(), store.SegmentFilter{
		StreamID:     id,
		AgentID:      queryInt(c, "agent"),
		VariantID:    queryInt(c, "variant"),
		OnlyProblems: c.Query("problems") == "true" || c.Query("problems") == "1",
		Before:       queryTime(c, "before"),
		Limit:        int(queryInt(c, "limit")),
	})
	if s.storeError(c, err) {
		return
	}
	c.JSON(http.StatusOK, rows)
}

// listSnapshots godoc
//
//	@Summary	Playlists captured when something was wrong, without their text
//	@Tags		streams
//	@Produce	json
//	@Param		id		path	int	true	"Stream ID"
//	@Param		limit	query	int	false	"Rows to return (default 50, max 500)"
//	@Success	200		{array}	store.Snapshot
//	@Router		/api/v1/streams/{id}/snapshots [get]
func (s *Server) listSnapshots(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	rows, err := s.st.ListSnapshots(c.Request.Context(), id, int(queryInt(c, "limit")))
	if s.storeError(c, err) {
		return
	}
	c.JSON(http.StatusOK, rows)
}

// getSnapshot godoc
//
//	@Summary	One captured playlist with its text
//	@Tags		streams
//	@Produce	json
//	@Param		id	path		int	true	"Snapshot ID"
//	@Success	200	{object}	store.Snapshot
//	@Failure	404	{object}	apiError
//	@Router		/api/v1/snapshots/{id} [get]
func (s *Server) getSnapshot(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	sn, err := s.st.GetSnapshot(c.Request.Context(), id)
	if s.storeError(c, err) {
		return
	}
	c.JSON(http.StatusOK, sn)
}

// listIncidents godoc
//
//	@Summary	Incidents, open ones first
//	@Tags		incidents
//	@Produce	json
//	@Param		stream	query	int		false	"Only this stream"
//	@Param		agent	query	int		false	"Only this agent"
//	@Param		open	query	bool	false	"true: only open, false: only closed"
//	@Param		problems	query	bool	false	"true: leave out informational incidents such as a change of server"
//	@Param		severity	query	string	false	"Only these severities, comma-separated: info, warn, crit"
//	@Param		from	query	number	false	"Only incidents still open at or after this Unix time"
//	@Param		to		query	number	false	"Only incidents started before this Unix time"
//	@Param		limit	query	int		false	"Rows to return (default 200, max 2000)"
//	@Success	200		{array}	store.Incident
//	@Router		/api/v1/incidents [get]
func (s *Server) listIncidents(c *gin.Context) {
	f := store.IncidentFilter{
		StreamID: queryInt(c, "stream"),
		AgentID:  queryInt(c, "agent"),
		From:     queryTime(c, "from"),
		To:       queryTime(c, "to"),
		Limit:    int(queryInt(c, "limit")),
	}
	f.Problems = c.Query("problems") == "true" || c.Query("problems") == "1"
	if raw := c.Query("severity"); raw != "" {
		for _, sev := range strings.Split(raw, ",") {
			switch sev = strings.TrimSpace(sev); sev {
			case "info", "warn", "crit":
				f.Severities = append(f.Severities, sev)
			default:
				badRequest(c, "severity must be a comma-separated list of info, warn, crit")
				return
			}
		}
	}
	switch c.Query("open") {
	case "true", "1":
		v := true
		f.Open = &v
	case "false", "0":
		v := false
		f.Open = &v
	}
	rows, err := s.st.ListIncidents(c.Request.Context(), f)
	if s.storeError(c, err) {
		return
	}
	c.JSON(http.StatusOK, rows)
}

// timelineResponse is the recent status history of every stream.
type timelineResponse struct {
	From float64 `json:"from"`
	To   float64 `json:"to"`
	Step float64 `json:"step"`
	// Streams maps a stream ID to [unix seconds, status] points, where the
	// status is the worst seen within the step: 0 ok, 1 warning, 2 failure.
	// A step without a point had no checks.
	Streams map[string][][2]float64 `json:"streams"`
}

// timeline godoc
//
//	@Summary	Recent status history of all streams
//	@Description	For every stream, the worst status across agents in each step of the range.
//	@Tags		overview
//	@Produce	json
//	@Param		range	query		int	false	"Length of the history in seconds (default 3600, max 604800)"
//	@Param		buckets	query		int	false	"Number of steps (default 60, max 240)"
//	@Success	200		{object}	timelineResponse
//	@Failure	502		{object}	apiError
//	@Router		/api/v1/timeline [get]
func (s *Server) timeline(c *gin.Context) {
	rng := time.Duration(queryInt(c, "range")) * time.Second
	if rng <= 0 {
		rng = time.Hour
	}
	rng = min(rng, 7*24*time.Hour)
	buckets := queryInt(c, "buckets")
	if buckets <= 0 {
		buckets = 60
	}
	buckets = min(buckets, 240)
	step := max((rng / time.Duration(buckets)).Truncate(time.Second), 5*time.Second)
	to := s.now().Truncate(step).Add(step)
	from := to.Add(-rng)

	expr := fmt.Sprintf("max by (stream) (max_over_time(hls_stream_status[%ds]))", int(step.Seconds()))
	res, err := s.mx.QueryRange(c.Request.Context(), expr, from, to, step)
	if err != nil {
		s.log.Warn("timeline query failed", "error", err)
		fail(c, http.StatusBadGateway, "metrics_unavailable", "the time series storage is not available")
		return
	}
	out := timelineResponse{From: float64(from.Unix()), To: float64(to.Unix()), Step: step.Seconds(), Streams: map[string][][2]float64{}}
	for _, sr := range res {
		out.Streams[sr.Labels["stream"]] = sr.Points
	}
	c.JSON(http.StatusOK, out)
}
