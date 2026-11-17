<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { api } from '@/api'
import { fmtUnit, statusLabel, type Unit } from '@/catalog'
import { fmtAgo, fmtBits, fmtSeconds } from '@/format'
import { t } from '@/i18n'
import { usePolling } from '@/poll'
import { useSort } from '@/sort'
import { useLive } from '@/stores/live'
import { useSession } from '@/stores/session'
import type { Aggregate, ServerSeries, ServersNetwork, ServerView, Status, Stream, Timeline } from '@/types'
import FilterEmpty from '@/components/FilterEmpty.vue'
import IncidentTable from '@/components/IncidentTable.vue'
import ServerIncidentTable from '@/components/ServerIncidentTable.vue'
import SortTh from '@/components/SortTh.vue'
import StatusStrip from '@/components/StatusStrip.vue'
import StreamFormDialog from '@/components/StreamFormDialog.vue'
import TimeChart, { type ChartLine } from '@/components/TimeChart.vue'

const live = useLive()
const session = useSession()
const router = useRouter()

const ranges = [
  { label: '1 ' + t('format.h'), seconds: 3600 },
  { label: '6 ' + t('format.h'), seconds: 6 * 3600 },
  { label: '24 ' + t('format.h'), seconds: 24 * 3600 },
  { label: '7 ' + t('format.d'), seconds: 7 * 24 * 3600 },
]
const range = ref(Number(localStorage.getItem('dash-range')) || 3600)
watch(range, (v) => localStorage.setItem('dash-range', String(v)))

// ── Figures over all streams ─────────────────────────────────────────
const aggregate = ref<Aggregate | null>(null)
const { error: aggregateError, loading: aggregateLoading, refresh: reloadAggregate } = usePolling(async (signal) => {
  aggregate.value = await api.get<Aggregate>(`/api/v1/aggregate?range=${range.value}&buckets=120`, signal)
}, 30_000)

interface SummaryChart {
  id: string
  title: string
  hint: string
  unit: Unit
  steps?: boolean
  colors?: string[]
  /** Counts: a step without a point is zero, not a gap. */
  zeroFill?: boolean
  series: { id: string; label: string }[]
  /** A line per server, of this figure, instead of the series. */
  perServer?: 'rtt' | 'loss' | 'requests'
}

const green = '#2ec4b6'
const orange = '#f4a261'
const red = '#e63946'

