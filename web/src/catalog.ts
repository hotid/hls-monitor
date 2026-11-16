// What the monitor measures and what it complains about, in the words
// shown to the user.

import { fmtBits, fmtBytes, fmtNumber, fmtSeconds } from './format'
import { perLocale, t } from './i18n'
import type { Level, Status } from './types'

export const statusLabel = perLocale((): Record<Status, string> => ({
  ok: t('common.ok'),
  warn: t('common.warning'),
  fail: t('common.failure'),
  nodata: t('catalog.noData'),
  disabled: t('common.disabled'),
}))

export const levelLabel = perLocale((): Record<string, string> => ({
  playlist: t('catalog.playlistsOnly'),
  header: t('catalog.playlistsAndTheStartOfEach'),
  segments: t('catalog.playlistsAndWholeSegments'),
  container: t('catalog.playlistsWholeSegmentsAndMediaParsing'),
}))

export const kindLabel = perLocale((): Record<string, string> => ({
  master: t('common.multivariantPlaylist'),
  media: t('catalog.mediaPlaylist'),
  video: t('common.video'),
  audio: t('common.audio'),
  subtitles: t('catalog.subtitles'),
}))

export const problemTitle = perLocale((): Record<string, string> => ({
  playlist_unreachable: t('catalog.playlistUnreachable'),
  playlist_http_error: t('catalog.playlistReturnsAnHTTPError'),
  playlist_parse_error: t('catalog.playlistCannotBeParsed'),
  playlist_syntax: t('catalog.playlistSyntaxViolation'),
  playlist_stale: t('catalog.playlistIsNotUpdating'),
  playlist_empty: t('catalog.playlistIsEmpty'),
  sequence_regression: t('catalog.mediaSequenceWentBackwards'),
  sequence_jump: t('catalog.segmentsMissedBetweenChecks'),
  segment_changed: t('catalog.segmentReplaced'),
  target_duration_exceeded: t('catalog.segmentLongerThanEXTXTARGETDURATION'),
  target_duration_changed: t('catalog.extXTARGETDURATIONChanged'),
  pdt_discontinuity: t('catalog.extXPROGRAMDATETIMEJump'),
  stream_ended: t('catalog.streamEnded'),
  variants_changed: t('catalog.setOfVariantsChanged'),
  variants_out_of_sync: t('catalog.variantsOutOfSync'),
  key_unavailable: t('catalog.encryptionKeyUnavailable'),
  init_unavailable: t('catalog.mediaInitializationSectionUnavailable'),
  cert_expiring: t('catalog.tlsCertificateExpiring'),
  segment_unreachable: t('catalog.segmentUnreachable'),
  segment_http_error: t('catalog.segmentReturnsAnHTTPError'),
  segment_truncated: t('catalog.segmentTruncated'),
  segment_slow: t('catalog.segmentDownloadsSlowerThanRealTime'),
  playback_stall: t('catalog.aPlayerWouldStallForBuffering'),
  bitrate_exceeds_declared: t('catalog.bitrateExceedsDeclared'),
  media_parse_error: t('catalog.mediaCannotBeParsed'),
  ts_sync_error: t('catalog.mpegTSSyncLoss'),
  ts_continuity_error: t('catalog.continuityCounterErrors'),
  ts_transport_error: t('catalog.packetsWithTransportErrorIndicatorSet'),
  ts_no_pat_pmt: t('catalog.noPATPMTInSegment'),
  no_keyframe_start: t('catalog.segmentDoesNotStartWithA'),
  duration_mismatch: t('catalog.durationDoesNotMatchThePlaylist'),
  pts_discontinuity: t('catalog.timestampGap'),
  av_desync: t('catalog.audioVideoDesync'),
  media_params_changed: t('catalog.videoParametersChanged'),
  playlist_source_changed: t('catalog.playlistServedByADifferentServer'),
  segment_source_changed: t('catalog.segmentsServedByADifferentServer'),
}))

