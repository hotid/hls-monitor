// Package api is the HTTP interface of the server: the web API used by the
// interface, the agent API and the read-only API for external monitoring.
package api

import (
	"context"
	"crypto/rand"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/hotid/hls-monitor/internal/capture"
	"github.com/hotid/hls-monitor/internal/httpx"
	"github.com/hotid/hls-monitor/internal/ingest"
	"github.com/hotid/hls-monitor/internal/metrics"
	"github.com/hotid/hls-monitor/internal/probe"
	"github.com/hotid/hls-monitor/internal/runner"
	"github.com/hotid/hls-monitor/internal/selfstat"
	"github.com/hotid/hls-monitor/internal/store"
	"github.com/hotid/hls-monitor/internal/types/cookiesecure"
	"github.com/hotid/hls-monitor/internal/types/errcode"
	"github.com/hotid/hls-monitor/internal/types/id"
)

// Config holds the settings of the HTTP layer.
type Config struct {
	Version string
	// Captures is where the segments around findings are saved; nil when
	// none are.
	Captures   *capture.Store
	SessionTTL time.Duration
	// CookieSecure is when the session cookie is marked Secure.
	CookieSecure cookiesecure.Mode
	// PublicURL is how agents reach the server; shown in enrollment hints.
	PublicURL string
	// AgentImage is the image named in the command for starting an agent;
	// empty means the published image of the server's own version.
	AgentImage string
	// BasePath is the URL prefix under which a reverse proxy publishes the
	// server, e.g. "/hls-monitor"; empty for the root. The proxy strips it,
	// so routes do not include it; it scopes the session cookie and is part
	// of the address given to agents.
	BasePath string
	// AgentOfflineAfter is the silence after which a remote agent counts as offline.
	AgentOfflineAfter time.Duration
	// EnrollTTL is how long an enrollment token stays valid.
	EnrollTTL      time.Duration
	TrustedProxies []string
	// PlayKey signs the addresses handed to players of watched streams.
	// A key that survives restarts keeps open players working across
	// them; when empty, one is made for the life of the process.
	PlayKey []byte
	// Load watches the server process itself; may be nil.
	Load *selfstat.Monitor
}

// Server wires storage and checking into HTTP handlers.
type Server struct {
	cfg   Config
	st    *store.Store
	mx    *metrics.Client
	ing   *ingest.Ingester
	local *runner.Runner
	log   *slog.Logger
	// slates are the pictures known to be slates, as the checks need them.
	slates atomic.Pointer[[]probe.Slate]
	// captures is nil when no captures are made.
	captures *capture.Store
	now      func() time.Time

	logins *loginLimiter
	// diagnoses holds one slot per live diagnosis allowed at a time.
	diagnoses chan struct{}
	router    *gin.Engine

	// For watching streams through the server: the key that signs the
	// addresses it hands out, and the clients that fetch what they name.
	relays        relays
	fetchPoll     time.Duration
	fetchHandOver time.Duration
	playKey       []byte
	playSecure    *httpx.Client
	playInsecure  *httpx.Client

	// reports holds what each remote agent last told about itself.
	reportsMu sync.Mutex
	reports   map[id.Agent]agentReport
}

// New builds the server. ui serves the web interface for every path that
// is not part of an API; it may be nil.
func New(cfg Config, st *store.Store, mx *metrics.Client, ing *ingest.Ingester, local *runner.Runner, ui http.Handler, log *slog.Logger) *Server {
	if log == nil {
		log = slog.Default()
	}
	if cfg.CookieSecure.IsZero() {
		cfg.CookieSecure = cookiesecure.Auto()
	}
	if cfg.SessionTTL <= 0 {
		cfg.SessionTTL = 7 * 24 * time.Hour
	}
	if cfg.AgentOfflineAfter <= 0 {
		cfg.AgentOfflineAfter = 90 * time.Second
	}
	if cfg.EnrollTTL <= 0 {
		cfg.EnrollTTL = 24 * time.Hour
	}
	s := &Server{cfg: cfg, st: st, mx: mx, ing: ing, local: local, captures: cfg.Captures, log: log, now: time.Now, logins: newLoginLimiter(), diagnoses: make(chan struct{}, maxDiagnoses), reports: map[id.Agent]agentReport{}}

	s.fetchPoll, s.fetchHandOver = fetchPoll, fetchHandOver
	s.playKey = cfg.PlayKey
	if len(s.playKey) < 16 {
		s.playKey = make([]byte, 32)
		_, _ = rand.Read(s.playKey)
	}
	s.playSecure = httpx.New(httpx.Options{UserAgent: "hls-monitor/" + cfg.Version})
	s.playInsecure = httpx.New(httpx.Options{UserAgent: "hls-monitor/" + cfg.Version, InsecureSkipVerify: true})

	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Recovery(), s.accessLog())
	_ = r.SetTrustedProxies(cfg.TrustedProxies)
	r.HandleMethodNotAllowed = true
	s.router = r
	s.routes(ui)
	return s
}