const summaryCharts: SummaryChart[] = [
  {
    id: 'states', title: t('dashboard.streamsByStatus'), hint: t('dashboard.howManyStreamsWereInEach'), unit: 'count', steps: true, zeroFill: true,
    colors: [red, orange, green],
    series: [
      { id: 'streams_fail', label: t('common.failure') },
      { id: 'streams_warn', label: t('common.warning') },
      { id: 'streams_ok', label: t('common.ok') },
    ],
  },
  {
    id: 'failures', title: t('dashboard.failuresAndLosses'), hint: t('dashboard.unavailablePlaylistsSegmentErrorsSegmentsMissed'), unit: 'count', steps: true, zeroFill: true,
    colors: [red, orange, '#b197fc', '#ffd166'],
    series: [
      { id: 'playlists_down', label: t('dashboard.playlistsUnavailable') },
      { id: 'segment_errors', label: t('common.segmentErrors') },
      { id: 'segments_missed', label: t('dashboard.missedSegments') },
      { id: 'stalls', label: t('dashboard.stalls') },
    ],
  },
  {
    id: 'playlist-time', title: t('common.playlistResponse'), hint: t('dashboard.playlistRequestTimeOverAllStreams'), unit: 's',
    series: [
      { id: 'playlist_time_avg', label: t('dashboard.average') },
      { id: 'playlist_time_p95', label: '95 %' },
    ],
  },
  {
    id: 'rtt', title: t('dashboard.roundTripTime'), hint: t('dashboard.roundTripTimeHint'), unit: 's',
    series: [
      { id: 'rtt_avg', label: t('dashboard.average') },
      { id: 'rtt_p95', label: '95 %' },
    ],
  },
  {
    id: 'out-of-order', title: t('dashboard.signsOfLoss'), hint: t('dashboard.signsOfLossHint'), unit: 'count', steps: true, zeroFill: true,
    series: [
      { id: 'retransmits', label: t('dashboard.retransmitsPerStep') },
      { id: 'out_of_order', label: t('dashboard.outOfOrderPerStep') },
    ],
  },
  {
    id: 'rtt-by-server', title: t('dashboard.rttByServer'), hint: t('dashboard.rttByServerHint'), unit: 's', perServer: 'rtt',
    series: [],
  },
  {
    id: 'loss-by-server', title: t('dashboard.lossByServer'), hint: t('dashboard.lossByServerHint'), unit: 'pct', perServer: 'loss',
    series: [],
  },
  {
    id: 'requests-by-server', title: t('dashboard.requestsByServer'), hint: t('dashboard.requestsByServerHint'), unit: 'count', perServer: 'requests',
    series: [],
  },
  {
    id: 'connect', title: t('dashboard.connectionSetup'), hint: t('dashboard.connectionSetupHint'), unit: 's',
    series: [
      { id: 'connect_avg', label: t('dashboard.connectAverage') },
      { id: 'connect_p95', label: t('dashboard.connect95') },
      { id: 'dns_avg', label: 'DNS' },
      { id: 'tls_avg', label: 'TLS' },
    ],
  },
  {
    id: 'entry-time', title: t('dashboard.entryTime'), hint: t('dashboard.entryTimeHint'), unit: 's',
    series: [
      { id: 'entry_time_avg', label: t('dashboard.average') },
      { id: 'entry_time_p95', label: '95 %' },
    ],
  },
  {
    id: 'entry-redirects', title: t('dashboard.entryRedirects'), hint: t('dashboard.entryRedirectsHint'), unit: 'count',
    series: [{ id: 'entry_redirects_avg', label: t('dashboard.redirectsPerRequest') }],
  },
  {
    id: 'segment-ttfb', title: t('dashboard.segmentFirstByte'), hint: t('dashboard.timeToTheFirstByteOf'), unit: 's',
    series: [
      { id: 'segment_ttfb_avg', label: t('dashboard.average') },
      { id: 'segment_ttfb_p95', label: '95 %' },
    ],
  },
  {
    id: 'source-changes', title: t('common.answeringServerChanges'), hint: t('dashboard.howManyTimesAPlaylistCame'), unit: 'count', steps: true, zeroFill: true,
    series: [{ id: 'source_changes', label: t('dashboard.changesPerStep') }],
  },
  {
    id: 'bitrate', title: t('dashboard.totalBitrateOfStreams'), hint: t('dashboard.sumOfTheBitratesOfStreams'), unit: 'bps',
    series: [
      { id: 'bitrate_total', label: t('dashboard.bitrateOfStreams') },
      { id: 'check_traffic', label: t('dashboard.trafficOfChecks') },
    ],
  },
]

function lines(c: SummaryChart): ChartLine[] {
  const a = aggregate.value
  if (!a) return []
  if (c.perServer) {
    const figure = c.perServer
    return busiest.value.map((s) => ({ label: serverLabel(s), points: s.series[figure] ?? [] }))
  }
  return c.series.map((s) => {
    let points = a.series[s.id] ?? []
    if (c.zeroFill) {
      const have = new Map(points.map(([t, v]) => [Math.round((t - a.from) / a.step), v]))
      const n = Math.round((a.to - a.from) / a.step)
      points = []
      for (let i = 0; i <= n; i++) points.push([a.from + i * a.step, have.get(i) ?? 0])
    }
    return { label: s.label, points }
  })
}

// ── Servers ──────────────────────────────────────────────────────────
const network = ref<ServersNetwork | null>(null)
const { refresh: reloadNetwork } = usePolling(async (signal) => {
  network.value = await api.get<ServersNetwork>(`/api/v1/servers/network?range=${range.value}&buckets=120`, signal)
}, 30_000)
// A chart reads well with a dozen lines at most: the servers that answer the most.
const busiest = computed(() => {
  const total = (s: ServerSeries) => (s.series.requests ?? []).reduce((sum, p) => sum + p[1], 0)
  return [...(network.value?.servers ?? [])].sort((a, b) => total(b) - total(a)).slice(0, 12).sort((a, b) => serverLabel(a).localeCompare(serverLabel(b)))
})
function serverLabel(s: ServerSeries): string {
  const same = (network.value?.servers ?? []).filter((o) => o.host === s.host).length
  return same > 1 && s.ip ? `${s.host} (${s.ip})` : s.host
}
const lossOf = (s: ServerView) => (s.network && s.network.requests > 0 ? s.network.retransmits / s.network.requests : undefined)

