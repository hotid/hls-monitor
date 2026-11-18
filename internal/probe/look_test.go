package probe

import (
	"context"
	"testing"
	"time"

	"github.com/hotid/hls-monitor/internal/hlstest"
	"github.com/hotid/hls-monitor/internal/picture"
)

// stubLooker shows the checker whatever a test wants it to see.
type stubLooker struct {
	frame picture.Frame
	err   error
	busy  bool
	looks int
	bytes []int
}

func (s *stubLooker) Sample(_ context.Context, data []byte) (*picture.Frame, error) {
	s.looks++
	s.bytes = append(s.bytes, len(data))
	if s.err != nil {
		return nil, s.err
	}
	f := s.frame
	return &f, nil
}
func (s *stubLooker) Busy() bool { return s.busy }

var (
	liveFrame  = picture.Frame{HasPicture: true, Luma: 120, Dark: 0.1, Hash: 0xF0F0, JPEG: []byte{0xFF, 0xD8}, HasAudio: true, AudioDB: -20}
	blackFrame = picture.Frame{HasPicture: true, Luma: 16, Dark: 1, HasAudio: true, AudioDB: -20}
	quietFrame = picture.Frame{HasPicture: true, Luma: 120, Dark: 0.1, Hash: 0x0FF0, HasAudio: true, AudioDB: picture.Silent}
)

func lookFixture(t *testing.T, level Level) (*fixture, *stubLooker) {
	f := newFixture(t, hlstest.Options{}, "/v0/index.m3u8", level)
	s := &stubLooker{frame: liveFrame}
	f.c.Looker, f.c.PictureEvery = s, 5*time.Minute
	return f, s
}

// watch runs cycles for a while and tells in which of them the problem was reported.
func watch(f *fixture, d time.Duration, kind string) (reported, cycles int) {
	for end := f.clock.Add(d); f.clock.Before(end); {
		cycles++
		if hasProblem(f.step(1), kind) != nil {
			reported++
		}
	}
	return reported, cycles
}

func TestBlackPicture(t *testing.T) {
	f, s := lookFixture(t, LevelSegments)
	first := f.step(1)
	if first.Picture == nil || !first.Picture.HasPicture || first.Picture.Luma != 120 || len(first.Picture.JPEG) == 0 {
		t.Fatalf("the first cycle looks at the stream: %+v", first.Picture)
	}
	if v, ok := metric(first, "hls_picture_luma", VariantMedia); !ok || v != 120 {
		t.Errorf("hls_picture_luma = %v, %v", v, ok)
	}
	// Nothing wrong: one look in five minutes, and no more.
	if n, _ := watch(f, 4*time.Minute, ProbPictureBlack); n != 0 || s.looks != 1 {
		t.Fatalf("an ordinary stream: reported %d times, looked %d times", n, s.looks)
	}

	// The screen goes black. One black picture is a dark scene; the
	// checker looks again soon, and a minute of it is a finding.
	s.frame = blackFrame
	if n, _ := watch(f, 100*time.Second, ProbPictureBlack); n != 0 {
		t.Fatalf("reported after %d looks, before a minute of black had passed", s.looks)
	}
	if s.looks < 3 {
		t.Fatalf("a black picture was looked at again %d times in a hundred seconds", s.looks-1)
	}
	n, cycles := watch(f, 2*time.Minute, ProbPictureBlack)
	if n < cycles-15 {
		t.Fatalf("black for minutes: reported in %d of %d cycles", n, cycles)
	}
	// It stands between looks: an incident ends when its problem is not reported.
	if n, cycles := watch(f, 30*time.Second, ProbPictureBlack); n != cycles {
		t.Errorf("between looks the problem was reported in %d of %d cycles", n, cycles)
	}
	p := hasProblem(f.step(1), ProbPictureBlack)
	if p == nil || p.Severity != SevCrit || p.Details["picture_luma"] != 16.0 {
		t.Fatalf("problem = %+v", p)
	}

	// The picture is back: the next look ends it.
	s.frame = liveFrame
	watch(f, 30*time.Second, ProbPictureBlack)
	if n, _ := watch(f, time.Minute, ProbPictureBlack); n != 0 {
		t.Errorf("still reported in %d cycles after the picture came back", n)
	}
}

