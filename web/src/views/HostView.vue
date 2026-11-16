<script setup lang="ts">
import { computed, ref } from 'vue'
import { api } from '@/api'
import { statusLabel } from '@/catalog'
import { fmtAgo, fmtBits, fmtSeconds } from '@/format'
import { t } from '@/i18n'
import { usePolling } from '@/poll'
import { useSort } from '@/sort'
import { useLive } from '@/stores/live'
import type { ServerView, Status, Stream } from '@/types'
import FilterEmpty from '@/components/FilterEmpty.vue'
import IncidentTable from '@/components/IncidentTable.vue'
import SortTh from '@/components/SortTh.vue'

const props = defineProps<{ host: string; ip?: string }>()
const live = useLive()

const servers = ref<ServerView[] | null>(null)
const { error } = usePolling(async (signal) => {
  servers.value = await api.get<ServerView[]>('/api/v1/servers', signal)
}, 30_000)

/** The server by host and address; without an address, every address of the host together. */
const server = computed<ServerView | null>(() => {
  const found = (servers.value ?? []).filter((s) => s.host === props.host && (!props.ip || s.ip === props.ip))
  const first = found[0]
  if (!first) return null
  if (found.length === 1) return first
  const ids = [...new Set(found.flatMap((s) => s.stream_ids))]
  return {
    ...first,
    ip: found.map((s) => s.ip).join(', '),
    stream_ids: ids,
    streams: ids.length,
    playlists: found.some((s) => s.playlists),
    segments: found.some((s) => s.segments),
    last_seen: found.map((s) => s.last_seen).sort().at(-1) ?? first.last_seen,
  }
})
/** Other addresses the same host name answered from. */
const siblings = computed(() => (props.ip ? (servers.value ?? []).filter((s) => s.host === props.host && s.ip !== props.ip) : []))

const order: Record<Status, number> = { fail: 0, warn: 1, nodata: 2, ok: 3, disabled: 4 }
const streams = computed<Stream[]>(() => {
  const ids = new Set(server.value?.stream_ids ?? [])
  return (live.overview?.streams ?? []).filter((s) => ids.has(s.id)).sort((a, b) => order[a.status] - order[b.status] || a.name.localeCompare(b.name, 'ru'))
})
const count = (st: Status) => streams.value.filter((s) => s.status === st).length

function figure(s: Stream, key: string): number | undefined {
  for (const a of s.agents) {
    const v = a.summary[key]
    if (v !== undefined) return v
  }
  return undefined
}

const sort = useSort(() => streams.value, {
  name: { value: (s) => s.name, filter: 'text', label: (s) => `${s.name} ${s.url}` },
  status: { value: (s) => 4 - order[s.status], filter: 'choice', label: (s) => statusLabel()[s.status] },
  incidents: { value: (s) => (s.open_incidents.crit ?? 0) * 1000 + (s.open_incidents.warn ?? 0), filter: 'range' },
  bitrate: { value: (s) => figure(s, 'bitrate_bps'), filter: 'range', unit: t('format.mbps'), scale: 1e6 },
  playlist: { value: (s) => figure(s, 'playlist_total_seconds'), filter: 'range', unit: t('format.ms'), scale: 1e-3 },
}, 'host-streams')

const incidents = computed(() => {
  const ids = new Set(server.value?.stream_ids ?? [])
  return (live.overview?.incidents ?? []).filter((i) => ids.has(i.stream_id))
})

const serves = computed(() => {
  const s = server.value
  if (!s) return ''
  return s.playlists && s.segments ? t('common.playlistsAndSegments') : s.playlists ? t('common.playlistsWord') : t('common.segmentsWord')
})
</script>

