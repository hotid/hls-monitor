package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
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

// The scope of an incident: who else sees it and which other streams of
// the same server have it.
func TestIncidentScope(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	now := time.Now().Truncate(time.Second)
	edge, err := e.st.CreateAgent(ctx, "edge", []byte("x"), now.Add(time.Hour), now)
	if err != nil {
		t.Fatal(err)
	}
	ing := ingest.New(e.st, metrics.New("", nil), nil)
	variants := []probe.Variant{{Key: probe.VariantMedia, Kind: probe.KindVideo}}
	streams := map[string]*store.Stream{}
	report := func(name string, agentID int64, server string) {
		t.Helper()
		st := streams[name]
		if st == nil {
			var err error
			st, err = e.st.CreateStream(ctx, store.StreamInput{Name: name, URL: "http://x/" + name + ".m3u8", Enabled: true, Level: "playlist",
				AgentIDs: []int64{store.LocalAgentID, edge.ID}}, now)
			if err != nil {
				t.Fatal(err)
			}
			streams[name] = st
		}
		r := &probe.Result{TargetID: st.ID, Time: now, Status: probe.StatusOK, Variants: variants}
		if server != "" {
			r.Status = probe.StatusFail
			r.Problems = []probe.Problem{{Kind: probe.ProbPlaylistStale, Severity: probe.SevCrit, Variant: probe.VariantMedia, Message: "stale", Count: 1,
				Details: map[string]any{"playlist_server": server}}}
		}
		if err := ing.Apply(ctx, agentID, r); err != nil {
			t.Fatal(err)
		}
	}
	report("a", store.LocalAgentID, "bad-server 10.0.0.6")
	report("a", edge.ID, "") // the other vantage point sees nothing wrong
	report("b", store.LocalAgentID, "bad-server 10.0.0.6")
	report("c", store.LocalAgentID, "bad-server 10.0.0.6")
	report("d", store.LocalAgentID, "good-server 10.0.0.1")

	c := e.login("viewer")
	var list []store.Incident
	c.want(200, "GET", "/api/v1/incidents?limit=50", nil, &list)
	var id int64
	for _, i := range list {
		if i.StreamName == "a" {
			id = i.ID
		}
	}
	var sc incidentScope
	c.want(200, "GET", fmt.Sprintf("/api/v1/incidents/%d/scope", id), nil, &sc)
	if sc.Vantage != "only_this" || len(sc.Agents) != 2 {
		t.Errorf("vantage = %q with agents %+v; want only_this of two", sc.Vantage, sc.Agents)
	}
	if !sc.ServerWide || len(sc.SameServer) != 2 || sc.SameServer[0].Name != "b" || sc.Elsewhere != 1 || sc.Server != "bad-server 10.0.0.6" {
		t.Errorf("server scope = %+v; want streams b and c on the same server, one elsewhere", sc)
	}
	c.want(404, "GET", "/api/v1/incidents/999999/scope", nil, nil)
}

