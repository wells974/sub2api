<template>
  <div v-if="details" class="min-w-48 space-y-1">
    <p>{{ t('accountOps.pool.available') }}: {{ details.available }} / {{ details.total }}</p>
    <p v-if="details.previous_total !== undefined" class="text-gray-500">{{ t('accountOps.pool.previous') }}: {{ details.previous_available }} / {{ details.previous_total }}</p>
    <p v-for="change in details.changes" :key="change.account_id" class="text-gray-500">
      {{ change.account_name }} (#{{ change.account_id }}): <template v-if="change.before">{{ change.before }} → </template>{{ change.after }}
    </p>
    <p v-if="details.omitted" class="text-gray-500">{{ t('accountOps.pool.omitted', { count: details.omitted }) }}</p>
  </div>
</template>
<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import type { AccountOpsPoolDetails } from '@/api/admin/accountOpsPool'
defineProps<{ details?: AccountOpsPoolDetails }>()
const { t } = useI18n()
</script>
