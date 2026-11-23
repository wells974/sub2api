<template>
  <section id="group-fallback" ref="panel" aria-labelledby="group-fallback-title"
    class="min-w-0 scroll-mt-20 overflow-hidden rounded-2xl border border-gray-200 bg-white shadow-sm dark:border-dark-700 dark:bg-dark-900">
    <header class="grid grid-cols-[auto_minmax(0,1fr)_auto] items-center gap-x-3 gap-y-1 border-b border-gray-100 px-5 py-4 dark:border-dark-700">
      <span class="flex h-10 w-10 items-center justify-center rounded-xl bg-primary-50 text-primary-700 sm:row-span-2 dark:bg-primary-950 dark:text-primary-300">
        <Icon name="shield" size="md" aria-hidden="true" />
      </span>
      <h2 id="group-fallback-title" class="text-lg font-semibold">{{ t('autoConfig.groupFallback.title') }}</h2>
      <label v-if="config && !loading" class="col-start-3 row-start-1 flex items-center gap-2 text-sm sm:row-span-2">
        <input v-model="config.enabled" type="checkbox" role="switch" :disabled="busy" class="h-4 w-4 rounded text-primary-600" />
        {{ t('autoConfig.enable') }}
      </label>
      <p class="col-span-full text-xs leading-5 text-gray-500 sm:col-span-1 sm:col-start-2 dark:text-gray-400">{{ t('autoConfig.groupFallback.hint') }}</p>
    </header>
    <p v-if="error" role="alert" class="px-5 pt-4 text-sm text-red-600">{{ error }}
      <button type="button" class="ml-2 underline" :disabled="busy" @click="load">{{ t('autoConfig.groupFallback.reload') }}</button>
    </p>
    <p v-if="loading" role="status" class="p-5 text-sm text-gray-500">{{ t('common.loading') }}</p>
    <form v-else-if="config" @submit.prevent="save">
      <fieldset :disabled="busy" class="min-w-0">
        <div class="flex flex-wrap items-end justify-between gap-3 px-5 py-4">
          <div class="flex w-full min-w-0 items-end gap-2 sm:w-auto">
            <label class="min-w-0 flex-1 text-sm font-medium">{{ t('autoConfig.groupFallback.group') }}
              <select v-model="selectedGroup" :disabled="!unusedGroups.length" class="input mt-1.5 block w-full sm:w-56">
                <option :value="0" disabled>{{ t(unusedGroups.length ? 'autoConfig.groupFallback.choose' : 'autoConfig.groupFallback.noGroups') }}</option>
                <option v-for="g in unusedGroups" :key="g.id" :value="g.id">{{ g.name }} #{{ g.id }}</option>
              </select>
            </label>
            <button type="button" class="btn btn-secondary shrink-0" :disabled="!selectedGroup" @click="addRule">{{ t('autoConfig.groupFallback.add') }}</button>
          </div>
          <button type="button" class="text-sm font-medium text-primary-600 hover:underline dark:text-primary-400" @click="refreshStatus">{{ t('autoConfig.apikeyFallback.refreshStatus') }}</button>
        </div>
        <div v-if="config.rules.length" ref="ruleList" role="region" :aria-label="t('autoConfig.groupFallback.rules')" tabindex="0"
          class="relative mx-5 mb-4 max-h-[45vh] space-y-3 sm:max-h-[min(55vh,32rem)] overflow-y-auto rounded-xl focus-visible:outline-primary-500">
          <section v-for="rule in config.rules" :key="rule.group_id" class="overflow-hidden rounded-xl border border-gray-200 dark:border-dark-700">
            <header class="flex flex-wrap items-center gap-3 border-b border-gray-100 bg-gray-50 px-4 py-2.5 dark:border-dark-700 dark:bg-dark-800">
              <h3 class="min-w-0 flex-1 break-words text-sm font-semibold">{{ groupMap.get(rule.group_id)?.name ?? t('autoConfig.groupFallback.unavailable') }} <span class="ml-1 text-xs font-normal text-gray-500">#{{ rule.group_id }}</span></h3>
              <label class="flex items-center gap-2 text-sm"><input v-model="rule.policy.enabled" type="checkbox" role="switch" />{{ t('autoConfig.enable') }}</label>
              <button type="button" class="text-sm text-red-600" @click="removeRule(rule.group_id)">{{ t('common.delete') }}</button>
            </header>
            <div class="grid gap-3 px-4 py-4 text-sm sm:grid-cols-3">
              <label>{{ t('autoConfig.apikeyFallback.metric') }}
                <select v-model="rule.policy.metric" class="input mt-1 w-full">
                  <option value="sla">{{ t('autoConfig.apikeyFallback.metrics.sla') }}</option>
                  <option value="health_score">{{ t('autoConfig.apikeyFallback.metrics.health_score') }}</option>
                </select>
              </label>
              <label>{{ t('autoConfig.apikeyFallback.threshold') }} ({{ rule.policy.metric === 'sla' ? '%' : t('autoConfig.apikeyFallback.scoreUnit') }})
                <input v-model.number="rule.policy.threshold" type="number" min="0" max="100" step="any" required class="input mt-1 w-full" />
              </label>
              <label>{{ t('autoConfig.apikeyFallback.window') }}
                <input v-model.number="rule.policy.window_minutes" type="number" min="1" max="1440" required class="input mt-1 w-full" />
              </label>
            </div>
            <div class="grid gap-4 px-4 pb-4 md:grid-cols-2">
              <fieldset class="min-w-0 text-sm">
                <legend class="font-medium">{{ t('autoConfig.apikeyFallback.accounts') }}</legend>
                <div class="mt-2 max-h-48 space-y-2 overflow-y-auto">
                  <p v-if="!groupMap.get(rule.group_id)?.accounts.length" class="text-gray-500">{{ t('autoConfig.apikeyFallback.noAccounts') }}</p>
                  <label v-for="a in groupMap.get(rule.group_id)?.accounts" :key="a.id" class="flex items-center gap-2">
                    <input v-model="rule.policy.account_ids" type="checkbox" :value="a.id" :disabled="rule.policy.account_ids.length >= 100 && !rule.policy.account_ids.includes(a.id)" />
                    {{ a.name }} #{{ a.id }} · {{ t(a.schedulable ? 'autoConfig.apikeyFallback.schedulingOn' : 'autoConfig.apikeyFallback.schedulingOff') }}
                  </label>
                  <label v-for="id in staleIDs(rule)" :key="id" class="flex items-center gap-2 text-amber-700">
                    <input v-model="rule.policy.account_ids" type="checkbox" :value="id" />#{{ id }} · {{ t('autoConfig.apikeyFallback.unavailableAccount') }}
                  </label>
                </div>
              </fieldset>
              <div class="min-w-0 border-t border-gray-100 pt-3 text-sm text-gray-500 md:border-l md:border-t-0 md:pl-4 md:pt-0 dark:border-dark-700">
                <h4 class="mb-1 font-medium">{{ t('autoConfig.apikeyFallback.status') }}</h4>
                <template v-if="status[rule.group_id]?.last_checked_at">
                  <p>{{ t('autoConfig.apikeyFallback.lastChecked') }} {{ formatTime(status[rule.group_id].last_checked_at) }}</p>
                  <p>{{ t('autoConfig.apikeyFallback.oauthAvailable') }} {{ status[rule.group_id].oauth_available_count }} / {{ status[rule.group_id].oauth_account_count }}</p>
                  <p>{{ t('autoConfig.apikeyFallback.metrics.' + status[rule.group_id].metric) }}
                    {{ formatMetric(status[rule.group_id]) }}
                    · {{ status[rule.group_id].window_minutes }} {{ t('autoConfig.apikeyFallback.minutes') }}
                    · {{ t('autoConfig.apikeyFallback.samples') }} {{ status[rule.group_id].samples }}</p>
                  <p v-if="status[rule.group_id].last_trigger_reason">{{ t('autoConfig.apikeyFallback.lastTrigger') }} {{ t('autoConfig.apikeyFallback.reasons.' + status[rule.group_id].last_trigger_reason) }}</p>
                  <p v-if="status[rule.group_id].last_enabled_at">{{ t('autoConfig.apikeyFallback.lastEnabled') }} {{ formatTime(status[rule.group_id].last_enabled_at) }}</p>
                  <p v-if="status[rule.group_id].last_error_category" class="text-amber-700">{{ t('autoConfig.apikeyFallback.errors.' + status[rule.group_id].last_error_category) }}</p>
                </template>
                <p v-else>{{ t('autoConfig.apikeyFallback.notChecked') }}</p>
              </div>
            </div>
          </section>
        </div>
        <p v-else class="mx-5 mb-4 rounded-xl border border-dashed border-gray-200 py-4 text-center text-sm text-gray-500 dark:border-dark-700">{{ t('autoConfig.groupFallback.empty') }}</p>
        <footer class="flex flex-wrap items-center justify-between gap-3 border-t border-gray-100 px-5 py-3 dark:border-dark-700">
          <p class="text-xs leading-5 text-gray-500 dark:text-gray-400">{{ t('autoConfig.groupFallback.shared') }}</p>
          <div class="ml-auto flex items-center gap-3">
            <p v-if="saved" role="status" class="text-sm text-emerald-600">{{ t('autoConfig.saved') }}</p>
            <button type="submit" class="btn btn-primary">{{ t('autoConfig.groupFallback.save') }}</button>
          </div>
        </footer>
      </fieldset>
    </form>
  </section>
