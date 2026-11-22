package media

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"
)

// Subtitles is what a segment of WebVTT subtitles holds.
type Subtitles struct {
	// Cues is how many cues the segment has; a segment with none is one
	// of a time when nothing is said.
	Cues int `json:"cues"`
	// Start and End are the earliest start and the latest end of its
	// cues, in seconds of the stream's timeline when Mapped, of the
	// segment's own otherwise.
	Start  float64 `json:"start,omitempty"`
	End    float64 `json:"end,omitempty"`
	Mapped bool    `json:"mapped,omitempty"`
}

// IsWebVTT tells whether data begins as a file of WebVTT does.
func IsWebVTT(data []byte) bool {
	data = bytes.TrimPrefix(data, []byte{0xef, 0xbb, 0xbf})
	if !bytes.HasPrefix(data, []byte("WEBVTT")) {
		return false
	}
	return len(data) == 6 || data[6] == ' ' || data[6] == '\t' || data[6] == '\n' || data[6] == '\r'
}

// ParseWebVTT reads a segment of WebVTT subtitles: its header, the mapping
// of its times onto the timeline of the stream (X-TIMESTAMP-MAP, RFC 8216,
// section 3.5) and the times of its cues. The text of the cues is not
// looked at.
func ParseWebVTT(data []byte) (*Subtitles, error) {
	if !IsWebVTT(data) {
		return nil, fmt.Errorf("does not begin with WEBVTT")
	}
	text := strings.ReplaceAll(strings.ReplaceAll(string(bytes.TrimPrefix(data, []byte{0xef, 0xbb, 0xbf})), "\r\n", "\n"), "\r", "\n")
	lines := strings.Split(text, "\n")
	s := &Subtitles{}
	shift := 0.0
	i := 1
	for ; i < len(lines) && lines[i] != ""; i++ {
		rest, ok := strings.CutPrefix(lines[i], "X-TIMESTAMP-MAP=")
		if !ok {
			continue
		}
		var ts, local float64
		var haveTS, haveLocal bool
		for _, part := range strings.Split(rest, ",") {
			k, v, _ := strings.Cut(strings.TrimSpace(part), ":")
			switch k {
			case "MPEGTS":
				n, err := strconv.ParseUint(v, 10, 64)
				if err != nil {
					return nil, fmt.Errorf("X-TIMESTAMP-MAP: MPEGTS is %q", v)
				}
				ts, haveTS = float64(n)/tsClock, true
			case "LOCAL":
				t, err := vttTime(v)
				if err != nil {
					return nil, fmt.Errorf("X-TIMESTAMP-MAP: LOCAL is %q", v)
				}
				local, haveLocal = t, true
			}
		}
		if !haveTS || !haveLocal {
			return nil, fmt.Errorf("X-TIMESTAMP-MAP lacks MPEGTS or LOCAL: %q", rest)
		}
		shift, s.Mapped = ts-local, true
	}
	for ; i < len(lines); i++ {
		from, rest, ok := strings.Cut(lines[i], "-->")
		if !ok {
			continue
		}
		to, _, _ := strings.Cut(strings.TrimSpace(rest), " ")
		start, err1 := vttTime(strings.TrimSpace(from))
		end, err2 := vttTime(strings.TrimSpace(to))
		if err1 != nil || err2 != nil {
			return nil, fmt.Errorf("line %d: the times of a cue cannot be read: %q", i+1, lines[i])
		}
		if end < start {
			return nil, fmt.Errorf("line %d: a cue ends before it starts: %q", i+1, lines[i])
		}
		start, end = start+shift, end+shift
		if s.Cues == 0 || start < s.Start {
			s.Start = start
		}
		if s.Cues == 0 || end > s.End {
			s.End = end
		}
		s.Cues++
	}
	return s, nil
}

// vttTime reads hh:mm:ss.ttt or mm:ss.ttt.
func vttTime(v string) (float64, error) {
	parts := strings.Split(v, ":")
	if len(parts) != 2 && len(parts) != 3 {
		return 0, fmt.Errorf("not a time")
	}
	sec, frac, ok := strings.Cut(parts[len(parts)-1], ".")
	if !ok || len(sec) != 2 || len(frac) != 3 {
		return 0, fmt.Errorf("not a time")
	}
	total := 0.0
	for _, p := range append(parts[:len(parts)-1], sec) {
		n, err := strconv.ParseUint(p, 10, 32)
		if err != nil {
			return 0, err
		}
		total = total*60 + float64(n)
	}
	ms, err := strconv.ParseUint(frac, 10, 32)
	if err != nil {
		return 0, err
	}
	return total + float64(ms)/1000, nil
}
