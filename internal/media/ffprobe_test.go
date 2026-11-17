package media

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// TestAgainstFFprobe compares what the analyzer says about real segments
// with what ffprobe says. It needs a directory of whole MPEG-TS segments
// and ffprobe, so it runs only when asked to:
//
//	HLSMON_FFPROBE_SAMPLES=/path/to/segments go test ./internal/media/ -run TestAgainstFFprobe -v
//
// ffprobe is the reference for what it measures from the packets: tracks,
// codecs, picture size, counts of pictures and keyframes, timestamps.
func TestAgainstFFprobe(t *testing.T) {
	dir := os.Getenv("HLSMON_FFPROBE_SAMPLES")
	if dir == "" {
		t.Skip("HLSMON_FFPROBE_SAMPLES is not set")
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe is not installed")
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*.ts"))
	if len(files) == 0 {
		t.Fatalf("no .ts files in %s", dir)
	}
	compareWithFFprobe(t, files)
}

// TestEncodingsAgainstFFprobe does the same for encodings rare in the wild
// but legal: interlaced video coded as frames, long runs of B-frames, open
// GOPs, intra refresh instead of keyframes, HEVC, MPEG-2. The segments are
// made by ffmpeg on the spot; without it, or without an encoder, the case
// is skipped.
func TestEncodingsAgainstFFprobe(t *testing.T) {
	if testing.Short() {
		t.Skip("encodes video")
	}
	for _, tool := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not installed", tool)
		}
	}
	dir := t.TempDir()
	var files []string
	for name, args := range map[string][]string{
		"h264-interlaced-frames": {"-r", "25", "-c:v", "libx264", "-flags", "+ildct+ilme", "-x264opts", "tff=1"},
		"h264-many-b-frames":     {"-r", "30000/1001", "-c:v", "libx264", "-bf", "8", "-b_strategy", "0", "-x264opts", "b-pyramid=normal:open-gop=1:keyint=48"},
		"h264-intra-refresh":     {"-r", "50", "-c:v", "libx264", "-x264opts", "intra-refresh=1:keyint=50"},
		"h264-60fps-no-b":        {"-r", "60", "-c:v", "libx264", "-bf", "0", "-g", "30"},
		"hevc-open-gop":          {"-r", "25", "-c:v", "libx265", "-x265-params", "open-gop=1:keyint=30:bframes=4:log-level=error"},
		"hevc-10bit-50fps":       {"-r", "50", "-c:v", "libx265", "-pix_fmt", "yuv420p10le", "-x265-params", "keyint=50:log-level=error"},
		"mpeg2-b-frames":         {"-r", "25", "-c:v", "mpeg2video", "-bf", "2", "-g", "12"},
		"mpeg2-interlaced":       {"-r", "30000/1001", "-c:v", "mpeg2video", "-flags", "+ildct+ilme", "-bf", "2", "-g", "15"},
	} {
		out := filepath.Join(dir, name+".ts")
		cmd := append([]string{"-v", "error", "-f", "lavfi", "-i", "testsrc2=size=640x360:rate=60", "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000", "-t", "4"}, args...)
		cmd = append(cmd, "-pix_fmt", "yuv420p", "-c:a", "aac", "-f", "mpegts", out)
		if name == "hevc-10bit-50fps" {
			cmd = append(cmd[:len(cmd)-7], "-c:a", "aac", "-f", "mpegts", out) // keeps its own pixel format
		}
		if msg, err := exec.Command("ffmpeg", cmd...).CombinedOutput(); err != nil {
			t.Logf("%s: not encoded, skipped: %s", name, strings.TrimSpace(string(msg)))
			continue
		}
		files = append(files, out)
	}
	if len(files) == 0 {
		t.Skip("ffmpeg has none of the encoders")
	}
	compareWithFFprobe(t, files)
}

