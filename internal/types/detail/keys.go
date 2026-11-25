package detail

import (
	"time"

	"github.com/hotid/hls-monitor/internal/types/node"
	"github.com/hotid/hls-monitor/internal/types/variantkey"
)

var (
	kAheadAgentID            = defKey("ahead_agent_id", countValue)
	kAllowedExcess           = defKey("allowed_excess", numberValue)
	kAudioFrameBytes         = defKey("audio_frame_bytes", numberValue)
	kAudioFrames             = defKey("audio_frames", countValue)
	kAudioLevelDB            = defKey("audio_level_db", numberValue)
	kBehindLimit             = defKey("behind_limit", numberValue)
	kBehindSeconds           = defKey("behind_seconds", numberValue)
	kBitrateRatio            = defKey("bitrate_ratio", numberValue)
	kBitrateRatioLimit       = defKey("bitrate_ratio_limit", numberValue)
	kBitsPerPixel            = defKey("bits_per_pixel", numberValue)
	kBufferAtStart           = defKey("buffer_at_start", numberValue)
	kBufferBefore            = defKey("buffer_before", numberValue)
	kBufferShortfall         = defKey("buffer_shortfall", numberValue)
	kChecksInARow            = defKey("checks_in_a_row", countValue)
	kClipSignature           = defKey("clip_signature", hashesValue)
	kComparedWith            = defKey("compared_with", variantValue)
	kCycleElapsed            = defKey("cycle_elapsed", numberValue)
	kCycleFailedSegments     = defKey("cycle_failed_segments", countValue)
	kCycleMedia              = defKey("cycle_media", numberValue)
	kCycleSegments           = defKey("cycle_segments", countValue)
	kDeclaredBandwidth       = defKey("declared_bandwidth", countValue)
	kDecoderSays             = defKey("decoder_says", textValue)
	kDistinctFrames          = defKey("distinct_frames", countValue)
	kDistinctKeyframes       = defKey("distinct_keyframes", countValue)
	kFailedChecksBetween     = defKey("failed_checks_between", countValue)
	kFirstSequence           = defKey("first_sequence", countValue)
	kJudgedBy                = defKey("judged_by", textValue)
	kKnownBy                 = defKey("known_by", textValue)
	kLastFailure             = defKey("last_failure", textValue)
	kLastSegmentURI          = defKey("last_segment_uri", textValue)
	kLastSequence            = defKey("last_sequence", countValue)
	kLeftLevelDB             = defKey("left_level_db", numberValue)
	kLookedAt                = defKey("looked_at", timeValue)
	kLooksInARow             = defKey("looks_in_a_row", countValue)
	kMediaRate               = defKey("media_rate", numberValue)
	kMediaReceived           = defKey("media_received", numberValue)
	kMissedSegments          = defKey("missed_segments", countValue)
	kPictureDarkShare        = defKey("picture_dark_share", numberValue)
	kPictureLuma             = defKey("picture_luma", numberValue)
	kPlayedFor               = defKey("played_for", numberValue)
	kPlaylistBytes           = defKey("playlist_bytes", countValue)
	kPlaylistError           = defKey("playlist_error", textValue)
	kPlaylistFinalURL        = defKey("playlist_final_url", textValue)
	kPlaylistHTTPStatus      = defKey("playlist_http_status", countValue)
	kPlaylistRedirects       = defKey("playlist_redirects", linesValue)
	kPlaylistSeconds         = defKey("playlist_seconds", numberValue)
	kPlaylistSegments        = defKey("playlist_segments", countValue)
	kPlaylistServer          = defKey("playlist_server", serverValue)
	kPlaylistServerBefore    = defKey("playlist_server_before", serverValue)
	kPlaylistSoftware        = defKey("playlist_software", textValue)
	kPlaylistURL             = defKey("playlist_url", textValue)
	kPlaylistWindow          = defKey("playlist_window", numberValue)
	kPreviousLastSequence    = defKey("previous_last_sequence", countValue)
	kPreviousPlaylistKeeps   = defKey("previous_playlist_keeps", numberValue)
	kQuietBelowDB            = defKey("quiet_below_db", numberValue)
	kRenditionOffset         = defKey("rendition_offset", numberValue)
	kRenditionOffsetLimit    = defKey("rendition_offset_limit", numberValue)
	kRightLevelDB            = defKey("right_level_db", numberValue)
	kSegmentBitrate          = defKey("segment_bitrate", numberValue)
	kSegmentBytes            = defKey("segment_bytes", countValue)
	kSegmentDuration         = defKey("segment_duration", numberValue)
	kSegmentDurationMax      = defKey("segment_duration_max", numberValue)
	kSegmentDurationMin      = defKey("segment_duration_min", numberValue)
	kSegmentError            = defKey("segment_error", textValue)
	kSegmentFetchedBytes     = defKey("segment_fetched_bytes", countValue)
	kSegmentHTTPStatus       = defKey("segment_http_status", countValue)
	kSegmentSequence         = defKey("segment_sequence", countValue)
	kSegmentServer           = defKey("segment_server", serverValue)
	kSegmentSoftware         = defKey("segment_software", textValue)
	kSegmentURI              = defKey("segment_uri", textValue)
	kSegmentURL              = defKey("segment_url", textValue)
	kSegmentsInARow          = defKey("segments_in_a_row", countValue)
	kSegmentsMoved           = defKey("segments_moved", countValue)
	kSegmentsMovedLimit      = defKey("segments_moved_limit", countValue)
	kSegmentsReceived        = defKey("segments_received", countValue)
	kSilentChannel           = defKey("silent_channel", textValue)
	kSince                   = defKey("since", timeValue)
	kSincePreviousCheck      = defKey("since_previous_check", numberValue)
	kSlate                   = defKey("slate", textValue)
	kTargetDuration          = defKey("target_duration", numberValue)
	kTargetDurationBefore    = defKey("target_duration_before", numberValue)
	kUnchangedFor            = defKey("unchanged_for", numberValue)
	kUnchangedLimit          = defKey("unchanged_limit", numberValue)
	kURIBefore               = defKey("uri_before", textValue)
	kURINow                  = defKey("uri_now", textValue)
	kVariantsEdgeSpread      = defKey("variants_edge_spread", numberValue)
	kVariantsEdgeSpreadLimit = defKey("variants_edge_spread_limit", numberValue)
	kVideoKbitS              = defKey("video_kbit_s", numberValue)
	kVideoPackets            = defKey("video_packets", countValue)
	kVideoPictures           = defKey("video_pictures", countValue)
)

