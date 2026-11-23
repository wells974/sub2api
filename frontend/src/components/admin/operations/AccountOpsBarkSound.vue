<template>
  <label class="block space-y-2 text-sm">
    <span>{{ t('accountOps.pool.barkSound') }}</span>
    <input
      v-model.trim="sound"
      type="text"
      autocomplete="off"
      spellcheck="false"
      placeholder="alarm"
      class="input w-full"
      :disabled="!templateObject"
      data-testid="account-ops-bark-sound"
    />
    <span class="block text-xs text-gray-500">{{ t(templateObject ? 'accountOps.pool.barkSoundHint' : 'accountOps.pool.barkSoundInvalid') }}</span>
  </label>
</template>

<script lang="ts">
export const barkTemplate = '{"title":"{{title}}","body":"{{message}}","group":"Sub2API"}'
</script>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { applyEdits, findNodeAtLocation, modify, parseTree, type ParseError } from 'jsonc-parser'

const template = defineModel<string>({ required: true })
const { t } = useI18n()
const source = computed(() => template.value.trim() ? template.value : barkTemplate)
const templateObject = computed(() => {
  const errors: ParseError[] = []
  const root = parseTree(source.value, errors, { disallowComments: true })
  return !errors.length && root?.type === 'object' ? root : undefined
})
const sound = computed({
  get(): string {
    if (!templateObject.value) return ''
    const node = findNodeAtLocation(templateObject.value, ['sound'])
    return node?.type === 'string' ? node.value : ''
  },
  set(value: string) {
    if (!templateObject.value || value === sound.value) return
    const edits = modify(source.value, ['sound'], value || undefined, {})
    template.value = applyEdits(source.value, edits)
  },
})
</script>
