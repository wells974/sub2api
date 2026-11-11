<template>
  <form class="card p-5" data-testid="pool-notification-settings" @submit.prevent="save">
    <fieldset :disabled="busy || disabled" class="space-y-3">
      <h2 class="text-sm font-semibold">{{ t('accountOps.pool.title') }}</h2>
      <p class="text-xs leading-5 text-gray-500">{{ t('accountOps.pool.description') }}</p>
      <label class="flex items-center gap-2 text-sm"><input v-model="draft.enabled" type="checkbox" data-testid="pool-enabled" @change="dirty = true" />{{ t('accountOps.pool.enabled') }}</label>
      <label class="flex items-center gap-2 text-sm"><input v-model="draft.notify_states" type="checkbox" data-testid="pool-states" @change="dirty = true" />{{ t('accountOps.pool.states') }}</label>
      <label class="flex items-center gap-2 text-sm"><input v-model="draft.notify_counts" type="checkbox" data-testid="pool-counts" @change="dirty = true" />{{ t('accountOps.pool.counts') }}</label>
      <p class="text-xs leading-5 text-gray-500">{{ t('accountOps.pool.hint') }}</p>
      <button type="submit" :disabled="!dirty" class="btn btn-secondary text-sm">{{ t(busy ? 'accountOps.pool.saving' : 'accountOps.pool.save') }}</button>
    </fieldset>
    <details class="mt-3 text-xs leading-5 text-gray-500">
      <summary class="cursor-pointer">Bark</summary>
      <p class="mt-2">{{ t('accountOps.pool.bark') }}</p>
      <code class="mt-2 block break-all">{{ barkTemplate }}</code>
    </details>
  </form>
</template>

<script setup lang="ts">
import { onBeforeUnmount, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import type { AccountOpsConfig } from '@/api/admin/accountOps'
import { saveAccountOpsPool } from '@/api/admin/accountOpsPool'
import { extractApiErrorMessage } from '@/utils/apiError'

const props = defineProps<{ config: AccountOpsConfig; disabled?: boolean }>()
const emit = defineEmits<{ saved: [config: AccountOpsConfig]; error: [message: string]; saving: [value: boolean] }>()
const { t } = useI18n()
const draft = reactive({ enabled: false, notify_states: true, notify_counts: true })
const dirty = ref(false), busy = ref(false)
const barkTemplate = '{"title":"{{title}}","body":"{{message}}","group":"Sub2API"}'
let alive = true
function reset(config: AccountOpsConfig) {
  Object.assign(draft, config.pool_notifications ?? { enabled: false, notify_states: true, notify_counts: true })
  dirty.value = false
}
watch(() => props.config.pool_notifications, () => {
  if (!dirty.value && !busy.value) reset(props.config)
}, { immediate: true, deep: true })
onBeforeUnmount(() => { alive = false; emit('saving', false) })
async function save() {
  if (busy.value || props.disabled || !dirty.value) return
  if (draft.enabled && !draft.notify_states && !draft.notify_counts) { emit('error', t('accountOps.pool.select')); return }
  busy.value = true
  emit('saving', true)
  try {
    const config = await saveAccountOpsPool({ ...draft })
    if (alive) { reset(config); emit('saved', config) }
  } catch (e) {
    if (alive) emit('error', extractApiErrorMessage(e, t('accountOps.pool.failed')))
  } finally {
    if (alive) { busy.value = false; emit('saving', false) }
  }
}
</script>
