<script setup lang="ts">
import { t } from '@/i18n'
import ModalDialog from './ModalDialog.vue'

defineProps<{ title: string; action: string; busy?: boolean }>()
const emit = defineEmits<{ close: []; confirm: [] }>()
</script>

<template>
  <ModalDialog :title="title" @close="emit('close')">
    <div class="modal-body"><slot /></div>
    <div class="modal-foot">
      <button class="btn" type="button" autofocus @click="emit('close')">{{ t('common.cancel') }}</button>
      <button class="btn danger" type="button" :disabled="busy" @click="emit('confirm')">{{ action }}</button>
    </div>
  </ModalDialog>
</template>
