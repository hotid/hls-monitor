package api

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/hotid/hls-monitor/internal/store"
	"github.com/hotid/hls-monitor/internal/types/errcode"
	"github.com/hotid/hls-monitor/internal/types/id"
	"github.com/hotid/hls-monitor/internal/types/sourcekind"
	"github.com/hotid/hls-monitor/internal/types/streamstatus"
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
	streamID, ok := pathID[id.Stream](c)
	if !ok {
		return
	}
	hours := queryInt(c, "hours")
	if hours <= 0 {
		hours = 24
	}
	hours = min(hours, 720)
	rows, err := s.st.ListSources(c.Request.Context(), streamID, s.now().Add(-time.Duration(hours)*time.Hour))
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
	// Network is the way to the server over the last five minutes, by the
	// kernel's count on the connections of the checks; absent where it
	// cannot be read (the checks do not run on Linux).
	Network *serverNetwork `json:"network,omitempty"`
	// FailingStreams are up to ten of the streams in the failed state.
	FailingStreams []streamRef `json:"failing_streams"`
	// StreamIDs are all the streams counted in Streams.
	StreamIDs []id.Stream `json:"stream_ids"`
}

// serverNetwork is what the checks saw of the way to one server.
type serverNetwork struct {
	// RTT is the round trip time in seconds, averaged over the requests.
	RTT float64 `json:"rtt_seconds"`
	// Requests is how many requests the figures rest on; Retransmits how
	// many TCP segments had to be sent again on the way there, OutOfOrder
	// how many arrived out of order on the way back.
	Requests    float64 `json:"requests"`
	Retransmits float64 `json:"retransmits"`
	OutOfOrder  float64 `json:"out_of_order"`
	// Lossy tells that the share of requests that needed a retransmission
	// is above what a network loses in the ordinary way.
	Lossy bool `json:"lossy"`
}

// A server is called lossy when, of at least lossyRequests requests, more
// than lossyShare needed a segment sent again. A fraction of a percent is
// what the internet loses anyway.
const (
	lossyShare    = 0.05
	lossyRequests = 200
)

// serverNetworks reads the figures of the last five minutes for every
// server; nil when the time series storage does not answer.
func (s *Server) serverNetworks(ctx context.Context, at time.Time) map[[2]string]*serverNetwork {
	out := map[[2]string]*serverNetwork{}
	for _, q := range []struct {
		expr string
		set  func(*serverNetwork, float64)
	}{
		{`sum by (server, ip) (sum_over_time(hls_server_requests[5m]))`, func(n *serverNetwork, v float64) { n.Requests = v }},
		{`sum by (server, ip) (sum_over_time(hls_server_retransmits[5m]))`, func(n *serverNetwork, v float64) { n.Retransmits = v }},
		{`sum by (server, ip) (sum_over_time(hls_server_out_of_order[5m]))`, func(n *serverNetwork, v float64) { n.OutOfOrder = v }},
		{`avg by (server, ip) (avg_over_time(hls_server_rtt_seconds[5m]))`, func(n *serverNetwork, v float64) { n.RTT = v }},
	} {
		res, err := s.mx.Query(ctx, q.expr, at)
		if err != nil {
			return nil
		}
		for _, r := range res {
			if len(r.Points) == 0 {
				continue
			}
			k := [2]string{r.Labels["server"], r.Labels["ip"]}
			if out[k] == nil {
				out[k] = &serverNetwork{}
			}
			q.set(out[k], r.Points[len(r.Points)-1][1])
		}
	}
	for _, n := range out {
		n.Lossy = n.Requests >= lossyRequests && n.Retransmits/n.Requests > lossyShare
	}
	return out
}

