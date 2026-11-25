package api

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/hotid/hls-monitor/internal/store"
	"github.com/hotid/hls-monitor/internal/types/agentkind"
	"github.com/hotid/hls-monitor/internal/types/errcode"
	"github.com/hotid/hls-monitor/internal/types/id"
	"github.com/hotid/hls-monitor/internal/types/level"
	"github.com/hotid/hls-monitor/internal/types/picturemode"
	"github.com/hotid/hls-monitor/internal/types/role"
	"github.com/hotid/hls-monitor/internal/types/severity"
	"github.com/hotid/hls-monitor/internal/types/streamstatus"
	"github.com/hotid/hls-monitor/internal/types/summarykey"
)

// agentStatus is the state of a stream as seen from one agent.
type agentStatus struct {
	AgentID   id.Agent                   `json:"agent_id"`
	AgentName string                     `json:"agent_name"`
	Status    streamstatus.Status        `json:"status" swaggertype:"string" enums:"ok,warn,fail,nodata,disabled"`
	CheckedAt *time.Time                 `json:"checked_at"`
	Summary   map[summarykey.Key]float64 `json:"summary"`
}

// streamView is a stream with its current state.
type streamView struct {
	*store.Stream
	Status        streamstatus.Status       `json:"status" swaggertype:"string" enums:"ok,warn,fail,nodata,disabled"`
	Agents        []agentStatus             `json:"agents"`
	OpenIncidents map[severity.Severity]int `json:"open_incidents"`
	// Picture says which look at the stream has a picture to show (see
	// /api/v1/streams/{id}/picture); absent when it was never looked at.
	Picture *store.PictureRef `json:"picture_ref,omitempty"`
}

// statusContext is what is needed to judge the state of streams.
type statusContext struct {
	now       time.Time
	agents    map[id.Agent]*store.Agent
	states    map[id.Stream]map[id.Agent]*store.StreamState
	incidents map[id.Stream]map[severity.Severity]int
	pictures  map[id.Stream]store.PictureRef
}

func (s *Server) agentOnline(a *store.Agent, now time.Time) bool {
	if a.Kind == agentkind.Local() {
		return true
	}
	return a.LastSeen != nil && now.Sub(*a.LastSeen) <= s.cfg.AgentOfflineAfter
}

func (s *Server) loadStatusContext(ctx context.Context) (*statusContext, error) {
	sc := &statusContext{now: s.now(), agents: map[id.Agent]*store.Agent{}, states: map[id.Stream]map[id.Agent]*store.StreamState{}}
	agents, err := s.st.ListAgents(ctx)
	if err != nil {
		return nil, err
	}
	for _, a := range agents {
		sc.agents[a.ID] = a
	}
	states, err := s.st.StreamStates(ctx, id.Stream{})
	if err != nil {
		return nil, err
	}
	for _, st := range states {
		if sc.states[st.StreamID] == nil {
			sc.states[st.StreamID] = map[id.Agent]*store.StreamState{}
		}
		sc.states[st.StreamID][st.AgentID] = st
	}
	if sc.incidents, err = s.st.OpenIncidentCounts(ctx); err != nil {
		return nil, err
	}
	if sc.pictures, err = s.st.LatestPictures(ctx); err != nil {
		return nil, err
	}
	return sc, nil
}

