import { fmtBits, fmtBytes, fmtNumber, fmtSeconds } from '@/format'
import { perLocale, t } from '@/i18n'

export type Details = Record<string, unknown>

type Unit = 's' | 'bps' | 'bytes' | 'times' | 'pct' | 'count' | 'text'

interface Field {
  key: string
  label: string
  unit: Unit
}

/** What a problem means and how the monitor arrives at it. */
export const problemAbout = perLocale((): Record<string, { what: string; how: string }> => ({
  playback_stall: {
    what: t('incident.aViewerWouldSeeAStall'),
    how: t('incident.theMonitorRunsAModelOf'),
  },
  playlist_stale: {
    what: t('incident.thePlaylistOfALiveStream'),
    how: t('incident.howPlaylistStale'),
  },
  variants_out_of_sync: {
    what: t('incident.variantsApartWhat'),
    how: t('incident.variantsApartHow'),
  },
  sequence_jump: {
    what: t('incident.betweenTwoChecksSegmentsTheMonitor'),
    how: t('incident.howSequenceJump'),
  },
  sequence_renumbered: {
    what: t('incident.renumberedWhat'),
    how: t('incident.renumberedHow'),
  },
  sequence_regression: {
    what: t('incident.theMediaSequenceWentBackwardsThe'),
    how: t('incident.howSequenceRegression'),
  },
  segment_changed: {
    what: t('incident.theSameMediaSequenceNumberNow'),
    how: t('incident.theMonitorRemembersTheSegmentUris'),
  },
  playlist_http_error: {
    what: t('incident.theServerAnsweredThePlaylistRequest'),
    how: t('incident.theRequestGoesToTheStream'),
  },
  playlist_unreachable: {
    what: t('incident.noResponseToThePlaylistRequest'),
    how: t('incident.theRequestDidNotEndWith'),
  },
  bitrate_exceeds_declared: {
    what: t('incident.theSegmentIsNoticeablyHeavierThan'),
    how: t('incident.theSegmentSizeInBitsIs'),
  },
  picture_black: { what: t('incident.pictureBlackWhat'), how: t('incident.pictureLookHow') },
  picture_frozen: { what: t('incident.pictureFrozenWhat'), how: t('incident.pictureLookHow') },
  audio_silent: { what: t('incident.audioSilentWhat'), how: t('incident.pictureLookHow') },
  picture_slate: { what: t('incident.pictureSlateWhat'), how: t('incident.pictureSlateHow') },
  picture_loop: { what: t('incident.pictureLoopWhat'), how: t('incident.pictureLoopHow') },
  audio_loop: { what: t('incident.audioLoopWhat'), how: t('incident.audioLoopHow') },
  audio_channel_silent: { what: t('incident.audioChannelSilentWhat'), how: t('incident.audioChannelSilentHow') },
  audio_quiet: { what: t('incident.audioQuietWhat'), how: t('incident.audioQuietHow') },
  audio_undecodable: { what: t('incident.audioUndecodableWhat'), how: t('incident.audioUndecodableHow') },
  audio_absent: { what: t('incident.audioAbsentWhat'), how: t('incident.audioAbsentHow') },
  media_undecodable: { what: t('incident.mediaUndecodableWhat'), how: t('incident.mediaUndecodableHow') },
  declared_mismatch: { what: t('incident.declaredMismatchWhat'), how: t('incident.declaredMismatchHow') },
  ts_clock_error: { what: t('incident.tsClockWhat'), how: t('incident.tsClockHow') },
  subtitles_invalid: { what: t('incident.subtitlesInvalidWhat'), how: t('incident.subtitlesInvalidHow') },
  no_keyframe_start: {
    what: t('incident.theSegmentDoesNotStartWith'),
    how: t('incident.theFirstVideoFrameOfThe'),
  },
  target_duration_exceeded: {
    what: t('incident.theSegmentIsLongerThanThe'),
    how: t('incident.theSegmentDurationFromEXTINFIs'),
  },
  target_duration_changed: {
    what: t('incident.theOriginChangedEXTXTARGETDURATION'),
    how: t('incident.theValueIsComparedWithThe'),
  },
  pts_discontinuity: {
    what: t('incident.theTimestampsAtASegmentBoundary'),
    how: t('incident.theStartOfTheVideoIn'),
  },
  segment_http_error: {
    what: t('incident.theServerAnsweredTheSegmentRequest'),
    how: t('incident.theStatusCodeOfTheResponse'),
  },
  segment_unreachable: {
    what: t('incident.aSegmentFromThePlaylistCould'),
    how: t('incident.theSegmentRequestDidNotEnd'),
  },
  variants_changed: {
    what: t('incident.theListOfVariantsInThe'),
    how: t('incident.theListOfVariantUrisIs'),
  },
  playlist_source_changed: {
    what: t('incident.thePlaylistRequestWasAnsweredBy'),
    how: t('incident.theHostThatReturnedTheResponse'),
  },
  segment_source_changed: {
    what: t('incident.aDifferentServerStartedServingThe'),
    how: t('incident.theHostThatServedTheLast'),
  },
}))