// streamRef is enough to name a stream and link to it.
type streamRef = store.StreamRef

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
	seen, err := s.st.ListSources(ctx, id.Stream{}, sc.now.Add(-5*time.Minute))
	if s.storeError(c, err) {
		return
	}
	status := map[id.Stream]streamstatus.Status{}
	names := map[id.Stream]string{}
	for _, st := range streams {
		status[st.ID] = s.view(st, sc, false).Status
		names[st.ID] = st.Name
	}

	type key struct{ host, ip string }
	type acc struct {
		view    serverView
		streams map[id.Stream]bool
	}
	byServer := map[key]*acc{}
	for _, r := range seen {
		k := key{r.Host, r.IP}
		a := byServer[k]
		if a == nil {
			a = &acc{view: serverView{Host: r.Host, IP: r.IP, FailingStreams: []streamRef{}, StreamIDs: []id.Stream{}}, streams: map[id.Stream]bool{}}
			byServer[k] = a
		}
		if r.Software != "" {
			a.view.Software = r.Software
		}
		if r.What == sourcekind.Playlist() {
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
		case streamstatus.OK():
			a.view.OK++
		case streamstatus.Warn():
			a.view.Warn++
		case streamstatus.Fail():
			a.view.Fail++
			if len(a.view.FailingStreams) < 10 {
				a.view.FailingStreams = append(a.view.FailingStreams, streamRef{ID: r.StreamID, Name: names[r.StreamID]})
			}
		}
	}
	network := s.serverNetworks(ctx, sc.now)
	out := make([]serverView, 0, len(byServer))
	for k, a := range byServer {
		a.view.Network = network[[2]string{k.host, k.ip}]
		sort.Slice(a.view.FailingStreams, func(i, j int) bool { return a.view.FailingStreams[i].Name < a.view.FailingStreams[j].Name })
		sort.Slice(a.view.StreamIDs, func(i, j int) bool { return a.view.StreamIDs[i].Compare(a.view.StreamIDs[j]) < 0 })
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

// serverSeries is the history of the way to one server.
type serverSeries struct {
	Host string `json:"host"`
	IP   string `json:"ip"`
	// Series maps a figure to [unix seconds, value] points: "rtt" in
	// seconds, "loss" as the retransmissions per request, "requests" per
	// second.
	Series map[string][][2]float64 `json:"series"`
}

type serversNetworkResponse struct {
	From    float64        `json:"from"`
	To      float64        `json:"to"`
	Step    float64        `json:"step"`
	Servers []serverSeries `json:"servers"`
}

// serversNetwork godoc
//
//	@Summary	Round trip time, loss and the share of requests, by server
//	@Description	By the kernel's count on the connections of the checks, from every vantage point together. Loss is the TCP retransmissions per request on the way to the server.
//	@Tags		overview
//	@Produce	json
//	@Param		range	query		int	false	"Length of the history in seconds (default 3600, max 604800)"
//	@Param		buckets	query		int	false	"Number of steps (default 120, max 300)"
//	@Success	200		{object}	serversNetworkResponse
//	@Failure	502		{object}	apiError
//	@Router		/api/v1/servers/network [get]
func (s *Server) serversNetwork(c *gin.Context) {
	from, to, step := s.chartWindow(c)
	sec := int(step.Seconds())
	out := serversNetworkResponse{From: float64(from.Unix()), To: float64(to.Unix()), Step: step.Seconds(), Servers: []serverSeries{}}
	byServer := map[[2]string]*serverSeries{}
	for _, q := range []struct{ id, expr string }{
		{"rtt", `avg by (server, ip) (avg_over_time(hls_server_rtt_seconds[%[1]ds]))`},
		{"loss", `sum by (server, ip) (sum_over_time(hls_server_retransmits[%[1]ds])) / sum by (server, ip) (sum_over_time(hls_server_requests[%[1]ds]))`},
		{"requests", `sum by (server, ip) (sum_over_time(hls_server_requests[%[1]ds])) / %[1]d`},
	} {
		res, err := s.mx.QueryRange(c.Request.Context(), fmt.Sprintf(q.expr, sec), from, to, step)
		if err != nil {
			s.log.Warn("servers network query failed", "error", err)
			fail(c, http.StatusBadGateway, errcode.MetricsUnavailable(), "the time series storage is not available")
			return
		}
		for _, r := range res {
			k := [2]string{r.Labels["server"], r.Labels["ip"]}
			if byServer[k] == nil {
				byServer[k] = &serverSeries{Host: k[0], IP: k[1], Series: map[string][][2]float64{}}
			}
			byServer[k].Series[q.id] = r.Points
		}
	}
	for _, v := range byServer {
		out.Servers = append(out.Servers, *v)
	}
	sort.Slice(out.Servers, func(i, j int) bool {
		a, b := out.Servers[i], out.Servers[j]
		if a.Host != b.Host {
			return a.Host < b.Host
		}
		return a.IP < b.IP
	})
	c.JSON(http.StatusOK, out)
}

// chartWindow reads the range and the number of steps of a chart request.
func (s *Server) chartWindow(c *gin.Context) (from, to time.Time, step time.Duration) {
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
	step = max((rng / time.Duration(buckets)).Truncate(time.Second), 30*time.Second)
	to = s.now().Truncate(step)
	return to.Add(-rng), to, step
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
	{"rtt_avg", `avg(avg_over_time(hls_tcp_rtt_seconds[%[1]ds]))`},
	{"rtt_p95", `quantile(0.95, avg_over_time(hls_tcp_rtt_seconds[%[1]ds]))`},
	{"out_of_order", `sum(sum_over_time(hls_tcp_out_of_order[%[1]ds]))`},
	// Measured on a connection made for it, every few minutes per stream.
	{"connect_avg", `avg(avg_over_time(hls_connect_seconds[%[1]ds]))`},
	{"connect_p95", `quantile(0.95, max_over_time(hls_connect_seconds[%[1]ds]))`},
	{"dns_avg", `avg(avg_over_time(hls_dns_seconds[%[1]ds]))`},
	{"tls_avg", `avg(avg_over_time(hls_tls_seconds[%[1]ds]))`},
	// The way in: how many redirects the stream's own address takes.
	{"entry_redirects_avg", `avg(avg_over_time(hls_entry_redirects[%[1]ds]))`},
	{"entry_time_avg", `avg(avg_over_time(hls_entry_seconds[%[1]ds]))`},
	{"entry_time_p95", `quantile(0.95, avg_over_time(hls_entry_seconds[%[1]ds]))`},
	{"retransmits", `sum(sum_over_time(hls_tcp_retransmits[%[1]ds]))`},
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
	// What the server itself uses: the process, and the whole container
	// with the programs it runs.
	{"self_cpu", `avg(avg_over_time(hlsmon_self_cpu_cores[%[1]ds]))`},
	{"self_cpu_all", `avg(avg_over_time(hlsmon_self_cpu_all_cores[%[1]ds]))`},
	{"self_memory", `avg(avg_over_time(hlsmon_self_memory_bytes[%[1]ds]))`},
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
	from, to, step := s.chartWindow(c)

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
		fail(c, http.StatusBadGateway, errcode.MetricsUnavailable(), "the time series storage is not available")
		return
	}
	c.JSON(http.StatusOK, out)
}
