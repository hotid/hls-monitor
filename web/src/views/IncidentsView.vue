<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { api, query } from '@/api'
import { t } from '@/i18n'
import { usePolling } from '@/poll'
import { useLive } from '@/stores/live'
import type { Incident, ServerIncident } from '@/types'
import IncidentTable from '@/components/IncidentTable.vue'
import ServerIncidentTable from '@/components/ServerIncidentTable.vue'

const live = useLive()
const scope = ref<'open' | 'all'>('open')
const streamId = ref(0)

// Events are a record of what changed, not something wrong: hidden until asked for.
const levels = [
  { id: 'crit', label: t('incidents.failures') },
  { id: 'warn', label: t('incidents.warnings') },
  { id: 'info', label: t('incidents.events') },
] as const
type LevelID = (typeof levels)[number]['id']
function savedLevels(): LevelID[] {
  try {
    const saved = JSON.parse(localStorage.getItem('incident-levels') ?? 'null') as unknown
    if (Array.isArray(saved)) {
      const known = levels.map((l) => l.id).filter((id) => saved.includes(id))
      if (known.length) return known
    }
  } catch {
    // Nothing remembered.
  }
  return ['crit', 'warn']
}
const shown = ref<LevelID[]>(savedLevels())
function toggleLevel(id: LevelID) {
  const next = shown.value.includes(id) ? shown.value.filter((l) => l !== id) : [...shown.value, id]
  // An empty list would be an empty page: the last level stays on.
  if (!next.length) return
  shown.value = next
  try {
    localStorage.setItem('incident-levels', JSON.stringify(next))
  } catch {
    // Not remembered, still applied.
  }
}
const incidents = ref<Incident[] | null>(null)
const ofServers = ref<ServerIncident[]>([])
const now = ref(Date.now())

const { error, refresh } = usePolling(async (signal) => {
  incidents.value = await api.get<Incident[]>(
    '/api/v1/incidents' + query({ open: scope.value === 'open' ? 'true' : undefined, stream: streamId.value, severity: shown.value.length < levels.length ? shown.value.join(',') : undefined, limit: 500 }),
    signal,
  )
  // Of servers: not about one stream, so not shown when one is chosen.
  ofServers.value = streamId.value ? [] : await api.get<ServerIncident[]>('/api/v1/server-incidents' + query({ open: scope.value === 'open' ? 'true' : undefined, limit: 100 }), signal)
  now.value = Date.now()
}, 10_000)
watch([scope, streamId, shown], () => void refresh())

const streams = computed(() => live.overview?.streams ?? [])
</script>

<template>
  <div class="page">
    <div class="page-head">
      <h1>{{ t('common.incidents') }}</h1>
      <span class="spacer" />
      <select v-model.number="streamId" class="input compact" :aria-label="t('common.stream')">
        <option :value="0">{{ t('incidents.allStreams') }}</option>
        <option v-for="s in streams" :key="s.id" :value="s.id">{{ s.name }}</option>
      </select>
      <div class="seg" role="group" :aria-label="t('incidents.levels')">
        <button v-for="l in levels" :key="l.id" type="button" :aria-pressed="shown.includes(l.id)" @click="toggleLevel(l.id)">{{ l.label }}</button>
      </div>
      <div class="seg" role="group" :aria-label="t('incidents.whichIncidentsToShow')">
        <button type="button" :aria-pressed="scope === 'open'" @click="scope = 'open'">{{ t('incidents.open') }}</button>
        <button type="button" :aria-pressed="scope === 'all'" @click="scope = 'all'">{{ t('common.all') }}</button>
      </div>
    </div>
    <div v-if="error" class="notice error">{{ error }}</div>
    <template v-if="ofServers.length">
      <div class="section-title">
        <h2>{{ t('incidents.ofServers') }}</h2>
        <span class="muted">{{ t('incidents.ofServersHint') }}</span>
      </div>
      <div class="panel" style="margin-bottom: 20px">
        <ServerIncidentTable :incidents="ofServers" :now="now" />
      </div>
      <div class="section-title"><h2>{{ t('incidents.ofStreams') }}</h2></div>
    </template>
    <div class="panel">
      <div v-if="!incidents" class="empty">{{ t('common.loading') }}</div>
      <IncidentTable v-else-if="incidents.length" :incidents="incidents" show-stream :now="now" />
      <div v-else class="empty">{{ scope === 'open' ? t('incidents.noOpenIncidentsOfTheSelected') : t('incidents.thereWereNoIncidentsOfThe') }}</div>
    </div>
  </div>
</template>
