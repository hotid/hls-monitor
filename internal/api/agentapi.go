package api

import (
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/hotid/hls-monitor/internal/ingest"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/hotid/hls-monitor/internal/agentproto"
	"github.com/hotid/hls-monitor/internal/auth"
	"github.com/hotid/hls-monitor/internal/probe"
	"github.com/hotid/hls-monitor/internal/store"
)

const (
	agentPollSec       = 15
	agentResultsMaxLen = 64 << 20
)

func (s *Server) requireAgent(c *gin.Context) {
	key := bearer(c)
	if key == "" {
		fail(c, http.StatusUnauthorized, "unauthorized", "agent key required")
		return
	}
	a, err := s.st.AgentByKey(c.Request.Context(), auth.HashToken(key))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			fail(c, http.StatusUnauthorized, "unauthorized", "unknown agent key")
			return
		}
		s.storeError(c, err)
		return
	}
	c.Set(ctxAgent, a)
	c.Next()
}

func currentAgent(c *gin.Context) *store.Agent {
	a, _ := c.MustGet(ctxAgent).(*store.Agent)
	return a
}

// agentEnroll godoc
//
//	@Summary	Exchange an enrollment token for a permanent agent key
//	@Description	The token works once. The key is returned only in this response.
//	@Tags		agent
//	@Accept		json
//	@Produce	json
//	@Param		request	body		agentproto.EnrollRequest	true	"Enrollment token"
//	@Success	200		{object}	agentproto.EnrollResponse
//	@Failure	401		{object}	apiError
//	@Router		/api/agent/v1/enroll [post]
func (s *Server) agentEnroll(c *gin.Context) {
	var req agentproto.EnrollRequest
	if err := c.ShouldBindJSON(&req); err != nil || req.Token == "" {
		badRequest(c, "token is required")
		return
	}
	now := s.now()
	ipKey := "enroll:" + c.ClientIP()
	if s.logins.blocked(now, ipKey) {
		fail(c, http.StatusTooManyRequests, "too_many_attempts", "too many failed attempts, try again in a few minutes")
		return
	}
	key, keyHash, err := auth.NewToken()
	if s.storeError(c, err) {
		return
	}
	a, err := s.st.EnrollAgent(c.Request.Context(), auth.HashToken(req.Token), keyHash, now)
	if errors.Is(err, store.ErrNotFound) {
		s.logins.fail(now, ipKey)
		fail(c, http.StatusUnauthorized, "bad_token", "the enrollment token is wrong, already used or expired")
		return
	}
	if s.storeError(c, err) {
		return
	}
	_ = s.st.TouchAgent(c.Request.Context(), a.ID, now, req.Version, c.ClientIP(), nil)
	s.log.Info("agent enrolled", "agent", a.Name, "ip", c.ClientIP())
	c.JSON(http.StatusOK, agentproto.EnrollResponse{AgentID: a.ID, Name: a.Name, Key: key})
}

// agentConfig godoc
//
//	@Summary	What the agent has to check
//	@Description	Doubles as the agent's heartbeat. Answers 304 when the revision passed in the query is still current.
//	@Tags		agent
//	@Produce	json
//	@Param		Authorization	header		string	true	"Bearer agent key"
//	@Param		revision		query		string	false	"Revision the agent already has"
//	@Success	200				{object}	agentproto.Config
//	@Success	304
//	@Failure	401				{object}	apiError
//	@Router		/api/agent/v1/config [get]
func (s *Server) agentConfig(c *gin.Context) {
	a := currentAgent(c)
	ctx := c.Request.Context()
	now := s.now()

	var offset *float64
	if v, err := strconv.ParseInt(c.GetHeader(agentproto.HeaderTime), 10, 64); err == nil && v > 0 {
		d := float64(v - now.UnixMilli())
		offset = &d
	}
	_ = s.st.TouchAgent(ctx, a.ID, now, c.GetHeader(agentproto.HeaderVersion), c.ClientIP(), offset)

	targets, err := s.st.TargetsForAgent(ctx, a.ID)
	if s.storeError(c, err) {
		return
	}
	cfg := agentproto.Config{
		AgentID: a.ID, Name: a.Name, Enabled: a.Enabled,
		Targets: targets, PollSec: agentPollSec, ServerTime: now.UnixMilli(),
	}
	cfg.Revision = targetsRevision(targets)
	if c.Query("revision") == cfg.Revision {
		c.Status(http.StatusNotModified)
		return
	}
	c.JSON(http.StatusOK, cfg)
}

// targetsRevision is a fingerprint of a target list: it changes when a
// stream is added, removed or edited.
func targetsRevision(targets []probe.Target) string {
	h := sha256.New()
	_ = json.NewEncoder(h).Encode(targets)
	return hex.EncodeToString(h.Sum(nil)[:12])
}

// agentResults godoc
//
//	@Summary	Deliver check results
//	@Description	The body may be gzip-compressed (Content-Encoding: gzip). Results for streams not assigned to the agent are rejected.
//	@Tags		agent
//	@Accept		json
//	@Produce	json
//	@Param		Authorization	header		string						true	"Bearer agent key"
//	@Param		request			body		agentproto.ResultsRequest	true	"Results"
//	@Success	200				{object}	agentproto.ResultsResponse
//	@Failure	401				{object}	apiError
//	@Failure	403				{object}	apiError
//	@Router		/api/agent/v1/results [post]
func (s *Server) agentResults(c *gin.Context) {
	a := currentAgent(c)
	if !a.Enabled {
		fail(c, http.StatusForbidden, "agent_disabled", "the agent is disabled")
		return
	}
	var body io.Reader = http.MaxBytesReader(c.Writer, c.Request.Body, agentResultsMaxLen)
	if strings.EqualFold(c.GetHeader("Content-Encoding"), "gzip") {
		zr, err := gzip.NewReader(body)
		if err != nil {
			badRequest(c, "invalid gzip body")
			return
		}
		defer zr.Close()
		body = io.LimitReader(zr, 4*agentResultsMaxLen)
	}
	var req agentproto.ResultsRequest
	if err := json.NewDecoder(body).Decode(&req); err != nil {
		badRequest(c, "invalid request body")
		return
	}

	ctx := c.Request.Context()
	var resp agentproto.ResultsResponse
	assigned := map[int64]bool{}
	for i, r := range req.Results {
		if r == nil {
			resp.Rejected++
			continue
		}
		ok, known := assigned[r.TargetID]
		if !known {
			var err error
			if ok, err = s.st.StreamAssigned(ctx, r.TargetID, a.ID); err != nil {
				s.log.Error("cannot check stream assignment", "error", err)
				resp.Failed = len(req.Results) - i
				break
			}
			assigned[r.TargetID] = ok
		}
		if !ok {
			resp.Rejected++
			continue
		}
		err := s.ing.Apply(ctx, a.ID, r)
		if errors.Is(err, store.ErrNotFound) {
			resp.Rejected++
			continue
		}
		if errors.Is(err, ingest.ErrStale) {
			resp.Duplicates++
			continue
		}
		if err != nil {
			// Earlier results are stored; the agent resends from this one on.
			s.log.Error("cannot store agent result", "agent", a.Name, "stream", r.TargetID, "error", err)
			resp.Failed = len(req.Results) - i
			break
		}
		resp.Accepted++
	}
	_ = s.st.TouchAgent(ctx, a.ID, s.now(), c.GetHeader(agentproto.HeaderVersion), c.ClientIP(), nil)
	c.JSON(http.StatusOK, resp)
}
