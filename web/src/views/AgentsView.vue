<script setup lang="ts">
import { computed, ref } from 'vue'
import { api } from '@/api'
import { fmtAgo, fmtBits, fmtBytes, fmtDuration, fmtFull, fmtMs, fmtNumber, fmtSeconds } from '@/format'
import { usePolling } from '@/poll'
import { t } from '@/i18n'
import { timeValue, useSort } from '@/sort'
import { useLive } from '@/stores/live'
import { useSession } from '@/stores/session'
import { useToasts } from '@/stores/toasts'
import type { Agent, AgentStats, AgentsStats, Enrollment } from '@/types'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import FilterEmpty from '@/components/FilterEmpty.vue'
import ModalDialog from '@/components/ModalDialog.vue'
import SortTh from '@/components/SortTh.vue'

const live = useLive()
const session = useSession()
const toasts = useToasts()
const agents = computed(() => live.overview?.agents ?? [])

// What every vantage point did over the last minutes.
const stats = ref<AgentsStats | null>(null)
usePolling(async (signal) => {
  stats.value = await api.get<AgentsStats>('/api/v1/agents/stats', signal)
}, 15_000)
const none: AgentStats = { ok: 0, warn: 0, fail: 0, incidents_crit: 0, incidents_warn: 0, traffic_bps: 0, segments_per_min: 0, segment_errors: 0, check_p95_seconds: 0, lag_max_seconds: 0 }
const statsOf = (a: Agent): AgentStats => ({ ...none, ...stats.value?.agents[String(a.id)] })
const totals = computed(() => {
  const sum = { ...none }
  for (const a of agents.value) {
    const s = statsOf(a)
    sum.traffic_bps += s.traffic_bps
    sum.segments_per_min += s.segments_per_min
    sum.segment_errors += s.segment_errors
    sum.incidents_crit += s.incidents_crit
    sum.incidents_warn += s.incidents_warn
  }
  return sum
})

const creating = ref(false)
const newName = ref('')
const busy = ref(false)
const enrollment = ref<Enrollment | null>(null)
const deleting = ref<Agent | null>(null)
const resetting = ref<Agent | null>(null)

function state(a: Agent): { cls: string; text: string } {
  if (!a.enabled) return { cls: 'disabled', text: t('common.disabled') }
  if (a.kind === 'local') return { cls: 'ok', text: t('agents.running') }
  if (!a.enrolled) return { cls: 'warn', text: t('agents.waitingToConnect') }
  return a.online ? { cls: 'ok', text: t('agents.online') } : { cls: 'fail', text: t('agents.offline') }
}

/** What needs attention in what the agent tells about itself. */
function notes(a: Agent): { cls: string; text: string; hint?: string }[] {
  const st = a.status
  if (!st || !a.enabled) return []
  const out: { cls: string; text: string; hint?: string }[] = []
  if (st.overloaded) {
    out.push({
      cls: 'fail',
      text: t('agents.overloaded'),
      hint: t('agents.overloadedHint', { cores: fmtNumber(st.cpu_cores, 1), limit: fmtNumber(st.cpu_limit, 1) }),
    })
  }
  if (st.behind > 0) out.push({ cls: 'warn', text: t('agents.behind', { n: st.behind }, st.behind), hint: t('agents.behindHint') })
  if (st.queue_failing) out.push({ cls: 'fail', text: t('agents.queueFailing') })
  // Results leave the queue every couple of seconds; older ones mean they do not.
  if (st.queue_results > 0 && st.queue_oldest_sec >= 30) {
    out.push({
      cls: 'warn',
      text: t('agents.queueWaiting', { n: st.queue_results, age: fmtDuration(st.queue_oldest_sec) }, st.queue_results),
      hint: t('agents.queueWaitingHint', { size: fmtBytes(st.queue_bytes) }),
    })
  }
  // The way to the server: everything the agent reports travels it.
  const link = st.link
  if (link) {
    if (link.measured && link.rtt_ms >= 300) out.push({ cls: 'warn', text: t('agents.linkSlow', { ms: Math.round(link.rtt_ms) }), hint: t('agents.linkHint', { n: link.retransmits }) })
    if (link.requests >= 20 && link.failures / link.requests >= 0.05) {
      out.push({ cls: 'fail', text: t('agents.linkFailing', { failed: link.failures, total: link.requests }), hint: t('agents.linkHint', { n: link.retransmits }) })
    }
  }
  if (st.queue_dropped > 0) out.push({ cls: 'fail', text: t('agents.queueDropped', { n: st.queue_dropped }, st.queue_dropped) })
  return out
}

