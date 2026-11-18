<script setup lang="ts">
// Marks a picture as a slate: what an operator puts on air in place of a
// channel. Every stream showing it afterwards is told at the first look.
import { ref } from 'vue'
import { api } from '@/api'
import { t } from '@/i18n'
import { useToasts } from '@/stores/toasts'
import type { Slate } from '@/types'

const props = defineProps<{ streamId?: number; agentId?: number; incidentId?: number }>()
const toasts = useToasts()
const open = ref(false)
const name = ref('')
const known = ref<Slate[]>([])
const saving = ref(false)

async function begin() {
  open.value = true
  try {
    known.value = await api.get<Slate[]>('/api/v1/slates')
    // One slate for all channels is the usual thing: offer it.
    if (!name.value && known.value.length === 1) name.value = known.value[0]!.name
  } catch {
    // The list is a convenience; a name can be typed without it.
  }
}
async function save() {
  if (!name.value.trim() || saving.value) return
  saving.value = true
  try {
    const sl = await api.post<Slate>('/api/v1/slates', { name: name.value.trim(), stream_id: props.streamId, agent_id: props.agentId, incident_id: props.incidentId })
    toasts.ok(t('stream.slateMarked', { name: sl.name, n: sl.frames.length }))
    open.value = false
  } catch (e) {
    toasts.error(e)
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <div class="mark">
    <button v-if="!open" class="btn small" type="button" :title="t('stream.thisIsASlateHint')" @click="begin">{{ t('stream.thisIsASlate') }}</button>
    <form v-else class="row" @submit.prevent="save">
      <input v-model="name" class="input compact" list="known-slates" :placeholder="t('stream.slateName')" :aria-label="t('stream.slateName')" maxlength="100" autofocus />
      <datalist id="known-slates"><option v-for="s in known" :key="s.id" :value="s.name" /></datalist>
      <button class="btn small primary" type="submit" :disabled="!name.trim() || saving">{{ t('common.save') }}</button>
      <button class="btn small" type="button" @click="open = false">{{ t('common.cancel') }}</button>
    </form>
  </div>
</template>

<style scoped>
.mark {
  margin-top: 6px;
}
.row {
  display: flex;
  gap: 6px;
  align-items: center;
}
.row .input {
  width: 220px;
}
.btn.small {
  padding: 3px 10px;
  font-size: 12px;
}
</style>
