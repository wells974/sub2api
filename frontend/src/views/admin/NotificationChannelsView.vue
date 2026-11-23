<template>
  <AppLayout>
    <SmartOpsNav />
    <h2 class="mb-4 text-xl font-semibold">{{ t('accountOps.channelsPage') }}</h2>
    <p v-if="error" role="alert">{{ error }} <button class="btn btn-secondary" @click="load">{{ t('qualityOps.retry') }}</button></p>
    <p v-else-if="!remote">{{ t('common.loading') }}</p>
    <section v-if="remote" class="card" data-testid="account-ops-channels">
      <h3 class="px-5 pt-5 text-sm font-semibold">{{ t('accountOps.notificationChannels') }}</h3>
          <div class="grid gap-3 p-5 sm:grid-cols-2 xl:grid-cols-3">
            <div v-if="remote.config.recipient" class="rounded-lg border border-gray-200 p-4 dark:border-dark-700"
              data-testid="account-ops-email-channel">
              <div class="flex items-center justify-between"><span
                  class="min-w-0 truncate text-sm font-medium" :title="remote.config.email_name || t('accountOps.providers.email')">{{ remote.config.email_name || t('accountOps.providers.email') }}</span><span
                  class="text-xs text-gray-500">{{ t(remote.config.recipient ? 'accountOps.channelConfigured' : 'accountOps.channelUnconfigured') }}</span>
              </div>
              <p class="mt-2 truncate text-xs text-gray-500">{{ remote.config.email_name ? `${t('accountOps.providers.email')} · ` : '' }}{{ remote.config.recipient }}</p>
              <div class="mt-3 flex gap-4 text-xs"><button type="button" class="text-primary-600" data-testid="edit-email-channel"
                  @click="showEmail = true">{{ t('common.edit') }}</button><button type="button" :disabled="mutating"
                  class="text-gray-500 hover:text-red-600 disabled:opacity-40" data-testid="delete-email-channel"
                  @click="removeEmail">{{ t('common.delete') }}</button></div>
            </div>
            <div v-for="(hook, index) in remote.config.webhooks" :key="hook.id"
              class="rounded-lg border border-gray-200 p-4 dark:border-dark-700">
              <div class="flex items-center justify-between gap-2"><span
                  class="min-w-0 truncate text-sm font-medium" :title="hook.name || `${t(`accountOps.providers.${hook.provider}`)} ${index + 1}`">{{ hook.name || `${t(`accountOps.providers.${hook.provider}`)} ${index + 1}` }}</span><span class="text-xs"
                  :class="hook.enabled ? 'text-emerald-600' : 'text-gray-500'">{{ t(hook.enabled ? 'accountOps.channelEnabled' : 'accountOps.channelDisabled') }}</span>
              </div>
              <p class="mt-2 text-xs text-gray-500">
                {{ hook.name ? `${t(`accountOps.providers.${hook.provider}`)} · ` : '' }}{{ t(hook.url_configured ? 'accountOps.credentialSaved' : 'accountOps.channelUnconfigured') }}</p>
              <div class="mt-3 flex gap-4 text-xs"><button type="button" class="text-primary-600" :data-testid="`edit-webhook-${hook.id}`"
                  @click="editWebhook(hook)">{{ t('common.edit') }}</button><button type="button"
                  class="text-primary-600 disabled:opacity-40" :disabled="!hook.url_configured || !!testingId"
                  :data-testid="`account-ops-webhook-test-${hook.id}`"
                  @click="testWebhook(hook.id)">{{ t(testingId === hook.id ? 'common.loading' : 'accountOps.testRobot') }}</button><button
                  type="button" class="text-gray-500 hover:text-red-600" @click="removeWebhook(hook.id)">{{ t('common.delete') }}</button>
              </div>
            </div><button type="button"
              class="min-h-28 rounded-lg border border-dashed border-gray-300 p-4 text-sm text-primary-600 disabled:opacity-40 dark:border-dark-600"
              :disabled="!canAddChannel || mutating" data-testid="account-ops-add-channel"
              @click="showAddChannel = true">{{ t('accountOps.addChannel') }}</button>
          </div>
    <details class="mx-5 mb-5 text-xs leading-5 text-gray-500">
      <summary class="cursor-pointer">Bark</summary>
      <p class="mt-2">{{ t('accountOps.pool.bark') }}</p>
      <code class="mt-2 block break-all">{{ barkTemplate }}</code>
    </details>
        <AccountOpsAddChannelDialog :show="showAddChannel" :email-configured="!!remote.config.recipient"
          :webhook-count="remote.config.webhooks?.length ?? 0" :encryption-configured="remote.encryption_key_configured === true"
          @close="showAddChannel = false" @select="addChannel" />
        <AccountOpsWebhookDialog :show="showWebhook" :hook="editingHook" :encryption-configured="remote.encryption_key_configured === true"
          :testing-id="testingId" @close="showWebhook = false" @saved="acceptConfig" @error="app.showError" @test="testWebhook" />
        <AccountOpsEmailDialog :user-id="auth.user?.id" :show="showEmail" :config="remote.config" :smtp-configured="remote.smtp_configured"
          @close="showEmail = false" @saved="acceptConfig" @error="app.showError" />
    </section>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import AppLayout from '@/components/layout/AppLayout.vue'
