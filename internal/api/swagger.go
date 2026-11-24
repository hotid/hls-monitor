package api

import (
	swaggerFiles "github.com/swaggo/files"
	ginSwagger "github.com/swaggo/gin-swagger"

	"github.com/hotid/hls-monitor/docs" // generated API specification
)

//	@title			hls-monitor API
//	@version		1.0
//	@description	HLS stream monitoring. /api/v1 is the web API (session cookie; every non-GET request needs an X-Requested-With header), /api/agent/v1 is for remote agents, /api/ext/v1 is the read-only API for external monitoring systems (Bearer token).

//	@securityDefinitions.apikey	SessionCookie
//	@in							header
//	@name						Cookie
//	@description				The session of the web interface: the hlsmon_session cookie that signing in sets. The browser sends it by itself.

//	@securityDefinitions.apikey	BearerToken
//	@in							header
//	@name						Authorization
//	@description				"Bearer <token>": an API token from the Settings page for /api/ext/v1 and /metrics, the key of an agent for /api/agent/v1.

// swaggerRoutes serves the API documentation to signed-in users.
func (s *Server) swaggerRoutes() {
	// Requests made from the documentation page must go through the same prefix.
	docs.SwaggerInfo.BasePath = s.cfg.BasePath + "/"
	s.router.GET("/swagger/*any", s.requireSession, ginSwagger.WrapHandler(swaggerFiles.Handler))
}
