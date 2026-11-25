package agentproto

import (
	"encoding/json"
	"testing"

	"github.com/hotid/hls-monitor/internal/probe"
	"github.com/hotid/hls-monitor/internal/types/answerkind"
	"github.com/hotid/hls-monitor/internal/types/id"
	"github.com/hotid/hls-monitor/internal/types/jobkind"
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

// Work says what it is in one way, and work that does not is refused
// rather than guessed at.
func TestWorkIsOfOneKind(t *testing.T) {
	fetch := Fetch(FetchRequest{ID: "a", URL: "http://x/a.ts"})
	diagnose := Diagnose(DiagnoseJob{Seconds: 5})
	diagnose.ID = "b"
	capture := Capture(CaptureJob{StreamID: id.StreamOf(7)})
	capture.ID = "c"
	for _, tc := range []struct {
		rq   FetchRequest
		want jobkind.Kind
	}{{fetch, jobkind.Fetch()}, {diagnose, jobkind.Diagnose()}, {capture, jobkind.Capture()}} {
		if got, err := tc.rq.Work(); err != nil || got != tc.want {
			t.Errorf("work of the kind %v read as %v, %v", tc.want, got, err)
		}
	}
	// A server from before kinds were told: the kind is what came with it.
	var old FetchRequest
	if err := json.Unmarshal([]byte(`{"id":"d","url":"","max_body":0,"diagnose":{"target":{"id":7,"url":"http://x/a.m3u8","level":"header"},"seconds":5}}`), &old); err != nil {
		t.Fatal(err)
	}
	if got, err := old.Work(); err != nil || got != jobkind.Diagnose() {
		t.Errorf("work of an older server read as %v, %v", got, err)
	}
	both := diagnose
	both.Kind, both.Capture = jobkind.Kind{}, capture.Capture
	wrong := fetch
	wrong.Diagnose = diagnose.Diagnose
	addressed := diagnose
	addressed.URL = "http://x/a.ts"
	for name, rq := range map[string]FetchRequest{"both a diagnosis and a capture": both, "a fetch that carries a diagnosis": wrong,
		"a diagnosis with an address": addressed, "a fetch of nothing": Fetch(FetchRequest{ID: "e"}), "no ID": Fetch(FetchRequest{URL: "http://x/"})} {
		if _, err := rq.Work(); err == nil {
			t.Errorf("%s was taken as work", name)
		}
	}
	if json.Unmarshal([]byte(`{"id":"f","kind":"reboot"}`), &old) == nil {
		t.Error("work of a kind that is not known was read")
	}
}

// An answer says what it is; one of an agent from before that is read by
// its content type.
func TestAnswerKinds(t *testing.T) {
	cycle, end := CycleAnswer(), EndAnswer()
	if cycle.Answer() != answerkind.DiagnoseCycle() || end.Answer() != answerkind.DiagnoseEnd() {
		t.Errorf("answers read as %v and %v", cycle.Answer(), end.Answer())
	}
	for ct, want := range map[string]answerkind.Kind{DiagnoseCycleType: answerkind.DiagnoseCycle(), DiagnoseEndType: answerkind.DiagnoseEnd(), "video/mp2t": answerkind.Fetched(), "": answerkind.Fetched()} {
		if got := (&FetchResult{ContentType: ct}).Answer(); got != want {
			t.Errorf("an answer with the content type %q read as %v", ct, got)
		}
	}
}