func compareWithFFprobe(t *testing.T, files []string) {
	t.Helper()
	sort.Strings(files)

	diffs := map[string][]string{}
	differ := func(what, file, format string, a ...any) {
		diffs[what] = append(diffs[what], filepath.Base(file)+": "+fmt.Sprintf(format, a...))
	}
	compared, videoTracks, audioTracks := 0, 0, 0
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if Detect(data) != ContainerTS {
			continue
		}
		ref, err := ffprobe(f)
		if err != nil {
			t.Logf("%s: ffprobe failed: %v", filepath.Base(f), err)
			continue
		}
		info := Analyze(data, nil)
		compared++
		if info.Error != "" {
			differ("analyzer error", f, "%s", info.Error)
		}
		for _, r := range ref {
			var tr *Track
			for i := range info.Tracks {
				if info.Tracks[i].ID == r.pid {
					tr = &info.Tracks[i]
				}
			}
			if tr == nil {
				differ("track missing", f, "PID %d (%s %s) is not reported", r.pid, r.kind, r.codec)
				continue
			}
			if tr.Codec != r.codec {
				differ("codec", f, "PID %d: %s, ffprobe %s", r.pid, tr.Codec, r.codec)
			}
			if r.packets == 0 || (r.kind == KindVideo && r.width == 0) || (r.kind == KindAudio && r.sampleRate == 0) {
				continue // ffprobe could not make sense of the track: no reference
			}
			if math.Abs(tr.StartPTS-r.start) > 0.002 {
				differ("start", f, "PID %d: %.3f, ffprobe %.3f", r.pid, tr.StartPTS, r.start)
			}
			if math.Abs(tr.EndPTS-r.end) > 0.002 {
				differ(r.kind+" end", f, "PID %d: %.3f, ffprobe %.3f (%+.3f)", r.pid, tr.EndPTS, r.end, tr.EndPTS-r.end)
			}
			switch r.kind {
			case KindVideo:
				videoTracks++
				if tr.Width != r.width || tr.Height != r.height {
					differ("picture size", f, "%dx%d, ffprobe %dx%d", tr.Width, tr.Height, r.width, r.height)
				}
				// Decoded frames are the reference, also for field-coded video
				// where a packet is a field. A damaged stream decodes into
				// fewer frames than it has pictures: then the packets are.
				if tr.Frames != r.frames && tr.Frames != r.pictures {
					differ("frames", f, "%d, ffprobe decoded %d, %d packets with a picture (%s, %s)", tr.Frames, r.frames, r.pictures, r.codec, r.fieldOrder)
				}
				if tr.Keyframes != r.keys {
					differ("keyframes", f, "%d, ffprobe %d", tr.Keyframes, r.keys)
				}
				if tr.FirstKeyframe != r.firstKey {
					differ("first is keyframe", f, "%v, ffprobe %v", tr.FirstKeyframe, r.firstKey)
				}
				if r.fps > 0 && math.Abs(tr.FPS-r.fps) > 0.02*r.fps {
					differ("fps", f, "%.3f, ffprobe %.3f (%s, %s)", tr.FPS, r.fps, r.codec, r.fieldOrder)
				}
				if math.Abs(tr.MaxGOP-r.maxGOP) > 0.002 {
					differ("longest GOP", f, "%.3f, ffprobe %.3f", tr.MaxGOP, r.maxGOP)
				}
			case KindAudio:
				audioTracks++
				if tr.SampleRate != 0 && (tr.SampleRate != r.sampleRate || tr.Channels != r.channels) {
					differ("audio format", f, "%d Hz %d ch, ffprobe %d Hz %d ch", tr.SampleRate, tr.Channels, r.sampleRate, r.channels)
				}
				if tr.Codec == "aac" && tr.Frames != r.packets {
					differ("audio frames", f, "%d, ffprobe %d", tr.Frames, r.packets)
				}
			}
		}
	}
	t.Logf("compared %d segments: %d video tracks, %d audio tracks", compared, videoTracks, audioTracks)
	kinds := make([]string, 0, len(diffs))
	for k := range diffs {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	for _, k := range kinds {
		t.Errorf("%s differs in %d cases, for example:\n  %s", k, len(diffs[k]), strings.Join(diffs[k][:min(len(diffs[k]), 6)], "\n  "))
	}
}

type ffRef struct {
	pid        uint32
	kind       string
	codec      string
	width      int
	height     int
	fieldOrder string
	sampleRate int
	channels   int

	packets  int // of video: access units, a field of field-coded video being one
	pictures int // packets large enough to hold a picture: not a delimiter alone
	frames   int // decoded by ffprobe; fewer than coded when the stream is damaged
	keys     int
	firstKey bool
	start    float64
	end      float64 // the latest timestamp plus the duration of that packet
	fps      float64 // what ffprobe takes for the frame rate
	maxGOP   float64
}

