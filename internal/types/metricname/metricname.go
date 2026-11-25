// Package metricname is the names of the figures the monitor records over
// time. A name is what a series is known by in the time series database
// and in the requests for it; inside the program a figure is told from
// another by the value of this type.
package metricname

import "github.com/hotid/hls-monitor/internal/types/enum"

type domain struct{}

var set = enum.NewSet("metric name")

func (domain) Set() *enum.Set { return set }

type value = enum.Of[domain]

// Name is one metric name.
type Name struct{ value }

func def(name string) Name {
	_ = set // the set is there before the first value is
	return Name{enum.Def[domain](name)}
}

var (
	audioLevelDB                  = def("hls_audio_level_db")
	audioLoudnessLufs             = def("hls_audio_loudness_lufs")
	bufferSeconds                 = def("hls_buffer_seconds")
	checkBytes                    = def("hls_check_bytes")
	checkDurationSeconds          = def("hls_check_duration_seconds")
	checkLagSeconds               = def("hls_check_lag_seconds")
	checkOverloaded               = def("hls_check_overloaded")
	connectSeconds                = def("hls_connect_seconds")
	discontinuitySequence         = def("hls_discontinuity_sequence")
	dnsSeconds                    = def("hls_dns_seconds")
	entryRedirects                = def("hls_entry_redirects")
	entrySeconds                  = def("hls_entry_seconds")
	liveLatencySeconds            = def("hls_live_latency_seconds")
	mediaAudioBitrateBps          = def("hls_media_audio_bitrate_bps")
	mediaAVOffsetSeconds          = def("hls_media_av_offset_seconds")
	mediaCcErrors                 = def("hls_media_cc_errors")
	mediaDurationDiffSeconds      = def("hls_media_duration_diff_seconds")
	mediaFps                      = def("hls_media_fps")
	mediaGopSeconds               = def("hls_media_gop_seconds")
	mediaHdr                      = def("hls_media_hdr")
	mediaHeight                   = def("hls_media_height")
	mediaKeyframeStart            = def("hls_media_keyframe_start")
	mediaPTSGapSeconds            = def("hls_media_pts_gap_seconds")
	mediaSequence                 = def("hls_media_sequence")
	mediaSyncErrors               = def("hls_media_sync_errors")
	mediaVideoBitrateBps          = def("hls_media_video_bitrate_bps")
	mediaWidth                    = def("hls_media_width")
	pdtDriftSeconds               = def("hls_pdt_drift_seconds")
	pictureChanged                = def("hls_picture_changed")
	pictureLuma                   = def("hls_picture_luma")
	playlistAdMarkers             = def("hls_playlist_ad_markers")
	playlistAgeSeconds            = def("hls_playlist_age_seconds")
	playlistCacheHit              = def("hls_playlist_cache_hit")
	playlistConnectSeconds        = def("hls_playlist_connect_seconds")
	playlistDiscontinuities       = def("hls_playlist_discontinuities")
	playlistDNSSeconds            = def("hls_playlist_dns_seconds")
	playlistEncrypted             = def("hls_playlist_encrypted")
	playlistGapSegments           = def("hls_playlist_gap_segments")
	playlistHTTPStatus            = def("hls_playlist_http_status")
	playlistLive                  = def("hls_playlist_live")
	playlistNewSegments           = def("hls_playlist_new_segments")
	playlistParts                 = def("hls_playlist_parts")
	playlistRedirects             = def("hls_playlist_redirects")
	playlistSegments              = def("hls_playlist_segments")
	playlistSizeBytes             = def("hls_playlist_size_bytes")
	playlistSourceChanged         = def("hls_playlist_source_changed")
	playlistStaleSeconds          = def("hls_playlist_stale_seconds")
	playlistTLSSeconds            = def("hls_playlist_tls_seconds")
	playlistTotalSeconds          = def("hls_playlist_total_seconds")
	playlistTTFBSeconds           = def("hls_playlist_ttfb_seconds")
	playlistUp                    = def("hls_playlist_up")
	playlistUpdateIntervalSeconds = def("hls_playlist_update_interval_seconds")
	playlistWindowSeconds         = def("hls_playlist_window_seconds")
	renditions                    = def("hls_renditions")
	segmentBitrateBps             = def("hls_segment_bitrate_bps")
	segmentBitrateRatio           = def("hls_segment_bitrate_ratio")
	segmentConnectSeconds         = def("hls_segment_connect_seconds")
	segmentDNSSeconds             = def("hls_segment_dns_seconds")
	segmentDownloadBps            = def("hls_segment_download_bps")
	segmentDownloadBpsMin         = def("hls_segment_download_bps_min")
	segmentDownloadRatio          = def("hls_segment_download_ratio")
	segmentDurationMaxSeconds     = def("hls_segment_duration_max_seconds")
	segmentDurationMinSeconds     = def("hls_segment_duration_min_seconds")
	segmentErrors                 = def("hls_segment_errors")
	segmentSizeBytes              = def("hls_segment_size_bytes")
	segmentSourceChanged          = def("hls_segment_source_changed")
	segmentTLSSeconds             = def("hls_segment_tls_seconds")
	segmentTotalSeconds           = def("hls_segment_total_seconds")
	segmentTotalSecondsMax        = def("hls_segment_total_seconds_max")
	segmentTTFBSeconds            = def("hls_segment_ttfb_seconds")
	segmentsChecked               = def("hls_segments_checked")
	segmentsMissed                = def("hls_segments_missed")
	segmentsSkipped               = def("hls_segments_skipped")
	serverOutOfOrder              = def("hls_server_out_of_order")
	serverRequests                = def("hls_server_requests")
	serverRetransmits             = def("hls_server_retransmits")
	serverRTTSeconds              = def("hls_server_rtt_seconds")
	stalls                        = def("hls_stalls")
	startupSeconds                = def("hls_startup_seconds")
	status                        = def("hls_status")
	streamStatus                  = def("hls_stream_status")
	subtitleCues                  = def("hls_subtitle_cues")
	subtitleCuesHour              = def("hls_subtitle_cues_hour")
	targetDurationSeconds         = def("hls_target_duration_seconds")
	tcpOutOfOrder                 = def("hls_tcp_out_of_order")
	tcpRetransmits                = def("hls_tcp_retransmits")
	tcprttSeconds                 = def("hls_tcp_rtt_seconds")
	tlsCertExpirySeconds          = def("hls_tls_cert_expiry_seconds")
	tlsSeconds                    = def("hls_tls_seconds")
	tsDecodeLeadSeconds           = def("hls_ts_decode_lead_seconds")
	tspcrGapSeconds               = def("hls_ts_pcr_gap_seconds")
	up                            = def("hls_up")
	variantDeclaredBandwidthBps   = def("hls_variant_declared_bandwidth_bps")
	variantOffsetSeconds          = def("hls_variant_offset_seconds")
	variants                      = def("hls_variants")
	variantsEdgeSpreadSeconds     = def("hls_variants_edge_spread_seconds")
	selfCPUCores                  = def("hlsmon_self_cpu_cores")
	selfMemoryBytes               = def("hlsmon_self_memory_bytes")
	selfCPUAllCores               = def("hlsmon_self_cpu_all_cores")
)

