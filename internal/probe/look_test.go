package probe

import (
	"context"
	"testing"
	"time"

	"github.com/hotid/hls-monitor/internal/hlstest"
	"github.com/hotid/hls-monitor/internal/media"
	"github.com/hotid/hls-monitor/internal/picture"
)

// stubLooker shows the checker whatever a test wants it to see.
type stubLooker struct {
	frame picture.Frame
	err   error
	busy  bool
	looks int
	bytes []int
	// heard is what Listen answers with, and listens how often it was asked.
	heard   *picture.Frame
	listens int
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

// Listen answers with the sound of the frame the stub is set to hear.
func (s *stubLooker) Listen(context.Context, []byte) (*picture.Frame, error) {
	s.listens++
	if s.heard == nil {
		return nil, picture.ErrNothing
	}
	f := *s.heard
	return &f, nil
}

// flat is a picture of one brightness all over, shrunk the way a look shrinks it.
func flat(v byte) []byte {
	c := make([]byte, picture.CellCols*picture.CellRows)
	for i := range c {
		c[i] = v
	}
	return c
}

var (
	liveFrame  = picture.Frame{HasPicture: true, Luma: 120, Dark: 0.1, Cells: flat(120), JPEG: []byte{0xFF, 0xD8}, HasAudio: true, AudioDB: -20}
	blackFrame = picture.Frame{HasPicture: true, Luma: 16, Dark: 1, Cells: flat(16), HasAudio: true, AudioDB: -20}
	quietFrame = picture.Frame{HasPicture: true, Luma: 120, Dark: 0.1, Cells: flat(120), HasAudio: true, AudioDB: picture.Silent}
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
	before := s.looks
	watch(f, 70*time.Second, ProbPictureBlack)
	// With a finding standing the stream is looked at once a minute, not
	// every twenty seconds for ever.
	if n := s.looks - before; n < 1 || n > 2 {
		t.Errorf("%d looks in seventy seconds with a finding standing", n)
	}
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
	watch(f, 70*time.Second, ProbPictureFrozen)
	// It looks much the same as a whole, and moves in one place: someone talking.
	var silent, frozen int
	for i := 0; i < 120; i++ {
		s.frame.Cells = flat(120)
		s.frame.Cells[200] = byte(60 + 10*(s.looks%5)) // never the 120 of the rest
		r := f.step(1)
		if hasProblem(r, ProbAudioSilent) != nil {
			silent++
		}
		if i > 45 && hasProblem(r, ProbPictureFrozen) != nil {
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

func TestDarkSceneIsNotABlackScreen(t *testing.T) {
	// A night scene, the credits: as dark as a black screen, look after
	// look, with something moving in it.
	f, s := lookFixture(t, LevelSegments)
	var frozen, black int
	for i := 0; i < 300; i++ {
		cells := flat(3)
		for j := 0; j < 40; j++ {
			cells[(s.looks*37+j)%len(cells)] = 30
		}
		s.frame = picture.Frame{HasPicture: true, Luma: 4, Dark: 0.99, Cells: cells, HasAudio: true, AudioDB: -20}
		r := f.step(1)
		if hasProblem(r, ProbPictureFrozen) != nil {
			frozen++
		}
		if hasProblem(r, ProbPictureBlack) != nil {
			black++
		}
	}
	if frozen != 0 || black != 0 || s.looks < 10 {
		t.Errorf("ten minutes of a dark scene, %d looks: frozen reported %d times, black %d times", s.looks, frozen, black)
	}
}

func TestDarkNoticeIsAPictureThatStays(t *testing.T) {
	// A dark slate with something blinking: all but black one look, not
	// quite the next, the same picture all along.
	f, s := lookFixture(t, LevelSegments)
	dark := picture.Frame{HasPicture: true, Luma: 8, Dark: 0.984, Cells: flat(8), HasAudio: true, AudioDB: -20}
	lighter := dark
	lighter.Dark, lighter.Luma = 0.975, 8.6
	var frozen, black int
	for i := 0; i < 300; i++ {
		s.frame = dark
		if s.looks%2 == 1 {
			s.frame = lighter
		}
		r := f.step(1)
		if hasProblem(r, ProbPictureFrozen) != nil {
			frozen++
		}
		if hasProblem(r, ProbPictureBlack) != nil {
			black++
		}
	}
	if frozen == 0 || black != 0 {
		t.Errorf("ten minutes of a dark notice: frozen reported %d times, black %d times", frozen, black)
	}
}

func TestKnownSlate(t *testing.T) {
	f, s := lookFixture(t, LevelSegments)
	notice := flat(70)
	notice[100], notice[101] = 200, 200 // something written on it
	f.c.Slates = func() []Slate { return []Slate{{ID: 7, Name: "technical break", Frames: [][]byte{flat(10), notice}}} }

	// An ordinary picture is not it.
	if n, _ := watch(f, time.Minute, ProbPictureSlate); n != 0 {
		t.Fatalf("an ordinary picture was taken for the slate %d times", n)
	}
	// The slate goes on air, coded anew, a little off the one that was
	// marked: it is known at the first look, with no minute to wait.
	shown := flat(74)
	shown[100], shown[101] = 196, 205
	s.frame = picture.Frame{HasPicture: true, Luma: 75, Dark: 0.1, Cells: shown, JPEG: []byte{0xFF, 0xD8}, HasAudio: true, AudioDB: -20}
	before := s.looks
	var first *Problem
	for i := 0; i < 200 && first == nil; i++ {
		first = hasProblem(f.step(1), ProbPictureSlate)
	}
	if first == nil || s.looks != before+1 {
		t.Fatalf("the slate was not known at the first look (%d looks): %+v", s.looks-before, first)
	}
	if first.Severity != SevCrit || first.Details["slate"] != "technical break" {
		t.Errorf("problem = %+v", first)
	}
	// It stands still, and is told once, as the slate it is.
	var slate, frozen, cycles int
	for end := f.clock.Add(5 * time.Minute); f.clock.Before(end); cycles++ {
		r := f.step(1)
		if hasProblem(r, ProbPictureSlate) != nil {
			slate++
		}
		if hasProblem(r, ProbPictureFrozen) != nil {
			frozen++
		}
	}
	if slate != cycles || frozen != 0 {
		t.Errorf("five minutes of a slate: told as the slate in %d of %d cycles, as frozen in %d", slate, cycles, frozen)
	}
}

func TestSlateWithSomethingBlinkingOnIt(t *testing.T) {
	// A test card: bright and dark in turns, something to tell it by.
	card := make([]byte, picture.CellCols*picture.CellRows)
	for i := range card {
		card[i] = byte(40 + 120*(i/8%2))
	}
	slates := []Slate{{ID: 1, Name: "card", Frames: [][]byte{card}}, {ID: 2, Name: "dark", Frames: [][]byte{flat(8)}}}

	// The same card with a clock in a corner showing another time.
	clock := append([]byte(nil), card...)
	for i := 0; i < 30; i++ {
		clock[i] = 250
	}
	if s := slateOf(slates, clock); s == nil || s.Name != "card" {
		t.Errorf("a card with another time on its clock: %+v", s)
	}
	// Half of it changed: another picture.
	other := append([]byte(nil), card...)
	for i := 0; i < len(other)/2; i++ {
		other[i] = 250
	}
	if s := slateOf(slates, other); s != nil {
		t.Errorf("a picture half unlike the card was taken for %q", s.Name)
	}
	// A dark slate is itself, and a dark scene with a lamp in it is not.
	if s := slateOf(slates, flat(10)); s == nil || s.Name != "dark" {
		t.Errorf("the dark slate coded anew: %+v", s)
	}
	scene := flat(9)
	for i := 100; i < 130; i++ {
		scene[i] = 90
	}
	if s := slateOf(slates, scene); s != nil {
		t.Errorf("a dark scene with a lamp in it was taken for %q", s.Name)
	}
}

func TestLookFetchesWholePackets(t *testing.T) {
	if lookBytes%188 != 0 || headerBytes%188 != 0 {
		t.Fatalf("lookBytes = %d, headerBytes = %d: not whole MPEG-TS packets", lookBytes, headerBytes)
	}
	// At the level that reads the beginning of segments, a cycle that
	// looks reads more of one of them, and finds nothing wrong for it.
	f, s := lookFixture(t, LevelHeader)
	for i := 0; i < 5; i++ {
		r := f.step(1)
		if p := hasProblem(r, ProbTSSync); p != nil {
			t.Fatalf("cycle %d: %+v", i, p)
		}
		wantClean(t, r)
	}
	if s.looks == 0 {
		t.Fatal("the stream was not looked at")
	}
}

func TestNothingCanBeDecoded(t *testing.T) {
	// A stream whose container is in order and whose contents are garbage:
	// the decoder makes nothing of it, look after look.
	f, s := lookFixture(t, LevelHeader)
	s.err = picture.ErrNothing
	if n, _ := watch(f, 50*time.Second, ProbMediaUndecodable); n != 0 {
		t.Fatalf("told after %d looks, in under a minute", s.looks)
	}
	// The segments of the fixture go round, which is found on the way
	// and brings one look forward: a minute of looks may end a look later.
	n, cycles := watch(f, 3*time.Minute, ProbMediaUndecodable)
	if n < cycles-15 || s.looks < 4 || s.looks > 9 {
		t.Fatalf("reported in %d of %d cycles, after %d looks", n, cycles, s.looks)
	}
	// What the container said of the video goes with it.
	if p := hasProblem(f.step(1), ProbMediaUndecodable); p == nil || p.Details["video_pictures"] == nil || p.Details["video_packets"] == nil || p.Details["video_kbit_s"] == nil {
		t.Errorf("what the container says is missing: %+v", p)
	}
	// It decodes again: over at the next look.
	s.err = nil
	if n, _ := watch(f, 3*time.Minute, ProbMediaUndecodable); n > 15 {
		t.Errorf("still reported in %d cycles after it decoded", n)
	}
	if hasProblem(f.step(1), ProbMediaUndecodable) != nil {
		t.Error("still reported")
	}

	// One segment that would not decode among good ones is nothing.
	f, s = lookFixture(t, LevelHeader)
	f.step(1)
	s.err = picture.ErrNothing
	f.clock = f.clock.Add(5 * time.Minute)
	f.step(1)
	s.err = nil
	if n, _ := watch(f, 6*time.Minute, ProbMediaUndecodable); n != 0 {
		t.Errorf("one bad look was reported in %d cycles", n)
	}
}

func TestOneChannelOfTheSoundIsSilent(t *testing.T) {
	f, s := lookFixture(t, LevelSegments)
	s.frame = liveFrame
	s.frame.ChannelDB = []float64{-20, picture.Silent}
	if n, _ := watch(f, 50*time.Second, ProbAudioChannelSilent); n != 0 {
		t.Fatalf("told after %d looks, in under a minute", s.looks)
	}
	watch(f, time.Minute, ProbAudioChannelSilent)
	p := hasProblem(f.step(1), ProbAudioChannelSilent)
	if p == nil || p.Severity != SevWarn || p.Details["silent_channel"] != "right" || p.Details["left_level_db"] != -20.0 {
		t.Fatalf("problem = %+v", p)
	}
	// Both quiet is a pause, not a channel that is missing; and channels
	// behind the first two may be silent as they like.
	s.frame.ChannelDB = []float64{-60, picture.Silent}
	watch(f, 2*time.Minute, ProbAudioChannelSilent)
	if hasProblem(f.step(1), ProbAudioChannelSilent) != nil {
		t.Error("a pause was told as a silent channel")
	}
	s.frame.ChannelDB = []float64{-20, -22, -25, picture.Silent, picture.Silent, picture.Silent}
	if n, _ := watch(f, 3*time.Minute, ProbAudioChannelSilent); n != 0 {
		t.Errorf("silent channels behind the first two were told %d times", n)
	}
}

func TestSoundFarTooQuiet(t *testing.T) {
	f, s := lookFixture(t, LevelSegments)
	s.frame = liveFrame
	s.frame.AudioDB = -52
	// A quiet hour is a quiet film.
	for end := f.clock.Add(time.Hour); f.clock.Before(end); {
		if hasProblem(f.step(1), ProbAudioQuiet) != nil {
			t.Fatal("an hour of quiet sound was told")
		}
	}
	// One loud look and the count begins anew.
	s.frame.AudioDB = -20
	watch(f, 6*time.Minute, ProbAudioQuiet)
	s.frame.AudioDB = -52
	if n, _ := watch(f, 90*time.Minute, ProbAudioQuiet); n != 0 {
		t.Fatalf("told %d times within ninety minutes of a loud look", n)
	}
	watch(f, 40*time.Minute, ProbAudioQuiet)
	p := hasProblem(f.step(1), ProbAudioQuiet)
	if p == nil || p.Severity != SevWarn || p.Details["audio_level_db"] != -52.0 {
		t.Fatalf("problem = %+v", p)
	}
	// Silence is told as silence, not as sound too quiet.
	s.frame.AudioDB = picture.Silent
	watch(f, 3*time.Minute, ProbAudioSilent)
	r := f.step(1)
	if hasProblem(r, ProbAudioSilent) == nil || hasProblem(r, ProbAudioQuiet) != nil {
		t.Errorf("silence: %+v", r.Problems)
	}
}

func TestSoundThatDoesNotDecode(t *testing.T) {
	// The container has sound, the decoder shows the picture and makes
	// nothing of the sound.
	f, s := lookFixture(t, LevelContainer)
	s.frame = liveFrame
	s.frame.HasAudio, s.frame.AudioError = false, "channel element 1.0 is not allocated"
	if n, _ := watch(f, 50*time.Second, ProbAudioUndecodable); n != 0 {
		t.Fatalf("told after %d looks, in under a minute", s.looks)
	}
	watch(f, time.Minute, ProbAudioUndecodable)
	p := hasProblem(f.step(1), ProbAudioUndecodable)
	if p == nil || p.Severity != SevCrit || p.Details["decoder_says"] != "channel element 1.0 is not allocated" {
		t.Fatalf("problem = %+v", p)
	}
	s.frame = liveFrame
	watch(f, 2*time.Minute, ProbAudioUndecodable)
	if hasProblem(f.step(1), ProbAudioUndecodable) != nil {
		t.Error("still told after the sound was heard")
	}
}

func TestSoundInAPlaylistOfItsOwn(t *testing.T) {
	// The segments of the picture have no sound in them: it is in a
	// playlist of its own, and is listened to there after every look.
	f := newFixture(t, hlstest.Options{SoundApart: true}, "/master.m3u8", LevelContainer)
	s := &stubLooker{frame: liveFrame}
	s.frame.HasAudio, s.frame.NoSound = false, true
	s.heard = &picture.Frame{HasAudio: true, AudioDB: -20, ChannelDB: []float64{-20, -21}}
	f.c.Looker, f.c.PictureEvery = s, 5*time.Minute
	var shown *Picture
	for i := 0; i < 5; i++ {
		if r := f.step(1); r.Picture != nil {
			shown = r.Picture
		}
	}
	if s.looks != 1 || s.listens != 1 || shown == nil || !shown.HasPicture || !shown.HasAudio || shown.AudioDB != -20 || len(shown.JPEG) == 0 {
		t.Fatalf("%d looks, %d listens, shown %+v", s.looks, s.listens, shown)
	}
	// Silence there is silence of the stream, and is listened to again soon.
	s.heard = &picture.Frame{HasAudio: true, AudioDB: picture.Silent, ChannelDB: []float64{picture.Silent, picture.Silent}}
	watch(f, 7*time.Minute, ProbAudioSilent)
	if p := hasProblem(f.step(1), ProbAudioSilent); p == nil || s.listens < 4 {
		t.Fatalf("silence in the playlist of sound: %+v after %d listens", p, s.listens)
	}
	// It stands through the looks at the picture, which has nothing to say of it.
	if n, cycles := watch(f, 3*time.Minute, ProbAudioSilent); n != cycles {
		t.Errorf("silence was told in %d of %d cycles", n, cycles)
	}
	// Sound nothing can be made of is sound that does not decode.
	s.heard = nil
	watch(f, 3*time.Minute, ProbAudioUndecodable)
	r := f.step(1)
	if hasProblem(r, ProbAudioUndecodable) == nil || hasProblem(r, ProbAudioSilent) != nil {
		t.Fatalf("sound that does not decode: %+v", r.Problems)
	}
	// And it is over when the sound is back.
	s.heard = &picture.Frame{HasAudio: true, AudioDB: -20, ChannelDB: []float64{-20, -21}}
	watch(f, 3*time.Minute, ProbAudioUndecodable)
	if r := f.step(1); hasProblem(r, ProbAudioUndecodable) != nil || hasProblem(r, ProbAudioSilent) != nil {
		t.Errorf("still told: %+v", r.Problems)
	}
}

func TestMoreIsFetchedWhereTheSoundComesLate(t *testing.T) {
	// A stream of many megabits: the beginning of a segment has the
	// picture and next to none of the sound, by its container. From then
	// on more of a segment is fetched for a look.
	c := New(Target{ID: 1, URL: "http://x/", Level: LevelHeader}, nil)
	s := &stubLooker{frame: liveFrame}
	s.frame.HasAudio = false
	c.Looker, c.PictureEvery = s, 5*time.Minute
	if c.lookSize() != lookBytes {
		t.Fatalf("a look fetches %d bytes to begin with", c.lookSize())
	}
	few := &media.Info{Tracks: []media.Track{{Kind: media.KindVideo, Frames: 12}, {Kind: media.KindAudio, Frames: 3}}}
	c.look(context.Background(), &cycle{res: &Result{}}, Variant{Key: "v", Kind: KindVideo}, []byte{0x47}, nil, few, time.Now())
	if c.lookSize() != lookBytesMore || lookBytesMore%188 != 0 {
		t.Fatalf("after a look with a picture and three frames of sound a look fetches %d bytes", c.lookSize())
	}
	// And the sound is not said not to decode: there was none to speak of.
	if len(c.looks.soundProblems) != 0 || c.looks.deaf.n != 0 {
		t.Errorf("told of the sound: %+v", c.looks.soundProblems)
	}
}

func TestStreamWithNoSound(t *testing.T) {
	// The picture and no sound, and no playlist of sound either: told for
	// information, after a few looks.
	f := newFixture(t, hlstest.Options{}, "/master.m3u8", LevelContainer)
	s := &stubLooker{frame: liveFrame}
	s.frame.HasAudio, s.frame.NoSound = false, true
	f.c.Looker, f.c.PictureEvery = s, 5*time.Minute
	// One look says nothing yet: the sound may be listened to next.
	if r := f.step(1); hasProblem(r, ProbAudioAbsent) != nil || s.looks != 1 {
		t.Fatalf("told after %d looks", s.looks)
	}
	watch(f, 6*time.Minute, ProbAudioAbsent)
	p := hasProblem(f.step(1), ProbAudioAbsent)
	if p == nil || p.Severity != SevInfo || s.listens != 0 {
		t.Fatalf("problem = %+v, %d listens", p, s.listens)
	}
	// Sound that came with the picture ends it.
	s.frame = liveFrame
	watch(f, 6*time.Minute, ProbAudioAbsent)
	if hasProblem(f.step(1), ProbAudioAbsent) != nil {
		t.Error("still told after sound was heard")
	}

	// A stream that keeps its sound in a playlist of its own has sound.
	f = newFixture(t, hlstest.Options{SoundApart: true}, "/master.m3u8", LevelContainer)
	s = &stubLooker{frame: liveFrame, heard: &picture.Frame{HasAudio: true, AudioDB: -20}}
	s.frame.HasAudio, s.frame.NoSound = false, true
	f.c.Looker, f.c.PictureEvery = s, 5*time.Minute
	if n, _ := watch(f, 20*time.Minute, ProbAudioAbsent); n != 0 || s.listens < 3 {
		t.Errorf("sound in a playlist of its own: told as missing %d times, %d listens", n, s.listens)
	}
}
