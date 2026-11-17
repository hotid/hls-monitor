<script setup lang="ts">
import { ref } from 'vue'
import { fmtUnit, problemName } from '@/catalog'
import { fmtDuration, fmtFull, fmtNumber, fmtWhen } from '@/format'
import { t } from '@/i18n'
import { problemAbout } from '@/incident'
import type { ServerIncident } from '@/types'

defineProps<{ incidents: ServerIncident[]; now: number }>()

// How long the problem was there: the whole span without the pauses.
function lasted(i: ServerIncident, now: number): string {
  const end = i.ended_at ? new Date(i.ended_at).getTime() : now
  return fmtDuration(Math.max(0, (end - new Date(i.started_at).getTime()) / 1000 - i.idle_seconds))
}
const title = (i: ServerIncident) => (i.kind === 'network_loss' ? t('incidents.networkLoss') : problemName(i.kind))
const streams = (i: ServerIncident) => i.details?.streams ?? []

// Which incidents are unfolded.
const open = ref(new Set<number>())
function toggle(id: number) {
  const next = new Set(open.value)
  if (!next.delete(id)) next.add(id)
  open.value = next
}
const isLoss = (i: ServerIncident) => i.kind === 'network_loss'
const what = (i: ServerIncident) => (isLoss(i) ? t('incidents.networkLossAbout') : problemAbout()[i.kind]?.what)
const share = (i: ServerIncident) => (i.details?.requests ? (i.details.retransmits ?? 0) / i.details.requests : undefined)
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
        <template v-for="i in incidents" :key="i.id">
        <tr class="clickable" :class="{ unfolded: open.has(i.id) }" tabindex="0" :aria-expanded="open.has(i.id)" @click="toggle(i.id)" @keydown.enter="toggle(i.id)">
          <td>
            <div class="head">
              <span class="chevron" aria-hidden="true" />
              <span class="dot" :class="i.ended_at ? 'disabled' : i.severity" />
              <span :class="i.ended_at ? '' : 'strong'">{{ title(i) }}</span>
              <span v-if="i.episodes > 1" class="muted">{{ i.episodes }} {{ t('incidents.times', i.episodes) }}</span>
            </div>
            <div class="message muted">{{ i.kind === 'network_loss' ? i.message : i.details?.example }}</div>
          </td>
          <td>
            <RouterLink :to="{ name: 'server', params: { host: i.host, ip: i.ip } }" class="mono" @click.stop>{{ i.host }}</RouterLink>
            <div v-if="i.ip && i.ip !== i.host" class="mono muted">{{ i.ip }}</div>
          </td>
          <td class="streams">
            <template v-if="i.kind !== 'network_loss'">
              <span class="mono strong">{{ i.streams }}</span>
              <span class="muted">
                <template v-for="(s, n) in streams(i).slice(0, 6)" :key="s.id"><template v-if="n">, </template><RouterLink :to="{ name: 'stream', params: { id: s.id } }" @click.stop>{{ s.name }}</RouterLink></template>
                <a v-if="streams(i).length > 6" class="more" href="#" :aria-expanded="open.has(i.id)" @click.prevent.stop="toggle(i.id)">{{ t('dashboard.andMore', { n: streams(i).length - 6 }) }}</a>
              </span>
            </template>
            <template v-else>—</template>
          </td>
          <td class="mono nowrap" :title="i.ended_at ? t('incidents.endedAt', { time: fmtWhen(i.ended_at) }) : t('incidents.lastSeen', { time: fmtWhen(i.last_seen) })">{{ fmtWhen(i.started_at) }}</td>
          <td class="mono right nowrap">{{ lasted(i, now) }}</td>
        </tr>
        <tr v-if="open.has(i.id)" class="details-row">
          <td colspan="100">
            <div class="details">
              <div class="col">
                <template v-if="what(i)">
                  <h3>{{ t('incidents.whatItMeans') }}</h3>
                  <p>{{ what(i) }}</p>
                </template>
                <h3>{{ t('incidents.whyTheServers') }}</h3>
                <p>{{ isLoss(i) ? t('incidents.networkLossWhy') : t('incidents.whyTheServersText', { n: i.streams }) }}</p>
                <h3>{{ t('common.checkMessage') }}</h3>
                <p class="mono">{{ isLoss(i) ? i.message : i.details?.example }}</p>
                <p class="muted">
                  {{ i.ended_at ? t('incidents.serverStartedLastEnded', { start: fmtFull(i.started_at), last: fmtFull(i.last_seen), end: fmtFull(i.ended_at) }) : t('incidents.serverStartedLast', { start: fmtFull(i.started_at), last: fmtFull(i.last_seen) }) }}
                  <template v-if="i.episodes > 1"> {{ t('incidents.serverEpisodes', { n: i.episodes, idle: fmtDuration(i.idle_seconds) }) }}</template>
                </p>
              </div>
              <div class="col">
                <template v-if="isLoss(i)">
                  <h3>{{ t('incidents.calculation') }}<span class="muted">{{ t('incidents.overFiveMinutes') }}</span></h3>
                  <dl>
                    <dt>{{ t('incidents.requestsToServer') }}</dt><dd class="mono">{{ fmtNumber(i.details?.requests, 0) }}</dd>
                    <dt>{{ t('incidents.retransmissions') }}</dt><dd class="mono">{{ fmtNumber(i.details?.retransmits, 0) }}</dd>
                    <dt>{{ t('incidents.retransmissionsPerRequest') }}</dt><dd class="mono">{{ fmtUnit(share(i), 'pct') }}</dd>
                  </dl>
                </template>
                <template v-else>
                  <h3>{{ t('incidents.streamsWithIt') }}<span class="muted">{{ t('incidents.atTheLatestLook', { n: streams(i).length, most: i.streams }) }}</span></h3>
                  <p>
                    <template v-for="(s, n) in streams(i)" :key="s.id"><template v-if="n">, </template><RouterLink :to="{ name: 'stream', params: { id: s.id } }">{{ s.name }}</RouterLink></template>
                  </p>
                </template>
                <p style="margin-top: 12px"><RouterLink :to="{ name: 'server', params: { host: i.host, ip: i.ip } }">{{ t('incidents.allStreamsOfTheServer') }}</RouterLink></p>
              </div>
            </div>
          </td>
        </tr>
        </template>
      </tbody>
    </table>
  </div>
