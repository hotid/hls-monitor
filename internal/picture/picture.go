// Package picture looks at what a stream shows and sounds like: it has one
// picture of a segment decoded, and a little of its sound, and tells how
// bright the picture is, what it looks like and how loud the sound is. A
// stream whose playlists and segments are all in order may still show a
// black screen, a picture that stopped, or a notice going round in circles
// in silence; nothing short of decoding tells.
//
// The decoding is done by ffmpeg, as a program of its own: a decoder reads
// what a stranger sent, and is the last thing to let into the monitor's
// own process. It is given the segment on its input and nothing else — no
// files, no network — and a limit of time and of memory.
package picture

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"math"
	"math/bits"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ErrNoDecoder tells that the program to decode with is not there.
var ErrNoDecoder = errors.New("ffmpeg is not installed")

// ErrNothing tells that neither a picture nor sound came out of the data:
// it was too short, encrypted, or not media at all. It is not a finding.
var ErrNothing = errors.New("nothing could be decoded")

const (
	// thumbWidth is how wide the picture is made; its height follows.
	thumbWidth = 160
	// audioRate and audioSeconds are how the sound is taken: every
	// channel it has, this many samples a second, for this long.
	audioRate    = 8000
	audioSeconds = 2
	// maxOutput bounds what is read from the decoder, picture or sound.
	maxOutput = 1 << 20
	// darkBelow is the luma under which a point counts as dark. Video
	// black is 16; a little above it is still nothing to look at.
	darkBelow = 32
	// Silent is the level given to sound that is exactly nothing.
	Silent = -100.0
)

// Frame is what one look at a segment found.
type Frame struct {
	// HasPicture tells that a picture was decoded; the fields up to JPEG
	// describe it.
	HasPicture    bool `json:"has_picture"`
	Width, Height int  `json:"-"`
	// Luma is the mean brightness, 0 to 255; Dark the share of points
	// that are all but black.
	Luma float64 `json:"luma"`
	Dark float64 `json:"dark"`
	// Hash describes what the picture looks like: pictures that look the
	// same have hashes that differ in a few bits at most (see Distance).
	Hash uint64 `json:"hash"`
	// Cells is the picture shrunk to CellCols by CellRows points of
	// brightness: what two looks are compared by to tell a picture that
	// stands still from one that merely looks alike (see Moved).
	Cells []byte `json:"-"`
	// JPEG is the picture, small.
	JPEG []byte `json:"jpeg,omitempty"`
	// HasAudio tells that sound was decoded; AudioDB is its level in
	// decibels below full scale, Silent when there was none at all.
	HasAudio bool    `json:"has_audio"`
	AudioDB  float64 `json:"audio_db"`
	// ChannelDB is the level of every channel of the sound, in the order
	// the stream has them: left and right first. AudioDB is the loudest.
	ChannelDB []float64 `json:"channel_db,omitempty"`
	// AudioError is what the decoder said when no sound came out.
	AudioError string `json:"-"`
}

// CellCols and CellRows are the size of Frame.Cells.
const (
	CellCols = 32
	CellRows = 18
)

// Moved tells how much two pictures differ where they differ most: the
// largest change of brightness of any one cell, 0 to 255. A picture that
// stands still changes by the noise of coding, a point or two; a studio
// with someone talking in it looks the same as a whole and moves by tens
// where the head is. It is 255 for pictures that cannot be compared.
func Moved(a, b []byte) int {
	if len(a) != CellCols*CellRows || len(b) != len(a) {
		return 255
	}
	most := 0
	for i := range a {
		d := int(a[i]) - int(b[i])
		if d < 0 {
			d = -d
		}
		most = max(most, d)
	}
	return most
}