/** Kinds that record a fact rather than report trouble. */
export const infoKinds = new Set(['playlist_source_changed', 'segment_source_changed', 'target_duration_changed'])

export function problemName(kind: string): string {
  return problemTitle()[kind] ?? kind
}

export type Unit = 's' | 'bps' | 'bytes' | 'ratio' | 'count' | 'px' | 'fps' | 'pct' | 'status'

const statusWord = perLocale(() => [t('common.okWord'), t('common.warningWord'), t('common.failureWord')])

export function fmtUnit(v: number | null | undefined, unit: Unit): string {
  if (v == null || !isFinite(v)) return '—'
  switch (unit) {
    case 's':
      return fmtSeconds(v)
    case 'bps':
      return fmtBits(v)
    case 'bytes':
      return fmtBytes(v)
    case 'ratio':
      return v.toFixed(2)
    case 'pct':
      return (v * 100).toFixed(v >= 0.9995 ? 0 : 1) + ' %'
    case 'fps':
      return fmtNumber(v, 2)
    case 'status':
      // Averaged over agents or variants the value may fall between two states.
      return statusWord()[Math.min(2, Math.max(0, Math.ceil(v - 1e-9)))] ?? ''
    default:
      return fmtNumber(v, 1)
  }
}

export interface ChartSeriesDef {
  metric: string
  label: string
}

export interface ChartDef {
  id: string
  title: string
  hint: string
  unit: Unit
  agg: 'avg' | 'max' | 'min' | 'sum'
  series: ChartSeriesDef[]
  /**
   * Check levels at which the chart has data; all levels when absent.
   * "header" reads only the beginning of segments, "segments" downloads
   * them whole without looking inside, "container" does both.
   */
  levels?: Level[]
  /** Shown instead of "no data" when the chart is empty for a known reason. */
  emptyHint?: string
  /** Draw as filled steps rather than lines. */
  steps?: boolean
  /** Fixed upper bound of the value axis. */
  max?: number
  /** Horizontal reference line. */
  threshold?: number
}

export interface ChartGroup {
  title: string
  charts: ChartDef[]
}

