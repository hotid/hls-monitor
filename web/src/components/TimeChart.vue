<script setup lang="ts">
// One time-series chart: uPlot canvas plus a legend that shows the values
// under the cursor (or the latest ones).
import { computed, onBeforeUnmount, onMounted, ref, shallowRef, watch } from 'vue'
import uPlot from 'uplot'
import { fmtUnit, type Unit } from '@/catalog'
import { fmtWhen } from '@/format'
import { t } from '@/i18n'

export interface ChartLine {
  label: string
  points: [number, number][]
}

const props = defineProps<{
  title: string
  hint?: string
  unit: Unit
  lines: ChartLine[]
  from: number
  to: number
  step: number
  steps?: boolean
  max?: number
  threshold?: number
  loading?: boolean
  /** Line colours, when the default palette would mislead (states have their own). */
  colors?: string[]
  /** Why the chart is empty, when there is a known reason. */
  emptyHint?: string
}>()
const emit = defineEmits<{ zoom: [from: number, to: number] }>()

const palette = ['#00b4d8', '#f4a261', '#2ec4b6', '#b197fc', '#ff7a85', '#ffd166', '#74b9ff', '#95d5b2', '#e599f7', '#a9e34b']
const color = (i: number) => props.colors?.[i] ?? palette[i % palette.length]!

const host = ref<HTMLElement | null>(null)
const plot = shallowRef<uPlot | null>(null)
const hoverIdx = ref<number | null>(null)
let observer: ResizeObserver | null = null

// All lines on one time grid; a step without a sample is a gap.
const aligned = computed(() => {
  const n = Math.max(1, Math.round((props.to - props.from) / props.step)) + 1
  const xs = new Array<number>(n)
  for (let i = 0; i < n; i++) xs[i] = props.from + i * props.step
  const ys = props.lines.map((line) => {
    const col = new Array<number | null>(n).fill(null)
    for (const [t, v] of line.points) {
      const i = Math.round((t - props.from) / props.step)
      if (i >= 0 && i < n) col[i] = v
    }
    return col
  })
  return { xs, ys }
})

const hasData = computed(() => aligned.value.ys.some((col) => col.some((v) => v !== null)))

const legend = computed(() =>
  props.lines.map((line, i) => {
    const col = aligned.value.ys[i] ?? []
    let v: number | null = null
    if (hoverIdx.value !== null) {
      v = col[hoverIdx.value] ?? null
    } else {
      for (let j = col.length - 1; j >= 0; j--) {
        if (col[j] != null) {
          v = col[j]!
          break
        }
      }
    }
    return { label: line.label, color: color(i), value: fmtUnit(v, props.unit), off: hidden.value.has(line.label) }
  }),
)

// The line pointed at in the legend stands out, the others fade.
const focused = ref<string | null>(null)
function focus(label: string | null) {
  if (label !== null && hidden.value.has(label)) label = null
  focused.value = label
  const i = label === null ? -1 : props.lines.findIndex((l) => l.label === label)
  plot.value?.setSeries(i < 0 ? null : i + 1, { focus: true })
}

// Which lines are switched off, by label. A click on a label leaves that
// line alone on the chart, and a click on the only one left brings all
// back; with Ctrl, Cmd or Shift a click adds or removes one line. This is
// what people know from Grafana.
const hidden = ref(new Set<string>())
function pick(label: string, e: MouseEvent | KeyboardEvent) {
  const all = props.lines.map((l) => l.label)
  const shown = all.filter((l) => !hidden.value.has(l))
  let next: string[]
  if (e.ctrlKey || e.metaKey || e.shiftKey) {
    next = shown.includes(label) ? shown.filter((l) => l !== label) : [...shown, label]
  } else {
    next = shown.length === 1 && shown[0] === label ? all : [label]
  }
  // Nothing shown is an empty chart: everything comes back instead.
  if (!next.length) next = all
  hidden.value = new Set(all.filter((l) => !next.includes(l)))
  props.lines.forEach((l, i) => plot.value?.setSeries(i + 1, { show: !hidden.value.has(l.label) }))
  focus(hidden.value.has(label) ? null : label)
}