// AheadAgentID is the figure ahead_agent_id.
func AheadAgentID[T integer](v T) Field { return countField(kAheadAgentID, float64(v)) }

// KeyAheadAgentID names the figure ahead_agent_id.
func KeyAheadAgentID() Key { return kAheadAgentID }

// AllowedExcess is the figure allowed_excess.
func AllowedExcess(v float64) Field { return numberField(kAllowedExcess, v) }

// KeyAllowedExcess names the figure allowed_excess.
func KeyAllowedExcess() Key { return kAllowedExcess }

// AudioFrameBytes is the figure audio_frame_bytes.
func AudioFrameBytes(v float64) Field { return numberField(kAudioFrameBytes, v) }

// KeyAudioFrameBytes names the figure audio_frame_bytes.
func KeyAudioFrameBytes() Key { return kAudioFrameBytes }

// AudioFrames is the figure audio_frames.
func AudioFrames[T integer](v T) Field { return countField(kAudioFrames, float64(v)) }

// KeyAudioFrames names the figure audio_frames.
func KeyAudioFrames() Key { return kAudioFrames }

// AudioLevelDB is the figure audio_level_db.
func AudioLevelDB(v float64) Field { return numberField(kAudioLevelDB, v) }

// KeyAudioLevelDB names the figure audio_level_db.
func KeyAudioLevelDB() Key { return kAudioLevelDB }

// BehindLimit is the figure behind_limit.
func BehindLimit(v float64) Field { return numberField(kBehindLimit, v) }

// KeyBehindLimit names the figure behind_limit.
func KeyBehindLimit() Key { return kBehindLimit }

// BehindSeconds is the figure behind_seconds.
func BehindSeconds(v float64) Field { return numberField(kBehindSeconds, v) }

// KeyBehindSeconds names the figure behind_seconds.
func KeyBehindSeconds() Key { return kBehindSeconds }

// BitrateRatio is the figure bitrate_ratio.
func BitrateRatio(v float64) Field { return numberField(kBitrateRatio, v) }

// KeyBitrateRatio names the figure bitrate_ratio.
func KeyBitrateRatio() Key { return kBitrateRatio }

