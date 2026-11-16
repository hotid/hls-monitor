import { onBeforeUnmount, onMounted, ref, type Ref } from 'vue'
import { ApiError } from './api'

/**
 * Runs load() now and then every intervalMs while the tab is visible.
 * A failed refresh keeps the data already shown and reports the error.
 */
export function usePolling(load: (signal: AbortSignal) => Promise<void>, intervalMs: number) {
  const error: Ref<string | null> = ref(null)
  const loading = ref(true)
  let timer: number | undefined
  let ctl: AbortController | null = null
  let stopped = false

  async function tick() {
    if (stopped) return
    ctl?.abort()
    ctl = new AbortController()
    try {
      await load(ctl.signal)
      error.value = null
    } catch (e) {
      if (e instanceof DOMException && e.name === 'AbortError') return
      if (e instanceof ApiError && e.status === 401) return
      error.value = e instanceof Error ? e.message : String(e)
    } finally {
      loading.value = false
    }
    schedule()
  }

  function schedule() {
    clearTimeout(timer)
    if (!stopped && !document.hidden) timer = window.setTimeout(tick, intervalMs)
  }

  function onVisibility() {
    if (!document.hidden) void tick()
    else clearTimeout(timer)
  }

  onMounted(() => {
    document.addEventListener('visibilitychange', onVisibility)
    void tick()
  })
  onBeforeUnmount(() => {
    stopped = true
    clearTimeout(timer)
    ctl?.abort()
    document.removeEventListener('visibilitychange', onVisibility)
  })

  return { error, loading, refresh: tick }
}
