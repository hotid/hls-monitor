package api

import (
	"fmt"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/hotid/hls-monitor/internal/store"
)

// The API documentation of streamSources refers to this type.
var _ store.SourceSeen

// streamSources godoc
//
//	@Summary	Servers that answered for a stream's playlists and segments
//	@Description	Every server seen within the period, with how often it answered and when it was last seen.
//	@Tags		streams
//	@Produce	json
//	@Param		id		path	int	true	"Stream ID"
//	@Param		hours	query	int	false	"How far back to look (default 24, max 720)"
//	@Success	200		{array}	store.SourceSeen
//	@Router		/api/v1/streams/{id}/sources [get]
func (s *Server) streamSources(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	hours := queryInt(c, "hours")
	if hours <= 0 {
		hours = 24
	}
	hours = min(hours, 720)
	rows, err := s.st.ListSources(c.Request.Context(), id, s.now().Add(-time.Duration(hours)*time.Hour))
	if s.storeError(c, err) {
		return
	}
	c.JSON(http.StatusOK, rows)
}

// serverView is one answering server with the streams it currently serves.
type serverView struct {
	Host     string `json:"host"`
	IP       string `json:"ip"`
	Software string `json:"software"`
	// Streams is the number of streams whose playlists or segments this
	// server answered for recently; the rest split them by current state.
	Streams   int       `json:"streams"`
	OK        int       `json:"ok"`
	Warn      int       `json:"warn"`
	Fail      int       `json:"fail"`
	Playlists bool      `json:"playlists"`
	Segments  bool      `json:"segments"`
	LastSeen  time.Time `json:"last_seen"`
	// FailingStreams are up to ten of the streams in the failed state.
	FailingStreams []streamRef `json:"failing_streams"`
	// StreamIDs are all the streams counted in Streams.
	StreamIDs []int64 `json:"stream_ids"`
}

// streamRef is enough to name a stream and link to it.
type streamRef struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// servers godoc
//
//	@Summary	Servers currently answering, with the state of the streams they serve
//	@Description	Built from the last five minutes of checks. A stream served by several servers is counted for each.
//	@Tags		overview
//	@Produce	json
//	@Success	200	{array}	serverView
//	@Router		/api/v1/servers [get]
func (s *Server) servers(c *gin.Context) {
	ctx := c.Request.Context()
	streams, err := s.st.ListStreams(ctx)
	if s.storeError(c, err) {
		return
	}
	sc, err := s.loadStatusContext(ctx)
	if s.storeError(c, err) {
		return
	}
	seen, err := s.st.ListSources(ctx, 0, sc.now.Add(-5*time.Minute))
	if s.storeError(c, err) {
		return
	}
	status := map[int64]string{}
	names := map[int64]string{}
	for _, st := range streams {
		status[st.ID] = s.view(st, sc, false).Status
		names[st.ID] = st.Name
	}

	type key struct{ host, ip string }
	type acc struct {
		view    serverView
		streams map[int64]bool
	}
	byServer := map[key]*acc{}
	for _, r := range seen {
		k := key{r.Host, r.IP}
		a := byServer[k]
		if a == nil {
			a = &acc{view: serverView{Host: r.Host, IP: r.IP, FailingStreams: []streamRef{}, StreamIDs: []int64{}}, streams: map[int64]bool{}}
			byServer[k] = a
		}
		if r.Software != "" {
			a.view.Software = r.Software
		}
		if r.What == "playlist" {
			a.view.Playlists = true
		} else {
			a.view.Segments = true
		}
		if r.LastSeen.After(a.view.LastSeen) {
			a.view.LastSeen = r.LastSeen
		}
		if a.streams[r.StreamID] {
			continue
		}
		a.streams[r.StreamID] = true
		a.view.Streams++
		a.view.StreamIDs = append(a.view.StreamIDs, r.StreamID)
		switch status[r.StreamID] {
		case statusOK:
			a.view.OK++
		case statusWarn:
			a.view.Warn++
		case statusFail:
			a.view.Fail++
			if len(a.view.FailingStreams) < 10 {
				a.view.FailingStreams = append(a.view.FailingStreams, streamRef{ID: r.StreamID, Name: names[r.StreamID]})
			}
		}
	}
	out := make([]serverView, 0, len(byServer))
	for _, a := range byServer {
		sort.Slice(a.view.FailingStreams, func(i, j int) bool { return a.view.FailingStreams[i].Name < a.view.FailingStreams[j].Name })
		sort.Slice(a.view.StreamIDs, func(i, j int) bool { return a.view.StreamIDs[i] < a.view.StreamIDs[j] })
		out = append(out, a.view)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Host != out[j].Host {
			return out[i].Host < out[j].Host
		}
		return out[i].IP < out[j].IP
	})
	c.JSON(http.StatusOK, out)
}

