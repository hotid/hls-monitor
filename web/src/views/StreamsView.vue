<script setup lang="ts">
import { computed, ref } from 'vue'
import { useRouter } from 'vue-router'
import { api } from '@/api'
import { levelLabel, statusLabel } from '@/catalog'
import { fmtAgo } from '@/format'
import { t } from '@/i18n'
import { timeValue, useSort } from '@/sort'
import { useLive } from '@/stores/live'
import { useSession } from '@/stores/session'
import { useToasts } from '@/stores/toasts'
import type { Status, Stream } from '@/types'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import FilterEmpty from '@/components/FilterEmpty.vue'
import SortTh from '@/components/SortTh.vue'
import StatusBadge from '@/components/StatusBadge.vue'
import StreamFormDialog from '@/components/StreamFormDialog.vue'

const live = useLive()
const session = useSession()
const toasts = useToasts()
const router = useRouter()

const filter = ref('')
const found = computed(() => {
  const q = filter.value.trim().toLowerCase()
  const all = live.overview?.streams ?? []
  return q ? all.filter((s) => s.name.toLowerCase().includes(q) || s.url.toLowerCase().includes(q)) : all
})

const editing = ref<Stream | null>(null)
const creating = ref(false)
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

// The worst first when sorted from the largest.
const severity: Record<Status, number> = { fail: 4, warn: 3, nodata: 2, ok: 1, disabled: 0 }
const sort = useSort(() => found.value, {
  name: { value: (s) => s.name, filter: 'text', label: (s) => `${s.name} ${s.url}` },
  status: { value: (s) => severity[s.status], filter: 'choice', label: (s) => statusLabel()[s.status] },
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
              <SortTh :sort="sort" by="name">{{ t('common.stream') }}</SortTh>
              <SortTh :sort="sort" by="status">{{ t('common.status') }}</SortTh>
              <SortTh :sort="sort" by="level" class="hide-narrow">{{ t('common.checkLevel') }}</SortTh>
              <SortTh :sort="sort" by="agents" class="hide-narrow">{{ t('common.agents') }}</SortTh>
              <SortTh :sort="sort" by="checked" class="hide-narrow">{{ t('streams.lastCheck') }}</SortTh>
              <th v-if="session.isAdmin" />
            </tr>
          </thead>
          <tbody>
            <tr v-for="s in streams" :key="s.id" class="clickable" @click="router.push({ name: 'stream', params: { id: s.id } })">
              <td style="max-width: 420px">
                <RouterLink :to="{ name: 'stream', params: { id: s.id } }" class="strong" @click.stop>{{ s.name }}</RouterLink>
                <div class="mono muted truncate" :title="s.url">{{ s.url }}</div>
              </td>
              <td><StatusBadge :status="s.status" /></td>
              <td class="hide-narrow">{{ levelLabel()[s.level] }}</td>
              <td class="hide-narrow">
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
    <StreamFormDialog v-if="editing" :stream="editing" @close="editing = null" />
    <ConfirmDialog v-if="deleting" :title="t('common.deleteStreamQuestion')" :action="t('common.deleteStream')" :busy="busy" @close="deleting = null" @confirm="remove">
      <p>{{ t('streams.streamWillBeDeletedTogetherWith', { name: deleting.name }) }}</p>
    </ConfirmDialog>
  </div>
</template>