const servers = ref<ServerView[]>([])
usePolling(async (signal) => {
  servers.value = await api.get<ServerView[]>('/api/v1/servers', signal)
}, 30_000)
const serverSort = useSort(() => servers.value, {
  host: { value: (s) => s.host, filter: 'text', label: (s) => `${s.host} ${s.ip}` },
  software: { value: (s) => s.software, filter: 'choice' },
  serves: { value: (s) => (s.playlists && s.segments ? t('common.playlistsAndSegments') : s.playlists ? t('common.playlistsWord') : t('common.segmentsWord')), filter: 'choice' },
  streams: { value: (s) => s.streams, filter: 'range' },
  ok: { value: (s) => s.ok, filter: 'range' },
  warn: { value: (s) => s.warn, filter: 'range' },
  fail: { value: (s) => s.fail, filter: 'range' },
  rtt: { value: (s) => s.network?.rtt_seconds ?? -1, filter: 'range' },
  loss: { value: (s) => lossOf(s) ?? -1, filter: 'range' },
}, 'dash-servers')

// ── Streams ──────────────────────────────────────────────────────────
const timeline = ref<Timeline | null>(null)
const { error: timelineError, refresh: reloadTimeline } = usePolling(async (signal) => {
  timeline.value = await api.get<Timeline>(`/api/v1/timeline?range=${range.value}&buckets=90`, signal)
}, 30_000)
watch(range, () => {
  void reloadAggregate()
  void reloadTimeline()
  void reloadNetwork()
})

const order: Record<Status, number> = { fail: 0, warn: 1, nodata: 2, ok: 3, disabled: 4 }
const scope = ref<'problems' | 'all'>('problems')
const search = ref('')
const shown = ref(50)
watch([scope, search], () => (shown.value = 50))

const sorted = computed(() =>
  [...(live.overview?.streams ?? [])].sort((a, b) => order[a.status] - order[b.status] || a.name.localeCompare(b.name, 'ru')),
)
const problems = computed(() => sorted.value.filter((s) => s.status === 'fail' || s.status === 'warn' || s.status === 'nodata'))
const filtered = computed(() => {
  const q = search.value.trim().toLowerCase()
  // With nothing wrong the "problems" view would be empty; show everything instead.
  const base = scope.value === 'problems' && problems.value.length ? problems.value : sorted.value
  return q ? sorted.value.filter((s) => s.name.toLowerCase().includes(q) || s.url.toLowerCase().includes(q)) : base
})
const streamSort = useSort(() => filtered.value, {
  name: { value: (s) => s.name, filter: 'text', label: (s) => `${s.name} ${s.url}` },
  // The worst first when sorted from the largest.
  status: { value: (s) => 4 - order[s.status], filter: 'choice', label: (s) => statusLabel()[s.status] },
  bitrate: { value: (s) => figure(s, 'bitrate_bps'), filter: 'range', unit: t('format.mbps'), scale: 1e6 },
  playlist: { value: (s) => figure(s, 'playlist_total_seconds'), filter: 'range', unit: t('format.ms'), scale: 1e-3 },
  agents: { value: (s) => s.agents.map((a) => a.agent_name).join(', '), filter: 'choice', label: (s) => s.agents.map((a) => a.agent_name).join(', ') || t('common.notAssigned') },
}, 'dash-streams')
const visible = computed(() => streamSort.rows.slice(0, shown.value))

const counts = computed(() => live.overview?.counts ?? {})
const total = computed(() => counts.value.total ?? 0)
const shares = computed(() =>
  (['fail', 'warn', 'nodata', 'ok', 'disabled'] as Status[]).map((s) => ({ status: s, n: counts.value[s] ?? 0 })).filter((s) => s.n > 0),
)

