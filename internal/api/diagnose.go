package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/hotid/hls-monitor/internal/agentproto"
	"github.com/hotid/hls-monitor/internal/probe"
	"github.com/hotid/hls-monitor/internal/store"
)

const (
	// maxDiagnoses bounds the live diagnoses running at once: each one polls
	// its stream every second and may download every segment in full.
	maxDiagnoses = 3
	// diagnoseSegments is how many segments per playlist one cycle may check.
	// A regular check takes three; a diagnosis must not skip what a burst of
	// short segments brings.
	diagnoseSegments = 10
)

// diagnoseEvent is one line of the diagnosis stream.
type diagnoseEvent struct {
	// Type is "start", "cycle" or "end".
	Type string    `json:"type"`
	Time time.Time `json:"time"`

	// Start: what is being checked and for how long.
	URL     string  `json:"url,omitempty"`
	Level   string  `json:"level,omitempty"`
	Seconds float64 `json:"seconds,omitempty"`

	// Cycle: its number from one, how long it took and everything it found.
	N      int           `json:"n,omitempty"`
	TookMs float64       `json:"took_ms,omitempty"`
	Result *probe.Result `json:"result,omitempty"`

	// End: "time" when the requested duration has passed; from an agent
	// it may instead say what went wrong.
	Reason string `json:"reason,omitempty"`
}

// diagnose godoc
//
//	@Summary		Check a stream every second and stream what each check finds
//	@Description	Runs a separate checker from the server itself, or with agent set from where that agent stands, apart from the regular checks: nothing it finds is stored, raises incidents or reaches the charts. The response is newline-delimited JSON, one event per line: "start", then a "cycle" per check with the full result, then "end". It lasts until the duration passes or the client disconnects. At most three diagnoses run at once.
//	@Tags			streams
//	@Produce		application/x-ndjson
//	@Param			id		path		int		true	"Stream ID"
//	@Param			level	query		string	false	"How deep to check: playlist, header, segments or container (default: the stream's own level)"
//	@Param			seconds	query		int		false	"How long to run (default 120, max 600)"
//	@Param			agent	query		int		false	"The agent to check from; the server itself by default"
//	@Success		200		{object}	diagnoseEvent
//	@Failure		400		{object}	apiError
//	@Failure		404		{object}	apiError
//	@Failure		409		{object}	apiError
//	@Failure		429		{object}	apiError
//	@Router			/api/v1/streams/{id}/diagnose [get]
func (s *Server) diagnose(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	st, err := s.st.GetStream(c.Request.Context(), id)
	if s.storeError(c, err) {
		return
	}
	level, length, agentID, ok := diagnoseAsked(c, st)
	if !ok || !s.canDiagnoseFrom(c, agentID) {
		return
	}
	select {
	case s.diagnoses <- struct{}{}:
		defer func() { <-s.diagnoses }()
	default:
		fail(c, http.StatusTooManyRequests, "diagnose_busy", "too many diagnoses are running, try again later")
		return
	}

	target := probe.Target{
		ID:          st.ID,
		URL:         st.URL,
		Level:       level,
		IntervalSec: 1,
		TimeoutSec:  st.TimeoutSec,
		MaxSegments: max(st.MaxSegments, diagnoseSegments),
		Headers:     st.Headers,
		Insecure:    st.Insecure,
	}

	c.Header("Content-Type", "application/x-ndjson")
	// A reverse proxy must pass every line on as it is written.
	c.Header("X-Accel-Buffering", "no")
	c.Status(http.StatusOK)
	enc := json.NewEncoder(c.Writer)
	send := func(e diagnoseEvent) bool {
		if enc.Encode(e) != nil {
			return false
		}
		c.Writer.Flush()
		return true
	}
	s.log.Info("diagnosis started", "stream", st.ID, "agent", agentID, "level", level, "seconds", length.Seconds(), "ip", c.ClientIP())
	if !send(diagnoseEvent{Type: "start", Time: s.now(), URL: st.URL, Level: string(level), Seconds: length.Seconds()}) {
		return
	}
	if agentID != store.LocalAgentID {
		// The agent checks and tells; a little time over the length is
		// left for its last word to arrive.
		actx, stop := context.WithTimeout(c.Request.Context(), length+20*time.Second)
		defer stop()
		reason := s.diagnoseThroughAgent(actx, agentID, agentproto.DiagnoseJob{Target: target, Seconds: int(length.Seconds())}, func(cy agentproto.DiagnoseCycle) bool {
			return send(diagnoseEvent{Type: "cycle", Time: cy.Result.Time, N: cy.N, TookMs: cy.TookMs, Result: cy.Result})
		})
		if reason != "" && c.Request.Context().Err() == nil {
			send(diagnoseEvent{Type: "end", Time: s.now(), Reason: reason})
		}
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), length)
	defer cancel()
	if s.diagnoseHere(ctx, target, send) && c.Request.Context().Err() == nil {
		send(diagnoseEvent{Type: "end", Time: s.now(), Reason: "time"})
	}
}

