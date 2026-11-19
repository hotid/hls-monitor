<script setup lang="ts">
// The segments saved around the findings of a stream, and the way to ask
// for a capture now. Nothing at all where captures are not made.
import { computed, ref } from 'vue'
import { api, basePath } from '@/api'
import { problemName } from '@/catalog'
import { fmtBytes, fmtFull } from '@/format'
import { t } from '@/i18n'
import { usePolling } from '@/poll'
import { useSession } from '@/stores/session'
import { useToasts } from '@/stores/toasts'
import type { Capture, Captures } from '@/types'
import ConfirmDialog from '@/components/ConfirmDialog.vue'

const props = defineProps<{ id: number }>()
const session = useSession()
const toasts = useToasts()
const data = ref<Captures | null>(null)
const { refresh } = usePolling(async (signal) => {
  data.value = await api.get<Captures>(`/api/v1/captures?stream=${props.id}`, signal)
}, 15_000)
const captures = computed(() => data.value?.captures ?? [])

const asking = ref(false)
async function captureNow() {
  asking.value = true
  try {
    await api.post(`/api/v1/streams/${props.id}/capture`, {})
    toasts.ok(t('stream.captureAsked'))
    setTimeout(refresh, 3000)
  } catch (e) {
    toasts.error(e)
  } finally {
    asking.value = false
  }
}

const deleting = ref<Capture | null>(null)
const busy = ref(false)
async function remove() {
  if (!deleting.value) return
  busy.value = true
  try {
    await api.del(`/api/v1/captures/${deleting.value.id}`)
    deleting.value = null
    await refresh()
  } catch (e) {
    toasts.error(e)
  } finally {
    busy.value = false
  }
}

const why = (c: Capture) => (c.manual ? t('stream.captureManual') : c.kinds.map(problemName).join(', '))
const state = (c: Capture) =>
  !c.done ? t('stream.captureGoing') : c.state === 'kept' ? t('stream.captureKept') : c.state === 'found' ? t('stream.captureFound') : t('stream.capturePending')
const whole = (c: Capture) => c.files.filter((f) => f.role === 'segment' && f.whole).length
</script>

<template>
  <section v-if="data?.enabled" class="captures">
    <div class="section-title">
      <h2>{{ t('stream.captures') }}</h2>
      <button v-if="session.isAdmin" class="btn" type="button" :disabled="asking" :title="t('stream.captureNowHint')" @click="captureNow">{{ t('stream.captureNow') }}</button>
    </div>
    <div class="panel">
      <div v-if="!captures.length" class="empty">{{ t('stream.noCaptures') }}</div>
      <table v-else class="table">
        <thead>
          <tr>
            <th>{{ t('stream.captureAt') }}</th>
            <th>{{ t('stream.captureWhy') }}</th>
            <th>{{ t('stream.captureState') }}</th>
            <th class="right">{{ t('stream.captureSegments') }}</th>
            <th class="right">{{ t('stream.captureSize') }}</th>
            <th />
          </tr>
        </thead>
        <tbody>
          <tr v-for="c in captures" :key="c.id">
            <td class="mono">{{ fmtFull(c.at) }}</td>
            <td>
              {{ why(c) }}
              <div v-for="(n, i) in c.notes ?? []" :key="i" class="muted note">{{ n }}</div>
            </td>
            <td :title="c.state === 'pending' ? t('stream.capturePendingHint') : c.state === 'found' ? t('stream.captureFoundHint') : ''">{{ state(c) }}</td>
            <td class="right mono">{{ whole(c) }}</td>
            <td class="right mono">{{ fmtBytes(c.bytes) }}</td>
            <td class="right actions">
              <template v-if="session.isAdmin && c.done">
                <a class="btn" :href="`${basePath}/api/v1/captures/${c.id}/archive`" download>{{ t('stream.captureDownload') }}</a>
                <button class="btn danger" type="button" @click="deleting = c">{{ t('common.delete') }}</button>
              </template>
            </td>
          </tr>
        </tbody>
      </table>
    </div>
    <ConfirmDialog v-if="deleting" :title="t('stream.captureDeleteQuestion')" :action="t('common.delete')" :busy="busy" @close="deleting = null" @confirm="remove">
      <p>{{ t('stream.captureDeleteText', { at: fmtFull(deleting.at) }) }}</p>
    </ConfirmDialog>
  </section>
</template>

<style scoped>
.note {
  font-size: 12px;
}
.actions {
  white-space: nowrap;
}
.actions .btn {
  margin-left: 6px;
  padding: 3px 10px;
  font-size: 12px;
  text-decoration: none;
}
</style>
