package api

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/hotid/hls-monitor/internal/probe"
	"github.com/hotid/hls-monitor/internal/store"
)

// Statuses shown for a stream. The first three come from check results.
const (
	statusOK       = "ok"
	statusWarn     = "warn"
	statusFail     = "fail"
	statusNoData   = "nodata"
	statusDisabled = "disabled"
)

// agentStatus is the state of a stream as seen from one agent.
type agentStatus struct {
	AgentID   int64              `json:"agent_id"`
	AgentName string             `json:"agent_name"`
	Status    string             `json:"status"`
	CheckedAt *time.Time         `json:"checked_at"`
	Summary   map[string]float64 `json:"summary"`
}

// streamView is a stream with its current state.
type streamView struct {
	*store.Stream
	Status        string         `json:"status"`
	Agents        []agentStatus  `json:"agents"`
	OpenIncidents map[string]int `json:"open_incidents"`
}

// statusContext is what is needed to judge the state of streams.
type statusContext struct {
	now       time.Time
	agents    map[int64]*store.Agent
	states    map[int64]map[int64]*store.StreamState
	incidents map[int64]map[string]int
}

func (s *Server) agentOnline(a *store.Agent, now time.Time) bool {
	if a.Kind == store.AgentLocal {
		return true
	}
	return a.LastSeen != nil && now.Sub(*a.LastSeen) <= s.cfg.AgentOfflineAfter
}

func (s *Server) loadStatusContext(ctx context.Context) (*statusContext, error) {
	sc := &statusContext{now: s.now(), agents: map[int64]*store.Agent{}, states: map[int64]map[int64]*store.StreamState{}}
	agents, err := s.st.ListAgents(ctx)
	if err != nil {
		return nil, err
	}
	for _, a := range agents {
		sc.agents[a.ID] = a
	}
	states, err := s.st.StreamStates(ctx, 0)
	if err != nil {
		return nil, err
	}
	for _, st := range states {
		if sc.states[st.StreamID] == nil {
			sc.states[st.StreamID] = map[int64]*store.StreamState{}
		}
		sc.states[st.StreamID][st.AgentID] = st
	}
	if sc.incidents, err = s.st.OpenIncidentCounts(ctx); err != nil {
		return nil, err
	}
	return sc, nil
}

func statusRank(st string) int {
	switch st {
	case statusFail:
		return 3
	case statusWarn:
		return 2
	case statusOK:
		return 1
	}
	return 0
}

// view judges a stream: every assigned agent contributes its latest result
// if that result is fresh and the agent is reachable; the stream takes the
// worst of them.
func (s *Server) view(st *store.Stream, sc *statusContext, admin bool) *streamView {
	v := &streamView{Stream: st, Status: statusNoData, Agents: []agentStatus{}, OpenIncidents: map[string]int{}}
	if n := sc.incidents[st.ID]; n != nil {
		v.OpenIncidents = n
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
	best := 0
	for _, id := range st.AgentIDs {
		a := sc.agents[id]
		if a == nil {
			continue
		}
		as := agentStatus{AgentID: id, AgentName: a.Name, Status: statusNoData, Summary: map[string]float64{}}
		if state := sc.states[st.ID][id]; state != nil {
			t := state.CheckedAt
			as.CheckedAt = &t
			as.Summary = state.Summary
			if st.Enabled && a.Enabled && s.agentOnline(a, sc.now) && sc.now.Sub(t) <= freshFor {
				as.Status = state.Status
			}
		}
		if !st.Enabled || !a.Enabled {
			as.Status = statusDisabled
		}
		best = max(best, statusRank(as.Status))
		v.Agents = append(v.Agents, as)
	}
	switch {
	case !st.Enabled:
		v.Status = statusDisabled
	case best == 3:
		v.Status = statusFail
	case best == 2:
		v.Status = statusWarn
	case best == 1:
		v.Status = statusOK
	}
	return v
}

// streamRequest is the body for creating and editing a stream.
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
	Notes       string            `json:"notes"`
	AgentIDs    []int64           `json:"agent_ids"`
}

func (r *streamRequest) input() (store.StreamInput, string) {
	in := store.StreamInput{
		Name:        strings.TrimSpace(r.Name),
		URL:         strings.TrimSpace(r.URL),
		Enabled:     r.Enabled == nil || *r.Enabled,
		Level:       r.Level,
		IntervalSec: r.IntervalSec,
		TimeoutSec:  r.TimeoutSec,
		MaxSegments: r.MaxSegments,
		Headers:     map[string]string{},
		Insecure:    r.Insecure,
		Notes:       strings.TrimSpace(r.Notes),
		AgentIDs:    r.AgentIDs,
	}
	if in.Level == "" {
		in.Level = string(probe.LevelSegments)
	}
	if in.AgentIDs == nil {
		in.AgentIDs = []int64{store.LocalAgentID}
	}
	switch {
	case in.Name == "" || len(in.Name) > 100:
		return in, "name must be 1 to 100 characters long"
	case !probe.ValidLevel(probe.Level(in.Level)):
		return in, "level must be one of: playlist, header, segments, container"
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

func isAdmin(c *gin.Context) bool { return currentUser(c).Role == store.RoleAdmin }

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
	id, ok := pathID(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	st, err := s.st.GetStream(ctx, id)
	if s.storeError(c, err) {
		return
	}
	sc, err := s.loadStatusContext(ctx)
	if s.storeError(c, err) {
		return
	}
	variants, err := s.st.ListVariants(ctx, id)
	if s.storeError(c, err) {
		return
	}
	states, err := s.st.VariantStates(ctx, id)
	if s.storeError(c, err) {
		return
	}
	byVariant := map[int64][]*store.VariantState{}
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
	id, ok := pathID(c)
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
	if _, err := s.st.GetStream(ctx, id); s.storeError(c, err) {
		return
	}
	st, err := s.st.UpdateStream(ctx, id, in, s.now())
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
	id, ok := pathID(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	if s.storeError(c, s.st.DeleteStream(ctx, id)) {
		return
	}
	s.configChanged(ctx)
	// Series are removed in the background: it does not have to hold the response.
	go func() {
		dctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := s.mx.DeleteStream(dctx, id); err != nil {
			s.log.Warn("cannot delete series of a removed stream", "stream", id, "error", err)
		}
	}()
	c.Status(http.StatusNoContent)
}

func (s *Server) setEnabled(c *gin.Context, enabled bool) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	if s.storeError(c, s.st.SetStreamEnabled(ctx, id, enabled, s.now())) {
		return
	}
	s.configChanged(ctx)
	st, err := s.st.GetStream(ctx, id)
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
	id, ok := pathID(c)
	if !ok {
		return
	}
	if _, err := s.st.GetStream(c.Request.Context(), id); s.storeError(c, err) {
		return
	}
	if s.local == nil || !s.local.CheckNow(id) {
		fail(c, http.StatusConflict, "not_local", "the stream is not checked by the server itself")
		return
	}
	c.Status(http.StatusAccepted)
}
