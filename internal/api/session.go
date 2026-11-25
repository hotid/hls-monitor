package api

import (
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/hotid/hls-monitor/internal/auth"
	"github.com/hotid/hls-monitor/internal/store"
	"github.com/hotid/hls-monitor/internal/types/cookiesecure"
	"github.com/hotid/hls-monitor/internal/types/errcode"
	"github.com/hotid/hls-monitor/internal/types/role"
)

const (
	sessionCookie = "hlsmon_session"
	ctxUser       = "user"
	ctxAgent      = "agent"
)

// requireCSRFHeader rejects state-changing requests that lack a header a
// cross-site form or image cannot set. Together with the SameSite cookie
// this keeps other sites from acting with the user's session.
func (s *Server) requireCSRFHeader(c *gin.Context) {
	switch c.Request.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
	default:
		if c.GetHeader("X-Requested-With") == "" {
			fail(c, http.StatusForbidden, errcode.CSRF(), "the X-Requested-With header is required")
			return
		}
	}
	c.Next()
}

func (s *Server) requireSession(c *gin.Context) {
	token, err := c.Cookie(sessionCookie)
	if err != nil || token == "" {
		fail(c, http.StatusUnauthorized, errcode.Unauthorized(), "sign in required")
		return
	}
	u, err := s.st.SessionUser(c.Request.Context(), auth.HashToken(token), s.now())
	// A database that did not answer is not a session that is not there:
	// the cookie stays, and nobody is sent to sign in again for it.
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		s.storeError(c, err)
		return
	}
	if err != nil {
		s.clearCookie(c)
		fail(c, http.StatusUnauthorized, errcode.Unauthorized(), "sign in required")
		return
	}
	c.Set(ctxUser, u)
	c.Next()
}

func (s *Server) requireAdmin(c *gin.Context) {
	if currentUser(c).Role != role.Admin() {
		fail(c, http.StatusForbidden, errcode.Forbidden(), "administrator rights required")
		return
	}
	c.Next()
}

func currentUser(c *gin.Context) *store.User {
	u, _ := c.MustGet(ctxUser).(*store.User)
	return u
}

func bearer(c *gin.Context) string {
	h := c.GetHeader("Authorization")
	if len(h) > 7 && strings.EqualFold(h[:7], "Bearer ") {
		return strings.TrimSpace(h[7:])
	}
	return ""
}

func (s *Server) cookieSecure(c *gin.Context) bool {
	switch s.cfg.CookieSecure {
	case cookiesecure.Always():
		return true
	case cookiesecure.Never():
		return false
	}
	return c.Request.TLS != nil || strings.EqualFold(c.GetHeader("X-Forwarded-Proto"), "https")
}

func (s *Server) setCookie(c *gin.Context, token string, maxAge int) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     sessionCookie,
		Value:    token,
		Path:     s.cfg.BasePath + "/",
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   s.cookieSecure(c),
		SameSite: http.SameSiteLaxMode,
	})
}

func (s *Server) clearCookie(c *gin.Context) { s.setCookie(c, "", -1) }

// loginLimiter slows down password guessing: after too many failures for
// one address or one username, attempts are refused for a while.
type loginLimiter struct {
	mu       sync.Mutex
	failures map[string][]time.Time
}

const (
	loginWindow      = 5 * time.Minute
	loginMaxFailures = 10
	// loginMaxKeys is how many addresses and names are remembered at once.
	loginMaxKeys = 10000
)

func newLoginLimiter() *loginLimiter { return &loginLimiter{failures: map[string][]time.Time{}} }

func (l *loginLimiter) blocked(now time.Time, keys ...string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, k := range keys {
		recent := l.failures[k][:0]
		for _, t := range l.failures[k] {
			if now.Sub(t) < loginWindow {
				recent = append(recent, t)
			}
		}
		if len(recent) == 0 {
			delete(l.failures, k)
		} else {
			l.failures[k] = recent
		}
		if len(recent) >= loginMaxFailures {
			return true
		}
	}
	return false
}

func (l *loginLimiter) fail(now time.Time, keys ...string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	// Memory is bounded under a flood of distinct keys, and not by
	// forgetting everybody: that would let whoever is held back start
	// over by sending rubbish. What is past its time goes, then those
	// with the fewest failures.
	if len(l.failures) > loginMaxKeys {
		for most := 1; len(l.failures) > loginMaxKeys/2 && most < loginMaxFailures; most++ {
			for k, at := range l.failures {
				if len(at) <= most || now.Sub(at[len(at)-1]) >= loginWindow {
					delete(l.failures, k)
				}
			}
		}
	}
	for _, k := range keys {
		l.failures[k] = append(l.failures[k], now)
	}
}

