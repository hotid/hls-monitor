<script setup lang="ts">
import { ref } from 'vue'
import { problemName, kindLabel } from '@/catalog'
import { fmtDuration, fmtWhen } from '@/format'
import { t } from '@/i18n'
import { timeValue, useSort } from '@/sort'
import type { Incident } from '@/types'
import IncidentDetails from '@/components/IncidentDetails.vue'
import FilterEmpty from '@/components/FilterEmpty.vue'
import SortTh from '@/components/SortTh.vue'

const props = defineProps<{ incidents: Incident[]; showStream?: boolean; now: number }>()

// Which incidents are unfolded to show how they were concluded.
const open = ref(new Set<number>())
function toggle(id: number) {
  const next = new Set(open.value)
  if (!next.delete(id)) next.add(id)
  open.value = next
}

function variantLabel(i: Incident): string {
  if (i.variant_kind === 'master' || i.variant_kind === 'media') return kindLabel()[i.variant_kind] ?? i.variant_kind
  return i.variant_key
}

// How long the problem was there: the whole span without the pauses
// between the times it came back.
function seconds(i: Incident, now: number): number {
  const end = i.ended_at ? new Date(i.ended_at).getTime() : now
  return Math.max(0, (end - new Date(i.started_at).getTime()) / 1000 - (i.idle_seconds ?? 0))
}

function lasted(i: Incident, now: number): string {
  return fmtDuration(seconds(i, now))
}

const sort = useSort(() => props.incidents, {
  kind: { value: (i) => problemName(i.kind), filter: 'choice' },
  stream: { value: (i) => i.stream_name, filter: 'choice' },
  variant: { value: (i) => variantLabel(i), filter: 'text' },
  agent: { value: (i) => i.agent_name, filter: 'choice' },
  started: (i) => timeValue(i.started_at),
  lasted: { value: (i) => seconds(i, props.now), filter: 'range', unit: t('format.min'), scale: 60 },
  count: { value: (i) => i.count, filter: 'range' },
})
</script>

<template>
  <div class="table-wrap">
    <table class="table">
      <thead>
        <tr>
          <SortTh :sort="sort" by="kind">{{ t('incidents.problem') }}</SortTh>
          <SortTh v-if="showStream" :sort="sort" by="stream">{{ t('common.stream') }}</SortTh>
          <SortTh :sort="sort" by="variant" class="hide-narrow">{{ t('incidents.variant') }}</SortTh>
          <SortTh :sort="sort" by="agent" class="hide-narrow">{{ t('common.agent') }}</SortTh>
          <SortTh :sort="sort" by="started">{{ t('incidents.started') }}</SortTh>
          <SortTh :sort="sort" by="lasted" class="right">{{ t('incidents.duration') }}</SortTh>
          <SortTh :sort="sort" by="count" class="right hide-narrow">{{ t('incidents.repeats') }}</SortTh>
        </tr>
      </thead>
      <tbody>
        <template v-for="i in sort.rows" :key="i.id">
        <tr class="clickable" :class="{ unfolded: open.has(i.id) }" tabindex="0" :aria-expanded="open.has(i.id)" :title="t('incidents.showHowThisWasDetermined')" @click="toggle(i.id)" @keydown.enter="toggle(i.id)">
          <td>
            <div class="incident-title">
              <span class="chevron" aria-hidden="true" />
              <span class="dot" :class="i.ended_at ? 'disabled' : i.severity" :title="i.ended_at ? t('incidents.ended') : i.severity === 'crit' ? t('common.failure') : i.severity === 'warn' ? t('common.warning') : t('incidents.event')" />
              <span :class="i.ended_at ? '' : 'strong'">{{ problemName(i.kind) }}</span>
              <span v-if="i.episodes > 1" class="muted" :title="t('incidents.timesHint')">{{ i.episodes }} {{ t('incidents.times', i.episodes) }}</span>
              <span v-if="!i.ended_at && !i.active && i.severity !== 'info'" class="muted">{{ t('incidents.goneForNow') }}</span>
            </div>
            <div class="incident-message muted">{{ i.message }}</div>
          </td>
          <td v-if="showStream">
            <RouterLink :to="{ name: 'stream', params: { id: i.stream_id } }" @click.stop>{{ i.stream_name }}</RouterLink>
          </td>
          <td class="hide-narrow mono incident-variant truncate" :title="i.variant_key">{{ variantLabel(i) }}</td>
          <td class="hide-narrow">{{ i.agent_name }}</td>
          <td class="mono nowrap" :title="i.ended_at ? t('incidents.endedAt', { time: fmtWhen(i.ended_at) }) : t('incidents.lastSeen', { time: fmtWhen(i.last_seen) })">{{ fmtWhen(i.started_at) }}</td>
          <td class="mono right nowrap">{{ lasted(i, now) }}</td>
          <td class="mono right hide-narrow">{{ i.count }}</td>
        </tr>
        <tr v-if="open.has(i.id)" class="details-row">
          <td colspan="100"><IncidentDetails :incident="i" /></td>
        </tr>
        </template>
        <FilterEmpty :sort="sort" />
      </tbody>
    </table>
  </div>
</template>

<style scoped>
.incident-title {
  display: flex;
  align-items: center;
  gap: 8px;
}

.chevron {
  flex: none;
  width: 0;
  height: 0;
  border-top: 4px solid transparent;
  border-bottom: 4px solid transparent;
  border-left: 5px solid var(--text-muted);
  transition: transform 0.12s;
}

.unfolded .chevron {
  transform: rotate(90deg);
}

tr.clickable:hover .chevron {
  border-left-color: var(--text);
}

tr.unfolded > td {
  border-bottom-color: transparent;
}

.details-row > td {
  background: var(--surface-2);
}

.incident-message {
  margin: 2px 0 0 29px;
  font-size: 12px;
  overflow-wrap: anywhere;
}

.incident-variant {
  max-width: 220px;
}
</style>
