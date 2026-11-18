<script setup lang="ts">
// The pictures marked as slates, and the way to forget one.
import { ref } from 'vue'
import { api, basePath } from '@/api'
import { fmtWhen } from '@/format'
import { t } from '@/i18n'
import { usePolling } from '@/poll'
import { useToasts } from '@/stores/toasts'
import type { Slate } from '@/types'

const toasts = useToasts()
const slates = ref<Slate[]>([])
const { refresh } = usePolling(async (signal) => {
  slates.value = await api.get<Slate[]>('/api/v1/slates', signal)
}, 60_000)

async function forget(s: Slate) {
  if (!window.confirm(t('access.forgetSlate', { name: s.name }))) return
  try {
    await api.del(`/api/v1/slates/${s.id}`)
    await refresh()
  } catch (e) {
    toasts.error(e)
  }
}
</script>

<template>
  <section>
    <div class="section-title">
      <h2>{{ t('access.slates') }}</h2>
    </div>
    <p class="muted lead">{{ t('access.slatesLead') }}</p>
    <div class="panel">
      <div v-if="!slates.length" class="empty">{{ t('access.noSlates') }}</div>
      <table v-else class="table">
        <tbody>
          <tr v-for="s in slates" :key="s.id">
            <td class="strong name">{{ s.name }}<div class="muted when">{{ fmtWhen(s.created_at) }}</div></td>
            <td>
              <div class="frames">
                <img v-for="f in s.frames" :key="f.id" :src="`${basePath}/api/v1/slates/frames/${f.id}`" :alt="s.name" loading="lazy" />
              </div>
            </td>
            <td class="right"><button class="btn danger" type="button" @click="forget(s)">{{ t('common.delete') }}</button></td>
          </tr>
        </tbody>
      </table>
    </div>
  </section>
</template>

<style scoped>
.lead {
  max-width: 78ch;
  margin: -2px 0 12px;
}
.name {
  width: 260px;
  vertical-align: top;
}
.when {
  font-weight: 400;
  font-size: 12px;
}
.frames {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
}
.frames img {
  width: 128px;
  border-radius: 4px;
  background: #000;
}
</style>
