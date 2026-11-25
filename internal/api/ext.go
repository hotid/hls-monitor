package api

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/hotid/hls-monitor/internal/auth"
	"github.com/hotid/hls-monitor/internal/store"
	"github.com/hotid/hls-monitor/internal/types/agentkind"
	"github.com/hotid/hls-monitor/internal/types/errcode"
	"github.com/hotid/hls-monitor/internal/types/id"
	"github.com/hotid/hls-monitor/internal/types/level"
	"github.com/hotid/hls-monitor/internal/types/severity"
	"github.com/hotid/hls-monitor/internal/types/streamstatus"
	"github.com/hotid/hls-monitor/internal/types/summarykey"
	"github.com/hotid/hls-monitor/internal/types/variantkey"
	"github.com/hotid/hls-monitor/internal/types/variantkind"
)

func (s *Server) requireAPIToken(c *gin.Context) {
	token := bearer(c)
	if token == "" {
		fail(c, http.StatusUnauthorized, errcode.Unauthorized(), "API token required: Authorization: Bearer <token>")
		return
	}
	hash := auth.HashToken(token)
	ok, err := s.st.APITokenValid(c.Request.Context(), hash)
	if s.storeError(c, err) {
		return
	}
	if !ok {
		fail(c, http.StatusUnauthorized, errcode.Unauthorized(), "unknown API token")
		return
	}
	_ = s.st.TouchAPIToken(c.Request.Context(), hash, s.now())
	c.Next()
}

// statusCode turns a status into a number that monitoring systems can
// compare: the higher, the worse.
func statusCode(st streamstatus.Status) int {
	switch st {
	case streamstatus.OK():
		return 0
	case streamstatus.Warn():
		return 1
	case streamstatus.Fail():
		return 2
	case streamstatus.NoData():
		return 3
	}
	return -1 // disabled
}

// extStream is a discovery row for a stream.
type extStream struct {
	ID      id.Stream   `json:"id"`
	Name    string      `json:"name"`
	URL     string      `json:"url"`
	Enabled bool        `json:"enabled"`
	Level   level.Level `json:"level" enums:"playlist,header,segments,container"`
}

// extDiscoverStreams godoc
//
//	@Summary	Streams for low-level discovery
//	@Tags		external
//	@Produce	json
//	@Param		Authorization	header	string	true	"Bearer API token"
//	@Success	200				{array}	extStream
//	@Failure	401				{object}	apiError
//	@Router		/api/ext/v1/discovery/streams [get]
func (s *Server) extDiscoverStreams(c *gin.Context) {
	streams, err := s.st.ListStreams(c.Request.Context())
	if s.storeError(c, err) {
		return
	}
	out := make([]extStream, 0, len(streams))
	for _, st := range streams {
		out = append(out, extStream{ID: st.ID, Name: st.Name, URL: st.URL, Enabled: st.Enabled, Level: st.Level})
	}
	c.JSON(http.StatusOK, out)
}

// extVariant is a discovery row for a variant of a stream.
type extVariant struct {
	ID         id.Variant       `json:"id"`
	StreamID   id.Stream        `json:"stream_id"`
	StreamName string           `json:"stream_name"`
	Kind       variantkind.Kind `json:"kind" enums:"master,media,video,audio,subtitles"`
	Key        variantkey.Key   `json:"key"`
	Name       string           `json:"name"`
	Bandwidth  int64            `json:"bandwidth"`
	Resolution string           `json:"resolution"`
}

// extDiscoverVariants godoc
//
//	@Summary	Active variants of all streams for low-level discovery
//	@Tags		external
//	@Produce	json
//	@Param		Authorization	header	string	true	"Bearer API token"
//	@Success	200				{array}	extVariant
//	@Failure	401				{object}	apiError
//	@Router		/api/ext/v1/discovery/variants [get]
func (s *Server) extDiscoverVariants(c *gin.Context) {
	ctx := c.Request.Context()
	streams, err := s.st.ListStreams(ctx)
	if s.storeError(c, err) {
		return
	}
	names := map[id.Stream]string{}
	for _, st := range streams {
		names[st.ID] = st.Name
	}
	variants, err := s.st.ListAllVariants(ctx)
	if s.storeError(c, err) {
		return
	}
	out := make([]extVariant, 0, len(variants))
	for _, v := range variants {
		out = append(out, extVariant{ID: v.ID, StreamID: v.StreamID, StreamName: names[v.StreamID], Kind: v.Kind, Key: v.Key,
			Name: v.Name, Bandwidth: v.Bandwidth, Resolution: v.Resolution})
	}
	c.JSON(http.StatusOK, out)
}

// extAgent is a discovery row for an agent, with its current reachability.
type extAgent struct {
	ID       id.Agent       `json:"id"`
	Name     string         `json:"name"`
	Kind     agentkind.Kind `json:"kind" enums:"local,remote"`
	Enabled  bool           `json:"enabled"`
	Online   bool           `json:"online"`
	LastSeen *time.Time     `json:"last_seen"`
}

