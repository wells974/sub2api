<template>
  <BaseDialog :show="true" :title="t('qualityOps.patrol.title')" width="normal" :show-close-button="!saving" :close-on-escape="!saving" @close="close">
    <p v-if="error" role="alert" class="mb-3 text-sm text-red-600">{{ error }}</p>
    <p v-if="loading" role="status">{{ t('common.loading') }}</p>
    <button v-else-if="!settings" class="btn btn-secondary" @click="load">{{ t('qualityOps.retry') }}</button>
    <form v-if="settings && draft" id="quality-patrol-settings" @submit.prevent="save">
      <fieldset :disabled="saving" class="space-y-5 text-sm">
        <label class="flex items-center gap-2"><input v-model="draft.enabled" type="checkbox" />{{ t('qualityOps.patrol.enabled') }}</label>
        <p class="text-gray-500">{{ t(settings.channels_configured ? 'qualityOps.patrol.channels' : 'qualityOps.patrol.noChannels') }}
          <RouterLink to="/admin/notification-channels" class="text-primary-600">{{ t('accountOps.channelsPage') }}</RouterLink>
        </p>
        <div class="space-y-2">
          <label class="flex items-center gap-2"><input v-model="draft.quiet_enabled" type="checkbox" />{{ t('qualityOps.patrol.quiet') }}</label>
          <template v-if="draft.quiet_enabled">
            <div class="grid grid-cols-2 gap-3">
              <label>{{ t('qualityOps.patrol.start') }}<input v-model="draft.quiet_start" class="input w-full" type="time" required /></label>
              <label>{{ t('qualityOps.patrol.end') }}<input v-model="draft.quiet_end" class="input w-full" type="time" required /></label>
            </div>
            <p class="text-xs text-gray-500">{{ t('qualityOps.patrol.zone', { zone: settings.timezone }) }}</p>
          </template>
        </div>
        <fieldset class="space-y-2">
          <legend class="font-medium">{{ t('qualityOps.patrol.rules') }}</legend>
          <p v-if="!settings.rules.length">{{ t('qualityOps.patrol.empty') }}</p>
          <div v-else class="max-h-64 space-y-1 overflow-y-auto">
            <label v-for="rule in settings.rules" :key="rule.id" class="flex items-center gap-2 py-1">
              <input v-model="draft.rules" type="checkbox" :value="rule.id" />
              <span>{{ rule.name }}</span><small class="text-gray-500">{{ t(rule.id.startsWith('group:') ? 'qualityOps.templateSection' : 'qualityOps.rule') }} #{{ rule.id.split(':')[1] }}</small>
            </label>
          </div>
        </fieldset>
      </fieldset>
    </form>
    <template #footer>
      <button type="button" class="btn btn-secondary" :disabled="saving" @click="close">{{ t('common.cancel') }}</button>
      <button type="submit" form="quality-patrol-settings" class="btn btn-primary" :disabled="!draft || saving">{{ t('common.save') }}</button>
    </template>
  </BaseDialog>
</template>
<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseDialog from '@/components/common/BaseDialog.vue'
import { getPatrolSettings, savePatrolSettings, type PatrolSettings, type PatrolConfig } from '@/api/admin/qualityPatrol'
import { extractApiErrorMessage } from '@/utils/apiError'

const emit = defineEmits<{ close: []; saved: [] }>()
const { t } = useI18n()
const settings = ref<PatrolSettings>(), draft = ref<PatrolConfig>()
const loading = ref(false), saving = ref(false), error = ref('')
let alive = true
const close = () => { if (!saving.value) emit('close') }

async function load() {
  loading.value = true
  error.value = ''
  try {
    const value = await getPatrolSettings()
    if (!alive) return
    settings.value = value
    const available = new Set(value.rules.map(rule => rule.id))
    draft.value = { ...value.config, rules: value.config.rules.filter(id => available.has(id)) }
  } catch (e) {
    if (alive) error.value = extractApiErrorMessage(e, t('qualityOps.patrol.loadFailed'))
  } finally {
    if (alive) loading.value = false
  }
}
async function save() {
  const config = draft.value
  if (!config || saving.value) return
  if (config.enabled && (!settings.value?.channels_configured || !config.rules.length)) {
    error.value = t('qualityOps.patrol.select')
    return
  }
  if (config.quiet_enabled && (!config.quiet_start || !config.quiet_end || config.quiet_start === config.quiet_end)) {
    error.value = t('qualityOps.patrol.hours')
    return
  }
  error.value = ''
  saving.value = true
  try {
    await savePatrolSettings(config)
    if (alive) emit('saved')
  } catch (e) {
    if (alive) error.value = extractApiErrorMessage(e, t('accountOps.saveFailed'))
  } finally {
    if (alive) saving.value = false
  }
}
onMounted(load)
onBeforeUnmount(() => { alive = false })
</script>
