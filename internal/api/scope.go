package api

import (
	"sort"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/hotid/hls-monitor/internal/store"
	"github.com/hotid/hls-monitor/internal/types/detail"
	"github.com/hotid/hls-monitor/internal/types/errcode"
	"github.com/hotid/hls-monitor/internal/types/id"
	"github.com/hotid/hls-monitor/internal/types/node"
	"github.com/hotid/hls-monitor/internal/types/vantagescope"
)

// One incident says what is wrong with one stream as seen from one place.
// Where the cause is can only be guessed by looking around it: do the other
// vantage points see the same, and do other streams from the same server
// have the same problem at the same time. The scope collects that evidence
// and names the guess it supports. It is a hypothesis, not a diagnosis.

// scopeSlack widens the incident's time span when looking for others:
// vantage points and streams are not checked at the same moments.
const scopeSlack = time.Minute

// serverWideFrom is how many other streams from the same server must have
// the problem for it to be called the server's.
const serverWideFrom = 2

type scopeAgent struct {
	ID   id.Agent `json:"id"`
	Name string   `json:"name"`
	// Checked is false when the agent was not reporting on the stream at
	// the time: it says nothing either way.
	Checked bool `json:"checked"`
	Saw     bool `json:"saw"`
}

type incidentScope struct {
	// Agents are the vantage points the stream is checked from.
	Agents []scopeAgent `json:"agents"`
	// Vantage sums them up.
	Vantage vantagescope.Scope `json:"vantage" swaggertype:"string" enums:"all,some,only_this,single"`

	// Server is the one that answered when the problem was found.
	Server node.Node `json:"server,omitzero" swaggertype:"string"`
	// SameServer are other streams that had a problem of the same kind at
	// the same time and were answered by the same server; Elsewhere counts
	// those answered by other servers.
	SameServer []streamRef `json:"same_server"`
	Elsewhere  int         `json:"elsewhere"`
	// ServerWide is set when enough streams of the server share the problem.
	ServerWide bool `json:"server_wide"`
}

// incidentServer returns the server an incident's details name.
func incidentServer(details detail.Set) node.Node {
	if at := details.Server(detail.KeySegmentServer()); !at.IsZero() {
		return at
	}
	return details.Server(detail.KeyPlaylistServer())
}

// incidentScope godoc
//
//	@Summary	Evidence of where an incident's cause is
//	@Description	Which vantage points saw the same problem, and which other streams from the same server had it at the same time.
//	@Tags		incidents
//	@Security	SessionCookie
//	@Produce	json
//	@Param		id	path		int	true	"Incident ID"
//	@Success	200	{object}	incidentScope
//	@Failure	404	{object}	apiError
//	@Router		/api/v1/incidents/{id}/scope [get]
func (s *Server) incidentScope(c *gin.Context) {
	incidentID, ok := pathID[id.Incident](c)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	found, err := s.st.ListIncidents(ctx, store.IncidentFilter{ID: incidentID, Limit: 1})
	if s.storeError(c, err) {
		return
	}
	if len(found) == 0 {
		fail(c, 404, errcode.NotFound(), "not found")
		return
	}
	inc := found[0]
	from := inc.StartedAt.Add(-scopeSlack)
	to := inc.LastSeen.Add(scopeSlack)
	if inc.EndedAt != nil && inc.EndedAt.After(inc.LastSeen) {
		to = inc.EndedAt.Add(scopeSlack)
	}
	same, err := s.st.ListIncidents(ctx, store.IncidentFilter{Kind: inc.Kind, From: from, To: to, Limit: 2000})
	if s.storeError(c, err) {
		return
	}
	states, err := s.st.StreamStates(ctx, inc.StreamID)
	if s.storeError(c, err) {
		return
	}
	agents, err := s.st.ListAgents(ctx)
	if s.storeError(c, err) {
		return
	}
	names := map[id.Agent]string{}
	for _, a := range agents {
		names[a.ID] = a.Name
	}

	out := incidentScope{Server: incidentServer(inc.Details), SameServer: []streamRef{}}
	saw := map[id.Agent]bool{inc.AgentID: true}
	streams := map[id.Stream]bool{}
	for _, o := range same {
		switch {
		case o.StreamID == inc.StreamID:
			if o.VariantKey == inc.VariantKey {
				saw[o.AgentID] = true
			}
		case streams[o.StreamID]:
		case !out.Server.IsZero() && incidentServer(o.Details) == out.Server:
			streams[o.StreamID] = true
			out.SameServer = append(out.SameServer, streamRef{ID: o.StreamID, Name: o.StreamName})
		default:
			streams[o.StreamID] = true
			out.Elsewhere++
		}
	}
	sort.Slice(out.SameServer, func(i, j int) bool { return out.SameServer[i].Name < out.SameServer[j].Name })
	out.ServerWide = len(out.SameServer) >= serverWideFrom

	checked, seen := 0, 0
	for _, st := range states {
		a := scopeAgent{ID: st.AgentID, Name: names[st.AgentID], Saw: saw[st.AgentID]}
		// An agent whose last result is older than the incident was not
		// looking; one that saw the problem obviously was.
		a.Checked = a.Saw || !st.CheckedAt.Before(inc.StartedAt)
		if a.Checked {
			checked++
		}
		if a.Saw {
			seen++
		}
		out.Agents = append(out.Agents, a)
	}
	sort.Slice(out.Agents, func(i, j int) bool { return out.Agents[i].Name < out.Agents[j].Name })
	switch {
	case checked < 2:
		out.Vantage = vantagescope.Single()
	case seen == checked:
		out.Vantage = vantagescope.All()
	case seen == 1:
		out.Vantage = vantagescope.OnlyThis()
	default:
		out.Vantage = vantagescope.Some()
	}
	c.JSON(200, out)
}