// MostlyMoved is Moved without the tenth of the cells that changed most:
// how much two pictures differ apart from a small part of them — a clock,
// something blinking — that may differ as it likes.
func MostlyMoved(a, b []byte) int {
	if len(a) != CellCols*CellRows || len(b) != len(a) {
		return 255
	}
	var hist [256]int
	for i := range a {
		d := int(a[i]) - int(b[i])
		if d < 0 {
			d = -d
		}
		hist[d]++
	}
	left := len(a) / 10
	for d := 255; d > 0; d-- {
		if left -= hist[d]; left < 0 {
			return d
		}
	}
	return 0
}

// Contrast tells how much there is to a picture: the spread of the
// brightness of its cells. A flat picture, all dark or all one colour, has
// next to none, and nothing to be told from another flat one by.
func Contrast(cells []byte) float64 {
	if len(cells) == 0 {
		return 0
	}
	var sum, sq float64
	for _, v := range cells {
		sum += float64(v)
		sq += float64(v) * float64(v)
	}
	n := float64(len(cells))
	return math.Sqrt(max(sq/n-(sum/n)*(sum/n), 0))
}

// Distance is in how many bits two hashes differ: 0 for the same picture,
// a few for one that barely changed, about 32 for unrelated ones.
func Distance(a, b uint64) int { return bits.OnesCount64(a ^ b) }

// Options configure a Decoder.
type Options struct {
	// FFmpeg is the program to run; "ffmpeg" by default.
	FFmpeg string
	// Timeout bounds one look; five seconds by default.
	Timeout time.Duration
	// MemoryMB bounds the address space of the decoder; 1024 by default.
	MemoryMB int
	// Parallel is how many looks may run at once; one by default.
	Parallel int
}

// Decoder looks at segments. It is safe for concurrent use.
type Decoder struct {
	path    string
	timeout time.Duration
	memory  int
	slots   chan struct{}

	once sync.Once
	err  error
}

// New makes a decoder. The program is looked for when it is first needed.
func New(o Options) *Decoder {
	if o.FFmpeg == "" {
		o.FFmpeg = "ffmpeg"
	}
	if o.Timeout <= 0 {
		o.Timeout = 5 * time.Second
	}
	if o.MemoryMB <= 0 {
		o.MemoryMB = 1024
	}
	if o.Parallel <= 0 {
		o.Parallel = 1
	}
	return &Decoder{path: o.FFmpeg, timeout: o.Timeout, memory: o.MemoryMB, slots: make(chan struct{}, o.Parallel)}
}

// Available tells whether the program is there, and why not.
func (d *Decoder) Available() error {
	d.once.Do(func() {
		p, err := exec.LookPath(d.path)
		if err != nil {
			d.err = fmt.Errorf("%w: %s", ErrNoDecoder, d.path)
			return
		}
		d.path = p
	})
	return d.err
}

// Busy tells whether every slot is taken: a look asked for now would wait.
func (d *Decoder) Busy() bool { return len(d.slots) == cap(d.slots) }