</template>

<style scoped>
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
tr.unfolded > td {
  border-bottom-color: transparent;
}
.details-row > td {
  background: var(--surface-2);
}
.details {
  display: grid;
  grid-template-columns: minmax(0, 5fr) minmax(0, 7fr);
  gap: 8px 32px;
  padding: 6px 2px 10px 16px;
  font-size: 13px;
  white-space: normal;
}
h3 {
  margin: 12px 0 4px;
  font-size: 12px;
  font-weight: 600;
  color: var(--text-muted);
}
h3 .muted {
  margin-left: 8px;
  font-weight: 400;
}
.col > h3:first-child {
  margin-top: 0;
}
p {
  margin: 0 0 4px;
  max-width: 70ch;
  overflow-wrap: anywhere;
}
dl {
  display: grid;
  grid-template-columns: max-content minmax(0, 1fr);
  gap: 3px 16px;
  margin: 0;
}
dt {
  color: var(--text-muted);
}
dd {
  margin: 0;
}
.head {
  display: flex;
  align-items: center;
  gap: 8px;
}
.message {
  margin: 2px 0 0 29px;
  font-size: 12px;
  overflow-wrap: anywhere;
}
.streams {
  max-width: 420px;
}
.streams .strong {
  margin-right: 8px;
}
.more {
  margin-left: 0.3em;
}
</style>