// view judges a stream: every assigned agent contributes its latest result
// if that result is fresh and the agent is reachable; the stream takes the
// worst of them.
func (s *Server) view(st *store.Stream, sc *statusContext, admin bool) *streamView {
	v := &streamView{Stream: st, Status: streamstatus.NoData(), Agents: []agentStatus{}, OpenIncidents: map[severity.Severity]int{}}
	if n := sc.incidents[st.ID]; n != nil {
		v.OpenIncidents = n
	}
	if p, ok := sc.pictures[st.ID]; ok {
		v.Picture = &p
	}
	if !admin && len(st.Headers) > 0 {
		// Request headers often carry credentials.
		masked := make(map[string]string, len(st.Headers))
		for k := range st.Headers {
			masked[k] = "***"
		}
		cp := *st
		cp.Headers = masked
		v.Stream = &cp
	}
	freshFor := max(90*time.Second, time.Duration(3*st.IntervalSec*float64(time.Second)))
	best := streamstatus.NoData()
	for _, id := range st.AgentIDs {
		a := sc.agents[id]
		if a == nil {
			continue
		}
		as := agentStatus{AgentID: id, AgentName: a.Name, Status: streamstatus.NoData(), Summary: map[summarykey.Key]float64{}}
		if state := sc.states[st.ID][id]; state != nil {
			t := state.CheckedAt
			as.CheckedAt = &t
			as.Summary = state.Summary
			if st.Enabled && a.Enabled && s.agentOnline(a, sc.now) && sc.now.Sub(t) <= freshFor {
				as.Status = streamstatus.Of(state.Status)
			}
		}
		if !st.Enabled || !a.Enabled {
			as.Status = streamstatus.Disabled()
		}
		if as.Status.Worse(best) {
			best = as.Status
		}
		v.Agents = append(v.Agents, as)
	}
	v.Status = best
	if !st.Enabled {
		v.Status = streamstatus.Disabled()
	}
	return v
}

// streamRequest is the body for creating and editing a stream, as it is
// written by whoever sends it: input reads it into what the monitor knows.
type streamRequest struct {
	Name        string            `json:"name"`
	URL         string            `json:"url"`
	Enabled     *bool             `json:"enabled"`
	Level       string            `json:"level" enums:"playlist,header,segments,container"`
	IntervalSec float64           `json:"interval_sec"`
	TimeoutSec  float64           `json:"timeout_sec"`
	MaxSegments int               `json:"max_segments"`
	Headers     map[string]string `json:"headers"`
	Insecure    bool              `json:"insecure"`
	// Picture: "" to check the picture and the sound, "static" when a
	// still picture is what the stream shows, "off" not to look.
	Picture  string     `json:"picture"`
	Notes    string     `json:"notes"`
	AgentIDs []id.Agent `json:"agent_ids"`
}

func (r *streamRequest) input() (store.StreamInput, string) {
	in := store.StreamInput{
		Name:        strings.TrimSpace(r.Name),
		URL:         strings.TrimSpace(r.URL),
		Enabled:     r.Enabled == nil || *r.Enabled,
		Level:       level.Segments(),
		IntervalSec: r.IntervalSec,
		TimeoutSec:  r.TimeoutSec,
		MaxSegments: r.MaxSegments,
		Headers:     map[string]string{},
		Insecure:    r.Insecure,
		Notes:       strings.TrimSpace(r.Notes),
		AgentIDs:    r.AgentIDs,
	}
	var levelErr, pictureErr error
	if r.Level != "" {
		in.Level, levelErr = level.Parse(r.Level)
	}
	in.Picture, pictureErr = picturemode.Parse(r.Picture)
	if in.AgentIDs == nil {
		in.AgentIDs = []id.Agent{store.LocalAgentID()}
	}
	switch {
	case in.Name == "" || len(in.Name) > 100:
		return in, "name must be 1 to 100 characters long"
	case levelErr != nil:
		return in, "level must be one of: playlist, header, segments, container"
	case pictureErr != nil:
		return in, "picture must be empty, static or off"
	case in.IntervalSec != 0 && (in.IntervalSec < 1 || in.IntervalSec > 3600):
		return in, "interval_sec must be 0 (automatic) or between 1 and 3600"
	case in.TimeoutSec != 0 && (in.TimeoutSec < 1 || in.TimeoutSec > 120):
		return in, "timeout_sec must be 0 (default) or between 1 and 120"
	case in.MaxSegments < 0 || in.MaxSegments > 20:
		return in, "max_segments must be between 0 and 20"
	case len(in.Notes) > 2000:
		return in, "notes are too long"
	case len(r.Headers) > 20:
		return in, "too many request headers"
	}
	u, err := url.Parse(in.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return in, "url must be an absolute http or https URL"
	}
	for k, v := range r.Headers {
		k = strings.TrimSpace(k)
		if k == "" || strings.ContainsAny(k, " \t\r\n:") || strings.ContainsAny(v, "\r\n") || len(v) > 4096 {
			return in, "invalid request header " + k
		}
		in.Headers[http.CanonicalHeaderKey(k)] = v
	}
	return in, ""
}

