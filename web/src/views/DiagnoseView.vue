<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, reactive, ref } from 'vue'
import { ApiError, api, stream as readStream } from '@/api'
import { kindLabel, problemName } from '@/catalog'
import { fmtBytes, fmtDuration, fmtMs, fmtSeconds } from '@/format'
import { dateLocale, t } from '@/i18n'
import { conclusion } from '@/incident'
import { useLive } from '@/stores/live'
import type { DiagnoseEvent, Endpoint, Incident, Level, ProbeProblem, SegmentDetail, StreamDetail } from '@/types'
import IncidentDetails from '@/components/IncidentDetails.vue'
import PlaylistPair from '@/components/PlaylistPair.vue'

const props = defineProps<{ id: number }>()

const levels: { id: Level; label: string; hint: string }[] = [
  { id: 'playlist', label: t('common.playlists'), hint: t('diagnose.playlistsOnlyAlmostNoTrafficBut') },
  { id: 'header', label: t('diagnose.startOfSegments'), hint: t('diagnose.playlistsAndTheFirstKilobytesOf') },
  { id: 'segments', label: t('diagnose.wholeSegments'), hint: t('diagnose.everyNewSegmentIsDownloadedIn') },
  { id: 'container', label: t('diagnose.withMediaParsing'), hint: t('diagnose.wholeSegmentsAndParsingOfTheir') },
]
const lengths = [1, 2, 5, 10]

const info = ref<StreamDetail | null>(null)
const missing = ref(false)
const level = ref<Level>('header')
const minutes = ref(Number(localStorage.getItem('diagnose-minutes')) || 2)

// Where the diagnosis is made from: the server itself (1), or an agent.
const live = useLive()
const vantage = ref(1)
const vantages = computed(() =>
  (live.overview?.agents ?? []).map((a) => ({
    id: a.id,
    label: a.kind === 'local' ? t('common.serverLocal') : a.name,
    // An agent must be there and new enough to take such work.
    available: a.kind === 'local' || (a.enabled && a.online && !!a.status?.diagnoses),
    checks: info.value?.agent_ids.includes(a.id) ?? false,
  })),
)
function setVantage() {
  void start()
}

// ── What the run has shown so far ─────────────────────────────────────
interface PlaylistRow {
  key: string
  kind: string
  name: string
  metrics: Record<string, number>
  source?: Endpoint
  /** Player buffer per check, for the sparkline. */
  buffer: number[]
  /** Slowest playlist response seen, seconds. */
  slowest: number
  /** The playlist's backlog has been seen; what comes next is new. */
  primed: boolean
  /** When the first new segment appeared and when the latest one did, ms. */
  since: number
  last: number
  /** Seconds of media that appeared after `since`. */
  media: number
  seen: Set<string>
  segments: number
  failed: number
}
interface Cycle {
  n: number
  time: string
  took: number
  status: 'ok' | 'warn' | 'fail'
  segments: SegmentDetail[]
  problems: ProbeProblem[]
  snapshots: { variant: string; url: string; body: string; previous?: string; previous_time?: string }[]
}
interface Finding extends ProbeProblem {
  id: string
  first: string
  last: string
  cycles: number
}

const running = ref(false)
const error = ref<string | null>(null)
const ended = ref<'' | 'time' | 'stopped' | 'lost' | 'agent'>('')
// What an agent said when its diagnosis ended for another reason than time.
const endedWhy = ref('')
const startedAt = ref(0)
const now = ref(Date.now())
const total = ref(0)
const planned = ref(0)
const worst = ref<'ok' | 'warn' | 'fail'>('ok')
const playlists = reactive(new Map<string, PlaylistRow>())
const findings = reactive(new Map<string, Finding>())
const feed = ref<Cycle[]>([])
const openFinding = ref<string | null>(null)
const show = ref<'events' | 'problems' | 'all'>('events')
const feedLimit = 300

let ctl: AbortController | null = null
let clock: number | undefined

function reset() {
  playlists.clear()
  findings.clear()
  feed.value = []
  total.value = 0
  worst.value = 'ok'
  error.value = null
  ended.value = ''
  openFinding.value = null
}