// Sample looks at a segment, or at its beginning: the first keyframe in
// the data and the first seconds of sound. For fragmented MP4 the data is
// the initialization section followed by the segment.
func (d *Decoder) Sample(ctx context.Context, data []byte) (*Frame, error) {
	if err := d.Available(); err != nil {
		return nil, err
	}
	select {
	case d.slots <- struct{}{}:
		defer func() { <-d.slots }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	ctx, cancel := context.WithTimeout(ctx, d.timeout)
	defer cancel()

	// One run for both, which is what most segments have. A track with
	// the picture alone, its sound being a rendition of its own, or the
	// other way round, makes the decoder refuse the output it has nothing
	// for and the other with it: then each is asked for by itself.
	shot, sound, errText, runErr := d.decode(ctx, data, true, true, true)
	noVideo, apart := false, false
	if len(shot) == 0 && len(sound) == 0 && refusedOutput(errText) {
		apart = true
		if shot, _, errText, runErr = d.decode(ctx, data, true, false, true); len(shot) == 0 {
			noVideo = refusedOutput(errText)
			_, sound, errText, runErr = d.decode(ctx, data, false, true, true)
		}
	}
	// Some encoders never send a frame the decoder calls a keyframe: every
	// group of pictures opens with a recovery point instead. Then the
	// first picture that decodes at all is taken.
	if len(shot) == 0 && !noVideo && ctx.Err() == nil {
		if any, _, _, _ := d.decode(ctx, data, true, false, false); len(any) > 0 {
			shot = any
		}
	}

	// The decoder stops at the first thing it cannot decode, and a picture
	// that would not decode takes the sound of the same run with it. Sound
	// that did not come out with the picture is asked for by itself, so
	// that what is said of it is said of the sound.
	pcm, channels := readWAV(sound)
	heard := func() bool { return channels > 0 && len(pcm)/2/channels >= audioRate/10 }
	audioErr := errText
	if !heard() && len(shot) > 0 && !apart && ctx.Err() == nil {
		_, sound, audioErr, _ = d.decode(ctx, data, false, true, true)
		pcm, channels = readWAV(sound)
	}

	f := &Frame{}
	if img := readPPM(shot); img != nil {
		f.HasPicture = true
		f.Width, f.Height = img.Bounds().Dx(), img.Bounds().Dy()
		f.Luma, f.Dark, f.Hash = look(img)
		f.Cells = cells(img)
		var out bytes.Buffer
		if err := jpeg.Encode(&out, img, &jpeg.Options{Quality: 70}); err == nil {
			f.JPEG = out.Bytes()
		}
	}
	if heard() {
		f.HasAudio = true
		f.AudioDB = Silent
		for ch := range channels {
			db := level(pcm, ch, channels)
			f.ChannelDB = append(f.ChannelDB, db)
			f.AudioDB = max(f.AudioDB, db)
		}
	} else if msg := strings.TrimSpace(audioErr); msg != "" && !refusedOutput(msg) {
		f.AudioError = firstLine(msg)
	}
	if !f.HasPicture && !f.HasAudio {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("the decoder did not finish in %s", d.timeout)
		}
		if msg := strings.TrimSpace(errText); msg != "" {
			return nil, fmt.Errorf("%w: %s", ErrNothing, firstLine(msg))
		}
		if runErr != nil {
			return nil, fmt.Errorf("%w: %v", ErrNothing, runErr)
		}
		return nil, ErrNothing
	}
	return f, nil
}

// decode runs the decoder once, for the picture, the sound or both, and
// returns what came out of it: the picture as PPM, the sound as a WAV
// file of signed 16-bit samples, what it had to say and how it ended.
func (d *Decoder) decode(ctx context.Context, data []byte, picture, audio, keyOnly bool) (shot, sound []byte, errText string, runErr error) {
	args := []string{
		"-nostdin", "-v", "error", "-threads", "1",
		// Nothing but what it is handed: no files, no network.
		"-protocol_whitelist", "pipe",
		// A frame that did not decode cleanly is not shown: a decoder
		// left to conceal its errors paints grey where it has no data,
		// and that would pass for a picture.
		"-xerror",
	}
	if picture && keyOnly {
		// Keyframes only; an option the decoder refuses when there is
		// no video to apply it to.
		args = append(args, "-skip_frame", "nokey")
	}
	args = append(args, "-i", "pipe:0")
	if picture {
		args = append(args, "-map", "0:v:0?", "-frames:v", "1", "-vf", "scale="+strconv.Itoa(thumbWidth)+":-2",
			// Eight bits a colour, whatever the video has: ten-bit video
			// would otherwise come out as a picture of sixteen.
			"-pix_fmt", "rgb24", "-f", "image2pipe", "-c:v", "ppm", "pipe:1")
	}
	if audio {
		// With the header of a sound file before the samples: it says
		// how many channels there are.
		args = append(args, "-map", "0:a:0?", "-t", strconv.Itoa(audioSeconds), "-vn", "-ar", strconv.Itoa(audioRate),
			"-c:a", "pcm_s16le", "-f", "wav", "pipe:3")
	}
	// The sound comes back over a pipe of its own, the picture over the
	// standard output.
	audioR, audioW, err := os.Pipe()
	if err != nil {
		return nil, nil, "", err
	}
	defer audioR.Close()
	// The shell is there for one thing: to set the limit of memory before
	// the decoder starts.
	script := `ulimit -v ` + strconv.Itoa(d.memory*1024) + ` 2>/dev/null; exec "$0" "$@"`
	cmd := exec.CommandContext(ctx, "/bin/sh", append([]string{"-c", script, d.path}, args...)...)
	cmd.Env = []string{"PATH=/usr/local/bin:/usr/bin:/bin"}
	cmd.Stdin = bytes.NewReader(data)
	cmd.ExtraFiles = []*os.File{audioW}
	var pictureOut, errOut limited
	pictureOut.left, errOut.left = maxOutput, 4096
	cmd.Stdout, cmd.Stderr = &pictureOut, &errOut
	cmd.WaitDelay = time.Second
	if err := cmd.Start(); err != nil {
		audioW.Close()
		return nil, nil, "", err
	}
	audioW.Close() // the decoder holds its own end now
	sound, _ = io.ReadAll(io.LimitReader(audioR, maxOutput))
	runErr = cmd.Wait()
	return pictureOut.buf.Bytes(), sound, errOut.buf.String(), runErr
}