/** Figures specific to a problem. */
const calculation = perLocale((): Field[] => [
  { key: 'played_for', label: t('incident.playerModelPlayedFor'), unit: 's' },
  { key: 'media_received', label: t('incident.videoReceivedInThatTime'), unit: 's' },
  { key: 'segments_received', label: t('incident.segmentsInThatTime'), unit: 'count' },
  { key: 'media_rate', label: t('incident.videoArrivalRate'), unit: 'pct' },
  { key: 'buffer_at_start', label: t('incident.bufferAtStart3EXTX'), unit: 's' },
  { key: 'buffer_before', label: t('incident.bufferBeforeTheLastCheck'), unit: 's' },
  { key: 'cycle_elapsed', label: t('incident.elapsedUntilTheLastCheck'), unit: 's' },
  { key: 'cycle_media', label: t('incident.videoReceivedInStep'), unit: 's' },
  { key: 'cycle_segments', label: t('incident.newSegments'), unit: 'count' },
  { key: 'cycle_failed_segments', label: t('incident.ofThemFailedToDownload'), unit: 'count' },
  { key: 'buffer_shortfall', label: t('incident.bufferShortfall'), unit: 's' },
  { key: 'unchanged_for', label: t('incident.playlistUnchangedFor'), unit: 's' },
  { key: 'unchanged_limit', label: t('incident.allowed'), unit: 's' },
  { key: 'last_segment_uri', label: t('incident.lastSegmentInThePlaylist'), unit: 'text' },
  { key: 'previous_last_sequence', label: t('incident.lastSegmentInThePreviousCheck'), unit: 'count' },
  { key: 'missed_segments', label: t('incident.segmentsMissed'), unit: 'count' },
  { key: 'slate', label: t('incident.slateName'), unit: 'text' },
  { key: 'decoder_says', label: t('incident.decoderSays'), unit: 'text' },
  { key: 'video_pictures', label: t('incident.videoPictures'), unit: 'count' },
  { key: 'video_packets', label: t('incident.videoPackets'), unit: 'count' },
  { key: 'video_kbit_s', label: t('incident.videoKbitS'), unit: 'count' },
  { key: 'segments_in_a_row', label: t('incident.segmentsInARow'), unit: 'count' },
  { key: 'distinct_keyframes', label: t('incident.distinctKeyframes'), unit: 'count' },
  { key: 'distinct_frames', label: t('incident.distinctFrames'), unit: 'count' },
  { key: 'bits_per_pixel', label: t('incident.bitsPerPixel'), unit: 'text' },
  { key: 'audio_frame_bytes', label: t('incident.audioFrameBytes'), unit: 'count' },
  { key: 'picture_luma', label: t('incident.pictureLuma'), unit: 'count' },
  { key: 'picture_dark_share', label: t('incident.pictureDarkShare'), unit: 'pct' },
  { key: 'audio_level_db', label: t('incident.audioLevel'), unit: 'count' },
  { key: 'quiet_below_db', label: t('incident.quietBelow'), unit: 'count' },
  { key: 'left_level_db', label: t('incident.leftLevel'), unit: 'count' },
  { key: 'right_level_db', label: t('incident.rightLevel'), unit: 'count' },
  { key: 'audio_frames', label: t('incident.audioFrames'), unit: 'count' },
  { key: 'looks_in_a_row', label: t('incident.looksInARow'), unit: 'count' },
  { key: 'since', label: t('incident.firstSeenSo'), unit: 'text' },
  { key: 'looked_at', label: t('incident.lastLookedAt'), unit: 'text' },
  { key: 'segment_bitrate', label: t('incident.segmentBitrate'), unit: 'bps' },
  { key: 'declared_bandwidth', label: t('incident.declaredInBANDWIDTH'), unit: 'bps' },
  { key: 'bitrate_ratio', label: t('incident.ratioToDeclared'), unit: 'times' },
  { key: 'bitrate_ratio_limit', label: t('incident.threshold'), unit: 'times' },
  { key: 'target_duration_before', label: t('incident.extXTARGETDURATIONWas'), unit: 's' },
  { key: 'allowed_excess', label: t('incident.tolerance'), unit: 's' },
  { key: 'uri_before', label: t('incident.uriWas'), unit: 'text' },
  { key: 'uri_now', label: t('incident.uriNow'), unit: 'text' },
  { key: 'segment_sequence', label: t('incident.mediaSequenceNumber'), unit: 'count' },
  { key: 'segment_url', label: t('incident.segmentURI'), unit: 'text' },
  { key: 'segment_uri', label: t('incident.segmentURI'), unit: 'text' },
  { key: 'segment_duration', label: t('incident.segmentDuration'), unit: 's' },
  { key: 'segment_http_status', label: t('incident.segmentResponseStatus'), unit: 'count' },
  { key: 'segment_error', label: t('incident.segmentRequestError'), unit: 'text' },
  { key: 'segment_bytes', label: t('common.segmentSize'), unit: 'bytes' },
  { key: 'segment_fetched_bytes', label: t('incident.ofThemDownloaded'), unit: 'bytes' },
  { key: 'segment_server', label: t('incident.segmentServedBy'), unit: 'text' },
  { key: 'segment_software', label: t('incident.segmentServerSoftware'), unit: 'text' },
])

