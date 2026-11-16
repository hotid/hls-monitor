<script setup lang="ts">
import { computed, ref } from 'vue'
import { api } from '@/api'
import { fmtAgo, fmtFull, fmtMs } from '@/format'
import { t } from '@/i18n'
import { timeValue, useSort } from '@/sort'
import { useLive } from '@/stores/live'
import { useSession } from '@/stores/session'
import { useToasts } from '@/stores/toasts'
import type { Agent, Enrollment } from '@/types'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import FilterEmpty from '@/components/FilterEmpty.vue'
import ModalDialog from '@/components/ModalDialog.vue'
import SortTh from '@/components/SortTh.vue'

const live = useLive()
const session = useSession()
const toasts = useToasts()
const agents = computed(() => live.overview?.agents ?? [])

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
.lead {
  max-width: 78ch;
  margin: -6px 0 14px;
}
</style>