const sort = useSort(() => agents.value, {
  name: { value: (a) => (a.kind === 'local' ? t('common.serverLocal') : a.name), filter: 'text' },
  state: { value: (a) => state(a).text, filter: 'choice' },
  streams: { value: (a) => a.streams, filter: 'range' },
  seen: (a) => (a.kind === 'local' ? undefined : timeValue(a.last_seen)),
  addr: { value: (a) => a.remote_addr, filter: 'text' },
  version: { value: (a) => a.version, filter: 'choice' },
  clock: (a) => (a.clock_offset_ms === null ? undefined : Math.abs(a.clock_offset_ms)),
}, 'agents')

async function create() {
  busy.value = true
  try {
    enrollment.value = await api.post<Enrollment>('/api/v1/agents', { name: newName.value.trim() })
    creating.value = false
    newName.value = ''
    await live.refresh()
  } catch (e) {
    toasts.error(e)
  } finally {
    busy.value = false
  }
}

async function toggle(a: Agent) {
  try {
    await api.put(`/api/v1/agents/${a.id}`, { enabled: !a.enabled })
    await live.refresh()
    toasts.ok(a.enabled ? t('agents.agentDisabled', { name: a.name }) : t('agents.agentEnabled', { name: a.name }))
  } catch (e) {
    toasts.error(e)
  }
}

async function resetToken() {
  const a = resetting.value
  if (!a) return
  busy.value = true
  try {
    enrollment.value = await api.post<Enrollment>(`/api/v1/agents/${a.id}/token`)
    resetting.value = null
    await live.refresh()
  } catch (e) {
    toasts.error(e)
  } finally {
    busy.value = false
  }
}

async function remove() {
  const a = deleting.value
  if (!a) return
  busy.value = true
  try {
    await api.del(`/api/v1/agents/${a.id}`)
    deleting.value = null
    await live.refresh()
    toasts.ok(t('agents.agentDeleted', { name: a.name }))
  } catch (e) {
    toasts.error(e)
  } finally {
    busy.value = false
  }
}

async function copy(text: string) {
  try {
    await navigator.clipboard.writeText(text)
    toasts.ok(t('common.copied'))
  } catch {
    toasts.error(t('common.couldNotCopySelectTheText'))
  }
}

/** The address works on this machine only: an agent elsewhere, or in a container, cannot use it. */
function localOnly(url: string): boolean {
  try {
    const host = new URL(url).hostname
    return host === 'localhost' || host === '[::1]' || host.startsWith('127.')
  } catch {
    return false
  }
}
</script>