// With one metric a chart draws a line per agent and variant; with several
// it draws a line per metric, averaged over what is selected.
export const chartGroups = perLocale((): ChartGroup[] => [
  {
    title: t('catalog.availability'),
    charts: [
      {
        id: 'availability', title: t('catalog.playlistAvailability'), hint: t('catalog.shareOfSuccessfulPlaylistRequests'), unit: 'pct', agg: 'avg', max: 1,
        series: [{ metric: 'hls_playlist_up', label: t('common.playlist') }],
      },
      {
        id: 'status', title: t('common.status'), hint: t('catalog.worstPlaylistStatusPerStepOK'), unit: 'status', agg: 'max', steps: true, max: 2,
        series: [{ metric: 'hls_status', label: t('common.status') }],
      },
      {
        id: 'stale', title: t('catalog.playlistUnchanged'), hint: t('catalog.howLongThePlaylistHasNot'), unit: 's', agg: 'max',
        series: [{ metric: 'hls_playlist_stale_seconds', label: t('catalog.unchanged') }],
      },
      {
        id: 'latency', title: t('catalog.latencyBehindRealTime'), hint: t('catalog.fromTheEXTXPROGRAMDATE'), unit: 's', agg: 'avg',
        emptyHint: t('catalog.noDateTimeTagsNoLatency'),
        series: [{ metric: 'hls_live_latency_seconds', label: t('common.latency') }],
      },
    ],
  },
  {
    title: t('catalog.playback'),
    charts: [
      {
        id: 'buffer', title: t('catalog.playerBufferModel'), hint: t('catalog.bufferOfAPlayerRunningThree'), unit: 's', agg: 'min', levels: ['header', 'segments', 'container'],
        series: [{ metric: 'hls_buffer_seconds', label: t('catalog.buffer') }],
      },
      {
        id: 'ratio', title: t('catalog.downloadTimeToDuration'), hint: t('catalog.above1TheSegmentDownloadsSlower'), unit: 'ratio', agg: 'max', threshold: 1, levels: ['segments', 'container'],
        series: [{ metric: 'hls_segment_download_ratio', label: t('catalog.ratio') }],
      },
      {
        id: 'startup', title: t('catalog.startupTime'), hint: t('catalog.playlistsPlusTheFirstSegment'), unit: 's', agg: 'avg', levels: ['segments', 'container'],
        series: [{ metric: 'hls_startup_seconds', label: t('catalog.startup') }],
      },
      {
        id: 'errors', title: t('common.segmentErrors'), hint: t('catalog.segmentsThatCouldNotBeFetched'), unit: 'count', agg: 'sum', steps: true, levels: ['header', 'segments', 'container'],
        series: [{ metric: 'hls_segment_errors', label: t('common.errors') }],
      },
    ],
  },
  {
    title: t('catalog.network'),
    charts: [
      {
        id: 'playlist-net', title: t('catalog.playlistRequestByPhase'), hint: t('catalog.averageOverTheSelectedVariantsAnd'), unit: 's', agg: 'avg',
        series: [
          { metric: 'hls_playlist_dns_seconds', label: 'DNS' },
          { metric: 'hls_playlist_connect_seconds', label: t('catalog.connect') },
          { metric: 'hls_playlist_tls_seconds', label: 'TLS' },
          { metric: 'hls_playlist_ttfb_seconds', label: t('common.firstByte') },
          { metric: 'hls_playlist_total_seconds', label: t('common.total') },
        ],
      },
      {
        id: 'segment-net', title: t('catalog.segmentDownloadByPhase'), hint: t('catalog.averageOverTheSelectedVariantsAnd'), unit: 's', agg: 'avg', levels: ['header', 'segments', 'container'],
        series: [
          { metric: 'hls_segment_dns_seconds', label: 'DNS' },
          { metric: 'hls_segment_connect_seconds', label: t('catalog.connect') },
          { metric: 'hls_segment_tls_seconds', label: 'TLS' },
          { metric: 'hls_segment_ttfb_seconds', label: t('common.firstByte') },
          { metric: 'hls_segment_total_seconds', label: t('catalog.totalWhenDownloadedWhole') },
        ],
      },
      {
        id: 'source', title: t('common.answeringServerChanges'), hint: t('catalog.howManyTimesTheResponseCame'), unit: 'count', agg: 'sum', steps: true,
        series: [
          { metric: 'hls_playlist_source_changed', label: t('common.playlist') },
          { metric: 'hls_segment_source_changed', label: t('common.segments') },
        ],
      },
      {
        id: 'redirects', title: t('catalog.playlistRedirects'), hint: t('catalog.howManyRedirectsWereFollowedBefore'), unit: 'count', agg: 'max', steps: true,
        series: [{ metric: 'hls_playlist_redirects', label: t('common.redirects') }],
      },
      {
        id: 'segment-time', title: t('catalog.segmentDownloadTime'), hint: t('catalog.worstValuePerStep'), unit: 's', agg: 'max', levels: ['segments', 'container'],
        series: [{ metric: 'hls_segment_total_seconds_max', label: t('common.download') }],
      },
      {
        id: 'rate', title: t('catalog.downloadRate'), hint: t('catalog.minimumPerStep'), unit: 'bps', agg: 'min', levels: ['segments', 'container'],
        series: [{ metric: 'hls_segment_download_bps_min', label: t('catalog.rate') }],
      },
    ],
  },
  {
    title: t('common.playlist'),
    charts: [
      {
        id: 'update', title: t('catalog.playlistUpdateInterval'), hint: t('catalog.timeBetweenChanges'), unit: 's', agg: 'max',
        series: [{ metric: 'hls_playlist_update_interval_seconds', label: t('catalog.interval') }],
      },
      {
        id: 'window', title: t('catalog.windowDepth'), hint: t('catalog.totalDurationOfTheSegmentsIn'), unit: 's', agg: 'avg',
        series: [{ metric: 'hls_playlist_window_seconds', label: t('catalog.window') }],
      },
      {
        id: 'missed', title: t('catalog.missedSegments'), hint: t('catalog.leftThePlaylistBetweenChecks'), unit: 'count', agg: 'sum', steps: true,
        series: [{ metric: 'hls_segments_missed', label: t('catalog.missed') }],
      },
      {
        id: 'pdt', title: t('catalog.extXPROGRAMDATETIMEJumps'), hint: t('catalog.differenceBetweenTheDateTimeTags'), unit: 's', agg: 'max',
        emptyHint: t('catalog.noDateTimeTags'),
        series: [{ metric: 'hls_pdt_drift_seconds', label: t('catalog.jump') }],
      },
    ],
  },
  {
    title: t('catalog.media'),
    charts: [
      {
        id: 'bitrate', title: t('catalog.segmentBitrate'), hint: t('catalog.segmentSizeDividedByDuration'), unit: 'bps', agg: 'avg', levels: ['header', 'segments', 'container'],
        series: [{ metric: 'hls_segment_bitrate_bps', label: t('common.bitrate') }],
      },
      {
        id: 'bitrate-ratio', title: t('catalog.bitrateToDeclared'), hint: t('catalog.ratioToBANDWIDTHFromTheMultivariant'), unit: 'ratio', agg: 'max', threshold: 1, levels: ['header', 'segments', 'container'],
        series: [{ metric: 'hls_segment_bitrate_ratio', label: t('catalog.ratio') }],
      },
      {
        id: 'pts-gap', title: t('catalog.timestampGaps'), hint: t('catalog.betweenTheEndOfASegment'), unit: 's', agg: 'max', levels: ['header', 'container'],
        series: [{ metric: 'hls_media_pts_gap_seconds', label: t('catalog.gap') }],
      },
      {
        id: 'dur-diff', title: t('catalog.durationMediaMinusPlaylist'), hint: t('catalog.differenceFromEXTINF'), unit: 's', agg: 'max', levels: ['container'],
        series: [{ metric: 'hls_media_duration_diff_seconds', label: t('catalog.difference') }],
      },
      {
        id: 'av', title: t('catalog.videoOffsetRelativeToAudio'), hint: t('catalog.differenceBetweenTheStartTimesOf'), unit: 's', agg: 'avg', levels: ['header', 'container'],
        series: [{ metric: 'hls_media_av_offset_seconds', label: t('catalog.offset') }],
      },
      {
        id: 'gop', title: t('catalog.gopLength'), hint: t('catalog.longestDistanceBetweenKeyframes'), unit: 's', agg: 'max', levels: ['container'],
        series: [{ metric: 'hls_media_gop_seconds', label: 'GOP' }],
      },
      {
        id: 'fps', title: t('catalog.frameRate'), hint: t('catalog.measuredFromTimestamps'), unit: 'fps', agg: 'avg', levels: ['container'],
        series: [{ metric: 'hls_media_fps', label: t('common.fps') }],
      },
      {
        id: 'ts-errors', title: t('catalog.mpegTSErrors'), hint: t('catalog.continuityCounterAndSync'), unit: 'count', agg: 'sum', steps: true, levels: ['header', 'container'],
        series: [
          { metric: 'hls_media_cc_errors', label: t('catalog.continuity') },
          { metric: 'hls_media_sync_errors', label: t('catalog.sync') },
        ],
      },
    ],
  },
])

export function chartAvailable(def: ChartDef, level: string): boolean {
  return !def.levels || def.levels.includes(level as Level)
}