// BitrateRatioLimit is the figure bitrate_ratio_limit.
func BitrateRatioLimit(v float64) Field { return numberField(kBitrateRatioLimit, v) }

// KeyBitrateRatioLimit names the figure bitrate_ratio_limit.
func KeyBitrateRatioLimit() Key { return kBitrateRatioLimit }

// BitsPerPixel is the figure bits_per_pixel.
func BitsPerPixel(v float64) Field { return numberField(kBitsPerPixel, v) }

// KeyBitsPerPixel names the figure bits_per_pixel.
func KeyBitsPerPixel() Key { return kBitsPerPixel }

// BufferAtStart is the figure buffer_at_start.
func BufferAtStart(v float64) Field { return numberField(kBufferAtStart, v) }

// KeyBufferAtStart names the figure buffer_at_start.
func KeyBufferAtStart() Key { return kBufferAtStart }

// BufferBefore is the figure buffer_before.
func BufferBefore(v float64) Field { return numberField(kBufferBefore, v) }

// KeyBufferBefore names the figure buffer_before.
func KeyBufferBefore() Key { return kBufferBefore }

// BufferShortfall is the figure buffer_shortfall.
func BufferShortfall(v float64) Field { return numberField(kBufferShortfall, v) }

// KeyBufferShortfall names the figure buffer_shortfall.
func KeyBufferShortfall() Key { return kBufferShortfall }

// ChecksInARow is the figure checks_in_a_row.
func ChecksInARow[T integer](v T) Field { return countField(kChecksInARow, float64(v)) }

// KeyChecksInARow names the figure checks_in_a_row.
func KeyChecksInARow() Key { return kChecksInARow }

// ClipSignature is the figure clip_signature.
func ClipSignature(v []uint64) Field { return hashesField(kClipSignature, v) }

// KeyClipSignature names the figure clip_signature.
func KeyClipSignature() Key { return kClipSignature }

// ComparedWith is the figure compared_with.
func ComparedWith(v variantkey.Key) Field { return textField(kComparedWith, v.String()) }

// KeyComparedWith names the figure compared_with.
func KeyComparedWith() Key { return kComparedWith }

// CycleElapsed is the figure cycle_elapsed.
func CycleElapsed(v float64) Field { return numberField(kCycleElapsed, v) }

// KeyCycleElapsed names the figure cycle_elapsed.
func KeyCycleElapsed() Key { return kCycleElapsed }

// CycleFailedSegments is the figure cycle_failed_segments.
func CycleFailedSegments[T integer](v T) Field { return countField(kCycleFailedSegments, float64(v)) }

// KeyCycleFailedSegments names the figure cycle_failed_segments.
func KeyCycleFailedSegments() Key { return kCycleFailedSegments }

// CycleMedia is the figure cycle_media.
func CycleMedia(v float64) Field { return numberField(kCycleMedia, v) }

// KeyCycleMedia names the figure cycle_media.
func KeyCycleMedia() Key { return kCycleMedia }

// CycleSegments is the figure cycle_segments.
func CycleSegments[T integer](v T) Field { return countField(kCycleSegments, float64(v)) }

// KeyCycleSegments names the figure cycle_segments.
func KeyCycleSegments() Key { return kCycleSegments }

// DeclaredBandwidth is the figure declared_bandwidth.
func DeclaredBandwidth[T integer](v T) Field { return countField(kDeclaredBandwidth, float64(v)) }

// KeyDeclaredBandwidth names the figure declared_bandwidth.
func KeyDeclaredBandwidth() Key { return kDeclaredBandwidth }

// DecoderSays is the figure decoder_says.
func DecoderSays(v string) Field { return textField(kDecoderSays, v) }

// KeyDecoderSays names the figure decoder_says.
func KeyDecoderSays() Key { return kDecoderSays }

// DistinctFrames is the figure distinct_frames.
func DistinctFrames[T integer](v T) Field { return countField(kDistinctFrames, float64(v)) }

// KeyDistinctFrames names the figure distinct_frames.
func KeyDistinctFrames() Key { return kDistinctFrames }

// DistinctKeyframes is the figure distinct_keyframes.
func DistinctKeyframes[T integer](v T) Field { return countField(kDistinctKeyframes, float64(v)) }

// KeyDistinctKeyframes names the figure distinct_keyframes.
func KeyDistinctKeyframes() Key { return kDistinctKeyframes }

// FailedChecksBetween is the figure failed_checks_between.
func FailedChecksBetween[T integer](v T) Field { return countField(kFailedChecksBetween, float64(v)) }

