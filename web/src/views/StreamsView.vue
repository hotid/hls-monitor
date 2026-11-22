<script setup lang="ts">
import { computed, ref } from 'vue'
import { useRouter } from 'vue-router'
import { api, basePath } from '@/api'
import { levelLabel, problemName, statusLabel } from '@/catalog'
import { fmtAgo, fmtBits, fmtNumber } from '@/format'
import { t } from '@/i18n'
import { timeValue, useSort } from '@/sort'
import { useLive } from '@/stores/live'
import { useSession } from '@/stores/session'
import { useToasts } from '@/stores/toasts'
import type { Incident, Status, Stream } from '@/types'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import FilterEmpty from '@/components/FilterEmpty.vue'
import SortTh from '@/components/SortTh.vue'
import StatusBadge from '@/components/StatusBadge.vue'
import StreamFormDialog from '@/components/StreamFormDialog.vue'
import StreamImportDialog from '@/components/StreamImportDialog.vue'

const live = useLive()
const session = useSession()
const toasts = useToasts()
const router = useRouter()

const filter = ref('')
const found = computed(() => {
  const q = filter.value.trim().toLowerCase()
  const all = live.overview?.streams ?? []
  return q ? all.filter((s) => s.name.toLowerCase().includes(q) || s.url.toLowerCase().includes(q) || rangeWord(s).toLowerCase() === q) : all
})

const editing = ref<Stream | null>(null)
const creating = ref(false)
const importing = ref(false)
const deleting = ref<Stream | null>(null)
const busy = ref(false)

function lastChecked(s: Stream): string | undefined {
  return s.agents.map((a) => a.checked_at).filter((t): t is string => t !== null).sort().at(-1)
}

const notChecked = t('common.notCheckedYet')
function lastCheck(s: Stream): string {
  const t = lastChecked(s)
  return t ? fmtAgo(t, live.updatedAt) : notChecked
}

// Why a stream is not in order: its open incidents, the failures first,
// each kind once.
const reasons = computed(() => {
  const by = new Map<number, Incident[]>()
  for (const i of live.overview?.incidents ?? []) {
    const list = by.get(i.stream_id) ?? []
    if (!list.some((o) => o.kind === i.kind)) list.push(i)
    by.set(i.stream_id, list)
  }
  for (const list of by.values()) list.sort((a, b) => Number(b.severity === 'crit') - Number(a.severity === 'crit'))
  return by
})
const why = (s: Stream) => (s.status === 'fail' || s.status === 'warn' ? (reasons.value.get(s.id) ?? []) : [])

// The figures of a stream as its vantage points last saw it: the largest of each.
function figure(s: Stream, key: string): number | undefined {
  const v = s.agents.map((a) => a.summary?.[key]).filter((x): x is number => typeof x === 'number' && x > 0)
  return v.length ? Math.max(...v) : undefined
}
// The dynamic range of the picture when it is a high one: found by the
// search as well ("hdr", "hlg"), so that such streams can be picked out.
function rangeWord(s: Stream): string {
  const r = figure(s, 'hdr')
  return r === 1 ? 'HDR' : r === 2 ? 'HLG' : ''
}
// A figure that may be negative or zero, as the agents last saw it: the
// largest of them; undefined where none was told.
function anyFigure(s: Stream, key: string): number | undefined {
  const v = s.agents.map((a) => a.summary?.[key]).filter((x): x is number => typeof x === 'number')
  return v.length ? Math.max(...v) : undefined
}
const loudness = (s: Stream) => anyFigure(s, 'loudness_lufs')
const subtitleTracks = (s: Stream) => anyFigure(s, 'subtitles')
// How far from the EBU R 128 norm of -23 LUFS a stream may be before it
// stands out in the list: a single measure is of two seconds of sound.
const loudOff = (v: number) => Math.abs(v + 23) > 6
// With one vantage point the column of agents says the same in every row.
const severalAgents = computed(() => (live.overview?.agents.length ?? 0) > 1)
const thumb = (s: Stream) => (s.picture_ref ? `${basePath}/api/v1/streams/${s.id}/picture?agent=${s.picture_ref.agent_id}&t=${new Date(s.picture_ref.time).getTime()}` : '')