// aggregates are figures over all streams at once. %[1]d is the step in
// seconds. A stream is counted once: by its worst agent and variant.
var aggregates = []struct{ id, expr string }{
	{"streams_ok", `count(max by (stream) (max_over_time(hls_stream_status[%[1]ds])) == 0)`},
	{"streams_warn", `count(max by (stream) (max_over_time(hls_stream_status[%[1]ds])) == 1)`},
	{"streams_fail", `count(max by (stream) (max_over_time(hls_stream_status[%[1]ds])) == 2)`},
	{"playlists_down", `count(min by (stream, variant) (min_over_time(hls_playlist_up[%[1]ds])) == 0)`},
	{"playlist_time_avg", `avg(avg_over_time(hls_playlist_total_seconds[%[1]ds]))`},
	{"playlist_time_p95", `quantile(0.95, avg_over_time(hls_playlist_total_seconds[%[1]ds]))`},
	{"playlist_time_max", `max(max_over_time(hls_playlist_total_seconds[%[1]ds]))`},
	{"segment_ttfb_avg", `avg(avg_over_time(hls_segment_ttfb_seconds[%[1]ds]))`},
	{"segment_ttfb_p95", `quantile(0.95, avg_over_time(hls_segment_ttfb_seconds[%[1]ds]))`},
	{"segment_errors", `sum(sum_over_time(hls_segment_errors[%[1]ds]))`},
	{"segments_checked", `sum(sum_over_time(hls_segments_checked[%[1]ds]))`},
	{"segments_missed", `sum(sum_over_time(hls_segments_missed[%[1]ds]))`},
	{"stalls", `sum(sum_over_time(hls_stalls[%[1]ds]))`},
	{"source_changes", `sum(sum_over_time(hls_playlist_source_changed[%[1]ds]))`},
	{"bitrate_total", `sum(max by (stream) (avg_over_time(hls_segment_bitrate_bps[%[1]ds])))`},
	// What the checks download, from every vantage point together: bytes
	// received within a step, as a rate.
	{"check_traffic", `sum(sum_over_time(hls_check_bytes[%[1]ds])) * 8 / %[1]d`},
	{"bitrate_avg", `avg(max by (stream) (avg_over_time(hls_segment_bitrate_bps[%[1]ds])))`},
	{"stale_max", `max(max_over_time(hls_playlist_stale_seconds[%[1]ds]))`},
	{"check_lag_max", `max(max_over_time(hls_check_lag_seconds[%[1]ds]))`},
	{"check_duration_p95", `quantile(0.95, avg_over_time(hls_check_duration_seconds[%[1]ds]))`},
}

// aggregateResponse carries figures over all streams for the overview charts.
type aggregateResponse struct {
	From float64 `json:"from"`
	To   float64 `json:"to"`
	Step float64 `json:"step"`
	// Series maps a figure to [unix seconds, value] points. A step with no
	// point means zero for counts and "no data" for averages.
	Series map[string][][2]float64 `json:"series"`
}

// aggregate godoc
//
//	@Summary	Figures over all streams for the overview charts
//	@Description	Counts of streams by state, playlist and segment response times (average and 95th percentile), errors, stalls, server changes, the total bitrate of the streams and the traffic of the checks themselves, as time series.
//	@Tags		overview
//	@Produce	json
//	@Param		range	query		int	false	"Length of the history in seconds (default 3600, max 604800)"
//	@Param		buckets	query		int	false	"Number of steps (default 120, max 300)"
//	@Success	200		{object}	aggregateResponse
//	@Failure	502		{object}	apiError
//	@Router		/api/v1/aggregate [get]
func (s *Server) aggregate(c *gin.Context) {
	rng := time.Duration(queryInt(c, "range")) * time.Second
	if rng <= 0 {
		rng = time.Hour
	}
	rng = min(rng, 7*24*time.Hour)
	buckets := queryInt(c, "buckets")
	if buckets <= 0 {
		buckets = 120
	}
	buckets = min(buckets, 300)
	// Not finer than 30 s: streams polled every 10-15 s need a few samples per step.
	step := max((rng / time.Duration(buckets)).Truncate(time.Second), 30*time.Second)
	to := s.now().Truncate(step)
	from := to.Add(-rng)

	out := aggregateResponse{From: float64(from.Unix()), To: float64(to.Unix()), Step: step.Seconds(), Series: map[string][][2]float64{}}
	var wg sync.WaitGroup
	var mu sync.Mutex
	var firstErr error
	sem := make(chan struct{}, 6)
	for _, a := range aggregates {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			res, err := s.mx.QueryRange(c.Request.Context(), fmt.Sprintf(a.expr, int(step.Seconds())), from, to, step)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				if firstErr == nil {
					firstErr = err
				}
				return
			}
			pts := [][2]float64{}
			if len(res) > 0 {
				pts = res[0].Points
			}
			out.Series[a.id] = pts
		}()
	}
	wg.Wait()
	if firstErr != nil {
		s.log.Warn("aggregate query failed", "error", firstErr)
		fail(c, http.StatusBadGateway, "metrics_unavailable", "the time series storage is not available")
		return
	}
	c.JSON(http.StatusOK, out)
}
