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

	"github.com/hotid/hls-monitor/internal/ingest"
	"github.com/hotid/hls-monitor/internal/probe"
	"github.com/hotid/hls-monitor/internal/store"
)

// An incident belongs to one stream. When a server is in trouble, every
// stream it serves gets its own, and the list fills with one problem told
// many times. The watch looks over the open incidents and the figures of
// the network, and keeps incidents of the servers themselves.

const (
	// serverIncidentFrom is how many streams of one server must have the
	// same problem at once for it to be the server's.
	serverIncidentFrom = 3
	// serverWatchEvery is how often the servers are looked at, and
	// serverIncidentQuiet how long a problem must be gone for its incident
	// to end.
	serverWatchEvery    = 30 * time.Second
	serverIncidentQuiet = 3 * serverWatchEvery
	// KindNetworkLoss is the incident of a server the way to which loses
	// packets; the others carry the kind of the streams' problem.
	KindNetworkLoss = "network_loss"
)

// ServerWatchEvery is how often WatchServers is meant to be called.
const ServerWatchEvery = serverWatchEvery

// serverKinds are the problems that can be a server's: those of answering
// at all. What is wrong with the content of a stream is the stream's, on
// however many streams of one server it shows.
var serverKinds = map[string]bool{
	probe.ProbPlaylistUnreachable: true, probe.ProbPlaylistHTTP: true, probe.ProbPlaylistParse: true,
	probe.ProbPlaylistStale: true, probe.ProbPlaylistEmpty: true,
	probe.ProbSegmentUnreachable: true, probe.ProbSegmentHTTP: true, probe.ProbSegmentTruncated: true, probe.ProbSegmentSlow: true,
	probe.ProbKeyUnavailable: true, probe.ProbInitUnavailable: true,
}

// serverIncidentDetails is what a server's incident tells besides its message.
type serverIncidentDetails struct {
	// Streams had the problem at the latest look; Example is what one of
	// them said.
	Streams []streamRef `json:"streams,omitempty"`
	Example string      `json:"example,omitempty"`
	// For loss: the figures of the last five minutes.
	Requests    float64 `json:"requests,omitempty"`
	Retransmits float64 `json:"retransmits,omitempty"`
}

// splitNode takes apart "host ip", the way problems name a server
// (probe.Endpoint.Node).
func splitNode(node string) (host, ip string) {
	host = strings.TrimSpace(node)
	if i := strings.LastIndex(host, " "); i > 0 {
		host, ip = host[:i], host[i+1:]
	}
	return host, ip
}

// WatchServers looks for what is wrong with servers rather than with
// streams, and opens, keeps and ends their incidents.
func (s *Server) WatchServers(ctx context.Context) {
	now := s.now()
	open := true
	incidents, err := s.st.ListIncidents(ctx, store.IncidentFilter{Open: &open, Problems: true, Limit: 2000})
	if err != nil {
		if ctx.Err() == nil {
			s.log.Warn("cannot look over the servers", "error", err)
		}
		return
	}
	type key struct{ host, ip, kind string }
	type group struct {
		streams  map[int64]string
		severity string
		example  string
	}
	groups := map[key]*group{}
	for _, in := range incidents {
		// Only what is there now: an incident stays open for a while after
		// the problem is gone, and one whose agent fell silent says nothing.
		if !serverKinds[in.Kind] || !in.Active || now.Sub(in.LastSeen) > 2*time.Minute {
			continue
		}
		node := incidentServer(in.Details)
		if node == "" {
			continue
		}
		host, ip := splitNode(node)
		k := key{host, ip, in.Kind}
		g := groups[k]
		if g == nil {
			g = &group{streams: map[int64]string{}, severity: probe.SevWarn}
			groups[k] = g
		}
		g.streams[in.StreamID] = in.StreamName
		g.example = in.Message
		if in.Severity == probe.SevCrit {
			g.severity = probe.SevCrit
		}
	}
	report := func(in store.ServerIncident, d serverIncidentDetails) {
		in.Details, _ = json.Marshal(d)
		if err := s.st.ReportServerIncident(ctx, in, now.Add(-ingest.ReopenWithin), now); err != nil && ctx.Err() == nil {
			s.log.Warn("cannot record an incident of a server", "server", in.Host, "kind", in.Kind, "error", err)
		}
	}
	for k, g := range groups {
		if len(g.streams) < serverIncidentFrom {
			continue
		}
		d := serverIncidentDetails{Example: g.example}
		for id, name := range g.streams {
			d.Streams = append(d.Streams, streamRef{ID: id, Name: name})
		}
		sort.Slice(d.Streams, func(i, j int) bool { return d.Streams[i].Name < d.Streams[j].Name })
		report(store.ServerIncident{Host: k.host, IP: k.ip, Kind: k.kind, Severity: g.severity, Streams: len(g.streams),
			Message: fmt.Sprintf("%d streams at once: %s", len(g.streams), g.example)}, d)
	}
	// Loss that hovers about the limit would begin and end all the time:
	// an incident that is open goes on until the loss is down to half of it.
	losing := map[[2]string]bool{}
	if openNow, err := s.st.ListServerIncidents(ctx, true, 2000); err == nil {
		for _, in := range openNow {
			if in.Kind == KindNetworkLoss {
				losing[[2]string{in.Host, in.IP}] = true
			}
		}
	}
	for k, n := range s.serverNetworks(ctx, now) {
		still := losing[k] && n.Requests >= lossyRequests && n.Retransmits/n.Requests > lossyShare/2
		if !n.Lossy && !still {
			continue
		}
		report(store.ServerIncident{Host: k[0], IP: k[1], Kind: KindNetworkLoss, Severity: probe.SevWarn,
			Message: fmt.Sprintf("%.1f %% of %.0f requests in five minutes needed a retransmission", 100*n.Retransmits/n.Requests, n.Requests)},
			serverIncidentDetails{Requests: n.Requests, Retransmits: n.Retransmits})
	}
	if err := s.st.CloseServerIncidents(ctx, now.Add(-serverIncidentQuiet), now); err != nil && ctx.Err() == nil {
		s.log.Warn("cannot end incidents of servers", "error", err)
	}
}

// listServerIncidents godoc
//
//	@Summary	Incidents of servers, open ones first
//	@Description	What is wrong with a server rather than with one stream: the same problem on several of its streams at once (the kind is that of the streams' problem), or the way to it losing packets (network_loss). A problem that comes back within ten minutes continues the same incident.
//	@Tags		incidents
//	@Security	SessionCookie
//	@Security	BearerToken
//	@Produce	json
//	@Param		open	query	bool	false	"true: only open"
//	@Param		limit	query	int		false	"Rows to return (default 200, max 2000)"
//	@Success	200	{array}	store.ServerIncident
//	@Router		/api/v1/server-incidents [get]
func (s *Server) listServerIncidents(c *gin.Context) {
	rows, err := s.st.ListServerIncidents(c.Request.Context(), c.Query("open") == "true" || c.Query("open") == "1", int(queryInt(c, "limit")))
	if s.storeError(c, err) {
		return
	}
	c.JSON(http.StatusOK, rows)
}
