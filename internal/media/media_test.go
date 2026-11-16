package media

import (
	"math"
	"path"
	"testing"

	"github.com/hotid/hls-monitor/internal/hlstest"
)

func read(t *testing.T, parts ...string) []byte {
	t.Helper()
	b, err := hlstest.Assets.ReadFile(path.Join(append([]string{"assets"}, parts...)...))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func near(a, b, eps float64) bool { return math.Abs(a-b) <= eps }

func checkTracks(t *testing.T, info *Info, wantVideoCodec, wantAudioCodec string) {
	t.Helper()
	if info.Error != "" {
		t.Fatalf("unexpected parse error: %s", info.Error)
	}
	v, a := info.Video(), info.Audio()
	if v == nil || a == nil {
		t.Fatalf("want video and audio tracks, got %+v", info.Tracks)
	}
	if v.Codec != wantVideoCodec || a.Codec != wantAudioCodec {
		t.Errorf("codecs = %s/%s, want %s/%s", v.Codec, a.Codec, wantVideoCodec, wantAudioCodec)
	}
	if v.Width != 160 || v.Height != 90 {
		t.Errorf("resolution = %dx%d, want 160x90", v.Width, v.Height)
	}
	if v.Profile != "High" {
		t.Errorf("profile = %q, want High", v.Profile)
	}
	if !v.HasTiming || !near(v.Duration(), 2.0, 0.05) {
		t.Errorf("video duration = %.3f (timing %v), want ~2.0", v.Duration(), v.HasTiming)
	}
	if !near(v.FPS, 25, 0.5) {
		t.Errorf("fps = %.3f, want ~25", v.FPS)
	}
	if v.Frames != 50 {
		t.Errorf("video frames = %d, want 50", v.Frames)
	}
	if !v.FirstKeyframe || v.Keyframes != 1 {
		t.Errorf("keyframes = %d first=%v, want 1/true", v.Keyframes, v.FirstKeyframe)
	}
	if !near(v.MaxGOP, 2.0, 0.05) {
		t.Errorf("max GOP = %.3f, want ~2.0", v.MaxGOP)
	}
	if a.SampleRate != 44100 || a.Channels != 2 {
		t.Errorf("audio = %d Hz / %d ch, want 44100/2", a.SampleRate, a.Channels)
	}
	if !a.HasTiming || !near(a.Duration(), 2.0, 0.15) {
		t.Errorf("audio duration = %.3f, want ~2.0", a.Duration())
	}
	if v.Bitrate() <= 0 || a.Bitrate() <= 0 {
		t.Errorf("bitrates must be positive: %v / %v", v.Bitrate(), a.Bitrate())
	}
}

func TestAnalyzeTS(t *testing.T) {
	info := Analyze(read(t, "ts", "seg1.ts"), nil)
	if info.Container != ContainerTS {
		t.Fatalf("container = %s", info.Container)
	}
	if !info.HasPAT || !info.HasPMT {
		t.Errorf("PAT/PMT = %v/%v, want both", info.HasPAT, info.HasPMT)
	}
	if info.SyncErrors != 0 || info.CCErrors != 0 || info.TransportErrors != 0 {
		t.Errorf("clean segment reported errors: sync=%d cc=%d tei=%d", info.SyncErrors, info.CCErrors, info.TransportErrors)
	}
	checkTracks(t, info, "h264", "aac")
}

func TestTSSegmentsAreContiguous(t *testing.T) {
	a := Analyze(read(t, "ts", "seg0.ts"), nil)
	b := Analyze(read(t, "ts", "seg1.ts"), nil)
	if gap := b.Video().StartPTS - a.Video().EndPTS; !near(gap, 0, 0.001) {
		t.Errorf("video gap between consecutive segments = %.4f, want 0", gap)
	}
}

func TestTSContinuityErrors(t *testing.T) {
	data := read(t, "ts", "seg1.ts")
	// Drop three packets from the middle: every PID that had payload there
	// skips a counter value.
	cut := 188 * 40
	broken := append(append([]byte{}, data[:cut]...), data[cut+188*3:]...)
	info := Analyze(broken, nil)
	if info.CCErrors == 0 {
		t.Error("dropped packets were not detected as continuity errors")
	}
	if info.SyncErrors != 0 {
		t.Errorf("sync errors = %d, want 0 for whole-packet loss", info.SyncErrors)
	}
}

func TestTSSyncErrors(t *testing.T) {
	data := read(t, "ts", "seg1.ts")
	// Remove 10 bytes: packet framing is lost until resync.
	cut := 188*40 + 20
	broken := append(append([]byte{}, data[:cut]...), data[cut+10:]...)
	info := Analyze(broken, nil)
	if info.SyncErrors == 0 {
		t.Error("lost framing was not detected as a sync error")
	}
	if info.Video() == nil {
		t.Error("parser should recover and still find the video track")
	}
}

func TestAnalyzeFMP4(t *testing.T) {
	init, err := ParseInit(read(t, "fmp4", "init.mp4"))
	if err != nil {
		t.Fatal(err)
	}
	info := Analyze(read(t, "fmp4", "seg1.m4s"), init)
	if info.Container != ContainerFMP4 {
		t.Fatalf("container = %s", info.Container)
	}
	checkTracks(t, info, "avc1", "mp4a")
}

func TestFMP4WithoutInit(t *testing.T) {
	info := Analyze(read(t, "fmp4", "seg1.m4s"), nil)
	if info.Error != "" {
		t.Fatalf("unexpected error: %s", info.Error)
	}
	if len(info.Tracks) != 2 {
		t.Fatalf("tracks = %d, want 2", len(info.Tracks))
	}
	for _, tr := range info.Tracks {
		if tr.HasTiming {
			t.Errorf("track %d has timing without a timescale", tr.ID)
		}
	}
}

func TestGarbage(t *testing.T) {
	for name, data := range map[string][]byte{
		"empty":     nil,
		"text":      []byte("<html>404 not found</html>"),
		"truncated": read(t, "fmp4", "seg1.m4s")[:300],
	} {
		info := Analyze(data, nil)
		if name != "truncated" && info.Container != ContainerUnknown {
			t.Errorf("%s: container = %s, want unknown", name, info.Container)
		}
		if name == "truncated" && info.Error == "" {
			t.Errorf("truncated fmp4 must report an error")
		}
	}
}

// Only the beginning of a segment: the last frame is cut in the middle,
// yet what the first packets say must still be read.
func TestPartialSegments(t *testing.T) {
	ts := Analyze(read(t, "ts", "seg1.ts")[:188*40], nil)
	v := ts.Video()
	if v == nil || !v.HasTiming || !v.FirstKeyframe || v.Width != 160 || v.Profile != "High" {
		t.Fatalf("partial TS: %+v (error %q)", ts.Tracks, ts.Error)
	}
	if ts.SyncErrors != 0 || ts.CCErrors != 0 || !ts.HasPAT || !ts.HasPMT {
		t.Errorf("a clean cut on a packet boundary reported errors: %+v", ts)
	}
	full := Analyze(read(t, "ts", "seg1.ts"), nil)
	if v.StartPTS != full.Video().StartPTS {
		t.Errorf("start PTS from the beginning = %v, from the whole segment = %v", v.StartPTS, full.Video().StartPTS)
	}

	init, err := ParseInit(read(t, "fmp4", "init.mp4"))
	if err != nil {
		t.Fatal(err)
	}
	// The movie fragment header fits; the media data is cut.
	mp := Analyze(read(t, "fmp4", "seg1.m4s")[:4000], init)
	if mv := mp.Video(); mv == nil || !mv.HasTiming || !mv.FirstKeyframe {
		t.Fatalf("partial fMP4: %+v (error %q)", mp.Tracks, mp.Error)
	}
}

// Broadcast encoders may never send an IDR picture: a segment then starts
// with an ordinary slice announced by a recovery point message.
func TestRecoveryPointIsKeyframe(t *testing.T) {
	aud := []byte{0, 0, 0, 1, 0x09, 0xf0}
	// SEI: buffering period (type 0, 1 byte), then recovery point (type 6, 1 byte).
	sei := []byte{0, 0, 0, 1, 0x06, 0x00, 0x01, 0x80, 0x06, 0x01, 0x84, 0x80}
	other := []byte{0, 0, 0, 1, 0x06, 0x01, 0x01, 0x10, 0x80}
	slice := []byte{0, 0, 0, 1, 0x21, 0x88, 0x84}
	idr := []byte{0, 0, 0, 1, 0x65, 0x88, 0x84}
	join := func(parts ...[]byte) []byte {
		var out []byte
		for _, p := range parts {
			out = append(out, p...)
		}
		return out
	}

	if _, vcl, rec := scanNALs(join(aud, sei, slice), false, false); vcl != 1 || !rec {
		t.Errorf("slice after a recovery point: type %d, recovery %v; want 1, true", vcl, rec)
	}
	if _, vcl, rec := scanNALs(join(aud, other, slice), false, false); vcl != 1 || rec {
		t.Errorf("slice after another message: type %d, recovery %v; want 1, false", vcl, rec)
	}
	if _, vcl, rec := scanNALs(join(aud, slice, sei), false, false); rec {
		t.Errorf("a message after the slice belongs to the next picture (type %d)", vcl)
	}
	if _, vcl, _ := scanNALs(join(aud, idr), false, false); vcl != 5 {
		t.Errorf("IDR type = %d, want 5", vcl)
	}
}
