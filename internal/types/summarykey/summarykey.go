// Package summarykey is the names of the figures kept with the latest
// state of a stream, so that lists and dashboards do not need a time
// series query per stream.
package summarykey

import "github.com/hotid/hls-monitor/internal/types/enum"

type domain struct{}

var set = enum.NewSet("summary figure")

func (domain) Set() *enum.Set { return set }

type value = enum.Of[domain]

// Key is one summary figure.
type Key struct{ value }

func def(name string) Key {
	_ = set // the set is there before the first value is
	return Key{enum.Def[domain](name)}
}

var (
	bitrateBps           = def("bitrate_bps")
	declaredBandwidthBps = def("declared_bandwidth_bps")
	segmentTotalSeconds  = def("segment_total_seconds")
	segmentDownloadRatio = def("segment_download_ratio")
	playlistTotalSeconds = def("playlist_total_seconds")
	liveLatencySeconds   = def("live_latency_seconds")
	bufferSeconds        = def("buffer_seconds")
	segmentsChecked      = def("segments_checked")
	segmentErrors        = def("segment_errors")
	stalls               = def("stalls")
	height               = def("height")
	width                = def("width")
	fps                  = def("fps")
	hdr                  = def("hdr")
	loudnessLufs         = def("loudness_lufs")
	subtitleCuesHour     = def("subtitle_cues_hour")
	checkDurationSeconds = def("check_duration_seconds")
	variants             = def("variants")
	subtitles            = def("subtitles")
	problems             = def("problems")
	overloaded           = def("overloaded")
)

func BitrateBps() Key { return bitrateBps }

func DeclaredBandwidthBps() Key { return declaredBandwidthBps }

func SegmentTotalSeconds() Key { return segmentTotalSeconds }

func SegmentDownloadRatio() Key { return segmentDownloadRatio }

func PlaylistTotalSeconds() Key { return playlistTotalSeconds }

func LiveLatencySeconds() Key { return liveLatencySeconds }

func BufferSeconds() Key { return bufferSeconds }

func SegmentsChecked() Key { return segmentsChecked }

func SegmentErrors() Key { return segmentErrors }

func Stalls() Key { return stalls }

func Height() Key { return height }

func Width() Key { return width }

func Fps() Key { return fps }

// Hdr: 1 for a picture in PQ (HDR10, Dolby Vision), 2 for HLG, 0 for
// standard.
func Hdr() Key { return hdr }

// LoudnessLufs: loudness by EBU R 128 of the loudest track, in LUFS:
// measured now and then, and kept until measured again.
func LoudnessLufs() Key { return loudnessLufs }

// SubtitleCuesHour: cues in the subtitles in the last hour; 0 for
// subtitles that are empty.
func SubtitleCuesHour() Key { return subtitleCuesHour }

func CheckDurationSeconds() Key { return checkDurationSeconds }

func Variants() Key { return variants }

func Subtitles() Key { return subtitles }

func Problems() Key { return problems }

func Overloaded() Key { return overloaded }

// Parse finds the summary figure a name stands for.
func Parse(name string) (Key, error) {
	v, err := enum.Parse[domain](name)
	return Key{v}, err
}

// Values lists every summary figure.
func Values() []Key {
	vs := enum.All[domain]()
	out := make([]Key, len(vs))
	for i, v := range vs {
		out[i] = Key{v}
	}
	return out
}
