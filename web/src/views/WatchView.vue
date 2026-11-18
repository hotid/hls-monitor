<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import type Hls from 'hls.js'
import { ApiError, api, basePath } from '@/api'
import { fmtAgo, fmtBits, fmtNumber } from '@/format'
import { t } from '@/i18n'
import type { FetchInfo, StreamDetail, WatchInfo, WatchSource, WatchVantage } from '@/types'
import TimeChart, { type ChartLine } from '@/components/TimeChart.vue'

const props = defineProps<{ id: number }>()

const info = ref<StreamDetail | null>(null)
const missing = ref(false)
const vantages = ref<WatchVantage[]>([])
/** Who requests the stream: the server itself or one of its agents. */
const vantage = ref(1)
const sources = ref<WatchSource[]>([])
const sourceIndex = ref(0)
const loadError = ref<string | null>(null)

const video = ref<HTMLVideoElement | null>(null)
let hls: Hls | null = null
/** New addresses were asked for after the old ones were refused. */
let renewed = false
let timer = 0

interface Quality {
  index: number
  label: string
}
const qualities = ref<Quality[]>([])
/** -1 lets the player choose. */
const quality = ref(-1)
const audios = ref<{ index: number; label: string }[]>([])
const audio = ref(0)

// ── What the player observes ──────────────────────────────────────────
const state = ref<'idle' | 'loading' | 'playing' | 'stalled' | 'failed'>('idle')
const playing = ref<{ label: string; bitrate: number } | null>(null)
const buffer = ref(0)
const latency = ref<number | null>(null)
const dropped = ref(0)
const upstream = ref('')
const stalls = ref(0)
interface Trouble {
  at: number
  text: string
  fatal: boolean
  n: number
  /** The library's own words, for whoever wants them. */
  detail: string
}
const troubles = ref<Trouble[]>([])
/** The browser cannot play what the stream carries. */
const unsupported = ref(false)

// ── History of the session, for the charts ────────────────────────────
// Every figure is sampled once a second: what happens per segment (how
// long each hop took, how fast it was) keeps its latest value in between,
// so that the lines are lines and not scattered dots.
const windowSec = 600
type Series = Record<string, [number, number][]>
let series: Series = {}
/** Latest values, written by the player's events and read by the sampler. */
let latest: Record<string, number> = {}
/** Counted since the last sample: losses, failures, stalls. */
let counted: Record<string, number> = {}
/** Running totals the counts are differences of. */
let totals: { link?: number; linkSegments?: number; ooo?: number; oooConn?: string; frames?: number } = {}
/** Retransmissions and segments sent, second by second, per hop: loss is their share over lossWindow seconds. */
let sent: Record<Hop, [number, number][]> = { origin: [], link: [], client: [] }
type Hop = 'origin' | 'link' | 'client'
const hops: Hop[] = ['origin', 'link', 'client']
const lossKey: Record<Hop, string> = { origin: 'lossOrigin', link: 'lossLink', client: 'lossClient' }
const lossWindow = 30
/** Round trips of the latest pings from this browser to the server. */
let pings: number[] = []
/** Set when the server reads the way to this browser from its own connection. */
let kernelClient = false
const sampled = ref(0)
const startedAt = ref(0)

function resetHistory() {
  series = {}
  latest = {}
  counted = {}
  totals = {}
  sent = { origin: [], link: [], client: [] }
  pings = []
  kernelClient = false
  startedAt.value = Math.floor(Date.now() / 1000)
  sampledSecond = 0
  sampled.value++
}

/** The last second a sample was written for. */
let sampledSecond = 0

