package api

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/hotid/hls-monitor/internal/ingest"
	"github.com/hotid/hls-monitor/internal/store"
	"github.com/hotid/hls-monitor/internal/types/id"
	"github.com/hotid/hls-monitor/internal/types/problemkind"
	"github.com/hotid/hls-monitor/internal/types/severity"
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
)

// ServerWatchEvery is how often WatchServers is meant to be called.
const ServerWatchEvery = serverWatchEvery

// serverKinds are the problems that can be a server's: those of answering
// at all. What is wrong with the content of a stream is the stream's, on
// however many streams of one server it shows.
var serverKinds = map[problemkind.Kind]bool{
	problemkind.PlaylistUnreachable(): true, problemkind.PlaylistHTTP(): true, problemkind.PlaylistParse(): true,
	problemkind.PlaylistStale(): true, problemkind.PlaylistEmpty(): true,
	problemkind.SegmentUnreachable(): true, problemkind.SegmentHTTP(): true, problemkind.SegmentTruncated(): true, problemkind.SegmentSlow(): true,
	problemkind.KeyUnavailable(): true, problemkind.InitUnavailable(): true,
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
	type key struct {
		host, ip string
		kind     problemkind.Kind
	}
	type group struct {
		streams  map[id.Stream]string
		severity severity.Severity
		example  string
	}
	groups := map[key]*group{}
	for _, in := range incidents {
		// Only what is there now: an incident stays open for a while after
		// the problem is gone, and one whose agent fell silent says nothing.
		if !serverKinds[in.Kind] || !in.Active || now.Sub(in.LastSeen) > 2*time.Minute {
			continue
		}
		at := incidentServer(in.Details)
		if at.IsZero() {
			continue
		}
		k := key{at.Host(), at.IP(), in.Kind}
		g := groups[k]
		if g == nil {
			g = &group{streams: map[id.Stream]string{}, severity: severity.Warn()}
			groups[k] = g
		}
		g.streams[in.StreamID] = in.StreamName
		g.example = in.Message
		if in.Severity == severity.Crit() {
			g.severity = severity.Crit()
		}
	}
	report := func(in store.ServerIncident, d store.ServerIncidentDetails) {
		in.Details = d
		if err := s.st.ReportServerIncident(ctx, in, now.Add(-ingest.ReopenWithin), now); err != nil && ctx.Err() == nil {
			s.log.Warn("cannot record an incident of a server", "server", in.Host, "kind", in.Kind, "error", err)
		}
	}
	for k, g := range groups {
		if len(g.streams) < serverIncidentFrom {
			continue
		}
		d := store.ServerIncidentDetails{Example: g.example}
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
			if in.Kind == problemkind.NetworkLoss() {
				losing[[2]string{in.Host, in.IP}] = true
			}
		}
	}
	for k, n := range s.serverNetworks(ctx, now) {
		still := losing[k] && n.Requests >= lossyRequests && n.Retransmits/n.Requests > lossyShare/2
		if !n.Lossy && !still {
			continue
		}
		report(store.ServerIncident{Host: k[0], IP: k[1], Kind: problemkind.NetworkLoss(), Severity: severity.Warn(),
			Message: fmt.Sprintf("%.1f %% of %.0f requests in five minutes needed a retransmission", 100*n.Retransmits/n.Requests, n.Requests)},
			store.ServerIncidentDetails{Requests: n.Requests, Retransmits: n.Retransmits})
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
