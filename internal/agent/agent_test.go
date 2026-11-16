package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hotid/hls-monitor/internal/api"
	"github.com/hotid/hls-monitor/internal/auth"
	"github.com/hotid/hls-monitor/internal/hlstest"
	"github.com/hotid/hls-monitor/internal/ingest"
	"github.com/hotid/hls-monitor/internal/metrics"
	"github.com/hotid/hls-monitor/internal/store"
)

var quiet = slog.New(slog.DiscardHandler)

type env struct {
	t      *testing.T
	st     *store.Store
	server *httptest.Server
	origin *httptest.Server
	admin  *http.Client
	// down makes the server answer 503 to everything, as a broken proxy would.
	down atomic.Bool
}

func newEnv(t *testing.T) *env {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	hash, _ := auth.HashPassword("admin-password")
	if _, err := st.CreateUser(ctx, "admin", hash, store.RoleAdmin, time.Now()); err != nil {
		t.Fatal(err)
	}
	mx := metrics.New("", nil)
	srv := api.New(api.Config{Version: "test"}, st, mx, ingest.New(st, mx, quiet), nil, nil, quiet)

	e := &env{t: t, st: st}
	e.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if e.down.Load() {
			http.Error(w, "bad gateway", http.StatusServiceUnavailable)
			return
		}
		srv.Handler().ServeHTTP(w, r)
	}))
	t.Cleanup(e.server.Close)

	fake := hlstest.New(hlstest.Options{})
	fake.Publish(4)
	e.origin = httptest.NewServer(fake.Handler())
	t.Cleanup(e.origin.Close)

	jar, _ := cookiejar.New(nil)
	e.admin = &http.Client{Jar: jar}
	e.call(http.StatusOK, "POST", "/api/v1/auth/login", map[string]string{"username": "admin", "password": "admin-password"}, nil)
	return e
}

func (e *env) call(want int, method, path string, body, out any) {
	e.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, e.server.URL+path, rd)
	req.Header.Set("X-Requested-With", "test")
	req.Header.Set("Content-Type", "application/json")
	resp, err := e.admin.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != want {
		e.t.Fatalf("%s %s = %d, want %d: %s", method, path, resp.StatusCode, want, b)
	}
	if out != nil {
		if err := json.Unmarshal(b, out); err != nil {
			e.t.Fatalf("decode %s: %v", b, err)
		}
	}
}

type enrollment struct {
	Agent struct {
		ID int64 `json:"id"`
	} `json:"agent"`
	Token string `json:"token"`
}

