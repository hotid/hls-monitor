package media

import (
	"bytes"
	"math"
	"path"
	"testing"

	"github.com/hotid/hls-monitor/internal/hlstest"
	"github.com/hotid/hls-monitor/internal/types/codec"
	"github.com/hotid/hls-monitor/internal/types/container"
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
	if v.Codec.String() != wantVideoCodec || a.Codec.String() != wantAudioCodec {
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
	if info.Container != container.TS() {
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
	if info.Container != container.FMP4() {
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
		if name != "truncated" && info.Container != container.Unknown() {
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

	scan := func(data []byte) pictureScan {
		var v videoScanner
		return v.scan(codec.H264(), data, false)
	}
	if sc := scan(join(aud, sei, slice)); sc.keys != 1 || !sc.firstKey {
		t.Errorf("slice after a recovery point: %+v; want a keyframe", sc)
	}
	if sc := scan(join(aud, other, slice)); sc.keys != 0 || sc.halves != 2 {
		t.Errorf("slice after another message: %+v; want one picture, no keyframe", sc)
	}
	if sc := scan(join(aud, slice, sei)); sc.firstKey || sc.keys != 0 {
		t.Errorf("a message after the slice belongs to the next picture: %+v", sc)
	}
	if sc := scan(join(aud, idr)); sc.keys != 1 {
		t.Errorf("IDR picture: %+v, want a keyframe", sc)
	}
	// Two pictures in one packet, the second one the keyframe.
	if sc := scan(join(aud, slice, aud, idr)); sc.halves != 4 || sc.keys != 1 || sc.firstKey {
		t.Errorf("slice and IDR in one packet: %+v; want two frames, one keyframe, not the first", sc)
	}
}

// tsPacket builds one transport packet with a payload on the given PID.
func tsPacket(pid uint16, cc uint8, fill byte) []byte {
	p := make([]byte, tsPacketSize)
	p[0] = 0x47
	p[1] = byte(pid >> 8 & 0x1f)
	p[2] = byte(pid)
	p[3] = 0x10 | cc&0x0f // payload only
	for i := 4; i < len(p); i++ {
		p[i] = fill
	}
	return p
}

func scanPackets(packets ...[]byte) *Info {
	info := &Info{}
	scanTSPackets(bytes.Join(packets, nil), info)
	return info
}

// A packet may be repeated once, unchanged. A repeated counter with other
// content, or a third copy, is a break.
func TestTSRepeatedPackets(t *testing.T) {
	a, b := tsPacket(256, 3, 0xaa), tsPacket(256, 4, 0xbb)
	if n := scanPackets(a, a, b).CCErrors; n != 0 {
		t.Errorf("one unchanged repeat: %d continuity errors, want 0", n)
	}
	if n := scanPackets(a, tsPacket(256, 3, 0xcc), b).CCErrors; n != 1 {
		t.Errorf("same counter, different content: %d continuity errors, want 1", n)
	}
	if n := scanPackets(a, a, a, b).CCErrors; n != 1 {
		t.Errorf("a packet sent three times: %d continuity errors, want 1", n)
	}
	if n := scanPackets(a, tsPacket(256, 6, 0xbb)).CCErrors; n != 1 {
		t.Errorf("a skipped counter: %d continuity errors, want 1", n)
	}
}

// The counters a segment begins and ends with are kept per PID.
func TestTSEdgeCounters(t *testing.T) {
	info := scanPackets(tsPacket(256, 14, 1), tsPacket(257, 2, 1), tsPacket(256, 15, 2), tsPacket(256, 0, 3))
	if info.FirstCC[256] != 14 || info.LastCC[256] != 0 || info.FirstCC[257] != 2 || info.LastCC[257] != 2 || info.CCErrors != 0 {
		t.Errorf("first %v, last %v, errors %d", info.FirstCC, info.LastCC, info.CCErrors)
	}
}

func TestCountPictures(t *testing.T) {
	nal := func(header ...byte) []byte { return append([]byte{0, 0, 1}, header...) }
	join := func(parts ...[]byte) []byte { return bytes.Join(parts, []byte{0x55, 0x55}) }
	aud, sps, slice, more := nal(0x09, 0xf0), nal(0x67, 0x42), nal(0x41, 0x9a), nal(0x41, 0x1a)
	for name, c := range map[string]struct {
		data []byte
		want int
	}{
		"one picture with a delimiter":      {join(aud, sps, slice), 1},
		"two pictures, each with delimiter": {join(aud, slice, aud, slice), 2},
		"no delimiters, two first slices":   {join(slice, slice), 2},
		"one picture of two slices":         {join(aud, slice, more), 1},
		"a delimiter without a picture":     {join(aud), 0},
	} {
		var v videoScanner
		if got := v.scan(codec.H264(), c.data, false).halves / 2; got != c.want {
			t.Errorf("%s: %d, want %d", name, got, c.want)
		}
	}
	// HEVC: a slice starts a picture when the first bit after the two header bytes is set; types 16 to 23 are keyframes.
	var v videoScanner
	sc := v.scan(codec.HEVC(), join(nal(35<<1, 1), nal(19<<1, 1, 0x80), nal(19<<1, 1, 0x00), nal(35<<1, 1), nal(1<<1, 1, 0x80)), false)
	if sc.halves != 4 || sc.keys != 1 || !sc.firstKey {
		t.Errorf("hevc: %+v, want two frames, the first a keyframe", sc)
	}
}

// Interlaced H.264 coded as fields: two field pictures make one frame, and
// the two fields of a keyframe one keyframe.
func TestH264FieldPictures(t *testing.T) {
	// Slice header: first_mb_in_slice=0 "1", slice_type=0 "1", pps=0 "1",
	// frame_num (4 bits) "0000", field_pic_flag, bottom_field_flag.
	field := func(typ byte, bottom bool) []byte {
		b := byte(0b11100001) // ..., field_pic_flag=1
		next := byte(0)
		if bottom {
			next = 0x80
		}
		return []byte{0, 0, 1, typ, b, next, 0xff}
	}
	frame := []byte{0, 0, 1, 0x41, 0b11100000, 0, 0xff}
	v := videoScanner{fieldsPossible: true, frameNumBits: 4}
	sc := v.scan(codec.H264(), bytes.Join([][]byte{field(0x65, false), field(0x65, true), field(0x41, false), field(0x41, true), frame}, nil), false)
	if sc.halves != 6 || sc.keys != 1 || !sc.firstKey {
		t.Errorf("two field pairs and a frame: %+v; want 6 half frames and 1 keyframe", sc)
	}
	// A field pair split between two packets is still one keyframe.
	v = videoScanner{fieldsPossible: true, frameNumBits: 4}
	a, b := v.scan(codec.H264(), field(0x65, false), false), v.scan(codec.H264(), field(0x65, true), false)
	if a.keys+b.keys != 1 || a.halves+b.halves != 2 {
		t.Errorf("a keyframe in two packets: %+v and %+v", a, b)
	}
}

// MPEG-2: picture headers, intra pictures as keyframes, field pictures.
func TestMPEG2Pictures(t *testing.T) {
	seq := []byte{0, 0, 1, 0xb3, 0x2d, 0x02, 0x40, 0x33} // 720x576
	pic := func(codingType byte) []byte { return []byte{0, 0, 1, 0x00, 0x00, codingType << 3, 0xff, 0xf8} }
	ext := func(structure byte) []byte { return []byte{0, 0, 1, 0xb5, 0x8f, 0xff, 0xf0 | structure, 0x80} }
	slice := []byte{0, 0, 1, 0x01, 0x12, 0x34}
	var v videoScanner
	sc := v.scan(codec.MPEG2Video(), bytes.Join([][]byte{seq, pic(1), ext(3), slice, slice, pic(2), ext(3), slice, pic(3), ext(3), slice}, nil), false)
	if sc.halves != 6 || sc.keys != 1 || !sc.firstKey || sc.width != 720 || sc.height != 576 {
		t.Errorf("I, P and B frames: %+v; want 3 frames, 1 keyframe, 720x576", sc)
	}
	v = videoScanner{}
	sc = v.scan(codec.MPEG2Video(), bytes.Join([][]byte{pic(1), ext(1), slice, pic(2), ext(2), slice}, nil), false)
	if sc.halves != 2 || sc.keys != 1 {
		t.Errorf("an I field and a P field: %+v; want one frame, one keyframe", sc)
	}
}

// The frame rate is not thrown off by dropped frames or by packets
// without a picture.
func TestVideoFrameDuration(t *testing.T) {
	var timed []pesTime
	for i := range 50 {
		if i >= 20 && i < 28 {
			continue // the encoder dropped eight frames
		}
		timed = append(timed, pesTime{int64(i) * 3600, 2})
	}
	timed = append(timed, pesTime{50 * 3600, 0}) // a delimiter alone
	if dur, last := videoFrameDuration(timed); dur != 3600 || last != 0 {
		t.Errorf("frame duration %v, last %d; want 3600 (25 fps), 0", dur, last)
	}
	// Two fields per packet at 25 frames per second, and one field per packet.
	if dur, _ := videoFrameDuration([]pesTime{{0, 2}, {3600, 2}, {7200, 2}}); dur != 3600 {
		t.Errorf("field pairs: %v, want 3600", dur)
	}
	if dur, _ := videoFrameDuration([]pesTime{{0, 1}, {1800, 1}, {3600, 1}}); dur != 3600 {
		t.Errorf("single fields: %v, want 3600", dur)
	}
	// The fields of a frame in two packets with the same timestamp.
	if dur, last := videoFrameDuration([]pesTime{{0, 1}, {0, 1}, {3600, 1}, {3600, 1}, {7200, 1}, {7200, 1}}); dur != 3600 || last != 2 {
		t.Errorf("fields sharing a timestamp: %v, last %d; want 3600, 2", dur, last)
	}
}
