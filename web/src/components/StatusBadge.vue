<script setup lang="ts">
import { statusLabel } from '@/catalog'
import type { Status } from '@/types'

defineProps<{ status: Status; label?: string }>()
</script>

<template>
  <span class="badge" :class="status"><span class="dot" :class="status" />{{ label ?? statusLabel()[status] }}</span>
</template>