// KeyFailedChecksBetween names the figure failed_checks_between.
func KeyFailedChecksBetween() Key { return kFailedChecksBetween }

// FirstSequence is the figure first_sequence.
func FirstSequence[T integer](v T) Field { return countField(kFirstSequence, float64(v)) }

// KeyFirstSequence names the figure first_sequence.
func KeyFirstSequence() Key { return kFirstSequence }

// JudgedBy is the figure judged_by.
func JudgedBy(v string) Field { return textField(kJudgedBy, v) }

// KeyJudgedBy names the figure judged_by.
func KeyJudgedBy() Key { return kJudgedBy }

// KnownBy is the figure known_by.
func KnownBy(v string) Field { return textField(kKnownBy, v) }

// KeyKnownBy names the figure known_by.
func KeyKnownBy() Key { return kKnownBy }

// LastFailure is the figure last_failure.
func LastFailure(v string) Field { return textField(kLastFailure, v) }

// KeyLastFailure names the figure last_failure.
func KeyLastFailure() Key { return kLastFailure }

// LastSegmentURI is the figure last_segment_uri.
func LastSegmentURI(v string) Field { return textField(kLastSegmentURI, v) }

// KeyLastSegmentURI names the figure last_segment_uri.
func KeyLastSegmentURI() Key { return kLastSegmentURI }

// LastSequence is the figure last_sequence.
func LastSequence[T integer](v T) Field { return countField(kLastSequence, float64(v)) }

// KeyLastSequence names the figure last_sequence.
func KeyLastSequence() Key { return kLastSequence }

// LeftLevelDB is the figure left_level_db.
func LeftLevelDB(v float64) Field { return numberField(kLeftLevelDB, v) }

// KeyLeftLevelDB names the figure left_level_db.
func KeyLeftLevelDB() Key { return kLeftLevelDB }

// LookedAt is the figure looked_at.
func LookedAt(v time.Time) Field { return timeField(kLookedAt, v) }

// KeyLookedAt names the figure looked_at.
func KeyLookedAt() Key { return kLookedAt }

// LooksInARow is the figure looks_in_a_row.
func LooksInARow[T integer](v T) Field { return countField(kLooksInARow, float64(v)) }

// KeyLooksInARow names the figure looks_in_a_row.
func KeyLooksInARow() Key { return kLooksInARow }

// MediaRate is the figure media_rate.
func MediaRate(v float64) Field { return numberField(kMediaRate, v) }

// KeyMediaRate names the figure media_rate.
func KeyMediaRate() Key { return kMediaRate }

// MediaReceived is the figure media_received.
func MediaReceived(v float64) Field { return numberField(kMediaReceived, v) }

// KeyMediaReceived names the figure media_received.
func KeyMediaReceived() Key { return kMediaReceived }

// MissedSegments is the figure missed_segments.
func MissedSegments[T integer](v T) Field { return countField(kMissedSegments, float64(v)) }

// KeyMissedSegments names the figure missed_segments.
func KeyMissedSegments() Key { return kMissedSegments }

// PictureDarkShare is the figure picture_dark_share.
func PictureDarkShare(v float64) Field { return numberField(kPictureDarkShare, v) }

// KeyPictureDarkShare names the figure picture_dark_share.
func KeyPictureDarkShare() Key { return kPictureDarkShare }

// PictureLuma is the figure picture_luma.
func PictureLuma(v float64) Field { return numberField(kPictureLuma, v) }

// KeyPictureLuma names the figure picture_luma.
func KeyPictureLuma() Key { return kPictureLuma }

// PlayedFor is the figure played_for.
func PlayedFor(v float64) Field { return numberField(kPlayedFor, v) }

// KeyPlayedFor names the figure played_for.
func KeyPlayedFor() Key { return kPlayedFor }

// PlaylistBytes is the figure playlist_bytes.
func PlaylistBytes[T integer](v T) Field { return countField(kPlaylistBytes, float64(v)) }

// KeyPlaylistBytes names the figure playlist_bytes.
func KeyPlaylistBytes() Key { return kPlaylistBytes }

// PlaylistError is the figure playlist_error.
func PlaylistError(v string) Field { return textField(kPlaylistError, v) }

// KeyPlaylistError names the figure playlist_error.
func KeyPlaylistError() Key { return kPlaylistError }