// Handler returns the HTTP handler of the server.
func (s *Server) Handler() http.Handler { return s.router }

func (s *Server) routes(ui http.Handler) {
	r := s.router
	r.GET("/healthz", s.healthz)
	r.GET("/readyz", s.readyz)
	r.GET("/metrics", noStore, s.requireAPIToken, s.selfMetrics)

	v1 := r.Group("/api/v1", noStore, s.requireCSRFHeader)
	v1.POST("/auth/login", s.login)
	v1.POST("/auth/logout", s.logout)

	user := v1.Group("", s.requireSession)
	user.GET("/auth/me", s.me)
	user.PUT("/auth/password", s.changePassword)
	user.GET("/overview", s.overview)
	user.GET("/ping", s.ping)
	user.GET("/system", s.system)
	user.GET("/timeline", s.timeline)
	user.GET("/aggregate", s.aggregate)
	user.GET("/servers", s.servers)
	user.GET("/servers/network", s.serversNetwork)
	user.GET("/streams/:id/sources", s.streamSources)
	user.GET("/streams/:id/pictures", s.streamPictures)
	user.GET("/streams/:id/picture", s.streamPicture)
	user.GET("/streams", s.listStreams)
	user.GET("/streams/:id", s.getStream)
	user.GET("/streams/:id/diagnose", s.diagnose)
	user.GET("/streams/:id/watch", s.watchSources)
	user.GET("/play/:token", s.play)
	user.GET("/streams/:id/series", s.series)
	user.GET("/streams/:id/segments", s.listSegments)
	user.GET("/streams/:id/snapshots", s.listSnapshots)
	user.GET("/snapshots/:id", s.getSnapshot)
	user.GET("/incidents", s.listIncidents)
	user.GET("/incidents/:id/scope", s.incidentScope)
	user.GET("/incidents/:id/picture", s.incidentPicture)
	user.GET("/slates", s.listSlates)
	user.GET("/captures", s.listCaptures)
	user.GET("/slates/frames/:id", s.slateFrame)
	user.GET("/server-incidents", s.listServerIncidents)
	user.GET("/agents", s.listAgents)
	user.GET("/agents/stats", s.agentsStats)

	admin := user.Group("", s.requireAdmin)
	admin.POST("/streams", s.createStream)
	admin.POST("/streams/import", s.importStreams)
	admin.POST("/slates", s.markSlate)
	admin.GET("/captures/:id/archive", s.captureArchive)
	admin.DELETE("/captures/:id", s.deleteCapture)
	admin.POST("/streams/:id/capture", s.captureNow)
	admin.DELETE("/slates/:id", s.deleteSlate)
	admin.DELETE("/slates/frames/:id", s.deleteSlateFrame)
	admin.PUT("/streams/:id", s.updateStream)
	admin.DELETE("/streams/:id", s.deleteStream)
	admin.POST("/streams/:id/enable", s.enableStream)
	admin.POST("/streams/:id/disable", s.disableStream)
	admin.POST("/streams/:id/check", s.checkStream)
	admin.POST("/agents", s.createAgent)
	admin.PUT("/agents/:id", s.updateAgent)
	admin.DELETE("/agents/:id", s.deleteAgent)
	admin.POST("/agents/:id/token", s.resetAgentToken)
	admin.GET("/users", s.listUsers)
	admin.POST("/users", s.createUser)
	admin.PUT("/users/:id", s.updateUser)
	admin.DELETE("/users/:id", s.deleteUser)
	admin.GET("/tokens", s.listTokens)
	admin.POST("/tokens", s.createToken)
	admin.DELETE("/tokens/:id", s.deleteToken)

	ag := r.Group("/api/agent/v1", noStore)
	ag.POST("/enroll", s.agentEnroll)
	agAuth := ag.Group("", s.requireAgent)
	agAuth.GET("/config", s.agentConfig)
	agAuth.POST("/results", s.agentResults)
	agAuth.GET("/fetch", s.agentFetchPoll)
	agAuth.POST("/fetch/:id", s.agentFetchResult)
	agAuth.POST("/captures", s.agentCapture)

	ext := r.Group("/api/ext/v1", noStore, s.requireAPIToken)
	ext.GET("/discovery/streams", s.extDiscoverStreams)
	ext.GET("/discovery/variants", s.extDiscoverVariants)
	ext.GET("/discovery/agents", s.extDiscoverAgents)
	ext.GET("/status", s.extStatus)
	ext.GET("/streams/:id/status", s.extStreamStatus)

	s.swaggerRoutes()

	r.NoRoute(func(c *gin.Context) {
		p := c.Request.URL.Path
		if strings.HasPrefix(p, "/api/") || ui == nil {
			fail(c, http.StatusNotFound, errcode.NotFound(), "no such endpoint")
			return
		}
		// NoRoute presets 404; the interface handler decides the status itself.
		c.Status(http.StatusOK)
		ui.ServeHTTP(c.Writer, c.Request)
	})
	r.NoMethod(func(c *gin.Context) {
		fail(c, http.StatusMethodNotAllowed, errcode.MethodNotAllowed(), "method not allowed")
	})
}