func TestSelfDiagnostics(t *testing.T) {
	e := newEnv(t)
	admin := e.login("admin")
	anon := e.client()

	var ready readiness
	anon.want(http.StatusOK, "GET", "/readyz", nil, &ready)
	if ready.Status != "ok" || ready.Checks["database"] != "ok" || ready.Checks["time_series"] != "disabled" || ready.Checks["load"] != "ok" {
		t.Errorf("readiness = %+v", ready)
	}

	// The server's own metrics need an API token, as the rest of what
	// monitoring systems read.
	anon.want(http.StatusUnauthorized, "GET", "/metrics", nil, nil)
	admin.want(http.StatusUnauthorized, "GET", "/metrics", nil, nil)
	var tok createdToken
	admin.want(http.StatusCreated, "POST", "/api/v1/tokens", map[string]string{"name": "prometheus"}, &tok)
	var en enrollment
	admin.want(http.StatusCreated, "POST", "/api/v1/agents", map[string]string{"name": "edge-1"}, &en)
	var enrolled agentproto.EnrollResponse
	anon.want(http.StatusOK, "POST", "/api/agent/v1/enroll", map[string]string{"token": en.Token, "version": "1.0"}, &enrolled)

	// An agent tells about itself with every request.
	agent := e.client()
	agent.headers["Authorization"] = "Bearer " + enrolled.Key
	agent.headers[agentproto.HeaderStatus] = `{"queue_results":7,"queue_bytes":4096,"queue_oldest_sec":12.5,"queue_dropped":3,"overloaded":"processor","streams":2,"behind":1,"link":{"rtt_ms":42.5,"retransmits":9,"request_ms":120,"requests":300,"failures":2,"measured":true}}`
	agent.want(http.StatusOK, "GET", "/api/agent/v1/config", nil, nil)

	var agents []agentView
	admin.want(http.StatusOK, "GET", "/api/v1/agents", nil, &agents)
	if len(agents) != 2 || agents[0].Status == nil || agents[1].Status == nil {
		t.Fatalf("agents = %+v", agents)
	}
	if st := agents[1].Status; st.QueueResults != 7 || st.QueueDropped != 3 || st.Overloaded != "processor" || st.Behind != 1 {
		t.Errorf("status of the remote agent = %+v", st)
	}

	// An agent that was never connected is not watched yet.
	admin.want(http.StatusCreated, "POST", "/api/v1/agents", map[string]string{"name": "not-yet"}, nil)

	scrape := e.client()
	scrape.headers["Authorization"] = "Bearer " + tok.Token
	code, body := scrape.do("GET", "/metrics", nil)
	if code != http.StatusOK {
		t.Fatalf("GET /metrics = %d: %s", code, body)
	}
	for _, want := range []string{
		`hlsmon_build_info{version="test"} 1`,
		"hlsmon_overloaded 0",
		"# TYPE hlsmon_results_total counter",
		`hlsmon_results_total{outcome="failed"} 0`,
		"hlsmon_local_streams 0",
		`hlsmon_agent_online{agent="edge-1"} 1`,
		`hlsmon_agent_queue_results{agent="edge-1"} 7`,
		`hlsmon_agent_queue_oldest_seconds{agent="edge-1"} 12.5`,
		`hlsmon_agent_queue_dropped_total{agent="edge-1"} 3`,
		`hlsmon_agent_overloaded{agent="edge-1"} 1`,
		`hlsmon_agent_link_rtt_seconds{agent="edge-1"} 0.0425`,
		`hlsmon_agent_link_retransmits_total{agent="edge-1"} 9`,
		`hlsmon_agent_link_failures_total{agent="edge-1"} 2`,
	} {
		if !strings.Contains(string(body), want+"\n") {
			t.Errorf("metrics lack %q", want)
		}
	}
	if strings.Contains(string(body), "not-yet") {
		t.Error("an agent that was never connected must not be in the metrics")
	}
	if strings.Contains(string(body), `hlsmon_agent_link_rtt_seconds{agent="local"}`) {
		t.Error("the server's own checks have no link to the server to report")
	}
	// Every sample line is a name, optional labels and a number.
	sample := regexp.MustCompile(`^[a-z_]+(\{[a-z_]+="[^"]*"(,[a-z_]+="[^"]*")*\})? -?[0-9.e+]+$`)
	seenType := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(string(body)), "\n") {
		if strings.HasPrefix(line, "# TYPE ") {
			name := strings.Fields(line)[2]
			if seenType[name] {
				t.Errorf("TYPE of %s is given twice", name)
			}
			seenType[name] = true
			continue
		}
		if !strings.HasPrefix(line, "# ") && !sample.MatchString(line) {
			t.Errorf("malformed line %q", line)
		}
	}

	var sys systemResponse
	admin.want(http.StatusOK, "GET", "/api/v1/system", nil, &sys)
	if sys.Load.Overloaded || sys.Results.Failed != 0 {
		t.Errorf("system = %+v", sys)
	}
}