<template>
  <div class="page">
    <div class="page-head">
      <div class="title">
        <RouterLink :to="{ name: 'dashboard' }" class="muted">{{ t('common.servers') }}</RouterLink>
        <h1>{{ host }}</h1>
      </div>
      <span v-if="server" class="badge" :class="count('fail') ? 'fail' : count('warn') ? 'warn' : 'ok'">
        {{ count('fail') ? t('common.failing', { n: count('fail') }) : count('warn') ? t('common.withWarnings', { n: count('warn') }) : t('host.streamsOK') }}
      </span>
    </div>
    <div v-if="error" class="notice error">{{ error }}</div>

    <div v-if="servers && !server" class="panel empty">
      <p class="strong">{{ t('host.thisServerHasNotAnsweredIn') }}</p>
      <p>{{ t('host.onlyServersThatRecentlyReturnedPlaylists') }} <RouterLink :to="{ name: 'dashboard' }">{{ t('host.allServers') }}</RouterLink></p>
    </div>
    <div v-else-if="!server" class="panel empty">{{ t('common.loading') }}</div>

    <template v-else>
      <div class="panel summary">
        <dl class="kv">
          <dt>{{ t('host.address') }}</dt>
          <dd class="mono">{{ server.ip || '—' }}</dd>
          <template v-if="siblings.length">
            <dt>{{ t('host.otherAddressesOfThisName') }}</dt>
            <dd class="mono">
              <template v-for="(o, i) in siblings" :key="o.ip"><template v-if="i">, </template><RouterLink :to="{ name: 'server', params: { host: o.host, ip: o.ip } }">{{ o.ip }}</RouterLink></template>
            </dd>
          </template>
          <dt>{{ t('common.software') }}</dt>
          <dd class="mono">{{ server.software || '—' }}</dd>
          <dt>{{ t('common.serves') }}</dt>
          <dd>{{ serves }}</dd>
          <dt>{{ t('host.lastResponse') }}</dt>
          <dd>{{ fmtAgo(server.last_seen, live.updatedAt || undefined) }}</dd>
          <dt>{{ t('common.streams') }}</dt>
          <dd>
            {{ t('host.okWithWarningsFailing', { n: streams.length, streams: t('common.streamPlural', streams.length), ok: count('ok'), warn: count('warn'), fail: count('fail') }) }}
          </dd>
        </dl>
        <p class="muted note">{{ t('host.aStreamIsListedHereIf') }}</p>
      </div>

      <section class="section">
        <div class="section-title"><h2>{{ t('host.streamsOfThisServer') }}</h2></div>
        <div class="panel table-wrap">
          <table class="table">
            <thead>
              <tr>
                <SortTh :sort="sort" by="name">{{ t('common.stream') }}</SortTh>
                <SortTh :sort="sort" by="status">{{ t('common.status') }}</SortTh>
                <SortTh :sort="sort" by="incidents">{{ t('host.openIncidents') }}</SortTh>
                <SortTh :sort="sort" by="bitrate" class="right">{{ t('common.bitrate') }}</SortTh>
                <SortTh :sort="sort" by="playlist" class="right">{{ t('common.playlistResponse') }}</SortTh>
                <th />
              </tr>
            </thead>
            <tbody>
              <tr v-for="s in sort.rows" :key="s.id">
                <td>
                  <div class="name"><span class="dot" :class="s.status" /><RouterLink :to="{ name: 'stream', params: { id: s.id } }" class="strong">{{ s.name }}</RouterLink></div>
                </td>
                <td>{{ statusLabel()[s.status] }}</td>
                <td>
                  <span v-if="s.open_incidents.crit" class="badge fail">{{ s.open_incidents.crit }} {{ t('common.failures', s.open_incidents.crit) }}</span>
                  <span v-if="s.open_incidents.warn" class="badge warn" style="margin-left: 4px">
                    {{ s.open_incidents.warn }} {{ t('common.warnings', s.open_incidents.warn) }}
                  </span>
                </td>
                <td class="mono right nowrap">{{ fmtBits(figure(s, 'bitrate_bps')) }}</td>
                <td class="mono right nowrap">{{ fmtSeconds(figure(s, 'playlist_total_seconds')) }}</td>
                <td class="right nowrap"><RouterLink :to="{ name: 'diagnose', params: { id: s.id } }">{{ t('common.liveDiagnosis') }}</RouterLink></td>
              </tr>
              <FilterEmpty :sort="sort" />
            </tbody>
          </table>
        </div>
      </section>

      <section class="section">
        <div class="section-title"><h2>{{ t('host.notOKNowOnTheseStreams') }}</h2></div>
        <div class="panel">
          <IncidentTable v-if="incidents.length" :incidents="incidents" show-stream :now="live.updatedAt" />
          <div v-else class="empty">{{ t('common.noOpenIncidents') }}</div>
        </div>
      </section>
    </template>
  </div>
</template>

<style scoped>
.summary {
  padding: 14px 16px;
}

.note {
  margin: 10px 0 0;
  font-size: 12px;
}

.name {
  display: flex;
  align-items: center;
  gap: 8px;
}
</style>
