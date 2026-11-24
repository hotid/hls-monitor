package api

import (
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/hotid/hls-monitor/internal/store"
)

// streamPictures godoc
//
//	@Summary	The latest looks at a stream's picture and sound
//	@Description	One per vantage point: when the stream was looked at, how bright the picture was and how loud the sound. The picture itself is at /api/v1/streams/{id}/picture. Empty when the picture is not checked (see HLSMON_PICTURE_CHECK).
//	@Tags		streams
//	@Security	SessionCookie
//	@Produce	json
//	@Param		id	path		int	true	"Stream ID"
//	@Success	200	{array}		store.StreamPicture
//	@Router		/api/v1/streams/{id}/pictures [get]
func (s *Server) streamPictures(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	rows, err := s.st.StreamPictures(c.Request.Context(), id)
	if s.storeError(c, err) {
		return
	}
	for i := range rows {
		rows[i].OwnClip = s.ing.OwnClip(id, rows[i].AgentID)
	}
	c.JSON(http.StatusOK, rows)
}

// streamPicture godoc
//
//	@Summary	The picture of the latest look at a stream
//	@Tags		streams
//	@Security	SessionCookie
//	@Produce	jpeg
//	@Param		id		path	int	true	"Stream ID"
//	@Param		agent	query	int	false	"Vantage point; the server's own by default"
//	@Success	200
//	@Failure	404	{object}	apiError
//	@Router		/api/v1/streams/{id}/picture [get]
func (s *Server) streamPicture(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	agent := queryInt(c, "agent")
	if agent <= 0 {
		agent = store.LocalAgentID
	}
	b, at, err := s.st.PictureJPEG(c.Request.Context(), id, agent)
	if s.storeError(c, err) {
		return
	}
	jpegAnswer(c, b, at)
}

// incidentPicture godoc
//
//	@Summary	The picture an incident was last seen on
//	@Description	Kept for incidents found by looking at the picture: a black screen, a picture that stopped.
//	@Tags		incidents
//	@Security	SessionCookie
//	@Produce	jpeg
//	@Param		id	path	int	true	"Incident ID"
//	@Success	200
//	@Failure	404	{object}	apiError
//	@Router		/api/v1/incidents/{id}/picture [get]
func (s *Server) incidentPicture(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	b, at, err := s.st.IncidentPictureJPEG(c.Request.Context(), id)
	if s.storeError(c, err) {
		return
	}
	jpegAnswer(c, b, at)
}

func jpegAnswer(c *gin.Context, b []byte, at time.Time) {
	c.Header("Cache-Control", "private, no-cache")
	c.Header("Last-Modified", at.UTC().Format(http.TimeFormat))
	c.Header("X-Picture-Time", strconv.FormatInt(at.UnixMilli(), 10))
	c.Data(http.StatusOK, "image/jpeg", b)
}