const hoverTime = computed(() => (hoverIdx.value === null ? null : fmtWhen((aligned.value.xs[hoverIdx.value] ?? 0) * 1000)))

function axisTime(_u: uPlot, ticks: number[]): string[] {
  const span = props.to - props.from
  return ticks.map((t) => {
    const d = new Date(t * 1000)
    const hm = `${String(d.getHours()).padStart(2, '0')}:${String(d.getMinutes()).padStart(2, '0')}`
    if (span > 36 * 3600) return `${String(d.getDate()).padStart(2, '0')}.${String(d.getMonth() + 1).padStart(2, '0')} ${hm}`
    if (span <= 20 * 60) return `${hm}:${String(d.getSeconds()).padStart(2, '0')}`
    return hm
  })
}

function build() {
  plot.value?.destroy()
  plot.value = null
  const el = host.value
  if (!el) return
  const grid = { stroke: 'rgba(26,48,80,0.7)', width: 1 }
  const axisFont = '11px Manrope, system-ui, sans-serif'
  const paths = props.steps ? uPlot.paths.stepped!({ align: 1 }) : undefined

  const opts: uPlot.Options = {
    width: Math.max(el.clientWidth, 120),
    height: 150,
    padding: [8, 8, 0, 0],
    cursor: { sync: { key: 'hls-monitor' }, points: { size: 6 }, drag: { x: true, y: false, setScale: false } },
    legend: { show: false },
    // How much the other lines fade while one is pointed at in the legend.
    focus: { alpha: 0.15 },
    scales: {
      x: { time: true, range: () => [props.from, props.to] },
      y: {
        range: (_u, dmin, dmax) => {
          let lo = Math.min(0, dmin ?? 0)
          let hi = props.max ?? Math.max(dmax ?? 0, props.threshold ?? 0)
          if (!(hi > lo)) hi = lo + 1
          if (props.max === undefined) hi += (hi - lo) * 0.12
          if (lo < 0) lo -= (hi - lo) * 0.08
          return [lo, hi]
        },
      },
    },
    axes: [
      { stroke: '#5a7a94', font: axisFont, grid, ticks: { show: false }, values: axisTime, space: 70 },
      {
        stroke: '#5a7a94', font: axisFont, grid, ticks: { show: false }, size: props.unit === 'status' ? 104 : props.unit === 'bps' ? 92 : 62,
        values: (_u, ticks) => ticks.map((v) => fmtUnit(v, props.unit)),
        // States are whole numbers: no ticks in between.
        splits: props.unit === 'status' ? () => [0, 1, 2] : undefined,
      },
    ],
    series: [
      {},
      ...props.lines.map((line, i) => ({
        label: line.label,
        show: !hidden.value.has(line.label),
        stroke: color(i),
        width: 1.5,
        paths,
        fill: props.steps ? color(i) + '33' : undefined,
        spanGaps: false,
        points: { show: false },
      })),
    ],
    hooks: {
      setCursor: [
        (u) => {
          hoverIdx.value = u.cursor.idx ?? null
        },
      ],
      setSelect: [
        (u) => {
          if (u.select.width < 4) return
          const a = u.posToVal(u.select.left, 'x')
          const b = u.posToVal(u.select.left + u.select.width, 'x')
          u.setSelect({ left: 0, top: 0, width: 0, height: 0 }, false)
          if (b - a >= 10) emit('zoom', Math.floor(a), Math.ceil(b))
        },
      ],
      draw: [
        (u) => {
          if (props.threshold === undefined) return
          const y = u.valToPos(props.threshold, 'y', true)
          const { left, width, top, height } = u.bbox
          if (y < top || y > top + height) return
          const c = u.ctx
          c.save()
          c.strokeStyle = 'rgba(230,57,70,0.7)'
          c.setLineDash([4, 4])
          c.lineWidth = 1
          c.beginPath()
          c.moveTo(left, y)
          c.lineTo(left + width, y)
          c.stroke()
          c.restore()
        },
      ],
    },
  }
  plot.value = new uPlot(opts, [aligned.value.xs, ...aligned.value.ys] as uPlot.AlignedData, el)
}