// The worst first when sorted from the largest.
const severity: Record<Status, number> = { fail: 4, warn: 3, nodata: 2, ok: 1, disabled: 0 }
const sort = useSort(() => found.value, {
  name: { value: (s) => s.name, filter: 'text', label: (s) => `${s.name} ${s.url}` },
  status: { value: (s) => severity[s.status], filter: 'choice', label: (s) => statusLabel()[s.status] },
  why: { value: (s) => why(s).map((i) => problemName(i.kind)).join(', '), filter: 'text' },
  bitrate: { value: (s) => figure(s, 'bitrate_bps') ?? 0, filter: 'range', unit: t('streams.mbit'), scale: 1e6 },
  loudness: { value: (s) => loudness(s), filter: 'range', unit: 'LUFS' },
  subtitles: {
    value: (s) => subtitleTracks(s),
    filter: 'choice',
    label: (s) => (subtitleTracks(s) === undefined ? t('streams.subtitlesUnknown') : subtitleTracks(s) ? t('streams.subtitlesYes') : t('streams.subtitlesNo')),
  },
  level: { value: (s) => levelLabel()[s.level], filter: 'choice' },
  agents: { value: (s) => s.agents.map((a) => a.agent_name).join(', '), filter: 'choice', label: (s) => s.agents.map((a) => a.agent_name).join(', ') || t('common.notAssigned') },
  checked: (s) => timeValue(lastChecked(s)),
}, 'streams')
const streams = computed(() => sort.rows)

async function toggle(s: Stream) {
  try {
    await api.post(`/api/v1/streams/${s.id}/${s.enabled ? 'disable' : 'enable'}`)
    await live.refresh()
    toasts.ok(s.enabled ? t('streams.checksOfStreamStopped', { name: s.name }) : t('streams.checksOfStreamResumed', { name: s.name }))
  } catch (e) {
    toasts.error(e)
  }
}

