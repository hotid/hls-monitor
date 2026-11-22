package agent

import (
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/hotid/hls-monitor/internal/api"
	"github.com/hotid/hls-monitor/internal/capture"
)

// A capture asked for of a stream only an agent checks is made by the
// agent, sent to the server and kept there as the agent's.
func TestCaptureByAnAgent(t *testing.T) {
	captureSendEvery = 200 * time.Millisecond
	t.Cleanup(func() { captureSendEvery = 15 * time.Second })
	store, err := capture.Open(capture.Options{Dir: filepath.Join(t.TempDir(), "captures"), MaxBytes: 100 << 20})
	if err != nil {
		t.Fatal(err)
	}
	e := newEnvWith(t, func(c *api.Config) { c.Captures = store })
	var en enrollment
	e.call(http.StatusCreated, "POST", "/api/v1/agents", map[string]string{"name": "edge-1"}, &en)
	e.call(http.StatusCreated, "POST", "/api/v1/streams", map[string]any{
		"name": "live", "url": e.origin.URL + "/master.m3u8", "level": "header", "agent_ids": []int64{en.Agent.ID},
	}, nil)
	// No agent in contact: nobody to ask.
	e.call(http.StatusConflict, "POST", "/api/v1/streams/1/capture", nil, nil)

	e.start(en.Token, filepath.Join(t.TempDir(), "agent.key"))
	// The stream goes on, so that there are segments after the one captured.
	done := make(chan struct{})
	t.Cleanup(func() { close(done) })
	go func() {
		for {
			select {
			case <-done:
				return
			case <-time.After(300 * time.Millisecond):
				e.fake.Publish(1)
			}
		}
	}()
	waitFor(t, "the agent to take the capture", func() bool {
		req, _ := http.NewRequest("POST", e.server.URL+"/api/v1/streams/1/capture", nil)
		req.Header.Set("X-Requested-With", "test")
		resp, err := e.admin.Do(req)
		if err != nil {
			return false
		}
		resp.Body.Close()
		return resp.StatusCode == http.StatusAccepted
	})
	waitFor(t, "the capture to reach the server", func() bool {
		for _, c := range store.List(1) {
			if c.AgentID == en.Agent.ID && c.Done && c.Manual && len(c.Files) > 0 {
				return true
			}
		}
		return false
	})
}