func isAdmin(c *gin.Context) bool { return currentUser(c).Role == role.Admin() }

// listStreams godoc
//
//	@Summary	List streams with their current state
//	@Tags		streams
//	@Produce	json
//	@Success	200	{array}	streamView
//	@Router		/api/v1/streams [get]
func (s *Server) listStreams(c *gin.Context) {
	ctx := c.Request.Context()
	streams, err := s.st.ListStreams(ctx)
	if s.storeError(c, err) {
		return
	}
	sc, err := s.loadStatusContext(ctx)
	if s.storeError(c, err) {
		return
	}
	out := make([]*streamView, 0, len(streams))
	for _, st := range streams {
		out = append(out, s.view(st, sc, isAdmin(c)))
	}
	c.JSON(http.StatusOK, out)
}

// variantView is a playlist of a stream with its state per agent.
type variantView struct {
	*store.Variant
	States []*store.VariantState `json:"states"`
}

// streamDetail is everything the stream page needs at once.
type streamDetail struct {
	streamView
	Variants []variantView `json:"variants"`
}

// getStream godoc
//
//	@Summary	Stream with variants and their latest metrics
//	@Tags		streams
//	@Produce	json
//	@Param		id	path		int	true	"Stream ID"
//	@Success	200	{object}	streamDetail
//	@Failure	404	{object}	apiError
//	@Router		/api/v1/streams/{id} [get]
func (s *Server) getStream(c *gin.Context) {
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
	out := streamDetail{streamView: *s.view(st, sc, isAdmin(c)), Variants: make([]variantView, 0, len(variants))}
	for _, v := range variants {
		vv := variantView{Variant: v, States: byVariant[v.ID]}
		if vv.States == nil {
			vv.States = []*store.VariantState{}
		}
		out.Variants = append(out.Variants, vv)
	}
	c.JSON(http.StatusOK, out)
}

func (s *Server) respondStream(c *gin.Context, st *store.Stream, status int) {
	sc, err := s.loadStatusContext(c.Request.Context())
	if s.storeError(c, err) {
		return
	}
	c.JSON(status, s.view(st, sc, true))
}

// createStream godoc
//
//	@Summary	Add a stream
//	@Tags		streams
//	@Accept		json
//	@Produce	json
//	@Param		request	body		streamRequest	true	"Stream settings"
//	@Success	201		{object}	streamView
//	@Failure	400		{object}	apiError
//	@Failure	409		{object}	apiError
//	@Router		/api/v1/streams [post]
func (s *Server) createStream(c *gin.Context) {
	var req streamRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		badRequest(c, "invalid request body")
		return
	}
	in, msg := req.input()
	if msg != "" {
		badRequest(c, msg)
		return
	}
	st, err := s.st.CreateStream(c.Request.Context(), in, s.now())
	if s.agentError(c, err) {
		return
	}
	s.configChanged(c.Request.Context())
	s.respondStream(c, st, http.StatusCreated)
}

// agentError reports a missing agent as a bad request rather than "not found".
func (s *Server) agentError(c *gin.Context, err error) bool {
	if err == store.ErrNotFound {
		badRequest(c, "agent_ids refers to an agent that does not exist")
		return true
	}
	return s.storeError(c, err)
}

