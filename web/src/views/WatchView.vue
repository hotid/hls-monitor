<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import type Hls from 'hls.js'
import { ApiError, api, basePath } from '@/api'
import { fmtAgo, fmtBits, fmtNumber } from '@/format'
import { t } from '@/i18n'
import type { StreamDetail, WatchSource } from '@/types'

const props = defineProps<{ id: number }>()

const info = ref<StreamDetail | null>(null)
const missing = ref(false)
const sources = ref<WatchSource[]>([])
const sourceIndex = ref(0)
const loadError = ref<string | null>(null)

const video = ref<HTMLVideoElement | null>(null)
let hls: Hls | null = null
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

const source = computed(() => sources.value[sourceIndex.value])

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
    if (l) playing.value = { label: levelLabel(l.height, l.width, 0, l.name), bitrate: l.bitrate }
  })
  player.on(HlsLib.Events.FRAG_LOADED, (_e, data) => {
    const xhr = data.networkDetails as XMLHttpRequest | undefined
    const from = typeof xhr?.getResponseHeader === 'function' ? xhr.getResponseHeader('X-Upstream') : null
    if (from) upstream.value = from
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
    sources.value = await api.get<WatchSource[]>(`/api/v1/streams/${props.id}/watch`)
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
        <div class="screen panel">
          <video ref="video" controls muted playsinline />
        </div>

        <div class="side">
          <div class="panel controls">
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
