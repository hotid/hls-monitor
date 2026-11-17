<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { api, ApiError, query } from '@/api'
import { chartAvailable, chartGroups, infoKinds, kindLabel, levelLabel, problemName, type ChartDef } from '@/catalog'
import { fmtAgo, fmtBits, fmtBytes, fmtFull, fmtMs, fmtNumber, fmtSeconds, fmtWhen } from '@/format'
import { usePolling } from '@/poll'
import { timeValue, useSort } from '@/sort'
import { useLive } from '@/stores/live'
import { useSession } from '@/stores/session'
import { useToasts } from '@/stores/toasts'
import { t } from '@/i18n'
import type { Endpoint, Incident, MediaTrack, SegmentRecord, SeriesResponse, Snapshot, SourceSeen, StreamDetail, Variant } from '@/types'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import FilterEmpty from '@/components/FilterEmpty.vue'
import IncidentTable from '@/components/IncidentTable.vue'
import PlaylistPair from '@/components/PlaylistPair.vue'
import ModalDialog from '@/components/ModalDialog.vue'
import SortTh from '@/components/SortTh.vue'
import StatusBadge from '@/components/StatusBadge.vue'
import StreamFormDialog from '@/components/StreamFormDialog.vue'
import TimeChart, { type ChartLine } from '@/components/TimeChart.vue'

const props = defineProps<{ id: number }>()
const router = useRouter()
const live = useLive()
const session = useSession()
const toasts = useToasts()

// ── Stream itself ────────────────────────────────────────────────────
const stream = ref<StreamDetail | null>(null)
const missing = ref(false)
const incidents = ref<Incident[]>([])
const now = ref(Date.now())

const { error } = usePolling(async (signal) => {
  try {
    const [s, inc] = await Promise.all([
      api.get<StreamDetail>(`/api/v1/streams/${props.id}`, signal),
      api.get<Incident[]>(`/api/v1/incidents?stream=${props.id}&limit=50`, signal),
    ])
    stream.value = s
    incidents.value = inc
    now.value = Date.now()
  } catch (e) {
    if (e instanceof ApiError && e.status === 404) missing.value = true
    else throw e
  }
}, 10_000)

const activeVariants = computed(() => (stream.value?.variants ?? []).filter((v) => v.active))
const variantById = computed(() => new Map((stream.value?.variants ?? []).map((v) => [v.id, v])))

function variantLabel(v: Variant | undefined): string {
  if (!v) return '—'
  if (v.kind === 'master') return t('common.multivariantPlaylist')
  if (v.kind === 'media') return t('common.playlist')
  const parts: string[] = []
  if (v.name) parts.push(v.name)
  if (v.resolution) parts.push(v.resolution.split('x')[1] + 'p')
  if (v.bandwidth) parts.push(fmtBits(v.bandwidth))
  if (v.language) parts.push(v.language)
  return parts.length ? parts.join(', ') : v.key
}

function variantMetric(v: Variant, name: string): number | undefined {
  for (const st of v.states) {
    const x = st.metrics[name]
    if (x !== undefined) return x
  }
  return undefined
}

const severity: Record<string, number> = { fail: 4, warn: 3, nodata: 2, ok: 1, disabled: 0 }
const agentSort = useSort(() => stream.value?.agents ?? [], {
  name: (a) => a.agent_name,
  status: (a) => severity[a.status],
  checked: (a) => timeValue(a.checked_at),
  bitrate: (a) => a.summary.bitrate_bps,
  latency: (a) => a.summary.live_latency_seconds,
  segment: (a) => a.summary.segment_total_seconds,
  buffer: (a) => a.summary.buffer_seconds,
  cycle: (a) => a.summary.check_duration_seconds,
})
const variantSort = useSort(() => activeVariants.value, {
  name: { value: (v) => variantLabel(v), filter: 'text', label: (v) => `${variantLabel(v)} ${v.key}` },
  declared: (v) => v.bandwidth || undefined,
  actual: (v) => variantMetric(v, 'hls_segment_bitrate_bps'),
  height: (v) => variantMetric(v, 'hls_media_height') ?? (Number(v.resolution?.split('x')[1]) || undefined),
  fps: (v) => variantMetric(v, 'hls_media_fps') ?? (v.frame_rate || undefined),
  gop: (v) => variantMetric(v, 'hls_media_gop_seconds'),
  target: (v) => variantMetric(v, 'hls_target_duration_seconds'),
  codecs: { value: (v) => v.codecs, filter: 'choice' },
})

// ── Where it is served from ──────────────────────────────────────────
function software(e: Endpoint | undefined): string {
  return e?.headers?.Server ?? ''
}

function node(e: Endpoint): string {
  return e.ip && e.ip !== e.host ? `${e.host} (${e.ip})` : e.host
}

/** Host of the first hop when the request was redirected. */
function via(e: Endpoint): string {
  const first = e.redirects?.[0]
  if (!first) return ''
  try {
    return new URL(first.url).host
  } catch {
    return ''
  }
}

interface Served {
  agents: string
  playlist: Endpoint
  segment?: Endpoint
}

