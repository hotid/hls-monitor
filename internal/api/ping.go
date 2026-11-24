package api

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// ping godoc
//
//	@Summary	An empty answer, to time the way to the server by
//	@Description	The player's page asks this while a stream is watched: how long the answer takes is the round trip time between the browser and the server, whatever stands in between.
//	@Tags		watch
//	@Security	SessionCookie
//	@Success	204
//	@Router		/api/v1/ping [get]
func (s *Server) ping(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	c.Status(http.StatusNoContent)
}