// refusedOutput tells that the decoder refused to start because one of the
// outputs asked for had no track to be made from.
func refusedOutput(errText string) bool {
	return strings.Contains(errText, "does not contain any stream") || strings.Contains(errText, "not a decoding option")
}

// limited keeps the first bytes written to it and drops the rest.
type limited struct {
	buf  bytes.Buffer
	left int
}

func (l *limited) Write(p []byte) (int, error) {
	if n := min(len(p), l.left); n > 0 {
		l.buf.Write(p[:n])
		l.left -= n
	}
	return len(p), nil
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}

// readPPM reads a picture in the plainest format there is: "P6", the
// width, the height and 255 as text, then three bytes a point. Nil when
// the data is not that, or not all of it.
func readPPM(b []byte) *image.RGBA {
	var magic string
	var w, h, depth, n int
	if _, err := fmt.Sscan(string(b[:min(len(b), 32)]), &magic, &w, &h, &depth); err != nil || magic != "P6" || depth != 255 {
		return nil
	}
	if w <= 0 || h <= 0 || w > 4096 || h > 4096 {
		return nil
	}
	// The points begin after the third line break of the header.
	for i, c := range b[:min(len(b), 32)] {
		if c == '\n' {
			if n++; n == 3 {
				b = b[i+1:]
				break
			}
		}
	}
	if n != 3 || len(b) < w*h*3 {
		return nil
	}
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := range w * h {
		img.Pix[4*i], img.Pix[4*i+1], img.Pix[4*i+2], img.Pix[4*i+3] = b[3*i], b[3*i+1], b[3*i+2], 255
	}
	return img
}