// Agents that see the same servers are shown as one line.
function served(v: Variant): Served[] {
  const groups = new Map<string, Served>()
  for (const st of v.states) {
    if (!st.playlist_source) continue
    const seg = st.segment_source
    const differs = seg && (seg.host !== st.playlist_source.host || seg.ip !== st.playlist_source.ip)
    const key = node(st.playlist_source) + '|' + (differs ? node(seg) : '') + '|' + via(st.playlist_source) + '|' + software(st.playlist_source)
    const g = groups.get(key)
    const name = live.agentName(st.agent_id)
    if (g) g.agents += ', ' + name
    else groups.set(key, { agents: name, playlist: st.playlist_source, segment: differs ? seg : undefined })
  }
  return [...groups.values()]
}

// Every server that answered within the last day, not only the current one.
const sources = ref<SourceSeen[]>([])
usePolling(async (signal) => {
  sources.value = await api.get<SourceSeen[]>(`/api/v1/streams/${props.id}/sources?hours=24`, signal)
}, 30_000)

interface ServerShare {
  key: string
  host: string
  ip: string
  software: string
  share: number
  lastSeen: string
  current: boolean
}

// Servers of one playlist over the day, across agents, by share of answers.
function history(v: Variant, what: 'playlist' | 'segment'): ServerShare[] {
  const rows = sources.value.filter((r) => r.variant_id === v.id && r.what === what)
  const total = rows.reduce((n, r) => n + r.hits, 0)
  const byServer = new Map<string, ServerShare & { hits: number }>()
  const current = new Set(
    v.states.map((st) => (what === 'playlist' ? st.playlist_source : st.segment_source)).filter((e): e is Endpoint => !!e).map((e) => e.host + ' ' + (e.ip ?? '')),
  )
  for (const r of rows) {
    const key = r.host + ' ' + r.ip
    const e = byServer.get(key)
    if (e) {
      e.hits += r.hits
      if (r.last_seen > e.lastSeen) e.lastSeen = r.last_seen
      if (r.software) e.software = r.software
    } else {
      byServer.set(key, { key, host: r.host, ip: r.ip, software: r.software, hits: r.hits, share: 0, lastSeen: r.last_seen, current: current.has(key) })
    }
  }
  return [...byServer.values()]
    .map((e) => ({ ...e, share: total ? e.hits / total : 0 }))
    .sort((a, b) => b.hits - a.hits)
}

function answered(v: Variant): string {
  const n = history(v, 'playlist').length
  return t('stream.answeredInTheLastDay', { n, servers: t('stream.servers', n) })
}

// ── Actions ──────────────────────────────────────────────────────────
const editing = ref(false)
const deleting = ref(false)
const busy = ref(false)

async function checkNow() {
  try {
    await api.post(`/api/v1/streams/${props.id}/check`)
    toasts.ok(t('stream.checkStarted'))
  } catch (e) {
    toasts.error(e)
  }
}

async function toggle() {
  const s = stream.value
  if (!s) return
  try {
    await api.post(`/api/v1/streams/${s.id}/${s.enabled ? 'disable' : 'enable'}`)
    stream.value = await api.get<StreamDetail>(`/api/v1/streams/${s.id}`)
    void live.refresh()
    toasts.ok(s.enabled ? t('stream.checksStopped') : t('stream.checksResumed'))
  } catch (e) {
    toasts.error(e)
  }
}

async function remove() {
  busy.value = true
  try {
    await api.del(`/api/v1/streams/${props.id}`)
    void live.refresh()
    toasts.ok(t('common.streamDeleted', { name: String(stream.value?.name) }))
    await router.push({ name: 'streams' })
  } catch (e) {
    toasts.error(e)
  } finally {
    busy.value = false
  }
}

// ── Charts ───────────────────────────────────────────────────────────
const ranges = [
  { label: '15 ' + t('format.min'), seconds: 900 },
  { label: '1 ' + t('format.h'), seconds: 3600 },
  { label: '6 ' + t('format.h'), seconds: 6 * 3600 },
  { label: '24 ' + t('format.h'), seconds: 24 * 3600 },
  { label: '7 ' + t('format.d'), seconds: 7 * 24 * 3600 },
]
const range = ref(Number(localStorage.getItem('stream-range')) || 3600)
const zoom = ref<{ from: number; to: number } | null>(null)
const agentFilter = ref(0)
const variantFilter = ref(0)
const group = ref(0)
watch(range, (v) => {
  localStorage.setItem('stream-range', String(v))
  zoom.value = null
})

const charts = computed<ChartDef[]>(() => {
  const level = stream.value?.level ?? 'playlist'
  return (chartGroups()[group.value]?.charts ?? []).filter((c) => chartAvailable(c, level))
})
const hiddenByLevel = computed(() => (chartGroups()[group.value]?.charts.length ?? 0) - charts.value.length)

interface Loaded {
  from: number
  to: number
  step: number
  lines: Record<string, ChartLine[]>
}
const loaded = ref<Loaded | null>(null)
const chartsLoading = ref(false)

function seriesLabel(labels: Record<string, string>, multiAgent: boolean): string {
  const v = variantById.value.get(Number(labels.variant))
  const name = labels.variant ? variantLabel(v) : t('common.stream')
  return multiAgent ? `${name} — ${live.agentName(Number(labels.agent))}` : name
}

// Average of several series point by point: used when a chart shows one
// line per metric.
function average(series: { points: [number, number][] }[]): [number, number][] {
  const sum = new Map<number, [number, number]>()
  for (const s of series) {
    for (const [t, v] of s.points) {
      const e = sum.get(t)
      if (e) {
        e[0] += v
        e[1]++
      } else sum.set(t, [v, 1])
    }
  }
  return [...sum.entries()].sort((a, b) => a[0] - b[0]).map(([t, [s, n]]) => [t, s / n])
}