// diagnoseAsked reads what a diagnosis is asked for: at what level, for how
// long and from where. It answers the request itself when that cannot be.
func diagnoseAsked(c *gin.Context, st *store.Stream) (level probe.Level, length time.Duration, agentID int64, ok bool) {
	level = probe.Level(st.Level)
	if q := c.Query("level"); q != "" {
		if level = probe.Level(q); !probe.ValidLevel(level) {
			badRequest(c, "level must be playlist, header, segments or container")
			return level, 0, 0, false
		}
	}
	length = time.Duration(queryInt(c, "seconds")) * time.Second
	if length <= 0 {
		length = 2 * time.Minute
	}
	length = min(length, 10*time.Minute)
	// Where from: the server itself, or an agent that takes such work.
	agentID = store.LocalAgentID
	if q := c.Query("agent"); q != "" {
		n, err := strconv.ParseInt(q, 10, 64)
		if err != nil || n <= 0 {
			badRequest(c, "agent must be the ID of an agent")
			return level, 0, 0, false
		}
		agentID = n
	}
	return level, length, agentID, true
}

// canDiagnoseFrom tells whether a diagnosis can be run from a vantage
// point, and answers the request when it cannot.
func (s *Server) canDiagnoseFrom(c *gin.Context, agentID int64) bool {
	if agentID == store.LocalAgentID {
		if s.local == nil {
			fail(c, http.StatusConflict, "not_local", "this server does not run checks itself")
			return false
		}
		return true
	}
	a, err := s.st.GetAgent(c.Request.Context(), agentID)
	if s.storeError(c, err) {
		return false
	}
	s.reportsMu.Lock()
	rep, ok := s.reports[agentID]
	s.reportsMu.Unlock()
	if !a.Enabled || !ok || s.now().Sub(rep.at) > s.cfg.AgentOfflineAfter || !rep.status.Diagnoses {
		fail(c, http.StatusConflict, "agent_unavailable", "the agent is out of contact, disabled or too old to run a diagnosis")
		return false
	}
	return true
}

// diagnoseHere checks the stream from the server itself, once a second,
// until ctx is over, and sends every check. It tells false when the one
// it sends to is gone.
func (s *Server) diagnoseHere(ctx context.Context, target probe.Target, send func(diagnoseEvent) bool) bool {
	checker := s.local.Checker(target)
	for n := 1; ; n++ {
		start := time.Now()
		res := checker.Run(ctx)
		if ctx.Err() != nil {
			return true // a cancelled cycle reports failures that are not real
		}
		if !send(diagnoseEvent{Type: "cycle", Time: res.Time, N: n, TookMs: float64(time.Since(start).Microseconds()) / 1000, Result: res}) {
			return false
		}
		t := time.NewTimer(time.Until(start.Add(checker.Interval())))
		select {
		case <-ctx.Done():
		case <-t.C:
		}
		t.Stop()
		if ctx.Err() != nil {
			return true
		}
	}
}
