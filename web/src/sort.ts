import { computed, reactive, ref, watch } from 'vue'

export type SortValue = string | number | null | undefined

export interface SortState {
  key: string
  desc: boolean
}

export type FilterKind = 'text' | 'choice' | 'range'

export interface Column<T> {
  /** What the column is sorted by. */
  value: (row: T) => SortValue
  /** How the column can be narrowed down; none when omitted. */
  filter?: FilterKind
  /** What the cell says, when that differs from the sorting value. */
  label?: (row: T) => string
  /** For a range: the unit the bounds are typed in and its size in sorting values. */
  unit?: string
  scale?: number
}

export interface ColumnFilter {
  text: string
  picked: string[]
  min: number | null
  max: number | null
}

export interface Sorter<T> {
  /** Null while the table keeps the order it was given. */
  state: SortState | null
  /** Filtered and sorted. */
  rows: T[]
  /** How many rows there are before filtering. */
  total: number
  /** Whether any column is filtered. */
  filtering: boolean
  toggle(key: string): void
  kind(key: string): FilterKind | undefined
  unit(key: string): string
  options(key: string): string[]
  get(key: string): ColumnFilter
  set(key: string, patch: Partial<ColumnFilter>): void
  active(key: string): boolean
  clear(key?: string): void
}

const none: ColumnFilter = Object.freeze({ text: '', picked: Object.freeze([]) as unknown as string[], min: null, max: null })
// A longer list of values is searched by text instead of being ticked.
const mostChoices = 40

function empty(v: SortValue): boolean {
  return v === null || v === undefined || v === '' || (typeof v === 'number' && Number.isNaN(v))
}

function compare(a: SortValue, b: SortValue): number {
  if (typeof a === 'number' && typeof b === 'number') return a - b
  return String(a).localeCompare(String(b), 'ru', { numeric: true, sensitivity: 'base' })
}

function isActive(f: ColumnFilter | undefined): boolean {
  return !!f && (f.text.trim() !== '' || f.picked.length > 0 || f.min !== null || f.max !== null)
}

/**
 * Sorting and filtering from the column headers of a table.
 *
 * A click on a header sorts by it (figures from the largest, text from A),
 * the second click reverses the order, the third returns the order the rows
 * came in. Rows without a value stay at the bottom in either direction.
 *
 * A column that names a filter can be narrowed down: by a substring, by a
 * set of its values or by a range. Filters of several columns add up.
 *
 * `remember` is the name under which the choice is kept: the order between
 * visits, the filters until the tab is closed.
 */