func AudioLevelDB() Name { return audioLevelDB }

func AudioLoudnessLufs() Name { return audioLoudnessLufs }

func BufferSeconds() Name { return bufferSeconds }

func CheckBytes() Name { return checkBytes }

func CheckDurationSeconds() Name { return checkDurationSeconds }

func CheckLagSeconds() Name { return checkLagSeconds }

func CheckOverloaded() Name { return checkOverloaded }

func ConnectSeconds() Name { return connectSeconds }

func DiscontinuitySequence() Name { return discontinuitySequence }

func DNSSeconds() Name { return dnsSeconds }

func EntryRedirects() Name { return entryRedirects }

func EntrySeconds() Name { return entrySeconds }

func LiveLatencySeconds() Name { return liveLatencySeconds }

func MediaAudioBitrateBps() Name { return mediaAudioBitrateBps }

func MediaAVOffsetSeconds() Name { return mediaAVOffsetSeconds }

func MediaCcErrors() Name { return mediaCcErrors }

func MediaDurationDiffSeconds() Name { return mediaDurationDiffSeconds }

func MediaFps() Name { return mediaFps }

func MediaGopSeconds() Name { return mediaGopSeconds }

func MediaHdr() Name { return mediaHdr }

func MediaHeight() Name { return mediaHeight }

func MediaKeyframeStart() Name { return mediaKeyframeStart }

func MediaPTSGapSeconds() Name { return mediaPTSGapSeconds }

func MediaSequence() Name { return mediaSequence }

func MediaSyncErrors() Name { return mediaSyncErrors }

func MediaVideoBitrateBps() Name { return mediaVideoBitrateBps }

func MediaWidth() Name { return mediaWidth }

func PDTDriftSeconds() Name { return pdtDriftSeconds }

func PictureChanged() Name { return pictureChanged }

func PictureLuma() Name { return pictureLuma }

func PlaylistAdMarkers() Name { return playlistAdMarkers }

func PlaylistAgeSeconds() Name { return playlistAgeSeconds }

func PlaylistCacheHit() Name { return playlistCacheHit }

func PlaylistConnectSeconds() Name { return playlistConnectSeconds }

