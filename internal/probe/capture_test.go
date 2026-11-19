package probe

import (
	"context"
	"sync"
	"testing"
	"time"
)

// stubCapturer remembers what it was asked.
type stubCapturer struct {
	mu        sync.Mutex
	asked     []CaptureRequest
	confirmed map[string]int
}

func (s *stubCapturer) Capture(rq CaptureRequest) {
	s.mu.Lock()
	s.asked = append(s.asked, rq)
	s.mu.Unlock()
}

func (s *stubCapturer) Confirm(_ int64, kinds []string) {
	s.mu.Lock()
	if s.confirmed == nil {
		s.confirmed = map[string]int{}
	}
	for _, k := range kinds {
		s.confirmed[k]++
	}
	s.mu.Unlock()
}

func TestSuspicionIsCaptured(t *testing.T) {
	f, s := lookFixture(t, LevelHeader)
	capt := &stubCapturer{}
	f.c.Capturer = capt
	// The stub shows one picture all along; that is not what is in question here.
	f.c.t.Picture = PictureStatic
	// An ordinary stream is not captured.
	watch(f, time.Minute, ProbPictureBlack)
	if len(capt.asked) != 0 {
		t.Fatalf("an ordinary stream was captured: %+v", capt.asked[0].Kinds)
	}

	// The first black picture is a suspicion and no finding: the segment
	// is handed over then, with the bytes that were read of it.
	s.frame = blackFrame
	f.clock = f.clock.Add(5 * time.Minute)
	r := f.step(1)
	if hasProblem(r, ProbPictureBlack) != nil || len(capt.asked) != 1 {
		t.Fatalf("at the first black picture: %d captures asked for, problems %+v", len(capt.asked), r.Problems)
	}
	rq := capt.asked[0]
	if rq.StreamID != 1 || len(rq.Kinds) != 1 || rq.Kinds[0] != ProbPictureBlack || rq.Manual || len(rq.Evidence) == 0 ||
		len(rq.Playlist) == 0 || len(rq.Window) < 2 || rq.Get == nil || rq.Status != 206 || rq.Server == "" {
		t.Fatalf("the request: kinds %v, %d bytes, %d segments, status %d, server %q", rq.Kinds, len(rq.Evidence), len(rq.Window), rq.Status, rq.Server)
	}
	found := false
	for _, w := range rq.Window {
		found = found || w.Seq == rq.Seq
	}
	if !found {
		t.Error("the segment in question is not among those of the playlist")
	}
	// What it is given to fetch with fetches the segments of the playlist.
	if resp, err := rq.Get(context.Background(), rq.Window[0].URL, 0, 0); err != nil || !resp.OK() || len(resp.Body) == 0 {
		t.Errorf("fetching a segment of the playlist: %v", err)
	}
	if capt.confirmed[ProbPictureBlack] != 0 {
		t.Fatal("confirmed before it was reported")
	}
	// Reported a minute later: confirmed, from then on.
	watch(f, 2*time.Minute, ProbPictureBlack)
	if capt.confirmed[ProbPictureBlack] == 0 {
		t.Error("the problem was reported and the capture not confirmed")
	}

	// Somebody asks for a capture: the next cycle hands a segment over.
	before := len(capt.asked)
	f.c.CaptureNow()
	f.step(1)
	if len(capt.asked) != before+1 || !capt.asked[before].Manual {
		t.Fatalf("a capture that was asked for: %d requests", len(capt.asked)-before)
	}
	f.step(1)
	if len(capt.asked) != before+1 {
		t.Error("asked for once and handed over twice")
	}
}