const { error: chartsError, refresh: reloadCharts } = usePolling(async (signal) => {
  if (!stream.value) return
  const to = zoom.value ? zoom.value.to : Math.floor(Date.now() / 1000)
  const from = zoom.value ? zoom.value.from : to - range.value
  const step = Math.max(5, Math.round((to - from) / 240 / 5) * 5)
  const multiAgent = agentFilter.value === 0 && (stream.value.agents.length > 1)
  chartsLoading.value = true
  try {
    const defs = charts.value
    const responses = await Promise.all(
      defs.map((c) =>
        api.get<SeriesResponse>(
          `/api/v1/streams/${props.id}/series` +
            query({ metric: c.series.map((s) => s.metric).join(','), agg: c.agg, from, to, step, agent: agentFilter.value, variant: variantFilter.value }),
          signal,
        ),
      ),
    )
    const lines: Record<string, ChartLine[]> = {}
    let grid = { from, to, step }
    defs.forEach((c, i) => {
      const r = responses[i]!
      grid = { from: r.from, to: r.to, step: r.step }
      if (c.series.length === 1) {
        lines[c.id] = (r.metrics[0]?.series ?? [])
          .map((s) => ({ label: seriesLabel(s.labels, multiAgent), points: s.points }))
          .sort((a, b) => a.label.localeCompare(b.label, 'ru'))
      } else {
        lines[c.id] = c.series.map((def, j) => ({ label: def.label, points: average(r.metrics[j]?.series ?? []) }))
      }
    })
    // "to" from the server is the request time; the grid ends at the last full step.
    loaded.value = { from: grid.from, to: grid.from + Math.ceil((grid.to - grid.from) / grid.step) * grid.step, step: grid.step, lines }
  } finally {
    chartsLoading.value = false
  }
}, 15_000)
watch([range, zoom, agentFilter, variantFilter, group, () => stream.value?.id, () => stream.value?.level], () => void reloadCharts())

function onZoom(from: number, to: number) {
  zoom.value = { from, to }
}

// ── Segment journal ──────────────────────────────────────────────────
const segments = ref<SegmentRecord[]>([])
const onlyProblems = ref(false)
const openSegment = ref<SegmentRecord | null>(null)
const { refresh: reloadSegments } = usePolling(async (signal) => {
  segments.value = await api.get<SegmentRecord[]>(
    `/api/v1/streams/${props.id}/segments` + query({ limit: 60, problems: onlyProblems.value ? 'true' : undefined, agent: agentFilter.value, variant: variantFilter.value }),
    signal,
  )
}, 10_000)
watch([onlyProblems, agentFilter, variantFilter], () => void reloadSegments())
const segmentSort = useSort(() => segments.value, {
  time: (s) => timeValue(s.time),
  variant: { value: (s) => variantLabel(variantById.value.get(s.variant_id)), filter: 'choice' },
  seq: (s) => s.seq,
  status: { value: (s) => s.status || undefined, filter: 'choice' },
  size: { value: (s) => s.size || undefined, filter: 'range', unit: t('format.mb'), scale: 1 << 20 },
  load: { value: (s) => (s.detail.partial ? s.detail.ttfb_ms : s.total_ms), filter: 'range', unit: t('format.ms') },
  bitrate: { value: (s) => (s.duration > 0 && s.size ? (s.size * 8) / s.duration : undefined), filter: 'range', unit: t('format.mbps'), scale: 1e6 },
  node: { value: (s) => (s.detail.source ? node(s.detail.source) : s.remote_addr), filter: 'choice' },
  problems: (s) => s.problems.length,
})

// ── Playlist snapshots ───────────────────────────────────────────────
const snapshots = ref<Snapshot[]>([])
const openSnapshot = ref<Snapshot | null>(null)
usePolling(async (signal) => {
  snapshots.value = await api.get<Snapshot[]>(`/api/v1/streams/${props.id}/snapshots?limit=30`, signal)
}, 30_000)

async function showSnapshot(s: Snapshot) {
  try {
    openSnapshot.value = await api.get<Snapshot>(`/api/v1/snapshots/${s.id}`)
  } catch (e) {
    toasts.error(e)
  }
}

function track(seg: SegmentRecord, kind: string) {
  return seg.detail.media?.tracks?.find((t) => t.kind === kind)
}

// Texts of a track row: the row's loop variable hides t() in the template.
function trackKind(kind: string): string {
  return kind === 'video' ? t('common.video') : kind === 'audio' ? t('common.audio') : kind
}

function trackParams(tr: MediaTrack): string {
  if (tr.kind === 'video') {
    const params = { w: tr.width ?? '', h: tr.height ?? '', fps: fmtNumber(tr.fps, 2), gop: fmtSeconds(tr.max_gop) }
    return tr.first_keyframe
      ? t('stream.fpsGOPStartsWithAKeyframe', params)
      : t('stream.fpsGOPDoesNotStartWith', params)
  }
  if (tr.kind === 'audio') return t('stream.hzChannels', { rate: tr.sample_rate ?? '', n: tr.channels ?? '' })
  return ''
}
</script>