// apiError is the body of every error response.
type apiError struct {
	Error string       `json:"error"`
	Code  errcode.Code `json:"code" swaggertype:"string"`
}

func fail(c *gin.Context, status int, code errcode.Code, msg string) {
	c.AbortWithStatusJSON(status, apiError{Error: msg, Code: code})
}

func badRequest(c *gin.Context, msg string) {
	fail(c, http.StatusBadRequest, errcode.BadRequest(), msg)
}

// storeError maps storage errors to responses; it reports whether err was non-nil.
func (s *Server) storeError(c *gin.Context, err error) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, store.ErrNotFound):
		fail(c, http.StatusNotFound, errcode.NotFound(), "not found")
	case errors.Is(err, store.ErrConflict):
		fail(c, http.StatusConflict, errcode.Conflict(), "this name is already taken")
	case errors.Is(err, store.ErrLastAdmin), errors.Is(err, store.ErrLocalAgent):
		fail(c, http.StatusConflict, errcode.Conflict(), err.Error())
	case errors.Is(err, context.Canceled):
		c.Abort()
	default:
		s.log.Error("request failed", "path", c.FullPath(), "error", err)
		fail(c, http.StatusInternalServerError, errcode.Internal(), "internal error")
	}
	return true
}

// identifier is an identifier of one of the kinds of package id, by a
// pointer to which it can be read.
type identifier[T any] interface {
	*T
	Parse(string) error
	IsZero() bool
}

// pathID reads the identifier an address names, and answers the request
// when it names none.
func pathID[T any, P identifier[T]](c *gin.Context) (T, bool) {
	var v T
	if err := P(&v).Parse(c.Param("id")); err != nil || P(&v).IsZero() || c.Param("id")[0] == '-' {
		fail(c, http.StatusNotFound, errcode.NotFound(), "not found")
		var none T
		return none, false
	}
	return v, true
}

// queryID reads an identifier given in the query; one that is not given,
// or is no number, is that of nothing.
func queryID[T any, P identifier[T]](c *gin.Context, name string) T {
	var v, none T
	if P(&v).Parse(c.Query(name)) != nil {
		return none
	}
	return v
}

// queryChoice reads a parameter that is yes or no: true or 1, false or
// 0. told is false when it is absent or is neither.
func queryChoice(c *gin.Context, name string) (yes, told bool) {
	switch c.Query(name) {
	case "true", "1":
		return true, true
	case "false", "0":
		return false, true
	}
	return false, false
}

// queryFlag reads a parameter that is yes when it says so, and no
// otherwise.
func queryFlag(c *gin.Context, name string) bool {
	yes, _ := queryChoice(c, name)
	return yes
}

func queryInt(c *gin.Context, name string) int64 {
	v, _ := strconv.ParseInt(c.Query(name), 10, 64)
	return v
}

// queryTime reads a Unix time in seconds (fractions allowed) or milliseconds.
func queryTime(c *gin.Context, name string) time.Time {
	v, err := strconv.ParseFloat(c.Query(name), 64)
	if err != nil || v <= 0 {
		return time.Time{}
	}
	if v > 1e11 { // milliseconds
		return time.UnixMilli(int64(v))
	}
	return time.UnixMilli(int64(v * 1000))
}

func noStore(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	c.Next()
}

func (s *Server) accessLog() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		p := c.Request.URL.Path
		if !strings.HasPrefix(p, "/api/") {
			return
		}
		lvl := slog.LevelDebug
		if c.Writer.Status() >= 500 {
			lvl = slog.LevelError
		}
		s.log.Log(c.Request.Context(), lvl, "http",
			"method", c.Request.Method, "path", p, "status", c.Writer.Status(),
			"ms", time.Since(start).Milliseconds(), "ip", c.ClientIP())
	}
}

// healthz godoc
//
//	@Summary	Liveness check
//	@Tags		system
//	@Produce	json
//	@Success	200	{object}	map[string]string
//	@Router		/healthz [get]
func (s *Server) healthz(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// SyncLocal makes the local runner check exactly the streams assigned to
// the server itself.
func (s *Server) SyncLocal(ctx context.Context) {
	s.LoadSlates(ctx)
	if s.local == nil {
		return
	}
	targets, err := s.st.TargetsForAgent(ctx, store.LocalAgentID())
	if err != nil {
		s.log.Error("cannot load local targets", "error", err)
		return
	}
	s.local.Sync(targets)
}

// configChanged is called after anything that affects who checks what.
func (s *Server) configChanged(ctx context.Context) {
	s.SyncLocal(ctx)
}