// Called a few times a second; writes one sample for every second that has
// passed. A timer does not fire on the second: without filling in, the
// seconds it steps over are holes in the lines. A long silence — the tab
// was asleep — stays a hole: nothing was observed then.
function sample() {
  const now = Math.floor(Date.now() / 1000)
  if (now <= sampledSecond) return
  const from = now - sampledSecond > 5 ? now : sampledSecond + 1
  for (let sec = from; sec <= now; sec++) {
    const put = (key: string, v: number | undefined) => {
      if (v === undefined || !Number.isFinite(v)) return
      const s = (series[key] ??= [])
      s.push([sec, v])
      while (s.length && s[0]![0] < sec - windowSec) s.shift()
    }
    for (const [k, v] of Object.entries(latest)) put(k, v)
    // Counts are per second; a second in which nothing went wrong is a
    // zero, and what was counted belongs to the latest one.
    for (const k of ['retransmits', 'outOfOrder', 'failed', 'stalls', 'dropped']) put(k, sec === now ? (counted[k] ?? 0) : 0)
    // Loss is a share, and a second holds too few segments to take a
    // share of: it is taken over the last lossWindow seconds.
    for (const hop of hops) {
      const w = sent[hop]
      w.push(sec === now ? [counted[hop + 'Retransmits'] ?? 0, counted[hop + 'Segments'] ?? 0] : [0, 0])
      while (w.length > lossWindow) w.shift()
      const segments = w.reduce((n, s) => n + s[1], 0)
      if (segments > 0) put(lossKey[hop], w.reduce((n, s) => n + s[0], 0) / segments)
    }
  }
  counted = {}
  sampledSecond = now
  sampled.value++
}

/** What the server tells about how a fetch went, hop by hop. */
function fetchInfo(details: unknown): FetchInfo | null {
  const xhr = details as XMLHttpRequest | undefined
  if (typeof xhr?.getResponseHeader !== 'function') return null
  try {
    const raw = xhr.getResponseHeader('X-Fetch-Info')
    return raw ? (JSON.parse(atob(raw)) as FetchInfo) : null
  } catch {
    return null
  }
}

/** Figures that come with every answer, playlist or segment. */
function noteHops(info: FetchInfo | null, connection: string) {
  if (!info) return
  if (info.origin?.measured) {
    latest.rttOrigin = (info.origin.rtt_ms ?? 0) / 1000
    // The kernel counts for the life of a connection; a smaller figure,
    // or another server, is a new connection.
    const now = info.origin.out_of_order ?? 0
    if (totals.oooConn === connection && totals.ooo !== undefined && now >= totals.ooo) {
      counted.outOfOrder = (counted.outOfOrder ?? 0) + (now - totals.ooo)
    }
    totals.ooo = now
    totals.oooConn = connection
    // The requests to the origin are sent from the vantage point, and it
    // counts how many of their packets it had to send again.
    counted.originRetransmits = (counted.originRetransmits ?? 0) + (info.origin.retransmits ?? 0)
    counted.originSegments = (counted.originSegments ?? 0) + (info.origin.segments ?? 0)
  }
  if (info.link?.measured) {
    latest.rttLink = info.link.rtt_ms / 1000
    if (totals.link !== undefined && info.link.retransmits >= totals.link) {
      counted.retransmits = (counted.retransmits ?? 0) + (info.link.retransmits - totals.link)
      counted.linkRetransmits = (counted.linkRetransmits ?? 0) + (info.link.retransmits - totals.link)
    }
    totals.link = info.link.retransmits
    const segments = info.link.segments ?? 0
    if (totals.linkSegments !== undefined && segments >= totals.linkSegments) {
      counted.linkSegments = (counted.linkSegments ?? 0) + (segments - totals.linkSegments)
    }
    totals.linkSegments = segments
  }
  if (info.client) {
    // The server sends to this browser, so its kernel knows the way here.
    kernelClient = true
    latest.rttBrowser = info.client.rtt_ms / 1000
    counted.clientRetransmits = (counted.clientRetransmits ?? 0) + info.client.retransmits
    counted.clientSegments = (counted.clientSegments ?? 0) + info.client.segments
  }
}

