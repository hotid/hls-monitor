package agent

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hotid/hls-monitor/internal/agentproto"
	"github.com/hotid/hls-monitor/internal/api"
	"github.com/hotid/hls-monitor/internal/auth"
	"github.com/hotid/hls-monitor/internal/hlstest"
	"github.com/hotid/hls-monitor/internal/ingest"
	"github.com/hotid/hls-monitor/internal/metrics"
	"github.com/hotid/hls-monitor/internal/store"
	"github.com/hotid/hls-monitor/internal/types/id"
	"github.com/hotid/hls-monitor/internal/types/role"
	"github.com/hotid/hls-monitor/internal/types/status"
)

var quiet = slog.New(slog.DiscardHandler)

type env struct {
	t      *testing.T
	st     *store.Store
	server *httptest.Server
	origin *httptest.Server
	admin  *http.Client
	fake   *hlstest.Origin
	// down makes the server answer 503 to everything, as a broken proxy would.
	down atomic.Bool
}

func newEnv(t *testing.T) *env { return newEnvWith(t, nil) }

// newEnvWith makes the environment with the server's configuration
// changed by change.
func newEnvWith(t *testing.T, change func(*api.Config)) *env {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	hash, _ := auth.HashPassword("admin-password")
	if _, err := st.CreateUser(ctx, "admin", hash, role.Admin(), time.Now()); err != nil {
		t.Fatal(err)
	}
	mx := metrics.New("", nil)
	cfg := api.Config{Version: "test"}
	if change != nil {
		change(&cfg)
	}
	srv := api.New(cfg, st, mx, ingest.New(st, mx, quiet), nil, nil, quiet)

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
	e.fake = fake
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
		ID id.Agent `json:"id"`
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

func (e *env) state(streamID id.Stream, agentID id.Agent) *store.StreamState {
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
		a, _ := e.st.GetAgent(context.Background(), id.AgentOf(en.Agent.ID.Int64()))
		return a != nil && a.Enrolled && a.LastSeen != nil && a.Version == "1.2.3" && a.ClockOffsetMs != nil
	})

	// A stream assigned to the agent starts being checked without a restart.
	e.call(http.StatusCreated, "POST", "/api/v1/streams", map[string]any{
		"name": "live", "url": e.origin.URL + "/master.m3u8", "level": "container", "interval_sec": 0.2 + 0.8, "agent_ids": []int64{en.Agent.ID.Int64()},
	}, nil)
	waitFor(t, "results from the agent", func() bool {
		s := e.state(id.StreamOf(1), id.AgentOf(en.Agent.ID.Int64()))
		return s != nil && s.Status == status.OK()
	})
	if e.state(id.StreamOf(1), store.LocalAgentID()) != nil {
		t.Error("the server must not check a stream assigned only to a remote agent")
	}
	segs, _ := e.st.ListSegments(context.Background(), store.SegmentFilter{StreamID: id.StreamOf(1), AgentID: id.AgentOf(en.Agent.ID.Int64())})
	if len(segs) == 0 {
		t.Error("the agent's segment records did not reach the journal")
	}

	// The server goes away: checks continue; when it returns, the backlog arrives.
	before := e.state(id.StreamOf(1), id.AgentOf(en.Agent.ID.Int64())).CheckedAt
	e.down.Store(true)
	time.Sleep(2500 * time.Millisecond)
	e.down.Store(false)
	waitFor(t, "delivery after the outage", func() bool { return e.state(id.StreamOf(1), id.AgentOf(en.Agent.ID.Int64())).CheckedAt.After(before) })

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
	segs, err := st.ListSegments(context.Background(), store.SegmentFilter{StreamID: id.StreamOf(1), Limit: 1000})
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
		a, _ := e.st.GetAgent(context.Background(), id.AgentOf(en.Agent.ID.Int64()))
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
		a, _ := e.st.GetAgent(context.Background(), id.AgentOf(en.Agent.ID.Int64()))
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