// PlaylistFinalURL is the figure playlist_final_url.
func PlaylistFinalURL(v string) Field { return textField(kPlaylistFinalURL, v) }

// KeyPlaylistFinalURL names the figure playlist_final_url.
func KeyPlaylistFinalURL() Key { return kPlaylistFinalURL }

// PlaylistHTTPStatus is the figure playlist_http_status.
func PlaylistHTTPStatus[T integer](v T) Field { return countField(kPlaylistHTTPStatus, float64(v)) }

// KeyPlaylistHTTPStatus names the figure playlist_http_status.
func KeyPlaylistHTTPStatus() Key { return kPlaylistHTTPStatus }

// PlaylistRedirects is the figure playlist_redirects.
func PlaylistRedirects(v []string) Field { return linesField(kPlaylistRedirects, v) }

// KeyPlaylistRedirects names the figure playlist_redirects.
func KeyPlaylistRedirects() Key { return kPlaylistRedirects }

// PlaylistSeconds is the figure playlist_seconds.
func PlaylistSeconds(v float64) Field { return numberField(kPlaylistSeconds, v) }

// KeyPlaylistSeconds names the figure playlist_seconds.
func KeyPlaylistSeconds() Key { return kPlaylistSeconds }

// PlaylistSegments is the figure playlist_segments.
func PlaylistSegments[T integer](v T) Field { return countField(kPlaylistSegments, float64(v)) }

// KeyPlaylistSegments names the figure playlist_segments.
func KeyPlaylistSegments() Key { return kPlaylistSegments }

// PlaylistServer is the figure playlist_server.
func PlaylistServer(v node.Node) Field { return textField(kPlaylistServer, v.String()) }

// KeyPlaylistServer names the figure playlist_server.
func KeyPlaylistServer() Key { return kPlaylistServer }

// PlaylistServerBefore is the figure playlist_server_before.
func PlaylistServerBefore(v node.Node) Field { return textField(kPlaylistServerBefore, v.String()) }

// KeyPlaylistServerBefore names the figure playlist_server_before.
func KeyPlaylistServerBefore() Key { return kPlaylistServerBefore }

// PlaylistSoftware is the figure playlist_software.
func PlaylistSoftware(v string) Field { return textField(kPlaylistSoftware, v) }

// KeyPlaylistSoftware names the figure playlist_software.
func KeyPlaylistSoftware() Key { return kPlaylistSoftware }

// PlaylistURL is the figure playlist_url.
func PlaylistURL(v string) Field { return textField(kPlaylistURL, v) }

// KeyPlaylistURL names the figure playlist_url.
func KeyPlaylistURL() Key { return kPlaylistURL }

// PlaylistWindow is the figure playlist_window.
func PlaylistWindow(v float64) Field { return numberField(kPlaylistWindow, v) }

// KeyPlaylistWindow names the figure playlist_window.
func KeyPlaylistWindow() Key { return kPlaylistWindow }

// PreviousLastSequence is the figure previous_last_sequence.
func PreviousLastSequence[T integer](v T) Field { return countField(kPreviousLastSequence, float64(v)) }

// KeyPreviousLastSequence names the figure previous_last_sequence.
func KeyPreviousLastSequence() Key { return kPreviousLastSequence }

// PreviousPlaylistKeeps is the figure previous_playlist_keeps.
func PreviousPlaylistKeeps(v float64) Field { return numberField(kPreviousPlaylistKeeps, v) }

// KeyPreviousPlaylistKeeps names the figure previous_playlist_keeps.
func KeyPreviousPlaylistKeeps() Key { return kPreviousPlaylistKeeps }

// QuietBelowDB is the figure quiet_below_db.
func QuietBelowDB(v float64) Field { return numberField(kQuietBelowDB, v) }

// KeyQuietBelowDB names the figure quiet_below_db.
func KeyQuietBelowDB() Key { return kQuietBelowDB }

// RenditionOffset is the figure rendition_offset.
func RenditionOffset(v float64) Field { return numberField(kRenditionOffset, v) }

// KeyRenditionOffset names the figure rendition_offset.
func KeyRenditionOffset() Key { return kRenditionOffset }

// RenditionOffsetLimit is the figure rendition_offset_limit.
func RenditionOffsetLimit(v float64) Field { return numberField(kRenditionOffsetLimit, v) }

// KeyRenditionOffsetLimit names the figure rendition_offset_limit.
func KeyRenditionOffsetLimit() Key { return kRenditionOffsetLimit }

