<script setup lang="ts">
import { statusLabel } from '@/catalog'
import type { Status } from '@/types'

// icon shows the state by a sign alone, with its name for the pointer and
// for screen readers: where a column of words would take the room.
defineProps<{ status: Status; label?: string; icon?: boolean }>()
</script>

<template>
  <span v-if="icon" class="badge sign" :class="status" role="img" :title="label ?? statusLabel()[status]" :aria-label="label ?? statusLabel()[status]">
    <svg viewBox="0 0 16 16" width="14" height="14" aria-hidden="true">
      <path v-if="status === 'ok'" d="M3.5 8.5l3 3 6-7" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" />
      <path v-else-if="status === 'warn'" d="M8 3.5v5.5M8 12.2v.3" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" />
      <path v-else-if="status === 'fail'" d="M4.5 4.5l7 7M11.5 4.5l-7 7" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" />
      <path v-else-if="status === 'disabled'" d="M6 4.5v7M10 4.5v7" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" />
      <circle v-else cx="8" cy="8" r="4" fill="none" stroke="currentColor" stroke-width="1.6" stroke-dasharray="2.2 2" />
    </svg>
  </span>
  <span v-else class="badge" :class="status"><span class="dot" :class="status" />{{ label ?? statusLabel()[status] }}</span>
</template>

<style scoped>
.sign {
  justify-content: center;
  width: 24px;
  height: 22px;
  padding: 0;
}
</style>