async function remove() {
  const s = deleting.value
  if (!s) return
  busy.value = true
  try {
    await api.del(`/api/v1/streams/${s.id}`)
    await live.refresh()
    toasts.ok(t('common.streamDeleted', { name: s.name }))
    deleting.value = null
  } catch (e) {
    toasts.error(e)
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <div class="page">
    <div class="page-head">
      <h1>{{ t('common.streams') }}</h1>
      <span class="spacer" />
      <input v-model="filter" class="input compact" type="search" :placeholder="t('streams.findByNameOrURL')" :aria-label="t('common.streamSearch')" style="width: 260px" />
      <button v-if="session.isAdmin" class="btn" type="button" @click="importing = true">{{ t('streams.importButton') }}</button>
      <button v-if="session.isAdmin" class="btn primary" type="button" @click="creating = true">{{ t('common.addStream') }}</button>
    </div>

    <div class="panel">
      <div v-if="!live.overview" class="empty">{{ t('common.loading') }}</div>
      <div v-else-if="!live.overview.streams.length" class="empty">
        <p class="strong">{{ t('common.noStreamsYet') }}</p>
        <p v-if="session.isAdmin">{{ t('streams.addAPlaylistURLChecksStart') }}</p>
      </div>
      <div v-else-if="!found.length" class="empty">{{ t('common.nothingFoundFor', { q: filter }) }}</div>
      <div v-else class="table-wrap">
        <table class="table">
          <thead>
            <tr>
              <th class="hide-narrow" />
              <SortTh :sort="sort" by="name">{{ t('common.stream') }}</SortTh>
              <SortTh :sort="sort" by="status" class="narrow-col" :title="t('common.status')"><span class="sr-only">{{ t('common.status') }}</span></SortTh>
              <SortTh :sort="sort" by="why">{{ t('streams.why') }}</SortTh>
              <th class="right hide-narrow">
                <span class="th-group">
                  <SortTh as="span" :sort="sort" by="bitrate">{{ t('streams.video') }}</SortTh>
                  <SortTh as="span" :sort="sort" by="loudness" :title="t('streams.loudnessHint')">LUFS</SortTh>
                  <SortTh as="span" :sort="sort" by="subtitles">{{ t('streams.subtitlesShort') }}</SortTh>
                </span>
              </th>
              <SortTh v-if="severalAgents" :sort="sort" by="agents" class="hide-narrow">{{ t('common.agents') }}</SortTh>
              <SortTh :sort="sort" by="checked" class="hide-narrow">{{ t('streams.lastCheck') }}</SortTh>
              <th v-if="session.isAdmin" />
            </tr>
          </thead>
          <tbody>
            <tr v-for="s in streams" :key="s.id" class="clickable" @click="router.push({ name: 'stream', params: { id: s.id } })">
              <td class="hide-narrow thumb"><img v-if="thumb(s)" :src="thumb(s)" alt="" loading="lazy" /></td>
              <td style="max-width: 250px" :title="levelLabel()[s.level]">
                <RouterLink :to="{ name: 'stream', params: { id: s.id } }" class="strong" @click.stop>{{ s.name }}</RouterLink>
                <div class="mono muted truncate" :title="s.url">{{ s.url }}</div>
              </td>
              <td class="narrow-col"><StatusBadge :status="s.status" icon /></td>
              <td class="why" :title="why(s).map((i) => problemName(i.kind)).join(', ')">
                <template v-for="(i, n) in why(s).slice(0, 2)" :key="i.id"><template v-if="n">, </template><span :class="i.severity" :title="i.message">{{ problemName(i.kind) }}</span></template>
                <span v-if="why(s).length > 2" class="muted more" :title="why(s).slice(2).map((i) => problemName(i.kind)).join(', ')">{{ t('streams.andMore', { n: why(s).length - 2 }) }}</span>
              </td>
              <td class="mono right hide-narrow nowrap media">
                <div>
                  <template v-if="figure(s, 'bitrate_bps')"><span v-if="figure(s, 'height')" class="muted">{{ figure(s, 'height') }}p<template v-if="rangeWord(s)"> <b class="hdr" :title="t('streams.hdrHint')">{{ rangeWord(s) }}</b></template> · </span>{{ fmtBits(figure(s, 'bitrate_bps')) }}</template>
                  <template v-else>—</template>
                </div>
                <div v-if="loudness(s) !== undefined || subtitleTracks(s)" class="second">
                  <span v-if="loudness(s) !== undefined" :class="{ off: loudOff(loudness(s)!) }" :title="t('streams.loudnessHint')">{{ fmtNumber(loudness(s), 1) }} LUFS</span>
                  <template v-if="loudness(s) !== undefined && subtitleTracks(s)"> · </template>
                  <span v-if="subtitleTracks(s)">{{ subtitleTracks(s) === 1 ? t('streams.subtitlesShort') : t('streams.subtitlesN', { n: subtitleTracks(s) }) }}</span>
                </div>
              </td>
              <td v-if="severalAgents" class="hide-narrow">
                <template v-if="s.agents.length">{{ s.agents.map((a) => a.agent_name).join(', ') }}</template>
                <span v-else class="muted">{{ t('common.notAssigned') }}</span>
              </td>
              <td class="hide-narrow nowrap">{{ lastCheck(s) }}</td>
              <td v-if="session.isAdmin" class="actions" @click.stop>
                <button class="btn small" type="button" @click="toggle(s)">{{ s.enabled ? t('common.stop') : t('common.resume') }}</button>
                <button class="btn small" type="button" @click="editing = s">{{ t('common.configure') }}</button>
                <button class="btn small danger" type="button" @click="deleting = s">{{ t('common.delete') }}</button>
              </td>
            </tr>
            <FilterEmpty :sort="sort" />
          </tbody>
        </table>
      </div>
    </div>

    <StreamFormDialog v-if="creating" @close="creating = false" @saved="(s) => router.push({ name: 'stream', params: { id: s.id } })" />
    <StreamImportDialog v-if="importing" @close="importing = false" />
    <StreamFormDialog v-if="editing" :stream="editing" @close="editing = null" />
    <ConfirmDialog v-if="deleting" :title="t('common.deleteStreamQuestion')" :action="t('common.deleteStream')" :busy="busy" @close="deleting = null" @confirm="remove">
      <p>{{ t('streams.streamWillBeDeletedTogetherWith', { name: deleting.name }) }}</p>
    </ConfirmDialog>
  </div>
</template>

<style scoped>
.thumb {
  width: 72px;
  padding-right: 0;
}
.thumb img {
  display: block;
  width: 64px;
  height: 36px;
  object-fit: cover;
  border-radius: 3px;
  background: #000;
}
.why {
  max-width: 270px;
  font-size: 13px;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.why .more {
  margin-left: 0.3em;
}
.why .crit {
  color: #ff7a85;
}
.why .warn {
  color: var(--accent-warm);
}
.hdr {
  color: var(--accent-warm);
  font-weight: 600;
}
.off {
  color: var(--accent-warm);
}
.narrow-col {
  width: 1%;
  padding-right: 4px;
}
.th-group {
  display: inline-flex;
  gap: 10px;
  justify-content: flex-end;
}
.media .second {
  margin-top: 2px;
  font-size: 11px;
  color: var(--text-muted);
}
.media .second .off {
  color: var(--accent-warm);
}
</style>