// A stream is watched through an agent: the viewer asks the server, the
// server the agent, the agent the origin. The agent accepts no
// connections; it takes this work as answers to its own requests.
func TestWatchThroughAgent(t *testing.T) {
	e := newEnv(t)
	var en enrollment
	e.call(http.StatusCreated, "POST", "/api/v1/agents", map[string]string{"name": "edge-1"}, &en)
	e.call(http.StatusCreated, "POST", "/api/v1/streams", map[string]any{
		"name": "live", "url": e.origin.URL + "/master.m3u8", "level": "header", "agent_ids": []int64{en.Agent.ID.Int64()},
	}, nil)

	type watch struct {
		Vantages []struct {
			ID        int64  `json:"id"`
			Local     bool   `json:"local"`
			Available bool   `json:"available"`
			Why       string `json:"why"`
		} `json:"vantages"`
		Vantage id.Agent `json:"vantage"`
		Sources []struct {
			URL string `json:"url"`
		} `json:"sources"`
	}
	path := "/api/v1/streams/1/watch?agent=" + strconv.FormatInt(en.Agent.ID.Int64(), 10)
	var w watch
	e.call(http.StatusOK, "GET", path, nil, &w)
	if len(w.Vantages) != 2 || !w.Vantages[0].Local || w.Vantages[1].Available || w.Vantages[1].Why != "offline" || w.Vantage != store.LocalAgentID() {
		t.Fatalf("before the agent is connected: %+v", w)
	}

	e.start(en.Token, filepath.Join(t.TempDir(), "agent.key"))
	waitFor(t, "the agent to offer itself", func() bool {
		e.call(http.StatusOK, "GET", path, nil, &w)
		return w.Vantage == en.Agent.ID && w.Vantages[1].Available
	})

	get := func(p string) (int, []byte, http.Header) {
		t.Helper()
		resp, err := e.admin.Get(e.server.URL + p)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, b, resp.Header
	}
	first := func(playlist []byte) string {
		for _, line := range strings.Split(string(playlist), "\n") {
			if line != "" && !strings.HasPrefix(line, "#") {
				return line
			}
		}
		t.Fatalf("no address in\n%s", playlist)
		return ""
	}
	code, master, _ := get(w.Sources[0].URL)
	if code != http.StatusOK || !bytes.HasPrefix(master, []byte("#EXTM3U")) {
		t.Fatalf("master through the agent: %d %s", code, master)
	}
	code, media, _ := get("/api/v1/play/" + first(master))
	if code != http.StatusOK || !bytes.Contains(media, []byte("#EXTINF")) {
		t.Fatalf("media playlist through the agent: %d %s", code, media)
	}
	code, seg, hdr := get("/api/v1/play/" + first(media))
	want, _ := http.Get(e.origin.URL + "/v0/seg-0.ts")
	direct, _ := io.ReadAll(want.Body)
	want.Body.Close()
	if code != http.StatusOK || len(seg) == 0 || seg[0] != 0x47 || len(seg) != len(direct) {
		t.Fatalf("segment through the agent: %d, %d bytes, the origin serves %d", code, len(seg), len(direct))
	}
	if !strings.HasPrefix(hdr.Get("X-Upstream"), "127.0.0.1:") {
		t.Errorf("X-Upstream = %q", hdr.Get("X-Upstream"))
	}
	// The page is told how each hop went: the agent's request to the
	// origin, the way from the agent to the server, and the agent's link.
	var info struct {
		Origin *agentproto.FetchTiming `json:"origin"`
		Relay  *struct {
			TotalMs float64 `json:"total_ms"`
			Bytes   int64   `json:"bytes"`
		} `json:"relay"`
		Link *agentproto.Link `json:"link"`
	}
	raw, err := base64.StdEncoding.DecodeString(hdr.Get("X-Fetch-Info"))
	if err != nil || json.Unmarshal(raw, &info) != nil {
		t.Fatalf("X-Fetch-Info = %q: %v", hdr.Get("X-Fetch-Info"), err)
	}
	if info.Origin == nil || info.Origin.Bytes != int64(len(direct)) || info.Relay == nil || info.Relay.Bytes != int64(len(direct)) || info.Relay.TotalMs <= 0 {
		t.Errorf("fetch info = %s", raw)
	}
	if info.Link == nil || info.Link.Requests == 0 {
		t.Errorf("the agent's link is not reported: %s", raw)
	}
	if runtime.GOOS == "linux" && (!info.Link.Measured || !info.Origin.Measured) {
		t.Errorf("on Linux the kernel's figures must be read: %s", raw)
	}
	if st := hdr.Get("Server-Timing"); !strings.Contains(st, "relay_upload;dur=") {
		t.Errorf("Server-Timing = %q", st)
	}
	// Several at once: a player asks for a playlist and segments together.
	var wg sync.WaitGroup
	for range 6 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if code, b, _ := get("/api/v1/play/" + first(media)); code != http.StatusOK || len(b) != len(direct) {
				t.Errorf("a concurrent fetch: %d, %d bytes", code, len(b))
			}
		}()
	}
	wg.Wait()
	// What the origin does not have is its answer, passed on.
	if code, _, _ := get("/api/v1/play/" + first(master) + "x"); code != http.StatusForbidden {
		t.Errorf("a damaged token: %d", code)
	}
}