/** Circumstances of the check, the same for every problem of a playlist. */
const circumstances = perLocale((): Field[] => [
  { key: 'playlist_url', label: t('incident.playlistRequested'), unit: 'text' },
  { key: 'playlist_redirects', label: t('common.redirects'), unit: 'text' },
  { key: 'playlist_final_url', label: t('incident.responseCameFromURL'), unit: 'text' },
  { key: 'playlist_server', label: t('incident.playlistServedBy'), unit: 'text' },
  { key: 'playlist_software', label: t('incident.serverSoftware'), unit: 'text' },
  { key: 'playlist_http_status', label: t('incident.playlistResponseStatus'), unit: 'count' },
  { key: 'playlist_error', label: t('incident.playlistRequestError'), unit: 'text' },
  { key: 'playlist_seconds', label: t('incident.playlistRequestTime'), unit: 's' },
  { key: 'playlist_bytes', label: t('incident.playlistSize'), unit: 'bytes' },
  { key: 'playlist_server_before', label: t('incident.playlistServerBefore'), unit: 'text' },
  { key: 'variants_edge_spread', label: t('incident.variantsEdgeSpread'), unit: 's' },
  { key: 'variants_edge_spread_limit', label: t('incident.variantsEdgeSpreadLimit'), unit: 's' },
  { key: 'rendition_offset', label: t('incident.renditionOffset'), unit: 's' },
  { key: 'rendition_offset_limit', label: t('incident.renditionOffsetLimit'), unit: 's' },
  { key: 'segments_moved', label: t('incident.segmentsMoved'), unit: 'count' },
  { key: 'segments_moved_limit', label: t('incident.segmentsMovedLimit'), unit: 'count' },
  { key: 'compared_with', label: t('incident.comparedWith'), unit: 'text' },
  { key: 'since_previous_check', label: t('incident.sinceThePreviousCheck'), unit: 's' },
  { key: 'previous_playlist_keeps', label: t('incident.previousPlaylistKeeps'), unit: 's' },
  { key: 'failed_checks_between', label: t('incident.failedChecksBetween'), unit: 'count' },
  { key: 'last_failure', label: t('incident.lastFailure'), unit: 'text' },
  { key: 'target_duration', label: t('incident.extXTARGETDURATION'), unit: 's' },
  { key: 'playlist_segments', label: t('incident.segmentsInThePlaylist'), unit: 'count' },
  { key: 'first_sequence', label: t('incident.firstSegment'), unit: 'count' },
  { key: 'last_sequence', label: t('incident.lastSegment'), unit: 'count' },
  { key: 'playlist_window', label: t('incident.playlistWindow'), unit: 's' },
  { key: 'segment_duration_min', label: t('incident.shortestSegment'), unit: 's' },
  { key: 'segment_duration_max', label: t('incident.longestSegment'), unit: 's' },
])

function show(v: unknown, unit: Unit): string {
  if (Array.isArray(v)) return v.map(String).join('\n')
  if (typeof v !== 'number') return String(v)
  switch (unit) {
    case 's':
      return fmtSeconds(v)
    case 'bps':
      return fmtBits(v)
    case 'bytes':
      return fmtBytes(v)
    case 'times':
      return '×' + fmtNumber(v, 2)
    case 'pct':
      return t('incident.ofRealTime', { n: fmtNumber(v * 100, 0) })
    default:
      return String(v)
  }
}

export interface Row {
  label: string
  value: string
  mono: boolean
}

function rows(d: Details, fields: Field[]): Row[] {
  const out: Row[] = []
  for (const f of fields) {
    const v = d[f.key]
    if (v === undefined || v === null || v === '') continue
    out.push({ label: f.label, value: show(v, f.unit), mono: f.unit === 'text' })
  }
  return out
}

/** Details that are for the monitor, not for the reader: what a clip is known by. */
const unshown = ['clip_signature', 'known_by', 'judged_by', 'silent_channel']

