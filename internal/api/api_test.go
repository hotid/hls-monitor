package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hotid/hls-monitor/internal/agentproto"
	"github.com/hotid/hls-monitor/internal/auth"
	"github.com/hotid/hls-monitor/internal/hlstest"
	"github.com/hotid/hls-monitor/internal/ingest"
	"github.com/hotid/hls-monitor/internal/metrics"
	"github.com/hotid/hls-monitor/internal/probe"
	"github.com/hotid/hls-monitor/internal/runner"
	"github.com/hotid/hls-monitor/internal/store"
)

type testEnv struct {
	t      *testing.T
	st     *store.Store
	srv    *Server
	http   *httptest.Server
	origin *httptest.Server
	fake   *hlstest.Origin
}

// client is an HTTP client with its own cookie jar, i.e. one browser.
type client struct {
	t    *testing.T
	base string
	hc   *http.Client
	// headers sent with every request.
	headers map[string]string
}

func newEnv(t *testing.T) *testEnv {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	for _, u := range []struct{ name, role string }{{"admin", store.RoleAdmin}, {"viewer", store.RoleViewer}} {
		hash, _ := auth.HashPassword(u.name + "-password")
		if _, err := st.CreateUser(ctx, u.name, hash, u.role, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	mx := metrics.New("", nil)
	ing := ingest.New(st, mx, nil)
	local := runner.New(func(ctx context.Context, r *probe.Result) {
		_ = ing.Apply(context.WithoutCancel(ctx), store.LocalAgentID, r)
	}, "test", nil)
	t.Cleanup(local.Stop)

	e := &testEnv{t: t, st: st, fake: hlstest.New(hlstest.Options{})}
	e.fake.Publish(4)
	e.origin = httptest.NewServer(e.fake.Handler())
	t.Cleanup(e.origin.Close)
	e.srv = New(Config{Version: "test"}, st, mx, ing, local, nil, slog.New(slog.DiscardHandler))
	e.http = httptest.NewServer(e.srv.Handler())
	t.Cleanup(e.http.Close)
	return e
}

func (e *testEnv) client() *client {
	jar, _ := cookiejar.New(nil)
	return &client{t: e.t, base: e.http.URL, hc: &http.Client{Jar: jar}, headers: map[string]string{"X-Requested-With": "test"}}
}

func (e *testEnv) login(name string) *client {
	e.t.Helper()
	c := e.client()
	c.want(http.StatusOK, "POST", "/api/v1/auth/login", map[string]string{"username": name, "password": name + "-password"}, nil)
	return c
}

func (c *client) do(method, path string, body any) (int, []byte) {
	c.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, c.base+path, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range c.headers {
		req.Header.Set(k, v)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// want performs a request, checks the status and decodes the body into out.
func (c *client) want(status int, method, path string, body, out any) {
	c.t.Helper()
	got, b := c.do(method, path, body)
	if got != status {
		c.t.Fatalf("%s %s = %d, want %d; body: %s", method, path, got, status, b)
	}
	if out != nil {
		if err := json.Unmarshal(b, out); err != nil {
			c.t.Fatalf("%s %s: cannot decode %s: %v", method, path, b, err)
		}
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestAuthentication(t *testing.T) {
	e := newEnv(t)
	anon := e.client()

	anon.want(http.StatusOK, "GET", "/healthz", nil, nil)
	for _, p := range []string{"/api/v1/auth/me", "/api/v1/streams", "/api/v1/overview", "/api/v1/users", "/swagger/index.html"} {
		anon.want(http.StatusUnauthorized, "GET", p, nil, nil)
	}
	anon.want(http.StatusUnauthorized, "POST", "/api/v1/auth/login", map[string]string{"username": "admin", "password": "wrong"}, nil)
	anon.want(http.StatusUnauthorized, "POST", "/api/v1/auth/login", map[string]string{"username": "nobody", "password": "admin-password"}, nil)
	anon.want(http.StatusNotFound, "GET", "/api/v1/nope", nil, nil)

	admin := e.login("admin")
	var me store.User
	admin.want(http.StatusOK, "GET", "/api/v1/auth/me", nil, &me)
	if me.Username != "admin" || me.Role != store.RoleAdmin {
		t.Fatalf("me = %+v", me)
	}
	_, raw := admin.do("GET", "/api/v1/auth/me", nil)
	if strings.Contains(string(raw), "argon2") || strings.Contains(string(raw), "password") {
		t.Errorf("the password hash leaks into the API: %s", raw)
	}
	admin.want(http.StatusOK, "GET", "/swagger/doc.json", nil, nil)

	// A state-changing request without the anti-CSRF header is refused even
	// with a valid session.
	delete(admin.headers, "X-Requested-With")
	admin.want(http.StatusForbidden, "POST", "/api/v1/auth/logout", nil, nil)
	admin.want(http.StatusOK, "GET", "/api/v1/auth/me", nil, nil)
	admin.headers["X-Requested-With"] = "test"

	admin.want(http.StatusNoContent, "POST", "/api/v1/auth/logout", nil, nil)
	admin.want(http.StatusUnauthorized, "GET", "/api/v1/auth/me", nil, nil)
}

func TestLoginThrottling(t *testing.T) {
	e := newEnv(t)
	c := e.client()
	bad := map[string]string{"username": "admin", "password": "guess"}
	for range loginMaxFailures {
		c.want(http.StatusUnauthorized, "POST", "/api/v1/auth/login", bad, nil)
	}
	c.want(http.StatusTooManyRequests, "POST", "/api/v1/auth/login", bad, nil)
	// Even the right password is refused while the lock holds.
	c.want(http.StatusTooManyRequests, "POST", "/api/v1/auth/login", map[string]string{"username": "admin", "password": "admin-password"}, nil)
}

func TestPasswordChangeEndsSessions(t *testing.T) {
	e := newEnv(t)
	a, b := e.login("viewer"), e.login("viewer")
	a.want(http.StatusBadRequest, "PUT", "/api/v1/auth/password", map[string]string{"current": "wrong", "new": "new-password-1"}, nil)
	a.want(http.StatusBadRequest, "PUT", "/api/v1/auth/password", map[string]string{"current": "viewer-password", "new": "short"}, nil)
	a.want(http.StatusNoContent, "PUT", "/api/v1/auth/password", map[string]string{"current": "viewer-password", "new": "new-password-1"}, nil)
	a.want(http.StatusUnauthorized, "GET", "/api/v1/auth/me", nil, nil)
	b.want(http.StatusUnauthorized, "GET", "/api/v1/auth/me", nil, nil)
	e.client().want(http.StatusOK, "POST", "/api/v1/auth/login", map[string]string{"username": "viewer", "password": "new-password-1"}, nil)
}

func TestViewerIsReadOnly(t *testing.T) {
	e := newEnv(t)
	admin, viewer := e.login("admin"), e.login("viewer")
	var created streamView
	admin.want(http.StatusCreated, "POST", "/api/v1/streams", map[string]any{
		"name": "secret", "url": e.origin.URL + "/v0/index.m3u8", "headers": map[string]string{"authorization": "Bearer abc"}, "agent_ids": []int64{},
	}, &created)
	if created.Headers["Authorization"] != "Bearer abc" {
		t.Fatalf("header names must be canonicalised: %+v", created.Headers)
	}

	id := "/api/v1/streams/1"
	for _, rq := range []struct{ method, path string }{
		{"POST", "/api/v1/streams"}, {"PUT", id}, {"DELETE", id}, {"POST", id + "/disable"}, {"POST", id + "/check"},
		{"POST", "/api/v1/agents"}, {"DELETE", "/api/v1/agents/1"}, {"GET", "/api/v1/users"}, {"POST", "/api/v1/users"},
		{"GET", "/api/v1/tokens"}, {"POST", "/api/v1/tokens"},
	} {
		viewer.want(http.StatusForbidden, rq.method, rq.path, map[string]any{"name": "x"}, nil)
	}

	var seen streamDetail
	viewer.want(http.StatusOK, "GET", id, nil, &seen)
	if seen.Headers["Authorization"] != "***" {
		t.Errorf("viewers must not see request header values: %+v", seen.Headers)
	}
	var list []streamView
	viewer.want(http.StatusOK, "GET", "/api/v1/streams", nil, &list)
	if len(list) != 1 || list[0].Headers["Authorization"] != "***" {
		t.Errorf("list for a viewer = %+v", list)
	}
}

func TestStreamValidation(t *testing.T) {
	e := newEnv(t)
	admin := e.login("admin")
	ok := map[string]any{"name": "a", "url": "https://example.com/a.m3u8"}
	for name, patch := range map[string]map[string]any{
		"empty name":       {"name": " "},
		"relative url":     {"url": "/a.m3u8"},
		"file url":         {"url": "file:///etc/passwd"},
		"bad level":        {"level": "deep"},
		"short interval":   {"interval_sec": 0.2},
		"long timeout":     {"timeout_sec": 500},
		"header newline":   {"headers": map[string]string{"X-A": "a\r\nX-B: b"}},
		"header name":      {"headers": map[string]string{"X A": "a"}},
		"unknown agent":    {"agent_ids": []int64{42}},
		"too many at once": {"max_segments": 100},
	} {
		body := map[string]any{}
		for k, v := range ok {
			body[k] = v
		}
		for k, v := range patch {
			body[k] = v
		}
		if status, b := admin.do("POST", "/api/v1/streams", body); status != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400; body %s", name, status, b)
		}
	}
	var list []streamView
	admin.want(http.StatusOK, "GET", "/api/v1/streams", nil, &list)
	if len(list) != 0 {
		t.Fatalf("rejected requests left %d streams behind", len(list))
	}
	admin.want(http.StatusCreated, "POST", "/api/v1/streams", ok, nil)
	admin.want(http.StatusConflict, "POST", "/api/v1/streams", ok, nil)
	admin.want(http.StatusCreated, "POST", "/api/v1/streams", map[string]any{"name": "b", "url": "https://example.com/b.m3u8", "level": "header"}, nil)
}

func TestStreamLifecycle(t *testing.T) {
	e := newEnv(t)
	admin := e.login("admin")

	var st streamView
	admin.want(http.StatusCreated, "POST", "/api/v1/streams", map[string]any{
		"name": "live", "url": e.origin.URL + "/master.m3u8", "level": "container", "interval_sec": 3600,
	}, &st)
	if st.Status != statusNoData || len(st.AgentIDs) != 1 || st.AgentIDs[0] != store.LocalAgentID {
		t.Fatalf("new stream = %+v", st)
	}

	// The server starts checking it on its own.
	var detail streamDetail
	waitFor(t, "first local check", func() bool {
		admin.want(http.StatusOK, "GET", "/api/v1/streams/1", nil, &detail)
		return detail.Status == statusOK
	})
	if len(detail.Variants) != 3 || detail.Variants[0].Kind != probe.KindMaster || detail.Variants[1].Resolution != "160x90" {
		t.Fatalf("variants = %+v", detail.Variants)
	}
	if len(detail.Variants[1].States) != 1 || detail.Variants[1].States[0].Metrics["hls_media_width"] != 160 {
		t.Errorf("variant state = %+v", detail.Variants[1].States)
	}
	if len(detail.Agents) != 1 || detail.Agents[0].Summary["variants"] != 2 {
		t.Errorf("agent summary = %+v", detail.Agents)
	}

	var segs []store.SegmentRecord
	admin.want(http.StatusOK, "GET", "/api/v1/streams/1/segments", nil, &segs)
	if len(segs) != 2 || segs[0].Status != 200 || !strings.Contains(string(segs[0].Detail), `"container":"ts"`) {
		t.Fatalf("segment journal = %+v", segs)
	}

	// Which servers answered: for this stream and across all of them.
	var sources []store.SourceSeen
	admin.want(http.StatusOK, "GET", "/api/v1/streams/1/sources", nil, &sources)
	if len(sources) < 3 || sources[0].Host != strings.TrimPrefix(e.origin.URL, "http://") || sources[0].Hits < 1 {
		t.Fatalf("stream sources = %+v", sources)
	}
	var servers []serverView
	admin.want(http.StatusOK, "GET", "/api/v1/servers", nil, &servers)
	if len(servers) != 1 || servers[0].Streams != 1 || servers[0].OK != 1 || !servers[0].Playlists || !servers[0].Segments {
		t.Fatalf("servers = %+v", servers)
	}

	// Break the stream and ask for an immediate check.
	e.fake.SetDown(http.StatusBadGateway)
	admin.want(http.StatusAccepted, "POST", "/api/v1/streams/1/check", nil, nil)
	var ov overviewResponse
	waitFor(t, "failure to show up", func() bool {
		admin.want(http.StatusOK, "GET", "/api/v1/overview", nil, &ov)
		return ov.Counts[statusFail] == 1
	})
	admin.want(http.StatusOK, "GET", "/api/v1/servers", nil, &servers)
	if len(servers) != 1 || servers[0].Fail != 1 || len(servers[0].FailingStreams) != 1 || servers[0].FailingStreams[0].Name != "live" || len(servers[0].StreamIDs) != 1 {
		t.Fatalf("servers with a failing stream = %+v", servers)
	}
	if len(ov.Incidents) != 1 || ov.Incidents[0].Kind != probe.ProbPlaylistHTTP || ov.Incidents[0].StreamName != "live" {
		t.Fatalf("open incidents = %+v", ov.Incidents)
	}
	if ov.Streams[0].OpenIncidents["crit"] != 1 || ov.Counts["total"] != 1 {
		t.Errorf("overview = %+v / %+v", ov.Counts, ov.Streams[0].OpenIncidents)
	}
	var incidents []store.Incident
	admin.want(http.StatusOK, "GET", "/api/v1/incidents?stream=1&open=true", nil, &incidents)
	if len(incidents) != 1 {
		t.Errorf("incident filter returned %d", len(incidents))
	}
	admin.want(http.StatusOK, "GET", "/api/v1/incidents?severity=crit,warn", nil, &incidents)
	if len(incidents) != 1 {
		t.Errorf("crit and warn incidents = %d, want 1", len(incidents))
	}
	admin.want(http.StatusOK, "GET", "/api/v1/incidents?severity=info", nil, &incidents)
	if len(incidents) != 0 {
		t.Errorf("info incidents = %+v, want none", incidents)
	}
	admin.want(http.StatusBadRequest, "GET", "/api/v1/incidents?severity=loud", nil, nil)

	// Disabling stops checks and closes incidents.
	admin.want(http.StatusOK, "POST", "/api/v1/streams/1/disable", nil, &st)
	if st.Status != statusDisabled {
		t.Errorf("status after disable = %s", st.Status)
	}
	admin.want(http.StatusOK, "GET", "/api/v1/incidents?open=true", nil, &incidents)
	if len(incidents) != 0 {
		t.Errorf("incidents still open after disable: %+v", incidents)
	}
	admin.want(http.StatusConflict, "POST", "/api/v1/streams/1/check", nil, nil)

	admin.want(http.StatusNoContent, "DELETE", "/api/v1/streams/1", nil, nil)
	admin.want(http.StatusNotFound, "GET", "/api/v1/streams/1", nil, nil)
	admin.want(http.StatusNotFound, "DELETE", "/api/v1/streams/1", nil, nil)
}

// A diagnosis checks the stream on its own and reports every cycle as a
// line; what it finds is not stored.
func TestDiagnose(t *testing.T) {
	e := newEnv(t)
	admin := e.login("admin")
	var st streamView
	admin.want(http.StatusCreated, "POST", "/api/v1/streams", map[string]any{
		"name": "live", "url": e.origin.URL + "/master.m3u8", "level": "playlist", "interval_sec": 3600, "agent_ids": []int64{},
	}, &st)
	path := fmt.Sprintf("/api/v1/streams/%d/diagnose", st.ID)
	viewer := e.login("viewer")
	viewer.want(http.StatusBadRequest, "GET", path+"?level=everything", nil, nil)

	status, body := viewer.do("GET", path+"?level=header&seconds=2", nil)
	if status != http.StatusOK {
		t.Fatalf("diagnose = %d: %s", status, body)
	}
	var events []diagnoseEvent
	for _, line := range bytes.Split(bytes.TrimSpace(body), []byte("\n")) {
		var ev diagnoseEvent
		if err := json.Unmarshal(line, &ev); err != nil {
			t.Fatalf("line %q: %v", line, err)
		}
		events = append(events, ev)
	}
	if len(events) < 3 || events[0].Type != "start" || events[0].Level != "header" || events[len(events)-1].Type != "end" {
		t.Fatalf("events = %+v", events)
	}
	first := events[1]
	if first.Type != "cycle" || first.N != 1 || first.Result == nil || first.Result.Status != probe.StatusOK || len(first.Result.Segments) == 0 {
		t.Fatalf("first cycle = %+v, result %+v", first, first.Result)
	}
	// Nothing of it reached the stream's stored state.
	var detail streamDetail
	admin.want(http.StatusOK, "GET", fmt.Sprintf("/api/v1/streams/%d", st.ID), nil, &detail)
	if len(detail.Variants) != 0 {
		t.Fatalf("a diagnosis stored variants: %+v", detail.Variants)
	}
}

func TestUsersAdministration(t *testing.T) {
	e := newEnv(t)
	admin := e.login("admin")

	var u store.User
	admin.want(http.StatusBadRequest, "POST", "/api/v1/users", map[string]any{"username": "bob", "password": "short"}, nil)
	admin.want(http.StatusBadRequest, "POST", "/api/v1/users", map[string]any{"username": "bob", "password": "long-enough", "role": "root"}, nil)
	admin.want(http.StatusCreated, "POST", "/api/v1/users", map[string]any{"username": "bob", "password": "long-enough"}, &u)
	if u.Role != store.RoleViewer {
		t.Errorf("default role = %s, want viewer", u.Role)
	}
	admin.want(http.StatusConflict, "POST", "/api/v1/users", map[string]any{"username": "BOB", "password": "long-enough"}, nil)

	bob := e.client()
	bob.want(http.StatusOK, "POST", "/api/v1/auth/login", map[string]string{"username": "bob", "password": "long-enough"}, nil)
	admin.want(http.StatusOK, "PUT", "/api/v1/users/3", map[string]any{"disabled": true}, nil)
	bob.want(http.StatusUnauthorized, "GET", "/api/v1/auth/me", nil, nil)
	e.client().want(http.StatusUnauthorized, "POST", "/api/v1/auth/login", map[string]string{"username": "bob", "password": "long-enough"}, nil)

	// The only administrator cannot remove their own rights.
	admin.want(http.StatusConflict, "PUT", "/api/v1/users/1", map[string]any{"role": "viewer"}, nil)
	admin.want(http.StatusConflict, "DELETE", "/api/v1/users/1", nil, nil)
	admin.want(http.StatusNoContent, "DELETE", "/api/v1/users/3", nil, nil)
}

func TestExternalAPI(t *testing.T) {
	e := newEnv(t)
	admin := e.login("admin")
	admin.want(http.StatusCreated, "POST", "/api/v1/streams", map[string]any{"name": "live", "url": e.origin.URL + "/master.m3u8", "interval_sec": 3600}, nil)

	var tok createdToken
	admin.want(http.StatusCreated, "POST", "/api/v1/tokens", map[string]string{"name": "zabbix"}, &tok)
	if len(tok.Token) < 40 {
		t.Fatalf("token = %+v", tok)
	}
	var listed []json.RawMessage
	admin.want(http.StatusOK, "GET", "/api/v1/tokens", nil, &listed)
	if len(listed) != 1 || strings.Contains(string(listed[0]), tok.Token) {
		t.Errorf("the token value must not be listed: %s", listed)
	}

	ext := e.client()
	ext.want(http.StatusUnauthorized, "GET", "/api/ext/v1/status", nil, nil)
	ext.headers["Authorization"] = "Bearer wrong"
	ext.want(http.StatusUnauthorized, "GET", "/api/ext/v1/status", nil, nil)
	// A session cookie is not a way into the external API, nor a token into the web API.
	admin.want(http.StatusUnauthorized, "GET", "/api/ext/v1/status", nil, nil)
	ext.headers["Authorization"] = "Bearer " + tok.Token
	ext.want(http.StatusUnauthorized, "GET", "/api/v1/streams", nil, nil)

	var streams []extStream
	ext.want(http.StatusOK, "GET", "/api/ext/v1/discovery/streams", nil, &streams)
	if len(streams) != 1 || streams[0].Name != "live" {
		t.Fatalf("discovery = %+v", streams)
	}
	var doc extStreamStatusDoc
	waitFor(t, "status to become ok", func() bool {
		ext.want(http.StatusOK, "GET", "/api/ext/v1/streams/1/status", nil, &doc)
		return doc.StatusCode == 0
	})
	if len(doc.Variants) != 3 || len(doc.Agents) != 1 || doc.Agents[0].AgeSeconds == nil {
		t.Errorf("status document = %+v", doc)
	}
	var variants []extVariant
	ext.want(http.StatusOK, "GET", "/api/ext/v1/discovery/variants", nil, &variants)
	if len(variants) != 3 || variants[0].StreamName != "live" {
		t.Errorf("variant discovery = %+v", variants)
	}
	var agents []extAgent
	ext.want(http.StatusOK, "GET", "/api/ext/v1/discovery/agents", nil, &agents)
	if len(agents) != 1 || !agents[0].Online {
		t.Errorf("agent discovery = %+v", agents)
	}

	admin.want(http.StatusNoContent, "DELETE", "/api/v1/tokens/1", nil, nil)
	ext.want(http.StatusUnauthorized, "GET", "/api/ext/v1/status", nil, nil)
}

func TestAgentProtocol(t *testing.T) {
	e := newEnv(t)
	admin := e.login("admin")

	var en enrollment
	admin.want(http.StatusBadRequest, "POST", "/api/v1/agents", map[string]string{"name": "bad name!"}, nil)
	admin.want(http.StatusCreated, "POST", "/api/v1/agents", map[string]string{"name": "edge-1"}, &en)
	admin.want(http.StatusConflict, "POST", "/api/v1/agents", map[string]string{"name": "edge-1"}, nil)
	if en.Agent.Enrolled || !strings.Contains(en.Command, en.Token) || !strings.Contains(en.Command, e.http.URL) {
		t.Fatalf("enrollment = %+v", en)
	}
	admin.want(http.StatusCreated, "POST", "/api/v1/streams", map[string]any{"name": "mine", "url": "http://x/a.m3u8", "agent_ids": []int64{en.Agent.ID}}, nil)
	admin.want(http.StatusCreated, "POST", "/api/v1/streams", map[string]any{"name": "not-mine", "url": "http://x/b.m3u8", "agent_ids": []int64{}}, nil)

	agent := e.client()
	agent.want(http.StatusUnauthorized, "GET", "/api/agent/v1/config", nil, nil)
	agent.want(http.StatusUnauthorized, "POST", "/api/agent/v1/enroll", map[string]string{"token": "wrong"}, nil)
	var enrolled agentproto.EnrollResponse
	agent.want(http.StatusOK, "POST", "/api/agent/v1/enroll", map[string]string{"token": en.Token, "version": "1.0"}, &enrolled)
	agent.want(http.StatusUnauthorized, "POST", "/api/agent/v1/enroll", map[string]string{"token": en.Token}, nil) // one-shot
	// The enrollment token is not a key.
	agent.headers["Authorization"] = "Bearer " + en.Token
	agent.want(http.StatusUnauthorized, "GET", "/api/agent/v1/config", nil, nil)
	agent.headers["Authorization"] = "Bearer " + enrolled.Key

	var cfg agentproto.Config
	agent.want(http.StatusOK, "GET", "/api/agent/v1/config", nil, &cfg)
	if len(cfg.Targets) != 1 || cfg.Targets[0].ID != 1 || !cfg.Enabled || cfg.Revision == "" {
		t.Fatalf("config = %+v", cfg)
	}
	agent.want(http.StatusNotModified, "GET", "/api/agent/v1/config?revision="+cfg.Revision, nil, nil)
	admin.want(http.StatusOK, "POST", "/api/v1/streams/1/disable", nil, nil)
	var cfg2 agentproto.Config
	agent.want(http.StatusOK, "GET", "/api/agent/v1/config?revision="+cfg.Revision, nil, &cfg2)
	if len(cfg2.Targets) != 0 || cfg2.Revision == cfg.Revision {
		t.Fatalf("config after disabling the stream = %+v", cfg2)
	}
	admin.want(http.StatusOK, "POST", "/api/v1/streams/1/enable", nil, nil)

	result := func(stream int64) *probe.Result {
		return &probe.Result{TargetID: stream, Time: time.Now(), Status: probe.StatusOK,
			Variants: []probe.Variant{{Key: probe.VariantMedia, Kind: probe.KindMedia}},
			Metrics:  []probe.Metric{{Name: "hls_up", Variant: probe.VariantMedia, Value: 1}}}
	}
	var rr agentproto.ResultsResponse
	agent.want(http.StatusOK, "POST", "/api/agent/v1/results", agentproto.ResultsRequest{Results: []*probe.Result{result(1), result(2), result(77), nil}}, &rr)
	if rr.Accepted != 1 || rr.Rejected != 3 || rr.Failed != 0 {
		t.Fatalf("results response = %+v, want only the assigned stream accepted", rr)
	}

	var list []streamView
	admin.want(http.StatusOK, "GET", "/api/v1/streams", nil, &list)
	if list[0].Name != "mine" || list[0].Status != statusOK || list[1].Status != statusNoData {
		t.Fatalf("statuses = %s / %s", list[0].Status, list[1].Status)
	}

	// The agent falls silent: its last result no longer counts.
	e.srv.now = func() time.Time { return time.Now().Add(5 * time.Minute) }
	admin.want(http.StatusOK, "GET", "/api/v1/streams", nil, &list)
	if list[0].Status != statusNoData || list[0].Agents[0].Status != statusNoData {
		t.Errorf("status with a silent agent = %s, want nodata", list[0].Status)
	}
	var agents []agentView
	admin.want(http.StatusOK, "GET", "/api/v1/agents", nil, &agents)
	if len(agents) != 2 || !agents[0].Online || agents[1].Online || agents[1].Streams != 1 {
		t.Errorf("agents = %+v", agents)
	}
	e.srv.now = time.Now

	// A disabled agent is told so and its results are refused.
	admin.want(http.StatusOK, "PUT", "/api/v1/agents/2", map[string]any{"enabled": false}, nil)
	agent.want(http.StatusOK, "GET", "/api/agent/v1/config", nil, &cfg)
	if cfg.Enabled || len(cfg.Targets) != 0 {
		t.Errorf("config of a disabled agent = %+v", cfg)
	}
	agent.want(http.StatusForbidden, "POST", "/api/agent/v1/results", agentproto.ResultsRequest{Results: []*probe.Result{result(1)}}, nil)

	// Resetting the token revokes the key; deleting the agent removes it.
	admin.want(http.StatusOK, "POST", "/api/v1/agents/2/token", nil, &en)
	agent.want(http.StatusUnauthorized, "GET", "/api/agent/v1/config", nil, nil)
	admin.want(http.StatusConflict, "DELETE", "/api/v1/agents/1", nil, nil)
	admin.want(http.StatusConflict, "POST", "/api/v1/agents/1/token", nil, nil)
	admin.want(http.StatusNoContent, "DELETE", "/api/v1/agents/2", nil, nil)
	var st streamDetail
	admin.want(http.StatusOK, "GET", "/api/v1/streams/1", nil, &st)
	if len(st.AgentIDs) != 0 {
		t.Errorf("a deleted agent is still assigned: %+v", st.AgentIDs)
	}
}

func TestWebInterfaceFallback(t *testing.T) {
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	mx := metrics.New("", nil)
	ui := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("<html>ui " + r.URL.Path + "</html>"))
	})
	srv := httptest.NewServer(New(Config{}, st, mx, ingest.New(st, mx, nil), nil, ui, slog.New(slog.DiscardHandler)).Handler())
	defer srv.Close()

	for path, want := range map[string]int{"/": 200, "/streams/1": 200, "/api/v1/nope": 404, "/api/": 404} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != want {
			t.Errorf("GET %s = %d, want %d", path, resp.StatusCode, want)
		}
		if isUI := strings.Contains(string(body), "<html>ui"); isUI != (want == 200) {
			t.Errorf("GET %s: interface served = %v", path, isUI)
		}
	}
}
