package api

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/hotid/hls-monitor/internal/agentproto"
	"github.com/hotid/hls-monitor/internal/capture"
	"github.com/hotid/hls-monitor/internal/store"
	"github.com/hotid/hls-monitor/internal/types/errcode"
	"github.com/hotid/hls-monitor/internal/types/id"
)

type capturesResponse struct {
	// Enabled tells whether captures are made at all (HLSMON_CAPTURE).
	Enabled  bool              `json:"enabled"`
	Captures []capture.Capture `json:"captures"`
	Usage    *capture.Usage    `json:"usage,omitempty"`
}

// listCaptures godoc
//
//	@Summary	Segments saved around findings
//	@Description	A capture is the segment a check suspected, a few before it and a few after, with the playlists they came from, what ffprobe says of them and a playlist that plays them with no stream to ask. It is made at the first suspicion and kept only when an incident came of it (state kept), somebody asked for it, or a decoder found fault with its segment (state found). Only the server's own checks capture, and only when HLSMON_CAPTURE is set.
//	@Tags		captures
//	@Security	SessionCookie
//	@Produce	json
//	@Param		stream	query		int	false	"Only the captures of this stream"
//	@Success	200		{object}	capturesResponse
//	@Router		/api/v1/captures [get]
func (s *Server) listCaptures(c *gin.Context) {
	out := capturesResponse{Captures: []capture.Capture{}}
	if s.captures != nil {
		out.Enabled = true
		out.Captures = s.captures.List(queryID[id.Stream](c, "stream"))
		u := s.captures.Usage()
		out.Usage = &u
	}
	c.JSON(http.StatusOK, out)
}

// captureArchive godoc
//
//	@Summary	A capture as an archive
//	@Description	Everything of the capture in one zip: capture.json describes the files, index.m3u8 plays the whole segments. Administrators only: playlists and addresses are in it as they were.
//	@Tags		captures
//	@Security	SessionCookie
//	@Produce	application/zip
//	@Param		id	path	string	true	"Capture ID"
//	@Success	200
//	@Failure	404	{object}	apiError
//	@Router		/api/v1/captures/{id}/archive [get]
func (s *Server) captureArchive(c *gin.Context) {
	id := c.Param("id")
	if s.captures == nil || !s.hasCapture(id) {
		fail(c, http.StatusNotFound, errcode.NotFound(), "not found")
		return
	}
	c.Header("Content-Type", "application/zip")
	c.Header("Content-Disposition", `attachment; filename="capture-`+id+`.zip"`)
	if err := s.captures.WriteZip(c.Writer, id); err != nil {
		s.log.Warn("a capture could not be sent", "capture", id, "error", err)
	}
}

func (s *Server) hasCapture(name string) bool {
	for _, cp := range s.captures.List(id.Stream{}) {
		if cp.ID == name && cp.Done {
			return true
		}
	}
	return false
}

// deleteCapture godoc
//
//	@Summary	Delete a capture
//	@Tags		captures
//	@Security	SessionCookie
//	@Param		id	path	string	true	"Capture ID"
//	@Success	204
//	@Failure	404	{object}	apiError
//	@Router		/api/v1/captures/{id} [delete]
func (s *Server) deleteCapture(c *gin.Context) {
	if s.captures == nil || !s.captures.Delete(c.Param("id")) {
		fail(c, http.StatusNotFound, errcode.NotFound(), "not found")
		return
	}
	s.log.Info("a capture was deleted", "capture", c.Param("id"), "by", currentUser(c).Username)
	c.Status(http.StatusNoContent)
}

// captureNow godoc
//
//	@Summary	Capture the segments of a stream now
//	@Description	The stream is checked at once and the segments of its picture are saved whatever the check finds: for what an operator sees or hears before the checks do. The capture appears in the list a moment later. At most one a minute for a stream.
//	@Tags		captures
//	@Security	SessionCookie
//	@Param		id	path	int	true	"Stream ID"
//	@Success	202
//	@Failure	404	{object}	apiError
//	@Failure	409	{object}	apiError
//	@Router		/api/v1/streams/{id}/capture [post]
func (s *Server) captureNow(c *gin.Context) {
	streamID, ok := pathID[id.Stream](c)
	if !ok {
		return
	}
	switch {
	case s.captures == nil:
		fail(c, http.StatusConflict, errcode.CapturesOff(), "captures are not made: HLSMON_CAPTURE is not set")
	case s.local != nil && s.local.Capture(streamID):
		s.log.Info("a capture was asked for", "stream", streamID, "by", currentUser(c).Username)
		c.Status(http.StatusAccepted)
	default:
		// The stream is checked by agents only: the first that can is asked.
		if agent := s.captureThroughAgent(c.Request.Context(), streamID); !agent.IsZero() {
			s.log.Info("a capture was asked for", "stream", streamID, "agent", agent, "by", currentUser(c).Username)
			c.Status(http.StatusAccepted)
			return
		}
		fail(c, http.StatusConflict, errcode.NotLocal(), "neither the server nor an agent in contact that captures checks this stream")
	}
}

