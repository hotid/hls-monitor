import { defineStore } from 'pinia'

export interface Toast {
  id: number
  kind: 'ok' | 'error'
  text: string
}

let seq = 0

export const useToasts = defineStore('toasts', {
  state: () => ({ items: [] as Toast[] }),
  actions: {
    push(kind: Toast['kind'], text: string) {
      const id = ++seq
      this.items.push({ id, kind, text })
      setTimeout(() => this.dismiss(id), kind === 'error' ? 8000 : 3500)
    },
    ok(text: string) {
      this.push('ok', text)
    },
    error(e: unknown) {
      this.push('error', e instanceof Error ? e.message : String(e))
    },
    dismiss(id: number) {
      this.items = this.items.filter((t) => t.id !== id)
    },
  },
})
