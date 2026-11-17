<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { api } from '@/api'
import { fmtFull } from '@/format'
import { t } from '@/i18n'
import { calculationRows, circumstanceRows, conclusion, problemAbout, type Details } from '@/incident'
import type { Incident, IncidentScope } from '@/types'

const props = defineProps<{ incident: Incident }>()
const details = computed<Details>(() => props.incident.details ?? {})
const about = computed(() => problemAbout()[props.incident.kind])
const verdict = computed(() => conclusion(props.incident.kind, details.value))
const figures = computed(() => calculationRows(details.value))
const context = computed(() => circumstanceRows(details.value))

// Who else sees the problem. A finding of a live diagnosis has no id and
// nothing to compare with.
const scope = ref<IncidentScope | null>(null)
onMounted(async () => {
  if (!props.incident.id) return
  try {
    scope.value = await api.get<IncidentScope>(`/incidents/${props.incident.id}/scope`)
  } catch {
    scope.value = null
  }
})
const scopeLines = computed(() => {
  const s = scope.value
  if (!s) return []
  const lines: string[] = []
  const names = (list: IncidentScope['agents']) => list.map((a) => a.name).join(', ')
  const seen = s.agents.filter((a) => a.saw)
  const clear = s.agents.filter((a) => a.checked && !a.saw)
  if (s.server_wide) lines.push(t('incidents.scopeServerWide', { server: s.server, n: s.same_server.length }))
  else if (s.server && !s.same_server.length) lines.push(t('incidents.scopeServerAlone', { server: s.server }))
  else if (s.server) lines.push(t('incidents.scopeServerOne', { server: s.server }))
  if (s.elsewhere) lines.push(t('incidents.scopeElsewhere', { n: s.elsewhere }))
  if (s.vantage === 'all') lines.push(t('incidents.scopeAll', { names: names(seen) }))
  else if (s.vantage === 'only_this') lines.push(t('incidents.scopeOnlyThis', { agent: names(seen), names: names(clear) }))
  else if (s.vantage === 'some') lines.push(t('incidents.scopeSome', { seen: names(seen), clear: names(clear) }))
  else lines.push(t('incidents.scopeSingle'))
  return lines
})
</script>

<template>
  <div class="incident-details">
    <div class="col">
      <template v-if="about">
        <h3>{{ t('incidents.whatItMeans') }}</h3>
        <p>{{ about.what }}</p>
        <h3>{{ t('incidents.howItIsDetermined') }}</h3>
        <p>{{ about.how }}</p>
      </template>
      <h3>{{ t('common.checkMessage') }}</h3>
      <p class="mono">{{ incident.message }}</p>
      <p class="muted">
        {{
          incident.ended_at
            ? t('incidents.startedLastSeenEndedRepeats', { start: fmtFull(incident.started_at), last: fmtFull(incident.last_seen), end: fmtFull(incident.ended_at), n: incident.count })
            : t('incidents.startedLastSeenRepeats', { start: fmtFull(incident.started_at), last: fmtFull(incident.last_seen), n: incident.count })
        }}
      </p>
    </div>
    <div class="col">
      <template v-if="verdict.length">
        <h3>{{ t('incidents.conclusion') }}</h3>
        <p v-for="(line, i) in verdict" :key="i">{{ line }}</p>
      </template>
      <template v-if="scopeLines.length">
        <h3>{{ t('incidents.scopeTitle') }}<span class="muted">{{ t('incidents.scopeIsAGuess') }}</span></h3>
        <p v-for="(line, i) in scopeLines" :key="i">{{ line }}</p>
        <p v-if="scope?.same_server.length">
          <template v-for="(s, i) in scope.same_server" :key="s.id">
            <RouterLink :to="`/streams/${s.id}`">{{ s.name }}</RouterLink><span v-if="i + 1 < scope.same_server.length">, </span>
          </template>
        </p>
      </template>
      <template v-if="figures.length">
        <h3>{{ t('incidents.calculation') }}<span class="muted">{{ t('incidents.fromTheLastOccurrence') }}</span></h3>
        <dl>
          <template v-for="r in figures" :key="r.label">
            <dt>{{ r.label }}</dt>
            <dd :class="{ mono: r.mono }">{{ r.value }}</dd>
          </template>
        </dl>
      </template>
      <template v-if="context.length">
        <h3>{{ t('incidents.circumstancesOfTheCheck') }}</h3>
        <dl>
          <template v-for="r in context" :key="r.label">
            <dt>{{ r.label }}</dt>
            <dd :class="{ mono: r.mono }">{{ r.value }}</dd>
          </template>
        </dl>
      </template>
      <p v-if="!figures.length && !context.length" class="muted">
        {{ t('incidents.noFiguresWereSavedForThis') }}
      </p>
    </div>
  </div>
</template>

<style scoped>
.incident-details {
  display: grid;
  grid-template-columns: minmax(0, 5fr) minmax(0, 7fr);
  gap: 8px 32px;
  padding: 6px 2px 10px 16px;
  font-size: 13px;
  white-space: normal;
}

h3 {
  margin: 12px 0 4px;
  font-size: 12px;
  font-weight: 600;
  color: var(--text-muted);
}

.col > h3:first-child {
  margin-top: 0;
}

p {
  margin: 0 0 4px;
  max-width: 70ch;
  overflow-wrap: anywhere;
}

dl {
  display: grid;
  grid-template-columns: max-content minmax(0, 1fr);
  gap: 3px 16px;
  margin: 0;
}

dt {
  color: var(--text-muted);
}

dd {
  margin: 0;
  overflow-wrap: anywhere;
  white-space: pre-line;
}

@media (max-width: 900px) {
  .incident-details {
    grid-template-columns: minmax(0, 1fr);
  }

  dl {
    grid-template-columns: minmax(0, 1fr);
  }
}
</style>
