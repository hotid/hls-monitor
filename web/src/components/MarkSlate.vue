<script setup lang="ts">
// Marks a picture as a slate: what an operator puts on air in place of a
// channel. Every stream showing it afterwards is told at the first look.
// A clip going round can also be marked as the channel's own: something it
// plays of its own accord, which is then left alone.
import { computed, ref } from 'vue'
import { api } from '@/api'
import { t } from '@/i18n'
import { useToasts } from '@/stores/toasts'
import type { Slate } from '@/types'

const props = defineProps<{
  streamId?: number
  agentId?: number
  incidentId?: number
  /** The incident is a clip going round, known by its coded frames. */
  clip?: boolean
  /** There is a picture to mark; without one only a clip can be. */
  picture?: boolean
  streamName?: string
}>()
const toasts = useToasts()
// What is being marked: a slate, or the channel's own clip.
const marking = ref<'slate' | 'own' | null>(null)
const name = ref('')
const known = ref<Slate[]>([])
const saving = ref(false)
const offered = computed(() => known.value.filter((s) => s.own === (marking.value === 'own')))

async function begin(what: 'slate' | 'own') {
  marking.value = what
  name.value = what === 'own' && props.streamName ? t('stream.ownClipDefaultName', { stream: props.streamName }) : ''
  try {
    known.value = await api.get<Slate[]>('/api/v1/slates')
    // One slate for all channels is the usual thing: offer it.
    if (what === 'slate' && !name.value && offered.value.length === 1) name.value = offered.value[0]!.name
  } catch {
    // The list is a convenience; a name can be typed without it.
  }
}
async function save() {
  if (!name.value.trim() || saving.value) return
  saving.value = true
  try {
    const own = marking.value === 'own'
    const sl = await api.post<Slate>('/api/v1/slates', { name: name.value.trim(), stream_id: props.streamId, agent_id: props.agentId, incident_id: props.incidentId, own })
    if (own) toasts.ok(t('stream.ownClipMarked', { name: sl.name }))
    else if (sl.coded_frames) toasts.ok(t('stream.slateMarkedClip', { name: sl.name, n: sl.frames.length, k: sl.coded_frames }))
    else toasts.ok(t('stream.slateMarked', { name: sl.name, n: sl.frames.length }))
    marking.value = null
  } catch (e) {
    toasts.error(e)
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <div class="mark">
    <div v-if="!marking" class="row">
      <button v-if="picture || clip" class="btn small" type="button" :title="t('stream.thisIsASlateHint')" @click="begin('slate')">{{ t('stream.thisIsASlate') }}</button>
      <button v-if="clip" class="btn small" type="button" :title="t('stream.thisIsNotASlateHint')" @click="begin('own')">{{ t('stream.thisIsNotASlate') }}</button>
    </div>
    <form v-else class="row" @submit.prevent="save">
      <input
        v-model="name" class="input compact" list="known-slates" maxlength="100" autofocus
        :placeholder="marking === 'own' ? t('stream.ownClipName') : t('stream.slateName')" :aria-label="marking === 'own' ? t('stream.ownClipName') : t('stream.slateName')"
      />
      <datalist id="known-slates"><option v-for="s in offered" :key="s.id" :value="s.name" /></datalist>
      <button class="btn small primary" type="submit" :disabled="!name.trim() || saving">{{ t('common.save') }}</button>
      <button class="btn small" type="button" @click="marking = null">{{ t('common.cancel') }}</button>
    </form>
    <p v-if="marking === 'own'" class="muted note">{{ t('stream.thisIsNotASlateHint') }}</p>
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
.note {
  margin: 6px 0 0;
  font-size: 12px;
  max-width: 60ch;
}
.btn.small {
  padding: 3px 10px;
  font-size: 12px;
}
</style>
