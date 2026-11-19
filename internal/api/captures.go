package api

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/hotid/hls-monitor/internal/capture"
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
//	@Security	BearerToken
//	@Produce	json
//	@Param		stream	query		int	false	"Only the captures of this stream"
//	@Success	200		{object}	capturesResponse
//	@Router		/api/v1/captures [get]
func (s *Server) listCaptures(c *gin.Context) {
	out := capturesResponse{Captures: []capture.Capture{}}
	if s.captures != nil {
		stream, _ := strconv.ParseInt(c.Query("stream"), 10, 64)
		out.Enabled = true
		out.Captures = s.captures.List(stream)
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
//	@Security	BearerToken
//	@Produce	application/zip
//	@Param		id	path	string	true	"Capture ID"
//	@Success	200
//	@Failure	404	{object}	apiError
//	@Router		/api/v1/captures/{id}/archive [get]
func (s *Server) captureArchive(c *gin.Context) {
	id := c.Param("id")
	if s.captures == nil || !s.hasCapture(id) {
		fail(c, http.StatusNotFound, "not_found", "not found")
		return
	}
	c.Header("Content-Type", "application/zip")
	c.Header("Content-Disposition", `attachment; filename="capture-`+id+`.zip"`)
	if err := s.captures.WriteZip(c.Writer, id); err != nil {
		s.log.Warn("a capture could not be sent", "capture", id, "error", err)
	}
}

func (s *Server) hasCapture(id string) bool {
	for _, cp := range s.captures.List(0) {
		if cp.ID == id && cp.Done {
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
//	@Security	BearerToken
//	@Param		id	path	string	true	"Capture ID"
//	@Success	204
//	@Failure	404	{object}	apiError
//	@Router		/api/v1/captures/{id} [delete]
func (s *Server) deleteCapture(c *gin.Context) {
	if s.captures == nil || !s.captures.Delete(c.Param("id")) {
		fail(c, http.StatusNotFound, "not_found", "not found")
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
//	@Security	BearerToken
//	@Param		id	path	int	true	"Stream ID"
//	@Success	202
//	@Failure	404	{object}	apiError
//	@Failure	409	{object}	apiError
//	@Router		/api/v1/streams/{id}/capture [post]
func (s *Server) captureNow(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	switch {
	case s.captures == nil:
		fail(c, http.StatusConflict, "captures_off", "captures are not made: HLSMON_CAPTURE is not set")
	case s.local == nil || !s.local.Capture(id):
		fail(c, http.StatusConflict, "not_local", "the server does not check this stream itself: only its own checks capture")
	default:
		s.log.Info("a capture was asked for", "stream", id, "by", currentUser(c).Username)
		c.Status(http.StatusAccepted)
	}
}
