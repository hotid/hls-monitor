import { defineStore } from 'pinia'
import { api, ApiError } from '@/api'
import type { User } from '@/types'

export const useSession = defineStore('session', {
  state: () => ({
    user: null as User | null,
    // false until the first check of an existing session has finished
    ready: false,
  }),
  getters: {
    isAdmin: (s) => s.user?.role === 'admin',
  },
  actions: {
    async restore() {
      try {
        this.user = await api.get<User>('/api/v1/auth/me')
      } catch (e) {
        if (!(e instanceof ApiError)) throw e
        this.user = null
      } finally {
        this.ready = true
      }
    },
    async signIn(username: string, password: string) {
      this.user = await api.post<User>('/api/v1/auth/login', { username, password })
    },
    async signOut() {
      try {
        await api.post('/api/v1/auth/logout')
      } finally {
        this.user = null
      }
    },
  },
})
