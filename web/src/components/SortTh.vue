<script setup lang="ts" generic="T">
import { computed, nextTick, onBeforeUnmount, ref } from 'vue'
import { t } from '@/i18n'
import type { Sorter } from '@/sort'

// as: a header cell of its own, or one of several in a cell ("span").
const props = defineProps<{ sort: Sorter<T>; by: string; as?: 'th' | 'span' }>()
const sorted = computed(() => props.sort.state?.key === props.by)
const desc = computed(() => sorted.value && !!props.sort.state?.desc)
const kind = computed(() => props.sort.kind(props.by))
const filter = computed(() => props.sort.get(props.by))
const filtered = computed(() => props.sort.active(props.by))

const open = ref(false)
const button = ref<HTMLButtonElement | null>(null)
const panel = ref<HTMLElement | null>(null)
const place = ref({ top: '0px', left: '0px' })
const options = ref<string[]>([])
const panelWidth = 260

function position() {
  const r = button.value?.getBoundingClientRect()
  if (!r) return
  const left = Math.max(8, Math.min(r.right - panelWidth / 2, window.innerWidth - panelWidth - 8))
  place.value = { top: `${r.bottom + 8}px`, left: `${left}px` }
}

function outside(e: Event) {
  const t = e.target as Node
  if (panel.value?.contains(t) || button.value?.contains(t)) return
  close()
}

function moved(e: Event) {
  // The list inside the panel scrolls on its own.
  if (panel.value?.contains(e.target as Node)) return
  close()
}

function show() {
  options.value = props.sort.options(props.by)
  position()
  open.value = true
  document.addEventListener('mousedown', outside, true)
  window.addEventListener('scroll', moved, true)
  window.addEventListener('resize', close)
  void nextTick(() => panel.value?.querySelector<HTMLElement>('input')?.focus())
}

function close() {
  if (!open.value) return
  open.value = false
  document.removeEventListener('mousedown', outside, true)
  window.removeEventListener('scroll', moved, true)
  window.removeEventListener('resize', close)
}

function escape() {
  close()
  button.value?.focus()
}

onBeforeUnmount(close)

function pick(option: string, on: boolean) {
  const picked = filter.value.picked.filter((p) => p !== option)
  if (on) picked.push(option)
  props.sort.set(props.by, { picked })
}

function bound(which: 'min' | 'max', raw: string) {
  const n = raw.trim() === '' ? null : Number(raw.replace(',', '.'))
  props.sort.set(props.by, { [which]: n === null || Number.isNaN(n) ? null : n })
}

function reset() {
  props.sort.clear(props.by)
  escape()
}
</script>

<template>
  <component :is="as ?? 'th'" :aria-sort="(as ?? 'th') === 'th' ? (sorted ? (desc ? 'descending' : 'ascending') : 'none') : undefined" :class="{ 'th-part': as === 'span' }">
    <span class="th-cell">
      <button type="button" class="th-sort" :class="{ active: sorted, desc }" :title="t('table.sort')" @click="sort.toggle(by)">
        <span><slot /></span><span class="th-arrow" aria-hidden="true" />
      </button>
      <button
        v-if="kind"
        ref="button"
        type="button"
        class="th-filter"
        :class="{ active: filtered, open }"
        :aria-expanded="open"
        :aria-label="filtered ? t('table.filterIsOnChange') : t('table.filter')"
        :title="filtered ? t('table.filterIsOn') : t('table.filter')"
        @click="open ? close() : show()"
      >
        <svg viewBox="0 0 12 12" width="11" height="11" aria-hidden="true"><path d="M1 1.5h10L7.2 6.2V10l-2.4 1V6.2z" fill="currentColor" /></svg>
      </button>
    </span>
    <Teleport to="body">
      <div v-if="open" ref="panel" class="th-panel" role="dialog" :aria-label="t('table.columnFilter')" :style="place" @keydown.esc.stop="escape" @keydown.enter="escape">
        <input
          v-if="kind === 'text'"
          class="input compact"
          type="search"
          :placeholder="t('table.containsPlaceholder')"
          :aria-label="t('table.contains')"
          :value="filter.text"
          @input="sort.set(by, { text: ($event.target as HTMLInputElement).value })"
        />
        <div v-else-if="kind === 'choice'" class="th-options">
          <label v-for="o in options" :key="o" class="check">
            <input type="checkbox" :checked="filter.picked.includes(o)" @change="pick(o, ($event.target as HTMLInputElement).checked)" />
            <span class="truncate" :title="o">{{ o }}</span>
          </label>
        </div>
        <div v-else class="th-range">
          <input class="input compact" inputmode="decimal" :placeholder="t('table.rangeFrom')" :aria-label="t('table.from')" :value="filter.min ?? ''" @input="bound('min', ($event.target as HTMLInputElement).value)" />
          <span class="muted">—</span>
          <input class="input compact" inputmode="decimal" :placeholder="t('table.rangeTo')" :aria-label="t('table.to')" :value="filter.max ?? ''" @input="bound('max', ($event.target as HTMLInputElement).value)" />
          <span v-if="sort.unit(by)" class="muted nowrap">{{ sort.unit(by) }}</span>
        </div>
        <div class="th-panel-foot">
          <span class="muted">{{ t('table.of', { n: sort.rows.length, total: sort.total }) }}</span>
          <button type="button" class="btn small" :disabled="!filtered" @click="reset">{{ t('table.clear') }}</button>
        </div>
      </div>
    </Teleport>
  </component>
</template>
