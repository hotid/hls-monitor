<script setup lang="ts">
// Adds streams from a list of channels: an IPTV list is pasted or chosen
// as a file, read by the server, shown, and only then added.
import { computed, reactive, ref } from 'vue'
import { api } from '@/api'
import { levelLabel } from '@/catalog'
import { t } from '@/i18n'
import { useLive } from '@/stores/live'
import { useToasts } from '@/stores/toasts'
import type { Level } from '@/types'
import ModalDialog from './ModalDialog.vue'

interface Entry {
  name: string
  url: string
  group?: string
  status: 'new' | 'created' | 'exists' | 'duplicate' | 'invalid' | 'failed'
  reason?: string
}
interface Result {
  entries: Entry[]
  new: number
  created: number
  existing: number
  invalid: number
}

const emit = defineEmits<{ close: [] }>()
const live = useLive()
const toasts = useToasts()
const agents = computed(() => live.overview?.agents ?? [])

const form = reactive({ text: '', level: 'header' as Level, agent_ids: [1] as number[] })
const read = ref<Result | null>(null)
const busy = ref(false)
const error = ref('')

async function choose(e: Event) {
  const file = (e.target as HTMLInputElement).files?.[0]
  if (!file) return
  form.text = await file.text()
  read.value = null
}

async function send(dryRun: boolean) {
  error.value = ''
  busy.value = true
  try {
    const res = await api.post<Result>('/api/v1/streams/import', {
      text: form.text,
      dry_run: dryRun,
      settings: { level: form.level, agent_ids: form.agent_ids },
    })
    if (dryRun) {
      read.value = res
      return
    }
    void live.refresh()
    toasts.ok(t('streams.importAdded', { n: res.created }))
    emit('close')
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    busy.value = false
  }
}

const statusText = (e: Entry) =>
  ({
    new: t('streams.importNew'),
    created: t('streams.importNew'),
    exists: t('streams.importExists'),
    duplicate: t('streams.importDuplicate'),
    invalid: t('streams.importInvalid'),
    failed: t('streams.importInvalid'),
  })[e.status]
</script>

<template>
  <ModalDialog :title="t('streams.importTitle')" wide @close="emit('close')">
    <form @submit.prevent="send(read === null)">
      <div class="modal-body">
        <div v-if="error" class="notice error" role="alert">{{ error }}</div>
        <div class="field">
          <label for="si-text">{{ t('streams.importList') }}</label>
          <textarea
            id="si-text" v-model="form.text" class="input mono" rows="7" spellcheck="false" required
            placeholder="#EXTM3U&#10;#EXTINF:-1 group-title=&quot;News&quot;,Channel&#10;https://example.com/channel/index.m3u8" @input="read = null"
          />
          <span class="help">{{ t('streams.importListHelp') }}</span>
          <input class="file" type="file" accept=".m3u,.m3u8,.txt,.csv,text/plain,audio/x-mpegurl" :aria-label="t('streams.importFile')" @change="choose" />
        </div>
        <div class="field-row">
          <div class="field">
            <label for="si-level">{{ t('streamForm.whatToCheck') }}</label>
            <select id="si-level" v-model="form.level" class="input" @change="read = null">
              <option v-for="(label, value) in levelLabel()" :key="value" :value="value">{{ label }}</option>
            </select>
          </div>
          <div class="field">
            <span class="label">{{ t('streamForm.whereToCheckFrom') }}</span>
            <label v-for="a in agents" :key="a.id" class="check">
              <input v-model="form.agent_ids" type="checkbox" :value="a.id" />
              <span>{{ a.kind === 'local' ? t('common.serverLocal') : a.name }}</span>
            </label>
          </div>
        </div>

        <template v-if="read">
          <p class="summary">
            {{ t('streams.importSummary', { total: read.entries.length, fresh: read.new, existing: read.existing, invalid: read.invalid }) }}
          </p>
          <div class="table-wrap found">
            <table class="table">
              <thead>
                <tr>
                  <th>{{ t('common.name') }}</th>
                  <th>{{ t('streams.importAddress') }}</th>
                  <th>{{ t('streams.importWhat') }}</th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="(e, i) in read.entries" :key="i" :class="{ skipped: e.status !== 'new' }">
                  <td>{{ e.name }}<span v-if="e.group" class="muted"> · {{ e.group }}</span></td>
                  <td class="mono truncate" :title="e.url">{{ e.url }}</td>
                  <td :title="e.reason">{{ statusText(e) }}</td>
                </tr>
              </tbody>
            </table>
          </div>
        </template>
      </div>
      <div class="modal-foot">
        <button class="btn" type="button" @click="emit('close')">{{ t('common.cancel') }}</button>
        <button v-if="read === null" class="btn primary" type="submit" :disabled="busy || !form.text.trim()">{{ t('streams.importRead') }}</button>
        <button v-else class="btn primary" type="submit" :disabled="busy || !read.new">{{ t('streams.importAdd', { n: read.new }) }}</button>
      </div>
    </form>
  </ModalDialog>
</template>

<style scoped>
.file {
  margin-top: 6px;
  font-size: 12px;
  color: var(--text-muted);
}
.summary {
  margin: 4px 0 8px;
}
.found {
  max-height: 260px;
  overflow-y: auto;
}
.found td {
  max-width: 320px;
}
.skipped {
  opacity: 0.55;
}
</style>
