package picture

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// clip has ffmpeg make a short MPEG-TS segment from a picture source and a
// sound source of its own.
func clip(t *testing.T, video, audio string, more ...string) []byte {
	t.Helper()
	out := filepath.Join(t.TempDir(), "clip.ts")
	args := []string{"-v", "error", "-f", "lavfi", "-i", video, "-f", "lavfi", "-i", audio, "-t", "3",
		"-r", "25", "-c:v", "libx264", "-g", "25", "-pix_fmt", "yuv420p", "-c:a", "aac"}
	args = append(args, more...)
	args = append(args, "-f", "mpegts", out)
	if msg, err := exec.Command("ffmpeg", args...).CombinedOutput(); err != nil {
		t.Skipf("ffmpeg cannot encode this: %s", strings.TrimSpace(string(msg)))
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func decoder(t *testing.T) *Decoder {
	t.Helper()
	if testing.Short() {
		t.Skip("encodes video")
	}
	d := New(Options{})
	if err := d.Available(); err != nil {
		t.Skip(err)
	}
	return d
}

const (
	moving = "testsrc2=size=640x360:rate=25"
	bars   = "smptebars=size=640x360:rate=25"
	black  = "color=c=black:size=640x360:rate=25"
	tone   = "sine=frequency=440:sample_rate=48000"
	quiet  = "anullsrc=sample_rate=48000:channel_layout=stereo"
)

func TestSample(t *testing.T) {
	d := decoder(t)
	ctx := context.Background()

	live, err := d.Sample(ctx, clip(t, moving, tone))
	if err != nil {
		t.Fatal(err)
	}
	if !live.HasPicture || live.Width != thumbWidth || live.Height != 90 || len(live.JPEG) < 500 {
		t.Fatalf("picture: %+v", *live)
	}
	if live.Luma < 40 || live.Luma > 220 || live.Dark > 0.5 {
		t.Errorf("a test card: luma %.1f, dark %.2f", live.Luma, live.Dark)
	}
	if !live.HasAudio || live.AudioDB < -40 || live.AudioDB > 0 {
		t.Errorf("a tone: %v dB, heard %v", live.AudioDB, live.HasAudio)
	}

	dead, err := d.Sample(ctx, clip(t, black, quiet))
	if err != nil {
		t.Fatal(err)
	}
	if !dead.HasPicture || dead.Luma > 20 || dead.Dark < 0.98 {
		t.Errorf("a black screen: luma %.1f, dark %.2f", dead.Luma, dead.Dark)
	}
	if !dead.HasAudio || dead.AudioDB > -90 {
		t.Errorf("silence: %v dB, heard %v", dead.AudioDB, dead.HasAudio)
	}

	// The same picture coded twice looks the same; another does not. A
	// flat picture has no shape to hash, and is not mistaken for one.
	again, err := d.Sample(ctx, clip(t, bars, tone, "-b:v", "300k"))
	if err != nil {
		t.Fatal(err)
	}
	still, err := d.Sample(ctx, clip(t, bars, tone))
	if err != nil {
		t.Fatal(err)
	}
	if n := Distance(still.Hash, again.Hash); n > 2 {
		t.Errorf("the same bars coded twice differ in %d bits", n)
	}
	if n := Distance(still.Hash, live.Hash); n < 10 {
		t.Errorf("bars and a test card differ in %d bits only", n)
	}
	if dead.Hash != 0 {
		t.Errorf("a black screen hashes to %x, want nothing", dead.Hash)
	}
}

func TestSampleOfWhatIsNotMedia(t *testing.T) {
	d := decoder(t)
	ctx := context.Background()
	for name, data := range map[string][]byte{
		"nothing": nil,
		"text":    []byte(strings.Repeat("#EXTM3U\n#EXTINF:6,\nseg.ts\n", 400)),
		"noise":   noise(200_000),
	} {
		if f, err := d.Sample(ctx, data); !errors.Is(err, ErrNothing) {
			t.Errorf("%s: %+v, %v; want ErrNothing", name, f, err)
		}
	}
	// The beginning of a segment is enough when the keyframe is in it, and
	// otherwise it is nothing to look at, not a failure.
	whole := clip(t, moving, tone)
	if f, err := d.Sample(ctx, whole[:len(whole)/3]); err != nil || !f.HasPicture {
		t.Errorf("the first third of a segment: %+v, %v", f, err)
	}
	if f, err := d.Sample(ctx, whole[:2000]); err == nil && f.HasPicture {
		t.Errorf("a picture out of 2000 bytes: %+v", f)
	}
}

func noise(n int) []byte {
	b := make([]byte, n)
	x := uint32(2463534242)
	for i := range b {
		x ^= x << 13
		x ^= x >> 17
		x ^= x << 5
		b[i] = byte(x)
	}
	return b
}

// TestRealSegments looks at real segments, when a directory of them is
// given, and tells what it saw: ordinary television is neither black nor
// silent, and what is flagged here would be a false alarm.
func TestRealSegments(t *testing.T) {
	dir := os.Getenv("HLSMON_FFPROBE_SAMPLES")
	if dir == "" {
		t.Skip("HLSMON_FFPROBE_SAMPLES is not set")
	}
	d := decoder(t)
	files, _ := filepath.Glob(filepath.Join(dir, "*.ts"))
	sort.Strings(files)
	var pictures, sounds, fromHead, black, silent int
	for _, name := range files {
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		f, err := d.Sample(context.Background(), data)
		if err != nil {
			t.Logf("%s: %v", filepath.Base(name), err)
			continue
		}
		if f.HasPicture {
			pictures++
			if f.Luma < 20 && f.Dark > 0.98 {
				black++
				t.Logf("%s: black, luma %.1f", filepath.Base(name), f.Luma)
			}
		}
		if f.HasAudio {
			sounds++
			if f.AudioDB < -60 {
				silent++
				t.Logf("%s: silent, %.1f dB", filepath.Base(name), f.AudioDB)
			}
		}
		if head, err := d.Sample(context.Background(), data[:min(len(data), 131600)]); err == nil && head.HasPicture {
			fromHead++
		}
	}
	t.Logf("%d segments: a picture from %d (from the first 131600 bytes of %d), sound from %d; black %d, silent %d",
		len(files), pictures, fromHead, sounds, black, silent)
}

// TestRealNeighbours compares the first pictures of consecutive segments
// of real streams, when a directory of them is given (N_0.ts and N_1.ts
// are six seconds apart): how often live television looks the same from
// one look to the next, and by how much it moves when it does.
func TestRealNeighbours(t *testing.T) {
	dir := os.Getenv("HLSMON_FFPROBE_SAMPLES")
	if dir == "" {
		t.Skip("HLSMON_FFPROBE_SAMPLES is not set")
	}
	d := decoder(t)
	firsts, _ := filepath.Glob(filepath.Join(dir, "*_0.ts"))
	sort.Strings(firsts)
	var pairs, alike int
	var moved []int
	for _, name := range firsts {
		a, errA := os.ReadFile(name)
		b, errB := os.ReadFile(strings.TrimSuffix(name, "_0.ts") + "_1.ts")
		if errA != nil || errB != nil {
			continue
		}
		fa, errA := d.Sample(context.Background(), a)
		fb, errB := d.Sample(context.Background(), b)
		if errA != nil || errB != nil || !fa.HasPicture || !fb.HasPicture {
			continue
		}
		pairs++
		m := Moved(fa.Cells, fb.Cells)
		moved = append(moved, m)
		if Distance(fa.Hash, fb.Hash) <= 2 && fa.Luma-fb.Luma < 2 && fb.Luma-fa.Luma < 2 {
			alike++
			t.Logf("%s: alike by the hash, moved by %d", filepath.Base(name), m)
		}
	}
	sort.Ints(moved)
	if pairs == 0 {
		t.Skip("no pairs of segments")
	}
	t.Logf("%d pairs: alike by the hash %d; moved by: least %d, tenth %d, median %d; by 4 or less %d, by 8 or less %d",
		pairs, alike, moved[0], moved[len(moved)/10], moved[len(moved)/2], count(moved, 4), count(moved, 8))
}

func count(sorted []int, atMost int) int { return sort.SearchInts(sorted, atMost+1) }

func TestStillPictureDoesNotMove(t *testing.T) {
	d := decoder(t)
	ctx := context.Background()
	// The same still picture coded twice, at different rates: it moves by
	// the noise of coding and no more. A test card with a counter on it
	// looks alike and moves where the counter is.
	a, err := d.Sample(ctx, clip(t, bars, tone))
	if err != nil {
		t.Fatal(err)
	}
	b, err := d.Sample(ctx, clip(t, bars, tone, "-b:v", "150k"))
	if err != nil {
		t.Fatal(err)
	}
	if m := Moved(a.Cells, b.Cells); m > 4 {
		t.Errorf("a still picture coded twice moved by %d", m)
	}
	early, err := d.Sample(ctx, clip(t, moving, tone))
	if err != nil {
		t.Fatal(err)
	}
	late, err := d.Sample(ctx, clip(t, moving, tone, "-ss", "7"))
	if err != nil {
		t.Fatal(err)
	}
	if m := Moved(early.Cells, late.Cells); m < 12 {
		t.Errorf("a test card seven seconds on moved by %d only", m)
	}
}

func TestTrackWithThePictureAloneOrTheSoundAlone(t *testing.T) {
	d := decoder(t)
	ctx := context.Background()
	// Where the sound is a rendition of its own, the video playlist has
	// segments with no sound in them, and the audio one none with a picture.
	silent, err := d.Sample(ctx, clip(t, moving, tone, "-an"))
	if err != nil || !silent.HasPicture || silent.HasAudio {
		t.Errorf("a segment with the picture alone: %+v, %v", silent, err)
	}
	blind, err := d.Sample(ctx, clip(t, moving, tone, "-vn"))
	if err != nil || blind.HasPicture || !blind.HasAudio || blind.AudioDB < -40 {
		t.Errorf("a segment with the sound alone: %+v, %v", blind, err)
	}
}

func TestTenBitPicture(t *testing.T) {
	d := decoder(t)
	// Ten-bit video must come out as an ordinary picture all the same.
	f, err := d.Sample(context.Background(), clip(t, moving, tone, "-pix_fmt", "yuv420p10le"))
	if err != nil || !f.HasPicture || f.Height != 90 || f.Luma < 40 {
		t.Errorf("ten-bit video: %+v, %v", f, err)
	}
}

func TestPictureWithoutKeyframes(t *testing.T) {
	d := decoder(t)
	// With intra refresh only the very first frame is a keyframe; past it
	// there are recovery points and nothing the decoder calls a key.
	whole := clip(t, moving, tone, "-t", "6", "-x264-params", "intra-refresh=1:keyint=25")
	from := len(whole) / 3 / 188 * 188
	f, err := d.Sample(context.Background(), whole[from:])
	if err != nil || !f.HasPicture || f.Luma < 40 {
		t.Errorf("a segment with no keyframe in it: %+v, %v", f, err)
	}
}
