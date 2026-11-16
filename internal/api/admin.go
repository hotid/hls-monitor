package api

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/hotid/hls-monitor/internal/auth"
	"github.com/hotid/hls-monitor/internal/store"
)

var nameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,62}$`)

// listAgents godoc
//
//	@Summary	List agents
//	@Tags		agents
//	@Produce	json
//	@Success	200	{array}	agentView
//	@Router		/api/v1/agents [get]
func (s *Server) listAgents(c *gin.Context) {
	ctx := c.Request.Context()
	agents, err := s.st.ListAgents(ctx)
	if s.storeError(c, err) {
		return
	}
	streams, err := s.st.ListStreams(ctx)
	if s.storeError(c, err) {
		return
	}
	byID := map[int64]*store.Agent{}
	for _, a := range agents {
		byID[a.ID] = a
	}
	c.JSON(http.StatusOK, s.agentViews(byID, streams, s.now()))
}

type agentRequest struct {
	Name    string `json:"name"`
	Enabled *bool  `json:"enabled"`
}

// enrollment is returned once, when an agent is created or its token is reset.
type enrollment struct {
	Agent     *store.Agent `json:"agent"`
	Token     string       `json:"token"`
	ExpiresAt time.Time    `json:"expires_at"`
	ServerURL string       `json:"server_url"`
	// Command is a ready-to-run example for starting the agent in Docker.
	Command string `json:"command"`
}

func (s *Server) enrollment(c *gin.Context, a *store.Agent, token string, expires time.Time) enrollment {
	u := s.publicURL(c)
	return enrollment{
		Agent: a, Token: token, ExpiresAt: expires.UTC(), ServerURL: u,
		Command: fmt.Sprintf("docker run -d --name hls-monitor-agent --restart unless-stopped \\\n  -e HLSMON_SERVER_URL=%s \\\n  -e HLSMON_ENROLL_TOKEN=%s \\\n  -v hls-monitor-agent:/var/lib/hls-monitor \\\n  %s", u, token, s.agentImage()),
	}
}

var releaseVersion = regexp.MustCompile(`^v?(\d+\.\d+\.\d+)$`)

// agentImage is the image an agent of this server should run: the one
// configured, or the published image of the same release. A build that is
// not a release has no image of its own and is pointed at the latest one.
func (s *Server) agentImage() string {
	if s.cfg.AgentImage != "" {
		return s.cfg.AgentImage
	}
	tag := "latest"
	if m := releaseVersion.FindStringSubmatch(s.cfg.Version); m != nil {
		tag = m[1]
	}
	return "ghcr.io/hotid/hls-monitor-agent:" + tag
}

// createAgent godoc
//
//	@Summary	Register a remote agent
//	@Description	Returns a one-time enrollment token. It is shown only in this response.
//	@Tags		agents
//	@Accept		json
//	@Produce	json
//	@Param		request	body		agentRequest	true	"Agent name"
//	@Success	201		{object}	enrollment
//	@Failure	400		{object}	apiError
//	@Failure	409		{object}	apiError
//	@Router		/api/v1/agents [post]
func (s *Server) createAgent(c *gin.Context) {
	var req agentRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		badRequest(c, "invalid request body")
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if !nameRe.MatchString(req.Name) {
		badRequest(c, "name must be 1 to 63 characters: letters, digits, dot, dash, underscore")
		return
	}
	token, hash, err := auth.NewToken()
	if s.storeError(c, err) {
		return
	}
	now := s.now()
	expires := now.Add(s.cfg.EnrollTTL)
	a, err := s.st.CreateAgent(c.Request.Context(), req.Name, hash, expires, now)
	if s.storeError(c, err) {
		return
	}
	c.JSON(http.StatusCreated, s.enrollment(c, a, token, expires))
}

// updateAgent godoc
//
//	@Summary	Rename an agent or switch it on or off
//	@Tags		agents
//	@Accept		json
//	@Produce	json
//	@Param		id		path		int				true	"Agent ID"
//	@Param		request	body		agentRequest	true	"Agent settings"
//	@Success	200		{object}	store.Agent
//	@Failure	400		{object}	apiError
//	@Failure	404		{object}	apiError
//	@Failure	409		{object}	apiError
//	@Router		/api/v1/agents/{id} [put]
func (s *Server) updateAgent(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	var req agentRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		badRequest(c, "invalid request body")
		return
	}
	ctx := c.Request.Context()
	cur, err := s.st.GetAgent(ctx, id)
	if s.storeError(c, err) {
		return
	}
	name, enabled := strings.TrimSpace(req.Name), cur.Enabled
	if name == "" {
		name = cur.Name
	}
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	if !nameRe.MatchString(name) {
		badRequest(c, "name must be 1 to 63 characters: letters, digits, dot, dash, underscore")
		return
	}
	a, err := s.st.UpdateAgent(ctx, id, name, enabled)
	if s.storeError(c, err) {
		return
	}
	if !enabled {
		_ = s.st.CloseAgentIncidents(ctx, id, s.now())
	}
	s.configChanged(ctx)
	c.JSON(http.StatusOK, a)
}

// deleteAgent godoc
//
//	@Summary	Delete a remote agent with everything it reported
//	@Tags		agents
//	@Param		id	path	int	true	"Agent ID"
//	@Success	204
//	@Failure	404	{object}	apiError
//	@Failure	409	{object}	apiError
//	@Router		/api/v1/agents/{id} [delete]
func (s *Server) deleteAgent(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	if s.storeError(c, s.st.DeleteAgent(c.Request.Context(), id)) {
		return
	}
	c.Status(http.StatusNoContent)
}

// resetAgentToken godoc
//
//	@Summary	Issue a new enrollment token
//	@Description	Invalidates the agent's current key: the running agent stops being accepted until it enrolls again.
//	@Tags		agents
//	@Produce	json
//	@Param		id	path		int	true	"Agent ID"
//	@Success	200	{object}	enrollment
//	@Failure	404	{object}	apiError
//	@Failure	409	{object}	apiError
//	@Router		/api/v1/agents/{id}/token [post]
func (s *Server) resetAgentToken(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	token, hash, err := auth.NewToken()
	if s.storeError(c, err) {
		return
	}
	ctx := c.Request.Context()
	expires := s.now().Add(s.cfg.EnrollTTL)
	if s.storeError(c, s.st.ResetAgentEnrollment(ctx, id, hash, expires)) {
		return
	}
	a, err := s.st.GetAgent(ctx, id)
	if s.storeError(c, err) {
		return
	}
	c.JSON(http.StatusOK, s.enrollment(c, a, token, expires))
}

// listUsers godoc
//
//	@Summary	List users
//	@Tags		users
//	@Produce	json
//	@Success	200	{array}	store.User
//	@Router		/api/v1/users [get]
func (s *Server) listUsers(c *gin.Context) {
	users, err := s.st.ListUsers(c.Request.Context())
	if s.storeError(c, err) {
		return
	}
	c.JSON(http.StatusOK, users)
}

type userRequest struct {
	Username string  `json:"username"`
	Password *string `json:"password"`
	Role     *string `json:"role" enums:"admin,viewer"`
	Disabled *bool   `json:"disabled"`
}

func validRole(r string) bool { return r == store.RoleAdmin || r == store.RoleViewer }

// createUser godoc
//
//	@Summary	Add a user
//	@Tags		users
//	@Accept		json
//	@Produce	json
//	@Param		request	body		userRequest	true	"User"
//	@Success	201		{object}	store.User
//	@Failure	400		{object}	apiError
//	@Failure	409		{object}	apiError
//	@Router		/api/v1/users [post]
func (s *Server) createUser(c *gin.Context) {
	var req userRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		badRequest(c, "invalid request body")
		return
	}
	req.Username = strings.TrimSpace(req.Username)
	role := store.RoleViewer
	if req.Role != nil {
		role = *req.Role
	}
	switch {
	case !nameRe.MatchString(req.Username):
		badRequest(c, "username must be 1 to 63 characters: letters, digits, dot, dash, underscore")
		return
	case !validRole(role):
		badRequest(c, "role must be admin or viewer")
		return
	case req.Password == nil:
		badRequest(c, "password is required")
		return
	}
	hash, ok := s.newPasswordHash(c, *req.Password)
	if !ok {
		return
	}
	u, err := s.st.CreateUser(c.Request.Context(), req.Username, hash, role, s.now())
	if s.storeError(c, err) {
		return
	}
	c.JSON(http.StatusCreated, u)
}

// updateUser godoc
//
//	@Summary	Change a user's role, password or access
//	@Description	Only the fields present in the body change. A new password or disabling ends the user's sessions.
//	@Tags		users
//	@Accept		json
//	@Produce	json
//	@Param		id		path		int			true	"User ID"
//	@Param		request	body		userRequest	true	"Fields to change"
//	@Success	200		{object}	store.User
//	@Failure	400		{object}	apiError
//	@Failure	404		{object}	apiError
//	@Failure	409		{object}	apiError
//	@Router		/api/v1/users/{id} [put]
func (s *Server) updateUser(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	var req userRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		badRequest(c, "invalid request body")
		return
	}
	up := store.UserUpdate{Role: req.Role, Disabled: req.Disabled}
	if req.Role != nil && !validRole(*req.Role) {
		badRequest(c, "role must be admin or viewer")
		return
	}
	if req.Password != nil {
		hash, ok := s.newPasswordHash(c, *req.Password)
		if !ok {
			return
		}
		up.PasswordHash = &hash
	}
	u, err := s.st.UpdateUser(c.Request.Context(), id, up)
	if s.storeError(c, err) {
		return
	}
	c.JSON(http.StatusOK, u)
}

// deleteUser godoc
//
//	@Summary	Delete a user
//	@Tags		users
//	@Param		id	path	int	true	"User ID"
//	@Success	204
//	@Failure	404	{object}	apiError
//	@Failure	409	{object}	apiError
//	@Router		/api/v1/users/{id} [delete]
func (s *Server) deleteUser(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	if s.storeError(c, s.st.DeleteUser(c.Request.Context(), id)) {
		return
	}
	c.Status(http.StatusNoContent)
}

// listTokens godoc
//
//	@Summary	List API tokens for external monitoring
//	@Tags		tokens
//	@Produce	json
//	@Success	200	{array}	store.APIToken
//	@Router		/api/v1/tokens [get]
func (s *Server) listTokens(c *gin.Context) {
	tokens, err := s.st.ListAPITokens(c.Request.Context())
	if s.storeError(c, err) {
		return
	}
	c.JSON(http.StatusOK, tokens)
}

type tokenRequest struct {
	Name string `json:"name"`
}

// createdToken carries the token value, which is shown only once.
type createdToken struct {
	*store.APIToken
	Token string `json:"token"`
}

// createToken godoc
//
//	@Summary	Create a read-only API token
//	@Description	The token value is shown only in this response.
//	@Tags		tokens
//	@Accept		json
//	@Produce	json
//	@Param		request	body		tokenRequest	true	"Token name"
//	@Success	201		{object}	createdToken
//	@Failure	400		{object}	apiError
//	@Failure	409		{object}	apiError
//	@Router		/api/v1/tokens [post]
func (s *Server) createToken(c *gin.Context) {
	var req tokenRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		badRequest(c, "invalid request body")
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if !nameRe.MatchString(req.Name) {
		badRequest(c, "name must be 1 to 63 characters: letters, digits, dot, dash, underscore")
		return
	}
	token, hash, err := auth.NewToken()
	if s.storeError(c, err) {
		return
	}
	t, err := s.st.CreateAPIToken(c.Request.Context(), req.Name, hash, s.now())
	if s.storeError(c, err) {
		return
	}
	c.JSON(http.StatusCreated, createdToken{APIToken: t, Token: token})
}

// deleteToken godoc
//
//	@Summary	Revoke an API token
//	@Tags		tokens
//	@Param		id	path	int	true	"Token ID"
//	@Success	204
//	@Failure	404	{object}	apiError
//	@Router		/api/v1/tokens/{id} [delete]
func (s *Server) deleteToken(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	if s.storeError(c, s.st.DeleteAPIToken(c.Request.Context(), id)) {
		return
	}
	c.Status(http.StatusNoContent)
}
