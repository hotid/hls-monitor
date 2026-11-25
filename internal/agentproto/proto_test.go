package agentproto

import (
	"encoding/json"
	"testing"

	"github.com/hotid/hls-monitor/internal/probe"
	"github.com/hotid/hls-monitor/internal/types/id"
	"github.com/hotid/hls-monitor/internal/types/level"
	"github.com/hotid/hls-monitor/internal/types/picturemode"
	"github.com/hotid/hls-monitor/internal/types/problemkind"
)

// A server and its agents are not always of one version. What an older
// one writes is read as it was meant.
func TestTargetFromAnOlderServer(t *testing.T) {
	var cfg Config
	old := `{"agent_id":2,"name":"edge","enabled":true,"revision":"r","targets":[
		{"id":7,"url":"http://x/a.m3u8","level":"header","interval_sec":0,"timeout_sec":0,"max_segments":0,"revision":3},
		{"id":8,"url":"http://x/b.m3u8","level":"container","picture":"static","revision":1}],"poll_sec":15,"server_time":1}`
	if err := json.Unmarshal([]byte(old), &cfg); err != nil {
		t.Fatal(err)
	}
	if len(cfg.Targets) != 2 || cfg.AgentID != id.AgentOf(2) || cfg.Targets[0].ID != id.StreamOf(7) || cfg.Targets[0].Level != level.Header() {
		t.Fatalf("read as %+v", cfg)
	}
	// One that does not say what the picture is checked for means all of it.
	if cfg.Targets[0].Picture != picturemode.Check() || cfg.Targets[1].Picture != picturemode.Static() {
		t.Errorf("picture modes read as %v and %v", cfg.Targets[0].Picture, cfg.Targets[1].Picture)
	}
	b, err := json.Marshal(probe.Target{ID: id.StreamOf(7), URL: "http://x/", Level: level.Segments(), Picture: picturemode.Check()})
	if err != nil {
		t.Fatal(err)
	}
	var back probe.Target
	if err := json.Unmarshal(b, &back); err != nil || back.Picture != picturemode.Check() || back.Level != level.Segments() {
		t.Errorf("written as %s and read back as %+v, %v", b, back, err)
	}
	if err := json.Unmarshal([]byte(`{"id":7,"level":"deeper"}`), &back); err == nil {
		t.Error("a level there is none of was read")
	}
}

// A result that tells of what its reader has no name for is refused by
// itself; the rest of its batch is taken.
func TestResultFromANewerAgent(t *testing.T) {
	batch := `{"results":[
		{"target_id":1,"time":"2026-01-01T00:00:00Z","status":"ok","metrics":[{"n":"hls_up","v":"media","x":1}]},
		{"target_id":1,"time":"2026-01-01T00:00:05Z","status":"fail","metrics":[],"problems":[{"kind":"a_problem_of_tomorrow","severity":"crit","variant":"media","message":"m","count":1}]},
		{"target_id":1,"time":"2026-01-01T00:00:10Z","status":"warn","metrics":[],"problems":[{"kind":"playlist_stale","severity":"warn","variant":"media","message":"m","count":1,"details":{"unchanged_for":12}}]}]}`
	var req ResultsRequest
	if err := json.Unmarshal([]byte(batch), &req); err != nil {
		t.Fatal(err)
	}
	if len(req.Results) != 3 || req.Results[0] == nil || req.Results[1] != nil || req.Results[2] == nil || len(req.Unreadable) != 1 {
		t.Fatalf("read as %v, unreadable %v", req.Results, req.Unreadable)
	}
	if p := req.Results[2].Problems; len(p) != 1 || p[0].Kind != problemkind.PlaylistStale() || p[0].Details.Len() != 1 {
		t.Errorf("the result after the one refused: %+v", p)
	}
}