function onCycle(ev: DiagnoseEvent) {
  const r = ev.result
  if (!r) return
  const at = new Date(r.time).getTime()
  total.value = ev.n ?? total.value + 1
  if (r.status === 'fail' || (r.status === 'warn' && worst.value === 'ok')) worst.value = r.status

  for (const v of r.variants ?? []) {
    if (!playlists.has(v.key)) {
      playlists.set(v.key, {
        key: v.key, kind: v.kind, name: [v.name, v.resolution].filter(Boolean).join(' '), metrics: {}, buffer: [], slowest: 0,
        primed: false, since: 0, last: 0, media: 0, seen: new Set(), segments: 0, failed: 0,
      })
    }
  }
  const fresh = new Map<string, Record<string, number>>()
  for (const m of r.metrics) {
    if (!m.v) continue
    let o = fresh.get(m.v)
    if (!o) fresh.set(m.v, (o = {}))
    o[m.n] = m.x
  }
  for (const [key, row] of playlists) {
    const m = fresh.get(key)
    if (!m) continue
    row.metrics = m
    row.slowest = Math.max(row.slowest, m.hls_playlist_total_seconds ?? 0)
    if (m.hls_buffer_seconds !== undefined) {
      row.buffer.push(m.hls_buffer_seconds)
      if (row.buffer.length > 180) row.buffer.shift()
    }
  }
  for (const s of r.sources ?? []) {
    const row = playlists.get(s.variant)
    if (row && s.playlist) row.source = s.playlist
  }
  // Media in files first seen in this check, per playlist.
  const arrived = new Map<string, number>()
  for (const seg of r.segments ?? []) {
    const row = playlists.get(seg.variant)
    if (!row) continue
    row.segments++
    if (seg.error || seg.status >= 400 || seg.status === 0) row.failed++
    // A server that renumbers its playlist shows one file under several
    // numbers; the file counts once.
    if (row.seen.has(seg.url)) continue
    row.seen.add(seg.url)
    arrived.set(seg.variant, (arrived.get(seg.variant) ?? 0) + seg.duration)
  }
  // The rate is measured between moments when segments appear, so that a
  // segment about to be published does not count as missing. The first
  // check only brings what was in the playlist already; the next arrival
  // starts the clock, and media counts from the one after it.
  for (const [key, media] of arrived) {
    const row = playlists.get(key)
    if (!row) continue
    if (!row.primed) row.primed = true
    else if (!row.since) row.since = row.last = at
    else {
      row.media += media
      row.last = at
    }
  }

  for (const p of r.problems ?? []) {
    const id = p.kind + '|' + p.variant
    const f = findings.get(id)
    if (f) Object.assign(f, p, { count: f.count + p.count, last: r.time, cycles: f.cycles + 1, details: p.details ?? f.details })
    else findings.set(id, { ...p, id, first: r.time, last: r.time, cycles: 1 })
  }

  feed.value.unshift({ n: ev.n ?? 0, time: r.time, took: ev.took_ms ?? 0, status: r.status, segments: r.segments ?? [], problems: r.problems ?? [], snapshots: r.snapshots ?? [] })
  if (feed.value.length > feedLimit) feed.value.length = feedLimit
}

async function start() {
  ctl?.abort()
  reset()
  const mine = (ctl = new AbortController())
  running.value = true
  startedAt.value = now.value = Date.now()
  planned.value = minutes.value * 60
  localStorage.setItem('diagnose-minutes', String(minutes.value))
  try {
    const from = vantage.value !== 1 ? `&agent=${vantage.value}` : ''
    await readStream<DiagnoseEvent>(`/api/v1/streams/${props.id}/diagnose?level=${level.value}&seconds=${planned.value}${from}`, mine.signal, (ev) => {
      if (ev.type === 'start') startedAt.value = Date.now()
      else if (ev.type === 'cycle') onCycle(ev)
      else if (ev.type === 'end') {
        ended.value = ev.reason === 'time' || !ev.reason ? 'time' : 'agent'
        endedWhy.value = ev.reason ?? ''
      }
    })
    if (!ended.value) ended.value = 'lost'
  } catch (e) {
    if (e instanceof DOMException && e.name === 'AbortError') return
    if (e instanceof ApiError && e.status === 404) missing.value = true
    error.value = e instanceof Error ? e.message : String(e)
    if (total.value) ended.value = 'lost'
  } finally {
    if (ctl === mine) {
      running.value = false
      now.value = Date.now()
    }
  }
}

