<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref } from 'vue'
import { t } from '@/i18n'

defineProps<{ title: string; wide?: boolean }>()
const emit = defineEmits<{ close: [] }>()
const box = ref<HTMLElement | null>(null)
let previous: Element | null = null

function onKey(e: KeyboardEvent) {
  if (e.key === 'Escape') emit('close')
}

onMounted(() => {
  previous = document.activeElement
  document.addEventListener('keydown', onKey)
  // Put the keyboard inside the dialog: first field, else the dialog itself.
  const first = box.value?.querySelector<HTMLElement>('[autofocus], .modal-body input, .modal-body select, .modal-body textarea')
  ;(first ?? box.value)?.focus()
})
onBeforeUnmount(() => {
  document.removeEventListener('keydown', onKey)
  if (previous instanceof HTMLElement) previous.focus()
})
</script>

<template>
  <Teleport to="body">
    <div class="modal-backdrop" @mousedown.self="emit('close')">
      <div ref="box" class="modal" :class="{ wide }" role="dialog" aria-modal="true" :aria-label="title" tabindex="-1">
        <div class="modal-head">
          <h2>{{ title }}</h2>
          <button class="btn ghost small" type="button" :aria-label="t('common.close')" @click="emit('close')">✕</button>
        </div>
        <slot />
      </div>
    </div>
  </Teleport>
</template>