<template>
  <div class="page">
    <div class="page-head">
      <h1>{{ t('common.agents') }}</h1>
      <span class="spacer" />
      <button v-if="session.isAdmin" class="btn primary" type="button" @click="creating = true">{{ t('agents.addAgent') }}</button>
    </div>
    <p class="muted lead">
      {{ t('agents.anAgentChecksStreamsFromIts') }}
    </p>

    <div class="panel">
      <div v-if="!live.overview" class="empty">{{ t('common.loading') }}</div>
      <div v-else class="table-wrap">
        <table class="table">
          <thead>
            <tr>
              <SortTh :sort="sort" by="name">{{ t('common.agent') }}</SortTh>
              <SortTh :sort="sort" by="state">{{ t('common.status') }}</SortTh>
              <SortTh :sort="sort" by="streams" class="right">{{ t('common.streamsCount') }}</SortTh>
              <SortTh :sort="sort" by="seen" class="hide-narrow">{{ t('agents.lastSeen') }}</SortTh>
              <SortTh :sort="sort" by="addr" class="hide-narrow">{{ t('agents.address') }}</SortTh>
              <SortTh :sort="sort" by="version" class="hide-narrow">{{ t('agents.version') }}</SortTh>
              <SortTh :sort="sort" by="clock" class="right hide-narrow" :title="t('agents.howFarTheAgentSClock')">{{ t('agents.clockOffset') }}</SortTh>
              <th v-if="session.isAdmin" />
            </tr>
          </thead>
          <tbody>
            <tr v-for="a in sort.rows" :key="a.id">
              <td>
                <span class="strong">{{ a.kind === 'local' ? t('common.serverLocal') : a.name }}</span>
                <div v-if="a.kind === 'local'" class="muted">{{ t('agents.checksFromTheServerItself') }}</div>
              </td>
              <td>
                <span class="badge" :class="state(a).cls"><span class="dot" :class="state(a).cls" />{{ state(a).text }}</span>
                <div v-if="a.kind === 'remote' && !a.enrolled && a.enroll_expires" class="muted" style="margin-top: 3px">
                  {{ t('agents.tokenIsValidUntil', { time: fmtFull(a.enroll_expires) }) }}
                </div>
                <div v-for="n in notes(a)" :key="n.text" class="agent-note" :class="n.cls" :title="n.hint">{{ n.text }}</div>
              </td>
              <td class="mono right">{{ a.streams }}</td>
              <td class="hide-narrow nowrap" :title="fmtFull(a.last_seen)">{{ a.kind === 'local' ? '—' : fmtAgo(a.last_seen, live.updatedAt) }}</td>
              <td class="hide-narrow mono">{{ a.remote_addr || '—' }}</td>
              <td class="hide-narrow mono">{{ a.version || '—' }}</td>
              <td class="hide-narrow mono right" :style="a.clock_offset_ms !== null && Math.abs(a.clock_offset_ms) > 2000 ? 'color: var(--accent-warm)' : ''">
                {{ a.clock_offset_ms === null ? '—' : (a.clock_offset_ms > 0 ? '+' : a.clock_offset_ms < 0 ? '−' : '') + fmtMs(Math.abs(a.clock_offset_ms)) }}
              </td>
              <td v-if="session.isAdmin" class="actions">
                <button class="btn small" type="button" @click="toggle(a)">{{ a.enabled ? t('agents.disable') : t('agents.enable') }}</button>
                <template v-if="a.kind === 'remote'">
                  <button class="btn small" type="button" @click="resetting = a">{{ t('agents.newToken') }}</button>
                  <button class="btn small danger" type="button" @click="deleting = a">{{ t('common.delete') }}</button>
                </template>
              </td>
            </tr>
            <FilterEmpty :sort="sort" />
          </tbody>
        </table>
      </div>
    </div>

    <div class="section-title"><h2>{{ t('agents.statsTitle') }}</h2></div>
    <p class="muted lead">{{ t('agents.statsLead') }}</p>
    <div class="panel stats">
      <div v-if="!stats" class="empty">{{ t('common.loading') }}</div>
      <div v-else class="table-wrap">
        <div v-if="!stats.metrics" class="notice" style="margin: 10px 14px">{{ t('agents.statsNoMetrics') }}</div>
        <table class="table">
          <thead>
            <tr>
              <th>{{ t('common.agent') }}</th>
              <th class="right" :title="t('agents.statsStreamsHint')">{{ t('agents.statsStreams') }}</th>
              <th class="right" :title="t('agents.statsIncidentsHint')">{{ t('agents.statsIncidents') }}</th>
              <th class="right" :title="t('agents.statsTrafficHint')">{{ t('agents.statsTraffic') }}</th>
              <th class="right">{{ t('agents.statsSegments') }}</th>
              <th class="right" :title="t('agents.statsErrorsHint')">{{ t('agents.statsErrors') }}</th>
              <th class="right hide-narrow" :title="t('agents.statsCheckHint')">{{ t('agents.statsCheck') }}</th>
              <th class="right hide-narrow" :title="t('agents.statsLagHint')">{{ t('agents.statsLag') }}</th>
              <th class="right hide-narrow">{{ t('agents.statsCPU') }}</th>
              <th class="right hide-narrow">{{ t('agents.statsMemory') }}</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="a in agents" :key="a.id">
              <td class="strong">{{ a.kind === 'local' ? t('common.serverLocal') : a.name }}</td>
              <td class="mono right nowrap">
                <span class="ok-text">{{ statsOf(a).ok }}</span> / <span :class="{ 'warn-text': statsOf(a).warn }">{{ statsOf(a).warn }}</span> /
                <span :class="{ 'fail-text': statsOf(a).fail }">{{ statsOf(a).fail }}</span>
              </td>
              <td class="mono right nowrap">
                <span :class="{ 'fail-text': statsOf(a).incidents_crit }">{{ statsOf(a).incidents_crit }}</span> / <span :class="{ 'warn-text': statsOf(a).incidents_warn }">{{ statsOf(a).incidents_warn }}</span>
              </td>
              <td class="mono right">{{ fmtBits(statsOf(a).traffic_bps) }}</td>
              <td class="mono right">{{ fmtNumber(statsOf(a).segments_per_min, 0) }}</td>
              <td class="mono right" :class="{ 'fail-text': statsOf(a).segment_errors }">{{ fmtNumber(statsOf(a).segment_errors, 0) }}</td>
              <td class="mono right hide-narrow">{{ fmtSeconds(statsOf(a).check_p95_seconds) }}</td>
              <td class="mono right hide-narrow">{{ fmtSeconds(statsOf(a).lag_max_seconds) }}</td>
              <td class="mono right hide-narrow">{{ a.status ? fmtNumber(a.status.cpu_cores, 2) + (a.status.cpu_limit ? ' / ' + fmtNumber(a.status.cpu_limit, 0) : '') : '—' }}</td>
              <td class="mono right hide-narrow">{{ a.status?.memory_bytes ? fmtBytes(a.status.memory_bytes) : '—' }}</td>
            </tr>
            <tr v-if="agents.length > 1" class="total">
              <td class="strong">{{ t('agents.statsTotal') }}</td>
              <td />
              <td class="mono right nowrap">{{ totals.incidents_crit }} / {{ totals.incidents_warn }}</td>
              <td class="mono right">{{ fmtBits(totals.traffic_bps) }}</td>
              <td class="mono right">{{ fmtNumber(totals.segments_per_min, 0) }}</td>
              <td class="mono right">{{ fmtNumber(totals.segment_errors, 0) }}</td>
              <td class="hide-narrow" colspan="4" />
            </tr>
          </tbody>
        </table>
      </div>
    </div>

    <ModalDialog v-if="creating" :title="t('agents.addAgent')" @close="creating = false">
      <form @submit.prevent="create">
        <div class="modal-body">
          <div class="field">
            <label for="agent-name">{{ t('common.name') }}</label>
            <input id="agent-name" v-model="newName" class="input" placeholder="msk-dc1" pattern="[A-Za-z0-9][A-Za-z0-9._\-]{0,62}" required />
            <span class="help">{{ t('agents.latinLettersDigitsDotHyphenAnd') }}</span>
          </div>
        </div>
        <div class="modal-foot">
          <button class="btn" type="button" @click="creating = false">{{ t('common.cancel') }}</button>
          <button class="btn primary" type="submit" :disabled="busy">{{ t('agents.addAgent') }}</button>
        </div>
      </form>
    </ModalDialog>

    <ModalDialog v-if="enrollment" :title="t('agents.connectingAgent', { name: enrollment.agent.name })" wide @close="enrollment = null">
      <div class="modal-body">
        <div class="notice">{{ t('agents.theTokenIsShownOnceIt', { time: fmtFull(enrollment.expires_at) }) }}</div>
        <div class="field">
          <span class="label">{{ t('agents.runInDocker') }}</span>
          <pre class="code wrap">{{ enrollment.command }}</pre>
          <span class="help">{{ t('agents.afterTheFirstStartTheAgent') }}</span>
          <div v-if="localOnly(enrollment.server_url)" class="notice" style="margin-top: 8px">{{ t('agents.serverAddressIsLocal', { url: enrollment.server_url }) }}</div>
        </div>
        <div class="field">
          <span class="label">{{ t('agents.enrollmentToken') }}</span>
          <pre class="code wrap">{{ enrollment.token }}</pre>
        </div>
      </div>
      <div class="modal-foot">
        <button class="btn" type="button" @click="copy(enrollment.token)">{{ t('common.copyToken') }}</button>
        <button class="btn" type="button" @click="copy(enrollment.command)">{{ t('agents.copyCommand') }}</button>
        <span class="spacer" />
        <button class="btn primary" type="button" @click="enrollment = null">{{ t('common.done') }}</button>
      </div>
    </ModalDialog>

    <ConfirmDialog v-if="resetting" :title="t('agents.issueANewToken')" :action="t('agents.issueNewToken')" :busy="busy" @close="resetting = null" @confirm="resetToken">
      <p>{{ t('agents.theCurrentKeyOfAgentWill', { name: resetting.name }) }}</p>
    </ConfirmDialog>
    <ConfirmDialog v-if="deleting" :title="t('agents.deleteAgentQuestion')" :action="t('agents.deleteAgent')" :busy="busy" @close="deleting = null" @confirm="remove">
      <p>
        {{ t('agents.agentWillBeDeletedTogetherWith', { name: deleting.name }) }}
        <template v-if="deleting.streams">{{ t('agents.itsChecksOfWillStop', { n: deleting.streams, streams: t('agents.streams', deleting.streams) }) }}</template>
      </p>
    </ConfirmDialog>
  </div>
</template>

<style scoped>
.ok-text {
  color: var(--ok, #4ade80);
}
.warn-text {
  color: var(--accent-warm, #f5b942);
}
.fail-text {
  color: #ff7a85;
}
tr.total td {
  border-top: 1px solid var(--border, #24324a);
}
.lead {
  max-width: 78ch;
  margin: -6px 0 14px;
}
.agent-note {
  margin-top: 3px;
  font-size: 12px;
  cursor: help;
}
.agent-note.fail {
  color: #ff8a93;
}
.agent-note.warn {
  color: var(--accent-warm);
}
</style>