</template>
<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import { useAuthStore } from '@/stores/auth'
import { extractApiErrorMessage } from '@/utils/apiError'
import { defaultOpenAIAPIKeyFallbackConfig, openaiAPIKeyFallbackError } from '@/utils/openaiAPIKeyFallback'
import { getGroupFallback, getGroupFallbackOptions, saveGroupFallback, type GroupFallbackConfig, type GroupFallbackOption, type GroupFallbackRule, type GroupFallbackState } from '@/api/admin/groupFallback'

const { t } = useI18n()
const auth = useAuthStore()
const panel = ref<HTMLElement>()
onMounted(() => {
  if (window.location.hash === '#group-fallback') panel.value?.scrollIntoView({ block: 'start' })
})
const config = ref<GroupFallbackConfig>()
const groups = ref<GroupFallbackOption[]>([])
const status = ref<GroupFallbackState['status']>({})
const selectedGroup = ref(0)
const ruleList = ref<HTMLElement>()
const loading = ref(false), busy = ref(false), saved = ref(false), error = ref('')
let generation = 0
const groupMap = computed(() => new Map(groups.value.map(g => [g.id, g])))
const unusedGroups = computed(() => groups.value.filter(g => !config.value?.rules.some(r => r.group_id === g.id)))
const formatTime = (time?: string) => time ? new Date(time).toLocaleString() : '—'
function formatMetric(state: GroupFallbackState['status'][number]) {
  if (state.metric_value == null) return '—'
  return state.metric_value.toFixed(2) + (state.metric === 'sla' ? '%' : t('autoConfig.apikeyFallback.scoreUnit'))
}
const staleIDs = (r: GroupFallbackRule) => r.policy.account_ids.filter(id => !groupMap.value.get(r.group_id)?.accounts.some(a => a.id === id))
watch(config, () => { saved.value = false }, { deep: true, flush: 'sync' })
async function addRule() {
  if (!config.value || !unusedGroups.value.some(g => g.id === selectedGroup.value)) return
  config.value.rules.push({ group_id: selectedGroup.value, policy: { ...defaultOpenAIAPIKeyFallbackConfig(), enabled: true } })
  selectedGroup.value = 0
  await nextTick()
  const list = ruleList.value
  const added = list?.lastElementChild
  if (list && added instanceof HTMLElement) list.scrollTop = added.offsetTop
}
function removeRule(id: number) {
  if (config.value) config.value.rules = config.value.rules.filter(rule => rule.group_id !== id)
}
async function load() {
  const current = ++generation
  loading.value = true
  busy.value = false
  error.value = ''
  try {
    const [state, options] = await Promise.all([getGroupFallback(), getGroupFallbackOptions()])
    if (current !== generation) return
    config.value = state.config
    groups.value = options
    status.value = state.status
    selectedGroup.value = 0
  } catch (e) {
    if (current === generation) error.value = extractApiErrorMessage(e, t('autoConfig.apikeyFallback.statusFailed'))
  } finally { if (current === generation) loading.value = false }
}
async function refreshStatus() {
  if (busy.value) return
  const current = generation
  busy.value = true
  error.value = ''
  try {
    const [state, options] = await Promise.all([getGroupFallback(), getGroupFallbackOptions()])
    if (current !== generation) return
    groups.value = options
    if (state.config.revision === config.value?.revision) status.value = state.status
    else error.value = t('autoConfig.groupFallback.conflict')
  } catch (e) {
    if (current === generation) error.value = extractApiErrorMessage(e, t('autoConfig.apikeyFallback.statusFailed'))
  } finally { if (current === generation) busy.value = false }
}
async function save() {
  if (!config.value || busy.value) return
  for (const rule of config.value.rules) {
    const invalid = openaiAPIKeyFallbackError(rule.policy)
    if (invalid) { error.value = t(invalid); return }
    if (config.value.enabled && rule.policy.enabled && staleIDs(rule).length) {
      error.value = t('autoConfig.apikeyFallback.unavailableHint'); return
    }
  }
  const current = generation
  busy.value = true
  error.value = ''
  try {
    const result = await saveGroupFallback(config.value)
    if (current !== generation) return
    config.value = result
    status.value = Object.fromEntries(result.rules.filter(rule => status.value[rule.group_id]).map(rule => [rule.group_id, status.value[rule.group_id]]))
    saved.value = true
  } catch (e) {
    if (current === generation) error.value = extractApiErrorMessage(e, t('accountOps.saveFailed'))
  } finally { if (current === generation) busy.value = false }
}
watch([() => auth.user?.id, () => auth.user?.role], () => {
  generation++
  config.value = undefined
  groups.value = []
  status.value = {}
  selectedGroup.value = 0
  saved.value = false
  error.value = ''
  if (auth.isAdmin) void load()
}, { immediate: true, flush: 'sync' })
onBeforeUnmount(() => { generation++ })
</script>