// captureThroughAgent asks the agents that check a stream, one after
// another, for a capture of it, and returns the one that will make it.
func (s *Server) captureThroughAgent(ctx context.Context, stream id.Stream) id.Agent {
	st, err := s.st.GetStream(ctx, stream)
	if err != nil {
		return id.Agent{}
	}
	for _, agent := range st.AgentIDs {
		if agent == store.LocalAgentID() {
			continue
		}
		s.reportsMu.Lock()
		rep, ok := s.reports[agent]
		s.reportsMu.Unlock()
		if !ok || s.now().Sub(rep.at) > s.cfg.AgentOfflineAfter || !rep.status.Captures {
			continue
		}
		actx, cancel := context.WithTimeout(ctx, 30*time.Second)
		res, _, _ := s.fetchThroughAgent(actx, agent, agentproto.FetchRequest{Capture: &agentproto.CaptureJob{StreamID: stream}})
		cancel()
		if res.Status == http.StatusAccepted {
			return agent
		}
	}
	return id.Agent{}
}

// agentCapture godoc
//
//	@Summary		Take a capture an agent made
//	@Description	The body is the capture as a zip archive, the way /api/v1/captures/{id}/archive gives it. A capture sent again is taken once.
//	@Tags			agent
//	@Accept			application/zip
//	@Produce		json
//	@Param			Authorization	header		string	true	"Bearer agent key"
//	@Success		200				{object}	map[string]string
//	@Failure		400				{object}	apiError
//	@Failure		409				{object}	apiError
//	@Failure		413				{object}	apiError
//	@Failure		507				{object}	apiError
//	@Router			/api/agent/v1/captures [post]
func (s *Server) agentCapture(c *gin.Context) {
	a := currentAgent(c)
	if s.captures == nil {
		fail(c, http.StatusConflict, errcode.CapturesOff(), "captures are not made: HLSMON_CAPTURE is not set")
		return
	}
	if !a.Enabled {
		fail(c, http.StatusForbidden, errcode.Forbidden(), "the agent is disabled")
		return
	}
	f, err := os.CreateTemp("", "hls-monitor-agent-capture-*.zip")
	if err != nil {
		fail(c, http.StatusInternalServerError, errcode.Internal(), "cannot take the capture now")
		return
	}
	defer os.Remove(f.Name())
	defer f.Close()
	n, err := io.Copy(f, io.LimitReader(c.Request.Body, agentCaptureMax+1))
	switch {
	case err != nil:
		fail(c, http.StatusBadRequest, errcode.BadRequest(), "the archive did not come whole")
		return
	case n > agentCaptureMax:
		fail(c, http.StatusRequestEntityTooLarge, errcode.TooLarge(), "the archive is larger than a capture may be")
		return
	}
	// An agent sends captures of the streams it checks, and of no other:
	// a capture is evidence, and nobody else's word is taken for it.
	id, err := s.captures.Import(a.ID, f, n, func(stream id.Stream) bool {
		ok, err := s.st.StreamAssigned(c.Request.Context(), stream, a.ID)
		return err == nil && ok
	})
	switch {
	case errors.Is(err, capture.ErrNotTheirs):
		fail(c, http.StatusForbidden, errcode.Forbidden(), "the agent does not check the stream of the capture")
	case errors.Is(err, capture.ErrNoRoom):
		fail(c, http.StatusInsufficientStorage, errcode.NoRoom(), "there is no room for the capture")
	case err != nil:
		fail(c, http.StatusBadRequest, errcode.BadRequest(), err.Error())
	default:
		s.log.Info("a capture was sent by an agent", "agent", a.ID, "capture", id)
		c.JSON(http.StatusOK, gin.H{"id": id})
	}
}

// agentCaptureMax bounds an archive an agent sends: a capture and its
// descriptions.
const agentCaptureMax = 104 << 20