function stop() {
  ctl?.abort()
  ctl = null
  running.value = false
  ended.value = 'stopped'
  now.value = Date.now()
}

function setLevel(l: Level) {
  level.value = l
  void start()
}

onMounted(async () => {
  clock = window.setInterval(() => running.value && (now.value = Date.now()), 500)
  try {
    info.value = await api.get<StreamDetail>(`/api/v1/streams/${props.id}`)
    level.value = info.value.level
    // A stream the server does not check itself is diagnosed from the
    // first of its vantage points that can do it.
    if (!info.value.agent_ids.includes(1)) {
      if (!live.overview) await live.refresh().catch(() => {})
      const first = vantages.value.find((v) => v.checks && v.available && v.id !== 1)
      if (first) vantage.value = first.id
    }
    void start()
  } catch (e) {
    if (e instanceof ApiError && e.status === 404) missing.value = true
    else error.value = e instanceof Error ? e.message : String(e)
  }
})
onBeforeUnmount(() => {
  clearInterval(clock)
  ctl?.abort()
})

// ── Presentation ──────────────────────────────────────────────────────
const elapsed = computed(() => Math.max(0, (now.value - startedAt.value) / 1000))
const rows = computed(() => [...playlists.values()])
const found = computed(() => {
  const rank = { crit: 0, warn: 1, info: 2 }
  return [...findings.values()].sort((a, b) => rank[a.severity] - rank[b.severity] || b.count - a.count)
})
const tally = computed(() => {
  const t = { crit: 0, warn: 0, info: 0 }
  for (const f of found.value) t[f.severity]++
  return t
})
const verdict = computed(() => {
  if (!total.value) return running.value ? t('diagnose.waitingForTheFirstCheck') : ''
  const parts: string[] = []
  if (tally.value.crit) parts.push(`${tally.value.crit} ${t('diagnose.kindsOfFailure', tally.value.crit)}`)
  if (tally.value.warn) parts.push(`${tally.value.warn} ${t('diagnose.kindsOfWarning', tally.value.warn)}`)
  const head = t('diagnose.checksInSpan', { n: total.value, checks: t('diagnose.checks', total.value), span: fmtDuration(Math.round(elapsed.value)) })
  return parts.length ? `${head}: ${parts.join(` ${t('diagnose.and')} `)}` : t('diagnose.everythingIsOK', { head })
})
const state = computed(() => {
  if (running.value) return t('diagnose.runningLeft', { span: fmtDuration(Math.max(0, Math.round(planned.value - elapsed.value))) })
  if (ended.value === 'time') return t('diagnose.finishedTimeIsUp')
  if (ended.value === 'stopped') return t('diagnose.stopped')
  if (ended.value === 'lost') return t('diagnose.interruptedConnectionToTheServerLost')
  if (ended.value === 'agent') return t('diagnose.interruptedByAgent', { why: endedWhy.value })
  return ''
})

const shownFeed = computed(() => {
  if (show.value === 'all') return feed.value
  if (show.value === 'problems') return feed.value.filter((c) => c.problems.some((p) => p.severity !== 'info'))
  return feed.value.filter((c) => c.segments.length || c.problems.length)
})

function playlistName(key: string): string {
  const row = playlists.get(key)
  if (!row) return kindLabel()[key] ?? key
  const kind = kindLabel()[row.kind] ?? row.kind
  return row.name ? `${kind} ${row.name}` : row.kind === 'master' || row.kind === 'media' ? kind : `${kind} ${key}`
}

/** Share of real time the media arrived at, once there is enough to judge. */
function rate(row: PlaylistRow): number | null {
  const wall = (row.last - row.since) / 1000
  // Arrivals are timed to the second, so a short stretch says little.
  return row.since && wall >= 20 ? row.media / wall : null
}

function rateClass(v: number | null): string {
  return v === null ? '' : v < 0.9 ? 'bad' : v < 0.95 ? 'poor' : ''
}

function spark(values: number[]): string {
  if (values.length < 2) return ''
  const top = Math.max(...values, 1)
  return values.map((v, i) => `${((i / (values.length - 1)) * 120).toFixed(1)},${(22 - (Math.max(v, 0) / top) * 20).toFixed(1)}`).join(' ')
}