func PlaylistDiscontinuities() Name { return playlistDiscontinuities }

func PlaylistDNSSeconds() Name { return playlistDNSSeconds }

func PlaylistEncrypted() Name { return playlistEncrypted }

func PlaylistGapSegments() Name { return playlistGapSegments }

func PlaylistHTTPStatus() Name { return playlistHTTPStatus }

func PlaylistLive() Name { return playlistLive }

func PlaylistNewSegments() Name { return playlistNewSegments }

func PlaylistParts() Name { return playlistParts }

func PlaylistRedirects() Name { return playlistRedirects }

func PlaylistSegments() Name { return playlistSegments }

func PlaylistSizeBytes() Name { return playlistSizeBytes }

func PlaylistSourceChanged() Name { return playlistSourceChanged }

func PlaylistStaleSeconds() Name { return playlistStaleSeconds }

func PlaylistTLSSeconds() Name { return playlistTLSSeconds }

func PlaylistTotalSeconds() Name { return playlistTotalSeconds }

func PlaylistTTFBSeconds() Name { return playlistTTFBSeconds }

func PlaylistUp() Name { return playlistUp }

func PlaylistUpdateIntervalSeconds() Name { return playlistUpdateIntervalSeconds }

func PlaylistWindowSeconds() Name { return playlistWindowSeconds }

func Renditions() Name { return renditions }

func SegmentBitrateBps() Name { return segmentBitrateBps }

func SegmentBitrateRatio() Name { return segmentBitrateRatio }

func SegmentConnectSeconds() Name { return segmentConnectSeconds }

func SegmentDNSSeconds() Name { return segmentDNSSeconds }

func SegmentDownloadBps() Name { return segmentDownloadBps }

func SegmentDownloadBpsMin() Name { return segmentDownloadBpsMin }

func SegmentDownloadRatio() Name { return segmentDownloadRatio }

func SegmentDurationMaxSeconds() Name { return segmentDurationMaxSeconds }

func SegmentDurationMinSeconds() Name { return segmentDurationMinSeconds }

func SegmentErrors() Name { return segmentErrors }

func SegmentSizeBytes() Name { return segmentSizeBytes }

func SegmentSourceChanged() Name { return segmentSourceChanged }

func SegmentTLSSeconds() Name { return segmentTLSSeconds }

func SegmentTotalSeconds() Name { return segmentTotalSeconds }

func SegmentTotalSecondsMax() Name { return segmentTotalSecondsMax }

func SegmentTTFBSeconds() Name { return segmentTTFBSeconds }

func SegmentsChecked() Name { return segmentsChecked }

func SegmentsMissed() Name { return segmentsMissed }

func SegmentsSkipped() Name { return segmentsSkipped }

func ServerOutOfOrder() Name { return serverOutOfOrder }

func ServerRequests() Name { return serverRequests }

func ServerRetransmits() Name { return serverRetransmits }

func ServerRTTSeconds() Name { return serverRTTSeconds }

func Stalls() Name { return stalls }

func StartupSeconds() Name { return startupSeconds }

func Status() Name { return status }

func StreamStatus() Name { return streamStatus }

func SubtitleCues() Name { return subtitleCues }

func SubtitleCuesHour() Name { return subtitleCuesHour }

func TargetDurationSeconds() Name { return targetDurationSeconds }

func TCPOutOfOrder() Name { return tcpOutOfOrder }

func TCPRetransmits() Name { return tcpRetransmits }

func TCPRTTSeconds() Name { return tcprttSeconds }

func TLSCertExpirySeconds() Name { return tlsCertExpirySeconds }

func TLSSeconds() Name { return tlsSeconds }

func TSDecodeLeadSeconds() Name { return tsDecodeLeadSeconds }

func TSPCRGapSeconds() Name { return tspcrGapSeconds }

func Up() Name { return up }

func VariantDeclaredBandwidthBps() Name { return variantDeclaredBandwidthBps }

func VariantOffsetSeconds() Name { return variantOffsetSeconds }

func Variants() Name { return variants }

func VariantsEdgeSpreadSeconds() Name { return variantsEdgeSpreadSeconds }

func SelfCPUCores() Name { return selfCPUCores }

func SelfMemoryBytes() Name { return selfMemoryBytes }

func SelfCPUAllCores() Name { return selfCPUAllCores }

// Parse finds the metric name a name stands for.
func Parse(name string) (Name, error) {
	v, err := enum.Parse[domain](name)
	return Name{v}, err
}

// Values lists every metric name.
func Values() []Name {
	vs := enum.All[domain]()
	out := make([]Name, len(vs))
	for i, v := range vs {
		out[i] = Name{v}
	}
	return out
}