// RightLevelDB is the figure right_level_db.
func RightLevelDB(v float64) Field { return numberField(kRightLevelDB, v) }

// KeyRightLevelDB names the figure right_level_db.
func KeyRightLevelDB() Key { return kRightLevelDB }

// SegmentBitrate is the figure segment_bitrate.
func SegmentBitrate(v float64) Field { return numberField(kSegmentBitrate, v) }

// KeySegmentBitrate names the figure segment_bitrate.
func KeySegmentBitrate() Key { return kSegmentBitrate }

// SegmentBytes is the figure segment_bytes.
func SegmentBytes[T integer](v T) Field { return countField(kSegmentBytes, float64(v)) }

// KeySegmentBytes names the figure segment_bytes.
func KeySegmentBytes() Key { return kSegmentBytes }

// SegmentDuration is the figure segment_duration.
func SegmentDuration(v float64) Field { return numberField(kSegmentDuration, v) }

// KeySegmentDuration names the figure segment_duration.
func KeySegmentDuration() Key { return kSegmentDuration }

// SegmentDurationMax is the figure segment_duration_max.
func SegmentDurationMax(v float64) Field { return numberField(kSegmentDurationMax, v) }

// KeySegmentDurationMax names the figure segment_duration_max.
func KeySegmentDurationMax() Key { return kSegmentDurationMax }

// SegmentDurationMin is the figure segment_duration_min.
func SegmentDurationMin(v float64) Field { return numberField(kSegmentDurationMin, v) }

// KeySegmentDurationMin names the figure segment_duration_min.
func KeySegmentDurationMin() Key { return kSegmentDurationMin }

// SegmentError is the figure segment_error.
func SegmentError(v string) Field { return textField(kSegmentError, v) }

// KeySegmentError names the figure segment_error.
func KeySegmentError() Key { return kSegmentError }

// SegmentFetchedBytes is the figure segment_fetched_bytes.
func SegmentFetchedBytes[T integer](v T) Field { return countField(kSegmentFetchedBytes, float64(v)) }

// KeySegmentFetchedBytes names the figure segment_fetched_bytes.
func KeySegmentFetchedBytes() Key { return kSegmentFetchedBytes }

// SegmentHTTPStatus is the figure segment_http_status.
func SegmentHTTPStatus[T integer](v T) Field { return countField(kSegmentHTTPStatus, float64(v)) }

// KeySegmentHTTPStatus names the figure segment_http_status.
func KeySegmentHTTPStatus() Key { return kSegmentHTTPStatus }

// SegmentSequence is the figure segment_sequence.
func SegmentSequence[T integer](v T) Field { return countField(kSegmentSequence, float64(v)) }

// KeySegmentSequence names the figure segment_sequence.
func KeySegmentSequence() Key { return kSegmentSequence }

// SegmentServer is the figure segment_server.
func SegmentServer(v node.Node) Field { return textField(kSegmentServer, v.String()) }

// KeySegmentServer names the figure segment_server.
func KeySegmentServer() Key { return kSegmentServer }

// SegmentSoftware is the figure segment_software.
func SegmentSoftware(v string) Field { return textField(kSegmentSoftware, v) }

// KeySegmentSoftware names the figure segment_software.
func KeySegmentSoftware() Key { return kSegmentSoftware }

// SegmentURI is the figure segment_uri.
func SegmentURI(v string) Field { return textField(kSegmentURI, v) }

// KeySegmentURI names the figure segment_uri.
func KeySegmentURI() Key { return kSegmentURI }

// SegmentURL is the figure segment_url.
func SegmentURL(v string) Field { return textField(kSegmentURL, v) }

// KeySegmentURL names the figure segment_url.
func KeySegmentURL() Key { return kSegmentURL }

// SegmentsInARow is the figure segments_in_a_row.
func SegmentsInARow[T integer](v T) Field { return countField(kSegmentsInARow, float64(v)) }

// KeySegmentsInARow names the figure segments_in_a_row.
func KeySegmentsInARow() Key { return kSegmentsInARow }

// SegmentsMoved is the figure segments_moved.
func SegmentsMoved[T integer](v T) Field { return countField(kSegmentsMoved, float64(v)) }

// KeySegmentsMoved names the figure segments_moved.
func KeySegmentsMoved() Key { return kSegmentsMoved }