// extDiscoverAgents godoc
//
//	@Summary	Agents for low-level discovery, with reachability
//	@Tags		external
//	@Produce	json
//	@Param		Authorization	header	string	true	"Bearer API token"
//	@Success	200				{array}	extAgent
//	@Failure	401				{object}	apiError
//	@Router		/api/ext/v1/discovery/agents [get]
func (s *Server) extDiscoverAgents(c *gin.Context) {
	agents, err := s.st.ListAgents(c.Request.Context())
	if s.storeError(c, err) {
		return
	}
	now := s.now()
	out := make([]extAgent, 0, len(agents))
	for _, a := range agents {
		out = append(out, extAgent{ID: a.ID, Name: a.Name, Kind: a.Kind, Enabled: a.Enabled, Online: s.agentOnline(a, now), LastSeen: a.LastSeen})
	}
	c.JSON(http.StatusOK, out)
}

// extAgentStatus is the state of a stream as seen from one agent.
type extAgentStatus struct {
	AgentID    id.Agent                   `json:"agent_id"`
	AgentName  string                     `json:"agent_name"`
	Status     streamstatus.Status        `json:"status"`
	StatusCode int                        `json:"status_code"`
	AgeSeconds *float64                   `json:"age_seconds"`
	Metrics    map[summarykey.Key]float64 `json:"metrics"`
}

// extStreamStatus is the current state of one stream in one document,
// meant to be fetched once and split into dependent items.
type extStreamStatusDoc struct {
	ID         id.Stream                 `json:"id"`
	Name       string                    `json:"name"`
	Status     streamstatus.Status       `json:"status"`
	StatusCode int                       `json:"status_code" example:"0"`
	Incidents  map[severity.Severity]int `json:"incidents"`
	Agents     []extAgentStatus          `json:"agents"`
	Variants   []extVariantDoc           `json:"variants,omitempty"`
}

// extVariantDoc is the latest state of a variant per agent.
type extVariantDoc struct {
	ID     id.Variant            `json:"id"`
	Kind   variantkind.Kind      `json:"kind"`
	Key    variantkey.Key        `json:"key"`
	States []*store.VariantState `json:"states"`
}

func (s *Server) extDoc(v *streamView, now time.Time) extStreamStatusDoc {
	doc := extStreamStatusDoc{ID: v.ID, Name: v.Name, Status: v.Status, StatusCode: statusCode(v.Status),
		Incidents: map[severity.Severity]int{severity.Warn(): v.OpenIncidents[severity.Warn()], severity.Crit(): v.OpenIncidents[severity.Crit()]},
		Agents:    make([]extAgentStatus, 0, len(v.Agents))}
	for _, a := range v.Agents {
		as := extAgentStatus{AgentID: a.AgentID, AgentName: a.AgentName, Status: a.Status, StatusCode: statusCode(a.Status), Metrics: a.Summary}
		if a.CheckedAt != nil {
			age := now.Sub(*a.CheckedAt).Seconds()
			as.AgeSeconds = &age
		}
		doc.Agents = append(doc.Agents, as)
	}
	return doc
}

// extStatus godoc
//
//	@Summary	Current state of all streams
//	@Description	status_code: 0 ok, 1 warning, 2 failure, 3 no data, -1 disabled.
//	@Tags		external
//	@Produce	json
//	@Param		Authorization	header	string	true	"Bearer API token"
//	@Success	200				{array}	extStreamStatusDoc
//	@Failure	401				{object}	apiError
//	@Router		/api/ext/v1/status [get]
func (s *Server) extStatus(c *gin.Context) {
	ctx := c.Request.Context()
	streams, err := s.st.ListStreams(ctx)
	if s.storeError(c, err) {
		return
	}
	sc, err := s.loadStatusContext(ctx)
	if s.storeError(c, err) {
		return
	}
	out := make([]extStreamStatusDoc, 0, len(streams))
	for _, st := range streams {
		out = append(out, s.extDoc(s.view(st, sc, false), sc.now))
	}
	c.JSON(http.StatusOK, out)
}

// extStreamStatus godoc
//
//	@Summary	Current state of one stream with per-variant metrics
//	@Description	status_code: 0 ok, 1 warning, 2 failure, 3 no data, -1 disabled.
//	@Tags		external
//	@Produce	json
//	@Param		Authorization	header		string	true	"Bearer API token"
//	@Param		id				path		int		true	"Stream ID"
//	@Success	200				{object}	extStreamStatusDoc
//	@Failure	401				{object}	apiError
//	@Failure	404				{object}	apiError
//	@Router		/api/ext/v1/streams/{id}/status [get]
func (s *Server) extStreamStatus(c *gin.Context) {
	streamID, ok := pathID[id.Stream](c)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	st, err := s.st.GetStream(ctx, streamID)
	if s.storeError(c, err) {
		return
	}
	sc, err := s.loadStatusContext(ctx)
	if s.storeError(c, err) {
		return
	}
	doc := s.extDoc(s.view(st, sc, false), sc.now)

	variants, err := s.st.ListVariants(ctx, streamID)
	if s.storeError(c, err) {
		return
	}
	states, err := s.st.VariantStates(ctx, streamID)
	if s.storeError(c, err) {
		return
	}
	byVariant := map[id.Variant][]*store.VariantState{}
	for _, vs := range states {
		byVariant[vs.VariantID] = append(byVariant[vs.VariantID], vs)
	}
	for _, v := range variants {
		if !v.Active {
			continue
		}
		vd := extVariantDoc{ID: v.ID, Kind: v.Kind, Key: v.Key, States: byVariant[v.ID]}
		if vd.States == nil {
			vd.States = []*store.VariantState{}
		}
		doc.Variants = append(doc.Variants, vd)
	}
	c.JSON(http.StatusOK, doc)
}