func ffprobe(file string) ([]ffRef, error) {
	out, err := exec.Command("ffprobe", "-v", "error", "-of", "json", "-show_streams", "-show_packets", "-count_frames",
		"-show_entries", "stream=index,id,codec_type,codec_name,width,height,field_order,sample_rate,channels,nb_read_frames,r_frame_rate:packet=stream_index,pts_time,duration_time,flags,size", file).Output()
	if err != nil {
		return nil, err
	}
	var doc struct {
		Streams []struct {
			Index      int    `json:"index"`
			ID         string `json:"id"`
			Type       string `json:"codec_type"`
			Codec      string `json:"codec_name"`
			Width      int    `json:"width"`
			Height     int    `json:"height"`
			FieldOrder string `json:"field_order"`
			SampleRate string `json:"sample_rate"`
			Channels   int    `json:"channels"`
			Frames     string `json:"nb_read_frames"`
			Rate       string `json:"r_frame_rate"`
		} `json:"streams"`
		Packets []struct {
			Stream   int    `json:"stream_index"`
			PTS      string `json:"pts_time"`
			Duration string `json:"duration_time"`
			Flags    string `json:"flags"`
			Size     string `json:"size"`
		} `json:"packets"`
	}
	if err := json.Unmarshal(out, &doc); err != nil {
		return nil, err
	}
	var refs []ffRef
	for _, s := range doc.Streams {
		if s.Type != "video" && s.Type != "audio" {
			continue
		}
		pid, _ := strconv.ParseUint(strings.TrimPrefix(s.ID, "0x"), 16, 32)
		r := ffRef{pid: uint32(pid), kind: s.Type, codec: s.Codec, width: s.Width, height: s.Height, fieldOrder: s.FieldOrder, channels: s.Channels}
		r.sampleRate, _ = strconv.Atoi(s.SampleRate)
		r.frames, _ = strconv.Atoi(s.Frames)
		if num, den, ok := strings.Cut(s.Rate, "/"); ok {
			n, _ := strconv.ParseFloat(num, 64)
			d, _ := strconv.ParseFloat(den, 64)
			if d > 0 {
				r.fps = n / d
			}
		}
		if r.codec == "mp2" {
			r.codec = "mp3" // the stream type does not tell the layers apart
		}
		type pkt struct {
			pts, dur float64
			key      bool
		}
		var pkts []pkt
		for i, p := range doc.Packets {
			if p.Stream != s.Index {
				continue
			}
			key := strings.HasPrefix(p.Flags, "K")
			if r.packets == 0 {
				r.firstKey = key
			}
			r.packets++
			if size, _ := strconv.Atoi(p.Size); size > 64 {
				r.pictures++
			}
			if key {
				r.keys++
			}
			pts, err := strconv.ParseFloat(p.PTS, 64)
			if err != nil {
				continue
			}
			dur, _ := strconv.ParseFloat(doc.Packets[i].Duration, 64)
			pkts = append(pkts, pkt{pts, dur, key})
		}
		if len(pkts) > 0 {
			sort.Slice(pkts, func(i, j int) bool { return pkts[i].pts < pkts[j].pts })
			last := pkts[len(pkts)-1]
			r.start, r.end = pkts[0].pts, last.pts+last.dur
			if r.frames > 0 && r.packets == 2*r.frames {
				// Field-coded video: ffprobe gives the two fields of a
				// frame one timestamp and half a frame each, so its end is
				// a field short of where the next segment starts.
				r.end += last.dur
			}
			prev := math.NaN()
			for _, p := range pkts {
				if !p.key {
					continue
				}
				if !math.IsNaN(prev) {
					r.maxGOP = max(r.maxGOP, p.pts-prev)
				}
				prev = p.pts
			}
			if math.IsNaN(prev) {
				r.maxGOP = r.end - r.start
			} else {
				r.maxGOP = max(r.maxGOP, r.end-prev)
			}
		}
		refs = append(refs, r)
	}
	return refs, nil
}
