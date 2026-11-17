<script setup lang="ts">
import { problemName } from '@/catalog'
import { fmtDuration, fmtWhen } from '@/format'
import { t } from '@/i18n'
import type { ServerIncident } from '@/types'

defineProps<{ incidents: ServerIncident[]; now: number }>()

// How long the problem was there: the whole span without the pauses.
function lasted(i: ServerIncident, now: number): string {
  const end = i.ended_at ? new Date(i.ended_at).getTime() : now
  return fmtDuration(Math.max(0, (end - new Date(i.started_at).getTime()) / 1000 - i.idle_seconds))
}
const title = (i: ServerIncident) => (i.kind === 'network_loss' ? t('incidents.networkLoss') : problemName(i.kind))
const streams = (i: ServerIncident) => i.details?.streams ?? []
</script>

<template>
  <div class="table-wrap">
    <table class="table">
      <thead>
        <tr>
          <th>{{ t('incidents.problem') }}</th>
          <th>{{ t('common.server') }}</th>
          <th>{{ t('common.streams') }}</th>
          <th>{{ t('incidents.started') }}</th>
          <th class="right">{{ t('incidents.duration') }}</th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="i in incidents" :key="i.id">
          <td>
            <div class="head">
              <span class="dot" :class="i.ended_at ? 'disabled' : i.severity" />
              <span :class="i.ended_at ? '' : 'strong'">{{ title(i) }}</span>
              <span v-if="i.episodes > 1" class="muted">{{ i.episodes }} {{ t('incidents.times', i.episodes) }}</span>
            </div>
            <div class="message muted">{{ i.kind === 'network_loss' ? i.message : i.details?.example }}</div>
          </td>
          <td>
            <RouterLink :to="{ name: 'server', params: { host: i.host, ip: i.ip } }" class="mono">{{ i.host }}</RouterLink>
            <div v-if="i.ip && i.ip !== i.host" class="mono muted">{{ i.ip }}</div>
          </td>
          <td class="streams">
            <template v-if="i.kind !== 'network_loss'">
              <span class="mono strong">{{ i.streams }}</span>
              <span class="muted">
                <template v-for="(s, n) in streams(i).slice(0, 6)" :key="s.id"><template v-if="n">, </template><RouterLink :to="{ name: 'stream', params: { id: s.id } }">{{ s.name }}</RouterLink></template>
                <template v-if="streams(i).length > 6"> {{ t('dashboard.andMore', { n: streams(i).length - 6 }) }}</template>
              </span>
            </template>
            <template v-else>—</template>
          </td>
          <td class="mono nowrap" :title="i.ended_at ? t('incidents.endedAt', { time: fmtWhen(i.ended_at) }) : t('incidents.lastSeen', { time: fmtWhen(i.last_seen) })">{{ fmtWhen(i.started_at) }}</td>
          <td class="mono right nowrap">{{ lasted(i, now) }}</td>
        </tr>
      </tbody>
    </table>
  </div>
</template>

<style scoped>
.head {
  display: flex;
  align-items: center;
  gap: 8px;
}
.message {
  margin: 2px 0 0 16px;
  font-size: 12px;
  overflow-wrap: anywhere;
}
.streams {
  max-width: 420px;
}
.streams .strong {
  margin-right: 8px;
}
</style>