const headline = computed(() => {
  const c = counts.value
  if (!total.value) return t('common.noStreamsYet')
  if (c.fail) return t('dashboard.ofFailing', { n: c.fail, streams: t('common.streamPlural', c.fail), total: total.value })
  if (c.warn) return t('dashboard.ofWithWarnings', { n: c.warn, streams: t('common.streamPlural', c.warn), total: total.value })
  if (c.nodata && c.nodata === total.value - (c.disabled ?? 0)) return t('dashboard.noDataOnStreams')
  return t('dashboard.allOK', { n: total.value, streams: t('common.streamPlural', total.value) })
})
const headlineStatus = computed<Status>(() => (counts.value.fail ? 'fail' : counts.value.warn ? 'warn' : total.value ? 'ok' : 'nodata'))

// Best figure across agents: the summary of the first agent that has it.
function figure(s: Stream, key: string): number | undefined {
  for (const a of s.agents) {
    const v = a.summary[key]
    if (v !== undefined) return v
  }
  return undefined
}

function resolution(s: Stream): string {
  const h = figure(s, 'height')
  return h ? `${h}p` : ''
}

const creating = ref(false)
</script>

<template>
  <div class="page">
    <div class="page-head">
      <h1 class="headline"><span class="dot" :class="headlineStatus" />{{ headline }}</h1>
      <span class="spacer" />
      <div class="seg" role="group" :aria-label="t('dashboard.historyPeriod')">
        <button v-for="r in ranges" :key="r.seconds" type="button" :aria-pressed="range === r.seconds" @click="range = r.seconds">{{ r.label }}</button>
      </div>
      <button v-if="session.isAdmin" class="btn primary" type="button" @click="creating = true">{{ t('common.addStream') }}</button>
    </div>

    <div v-if="total" class="share" role="img" :aria-label="headline">
      <span v-for="s in shares" :key="s.status" :class="s.status" :style="{ flexGrow: s.n }" />
    </div>

    <!-- All streams at once -->
    <template v-if="total">
      <div v-if="aggregateError" class="notice error">{{ aggregateError }}</div>
      <div v-if="aggregate" class="charts">
        <TimeChart
          v-for="c in summaryCharts"
          :key="c.id"
          :title="c.title"
          :hint="c.hint"
          :unit="c.unit"
          :lines="lines(c)"
          :from="aggregate.from"
          :to="aggregate.to"
          :step="aggregate.step"
          :steps="c.steps"
          :colors="c.colors"
          :loading="aggregateLoading"
        />
      </div>
    </template>

    <!-- Servers -->
    <section v-if="servers.length" class="section">
      <div class="section-title">
        <h2>{{ t('common.servers') }}</h2>
        <span class="muted">{{ t('dashboard.whichServersAnsweredInTheLast') }}</span>
      </div>
      <div class="panel table-wrap">
        <table class="table">
          <thead>
            <tr>
              <SortTh :sort="serverSort" by="host">{{ t('common.server') }}</SortTh>
              <SortTh :sort="serverSort" by="software">{{ t('common.software') }}</SortTh>
              <SortTh :sort="serverSort" by="serves">{{ t('common.serves') }}</SortTh>
              <SortTh :sort="serverSort" by="streams" class="right">{{ t('common.streamsCount') }}</SortTh>
              <SortTh :sort="serverSort" by="ok" class="right">{{ t('common.ok') }}</SortTh>
              <SortTh :sort="serverSort" by="warn" class="right">{{ t('common.warning') }}</SortTh>
              <SortTh :sort="serverSort" by="fail" class="right">{{ t('common.failure') }}</SortTh>
              <SortTh :sort="serverSort" by="rtt" class="right" :title="t('dashboard.rttByServerHint')">RTT</SortTh>
              <SortTh :sort="serverSort" by="loss" class="right" :title="t('dashboard.lossByServerHint')">{{ t('dashboard.loss') }}</SortTh>
              <th>{{ t('dashboard.failingStreams') }}</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="s in serverSort.rows" :key="s.host + s.ip">
              <td>
                <div class="name"><span class="dot" :class="s.fail ? 'fail' : s.warn ? 'warn' : 'ok'" /><RouterLink :to="{ name: 'server', params: { host: s.host, ip: s.ip } }" class="mono strong">{{ s.host }}</RouterLink></div>
                <div v-if="s.ip && s.ip !== s.host" class="mono muted" style="margin-left: 16px">{{ s.ip }}</div>
              </td>
              <td class="mono">{{ s.software || '—' }}</td>
              <td>{{ s.playlists && s.segments ? t('common.playlistsAndSegments') : s.playlists ? t('common.playlistsWord') : t('common.segmentsWord') }}</td>
              <td class="mono right">{{ s.streams }}</td>
              <td class="mono right">{{ s.ok }}</td>
              <td class="mono right" :style="s.warn ? 'color: var(--accent-warm)' : ''">{{ s.warn }}</td>
              <td class="mono right" :style="s.fail ? 'color: #ff7a85' : ''">{{ s.fail }}</td>
              <td class="mono right">{{ s.network ? fmtSeconds(s.network.rtt_seconds) : '—' }}</td>
              <td class="mono right" :style="s.network?.lossy ? 'color: var(--accent-warm)' : ''" :title="s.network?.lossy ? t('dashboard.lossyServer') : ''">{{ fmtUnit(lossOf(s), 'pct') }}</td>
              <td class="muted failing">
                <template v-for="(f, i) in s.failing_streams" :key="f.id"><template v-if="i">, </template><RouterLink :to="{ name: 'stream', params: { id: f.id } }">{{ f.name }}</RouterLink></template>
                <RouterLink v-if="s.fail > s.failing_streams.length" :to="{ name: 'server', params: { host: s.host, ip: s.ip } }"> {{ t('dashboard.andMore', { n: s.fail - s.failing_streams.length }) }}</RouterLink>
              </td>
            </tr>
            <FilterEmpty :sort="serverSort" />
          </tbody>
        </table>
      </div>
    </section>

    <!-- Streams -->
    <section class="section">
      <div class="section-title streams-head">
        <h2>{{ t('common.streams') }}</h2>
        <span class="spacer" />
        <input v-model="search" class="input compact" type="search" :placeholder="t('dashboard.findAStream')" :aria-label="t('common.streamSearch')" style="width: 220px" />
        <div class="seg" role="group" :aria-label="t('dashboard.whichStreamsToShow')">
          <button type="button" :aria-pressed="scope === 'problems'" @click="scope = 'problems'">{{ t('dashboard.notOK') }}<template v-if="problems.length"> ({{ problems.length }})</template></button>
          <button type="button" :aria-pressed="scope === 'all'" @click="scope = 'all'">{{ t('dashboard.all', { n: total }) }}</button>
        </div>
      </div>
      <div class="panel">
        <div v-if="!live.overview" class="empty">{{ t('common.loading') }}</div>
        <div v-else-if="!total" class="empty">
          <p class="strong">{{ t('dashboard.streamsAndTheirStatusHistoryWill') }}</p>
          <p v-if="session.isAdmin">{{ t('dashboard.addTheFirstStreamChecksStart') }}</p>
          <p v-else>{{ t('dashboard.streamsAreAddedByAnAdministrator') }}</p>
        </div>
        <div v-else-if="!filtered.length" class="empty">{{ t('common.nothingFoundFor', { q: search }) }}</div>
        <template v-else>
          <div class="table-wrap">
            <table class="table wall">
              <thead>
                <tr>
                  <SortTh :sort="streamSort" by="name">{{ t('common.stream') }}</SortTh>
                  <SortTh :sort="streamSort" by="status" class="strip-col" :title="t('dashboard.sortedByTheCurrentStatus')">
                    {{ t('common.statusHistory') }}
                    <span v-if="timelineError" class="muted" :title="timelineError"> {{ t('dashboard.unavailable') }}</span>
                  </SortTh>
                  <SortTh :sort="streamSort" by="bitrate" class="right hide-narrow">{{ t('common.bitrate') }}</SortTh>
                  <SortTh :sort="streamSort" by="playlist" class="right hide-narrow">{{ t('common.playlistResponse') }}</SortTh>
                  <SortTh :sort="streamSort" by="agents" class="hide-narrow">{{ t('common.agents') }}</SortTh>
                </tr>
              </thead>
              <tbody>
                <tr v-for="s in visible" :key="s.id" class="clickable" @click="router.push({ name: 'stream', params: { id: s.id } })">
                  <td class="name-col">
                    <div class="name">
                      <span class="dot" :class="s.status" />
                      <RouterLink :to="{ name: 'stream', params: { id: s.id } }" class="strong truncate" @click.stop>{{ s.name }}</RouterLink>
                      <span v-if="resolution(s)" class="muted mono">{{ resolution(s) }}</span>
                    </div>
                    <div v-if="(s.open_incidents.crit ?? 0) + (s.open_incidents.warn ?? 0)" class="sub">
                      <span v-if="s.open_incidents.crit" class="badge fail">{{ s.open_incidents.crit }} {{ t('common.failures', s.open_incidents.crit) }}</span>
                      <span v-if="s.open_incidents.warn" class="badge warn">
                        {{ s.open_incidents.warn }} {{ t('common.warnings', s.open_incidents.warn) }}
                      </span>
                    </div>
                  </td>
                  <td class="strip-col">
                    <StatusStrip v-if="timeline" :points="timeline.streams[String(s.id)]" :from="timeline.from" :to="timeline.to" :step="timeline.step" />
                  </td>
                  <td class="mono right hide-narrow nowrap">{{ fmtBits(figure(s, 'bitrate_bps')) }}</td>
                  <td class="mono right hide-narrow nowrap">{{ fmtSeconds(figure(s, 'playlist_total_seconds')) }}</td>
                  <td class="hide-narrow">
                    <span class="agents">
                      <span v-for="a in s.agents" :key="a.agent_id" class="agent" :title="t('dashboard.checked', { agent: a.agent_name, ago: fmtAgo(a.checked_at) })">
                        <span class="dot" :class="a.status" />{{ a.agent_name }}
                      </span>
                      <span v-if="!s.agents.length" class="muted">{{ t('common.notAssigned') }}</span>
                    </span>
                  </td>
                </tr>
                <FilterEmpty :sort="streamSort" />
              </tbody>
            </table>
          </div>
          <div v-if="streamSort.rows.length > visible.length" class="more">
            <button class="btn" type="button" @click="shown += 100">{{ t('dashboard.showMoreOf', { n: Math.min(100, streamSort.rows.length - visible.length), total: streamSort.rows.length - visible.length }) }}</button>
          </div>
        </template>
      </div>
    </section>

    <section v-if="live.overview?.server_incidents?.length" class="section">
      <div class="section-title">
        <h2>{{ t('incidents.ofServers') }}</h2>
        <span class="muted">{{ t('incidents.ofServersHint') }}</span>
      </div>
      <div class="panel">
        <ServerIncidentTable :incidents="live.overview.server_incidents" :now="live.updatedAt" />
      </div>
    </section>

    <section class="section">
      <div class="section-title">
        <h2>{{ t('dashboard.notOKNow') }}</h2>
        <RouterLink :to="{ name: 'incidents' }">{{ t('dashboard.allIncidents') }}</RouterLink>
      </div>
      <div class="panel">
        <IncidentTable v-if="live.overview?.incidents.length" :incidents="live.overview.incidents" show-stream :now="live.updatedAt" />
        <div v-else class="empty">{{ t('common.noOpenIncidents') }}</div>
      </div>
    </section>

    <StreamFormDialog v-if="creating" @close="creating = false" @saved="(s) => router.push({ name: 'stream', params: { id: s.id } })" />
  </div>
