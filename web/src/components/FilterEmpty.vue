<script setup lang="ts" generic="T">
import { t } from '@/i18n'
import type { Sorter } from '@/sort'

defineProps<{ sort: Sorter<T> }>()
</script>

<template>
  <tr v-if="sort.filtering && !sort.rows.length">
    <td colspan="100" class="empty">
      {{ t('table.nothingMatchesTheFiltersInThe') }}
      <button type="button" class="btn small" style="margin-left: 10px" @click="sort.clear()">{{ t('table.clearFilters') }}</button>
    </td>
  </tr>
</template>