// Same set of lines: only the data changes. Otherwise the chart is rebuilt.
watch(
  () => props.lines.map((l) => l.label).join('\u0000') + '|' + props.unit + '|' + props.steps + '|' + (props.colors ?? []).join(','),
  () => build(),
)
watch(aligned, (a) => {
  if (plot.value && plot.value.series.length === a.ys.length + 1) {
    plot.value.setData([a.xs, ...a.ys] as uPlot.AlignedData)
  } else {
    build()
  }
})

onMounted(() => {
  build()
  observer = new ResizeObserver(() => {
    const el = host.value
    if (el && plot.value && el.clientWidth > 0) plot.value.setSize({ width: el.clientWidth, height: 150 })
  })
  if (host.value) observer.observe(host.value)
})
onBeforeUnmount(() => {
  observer?.disconnect()
  plot.value?.destroy()
})
</script>

<template>
  <div class="chart panel">
    <div class="chart-head">
      <h2 :title="hint">{{ title }}</h2>
      <span v-if="hoverTime" class="mono muted">{{ hoverTime }}</span>
    </div>
    <div class="chart-body">
      <div ref="host" class="chart-host" @mouseleave="hoverIdx = null" />
      <div v-if="!hasData" class="chart-empty muted">{{ loading ? t('common.loading') : (emptyHint ?? t('chart.noDataForThisPeriod')) }}</div>
    </div>
    <ul class="chart-legend" :class="{ focusing: focused !== null }" @mouseleave="focus(null)">
      <li
        v-for="l in legend" :key="l.label" :class="{ off: l.off, focused: focused === l.label }" :title="l.label + ' — ' + t('chart.legendHint')"
        role="button" tabindex="0" :aria-pressed="!l.off"
        @mouseenter="focus(l.label)" @focus="focus(l.label)" @blur="focus(null)"
        @click="pick(l.label, $event)" @keydown.enter.prevent="pick(l.label, $event)" @keydown.space.prevent="pick(l.label, $event)"
      >
        <i :style="{ background: l.color }" />
        <span class="truncate">{{ l.label }}</span>
        <b class="mono">{{ l.value }}</b>
      </li>
    </ul>
  </div>
</template>

<style scoped>
.chart {
  display: flex;
  flex-direction: column;
  padding: 10px 12px 8px;
}

.chart-head {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  gap: 8px;
  margin-bottom: 4px;
  min-height: 18px;
}

.chart-head h2 {
  cursor: help;
}

.chart-body {
  position: relative;
}

.chart-host {
  width: 100%;
  height: 150px;
}

.chart-empty {
  position: absolute;
  inset: 0;
  display: grid;
  place-items: center;
  padding: 0 24px;
  text-align: center;
  pointer-events: none;
  font-size: 12px;
}

.chart-legend {
  display: flex;
  flex-wrap: wrap;
  gap: 2px 14px;
  margin: 6px 0 0;
  padding: 0;
  list-style: none;
  max-height: 58px;
  overflow-y: auto;
  font-size: 12px;
}

.chart-legend li {
  display: flex;
  align-items: center;
  gap: 6px;
  min-width: 0;
  max-width: 100%;
  color: var(--text-muted);
}

.chart-legend li {
  cursor: pointer;
  user-select: none;
}
.chart-legend li:hover span {
  color: var(--text);
}
.chart-legend li {
  transition: opacity 0.1s;
}
.chart-legend.focusing li:not(.focused) {
  opacity: 0.45;
}
.chart-legend li.focused span {
  color: var(--text-strong);
}
.chart-legend li.off,
.chart-legend.focusing li.off {
  opacity: 0.4;
}
.chart-legend li.off span {
  text-decoration: line-through;
}

.chart-legend i {
  width: 10px;
  height: 3px;
  border-radius: 2px;
  flex: none;
}

.chart-legend b {
  color: var(--text-strong);
  font-weight: 400;
  white-space: nowrap;
}
</style>