// look measures a picture: its mean brightness, the share of points that
// are all but black, and a hash of what it looks like.
func look(img *image.RGBA) (luma, dark float64, hash uint64) {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w == 0 || h == 0 {
		return 0, 0, 0
	}
	ys := make([]float64, w*h)
	var sum float64
	var darks int
	for y := range h {
		for x := range w {
			p := img.Pix[4*(y*w+x):]
			v := 0.299*float64(p[0]) + 0.587*float64(p[1]) + 0.114*float64(p[2])
			ys[y*w+x] = v
			sum += v
			if v < darkBelow {
				darks++
			}
		}
	}
	n := float64(w * h)

	// A difference hash: the picture is shrunk to nine by eight cells, and
	// every cell tells whether it is brighter than its neighbour to the
	// right. It does not change with brightness or small detail, and does
	// with what is in the picture.
	const cols, rows = 9, 8
	var cells [rows][cols]float64
	for r := range rows {
		for c := range cols {
			x0, x1 := c*w/cols, max((c+1)*w/cols, c*w/cols+1)
			y0, y1 := r*h/rows, max((r+1)*h/rows, r*h/rows+1)
			var s float64
			for y := y0; y < min(y1, h); y++ {
				for x := x0; x < min(x1, w); x++ {
					s += ys[y*w+x]
				}
			}
			cells[r][c] = s / float64((min(y1, h)-y0)*(min(x1, w)-x0))
		}
	}
	for r := range rows {
		for c := range cols - 1 {
			hash <<= 1
			// A cell must be brighter by more than the noise of coding,
			// or a flat picture would hash to something random.
			if cells[r][c] > cells[r][c+1]+1 {
				hash |= 1
			}
		}
	}
	return sum / n, float64(darks) / n, hash
}

// cells shrinks a picture to CellCols by CellRows points of brightness,
// each the mean of the points it covers.
func cells(img *image.RGBA) []byte {
	w, h := img.Bounds().Dx(), img.Bounds().Dy()
	out := make([]byte, CellCols*CellRows)
	for r := range CellRows {
		for c := range CellCols {
			x0, x1 := c*w/CellCols, max((c+1)*w/CellCols, c*w/CellCols+1)
			y0, y1 := r*h/CellRows, max((r+1)*h/CellRows, r*h/CellRows+1)
			var sum float64
			n := 0
			for y := y0; y < min(y1, h); y++ {
				for x := x0; x < min(x1, w); x++ {
					p := img.Pix[4*(y*w+x):]
					sum += 0.299*float64(p[0]) + 0.587*float64(p[1]) + 0.114*float64(p[2])
					n++
				}
			}
			if n > 0 {
				out[r*CellCols+c] = byte(sum/float64(n) + 0.5)
			}
		}
	}
	return out
}

// readWAV finds the samples in a sound file written to a pipe — signed,
// 16 bits, the channels interleaved — and how many channels they are of.
// No channels when the data is not that. The sizes in the header of a
// file written to a pipe are not to be believed: the samples run to the
// end of the data.
func readWAV(b []byte) (pcm []byte, channels int) {
	if len(b) < 12 || string(b[:4]) != "RIFF" || string(b[8:12]) != "WAVE" {
		return nil, 0
	}
	for b = b[12:]; len(b) >= 8; {
		id, size := string(b[:4]), int(uint32(b[4])|uint32(b[5])<<8|uint32(b[6])<<16|uint32(b[7])<<24)
		b = b[8:]
		switch {
		case id == "data":
			if channels < 1 || channels > 16 {
				return nil, 0
			}
			return b[:len(b)-len(b)%(2*channels)], channels
		case size < 0 || size > len(b):
			return nil, 0
		case id == "fmt " && size >= 16:
			// 1 is plain samples, 0xFFFE the same said at greater length.
			if format := int(b[0]) | int(b[1])<<8; (format != 1 && format != 0xFFFE) || int(b[14])|int(b[15])<<8 != 16 {
				return nil, 0
			}
			channels = int(b[2]) | int(b[3])<<8
		}
		b = b[size+size%2:]
	}
	return nil, 0
}

// level is the loudness of one channel of signed 16-bit samples in
// decibels below full scale, by their root mean square.
func level(pcm []byte, ch, channels int) float64 {
	n := len(pcm) / 2 / channels
	if n == 0 {
		return Silent
	}
	var sum float64
	for i := range n {
		o := 2 * (i*channels + ch)
		v := float64(int16(uint16(pcm[o]) | uint16(pcm[o+1])<<8))
		sum += v * v
	}
	rms := math.Sqrt(sum / float64(n))
	if rms < 1 {
		return Silent
	}
	return max(20*math.Log10(rms/32768), Silent)
}
