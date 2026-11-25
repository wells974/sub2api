<template>
  <BaseDialog :show="true" :title="t('qualityOps.jevSettings.title')" width="normal"
    :show-close-button="!saving" :close-on-escape="!saving" @close="close">
    <p v-if="error" role="alert" class="mb-3 text-sm text-red-600">{{ error }}</p>
    <p v-if="loading" role="status">{{ t('common.loading') }}</p>
    <button v-else-if="!draft" class="btn btn-secondary" @click="load">{{ t('qualityOps.retry') }}</button>
    <form v-if="draft" id="quality-jev-settings" @submit.prevent="save">
      <fieldset :disabled="saving" class="space-y-4 text-sm">
        <label class="flex items-center gap-2"><input v-model="draft.enabled" type="checkbox" />{{ t('qualityOps.jevSettings.enabled') }}</label>
        <label class="block">{{ t('admin.riskControl.defaultKeyUrl') }}
          <input v-model.trim="draft.base_url" type="url" class="input w-full" placeholder="https://api.typesafe.ai" />
        </label>
        <fieldset class="space-y-2">
          <legend>{{ t('admin.riskControl.apiKeys') }}</legend>
          <div v-for="(row, index) in draft.keys" :key="row.id || index" class="space-y-2 rounded border p-3">
            <div class="flex items-center justify-between gap-2">
              <span v-if="row.id">{{ t('qualityOps.jevSettings.savedKey', { id: row.masked }) }}</span>
              <input v-else v-model.trim="row.key" class="input flex-1" type="password" autocomplete="new-password" required
                :aria-label="t('admin.riskControl.apiKey')" :placeholder="t('admin.riskControl.apiKeyPlaceholder')" />
              <button type="button" class="btn btn-secondary" @click="draft.keys.splice(index, 1)">{{ t('common.remove') }}</button>
            </div>
            <label class="block">{{ t('admin.riskControl.keyUrl') }}
              <input v-model.trim="row.base_url" type="url" class="input w-full" :placeholder="draft.base_url || 'https://api.typesafe.ai'" />
            </label>
          </div>
          <button type="button" class="btn btn-secondary" @click="draft.keys.push({ key: '', base_url: '' })">{{ t('qualityOps.jevSettings.addKey') }}</button>
        </fieldset>
        <div class="grid grid-cols-2 gap-3">
          <label>{{ t('admin.riskControl.timeoutMs') }}<input v-model.number="draft.timeout_ms" class="input w-full" type="number" min="1" max="30000" required /></label>
          <label>{{ t('admin.riskControl.retryCount') }}<input v-model.number="draft.retry_count" class="input w-full" type="number" min="0" max="2" required /></label>
        </div>
        <label class="block">{{ t('admin.riskControl.proxy') }}
          <select v-model="draft.proxy_id" class="input w-full">
            <option :value="null">{{ t('admin.accounts.noProxy') }}</option>
            <option v-if="draft.proxy_id && !proxies.some(p => p.id === draft?.proxy_id)" :value="draft.proxy_id">#{{ draft.proxy_id }}</option>
            <option v-for="proxy in proxies" :key="proxy.id" :value="proxy.id">{{ proxy.name }}</option>
          </select>
        </label>
      </fieldset>
    </form>
    <template #footer>
      <button type="button" class="btn btn-secondary" :disabled="saving" @click="close">{{ t('common.cancel') }}</button>
      <button type="submit" form="quality-jev-settings" class="btn btn-primary" :disabled="!draft || saving">{{ t('common.save') }}</button>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseDialog from '@/components/common/BaseDialog.vue'
import { getJevSettings, saveJevSettings, type JevSettings } from '@/api/admin/qualityJev'
import { getAll as getProxies } from '@/api/admin/proxies'
import type { Proxy } from '@/types'
import { extractApiErrorMessage } from '@/utils/apiError'

const emit = defineEmits<{ close: []; saved: [] }>()
const { t } = useI18n()
const draft = ref<JevSettings>()
const proxies = ref<Proxy[]>([])
const error = ref('')
const loading = ref(true)
const saving = ref(false)
const close = () => { if (!saving.value) emit('close') }

async function load() {
  loading.value = true
  error.value = ''
  try {
    const [settings, options] = await Promise.all([getJevSettings(), getProxies()])
    draft.value = settings
    proxies.value = options
  } catch (e) {
    error.value = extractApiErrorMessage(e, t('qualityOps.jevReasons.config_unavailable'))
  } finally {
    loading.value = false
  }
}

async function save() {
  if (!draft.value || saving.value) return
  if (draft.value.enabled && !draft.value.keys.length) {
    error.value = t('qualityOps.jevReasons.not_configured')
    return
  }
  saving.value = true
  error.value = ''
  try {
    await saveJevSettings(draft.value)
    emit('saved')
  } catch (e) {
    error.value = extractApiErrorMessage(e, t('accountOps.saveFailed'))
  } finally {
    saving.value = false
  }
}

onMounted(load)
</script>
