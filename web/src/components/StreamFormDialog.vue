<script setup lang="ts">
import { computed, reactive, ref } from 'vue'
import { api } from '@/api'
import { levelLabel } from '@/catalog'
import { t } from '@/i18n'
import { useLive } from '@/stores/live'
import { useToasts } from '@/stores/toasts'
import type { Level, Stream } from '@/types'
import ModalDialog from './ModalDialog.vue'

const props = defineProps<{ stream?: Stream }>()
const emit = defineEmits<{ close: []; saved: [stream: Stream] }>()

const live = useLive()
const toasts = useToasts()
const agents = computed(() => live.overview?.agents ?? [])

const form = reactive({
  name: props.stream?.name ?? '',
  url: props.stream?.url ?? '',
  level: (props.stream?.level ?? 'container') as Level,
  interval_sec: props.stream?.interval_sec ?? 0,
  timeout_sec: props.stream?.timeout_sec ?? 0,
  max_segments: props.stream?.max_segments ?? 0,
  insecure: props.stream?.insecure ?? false,
  notes: props.stream?.notes ?? '',
  agent_ids: [...(props.stream?.agent_ids ?? [1])],
  headers: Object.entries(props.stream?.headers ?? {})
    .map(([k, v]) => `${k}: ${v}`)
    .join('\n'),
})
const advanced = ref(Boolean(props.stream && (props.stream.interval_sec || props.stream.timeout_sec || props.stream.max_segments || props.stream.insecure || Object.keys(props.stream.headers).length)))
const saving = ref(false)
const error = ref('')

function parseHeaders(text: string): Record<string, string> | string {
  const out: Record<string, string> = {}
  for (const raw of text.split('\n')) {
    const line = raw.trim()
    if (!line) continue
    const i = line.indexOf(':')
    if (i <= 0) return t('streamForm.headerMustHaveTheFormName', { line })
    out[line.slice(0, i).trim()] = line.slice(i + 1).trim()
  }
  return out
}

async function save() {
  error.value = ''
  const headers = parseHeaders(form.headers)
  if (typeof headers === 'string') {
    error.value = headers
    return
  }
  const body = {
    name: form.name,
    url: form.url,
    level: form.level,
    interval_sec: Number(form.interval_sec) || 0,
    timeout_sec: Number(form.timeout_sec) || 0,
    max_segments: Number(form.max_segments) || 0,
    insecure: form.insecure,
    notes: form.notes,
    agent_ids: form.agent_ids,
    headers,
    enabled: props.stream?.enabled ?? true,
  }
  saving.value = true
  try {
    const saved = props.stream
      ? await api.put<Stream>(`/api/v1/streams/${props.stream.id}`, body)
      : await api.post<Stream>('/api/v1/streams', body)
    void live.refresh()
    toasts.ok(props.stream ? t('streamForm.streamSaved') : t('streamForm.streamAdded'))
    emit('saved', saved)
    emit('close')
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <ModalDialog :title="stream ? t('streamForm.streamSettings') : t('common.addStream')" @close="emit('close')">
    <form @submit.prevent="save">
      <div class="modal-body">
        <div v-if="error" class="notice error" role="alert">{{ error }}</div>
        <div class="field">
          <label for="sf-name">{{ t('common.name') }}</label>
          <input id="sf-name" v-model="form.name" class="input" maxlength="100" required />
        </div>
        <div class="field">
          <label for="sf-url">{{ t('streamForm.playlistURL') }}</label>
          <input id="sf-url" v-model="form.url" class="input mono" type="url" placeholder="https://example.com/live/master.m3u8" required />
          <span class="help">{{ t('streamForm.multivariantPlaylistOrMediaPlaylistVariants') }}</span>
        </div>
        <div class="field">
          <label for="sf-level">{{ t('streamForm.whatToCheck') }}</label>
          <select id="sf-level" v-model="form.level" class="input">
            <option v-for="(label, value) in levelLabel()" :key="value" :value="value">{{ label }}</option>
          </select>
          <span class="help">
            <template v-if="form.level === 'playlist'">{{ t('streamForm.segmentsAreNotRequestedOnlyThe') }}</template>
            <template v-else-if="form.level === 'header'">
              {{ t('streamForm.theFirst130KBOfEvery') }}
            </template>
            <template v-else-if="form.level === 'segments'">{{ t('streamForm.levelSegmentsHelp') }}</template>
            <template v-else>{{ t('streamForm.levelContainerHelp') }}</template>
          </span>
        </div>
        <div class="field">
          <span class="label">{{ t('streamForm.whereToCheckFrom') }}</span>
          <label v-for="a in agents" :key="a.id" class="check">
            <input v-model="form.agent_ids" type="checkbox" :value="a.id" />
            <span>
              {{ a.kind === 'local' ? t('common.serverLocal') : a.name }}
              <span v-if="a.kind === 'remote' && !a.online" class="muted">{{ t('streamForm.offline') }}</span>
              <span v-if="!a.enabled" class="muted">{{ t('streamForm.disabled') }}</span>
            </span>
          </label>
          <span v-if="!form.agent_ids.length" class="help" style="color: var(--accent-warm)">{{ t('streamForm.nothingIsSelectedTheStreamWill') }}</span>
        </div>

        <button v-if="!advanced" class="btn ghost small" type="button" @click="advanced = true">{{ t('streamForm.advancedSettings') }}</button>
        <template v-else>
          <div class="field-row">
            <div class="field">
              <label for="sf-interval">{{ t('streamForm.intervalS') }}</label>
              <input id="sf-interval" v-model.number="form.interval_sec" class="input mono" type="number" min="0" max="3600" step="1" />
              <span class="help">{{ t('streamForm.0HalfTheSegmentDuration') }}</span>
            </div>
            <div class="field">
              <label for="sf-timeout">{{ t('streamForm.requestTimeoutS') }}</label>
              <input id="sf-timeout" v-model.number="form.timeout_sec" class="input mono" type="number" min="0" max="120" step="1" />
              <span class="help">{{ t('streamForm.010Seconds') }}</span>
            </div>
            <div class="field">
              <label for="sf-max">{{ t('streamForm.segmentsPerCycle') }}</label>
              <input id="sf-max" v-model.number="form.max_segments" class="input mono" type="number" min="0" max="20" step="1" />
              <span class="help">{{ t('streamForm.0AtMostThree') }}</span>
            </div>
          </div>
          <div class="field">
            <label for="sf-headers">{{ t('streamForm.requestHeaders') }}</label>
            <textarea id="sf-headers" v-model="form.headers" class="input mono" rows="2" placeholder="Authorization: Bearer …" spellcheck="false" />
            <span class="help">{{ t('streamForm.onePerLineSentWithEvery') }}</span>
          </div>
          <label class="check" style="margin-bottom: 14px">
            <input v-model="form.insecure" type="checkbox" />
            <span>{{ t('streamForm.doNotVerifyTheTLSCertificate') }}<br /><span class="help muted">{{ t('streamForm.forSelfSignedCertificates') }}</span></span>
          </label>
          <div class="field">
            <label for="sf-notes">{{ t('common.notes') }}</label>
            <textarea id="sf-notes" v-model="form.notes" class="input" rows="2" maxlength="2000" />
          </div>
        </template>
      </div>
      <div class="modal-foot">
        <button class="btn" type="button" @click="emit('close')">{{ t('common.cancel') }}</button>
        <button class="btn primary" type="submit" :disabled="saving">{{ stream ? t('common.save') : t('common.addStream') }}</button>
      </div>
    </form>
  </ModalDialog>
</template>