import SmartOpsNav from '@/components/admin/operations/SmartOpsNav.vue'
import AccountOpsWebhookDialog from '@/components/admin/operations/AccountOpsWebhookDialog.vue'
import { barkTemplate } from '@/components/admin/operations/AccountOpsBarkSound.vue'
import AccountOpsEmailDialog from '@/components/admin/operations/AccountOpsEmailDialog.vue'
import AccountOpsAddChannelDialog from '@/components/admin/operations/AccountOpsAddChannelDialog.vue'
import { useAuthStore } from '@/stores/auth'
import { useAppStore } from '@/stores/app'
import { extractApiErrorMessage } from '@/utils/apiError'
import { getAccountOpsSettings, saveAccountOpsNotificationSettings, deleteAccountOpsWebhook, testAccountOpsWebhook } from '@/api/admin/accountOps'
import type { AccountOpsConfig, AccountOpsSettings, AccountOpsWebhook } from '@/api/admin/accountOps'

const { t } = useI18n()
const auth = useAuthStore(), app = useAppStore()
const remote = ref<AccountOpsSettings | null>(null)
const editingHook = ref<AccountOpsWebhook | null>(null)
const showEmail = ref(false), showWebhook = ref(false), showAddChannel = ref(false)
const mutating = ref(false), error = ref(''), testingId = ref<string | null>(null)
let generation = 0
const canAddChannel = computed(() => remote.value && (!remote.value.config.recipient ||
  (remote.value.encryption_key_configured && (remote.value.config.webhooks?.length ?? 0) < 5)))
const message = (e: unknown) => extractApiErrorMessage(e, t('accountOps.saveFailed'))

async function load() {
  const current = ++generation
  error.value = ''
  if (!auth.user) return
  try {
    const settings = await getAccountOpsSettings()
    if (current === generation) remote.value = settings
  } catch (e) {
    if (current === generation) error.value = message(e)
  }
}
watch(() => [auth.user?.id, auth.user?.role], () => {
  remote.value = null
  showWebhook.value = showEmail.value = showAddChannel.value = mutating.value = false
  testingId.value = null
  void load()
}, { immediate: true, flush: 'sync' })
onBeforeUnmount(() => { generation++ })

function editWebhook(hook: AccountOpsWebhook | null) {
  editingHook.value = hook
  showWebhook.value = true
}
function addChannel(provider: 'email' | 'webhook') {
  if (!remote.value || !canAddChannel.value) return
  if (provider === 'email' && !remote.value.config.recipient) showEmail.value = true
  if (provider === 'webhook' && remote.value.encryption_key_configured && (remote.value.config.webhooks?.length ?? 0) < 5) editWebhook(null)
  showAddChannel.value = false
}
function acceptConfig(config: AccountOpsConfig) {
  if (!remote.value) return
  remote.value = { ...remote.value, config }
  app.showSuccess(t('accountOps.settingsSaved'))
}
async function removeChannel(action: () => Promise<AccountOpsConfig>) {
  if (mutating.value) return
  const current = generation
  mutating.value = true
  try {
    const config = await action()
    if (current === generation) acceptConfig(config)
  } catch (e) {
    if (current === generation) app.showError(message(e))
  } finally {
    if (current === generation) mutating.value = false
  }
}
const removeWebhook = (id: string) => removeChannel(() => deleteAccountOpsWebhook(id))
const removeEmail = () => removeChannel(() => saveAccountOpsNotificationSettings({ recipient: '', email_name: '' }))
async function testWebhook(id: string) {
  if (testingId.value) return
  const current = generation
  testingId.value = id
  try {
    await testAccountOpsWebhook(id)
    if (current === generation) app.showSuccess(t('accountOps.testSuccess'))
  } catch (e) {
    if (current === generation) app.showError(message(e))
  } finally {
    if (current === generation) testingId.value = null
  }
}
</script>