// Behind a proxy the server cannot see the way to this browser, so the
// browser times it itself: an empty request, and how long its answer took.
// The smallest of the last few is the round trip, with the least of the
// server's own time in it.
let pinging = false
async function ping() {
  if (pinging || kernelClient) return
  pinging = true
  const url = basePath + '/api/v1/ping'
  try {
    const began = performance.now()
    const resp = await fetch(url, { cache: 'no-store', credentials: 'same-origin' })
    let took = performance.now() - began
    // The browser's own record leaves out the wait for a free connection.
    const entry = performance.getEntriesByName(new URL(url, location.href).href).at(-1) as PerformanceResourceTiming | undefined
    if (entry && entry.requestStart > 0 && entry.responseStart >= entry.requestStart) took = entry.responseStart - entry.requestStart
    performance.clearResourceTimings()
    if (resp.ok && !kernelClient) {
      pings = [...pings, took].slice(-5)
      latest.rttBrowser = Math.min(...pings) / 1000
    }
  } catch {
    // No answer: nothing to say this time.
  } finally {
    pinging = false
  }
}

// Shorter than this, the time a segment took says nothing of the speed.
const minSpeedTime = 0.02

interface ChartDef {
  id: string
  title: string
  hint: string
  unit: 's' | 'bps' | 'count' | 'pct'
  steps?: boolean
  /** Shown only once there is something on it: what it draws cannot be measured everywhere. */
  whenMeasured?: boolean
  lines: { key: string; label: string; agent?: boolean }[]
}
const chartDefs = computed<ChartDef[]>(() => [
  {
    id: 'buffer', title: t('watch.chartBuffer'), hint: t('watch.chartBufferHint'), unit: 's',
    lines: [
      { key: 'buffer', label: t('watch.buffer') },
      { key: 'latency', label: t('watch.latency') },
    ],
  },
  {
    id: 'rate', title: t('watch.chartRate'), hint: t('watch.chartRateHint'), unit: 'bps',
    lines: [
      { key: 'declared', label: t('watch.lineDeclared') },
      { key: 'actual', label: t('watch.lineActual') },
    ],
  },
  {
    id: 'speed', title: t('watch.chartSpeed'), hint: t('watch.chartSpeedHint'), unit: 'bps',
    lines: [
      { key: 'speedOrigin', label: t('watch.lineSpeedOrigin') },
      { key: 'speedRelay', label: t('watch.lineSpeedRelay'), agent: true },
      { key: 'speedBrowser', label: t('watch.lineSpeedBrowser') },
      { key: 'estimate', label: t('watch.lineEstimate') },
    ],
  },
  {
    id: 'delay', title: t('watch.chartDelay'), hint: t('watch.chartDelayHint'), unit: 's',
    lines: [
      { key: 'originTTFB', label: t('watch.lineOriginTTFB') },
      { key: 'originBody', label: t('watch.lineOriginBody') },
      { key: 'relayWait', label: t('watch.lineRelayWait'), agent: true },
      { key: 'relayUpload', label: t('watch.lineRelayUpload'), agent: true },
      { key: 'browser', label: t('watch.lineBrowser') },
    ],
  },
  {
    id: 'rtt', title: t('watch.chartRTT'), hint: t('watch.chartRTTHint'), unit: 's',
    lines: [
      { key: 'rttOrigin', label: t('watch.lineRTTOrigin') },
      { key: 'rttLink', label: t('watch.lineRTTLink'), agent: true },
      { key: 'rttBrowser', label: t('watch.lineRTTBrowser') },
    ],
  },
  {
    id: 'loss-share', title: t('watch.chartLossShare'), hint: t('watch.chartLossShareHint'), unit: 'pct', whenMeasured: true,
    lines: [
      { key: 'lossOrigin', label: vantage.value !== 1 ? t('watch.lineLossOriginAgent') : t('watch.lineLossOriginServer') },
      { key: 'lossLink', label: t('watch.lineLossLink'), agent: true },
      { key: 'lossClient', label: t('watch.lineLossClient') },
    ],
  },
  {
    id: 'loss', title: t('watch.chartLoss'), hint: t('watch.chartLossHint'), unit: 'count', steps: true,
    lines: [
      { key: 'retransmits', label: t('watch.lineRetransmits'), agent: true },
      { key: 'outOfOrder', label: t('watch.lineOutOfOrder') },
      { key: 'failed', label: t('watch.lineFailed') },
      { key: 'stalls', label: t('watch.stalls') },
      { key: 'dropped', label: t('watch.dropped') },
    ],
  },
])
const charts = computed(() => {
  void sampled.value
  const to = Math.floor(Date.now() / 1000)
  // A window that grows with the session up to its full length.
  const from = Math.max(startedAt.value, to - windowSec)
  const viaAgent = vantage.value !== 1
  return chartDefs.value
    .map((c) => ({
      ...c,
      from,
      to: Math.max(to, from + 60),
      lines: c.lines.filter((l) => viaAgent || !l.agent).map((l): ChartLine => ({ label: l.label, points: series[l.key] ?? [] })),
    }))
    // What cannot be measured here is not shown at all: neither a line
    // without a single point, nor a chart without lines.
    .map((c) => (c.whenMeasured ? { ...c, lines: c.lines.filter((l) => l.points.length) } : c))
    .filter((c) => !c.whenMeasured || c.lines.length)
})

