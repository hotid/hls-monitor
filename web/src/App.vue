<script setup lang="ts">
import AppShell from './components/AppShell.vue'
import { locale } from './i18n'
import { useSession } from './stores/session'
import { useToasts } from './stores/toasts'

const session = useSession()
const toasts = useToasts()
</script>

<template>
  <!-- Pages are rebuilt when the language changes: they keep texts in constants. -->
  <AppShell v-if="session.user" :key="locale">
    <RouterView />
  </AppShell>
  <RouterView v-else :key="locale" />
  <div class="toasts" aria-live="polite">
    <div v-for="t in toasts.items" :key="t.id" class="toast" :class="t.kind" role="status" @click="toasts.dismiss(t.id)">{{ t.text }}</div>
  </div>
</template>