function clockTime(t: string): string {
  const d = new Date(t)
  return d.toLocaleTimeString(dateLocale()) + '.' + String(d.getMilliseconds()).padStart(3, '0')
}

function tail(url: string): string {
  const path = url.split('?')[0] ?? url
  return path.split('/').slice(-3).join('/')
}

function keyframe(seg: SegmentDetail): boolean | undefined {
  const video = seg.media?.tracks?.find((t) => t.kind === 'video')
  if (!video || video.first_keyframe === undefined) return undefined
  return !!video.first_keyframe
}

function asIncident(f: Finding): Incident {
  return {
    id: 0, stream_id: props.id, stream_name: info.value?.name ?? '', agent_id: 0, agent_name: '', variant_id: 0, variant_key: f.variant, variant_kind: '',
    kind: f.kind, severity: f.severity, message: f.message, started_at: f.first, last_seen: f.last, ended_at: null, count: f.count, episodes: 1, idle_seconds: 0, active: true, details: f.details,
  }
}
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
          <h1>{{ t('common.liveDiagnosis') }}</h1>
        </div>
        <span v-if="total" class="badge" :class="worst">{{ worst === 'fail' ? t('diagnose.thereWereFailures') : worst === 'warn' ? t('diagnose.thereWereWarnings') : t('diagnose.noProblems') }}</span>
        <span class="spacer" />
        <select v-if="vantages.length > 1" v-model.number="vantage" class="input compact" :aria-label="t('diagnose.vantage')" :title="t('diagnose.vantageHint')" @change="setVantage">
          <option v-for="v in vantages" :key="v.id" :value="v.id" :disabled="!v.available">{{ v.label }}{{ v.checks ? '' : ' ' + t('diagnose.vantageNotAssigned') }}{{ v.available ? '' : ' ' + t('diagnose.vantageUnavailable') }}</option>
        </select>
        <div class="seg" role="group" :aria-label="t('diagnose.checkDepth')">
          <button v-for="l in levels" :key="l.id" type="button" :aria-pressed="level === l.id" :title="l.hint" @click="setLevel(l.id)">{{ l.label }}</button>
        </div>
        <select v-model.number="minutes" class="input compact" :aria-label="t('diagnose.howManyMinutesToCheck')" :disabled="running">
          <option v-for="m in lengths" :key="m" :value="m">{{ m }} {{ t('format.min') }}</option>
        </select>
        <button v-if="running" class="btn" type="button" @click="stop">{{ t('common.stop') }}</button>
        <button v-else class="btn primary" type="button" :disabled="!info" @click="start">{{ t('diagnose.runAgain') }}</button>
      </div>

      <div v-if="error" class="notice error">{{ error }}</div>

      <div class="panel lead">
        <div class="lead-main">
          <span v-if="running" class="pulse" aria-hidden="true" />
          <span class="strong">{{ verdict || t('diagnose.diagnosisIsNotRunning') }}</span>
          <span class="muted">{{ state }}</span>
        </div>
        <p class="muted">
          {{ vantage === 1 ? t('diagnose.aSeparateCheckEverySecondFrom') : t('diagnose.aSeparateCheckEverySecondFromAgent', { agent: vantages.find((v) => v.id === vantage)?.label ?? '' }) }}
        </p>
        <p v-if="info" class="mono muted url">{{ info.url }}</p>
      </div>

      <!-- Playlists now -->
      <section v-if="rows.length" class="section">
        <div class="section-title"><h2>{{ t('diagnose.playlistsNow') }}</h2></div>
        <div class="panel table-wrap">
          <table class="table">
            <thead>
              <tr>
                <th>{{ t('common.playlist') }}</th>
                <th>{{ t('common.response') }}</th>
                <th>{{ t('common.server') }}</th>
                <th class="right" :title="t('diagnose.numberOfTheFirstSegmentIn')">{{ t('common.segments') }}</th>
                <th class="right">TARGETDURATION</th>
                <th class="right" :title="t('diagnose.howLongThePlaylistHasNot')">{{ t('diagnose.unchangedFor') }}</th>
                <th :title="t('diagnose.howMuchVideoAPlayerThat')">{{ t('diagnose.playerBuffer') }}</th>
                <th class="right" :title="t('diagnose.howMuchVideoAppearsInThe')">{{ t('diagnose.videoArrives') }}</th>
                <th class="right" :title="t('diagnose.segmentsCheckedAndHowManyOf')">{{ t('diagnose.checked') }}</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="r in rows" :key="r.key">
                <td>
                  <div class="strong">{{ playlistName(r.key) }}</div>
                  <div v-if="r.kind !== 'master' && r.kind !== 'media'" class="mono muted small">{{ r.key }}</div>
                </td>
                <td class="mono nowrap">
                  <span :class="{ bad: (r.metrics.hls_playlist_http_status ?? 200) >= 400 || r.metrics.hls_playlist_up === 0 }">{{ r.metrics.hls_playlist_up === 0 && !r.metrics.hls_playlist_http_status ? t('common.noResponse') : (r.metrics.hls_playlist_http_status ?? '—') }}</span>
                  · {{ fmtSeconds(r.metrics.hls_playlist_total_seconds) }}
                  <div class="muted small">{{ t('diagnose.slowest', { time: fmtSeconds(r.slowest) }) }}</div>
                </td>
                <td class="mono">
                  <template v-if="r.source">
                    <RouterLink :to="{ name: 'server', params: { host: r.source.host, ip: r.source.ip ?? '' } }">{{ r.source.host }}</RouterLink>
                    <div v-if="r.source.ip && r.source.ip !== r.source.host" class="muted small">{{ r.source.ip }}</div>
                  </template>
                  <template v-else>—</template>
                </td>
                <td class="mono right nowrap">
                  <template v-if="r.metrics.hls_playlist_segments !== undefined">{{ t('diagnose.from', { n: r.metrics.hls_media_sequence ?? '' }) }}<div class="muted small">{{ t('diagnose.seg', { n: r.metrics.hls_playlist_segments, span: fmtSeconds(r.metrics.hls_playlist_window_seconds) }) }}</div></template>
                  <template v-else>—</template>
                </td>
                <td class="mono right">{{ fmtSeconds(r.metrics.hls_target_duration_seconds) }}</td>
                <td class="mono right">{{ fmtSeconds(r.metrics.hls_playlist_stale_seconds) }}</td>
                <td class="nowrap">
                  <template v-if="r.buffer.length">
                    <span class="mono" :class="{ bad: (r.buffer.at(-1) ?? 0) <= 0 }">{{ fmtSeconds(r.buffer.at(-1)) }}</span>
                    <svg class="spark" viewBox="0 0 120 24" preserveAspectRatio="none" aria-hidden="true"><polyline :points="spark(r.buffer)" /></svg>
                  </template>
                  <span v-else class="muted">—</span>
                </td>
                <td class="mono right" :class="rateClass(rate(r))">
                  <template v-if="rate(r) !== null">{{ Math.round((rate(r) ?? 0) * 100) }} %<div class="muted small">{{ t('diagnose.mediaInSpan', { media: fmtSeconds(r.media), span: fmtSeconds((r.last - r.since) / 1000) }) }}</div></template>
                  <span v-else class="muted" :title="level === 'playlist' ? t('diagnose.calculatedWhenSegmentsAreChecked') : t('diagnose.calculatedBetweenSegmentArrivalsSeveralNew')">—</span>
                </td>
                <td class="mono right">
                  <template v-if="r.segments">{{ r.segments }}<div v-if="r.failed" class="bad small">{{ t('diagnose.failedToDownload', { n: r.failed }) }}</div></template>
                  <span v-else class="muted">—</span>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </section>

      <!-- Findings -->
      <section v-if="total" class="section">
        <div class="section-title">
          <h2>{{ t('diagnose.whatWasFound') }}</h2>
          <span class="muted">{{ t('diagnose.clickARowToExpandIt') }}</span>
        </div>
        <div class="panel table-wrap">
          <div v-if="!found.length" class="empty">{{ t('diagnose.nothingSoFarAllChecksPassed') }}</div>
          <table v-else class="table">
            <thead>
              <tr>
                <th>{{ t('diagnose.what') }}</th>
                <th>{{ t('common.playlist') }}</th>
                <th class="right" :title="t('diagnose.howManyTimesItHappenedAnd')">{{ t('diagnose.times') }}</th>
                <th>{{ t('diagnose.firstSeen') }}</th>
                <th>{{ t('diagnose.lastSeen') }}</th>
                <th>{{ t('common.checkMessage') }}</th>
              </tr>
            </thead>
            <tbody>
              <template v-for="f in found" :key="f.id">
                <tr class="clickable" @click="openFinding = openFinding === f.id ? null : f.id">
                  <td class="nowrap"><span class="dot" :class="f.severity" /> <span class="strong">{{ problemName(f.kind) }}</span></td>
                  <td>{{ playlistName(f.variant) }}</td>
                  <td class="mono right nowrap">{{ f.count }}<span class="muted"> / {{ f.cycles }}</span></td>
                  <td class="mono nowrap">{{ clockTime(f.first) }}</td>
                  <td class="mono nowrap">{{ clockTime(f.last) }}</td>
                  <td class="mono small">{{ f.message }}</td>
                </tr>
                <tr v-if="openFinding === f.id">
                  <td colspan="6" class="unfolded"><IncidentDetails :incident="asIncident(f)" /></td>
                </tr>
              </template>
            </tbody>
          </table>
        </div>
      </section>

      <!-- Feed -->
      <section v-if="total" class="section">
        <div class="section-title feed-head">
          <h2>{{ t('diagnose.checkFeed') }}</h2>
          <span class="muted">{{ feed.length >= feedLimit ? t('diagnose.newestFirstLast', { n: feedLimit }) : t('diagnose.newestFirst') }}</span>
          <span class="spacer" />
          <div class="seg" role="group" :aria-label="t('diagnose.whichChecksToShow')">
            <button type="button" :aria-pressed="show === 'events'" :title="t('diagnose.checksInWhichNewSegmentsAppeared')" @click="show = 'events'">{{ t('diagnose.withEvents') }}</button>
            <button type="button" :aria-pressed="show === 'problems'" @click="show = 'problems'">{{ t('diagnose.withProblemsOnly') }}</button>
            <button type="button" :aria-pressed="show === 'all'" @click="show = 'all'">{{ t('common.all') }}</button>
          </div>
        </div>
        <div class="panel feed">
          <div v-if="!shownFeed.length" class="empty">{{ show === 'problems' ? t('diagnose.noChecksWithProblemsYet') : t('diagnose.nothingNewSoFarThePlaylist') }}</div>
          <div v-for="c in shownFeed" :key="c.n" class="cycle">
            <div class="cycle-head">
              <span class="dot" :class="c.status" />
              <span class="mono">{{ clockTime(c.time) }}</span>
              <span class="muted">{{ t('diagnose.checkTook', { n: c.n, took: fmtMs(c.took) }) }}</span>
              <span v-if="!c.segments.length && !c.problems.length" class="muted">{{ t('diagnose.nothingNew') }}</span>
            </div>
            <div v-for="(p, i) in c.problems" :key="'p' + i" class="line problem">
              <span class="badge" :class="p.severity === 'info' ? 'accent' : p.severity">{{ problemName(p.kind) }}</span>
              <span class="muted">{{ playlistName(p.variant) }}</span>
              <span class="mono small">{{ p.message }}<template v-if="p.count > 1"> ×{{ p.count }}</template></span>
              <div v-for="(t, j) in conclusion(p.kind, p.details ?? {})" :key="j" class="why">{{ t }}</div>
            </div>
            <table v-if="c.segments.length" class="segs">
              <colgroup>
                <col style="width: 90px" /><col style="width: 26%" /><col style="width: 70px" /><col style="width: 90px" /><col style="width: 150px" /><col style="width: 80px" /><col style="width: 16%" /><col style="width: 150px" /><col />
              </colgroup>
              <tbody>
                <tr v-for="s in c.segments" :key="s.variant + s.seq + s.url" :class="{ flagged: s.problems?.length || s.error || s.status >= 400 }">
                  <td class="mono">{{ s.seq }}</td>
                  <td class="mono file" :title="s.url">{{ tail(s.url) }}</td>
                  <td class="mono right" :title="t('diagnose.durationAccordingToThePlaylistEXTINF')">{{ fmtSeconds(s.duration) }}</td>
                  <td class="mono" :class="{ bad: s.error || s.status >= 400 }" :title="s.error">{{ s.status || t('common.noResponse') }}</td>
                  <td class="mono right" :title="t('diagnose.timeToFirstByteAndTotal')">{{ fmtMs(s.ttfb_ms) }} / {{ fmtMs(s.total_ms) }}</td>
                  <td class="mono right" :title="t('common.segmentSize')">{{ fmtBytes(s.size) }}</td>
                  <td class="mono" :title="t('diagnose.serverThatServedTheSegment')">{{ s.source?.host ?? s.remote_addr ?? '' }}</td>
                  <td :title="keyframe(s) !== undefined ? t('diagnose.whetherTheSegmentStartsWithA') : ''"><span v-if="keyframe(s) !== undefined" :class="{ bad: keyframe(s) === false }">{{ keyframe(s) ? t('diagnose.keyframeYes') : t('diagnose.keyframeNo') }}</span></td>
                  <td>
                    <span v-if="s.discontinuity" class="badge">DISCONTINUITY</span>
                    <span v-for="k in s.problems ?? []" :key="k" class="badge warn">{{ problemName(k) }}</span>
                    <span v-if="s.error" class="bad small">{{ s.error }}</span>
                  </td>
                </tr>
              </tbody>
            </table>
            <details v-for="sn in c.snapshots" :key="sn.url" class="snapshot">
              <summary>{{ t('diagnose.playlistAtThisMoment') }} <span class="mono">{{ tail(sn.url) }}</span></summary>
              <PlaylistPair :body="sn.body" :time="c.time" :previous="sn.previous" :previous-time="sn.previous_time" />
            </details>
          </div>
        </div>
      </section>
    </template>
  </div>