</template>

<style scoped>
.headline {
  display: flex;
  align-items: center;
  gap: 10px;
}

.headline .dot {
  width: 10px;
  height: 10px;
}

.share {
  display: flex;
  gap: 2px;
  height: 4px;
  margin-bottom: 14px;
  border-radius: 2px;
  overflow: hidden;
}

.share span {
  background: var(--nodata);
}

.share .ok {
  background: var(--success);
}

.share .warn {
  background: var(--accent-warm);
}

.share .fail {
  background: var(--danger);
}

.share .disabled {
  background: var(--border);
}

.charts {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(min(100%, 420px), 1fr));
  gap: 12px;
}

.streams-head {
  align-items: center;
  flex-wrap: wrap;
}

.streams-head .spacer {
  flex: 1;
}

.failing {
  max-width: 420px;
  font-size: 12px;
}

.wall .name-col {
  width: 28%;
  max-width: 360px;
}

.wall .strip-col {
  width: 42%;
  min-width: 180px;
}

.name {
  display: flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
}

.sub {
  display: flex;
  gap: 6px;
  margin: 4px 0 0 16px;
}

.agents {
  display: flex;
  flex-wrap: wrap;
  gap: 4px 12px;
}

.agent {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  white-space: nowrap;
}

.more {
  padding: 10px 14px;
  border-top: 1px solid var(--border-subtle);
  text-align: center;
}
</style>