func (l *loginLimiter) reset(keys ...string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, k := range keys {
		delete(l.failures, k)
	}
}

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// login godoc
//
//	@Summary	Sign in
//	@Description	Sets the session cookie. Every non-GET request to /api/v1 must carry an X-Requested-With header.
//	@Tags		auth
//	@Accept		json
//	@Produce	json
//	@Param		request	body		loginRequest	true	"Credentials"
//	@Success	200		{object}	store.User
//	@Failure	401		{object}	apiError
//	@Failure	429		{object}	apiError
//	@Router		/api/v1/auth/login [post]
func (s *Server) login(c *gin.Context) {
	var req loginRequest
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, anonymousBody)
	if err := c.ShouldBindJSON(&req); err != nil || req.Username == "" || req.Password == "" {
		badRequest(c, "username and password are required")
		return
	}
	now := s.now()
	ipKey, userKey := "ip:"+c.ClientIP(), "user:"+strings.ToLower(req.Username)
	if s.logins.blocked(now, ipKey, userKey) {
		fail(c, http.StatusTooManyRequests, errcode.TooManyAttempts(), "too many failed attempts, try again in a few minutes")
		return
	}

	ctx := c.Request.Context()
	u, err := s.st.GetUserByName(ctx, req.Username)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		s.storeError(c, err)
		return
	}
	ok := false
	if err == nil {
		ok = auth.VerifyPassword(u.PasswordHash, req.Password) && !u.Disabled
	} else {
		auth.DummyVerify(req.Password)
	}
	if !ok {
		s.logins.fail(now, ipKey, userKey)
		s.log.Warn("failed sign-in", "username", req.Username, "ip", c.ClientIP())
		fail(c, http.StatusUnauthorized, errcode.BadCredentials(), "wrong username or password")
		return
	}
	s.logins.reset(userKey)

	token, hash, err := auth.NewToken()
	if err == nil {
		err = s.st.CreateSession(ctx, hash, u.ID, now, now.Add(s.cfg.SessionTTL))
	}
	if s.storeError(c, err) {
		return
	}
	s.setCookie(c, token, int(s.cfg.SessionTTL.Seconds()))
	c.JSON(http.StatusOK, u)
}

// logout godoc
//
//	@Summary	Sign out
//	@Tags		auth
//	@Success	204
//	@Router		/api/v1/auth/logout [post]
func (s *Server) logout(c *gin.Context) {
	if token, err := c.Cookie(sessionCookie); err == nil && token != "" {
		// A session that could not be ended is not said to be ended: the
		// cookie, kept by somebody else, would go on working.
		if err := s.st.DeleteSession(c.Request.Context(), auth.HashToken(token)); err != nil && !errors.Is(err, store.ErrNotFound) {
			s.storeError(c, err)
			return
		}
	}
	s.clearCookie(c)
	c.Status(http.StatusNoContent)
}

// me godoc
//
//	@Summary	Current user
//	@Tags		auth
//	@Produce	json
//	@Success	200	{object}	store.User
//	@Failure	401	{object}	apiError
//	@Router		/api/v1/auth/me [get]
func (s *Server) me(c *gin.Context) {
	c.JSON(http.StatusOK, currentUser(c))
}

type passwordRequest struct {
	Current string `json:"current"`
	New     string `json:"new"`
}

// changePassword godoc
//
//	@Summary	Change own password
//	@Description	Ends all sessions of the user, including the current one.
//	@Tags		auth
//	@Accept		json
//	@Param		request	body	passwordRequest	true	"Passwords"
//	@Success	204
//	@Failure	400	{object}	apiError
//	@Router		/api/v1/auth/password [put]
func (s *Server) changePassword(c *gin.Context) {
	var req passwordRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		badRequest(c, "invalid request body")
		return
	}
	u := currentUser(c)
	if !auth.VerifyPassword(u.PasswordHash, req.Current) {
		fail(c, http.StatusBadRequest, errcode.BadCredentials(), "the current password is wrong")
		return
	}
	hash, ok := s.newPasswordHash(c, req.New)
	if !ok {
		return
	}
	if _, err := s.st.UpdateUser(c.Request.Context(), u.ID, store.UserUpdate{PasswordHash: &hash}); s.storeError(c, err) {
		return
	}
	s.clearCookie(c)
	c.Status(http.StatusNoContent)
}

func (s *Server) newPasswordHash(c *gin.Context, password string) (string, bool) {
	if len(password) < auth.MinPasswordLen {
		badRequest(c, "the password must be at least 8 characters long")
		return "", false
	}
	if len(password) > 256 {
		badRequest(c, "the password is too long")
		return "", false
	}
	hash, err := auth.HashPassword(password)
	if s.storeError(c, err) {
		return "", false
	}
	return hash, true
}

// anonymousBody bounds what is read of a request from somebody not known
// yet: a login or an enrollment.
const anonymousBody = 64 << 10
