package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/hotid/hls-monitor/internal/probe"
	"github.com/hotid/hls-monitor/internal/store"
)

// Slates are pictures an operator puts on air in place of a channel. A
// look cannot tell one from a programme by itself; somebody who sees one
// marks it, and from then on every stream showing it is told at the first
// look, by the slate's name.

// Slates returns the pictures known to be slates, for the checks.
func (s *Server) Slates() []probe.Slate {
	if p := s.slates.Load(); p != nil {
		return *p
	}
	return nil
}

// LoadSlates reads the slates into memory; the checks and the agents get
// them from there.
func (s *Server) LoadSlates(ctx context.Context) {
	rows, err := s.st.ListSlates(ctx)
	if err != nil {
		if ctx.Err() == nil {
			s.log.Warn("cannot read the slates", "error", err)
		}
		return
	}
	out := make([]probe.Slate, 0, len(rows))
	for _, sl := range rows {
		p := probe.Slate{ID: sl.ID, Name: sl.Name, Keys: sl.Keys, Own: sl.Own}
		for _, f := range sl.Frames {
			p.Frames = append(p.Frames, f.Cells)
		}
		out = append(out, p)
	}
	s.slates.Store(&out)
}

// slatesRevision is a fingerprint of the slates: it changes when one is
// marked or forgotten, and with it what an agent has to know.
func slatesRevision(slates []probe.Slate) string {
	if len(slates) == 0 {
		return ""
	}
	h := sha256.New()
	_ = json.NewEncoder(h).Encode(slates)
	return hex.EncodeToString(h.Sum(nil)[:6])
}

// listSlates godoc
//
//	@Summary	Pictures known to be slates
//	@Description	A slate is what is put on air in place of a channel. A stream showing a picture marked as one gets the incident picture_slate at the first look. The picture of a frame is at /api/v1/slates/frames/{id}.
//	@Tags		slates
//	@Security	SessionCookie
//	@Security	BearerToken
//	@Produce	json
//	@Success	200	{array}	store.Slate
//	@Router		/api/v1/slates [get]
func (s *Server) listSlates(c *gin.Context) {
	rows, err := s.st.ListSlates(c.Request.Context())
	if s.storeError(c, err) {
		return
	}
	c.JSON(http.StatusOK, rows)
}

type slateRequest struct {
	// Name of the slate; a picture marked under a name that exists is
	// added to that slate, which is how one that moves gets its frames.
	Name string `json:"name"`
	// The picture: the latest look at a stream from a vantage point (the
	// server's own by default), or the one kept with an incident.
	StreamID   int64 `json:"stream_id"`
	AgentID    int64 `json:"agent_id"`
	IncidentID int64 `json:"incident_id"`
	// Own marks not a slate but a clip the channel itself plays over and
	// over: a stream playing it is left alone from then on. It takes an
	// incident of a clip going round (picture_loop, audio_loop), whose
	// coded frames the clip is known by.
	Own bool `json:"own"`
}

// markSlate godoc
//
//	@Summary	Mark a picture or a clip as a slate, or a clip as the channel's own
//	@Description	A picture is known by what it looks like, to a decoder. A clip going round (an incident picture_loop or audio_loop) is known by its coded frames as well: a stream playing it again gets picture_slate at the first segment, with no decoding — or, marked with own, nothing at all.
//	@Tags		slates
//	@Security	SessionCookie
//	@Security	BearerToken
//	@Accept		json
//	@Produce	json
//	@Param		request	body		slateRequest	true	"Which picture, and the name of the slate"
//	@Success	200		{object}	store.Slate
//	@Failure	400		{object}	apiError
//	@Failure	404		{object}	apiError
//	@Failure	409		{object}	apiError
//	@Router		/api/v1/slates [post]
func (s *Server) markSlate(c *gin.Context) {
	var req slateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		badRequest(c, "invalid JSON body")
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	switch {
	case req.Name == "" || len(req.Name) > 100:
		badRequest(c, "name must be 1 to 100 characters long")
		return
	case req.StreamID <= 0 && req.IncidentID <= 0:
		badRequest(c, "stream_id or incident_id is required")
		return
	}
	if req.AgentID <= 0 {
		req.AgentID = store.LocalAgentID
	}
	ctx := c.Request.Context()
	sl, err := s.st.MarkSlate(ctx, req.Name, req.StreamID, req.AgentID, req.IncidentID, req.Own, s.now())
	switch {
	case errors.Is(err, store.ErrNoPicture) && req.Own:
		fail(c, http.StatusNotFound, "no_clip", "there is nothing to know the clip by: only an incident of a clip going round can be marked so")
		return
	case errors.Is(err, store.ErrNoPicture):
		fail(c, http.StatusNotFound, "no_picture", "there is no picture to mark: the stream was not looked at yet")
		return
	case errors.Is(err, store.ErrConflict):
		fail(c, http.StatusConflict, "conflict", "the name is taken: a slate and a channel's own clip cannot share one")
		return
	}
	if s.storeError(c, err) {
		return
	}
	s.LoadSlates(ctx)
	s.log.Info("a picture was marked as a slate", "slate", sl.Name, "own", sl.Own, "frames", len(sl.Frames), "coded_frames", sl.CodedFrames, "by", currentUser(c).Username)
	c.JSON(http.StatusOK, sl)
}

// deleteSlate godoc
//
//	@Summary	Forget a slate
//	@Tags		slates
//	@Security	SessionCookie
//	@Security	BearerToken
//	@Param		id	path	int	true	"Slate ID"
//	@Success	204
//	@Failure	404	{object}	apiError
//	@Router		/api/v1/slates/{id} [delete]
func (s *Server) deleteSlate(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	if s.storeError(c, s.st.DeleteSlate(ctx, id)) {
		return
	}
	s.LoadSlates(ctx)
	c.Status(http.StatusNoContent)
}

// deleteSlateFrame godoc
//
//	@Summary	Forget one picture of a slate
//	@Description	The slate goes with its last picture.
//	@Tags		slates
//	@Security	SessionCookie
//	@Security	BearerToken
//	@Param		id	path	int	true	"Frame ID"
//	@Success	204
//	@Failure	404	{object}	apiError
//	@Router		/api/v1/slates/frames/{id} [delete]
func (s *Server) deleteSlateFrame(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	if s.storeError(c, s.st.DeleteSlateFrame(ctx, id)) {
		return
	}
	s.LoadSlates(ctx)
	c.Status(http.StatusNoContent)
}

// slateFrame godoc
//
//	@Summary	The picture of one frame of a slate
//	@Tags		slates
//	@Security	SessionCookie
//	@Security	BearerToken
//	@Produce	jpeg
//	@Param		id	path	int	true	"Frame ID"
//	@Success	200
//	@Failure	404	{object}	apiError
//	@Router		/api/v1/slates/frames/{id} [get]
func (s *Server) slateFrame(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		fail(c, http.StatusNotFound, "not_found", "not found")
		return
	}
	b, at, err := s.st.SlateFrameJPEG(c.Request.Context(), id)
	if s.storeError(c, err) {
		return
	}
	jpegAnswer(c, b, at)
}
