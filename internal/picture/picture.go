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
	// audioRate and audioSeconds are how the sound is taken: mono, this
	// many samples a second, for this long.
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

	// The sound comes back over a pipe of its own, the picture over the
	// standard output.
	audioR, audioW, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	defer audioR.Close()

	args := []string{
		"-nostdin", "-v", "error", "-threads", "1",
		// Nothing but what it is handed: no files, no network.
		"-protocol_whitelist", "pipe",
		// A frame that did not decode cleanly is not shown: a decoder
		// left to conceal its errors paints grey where it has no data,
		// and that would pass for a picture.
		"-xerror",
		"-skip_frame", "nokey", "-i", "pipe:0",
		"-map", "0:v:0?", "-frames:v", "1", "-vf", "scale=" + strconv.Itoa(thumbWidth) + ":-2",
		"-f", "image2pipe", "-c:v", "ppm", "pipe:1",
		"-map", "0:a:0?", "-t", strconv.Itoa(audioSeconds), "-vn", "-ac", "1", "-ar", strconv.Itoa(audioRate),
		"-f", "s16le", "pipe:3",
	}
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
		return nil, err
	}
	audioW.Close() // the decoder holds its own end now
	sound, _ := io.ReadAll(io.LimitReader(audioR, maxOutput))
	runErr := cmd.Wait()

	f := &Frame{}
	if img := readPPM(pictureOut.buf.Bytes()); img != nil {
		f.HasPicture = true
		f.Width, f.Height = img.Bounds().Dx(), img.Bounds().Dy()
		f.Luma, f.Dark, f.Hash = look(img)
		f.Cells = cells(img)
		var out bytes.Buffer
		if err := jpeg.Encode(&out, img, &jpeg.Options{Quality: 70}); err == nil {
			f.JPEG = out.Bytes()
		}
	}
	if n := len(sound) / 2; n >= audioRate/10 {
		f.HasAudio = true
		f.AudioDB = level(sound)
	}
	if !f.HasPicture && !f.HasAudio {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("the decoder did not finish in %s", d.timeout)
		}
		if msg := strings.TrimSpace(errOut.buf.String()); msg != "" {
			return nil, fmt.Errorf("%w: %s", ErrNothing, firstLine(msg))
		}
		if runErr != nil {
			return nil, fmt.Errorf("%w: %v", ErrNothing, runErr)
		}
		return nil, ErrNothing
	}
	return f, nil
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

// level is the loudness of signed 16-bit samples in decibels below full
// scale, by their root mean square.
func level(pcm []byte) float64 {
	n := len(pcm) / 2
	var sum float64
	for i := range n {
		v := float64(int16(uint16(pcm[2*i]) | uint16(pcm[2*i+1])<<8))
		sum += v * v
	}
	rms := math.Sqrt(sum / float64(n))
	if rms < 1 {
		return Silent
	}
	return max(20*math.Log10(rms/32768), Silent)
}