func TestFrozenPictureAndSilence(t *testing.T) {
	f, s := lookFixture(t, LevelSegments)
	// The same picture look after look, and it has sound: frozen. That
	// it does not change shows at the second look, a period after the
	// first; from then on the checker looks again soon.
	if n, _ := watch(f, 4*time.Minute, ProbPictureFrozen); n != 0 || s.looks != 1 {
		t.Fatalf("before the second look: reported %d times after %d looks", n, s.looks)
	}
	if n, _ := watch(f, 4*time.Minute, ProbPictureFrozen); n == 0 {
		t.Fatalf("the same picture for eight minutes (%d looks) was not reported", s.looks)
	}
	if hasProblem(f.step(1), ProbAudioSilent) != nil {
		t.Error("a tone was reported as silence")
	}
	// The picture moves again, in silence.
	s.frame = quietFrame
	watch(f, 30*time.Second, ProbPictureFrozen)
	hashes := []uint64{0x1111, 0x2222, 0x4444, 0x8888, 0xAAAA, 0x5555, 0x3333, 0xCCCC}
	var silent, frozen int
	for i := 0; i < 120; i++ {
		s.frame.Hash = hashes[s.looks%len(hashes)] << 16
		r := f.step(1)
		if hasProblem(r, ProbAudioSilent) != nil {
			silent++
		}
		if i > 20 && hasProblem(r, ProbPictureFrozen) != nil {
			frozen++
		}
	}
	if silent == 0 || frozen != 0 {
		t.Errorf("a moving picture in silence for four minutes: silence reported %d times, frozen %d times", silent, frozen)
	}
}

func TestStaticPictureIsWhatSomeStreamsShow(t *testing.T) {
	f, s := lookFixture(t, LevelSegments)
	f.c.t.Picture = PictureStatic
	if n, _ := watch(f, 4*time.Minute, ProbPictureFrozen); n != 0 {
		t.Errorf("a stream said to show a still picture was reported frozen %d times", n)
	}
	// Black is still black.
	s.frame = blackFrame
	if n, _ := watch(f, 4*time.Minute, ProbPictureBlack); n == 0 {
		t.Error("a black screen was not reported on a stream with a still picture")
	}

	off, so := lookFixture(t, LevelSegments)
	off.c.t.Picture = PictureOff
	watch(off, time.Minute, ProbPictureBlack)
	if so.looks != 0 {
		t.Errorf("a stream not to be looked at was looked at %d times", so.looks)
	}
}

func TestLookIsPutOff(t *testing.T) {
	// A decoder with a queue, or a monitor that cannot keep up, waits.
	f, s := lookFixture(t, LevelSegments)
	s.busy = true
	watch(f, time.Minute, ProbPictureBlack)
	if s.looks != 0 {
		t.Fatalf("looked %d times while the decoder was busy", s.looks)
	}
	s.busy = false
	f.c.Overloaded = func(time.Time) string { return "processor" }
	watch(f, time.Minute, ProbPictureBlack)
	if s.looks != 0 {
		t.Fatalf("looked %d times while overloaded", s.looks)
	}
	f.c.Overloaded = nil
	if f.step(1).Picture == nil {
		t.Error("no look once there was time for it")
	}

	// Where only playlists are checked there is nothing to look at.
	p, sp := lookFixture(t, LevelPlaylist)
	watch(p, time.Minute, ProbPictureBlack)
	if sp.looks != 0 {
		t.Errorf("looked %d times at a stream whose segments are not fetched", sp.looks)
	}
}