</template>

<style scoped>
.lead {
  padding: 14px 16px;
}

.lead p {
  margin: 6px 0 0;
  font-size: 12px;
}

.lead-main {
  display: flex;
  align-items: center;
  gap: 10px;
  flex-wrap: wrap;
}

.url {
  overflow-wrap: anywhere;
}

.pulse {
  width: 8px;
  height: 8px;
  border-radius: 50%;
  background: var(--accent);
  animation: pulse 1.2s ease-in-out infinite;
}

@keyframes pulse {
  50% {
    opacity: 0.25;
  }
}

@media (prefers-reduced-motion: reduce) {
  .pulse {
    animation: none;
  }
}

.small {
  font-size: 12px;
}

.bad {
  color: #ff7a85;
}

.poor {
  color: var(--accent-warm);
}

.spark {
  width: 120px;
  height: 24px;
  margin-left: 8px;
  vertical-align: middle;
}

.spark polyline {
  fill: none;
  stroke: var(--accent);
  stroke-width: 1.5;
  vector-effect: non-scaling-stroke;
}

.unfolded {
  background: var(--surface-2);
}

.feed-head {
  align-items: center;
  flex-wrap: wrap;
}

.feed-head .spacer {
  flex: 1;
}

.feed {
  max-height: 70vh;
  overflow-y: auto;
}