export function calculationRows(d: Details): Row[] {
  const known = new Set([...calculation(), ...circumstances()].map((f) => f.key).concat(unshown))
  const extra = Object.keys(d)
    .filter((k) => !known.has(k))
    .sort()
    .map((k) => ({ label: k, value: show(d[k], 'text'), mono: true }))
  return [...rows(d, calculation()), ...extra]
}

export function circumstanceRows(d: Details): Row[] {
  return rows(d, circumstances())
}

function num(d: Details, key: string): number | undefined {
  const v = d[key]
  return typeof v === 'number' ? v : undefined
}

/** What follows from the figures, in words. */
export function conclusion(kind: string, d: Details): string[] {
  const out: string[] = []
  const s = (v: number) => fmtSeconds(v)
  if (d.judged_by) out.push(t('incident.judgedBySizes'))
  if (kind === 'media_undecodable') {
    const pictures = num(d, 'video_pictures')
    const packets = num(d, 'video_packets')
    // Video has a picture to a packet, or one to two where fields come apart.
    if (pictures !== undefined && packets !== undefined && packets >= 20 && pictures * 4 < packets) {
      out.push(t('incident.noPicturesInTheVideoTrack', { rate: num(d, 'video_kbit_s') ?? '?', pictures, packets }))
    }
  }
  if (kind === 'playback_stall') {
    const wall = num(d, 'played_for')
    const media = num(d, 'media_received')
    const rate = num(d, 'media_rate')
    if (wall !== undefined && media !== undefined && rate !== undefined) {
      out.push(t('incident.inOfPlaybackTheOriginPublished', { wall: s(wall), media: s(media), pct: fmtNumber(rate * 100, 0) }))
      if (rate < 0.9) out.push(t('incident.theOriginProducesVideoSlowerThan'))
      else out.push(t('incident.onAverageVideoArrivedAlmostOn'))
    }
    const before = num(d, 'buffer_before')
    const elapsed = num(d, 'cycle_elapsed')
    const gained = num(d, 'cycle_media')
    const short = num(d, 'buffer_shortfall')
    if (before !== undefined && elapsed !== undefined && gained !== undefined && short !== undefined) {
      out.push(t('incident.lastStepTheBufferHeldAnd', { before: s(before), elapsed: s(elapsed), gained: s(gained), short: s(short) }))
    }
    if ((num(d, 'cycle_failed_segments') ?? 0) > 0) out.push(t('incident.someOfTheNewSegmentsFailed'))
  }
  if (kind === 'sequence_jump') {
    const since = num(d, 'since_previous_check')
    const window = num(d, 'playlist_window')
    const longest = num(d, 'segment_duration_max')
    const failed = num(d, 'failed_checks_between') ?? 0
    if (since !== undefined && failed > 0) {
      out.push(t('incident.jumpAfterOutage', { since: s(since), n: failed, why: String(d.last_failure ?? '') }))
      out.push(t('incident.jumpIsConsequence'))
    } else if (since !== undefined && num(d, 'previous_playlist_keeps') !== undefined) {
      // The pause was chosen by what the playlist promised at the check before.
      const promised = num(d, 'previous_playlist_keeps') ?? 0
      out.push(t('incident.jumpPromised', { since: s(since), keeps: s(promised) }))
      out.push(since > promised ? t('incident.theCheckCameLaterThanA') : t('incident.jumpBeyondWindow'))
    } else if (since !== undefined && window !== undefined) {
      const keeps = window - (longest ?? 0)
      out.push(t('incident.passedBetweenChecksASegmentIs', { since: s(since), keeps: s(Math.max(keeps, 0)), window: s(window) }))
      out.push(since > keeps ? t('incident.theCheckCameLaterThanA') : t('incident.theCheckCameWithinTheWindow'))
    }
  }
  if (kind === 'playlist_stale') {
    const stale = num(d, 'unchanged_for')
    const limit = num(d, 'unchanged_limit')
    if (stale !== undefined && limit !== undefined) out.push(t('incident.thePlaylistWasUnchangedForThe', { stale: s(stale), limit: s(limit) }))
  }
  if (kind === 'bitrate_exceeds_declared') {
    const bytes = num(d, 'segment_bytes')
    const dur = num(d, 'segment_duration')
    const rate = num(d, 'segment_bitrate')
    const declared = num(d, 'declared_bandwidth')
    if (bytes !== undefined && dur !== undefined && rate !== undefined && declared !== undefined) {
      out.push(t('incident.inIsAgainstTheDeclared', { bytes: fmtBytes(bytes), dur: s(dur), rate: fmtBits(rate), declared: fmtBits(declared), ratio: fmtNumber(rate / declared, 2) }))
    }
  }
  return out
}