const source = computed(() => sources.value[sourceIndex.value])
/** The server asked directly passed the request on: the one that answers. */
const passedOn = computed(() => {
  const asked = source.value
  const got = upstream.value.split(' ')[0] ?? ''
  if (!asked || asked.kind !== 'server' || !asked.host || !got) return ''
  const bare = (h: string) => h.replace(/:\d+$/, '').toLowerCase()
  return bare(got) === bare(asked.host) ? '' : bare(got)
})

function vantageLabel(v: WatchVantage): string {
  const name = v.local ? t('common.serverLocal') : v.name
  return v.available ? name : `${name} — ${v.why === 'too_old' ? t('watch.agentTooOld') : t('watch.agentOffline')}`
}

async function loadSources() {
  const info = await api.get<WatchInfo>(`/api/v1/streams/${props.id}/watch?agent=${vantage.value}`)
  vantages.value = info.vantages
  sources.value = info.sources
  if (sourceIndex.value >= info.sources.length) sourceIndex.value = 0
}

async function changeVantage() {
  stop()
  try {
    await loadSources()
    await start()
  } catch (e) {
    loadError.value = e instanceof Error ? e.message : String(e)
  }
}

function sourceLabel(s: WatchSource): string {
  if (s.kind === 'configured') return t('watch.asConfigured')
  const name = s.software ? `${s.host} · ${s.software}` : (s.host ?? '')
  return s.seen ? name : t('watch.serverNotSeen', { name })
}

function levelLabel(height: number, width: number, bitrate: number, name?: string): string {
  const size = height ? `${height}p` : name || t('watch.audioOnly')
  return bitrate ? `${size} · ${fmtBits(bitrate)}` : width ? `${width}×${height}` : size
}

function note(text: string, fatal: boolean, detail = '') {
  const last = troubles.value[0]
  if (last && last.text === text) {
    last.n++
    last.at = Date.now()
    last.fatal ||= fatal
    return
  }
  troubles.value = [{ at: Date.now(), text, fatal, n: 1, detail }, ...troubles.value].slice(0, 8)
}

/** Ticks of the timer, and how many of them pass between pings. */
let ticks = 0
const pingEvery = 8

function stop() {
  window.clearInterval(timer)
  hls?.destroy()
  hls = null
  const el = video.value
  if (el) {
    el.removeAttribute('src')
    el.load()
  }
  state.value = 'idle'
  playing.value = null
  qualities.value = []
  audios.value = []
  upstream.value = ''
  buffer.value = 0
  latency.value = null
}