<template>
  <div class="page">
    <div v-if="missing" class="panel empty">
      <p class="strong">{{ t('common.noSuchStream') }}</p>
      <p>{{ t('common.itMayHaveBeenDeleted') }} <RouterLink :to="{ name: 'streams' }">{{ t('common.goToTheStreamList') }}</RouterLink></p>
    </div>
    <div v-else-if="!stream" class="panel empty">{{ error ?? t('common.loading') }}</div>

    <template v-else>
      <div class="page-head">
        <div class="title">
          <RouterLink :to="{ name: 'streams' }" class="muted">{{ t('common.streams') }}</RouterLink>
          <h1>{{ stream.name }}</h1>
        </div>
        <StatusBadge :status="stream.status" />
        <span class="spacer" />
        <RouterLink class="btn" :to="{ name: 'watch', params: { id: stream.id } }">{{ t('watch.title') }}</RouterLink>
        <RouterLink class="btn" :to="{ name: 'diagnose', params: { id: stream.id } }">{{ t('common.liveDiagnosis') }}</RouterLink>
        <template v-if="session.isAdmin">
          <button class="btn" type="button" :disabled="!stream.enabled" @click="checkNow">{{ t('stream.checkNow') }}</button>
          <button class="btn" type="button" @click="toggle">{{ stream.enabled ? t('common.stop') : t('common.resume') }}</button>
          <button class="btn" type="button" @click="editing = true">{{ t('common.configure') }}</button>
          <button class="btn danger" type="button" @click="deleting = true">{{ t('common.delete') }}</button>
        </template>
      </div>
      <div v-if="error" class="notice error">{{ error }}</div>

      <div class="summary panel">
        <dl class="kv">
          <dt>{{ t('stream.address') }}</dt>
          <dd class="mono"><a :href="stream.url" target="_blank" rel="noopener noreferrer">{{ stream.url }}</a></dd>
          <dt>{{ t('common.checkLevel') }}</dt>
          <dd>{{ stream.interval_sec ? t('stream.every', { level: levelLabel()[stream.level] ?? '', span: fmtSeconds(stream.interval_sec) }) : levelLabel()[stream.level] }}</dd>
          <template v-if="stream.notes">
            <dt>{{ t('common.notes') }}</dt>
            <dd style="white-space: pre-wrap">{{ stream.notes }}</dd>
          </template>
        </dl>
      </div>

      <!-- Vantage points -->
      <section class="section">
        <div class="section-title"><h2>{{ t('stream.vantagePoints') }}</h2></div>
        <div class="panel">
          <div v-if="!stream.agents.length" class="empty">
            {{ t('stream.noAgentsAreAssignedSoThe') }}
            <template v-if="session.isAdmin">{{ t('stream.chooseThemInTheStreamSettings') }}</template>
          </div>
          <div v-else class="table-wrap">
            <table class="table">
              <thead>
                <tr>
                  <SortTh :sort="agentSort" by="name">{{ t('common.agent') }}</SortTh>
                  <SortTh :sort="agentSort" by="status">{{ t('common.status') }}</SortTh>
                  <SortTh :sort="agentSort" by="checked">{{ t('stream.checked') }}</SortTh>
                  <SortTh :sort="agentSort" by="bitrate" class="right">{{ t('common.bitrate') }}</SortTh>
                  <SortTh :sort="agentSort" by="latency" class="right">{{ t('common.latency') }}</SortTh>
                  <SortTh :sort="agentSort" by="segment" class="right">{{ t('stream.segmentDownload') }}</SortTh>
                  <SortTh :sort="agentSort" by="buffer" class="right">{{ t('stream.playerBuffer') }}</SortTh>
                  <SortTh :sort="agentSort" by="cycle" class="right">{{ t('stream.checkCycle') }}</SortTh>
                </tr>
              </thead>
              <tbody>
                <tr v-for="a in agentSort.rows" :key="a.agent_id">
                  <td class="strong">{{ a.agent_name }}</td>
                  <td><StatusBadge :status="a.status" /></td>
                  <td class="nowrap" :title="fmtFull(a.checked_at)">{{ a.checked_at ? fmtAgo(a.checked_at, now) : t('common.notCheckedYet') }}</td>
                  <td class="mono right">{{ fmtBits(a.summary.bitrate_bps) }}</td>
                  <td class="mono right">{{ fmtSeconds(a.summary.live_latency_seconds) }}</td>
                  <td class="mono right">{{ fmtSeconds(a.summary.segment_total_seconds) }}</td>
                  <td class="mono right">{{ fmtSeconds(a.summary.buffer_seconds) }}</td>
                  <td class="mono right">{{ fmtSeconds(a.summary.check_duration_seconds) }}</td>
                </tr>
              </tbody>
            </table>
          </div>
        </div>
      </section>

      <!-- Variants -->
      <section v-if="activeVariants.length" class="section">
        <div class="section-title"><h2>{{ t('common.playlists') }}</h2></div>
        <div class="panel table-wrap">
          <table class="table">
            <thead>
              <tr>
                <SortTh :sort="variantSort" by="name">{{ t('common.playlist') }}</SortTh>
                <th>{{ t('stream.statusByAgent') }}</th>
                <th>{{ t('stream.servedFrom') }}</th>
                <SortTh :sort="variantSort" by="declared" class="right">{{ t('stream.declared') }}</SortTh>
                <SortTh :sort="variantSort" by="actual" class="right">{{ t('stream.actual') }}</SortTh>
                <SortTh :sort="variantSort" by="height" class="right">{{ t('stream.resolution') }}</SortTh>
                <SortTh :sort="variantSort" by="fps" class="right">{{ t('common.fps') }}</SortTh>
                <SortTh :sort="variantSort" by="gop" class="right">GOP</SortTh>
                <SortTh :sort="variantSort" by="target" class="right">{{ t('stream.segment') }}</SortTh>
                <SortTh :sort="variantSort" by="codecs">{{ t('stream.codecs') }}</SortTh>
              </tr>
            </thead>
            <tbody>
              <tr v-for="v in variantSort.rows" :key="v.id">
                <td>
                  <div class="strong">{{ variantLabel(v) }}</div>
                  <div v-if="v.kind !== 'master' && v.kind !== 'media'" class="mono muted truncate" style="max-width: 280px" :title="v.key">{{ kindLabel()[v.kind] }}: {{ v.key }}</div>
                </td>
                <td>
                  <span class="states">
                    <span v-for="st in v.states" :key="st.agent_id" class="state" :title="t('stream.checkedAgo', { ago: fmtAgo(st.checked_at, now) })">
                      <span class="dot" :class="st.status" />{{ live.agentName(st.agent_id) }}
                    </span>
                    <span v-if="!v.states.length" class="muted">{{ t('stream.noData') }}</span>
                  </span>
                </td>
                <td class="served">
                  <div v-for="(g, gi) in served(v)" :key="gi">
                    <span class="mono">{{ node(g.playlist) }}</span>
                    <span v-if="software(g.playlist)" class="muted">, {{ software(g.playlist) }}</span>
                    <span v-if="served(v).length > 1" class="muted"> — {{ t('stream.seenBy', { agents: g.agents }) }}</span>
                    <div v-if="via(g.playlist)" class="muted">{{ t('stream.afterARedirectFrom') }} <span class="mono">{{ via(g.playlist) }}</span></div>
                    <div v-if="g.segment" class="muted">{{ t('stream.segments') }} <span class="mono">{{ node(g.segment) }}</span><template v-if="software(g.segment)">, {{ software(g.segment) }}</template></div>
                  </div>
                  <span v-if="!served(v).length" class="muted">—</span>
                  <details v-if="history(v, 'playlist').length > 1 || history(v, 'segment').length > 1" class="servers">
                    <summary>{{ answered(v) }}</summary>
                    <template v-for="what in (['playlist', 'segment'] as const)" :key="what">
                      <div v-if="history(v, what).length" class="servers-group">
                        <div class="muted">{{ what === 'playlist' ? t('common.playlist') : t('common.segments') }}</div>
                        <div v-for="h in history(v, what)" :key="h.key" class="server-row" :title="t('stream.lastSeen', { when: fmtWhen(h.lastSeen) })">
                          <span class="mono">{{ h.host }}<template v-if="h.ip && h.ip !== h.host"> ({{ h.ip }})</template></span>
                          <span v-if="h.software" class="muted">{{ h.software }}</span>
                          <span class="mono share">{{ (h.share * 100).toFixed(h.share >= 0.1 ? 0 : 1) }} %</span>
                          <span v-if="h.current" class="badge accent">{{ t('stream.now') }}</span>
                          <span v-else class="muted nowrap">{{ fmtAgo(h.lastSeen, now) }}</span>
                        </div>
                      </div>
                    </template>
                  </details>
                </td>
                <td class="mono right">{{ v.bandwidth ? fmtBits(v.bandwidth) : '—' }}</td>
                <td class="mono right">{{ fmtBits(variantMetric(v, 'hls_segment_bitrate_bps')) }}</td>
                <td class="mono right">
                  <template v-if="variantMetric(v, 'hls_media_width')">{{ variantMetric(v, 'hls_media_width') }}×{{ variantMetric(v, 'hls_media_height') }}</template>
                  <template v-else>{{ v.resolution?.replace('x', '×') || '—' }}</template>
                </td>
                <td class="mono right">{{ fmtNumber(variantMetric(v, 'hls_media_fps') ?? (v.frame_rate || undefined), 2) }}</td>
                <td class="mono right">{{ fmtSeconds(variantMetric(v, 'hls_media_gop_seconds')) }}</td>
                <td class="mono right">{{ fmtSeconds(variantMetric(v, 'hls_target_duration_seconds')) }}</td>
                <td class="mono muted">{{ v.codecs || '—' }}</td>
              </tr>
              <FilterEmpty :sort="variantSort" />
            </tbody>
          </table>
        </div>
      </section>

      <!-- Charts -->
      <section class="section">
        <div class="chart-controls">
          <div class="seg" role="group" :aria-label="t('stream.chartGroup')">
            <button v-for="(g, i) in chartGroups()" :key="g.title" type="button" :aria-pressed="group === i" @click="group = i">{{ g.title }}</button>
          </div>
          <span class="spacer" />
          <select v-if="stream.agents.length > 1" v-model.number="agentFilter" class="input compact" :aria-label="t('common.agent')">
            <option :value="0">{{ t('stream.allAgents') }}</option>
            <option v-for="a in stream.agents" :key="a.agent_id" :value="a.agent_id">{{ a.agent_name }}</option>
          </select>
          <select v-if="activeVariants.length > 1" v-model.number="variantFilter" class="input compact" :aria-label="t('common.playlist')">
            <option :value="0">{{ t('stream.allPlaylists') }}</option>
            <option v-for="v in activeVariants" :key="v.id" :value="v.id">{{ variantLabel(v) }}</option>
          </select>
          <button v-if="zoom" class="btn" type="button" @click="zoom = null">{{ t('stream.resetZoom') }}</button>
          <div class="seg" role="group" :aria-label="t('stream.period')">
            <button v-for="r in ranges" :key="r.seconds" type="button" :aria-pressed="!zoom && range === r.seconds" @click="((range = r.seconds), (zoom = null))">{{ r.label }}</button>
          </div>
        </div>
        <div v-if="chartsError" class="notice error">{{ chartsError }}</div>
        <div v-if="live.system && !live.system.metrics.enabled" class="notice">
          {{ t('stream.noMetricsStorageIsConnectedSo') }} {{ t('stream.giveTheServerTheVictoriametricsAddress') }}
        </div>
        <div v-if="loaded" class="charts">
          <TimeChart
            v-for="c in charts"
            :key="c.id"
            :title="c.title"
            :hint="c.hint"
            :unit="c.unit"
            :lines="loaded.lines[c.id] ?? []"
            :from="loaded.from"
            :to="loaded.to"
            :step="loaded.step"
            :steps="c.steps"
            :max="c.max"
            :threshold="c.threshold"
            :empty-hint="c.emptyHint"
            :loading="chartsLoading"
            @zoom="onZoom"
          />
        </div>
        <p v-if="hiddenByLevel" class="muted" style="margin-top: 10px">
          {{ t('stream.anotherInThisGroupCannotBe', { n: hiddenByLevel, level: levelLabel()[stream.level] ?? '' }) }}
        </p>
      </section>

      <!-- Incidents -->
      <section class="section">
        <div class="section-title"><h2>{{ t('common.incidents') }}</h2></div>
        <div class="panel">
          <IncidentTable v-if="incidents.length" :incidents="incidents" :now="now" />
          <div v-else class="empty">{{ t('stream.thisStreamHasHadNoIncidents') }}</div>
        </div>
      </section>

      <!-- Segment journal -->
      <section v-if="stream.level !== 'playlist'" class="section">
        <div class="section-title">
          <h2>{{ t('stream.segmentJournal') }}</h2>
          <label class="check"><input v-model="onlyProblems" type="checkbox" /> {{ t('stream.onlyWithProblems') }}</label>
        </div>
        <div class="panel">
          <div v-if="!segments.length" class="empty">{{ onlyProblems ? t('stream.noSegmentsWithProblems') : t('stream.noSegmentsCheckedYet') }}</div>
          <div v-else class="table-wrap">
            <table class="table">
              <thead>
                <tr>
                  <SortTh :sort="segmentSort" by="time">{{ t('stream.time') }}</SortTh>
                  <SortTh :sort="segmentSort" by="variant">{{ t('common.playlist') }}</SortTh>
                  <SortTh :sort="segmentSort" by="seq" class="right">{{ t('stream.numberSign') }}</SortTh>
                  <SortTh :sort="segmentSort" by="status" class="right">HTTP</SortTh>
                  <SortTh :sort="segmentSort" by="size" class="right">{{ t('stream.size') }}</SortTh>
                  <SortTh :sort="segmentSort" by="load" class="right" :title="stream.level === 'header' ? t('stream.timeToFirstByteTheSegment') : t('stream.timeToDownloadTheWholeSegment')">
                    {{ stream.level === 'header' ? t('common.firstByte') : t('common.download') }}
                  </SortTh>
                  <SortTh :sort="segmentSort" by="bitrate" class="right">{{ t('common.bitrate') }}</SortTh>
                  <SortTh :sort="segmentSort" by="node">{{ t('stream.node') }}</SortTh>
                  <SortTh :sort="segmentSort" by="problems">{{ t('stream.problems') }}</SortTh>
                </tr>
              </thead>
              <tbody>
                <tr v-for="s in segmentSort.rows" :key="s.id" class="clickable" tabindex="0" @click="openSegment = s" @keydown.enter="openSegment = s">
                  <td class="mono nowrap">{{ fmtWhen(s.time) }}</td>
                  <td class="truncate" style="max-width: 200px">
                    {{ variantLabel(variantById.get(s.variant_id)) }}<span v-if="stream.agents.length > 1" class="muted">, {{ live.agentName(s.agent_id) }}</span>
                  </td>
                  <td class="mono right">{{ s.seq }}</td>
                  <td class="mono right" :style="s.status !== 200 && s.status !== 206 ? 'color: #ff7a85' : ''">{{ s.status || '—' }}</td>
                  <td class="mono right nowrap">{{ fmtBytes(s.size) }}</td>
                  <td class="mono right nowrap">{{ fmtMs(s.detail.partial ? s.detail.ttfb_ms : s.total_ms) }}</td>
                  <td class="mono right nowrap">{{ s.duration > 0 && s.size ? fmtBits((s.size * 8) / s.duration) : '—' }}</td>
                  <td class="mono muted">{{ s.detail.source ? node(s.detail.source) : s.remote_addr || '—' }}</td>
                  <td>
                    <span v-for="p in s.problems" :key="p" class="badge" :class="infoKinds.has(p) ? 'accent' : 'warn'" style="margin-right: 4px">{{ problemName(p) }}</span>
                  </td>
                </tr>
                <FilterEmpty :sort="segmentSort" />
              </tbody>
            </table>
          </div>
        </div>
      </section>

      <!-- Snapshots -->
      <section v-if="snapshots.length" class="section">
        <div class="section-title">
          <h2>{{ t('stream.playlistSnapshots') }}</h2>
          <span class="muted">{{ t('stream.thePlaylistTextAtTheMoment') }}</span>
        </div>
        <div class="panel table-wrap">
          <table class="table">
            <tbody>
              <tr v-for="s in snapshots" :key="s.id" class="clickable" tabindex="0" @click="showSnapshot(s)" @keydown.enter="showSnapshot(s)">
                <td class="mono nowrap" style="width: 1%">{{ fmtWhen(s.time) }}</td>
                <td class="nowrap" style="width: 1%">{{ variantLabel(variantById.get(s.variant_id)) }}</td>
                <td>
                  <span v-for="p in s.problems" :key="p" class="badge warn" style="margin-right: 4px">{{ problemName(p) }}</span>
                </td>
                <td class="muted right nowrap">{{ live.agentName(s.agent_id) }}</td>
              </tr>
            </tbody>
          </table>
        </div>
      </section>

      <StreamFormDialog v-if="editing" :stream="stream" @close="editing = false" @saved="(s) => (stream = { ...stream!, ...s })" />
      <ConfirmDialog v-if="deleting" :title="t('common.deleteStreamQuestion')" :action="t('common.deleteStream')" :busy="busy" @close="deleting = false" @confirm="remove">
        <p>{{ t('stream.streamWillBeDeletedTogetherWith', { name: stream.name }) }} {{ t('stream.thisCannotBeUndone') }}</p>
      </ConfirmDialog>

      <ModalDialog v-if="openSegment" :title="t('stream.segmentNumber', { n: openSegment.seq })" wide @close="openSegment = null">
        <div class="modal-body">
          <dl class="kv">
            <dt>{{ t('stream.address') }}</dt>
            <dd class="mono">{{ openSegment.url }}</dd>
            <dt>{{ t('stream.checked') }}</dt>
            <dd>{{ fmtFull(openSegment.time) }}, {{ live.agentName(openSegment.agent_id) }}</dd>
            <dt>{{ t('common.response') }}</dt>
            <dd class="mono">
              {{ openSegment.status ? 'HTTP ' + openSegment.status : t('common.noResponse') }}
              <template v-if="openSegment.detail.proto">, {{ openSegment.detail.proto }}</template>
              <template v-if="openSegment.remote_addr">, {{ openSegment.remote_addr }}</template>
              <template v-if="openSegment.detail.cache">, {{ t('stream.cache') }} {{ openSegment.detail.cache }}</template>
            </dd>
            <template v-if="openSegment.detail.source">
              <dt>{{ t('stream.servedBy') }}</dt>
              <dd class="mono">
                {{ node(openSegment.detail.source) }}
                <div v-for="(val, name) in openSegment.detail.source.headers" :key="name" class="muted">{{ name }}: {{ val }}</div>
              </dd>
              <template v-if="openSegment.detail.source.redirects?.length">
                <dt>{{ t('common.redirects') }}</dt>
                <dd class="mono">
                  <div v-for="(h, hi) in openSegment.detail.source.redirects" :key="hi">
                    {{ h.status }} {{ h.url }}<span v-if="h.remote_addr" class="muted"> ({{ h.remote_addr }}<template v-if="h.server">, {{ h.server }}</template>)</span>
                  </div>
                  <div>→ {{ openSegment.detail.source.url }}</div>
                </dd>
              </template>
            </template>
            <template v-if="openSegment.error">
              <dt>{{ t('stream.error') }}</dt>
              <dd style="color: #ff8a93">{{ openSegment.error }}</dd>
            </template>
            <dt>{{ t('stream.size') }}</dt>
            <dd class="mono">
              {{ t('stream.accordingToThePlaylist', { size: fmtBytes(openSegment.size), duration: fmtSeconds(openSegment.duration) }) }}
              <div v-if="openSegment.detail.partial" class="muted">{{ t('stream.onlyTheBeginningWasRequestedDownloaded', { size: fmtBytes(openSegment.detail.fetched) }) }}</div>
            </dd>
            <dt>{{ t('common.download') }}</dt>
            <dd class="mono">
              {{ t('stream.dnsConnectTLSFirstByteTotal', { dns: fmtMs(openSegment.detail.dns_ms), connect: fmtMs(openSegment.detail.connect_ms), tls: fmtMs(openSegment.detail.tls_ms), ttfb: fmtMs(openSegment.detail.ttfb_ms), total: fmtMs(openSegment.detail.total_ms), rate: fmtBits(openSegment.detail.bps) }) }}
            </dd>
            <template v-if="openSegment.detail.program_time">
              <dt>PROGRAM-DATE-TIME</dt>
              <dd class="mono">{{ fmtFull(openSegment.detail.program_time) }}</dd>
            </template>
            <template v-if="openSegment.problems.length">
              <dt>{{ t('stream.problems') }}</dt>
              <dd><span v-for="p in openSegment.problems" :key="p" class="badge warn" style="margin: 0 4px 4px 0">{{ problemName(p) }}</span></dd>
            </template>
          </dl>

          <template v-if="openSegment.detail.media">
            <h2 style="margin: 18px 0 8px">
              {{ t('stream.contents', { container: openSegment.detail.media.container.toUpperCase() }) }}
              <span v-if="openSegment.detail.partial" class="muted" style="font-weight: 400">— {{ t('stream.fromTheBeginningOfTheSegment') }}</span>
            </h2>
            <p v-if="openSegment.detail.media.error && !openSegment.detail.partial" class="notice error">{{ openSegment.detail.media.error }}</p>
            <dl v-if="openSegment.detail.media.container === 'ts'" class="kv" style="margin-bottom: 12px">
              <dt>{{ t('stream.packets') }}</dt>
              <dd class="mono">{{ openSegment.detail.media.packets ?? 0 }}</dd>
              <dt>{{ t('common.errors') }}</dt>
              <dd class="mono">
                {{ t('stream.syncContinuityTransport', { sync: openSegment.detail.media.sync_errors ?? 0, cc: openSegment.detail.media.cc_errors ?? 0, transport: openSegment.detail.media.transport_errors ?? 0 }) }}
              </dd>
              <dt>{{ t('stream.tables') }}</dt>
              <dd class="mono">{{ openSegment.detail.media.has_pat ? t('stream.patPresent') : t('stream.patMissing') }}, {{ openSegment.detail.media.has_pmt ? t('stream.pmtPresent') : t('stream.pmtMissing') }}</dd>
            </dl>
            <div v-if="openSegment.detail.media.tracks?.length" class="table-wrap">
              <table class="table">
                <thead>
                  <tr>
                    <th>{{ t('stream.track') }}</th>
                    <th>{{ t('stream.codec') }}</th>
                    <th>{{ t('stream.parameters') }}</th>
                    <th class="right">{{ t('stream.frames') }}</th>
                    <th class="right">{{ t('stream.startPTS') }}</th>
                    <th class="right">{{ t('stream.duration') }}</th>
                    <th class="right">{{ t('common.bitrate') }}</th>
                  </tr>
                </thead>
                <tbody>
                  <tr v-for="t in openSegment.detail.media.tracks" :key="t.id">
                    <td>{{ trackKind(t.kind) }} <span class="muted mono">{{ t.id }}</span></td>
                    <td class="mono">{{ t.codec }}<template v-if="t.profile"> {{ t.profile }}<template v-if="t.level">@{{ t.level }}</template></template></td>
                    <td class="mono">{{ trackParams(t) }}</td>
                    <td class="mono right">{{ t.frames }}</td>
                    <td class="mono right">{{ t.has_timing ? t.start_pts.toFixed(3) : '—' }}</td>
                    <td class="mono right">{{ t.has_timing ? fmtSeconds(t.end_pts - t.start_pts) : '—' }}</td>
                    <td class="mono right">{{ t.has_timing && t.end_pts > t.start_pts ? fmtBits((t.bytes * 8) / (t.end_pts - t.start_pts)) : '—' }}</td>
                  </tr>
                </tbody>
              </table>
            </div>
            <p v-if="track(openSegment, 'video') && track(openSegment, 'audio')" class="muted" style="margin-top: 8px">
              {{
                track(openSegment, 'video')!.start_pts >= track(openSegment, 'audio')!.start_pts
                  ? t('stream.videoStartsLaterThanAudio', { span: fmtSeconds(Math.abs(track(openSegment, 'video')!.start_pts - track(openSegment, 'audio')!.start_pts)) })
                  : t('stream.videoStartsEarlierThanAudio', { span: fmtSeconds(Math.abs(track(openSegment, 'video')!.start_pts - track(openSegment, 'audio')!.start_pts)) })
              }}
            </p>
          </template>
        </div>
        <div class="modal-foot">
          <button class="btn" type="button" @click="openSegment = null">{{ t('common.close') }}</button>
        </div>
      </ModalDialog>

      <ModalDialog v-if="openSnapshot" :title="t('stream.playlistSnapshot')" wide @close="openSnapshot = null">
        <div class="modal-body">
          <dl class="kv" style="margin-bottom: 12px">
            <dt>{{ t('stream.address') }}</dt>
            <dd class="mono">{{ openSnapshot.url }}</dd>
            <dt>{{ t('stream.taken') }}</dt>
            <dd>{{ fmtFull(openSnapshot.time) }}, {{ live.agentName(openSnapshot.agent_id) }}</dd>
            <dt>{{ t('stream.reason') }}</dt>
            <dd><span v-for="p in openSnapshot.problems" :key="p" class="badge warn" style="margin-right: 4px">{{ problemName(p) }}</span></dd>
          </dl>
          <PlaylistPair :body="openSnapshot.body ?? ''" :time="openSnapshot.time" :previous="openSnapshot.previous_body" :previous-time="openSnapshot.previous_time" />
        </div>
        <div class="modal-foot">
          <button class="btn" type="button" @click="openSnapshot = null">{{ t('common.close') }}</button>
        </div>
      </ModalDialog>
    </template>
  </div>
</template>

<style scoped>
.title {
  display: grid;
  gap: 1px;
  min-width: 0;
}

.title a {
  font-size: 12px;
}

.summary {
  padding: 12px 14px;
}

.states {
  display: flex;
  flex-wrap: wrap;
  gap: 4px 12px;
}

.state {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  white-space: nowrap;
}

.served {
  min-width: 220px;
  font-size: 12px;
}

.served > div + div {
  margin-top: 4px;
}

.servers {
  margin-top: 6px;
}

.servers summary {
  cursor: pointer;
  color: var(--accent);
}

.servers-group {
  margin-top: 6px;
}

.server-row {
  display: flex;
  flex-wrap: wrap;
  align-items: baseline;
  gap: 2px 10px;
  padding: 2px 0;
}

.server-row .share {
  color: var(--text-strong);
}

.chart-controls {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px;
  margin-bottom: 12px;
}

.chart-controls .spacer {
  flex: 1;
}

.charts {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(min(100%, 420px), 1fr));
  gap: 12px;
}
</style>
