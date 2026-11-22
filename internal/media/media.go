// Package media inspects HLS media segments (MPEG-TS and fragmented MP4)
// without decoding them: container integrity, track parameters and timing.
package media

import (
	"bytes"
	"fmt"
)

// Container kinds reported in Info.Container.
const (
	ContainerTS      = "ts"
	ContainerFMP4    = "fmp4"
	ContainerAAC     = "aac"
	ContainerVTT     = "vtt"
	ContainerUnknown = "unknown"
)

// Track kinds.
const (
	KindVideo = "video"
	KindAudio = "audio"
	KindOther = "other"
)

// Track describes one elementary stream found in a segment.
type Track struct {
	ID    uint32 `json:"id"` // PID for TS, track ID for fMP4
	Kind  string `json:"kind"`
	Codec string `json:"codec"`

	Width   int     `json:"width,omitempty"`
	Height  int     `json:"height,omitempty"`
	Profile string  `json:"profile,omitempty"`
	Level   string  `json:"level,omitempty"`
	FPS     float64 `json:"fps,omitempty"`

	SampleRate int `json:"sample_rate,omitempty"`
	Channels   int `json:"channels,omitempty"`

	Frames int   `json:"frames"`
	Bytes  int64 `json:"bytes"`

	// HasTiming is false when presentation times could not be determined.
	HasTiming bool `json:"has_timing"`
	// StartPTS and EndPTS are in seconds on the track timeline. EndPTS is the
	// presentation end of the last frame.
	StartPTS float64 `json:"start_pts"`
	EndPTS   float64 `json:"end_pts"`

	Keyframes     int  `json:"keyframes,omitempty"`
	FirstKeyframe bool `json:"first_keyframe,omitempty"`
	// MaxGOP is the longest distance between keyframes in seconds, counting
	// the tail after the last keyframe.
	MaxGOP float64 `json:"max_gop,omitempty"`
	// KeyHashes are hashes of the coded bytes of the keyframes, in order,
	// the first maxKeyHashes of them. A live encoder never codes two
	// pictures into the same bytes; a clip that goes round does every time.
	KeyHashes []uint64 `json:"-"`
	// Packets is how many packets (PES packets, or samples of fragmented
	// MP4) the track came in. Video has a picture to a packet, or one to
	// two where fields are sent apart.
	Packets int `json:"-"`
	// PlainBytes are the bytes of the pictures that are not keyframes,
	// without filler, and PlainFrames how many such pictures there are:
	// what tells how much the picture changes from one frame to the next.
	PlainBytes  int64 `json:"-"`
	PlainFrames int   `json:"-"`
	// SoundHashes are the same for the coded frames of sound, the first
	// maxSoundHashes of them.
	SoundHashes []uint64 `json:"-"`
}

// Duration is the presentation duration of the track in the segment.
func (t *Track) Duration() float64 {
	if !t.HasTiming {
		return 0
	}
	return t.EndPTS - t.StartPTS
}

// Bitrate is the average elementary stream bitrate in bits per second.
func (t *Track) Bitrate() float64 {
	d := t.Duration()
	if d <= 0 {
		return 0
	}
	return float64(t.Bytes*8) / d
}

// Info is the result of analysing one segment.
type Info struct {
	Container string  `json:"container"`
	Tracks    []Track `json:"tracks,omitempty"`

	// MPEG-TS specifics.
	Packets         int  `json:"packets,omitempty"`
	SyncErrors      int  `json:"sync_errors,omitempty"`
	CCErrors        int  `json:"cc_errors,omitempty"`
	TransportErrors int  `json:"transport_errors,omitempty"`
	HasPAT          bool `json:"has_pat,omitempty"`
	HasPMT          bool `json:"has_pmt,omitempty"`
	Scrambled       bool `json:"scrambled,omitempty"`
	// Clock is what the program clock of the segment says.
	Clock *TSClock `json:"clock,omitempty"`

	// FirstCC and LastCC are the continuity counters of the first and the
	// last packet with a payload on every PID: what the joints with the
	// neighbouring segments are checked by.
	FirstCC map[uint16]uint8 `json:"-"`
	LastCC  map[uint16]uint8 `json:"-"`

	// TimeWrap is the period in seconds after which timestamps wrap around
	// (2^33 / 90000 for MPEG-TS), zero when they do not wrap.
	TimeWrap float64 `json:"time_wrap,omitempty"`

	// Error is set when the segment could not be parsed completely.
	Error string `json:"error,omitempty"`
}

// Video returns the first video track or nil.
func (i *Info) Video() *Track { return i.first(KindVideo) }

// Audio returns the first audio track or nil.
func (i *Info) Audio() *Track { return i.first(KindAudio) }

func (i *Info) first(kind string) *Track {
	for n := range i.Tracks {
		if i.Tracks[n].Kind == kind {
			return &i.Tracks[n]
		}
	}
	return nil
}

// Detect guesses the container from the first bytes of a segment.
func Detect(data []byte) string {
	switch {
	case len(data) >= 188 && data[0] == 0x47 && (len(data) < 376 || data[188] == 0x47):
		return ContainerTS
	case len(data) >= 8 && isMP4BoxType(data[4:8]):
		return ContainerFMP4
	case bytes.HasPrefix(data, []byte("WEBVTT")) || bytes.HasPrefix(data, []byte("\xef\xbb\xbfWEBVTT")):
		return ContainerVTT
	case bytes.HasPrefix(data, []byte("ID3")) || (len(data) >= 2 && data[0] == 0xff && data[1]&0xf6 == 0xf0):
		return ContainerAAC
	}
	// A TS segment may start with garbage; accept it if sync bytes line up later.
	if off := findTSSync(data, 0); off >= 0 && off < 188*4 {
		return ContainerTS
	}
	return ContainerUnknown
}

func isMP4BoxType(b []byte) bool {
	switch string(b) {
	case "ftyp", "styp", "moof", "moov", "sidx", "emsg", "prft", "free":
		return true
	}
	return false
}

// Analyze inspects a segment. init may be nil; it is only used for fMP4
// segments whose initialization section is delivered separately.
func Analyze(data []byte, init *Init) *Info {
	info := &Info{Container: Detect(data)}
	defer func() {
		// Parsers of untrusted binary data must not take the process down.
		if r := recover(); r != nil {
			info.Error = fmt.Sprintf("parser panic: %v", r)
		}
	}()
	switch info.Container {
	case ContainerTS:
		analyzeTS(data, info)
	case ContainerFMP4:
		analyzeFMP4(data, init, info)
	case ContainerAAC:
		analyzePackedAAC(data, info)
	}
	return info
}

// ProfileName names the profile of H.264 ("h264") or HEVC ("hevc") by its
// number, the way a parsed track has it.
func ProfileName(codec string, idc uint32) string {
	if codec == "hevc" {
		return hevcProfile(idc)
	}
	return h264Profile(idc)
}

func h264Profile(idc uint32) string {
	switch idc {
	case 66:
		return "Baseline"
	case 77:
		return "Main"
	case 88:
		return "Extended"
	case 100:
		return "High"
	case 110:
		return "High 10"
	case 122:
		return "High 4:2:2"
	case 244:
		return "High 4:4:4"
	}
	return fmt.Sprintf("%d", idc)
}

func hevcProfile(idc uint32) string {
	switch idc {
	case 1:
		return "Main"
	case 2:
		return "Main 10"
	case 3:
		return "Main Still Picture"
	case 4:
		return "Range Extensions"
	}
	return fmt.Sprintf("%d", idc)
}
