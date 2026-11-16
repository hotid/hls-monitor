import { defineStore } from 'pinia'
import { api } from '@/api'
import type { Overview, SystemInfo } from '@/types'

/** Data refreshed in the background for the whole interface. */
export const useLive = defineStore('live', {
  state: () => ({
    overview: null as Overview | null,
    system: null as SystemInfo | null,
    error: null as string | null,
    updatedAt: 0,
  }),
  getters: {
    openIncidents: (s) => s.overview?.incidents.length ?? 0,
    agentName: (s) => (id: number) => s.overview?.agents.find((a) => a.id === id)?.name ?? `#${id}`,
  },
  actions: {
    async refresh(signal?: AbortSignal) {
      this.overview = await api.get<Overview>('/api/v1/overview', signal)
      this.updatedAt = Date.now()
    },
    async refreshSystem(signal?: AbortSignal) {
      this.system = await api.get<SystemInfo>('/api/v1/system', signal)
    },
    reset() {
      this.overview = null
      this.system = null
    },
  },
})