.cycle {
  padding: 8px 14px;
  border-bottom: 1px solid var(--border-subtle);
}

.cycle-head {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 12px;
}

.line {
  display: flex;
  align-items: baseline;
  flex-wrap: wrap;
  gap: 4px 8px;
  margin: 6px 0 0 16px;
}

.why {
  flex-basis: 100%;
  font-size: 12px;
  color: var(--text);
}

.segs {
  table-layout: fixed;
  width: calc(100% - 16px);
  margin: 6px 0 0 16px;
  border-collapse: collapse;
  font-size: 12px;
}

.segs td {
  padding: 2px 12px 2px 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  vertical-align: baseline;
}

.segs td:last-child {
  overflow: visible;
  white-space: normal;
}

.segs .flagged td:first-child {
  box-shadow: inset 2px 0 0 var(--accent-warm);
  padding-left: 6px;
}

.segs .badge {
  margin: 1px 4px 1px 0;
  white-space: normal;
}

.snapshot {
  margin: 6px 0 0 16px;
  font-size: 12px;
}

.snapshot summary {
  cursor: pointer;
  color: var(--text-muted);
}

.snapshot pre {
  max-height: 260px;
  margin: 6px 0 0;
  padding: 8px 10px;
  overflow: auto;
  border-radius: 4px;
  background: var(--deep);
}
</style>
