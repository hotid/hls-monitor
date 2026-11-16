<script setup lang="ts">
// A playlist as it was when something went wrong, next to what it said at
// the check before: most playlist problems are a difference between the two.
import { computed } from 'vue'
import { fmtSeconds, fmtTime } from '@/format'
import { t } from '@/i18n'

const props = defineProps<{
  body: string
  time: string
  previous?: string
  previousTime?: string | null
}>()

interface Line {
  text: string
  /** The other playlist does not have this line. */
  own: boolean
}

function lines(text: string, other: string | undefined): Line[] {
  const theirs = new Set((other ?? '').split('\n').map((l) => l.trim()))
  return text
    .replace(/\n+$/, '')
    .split('\n')
    .map((l) => ({ text: l, own: other !== undefined && l.trim() !== '' && !theirs.has(l.trim()) }))
}

const has = computed(() => !!props.previous && !!props.previousTime)
const before = computed(() => (has.value ? lines(props.previous ?? '', props.body) : []))
const now = computed(() => lines(props.body, has.value ? props.previous : undefined))
const span = computed(() => (has.value ? (new Date(props.time).getTime() - new Date(props.previousTime ?? 0).getTime()) / 1000 : 0))
</script>

<template>
  <div class="pair" :class="{ single: !has }">
    <div v-if="has" class="side">
      <div class="caption">{{ t('snapshot.previous', { time: fmtTime(previousTime), span: fmtSeconds(span) }) }}</div>
      <pre class="code"><span v-for="(l, i) in before" :key="i" :class="{ gone: l.own }">{{ l.text + '\n' }}</span></pre>
    </div>
    <div class="side">
      <div class="caption">{{ t('snapshot.current', { time: fmtTime(time) }) }}</div>
      <pre class="code"><span v-for="(l, i) in now" :key="i" :class="{ fresh: l.own }">{{ l.text + '\n' }}</span></pre>
    </div>
    <p class="muted note">{{ has ? t('snapshot.legend') : t('snapshot.noPrevious') }}</p>
  </div>
</template>

<style scoped>
.pair {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 12px;
}

.pair.single {
  grid-template-columns: minmax(0, 1fr);
}

@media (max-width: 900px) {
  .pair {
    grid-template-columns: minmax(0, 1fr);
  }
}

.caption {
  margin-bottom: 6px;
  font-size: 12px;
  color: var(--text-muted);
}

.code {
  max-height: 60vh;
  margin: 0;
  overflow: auto;
}

.code span {
  display: block;
}

.gone {
  background: var(--danger-tint-15);
}

.fresh {
  background: var(--success-tint-15);
}

.note {
  grid-column: 1 / -1;
  margin: 0;
  font-size: 12px;
}
</style>