// Series of a deleted stream are removed from the time series storage
// even when it is away at that moment, and once more after everything
// that was on its way there has arrived.
func TestSeriesOfDeletedStreamAreRemoved(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	var mu sync.Mutex
	var deleted []string
	down := true
	vm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if down {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		if r.URL.Path == "/api/v1/admin/tsdb/delete_series" {
			deleted = append(deleted, r.URL.Query().Get("match[]"))
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer vm.Close()
	mx := metrics.New(vm.URL, slog.New(slog.DiscardHandler))
	srv := New(Config{Version: "test"}, st, mx, ingest.New(st, mx, nil), nil, nil, slog.New(slog.DiscardHandler))
	now := time.Now()
	srv.now = func() time.Time { return now }

	s, err := st.CreateStream(ctx, store.StreamInput{Name: "gone", URL: "http://x/a.m3u8", Enabled: true, Level: "header"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteStream(ctx, s.ID); err != nil {
		t.Fatal(err)
	}
	pending := func() int { p, _ := st.SeriesDeletions(ctx); return len(p) }
	got := func() []string { mu.Lock(); defer mu.Unlock(); return append([]string(nil), deleted...) }

	srv.PurgeSeries(ctx)
	if pending() != 1 || len(got()) != 0 {
		t.Fatalf("while the storage is away the deletion must wait: pending %d, deleted %v", pending(), got())
	}
	mu.Lock()
	down = false
	mu.Unlock()
	srv.PurgeSeries(ctx)
	if d := got(); len(d) != 1 || d[0] != `{stream="1"}` {
		t.Fatalf("deleted = %v", d)
	}
	if pending() != 1 {
		t.Fatal("samples may still be on their way: the deletion must be repeated later")
	}
	now = now.Add(3 * time.Minute)
	srv.PurgeSeries(ctx)
	if len(got()) != 2 || pending() != 0 {
		t.Errorf("deleted %v, pending %d", got(), pending())
	}
}

// A stream is watched through the server: the browser is given signed
// paths only, playlists come with their addresses replaced by such paths,
// and what they lead to is what the origin serves.
func TestWatchThroughTheServer(t *testing.T) {
	e := newEnv(t)
	admin := e.login("admin")
	admin.want(http.StatusCreated, "POST", "/api/v1/streams", map[string]any{"name": "live", "url": e.origin.URL + "/master.m3u8", "level": "header", "interval_sec": 1}, nil)
	e.client().want(http.StatusUnauthorized, "GET", "/api/v1/streams/1/watch", nil, nil)

	var watch watchResponse
	admin.want(http.StatusOK, "GET", "/api/v1/streams/1/watch", nil, &watch)
	sources := watch.Sources
	if len(watch.Vantages) != 1 || !watch.Vantages[0].Local || !watch.Vantages[0].Available || watch.Vantage != store.LocalAgentID {
		t.Fatalf("vantages = %+v", watch)
	}
	if len(sources) != 1 || sources[0].Kind != "configured" || !strings.HasPrefix(sources[0].URL, "/api/v1/play/") {
		t.Fatalf("sources = %+v", sources)
	}
	viewer := e.login("viewer")
	get := func(path string) (int, string, http.Header) {
		t.Helper()
		req, _ := http.NewRequest("GET", e.http.URL+path, nil)
		resp, err := viewer.hc.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b), resp.Header
	}
	uris := func(playlist string) (out []string) {
		for _, line := range strings.Split(playlist, "\n") {
			if line != "" && !strings.HasPrefix(line, "#") {
				out = append(out, line)
			}
		}
		return out
	}

	code, master, hdr := get(sources[0].URL)
	if code != http.StatusOK || !strings.HasPrefix(master, "#EXTM3U") || !strings.Contains(hdr.Get("Content-Type"), "mpegurl") {
		t.Fatalf("master: %d %q", code, master)
	}
	if strings.Contains(master, "127.0.0.1") || strings.Contains(master, ".m3u8") {
		t.Errorf("the playlist still names the origin:\n%s", master)
	}
	variants := uris(master)
	if len(variants) == 0 {
		t.Fatalf("no variants in\n%s", master)
	}
	// A path in a playlist is relative to the playlist's own.
	code, media, _ := get("/api/v1/play/" + variants[0])
	if code != http.StatusOK || !strings.Contains(media, "#EXTINF") {
		t.Fatalf("media playlist: %d %q", code, media)
	}
	segments := uris(media)
	if len(segments) == 0 {
		t.Fatalf("no segments in\n%s", media)
	}
	// A player reads a live playlist again and again and expects a segment
	// to keep its address.
	time.Sleep(1100 * time.Millisecond)
	if _, again, _ := get("/api/v1/play/" + variants[0]); again != media {
		t.Errorf("the same playlist read twice names its segments differently:\n%s\n---\n%s", media, again)
	}
	code, seg, hdr := get("/api/v1/play/" + segments[0])
	if code != http.StatusOK || len(seg) < 188 || seg[0] != 0x47 {
		t.Fatalf("segment: %d, %d bytes", code, len(seg))
	}
	if up := hdr.Get("X-Upstream"); !strings.HasPrefix(up, "127.0.0.1:") {
		t.Errorf("X-Upstream = %q", up)
	}
	// How the fetch went is told to the page, for its charts.
	var info fetchInfo
	raw, err := base64.StdEncoding.DecodeString(hdr.Get("X-Fetch-Info"))
	if err != nil || json.Unmarshal(raw, &info) != nil || info.Origin == nil || info.Origin.Bytes != int64(len(seg)) || info.Origin.TotalMs <= 0 || info.Relay != nil {
		t.Errorf("X-Fetch-Info = %s (%v)", raw, err)
	}
	// The requests to the origin are sent from here, and the kernel tells
	// how many packets that took: what loss on the way there is a share of.
	if runtime.GOOS == "linux" && (info.Origin.Segments <= 0 || info.Origin.Retransmits < 0) {
		t.Errorf("segments sent to the origin = %d, sent again = %d", info.Origin.Segments, info.Origin.Retransmits)
	}
	if !strings.HasPrefix(hdr.Get("Server-Timing"), "origin;dur=") {
		t.Errorf("Server-Timing = %q", hdr.Get("Server-Timing"))
	}

	// Nothing but what the server signed is served.
	token := strings.TrimPrefix(sources[0].URL, "/api/v1/play/")
	body, sig, _ := strings.Cut(token, ".")
	forged := base64.RawURLEncoding.EncodeToString([]byte(`{"u":"http://127.0.0.1:1/secret","s":1,"e":99999999999}`))
	for name, bad := range map[string]string{"another address under the same signature": forged + "." + sig, "a changed signature": body + ".AAAAAAAAAAAAAAAAAAAAAA", "no signature": body} {
		if code, _, _ := get("/api/v1/play/" + bad); code != http.StatusForbidden {
			t.Errorf("%s: status %d, want 403", name, code)
		}
	}
	// What the origin does not have is the origin's answer, not an error of ours.
	e.srv.now = func() time.Time { return time.Now() }
	missing := e.srv.signPlay(playToken{URL: e.origin.URL + "/nothing.ts", Stream: 1, Expires: time.Now().Add(time.Minute).Unix(), Inside: true})
	if code, _, _ := get("/api/v1/play/" + missing); code != http.StatusNotFound {
		t.Errorf("a missing segment: status %d, want 404", code)
	}
	expired := e.srv.signPlay(playToken{URL: e.origin.URL + "/master.m3u8", Stream: 1, Expires: time.Now().Add(-time.Minute).Unix(), Entry: true})
	if code, _, _ := get("/api/v1/play/" + expired); code != http.StatusForbidden {
		t.Errorf("an expired path: status %d, want 403", code)
	}

	// Once the stream has been checked, the server it was served from is known.
	waitFor(t, "the stream to be checked", func() bool {
		return e.srv.entryFinalURL(context.Background(), 1) != nil
	})

	// An agent that is not asking for such work: the viewer is told so,
	// and soon.
	e.srv.fetchHandOver = 100 * time.Millisecond
	idle := e.srv.signPlay(playToken{URL: e.origin.URL + "/master.m3u8", Stream: 1, Agent: 7, Expires: time.Now().Add(time.Minute).Unix(), Entry: true})
	if code, body, _ := get("/api/v1/play/" + idle); code != http.StatusBadGateway || !strings.Contains(body, "not taking requests") {
		t.Errorf("an agent that does not listen: %d %s", code, body)
	}
}

func TestRewritePlaylist(t *testing.T) {
	in := "#EXTM3U\r\n#EXT-X-KEY:METHOD=AES-128,URI=\"key.bin\",IV=0x1\n#EXT-X-MAP:URI=\"/init.mp4\"\n#EXT-X-KEY:METHOD=SAMPLE-AES,URI=\"skd://abc\"\n#EXTINF:2.0,\nseg-1.ts?t=1\n#EXTINF:2.0,\nhttps://other.example/a/seg-2.ts\n"
	got := string(rewritePlaylist([]byte(in), "http://origin.example/live/v0/index.m3u8", func(u string) string { return "<" + u + ">" }))
	want := "#EXTM3U\n#EXT-X-KEY:METHOD=AES-128,URI=\"<http://origin.example/live/v0/key.bin>\",IV=0x1\n#EXT-X-MAP:URI=\"<http://origin.example/init.mp4>\"\n#EXT-X-KEY:METHOD=SAMPLE-AES,URI=\"skd://abc\"\n#EXTINF:2.0,\n<http://origin.example/live/v0/seg-1.ts?t=1>\n#EXTINF:2.0,\n<https://other.example/a/seg-2.ts>\n\n"
	if got != want {
		t.Errorf("rewritten playlist:\n%s\nwant:\n%s", got, want)
	}
	if from, n, ok := parseRange("bytes=100-299"); !ok || from != 100 || n != 200 {
		t.Errorf("range: %d %d %v", from, n, ok)
	}
	for _, bad := range []string{"", "bytes=5-", "bytes=-5", "bytes=0-1,5-9", "items=0-1"} {
		if _, _, ok := parseRange(bad); ok {
			t.Errorf("range %q must not be passed on", bad)
		}
	}
}

// With a key that is kept, an address given before a restart of the
// server works after it.
func TestPlayKeySurvivesRestart(t *testing.T) {
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	mx := metrics.New("", nil)
	key := []byte("0123456789abcdef0123456789abcdef")
	tok := playToken{URL: "http://x/a.m3u8", Stream: 1, Expires: time.Now().Add(time.Hour).Unix()}
	before := New(Config{PlayKey: key}, st, mx, ingest.New(st, mx, nil), nil, nil, nil)
	after := New(Config{PlayKey: key}, st, mx, ingest.New(st, mx, nil), nil, nil, nil)
	other := New(Config{}, st, mx, ingest.New(st, mx, nil), nil, nil, nil)
	signed := before.signPlay(tok)
	if got, ok := after.openPlay(signed); !ok || got.URL != tok.URL {
		t.Error("the same key must accept the address after a restart")
	}
	if _, ok := other.openPlay(signed); ok {
		t.Error("another key must refuse it")
	}
}

func TestServerIncidents(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	now := time.Now().Truncate(time.Second)
	e.srv.now = func() time.Time { return now }
	ing := ingest.New(e.st, metrics.New("", nil), nil)
	variants := []probe.Variant{{Key: probe.VariantMedia, Kind: probe.KindVideo}}
	streams := map[string]*store.Stream{}
	report := func(name, server string) {
		t.Helper()
		st := streams[name]
		if st == nil {
			var err error
			if st, err = e.st.CreateStream(ctx, store.StreamInput{Name: name, URL: "http://x/" + name + ".m3u8", Enabled: true, Level: "playlist",
				AgentIDs: []int64{store.LocalAgentID}}, now); err != nil {
				t.Fatal(err)
			}
			streams[name] = st
		}
		r := &probe.Result{TargetID: st.ID, Time: now, Status: probe.StatusOK, Variants: variants,
			Metrics: []probe.Metric{{Name: "hls_up", Variant: probe.VariantMedia, Value: 1}}}
		if server != "" {
			r.Status = probe.StatusFail
			r.Problems = []probe.Problem{{Kind: probe.ProbPlaylistHTTP, Severity: probe.SevCrit, Variant: probe.VariantMedia, Message: "playlist returned HTTP 404", Count: 1,
				Details: map[string]any{"playlist_server": server}}}
		}
		if err := ing.Apply(ctx, store.LocalAgentID, r); err != nil {
			t.Fatal(err)
		}
	}
	c := e.login("viewer")
	list := func(q string) []store.ServerIncident {
		t.Helper()
		var out []store.ServerIncident
		c.want(200, "GET", "/api/v1/server-incidents"+q, nil, &out)
		return out
	}

	// Two streams of a server are two streams; a third makes it the server's.
	report("a", "bad-server 10.0.0.6")
	report("b", "bad-server 10.0.0.6")
	report("z", "other-server 10.0.0.9")
	e.srv.WatchServers(ctx)
	if got := list(""); len(got) != 0 {
		t.Fatalf("two streams made an incident of the server: %+v", got)
	}
	report("c", "bad-server 10.0.0.6")
	e.srv.WatchServers(ctx)
	got := list("?open=true")
	if len(got) != 1 || got[0].Host != "bad-server" || got[0].IP != "10.0.0.6" || got[0].Kind != probe.ProbPlaylistHTTP ||
		got[0].Severity != probe.SevCrit || got[0].Streams != 3 || got[0].EndedAt != nil {
		t.Fatalf("server incidents = %+v", got)
	}
	var d serverIncidentDetails
	if err := json.Unmarshal(got[0].Details, &d); err != nil || len(d.Streams) != 3 || d.Streams[0].Name != "a" {
		t.Errorf("details = %s, %v", got[0].Details, err)
	}
	var ov overviewResponse
	c.want(200, "GET", "/api/v1/overview", nil, &ov)
	if len(ov.ServerIncidents) != 1 {
		t.Errorf("the overview names %d incidents of servers, want 1", len(ov.ServerIncidents))
	}

	// The streams recover: the incident outlives them by a little, then ends.
	step := func(d time.Duration) { now = now.Add(d) }
	for range 3 {
		step(10 * time.Second)
		for _, name := range []string{"a", "b", "c"} {
			report(name, "")
		}
		e.srv.WatchServers(ctx)
	}
	if got := list("?open=true"); len(got) != 1 {
		t.Fatalf("the incident ended %v after the problem, sooner than it should: %+v", 30*time.Second, got)
	}
	step(2 * time.Minute)
	e.srv.WatchServers(ctx)
	if got := list("?open=true"); len(got) != 0 {
		t.Fatalf("still open long after the problem was gone: %+v", got)
	}
	first := list("")[0]

	// It comes back a few minutes later: the same incident goes on.
	step(time.Minute)
	for _, name := range []string{"a", "b", "c"} {
		report(name, "bad-server 10.0.0.6")
	}
	e.srv.WatchServers(ctx)
	got = list("")
	if len(got) != 1 || got[0].ID != first.ID || got[0].EndedAt != nil || got[0].Episodes != 2 || got[0].IdleSeconds != 60 {
		t.Fatalf("after the problem came back: %+v", got)
	}
}

func TestWayToTheViewer(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("read on Linux only")
	}
	e := newEnv(t)
	// The server as it runs: handlers can find the connection of a request.
	srv := httptest.NewUnstartedServer(e.srv.Handler())
	srv.Config.ConnContext = ConnContext
	srv.Start()
	t.Cleanup(srv.Close)
	e.http = srv

	admin := e.login("admin")
	var st streamView
	admin.want(http.StatusCreated, "POST", "/api/v1/streams", map[string]any{"name": "s", "url": e.origin.URL + "/master.m3u8", "level": "playlist"}, &st)
	var info watchResponse
	admin.want(http.StatusOK, "GET", fmt.Sprintf("/api/v1/streams/%d/watch", st.ID), nil, &info)
	admin.want(http.StatusNoContent, "GET", "/api/v1/ping", nil, nil)

	get := func(headers map[string]string) *fetchInfo {
		t.Helper()
		req, _ := http.NewRequest("GET", srv.URL+info.Sources[0].URL, nil)
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		resp, err := admin.hc.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		raw, err := base64.StdEncoding.DecodeString(resp.Header.Get("X-Fetch-Info"))
		var fi fetchInfo
		if err != nil || json.Unmarshal(raw, &fi) != nil {
			t.Fatalf("X-Fetch-Info = %q (%v)", resp.Header.Get("X-Fetch-Info"), err)
		}
		return &fi
	}
	first := get(nil)
	if first.Client == nil || first.Client.RTTMs <= 0 || first.Client.RTTMs > 1000 {
		t.Fatalf("the way to the viewer = %+v", first.Client)
	}
	// The counters are those of the connection; an answer tells how much
	// they grew since the one before, not the whole again.
	second := get(nil)
	if second.Client == nil || second.Client.Segments <= 0 || second.Client.Segments > first.Client.Segments+8 {
		t.Errorf("segments sent: %d at first, then %d more", first.Client.Segments, second.Client.Segments)
	}
	// Behind a proxy the connection is not the viewer's.
	if via := get(map[string]string{"X-Forwarded-For": "203.0.113.7"}); via.Client != nil {
		t.Errorf("a request that came through a proxy was told the way to the proxy: %+v", via.Client)
	}
}

func TestSlates(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	now := time.Now().Truncate(time.Second)
	st, err := e.st.CreateStream(ctx, store.StreamInput{Name: "tv", URL: "http://x/tv.m3u8", Enabled: true, Level: "segments",
		AgentIDs: []int64{store.LocalAgentID}}, now)
	if err != nil {
		t.Fatal(err)
	}
	cells := bytes.Repeat([]byte{70}, 32*18)
	jpg := []byte{0xFF, 0xD8, 0xFF, 0xD9}
	ing := ingest.New(e.st, metrics.New("", nil), nil)
	if err := ing.Apply(ctx, store.LocalAgentID, &probe.Result{TargetID: st.ID, Time: now, Status: probe.StatusOK,
		Picture: &probe.Picture{Variant: probe.VariantMedia, Time: now, HasPicture: true, Luma: 70, JPEG: jpg, Cells: cells}}); err != nil {
		t.Fatal(err)
	}

	admin, viewer := e.login("admin"), e.login("viewer")
	mark := map[string]any{"name": "technical break", "stream_id": st.ID}
	viewer.want(http.StatusForbidden, "POST", "/api/v1/slates", mark, nil)
	admin.want(http.StatusNotFound, "POST", "/api/v1/slates", map[string]any{"name": "x", "stream_id": 999}, nil)
	admin.want(http.StatusBadRequest, "POST", "/api/v1/slates", map[string]any{"stream_id": st.ID}, nil)

	var sl store.Slate
	admin.want(http.StatusOK, "POST", "/api/v1/slates", mark, &sl)
	if sl.Name != "technical break" || len(sl.Frames) != 1 {
		t.Fatalf("slate = %+v", sl)
	}
	// Marked again under the same name: another frame of the same slate.
	admin.want(http.StatusOK, "POST", "/api/v1/slates", mark, &sl)
	if len(sl.Frames) != 2 {
		t.Fatalf("frames after a second mark = %d", len(sl.Frames))
	}
	var list []store.Slate
	viewer.want(http.StatusOK, "GET", "/api/v1/slates", nil, &list)
	if len(list) != 1 || len(list[0].Frames) != 2 {
		t.Fatalf("slates = %+v", list)
	}
	resp, err := viewer.hc.Get(e.http.URL + fmt.Sprintf("/api/v1/slates/frames/%d", list[0].Frames[0].ID))
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "image/jpeg" || !bytes.Equal(got, jpg) {
		t.Errorf("frame: %d %s %x", resp.StatusCode, resp.Header.Get("Content-Type"), got)
	}

	// The checks learn of it at once, and so does what an agent is told.
	known := e.srv.Slates()
	if len(known) != 1 || known[0].Name != "technical break" || len(known[0].Frames) != 2 || !bytes.Equal(known[0].Frames[0], cells) {
		t.Fatalf("the checks know %+v", known)
	}
	with := slatesRevision(known)
	// One picture can be forgotten by itself; the slate goes with its last.
	viewer.want(http.StatusForbidden, "DELETE", fmt.Sprintf("/api/v1/slates/frames/%d", list[0].Frames[0].ID), nil, nil)
	admin.want(http.StatusNoContent, "DELETE", fmt.Sprintf("/api/v1/slates/frames/%d", list[0].Frames[0].ID), nil, nil)
	if known := e.srv.Slates(); len(known) != 1 || len(known[0].Frames) != 1 {
		t.Fatalf("after one picture was forgotten the checks know %+v", known)
	}
	admin.want(http.StatusNoContent, "DELETE", fmt.Sprintf("/api/v1/slates/frames/%d", list[0].Frames[1].ID), nil, nil)
	if known := e.srv.Slates(); len(known) != 0 {
		t.Fatalf("the slate outlived its last picture: %+v", known)
	}
	admin.want(http.StatusOK, "POST", "/api/v1/slates", mark, &sl)
	admin.want(http.StatusNoContent, "DELETE", fmt.Sprintf("/api/v1/slates/%d", sl.ID), nil, nil)
	if len(e.srv.Slates()) != 0 || with == "" || slatesRevision(e.srv.Slates()) == with {
		t.Errorf("after the slate was forgotten the checks know %+v", e.srv.Slates())
	}
}

func TestClipsKnownByTheirFrames(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	now := time.Now().Truncate(time.Second)
	st, err := e.st.CreateStream(ctx, store.StreamInput{Name: "tv", URL: "http://x/tv.m3u8", Enabled: true, Level: "segments",
		AgentIDs: []int64{store.LocalAgentID}}, now)
	if err != nil {
		t.Fatal(err)
	}
	// A clip going round, found without a decoder: no picture, only the
	// coded frames it is made of.
	keys := []uint64{0xfedcba9876543210, 3, 17}
	ing := ingest.New(e.st, metrics.New("", nil), nil)
	if err := ing.Apply(ctx, store.LocalAgentID, &probe.Result{TargetID: st.ID, Time: now, Status: probe.StatusFail,
		Problems: []probe.Problem{{Kind: probe.ProbPictureLoop, Severity: probe.SevCrit, Variant: probe.VariantMedia, Message: "round",
			Details: map[string]any{"clip_signature": probe.Signature(keys)}}}}); err != nil {
		t.Fatal(err)
	}
	incs, err := e.st.ListIncidents(ctx, store.IncidentFilter{Kind: probe.ProbPictureLoop})
	if err != nil || len(incs) != 1 {
		t.Fatalf("incidents = %v, %v", incs, err)
	}
	admin := e.login("admin")
	// A stream that was never looked at has nothing to be the channel's own clip by.
	admin.want(http.StatusNotFound, "POST", "/api/v1/slates", map[string]any{"name": "x", "stream_id": st.ID, "own": true}, nil)

	var sl store.Slate
	admin.want(http.StatusOK, "POST", "/api/v1/slates", map[string]any{"name": "tv: promo", "incident_id": incs[0].ID, "own": true}, &sl)
	if !sl.Own || sl.CodedFrames != 3 || len(sl.Frames) != 0 {
		t.Fatalf("the channel's own clip = %+v", sl)
	}
	known := e.srv.Slates()
	if len(known) != 1 || !known[0].Own || len(known[0].Keys) != 3 || known[0].Keys[2] != keys[0] {
		t.Fatalf("the checks know %+v", known)
	}
	// The same name cannot be a slate as well.
	admin.want(http.StatusConflict, "POST", "/api/v1/slates", map[string]any{"name": "tv: promo", "incident_id": incs[0].ID}, nil)
	admin.want(http.StatusOK, "POST", "/api/v1/slates", map[string]any{"name": "no signal", "incident_id": incs[0].ID}, &sl)
	if sl.Own || sl.CodedFrames != 3 {
		t.Fatalf("the slate = %+v", sl)
	}
	admin.want(http.StatusNoContent, "DELETE", fmt.Sprintf("/api/v1/slates/%d", sl.ID), nil, nil)
	if known := e.srv.Slates(); len(known) != 1 || known[0].Name != "tv: promo" {
		t.Fatalf("after the slate was forgotten the checks know %+v", known)
	}
}
