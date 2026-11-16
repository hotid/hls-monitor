<script setup lang="ts">
// A strip of equal time buckets coloured by the worst status seen in each.
import { computed } from 'vue'
import { fmtWhen } from '@/format'
import { t } from '@/i18n'

const props = defineProps<{
  points: [number, number][] | undefined
  from: number
  to: number
  step: number
}>()

const labels = [t('common.okWord'), t('common.warningWord'), t('common.failureWord')]

const buckets = computed(() => {
  const n = Math.max(1, Math.round((props.to - props.from) / props.step))
  const out: { cls: string; title: string }[] = []
  const byIndex = new Map<number, number>()
  for (const [t, v] of props.points ?? []) {
    byIndex.set(Math.round((t - props.from) / props.step), v)
  }
  for (let i = 0; i < n; i++) {
    // A point at time t summarises the step that ends at t.
    const v = byIndex.get(i + 1)
    const start = (props.from + i * props.step) * 1000
    const cls = v === undefined ? 'none' : v >= 2 ? 'fail' : v >= 1 ? 'warn' : 'ok'
    out.push({ cls, title: `${fmtWhen(start)} — ${v === undefined ? t('chart.noChecks') : labels[Math.min(2, Math.round(v))]}` })
  }
  return out
})
</script>

<template>
  <div class="strip" role="img" :aria-label="t('common.statusHistory')">
    <span v-for="(b, i) in buckets" :key="i" :class="b.cls" :title="b.title" />
  </div>
</template>

<style scoped>
.strip {
  display: flex;
  gap: 1px;
  height: 22px;
  min-width: 120px;
}

.strip span {
  flex: 1;
  min-width: 1px;
  border-radius: 1px;
  background: var(--nodata);
  opacity: 0.45;
}

.strip span.ok {
  background: var(--success);
  opacity: 0.75;
}

.strip span.warn {
  background: var(--accent-warm);
  opacity: 1;
}

.strip span.fail {
  background: var(--danger);
  opacity: 1;
}

.strip span:hover {
  opacity: 1;
  outline: 1px solid var(--text-strong);
}
</style>
