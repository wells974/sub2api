<template>
  <span role="status" class="inline-flex items-center gap-2 text-sm">
    {{ t(`admin.accounts.newModelSync.${result.state}`, { count: result.count }) }}
    <button v-if="result.state === 'failed'" type="button" class="text-primary-600 underline" @click="$emit('retry', result)">
      {{ t('admin.accounts.newModelSync.retry') }}
    </button>
  </span>
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import type { NewAccountModelSync } from '@/composables/useNewAccountModelSync'

defineProps<{ result: NewAccountModelSync }>()
defineEmits<{ retry: [row: NewAccountModelSync] }>()
const { t } = useI18n()
</script>