// SegmentsMovedLimit is the figure segments_moved_limit.
func SegmentsMovedLimit[T integer](v T) Field { return countField(kSegmentsMovedLimit, float64(v)) }

// KeySegmentsMovedLimit names the figure segments_moved_limit.
func KeySegmentsMovedLimit() Key { return kSegmentsMovedLimit }

// SegmentsReceived is the figure segments_received.
func SegmentsReceived[T integer](v T) Field { return countField(kSegmentsReceived, float64(v)) }

// KeySegmentsReceived names the figure segments_received.
func KeySegmentsReceived() Key { return kSegmentsReceived }

// SilentChannel is the figure silent_channel.
func SilentChannel(v string) Field { return textField(kSilentChannel, v) }

// KeySilentChannel names the figure silent_channel.
func KeySilentChannel() Key { return kSilentChannel }

// Since is the figure since.
func Since(v time.Time) Field { return timeField(kSince, v) }

// KeySince names the figure since.
func KeySince() Key { return kSince }

// SincePreviousCheck is the figure since_previous_check.
func SincePreviousCheck(v float64) Field { return numberField(kSincePreviousCheck, v) }

// KeySincePreviousCheck names the figure since_previous_check.
func KeySincePreviousCheck() Key { return kSincePreviousCheck }

// Slate is the figure slate.
func Slate(v string) Field { return textField(kSlate, v) }

// KeySlate names the figure slate.
func KeySlate() Key { return kSlate }

// TargetDuration is the figure target_duration.
func TargetDuration(v float64) Field { return numberField(kTargetDuration, v) }

// KeyTargetDuration names the figure target_duration.
func KeyTargetDuration() Key { return kTargetDuration }

// TargetDurationBefore is the figure target_duration_before.
func TargetDurationBefore(v float64) Field { return numberField(kTargetDurationBefore, v) }

// KeyTargetDurationBefore names the figure target_duration_before.
func KeyTargetDurationBefore() Key { return kTargetDurationBefore }

// UnchangedFor is the figure unchanged_for.
func UnchangedFor(v time.Duration) Field { return numberField(kUnchangedFor, v.Seconds()) }

// KeyUnchangedFor names the figure unchanged_for.
func KeyUnchangedFor() Key { return kUnchangedFor }

// UnchangedLimit is the figure unchanged_limit.
func UnchangedLimit(v time.Duration) Field { return numberField(kUnchangedLimit, v.Seconds()) }

// KeyUnchangedLimit names the figure unchanged_limit.
func KeyUnchangedLimit() Key { return kUnchangedLimit }

// URIBefore is the figure uri_before.
func URIBefore(v string) Field { return textField(kURIBefore, v) }

// KeyURIBefore names the figure uri_before.
func KeyURIBefore() Key { return kURIBefore }

// URINow is the figure uri_now.
func URINow(v string) Field { return textField(kURINow, v) }

// KeyURINow names the figure uri_now.
func KeyURINow() Key { return kURINow }

// VariantsEdgeSpread is the figure variants_edge_spread.
func VariantsEdgeSpread(v float64) Field { return numberField(kVariantsEdgeSpread, v) }

// KeyVariantsEdgeSpread names the figure variants_edge_spread.
func KeyVariantsEdgeSpread() Key { return kVariantsEdgeSpread }

// VariantsEdgeSpreadLimit is the figure variants_edge_spread_limit.
func VariantsEdgeSpreadLimit(v float64) Field { return numberField(kVariantsEdgeSpreadLimit, v) }

// KeyVariantsEdgeSpreadLimit names the figure variants_edge_spread_limit.
func KeyVariantsEdgeSpreadLimit() Key { return kVariantsEdgeSpreadLimit }

// VideoKbitS is the figure video_kbit_s.
func VideoKbitS(v float64) Field { return numberField(kVideoKbitS, v) }

// KeyVideoKbitS names the figure video_kbit_s.
func KeyVideoKbitS() Key { return kVideoKbitS }

// VideoPackets is the figure video_packets.
func VideoPackets[T integer](v T) Field { return countField(kVideoPackets, float64(v)) }

// KeyVideoPackets names the figure video_packets.
func KeyVideoPackets() Key { return kVideoPackets }

// VideoPictures is the figure video_pictures.
func VideoPictures[T integer](v T) Field { return countField(kVideoPictures, float64(v)) }

// KeyVideoPictures names the figure video_pictures.
func KeyVideoPictures() Key { return kVideoPictures }