async function start() {
  stop()
  const src = source.value
  const el = video.value
  if (!src || !el) return
  troubles.value = []
  unsupported.value = false
  stalls.value = 0
  dropped.value = 0
  quality.value = -1
  state.value = 'loading'
  resetHistory()

  // The player is a large library needed on this page only.
  const { default: HlsLib } = await import('hls.js')
  if (source.value !== src) return // another source was chosen meanwhile
  if (!HlsLib.isSupported()) {
    unsupported.value = true
    state.value = 'failed'
    note(t('watch.browserCannotPlay'), true)
    return
  }
  const player = new HlsLib({ liveDurationInfinity: true, backBufferLength: 30 })
  hls = player

  player.on(HlsLib.Events.MANIFEST_PARSED, () => {
    qualities.value = player.levels
      .map((l, index) => ({ index, label: levelLabel(l.height, l.width, l.bitrate, l.name) }))
      .sort((a, b) => (player.levels[b.index]?.bitrate ?? 0) - (player.levels[a.index]?.bitrate ?? 0))
    el.play().catch(() => {
      /* the browser wants a click first: the controls are there */
    })
  })
  player.on(HlsLib.Events.AUDIO_TRACKS_UPDATED, () => {
    audios.value = player.audioTracks.map((a, index) => ({ index, label: [a.name, a.lang].filter(Boolean).join(' · ') || String(index + 1) }))
    audio.value = Math.max(player.audioTrack, 0)
  })
  player.on(HlsLib.Events.LEVEL_SWITCHED, (_e, data) => {
    const l = player.levels[data.level]
    if (l) {
      playing.value = { label: levelLabel(l.height, l.width, 0, l.name), bitrate: l.bitrate }
      latest.declared = l.bitrate
    }
  })
  player.on(HlsLib.Events.LEVEL_LOADED, (_e, data) => {
    const xhr = data.networkDetails as XMLHttpRequest | undefined
    noteHops(fetchInfo(xhr), (typeof xhr?.getResponseHeader === 'function' && xhr.getResponseHeader('X-Upstream')) || '')
  })
  player.on(HlsLib.Events.FRAG_LOADED, (_e, data) => {
    renewed = false
    const xhr = data.networkDetails as XMLHttpRequest | undefined
    const from = typeof xhr?.getResponseHeader === 'function' ? xhr.getResponseHeader('X-Upstream') : null
    if (from) upstream.value = from
    if (data.frag.type !== 'main') return

    // The last hop, as the browser saw it: until the first byte the server
    // was fetching, after it the segment travelled here.
    const st = data.frag.stats
    const bytes = st.total || st.loaded
    const travel = (st.loading.end - st.loading.first) / 1000
    latest.browser = Math.max(travel, 0)
    if (data.frag.duration > 0 && bytes) latest.actual = (bytes * 8) / data.frag.duration

    const info = fetchInfo(xhr)
    noteHops(info, from ?? '')
    // The speed of a hop is the size over the whole time the hop took,
    // waiting for the answer included. The time between the first and the
    // last byte alone is no measure: a browser is handed a small segment
    // in one piece, and that gave gigabits where there are none. What
    // took less than minSpeedTime is too short to tell a speed by.
    const speed = (size: number, seconds: number) => (size > 0 && seconds >= minSpeedTime ? (size * 8) / seconds : undefined)
    const behind = (info?.relay?.total_ms ?? info?.origin?.total_ms ?? 0) / 1000
    const here = speed(bytes, (st.loading.end - st.loading.start) / 1000 - behind)
    if (here !== undefined) latest.speedBrowser = here
    if (info?.origin) {
      const o = info.origin
      latest.originTTFB = o.ttfb_ms / 1000
      latest.originBody = Math.max(o.total_ms - o.ttfb_ms, 0) / 1000
      const fromOrigin = speed(o.bytes, o.total_ms / 1000)
      if (fromOrigin !== undefined) latest.speedOrigin = fromOrigin
    }
    if (info?.relay) {
      latest.relayWait = info.relay.wait_ms / 1000
      latest.relayUpload = info.relay.upload_ms / 1000
      const through = speed(info.relay.bytes, info.relay.upload_ms / 1000)
      if (through !== undefined) latest.speedRelay = through
    }
  })
  player.on(HlsLib.Events.ERROR, (_e, data) => {
    const code = data.response?.code
    let text: string = data.details
    switch (data.details) {
      case HlsLib.ErrorDetails.MANIFEST_LOAD_ERROR:
      case HlsLib.ErrorDetails.LEVEL_LOAD_ERROR:
      case HlsLib.ErrorDetails.AUDIO_TRACK_LOAD_ERROR:
        text = code ? t('watch.playlistAnswered', { code }) : t('watch.playlistDidNotLoad')
        break
      case HlsLib.ErrorDetails.MANIFEST_LOAD_TIMEOUT:
      case HlsLib.ErrorDetails.LEVEL_LOAD_TIMEOUT:
        text = t('watch.playlistTimedOut')
        break
      case HlsLib.ErrorDetails.FRAG_LOAD_ERROR:
        text = code ? t('watch.segmentAnswered', { code }) : t('watch.segmentDidNotLoad')
        break
      case HlsLib.ErrorDetails.FRAG_LOAD_TIMEOUT:
        text = t('watch.segmentTimedOut')
        break
      case HlsLib.ErrorDetails.BUFFER_STALLED_ERROR:
        stalls.value++
        counted.stalls = (counted.stalls ?? 0) + 1
        text = t('watch.ranOutOfBuffer')
        break
      case HlsLib.ErrorDetails.MANIFEST_INCOMPATIBLE_CODECS_ERROR:
      case HlsLib.ErrorDetails.BUFFER_ADD_CODEC_ERROR:
      case HlsLib.ErrorDetails.BUFFER_INCOMPATIBLE_CODECS_ERROR:
        unsupported.value = true
        text = t('watch.codecNotSupported')
        break
      case HlsLib.ErrorDetails.FRAG_PARSING_ERROR:
        text = t('watch.segmentCannotBeParsed')
        break
      case HlsLib.ErrorDetails.MANIFEST_PARSING_ERROR:
      case HlsLib.ErrorDetails.LEVEL_PARSING_ERROR:
        text = t('watch.playlistCannotBeParsed')
        break
    }
    // The addresses given to the player have expired, or the server was
    // started anew with another key: ask for new ones, once.
    if (code === 403 && !renewed) {
      renewed = true
      void changeVantage()
      return
    }
    if (data.type === HlsLib.ErrorTypes.NETWORK_ERROR) counted.failed = (counted.failed ?? 0) + 1
    note(text, data.fatal, [data.details, data.error?.message, data.reason].filter(Boolean).join(': ').slice(0, 300))
    if (!data.fatal) return
    // The library gives up on these; what can be retried is retried once more.
    if (data.type === HlsLib.ErrorTypes.MEDIA_ERROR && !unsupported.value) {
      player.recoverMediaError()
      return
    }
    state.value = 'failed'
  })

  player.loadSource(basePath + src.url)
  player.attachMedia(el)

  timer = window.setInterval(() => {
    if (!hls) return
    const ranges = el.buffered
    let ahead = 0
    for (let i = 0; i < ranges.length; i++) {
      if (ranges.start(i) <= el.currentTime + 0.1 && ranges.end(i) > el.currentTime) ahead = ranges.end(i) - el.currentTime
    }
    buffer.value = ahead
    latency.value = Number.isFinite(hls.latency) && hls.latency > 0 ? hls.latency : null
    dropped.value = el.getVideoPlaybackQuality?.().droppedVideoFrames ?? 0
    latest.buffer = ahead
    if (latency.value !== null) latest.latency = latency.value
    if (Number.isFinite(hls.bandwidthEstimate) && hls.bandwidthEstimate > 0) latest.estimate = hls.bandwidthEstimate
    if (totals.frames !== undefined && dropped.value >= totals.frames) counted.dropped = (counted.dropped ?? 0) + (dropped.value - totals.frames)
    totals.frames = dropped.value
    if (++ticks % pingEvery === 0) void ping()
    sample()
    if (state.value !== 'failed') {
      state.value = el.readyState >= 3 && !el.paused ? 'playing' : el.readyState < 3 && el.currentTime > 0 ? 'stalled' : state.value === 'playing' ? 'stalled' : 'loading'
      if (el.paused && el.readyState >= 3) state.value = 'playing'
    }
  }, 500)
}