func TestDiagnosisFromAnAgent(t *testing.T) {
	e := newEnv(t)
	var en enrollment
	e.call(http.StatusCreated, "POST", "/api/v1/agents", map[string]string{"name": "edge-1"}, &en)
	e.call(http.StatusCreated, "POST", "/api/v1/streams", map[string]any{
		"name": "live", "url": e.origin.URL + "/master.m3u8", "level": "header", "agent_ids": []int64{en.Agent.ID.Int64()},
	}, nil)
	path := fmt.Sprintf("/api/v1/streams/1/diagnose?seconds=3&agent=%d", en.Agent.ID)
	// An agent that is not there cannot be asked.
	e.call(http.StatusConflict, "GET", path, nil, nil)
	e.call(http.StatusNotFound, "GET", "/api/v1/streams/1/diagnose?agent=77", nil, nil)

	e.start(en.Token, filepath.Join(t.TempDir(), "agent.key"))
	waitFor(t, "the agent to say what it can do", func() bool {
		req, _ := http.NewRequest("GET", e.server.URL+path, nil)
		resp, err := e.admin.Do(req)
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return false
		}
		// The agent checks the stream every second and the server passes
		// on what each check found, then says that the time is up.
		var types []string
		cycles := 0
		dec := json.NewDecoder(resp.Body)
		for {
			var ev struct {
				Type   string `json:"type"`
				N      int    `json:"n"`
				Reason string `json:"reason"`
				Result *struct {
					TargetID int64  `json:"target_id"`
					Status   string `json:"status"`
					Variants []any  `json:"variants"`
				} `json:"result"`
			}
			if dec.Decode(&ev) != nil {
				break
			}
			types = append(types, ev.Type)
			switch ev.Type {
			case "cycle":
				cycles++
				if ev.N != cycles || ev.Result == nil || ev.Result.TargetID != 1 || len(ev.Result.Variants) == 0 {
					t.Fatalf("cycle %d: %+v", cycles, ev)
				}
			case "end":
				if ev.Reason != "time" {
					t.Fatalf("ended because of %q", ev.Reason)
				}
			}
		}
		if cycles < 2 || len(types) < 4 || types[0] != "start" || types[len(types)-1] != "end" {
			t.Fatalf("%d cycles, events %v", cycles, types)
		}
		return true
	})
	// Nothing of it was stored: the agent's own checks are another matter,
	// and what a diagnosis finds is for the one who watches it.
	if got := e.state(id.StreamOf(1), id.AgentOf(en.Agent.ID.Int64())); got != nil && got.Status.IsZero() {
		t.Errorf("state = %+v", got)
	}
}
