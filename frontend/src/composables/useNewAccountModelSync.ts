import { reactive, ref } from 'vue'
import { adminAPI } from '@/api/admin'
import type { SyncUpstreamModelsOptions } from '@/api/admin/accounts'

export interface NewAccountModelSync {
  id: number
  name: string
  state: 'syncing' | 'saved' | 'policy' | 'throttled' | 'failed'
  count: number
}

export function useNewAccountModelSync(onRetrySaved: () => void) {
  const results = ref<NewAccountModelSync[]>([])

  async function run(row: NewAccountModelSync, options: SyncUpstreamModelsOptions, signal?: AbortSignal) {
    row.state = 'syncing'
    try {
      const result = await adminAPI.accounts.syncUpstreamModels(row.id, options, { timeout: 70000, signal })
      if (result.applied && !result.skipped) {
        row.count = result.models.length
        row.state = 'saved'
      } else if (result.skipped === 'throttled') {
        row.state = 'throttled'
      } else {
        row.state = result.skipped === 'account_policy' ? 'policy' : 'failed'
      }
    } catch {
      row.state = 'failed'
    }
    return row.state === 'saved'
  }

  async function syncCreated(id: number, name: string, signal?: AbortSignal) {
    if (results.value.some(row => row.id === id)) return
    const row = reactive<NewAccountModelSync>({ id, name, state: 'syncing', count: 0 })
    results.value.push(row)
    await run(row, { apply: true, after_create: true }, signal)
  }

  async function retry(row: NewAccountModelSync) {
    if (row.state === 'failed' && await run(row, { apply: true })) onRetrySaved()
  }

  return { results, syncCreated, retry }
}