watch(quality, (q) => {
  if (hls) hls.currentLevel = q
})
watch(audio, (a) => {
  if (hls && hls.audioTrack !== a) hls.audioTrack = a
})
watch(sourceIndex, () => void start())

const stateLabel = computed(() => {
  switch (state.value) {
    case 'playing':
      return t('watch.statePlaying')
    case 'stalled':
      return t('watch.stateStalled')
    case 'failed':
      return t('watch.stateFailed')
    case 'loading':
      return t('watch.stateLoading')
    default:
      return t('watch.stateStopped')
  }
})
const stateClass = computed(() => (state.value === 'playing' ? 'ok' : state.value === 'failed' ? 'fail' : state.value === 'stalled' ? 'warn' : ''))

onMounted(async () => {
  try {
    info.value = await api.get<StreamDetail>(`/api/v1/streams/${props.id}`)
    await loadSources()
    await start()
  } catch (e) {
    if (e instanceof ApiError && e.status === 404) missing.value = true
    else loadError.value = e instanceof Error ? e.message : String(e)
  }
})
onBeforeUnmount(stop)
</script>

<template>
  <div class="page">
    <div v-if="missing" class="panel empty">
      <p class="strong">{{ t('common.noSuchStream') }}</p>
      <p>{{ t('common.itMayHaveBeenDeleted') }} <RouterLink :to="{ name: 'streams' }">{{ t('common.goToTheStreamList') }}</RouterLink></p>
    </div>
    <template v-else>
      <div class="page-head">
        <div class="title">
          <RouterLink :to="{ name: 'stream', params: { id } }" class="muted">{{ info?.name ?? t('common.stream') }}</RouterLink>
          <h1>{{ t('watch.title') }}</h1>
        </div>
        <span class="badge" :class="stateClass"><span class="dot" :class="stateClass" />{{ stateLabel }}</span>
        <span class="spacer" />
        <button v-if="state === 'idle' || state === 'failed'" class="btn primary" type="button" :disabled="!source" @click="start">{{ t('watch.play') }}</button>
        <button v-else class="btn" type="button" @click="stop">{{ t('common.stop') }}</button>
      </div>
      <p class="muted lead">{{ t('watch.lead') }}</p>
      <div v-if="loadError" class="notice error">{{ loadError }}</div>

      <div class="watch">
        <div class="main">
          <div class="screen panel">
            <video ref="video" controls muted playsinline />
          </div>
          <div v-if="startedAt" class="charts">
            <TimeChart
              v-for="c in charts"
              :key="c.id"
              :title="c.title"
              :hint="c.hint"
              :unit="c.unit"
              :lines="c.lines"
              :from="c.from"
              :to="c.to"
              :step="1"
              :steps="c.steps"
            />
          </div>
        </div>

        <div class="side">
          <div class="panel controls">
            <div class="field">
              <label for="watch-vantage">{{ t('watch.vantage') }}</label>
              <select id="watch-vantage" v-model.number="vantage" class="input" @change="changeVantage">
                <option v-for="v in vantages" :key="v.id" :value="v.id" :disabled="!v.available">{{ vantageLabel(v) }}</option>
              </select>
              <span class="help">{{ vantage === 1 ? t('watch.vantageServerHint') : t('watch.vantageAgentHint') }}</span>
            </div>
            <div class="field">
              <label for="watch-source">{{ t('watch.source') }}</label>
              <select id="watch-source" v-model.number="sourceIndex" class="input">
                <option v-for="(s, i) in sources" :key="s.url" :value="i">{{ sourceLabel(s) }}</option>
              </select>
              <span v-if="source" class="help">
                <template v-if="source.kind === 'configured'">{{ t('watch.asConfiguredHint') }}</template>
                <template v-else-if="source.seen">{{ t('watch.serverSeenHint', { ago: fmtAgo(source.last_seen ?? null) }) }}</template>
                <template v-else>{{ t('watch.serverNotSeenHint') }}</template>
              </span>
              <span v-if="source" class="help mono address">{{ source.address }}</span>
              <span v-if="passedOn" class="help passed-on">{{ t('watch.passedOn', { asked: source?.host ?? '', got: passedOn }) }}</span>
            </div>
            <div class="field">
              <label for="watch-quality">{{ t('watch.quality') }}</label>
              <select id="watch-quality" v-model.number="quality" class="input" :disabled="qualities.length < 2">
                <option :value="-1">{{ qualities.length < 2 ? (qualities[0]?.label ?? '—') : t('watch.qualityAuto') }}</option>
                <template v-if="qualities.length > 1">
                  <option v-for="q in qualities" :key="q.index" :value="q.index">{{ q.label }}</option>
                </template>
              </select>
            </div>
            <div v-if="audios.length > 1" class="field">
              <label for="watch-audio">{{ t('watch.audioTrack') }}</label>
              <select id="watch-audio" v-model.number="audio" class="input">
                <option v-for="a in audios" :key="a.index" :value="a.index">{{ a.label }}</option>
              </select>
            </div>
          </div>

          <div class="panel">
            <dl class="kv">
              <dt>{{ t('watch.answeredBy') }}</dt>
              <dd class="mono">{{ upstream || '—' }}</dd>
              <dt>{{ t('watch.nowPlaying') }}</dt>
              <dd>{{ playing ? playing.label + (playing.bitrate ? ' · ' + fmtBits(playing.bitrate) : '') : '—' }}</dd>
              <dt :title="t('watch.bufferHint')">{{ t('watch.buffer') }}</dt>
              <dd class="mono">{{ state === 'idle' ? '—' : fmtNumber(buffer, 1) + ' ' + t('format.s') }}</dd>
              <dt :title="t('watch.latencyHint')">{{ t('watch.latency') }}</dt>
              <dd class="mono">{{ latency === null ? '—' : fmtNumber(latency, 1) + ' ' + t('format.s') }}</dd>
              <dt>{{ t('watch.stalls') }}</dt>
              <dd class="mono" :style="stalls ? 'color: var(--accent-warm)' : ''">{{ stalls }}</dd>
              <dt :title="t('watch.droppedHint')">{{ t('watch.dropped') }}</dt>
              <dd class="mono">{{ dropped }}</dd>
            </dl>
          </div>

          <div v-if="unsupported" class="notice">{{ t('watch.unsupportedHint') }}</div>
          <div v-if="troubles.length" class="panel troubles">
            <div class="strong">{{ t('watch.whatWentWrong') }}</div>
            <div v-for="tr in troubles" :key="tr.at + tr.text" class="trouble" :class="{ fatal: tr.fatal }" :title="tr.detail">
              <span class="mono muted">{{ new Date(tr.at).toLocaleTimeString() }}</span>
              {{ tr.text }}<span v-if="tr.n > 1" class="muted"> ×{{ tr.n }}</span>
            </div>
          </div>
        </div>
      </div>
    </template>
  </div>
</template>

<style scoped>
.lead {
  max-width: 90ch;
  margin: -6px 0 14px;
}
.watch {
  display: grid;
  grid-template-columns: minmax(0, 1fr) 340px;
  gap: 14px;
  align-items: start;
}
.screen {
  padding: 0;
  overflow: hidden;
  background: #000;
}
.screen video {
  display: block;
  width: 100%;
  aspect-ratio: 16 / 9;
  background: #000;
}
.main {
  display: grid;
  gap: 14px;
  min-width: 0;
}
.charts {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(min(100%, 380px), 1fr));
  gap: 12px;
}
.side {
  display: grid;
  gap: 14px;
}
.side .panel {
  padding: 14px 16px;
}
.controls {
  display: grid;
  gap: 12px;
}
.address {
  word-break: break-all;
}
.passed-on {
  color: var(--accent-warm);
}
.troubles {
  display: grid;
  gap: 4px;
  font-size: 13px;
}
.trouble.fatal {
  color: #ff8a93;
}
@media (max-width: 900px) {
  .watch {
    grid-template-columns: 1fr;
  }
}
</style>