// start runs an agent in the background; the returned function stops it and
// returns what Run returned.
func (e *env) start(token, keyFile string) (stop func() error) {
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() {
		errc <- Run(ctx, Options{ServerURL: e.server.URL, EnrollToken: token, KeyFile: keyFile, Version: "1.2.3", Log: quiet, PollInterval: 50 * time.Millisecond})
	}()
	stopped := false
	stop = func() error {
		if stopped {
			return nil
		}
		stopped = true
		cancel()
		select {
		case err := <-errc:
			return err
		case <-time.After(10 * time.Second):
			e.t.Error("agent did not stop")
			return nil
		}
	}
	e.t.Cleanup(func() { _ = stop() })
	return stop
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func (e *env) state(streamID, agentID int64) *store.StreamState {
	states, err := e.st.StreamStates(context.Background(), streamID)
	if err != nil {
		e.t.Fatal(err)
	}
	for _, s := range states {
		if s.AgentID == agentID {
			return s
		}
	}
	return nil
}

func TestAgentEndToEnd(t *testing.T) {
	e := newEnv(t)
	var en enrollment
	e.call(http.StatusCreated, "POST", "/api/v1/agents", map[string]string{"name": "edge-1"}, &en)
	keyFile := filepath.Join(t.TempDir(), "sub", "agent.key")
	stop := e.start(en.Token, keyFile)

	// Enrollment: the key is saved with tight permissions and the server sees the agent.
	waitFor(t, "key file", func() bool { _, err := os.Stat(keyFile); return err == nil })
	if fi, _ := os.Stat(keyFile); fi.Mode().Perm() != 0o600 {
		t.Errorf("key file mode = %v, want 0600", fi.Mode().Perm())
	}
	waitFor(t, "heartbeat", func() bool {
		a, _ := e.st.GetAgent(context.Background(), en.Agent.ID)
		return a != nil && a.Enrolled && a.LastSeen != nil && a.Version == "1.2.3" && a.ClockOffsetMs != nil
	})

	// A stream assigned to the agent starts being checked without a restart.
	e.call(http.StatusCreated, "POST", "/api/v1/streams", map[string]any{
		"name": "live", "url": e.origin.URL + "/master.m3u8", "level": "container", "interval_sec": 0.2 + 0.8, "agent_ids": []int64{en.Agent.ID},
	}, nil)
	waitFor(t, "results from the agent", func() bool { s := e.state(1, en.Agent.ID); return s != nil && s.Status == "ok" })
	if e.state(1, store.LocalAgentID) != nil {
		t.Error("the server must not check a stream assigned only to a remote agent")
	}
	segs, _ := e.st.ListSegments(context.Background(), store.SegmentFilter{StreamID: 1, AgentID: en.Agent.ID})
	if len(segs) == 0 {
		t.Error("the agent's segment records did not reach the journal")
	}

	// The server goes away: checks continue; when it returns, the backlog arrives.
	before := e.state(1, en.Agent.ID).CheckedAt
	e.down.Store(true)
	time.Sleep(2500 * time.Millisecond)
	e.down.Store(false)
	waitFor(t, "delivery after the outage", func() bool { return e.state(1, en.Agent.ID).CheckedAt.After(before) })

	// Unassigning stops the checks.
	e.call(http.StatusOK, "PUT", "/api/v1/streams/1", map[string]any{"name": "live", "url": e.origin.URL + "/master.m3u8", "agent_ids": []int64{}}, nil)
	time.Sleep(300 * time.Millisecond)
	n := len(mustSegments(t, e.st))
	time.Sleep(1500 * time.Millisecond)
	if got := len(mustSegments(t, e.st)); got != n {
		t.Errorf("segments kept arriving after the stream was unassigned: %d → %d", n, got)
	}

	if err := stop(); err != nil {
		t.Errorf("a clean stop returned %v", err)
	}
}

func mustSegments(t *testing.T, st *store.Store) []*store.SegmentRecord {
	t.Helper()
	segs, err := st.ListSegments(context.Background(), store.SegmentFilter{StreamID: 1, Limit: 1000})
	if err != nil {
		t.Fatal(err)
	}
	return segs
}

func TestAgentRestartUsesSavedKey(t *testing.T) {
	e := newEnv(t)
	var en enrollment
	e.call(http.StatusCreated, "POST", "/api/v1/agents", map[string]string{"name": "edge-1"}, &en)
	keyFile := filepath.Join(t.TempDir(), "agent.key")

	stop := e.start(en.Token, keyFile)
	waitFor(t, "enrollment", func() bool { _, err := os.Stat(keyFile); return err == nil })
	_ = stop()
	key1, _ := os.ReadFile(keyFile)

	// The token is spent; a restart must work from the key file alone.
	seen := time.Now()
	stop = e.start("", keyFile)
	waitFor(t, "heartbeat after restart", func() bool {
		a, _ := e.st.GetAgent(context.Background(), en.Agent.ID)
		return a.LastSeen != nil && a.LastSeen.After(seen)
	})
	_ = stop()

	// The administrator resets the token: the old key dies, and an agent
	// started with the new token enrolls again over the stale key file.
	var again enrollment
	e.call(http.StatusOK, "POST", "/api/v1/agents/2/token", nil, &again)
	e.start(again.Token, keyFile)
	waitFor(t, "re-enrollment", func() bool {
		key2, _ := os.ReadFile(keyFile)
		a, _ := e.st.GetAgent(context.Background(), en.Agent.ID)
		return !bytes.Equal(key1, key2) && a.Enrolled
	})
}

func TestAgentRejected(t *testing.T) {
	e := newEnv(t)
	dir := t.TempDir()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err := Run(ctx, Options{ServerURL: e.server.URL, EnrollToken: "not-a-token", KeyFile: filepath.Join(dir, "a.key"), Log: quiet})
	if !errors.Is(err, ErrRejected) {
		t.Errorf("a wrong enrollment token: %v, want ErrRejected", err)
	}

	// A key the server does not know, and no token to enroll again with.
	stale := filepath.Join(dir, "stale.key")
	_ = os.WriteFile(stale, []byte("some-old-key\n"), 0o600)
	err = Run(ctx, Options{ServerURL: e.server.URL, KeyFile: stale, Log: quiet})
	if !errors.Is(err, ErrRejected) {
		t.Errorf("a revoked key: %v, want ErrRejected", err)
	}

	if err := Run(ctx, Options{ServerURL: e.server.URL, KeyFile: filepath.Join(dir, "none.key"), Log: quiet}); err == nil {
		t.Error("no key and no token must be an error")
	}
	if err := Run(ctx, Options{ServerURL: "monitor.example.com", KeyFile: stale, Log: quiet}); err == nil {
		t.Error("a server URL without a scheme must be an error")
	}
}