// updateStream godoc
//
//	@Summary	Change a stream
//	@Description	Replaces all settings. Checking restarts with a clean state.
//	@Tags		streams
//	@Accept		json
//	@Produce	json
//	@Param		id		path		int				true	"Stream ID"
//	@Param		request	body		streamRequest	true	"Stream settings"
//	@Success	200		{object}	streamView
//	@Failure	400		{object}	apiError
//	@Failure	404		{object}	apiError
//	@Failure	409		{object}	apiError
//	@Router		/api/v1/streams/{id} [put]
func (s *Server) updateStream(c *gin.Context) {
	streamID, ok := pathID[id.Stream](c)
	if !ok {
		return
	}
	var req streamRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		badRequest(c, "invalid request body")
		return
	}
	in, msg := req.input()
	if msg != "" {
		badRequest(c, msg)
		return
	}
	ctx := c.Request.Context()
	if _, err := s.st.GetStream(ctx, streamID); s.storeError(c, err) {
		return
	}
	st, err := s.st.UpdateStream(ctx, streamID, in, s.now())
	if s.agentError(c, err) {
		return
	}
	s.configChanged(ctx)
	s.respondStream(c, st, http.StatusOK)
}

// deleteStream godoc
//
//	@Summary	Delete a stream with its history
//	@Tags		streams
//	@Param		id	path	int	true	"Stream ID"
//	@Success	204
//	@Failure	404	{object}	apiError
//	@Router		/api/v1/streams/{id} [delete]
func (s *Server) deleteStream(c *gin.Context) {
	streamID, ok := pathID[id.Stream](c)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	if s.storeError(c, s.st.DeleteStream(ctx, streamID)) {
		return
	}
	// What was captured of it goes with it.
	if s.captures != nil {
		if n := s.captures.Forget(streamID, id.Agent{}); n > 0 {
			s.log.Info("the captures of a deleted stream were removed", "stream", streamID, "captures", n)
		}
	}
	s.configChanged(ctx)
	s.purgeSeriesSoon()
	c.Status(http.StatusNoContent)
}

func (s *Server) setEnabled(c *gin.Context, enabled bool) {
	streamID, ok := pathID[id.Stream](c)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	if s.storeError(c, s.st.SetStreamEnabled(ctx, streamID, enabled, s.now())) {
		return
	}
	s.configChanged(ctx)
	st, err := s.st.GetStream(ctx, streamID)
	if s.storeError(c, err) {
		return
	}
	s.respondStream(c, st, http.StatusOK)
}

// enableStream godoc
//
//	@Summary	Resume checking a stream
//	@Tags		streams
//	@Produce	json
//	@Param		id	path		int	true	"Stream ID"
//	@Success	200	{object}	streamView
//	@Failure	404	{object}	apiError
//	@Router		/api/v1/streams/{id}/enable [post]
func (s *Server) enableStream(c *gin.Context) { s.setEnabled(c, true) }

// disableStream godoc
//
//	@Summary	Stop checking a stream
//	@Description	Open incidents of the stream are closed.
//	@Tags		streams
//	@Produce	json
//	@Param		id	path		int	true	"Stream ID"
//	@Success	200	{object}	streamView
//	@Failure	404	{object}	apiError
//	@Router		/api/v1/streams/{id}/disable [post]
func (s *Server) disableStream(c *gin.Context) { s.setEnabled(c, false) }

// checkStream godoc
//
//	@Summary	Check a stream now
//	@Description	Starts the next local check cycle immediately. Remote agents follow their own schedule.
//	@Tags		streams
//	@Param		id	path	int	true	"Stream ID"
//	@Success	202
//	@Failure	404	{object}	apiError
//	@Failure	409	{object}	apiError
//	@Router		/api/v1/streams/{id}/check [post]
func (s *Server) checkStream(c *gin.Context) {
	streamID, ok := pathID[id.Stream](c)
	if !ok {
		return
	}
	if _, err := s.st.GetStream(c.Request.Context(), streamID); s.storeError(c, err) {
		return
	}
	if s.local == nil || !s.local.CheckNow(streamID) {
		fail(c, http.StatusConflict, errcode.NotLocal(), "the stream is not checked by the server itself")
		return
	}
	c.Status(http.StatusAccepted)
}
