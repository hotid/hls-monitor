// Package problemkind is what can be wrong with a stream, or with a
// server that several streams are served by.
package problemkind

import "github.com/hotid/hls-monitor/internal/types/enum"

type domain struct{}

var set = enum.NewSet("problem kind")

func (domain) Set() *enum.Set { return set }

// The values of the set are all defined by the time the package is
// loaded; nothing is added after.
func init() { set.Seal() }

type value = enum.Of[domain]

// Kind is one problem kind.
type Kind struct{ value }

func def(name string) Kind {
	_ = set // the set is there before the first value is
	return Kind{enum.Def[domain](name)}
}

var (
	playlistUnreachable = def("playlist_unreachable")
	playlistHTTP        = def("playlist_http_error")
	playlistParse       = def("playlist_parse_error")
	playlistSyntax      = def("playlist_syntax")
	playlistStale       = def("playlist_stale")
	playlistEmpty       = def("playlist_empty")
	seqRegression       = def("sequence_regression")
	seqJump             = def("sequence_jump")
	seqRenumbered       = def("sequence_renumbered")
	segmentChanged      = def("segment_changed")
	targetDuration      = def("target_duration_exceeded")
	targetDurChanged    = def("target_duration_changed")
	pdtDiscontinuity    = def("pdt_discontinuity")
	streamEnded         = def("stream_ended")
	variantsChanged     = def("variants_changed")
	variantsOutOfSync   = def("variants_out_of_sync")
	keyUnavailable      = def("key_unavailable")
	initUnavailable     = def("init_unavailable")
	certExpiring        = def("cert_expiring")
	segmentUnreachable  = def("segment_unreachable")
	segmentHTTP         = def("segment_http_error")
	segmentTruncated    = def("segment_truncated")
	segmentSlow         = def("segment_slow")
	playbackStall       = def("playback_stall")
	bitrateExceeds      = def("bitrate_exceeds_declared")
	mediaParse          = def("media_parse_error")
	tsSync              = def("ts_sync_error")
	tsContinuity        = def("ts_continuity_error")
	tsTransport         = def("ts_transport_error")
	tsNoTables          = def("ts_no_pat_pmt")
	noKeyframeStart     = def("no_keyframe_start")
	durationMismatch    = def("duration_mismatch")
	ptsDiscontinuity    = def("pts_discontinuity")
	avDesync            = def("av_desync")
	mediaParamsChanged  = def("media_params_changed")
	playlistSource      = def("playlist_source_changed")
	segmentSource       = def("segment_source_changed")
	pictureBlack        = def("picture_black")
	pictureFrozen       = def("picture_frozen")
	audioSilent         = def("audio_silent")
	pictureSlate        = def("picture_slate")
	pictureLoop         = def("picture_loop")
	audioLoop           = def("audio_loop")
	audioChannelSilent  = def("audio_channel_silent")
	audioQuiet          = def("audio_quiet")
	audioUndecodable    = def("audio_undecodable")
	audioAbsent         = def("audio_absent")
	mediaUndecodable    = def("media_undecodable")
	declaredMismatch    = def("declared_mismatch")
	tsClock             = def("ts_clock_error")
	subtitlesInvalid    = def("subtitles_invalid")
	vantageBehind       = def("vantage_behind")
	networkLoss         = def("network_loss")
)

func PlaylistUnreachable() Kind { return playlistUnreachable }

func PlaylistHTTP() Kind { return playlistHTTP }

func PlaylistParse() Kind { return playlistParse }

func PlaylistSyntax() Kind { return playlistSyntax }

func PlaylistStale() Kind { return playlistStale }

func PlaylistEmpty() Kind { return playlistEmpty }

func SeqRegression() Kind { return seqRegression }

func SeqJump() Kind { return seqJump }

func SeqRenumbered() Kind { return seqRenumbered }

func SegmentChanged() Kind { return segmentChanged }

func TargetDuration() Kind { return targetDuration }

func TargetDurChanged() Kind { return targetDurChanged }

func PDTDiscontinuity() Kind { return pdtDiscontinuity }

func StreamEnded() Kind { return streamEnded }

func VariantsChanged() Kind { return variantsChanged }

func VariantsOutOfSync() Kind { return variantsOutOfSync }

func KeyUnavailable() Kind { return keyUnavailable }

func InitUnavailable() Kind { return initUnavailable }

func CertExpiring() Kind { return certExpiring }

func SegmentUnreachable() Kind { return segmentUnreachable }

func SegmentHTTP() Kind { return segmentHTTP }

func SegmentTruncated() Kind { return segmentTruncated }

func SegmentSlow() Kind { return segmentSlow }

func PlaybackStall() Kind { return playbackStall }

func BitrateExceeds() Kind { return bitrateExceeds }

func MediaParse() Kind { return mediaParse }

func TSSync() Kind { return tsSync }

func TSContinuity() Kind { return tsContinuity }

func TSTransport() Kind { return tsTransport }

func TSNoTables() Kind { return tsNoTables }

func NoKeyframeStart() Kind { return noKeyframeStart }

func DurationMismatch() Kind { return durationMismatch }

func PTSDiscontinuity() Kind { return ptsDiscontinuity }

func AVDesync() Kind { return avDesync }

func MediaParamsChanged() Kind { return mediaParamsChanged }

func PlaylistSource() Kind { return playlistSource }

func SegmentSource() Kind { return segmentSource }

func PictureBlack() Kind { return pictureBlack }

func PictureFrozen() Kind { return pictureFrozen }

func AudioSilent() Kind { return audioSilent }

func PictureSlate() Kind { return pictureSlate }

func PictureLoop() Kind { return pictureLoop }

func AudioLoop() Kind { return audioLoop }

func AudioChannelSilent() Kind { return audioChannelSilent }

func AudioQuiet() Kind { return audioQuiet }

func AudioUndecodable() Kind { return audioUndecodable }

func AudioAbsent() Kind { return audioAbsent }

func MediaUndecodable() Kind { return mediaUndecodable }

func DeclaredMismatch() Kind { return declaredMismatch }

func TSClock() Kind { return tsClock }

func SubtitlesInvalid() Kind { return subtitlesInvalid }

func VantageBehind() Kind { return vantageBehind }

// NetworkLoss is a server's and not a stream's: the way to the server
// loses packets.
func NetworkLoss() Kind { return networkLoss }

// Parse finds the problem kind a name stands for.
func Parse(name string) (Kind, error) {
	v, err := enum.Parse[domain](name)
	return Kind{v}, err
}

// Values lists every problem kind.
func Values() []Kind {
	vs := enum.All[domain]()
	out := make([]Kind, len(vs))
	for i, v := range vs {
		out[i] = Kind{v}
	}
	return out
}