export function useSort<T>(rows: () => readonly T[], columns: Record<string, Column<T> | ((row: T) => SortValue)>, remember?: string): Sorter<T> {
  const cols: Record<string, Column<T>> = {}
  for (const [k, c] of Object.entries(columns)) cols[k] = typeof c === 'function' ? { value: c } : c

  const sortKey = remember ? `sort-${remember}` : ''
  const filterKey = remember ? `filter-${remember}` : ''
  const state = ref<SortState | null>(null)
  const filters = ref<Record<string, ColumnFilter>>({})
  try {
    if (sortKey) {
      const saved = JSON.parse(localStorage.getItem(sortKey) ?? 'null') as SortState | null
      if (saved && typeof saved.key === 'string' && saved.key in cols) state.value = { key: saved.key, desc: !!saved.desc }
    }
    if (filterKey) {
      const saved = JSON.parse(sessionStorage.getItem(filterKey) ?? '{}') as Record<string, Partial<ColumnFilter>>
      for (const [k, f] of Object.entries(saved)) {
        if (!cols[k]?.filter || !f) continue
        filters.value[k] = {
          text: typeof f.text === 'string' ? f.text : '',
          picked: Array.isArray(f.picked) ? f.picked.map(String) : [],
          min: typeof f.min === 'number' ? f.min : null,
          max: typeof f.max === 'number' ? f.max : null,
        }
      }
    }
  } catch {
    // Storage is unavailable or holds something else: start as given.
  }

  function label(key: string, row: T): string {
    const c = cols[key]
    if (!c) return ''
    if (c.label) return c.label(row)
    const v = c.value(row)
    return empty(v) ? '—' : String(v)
  }

  function options(key: string): string[] {
    const c = cols[key]
    if (!c) return []
    const seen = new Map<string, SortValue>()
    for (const row of rows()) {
      const l = label(key, row)
      if (!seen.has(l)) seen.set(l, c.value(row))
    }
    // Something picked earlier stays in the list even when no row has it now.
    for (const p of filters.value[key]?.picked ?? []) if (!seen.has(p)) seen.set(p, p)
    return [...seen.entries()]
      .sort(([la, a], [lb, b]) => {
        const ea = empty(a)
        const eb = empty(b)
        if (ea || eb) return ea === eb ? 0 : ea ? 1 : -1
        return compare(a, b) || compare(la, lb)
      })
      .map(([l]) => l)
  }

  function kind(key: string): FilterKind | undefined {
    const k = cols[key]?.filter
    if (k !== 'choice') return k
    // A ticked value keeps the list, otherwise it could not be unticked.
    return options(key).length > mostChoices && !filters.value[key]?.picked.length ? 'text' : 'choice'
  }

  function passes(key: string, f: ColumnFilter, row: T): boolean {
    const c = cols[key]
    if (!c) return true
    const q = f.text.trim().toLowerCase()
    if (q && !label(key, row).toLowerCase().includes(q)) return false
    if (f.picked.length && !f.picked.includes(label(key, row))) return false
    if (f.min !== null || f.max !== null) {
      const v = c.value(row)
      if (typeof v !== 'number' || Number.isNaN(v)) return false
      const scaled = v / (c.scale ?? 1)
      if (f.min !== null && scaled < f.min) return false
      if (f.max !== null && scaled > f.max) return false
    }
    return true
  }

  const filtered = computed(() => {
    const on = Object.entries(filters.value).filter(([k, f]) => cols[k] && isActive(f))
    const all = rows()
    return on.length ? all.filter((row) => on.every(([k, f]) => passes(k, f, row))) : (all as T[])
  })

  const sorted = computed(() => {
    const all = filtered.value
    const s = state.value
    const value = s && cols[s.key]?.value
    if (!s || !value) return all as T[]
    const sign = s.desc ? -1 : 1
    return all
      .map((row, i) => ({ row, i, v: value(row) }))
      .sort((a, b) => {
        const ea = empty(a.v)
        const eb = empty(b.v)
        if (ea || eb) return ea === eb ? a.i - b.i : ea ? 1 : -1
        return sign * compare(a.v, b.v) || a.i - b.i
      })
      .map((x) => x.row)
  })

  function toggle(key: string) {
    const value = cols[key]?.value
    if (!value) return
    // Figures start from the largest, text from A.
    const first = typeof rows().map(value).find((v) => !empty(v)) === 'number'
    const s = state.value
    if (s?.key !== key) state.value = { key, desc: first }
    else if (s.desc === first) state.value = { key, desc: !first }
    else state.value = null
    if (!sortKey) return
    try {
      if (state.value) localStorage.setItem(sortKey, JSON.stringify(state.value))
      else localStorage.removeItem(sortKey)
    } catch {
      // Not remembered, still sorted.
    }
  }

  function set(key: string, patch: Partial<ColumnFilter>) {
    if (!cols[key]) return
    const next = { ...(filters.value[key] ?? { text: '', picked: [], min: null, max: null }), ...patch }
    const all = { ...filters.value }
    if (isActive(next)) all[key] = next
    else delete all[key]
    filters.value = all
  }

  function clear(key?: string) {
    if (key === undefined) filters.value = {}
    else if (key in filters.value) {
      const all = { ...filters.value }
      delete all[key]
      filters.value = all
    }
  }

  if (filterKey) {
    watch(filters, (f) => {
      try {
        if (Object.keys(f).length) sessionStorage.setItem(filterKey, JSON.stringify(f))
        else sessionStorage.removeItem(filterKey)
      } catch {
        // Not remembered, still filtered.
      }
    })
  }

  return reactive({
    state,
    rows: sorted,
    total: computed(() => rows().length),
    filtering: computed(() => Object.keys(filters.value).length > 0),
    toggle,
    kind,
    unit: (key: string) => cols[key]?.unit ?? '',
    options,
    get: (key: string) => filters.value[key] ?? none,
    set,
    active: (key: string) => isActive(filters.value[key]),
    clear,
  }) as unknown as Sorter<T>
}

/** Timestamp for sorting; empty when there is no time. */
export function timeValue(t: string | null | undefined): number | undefined {
  if (!t) return undefined
  const ms = Date.parse(t)
  return Number.isNaN(ms) ? undefined : ms
}
